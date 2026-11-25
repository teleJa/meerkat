package harnessweb

import (
	"errors"
	"regexp"
	"strings"

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
	outer       *ast.ArrayLiteral
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
	return &trustExpression{outer: outer, owned: owned, baseSection: source[int(startup.RightBracket) : int(owned.LeftBracket)-1], additions: len(args.Value) > 3 || len(outer.Value) > 1}
}

// trustBlock embeds the effective expression verbatim as the second spread.
// A previously released row can therefore be identified even when its manual
// address is also leased by the new block. Only unwrap our exact delimiter.
func inheritedTrustExpressions(saved string) map[string]bool {
	inherited := make(map[string]bool)
	for expr := parseTrustExpression(saved); expr != nil; expr = parseTrustExpression(saved) {
		base, ok := strings.CutPrefix(expr.baseSection, "), ...(")
		if !ok {
			break
		}
		base, ok = strings.CutSuffix(base, "), ...")
		if !ok {
			break
		}
		inherited[base] = true
		saved = base
	}
	return inherited
}

// sameStaticTrust compares the small expression language emitted by trustBlock.
// Opaque operands are never evaluated. In particular, order, holes, spreads
// and nesting are significant even when the resulting host sets might agree.
func sameStaticTrust(a, b ast.Expression) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case *ast.StringLiteral:
		y, ok := b.(*ast.StringLiteral)
		return ok && x.Value == y.Value
	case *ast.ArrayLiteral:
		y, ok := b.(*ast.ArrayLiteral)
		if !ok || len(x.Value) != len(y.Value) {
			return false
		}
		for i, item := range x.Value {
			if !sameStaticTrust(item, y.Value[i]) {
				return false
			}
		}
		return true
	case *ast.SpreadElement:
		y, ok := b.(*ast.SpreadElement)
		return ok && sameStaticTrust(x.Expression, y.Expression)
	case *ast.NewExpression:
		y, ok := b.(*ast.NewExpression)
		return ok && named(x.Callee, "Set") && named(y.Callee, "Set") && len(x.ArgumentList) == 1 && len(y.ArgumentList) == 1 && sameStaticTrust(x.ArgumentList[0], y.ArgumentList[0])
	default:
		return startupFallback(a) != nil && startupFallback(b) != nil
	}
}

func recordedTrustExpression(history map[string]bool, source string) bool {
	if history[source] {
		return true
	}
	current := parseTrustExpression(source)
	if current == nil {
		return false
	}
	for prior := range history {
		saved := parseTrustExpression(prior)
		if saved == nil {
			continue
		}
		if sameStaticTrust(saved.outer, current.outer) {
			return true
		}
		// The ledger embeds the historical wrapper as its base operand. Match
		// that wrapper around the array and its original entries in order.
		// A manual literal can independently grant a currently leased address.
		// A copied current block has an extra wrapper around this historical base.
		if prior[:int(saved.owned.LeftBracket)] != source[:int(current.owned.LeftBracket)] ||
			prior[int(saved.owned.RightBracket)-1:] != source[int(current.owned.RightBracket)-1:] ||
			len(current.owned.Value) < len(saved.owned.Value) {
			continue
		}
		matched := 0
		for _, item := range current.owned.Value {
			if matched < len(saved.owned.Value) && sameStaticTrust(saved.owned.Value[matched], item) {
				matched++
				continue
			}
			if _, ok := item.(*ast.StringLiteral); !ok {
				matched = -1
				break
			}
		}
		if matched == len(saved.owned.Value) {
			return true
		}
	}
	return false
}

// goja excludes grouping parentheses and comments from literal spans. Walk
// only the gap between AST elements to locate real array separators.
const jsTrivia = `(?:[\s\p{Zs}\x{FEFF}\x{2028}\x{2029}]|/\*(?s:.*?)\*/|//[^\r\n\x{2028}\x{2029}]*)*`

var afterElement = regexp.MustCompile(`^` + jsTrivia + `(?:\)` + jsTrivia + `)*`)
var beforeLiteral = regexp.MustCompile(`^` + jsTrivia + `(?:\(` + jsTrivia + `)*$`)

func literalStart(source string, array *ast.ArrayLiteral, i, literal int) (int, bool) {
	previous := i - 1
	for previous >= 0 && array.Value[previous] == nil {
		previous--
	}
	pos := int(array.LeftBracket)
	if previous >= 0 {
		pos = int(array.Value[previous].Idx1()) - 1
		if pos < int(array.LeftBracket) || pos > literal {
			return 0, false
		}
	}
	for j := max(previous, 0); j < i; j++ {
		pos += len(afterElement.FindString(source[pos:literal]))
		if pos >= literal || source[pos] != ',' {
			return 0, false
		}
		pos++
	}
	return pos, beforeLiteral.MatchString(source[pos:literal])
}

var errTrustOwnership = errors.New("无法确认受管数组中的地址归属")

func releaseAppendedHosts(saved, current *yaml.Node, owners []string) (string, bool, error) {
	if current == nil || current.Kind != yaml.ScalarNode || current.Tag != saved.Tag || len(owners) == 0 {
		return "", false, nil
	}
	now := parseTrustExpression(current.Value)
	if now == nil {
		return "", false, nil
	}
	remaining := make(map[string]int, len(owners))
	for _, owner := range owners {
		remaining[owner]++
	}
	var out strings.Builder
	source := current.Value
	cursor, removed := 0, 0
	dynamic := false
	for i, element := range now.owned.Value {
		literal, ok := element.(*ast.StringLiteral)
		if !ok {
			dynamic = dynamic || element != nil
			continue
		}
		host := literal.Value.String()
		if remaining[host] == 0 {
			continue
		}
		start, ok := literalStart(source, now.owned, i, int(literal.Idx0())-1)
		if !ok || start < cursor {
			return "", false, errTrustOwnership
		}
		end := int(literal.Idx1()) - 1
		end += len(afterElement.FindString(source[end : int(now.owned.RightBracket)-1]))
		if source[end] == ',' {
			end++
		} else if end != int(now.owned.RightBracket)-1 {
			return "", false, errTrustOwnership
		}
		out.WriteString(source[cursor:start])
		cursor = end
		removed++
		remaining[host]--
	}
	// A missing literal is already released. An opaque replacement could still
	// grant that authority, so its provenance cannot be established statically.
	if dynamic && removed < len(owners) {
		return "", false, errTrustOwnership
	}
	prior := parseTrustExpression(saved.Value)
	sameBase := prior != nil && prior.baseSection == now.baseSection
	if removed == 0 && !sameBase {
		// This static array contains no owned literals and has a different base.
		return "", false, nil
	}
	if removed == len(now.owned.Value) && !now.additions && sameBase {
		return saved.Value, true, nil
	}
	out.WriteString(source[cursor:])
	return out.String(), true, nil
}
