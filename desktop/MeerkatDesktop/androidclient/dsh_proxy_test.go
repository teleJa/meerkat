package androidclient

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newRequest(method, target string, headers map[string]string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return req
}

// 只有入口页的文档导航才该摘掉 Accept-Encoding。其余请求必须保留 WebView 自己
// 发的协商头，否则整条链路裸传——实测 12.8 MB 一个字节都没压缩。
func TestIsEntryNavigation(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		headers map[string]string
		want    bool
	}{
		{
			name:    "document in harness",
			target:  "/harness-proxy/dev_1/",
			headers: map[string]string{"Sec-Fetch-Dest": "document"},
			want:    true,
		},
		{
			name:    "index.html as document",
			target:  "/harness-proxy/dev_1/index.html",
			headers: map[string]string{"Sec-Fetch-Dest": "document"},
			want:    true,
		},
		{
			// 关键：插件包和 assets 是 script，绝不能摘掉压缩协商。
			name:    "script asset must keep encoding",
			target:  "/harness-proxy/dev_1/assets/index-ghOpC9uV.js",
			headers: map[string]string{"Sec-Fetch-Dest": "script"},
			want:    false,
		},
		{
			name:    "plugin bundle is a script",
			target:  "/harness-proxy/dev_1/plugins/??@deepseek-ai/dsh-client-modules/client.js",
			headers: map[string]string{"Sec-Fetch-Dest": "script"},
			want:    false,
		},
		{
			name:    "fetch of an api endpoint",
			target:  "/harness-proxy/dev_1/api/remote.mux",
			headers: map[string]string{"Sec-Fetch-Dest": "empty"},
			want:    false,
		},
		{
			// 没有 Sec-Fetch-Dest 时退回路径判断。
			name:   "fallback trailing slash",
			target: "/harness-proxy/dev_1/",
			want:   true,
		},
		{
			name:   "fallback .html suffix",
			target: "/harness-proxy/dev_1/sub/page.html",
			want:   true,
		},
		{
			name:   "fallback asset stays compressed",
			target: "/harness-proxy/dev_1/assets/vendor-BUMdD3V7.js",
			want:   false,
		},
		{
			// harness 之外的路径（其它 Hub API）从来不注入，也不需要 identity。
			name:    "not a harness path",
			target:  "/api/devices",
			headers: map[string]string{"Sec-Fetch-Dest": "document"},
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEntryNavigation(newRequest(http.MethodGet, tc.target, tc.headers)); got != tc.want {
				t.Fatalf("isEntryNavigation(%s, dest=%q) = %v want %v", tc.target, tc.headers["Sec-Fetch-Dest"], got, tc.want)
			}
		})
	}
}

// 只有 /assets/ 下带内容哈希的文件名才加长期缓存；别的路径一律不动，免得把
// 会变的东西（favicon、manifest）也永久钉死。
func TestIsHashedAssetPath(t *testing.T) {
	cases := map[string]bool{
		"/harness-proxy/dev_1/assets/index-ghOpC9uV.js":       true,
		"/harness-proxy/dev_1/assets/vendor-BUMdD3V7.js":      true,
		"/harness-proxy/dev_1/assets/vendor-DV2921Zo.css":     true,
		"/harness-proxy/dev_1/assets/index-CSAdOBx6.css":      true,
		"/harness-proxy/dev_1/favicon.svg":                    false,
		"/harness-proxy/dev_1/manifest.webmanifest":           false,
		"/harness-proxy/dev_1/plugins/??a/client.js":          false,
		"/harness-proxy/dev_1/index.html":                     false,
		"/harness-proxy/dev_1/assets/logo.png":                false, // 名字不带哈希
		"/harness-proxy/dev_1/assets/app-abc.js":              false, // 哈希太短
		"/harness-proxy/dev_1/assets/THIRD_PARTY_NOTICES.txt": false,
	}
	for path, want := range cases {
		if got := isHashedAssetPath(path); got != want {
			t.Fatalf("isHashedAssetPath(%q) = %v want %v", path, got, want)
		}
	}
}

// 入口页拿到的是 identity 就原样返回。
func TestReadDSHHTMLPlain(t *testing.T) {
	body := []byte(`<html><head></head><body><div id="root"></div></body></html>`)
	resp := &http.Response{
		Header:        http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	got, err := readDSHHTML(resp)
	if err != nil {
		t.Fatalf("读 identity 入口页失败: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("identity 正文被改动")
	}
}

// 上游无视 identity 仍然 gzip 时必须解压后再注入，不能像以前那样直接放弃
// 注入——那会让移动端样式和回前台补连一起静默失效。
func TestReadDSHHTMLDecompressesGzip(t *testing.T) {
	plain := []byte(`<html><head></head><body><div id="root"></div><script>__DSH_BOOT__</script></body></html>`)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{
		Header: http.Header{
			"Content-Type":     []string{"text/html; charset=utf-8"},
			"Content-Encoding": []string{"gzip"},
		},
		Body: io.NopCloser(bytes.NewReader(buf.Bytes())),
	}
	got, err := readDSHHTML(resp)
	if err != nil {
		t.Fatalf("解压入口页失败: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("解压结果与原文不一致")
	}
}

// 解压后的 HTML 必须还能正常注入——这是「不解压就放弃注入」那个老行为的回归。
func TestGzippedHTMLEntryStillInjects(t *testing.T) {
	html := `<html><head><script>globalThis["__DSH_BOOT__"]={"release":{"version":"1.2.3","commit":"abc"}}</script></head><body><div id="root"></div></body></html>`
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(html)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{
		Header: http.Header{
			"Content-Type":     []string{"text/html; charset=utf-8"},
			"Content-Encoding": []string{"gzip"},
		},
		Body: io.NopCloser(bytes.NewReader(buf.Bytes())),
	}
	body, err := readDSHHTML(resp)
	if err != nil {
		t.Fatal(err)
	}
	out := InjectDSHMobileHTML(body)
	if !out.Injected {
		t.Fatalf("gzip 入口页注入失败: %s", out.Reason)
	}
	text := string(out.Body)
	for _, want := range []string{`id="meerkat-mobile"`, `id="` + DSHResumeShimID + `"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("注入结果缺少 %q", want)
		}
	}
}

// 不支持的编码要还原原始字节再报错，让调用方能把响应原样交给 WebView，
// 而不是给一个空 body。
func TestReadDSHHTMLRestoresOnUnsupportedEncoding(t *testing.T) {
	raw := []byte("not-really-brotli")
	resp := &http.Response{
		Header: http.Header{
			"Content-Type":     []string{"text/html"},
			"Content-Encoding": []string{"br"},
		},
		Body: io.NopCloser(bytes.NewReader(raw)),
	}
	if _, err := readDSHHTML(resp); err == nil {
		t.Fatal("不支持的编码应报错")
	}
	restored, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, raw) {
		t.Fatal("报错时原始字节没有还原")
	}
}
