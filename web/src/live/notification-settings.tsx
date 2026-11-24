import { useCallback, useEffect, useState } from "react";
import { Bell, BellRing, Check, Loader2, RefreshCw, ShieldAlert } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
  desktopNotificationPermission,
  liveApi,
  requestDesktopNotificationPermission,
  type NotificationPermissionStatus,
  type TurnNotificationSettings,
} from "@/live/api";
import { cn } from "@/lib/utils";

const DEFAULT_SETTINGS: TurnNotificationSettings = {
  enabled: true,
  task_pool_enabled: true,
  bot_events_enabled: true,
  turn_events_enabled: true,
  group_decisions_enabled: true,
  approval_bypass: false,

  quiet_hours_enabled: true,
  quiet_start: "22:00",
  quiet_end: "08:00",
};

function permissionLabel(status: NotificationPermissionStatus) {
  switch (status) {
    case "granted":
      return "已授权";
    case "denied":
      return "未授权";
    default:
      return "仅 App 内可用";
  }
}

function permissionTone(status: NotificationPermissionStatus) {
  if (status === "granted") return "border-emerald-200 bg-emerald-50 text-emerald-700";
  if (status === "denied") return "border-amber-200 bg-amber-50 text-amber-700";
  return "border-border bg-muted text-muted-foreground";
}

export function TurnNotificationsSection() {
  const [settings, setSettings] = useState<TurnNotificationSettings>(DEFAULT_SETTINGS);
  const [permission, setPermission] = useState<NotificationPermissionStatus>("unavailable");
  const [supported, setSupported] = useState(false);
  const [loading, setLoading] = useState(true);
  const [hubAvailable, setHubAvailable] = useState(false);
  const [saving, setSaving] = useState(false);
  const [requesting, setRequesting] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setError("");
    const local = desktopNotificationPermission().then(({ status, supported }) => {
      setPermission(status);
      setSupported(supported);
    });
    const hub = liveApi.notificationSettings().then(next => {
      setSettings(next);
      setHubAvailable(true);
    }).catch(loadError => {
      setHubAvailable(false);
      setError(loadError instanceof Error ? loadError.message : "读取通知设置失败");
    }).finally(() => setLoading(false));
    await Promise.all([local, hub]);
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 15000);
    return () => window.clearInterval(timer);
  }, [load]);

  const save = async (next: TurnNotificationSettings) => {
    setSaving(true);
    setNotice("");
    try {
      const saved = await liveApi.updateNotificationSettings(next);
      setSettings(saved);
      setNotice("已保存");
    } catch (saveError) {
      setError(saveError instanceof Error ? saveError.message : "保存失败");
    } finally {
      setSaving(false);
    }
  };

  const toggle = (patch: Partial<TurnNotificationSettings>) => {
    const next = { ...settings, ...patch };
    setSettings(next);
    void save(next);
  };

  const sendTest = async () => {
    setError(""); setNotice("");
    try {
      const response = await fetch("/desktop/notifications/test", { method: "POST", headers: { "x-meerkat-request": "1" } });
      if (!response.ok) throw new Error("发送测试通知失败，请检查系统通知权限");
      setNotice("测试通知已发送");
    } catch (error) { setError(error instanceof Error ? error.message : "发送失败"); }
  };

  const requestPermission = async () => {
    setRequesting(true);
    try {
      const status = await requestDesktopNotificationPermission();
      setPermission(status);
      setNotice(status === "granted" ? "通知权限已开启" : "系统未授权，可在系统设置中开启 Meerkat 通知");
    } finally {
      setRequesting(false);
    }
  };


  return (
    <section data-testid="turn-notifications" className="overflow-hidden rounded-xl border bg-card">
      <div className="flex flex-wrap items-start justify-between gap-3 border-b px-5 py-4">
        <div>
          <div className="flex items-center gap-2">
            <BellRing className="size-4 text-muted-foreground" />
            <h2 className="text-sm font-semibold">桌面通知</h2>
            <Badge variant="secondary" className="text-[10px]">
              Hub 通知
            </Badge>
          </div>
          <p className="mt-1 max-w-2xl text-xs leading-relaxed text-muted-foreground">
            选择哪些设备事件需要系统提醒。免打扰按接收设备的本地时间生效。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <span
            data-testid="notification-permission"
            className={cn("rounded-md border px-2 py-1 text-[10px] font-medium", permissionTone(permission))}
          >
            通知权限：{permissionLabel(permission)}
          </span>
          <Button
            data-testid="notification-refresh"
            size="sm"
            variant="outline"
            className="h-7 gap-1 px-2 text-[11px]"
            onClick={() => void load()}
          >
            <RefreshCw className={cn("size-3", loading && "animate-spin")} />
            刷新
          </Button>
        </div>
      </div>

      <div className="grid gap-px bg-border sm:grid-cols-2">
        <div className="bg-card px-5 py-3">
          <div className="flex items-center justify-between gap-3">
            <div>
              <div className="text-xs font-medium">通知总开关</div>
              <div className="mt-0.5 text-[11px] text-muted-foreground">控制所有分类</div>
            </div>
            <Switch
              aria-label="通知总开关" disabled={!hubAvailable || loading || saving} data-testid="notification-enabled"
              checked={settings.enabled}
              onCheckedChange={(checked) => toggle({ enabled: checked })}
            />
          </div>
        </div>
        <div className="bg-card px-5 py-3">
          <div className="flex items-center justify-between gap-3">
            <div>
              <div className="text-xs font-medium">免打扰时段</div>
              <div className="mt-0.5 text-[11px] text-muted-foreground">时段内不弹系统通知</div>
            </div>
            <Switch
              aria-label="免打扰时段" disabled={!hubAvailable || loading || saving} data-testid="notification-quiet-enabled"
              checked={settings.quiet_hours_enabled}
              onCheckedChange={(checked) => toggle({ quiet_hours_enabled: checked })}
            />
          </div>
          <div className="mt-2 flex items-center gap-2">
            <input
              data-testid="notification-quiet-start"
              type="time"
              value={settings.quiet_start}
              disabled={!hubAvailable || saving || !settings.quiet_hours_enabled}
              onChange={(event) => setSettings((current) => ({ ...current, quiet_start: event.target.value }))}
              onBlur={() => void save(settings)}
              className="h-7 rounded-md border bg-background px-2 font-mono text-[11px] disabled:opacity-50"
            />
            <span className="text-[11px] text-muted-foreground">至</span>
            <input
              data-testid="notification-quiet-end"
              type="time"
              value={settings.quiet_end}
              disabled={!hubAvailable || saving || !settings.quiet_hours_enabled}
              onChange={(event) => setSettings((current) => ({ ...current, quiet_end: event.target.value }))}
              onBlur={() => void save(settings)}
              className="h-7 rounded-md border bg-background px-2 font-mono text-[11px] disabled:opacity-50"
            />
            {saving ? <Loader2 className="size-3 animate-spin text-muted-foreground" /> : null}
          </div>
        </div>
      </div>

      <div className="grid gap-px bg-border sm:grid-cols-2">
        {([["task_pool_enabled","任务池状态"],["bot_events_enabled","智能体消息与审批"],["turn_events_enabled","DSH 回合状态"],["group_decisions_enabled","群内待决策提醒（语义识别）"],["approval_bypass","审批穿透免打扰"]] as const).map(([key,label]) => (
          <div key={key} className="flex items-center justify-between gap-3 bg-card px-5 py-3 text-xs">
            <label htmlFor={key}>{label}</label>
            <Switch id={key} checked={settings[key]} disabled={!hubAvailable || loading || saving} onCheckedChange={value => toggle({[key]:value})} />
          </div>
        ))}
      </div>
      {supported && <Button variant="outline" className="m-3" disabled={permission !== "granted"} onClick={() => void sendTest()}>发送测试通知</Button>}
      {!supported ? (
        <div className="border-t bg-muted/30 px-5 py-3 text-[11px] text-muted-foreground">
          当前页面不在 Meerkat App 内运行，桌面通知不可用。
        </div>
      ) : permission === "granted" ? null : (
        <div className="flex flex-wrap items-center gap-2 border-t border-amber-200 bg-amber-50/60 px-5 py-3">
          <ShieldAlert className="size-3.5 text-amber-600" />
          <span className="text-[11px] text-amber-900">
            系统未授予通知权限，回合结束时不会弹通知。
          </span>
          <Button
            data-testid="notification-request-permission"
            size="sm"
            className="ml-auto h-7 gap-1 px-2 text-[11px]"
            disabled={requesting}
            onClick={() => void requestPermission()}
          >
            {requesting ? <Loader2 className="size-3 animate-spin" /> : <Bell className="size-3" />}
            请求权限
          </Button>
        </div>
      )}

      {notice ? (
        <div role="status" className="flex items-center gap-1.5 border-t bg-emerald-50/50 px-5 py-2 text-[11px] text-emerald-700">
          <Check className="size-3" />
          {notice}
        </div>
      ) : null}
      {error ? (
        <div className="border-t bg-rose-50/60 px-5 py-2 text-[11px] text-rose-700">{error}</div>
      ) : null}
    </section>
  );
}
