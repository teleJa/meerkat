package androidclient

import (
	"fmt"
	"time"
)

// DSH 页面在手机上的实时通道（api/remote.mux 的 WebSocket、plugins/events 的
// SSE）没有任何应用层心跳，两跳反代也只是原样转发字节。切后台、熄屏后 Doze
// 冻结进程、切 Wi-Fi/5G、NAT 映射过期都会让 TCP 变成半开：没有 FIN/RST，两边
// 都以为还连着，所以 DSH 永远不会重连，回到前台也收不到新消息。
//
// DSH 自己的恢复逻辑本来是好的，只是等不到那个 close：dsh-client-connection 的
// watchBrowserNetwork() 监听 window 的 online/offline，offline 会把状态置为不
// 可用并 abort 当前连接，online 会重置退避并立即重连。它唯一缺的是一个「回前台」
// 的信号。
//
// 方案 C（用户选定）：回到前台时在 DSH 页面里补一次 offline → online 事件派发。
// 不做 WS 心跳、不改反代、不动 DSH 上游包。
//
// 执行路径有个硬约束：Wails 的 WebviewWindow.ExecJS 只作用于顶层 frame，而 DSH
// 跑在外壳页面的跨源 iframe 里（https://wails.localhost 里的
// http://127.0.0.1:<port>）。父页面对 iframe 直接调 dispatchEvent 会被跨源策略
// 拦掉（真机实测 SecurityError: Blocked a frame ... from accessing a cross-origin
// frame），postMessage 则允许。
//
// 主触发在**页面内**：注入的 shim 自己监听 document 的 visibilitychange。真机
// 实测（iQOO 11 Pro / Android 16 / WebView 156）后台 90 秒再回来，iframe 里的
// visibilitychange 稳定触发，而 Go 侧 ExecJS 送进去的脚本在冻结恢复后被静默
// 丢弃——Go 那边只看到「已提交」，页面里一次都没收到。所以 ExecJS + postMessage
// 只作为短后台时的兜底路径保留，两条路径共用同一个页内节流。

// DSHResumeShimID 是注入脚本的 DOM id。它与样式表的 meerkat-mobile 各自独立
// 判幂等：任何一个已存在都不能让另一个被跳过或重复注入。
const DSHResumeShimID = "meerkat-resume-shim"

const (
	// DSHResumeWarmupMs 是页面加载完成后的静默期。刚加载完的页面本来就是新连接，
	// 没有半开可言；在它身上拆一次连接只会让首屏重新握手。
	DSHResumeWarmupMs = 10000
	// DSHResumeMinHiddenMs 是「离开多久才算真离开」的下限。下拉通知栏、切一下
	// 输入法、短暂切 App 都会触发 hidden → visible，时间通常不到 2 秒；5 秒能把
	// 这些噪音滤掉。真机实测的短后台是 20 秒（能救），长后台 75~180 秒（要救），
	// 两者都远大于这个门槛。
	DSHResumeMinHiddenMs = 5000
	// DSHResumeThrottleMs 是两条触发路径共用的最小间隔：visibilitychange 和
	// postMessage 都走同一个 lastFireAt，不各算各的，否则一次回前台可能拆两遍
	// 连接（每次都要重新握手、重新拉一次会话列表）。
	DSHResumeThrottleMs = 5000
)

// dshResumeMessage 是外壳页发给 DSH 页面的固定消息形状。带 meerkat 字段而不是
// 裸字符串，避免和 DSH 自己的 postMessage 流量混淆。
const dshResumeMessage = `{"meerkat":"resume"}`

// resumeShimTemplate 是注入到 DSH 页面内部的脚本。
//
// 主触发是 document 的 visibilitychange：回到可见且离开超过
// DSHResumeMinHiddenMs、页面已过 DSHResumeWarmupMs、距上次触发超过
// DSHResumeThrottleMs，才派发 offline → online。这三个守卫都必须在页内，因为
// 长时间后台之后 Go 侧根本送不进脚本，判断写在外面也没有机会执行。
//
// 兜底触发是 window 的 message：外壳页 postMessage 进来（短后台时可用），校验
// 来源必须是 window.parent、形状必须是 {meerkat:'resume'}，然后走同一个 fire()。
//
// online 派发两次（200ms、2s）：setNetworkAvailable(false) 之后 DSH 的循环停在
// 等待退避计时器上，只有 online 能唤醒它；200ms 那个 timer 如果因为页面被冻结或
// 脚本异常丢了，页面会永久停在离线，比改动前更糟。setNetworkAvailable(true) 在
// 状态已经是 true 时会 early return，重复派发没有副作用。
const resumeShimTemplate = `<script id="` + DSHResumeShimID + `">(function(){
  if (window.__meerkatResumeShim) return;
  window.__meerkatResumeShim = true;
  var WARMUP = %d, MIN_HIDDEN = %d, THROTTLE = %d;
  var startedAt = Date.now();
  var hiddenAt = 0;
  var lastFireAt = 0;
  if (document.visibilityState === 'hidden') hiddenAt = startedAt;
  function now() { return Date.now(); }
  // 真机上 logcat 拿不到这行：vivo 把全局 log.tag 设成 I，Android 会丢弃所有
  // Log.d（实测 log -p d 写不进去、setprop log.tag.WailsActivity D 之后才出现）。
  // 所以结果必须另走一条不依赖日志级别的通路：postMessage 给外壳页，外壳页
  // POST 回 Go，Go 写进 files/android-tsnet.log（run-as cat 就能读）。
  function report(result) {
    try { console.log('[meerkat] dsh-resume ' + result); } catch (e) {}
    try { window.parent.postMessage({ meerkat: 'resume-report', result: String(result) }, '*'); } catch (e) {}
    return result;
  }
  function tick() { try { window.dispatchEvent(new Event('online')); } catch (e) {} }
  function fire(source) {
    var t = now();
    if (t - startedAt < WARMUP) return report('skip=warming-up source=' + source);
    if (lastFireAt && t - lastFireAt < THROTTLE) return report('skip=throttled source=' + source);
    lastFireAt = t;
    try { window.dispatchEvent(new Event('offline')); } catch (e) {}
    setTimeout(tick, 200);
    setTimeout(tick, 2000);
    return report('sent source=' + source);
  }
  document.addEventListener('visibilitychange', function () {
    var state = document.visibilityState;
    if (state === 'hidden') { hiddenAt = now(); return; }
    if (state !== 'visible') return;
    if (!hiddenAt) return;
    var away = now() - hiddenAt;
    hiddenAt = 0;
    if (away < MIN_HIDDEN) {
      report('skip=short-away away=' + away + 'ms');
      return;
    }
    fire('visibility away=' + away + 'ms');
  });
  window.addEventListener('message', function (event) {
    if (event.source !== window.parent) return;
    var data = event.data;
    if (!data || typeof data !== 'object' || data.meerkat !== 'resume') return;
    fire('message');
  });
})()</script>`

// DSHResumeShim 由模板注入三个门槛值，Go 侧常量是唯一来源。
var DSHResumeShim = fmt.Sprintf(resumeShimTemplate, DSHResumeWarmupMs, DSHResumeMinHiddenMs, DSHResumeThrottleMs)

// DSHResumeDispatchJS 是交给外壳页执行的那段兜底脚本（短后台路径）。
//
// 页内守卫已经覆盖了「加载多久」「离开多久」「节流」这些判断，外壳页只做最基
// 本的两件事：确认现在真的有 DSH iframe，且它不是 about:blank（切设备中间态、
// 加载失败），然后把消息投进去。不再重复判 data-loaded-at——两处各判一次只会
// 让「到底是谁跳过的」更难查。
//
// 结果走两条路：console.log（会被 WebChromeClient 转 logcat，但真机上 vivo 把
// 全局 log.tag 设成 I，Log.d 被系统丢弃，这条路实际拿不到）+ 回传给外壳页由
// Go 写进日志文件（见 handleDSHLog）。后者不依赖日志级别，是真机上唯一可靠的
// 观察窗口。注意它只证明脚本被提交了；长时间后台时 WebView 会把这段执行整个
// 丢掉，那时只有页内 visibilitychange 那条路能生效。
const DSHResumeDispatchJS = `(function(){
  function report(result) {
    try { console.log('[meerkat] dsh-resume-fallback ' + result); } catch (e) {}
    // 这段本身就跑在外壳页里，所以能直接把结果送回去落盘（logcat 那条路在
    // vivo 上被 log.tag=I 吃掉了）。
    try {
      var bytes = new TextEncoder().encode('[fallback] ' + result);
      var binary = '';
      for (var i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]);
      var encoded = btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
      fetch('/client/dsh-log/' + encoded).catch(function () {});
    } catch (e) {}
    return result;
  }
  var frame = document.getElementById('dshFrame');
  if (!frame) return report('skip=no-frame');
  var device = document.body ? document.body.getAttribute('data-dsh-device') : '';
  if (!device) return report('skip=no-dsh-page');
  var src = frame.getAttribute('src') || '';
  if (!src || src === 'about:blank') return report('skip=frame-idle');
  var target = frame.contentWindow;
  if (!target) return report('skip=no-content-window');
  try {
    target.postMessage(` + dshResumeMessage + `, '*');
  } catch (e) {
    return report('error=' + ((e && e.name) || 'unknown'));
  }
  return report('posted device=' + device);
})()`

// DSHResumeThrottle 是 Go 侧对兜底路径的节流。
//
// 页内已经有自己的节流（两条路径共用），这一层只减少无谓的 ExecJS 提交：一次
// 回前台往往同时带来 ActivityResumed 和 NetworkChanged，5 秒足够把它们合并。
// 两层的值一致只是巧合，各管各的：一个是省掉多余的 JS 注入，一个是真正拦住重复
// 拆连接。
type DSHResumeThrottle struct {
	// MinInterval 为零时按 DSHResumeThrottleMs 处理。
	MinInterval time.Duration
	last        time.Time
}

// Allow 报告这次是否该触发；返回 true 时已经记下这次时间。
func (t *DSHResumeThrottle) Allow(now time.Time) bool {
	interval := t.MinInterval
	if interval <= 0 {
		interval = time.Duration(DSHResumeThrottleMs) * time.Millisecond
	}
	if !t.last.IsZero() && now.Sub(t.last) < interval {
		return false
	}
	t.last = now
	return true
}

// DSHResumeTriggerForNetwork 判断一次网络变化是否该触发重连。
//
// 只在切到可用网络时触发：断网时触发没有意义，还要白拆一次已经断掉的连接。
// connected 来自 android:NetworkChanged 事件的 payload（MainActivity 的
// emitNetwork 写入）。
func DSHResumeTriggerForNetwork(connected bool) bool {
	return connected
}
