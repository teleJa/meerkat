import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowUpRight,
  Check,
  Copy,
  Loader2,
  LogOut,
  RefreshCw,
  QrCode,
  Server,
} from "lucide-react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { PageHeader } from "@/components/page-header";
import { DeviceStatusBadge } from "@/components/status-badge";
import { QrCode as QrCodeImage } from "@/components/qr-code";
import { LiveGate } from "@/live/live-gate";
import {
  errorMessage,
  formatBytes,
  formatClock,
  formatRelative,
  hasGpu,
  liveApi,
  pct,
  type LiveDevice,
} from "@/live/api";

export function LiveDevicesPage() {
  return (
    <LiveGate>
      <LiveDevicesContent />
    </LiveGate>
  );
}

function LiveDevicesContent() {
  const queryClient = useQueryClient();
  const devicesQuery = useQuery({
    queryKey: ["live", "devices"],
    queryFn: liveApi.devices,
    refetchInterval: 5000,
    retry: false,
  });
  const eventsQuery = useQuery({
    queryKey: ["live", "events"],
    queryFn: () => liveApi.events(30),
    refetchInterval: 10000,
    retry: false,
  });

  const devices = devicesQuery.data?.devices ?? [];
  const counts = useMemo(() => {
    return {
      total: devices.length,
      online: devices.filter((device) => device.status === "online").length,
      stale: devices.filter((device) => device.status === "stale").length,
      offline: devices.filter((device) => device.status === "offline").length,
    };
  }, [devices]);

  const events = eventsQuery.data?.events ?? [];
  const nameOf = (id: string | null) => devices.find((device) => device.id === id)?.name ?? id ?? "中心";

  const logout = async () => {
    try {
      await liveApi.logout();
    } catch {
      /* 已经退出或会话失效时同样回到登录页 */
    }
    queryClient.clear();
    window.location.reload();
  };

  if (devicesQuery.isError) {
    return (
      <Card className="mx-auto mt-10 max-w-lg">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-sm">
            <Server className="size-4 text-rose-500" />
            读取设备失败
          </CardTitle>
          <CardDescription>{errorMessage(devicesQuery.error)}</CardDescription>
        </CardHeader>
        <CardContent className="flex gap-2">
          <Button variant="outline" onClick={() => devicesQuery.refetch()}>
            <RefreshCw className="size-4" />
            重试
          </Button>
          <Button variant="ghost" onClick={logout}>
            <LogOut className="size-4" />
            重新登录
          </Button>
        </CardContent>
      </Card>
    );
  }

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="实时设备"
        description={
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span>数据来自当前中心 {window.location.origin}</span>
            <span className="text-muted-foreground/40">|</span>
            <span>每 5 秒刷新</span>
            <span className="text-muted-foreground/40">|</span>
            <span className="tabular-nums">
              最后同步 {formatClock(devicesQuery.data?.server_time ?? null)}
            </span>
          </span>
        }
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => void devicesQuery.refetch()}>
              <RefreshCw className={"size-3.5 " + (devicesQuery.isFetching ? "animate-spin" : "")} />
              刷新
            </Button>
            <PairingTokenDialog />
            <Button variant="ghost" size="sm" onClick={() => void logout()}>
              <LogOut className="size-3.5" />
              退出
            </Button>
          </>
        }
      />

      <Card className="gap-0 py-0">
        <div className="grid grid-cols-2 divide-y sm:grid-cols-4 sm:divide-y-0 sm:divide-x">
          <Stat label="设备总数" value={String(counts.total)} hint="已注册" />
          <Stat label="在线" value={String(counts.online)} hint="心跳 ≤ 30s" />
          <Stat label="状态陈旧" value={String(counts.stale)} hint="30–60s 无心跳" />
          <Stat label="离线" value={String(counts.offline)} hint="> 60s 或未连接" />
        </div>
      </Card>

      <div className="grid items-start gap-4 xl:grid-cols-3">
        <div className="xl:col-span-2">
          {devicesQuery.isLoading ? (
            <div className="grid gap-4 md:grid-cols-2">
              <Skeleton className="h-56" />
              <Skeleton className="h-56" />
            </div>
          ) : devices.length === 0 ? (
            <Card className="border-dashed">
              <CardHeader>
                <CardTitle className="text-sm">还没有设备接入</CardTitle>
                <CardDescription>
                  桌面端点「接入设备」生成邀请码，在目标电脑的 Meerkat「设置 → 加入工作区」粘贴。手机请到「设置 → 网络 → 移动设备」生成配对码。
                </CardDescription>
              </CardHeader>
              <CardContent>
                <PairingTokenDialog />
              </CardContent>
            </Card>
          ) : (
            <div className="grid gap-4 md:grid-cols-2">
              {devices.map((device) => (
                <LiveDeviceCard key={device.id} device={device} />
              ))}
            </div>
          )}
        </div>

        <Card className="gap-0 py-0">
          <CardHeader className="flex flex-row items-center justify-between px-5 pt-5 pb-2">
            <CardTitle className="text-sm">实时事件</CardTitle>
            <span className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
              <span className="relative flex size-1.5">
                <span className="absolute inline-flex size-full animate-ping rounded-full bg-emerald-500 opacity-60" />
                <span className="relative inline-flex size-1.5 rounded-full bg-emerald-500" />
              </span>
              每 10 秒
            </span>
          </CardHeader>
          <CardContent className="px-0 pb-1">
            {eventsQuery.isLoading ? (
              <div className="space-y-2 px-5 py-3">
                <Skeleton className="h-4 w-full" />
                <Skeleton className="h-4 w-3/4" />
              </div>
            ) : events.length === 0 ? (
              <p className="px-5 py-6 text-center text-xs text-muted-foreground">暂无事件</p>
            ) : (
              events.slice(0, 12).map((event) => (
                <div key={event.id} className="flex items-start gap-3 px-5 py-2.5">
                  <span
                    className={
                      "mt-1.5 size-1.5 shrink-0 rounded-full " +
                      (event.type === "device.connected"
                        ? "bg-emerald-500"
                        : event.type === "device.disconnected" || event.type === "device.revoked"
                          ? "bg-rose-500"
                          : "bg-muted-foreground/40")
                    }
                  />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-xs">{eventLabel(event.type)}</span>
                    <span className="mt-0.5 block truncate text-[10px] text-muted-foreground">
                      {nameOf(event.device_id)} · <span className="font-mono">{event.type}</span>
                    </span>
                  </span>
                  <span className="shrink-0 text-[10px] tabular-nums text-muted-foreground">
                    {formatClock(event.at)}
                  </span>
                </div>
              ))
            )}
          </CardContent>
        </Card>
      </div>

      <div className="rounded-lg border bg-muted/30 px-4 py-3 text-[11px] leading-relaxed text-muted-foreground">
        状态口径：设备状态由最后心跳推导，与任务状态正交；指标缺失或不支持时显示 “—”，不记为 0。
        断开连接的设备会先显示“状态陈旧”，超过 60 秒转为“离线”。
      </div>
    </div>
  );
}

function LiveDeviceCard({ device }: { device: LiveDevice }) {
  const metrics = device.metrics;
  const memPct = pct(metrics?.mem_used_bytes ?? null, metrics?.mem_total_bytes ?? null);
  const diskPct = pct(metrics?.disk_used_bytes ?? null, metrics?.disk_total_bytes ?? null);
  const gpuVisible = hasGpu(device);

  return (
    <Card className="gap-0 overflow-hidden py-0">
      <CardHeader className="px-5 pt-5">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="truncate text-sm font-semibold">{device.name}</h3>
              <DeviceStatusBadge status={device.status} />
              {device.revoked_at ? (
                <Badge variant="outline" className="border-rose-200 text-rose-600 dark:border-rose-500/30 dark:text-rose-400">
                  已撤销
                </Badge>
              ) : null}
            </div>
            <p className="truncate text-xs text-muted-foreground">
              {device.hostname} · {device.os} · {device.arch}
            </p>
          </div>
          <Button asChild variant="ghost" size="icon" className="size-8 shrink-0 text-muted-foreground">
            <Link to={"/live/devices/" + device.id} aria-label={"查看 " + device.name + " 指标"}>
              <ArrowUpRight className="size-4" />
            </Link>
          </Button>
        </div>
      </CardHeader>
      <CardContent className="px-5 pb-4 pt-4">
        <div className="space-y-2.5">
          {[
            { label: "CPU", value: metrics?.cpu_pct },
            { label: "内存", value: memPct, detail: formatBytes(metrics?.mem_total_bytes ?? null) },
            { label: "磁盘", value: diskPct, detail: formatBytes(metrics?.disk_total_bytes ?? null) },
            ...(gpuVisible ? [{ label: "GPU", value: metrics?.gpu_util_pct, detail: metrics?.gpu_temp_c != null ? `${metrics.gpu_temp_c}°C` : undefined }] : []),
          ].map(({ label, value, detail }) => (
            <div key={label} title={detail} className="grid grid-cols-[2.5rem_1fr_3rem] items-center gap-3 text-xs">
              <span>{label}</span>
              <div aria-hidden="true" className="h-1.5 overflow-hidden rounded-full bg-muted">
                <div className={device.status === "online" ? "h-full rounded-full bg-primary" : "h-full bg-muted-foreground"} style={{ width: `${Math.max(0, Math.min(100, value ?? 0))}%` }} />
              </div>
              <span className="text-right tabular-nums">{value == null ? "—" : `${Math.round(value)}%`}</span>
            </div>
          ))}
        </div>
        <Separator className="my-4" />
        <div className="grid grid-cols-3 gap-2 text-xs">
          <div className="space-y-0.5">
            <div className="text-muted-foreground">角色</div>
            <div className="truncate font-medium">{device.role}</div>
          </div>
          <div className="space-y-0.5">
            <div className="text-muted-foreground">最近心跳</div>
            <div className="font-medium">{device.status_age_seconds === null ? "—" : device.status_age_seconds + "s 前"}</div>
          </div>
          <div className="space-y-0.5">
            <div className="text-muted-foreground">指标时间</div>
            <div className="font-medium tabular-nums">{formatClock(metrics?.sampled_at ?? null)}</div>
          </div>
        </div>
      </CardContent>
      <div className="flex items-center justify-between border-t bg-muted/20 px-5 py-2.5 text-[11px] text-muted-foreground">
        <span>心跳 {formatRelative(device.last_seen_at)}</span>
        <span className="tabular-nums">
          Edge {device.edge_version} · 协议 {device.protocol_version}
        </span>
      </div>
    </Card>
  );
}

type InvitePayload = { code: string; hub_url?: string };

function PairingTokenDialog() {
  const [open, setOpen] = useState(false);
  const [invite, setInvite] = useState<InvitePayload | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const generate = async () => {
    setPending(true);
    setError(null);
    setInvite(null);
    try {
      // 邀请码由桌面端生成：它包含 Hub 地址、内嵌网络地址与一次性配对令牌。
      const response = await fetch("/desktop/invite", { headers: { accept: "application/json" } });
      const payload = (await response.json().catch(() => ({}))) as InvitePayload & {
        error?: { message?: string };
      };
      if (!response.ok) throw new Error(payload?.error?.message ?? "生成邀请码失败");
      if (!payload.code) throw new Error("邀请码为空");
      setInvite(payload);
    } catch (generateError) {
      setError(errorMessage(generateError));
    } finally {
      setPending(false);
    }
  };

  const onOpenChange = (next: boolean) => {
    setOpen(next);
    if (next) {
      setCopied(false);
      void generate();
    } else {
      setInvite(null);
      setError(null);
    }
  };

  const copy = async () => {
    if (!invite) return;
    try {
      await navigator.clipboard?.writeText(invite.code);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setError("复制失败，请手动选择文本复制");
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button size="sm" data-testid="pairing-open">
          <QrCode className="size-3.5" />
          接入设备
        </Button>
      </DialogTrigger>
      <DialogContent data-testid="pairing-dialog" className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>邀请设备加入</DialogTitle>
          <DialogDescription>
            在目标电脑的 Meerkat「设置 → 加入工作区」粘贴桌面邀请码。手机配对请到 Hub 的「设置 → 网络 → 移动设备」。
          </DialogDescription>
        </DialogHeader>
        {pending ? (
          <div className="flex items-center gap-2 rounded-lg border bg-muted/40 px-3 py-6 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            正在生成邀请码…
          </div>
        ) : error ? (
          <div className="rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-500/25 dark:bg-rose-500/10 dark:text-rose-400">
            {error}
          </div>
        ) : invite ? (
          <div className="space-y-3">
            <div className="flex justify-center rounded-lg border bg-white p-3" data-testid="pairing-qr">
              <QrCodeImage value={invite.code} size={176} label="桌面邀请码二维码" />
            </div>
            <div className="rounded-lg border bg-zinc-950 px-3 py-2.5">
              <div className="flex min-w-0 items-start justify-between gap-2">
                <code
                  data-testid="pairing-invite-code"
                  className="min-w-0 flex-1 select-all whitespace-pre-wrap break-all font-mono text-[11px] leading-relaxed text-zinc-200"
                >
                  {invite.code}
                </code>
                <Button
                  variant="ghost"
                  size="icon"
                  className="size-7 shrink-0 text-zinc-400 hover:text-zinc-100"
                  onClick={() => void copy()}
                  data-testid="pairing-copy"
                  aria-label="复制邀请码"
                >
                  {copied ? <Check className="size-3.5 text-emerald-400" /> : <Copy className="size-3.5" />}
                </Button>
              </div>
            </div>
            <p className="text-[11px] leading-relaxed text-muted-foreground">
              桌面邀请码和二维码内容相同，默认 10 分钟内有效，只用一次，请勿公开分享。首次加入时，App 会打开 Tailscale 登录页。
            </p>
            <div className="flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
              <span className="min-w-0 truncate">Hub: {invite.hub_url || "—"}</span>
              <button
                type="button"
                data-testid="pairing-regenerate"
                className="shrink-0 underline-offset-4 hover:underline"
                onClick={() => void generate()}
              >
                重新生成
              </button>
            </div>
          </div>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function Stat({ label, value, hint }: { label: string; value: string; hint: string }) {
  return (
    <div className="px-5 py-4">
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className="mt-1 flex items-baseline gap-1.5">
        <span className="text-xl font-semibold tabular-nums tracking-tight">{value}</span>
        <span className="text-[11px] text-muted-foreground">{hint}</span>
      </div>
    </div>
  );
}

function eventLabel(type: string): string {
  switch (type) {
    case "device.registered":
      return "设备已注册";
    case "device.connected":
      return "Edge 已连接";
    case "device.disconnected":
      return "Edge 连接断开";
    case "device.revoked":
      return "设备凭据已撤销";
    default:
      return type;
  }
}
