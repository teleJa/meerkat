package androidclient

import (
	"bytes"
	"strings"
	"testing"
)

func dshHTML() string {
	return `<!doctype html><html><head><script>globalThis["__DSH_BOOT__"]={"release":{"version":"0.2.2-rc.2","commit":"a6b1b85"}}</script></head><body><div id="root"></div><div class="bt-leftcol"></div></body></html>`
}

func TestInjectDSHMobileHTML(t *testing.T) {
	out := InjectDSHMobileHTML([]byte(dshHTML()))
	if !out.Injected {
		t.Fatalf("expected injection, reason=%s", out.Reason)
	}
	if out.Version != "0.2.2-rc.2" || out.Commit != "a6b1b85" {
		t.Fatalf("version=%q commit=%q", out.Version, out.Commit)
	}
	text := string(out.Body)
	if !strings.Contains(text, `id="meerkat-mobile"`) {
		t.Fatal("style not injected")
	}
	if !strings.Contains(text, "@media (max-width:720px)") {
		t.Fatal("mobile media query missing")
	}
	// 顶栏让位走 ds-bot 自己的钩子，不再手工覆盖 .bt-leftcol 的 top/height
	// （两者叠加会把侧边栏内容顶到 94px 那么低）。
	if !strings.Contains(text, "--dsh-frame-top-clearance:44px") {
		t.Fatal("top clearance hook missing")
	}
	if strings.Contains(text, ".bt-leftcol{top:44px") {
		t.Fatal("manual .bt-leftcol top override should be gone")
	}
	// 窄屏抽屉覆盖正文，开合时都不应给正文留出侧栏宽度。
	if strings.Contains(text, "margin-left:72px") {
		t.Fatal("mobile drawer must not reserve the old 72px sidebar offset")
	}
	if !strings.Contains(text, "@media (max-width:720px){\n  "+`html body #root [class*="_centerCol"]{margin-left:0!important`) {
		t.Fatal("narrow-screen content must have zero left margin")
	}
	if strings.Contains(text, "@media (max-width:720px){\n  #root{margin-left:72px") {
		t.Fatal("unconditional root margin would keep the content narrow when the sidebar is hidden")
	}
	// 外壳（第三方 sidebar 组件）自带的 toggle 必须藏掉，否则窄屏下和 ds-bot
	// 自己的按钮同时出现。用稳定语义属性，不用会随版本变的哈希 class。
	if !strings.Contains(text, `[data-shell-leading] button`) {
		t.Fatal("shell leading toggle hiding rule missing")
	}
	if !strings.Contains(text, `[data-slot="shell.leading"] button`) {
		t.Fatal("shell.leading slot toggle hiding rule missing")
	}
	// 反过来：不能再依赖 ds-bot 那条只在 Windows 标题栏生效的排他规则。
	if strings.Contains(text, "data-windows-titlebar") {
		t.Fatal("不该假设 Windows 标题栏属性存在")
	}
	if strings.Index(text, "meerkat-mobile") > strings.Index(text, "</head>") {
		t.Fatal("style inserted after head")
	}
}

// 触屏上气泡的宽度上限必须整条放开：ds-bot 的 `calc(100% - 82px)` 是给桌面端
// hover 工具条留的位置，手机上没有 hover，那 82px 纯属白留。
func TestInjectDSHMobileHTMLWidensBubblesOnTouch(t *testing.T) {
	out := InjectDSHMobileHTML([]byte(dshHTML()))
	if !out.Injected {
		t.Fatalf("expected injection, reason=%s", out.Reason)
	}
	text := string(out.Body)
	if !strings.Contains(text, "@media (hover:none)") {
		t.Fatal("touch-only bubble rule must be scoped to (hover:none)")
	}
	// 三个选择器都要覆盖：ds-bot 的三道上限分别挂在不同元素上。
	for _, selector := range []string{".bt-msg", ".bt-msg-line>.bt-bubble", ".bt-bubble"} {
		if !strings.Contains(text, selector+",") && !strings.Contains(text, selector+"{") {
			t.Fatalf("气泡宽度规则缺少选择器 %q", selector)
		}
	}
	// 必须是 100%，不能是 calc(var(--bt-msg-w)*100%)：后者只放开 82px、
	// 仍保留 80% 上限，气泡只能到 260.8，用户抱怨的那条空白还在。
	if !strings.Contains(text, "max-width:100%!important") {
		t.Fatal("气泡应放开到 100%")
	}
	if strings.Contains(text, "max-width:calc(var(--bt-msg-w)") {
		t.Fatal("不该只放开 80% 上限")
	}
	// 不能顺手改掉多行缩进（那是 calc(100% - 30px)，不是 82px）。
	if strings.Contains(text, "100% - 30px") {
		t.Fatal("多行缩进规则不在本次范围内，不要覆盖")
	}
}

// DSH 的图片查看器与 Modal 遮罩用另一个宿主钩子 --dsh-frame-chrome-top：
// 不设它时图片查看器的关闭按钮落在 top:20px，会钻进 Meerkat 44px 顶栏里和
// 刷新按钮重叠。这两个变量管不同元素，必须都设且都等于顶栏高度。
func TestInjectDSHMobileHTMLSetsChromeTop(t *testing.T) {
	out := InjectDSHMobileHTML([]byte(dshHTML()))
	if !out.Injected {
		t.Fatalf("expected injection, reason=%s", out.Reason)
	}
	text := string(out.Body)
	if !strings.Contains(text, "--dsh-frame-chrome-top:44px") {
		t.Fatal("缺少 --dsh-frame-chrome-top:44px（图片查看器/Modal 遮罩的让位钩子）")
	}
	// 上一轮的 --dsh-frame-top-clearance 不能丢：它管 ds-bot 自己的按钮。
	if !strings.Contains(text, "--dsh-frame-top-clearance:44px") {
		t.Fatal("--dsh-frame-top-clearance 不能丢")
	}
	// 两个值必须一致（都等于 Meerkat 顶栏高度），否则按钮和浮层会对不齐。
	for _, decl := range []string{"--dsh-frame-top-clearance:44px", "--dsh-frame-chrome-top:44px"} {
		if !strings.Contains(text, decl) {
			t.Fatalf("缺少 %q", decl)
		}
	}
}

func TestInjectDSHMobileHTMLIsConservative(t *testing.T) {
	for name, body := range map[string]string{
		"missing boot": `<html><head></head><body><div id="root"></div></body></html>`,
		"missing root": `<html><head><script>__DSH_BOOT__</script></head><body></body></html>`,
		"missing head": `<html><body><div id="root"></div><div class="bt-leftcol"></div></body></html>`,
		"not html":     `{"devices":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			original := []byte(body)
			out := InjectDSHMobileHTML(original)
			if out.Injected {
				t.Fatal("should not inject")
			}
			if !bytes.Equal(out.Body, original) {
				t.Fatal("body changed on fallback")
			}
			if out.Reason == "" {
				t.Fatal("expected fallback reason")
			}
		})
	}
}

func TestInjectDSHMobileHTMLAlreadyInjected(t *testing.T) {
	first := InjectDSHMobileHTML([]byte(dshHTML()))
	if !first.Injected {
		t.Fatal("first injection failed")
	}
	second := InjectDSHMobileHTML(first.Body)
	if second.Injected {
		t.Fatal("second injection should be skipped")
	}
	if !bytes.Equal(second.Body, first.Body) {
		t.Fatal("body changed on already-injected fallback")
	}
}
