import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Activity, ArrowLeft, Cpu, PowerOff, RefreshCw, Server } from "lucide-react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { PageHeader } from "@/components/page-header";
import { DeviceStatusBadge } from "@/components/status-badge";
import { MetricRing } from "@/components/metric-ring";
import { ChartLegend, MetricChart } from "@/components/metric-chart";
import { LiveGate } from "@/live/live-gate";
import {
  errorMessage,
  formatBytes,
  formatClock,
  formatRelative,
  hasGpu,
  liveApi,
  pct,
  toChartPoints,
} from "@/live/api";

export function LiveDeviceDetailPage() {
  return (
    <LiveGate>
      <LiveDeviceDetailContent />
    </LiveGate>
  );
}

function LiveDeviceDetailContent() {
  const { deviceId } = useParams();
  const queryClient = useQueryClient();
  const deviceQuery = useQuery({
    queryKey: ["live", "device", deviceId],
    queryFn: () => liveApi.device(deviceId as string),
    refetchInterval: 5000,
    retry: false,
    enabled: Boolean(deviceId),
  });
  const metricsQuery = useQuery({
    queryKey: ["live", "metrics", deviceId],
    queryFn: () => liveApi.metrics(deviceId as string, 120),
    refetchInterval: 5000,
    retry: false,
    enabled: Boolean(deviceId),
  });
  const eventsQuery = useQuery({
    queryKey: ["live", "events"],
    queryFn: () => liveApi.events(50),
    refetchInterval: 10000,
    retry: false,
  });

  const [revokeOpen, setRevokeOpen] = useState(false);
  const [revoking, setRevoking] = useState(false);
  const [revokeError, setRevokeError] = useState<string | null>(null);

  if (deviceQuery.isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-72 w-full" />
      </div>
    );
  }

  if (deviceQuery.isError || !deviceQuery.data) {
    return (
      <div className="mx-auto mt-10 max-w-lg">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <Server className="size-4 text-rose-500" />
              无法读取设备
            </CardTitle>
            <CardDescription>{errorMessage(deviceQuery.error)}</CardDescription>
          </CardHeader>
          <CardContent>
            <Button asChild variant="outline">
              <Link to="/live">
                <ArrowLeft className="size-4" />
                返回实时设备
              </Link>
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  const device = deviceQuery.data.device;
  const samples = metricsQuery.data?.samples ?? [];
  const latest = device.metrics;
  const memPct = pct(latest?.mem_used_bytes ?? null, latest?.mem_total_bytes ?? null);
  const diskPct = pct(latest?.disk_used_bytes ?? null, latest?.disk_total_bytes ?? null);
  const gpuVisible = hasGpu(device);
  const events = (eventsQuery.data?.events ?? []).filter((event) => event.device_id === device.id);

  const revoke = async () => {
    setRevoking(true);
    setRevokeError(null);
    try {
      await liveApi.revokeDevice(device.id);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["live", "device", device.id] }),
        queryClient.invalidateQueries({ queryKey: ["live", "devices"] }),
        queryClient.invalidateQueries({ queryKey: ["live", "events"] }),
      ]);
      setRevokeOpen(false);
    } catch (revokeFailure) {
      setRevokeError(errorMessage(revokeFailure));
    } finally {
      setRevoking(false);
    }
  };

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        backTo="/live"
        backLabel="返回实时设备"
        title={
          <span className="flex flex-wrap items-center gap-2">
            {device.name}
            <DeviceStatusBadge status={device.status} />
            {device.revoked_at ? (
              <Badge variant="outline" className="border-rose-200 text-rose-600 dark:border-rose-500/30 dark:text-rose-400">
                凭据已撤销
              </Badge>
            ) : null}
          </span>
        }
        description={
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span>{device.hostname}</span>
            <span className="text-muted-foreground/40">·</span>
            <span>{device.os}</span>
            <span className="text-muted-foreground/40">·</span>
            <span className="font-mono">{device.arch}</span>
            <span className="text-muted-foreground/40">·</span>
            <span>{device.role}</span>
          </span>
        }
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => void deviceQuery.refetch()}>
              <RefreshCw className={"size-3.5 " + (deviceQuery.isFetching ? "animate-spin" : "")} />
              刷新
            </Button>
            <Dialog open={revokeOpen} onOpenChange={setRevokeOpen}>
              <DialogTrigger asChild>
                <Button variant="outline" size="sm" disabled={Boolean(device.revoked_at)}>
                  <PowerOff className="size-3.5" />
                  撤销设备
                </Button>
              </DialogTrigger>
              <DialogContent>
                <DialogHeader>
                  <DialogTitle>撤销 {device.name} 的凭据？</DialogTitle>
                  <DialogDescription>
                    撤销后该设备的 WebSocket 会被立即关闭，Edge 用旧凭据重连会被拒绝（4401），需要重新配对。
                    已入库的指标与事件会保留。
                  </DialogDescription>
                </DialogHeader>
                {revokeError ? (
                  <p className="rounded-md bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:bg-rose-500/10 dark:text-rose-400">
                    {revokeError}
                  </p>
                ) : null}
                <DialogFooter>
                  <Button variant="outline" onClick={() => setRevokeOpen(false)}>
                    取消
                  </Button>
                  <Button variant="destructive" onClick={() => void revoke()} disabled={revoking}>
                    {revoking ? "撤销中…" : "确认撤销"}
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>
          </>
        }
      />

      <Card className="gap-0 py-0">
        <div className="grid grid-cols-2 gap-x-4 gap-y-4 px-5 py-4 sm:grid-cols-3 lg:grid-cols-6">
          <Meta label="设备 ID" value={device.id} mono />
          <Meta label="Edge 版本" value={device.edge_version} />
          <Meta label="协议版本" value={device.protocol_version} />
          <Meta label="最近心跳" value={formatRelative(device.last_seen_at)} />
          <Meta label="指标时间" value={formatClock(latest?.sampled_at ?? null)} mono />
          <Meta label="注册时间" value={formatClock(device.created_at)} mono />
        </div>
      </Card>

      <div className="grid items-start gap-4 xl:grid-cols-3">
        <Card className="gap-0 py-0 xl:col-span-2">
          <CardHeader className="flex flex-row items-start justify-between gap-4 px-5 pt-5">
            <div className="space-y-1">
              <CardTitle className="text-sm">资源历史</CardTitle>
              <CardDescription>
                {samples.length} 个采样 · 每 5 秒 · 本地时区；缺失值不补零
              </CardDescription>
            </div>
            <ChartLegend showGpu={gpuVisible} />
          </CardHeader>
          <CardContent className="px-2 pb-3">
            {samples.length < 2 ? (
              <div className="flex h-[240px] flex-col items-center justify-center gap-1 rounded-lg bg-muted/20 text-center">
                <Activity className="size-5 text-muted-foreground" />
                <p className="text-sm font-medium">采样累计中</p>
                <p className="text-xs text-muted-foreground">Edge 每 5 秒上报一次，稍后刷新即可看到曲线</p>
              </div>
            ) : (
              <MetricChart data={toChartPoints(samples)} showGpu={gpuVisible} height={240} />
            )}
          </CardContent>
        </Card>

        <Card className="gap-0 py-0">
          <CardHeader className="px-5 pt-5">
            <CardTitle className="text-sm">当前指标</CardTitle>
            <CardDescription>直接来自 Edge 上报；缺失记为 unavailable。</CardDescription>
          </CardHeader>
          <CardContent className="px-5 pb-5">
            <div className="grid grid-cols-2 gap-x-2 gap-y-5">
              <MetricRing
                label="CPU"
                value={latest?.cpu_pct ?? null}
                detail="整机 0–100%"
                stale={device.status !== "online"}
                size={82}
              />
              <MetricRing
                label="内存"
                value={memPct}
                detail={formatBytes(latest?.mem_total_bytes ?? null)}
                stale={device.status !== "online"}
                size={82}
              />
              <MetricRing
                label="磁盘"
                value={diskPct}
                detail={formatBytes(latest?.disk_total_bytes ?? null)}
                stale={device.status !== "online"}
                size={82}
              />
              {gpuVisible ? (
                <MetricRing
                  label="GPU"
                  value={latest?.gpu_util_pct ?? null}
                  detail={latest?.gpu_temp_c ? latest.gpu_temp_c + "°C" : "nvidia-smi"}
                  stale={device.status !== "online"}
                  size={82}
                />
              ) : (
                <div className="flex flex-col items-center justify-center gap-1 text-center text-[11px] text-muted-foreground">
                  <Cpu className="size-5" />
                  <span>GPU 不可用</span>
                  <span>该设备未上报 NVIDIA 指标</span>
                </div>
              )}
            </div>
            <Separator className="my-4" />
            <div className="space-y-1.5 text-[11px] text-muted-foreground">
              <Row label="内存" value={formatBytes(latest?.mem_used_bytes ?? null) + " / " + formatBytes(latest?.mem_total_bytes ?? null)} />
              <Row label="磁盘" value={formatBytes(latest?.disk_used_bytes ?? null) + " / " + formatBytes(latest?.disk_total_bytes ?? null)} />
              {gpuVisible ? (
                <Row
                  label="显存"
                  value={formatBytes(latest?.gpu_mem_used_bytes ?? null) + " / " + formatBytes(latest?.gpu_mem_total_bytes ?? null)}
                />
              ) : null}
            </div>
          </CardContent>
        </Card>
      </div>

      <div className="grid items-start gap-4 xl:grid-cols-3">
        <Card className="gap-0 py-0 xl:col-span-2">
          <CardHeader className="flex flex-row items-center justify-between px-5 pt-5 pb-2">
            <CardTitle className="text-sm">该设备事件</CardTitle>
            <span className="text-[11px] text-muted-foreground">{events.length} 条</span>
          </CardHeader>
          <CardContent className="px-0 pb-2">
            {events.length === 0 ? (
              <p className="px-5 py-8 text-center text-xs text-muted-foreground">暂无事件</p>
            ) : (
              events.slice(0, 10).map((event) => (
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
                    <span className="block truncate text-xs">{event.type}</span>
                    <span className="mt-0.5 block truncate text-[10px] text-muted-foreground">
                      {event.detail ?? "—"}
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

        <div className="flex flex-col gap-4">
          <Card className="gap-0 py-0">
            <CardHeader className="px-5 pt-5">
              <CardTitle className="text-sm">能力与身份</CardTitle>
              <CardDescription>注册时由 Edge 上报，中心按 device_id 识别。</CardDescription>
            </CardHeader>
            <CardContent className="space-y-3 px-5 pb-5">
              <div className="flex flex-wrap gap-1.5">
                {device.capabilities.length === 0 ? (
                  <span className="text-xs text-muted-foreground">未上报能力</span>
                ) : (
                  device.capabilities.map((capability) => (
                    <Badge key={capability} variant="outline" className="text-[10px] font-normal">
                      {capability}
                    </Badge>
                  ))
                )}
              </div>
              <Separator />
              <div className="space-y-1.5 text-[11px] text-muted-foreground">
                <Row label="boot_id" value={device.boot_id} mono />
                <Row label="最后指标" value={formatRelative(device.last_metrics_at)} />
                <Row label="状态年龄" value={device.status_age_seconds === null ? "—" : device.status_age_seconds + " 秒"} />
              </div>
            </CardContent>
          </Card>

          <div className="rounded-lg border border-dashed px-4 py-3 text-[11px] leading-relaxed text-muted-foreground">
            <div className="mb-1 font-medium text-foreground">数据说明</div>
            曲线来自中心 SQLite 中的真实采样；设备断线后数值冻结，状态随时间变为 stale → offline。
          </div>
        </div>
      </div>
    </div>
  );
}

function Meta({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="min-w-0 space-y-0.5">
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className={(mono ? "font-mono text-xs " : "text-sm ") + "truncate font-medium"}>{value}</div>
    </div>
  );
}

function Row({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <span className="shrink-0">{label}</span>
      <span className={(mono ? "font-mono " : "") + "min-w-0 truncate text-right"}>{value}</span>
    </div>
  );
}
