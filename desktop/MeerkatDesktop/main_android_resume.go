//go:build android

package main

import (
	"log"
	"time"

	"meerkat/desktop/androidclient"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// 回前台后让 DSH 自己重连（方案 C）。
//
// 手机上 DSH 的实时通道没有心跳，TCP 半开时 DSH 永远等不到那个 close，于是回到
// 前台也收不到新消息。DSH 自己的恢复逻辑是好的——它监听 window 的 online/offline
// 并据此 abort 旧连接、重置退避重连——缺的只是一个「回前台」的信号。这里就是在
// 回前台时把那个信号补上。
//
// 三个触发点都是 Wails v3 已经转发好的 application 事件，不需要改 build/android
// 下的 Java（那是生成资产，升级会覆盖）：
//
//	ActivityResumed  MainActivity.onResume → nativeOnResume
//	ScreenUnlocked   熄灭屏幕后解锁回来
//	NetworkChanged   MainActivity.emitNetwork，只在切到可用网络时触发
//
// 用户报的现象是「切后台/熄屏回来」，ScreenUnlocked 直接对得上；切 Wi-Fi/5G 是
// 另一个高发场景。三个触发点共享同一个 5 秒节流，避免一次回前台被派发三次。

// androidResumeThrottle 是三个触发点共享的最小间隔。取 5 秒：一次回前台通常同时
// 带来 ActivityResumed 和（熄屏期间网络变过的）NetworkChanged，两者相差不到 1 秒，
// 5 秒能把它们合并成一次；用户主动切出去再回来通常超过 5 秒，不会被误吞。
const androidResumeThrottle = 5 * time.Second

// attachResumeReconnect 订阅回前台相关的 application 事件。
//
// 传 nil window 时不挂任何监听（窗口还没建好，挂了也没有执行目标）。
func attachResumeReconnect(c *androidClient, window *application.WebviewWindow) {
	if window == nil {
		return
	}
	c.window = window

	// 所有触发点共用一份节流状态，所以放在这里而不是每个回调各自持有。
	throttle := &androidclient.DSHResumeThrottle{MinInterval: androidResumeThrottle}

	trigger := func(source string) {
		if !throttle.Allow(time.Now()) {
			c.logf("resume: %s 被节流跳过（%.0fs 内已触发过）", source, androidResumeThrottle.Seconds())
			return
		}
		c.logf("resume: %s → 触发 DSH 重连", source)
		window.ExecJS(androidclient.DSHResumeDispatchJS)
		c.logf("resume: %s → 已提交 JS（结果见 logcat 的 [meerkat] dsh-resume）", source)
	}

	application.Get().Event.OnApplicationEvent(events.Android.ActivityResumed, func(*application.ApplicationEvent) {
		trigger("activity-resumed")
	})
	application.Get().Event.OnApplicationEvent(events.Android.ScreenUnlocked, func(*application.ApplicationEvent) {
		trigger("screen-unlocked")
	})
	application.Get().Event.OnApplicationEvent(events.Android.NetworkChanged, func(event *application.ApplicationEvent) {
		// 只在切到可用网络时触发：断网时触发没有意义，还要白拆一次已经断掉的连接。
		connected := false
		if ctx := event.Context(); ctx != nil {
			if value, ok := ctx.Data()["connected"].(bool); ok {
				connected = value
			}
		}
		if !androidclient.DSHResumeTriggerForNetwork(connected) {
			c.logf("resume: network-changed 已断开，跳过")
			return
		}
		trigger("network-changed")
	})
}

// logf 在 client 还没起来时退化成标准日志，保证窗口期的事件也有痕迹。
func (c *androidClient) logf(format string, args ...any) {
	if c.client != nil {
		c.client.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}
