package androidclient

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
)

// DSH 页面的初始加载量很大：实测 37 个请求合计 12.8 MB，其中插件包 11.2 MB、
// assets 1.8 MB。这些都经过手机上的回环反代，而反代此前对所有请求无条件摘掉
// Accept-Encoding（那是为了 HTML 注入），于是最贵的「手机 ↔ Hub」这一段一个
// 字节都没压缩，loadEvent 11.96 s。
//
// 这里把「要注入的 HTML」和「其余资源」分开：只有入口页请求摘 Accept-Encoding，
// 其余让 WebView 自己的压缩协商原样透传。同时给带内容哈希的 assets 补上长期
// 缓存——DSH 自己发的是 no-cache，每次打开都要重下 1.8 MB。

const (
	// dshHTMLCap 是允许注入的 HTML 上限，超了就完全不碰、按原样返回。
	dshHTMLCap = 8 << 20
	// dshAssetCacheControl 只给文件名带内容哈希的静态产物用：内容变了名字就变，
	// 所以可以永久缓存。
	dshAssetCacheControl = "public, max-age=31536000, immutable"
)

// hashedAssetName 匹配 Vite 之类的构建产物命名：`index-ghOpC9uV.js`、
// `vendor-BUMdD3V7.js`、`vendor-DV2921Zo.css`。要求分隔符后至少 6 位
// base62-ish 且紧贴扩展名，才能说明它带内容哈希而不是任意名字。
var hashedAssetName = regexp.MustCompile(`^[^/]+-[A-Za-z0-9_-]{6,}\.[A-Za-z0-9]+$`)

// isHashedAssetPath 判断这次响应是不是可以长期缓存的静态产物。
//
// 只看 /assets/ 下的资源：/plugins/ 的 bundle 本来就带 immutable，其它路径
// （favicon、manifest 之类）名字不带哈希，钉住会有更新不生效的风险。
func isHashedAssetPath(requestPath string) bool {
	if !strings.Contains(requestPath, "/assets/") {
		return false
	}
	return hashedAssetName.MatchString(path.Base(requestPath))
}

// isEntryNavigation 判断这次请求是不是 DSH 入口页的文档导航——只有它需要
// identity 字节来注入。
//
// 首选 Sec-Fetch-Dest: document（WebView 会带），它最准确：同样是 text/html，
// 入口页是 document，别的用途可能是 fetch。拿不到这个头时退回路径判断，只
// 认 /harness-proxy/ 前缀下以 / 结尾或 .html 结尾的路径。
func isEntryNavigation(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	requestPath := req.URL.Path
	if !strings.Contains(requestPath, "/harness-proxy/") {
		return false
	}
	switch strings.ToLower(req.Header.Get("Sec-Fetch-Dest")) {
	case "document", "iframe":
		return true
	case "":
		// 没有 Sec-Fetch-Dest（老 WebView 或非浏览器调用）：退回路径判断。
		if strings.HasSuffix(requestPath, "/") || strings.HasSuffix(strings.ToLower(requestPath), ".html") {
			return true
		}
		return false
	default:
		// 明确是别的用途（fetch/script/image…）：不摘，让压缩生效。
		return false
	}
}

// readDSHHTML 读出入口页的 identity 字节。
//
// 入口页请求已经被摘掉 Accept-Encoding，正常情况下上游回 identity；但 Hub 那层
// 的 http.DefaultTransport 在没收到 Accept-Encoding 时会自己补 gzip 再自己解压，
// 也可能有别的中间层插一手。所以这里兜底解一次 gzip，而不是像以前那样「看到
// Content-Encoding 就放弃注入」——那会让注入静默失效，页面看着正常但没有移动端
// 样式也没有回前台补连。
//
// 出错时把已经读到的原始字节放回去（连同原来的 Content-Encoding），保证调用方
// 至少能把响应原样交给 WebView，而不是给一个空 body。
func readDSHHTML(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("空响应")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, dshHTMLCap+1))
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	restore := func(body []byte) {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	}
	if len(raw) > dshHTMLCap {
		restore(raw)
		return nil, fmt.Errorf("入口页超过 %d 字节，放弃注入", dshHTMLCap)
	}
	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	if encoding == "" || encoding == "identity" {
		return raw, nil
	}
	if encoding != "gzip" {
		restore(raw)
		return nil, fmt.Errorf("不支持的 Content-Encoding %q", encoding)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		restore(raw)
		return nil, fmt.Errorf("解压入口页失败: %w", err)
	}
	defer func() { _ = gz.Close() }()
	plain, err := io.ReadAll(io.LimitReader(gz, dshHTMLCap+1))
	if err != nil {
		restore(raw)
		return nil, fmt.Errorf("解压入口页失败: %w", err)
	}
	if len(plain) > dshHTMLCap {
		restore(raw)
		return nil, fmt.Errorf("解压后的入口页超过 %d 字节，放弃注入", dshHTMLCap)
	}
	return plain, nil
}
