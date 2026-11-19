package harnessweb

import (
	"context"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var assetRefPattern = regexp.MustCompile(`(?:src|href)="([^"]+)"`)

// warmup 在 dsh web 刚启动时主动拉取 HTML 和主要静态包，触发插件 bundle 的首次构建，
// 避免用户第一次打开 iframe 时长时间停在 Loading plugins。
func warmup(ctx context.Context, rawURL string, logger *log.Logger) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Timeout: 30 * time.Second,
		Jar:     jar,
		Transport: &http.Transport{
			Proxy:             nil,
			ForceAttemptHTTP2: true,
			DialContext:       (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		},
	}
	response, err := client.Get(rawURL)
	if err != nil {
		logger.Printf("dsh warmup 打开首页失败: %v", err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	_ = response.Body.Close()
	if err != nil {
		logger.Printf("dsh warmup 读取首页失败: %v", err)
		return
	}
	base, err := url.Parse(response.Request.URL.String())
	if err != nil {
		return
	}
	seen := map[string]bool{}
	refs := []string{}
	for _, match := range assetRefPattern.FindAllStringSubmatch(string(body), -1) {
		ref := html.UnescapeString(strings.TrimSpace(match[1]))
		if ref == "" || strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "#") {
			continue
		}
		parsed, err := base.Parse(ref)
		if err != nil {
			continue
		}
		path := parsed.Path
		if !(strings.Contains(path, "/assets/") || strings.Contains(path, "/plugins/")) {
			continue
		}
		key := parsed.String()
		if seen[key] || len(refs) >= 24 {
			continue
		}
		seen[key] = true
		refs = append(refs, key)
	}
	ok := 0
	for _, ref := range refs {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(request)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			ok++
		}
	}
	logger.Printf("dsh warmup 完成：首页 %d 字节，预取 %d/%d 个静态资源", len(body), ok, len(refs))
}
