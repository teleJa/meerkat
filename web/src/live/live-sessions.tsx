import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { ArrowUpRight, Eye, MonitorSmartphone, RefreshCw, Server, ShieldQuestion } from "lucide-react";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { PageHeader } from "@/components/page-header";
import { SessionStatusBadge } from "@/components/status-badge";
import { LiveGate } from "@/live/live-gate";
import { EmptyState, InfoBanner, Meta } from "@/live/live-shared";
import { LiveEventTimeline, LiveTurnList } from "@/live/live-timeline";
import {
  errorMessage,
  formatDateTime,
  formatRelative,
  liveApi,
  type LiveSessionSummary,
} from "@/live/api";
import { modeLabel } from "@/lib/status";

export function LiveSessionsPage() {
  return (
    <LiveGate>
      <LiveSessionsContent />
    </LiveGate>
  );
}

function LiveSessionsContent() {
  const [searchParams, setSearchParams] = useSearchParams();
  const [tab, setTab] = useState<string>("all");

  const sessionsQuery = useQuery({
    queryKey: ["live", "sessions", 100],
    queryFn: () => liveApi.listSessions({ limit: 100 }),
    refetchInterval: 5000,
    retry: false,
  });
  const devicesQuery = useQuery({
    queryKey: ["live", "devices"],
    queryFn: liveApi.devices,
    refetchInterval: 15000,
    retry: false,
  });

  const sessions = sessionsQuery.data?.sessions ?? [];
  const devices = devicesQuery.data?.devices ?? [];
  const selectedId = searchParams.get("session");

  const filtered = useMemo(() => {
    if (tab === "managed") return sessions.filter((session) => session.mode === "managed");
    if (tab === "observed") return sessions.filter((session) => session.mode === "observed");
    return sessions;
  }, [sessions, tab]);

  const managedCount = sessions.filter((session) => session.mode === "managed").length;
  const observedCount = sessions.length - managedCount;
  const deviceName = (id: string | null | undefined) =>
    (id ? devices.find((device) => device.id === id)?.name : null) ?? id ?? "—";

  const openSession = (id: string) => {
    const next = new URLSearchParams(searchParams);
    next.set("session", id);
    setSearchParams(next, { replace: true });
  };

  const closeSession = () => {
    const next = new URLSearchParams(searchParams);
    next.delete("session");
    setSearchParams(next, { replace: true });
  };

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="实时会话"
        description={
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span>托管会话可追问与取消；观察会话只展示本机终端请求，不注入输入</span>
            <span className="text-muted-foreground/40">|</span>
            <span>每 5 秒刷新</span>
            <span className="text-muted-foreground/40">|</span>
            <span className="tabular-nums">
              最后同步 {formatRelative(sessionsQuery.dataUpdatedAt > 0 ? new Date(sessionsQuery.dataUpdatedAt).toISOString() : null)}
            </span>
          </span>
        }
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => void sessionsQuery.refetch()}>
              <RefreshCw className={"size-3.5 " + (sessionsQuery.isFetching ? "animate-spin" : "")} />
              刷新
            </Button>
            <div className="hidden items-center gap-3 text-xs text-muted-foreground sm:flex">
              <span className="flex items-center gap-1.5">
                <MonitorSmartphone className="size-3.5" /> {managedCount} 托管
              </span>
              <span className="flex items-center gap-1.5">
                <Eye className="size-3.5" /> {observedCount} 观察
              </span>
            </div>
          </>
        }
      />

      {sessionsQuery.isError ? (
        <InfoBanner
          tone={sessions.length > 0 ? "warning" : "danger"}
          title={sessions.length > 0 ? "刷新失败，正在显示上次数据" : "读取会话失败"}
        >
          {errorMessage(sessionsQuery.error)}
        </InfoBanner>
      ) : null}

      <Card className="gap-0 overflow-hidden py-0">
        <div className="flex items-center justify-between border-b px-4 py-3">
          <Tabs value={tab} onValueChange={setTab}>
            <TabsList variant="line">
              <TabsTrigger value="all" className="px-2.5">
                全部
              </TabsTrigger>
              <TabsTrigger value="managed" className="px-2.5">
                托管
              </TabsTrigger>
              <TabsTrigger value="observed" className="px-2.5">
                观察
              </TabsTrigger>
            </TabsList>
          </Tabs>
          <span className="text-xs tabular-nums text-muted-foreground">{filtered.length} 个会话</span>
        </div>

        {sessionsQuery.isLoading ? (
          <div className="space-y-3 px-4 py-4">
            {[0, 1, 2, 3].map((row) => (
              <Skeleton key={row} className="h-9 w-full" />
            ))}
          </div>
        ) : sessionsQuery.isError && sessions.length === 0 ? (
          <div className="p-4">
            <EmptyState
              icon={Server}
              title="无法读取会话"
              hint="中心未就绪或网络异常；修复后可点「刷新」重试。"
              action={
                <Button variant="outline" size="sm" onClick={() => void sessionsQuery.refetch()}>
                  <RefreshCw className="size-3.5" />
                  重试
                </Button>
              }
            />
          </div>
        ) : filtered.length === 0 ? (
          <div className="p-4">
            <EmptyState
              icon={ShieldQuestion}
              title={sessions.length === 0 ? "还没有会话" : "没有符合筛选条件的会话"}
              hint={
                sessions.length === 0
                  ? "派发任务后中心会自动创建托管会话；观察模式需要在本机安装 Hooks 才会出现。"
                  : "切换上方筛选查看其它模式的会话。"
              }
            />
          </div>
        ) : (
          <Table className="table-fixed">
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="w-auto pl-4">会话</TableHead>
                <TableHead className="w-[88px]">模式</TableHead>
                <TableHead className="hidden w-[120px] md:table-cell">设备</TableHead>
                <TableHead className="w-[120px]">状态</TableHead>
                <TableHead className="hidden w-[72px] text-center lg:table-cell">轮次</TableHead>
                <TableHead className="w-[96px] pr-4 text-right">最近活动</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {filtered.map((session) => (
                <SessionRow
                  key={session.id}
                  session={session}
                  deviceName={deviceName(session.device_id)}
                  onOpen={() => openSession(session.id)}
                />
              ))}
            </TableBody>
          </Table>
        )}
      </Card>

      <div className="grid gap-4 lg:grid-cols-3">
        <div className="rounded-lg border bg-muted/30 px-4 py-3 text-[11px] leading-relaxed text-muted-foreground lg:col-span-2">
          <div className="mb-1 flex items-center gap-1.5 font-medium text-foreground">
            <ShieldQuestion className="size-3.5" />
            观察模式边界
          </div>
          外部会话通过明确安装的 Claude Hooks 上报；首期只依赖目标版本验证过的事件，不承诺把网页输入注入任意终端。完整控制请使用托管模式。
        </div>
        <div className="rounded-lg border bg-muted/30 px-4 py-3 text-[11px] leading-relaxed text-muted-foreground">
          <div className="mb-1 font-medium text-foreground">同会话串行</div>
          同一会话同一时刻至多一个执行中的轮次，新轮次排队；恢复使用准确的 harness_session_id，恢复失败会显式报错，不静默新建会话。
        </div>
      </div>

      <SessionSheet
        sessionId={selectedId}
        onClose={closeSession}
        summary={sessions.find((session) => session.id === selectedId) ?? null}
        deviceName={deviceName}
      />
    </div>
  );
}

function SessionRow({
  session,
  deviceName,
  onOpen,
}: {
  session: LiveSessionSummary;
  deviceName: string;
  onOpen: () => void;
}) {
  return (
    <TableRow className="cursor-pointer" onClick={onOpen}>
      <TableCell className="max-w-[320px] pl-4">
        <div className="flex items-center gap-1.5">
          <span className="truncate font-mono text-xs font-medium">{session.id}</span>
          <ArrowUpRight className="size-3 shrink-0 text-muted-foreground" />
        </div>
        <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[11px] text-muted-foreground">
          {session.task_id ? (
            <span className="truncate font-mono">{session.task_id}</span>
          ) : (
            <span className="truncate">无关联任务</span>
          )}
          <span className="shrink-0 text-muted-foreground/40">·</span>
          <span className="truncate">{session.cwd || "—"}</span>
        </div>
      </TableCell>
      <TableCell>
        <Badge variant={session.mode === "managed" ? "secondary" : "outline"} className="font-normal">
          {modeLabel[session.mode]}
        </Badge>
      </TableCell>
      <TableCell className="hidden whitespace-nowrap text-xs text-muted-foreground md:table-cell">
        {deviceName}
      </TableCell>
      <TableCell>
        <SessionStatusBadge status={session.status} />
      </TableCell>
      <TableCell className="hidden text-center text-xs tabular-nums text-muted-foreground lg:table-cell">
        {session.turn_count}
      </TableCell>
      <TableCell className="pr-4 text-right text-xs tabular-nums text-muted-foreground">
        <span title={formatDateTime(session.last_activity_at)}>{formatRelative(session.last_activity_at)}</span>
      </TableCell>
    </TableRow>
  );
}

function SessionSheet({
  sessionId,
  onClose,
  summary,
  deviceName,
}: {
  sessionId: string | null;
  onClose: () => void;
  summary: LiveSessionSummary | null;
  deviceName: (id: string | null | undefined) => string;
}) {
  const detailQuery = useQuery({
    queryKey: ["live", "session", sessionId],
    queryFn: () => liveApi.getSession(sessionId as string),
    refetchInterval: 3000,
    retry: false,
    enabled: Boolean(sessionId),
  });

  const session = detailQuery.data?.session ?? summary;
  const turns = detailQuery.data?.turns ?? [];
  const events = detailQuery.data?.events ?? [];

  return (
    <Sheet
      open={Boolean(sessionId)}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <SheetContent className="flex w-full flex-col gap-0 sm:max-w-lg">
        <SheetHeader className="border-b">
          <SheetTitle>会话详情</SheetTitle>
          <SheetDescription className="flex flex-wrap items-center gap-1.5">
            {session ? (
              <>
                <Badge variant={session.mode === "managed" ? "secondary" : "outline"} className="font-normal">
                  {modeLabel[session.mode]}
                </Badge>
                <SessionStatusBadge status={session.status} />
                <span className="font-mono text-[11px]">{session.id}</span>
              </>
            ) : (
              <span className="font-mono text-[11px]">{sessionId}</span>
            )}
          </SheetDescription>
        </SheetHeader>

        <div className="flex-1 overflow-y-auto p-4">
          {detailQuery.isLoading ? (
            <div className="space-y-3">
              <Skeleton className="h-20 w-full" />
              <Skeleton className="h-40 w-full" />
            </div>
          ) : detailQuery.isError ? (
            <InfoBanner tone="danger" title="读取会话详情失败">
              <div className="space-y-2">
                <div>{errorMessage(detailQuery.error)}</div>
                <Button variant="outline" size="sm" onClick={() => void detailQuery.refetch()}>
                  <RefreshCw className="size-3.5" />
                  重试
                </Button>
              </div>
            </InfoBanner>
          ) : session ? (
            <div className="space-y-4">
              <div className="grid grid-cols-2 gap-x-3 gap-y-3">
                <Meta label="设备" value={deviceName(session.device_id ?? summary?.device_id ?? null)} />
                <Meta label="轮次" value={String(summary?.turn_count ?? turns.length)} />
                <Meta label="创建时间" value={formatDateTime(session.created_at)} mono />
                <Meta label="最近活动" value={formatRelative(session.last_activity_at)} />
                <Meta
                  label="harness_session_id"
                  value={session.harness_session_id ?? "—"}
                  mono
                  className="col-span-2"
                />
                <Meta label="工作目录" value={session.cwd || "—"} mono className="col-span-2" />
              </div>

              {session.task_id ? (
                <div className="flex items-center justify-between gap-2 rounded-lg border px-3 py-2 text-xs">
                  <span className="text-[11px] text-muted-foreground">关联任务</span>
                  <Link
                    to={"/live/tasks/" + session.task_id}
                    className="font-mono text-[11px] underline-offset-4 hover:underline"
                  >
                    {session.task_id}
                  </Link>
                </div>
              ) : null}

              <Tabs defaultValue="turns">
                <TabsList variant="line">
                  <TabsTrigger value="turns" className="px-2.5">
                    轮次 ({turns.length})
                  </TabsTrigger>
                  <TabsTrigger value="events" className="px-2.5">
                    事件 ({events.length})
                  </TabsTrigger>
                </TabsList>
                <TabsContent value="turns" className="mt-3">
                  <LiveTurnList turns={turns} emptyHint="该会话还没有轮次。" />
                </TabsContent>
                <TabsContent value="events" className="mt-3">
                  <LiveEventTimeline events={events} emptyHint="该会话还没有上报事件。" />
                </TabsContent>
              </Tabs>
            </div>
          ) : (
            <EmptyState icon={Server} title="会话不存在" hint="会话可能已被清理，或链接来自旧快照。" />
          )}
        </div>

        <div className="border-t px-4 py-3 text-[11px] leading-relaxed text-muted-foreground">
          每 3 秒刷新；关闭面板不会停止刷新列表。
        </div>
      </SheetContent>
    </Sheet>
  );
}
