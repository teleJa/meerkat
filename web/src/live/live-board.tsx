import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Link, useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { ArrowRight, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { PageHeader } from "@/components/page-header";
import { TaskStatusBadge } from "@/components/status-badge";
import { LiveGate } from "@/live/live-gate";
import { EmptyState, InfoBanner } from "@/live/live-shared";
import { errorMessage, formatRelative, liveApi, type LiveTask } from "@/live/api";
import { TaskPoolBoard } from "@/live/live-task-pool";

const columns: { key: string; label: string; hint: string; statuses: LiveTask["status"][] }[] = [
  { key: "queued", label: "待处理", hint: "指令已排队，等待 Edge 执行", statuses: ["queued"] },
  { key: "running", label: "进行中", hint: "Edge 正在执行", statuses: ["running", "waiting_input", "cancel_requested"] },
  { key: "done", label: "已完成", hint: "成功结束的任务", statuses: ["succeeded", "needs_review"] },
  { key: "failed", label: "失败 / 取消", hint: "需要关注或重试", statuses: ["failed", "cancelled"] },
];

export function LiveBoardPage() {
  return (
    <LiveGate>
      <Tabs defaultValue="pool" className="gap-4">
        <TabsList aria-label="看板视图">
          <TabsTrigger value="devices">设备执行</TabsTrigger>
          <TabsTrigger value="pool">任务池</TabsTrigger>
        </TabsList>
        <TabsContent value="pool"><TaskPoolBoard /></TabsContent>
        <TabsContent value="devices"><LiveBoardContent /></TabsContent>
      </Tabs>
    </LiveGate>
  );
}

function LiveBoardContent() {
  const navigate = useNavigate();
  const tasksQuery = useQuery({
    queryKey: ["live", "tasks", "board"],
    queryFn: () => liveApi.listTasks({ limit: 200 }),
    refetchInterval: 5000,
    retry: false,
  });
  const devicesQuery = useQuery({ queryKey: ["live", "devices"], queryFn: liveApi.devices, refetchInterval: 15000, retry: false });

  const tasks = tasksQuery.data?.tasks ?? [];
  const devices = devicesQuery.data?.devices ?? [];
  const deviceName = (id: string) => devices.find((device) => device.id === id)?.name ?? id;

  if (tasksQuery.isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-56" />
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
          <Skeleton className="h-72" />
          <Skeleton className="h-72" />
          <Skeleton className="h-72" />
          <Skeleton className="h-72" />
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <PageHeader
        title="设备执行记录"
        description="按状态查看所有设备上的任务；点击卡片查看任务详情。"
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => void tasksQuery.refetch()}>
              <RefreshCw className={"size-3.5 " + (tasksQuery.isFetching ? "animate-spin" : "")} />
              刷新
            </Button>
            <Button size="sm" asChild>
              <Link to="/live/harness">打开 DSH</Link>
            </Button>
          </>
        }
      />
      {tasksQuery.isError ? (
        <InfoBanner tone="danger" title="读取任务失败">
          {errorMessage(tasksQuery.error)}
        </InfoBanner>
      ) : null}
      {tasks.length === 0 ? (
        <EmptyState
          title="还没有任务"
          hint="从「派发任务」创建任务后，任务会出现在看板上。"
          action={
            <Button asChild size="sm">
              <Link to="/live/tasks">去派发任务</Link>
            </Button>
          }
        />
      ) : (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
          {columns.map((column) => {
            const items = tasks
              .filter((task) => column.statuses.includes(task.status))
              .sort((a, b) => b.updated_at.localeCompare(a.updated_at));
            return (
              <section key={column.key} className="flex min-h-[260px] flex-col rounded-xl border bg-card">
                <div className="flex items-center justify-between gap-2 border-b px-3 py-2.5">
                  <div>
                    <div className="text-xs font-medium">{column.label}</div>
                    <div className="mt-0.5 text-[10px] text-muted-foreground">{column.hint}</div>
                  </div>
                  <span className="rounded-full bg-muted px-2 py-0.5 text-[10px] tabular-nums text-muted-foreground">{items.length}</span>
                </div>
                <div className="flex-1 space-y-2 p-2">
                  {items.length === 0 ? (
                    <p className="px-2 py-8 text-center text-[11px] text-muted-foreground">暂无任务</p>
                  ) : (
                    items.map((task) => (
                      <button
                        key={task.id}
                        type="button"
                        onClick={() => navigate("/live/tasks/" + task.id)}
                        className="w-full rounded-lg border bg-background p-3 text-left transition-colors hover:bg-accent"
                      >
                        <div className="flex items-start justify-between gap-2">
                          <div className="min-w-0 flex-1">
                            <div className="line-clamp-2 text-xs font-medium">{task.title}</div>
                            <div className="mt-1 truncate text-[10px] text-muted-foreground">{deviceName(task.device_id)} · {formatRelative(task.updated_at)}</div>
                          </div>
                          <span className="shrink-0 scale-90"><TaskStatusBadge status={task.status} /></span>
                        </div>
                        <div className="mt-2 truncate rounded bg-muted/40 px-2 py-1 font-mono text-[10px] text-muted-foreground" title={task.cwd}>
                          {task.cwd}
                        </div>
                        <div className="mt-2 flex items-center justify-between text-[10px] text-muted-foreground">
                          <span className="truncate font-mono">{task.id}</span>
                          <span className="flex items-center gap-1">打开 <ArrowRight className="size-3" /></span>
                        </div>
                      </button>
                    ))
                  )}
                </div>
              </section>
            );
          })}
        </div>
      )}
    </div>
  );
}
