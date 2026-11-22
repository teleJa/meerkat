import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, KeyRound, Loader2, Network, RefreshCw, Settings, ShieldCheck } from "lucide-react";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PageHeader } from "@/components/page-header";
import { Skeleton } from "@/components/ui/skeleton";
import { Link } from "react-router-dom";
import { ApiError, errorMessage, liveApi } from "@/live/api";
import { openExternal } from "@/lib/open-external";

export function LiveGate({ children }: { children: React.ReactNode }) {
  const queryClient = useQueryClient();
  const session = useQuery({
    queryKey: ["live", "session"],
    queryFn: liveApi.session,
    retry: false,
    staleTime: 60_000,
  });

  if (session.isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-4 w-80" />
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          <Skeleton className="h-56" />
          <Skeleton className="h-56" />
          <Skeleton className="h-56" />
        </div>
      </div>
    );
  }

  if (session.isError) {
    if (session.error instanceof ApiError && session.error.status === 401) {
      return (
        <LiveLogin
          onLoggedIn={() => {
            void queryClient.invalidateQueries();
          }}
        />
      );
    }
    return <NetworkOnboarding error={errorMessage(session.error)} onRetry={() => session.refetch()} />;
  }

  return <>{children}</>;
}

type EdgeNetworkState = {
  enabled?: boolean;
  state?: string;
  auth_url?: string;
  paired?: boolean;
  device_id?: string;
  pairing?: { phase?: string; error?: string };
};

function stepTone(state: "wait" | "ok" | "bad") {
  if (state === "ok") return "bg-emerald-500";
  if (state === "bad") return "bg-rose-500";
  return "bg-amber-500";
}

function NetworkOnboarding({ error, onRetry }: { error: string; onRetry: () => void }) {
  const [mode, setMode] = useState("");
  const [edge, setEdge] = useState<EdgeNetworkState>({});
  const retried = useRef(false);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const [healthResponse, edgeResponse] = await Promise.all([
          fetch("/desktop/health").catch(() => null),
          fetch("/desktop/edge-network").catch(() => null),
        ]);
        if (cancelled) return;
        if (healthResponse?.ok) {
          const health = (await healthResponse.json()) as { mode?: string };
          setMode(health.mode ?? "");
        }
        if (edgeResponse?.ok) {
          setEdge((await edgeResponse.json()) as EdgeNetworkState);
        }
      } catch {
        // 引导卡只依赖本机端点；不可用时仍显示通用步骤。
      }
    };
    void load();
    const timer = window.setInterval(() => void load(), 2000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, []);

  // 身份已写入且内嵌网络已连接后自动重试一次，成功后进入节点界面。
  useEffect(() => {
    if (retried.current || !edge.paired || edge.state !== "running") return;
    retried.current = true;
    onRetry();
  }, [edge.paired, edge.state, onRetry]);

  const isNode = mode === "node";
  const authURL = edge.auth_url?.trim() ?? "";
  const pairingPhase = edge.pairing?.phase ?? "";
  const pairingError = edge.pairing?.error?.trim() ?? "";
  const networkReady = edge.state === "running";

  const networkState: "wait" | "ok" | "bad" =
    networkReady ? "ok" : edge.state === "error" ? "bad" : "wait";
  const networkText = networkReady
    ? "已连接（内嵌 Tailscale 节点在线）"
    : edge.state === "needs_login"
      ? "等待浏览器完成 Tailscale 登录；登录页会自动打开"
      : edge.state === "error"
        ? "内嵌网络启动失败，请打开网络设置重试"
        : "正在启动内嵌网络…";

  const pairingState: "wait" | "ok" | "bad" =
    edge.paired ? "ok" : pairingPhase === "error" ? "bad" : "wait";
  const pairingText = edge.paired
    ? "已完成" + (edge.device_id ? " · " + edge.device_id : "")
    : pairingPhase === "error"
      ? "失败：" + (pairingError || "未知错误")
      : pairingPhase === "pairing"
        ? "正在向 Hub 注册本机设备…"
        : "等待网络就绪后自动注册，无需命令行";

  return (
    <Card className="mx-auto mt-10 max-w-xl" data-testid="network-onboarding">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Network className="size-4 text-amber-500" />
          {edge.paired && networkReady ? "正在进入 Meerkat" : "正在把这台设备接入工作区"}
        </CardTitle>
        <CardDescription className="leading-relaxed">
          {isNode
            ? "这台设备会先加入 Hub 的内嵌 Tailscale 网络，再自动完成配对。首次加入只需在浏览器登录一次 Tailscale，App 会自动打开登录页，不需要命令行。"
            : "Hub 的内嵌 Tailscale 网络还没有就绪；完成一次网络配置后才能打开设备与 DSH。"}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <ol className="space-y-3 text-xs leading-relaxed">
          {[
            { title: "内嵌网络", text: networkText, tone: networkState },
            { title: "自动配对", text: pairingText, tone: pairingState },
            {
              title: "进入设备界面",
              text: edge.paired && networkReady ? "已就绪，正在自动刷新…" : "配对完成后自动进入",
              tone: edge.paired && networkReady ? ("ok" as const) : ("wait" as const),
            },
          ].map((step, index) => (
            <li key={step.title} className="flex items-start gap-2.5">
              <span className={`mt-1 size-2 shrink-0 rounded-full ${stepTone(step.tone)}`} />
              <span>
                <span className="font-medium text-foreground">
                  {index + 1}. {step.title}
                </span>
                <span className="mt-0.5 block text-muted-foreground">{step.text}</span>
              </span>
            </li>
          ))}
        </ol>
        {authURL ? (
          <div className="rounded-lg border border-amber-200 bg-amber-50/70 px-3 py-2.5 text-xs text-amber-900">
            <div className="font-medium">Edge 正在等待 Tailscale 登录</div>
            <div className="mt-1 text-amber-800/80">App 已尝试自动打开登录页；如果没有弹出，请点下面的按钮。</div>
            <div className="mt-1 break-all font-mono text-[10px] leading-relaxed text-amber-800/80">{authURL}</div>
            <Button
              size="sm"
              className="mt-2 h-7 gap-1.5 px-2 text-[11px]"
              onClick={() => void openExternal(authURL)}
            >
              <ExternalLink className="size-3" />
              打开 Edge 登录页
            </Button>
          </div>
        ) : null}
        {isNode && !edge.enabled ? (
          <p className="rounded-lg border bg-muted/40 px-3 py-2 text-[11px] leading-relaxed text-muted-foreground">
            如果持续看不到进度，说明这台设备还没有加入 Hub：请打开「设置 → 加入工作区」，粘贴 Hub「接入设备」里生成的邀请码。
          </p>
        ) : null}
        <p className="text-[11px] leading-relaxed text-muted-foreground">
          网络配置只需要做一次；节点身份保存在本机，之后启动 Meerkat 会自动连接。
        </p>
        <p className="break-all text-[10px] text-muted-foreground/70">错误详情：{error}</p>
      </CardContent>
      <CardFooter className="flex flex-wrap gap-2">
        <Button asChild data-testid="network-settings-link">
          <Link to="/live/settings?section=network">
            <Settings className="size-4" />
            打开网络设置
          </Link>
        </Button>
        <Button onClick={onRetry} variant="outline" data-testid="network-retry">
          <RefreshCw className="size-4" />
          立即重试
        </Button>
      </CardFooter>
    </Card>
  );
}
function LiveLogin({ onLoggedIn }: { onLoggedIn: () => void }) {
  const [token, setToken] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!token.trim() || pending) return;
    setPending(true);
    setError(null);
    try {
      await liveApi.login(token.trim());
      setToken("");
      onLoggedIn();
    } catch (loginError) {
      setError(errorMessage(loginError));
    } finally {
      setPending(false);
    }
  };

  return (
    <div className="mx-auto flex max-w-md flex-col gap-5 pt-6">
      <PageHeader
        title="实时设备面板"
        description="连接本机中心，查看真实设备状态与指标。数据来自 Meerkat Center。"
      />
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-sm">
            <ShieldCheck className="size-4 text-emerald-500" />
            登录中心
          </CardTitle>
          <CardDescription>
            输入中心管理令牌（admin token）。本地启动时位于{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-[11px]">center/data/admin-token</code>
            ，或使用环境变量 <code className="rounded bg-muted px-1 py-0.5 text-[11px]">MEERKAT_ADMIN_TOKEN</code>。
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit} className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="live-token" className="flex items-center gap-1.5">
                <KeyRound className="size-3.5" />
                管理令牌
              </Label>
              <Input
                id="live-token"
                type="password"
                value={token}
                onChange={(event) => setToken(event.target.value)}
                placeholder="粘贴 admin token"
                autoComplete="current-password"
              />
            </div>
            {error ? (
              <p className="rounded-md bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:bg-rose-500/10 dark:text-rose-400">
                {error}
              </p>
            ) : null}
            <Button type="submit" className="w-full" disabled={pending || token.trim().length === 0}>
              {pending ? <Loader2 className="size-4 animate-spin" /> : null}
              {pending ? "登录中…" : "登录"}
            </Button>
          </form>
        </CardContent>
        <CardFooter className="text-[11px] leading-relaxed text-muted-foreground">
          令牌只用于换取 HttpOnly 会话 Cookie，不会保存在浏览器存储中；会话有效期 12 小时。
        </CardFooter>
      </Card>
    </div>
  );
}