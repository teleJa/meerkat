import { useCallback, useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useSearchParams } from "react-router-dom";
import {
  AlertTriangle,
  Bell,
  ArrowRight,
  Check,
  Copy,
  ExternalLink,
  Globe2,
  KeyRound,
  Laptop,
  Link2,
  Loader2,
  Network,
  QrCode as QrCodeIcon,
  RefreshCw,
  Server,
  ShieldCheck,
  Smartphone,
  Trash2,
} from "lucide-react";
import { QrCode as QrCodeImage } from "@/components/qr-code";
import { liveApi, type MobileCredential, type MobilePairingCode } from "@/live/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { PageHeader } from "@/components/page-header";
import { openExternal } from "@/lib/open-external";
import { openJoinWorkspace } from "@/lib/open-setup";
import { TurnNotificationsSection } from "@/live/notification-settings";
import { focusAboutMeerkat, UpdateSettings } from "@/live/update-settings";
import { cn } from "@/lib/utils";

type TSNetStatus = {
  enabled: boolean;
  state: string;
  auth_url?: string;
  hostname?: string;
  tailscale_ips?: string[];
  hub_url?: string;
  port?: number;
  error?: string;
};

const EMPTY: TSNetStatus = { enabled: false, state: "off" };
type EdgeStatus = TSNetStatus & {
  listen_port?: number;
  paired?: boolean;
  device_id?: string;
  pairing?: { phase?: string; error?: string };
};
type DesktopHealth = {
  mode?: string;
  center_url?: string;
  hub_url?: string;
  center?: string;
  edge?: string;
  local_device_id?: string;
};

function modeLabel(mode: string | undefined) {
  if (mode === "bundled") return "Hub";
  if (mode === "node") return "Node";
  return mode || "未知";
}

function roleBadgeLabel(mode: string | undefined) {
  if (mode === "bundled") return "Hub 节点";
  if (mode === "node") return "Edge 节点";
  return "Meerkat";
}

function networkDependency(mode: string | undefined, edge: EdgeStatus) {
  if (edge.enabled && edge.state === "running") return "Edge 内置 tsnet，不依赖系统 Tailscale";
  if (mode === "bundled") return "Hub 内置 tsnet";
  return "Edge tsnet 未启用";
}

const htmlOpenSettingsKey = ["desktop", "review-html-open"] as const;

async function reviewHTMLOpenSettings(ask?: boolean): Promise<boolean> {
  const response = await fetch("/desktop/review-html-open", ask === undefined ? undefined : {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ ask_before_open: ask }),
  });
  const body = (await response.json()) as { ask_before_open?: boolean; message?: string };
  if (!response.ok || typeof body.ask_before_open !== "boolean") {
    throw new Error(body.message ?? "无法读取本机的 HTML 打开设置。");
  }
  return body.ask_before_open;
}

function ReviewHTMLOpenSettings() {
  const queryClient = useQueryClient();
  const preference = useQuery({
    queryKey: htmlOpenSettingsKey,
    queryFn: () => reviewHTMLOpenSettings(),
    retry: false,
  });
  const save = useMutation({
    mutationFn: (ask: boolean) => reviewHTMLOpenSettings(ask),
    onMutate: () => queryClient.cancelQueries({ queryKey: htmlOpenSettingsKey }),
    onSuccess: ask => queryClient.setQueryData(htmlOpenSettingsKey, ask),
  });

  return (
    <section data-testid="review-html-open-settings" className="rounded-xl border bg-card px-5 py-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <label className="flex min-h-12 items-center gap-3 text-sm font-medium">
          <input
            type="checkbox"
            className="size-4 accent-primary"
            checked={preference.data ?? true}
            disabled={preference.data === undefined || save.isPending}
            aria-describedby="review-html-open-help"
            onChange={event => save.mutate(event.currentTarget.checked)}
          />
          打开 HTML 前先询问
        </label>
        {preference.isError && preference.data === undefined ? (
          <Button type="button" variant="ghost" size="sm" onClick={() => void preference.refetch()}>重新读取</Button>
        ) : null}
      </div>
      <p id="review-html-open-help" className="text-xs leading-relaxed text-muted-foreground">
        开启后，下次从差异面板打开 HTML 时会先询问。确认打开后会记住选择。
      </p>
      <p role="status" className="mt-1 text-xs text-muted-foreground">
        {save.isPending ? "正在保存…" : save.isError ? "保存失败，请重试。" : preference.isError ? "无法读取本机的 HTML 打开设置。" : save.isSuccess ? "已保存" : null}
      </p>
    </section>
  );
}

const settingsSections = [
  { id: "general", label: "常规", icon: Laptop },
  { id: "network", label: "网络", icon: Network },
  { id: "notifications", label: "通知", icon: Bell },
  { id: "about", label: "关于 Meerkat", icon: Globe2 },
] as const;

type SettingsSection = typeof settingsSections[number]["id"];

function focusSettingsSection(section: SettingsSection) {
  if (section === "about") {
    focusAboutMeerkat();
    return;
  }
  const target = document.getElementById("settings-" + section);
  target?.focus({ preventScroll: true });
  target?.scrollIntoView({ block: "start" });
}

export function LiveSettingsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const requestedSection = searchParams.get("section");
  const selectedSection = settingsSections.find(section => section.id === requestedSection);
  const currentSection = selectedSection?.id ?? "general";
  const [status, setStatus] = useState<TSNetStatus>(EMPTY);
  const [edgeStatus, setEdgeStatus] = useState<EdgeStatus>(EMPTY);
  const [health, setHealth] = useState<DesktopHealth | null>(null);
  const [appMode, setAppMode] = useState("");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");

  useEffect(() => {
    // The tray changes the open query parameter to refocus the same section.
    if (selectedSection) focusSettingsSection(selectedSection.id);
  }, [searchParams, selectedSection]);

  const load = useCallback(async () => {
    try {
      const response = await fetch("/api/network/tsnet");
      if (!response.ok) return;
      setStatus((await response.json()) as TSNetStatus);
    } catch {
      // 设置页在 Hub 不可用时保持静态，不阻塞其他页面。
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 2500);
    return () => window.clearInterval(timer);
  }, [load]);

  useEffect(() => {
    const loadLocal = async () => {
      try {
        const edgeResponse = await fetch("/desktop/edge-network");
        if (edgeResponse.ok) setEdgeStatus((await edgeResponse.json()) as EdgeStatus);
        const healthResponse = await fetch("/desktop/health");
        if (healthResponse.ok) {
          const nextHealth = (await healthResponse.json()) as DesktopHealth;
          setHealth(nextHealth);
          setAppMode(nextHealth.mode ?? "");
        }
      } catch {
        // 浏览器预览或本地端点不可用时忽略。
      }
    };
    void loadLocal();
    const timer = window.setInterval(() => void loadLocal(), 2500);
    return () => window.clearInterval(timer);
  }, []);

  const mutate = async (action: "start" | "stop") => {
    setBusy(true);
    setNotice("");
    try {
      const response = await fetch("/api/network/tsnet/" + action, {
        method: "POST",
        headers: { "content-type": "application/json", "x-meerkat-request": "1" },
      });
      const body = (await response.json().catch(() => ({}))) as TSNetStatus & { error?: { message?: string } };
      if (!response.ok) throw new Error(body.error?.message ?? "操作失败");
      setStatus(body);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : "操作失败");
    } finally {
      setBusy(false);
      void load();
    }
  };

  const retry = async () => {
    setBusy(true);
    setNotice("");
    try {
      if (status.state !== "off") {
        await fetch("/api/network/tsnet/stop", {
          method: "POST",
          headers: { "content-type": "application/json", "x-meerkat-request": "1" },
        });
        await new Promise((resolve) => window.setTimeout(resolve, 500));
      }
      const response = await fetch("/api/network/tsnet/start", {
        method: "POST",
        headers: { "content-type": "application/json", "x-meerkat-request": "1" },
      });
      const body = (await response.json().catch(() => ({}))) as TSNetStatus & { error?: { message?: string } };
      if (!response.ok) throw new Error(body.error?.message ?? "操作失败");
      setStatus(body);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : "操作失败");
    } finally {
      setBusy(false);
      void load();
    }
  };

  const authURL = status.auth_url?.trim() ?? "";
  const isOnline = status.state === "running";
  const ip = status.tailscale_ips?.find(value => !value.includes(":")) || status.tailscale_ips?.[0];
  const port = status.port;
  const pairingHubAddress = isOnline && ip && port && Number.isInteger(port) && port > 0 && port <= 65535
    ? `${ip.includes(":") ? `[${ip}]` : ip}:${port}` : "";
  const isAuth = status.state === "needs_login";
  const isStarting = status.state === "starting";
  const isError = status.state === "error";

  const copyAuth = async () => {
    try {
      await navigator.clipboard.writeText(authURL);
      setNotice("登录地址已复制");
    } catch {
      setNotice("复制失败，请手动打开");
    }
  };

  return (
    <div className="space-y-5">
      <PageHeader
        title="设置"
        description="Meerkat 的本机与网络配置。查看节点角色、Edge 与 Hub 连接状态。"
        actions={<Badge variant="outline" className="gap-1 text-[10px]"><Globe2 className="size-3" />{roleBadgeLabel(appMode)}</Badge>}
      />

      <div className="grid gap-5 lg:grid-cols-[210px_minmax(0,1fr)]">
        <nav aria-label="设置分区" className="h-fit rounded-xl border bg-card p-2 lg:sticky lg:top-20">
          {settingsSections.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              type="button"
              aria-current={id === currentSection ? "location" : undefined}
              onClick={() => {
                if (requestedSection === id) focusSettingsSection(id);
                else setSearchParams({ section: id });
              }}
              className={cn(
                "flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm transition-colors",
                id === currentSection ? "bg-accent font-medium text-accent-foreground" : "text-muted-foreground hover:bg-accent/50",
              )}
            >
              <Icon className="size-4" />
              {label}
              {id === currentSection ? <span className="ml-auto size-1.5 rounded-full bg-emerald-500" /> : null}
            </button>
          ))}
          <div className="mt-2 rounded-lg bg-muted/50 px-3 py-2 text-[10px] leading-relaxed text-muted-foreground">
            设置页展示当前 Meerkat App 的网络与连接状态；不同模式的可用操作不同。
          </div>
        </nav>

        <div className="min-w-0 space-y-5">
          <div hidden={currentSection !== "about"}><UpdateSettings /></div>
          <div id="settings-general" tabIndex={-1} hidden={currentSection !== "general"} className="space-y-5">
            {health ? <ReviewHTMLOpenSettings /> : null}
          {health ? (
            <section data-testid="node-role-card" className="overflow-hidden rounded-xl border bg-card">
              <div className="flex flex-wrap items-start justify-between gap-3 border-b px-5 py-4">
                <div>
                  <div className="flex items-center gap-2">
                    <Server className="size-4 text-muted-foreground" />
                    <h2 className="text-sm font-semibold">节点角色与连接</h2>
                    <Badge variant="secondary" className="text-[10px]">只读</Badge>
                  </div>
                  <p className="mt-1 text-xs leading-relaxed text-muted-foreground">当前这台设备的运行模式、连接去向和本机 Edge 状态。</p>
                </div>
                <Badge variant="outline" data-testid="node-role-mode">{modeLabel(health.mode)}</Badge>
              </div>
              <dl className="grid gap-px bg-border sm:grid-cols-2">
                <div className="bg-card px-5 py-3">
                  <dt className="text-[11px] text-muted-foreground">当前模式</dt>
                  <dd className="mt-1 font-medium">{modeLabel(health.mode)}</dd>
                  <dd className="mt-0.5 font-mono text-[10px] text-muted-foreground">{health.mode || "-"}</dd>
                </div>
                <div className="bg-card px-5 py-3">
                  <dt className="text-[11px] text-muted-foreground">Hub 地址</dt>
                  <dd data-testid="node-role-center" className="mt-1 break-all font-mono text-[11px]">{health.center_url || health.hub_url || "-"}</dd>
                </div>
                <div className="bg-card px-5 py-3">
                  <dt className="text-[11px] text-muted-foreground">本机设备</dt>
                  <dd data-testid="node-role-device" className="mt-1 font-mono text-[11px]">{health.local_device_id || "未注册"}</dd>
                </div>
                <div className="bg-card px-5 py-3">
                  <dt className="text-[11px] text-muted-foreground">本机 Edge</dt>
                  <dd data-testid="node-role-edge" className="mt-1 break-all font-mono text-[11px]">{health.edge || "-"}</dd>
                </div>
                <div className="bg-card px-5 py-3">
                  <dt className="text-[11px] text-muted-foreground">Edge 内置 tsnet</dt>
                  <dd data-testid="node-role-tsnet" className="mt-1 text-[11px]">
                    {edgeStatus.enabled ? (edgeStatus.state === "running" ? "已启用 · 已连接" : "已启用 · " + (edgeStatus.state || "未知")) : "未启用"}
                  </dd>
                </div>
                <div className="bg-card px-5 py-3">
                  <dt className="text-[11px] text-muted-foreground">网络依赖</dt>
                  <dd data-testid="node-role-dependency" className="mt-1 text-[11px]">{networkDependency(health.mode, edgeStatus)}</dd>
                </div>
              </dl>
              <p className="border-t px-5 py-2.5 text-[10px] leading-relaxed text-muted-foreground">只读信息，不会修改配置。模式与 Hub 地址由安装、加入工作区或迁移流程决定。</p>
            </section>
          ) : null}
          <section data-testid="join-workspace" className="rounded-xl border bg-card p-4">
            <div className="flex items-center gap-2">
              <Link2 className="size-4 text-sky-600" />
              <h2 className="text-sm font-semibold">加入工作区</h2>
            </div>
            <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
              用另一台 Meerkat Hub 生成的邀请码把本机接进团队。不论当前是哪种模式都可以加入：加入后本机会切换为「节点」，用对方的设备与任务视图。邀请码一次性有效，默认 10 分钟。
            </p>
            <div className="mt-3 flex flex-wrap gap-2">
              <Button data-testid="join-workspace-open" size="sm" className="h-7 gap-1 px-2 text-[11px]" onClick={() => void openJoinWorkspace()}>
                <Link2 className="size-3" />打开加入工作区
              </Button>
              <Button size="sm" variant="outline" className="h-7 gap-1 px-2 text-[11px]" onClick={() => window.location.reload()}>
                <RefreshCw className="size-3" />刷新状态
              </Button>
            </div>
            {appMode === "node" && !edgeStatus.enabled ? (
              <p data-testid="edge-network-missing" className="mt-2 text-[11px] leading-relaxed text-amber-700">
                当前还没加入 tailnet：点上面的按钮粘贴邀请码即可。加入成功后 Edge 会启动内嵌 Tailscale 节点并生成登录地址。
              </p>
            ) : null}
          </section>
          </div>
          <div id="settings-network" tabIndex={-1} hidden={currentSection !== "network"} className="space-y-5">
          {edgeStatus.enabled ? (
            <section className="rounded-xl border border-sky-200 bg-sky-50/40 p-4">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <div className="flex items-center gap-2">
                    <h2 className="text-sm font-semibold">Edge 内嵌节点</h2>
                    <Badge variant="secondary" className="text-[10px]">本机</Badge>
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {edgeStatus.state === "needs_login" ? "Edge 需要登录一次 Tailscale" : edgeStatus.state === "running" ? "Edge 已加入 tailnet，可脱离系统 Tailscale" : "Edge tsnet 正在初始化"}
                  </p>
                  {edgeStatus.paired ? (
                    <p className="mt-1 text-[11px] text-emerald-700" data-testid="edge-pairing-status">
                      自动配对已完成{edgeStatus.device_id ? " · " + edgeStatus.device_id : ""}
                    </p>
                  ) : edgeStatus.pairing?.phase === "error" ? (
                    <p className="mt-1 text-[11px] text-rose-600" data-testid="edge-pairing-status">
                      自动配对失败：{edgeStatus.pairing.error || "未知错误"}
                    </p>
                  ) : edgeStatus.pairing?.phase ? (
                    <p className="mt-1 text-[11px] text-muted-foreground" data-testid="edge-pairing-status">
                      自动配对：{edgeStatus.pairing.phase === "pairing" ? "正在注册本机设备" : "等待内嵌网络就绪"}
                    </p>
                  ) : null}
                </div>
                <span className={cn("text-[11px] font-medium", edgeStatus.state === "running" ? "text-emerald-600" : "text-amber-700")}>
                  {edgeStatus.state === "running" ? "已连接 · " + (edgeStatus.tailscale_ips?.[0] ?? "") : edgeStatus.state}
                </span>
              </div>
              {edgeStatus.auth_url ? (
                <div className="mt-3 flex flex-wrap items-center gap-2">
                  <code className="max-w-full truncate rounded-md border bg-white px-2 py-1 text-[10px]">{edgeStatus.auth_url}</code>
                  <Button data-testid="edge-tailscale-login" size="sm" className="h-7 gap-1 px-2 text-[11px]" onClick={() => void openExternal(edgeStatus.auth_url ?? "")}>
                    <ExternalLink className="size-3" />打开 Edge Tailscale 登录页
                  </Button>
                </div>
              ) : null}
            </section>
          ) : null}
          <section className={cn("overflow-hidden rounded-xl border bg-card", appMode === "node" && "hidden")}>
            <div className="flex flex-wrap items-start justify-between gap-3 border-b px-5 py-4">
              <div>
                <div className="flex items-center gap-2">
                  <h2 className="text-sm font-semibold">内嵌 Tailscale 网络</h2>
                  <Badge variant="secondary" className="text-[10px]">实验性</Badge>
                </div>
                <p className="mt-1 max-w-2xl text-xs leading-relaxed text-muted-foreground">
                  Hub 进程内直接运行一个 userspace Tailscale 节点，不再依赖系统 Tailscale。首次需要登录一次。
                </p>
              </div>
            </div>

            <div className="grid gap-4 p-5 md:grid-cols-3">
              {[
                { title: "无需安装", desc: "不需要系统 Tailscale 客户端", icon: Laptop },
                { title: "无需管理员", desc: "userspace 网络栈，无 TUN", icon: ShieldCheck },
                { title: "一次登录", desc: "登录结果持久化到本机", icon: KeyRound },
              ].map((item) => (
                <div key={item.title} className="rounded-lg border bg-background/60 p-3">
                  <item.icon className="size-4 text-muted-foreground" />
                  <div className="mt-2 text-xs font-medium">{item.title}</div>
                  <div className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">{item.desc}</div>
                </div>
              ))}
            </div>

            <div className="mx-5 mb-5 rounded-xl border bg-muted/20 p-4">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div className="flex items-center gap-3">
                  <span className={cn(
                    "relative flex size-2.5",
                    isOnline ? "text-emerald-500" : isAuth || isStarting ? "text-amber-500" : isError ? "text-rose-500" : "text-muted-foreground",
                  )}>
                    <span className={cn(
                      "absolute inline-flex size-full rounded-full opacity-60",
                      isOnline ? "animate-ping bg-emerald-500" : isAuth || isStarting ? "bg-amber-500" : isError ? "bg-rose-500" : "bg-muted-foreground",
                    )} />
                    <span className={cn(
                      "relative inline-flex size-2.5 rounded-full",
                      isOnline ? "bg-emerald-500" : isAuth || isStarting ? "bg-amber-500" : isError ? "bg-rose-500" : "bg-muted-foreground",
                    )} />
                  </span>
                  <div>
                    <div className="text-sm font-medium">
                      {isOnline ? "已连接 tailnet" : isAuth ? "等待 Tailscale 登录" : isStarting ? "正在启动内嵌节点" : isError ? "内嵌网络启动失败" : "内嵌网络未启动"}
                    </div>
                    <div className="mt-0.5 text-[11px] text-muted-foreground">
                      {isOnline ? "Hub API 已监听 tailnet，可用于 Edge 接入" : isAuth ? "请打开登录地址完成一次授权" : isStarting ? "正在初始化 userspace 网络栈" : isError ? status.error ?? "请稍后重试" : "启动后生成本机 Tailscale 身份"}
                    </div>
                  </div>
                </div>
                <div className="flex flex-wrap gap-2">
                  {status.state === "off" ? (
                    <Button size="sm" className="h-8 gap-1.5" disabled={busy} onClick={() => void mutate("start")}>
                      {busy ? <Loader2 className="size-3.5 animate-spin" /> : <Network className="size-3.5" />}
                      启动内嵌网络
                    </Button>
                  ) : null}
                  {isStarting ? (
                    <Button size="sm" className="h-8 gap-1.5" disabled>
                      <Loader2 className="size-3.5 animate-spin" />
                      正在启动…
                    </Button>
                  ) : null}
                  {authURL ? (
                    <>
                      <Button data-testid="hub-tailscale-login" size="sm" className="h-8 gap-1.5" onClick={() => void openExternal(authURL)}>
                        <ExternalLink className="size-3.5" />打开 Tailscale 登录页
                      </Button>
                      <Button size="sm" variant="outline" className="h-8 gap-1.5" onClick={() => void load()}>
                        <RefreshCw className="size-3.5" />刷新状态
                      </Button>
                    </>
                  ) : isAuth ? (
                    <Button size="sm" variant="outline" className="h-8 gap-1.5" onClick={() => void load()}>
                      <RefreshCw className="size-3.5" />获取登录地址
                    </Button>
                  ) : null}
                  {isOnline ? (
                    <Button size="sm" variant="outline" className="h-8 gap-1.5" disabled={busy} onClick={() => void mutate("stop")}>
                      <RefreshCw className="size-3.5" />停止网络
                    </Button>
                  ) : null}
                  {isError ? (
                    <Button size="sm" className="h-8 gap-1.5" disabled={busy} onClick={() => void retry()}>
                      {busy ? <Loader2 className="size-3.5 animate-spin" /> : <RefreshCw className="size-3.5" />}
                      重试启动
                    </Button>
                  ) : null}
                </div>
              </div>

              {authURL ? (
                <div className="mt-4 rounded-lg border border-amber-200 bg-amber-50/70 p-3">
                  <div className="flex items-center gap-1.5 text-[11px] font-medium text-amber-800">
                    <AlertTriangle className="size-3.5" />Tailscale 登录地址
                  </div>
                  <div className="mt-2 flex flex-wrap items-center gap-2">
                    <code className="max-w-full truncate rounded-md border bg-white px-2 py-1 text-[10px] text-amber-900">{authURL}</code>
                    <Button size="sm" variant="ghost" className="h-7 gap-1 px-2 text-[11px]" onClick={() => void copyAuth()}>
                      <Copy className="size-3" />复制
                    </Button>
                  </div>
                  <p className="mt-2 text-[10px] leading-relaxed text-amber-800/80">
                    地址由本机 tsnet 节点生成；登录一次后节点身份会保存在本机。
                  </p>
                </div>
              ) : null}

              {notice ? <div className="mt-2 text-[11px] text-muted-foreground">{notice}</div> : null}
            </div>

            <div className="grid gap-4 border-t px-5 py-4 sm:grid-cols-3">
              {[
                { label: "节点名", value: status.hostname || "—" },
                { label: "Tailnet IP", value: status.tailscale_ips?.join(", ") || "—" },
                { label: "Hub 地址", value: status.hub_url || "—" },
              ].map((item) => (
                <div key={item.label}>
                  <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{item.label}</div>
                  <div className="mt-1 truncate font-mono text-xs">{item.value}</div>
                </div>
              ))}
            </div>
          </section>

          <MobileDevicesSection hubAddress={pairingHubAddress} />
          <section className="scroll-mt-20 rounded-xl border bg-card p-5">
            <div className="flex items-center justify-between gap-3">
              <div>
                <h2 className="text-sm font-semibold">连接拓扑</h2>
                <p className="mt-1 text-xs text-muted-foreground">Hub、Edge 与 DSH 都使用内嵌 tsnet；不再依赖系统 Tailscale。</p>
              </div>
              <Badge variant="outline" className="text-[10px]">Hub 节点</Badge>
            </div>
            <div className="mt-5 flex flex-col items-stretch gap-2 md:flex-row md:items-center">
              <TopologyNode icon={Server} title="Meerkat Hub" subtitle={status.hostname || "meerkat-hub"} />
              <ArrowRight className="mx-auto size-4 shrink-0 rotate-90 text-muted-foreground/60 md:rotate-0" />
              <div className="rounded-xl border border-dashed bg-muted/30 px-4 py-3 text-center">
                <div className="text-[11px] font-medium">Tailscale tailnet</div>
                <div className="mt-1 text-[10px] text-muted-foreground">WireGuard · userspace</div>
              </div>
              <ArrowRight className="mx-auto size-4 shrink-0 rotate-90 text-muted-foreground/60 md:rotate-0" />
              <TopologyNode icon={Laptop} title="Meerkat Edge" subtitle="meerkat-edge / :51858" />
              <ArrowRight className="mx-auto size-4 shrink-0 rotate-90 text-muted-foreground/60 md:rotate-0" />
              <TopologyNode icon={Network} title="DSH Web" subtitle="127.0.0.1:51857" />
            </div>
          </section>

          <section className="grid gap-4 md:grid-cols-2">
            <div className="rounded-xl border bg-card p-5">
              <h2 className="text-sm font-semibold">接入设置</h2>
              <div className="mt-3 space-y-3 text-xs">
                <div className="flex items-center justify-between gap-3 border-b pb-2">
                  <span className="text-muted-foreground">Hub 监听端口</span>
                  <span className="font-medium">{status.port ?? 7443}</span>
                </div>
                <div className="flex items-center justify-between gap-3 border-b pb-2">
                  <span className="text-muted-foreground">Tailnet ACL</span>
                  <span className="font-medium">默认允许 Meerkat 节点</span>
                </div>
                <div className="flex items-center justify-between gap-3">
                  <span className="text-muted-foreground">退出/清除身份</span>
                  <span className="font-medium text-muted-foreground">下一阶段</span>
                </div>
              </div>
            </div>
            <div className="rounded-xl border bg-card p-5">
              <h2 className="text-sm font-semibold">当前边界</h2>
              <ul className="mt-3 space-y-2 text-xs leading-relaxed text-muted-foreground">
                <li>· Hub 与 Edge 均使用内嵌 tsnet。</li>
                <li>· 首次需要 Tailscale 账号登录一次。</li>
                <li>· Edge 的 DSH 通过 :51858 暴露到 tailnet。</li>
                <li>· 首次需要 Tailscale 登录一次，之后自动连接。</li>
              </ul>
            </div>
          </section>
          </div>
          <div id="settings-notifications" tabIndex={-1} hidden={currentSection !== "notifications"} className="scroll-mt-20">
            <TurnNotificationsSection />
          </div>
        </div>
      </div>
    </div>
  );
}

function formatMobileTime(iso: string | null | undefined) {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
}

function mobileStatusLabel(credential: MobileCredential) {
  if (credential.status === "expired") return "已过期";
  if (credential.status === "expiring") {
    return credential.days_to_expiry == null ? "即将过期" : credential.days_to_expiry + " 天后过期";
  }
  return "有效";
}

function mobileStatusTone(credential: MobileCredential) {
  if (credential.status === "expired") return "border-rose-200 text-rose-600 dark:border-rose-500/30 dark:text-rose-400";
  if (credential.status === "expiring") return "border-amber-200 text-amber-700 dark:border-amber-500/30 dark:text-amber-400";
  return "border-emerald-200 text-emerald-700 dark:border-emerald-500/30 dark:text-emerald-400";
}

function MobileDevicesSection({ hubAddress }: { hubAddress: string }) {
  const [credentials, setCredentials] = useState<MobileCredential[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [pairing, setPairing] = useState<MobilePairingCode | null>(null);
  const [generating, setGenerating] = useState(false);
  const [revoking, setRevoking] = useState("");
  const [copied, setCopied] = useState(false);

  const load = useCallback(async () => {
    try {
      const payload = await liveApi.mobileCredentials();
      setCredentials(payload.credentials ?? []);
      setError("");
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "读取移动设备失败");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 10_000);
    return () => window.clearInterval(timer);
  }, [load]);

  const generate = async () => {
    setGenerating(true);
    setError("");
    setCopied(false);
    try {
      setPairing(await liveApi.createMobilePairingToken("android-app"));
    } catch (generateError) {
      setError(generateError instanceof Error ? generateError.message : "生成配对码失败");
    } finally {
      setGenerating(false);
    }
  };

  const copyCode = async () => {
    if (!pairing) return;
    try {
      await navigator.clipboard.writeText(pairing.code);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setError("复制失败，请手动选择配对码");
    }
  };

  const revoke = async (credential: MobileCredential) => {
    if (!window.confirm("撤销会永久删除「" + credential.label + "」的凭证记录，设备下一次请求会被拒绝且无法恢复，需要重新配对。确定撤销？")) return;
    setRevoking(credential.id);
    setError("");
    try {
      await liveApi.revokeMobileCredential(credential.id);
      await load();
    } catch (revokeError) {
      setError(revokeError instanceof Error ? revokeError.message : "撤销失败");
    } finally {
      setRevoking("");
    }
  };

  return (
    <section data-testid="mobile-devices" className="overflow-hidden rounded-xl border bg-card">
      <div className="flex flex-wrap items-start justify-between gap-3 border-b px-5 py-4">
        <div>
          <div className="flex items-center gap-2">
            <Smartphone className="size-4 text-muted-foreground" />
            <h2 className="text-sm font-semibold">移动设备</h2>
            <Badge variant="secondary" className="text-[10px]">只读凭证</Badge>
          </div>
          <p className="mt-1 max-w-2xl text-xs leading-relaxed text-muted-foreground">
            给 Android App 生成一次性配对码；App 用它换取独立设备凭证。凭证只能读设备和任务、打开 DSH，有效期 90 天，可单独撤销（撤销即删除该记录，不可恢复）。
          </p>
        </div>
        <Button data-testid="mobile-pairing-generate" size="sm" className="h-8 gap-1.5" disabled={generating} onClick={() => void generate()}>
          {generating ? <Loader2 className="size-3.5 animate-spin" /> : <QrCodeIcon className="size-3.5" />}
          生成配对码
        </Button>
      </div>

      {pairing ? (
        <div className="border-b bg-muted/20 px-5 py-4" data-testid="mobile-pairing-result">
          <div className="flex flex-col gap-4 sm:flex-row sm:items-center">
            <div className="flex shrink-0 justify-center rounded-lg border bg-white p-2">
              <QrCodeImage value={hubAddress ? `meerkat-pair://v1?${new URLSearchParams({ hub: hubAddress, code: pairing.code })}` : pairing.code} size={132} label="移动设备配对码二维码" />
            </div>
            <div className="min-w-0 flex-1 space-y-2">
              <div className="text-[11px] text-muted-foreground">在手机 Meerkat「设置 → 设备配对」点「扫码配对」，或手动输入下方配对码。10 分钟内有效，只用一次。</div>
              <div className="flex items-start gap-2 rounded-lg border bg-zinc-950 px-3 py-2">
                <code data-testid="mobile-pairing-code" className="min-w-0 flex-1 select-all break-all font-mono text-xs text-zinc-100">
                  {pairing.code}
                </code>
                <Button variant="ghost" size="icon" className="size-7 shrink-0 text-zinc-400 hover:text-zinc-100" onClick={() => void copyCode()} aria-label="复制配对码">
                  {copied ? <Check className="size-3.5 text-emerald-400" /> : <Copy className="size-3.5" />}
                </Button>
              </div>
              <div className="text-[10px] text-muted-foreground">过期时间：{formatMobileTime(pairing.expires_at)} · scopes：{pairing.scopes.join("、")}</div>
            </div>
          </div>
        </div>
      ) : null}

      {error ? (
        <div className="border-b border-rose-200 bg-rose-50 px-5 py-2 text-xs text-rose-700 dark:border-rose-500/25 dark:bg-rose-500/10 dark:text-rose-400">
          {error}
        </div>
      ) : null}

      <div className="divide-y">
        {loading ? (
          <div className="px-5 py-6 text-center text-xs text-muted-foreground">正在读取移动设备…</div>
        ) : credentials.length === 0 ? (
          <div className="px-5 py-8 text-center">
            <Smartphone className="mx-auto size-6 text-muted-foreground/50" />
            <div className="mt-2 text-xs font-medium">还没有移动设备配对</div>
            <div className="mt-1 text-[11px] text-muted-foreground">生成后，在手机 Meerkat 的「设置 → 设备配对」扫码或手动输入。</div>
          </div>
        ) : (
          credentials.map((credential) => (
            <div key={credential.id} className="flex flex-wrap items-center gap-3 px-5 py-3" data-testid="mobile-credential-row">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="truncate text-xs font-medium">{credential.label || "移动设备"}</span>
                  <Badge variant="outline" className={"text-[10px] " + mobileStatusTone(credential)}>{mobileStatusLabel(credential)}</Badge>
                </div>
                <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 font-mono text-[10px] text-muted-foreground">
                  <span>{credential.id}</span>
                  <span>{credential.platform || "android"}{credential.model ? " · " + credential.model : ""}</span>
                  <span>最近使用 {formatMobileTime(credential.last_used_at)}</span>
                  <span>过期 {formatMobileTime(credential.expires_at)}</span>
                </div>
              </div>
              <Button
                data-testid="mobile-credential-revoke"
                size="sm"
                variant="outline"
                className="h-7 gap-1 px-2 text-[11px] text-rose-600"
                // 撤销是硬删除：这一行删掉就从列表消失，所以只需挡「正在撤销这一行」。
                disabled={revoking === credential.id}
                onClick={() => void revoke(credential)}
              >
                {revoking === credential.id ? <Loader2 className="size-3 animate-spin" /> : <Trash2 className="size-3" />}
                撤销
              </Button>
            </div>
          ))
        )}
      </div>
    </section>
  );
}

function TopologyNode({ icon: Icon, title, subtitle }: { icon: typeof Server; title: string; subtitle: string }) {
  return (
    <div className="min-w-0 flex-1 rounded-xl border bg-background px-4 py-3">
      <Icon className="size-4 text-muted-foreground" />
      <div className="mt-2 truncate text-xs font-medium">{title}</div>
      <div className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground">{subtitle}</div>
    </div>
  );
}