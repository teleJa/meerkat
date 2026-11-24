package androidclient

import (
	"bytes"
	"regexp"
)

// dshMobileCSS is injected into the DSH HTML by the local reverse proxy.
//
// 三件事：
//
//  1. --dsh-frame-top-clearance 是 ds-bot 留给宿主的钩子（它自己只读不写）。
//     设成 Meerkat 浮动顶栏的 44px 后，ds-bot 的两处定位会自动让开：
//     折叠/展开按钮 `top:calc(var(--dsh-frame-top-clearance) + 6px)`，以及
//     `.bt-leftcol` 的 `--bt-lc-clear`（它的 padding 和 top 都基于这个值）。
//     不要再手写 `.bt-leftcol{top:44px!important}` 之类的覆盖：两者叠加会把
//     侧边栏内容顶到 94px 那么低。
//
//  2. #root 预留 44px 给浮动顶栏，避免会话内容钻到顶栏下面。
//
//  3. 窄屏侧栏覆盖正文。正文列必须覆盖 ds-bot 在运行时追加的 88px 外边距。
//
//  4. 隐藏外壳（第三方 sidebar 组件）自带的侧边栏按钮。
//     ds-bot 自己有一条 `html[data-windows-titlebar] [class*="_logoRow"]>[class*="_toggle"]
//     {display:none!important}`，但前缀是排他的 —— 只在 Windows 标题栏环境下生效。
//     Android WebView 里 documentElement 没有 data-windows-titlebar，那条规则不触发，
//     于是外壳按钮和 ds-bot 自己的按钮同时显示（rail 时是两个：外壳「打开侧边栏」+
//     ds-bot「收起侧边栏」；hidden 时也是两个，几乎重叠）。
//     这是 ds-bot 的实现缺口（它注释写 "hidden wherever it sits"，代码只覆盖了一种），
//     不是我们注入 CSS 造成的：不注入任何 CSS、纯窄视口下两个按钮就已经并存。
//     这里用**稳定语义属性**而不是哈希 class（IW6AQa_iconButton 之类的换版本就变）：
//     外壳 toggle 的祖先带 data-shell-leading / data-slot="shell.leading"。
//     注意必须写 `button` 而不是 `[class*="_toggle"]`：按钮本身带 _toggle class，
//     但它不是带 data-shell-leading 那个元素的后代，后代选择器命中数为 0。
//
//  5. 触屏上把气泡的宽度上限整条放开。
//     ds-bot 给 .bt-msg 设了两道上限：
//     .bt-msg{max-width:min(calc(var(--bt-msg-w) * 100%),calc(100% - 82px))}
//     --bt-msg-w: .8
//     窄屏下真正卡住的是 **82px** 那一项（不是 80%）：它是给桌面端「hover 时出现在
//     气泡右侧的工具条」留的位置。触屏没有 hover —— ds-bot 自己已经有一条
//     `@media (hover:none)` 把工具条挪到气泡下方（`body[data-bt-layout=flat]
//     .bt-tools{top:100%;right:0}`），所以这 82px 在手机上纯属白留。
//     实测（360dp 视口，内容容器 326）：气泡卡在 244（= 326 - 82），右侧空 82px；
//     侧边栏展开时容器只有 254，气泡卡在 172，浪费 32%。
//     这里用 `max-width:100%` 而不是 `calc(var(--bt-msg-w)*100%)`：后者只放开 82px
//     仍保留 80%，气泡只到 260.8，用户抱怨的那条空白还在。100% 才是整条放开。
//     80% 那个上限本来是给桌面端长行限宽的，手机 326px 本身就不宽，没理由再打八折。
//     注意不要碰 `.bt-msg:not(:last-child)>.bt-msg-line{max-width:calc(100% - 30px)}`：
//     那是同一条消息里多行的缩进（data-lead），是 `100% - 30px` 不是 82px，放开气泡
//     宽度不会破坏它。
//
//  6. 把 DSH 自己的浮层从 Meerkat 顶栏下面让开（--dsh-frame-chrome-top）。
//     这是 DSH 留给宿主的第二个钩子，和 ds-bot 的 --dsh-frame-top-clearance 管的
//     不是同一批元素，两个都要设：
//     --dsh-frame-top-clearance → ds-bot 的折叠按钮与 .bt-leftcol 的 padding
//     --dsh-frame-chrome-top    → DSH 的图片查看器舞台 + Modal 遮罩
//     在 DSH 前端产物里这个变量有 2 处用途：
//     ._stage_…{position:absolute;inset:var(--dsh-frame-chrome-top,0px) 0 0}
//     ._mask_… {inset:var(--dsh-frame-chrome-top,0px) 0 0}
//     不设它时取默认 0：图片查看器的关闭按钮 `top:20px` 就落进 44px 顶栏里，和
//     Meerkat 的刷新按钮叠在一起（真机实测两者纵向重叠 21dp）；Modal 遮罩也会
//     从 y=0 开始，把顶栏一起盖住。
//     44px 必须与 Meerkat 浮动顶栏高度一致；以后顶栏高度变了，两个变量一起改。
//
// `:has()` 在这里可以放心用：DSH 自己的 client.js 里就有 67 处 `:has()`，
// 也就是说能在 Android WebView 里跑起来的 DSH 必然支持它（实测 WebView
// 156 / Android 16 上 CSS.supports('selector(:has(*))') 为 true）。
const dshMobileCSS = `
:root{--dsh-frame-top-clearance:44px;--dsh-frame-chrome-top:44px}
#root{padding-top:44px!important;box-sizing:border-box!important}
[data-shell-leading] button,[data-slot="shell.leading"] button{display:none!important}
@media (max-width:720px){
  html body #root [class*="_centerCol"]{margin-left:0!important;width:100%!important;max-width:100%!important;box-sizing:border-box}
  .bt-rail-top .bt-round,.bt-rail .bt-row,.bt-rail-folder,
  .bt-leftcol-foot-rail>:is(.bt-round,.bt-account),.bt-leftcol-foot-rail .bt-you{width:44px!important;height:44px!important;min-height:44px!important}
  .bt-rail .bt-agent-ava{width:36px!important;height:36px!important}
  .bt-round.bt-title-toggle{width:38px;height:38px;background:var(--bt-glass-bg);backdrop-filter:blur(12px)}
  header[class*="_header"] .bt-head{padding:0 56px!important;box-sizing:border-box}
  header[class*="_header"] .bt-pill{max-width:100%!important}
  .bt-leftcol-foot-rail{padding-bottom:env(safe-area-inset-bottom,0px)}
  body:has(>.bt-leftcol:not([data-leaving]))>#meerkat-sidebar-mask{display:block;position:fixed;inset:var(--dsh-frame-top-clearance) 0 0;z-index:24;border:0;padding:0;background:rgba(15,23,42,.28)}
}
@media (hover:none){
  .bt-msg,.bt-msg-line>.bt-bubble,.bt-bubble{max-width:100%!important}
}
`

// Run before the deferred DSH client reads its initial sidebar preference.
const mobileSidebar = `<script id="meerkat-mobile-sidebar">(() => {
  const narrow = matchMedia('(max-width:720px)');
  if (narrow.matches) {
    try {
      const marker = 'meerkat:mobile-sidebar-initialized';
      if (!sessionStorage.getItem(marker)) {
        const key = 'ds-bot:sidebar';
        let saved;
        try { saved = JSON.parse(localStorage.getItem(key)); } catch {}
        localStorage.setItem(key, JSON.stringify({ ...saved, pref: 'hidden',
          shown: 'rail',
          width: Number.isFinite(saved?.width) ? saved.width : 280 }));
        sessionStorage.setItem(marker, '1');
      }
    } catch {} // Storage may be unavailable in an embedded document.
  }
  document.addEventListener('DOMContentLoaded', () => {
    const mask = document.createElement('button');
    mask.id = 'meerkat-sidebar-mask';
    mask.type = 'button';
    mask.hidden = true; // The narrow-screen rule shows it only beside an active sidebar.
    mask.setAttribute('aria-label', '收起侧栏');
    document.body.append(mask);
    const collapse = () => document.querySelector('body > .bt-leftcol:not([data-leaving]) .bt-side-collapse')?.click();
    mask.addEventListener('click', collapse);
    document.addEventListener('click', event => {
      if (narrow.matches && event.target.closest?.('.bt-leftcol .bt-row, .bt-leftcol .bt-agent')) {
        setTimeout(collapse, 0); // Let ds-bot select the conversation first.
      }
    }, true);
  }, { once: true });
})();</script>`

var dshReleasePattern = regexp.MustCompile(`"release":\{"version":"([^"]+)","commit":"([^"]+)"\}`)

// DSHInjection describes what happened to one HTML response.
type DSHInjection struct {
	Body     []byte
	Injected bool
	Version  string
	Commit   string
	Reason   string
}

// InjectDSHMobileHTML adds the mobile stylesheet and the resume shim to a DSH
// HTML document. It is deliberately conservative: if the expected DSH markers
// are missing, or no </head> exists, it returns the original bytes untouched and
// sets Reason. The caller must fall back to the original response on any
// unexpected condition.
//
// The two injected nodes are independent: each is added only when its own id is
// absent. A page that already carries the stylesheet but not the shim (or the
// other way round) still gets the missing one, and a page with both is left
// untouched.
func InjectDSHMobileHTML(body []byte) DSHInjection {
	result := DSHInjection{Body: body, Reason: "not_dsh_html"}
	if !bytes.Contains(body, []byte(`id="root"`)) {
		return result
	}
	// .bt-leftcol is created by the DSH client at runtime, so it is not in
	// the initial HTML. Use the boot payload/title as the stable DSH marker.
	if !bytes.Contains(body, []byte("__DSH_BOOT__")) && !bytes.Contains(body, []byte("DeepSeek Harness")) {
		return result
	}
	if match := dshReleasePattern.FindSubmatch(body); len(match) == 3 {
		result.Version = string(match[1])
		result.Commit = string(match[2])
	}
	hasStyle := bytes.Contains(body, []byte(`id="meerkat-mobile"`))
	hasShim := bytes.Contains(body, []byte(`id="`+DSHResumeShimID+`"`))
	if hasStyle && hasShim {
		result.Reason = "already_injected"
		return result
	}
	idx := bytes.LastIndex(bytes.ToLower(body), []byte("</head>"))
	if idx < 0 {
		result.Reason = "missing_head"
		return result
	}
	var injected []byte
	if !hasStyle {
		injected = append(injected, []byte(`<style id="meerkat-mobile">`+dshMobileCSS+`</style>`)...)
		injected = append(injected, []byte(mobileSidebar)...)
	}
	if !hasShim {
		injected = append(injected, []byte(DSHResumeShim)...)
	}
	next := make([]byte, 0, len(body)+len(injected))
	next = append(next, body[:idx]...)
	next = append(next, injected...)
	next = append(next, body[idx:]...)
	result.Body = next
	result.Injected = true
	result.Reason = ""
	return result
}
