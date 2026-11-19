//go:build !android

package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	reviewOpenPath      = "/api/changes.open"
	reviewGestureHeader = "X-Meerkat-Review-Gesture"
	reviewPreviewHeader = "X-Meerkat-Review-Preview"
	reviewPreviewPolicy = "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; media-src 'self'; connect-src 'self'; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

type reviewOpen struct {
	host         previewHost
	authority    string
	localID      func() string
	confirm      func(context.Context, string) (bool, error)
	open         func(string) error
	gestureToken string
}

// intercept handles the known changed-file open route for remote devices. The
// application query can name the remote default when DSH lacks an OS default.
func (p reviewOpen) intercept(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.Path != reviewOpenPath || p.host.device == p.localID() {
		return false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err == nil && query.Get("action") == "reveal" {
		return false
	}
	fail := func(status int, message string) {
		w.Header().Set(reviewPreviewHeader, "1")
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, message, status)
	}
	if r.Host != p.authority || r.Header.Get("Origin") != "http://"+p.authority || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		fail(http.StatusForbidden, "请从 Meerkat 的差异面板打开文件。")
		return true
	}
	session, seq := query.Get("sessionId"), query.Get("seq")
	index, parseErr := strconv.Atoi(query.Get("index"))
	if err != nil || parseErr != nil || index < 0 || !previewID.MatchString(session) || !reviewSequence(seq) ||
		(query.Get("action") != "" && query.Get("action") != "open") ||
		len(query["sessionId"]) != 1 || len(query["seq"]) != 1 || len(query["index"]) != 1 || len(query["action"]) > 1 {
		fail(http.StatusBadRequest, "无法识别这次改动，请重新打开差异面板。")
		return true
	}
	host := p.host
	if cookie, err := r.Cookie("meerkat_session"); err == nil {
		host.cookie = cookie.Value
	}
	ctx := r.Context()
	file, err := host.changedFile(ctx, session, seq, index)
	if err != nil {
		fail(http.StatusBadGateway, "无法读取这轮改动，请重新打开差异面板。")
		return true
	}
	if !reviewIsHTML(file) {
		return false
	}
	if r.Header.Get("X-Meerkat-Review-Release") != "0.2.2-rc.2" {
		fail(http.StatusPreconditionFailed, "这个 DSH 版本的差异面板本机接管尚未验证。仍可在 DSH 差异面板内预览。")
		return true
	}
	if p.gestureToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get(reviewGestureHeader)), []byte(p.gestureToken)) != 1 {
		fail(http.StatusForbidden, "请点击差异面板中的打开按钮。")
		return true
	}
	if !reviewRelativePath(file) {
		fail(http.StatusForbidden, "这项 HTML 改动无法定位到会话工作区内的文件。")
		return true
	}
	stat, err := host.reviewFileStat(ctx, session, file)
	if err != nil || *stat.Bytes == 0 {
		fail(http.StatusForbidden, "无法确认工作区内的普通 HTML 文件，请检查符号链接及 4 MiB 大小限制。")
		return true
	}
	absolute := strings.ReplaceAll(stat.AbsolutePath, `\`, "/")
	if !strings.HasSuffix(absolute, "/"+file) {
		fail(http.StatusForbidden, "文件的实际位置与改动记录不一致，请检查路径。")
		return true
	}
	message := fmt.Sprintf("来源设备 %s\n文件 %s\n\n绝对路径\n%s\n\n此页面会以你的浏览器身份运行脚本，可以读取此文件所在目录内受支持的预览资源。仅在信任此页面时打开。\n\n点「打开」会记住选择。可在 Meerkat 设置中恢复询问。", host.device, path.Base(file), previewDialogPath(stat.AbsolutePath))
	allowed, err := p.confirm(ctx, message)
	if err != nil {
		status, message := http.StatusInternalServerError, "无法保存或读取本机的 HTML 打开设置。"
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			status, message = http.StatusRequestTimeout, "本次本机预览确认已过期。"
		case errors.Is(err, errReviewConsentBusy):
			status, message = http.StatusConflict, errReviewConsentBusy.Error()
		default:
			log.Printf("确认 HTML 打开请求: %v", err)
		}
		fail(status, message)
		return true
	}
	if !allowed {
		fail(http.StatusConflict, "已取消在本机浏览器打开。")
		return true
	}
	latest, err := host.reviewFileStat(ctx, session, file)
	if ctx.Err() != nil || err != nil || !samePreviewFile(stat, latest) {
		fail(http.StatusConflict, "文件在确认期间已变化，请重新打开。")
		return true
	}
	preview := reviewPreview{
		host: host, session: session, directory: path.Dir(file),
		canonicalDirectory: strings.TrimSuffix(absolute, path.Base(file)),
		entry:              path.Base(file), entryStat: stat,
	}
	if err := launchReviewPreview(preview, p.open); err != nil {
		if errors.Is(err, errPreviewCapacity) {
			fail(http.StatusTooManyRequests, errPreviewCapacity.Error())
		} else {
			fail(http.StatusInternalServerError, "本机浏览器未接受预览请求。")
		}
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
	return true
}

func reviewSequence(value string) bool {
	seq, err := strconv.ParseUint(value, 10, 53)
	return err == nil && strconv.FormatUint(seq, 10) == value
}

func (h previewHost) changedFile(ctx context.Context, session, seq string, index int) (string, error) {
	query := url.Values{"sessionId": {session}, "seq": {seq}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, h.origin+"/harness-proxy/"+h.device+"/api/changes.summary?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	if h.cookie != "" {
		request.AddCookie(&http.Cookie{Name: "meerkat_session", Value: h.cookie})
	}
	response, err := previewClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("DSH changes.summary returned HTTP %d", response.StatusCode)
	}
	data, err := boundedPreviewRead(response.Body, previewMetadataLimit)
	if err != nil {
		return "", err
	}
	var summary struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if json.Unmarshal(data, &summary) != nil || index >= len(summary.Files) || summary.Files[index].Path == "" {
		return "", errors.New("找不到这项改动")
	}
	return summary.Files[index].Path, nil
}

// Native Windows task dialogs truncate long strings without break points.
func previewDialogPath(absolute string) string {
	var lines []string
	remaining := []rune(absolute)
	for len(remaining) > 36 {
		end := 36
		for index := end - 1; index >= end/2; index-- {
			if remaining[index] == '/' || remaining[index] == '\\' {
				end = index + 1
				break
			}
		}
		lines = append(lines, string(remaining[:end]))
		remaining = remaining[end:]
	}
	return strings.Join(append(lines, string(remaining)), "\n")
}

func reviewIsHTML(file string) bool {
	return strings.EqualFold(path.Ext(file), ".html") || strings.EqualFold(path.Ext(file), ".htm")
}

func reviewRelativePath(file string) bool {
	if len(file) == 0 || len(file) > 1024 || !utf8.ValidString(file) || strings.ContainsAny(file, `\:%`) {
		return false
	}
	for _, segment := range strings.Split(file, "/") {
		if segment == "" || strings.HasPrefix(segment, ".") || strings.TrimSpace(segment) != segment || strings.HasSuffix(segment, ".") || strings.ContainsAny(segment, `<>"|?*`) || unsafePreviewName(segment) {
			return false
		}
	}
	return true
}

func unsafePreviewName(name string) bool {
	for _, r := range name {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return true
		}
	}
	return false
}

func (h previewHost) reviewFileStat(ctx context.Context, session, file string) (previewStat, error) {
	// list checks containment using the remote filesystem's canonical paths;
	// stat and readBytes reject a symlink in the final path component.
	if err := h.checkParent(ctx, session, file); err != nil {
		return previewStat{}, err
	}
	stat, err := h.stat(ctx, session, file)
	if err != nil || stat.AbsolutePath == "" || unsafePreviewName(stat.AbsolutePath) || stat.Version == "" || stat.Bytes == nil || *stat.Bytes < 0 || *stat.Bytes > htmlPreviewLimit {
		return previewStat{}, errors.New("文件不存在、不是普通文件或超过 4 MiB")
	}
	return stat, nil
}

func samePreviewFile(before, after previewStat) bool {
	return before.AbsolutePath == after.AbsolutePath && before.Version == after.Version && before.Bytes != nil && after.Bytes != nil && *before.Bytes == *after.Bytes
}

type reviewPreview struct {
	host                                          previewHost
	session, directory, canonicalDirectory, entry string
	entryStat                                     previewStat
}

// Only the confirmed HTML is a document. Other responses are limited to
// common web resources under its directory and never forward upstream headers.
var reviewResourceTypes = map[string]string{
	".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8", ".json": "application/json",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".svg": "image/svg+xml", ".ico": "image/x-icon",
	".avif": "image/avif", ".webp": "image/webp", ".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf",
	".mp4": "video/mp4", ".webm": "video/webm", ".mp3": "audio/mpeg", ".ogg": "audio/ogg",
}

func (p reviewPreview) serve(host, ticket string, deadline time.Time) http.Handler {
	prefix := "/pv/" + ticket + "/"
	slots := make(chan struct{}, 8)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; frame-ancestors 'none'")
		origin, site := r.Header.Get("Origin"), r.Header.Get("Sec-Fetch-Site")
		if r.Host != host || (origin != "" && origin != "http://"+host) || (site != "" && site != "same-origin" && site != "none") || r.Header.Get("Sec-Fetch-Dest") == "serviceworker" {
			http.Error(w, "本机预览拒绝了其他来源的请求。", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "本机预览只支持读取。", http.StatusMethodNotAllowed)
			return
		}
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, "预览路径或票据无效。", http.StatusForbidden)
			return
		}
		if !time.Now().Before(deadline) {
			http.Error(w, "预览票据已过期，请返回 Meerkat 重新打开。", http.StatusGone)
			return
		}
		relative := strings.TrimPrefix(r.URL.Path, prefix)
		if !reviewRelativePath(relative) || strings.Contains(strings.ToLower(r.URL.EscapedPath()), "%2f") {
			http.Error(w, "请求的文件超出本次预览目录。", http.StatusForbidden)
			return
		}
		isEntry := relative == p.entry
		media := reviewResourceTypes[strings.ToLower(path.Ext(relative))]
		if isEntry {
			media = "text/html; charset=utf-8"
		}
		if media == "" {
			http.Error(w, "该文件类型无法通过本机预览读取。", http.StatusForbidden)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "预览资源请求过多，请重试。", http.StatusTooManyRequests)
			return
		}
		ctx, cancel := context.WithDeadline(r.Context(), deadline)
		defer cancel()
		file := path.Join(p.directory, relative)
		stat, err := p.host.reviewFileStat(ctx, p.session, file)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			http.Error(w, "预览票据已过期，请返回 Meerkat 重新打开。", http.StatusGone)
			return
		}
		if err != nil || strings.ReplaceAll(stat.AbsolutePath, `\`, "/") != p.canonicalDirectory+relative || (isEntry && !samePreviewFile(p.entryStat, stat)) {
			http.Error(w, "无法读取该预览文件，文件可能已变化、越界或是符号链接。", http.StatusForbidden)
			return
		}
		var data []byte
		if r.Method == http.MethodGet {
			data, err = p.host.readBytes(ctx, p.session, file, stat)
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				http.Error(w, "预览票据已过期，请返回 Meerkat 重新打开。", http.StatusGone)
				return
			}
			if err != nil || (isEntry && !utf8.Valid(data)) {
				http.Error(w, "文件已变化或读取不完整，请返回 Meerkat 重新打开。", http.StatusConflict)
				return
			}
		}
		if isEntry {
			w.Header().Set("Content-Security-Policy", reviewPreviewPolicy)
		}
		w.Header().Set("Content-Type", media)
		w.Header().Set("Content-Length", strconv.Itoa(*stat.Bytes))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	})
}

func launchReviewPreview(preview reviewPreview, open func(string) error) error {
	select {
	case previewListeners <- struct{}{}:
	default:
		return errPreviewCapacity
	}
	release := func() { <-previewListeners }
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		release()
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		release()
		return err
	}
	// Browser cookies are host-scoped across ports. Use localhost for the
	// content origin, separate from the 127.0.0.1 control plane and its cookies.
	host := net.JoinHostPort("localhost", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	ticket := hex.EncodeToString(token[:])
	server := &http.Server{
		ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 5 * time.Second,
		Handler: preview.serve(host, ticket, time.Now().Add(htmlPreviewTTL)),
	}
	go func() { _ = server.Serve(listener) }()
	// Keep returning a readable expiry error for a short interval before shutdown.
	timer := time.AfterFunc(htmlPreviewTTL+30*time.Second, func() {
		_ = server.Close()
		release()
	})
	target := url.URL{Scheme: "http", Host: host, Path: "/pv/" + ticket + "/" + preview.entry}
	if err := open(target.String()); err != nil {
		if timer.Stop() {
			_ = listener.Close()
			_ = server.Close()
			release()
		}
		return err
	}
	return nil
}
