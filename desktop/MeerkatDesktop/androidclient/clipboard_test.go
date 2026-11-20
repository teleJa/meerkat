package androidclient

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestClipboardGrantOnlyForShellNavigation(t *testing.T) {
	for _, test := range []struct {
		name, path, dest, site, referrer string
		granted                          bool
	}{
		{"shell iframe", "/harness-proxy/dev_fixture/", "iframe", "cross-site", "https://wails.localhost/", true},
		{"direct tab", "/harness-proxy/dev_fixture/", "document", "none", "", false},
		{"same origin iframe", "/harness-proxy/dev_fixture/", "iframe", "same-origin", "http://127.0.0.1:40997/", false},
		{"other site iframe", "/harness-proxy/dev_fixture/", "iframe", "cross-site", "https://other.invalid/", false},
		{"preview", "/harness-proxy/dev_fixture/api/file", "iframe", "cross-site", "https://wails.localhost/", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, test.path, nil)
			r.Header.Set("Sec-Fetch-Dest", test.dest)
			r.Header.Set("Sec-Fetch-Mode", "navigate")
			r.Header.Set("Sec-Fetch-Site", test.site)
			r.Header.Set("Referer", test.referrer)
			prepareClipboardNavigation(r)
			bridge := clipboardBridge{write: func(string) bool { return true }}
			result, granted := bridge.inject(r, []byte(dshHTML()))
			if granted != test.granted || bytes.Contains(result, []byte("data-meerkat-clipboard=")) != test.granted {
				t.Fatalf("clipboard grant = %v, want %v", granted, test.granted)
			}
			if granted && r.Referer() != "" {
				t.Fatal("Android shell referrer would be forwarded upstream")
			}
		})
	}
}

func TestClipboardBridgeAuthorizationAndWriteResult(t *testing.T) {
	const host = "127.0.0.1:40997"
	const token = "fixture-page-token"
	for _, test := range []struct {
		name, body     string
		change         func(*http.Request, *clipboardBridge)
		status         int
		write, writeOK bool
		log            string
	}{
		{name: "two copies can reuse the page grant", body: `{"text":"雪\n🦊\u0000"}`, status: 204, write: true, writeOK: true, log: "copy=bridge"},
		{name: "browser success is audited only", body: `{"native":true}`, status: 204, log: "copy=native"},
		{name: "system failure", body: `{"text":"snow"}`, status: 503, write: true, log: "copy: system clipboard write failed"},
		{name: "other origin", body: `{"text":"snow"}`, change: func(r *http.Request, _ *clipboardBridge) { r.Header.Set("Origin", "http://127.0.0.1:40998") }, status: 403},
		{name: "other site", body: `{"text":"snow"}`, change: func(r *http.Request, _ *clipboardBridge) { r.Header.Set("Sec-Fetch-Site", "same-site") }, status: 403},
		{name: "missing token", body: `{"text":"snow"}`, change: func(r *http.Request, _ *clipboardBridge) { r.Header.Del(clipboardTokenHeader) }, status: 403},
		{name: "expired token", body: `{"text":"snow"}`, change: func(_ *http.Request, b *clipboardBridge) { b.expires = time.Now().Add(-time.Second) }, status: 403},
		{name: "GET", body: `{"text":"snow"}`, change: func(r *http.Request, _ *clipboardBridge) { r.Method = http.MethodGet }, status: 405},
		{name: "non-text", body: `{"text":19}`, status: 400},
		{name: "oversize", body: `{"text":"` + strings.Repeat("a", clipboardTextLimit+1) + `"}`, status: 413},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			var logs []string
			bridge := &clipboardBridge{host: host, token: token, expires: time.Now().Add(time.Minute),
				write: func(text string) bool {
					calls++
					if test.writeOK && text != "雪\n🦊\x00" {
						t.Error("plain text changed before native handoff")
					}
					return test.writeOK
				},
				log: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
			}
			for range 2 {
				r := httptest.NewRequest(http.MethodPost, "http://"+host+clipboardPath, strings.NewReader(test.body))
				r.Header.Set("Origin", "http://"+host)
				r.Header.Set("Sec-Fetch-Site", "same-origin")
				r.Header.Set("Sec-Fetch-Mode", "same-origin")
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set(clipboardTokenHeader, token)
				if test.change != nil {
					test.change(r, bridge)
				}
				w := httptest.NewRecorder()
				bridge.proxy(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("clipboard reached upstream") })).ServeHTTP(w, r)
				if w.Code != test.status || w.Body.Len() != 0 {
					t.Fatalf("HTTP %d body bytes=%d, want %d without a body", w.Code, w.Body.Len(), test.status)
				}
			}
			if (test.write && calls != 2) || (!test.write && calls != 0) {
				t.Fatalf("unexpected writes: %d", calls)
			}
			if test.log != "" && (len(logs) != 2 || logs[0] != test.log || logs[1] != test.log) {
				t.Fatalf("unexpected logs: %q", logs)
			}
			if test.log == "" && len(logs) > 0 {
				t.Fatalf("unexpected logs: %q", logs)
			}
		})
	}
}

func TestClipboardGrantChangesOnNavigation(t *testing.T) {
	bridge := clipboardBridge{write: func(string) bool { return true }}
	pattern := regexp.MustCompile(`data-meerkat-clipboard="([a-f0-9]{64})"`)
	tokens := make([]string, 2)
	for i := range tokens {
		r := httptest.NewRequest(http.MethodGet, "/harness-proxy/dev_fixture/", nil)
		r.Header.Set("Sec-Fetch-Dest", "iframe")
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Referer", "https://wails.localhost/")
		prepareClipboardNavigation(r)
		body, _ := bridge.inject(r, []byte(dshHTML()))
		match := pattern.FindSubmatch(body)
		if len(match) != 2 {
			t.Fatal("no random clipboard token injected")
		}
		tokens[i] = string(match[1])
	}
	if tokens[0] == tokens[1] || bridge.token != tokens[1] {
		t.Fatal("previous page token was not replaced")
	}
}
