package harnessweb

import (
	"regexp"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
	"github.com/dop251/goja/token"
	"gopkg.in/yaml.v3"
)

func spreadOperand(e ast.Expression) ast.Expression {
	if spread, ok := e.(*ast.SpreadElement); ok {
		return spread.Expression
	}
	return nil
}

func named(e ast.Expression, name string) bool {
	id, ok := e.(*ast.Identifier)
	return ok && id.Name.String() == name
}

// Recognize the startup fallback independently of whitespace, quotes and parentheses.
func startupFallback(e ast.Expression) *ast.ArrayLiteral {
	coalesce, ok := e.(*ast.BinaryExpression)
	if !ok || coalesce.Operator != token.COALESCE {
		return nil
	}
	empty, ok := coalesce.Right.(*ast.ArrayLiteral)
	if !ok || len(empty.Value) != 0 {
		return nil
	}
	chain, ok := coalesce.Left.(*ast.OptionalChain)
	if !ok {
		return nil
	}
	member, ok := chain.Expression.(*ast.DotExpression)
	if !ok || member.Identifier.Name.String() != "trustedHosts" {
		return nil
	}
	optional, ok := member.Left.(*ast.Optional)
	if !ok {
		return nil
	}
	call, ok := optional.Expression.(*ast.CallExpression)
	if !ok || len(call.ArgumentList) != 1 {
		return nil
	}
	get, ok := call.Callee.(*ast.DotExpression)
	if !ok || get.Identifier.Name.String() != "get" || !named(get.Left, "ctx") {
		return nil
	}
	arg, ok := call.ArgumentList[0].(*ast.StringLiteral)
	if !ok || arg.Value.String() != "webStartup" {
		return nil
	}
	return empty
}

type trustExpression struct {
	owned       *ast.ArrayLiteral
	baseSection string
	additions   bool
}

func parseTrustExpression(source string) *trustExpression {
	program, err := parser.ParseFile(nil, "", source, 0, parser.WithDisableSourceMaps)
	if err != nil || len(program.Body) != 1 {
		return nil
	}
	stmt, ok := program.Body[0].(*ast.ExpressionStatement)
	if !ok {
		return nil
	}
	outer, ok := stmt.Expression.(*ast.ArrayLiteral)
	if !ok || len(outer.Value) == 0 {
		return nil
	}
	set, ok := spreadOperand(outer.Value[0]).(*ast.NewExpression)
	if !ok || !named(set.Callee, "Set") || len(set.ArgumentList) != 1 {
		return nil
	}
	args, ok := set.ArgumentList[0].(*ast.ArrayLiteral)
	if !ok || len(args.Value) < 3 {
		return nil
	}
	startup := startupFallback(spreadOperand(args.Value[0]))
	owned, ok := spreadOperand(args.Value[2]).(*ast.ArrayLiteral)
	if startup == nil || spreadOperand(args.Value[1]) == nil || !ok {
		return nil
	}
	// Include grouping and comments around the user's operand. This comparison
	// only decides whether a fully released row can inherit its underlying field.
	return &trustExpression{owned: owned, baseSection: source[int(startup.RightBracket) : int(owned.LeftBracket)-1], additions: len(args.Value) > 3 || len(outer.Value) > 1}
}

// The parser omits grouping parentheses and trivia from string literal spans.
// After the last owned literal, consume that trivia up to its array separator.
var elementSeparator = regexp.MustCompile(`^(?:[\s\p{Zs}\x{FEFF}\x{2028}\x{2029})]|/\*(?s:.*?)\*/|//[^\r\n\x{2028}\x{2029}]*)*,`)

func releaseAppendedHosts(saved, current *yaml.Node, owners []string) (string, bool) {
	if current == nil || current.Kind != yaml.ScalarNode || current.Tag != saved.Tag || len(owners) == 0 {
		return "", false
	}
	now := parseTrustExpression(current.Value)
	if now == nil || len(now.owned.Value) < len(owners) {
		return "", false
	}
	var last *ast.StringLiteral
	for i, owner := range owners {
		lit, ok := now.owned.Value[i].(*ast.StringLiteral)
		if !ok || lit.Value.String() != owner {
			return "", false
		}
		last = lit
	}
	start, end := int(now.owned.LeftBracket), int(now.owned.RightBracket)-1
	if len(now.owned.Value) > len(owners) {
		after := int(last.Idx1()) - 1
		sep := elementSeparator.FindStringIndex(current.Value[after:end])
		if sep == nil {
			return "", false
		}
		end = after + sep[1]
	} else if !now.additions {
		if prior := parseTrustExpression(saved.Value); prior != nil && prior.baseSection == now.baseSection {
			return saved.Value, true
		}
	}
	return current.Value[:start] + current.Value[end:], true
}
