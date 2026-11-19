//go:build !android

package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 1366×768 笔记本（100% 缩放，任务栏约 40px）上，默认 1440×900 必须被收进可用区，
// 否则欢迎页底部按钮会被顶到屏幕外。
func TestClampWindowToWorkAreaSmallLaptop(t *testing.T) {
	width, height := clampWindowToWorkArea(1440, 900, application.Rect{Width: 1366, Height: 728})
	if width > 1366-40 {
		t.Fatalf("宽度 %d 超出 1366 宽的可用区", width)
	}
	if height > 728-40 {
		t.Fatalf("高度 %d 超出 728 高的可用区", height)
	}
	if width < windowMinWidth || height < windowMinHeight {
		t.Fatalf("收缩过度：%dx%d", width, height)
	}
}

// 1080p + 150% 缩放：可用区只有 1280×672 左右，同样必须收进去。
func TestClampWindowToWorkAreaScaledDisplay(t *testing.T) {
	width, height := clampWindowToWorkArea(1440, 900, application.Rect{Width: 1280, Height: 672})
	if width > 1280-40 || height > 672-40 {
		t.Fatalf("收缩后仍超出可用区：%dx%d", width, height)
	}
	if width < windowMinWidth || height < windowMinHeight {
		t.Fatalf("收缩过度：%dx%d", width, height)
	}
}

// 大屏保持默认尺寸，不该无谓放大或缩小。
func TestClampWindowToWorkAreaLargeDisplayKeepsDefault(t *testing.T) {
	width, height := clampWindowToWorkArea(1440, 900, application.Rect{Width: 2560, Height: 1440})
	if width != defaultWindowWidth || height != defaultWindowHeight {
		t.Fatalf("大屏上尺寸被改动：%dx%d", width, height)
	}
}

// 拿不到屏幕信息时不能把窗口缩成 0。
func TestClampWindowToWorkAreaUnknownScreen(t *testing.T) {
	if width, height := clampWindowToWorkArea(1440, 900, application.Rect{}); width != 1440 || height != 900 {
		t.Fatalf("未知屏幕时应保持原尺寸，实际 %dx%d", width, height)
	}
}

// 屏幕比窗口下限还小时，宁可略微超出也要保住可用性（不能被压成一条缝）。
func TestClampWindowToWorkAreaTinyScreenKeepsMinimum(t *testing.T) {
	width, height := clampWindowToWorkArea(1440, 900, application.Rect{Width: 640, Height: 480})
	if width < 640 {
		t.Fatalf("宽度被压到 %d，小于屏幕本身", width)
	}
	if height < 480 {
		t.Fatalf("高度被压到 %d，小于屏幕本身", height)
	}
}

// setup.html 的返回链接靠这个只读端点决定显示与否：首启时 true。
func TestFirstRunEndpointPending(t *testing.T) {
	withConfigDir(t)
	loadConfig()

	handler := buildUIHandlerForTest(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/desktop/first-run", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d，want 200", recorder.Code)
	}
	var payload struct {
		Pending bool `json:"pending"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if !payload.Pending {
		t.Fatal("首启态应返回 pending=true")
	}
}

// 已有配置的老用户从托盘进 setup 时不能显示「返回」：回 /welcome 是错的。
func TestFirstRunEndpointNotPendingWithConfig(t *testing.T) {
	withConfigDir(t)
	writeConfig(t, `{"mode":"bundled","center_url":"http://127.0.0.1:8787"}`, false)
	loadConfig()

	handler := buildUIHandlerForTest(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/desktop/first-run", nil))

	var payload struct {
		Pending bool `json:"pending"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if payload.Pending {
		t.Fatal("已有配置时不该返回 pending=true")
	}
}

// 欢迎页品牌图标要被 embed 进去并能取到，否则欢迎页左上角是破图。
func TestWelcomeIconServed(t *testing.T) {
	withConfigDir(t)
	loadConfig()
	handler := buildUIHandlerForTest(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/welcome-icon.png", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d，want 200", recorder.Code)
	}
	if got := recorder.Header().Get("content-type"); !strings.Contains(got, "image/png") {
		t.Fatalf("content-type = %q，want image/png", got)
	}
	if recorder.Body.Len() < 1024 {
		t.Fatalf("图标只有 %d 字节，太小", recorder.Body.Len())
	}
}

// 欢迎页与 setup 页必须能从 embed 取到（改了 embed 指令后容易漏文件）。
// 首启态下取，否则 /welcome 会按设计重定向。
func TestWelcomeAndSetupPagesServed(t *testing.T) {
	withConfigDir(t)
	loadConfig()
	handler := buildUIHandlerForTest(t)

	for _, path := range []string{"/welcome", "/setup"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d，want 200", path, recorder.Code)
		}
		if recorder.Body.Len() < 500 {
			t.Fatalf("%s 只有 %d 字节", path, recorder.Body.Len())
		}
	}
}

// setup 页的返回链接必须是「首启才显示」：页面本体先 hidden，由 /desktop/first-run 放开。
func TestSetupPageHasHiddenBackLink(t *testing.T) {
	withConfigDir(t)
	loadConfig()
	handler := buildUIHandlerForTest(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/setup", nil))

	body := recorder.Body.String()
	if !strings.Contains(body, `id="back"`) {
		t.Fatal("setup 页缺少返回链接元素")
	}
	if !strings.Contains(body, `href="/welcome"`) {
		t.Fatal("返回链接没有指向 /welcome")
	}
	if !strings.Contains(body, "/desktop/first-run") {
		t.Fatal("返回链接没有按首启态判断")
	}
}

// buildUIHandlerForTest 拿到和桌面端真实路由一致的 handler（不打 Wails 运行时）。
func buildUIHandlerForTest(t *testing.T) http.Handler {
	t.Helper()
	dist, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		t.Fatalf("frontend/dist 未嵌入: %v", err)
	}
	target, err := url.Parse(centerURL())
	if err != nil {
		t.Fatalf("center URL 无效: %v", err)
	}
	return buildUIHandler(dist, newUIProxy(target))
}

// 已经有配置的用户直接访问 /welcome 会被送回主界面：那两个按钮会重写 config，
// node 用户误入后点「创建」就把 Hub 地址和配对令牌整段覆盖掉了。
func TestWelcomeRedirectsWhenConfigured(t *testing.T) {
	withConfigDir(t)
	writeConfig(t, `{"mode":"node","center_url":"http://192.0.2.10:7443","pairing_token":"keep-me"}`, false)
	loadConfig()

	handler := buildUIHandlerForTest(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/welcome", nil))

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d，want 302（已有配置不该看到欢迎页）", recorder.Code)
	}
	if loc := recorder.Header().Get("location"); loc != "/" {
		t.Fatalf("重定向到 %q，want /", loc)
	}
}

// 首启时欢迎页必须正常渲染（不能被上面那条重定向误伤）。
func TestWelcomeServesInFirstRun(t *testing.T) {
	withConfigDir(t)
	loadConfig()

	handler := buildUIHandlerForTest(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/welcome", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d，want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "创建新工作区") {
		t.Fatal("欢迎页缺少「创建新工作区」按钮")
	}
}
