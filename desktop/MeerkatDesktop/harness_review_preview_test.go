//go:build !android

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func reviewTestHost(t *testing.T, files map[string]string) (previewHost, *atomic.Int32) {
	t.Helper()
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/harness-proxy/dev_fixture/api/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			t.Errorf("wrong device path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		method := strings.TrimPrefix(r.URL.Path, prefix)
		if method == "changes.summary" {
			if r.Method != http.MethodGet || r.URL.Query().Get("sessionId") != "fixture" || r.URL.Query().Get("seq") != "42" {
				t.Errorf("wrong summary request: %s %s", r.Method, r.URL)
			}
			_, _ = io.WriteString(w, `{"files":[{"path":"site/Review demo.html"},{"path":"notes.txt"},{"path":"/outside.html"}]}`)
			return
		}
		var msg struct {
			Payload struct {
				Args struct{ Path, WorkspaceFileScopeId string }
			}
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&msg) != nil || msg.Payload.Args.WorkspaceFileScopeId != "fixture" {
			t.Errorf("invalid workspace request: %s %s", r.Method, r.URL)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		file := msg.Payload.Args.Path
		data, exists := files[file]
		absolute := "/workspace/" + file
		if file == "site/linked/image.svg" {
			absolute = "/workspace/private/image.svg"
		}
		writeValue := func(value any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"rpcId": "meerkat-html-preview", "result": map[string]any{"ok": true, "value": value}})
		}
		switch method {
		case "workspaceFiles/list":
			writeValue(map[string]any{"path": file})
		case "workspaceFiles/stat":
			if !exists {
				_, _ = io.WriteString(w, `{"rpcId":"meerkat-html-preview","result":{"ok":false}}`)
				return
			}
			writeValue(map[string]any{"absolutePath": absolute, "version": "v1", "bytes": len(data)})
		case "workspaceFiles/readBytes":
			reads.Add(1)
			writer := multipart.NewWriter(w)
			w.Header().Set("Content-Type", writer.FormDataContentType())
			w.Header().Set("Set-Cookie", "upstream=private")
			part, _ := writer.CreateFormFile("bytes-0", "bytes-0")
			_, _ = io.WriteString(part, data)
			_ = writer.WriteField("metadata", fmt.Sprintf(`{"rpcId":"meerkat-html-preview","result":{"ok":true,"value":{"absolutePath":%q,"version":"v1","bytes":%d,"offset":0,"eof":true}}}`, absolute, len(data)))
			_ = writer.Close()
		default:
			t.Errorf("unexpected upstream request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return previewHost{origin: server.URL, device: "dev_fixture"}, &reads
}

func TestReviewOpenInterception(t *testing.T) {
	const entry = "site/Review demo.html"
	host, reads := reviewTestHost(t, map[string]string{entry: "<!doctype html><script>fixture()</script>"})
	fixtureClose := fmt.Errorf("end fixture preview")
	for _, test := range []struct {
		name, query, origin, version, localID string
		allow                                 bool
		status, confirms, opens               int
	}{
		{"approved", "0&application=remote-app", "http://127.0.0.1:1234", "0.2.2-rc.2", "windows", true, 500, 1, 1},
		{"cancelled", "0", "http://127.0.0.1:1234", "0.2.2-rc.2", "windows", false, 409, 1, 0},
		{"non HTML", "1", "http://127.0.0.1:1234", "0.2.2-rc.2", "windows", false, 0, 0, 0},
		{"absolute", "2", "http://127.0.0.1:1234", "0.2.2-rc.2", "windows", true, 403, 0, 0},
		{"reveal", "0&action=reveal", "http://127.0.0.1:1234", "0.2.2-rc.2", "windows", false, 0, 0, 0},
		{"local device", "0", "http://127.0.0.1:1234", "0.2.2-rc.2", "dev_fixture", false, 0, 0, 0},
		{"unverified release", "0", "http://127.0.0.1:1234", "0.3.0", "windows", true, 412, 0, 0},
		{"missing gesture", "0", "http://127.0.0.1:1234", "0.2.2-rc.2", "windows", true, 403, 0, 0},
		{"another origin", "0", "http://localhost:1234", "0.2.2-rc.2", "windows", true, 403, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			confirms, opens := 0, 0
			p := reviewOpen{host: host, authority: "127.0.0.1:1234", gestureToken: "test-gesture", localID: func() string { return test.localID }, confirm: func(_ context.Context, message string) (bool, error) {
				confirms++
				for _, required := range []string{"/workspace/" + entry, "dev_fixture", "运行脚本"} {
					if !strings.Contains(message, required) {
						t.Errorf("confirmation missing %q", required)
					}
				}
				return test.allow, nil
			}, open: func(target string) error {
				opens++
				parsed, _ := url.Parse(target)
				if parsed.Hostname() != "localhost" || path.Base(parsed.Path) != "Review demo.html" {
					t.Errorf("wrong preview destination %q", parsed.Path)
				}
				response, err := previewClient.Get(target)
				if err != nil {
					t.Error(err)
					return fixtureClose
				}
				defer response.Body.Close()
				if response.StatusCode != 200 || strings.Contains(response.Header.Get("Content-Security-Policy"), "sandbox") {
					t.Errorf("preview returned %d with %v", response.StatusCode, response.Header)
				}
				return fixtureClose
			}}
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:1234"+reviewOpenPath+"?sessionId=fixture&seq=42&index="+test.query, nil)
			request.Header.Set("Origin", test.origin)
			request.Header.Set("X-Meerkat-Review-Release", test.version)
			if test.name != "missing gesture" {
				request.Header.Set(reviewGestureHeader, "test-gesture")
			}
			response := httptest.NewRecorder()
			intercepted := p.intercept(response, request)
			if intercepted != (test.status != 0) || (intercepted && response.Code != test.status) || confirms != test.confirms || opens != test.opens {
				t.Fatalf("intercepted=%t status=%d confirms=%d opens=%d body=%q", intercepted, response.Code, confirms, opens, response.Body.String())
			}
		})
	}
	if reads.Load() != 1 {
		t.Fatalf("read bytes %d times, want once after confirmation", reads.Load())
	}
}

func TestReviewTicketFileBoundary(t *testing.T) {
	host, reads := reviewTestHost(t, map[string]string{
		"site/review.html": "<p>entry</p>", "site/assets/pixel.png": "\xff\x00\x80", "site/extra.html": "<script>unconfirmed()</script>", "site/linked/image.svg": "<svg/>",
	})
	size := len("<p>entry</p>")
	p := reviewPreview{host: host, session: "fixture", directory: "site", canonicalDirectory: "/workspace/site/", entry: "review.html", entryStat: previewStat{AbsolutePath: "/workspace/site/review.html", Version: "v1", Bytes: &size}}
	const authority = "localhost:1234"
	for _, test := range []struct {
		path, metadata, method string
		expired                bool
		status                 int
	}{
		{"/pv/ticket/review.html", "none", http.MethodGet, false, 200},
		{"/pv/ticket/assets/pixel.png", "same-origin", http.MethodGet, false, 200},
		{"/pv/ticket/assets/pixel.png", "same-origin", http.MethodHead, false, 200},
		{"/pv/ticket/review.html", "none", http.MethodGet, true, 410},
		{"/pv/ticket/../outside.js", "same-origin", http.MethodGet, false, 403},
		{"/pv/ticket/%2e%2e/outside.js", "same-origin", http.MethodGet, false, 403},
		{"/pv/ticket/assets%2fpixel.png", "same-origin", http.MethodGet, false, 403},
		{"/pv/ticket/linked/image.svg", "same-origin", http.MethodGet, false, 403},
		{"/pv/ticket/extra.html", "same-origin", http.MethodGet, false, 403},
		{"/api/session/list", "same-origin", http.MethodGet, false, 403},
		{"/pv/ticket/review.html", "same-site", http.MethodGet, false, 403},
		{"/pv/ticket/review.html", "same-origin", http.MethodPost, false, 405},
	} {
		deadline := time.Now().Add(time.Minute)
		if test.expired {
			deadline = time.Now().Add(-time.Second)
		}
		request := httptest.NewRequest(test.method, "http://"+authority+test.path, nil)
		request.Header.Set("Sec-Fetch-Site", test.metadata)
		response := httptest.NewRecorder()
		p.serve(authority, "ticket", deadline).ServeHTTP(response, request)
		if response.Code != test.status || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Set-Cookie") != "" {
			t.Errorf("%s %s: got %d, want %d", test.method, test.path, response.Code, test.status)
		}
		if test.method == http.MethodGet && test.status == 200 && strings.HasSuffix(test.path, ".png") && (response.Body.String() != "\xff\x00\x80" || response.Header().Get("Content-Type") != "image/png") {
			t.Errorf("binary resource corrupted: %q", response.Body.String())
		}
	}
	if reads.Load() != 2 {
		t.Fatalf("read %d files, want only the two authorized GETs", reads.Load())
	}
}
