import { useQuery } from "@tanstack/react-query";
import { Monitor } from "lucide-react";
import { ExternalLink, Loader2, RefreshCw } from "lucide-react";
import { useEffect, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { LiveGate } from "@/live/live-gate";
import { EmptyState } from "@/live/live-shared";
import { errorMessage, type LiveDevice } from "@/live/api";
import { DshDeviceCard } from "@/live/live-dsh-card";
import { HARNESS_LIST_STATE, forgetHarnessDevice, readLastHarnessDevice, rememberHarnessDevice } from "@/live/last-harness-device";
import { openExternal } from "@/lib/open-external";

export function LiveHarnessPage() {
  return (
    <LiveGate>
      <LiveHarnessIndex />
    </LiveGate>
  );
}

// 常驻的 DSH 详情页：切换 Meerkat 路由时只隐藏，不卸载 iframe。
export function PersistentHarnessDetail() {
  const location = useLocation();
  const match = location.pathname.match(/^\/live\/harness\/([^/]+)$/);
  const [lastDevice, setLastDevice] = useState("");

  useEffect(() => {
    if (match?.[1]) setLastDevice(decodeURIComponent(match[1]));
  }, [match?.[1]]);

  const deviceId = match?.[1] ? decodeURIComponent(match[1]) : lastDevice;
  const active = Boolean(match?.[1]);
  useEffect(() => {
    if (deviceId) rememberHarnessDevice(deviceId);
  }, [deviceId]);
  if (!deviceId) return null;
  return (
    <div className={active ? "min-h-0 flex-1" : "hidden"}>
      <LiveHarnessContent deviceId={deviceId} />
    </div>
  );
}

function LiveHarnessIndex() {
  const location = useLocation();
  const navigate = useNavigate();
  const pickDevice = Boolean((location.state as { pickDevice?: boolean } | null)?.pickDevice);
  const devicesQuery = useQuery({
    queryKey: ["live", "devices"],
    queryFn: async () => {
      const response = await fetch("/api/devices");
      if (!response.ok) throw new Error("读取设备失败");
      return (await response.json()) as { devices: LiveDevice[] };
    },
    refetchInterval: 15000,
    retry: false,
  });
  const devices = devicesQuery.data?.devices ?? [];
  const localQuery = useQuery({
    queryKey: ["live", "local-device"],
    queryFn: async () => {
      const response = await fetch("/desktop/health");
      if (!response.ok) throw new Error("local health unavailable");
      return (await response.json()) as { local_device_id?: string };
    },
    retry: false,
    staleTime: 60_000,
  });
  const localDeviceId = localQuery.data?.local_device_id ?? "";
  const lastDevice = readLastHarnessDevice();
  const deviceExists = devices.some((device) => device.id === lastDevice);
  const willRedirect = !pickDevice && lastDevice !== "" && deviceExists;
  const redirectPending = !pickDevice && lastDevice !== "" && !devicesQuery.isSuccess && !devicesQuery.isError;

  useEffect(() => {
    if (!devicesQuery.isSuccess || lastDevice === "") return;
    if (!deviceExists) {
      // 设备被删除或不在当前中心：清掉，回退到列表，避免每次进来都跳到空页。
      forgetHarnessDevice();
      return;
    }
    if (pickDevice) return;
    navigate("/live/harness/" + encodeURIComponent(lastDevice), { replace: true });
  }, [pickDevice, devicesQuery.isSuccess, lastDevice, deviceExists, navigate]);

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-lg font-semibold">DSH</h1>
        <p className="mt-1 text-xs text-muted-foreground">
          选择一台设备的 DSH profile：可以直接连接已运行的实例，也可以由 Meerkat 启动。
          打开过的设备会被记住，下次进入 DSH 会直接回到它。
        </p>
      </div>
      {devicesQuery.isLoading || (redirectPending && !pickDevice) || (willRedirect && !pickDevice) ? (
        <Skeleton className="h-40 w-full" />
      ) : null}
      {!devicesQuery.isLoading && devices.length === 0 ? (
        <EmptyState icon={Monitor} title="没有已注册设备" hint="先在实时设备页完成 Edge 配对。" />
      ) : null}
      {!pickDevice && willRedirect ? null : (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {devices.map((device) => (
            <DshDeviceCard key={device.id} device={device} localDeviceId={localDeviceId} />
          ))}
        </div>
      )}
    </div>
  );
}

function LiveHarnessContent({ deviceId }: { deviceId: string }) {
  const [loaded, setLoaded] = useState(false);
  const localQuery = useQuery({
    queryKey: ["live", "local-device"],
    queryFn: async () => {
      const response = await fetch("/desktop/health");
      if (!response.ok) throw new Error("local health unavailable");
      return (await response.json()) as { local_device_id?: string };
    },
    retry: false,
    staleTime: 60_000,
  });
  const localDeviceId = localQuery.data?.local_device_id ?? "";
  const proxyQuery = useQuery({
    queryKey: ["live", "harness-proxy", deviceId],
    queryFn: async () => {
      const local = await fetch("/desktop/harness-origin?device=" + encodeURIComponent(deviceId ?? "")).catch(() => null);
      if (local?.ok) {
        const origin = (await local.json()) as { url?: string };
        if (origin.url) return { url: origin.url, path: undefined, desktopOrigin: true };
      }
      const response = await fetch("/api/devices/" + encodeURIComponent(deviceId ?? "") + "/harness-proxy");
      if (!response.ok) throw new Error("该设备还没有配置 DSH");
      return (await response.json()) as { url: string; path?: string };
    },
    enabled: Boolean(deviceId),
    retry: false,
  });

  const url = proxyQuery.data?.path ?? proxyQuery.data?.url?.replace("127.0.0.1", window.location.hostname);

  return (
    <div className="flex h-[calc(100dvh-7.5rem)] min-h-[540px] flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-xl border bg-card px-4 py-2.5">
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-sm font-medium">
            DSH
            {deviceId === localDeviceId ? (
              <span className="rounded-full border border-sky-200 bg-sky-50 px-1.5 py-0.5 text-[10px] font-medium text-sky-700">本机</span>
            ) : null}
          </div>
          <div className="truncate text-[11px] text-muted-foreground">
            设备 {deviceId} · DSH 页面通过 Meerkat 同源代理加载
            {proxyQuery.data && "desktopOrigin" in proxyQuery.data && localDeviceId && deviceId !== localDeviceId
              ? " · 差异面板可在本机浏览器打开 HTML"
              : null}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="ghost" size="sm" asChild>
            <Link to="/live/harness" state={HARNESS_LIST_STATE}>切换设备</Link>
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={async () => {
              let target = proxyQuery.data && "desktopOrigin" in proxyQuery.data ? url : undefined;
              if (!target) {
                const response = await fetch("/api/devices/" + encodeURIComponent(deviceId) + "/harness-proxy");
                if (!response.ok) return;
                const data = (await response.json()) as { url?: string; path?: string };
                target = data.path ? new URL(data.path, window.location.origin).toString() : data.url;
              }
              if (!target) return;
              const local = await fetch("/desktop/open-window", {
                method: "POST",
                headers: { "content-type": "application/json" },
                body: JSON.stringify({ url: target, title: "DSH" }),
              }).catch(() => null);
              if (!local?.ok) await openExternal(target);
            }}
          >
            <ExternalLink className="size-3.5" />
            新窗口打开
          </Button>
        </div>
      </div>

      {proxyQuery.isLoading ? (
        <Skeleton className="min-h-0 flex-1" />
      ) : proxyQuery.isError || !url ? (
        <EmptyState
          icon={RefreshCw}
          title="无法打开 DSH"
          hint={errorMessage(proxyQuery.error)}
          action={
            <div className="flex flex-wrap items-center justify-center gap-2">
              <Button variant="outline" size="sm" onClick={() => void proxyQuery.refetch()}>
                <RefreshCw className="size-3.5" />
                重试
              </Button>
              <Button variant="ghost" size="sm" asChild>
                <Link to="/live/harness" state={HARNESS_LIST_STATE}>切换设备</Link>
              </Button>
            </div>
          }
        />
      ) : (
        <div className="relative min-h-0 flex-1 overflow-hidden rounded-xl border bg-white">
          <iframe
            title="DeepSeek Harness"
            src={url}
            className="h-full w-full border-0"
            allow="clipboard-read; clipboard-write"
            onLoad={() => setLoaded(true)}
          />
          {!loaded ? (
            <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-white text-xs text-slate-500">
              <Loader2 className="size-5 animate-spin" />
              正在加载 DSH…
            </div>
          ) : null}
        </div>
      )}
    </div>
  );
}