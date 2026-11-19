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
	"strings"
	"testing"
)

func TestPreviewFilePath(t *testing.T) {
	for _, file := range []string{"报告/index.html", "demo.HTM"} {
		if !previewFilePath(file) {
			t.Errorf("rejected %q", file)
		}
	}
	for _, file := range []string{"/tmp/index.html", "../index.html", "a/../../index.html", `C:\index.html`, `a\index.html`, ".secret/index.html", "file.svg", "a/ x.html"} {
		if previewFilePath(file) {
			t.Errorf("accepted %q", file)
		}
	}
}

func TestPreviewSearchIncludesIgnoredBuildOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			Payload struct {
				Args struct {
					Request struct {
						RespectGitignore *bool
						ExcludedPaths    []string
					}
				}
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil || msg.Payload.Args.Request.RespectGitignore == nil || *msg.Payload.Args.Request.RespectGitignore {
			t.Errorf("search must explicitly include ignored files")
		}
		excluded := msg.Payload.Args.Request.ExcludedPaths
		if len(excluded) != 1 || excluded[0] != "node_modules" {
			t.Errorf("search exclusions = %v, want [node_modules]", excluded)
		}
		_, _ = io.WriteString(w, `{"rpcId":"meerkat-html-preview","result":{"ok":true,"value":{"matches":[{"path":"dist/index.html","type":"file"},{"path":"../secret.html","type":"file"}]}}}`)
	}))
	defer server.Close()
	host := previewHost{origin: server.URL, device: "dev_test"}
	files, _, err := host.files(context.Background(), "s_test")
	if err != nil || len(files) != 1 || files[0] != "dist/index.html" {
		t.Fatalf("files: %v, %v", files, err)
	}
}

func TestPreviewParentUsesRemoteConfinement(t *testing.T) {
	for _, tc := range []struct {
		file, request, response string
		wantErr                 bool
	}{
		{"index.html", ".", `{"ok":true,"value":{"path":""}}`, false},
		{"dist/index.html", "dist", `{"ok":true,"value":{"path":"dist"}}`, false},
		{"dist/index.html", "dist", `{"ok":true,"value":{"path":"other"}}`, true},
		{"dist/index.html", "dist", `{"ok":false}`, true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var msg struct {
				Payload struct{ Args struct{ Path string } }
			}
			if err := json.NewDecoder(r.Body).Decode(&msg); err != nil || msg.Payload.Args.Path != tc.request || !strings.HasSuffix(r.URL.Path, "/workspaceFiles/list") {
				t.Errorf("incorrect parent query: %s %q, %v", r.URL.Path, msg.Payload.Args.Path, err)
			}
			_, _ = fmt.Fprintf(w, `{"rpcId":"meerkat-html-preview","result":%s}`, tc.response)
		}))
		host := previewHost{origin: server.URL, device: "dev_test"}
		err := host.checkParent(context.Background(), "s_test", tc.file)
		server.Close()
		if (err != nil) != tc.wantErr {
			t.Errorf("parent check for %s with %s: %v", tc.file, tc.response, err)
		}
	}
}

func TestPreviewReadRejectsOtherRevision(t *testing.T) {
	// A generated report above the old 1 MiB cap must still work.
	data := "<h1>Preview</h1>" + strings.Repeat(" ", 2<<20)
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/harness-proxy/dev_test/api/workspaceFiles/readBytes" {
					t.Errorf("unexpected route %q", r.URL.Path)
				}
				var msg struct {
					Payload struct{ Args map[string]json.RawMessage }
				}
				if err := json.NewDecoder(r.Body).Decode(&msg); err != nil || string(msg.Payload.Args["path"]) != `"public/demo.html"` {
					t.Errorf("unexpected RPC request: %+v, %v", msg, err)
				}
				writer := multipart.NewWriter(w)
				w.Header().Set("Content-Type", writer.FormDataContentType())
				part, _ := writer.CreateFormFile("bytes-0", "bytes-0")
				_, _ = io.WriteString(part, data)
				_ = writer.WriteField("metadata", fmt.Sprintf(`{"rpcId":"meerkat-html-preview","result":{"ok":true,"value":{"absolutePath":"/workspace/public/demo.html","version":%q,"bytes":%d,"offset":0,"eof":true,"data":null}}}`, version, len(data)))
				_ = writer.Close()
			}))
			defer server.Close()
			size := len(data)
			host := previewHost{origin: server.URL, device: "dev_test"}
			got, err := host.read(context.Background(), "s_test", "public/demo.html", previewStat{AbsolutePath: "/workspace/public/demo.html", Version: "v1", Bytes: &size})
			if version == "v1" && (err != nil || string(got) != data) {
				t.Fatalf("valid preview failed: %q, %v", got, err)
			}
			if version == "v2" && (err == nil || got != nil) {
				t.Fatal("changed revision was exposed")
			}
		})
	}
}
