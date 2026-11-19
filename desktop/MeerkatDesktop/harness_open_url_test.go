//go:build !android

package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testHarnessToken = "unit-test-harness-open-url-token"

// newHarnessTestOrigin 起一个假的上游（冒充 Hub 的 /harness-proxy/{id}/），
// 再起一个真的 harness-origin listener，返回它的 base URL。
func newHarnessTestOrigin(t *testing.T, upstream http.HandlerFunc) (*httptest.Server, *httptest.Server) {
	t.Helper()
	hub := httptest.NewServer(upstream)
	t.Cleanup(hub.Close)
	target, err := url.Parse(hub.URL + "/harness-proxy/dev_test/")
	if err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewServer(newHarnessOriginHandler(target, testHarnessToken, nil))
	t.Cleanup(origin.Close)
	return hub, origin
}

func getHarness(t *testing.T, base, path string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return resp, buf.Bytes()
}

func TestReviewGestureCredentialOnlyInNavigation(t *testing.T) {
	const credential = "private-review-gesture"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(reviewGestureHeader) != "" {
			t.Error("gesture credential reached the remote DSH")
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("ETag", "fixture")
		_, _ = w.Write([]byte(`<html><head><script id="dsh"></script></head><body></body></html>`))
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	review := &reviewOpen{host: previewHost{device: "remote"}, localID: func() string { return "local" }, gestureToken: credential}
	local := httptest.NewServer(newHarnessOriginHandler(target, testHarnessToken, review))
	defer local.Close()
	resp, body := getHarness(t, local.URL, "/", map[string]string{reviewGestureHeader: credential, "Sec-Fetch-Mode": "same-origin"})
	if strings.Contains(string(body), credential) || resp.StatusCode != http.StatusOK {
		t.Fatalf("programmatic entry request exposed the credential: HTTP %d", resp.StatusCode)
	}
	resp, body = getHarness(t, local.URL, "/", map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "iframe"})
	text := string(body)
	credentialAt, dshAt := strings.Index(text, credential), strings.Index(text, `id="dsh"`)
	if credentialAt < 0 || credentialAt >= dshAt || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("ETag") != "" {
		t.Fatal("review credential must be isolated from the remote script and caching")
	}
	resp, _ = getHarness(t, local.URL, "/worker.js", map[string]string{"Sec-Fetch-Dest": "serviceworker"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("same-origin worker can observe navigation responses: HTTP %d", resp.StatusCode)
	}
}

func TestHarnessShimInjectsBeforeBodyClose(t *testing.T) {
	_, origin := newHarnessTestOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><div>dsh</div></body></html>"))
	})

	resp, body := getHarness(t, origin.URL, "/", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	text := string(body)
	if !strings.Contains(text, harnessShimMarker) {
		t.Fatal("入口页应被注入 shim")
	}
	if strings.Count(text, harnessShimMarker) != 1 {
		t.Fatalf("只应注入一次，实际 %d", strings.Count(text, harnessShimMarker))
	}
	// 插在 </body> 之前，页面结构不被破坏。
	if strings.Index(text, harnessShimMarker) > strings.Index(text, "</body>") {
		t.Fatal("脚本应插在 </body> 之前")
	}
	if !strings.Contains(text, "<div>dsh</div>") {
		t.Fatal("原页面内容应保留")
	}
	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("注入后不应带 Content-Encoding，实际 %q", got)
	}
	if resp.ContentLength != int64(len(body)) {
		t.Fatalf("Content-Length = %d 与实体 %d 不一致", resp.ContentLength, len(body))
	}
}

func TestHarnessShimInjectsWhenNoBodyTag(t *testing.T) {
	_, origin := newHarnessTestOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html")
		_, _ = w.Write([]byte("<div>fragment</div>"))
	})

	_, body := getHarness(t, origin.URL, "/index.html", nil)
	text := string(body)
	if !strings.Contains(text, harnessShimMarker) {
		t.Fatal("没有 </body> 时应追加脚本")
	}
	if !strings.HasPrefix(text, "<div>fragment</div>") {
		t.Fatal("原内容应保留在最前")
	}
}

func TestHarnessShimSkipsNonHTML(t *testing.T) {
	_, origin := newHarnessTestOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/javascript")
		_, _ = w.Write([]byte("console.log('app');"))
	})

	_, body := getHarness(t, origin.URL, "/", nil)
	if strings.Contains(string(body), harnessShimMarker) {
		t.Fatal("非 HTML 响应不应注入")
	}
	if string(body) != "console.log('app');" {
		t.Fatalf("body 被改动: %q", body)
	}
}

func TestHarnessShimSkipsOtherPaths(t *testing.T) {
	_, origin := newHarnessTestOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html")
		_, _ = w.Write([]byte("<html><body>other</body></html>"))
	})

	_, body := getHarness(t, origin.URL, "/plugins/index.html", nil)
	if strings.Contains(string(body), harnessShimMarker) {
		t.Fatal("只有 / 和 /index.html 注入，其它路径不动")
	}
}

// 入口页请求必须不带 Accept-Encoding（上游才会回 identity），
// 其余请求保持客户端原始的压缩协商。
func TestHarnessShimStripsAcceptEncodingOnEntryOnly(t *testing.T) {
	var entryEncoding, assetEncoding string
	_, origin := newHarnessTestOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		if isHarnessHTMLEntry(r.URL.Path) {
			entryEncoding = r.Header.Get("Accept-Encoding")
			w.Header().Set("content-type", "text/html")
			_, _ = w.Write([]byte("<html><body>x</body></html>"))
			return
		}
		assetEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("content-type", "text/css")
		_, _ = w.Write([]byte("body{}"))
	})

	_, _ = getHarness(t, origin.URL, "/", map[string]string{"Accept-Encoding": "gzip"})
	_, _ = getHarness(t, origin.URL, "/app.css", map[string]string{"Accept-Encoding": "gzip"})

	if entryEncoding != "" {
		t.Fatalf("入口页应去掉 Accept-Encoding，实际 %q", entryEncoding)
	}
	if assetEncoding != "gzip" {
		t.Fatalf("其它资源应保留 Accept-Encoding，实际 %q", assetEncoding)
	}
}

// 上游无视 identity 仍然 gzip 时，注入前要解压，注入后不能留 gzip 头。
func TestHarnessShimHandlesGzippedUpstream(t *testing.T) {
	_, origin := newHarnessTestOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html")
		w.Header().Set("content-encoding", "gzip")
		gz := gzip.NewWriter(w)
		_, _ = gz.Write([]byte("<html><body>zipped</body></html>"))
		_ = gz.Close()
	})

	resp, body := getHarness(t, origin.URL, "/", map[string]string{"Accept-Encoding": "gzip"})
	if resp.Header.Get("Content-Encoding") != "" {
		t.Fatal("注入后应删掉 Content-Encoding")
	}
	text := string(body)
	if !strings.Contains(text, "zipped") || !strings.Contains(text, harnessShimMarker) {
		t.Fatalf("解压并注入后的内容不符: %q", text)
	}
}

func TestHarnessOpenURLRejectsBadToken(t *testing.T) {
	var opened []string
	var mu sync.Mutex
	restore := harnessOpenExternalURL
	harnessOpenExternalURL = func(target string) error {
		mu.Lock()
		opened = append(opened, target)
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { harnessOpenExternalURL = restore })

	_, origin := newHarnessTestOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html")
		_, _ = w.Write([]byte("<html><body>x</body></html>"))
	})

	post := func(payload map[string]any, headerToken string) int {
		body, _ := json.Marshal(payload)
		req, err := http.NewRequest(http.MethodPost, origin.URL+harnessOpenURLPath, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("content-type", "application/json")
		if headerToken != "" {
			req.Header.Set(harnessOpenURLHeader, headerToken)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if status := post(map[string]any{"url": "https://example.com/"}, ""); status != http.StatusForbidden {
		t.Fatalf("缺 token 应 403，实际 %d", status)
	}
	if status := post(map[string]any{"url": "https://example.com/", "token": "wrong"}, ""); status != http.StatusForbidden {
		t.Fatalf("错 token 应 403，实际 %d", status)
	}
	if status := post(map[string]any{"url": "https://example.com/"}, "wrong"); status != http.StatusForbidden {
		t.Fatalf("header 里的错 token 应 403，实际 %d", status)
	}
	if len(opened) != 0 {
		t.Fatalf("token 不合法时不应打开任何链接: %+v", opened)
	}
}

func TestHarnessOpenURLSchemeValidation(t *testing.T) {
	var opened []string
	var mu sync.Mutex
	restore := harnessOpenExternalURL
	harnessOpenExternalURL = func(target string) error {
		mu.Lock()
		opened = append(opened, target)
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { harnessOpenExternalURL = restore })

	_, origin := newHarnessTestOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html")
		_, _ = w.Write([]byte("<html><body>x</body></html>"))
	})

	post := func(target string) int {
		body, _ := json.Marshal(map[string]any{"url": target, "token": testHarnessToken})
		resp, err := http.Post(origin.URL+harnessOpenURLPath, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	for _, bad := range []string{
		"javascript:alert(1)",
		"file:///etc/passwd",
		"data:text/html,<h1>x</h1>",
		"ftp://example.com/x",
		"",
		"not a url at all",
		"http://",
	} {
		if status := post(bad); status != http.StatusBadRequest {
			t.Fatalf("%q 应 400，实际 %d", bad, status)
		}
	}
	if len(opened) != 0 {
		t.Fatalf("非法 scheme 不应打开任何链接: %+v", opened)
	}

	for _, good := range []string{"https://example.com/docs", "http://127.0.0.1:8080/x?y=1"} {
		if status := post(good); status != http.StatusOK {
			t.Fatalf("%q 应 200，实际 %d", good, status)
		}
	}
	if len(opened) != 2 || opened[0] != "https://example.com/docs" {
		t.Fatalf("http/https 应放行: %+v", opened)
	}
}

func TestHarnessOpenURLRejectsGet(t *testing.T) {
	_, origin := newHarnessTestOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html")
		_, _ = w.Write([]byte("<html><body>x</body></html>"))
	})
	resp, err := http.Get(origin.URL + harnessOpenURLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应 405，实际 %d", resp.StatusCode)
	}
}

// 注入的脚本必须是合法 JS——它跑在用户的 DSH 页面里，语法错就整段失效。
func TestHarnessShimScriptParses(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("未安装 node，跳过 JS 语法检查")
	}
	script := harnessShimScript(testHarnessToken)
	// 取出 <script> 标签内的内容交给 node --check。
	start := strings.Index(script, ">")
	end := strings.LastIndex(script, "</script>")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("script 结构异常: %q", script)
	}
	code := script[start+1 : end]

	file := filepath.Join(t.TempDir(), "shim.js")
	if err := os.WriteFile(file, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(nodePath, "--check", file).CombinedOutput(); err != nil {
		t.Fatalf("注入脚本语法错误: %v\n%s", err, out)
	}
}

// shim 只拦「_blank + http(s) + noopener」，其余必须原样透传给原 window.open。
// 这条用 node 跑真实脚本，验证拦截条件而不是 Go 侧的条件。
func TestHarnessShimScriptBehavior(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("未安装 node，跳过 JS 行为检查")
	}
	script := harnessShimScript(testHarnessToken)
	start := strings.Index(script, ">")
	end := strings.LastIndex(script, "</script>")
	code := script[start+1 : end]

	harness := `
const calls = [];
const opened = [];
global.window = {
  __meerkat: true,
  open: function (url, target, features) { calls.push([url, target, features]); return "original"; },
  addEventListener: function () {},
  location: { href: "http://127.0.0.1:1/" }
};
global.document = {
  addEventListener: function () {},
  getElementById: function () { return null; }
};
global.fetch = function (endpoint, options) { opened.push(JSON.parse(options.body).url); return Promise.resolve({ ok: true }); };
global.setTimeout = setTimeout;
` + code + `
function attempt(url, target, features) {
  calls.length = 0;
  const result = window.open(url, target, features);
  return { result: result === null ? "null" : String(result), calls: calls.length };
}
const results = {
  sidebar: attempt("https://example.com/docs", "_blank", "noopener,noreferrer"),
  sidebarUpper: attempt("https://example.com/docs", "_blank", "NOOPENER"),
  aboutBlank: attempt("about:blank", "_blank", "noopener"),
  noNoopener: attempt("https://example.com/", "_blank", "width=600"),
  sameWindow: attempt("https://example.com/", "_self", "noopener"),
  nonHttp: attempt("mailto:a@b.c", "_blank", "noopener"),
  opened: opened
};
console.log(JSON.stringify(results));
`
	file := filepath.Join(t.TempDir(), "behavior.js")
	if err := os.WriteFile(file, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(nodePath, file).CombinedOutput()
	if err != nil {
		t.Fatalf("跑注入脚本失败: %v\n%s", err, out)
	}

	var result struct {
		Sidebar struct {
			Result string
			Calls  int
		} `json:"sidebar"`
		SidebarUpper struct {
			Result string
			Calls  int
		} `json:"sidebarUpper"`
		AboutBlank struct {
			Result string
			Calls  int
		} `json:"aboutBlank"`
		NoNoopener struct {
			Result string
			Calls  int
		} `json:"noNoopener"`
		SameWindow struct {
			Result string
			Calls  int
		} `json:"sameWindow"`
		NonHTTP struct {
			Result string
			Calls  int
		} `json:"nonHttp"`
		Opened []string `json:"opened"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("解析结果失败: %v\n%s", err, out)
	}

	// 侧边栏那种调用：被拦，返回 null，且没有走到原函数。
	for name, caseResult := range map[string]struct {
		Result string
		Calls  int
	}{
		"sidebar":      result.Sidebar,
		"sidebarUpper": result.SidebarUpper,
	} {
		if caseResult.Result != "null" || caseResult.Calls != 0 {
			t.Fatalf("%s 应被拦截: %+v", name, caseResult)
		}
	}
	// 其它调用：原样交给原函数。
	for name, caseResult := range map[string]struct {
		Result string
		Calls  int
	}{
		"aboutBlank": result.AboutBlank,
		"noNoopener": result.NoNoopener,
		"sameWindow": result.SameWindow,
		"nonHttp":    result.NonHTTP,
	} {
		if caseResult.Result != "original" || caseResult.Calls != 1 {
			t.Fatalf("%s 应原样透传: %+v", name, caseResult)
		}
	}
	if len(result.Opened) != 2 {
		t.Fatalf("应只把两条侧边栏调用转出去，实际 %+v", result.Opened)
	}
}
