import { useSyncExternalStore } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";

interface UpdateState {
  version: string;
  mode: "windows" | "manual" | "unconfigured";
  automatic: boolean;
  phase: string;
  message?: string;
  available?: string;
  installable: boolean;
  last_check?: string;
  check_result?: string;
  last_install?: string;
  failed_version?: string;
  bytes?: number;
  total?: number;
  prepared?: boolean;
  blocker?: { pid: number; name: string };
}

type UpdateAction =
  | { action: "status" | "check" }
  | { action: "automatic"; enabled: boolean }
  | { action: "install"; version: string }
  | { action: "end-process"; version: string; pid: number };

function subscribeToDesktopUpdate(listener: () => void) {
  window.addEventListener("meerkat-update-ready", listener);
  return () => window.removeEventListener("meerkat-update-ready", listener);
}

function desktopUpdateToken() {
  return (window as Window & { __meerkatUpdateToken?: string }).__meerkatUpdateToken;
}

async function requestUpdateSettings(token: string, request: UpdateAction): Promise<UpdateState> {
  const response = await fetch("/desktop/update-settings", {
    method: "POST",
    cache: "no-store",
    headers: { "Content-Type": "application/json", "X-Meerkat-Update-Token": token },
    body: JSON.stringify(request),
  });
  const state = await response.json().catch(() => null) as (UpdateState & { message?: string }) | null;
  if (!response.ok || !state?.version) throw new Error(state?.message ?? "无法与本机更新器通信，请重试。");
  return state;
}

export function focusAboutMeerkat() {
  const title = document.getElementById("about-meerkat-title");
  title?.focus({ preventScroll: true });
  document.getElementById("about-meerkat")?.scrollIntoView({ block: "start" });
}

export function UpdateSettings() {
  const token = useSyncExternalStore(subscribeToDesktopUpdate, desktopUpdateToken, () => undefined);
  const queryClient = useQueryClient();
  const updates = useQuery({
    queryKey: ["desktop", "updates"],
    queryFn: () => requestUpdateSettings(token!, { action: "status" }),
    enabled: !!token,
    refetchInterval: 1500,
  });
  const change = useMutation({
    mutationFn: (request: UpdateAction) => requestUpdateSettings(token!, request),
    onMutate: () => queryClient.cancelQueries({ queryKey: ["desktop", "updates"] }),
    onSuccess: state => queryClient.setQueryData(["desktop", "updates"], state),
  });
  const state = token ? updates.data : undefined;
  const busy = change.isPending || ["checking", "confirming", "downloading", "installing"].includes(state?.phase ?? "");
  const versionToInstall = state?.mode !== "unconfigured"
    ? (state?.installable && state.available) || state?.failed_version
    : undefined;
  const checkLabel = state?.phase === "error" && state.check_result?.startsWith("检查失败：") ? "重试检查" : "检查更新";
  const retry = !!state?.failed_version || state?.phase === "blocked" || state?.phase === "error";
  const installingText = state?.mode === "manual" ? "打开安装包" : retry ? "重试更新" : "立即更新";
  const lastCheck = state?.last_check && new Date(state.last_check).toLocaleString("zh-CN", { dateStyle: "short", timeStyle: "short" });
  const downloaded = state?.bytes ?? 0;
  const total = state?.total ?? 0;
  const isDownloading = state?.phase === "downloading";

  return (
    <section id="about-meerkat" className="scroll-mt-20 overflow-hidden rounded-xl border bg-card">
      <div className="flex flex-wrap items-start justify-between gap-3 px-5 py-4">
        <div>
          <h2 id="about-meerkat-title" tabIndex={-1} className="text-base font-semibold">关于 Meerkat</h2>
          {state ? (
            <p className="mt-1 flex items-baseline gap-2 text-sm text-muted-foreground">
              当前版本 <strong className="font-mono text-base text-foreground">{state.version}</strong>
            </p>
          ) : (
            <p role="status" className="mt-1 text-sm text-muted-foreground">
              {token ? updates.isError ? "无法读取本机版本和更新状态。" : "正在读取本机版本…" : "版本与更新仅在 Meerkat 桌面应用中可用。"}
            </p>
          )}
        </div>
        {state && state.mode !== "unconfigured" ? (
          <Button type="button" variant="outline" size="sm" disabled={busy || (state.mode === "windows" && state.prepared)} onClick={() => change.mutate({ action: "check" })}>
            <RefreshCw aria-hidden="true" className="size-3.5" />{checkLabel}
          </Button>
        ) : null}
      </div>
      {state ? (
        <div className="space-y-4 border-t px-5 py-4 text-sm">
          {state.mode === "windows" ? (
            <label className="flex w-fit min-h-8 items-center gap-2.5 font-medium">
              <input type="checkbox" className="size-4 accent-primary" checked={state.automatic} disabled={change.isPending || state.phase === "installing"}
                aria-describedby="meerkat-automatic-description" onChange={event => change.mutate({ action: "automatic", enabled: event.currentTarget.checked })} />
              自动更新
            </label>
          ) : null}
          {state.mode === "windows" ? (
            <p id="meerkat-automatic-description" className="-mt-3 text-xs leading-relaxed text-muted-foreground">
              {state.automatic ? "有新版时自动下载，等电脑空闲且任务结束后安装。" : "自动下载已关闭，仍可手动检查和安装。"}
            </p>
          ) : null}
          <dl className="grid grid-cols-1 gap-2 sm:grid-cols-[max-content_minmax(0,1fr)] sm:gap-x-4">
            {state.mode === "windows" ? <>
              <dt className="text-muted-foreground">上次检查</dt>
              <dd>{lastCheck ? <time dateTime={state.last_check}>{lastCheck}</time> : "尚未检查"}</dd>
              <dt className="text-muted-foreground">检查结果</dt>
              <dd role="status" className="break-words">{state.check_result ?? "—"}</dd>
            </> : null}
            {state.last_install ? <>
              <dt className="text-muted-foreground">上次安装</dt>
              <dd className="break-words">{state.last_install}</dd>
            </> : null}
          </dl>
          {state.message && state.message !== state.check_result ? <p role="status" className="break-words text-muted-foreground">{state.message}</p> : null}
          {isDownloading ? (
            <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
              <progress aria-label="下载更新进度" className="w-full max-w-xs accent-primary" max={total > 0 ? total : undefined} value={total > 0 ? downloaded : undefined} />
              <span>{total > 0 ? `${Math.floor(100 * downloaded / total)}%` : `${(downloaded / 1048576).toFixed(1)} MB`}</span>
            </div>
          ) : null}
          {state.blocker ? <p className="text-sm">占用进程 <strong>{state.blocker.name}</strong>（PID {state.blocker.pid}）</p> : null}
          {versionToInstall ? (
            <div className="flex flex-wrap items-center gap-2">
              <div className="mr-1 font-medium">{state.failed_version === versionToInstall ? "待重试版本" : "可用版本"} {versionToInstall}</div>
              {state.blocker ? (
                <Button type="button" size="sm" disabled={busy} onClick={() => change.mutate({ action: "end-process", version: versionToInstall, pid: state.blocker!.pid })}>
                  结束该进程并继续
                </Button>
              ) : null}
              <Button type="button" size="sm" variant={state.blocker ? "outline" : "default"} disabled={busy}
                onClick={() => change.mutate({ action: "install", version: versionToInstall })}>{installingText}</Button>
            </div>
          ) : null}
          {change.error ? <p role="status" className="text-destructive">{change.error.message}</p> : null}
        </div>
      ) : null}
    </section>
  );
}
