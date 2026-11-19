package androidclient

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 注入的两个节点各自独立判幂等：任何一个已存在都不能让另一个被跳过或重复注入。
// 四种组合都覆盖，因为这正是「分批上线」和「缓存了旧注入」两种真实情况会撞到的。
//
// 断言用计数而不是布尔：用例里预置的那个节点本身也会被 Contains 命中，只有数它
// 在结果里出现的次数才能分辨「原本就有」和「又被加了一份」。
func TestInjectDSHMobileHTMLTrimsResumeShimIndependently(t *testing.T) {
	base := dshHTML()
	styleTag := `<style id="meerkat-mobile">x</style>`
	shimTag := `<script id="` + DSHResumeShimID + `">x</script>`
	cases := []struct {
		name       string
		body       string
		wantStyle  int
		wantShim   int
		wantReason string
	}{
		{name: "neither", body: base, wantStyle: 1, wantShim: 1},
		{
			name:      "style only",
			body:      strings.Replace(base, "</head>", styleTag+"</head>", 1),
			wantStyle: 1, // 原本就有，不该再补
			wantShim:  1, // 缺的那个要补上
		},
		{
			name:      "shim only",
			body:      strings.Replace(base, "</head>", shimTag+"</head>", 1),
			wantStyle: 1,
			wantShim:  1,
		},
		{
			name:       "both",
			body:       strings.Replace(base, "</head>", styleTag+shimTag+"</head>", 1),
			wantStyle:  1,
			wantShim:   1,
			wantReason: "already_injected",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := InjectDSHMobileHTML([]byte(tc.body))
			text := string(out.Body)
			if tc.wantReason != "" {
				if out.Injected {
					t.Fatal("两个节点都在时不该再注入")
				}
				if out.Reason != tc.wantReason {
					t.Fatalf("reason=%q want %q", out.Reason, tc.wantReason)
				}
				if text != tc.body {
					t.Fatal("already_injected 时正文不该变")
				}
				return
			}
			if !out.Injected {
				t.Fatalf("expected injection, reason=%s", out.Reason)
			}
			if n := strings.Count(text, `id="meerkat-mobile"`); n != tc.wantStyle {
				t.Fatalf("style 出现 %d 次 want %d（重复注入或多补一份）", n, tc.wantStyle)
			}
			if n := strings.Count(text, `id="`+DSHResumeShimID+`"`); n != tc.wantShim {
				t.Fatalf("shim 出现 %d 次 want %d（重复注入或多补一份）", n, tc.wantShim)
			}
		})
	}
}

// 两次连续注入必须稳定：第二次既不改正文也不再报告注入。
func TestInjectDSHMobileHTMLResumeShimStable(t *testing.T) {
	first := InjectDSHMobileHTML([]byte(dshHTML()))
	if !first.Injected {
		t.Fatalf("first injection failed: %s", first.Reason)
	}
	second := InjectDSHMobileHTML(first.Body)
	if second.Injected {
		t.Fatal("second injection should be skipped")
	}
	if string(second.Body) != string(first.Body) {
		t.Fatal("body changed on already-injected fallback")
	}
}

// 三个门槛值必须真的写进注入脚本：模板里的 %d 一旦漏填，页内守卫会变成 NaN
// 比较，全部门槛静默失效。
func TestDSHResumeShimCarriesThresholds(t *testing.T) {
	for name, want := range map[string]string{
		"warmup":     "WARMUP = 10000",
		"min hidden": "MIN_HIDDEN = 5000",
		"throttle":   "THROTTLE = 5000",
	} {
		if !strings.Contains(DSHResumeShim, want) {
			t.Fatalf("%s 门槛没写进脚本，期望含 %q", name, want)
		}
	}
	if strings.Contains(DSHResumeShim, "%d") {
		t.Fatal("模板占位符没替换")
	}
}

// 注入脚本的结构性断言：主触发必须挂在 visibilitychange 上（真机实测这条不受
// 冻结影响），postMessage 只作兜底，且来源与形状都要校验。
func TestDSHResumeShimStructure(t *testing.T) {
	for _, want := range []string{
		"document.addEventListener('visibilitychange'",
		"document.visibilityState",
		"event.source !== window.parent", // 兜底路径的来源校验
		`data.meerkat !== 'resume'`,      // 固定消息形状
		`new Event('offline')`,
		`new Event('online')`,
		"2000",                       // 补一次 online，防卡在离线
		"window.__meerkatResumeShim", // 页内防重复安装
		"lastFireAt",                 // 两条路径共用的节流状态
	} {
		if !strings.Contains(DSHResumeShim, want) {
			t.Fatalf("注入脚本缺少 %q", want)
		}
	}
}

// 注入脚本和兜底脚本都要是合法 JS——它们跑在真机上，语法错就整段静默失效。
func TestDSHResumeScriptsParse(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("未安装 node，跳过 JS 语法检查")
	}
	for name, code := range map[string]string{
		"shim":     extractScriptBody(t, DSHResumeShim),
		"fallback": DSHResumeDispatchJS,
	} {
		file := filepath.Join(t.TempDir(), name+".js")
		if err := os.WriteFile(file, []byte(code), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(nodePath, "--check", file).CombinedOutput(); err != nil {
			t.Fatalf("%s 脚本语法错误: %v\n%s", name, err, out)
		}
	}
}

// extractScriptBody 取出 <script …> 与 </script> 之间的代码。
func extractScriptBody(t *testing.T, tag string) string {
	t.Helper()
	start := strings.Index(tag, ">")
	end := strings.LastIndex(tag, "</script>")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("script 结构异常: %q", tag)
	}
	return tag[start+1 : end]
}

// 页内守卫的行为验证：用 node + 假 DOM 跑真实注入脚本，按一条时间轴走完
// 整个生命周期。
//
// 这条不能用「grep 脚本里有没有关键字」代替：五个分支（预热期、短暂离开、
// 长时间离开、共享节流、来源/形状校验）的差别全在时间比较上，只有真跑才能
// 分辨。真机上这些分支判错的表现是「该重连没重连」，日志里看不出来。
func TestDSHResumeShimBehavior(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("未安装 node，跳过 JS 行为检查")
	}

	harness := `
const logs = [];
const events = [];
const timers = [];
const docListeners = {};
const winListeners = {};
const reports = [];
const parentSentinel = { __parent: true, postMessage(msg) { reports.push(msg); } };

global.window = {
  parent: parentSentinel,
  addEventListener(type, fn) { (winListeners[type] = winListeners[type] || []).push(fn); },
  dispatchEvent(e) { events.push(e.type); return true; },
};
global.document = {
  visibilityState: 'visible',
  addEventListener(type, fn) { (docListeners[type] = docListeners[type] || []).push(fn); },
};
const realLog = console.log;
console.log = (line) => logs.push(String(line));
const realSetTimeout = setTimeout;
global.setTimeout = (fn, ms) => { timers.push({ fn, ms }); return timers.length; };
const clock = { t: 0 };
Date.now = () => clock.t;

// ---- 被测脚本 ----
` + extractScriptBody(t, DSHResumeShim) + `
// -----------------

function setVisibility(state) {
  global.document.visibilityState = state;
  (docListeners['visibilitychange'] || []).forEach((fn) => fn());
}
function post(source, data) {
  (winListeners['message'] || []).forEach((fn) => fn({ source, data }));
}
function logCount() { return logs.length; }
function lastLogs(n) { return logs.slice(logs.length - n); }

const steps = [];
function step(name, at, fn) {
  clock.t = at;
  const before = logs.length;
  fn();
  steps.push({ name, at, logs: logs.slice(before) });
}

step('message-before-warmup', 3000, () => post(parentSentinel, { meerkat: 'resume' }));
step('hide-short', 12000, () => setVisibility('hidden'));
step('show-short', 16000, () => setVisibility('visible'));
step('hide-long', 17000, () => setVisibility('hidden'));
step('show-long', 37000, () => setVisibility('visible'));
step('message-throttled', 38000, () => post(parentSentinel, { meerkat: 'resume' }));
step('message-after-throttle', 43000, () => post(parentSentinel, { meerkat: 'resume' }));
step('message-wrong-source', 44000, () => post({ __other: true }, { meerkat: 'resume' }));
step('message-wrong-shape', 45000, () => post(parentSentinel, { meerkat: 'other' }));
step('message-throttled-again', 46000, () => post(parentSentinel, { meerkat: 'resume' }));

// 跑掉第一次 fire 排的两个 online timer，确认防卡在离线的补发真的存在。
const timersBefore = timers.map((x) => x.ms);
timers.forEach((x) => x.fn());

process.stdout.write(JSON.stringify({
  steps,
  events,
  timerDelays: timersBefore,
  finalLogs: logs,
  reports,
}));
`

	file := filepath.Join(t.TempDir(), "shim_behavior.js")
	if err := os.WriteFile(file, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(nodePath, file).CombinedOutput()
	if err != nil {
		t.Fatalf("跑注入脚本失败: %v\n%s", err, out)
	}

	var result struct {
		Steps []struct {
			Name string
			At   int64
			Logs []string
		}
		Events      []string
		TimerDelays []int
		Reports     []struct {
			Meerkat string `json:"meerkat"`
			Result  string `json:"result"`
		}
	}
	text := string(out)
	start := strings.Index(text, "{")
	if start < 0 {
		t.Fatalf("没有 JSON 输出：%s", text)
	}
	if err := json.Unmarshal([]byte(text[start:]), &result); err != nil {
		t.Fatalf("解析结果失败: %v\n%s", err, text)
	}

	logOf := map[string]string{}
	for _, s := range result.Steps {
		logOf[s.Name] = strings.Join(s.Logs, " | ")
	}

	// 1) 预热期：页面刚起来 10 秒内的触发一律跳过。
	if got := logOf["message-before-warmup"]; !strings.Contains(got, "skip=warming-up") {
		t.Fatalf("预热期内应跳过，实际日志：%q", got)
	}
	// 2) 短暂离开（4 秒 < 5 秒门槛）不触发，并且理由要能区分出来。
	if got := logOf["show-short"]; !strings.Contains(got, "skip=short-away") {
		t.Fatalf("短暂离开应跳过，实际日志：%q", got)
	}
	// 3) 长时间离开（20 秒）要真的派发。
	if got := logOf["show-long"]; !strings.Contains(got, "sent source=visibility") {
		t.Fatalf("长时间离开应派发，实际日志：%q", got)
	}
	// 4) 紧随其后的 postMessage 被同一个节流拦住（两条路径共用状态）。
	if got := logOf["message-throttled"]; !strings.Contains(got, "skip=throttled") {
		t.Fatalf("节流期内应跳过，实际日志：%q", got)
	}
	// 5) 过掉节流后，兜底路径自己也能派发。
	if got := logOf["message-after-throttle"]; !strings.Contains(got, "sent source=message") {
		t.Fatalf("节流过后兜底路径应能派发，实际日志：%q", got)
	}
	// 6) 来源不对、形状不对的消息必须完全静默（连一行日志都不留）。
	for _, name := range []string{"message-wrong-source", "message-wrong-shape"} {
		if got := logOf[name]; got != "" {
			t.Fatalf("%s 不该有任何日志，实际：%q", name, got)
		}
	}

	// 7) 两次真正派发 = 两次 offline；online 靠 timer 补发，脚本里各排了
	// 200ms 和 2000ms 两档，两次派发一共四个 timer。
	offs := 0
	for _, e := range result.Events {
		if e == "offline" {
			offs++
		}
	}
	if offs != 2 {
		t.Fatalf("offline 应派发 2 次（短离开那次不算），实际 %d 次：%v", offs, result.Events)
	}
	onlines := 0
	for _, e := range result.Events {
		if e == "online" {
			onlines++
		}
	}
	if onlines != 4 {
		t.Fatalf("online 应派发 4 次（两次触发 × 200ms/2s 各一次），实际 %d 次：%v", onlines, result.Events)
	}
	wantDelays := []int{200, 2000, 200, 2000}
	if len(result.TimerDelays) != len(wantDelays) {
		t.Fatalf("timer 数量不对：%v", result.TimerDelays)
	}
	for i, want := range wantDelays {
		if result.TimerDelays[i] != want {
			t.Fatalf("第 %d 个 timer 延时 %dms，期望 %dms（完整：%v）", i, result.TimerDelays[i], want, result.TimerDelays)
		}
	}

	// 8) 触发结果必须回传给外壳页——真机上 logcat 拿不到 console.log（vivo 把
	// 全局 log.tag 设成 I，Log.d 被系统丢弃），这是唯一能落盘的观察窗口。
	// 跳过分支也要报：真机上「到底跳过了还是发了」同样要能看见，否则分不清
	// 是守卫拦住了还是 shim 根本没跑。
	if len(result.Reports) != 6 {
		t.Fatalf("应回传 6 条报告，实际 %d 条：%v", len(result.Reports), result.Reports)
	}
	wantReports := []string{
		"skip=warming-up source=message",
		"skip=short-away away=4000ms",
		"sent source=visibility away=20000ms",
		"skip=throttled source=message",
		"sent source=message",
		"skip=throttled source=message",
	}
	for i, want := range wantReports {
		if result.Reports[i].Meerkat != "resume-report" {
			t.Fatalf("第 %d 条回传消息形状不对：%q", i, result.Reports[i].Meerkat)
		}
		if result.Reports[i].Result != want {
			t.Fatalf("第 %d 条回传 %q，期望 %q", i, result.Reports[i].Result, want)
		}
	}
	// 来源/形状不对的消息必须连报告都没有（与「完全静默」一致）。
	for _, report := range result.Reports {
		if strings.Contains(report.Result, "source=unknown") {
			t.Fatalf("来源/形状不对的消息不该产生报告：%v", result.Reports)
		}
	}
}

// 外壳页那段兜底脚本只判「有没有真的 DSH 页面」，页内守卫已经管住了时间和节流。
func TestDSHResumeDispatchJSBranches(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("未安装 node，跳过 JS 行为检查")
	}

	harness := `
const results = [];
function run(name, setup) {
  const posted = [];
  const frame = {
    _attrs: Object.assign({ src: '' }, setup.attrs || {}),
    getAttribute(k) { return k in this._attrs ? this._attrs[k] : null; },
    contentWindow: { postMessage(msg, origin) { posted.push([msg, origin]); } },
  };
  if (setup.noFrame) {
    global.document = { getElementById() { return null; }, body: { getAttribute: () => setup.device || '' } };
  } else {
    global.document = {
      getElementById(id) { return id === 'dshFrame' ? frame : null; },
      body: { getAttribute: (k) => (k === 'data-dsh-device' ? (setup.device || '') : '') },
    };
  }
  const logs = [];
  const realLog = console.log;
  console.log = (line) => logs.push(String(line));
  eval(process.env.DISPATCH_JS);
  console.log = realLog;
  results.push({ name, posted, logs });
}

run('ok', { device: 'dev_1', attrs: { src: 'http://127.0.0.1:1/harness-proxy/dev_1/' } });
run('no-dsh-page', { device: '', attrs: { src: 'http://127.0.0.1:1/x' } });
run('frame-idle', { device: 'dev_1', attrs: { src: 'about:blank' } });
run('empty-src', { device: 'dev_1', attrs: { src: '' } });
run('no-frame', { noFrame: true, device: 'dev_1' });

process.stdout.write(JSON.stringify(results));
`

	file := filepath.Join(t.TempDir(), "dispatch_behavior.js")
	if err := os.WriteFile(file, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(nodePath, file)
	cmd.Env = append(os.Environ(), "DISPATCH_JS="+DSHResumeDispatchJS)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("跑兜底脚本失败: %v\n%s", err, out)
	}

	var results []struct {
		Name   string
		Posted [][2]json.RawMessage
		Logs   []string
	}
	text := string(out)
	start := strings.Index(text, "[")
	if start < 0 {
		t.Fatalf("没有 JSON 输出：%s", text)
	}
	if err := json.Unmarshal([]byte(text[start:]), &results); err != nil {
		t.Fatalf("解析结果失败: %v\n%s", err, text)
	}

	byName := map[string]struct {
		Posted [][2]json.RawMessage
		Logs   []string
	}{}
	for _, r := range results {
		byName[r.Name] = struct {
			Posted [][2]json.RawMessage
			Logs   []string
		}{r.Posted, r.Logs}
	}

	ok, found := byName["ok"]
	if !found {
		t.Fatal("缺少 ok 用例")
	}
	if len(ok.Posted) != 1 {
		t.Fatalf("ok 应投递 1 次，实际 %d 次", len(ok.Posted))
	}
	var message map[string]any
	if err := json.Unmarshal(ok.Posted[0][0], &message); err != nil {
		t.Fatalf("消息不是对象: %v", err)
	}
	if message["meerkat"] != "resume" {
		t.Fatalf("消息形状不对: %s", ok.Posted[0][0])
	}
	var targetOrigin string
	if err := json.Unmarshal(ok.Posted[0][1], &targetOrigin); err != nil {
		t.Fatal(err)
	}
	if targetOrigin != "*" {
		t.Fatalf("targetOrigin 应为 *（跨源无法算出精确 origin），实际 %q", targetOrigin)
	}

	for name, wantLog := range map[string]string{
		"no-dsh-page": "skip=no-dsh-page",
		"frame-idle":  "skip=frame-idle",
		"empty-src":   "skip=frame-idle",
		"no-frame":    "skip=no-frame",
	} {
		r, found := byName[name]
		if !found {
			t.Fatalf("缺少 %s 用例", name)
		}
		if len(r.Posted) != 0 {
			t.Fatalf("%s 不该投递，实际 %d 次", name, len(r.Posted))
		}
		if !strings.Contains(strings.Join(r.Logs, "\n"), wantLog) {
			t.Fatalf("%s 应记录 %q，实际日志：%v", name, wantLog, r.Logs)
		}
	}
}

// 节流：一次回前台往往同时带来多个 Go 侧事件，5 秒内只提交一次 ExecJS。
func TestDSHResumeThrottle(t *testing.T) {
	throttle := &DSHResumeThrottle{MinInterval: 5 * time.Second}
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	if !throttle.Allow(now) {
		t.Fatal("第一次应放行")
	}
	if throttle.Allow(now.Add(200 * time.Millisecond)) {
		t.Fatal("同一批的第二个事件应被节流")
	}
	if throttle.Allow(now.Add(4 * time.Second)) {
		t.Fatal("5 秒内应继续节流")
	}
	if !throttle.Allow(now.Add(5*time.Second + time.Millisecond)) {
		t.Fatal("超过 5 秒应放行")
	}
}

// MinInterval 为零时退化成默认值，不能变成「每次都放行」。
func TestDSHResumeThrottleDefaultInterval(t *testing.T) {
	throttle := &DSHResumeThrottle{}
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	if !throttle.Allow(now) {
		t.Fatal("第一次应放行")
	}
	if throttle.Allow(now.Add(time.Second)) {
		t.Fatal("默认间隔 5 秒，1 秒内应被节流")
	}
	if !throttle.Allow(now.Add(6 * time.Second)) {
		t.Fatal("超过默认间隔应放行")
	}
}

// 网络变化的触发口径：只有切到可用网络才触发，断网不触发。
func TestDSHResumeTriggerForNetwork(t *testing.T) {
	if !DSHResumeTriggerForNetwork(true) {
		t.Fatal("连上网络应触发")
	}
	if DSHResumeTriggerForNetwork(false) {
		t.Fatal("断网不该触发")
	}
}
