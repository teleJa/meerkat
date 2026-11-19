import type * as React from "react";
import { useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Copy, Loader2, Plus, RefreshCw, Search, Send, Server } from "lucide-react";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
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
import { TaskStatusBadge } from "@/components/status-badge";
import { LiveGate } from "@/live/live-gate";
import { EmptyState, InfoBanner } from "@/live/live-shared";
import {
  errorMessage,
  formatDateTime,
  formatRelative,
  liveApi,
  type CreateTaskResponse,
  type LiveDevice,
} from "@/live/api";
import { sourceLabel } from "@/lib/status";

const statusFilters = [
  { value: "all", label: "全部" },
  { value: "running", label: "运行中" },
  { value: "waiting", label: "等待处理" },
  { value: "failed", label: "失败" },
  { value: "done", label: "已完成" },
] as const;

const waitingStatuses = ["queued", "waiting_input", "needs_review", "cancel_requested"];
const doneStatuses = ["succeeded", "cancelled"];

export function LiveTasksPage() {
  return (
    <LiveGate>
      <LiveTasksContent />
    </LiveGate>
  );
}

function LiveTasksContent() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<string>("all");
  const [deviceId, setDeviceId] = useState<string>("all");
  const [query, setQuery] = useState("");

  const tasksQuery = useQuery({
    queryKey: ["live", "tasks", 100],
    queryFn: () => liveApi.listTasks({ limit: 100 }),
    refetchInterval: 5000,
    retry: false,
  });
  const devicesQuery = useQuery({
    queryKey: ["live", "devices"],
    queryFn: liveApi.devices,
    refetchInterval: 15000,
    retry: false,
  });

  const tasks = tasksQuery.data?.tasks ?? [];
  const devices = devicesQuery.data?.devices ?? [];

  const filtered = useMemo(() => {
    const keyword = query.trim().toLowerCase();
    return tasks.filter((task) => {
      if (status === "running" && task.status !== "running") return false;
      if (status === "failed" && task.status !== "failed") return false;
      if (status === "waiting" && !waitingStatuses.includes(task.status)) return false;
      if (status === "done" && !doneStatuses.includes(task.status)) return false;
      if (deviceId !== "all" && task.device_id !== deviceId) return false;
      if (keyword.length > 0) {
        const haystack = [task.title, task.id, task.cwd, task.request]
          .join("\n")
          .toLowerCase();
        if (!haystack.includes(keyword)) return false;
      }
      return true;
    });
  }, [tasks, status, deviceId, query]);

  const deviceName = (id: string) => devices.find((device) => device.id === id)?.name ?? id;

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="实时任务"
        description={
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span>派发指令给已接入的 Edge，并跟踪真实执行状态</span>
            <span className="text-muted-foreground/40">|</span>
            <span>每 5 秒刷新</span>
            <span className="text-muted-foreground/40">|</span>
            <span className="tabular-nums">
              最后同步 {formatRelative(tasksQuery.dataUpdatedAt > 0 ? new Date(tasksQuery.dataUpdatedAt).toISOString() : null)}
            </span>
          </span>
        }
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => void tasksQuery.refetch()}>
              <RefreshCw className={"size-3.5 " + (tasksQuery.isFetching ? "animate-spin" : "")} />
              刷新
            </Button>
            <DispatchTaskDialog devices={devices} devicesLoading={devicesQuery.isLoading} />
          </>
        }
      />

      {tasksQuery.isError ? (
        <InfoBanner
          tone={tasks.length > 0 ? "warning" : "danger"}
          title={tasks.length > 0 ? "刷新失败，正在显示上次数据" : "读取任务失败"}
        >
          {errorMessage(tasksQuery.error)}
        </InfoBanner>
      ) : null}

      <Card className="gap-0 overflow-hidden py-0">
        <div className="flex flex-col gap-3 border-b px-4 py-3 lg:flex-row lg:items-center lg:justify-between">
          <Tabs value={status} onValueChange={setStatus}>
            <TabsList variant="line">
              {statusFilters.map((tab) => (
                <TabsTrigger key={tab.value} value={tab.value} className="px-2.5">
                  {tab.label}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <div className="relative">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="搜索标题、ID、目录或指令"
                className="h-8 w-full pl-8 text-xs sm:w-56"
              />
            </div>
            <Select value={deviceId} onValueChange={setDeviceId}>
              <SelectTrigger className="h-8 w-full text-xs sm:w-44">
                <SelectValue placeholder="设备" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部设备</SelectItem>
                {devices.map((device) => (
                  <SelectItem key={device.id} value={device.id}>
                    {device.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <span className="text-xs tabular-nums text-muted-foreground">
              {filtered.length} / {tasks.length}
            </span>
          </div>
        </div>

        {tasksQuery.isLoading ? (
          <div className="space-y-3 px-4 py-4">
            {[0, 1, 2, 3, 4].map((row) => (
              <div key={row} className="flex items-center gap-4">
                <Skeleton className="h-8 flex-1" />
                <Skeleton className="hidden h-6 w-20 sm:block" />
                <Skeleton className="h-6 w-16" />
                <Skeleton className="hidden h-6 w-32 lg:block" />
              </div>
            ))}
          </div>
        ) : tasksQuery.isError && tasks.length === 0 ? (
          <div className="p-4">
            <EmptyState
              icon={Server}
              title="无法读取任务"
              hint="中心未就绪或网络异常；修复后可点「刷新」重试，页面其余部分仍可用。"
              action={
                <Button variant="outline" size="sm" onClick={() => void tasksQuery.refetch()}>
                  <RefreshCw className="size-3.5" />
                  重试
                </Button>
              }
            />
          </div>
        ) : tasks.length === 0 ? (
          <div className="p-4">
            <EmptyState
              icon={Send}
              title="还没有任务"
              hint="选择一台在线设备派发第一条指令；指令会持久化，设备离线时排队等待重连补发。"
              action={
                <DispatchTaskDialog
                  devices={devices}
                  devicesLoading={devicesQuery.isLoading}
                  trigger={
                    <Button size="sm" data-testid="dispatch-open">
                      <Plus className="size-3.5" />
                      派发任务
                    </Button>
                  }
                />
              }
            />
          </div>
        ) : filtered.length === 0 ? (
          <p className="px-4 py-12 text-center text-sm text-muted-foreground">没有符合筛选条件的任务</p>
        ) : (
          <Table className="table-fixed">
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="w-auto pl-4">任务</TableHead>
                <TableHead className="hidden w-[120px] md:table-cell">设备</TableHead>
                <TableHead className="w-[104px]">状态</TableHead>
                <TableHead className="hidden w-[220px] xl:table-cell">阶段</TableHead>
                <TableHead className="w-[96px] pr-4 text-right">更新</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {filtered.map((task) => (
                <TableRow
                  key={task.id}
                  className="cursor-pointer"
                  onClick={() => navigate("/live/tasks/" + task.id)}
                >
                  <TableCell className="max-w-[380px] pl-4">
                    <div className="truncate text-sm font-medium">{task.title}</div>
                    <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[11px] text-muted-foreground">
                      <span className="shrink-0 font-mono">{task.id}</span>
                      <span className="shrink-0 text-muted-foreground/40">·</span>
                      <span className="shrink-0">{sourceLabel[task.source]}</span>
                      <span className="shrink-0 text-muted-foreground/40">·</span>
                      <span className="truncate">{task.cwd || "—"}</span>
                    </div>
                  </TableCell>
                  <TableCell className="hidden whitespace-nowrap text-xs text-muted-foreground md:table-cell">
                    {deviceName(task.device_id)}
                  </TableCell>
                  <TableCell>
                    <TaskStatusBadge status={task.status} />
                  </TableCell>
                  <TableCell className="hidden xl:table-cell">
                    <span className="block truncate text-xs text-muted-foreground">{task.stage || "—"}</span>
                  </TableCell>
                  <TableCell className="pr-4 text-right text-xs tabular-nums text-muted-foreground">
                    <span title={formatDateTime(task.updated_at)}>{formatRelative(task.updated_at)}</span>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Card>

      <div className="rounded-lg border bg-muted/30 px-4 py-3 text-[11px] leading-relaxed text-muted-foreground">
        状态口径：queued 表示指令已持久化但 Edge 尚未执行；cancel_requested 表示取消已记录、等待
        Edge 确认；只有成功终态才视为完成，失败与过期会写明原因。
      </div>
    </div>
  );
}

function DispatchTaskDialog({
  devices,
  devicesLoading,
  trigger,
}: {
  devices: LiveDevice[];
  devicesLoading: boolean;
  trigger?: React.ReactNode;
}) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [deviceId, setDeviceId] = useState("");
  const [cwd, setCwd] = useState("");
  const [title, setTitle] = useState("");
  const [request, setRequest] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [receipt, setReceipt] = useState<CreateTaskResponse | null>(null);

  const selectedDevice = devices.find((device) => device.id === deviceId);
  const canSubmit =
    request.trim().length > 0 && deviceId.length > 0 && cwd.trim().length > 0 && !pending;

  const reset = () => {
    setDeviceId("");
    setCwd("");
    setTitle("");
    setRequest("");
    setError(null);
    setReceipt(null);
    setPending(false);
  };

  const onOpenChange = (next: boolean) => {
    setOpen(next);
    if (!next) reset();
  };

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!canSubmit) return;
    setPending(true);
    setError(null);
    try {
      const created = await liveApi.createTask({
        request: request.trim(),
        device_id: deviceId,
        cwd: cwd.trim(),
        ...(title.trim().length > 0 ? { title: title.trim() } : {}),
      });
      setReceipt(created);
      await queryClient.invalidateQueries({ queryKey: ["live", "tasks"] });
    } catch (createError) {
      setError(errorMessage(createError));
    } finally {
      setPending(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        {trigger ?? (
          <Button size="sm" data-testid="dispatch-open">
            <Send className="size-3.5" />
            派发任务
          </Button>
        )}
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>派发任务</DialogTitle>
          <DialogDescription>
            指令会持久化后经 WebSocket 投递给 Edge；设备离线时保持 pending，重连后补发。
          </DialogDescription>
        </DialogHeader>

        {receipt ? (
          <div className="space-y-4">
            <div className="flex items-start gap-2 rounded-lg border border-emerald-200 bg-emerald-50 px-3 py-2.5 text-xs dark:border-emerald-500/25 dark:bg-emerald-500/10">
              <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-emerald-600 dark:text-emerald-400" />
              <div className="min-w-0 space-y-1">
                <div className="font-medium text-emerald-800 dark:text-emerald-300">指令已受理</div>
                <p className="leading-relaxed text-emerald-700 dark:text-emerald-400">
                  task 状态为 {receipt.task?.status ?? "queued"}；等待 Edge ACK 后转为运行时状态。
                </p>
              </div>
            </div>
            <div className="space-y-2 rounded-lg border px-3 py-2.5 text-xs">
              <ReceiptRow label="task_id" value={receipt.task?.id ?? "—"} />
              <ReceiptRow label="command_id" value={receipt.command_id} />
              <ReceiptRow label="session_id" value={receipt.session_id} />
            </div>
            <DialogFooter className="gap-2 sm:justify-between">
              <Button
                variant="outline"
                onClick={() => {
                  setReceipt(null);
                  setRequest("");
                  setTitle("");
                }}
              >
                继续派发
              </Button>
              <Button
                onClick={() => {
                  setOpen(false);
                  reset();
                  if (receipt.task?.id) navigate("/live/tasks/" + receipt.task.id);
                }}
              >
                查看任务详情
              </Button>
            </DialogFooter>
          </div>
        ) : (
          <form onSubmit={submit} className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="dispatch-device">目标设备</Label>
              <Select value={deviceId} onValueChange={setDeviceId}>
                <SelectTrigger id="dispatch-device" className="w-full">
                  <SelectValue placeholder={devicesLoading ? "正在读取设备…" : "选择已接入的设备"} />
                </SelectTrigger>
                <SelectContent>
                  {devices.length === 0 ? (
                    <SelectItem value="__none" disabled>
                      暂无已接入设备
                    </SelectItem>
                  ) : (
                    devices.map((device) => (
                      <SelectItem key={device.id} value={device.id}>
                        {device.name} · {device.hostname}（{device.status === "online" ? "在线" : device.status === "stale" ? "状态陈旧" : "离线"}）
                      </SelectItem>
                    ))
                  )}
                </SelectContent>
              </Select>
              {selectedDevice && selectedDevice.status !== "online" ? (
                <p className="text-[11px] leading-relaxed text-amber-600 dark:text-amber-400">
                  该设备当前{selectedDevice.status === "stale" ? "心跳陈旧" : "离线"}：指令会保持
                  pending，设备重连并通过 hello 后补发。
                </p>
              ) : null}
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="dispatch-cwd">工作目录</Label>
              <Input
                id="dispatch-cwd"
                value={cwd}
                onChange={(event) => setCwd(event.target.value)}
                placeholder="/Users/you/workspace/project"
                autoComplete="off"
                spellCheck={false}
                required
              />
              <p className="text-[11px] text-muted-foreground">
                必填：Edge 会校验目录存在且在允许根目录（默认用户 home）下，不合法时返回 rejected:
                cwd_not_allowed。
              </p>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="dispatch-title">标题（可选）</Label>
              <Input
                id="dispatch-title"
                value={title}
                onChange={(event) => setTitle(event.target.value)}
                placeholder="留空则取指令首行"
                autoComplete="off"
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="dispatch-request">指令</Label>
              <Textarea
                id="dispatch-request"
                value={request}
                onChange={(event) => setRequest(event.target.value)}
                placeholder="例如：请回复 OK，并说明当前目录。"
                rows={5}
                className="resize-none"
              />
            </div>

            {error ? (
              <p className="rounded-md bg-rose-50 px-3 py-2 text-xs leading-relaxed text-rose-700 dark:bg-rose-500/10 dark:text-rose-400">
                {error}
              </p>
            ) : null}

            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                取消
              </Button>
              <Button type="submit" disabled={!canSubmit}>
                {pending ? <Loader2 className="size-4 animate-spin" /> : null}
                {pending ? "提交中…" : "派发"}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

function ReceiptRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <span className="shrink-0 font-mono text-[11px] text-muted-foreground">{label}</span>
      <span className="flex min-w-0 items-center gap-1.5">
        <span className="truncate font-mono text-[11px]">{value}</span>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="size-6 shrink-0 text-muted-foreground"
          aria-label={"复制 " + label}
          onClick={() => {
            void navigator.clipboard?.writeText(value);
          }}
        >
          <Copy className="size-3" />
        </Button>
      </span>
    </div>
  );
}
