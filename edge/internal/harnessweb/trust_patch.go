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
	// Keep cfg in the profile's node graph. Encoding this row on its own
	// would parse aliases without their anchors in preceding rows.
	str := func(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		str("config"), cfg, str("id"), str("connection"), str(trustRowID), str(marker),
	}}, nil
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

// Compare decoded values, including custom tags. Mapping order and scalar
// presentation can change when the profile is saved by DSH.
func sameSlot(a, b *yaml.Node) bool {
	if a.Kind != b.Kind || a.Anchor != b.Anchor {
		return false
	}
	if a.Kind == yaml.AliasNode {
		// The ledger records the reference, not a snapshot of its target.
		// Never follow Alias: anchors may contain a reference to themselves.
		return a.Value == b.Value
	}
	if a.ShortTag() != b.ShortTag() || len(a.Content) != len(b.Content) {
		return false
	}
	switch a.Kind {
	case yaml.MappingNode:
		used := make([]bool, len(b.Content)/2)
		for i := 0; i < len(a.Content); i += 2 {
			matched := false
			for j := 0; j < len(b.Content); j += 2 {
				if !used[j/2] && sameSlot(a.Content[i], b.Content[j]) && sameSlot(a.Content[i+1], b.Content[j+1]) {
					used[j/2] = true
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
	case yaml.SequenceNode:
		for i, c := range a.Content {
			if !sameSlot(c, b.Content[i]) {
				return false
			}
		}
	case yaml.ScalarNode:
		return a.Value == b.Value
	default:
		return false
	}
	return true
}

func retireTrustBlock(src []byte, block string, original []byte) ([]byte, error) {
	separator := "\n" + legacyHeading + "\n"
	split := strings.LastIndex(block, separator)
	if split < 0 {
		return nil, errors.New("旧账本缺少已记录的设备槽位")
	}
	layout, err := inspectSlots(src)
	if err != nil {
		return nil, err
	}
	fragment := []byte(block[split+len(separator):])
	recorded, err := inspectSlots(fragment)
	expected := 1
	if err != nil && len(layout.rows) > 0 {
		// An old slot may reference an anchor in a different profile row.
		// Supply the document context in block sequence form for parsing only.
		prefix, marshalErr := yaml.Marshal(layout.rows)
		if marshalErr != nil {
			return nil, marshalErr
		}
		recorded, err = inspectSlots(append(prefix, fragment...))
		expected += len(layout.rows)
	}
	if err != nil || len(recorded.rows) != expected {
		return nil, errors.New("旧账本设备槽位无效")
	}
	index := -1
	for i, row := range layout.rows {
		if sameSlot(row, recorded.rows[expected-1]) {
			if index >= 0 {
				return nil, errors.New("旧账本的设备槽位在 profile 中重复")
			}
			index = i
		}
	}
	if index < 0 {
		return nil, errors.New("profile 与旧账本的设备槽位不匹配")
	}
	start, end := layout.spans[index][0], layout.spans[index][1]
	// A flow sequence has its own item separator and cannot contain the old
	// block prefix. Keep its current spelling just as for a marked flow slot.
	if layout.flow {
		return removeFlowSlot(src, layout, start, end), nil
	}
	// Walk back over blank lines to the heading directly attached to this row.
	for pos := start; pos > 0; {
		prev := bytes.LastIndexByte(src[:pos-1], '\n') + 1
		line := bytes.TrimSpace(src[prev:pos])
		if bytes.Equal(line, []byte(legacyHeading)) {
			start = prev
			if start > 0 {
				eol := start - 1
				if eol > 0 && src[eol-1] == '\r' {
					eol--
				}
				// Keep the preceding row terminated if another row follows.
				if end == len(src) || eol == 0 || src[eol-1] == '\n' {
					start = eol
				}
			}
			break
		}
		if len(line) != 0 {
			break
		}
		pos = prev
	}
	// The legacy writer sometimes expanded a flow-style base before appending.
	// Restore its saved spelling only while that exact expansion is present.
	if original != nil && end == len(src) && bytes.Equal(src[:start], []byte(block[:split])) {
		return bytes.Clone(original), nil
	}
	return append(bytes.Clone(src[:start]), src[end:]...), nil
}

func removeFlowSlot(src []byte, layout slotLayout, start, end int) []byte {
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
	return append(bytes.Clone(src[:start]), src[end:]...)
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
			if marker == nil {
				continue
			}
			if layout.flow {
				src = removeFlowSlot(src, layout, start, end)
			} else {
				if marker.Value == "joined" && start > 0 && src[start-1] == '\n' && end == layout.insert {
					start--
				}
				src = append(bytes.Clone(src[:start]), src[end:]...)
			}
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
