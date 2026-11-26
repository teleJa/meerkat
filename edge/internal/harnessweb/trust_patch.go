package harnessweb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml/lexer"
	"github.com/goccy/go-yaml/token"
	"gopkg.in/yaml.v3"
)

const trustRowID = "x-meerkat-trust-id"

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
var numericLabel = regexp.MustCompile(`^(?:[0-9]+|0x[0-9a-f]*)$`)

func trustedAuthority(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	invalid := errors.New("DSH 公网地址须为精确的 HTTP(S) 主机和端口")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Host == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.EscapedPath() != "" && u.EscapedPath() != "/") {
		return "", invalid
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", invalid
	}
	authority := net.JoinHostPort(host, port)
	canonical := authority
	if u.Port() == "" {
		canonical = strings.TrimSuffix(authority, ":"+port)
	}
	if canonical != strings.ToLower(u.Host) {
		return "", invalid
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" || ip.String() != host || (ip.Is4In6() && !ip.Unmap().IsLoopback()) {
			return "", invalid
		}
		if ip.Unmap().IsLoopback() {
			return "", nil
		}
		return authority, nil
	}
	labels := strings.Split(host, ".")
	if len(host) > 253 || numericLabel.MatchString(labels[len(labels)-1]) {
		return "", invalid
	}
	for _, s := range labels {
		if !dnsLabel.MatchString(s) {
			return "", invalid
		}
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "", nil
	}
	return authority, nil
}

func field(n *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func managedRow(rows []*yaml.Node, hosts []string, marker string) (*yaml.Node, error) {
	cfg := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, row := range rows {
		id := field(row, "id")
		if id != nil && id.Value == "connection" && field(row, "insert") == nil {
			if conf := field(row, "config"); conf != nil {
				cfg = conf
			}
		}
	}
	if cfg.Kind != yaml.MappingNode {
		return nil, errors.New("connection.config 须为映射")
	}
	original := field(cfg, "trustedHosts")
	expr := "[]"
	if original != nil {
		switch {
		case original.Kind == yaml.ScalarNode && (original.Tag == "!!js" || original.Tag == "tag:yaml.org,2002:js"):
			expr = original.Value
		case original.Kind == yaml.SequenceNode:
			var hosts []string
			for _, item := range original.Content {
				if item.Tag != "!!str" {
					return nil, errors.New("trustedHosts 包含非字符串")
				}
				hosts = append(hosts, item.Value)
			}
			if hosts != nil {
				data, _ := json.Marshal(hosts)
				expr = string(data)
			}
		default:
			return nil, errors.New("无法保留原 trustedHosts 设置")
		}
	} else {
		original = &yaml.Node{}
		cfg.Content = append(cfg.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "trustedHosts"}, original)
	}
	managed, _ := json.Marshal(hosts)
	*original = yaml.Node{Kind: yaml.ScalarNode, Tag: "tag:yaml.org,2002:js", Style: yaml.DoubleQuotedStyle | yaml.TaggedStyle, Value: fmt.Sprintf("[...new Set([...(ctx.get('webStartup')?.trustedHosts ?? []), ...(%s), ...%s])]", expr, managed)}
	var row yaml.Node
	err := row.Encode(map[string]any{"id": "connection", "config": cfg, trustRowID: marker})
	return &row, err
}

const legacyHeading = "# Meerkat managed trusted hosts"

type slotLayout struct {
	rows   []*yaml.Node
	spans  [][2]int
	insert int
	indent string
	flow   bool
	commas []int
}

func inspectSlots(src []byte) (slotLayout, error) {
	layout := slotLayout{insert: len(src)}
	decoder := yaml.NewDecoder(bytes.NewReader(src))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err == io.EOF {
		return layout, nil
	} else if err != nil {
		return layout, err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.SequenceNode || decoder.Decode(&yaml.Node{}) != io.EOF {
		return layout, errors.New("profile 补丁须为单个序列")
	}
	root := doc.Content[0]
	layout.rows = root.Content
	for _, r := range layout.rows {
		if r.Kind != yaml.MappingNode {
			return layout, errors.New("无效的 profile 补丁行")
		}
	}
	lines := bytes.SplitAfter(src, []byte("\n"))
	offsets := make([]int, len(lines)+1)
	for i, b := range lines {
		offsets[i+1] = offsets[i] + len(b)
	}
	if root.Style&yaml.FlowStyle != 0 {
		layout.flow = true
		// Scan punctuation, ignoring delimiters inside strings, comments and nested values.
		level, start, offset := 0, 0, 0
		for _, tok := range lexer.Tokenize(string(src)) {
			if !bytes.HasPrefix(src[offset:], []byte(tok.Origin)) {
				return layout, errors.New("无法定位 flow 补丁原文")
			}
			offset += len(tok.Origin)
			switch tok.Type {
			case token.SequenceStartType, token.MappingStartType, token.SequenceEndType, token.MappingEndType, token.CollectEntryType:
			default:
				continue
			}
			at := offset - len(tok.Origin) + strings.Index(tok.Origin, tok.Value)
			switch tok.Type {
			case token.SequenceStartType, token.MappingStartType:
				if level == 1 {
					start = at
				}
				level++
			case token.MappingEndType, token.SequenceEndType:
				level--
				if level == 1 {
					layout.spans = append(layout.spans, [2]int{start, at + 1})
				}
				if level == 0 {
					layout.insert = at
				}
			case token.CollectEntryType:
				if level == 1 {
					layout.commas = append(layout.commas, at)
				}
			}
		}
		if len(layout.spans) != len(layout.rows) {
			return layout, errors.New("无法定位 flow 补丁行")
		}
		return layout, nil
	}
	layout.indent = strings.Repeat(" ", root.Column-1)
	starts := make([]int, len(layout.rows)+1)
	for i, r := range layout.rows {
		line := r.Line - 1
		for line >= 0 && !blockDash(lines[line], layout.indent) {
			line--
		}
		if line < 0 || i > 0 && starts[i-1] >= line {
			return layout, errors.New("无法定位块序列行")
		}
		starts[i] = line
	}
	end := len(lines)
	for i := starts[len(layout.rows)-1]; i < len(lines); i++ {
		if bytes.HasPrefix(lines[i], []byte("...")) {
			end = i
			layout.insert = offsets[i]
			break
		}
	}
	starts[len(layout.rows)] = end
	for i := range layout.rows {
		last := starts[i+1]
		for last > starts[i]+1 {
			line := bytes.TrimSpace(lines[last-1])
			if len(line) > 0 && line[0] != '#' {
				break
			}
			last--
		}
		layout.spans = append(layout.spans, [2]int{offsets[starts[i]], offsets[last]})
	}
	return layout, nil
}

func blockDash(line []byte, indent string) bool {
	rest, ok := strings.CutPrefix(string(line), indent+"-")
	return ok && (rest == "" || strings.ContainsRune(" \t\r\n", rune(rest[0])))
}

func rewriteTrustSlot(src []byte, hosts []string) ([]byte, error) {
	for {
		layout, err := inspectSlots(src)
		if err != nil {
			return nil, err
		}
		removed := false
		for i, row := range layout.rows {
			id := field(row, "id")
			if id == nil || id.Value != "connection" || field(row, "insert") != nil {
				continue
			}
			marker := field(row, trustRowID)
			start, end := layout.spans[i][0], layout.spans[i][1]
			// The old writer reserved the immediately preceding heading. Even rows copied
			// from this slot belong to Meerkat while they carry either ownership marker.
			before := bytes.TrimSuffix(src[:start], []byte("\n"))
			line := bytes.LastIndexByte(before, '\n') + 1
			heading := !layout.flow && string(bytes.TrimSpace(before[line:])) == legacyHeading
			if marker == nil && !heading {
				continue
			}
			if heading {
				start = line
				if start > 0 && src[start-1] == '\n' && (start == 1 || src[start-2] == '\n') {
					start--
				}
			}
			if layout.flow {
				// Remove just one separator; preserve intervening whitespace and comments.
				comma := -1
				for _, at := range layout.commas {
					if at >= end {
						comma = at
						break
					}
					if at < start {
						comma = at
					}
				}
				if comma >= 0 {
					src = append(bytes.Clone(src[:comma]), src[comma+1:]...)
					if comma < start {
						start--
						end--
					}
				}
			} else if marker != nil && marker.Value == "joined" && start > 0 && src[start-1] == '\n' && end == layout.insert {
				start--
			}
			src = append(bytes.Clone(src[:start]), src[end:]...)
			removed = true
			break
		}
		if removed {
			continue
		}
		if len(hosts) == 0 {
			return src, nil
		}
		at := layout.insert
		marker := "leases"
		prefix := ""
		suffix := ""
		if layout.flow {
			if len(layout.rows) > 0 {
				last := layout.spans[len(layout.rows)-1][1]
				if len(layout.commas) > 0 && layout.commas[len(layout.commas)-1] >= last {
					suffix = ","
				} else {
					prefix = ","
				}
			}
		} else if at > 0 && src[at-1] != '\n' {
			prefix = "\n"
			marker = "joined"
		}
		row, err := managedRow(layout.rows, hosts, marker)
		if err != nil {
			return nil, err
		}
		if layout.flow {
			setFlowStyle(row)
		}
		var data []byte
		if layout.flow {
			data, err = yaml.Marshal(row)
			if err != nil {
				return nil, err
			}
			data = bytes.TrimSuffix(data, []byte("\n"))
		} else {
			var sequence yaml.Node
			sequence.Kind = yaml.SequenceNode
			sequence.Content = []*yaml.Node{row}
			data, err = yaml.Marshal(&sequence)
			if err != nil {
				return nil, err
			}
			data = []byte(layout.indent + strings.ReplaceAll(strings.TrimSuffix(string(data), "\n"), "\n", "\n"+layout.indent) + "\n")
		}
		result := append(bytes.Clone(src[:at]), prefix...)
		result = append(result, data...)
		result = append(result, suffix...)
		result = append(result, src[at:]...)
		return result, nil
	}
}
func setFlowStyle(n *yaml.Node) {
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		n.Style |= yaml.FlowStyle
	}
	for _, c := range n.Content {
		setFlowStyle(c)
	}
}
