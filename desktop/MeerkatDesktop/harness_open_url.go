//go:build !android

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// DSH 侧边栏的「在系统浏览器中打开」是纯客户端 window.open(url, '_blank',
// 'noopener,noreferrer')。Wails v3.0.0 的 macOS WKWebView 没实现
// webView:createWebViewWithConfiguration:...，Windows 侧也没注册
// NewWindowRequested，于是这个调用被静默丢弃。
//
// 方案 A：在 Meerkat 给设备起的 harness-origin 反代里注入 shim，把这类调用
// 转成一个同源 HTTP 请求，由 Go 侧调 Browser.OpenURL。
//
// 不碰 DSH 安装、不 fork Wails、不改第三方包——这是选方案 A 的前提。

const (
	// harnessOpenURLPath 是注入脚本 POST 的端点。它挂在与 harness origin 同一个
	// listener 上，所以是同源请求，不需要 CORS。
	harnessOpenURLPath = "/__meerkat/open-url"
	// harnessOpenURLHeader 允许用 header 而不是 body 字段传 token。
	harnessOpenURLHeader = "X-Meerkat-Open-URL-Token"
	// harnessShimCap 是允许注入的 HTML 上限，超过就跳过注入（页面照常返回）。
	harnessShimCap = 8 << 20
	// harnessShimMarker 写进注入脚本的 id，方便排查也方便测试断言只注入一次。
	harnessShimMarker = "meerkat-open-url-shim"
)

// harnessEntryKey 标记「这次响应是要注入 shim 的入口页」，由 Director 放在
// 出站请求的 context 上，ModifyResponse 再读出来。
type harnessEntryKey struct{}

//go:embed harness_review_preview.js
var harnessReviewPreviewScript string

// harnessOpenURLToken 是进程启动时随机生成的，注入脚本里带上。
// listener 已经绑在 loopback 上，这一层是防同 origin 上的第三方脚本乱调。
var harnessOpenURLToken = newHarnessToken()

func newHarnessToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

// harnessOpenExternalURL 是浏览器出口。抽成变量是为了让单测换成假实现，
// 不用真的起 Wails。
var harnessOpenExternalURL = func(target string) error {
	if desktopApp == nil {
		return fmt.Errorf("wails app not ready")
	}
	return desktopApp.Browser.OpenURL(target)
}

// isHarnessHTMLEntry 只有这两个路径会返回 DSH 的入口 HTML（没有 SPA fallback）。
func isHarnessHTMLEntry(path string) bool {
	return path == "/" || path == "/index.html"
}

// newHarnessOriginHandler 组装 harness-origin 的 handler：先匹配 shim 自己的
// 端点，再兜底交给反代。端点必须在前，否则会被反代吞掉。
func newHarnessOriginHandler(target *url.URL, token string, review *reviewOpen) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	// 不继承系统代理，否则 Hub 在 tailnet 时请求会被 Clash 等代理拦截。
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	// 关掉 Transport 的透明 gzip：入口 HTML 必须拿到 identity 才能注入。
	// 其它请求仍把客户端的 Accept-Encoding 原样透传，压缩照旧。
	transport.DisableCompression = true
	proxy.Transport = transport

	original := proxy.Director
	proxy.Director = func(req *http.Request) {
		// 要在 original 改写 URL 之前判断，改写后路径会变成 /harness-proxy/{id}/...
		entry := isHarnessHTMLEntry(req.URL.Path)
		original(req)
		req.Header.Del(reviewGestureHeader)
		if !entry {
			return
		}
		req.Header.Del("Accept-Encoding")
		*req = *req.WithContext(context.WithValue(req.Context(), harnessEntryKey{}, true))
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.Request.Context().Value(harnessEntryKey{}) == nil {
			return nil
		}
		var reviewScript string
		if review != nil && review.host.device != review.localID() && resp.Request.Header.Get("Sec-Fetch-Mode") == "navigate" {
			destination := resp.Request.Header.Get("Sec-Fetch-Dest")
			if destination == "document" || destination == "iframe" {
				reviewScript = `<script id="meerkat-review-preview">` + harnessReviewPreviewScript + `(` + strconv.Quote(review.gestureToken) + `);</script>`
			}
		}
		return injectHarnessShimResponse(resp, harnessShimScript(token), reviewScript)
	}

	diagnosticProxy := logFileProxyRequests(proxy, "harness-origin", appendFileProxyDiagnostic)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == harnessOpenURLPath {
			handleHarnessOpenURL(w, r, token)
			return
		}
		// The review capability is attached only to navigations. Service workers
		// on this origin could otherwise read the injected navigation response.
		if review != nil && (r.Header.Get("Sec-Fetch-Dest") == "serviceworker" || r.Header.Get("Service-Worker") == "script") {
			http.Error(w, "Service workers are disabled on this local review origin.", http.StatusForbidden)
			return
		}
		if review != nil && review.intercept(w, r) {
			return
		}
		diagnosticProxy.ServeHTTP(w, r)
	})
}

// injectHarnessShimResponse 把 shim 插进入口 HTML，并把编码/长度改回 identity。
func injectHarnessShimResponse(resp *http.Response, script, reviewScript string) error {
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		return nil
	}
	// 已知超限就完全不碰流，页面照常返回（只是没有 shim）。
	if resp.ContentLength > harnessShimCap {
		return nil
	}
	body, err := readHarnessBody(resp)
	if err != nil {
		return err
	}
	if reviewScript != "" {
		if start := bytes.Index(bytes.ToLower(body), []byte("<head>")); start >= 0 {
			start += len("<head>")
			var entry bytes.Buffer
			entry.Write(body[:start])
			entry.WriteString(reviewScript)
			entry.Write(body[start:])
			body = entry.Bytes()
			resp.Header.Set("Cache-Control", "no-store")
			resp.Header.Del("ETag")
		}
	}
	injected := injectHarnessShim(body, script)
	resp.Body = io.NopCloser(bytes.NewReader(injected))
	resp.ContentLength = int64(len(injected))
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length")
	resp.Header.Set("Content-Length", strconv.Itoa(len(injected)))
	return nil
}

// readHarnessBody 读完响应体；上游若无视 identity 仍然 gzip，这里解掉。
func readHarnessBody(resp *http.Response) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	source := io.LimitReader(resp.Body, harnessShimCap+1)
	var reader io.Reader = source
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		gz, err := gzip.NewReader(source)
		if err != nil {
			return nil, fmt.Errorf("解压入口页失败: %w", err)
		}
		defer func() { _ = gz.Close() }()
		reader = io.LimitReader(gz, harnessShimCap+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if len(body) > harnessShimCap {
		return nil, fmt.Errorf("入口页超过 %d 字节，放弃注入", harnessShimCap)
	}
	return body, nil
}

// injectHarnessShim 在最后一个 </body> 前插入脚本；没有 </body> 就追加。
func injectHarnessShim(html []byte, shim string) []byte {
	script := []byte(shim)
	out := make([]byte, 0, len(html)+len(script))
	if index := bytes.LastIndex(bytes.ToLower(html), []byte("</body>")); index >= 0 {
		out = append(out, html[:index]...)
		out = append(out, script...)
		return append(out, html[index:]...)
	}
	out = append(out, html...)
	return append(out, script...)
}

// harnessShimScript 返回要注入的 <script> 片段。
func harnessShimScript(token string) string {
	marshaledToken, _ := json.Marshal(token)
	marshaledEndpoint, _ := json.Marshal(harnessOpenURLPath)
	return `<script id="` + harnessShimMarker + `">` + `
(function () {
  if (window.__meerkatOpenURLShimInstalled) { return; }
  window.__meerkatOpenURLShimInstalled = true;
  var TOKEN = ` + string(marshaledToken) + `;
  var ENDPOINT = ` + string(marshaledEndpoint) + `;
  function isHttpUrl(value) {
    return /^https?:\/\//i.test(String(value == null ? "" : value).trim());
  }
  function openExternally(value) {
    try {
      fetch(ENDPOINT, {
        method: "POST",
        headers: { "content-type": "application/json", "` + harnessOpenURLHeader + `": TOKEN },
        body: JSON.stringify({ url: String(value), token: TOKEN }),
        credentials: "omit",
        keepalive: true
      }).catch(function () {});
    } catch (error) {}
  }
  // 只拦 DSH 侧边栏那一种调用：_blank + http(s) + features 里带 noopener。
  // 其余（尤其是 about:blank 的登录弹窗）原样交给原函数，行为不变。
  var originalOpen = window.open;
  window.open = function (url, target, features) {
    try {
      if (target === "_blank" && isHttpUrl(url) && String(features == null ? "" : features).toLowerCase().indexOf("noopener") >= 0) {
        openExternally(url);
        return null;
      }
    } catch (error) {}
    return originalOpen.apply(window, arguments);
  };
  // 锚点走的是同一类死路（WKWebView 也静默丢弃），在 capture 阶段一起拦。
  // 用 element.href（已解析的绝对地址）判断 scheme：相对链接也会解析成 http(s)，
  // 而它在浏览器里打到的正是这个 loopback 代理，能正常打开。
  document.addEventListener("click", function (event) {
    try {
      if (event.defaultPrevented) { return; }
      var element = event.target;
      while (element && element.tagName !== "A") { element = element.parentElement; }
      if (!element || element.target !== "_blank") { return; }
      var resolved = element.href || "";
      if (!isHttpUrl(resolved)) { return; }
      var rel = String(element.getAttribute("rel") || "").toLowerCase();
      if (rel.indexOf("noopener") < 0 && rel.indexOf("noreferrer") < 0) { return; }
      event.preventDefault();
      event.stopPropagation();
      openExternally(resolved);
    } catch (error) {}
  }, true);
})();
` + `</script>`
}

// handleHarnessOpenURL 处理注入脚本发来的「用系统浏览器打开」请求。
func handleHarnessOpenURL(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeDesktopJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"error": map[string]string{"message": "只接受 POST"},
		})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeDesktopJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "读取请求失败"},
		})
		return
	}
	var input struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	// body 可能不是 JSON（只用 header 传 token 的情况），解析失败不算错误。
	_ = json.Unmarshal(body, &input)

	candidate := strings.TrimSpace(input.Token)
	if candidate == "" {
		candidate = strings.TrimSpace(r.Header.Get(harnessOpenURLHeader))
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) != 1 {
		writeDesktopJSON(w, http.StatusForbidden, map[string]any{
			"error": map[string]string{"message": "token 校验失败"},
		})
		return
	}

	target, err := normalizeHarnessOpenURL(input.URL)
	if err != nil {
		writeDesktopJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": err.Error()},
		})
		return
	}
	if err := harnessOpenExternalURL(target); err != nil {
		writeDesktopJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{"message": err.Error()},
		})
		return
	}
	writeDesktopJSON(w, http.StatusOK, map[string]any{"ok": true, "url": target})
}

// normalizeHarnessOpenURL 只放行 http/https，并且必须有主机名。
func normalizeHarnessOpenURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("缺少 url")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("url 无法解析")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("只允许 http/https 链接")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("url 缺少主机名")
	}
	return parsed.String(), nil
}
