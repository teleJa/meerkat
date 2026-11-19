package proxyenv

import "testing"

func TestParseOnlyProxyKeys(t *testing.T) {
	values := Parse("HTTP_PROXY=http://127.0.0.1:7899\nPATH=/usr/bin\nHTTPS_PROXY=https://proxy.example:443\n")
	if values["HTTP_PROXY"] != "http://127.0.0.1:7899" || values["HTTPS_PROXY"] != "https://proxy.example:443" {
		t.Fatalf("unexpected proxy values: %#v", values)
	}
	if _, ok := values["PATH"]; ok {
		t.Fatal("PATH should not be copied")
	}
}

func TestNormalizeAddsNodeProxyAndNoProxy(t *testing.T) {
	values := normalize(map[string]string{"HTTPS_PROXY": "http://127.0.0.1:7899", "NO_PROXY": "example.com"}, "http://192.0.2.10:7443")
	if values["NODE_USE_ENV_PROXY"] != "1" {
		t.Fatal("NODE_USE_ENV_PROXY must be set")
	}
	for _, want := range []string{"localhost", "127.0.0.1", "::1", "192.0.2.10", "example.com"} {
		if !contains(values["NO_PROXY"], want) {
			t.Fatalf("NO_PROXY missing %q: %s", want, values["NO_PROXY"])
		}
	}
}

func TestNormalizeWithoutProxy(t *testing.T) {
	if values := normalize(map[string]string{"NO_PROXY": "localhost"}, ""); values != nil {
		t.Fatalf("expected nil, got %#v", values)
	}
}

func contains(value, item string) bool {
	for _, part := range splitComma(value) {
		if part == item {
			return true
		}
	}
	return false
}

func splitComma(value string) []string {
	var result []string
	for _, item := range iterate(value) {
		result = append(result, item)
	}
	return result
}

func iterate(value string) []string {
	var result []string
	current := ""
	for _, r := range value {
		if r == ',' {
			result = append(result, current)
			current = ""
			continue
		}
		current += string(r)
	}
	result = append(result, current)
	return result
}
