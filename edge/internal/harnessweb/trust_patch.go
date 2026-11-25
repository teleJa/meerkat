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

	"gopkg.in/yaml.v3"
)

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

// Flow-style documents cannot accept a block row at the end. Keep their
// original representation in the ledger for exact restoration after release.
func appendableBase(base []byte) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(base, &doc); err != nil {
		return nil, false, err
	}
	if len(doc.Content) == 0 {
		return base, false, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.SequenceNode {
		return nil, false, errors.New("profile 补丁须为序列")
	}
	if root.Style&yaml.FlowStyle == 0 && !bytes.HasSuffix(bytes.TrimSpace(base), []byte("...")) {
		return base, false, nil
	}
	root.Style &^= yaml.FlowStyle
	if len(root.Content) == 0 {
		return nil, true, nil
	}
	out, err := yaml.Marshal(&doc)
	return out, true, err
}

// Keep the user's bytes verbatim. Append a final Cordis patch that restates
// the effective connection config since Cordis replaces config as a whole.
func trustBlock(base []byte, hosts []string) (string, error) {
	if len(hosts) == 0 {
		return "", nil
	}
	cfg := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	dec := yaml.NewDecoder(bytes.NewReader(base))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil && err != io.EOF {
		return "", err
	}
	if len(doc.Content) > 0 {
		root := doc.Content[0]
		if root.Kind != yaml.SequenceNode || root.Style&yaml.FlowStyle != 0 {
			return "", errors.New("profile 补丁须为块序列")
		}
		for _, row := range root.Content {
			if row.Kind != yaml.MappingNode {
				return "", errors.New("无效的 profile 补丁行")
			}
			id := field(row, "id")
			if id != nil && id.Value == "connection" && field(row, "insert") == nil {
				if conf := field(row, "config"); conf != nil {
					cfg = conf
				}
			}
		}
		if err := dec.Decode(&doc); err != io.EOF {
			return "", errors.New("不支持多文档补丁")
		}
	}
	if cfg.Kind != yaml.MappingNode {
		return "", errors.New("connection.config 须为映射")
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
					return "", errors.New("trustedHosts 包含非字符串")
				}
				hosts = append(hosts, item.Value)
			}
			if hosts != nil {
				data, _ := json.Marshal(hosts)
				expr = string(data)
			}
		default:
			return "", errors.New("无法保留原 trustedHosts 设置")
		}
	} else {
		original = &yaml.Node{}
		cfg.Content = append(cfg.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "trustedHosts"}, original)
	}
	managed, _ := json.Marshal(hosts)
	*original = yaml.Node{Kind: yaml.ScalarNode, Tag: "tag:yaml.org,2002:js", Style: yaml.DoubleQuotedStyle | yaml.TaggedStyle, Value: fmt.Sprintf("[...new Set([...(ctx.get('webStartup')?.trustedHosts ?? []), ...(%s), ...%s])]", expr, managed)}
	row := map[string]any{"id": "connection", "config": cfg}
	data, err := yaml.Marshal([]any{row})
	if err != nil {
		return "", err
	}
	block := "\n# Meerkat managed trusted hosts\n" + string(data)
	var check []map[string]any
	if err := yaml.Unmarshal(append(bytes.Clone(base), block...), &check); err != nil {
		return "", err
	}
	if len(check) == 0 || check[len(check)-1]["id"] != "connection" {
		return "", errors.New("无法追加 connection 补丁")
	}
	return block, nil
}
