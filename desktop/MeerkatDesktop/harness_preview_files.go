//go:build !android

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	htmlPreviewLimit     = 4 << 20
	previewMetadataLimit = 1 << 20
	htmlPreviewTTL       = 60 * time.Second
)

var (
	previewID          = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	previewPending     atomic.Bool
	previewListeners   = make(chan struct{}, 4)
	errPreviewCapacity = errors.New("本机预览链接过多，请稍候重试")
	previewClient      = &http.Client{
		Transport:     &http.Transport{},
		Timeout:       12 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
)

type previewStat struct {
	AbsolutePath string `json:"absolutePath"`
	Version      string `json:"version"`
	Bytes        *int   `json:"bytes"`
}

type previewHost struct{ origin, device, cookie string }

type previewRemoteResponse struct {
	RPCID  string `json:"rpcId"`
	Result struct {
		OK    bool            `json:"ok"`
		Value json.RawMessage `json:"value"`
	} `json:"result"`
}

func (h previewHost) call(ctx context.Context, method string, args any) (*http.Response, error) {
	body, err := json.Marshal(map[string]any{"type": "client-request", "rpcId": "meerkat-html-preview", "method": method, "payload": map[string]any{"args": args}})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.origin+"/harness-proxy/"+h.device+"/api/"+method, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if h.cookie != "" {
		request.AddCookie(&http.Cookie{Name: "meerkat_session", Value: h.cookie})
	}
	response, err := previewClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("DSH %s returned HTTP %d", method, response.StatusCode)
	}
	return response, nil
}

func boundedPreviewRead(reader io.Reader, limit int) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, errors.New("响应超过预览上限")
	}
	return raw, nil
}

func decodePreviewValue(raw []byte, output any) error {
	var envelope previewRemoteResponse
	if json.Unmarshal(raw, &envelope) != nil || envelope.RPCID != "meerkat-html-preview" || !envelope.Result.OK {
		return errors.New("DSH 拒绝文件请求或响应格式不受支持")
	}
	return json.Unmarshal(envelope.Result.Value, output)
}

func (h previewHost) json(ctx context.Context, method string, args any, output any) error {
	response, err := h.call(ctx, method, args)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := boundedPreviewRead(response.Body, previewMetadataLimit)
	if err != nil {
		return err
	}
	return decodePreviewValue(raw, output)
}

func previewFilePath(value string) bool {
	if len(value) == 0 || len(value) > 1024 || !utf8.ValidString(value) || path.IsAbs(value) || path.Clean(value) != value || strings.ContainsAny(value, `\:`) {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if strings.HasPrefix(segment, ".") || strings.TrimSpace(segment) != segment {
			return false
		}
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return false
		}
	}
	ext := strings.ToLower(path.Ext(value))
	return ext == ".html" || ext == ".htm"
}

func (h previewHost) files(ctx context.Context, session string) ([]string, bool, error) {
	var result struct {
		Matches    []struct{ Path, Type string } `json:"matches"`
		Truncated  bool                          `json:"truncated"`
		Incomplete bool                          `json:"incomplete"`
	}
	args := map[string]any{"workspaceFileScopeId": session, "request": map[string]any{
		"query": ".htm", "excludedPaths": []string{"node_modules"}, "includeHidden": false, "respectGitignore": false,
	}}
	if err := h.json(ctx, "workspaceFiles/search", args, &result); err != nil {
		return nil, false, err
	}
	files := make([]string, 0, len(result.Matches))
	for _, file := range result.Matches {
		if file.Type == "file" && previewFilePath(file.Path) {
			files = append(files, file.Path)
		}
	}
	return files, result.Truncated || result.Incomplete, nil
}

// workspaceFiles/list confines the resolved directory to the resolved workspace.
// Unlike cwd string comparison this works when the workspace root is an alias.
func (h previewHost) checkParent(ctx context.Context, session, file string) error {
	var result struct {
		Path *string `json:"path"`
	}
	parent := path.Dir(file)
	args := map[string]any{"workspaceFileScopeId": session, "path": parent}
	if err := h.json(ctx, "workspaceFiles/list", args, &result); err != nil {
		return err
	}
	if parent == "." {
		parent = ""
	}
	if result.Path == nil || *result.Path != parent {
		return errors.New("文件父目录无法确认")
	}
	return nil
}

func (h previewHost) stat(ctx context.Context, session, file string) (previewStat, error) {
	var result previewStat
	err := h.json(ctx, "workspaceFiles/stat", map[string]any{"workspaceFileScopeId": session, "path": file}, &result)
	return result, err
}

func (h previewHost) read(ctx context.Context, session, file string, expected previewStat) ([]byte, error) {
	data, err := h.readBytes(ctx, session, file, expected)
	if err == nil && !utf8.Valid(data) {
		return nil, errors.New("文件不是 UTF-8 HTML")
	}
	return data, err
}

func (h previewHost) readBytes(ctx context.Context, session, file string, expected previewStat) ([]byte, error) {
	response, err := h.call(ctx, "workspaceFiles/readBytes", map[string]any{
		"workspaceFileScopeId": session, "path": file, "options": map[string]any{},
	})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	mediaType, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, errors.New("DSH 的文件响应格式不受支持")
	}
	reader := multipart.NewReader(io.LimitReader(response.Body, htmlPreviewLimit+(128<<10)), params["boundary"])
	var metadata, data []byte
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch name := part.FormName(); {
		case name == "metadata" && metadata == nil:
			metadata, err = boundedPreviewRead(part, 64<<10)
		case name == "bytes-0" && data == nil:
			data, err = boundedPreviewRead(part, htmlPreviewLimit)
		default:
			return nil, errors.New("DSH 的文件响应包含额外内容")
		}
		if err != nil {
			return nil, err
		}
	}
	var info struct {
		previewStat
		EOF    bool `json:"eof"`
		Offset int  `json:"offset"`
	}
	if data == nil || decodePreviewValue(metadata, &info) != nil || !info.EOF || info.Offset != 0 ||
		info.AbsolutePath != expected.AbsolutePath || info.Version != expected.Version || info.Bytes == nil ||
		*info.Bytes != len(data) || expected.Bytes == nil || *expected.Bytes != len(data) {
		return nil, errors.New("文件已变化或响应不完整，请重新选择")
	}
	return data, nil
}

func showReviewOpenConfirmation(ctx context.Context, message string) (bool, error) {
	if desktopApp == nil {
		return false, errors.New("本机确认窗口尚未就绪")
	}
	if !previewPending.CompareAndSwap(false, true) {
		return false, errReviewConsentBusy
	}
	confirmed := make(chan bool, 1)
	var settled atomic.Bool
	signal := func(value bool) {
		if settled.CompareAndSwap(false, true) {
			previewPending.Store(false)
			confirmed <- value
		}
	}
	dialog := desktopApp.Dialog.Question().SetTitle("在此设备的默认浏览器打开 HTML？")
	dialog.SetMessage(message)
	open := dialog.AddButton("打开")
	open.OnClick(func() { signal(true) })
	cancel := dialog.AddButton("取消")
	cancel.OnClick(func() { signal(false) })
	dialog.SetDefaultButton(cancel).SetCancelButton(cancel)
	parent := desktopApp.Window.Current()
	if parent == nil && mainWindow != nil {
		parent = mainWindow
	}
	if parent != nil {
		dialog.AttachToWindow(parent)
	}
	confirmation, cancelConfirmation := context.WithTimeout(ctx, htmlPreviewTTL)
	defer cancelConfirmation()
	go dialog.Show()
	select {
	case allowed := <-confirmed:
		return allowed, confirmation.Err()
	case <-confirmation.Done():
		return false, confirmation.Err()
	}
}
