import type * as React from "react";
import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowLeft,
  CheckCheck,
  Loader2,
  MessageSquare,
  RefreshCw,
  Server,
  XCircle,
} from "lucide-react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
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
import { SessionStatusBadge, TaskStatusBadge } from "@/components/status-badge";
import { LiveGate } from "@/live/live-gate";
import { LiveArtifactPanel } from "@/live/live-artifacts";
import { EmptyState, InfoBanner, KeyRow, Meta } from "@/live/live-shared";
import {
  commandKindName,
  LiveCommandList,
  LiveEventTimeline,
  LiveTurnList,
} from "@/live/live-timeline";
import {
  errorMessage,
  formatDateTime,
  formatRelative,
  isTerminalStatus,
  liveApi,
  type LiveTask,
} from "@/live/api";
import { evidenceLabel, modeLabel, sourceLabel } from "@/lib/status";

export function LiveTaskDetailPage() {
  return (
    <LiveGate>
      <LiveTaskDetailContent />
    </LiveGate>
  );
}

interface Notice {
  tone: "success" | "danger";
  text: string;
}

function LiveTaskDetailContent() {
  const params = useParams();
  // 兼容 Lead 接线的两种参数命名：/live/tasks/:taskId 或 /live/tasks/:id。
  const taskId = params.taskId ?? params.id;
  const queryClient = useQueryClient();
  const [notice, setNotice] = useState<Notice | null>(null);
  const [followUpOpen, setFollowUpOpen] = useState(false);
  const [cancelOpen, setCancelOpen] = useState(false);

  const detailQuery = useQuery({
    queryKey: ["live", "task", taskId],
    queryFn: () => liveApi.getTask(taskId as string),
    refetchInterval: 3000,
    retry: false,
    enabled: Boolean(taskId),
  });
  const devicesQuery = useQuery({
    queryKey: ["live", "devices"],
    queryFn: liveApi.devices,
    refetchInterval: 15000,
    retry: false,
  });

  const deviceName = (id: string) =>
    devicesQuery.data?.devices.find((device) => device.id === id)?.name ?? id;

  if (detailQuery.isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-72 w-full" />
      </div>
    );
  }

  if (detailQuery.isError || !detailQuery.data) {
    return (
      <div className="mx-auto mt-10 max-w-lg">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <Server className="size-4 text-rose-500" />
              无法读取任务
            </CardTitle>
            <CardDescription>{errorMessage(detailQuery.error)}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={() => void detailQuery.refetch()}>
              <RefreshCw className="size-4" />
              重试
            </Button>
            <Button asChild variant="ghost">
              <Link to="/live/tasks">
                <ArrowLeft className="size-4" />
                返回任务列表
              </Link>
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  const { task, session } = detailQuery.data;
  const turns = detailQuery.data.turns ?? [];
  const artifacts = detailQuery.data.artifacts ?? [];
  const commands = detailQuery.data.commands ?? [];
  const events = detailQuery.data.events ?? [];

  const terminal = isTerminalStatus(task.status);
  const managed = task.source === "managed" && (session?.mode ?? task.session_mode ?? "managed") === "managed";
  const canFollowUp = managed && !terminal && task.status !== "cancel_requested";
  const canCancel = ["queued", "running", "waiting_input"].includes(task.status);
  const terminalSnapshots = events.filter((event) => event.type === "terminal.snapshot");
  const latestSnapshot = terminalSnapshots[terminalSnapshots.length - 1];
  const snapshotText = typeof latestSnapshot?.payload?.text === "string" ? latestSnapshot.payload.text : "";
  const failedCommand = commands.find((command) => command.status === "rejected" || command.status === "expired");
  const pendingCommand = commands.find((command) => command.status === "pending");

  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["live", "task", task.id] }),
      queryClient.invalidateQueries({ queryKey: ["live", "tasks"] }),
      queryClient.invalidateQueries({ queryKey: ["live", "sessions"] }),
    ]);
  };

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        backTo="/live/tasks"
        backLabel="返回任务列表"
        title={
          <span className="flex flex-wrap items-center gap-2">
            {task.title}
            <TaskStatusBadge status={task.status} />
          </span>
        }
        description={
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <Badge variant="outline" className="font-normal">
              {sourceLabel[task.source]}
            </Badge>
            <Link
              to={"/live/devices/" + task.device_id}
              className="text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
            >
              {deviceName(task.device_id)}
            </Link>
            <span className="text-muted-foreground/40">·</span>
            <span className="font-mono text-[11px] text-muted-foreground">{task.id}</span>
            <span className="text-muted-foreground/40">·</span>
            <span className="tabular-nums">更新 {formatRelative(task.updated_at)}</span>
          </span>
        }
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => void detailQuery.refetch()}>
              <RefreshCw className={"size-3.5 " + (detailQuery.isFetching ? "animate-spin" : "")} />
              刷新
            </Button>
            <FollowUpDialog
              open={followUpOpen}
              onOpenChange={setFollowUpOpen}
              task={task}
              disabled={!canFollowUp}
              onDone={(text) => {
                setNotice({ tone: "success", text });
                void refresh();
              }}
            />
            <CancelDialog
              open={cancelOpen}
              onOpenChange={setCancelOpen}
              task={task}
              disabled={!canCancel}
              onDone={(text) => {
                setNotice({ tone: "success", text });
                void refresh();
              }}
            />
          </>
        }
      />

      {notice ? (
        <InfoBanner tone={notice.tone} title={notice.tone === "success" ? "操作已提交" : "操作失败"}>
          {notice.text}
        </InfoBanner>
      ) : null}

      {task.status === "queued" ? (
        <InfoBanner tone="info" title="指令已持久化，尚未开始执行">
          {task.stage || "等待 Edge 校验能力、目录与并发后 ACK；重复投递由 command_id 去重。"}
        </InfoBanner>
      ) : null}

      {task.status === "cancel_requested" ? (
        <InfoBanner tone="warning" title="取消已记录，等待 Edge 确认">
          {task.stage || "Edge 确认后任务转为 cancelled；若指令尚未投递则会被直接撤销。"}
        </InfoBanner>
      ) : null}

      {task.status === "needs_review" ? (
        <InfoBanner tone="warning" title="结果待确认">
          {task.evidence_note ?? "缺少业务完成证据，不自动认定成功。"}
        </InfoBanner>
      ) : null}

      {task.status === "failed" ? (
        <InfoBanner tone="danger" title="任务失败">
          <span className="space-y-1">
            <span className="block">{task.stage || "Edge 上报失败。"}</span>
            {failedCommand ? (
              <span className="block">
                {commandKindName(failedCommand.kind)}指令{failedCommand.status === "expired" ? "已过期" : "被拒绝"}
                {failedCommand.detail || failedCommand.error ? "：" + (failedCommand.error ?? failedCommand.detail) : "。"}
              </span>
            ) : null}
          </span>
        </InfoBanner>
      ) : null}

      {task.status !== "failed" && failedCommand ? (
        <InfoBanner
          tone="warning"
          title={"存在" + (failedCommand.status === "expired" ? "已过期" : "被拒绝") + "的指令"}
        >
          {commandKindName(failedCommand.kind)}指令 {failedCommand.id}
          {failedCommand.detail || failedCommand.error ? "：" + (failedCommand.error ?? failedCommand.detail) : "。"}
        </InfoBanner>
      ) : null}

      {pendingCommand && task.status === "queued" ? (
        <p className="text-[11px] text-muted-foreground">
          指令 {pendingCommand.id} 尚未投递；设备重连并通过 hello 后补发。
        </p>
      ) : null}

      <Card className="gap-0 py-0">
        <div className="grid grid-cols-2 gap-x-4 gap-y-4 px-5 py-4 sm:grid-cols-3 lg:grid-cols-6">
          <Meta label="设备" value={<Link to={"/live/devices/" + task.device_id} className="hover:underline">{deviceName(task.device_id)}</Link>} />
          <Meta label="来源" value={sourceLabel[task.source]} />
          <Meta label="阶段" value={task.stage || "—"} />
          <Meta label="会话" value={
            session ? (
              <Link to={"/live/sessions?session=" + session.id} className="font-mono text-xs hover:underline">
                {session.id}
              </Link>
            ) : (
              task.session_id ?? "—"
            )
          } mono />
          <Meta label="轮次" value={String(turns.length)} />
          <Meta label="更新时间" value={formatDateTime(task.updated_at)} mono />
        </div>
      </Card>

      <div className="grid items-start gap-4 xl:grid-cols-[minmax(0,1fr)_340px]">
        <Card className="gap-0 overflow-hidden py-0">
          <Tabs defaultValue="timeline">
            <div className="border-b px-5 pt-4">
              <TabsList variant="line" className="flex-wrap">
                <TabsTrigger value="timeline">时间线 ({events.length})</TabsTrigger>
                <TabsTrigger value="turns">轮次 ({turns.length})</TabsTrigger>
                <TabsTrigger value="commands">指令 ({commands.length})</TabsTrigger>
                <TabsTrigger value="artifacts">工件 ({artifacts.length})</TabsTrigger>
                <TabsTrigger value="terminal">终端 ({terminalSnapshots.length})</TabsTrigger>
              </TabsList>
            </div>
            <TabsContent value="timeline" className="mt-0 max-h-[560px] overflow-y-auto p-5">
              <LiveEventTimeline events={events} />
            </TabsContent>
            <TabsContent value="turns" className="mt-0 max-h-[560px] overflow-y-auto p-5">
              <LiveTurnList turns={turns} emptyHint="任务开始执行后会生成轮次；追问会追加新轮次。" />
            </TabsContent>
            <TabsContent value="commands" className="mt-0 max-h-[560px] overflow-y-auto p-5">
              <LiveCommandList commands={commands} emptyHint="中心尚未为该任务创建指令。" />
            </TabsContent>
            <TabsContent value="artifacts" className="mt-0 max-h-[720px] overflow-y-auto p-5">
              <LiveArtifactPanel artifacts={artifacts} />
            </TabsContent>
            <TabsContent value="terminal" className="mt-0 max-h-[560px] overflow-y-auto p-5">
              {snapshotText ? (
                <pre className="overflow-x-auto whitespace-pre-wrap break-words rounded-lg border bg-black/90 p-3 font-mono text-[11px] leading-relaxed text-zinc-100">
                  {snapshotText}
                </pre>
              ) : (
                <EmptyState title="暂无终端快照" hint="只有 Herdr 运行时会上报终端屏幕快照；headless 运行时使用时间线中的流式事件。" />
              )}
            </TabsContent>
          </Tabs>
        </Card>

        <div className="flex flex-col gap-4">
          <Card className="gap-0 py-0">
            <CardHeader className="px-5 pt-5">
              <CardTitle className="text-sm">完成依据</CardTitle>
              <CardDescription>成功不代表本轮结束；依据来源可查看。</CardDescription>
            </CardHeader>
            <CardContent className="px-5 pb-5">
              {task.evidence_kind ? (
                <div className="flex items-start gap-2">
                  <CheckCheck className="mt-0.5 size-4 shrink-0 text-emerald-500" />
                  <div>
                    <div className="text-sm font-medium">{evidenceLabel[task.evidence_kind]}</div>
                    <p className="mt-0.5 break-words text-[11px] leading-relaxed text-muted-foreground">
                      {task.evidence_note ?? "—"}
                    </p>
                  </div>
                </div>
              ) : task.status === "needs_review" ? (
                <div className="flex items-start gap-2">
                  <AlertTriangle className="mt-0.5 size-4 shrink-0 text-amber-500" />
                  <div>
                    <div className="text-sm font-medium">无完成证据</div>
                    <p className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">
                      {task.evidence_note ?? "仅收到本轮结束信号，不能证明业务目标完成，等待人工确认。"}
                    </p>
                  </div>
                </div>
              ) : task.status === "failed" ? (
                <div className="flex items-start gap-2">
                  <XCircle className="mt-0.5 size-4 shrink-0 text-rose-500" />
                  <div>
                    <div className="text-sm font-medium">执行失败</div>
                    <p className="mt-0.5 break-words text-[11px] leading-relaxed text-muted-foreground">
                      {task.evidence_note ?? task.stage ?? "—"}
                    </p>
                  </div>
                </div>
              ) : (
                <div className="flex items-start gap-2">
                  <AlertTriangle className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  <div>
                    <div className="text-sm font-medium">
                      {terminal ? "无可核对依据" : "尚未产生完成依据"}
                    </div>
                    <p className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">
                      {task.evidence_note ?? "任务仍在进行，完成前不标记成功。"}
                    </p>
                  </div>
                </div>
              )}
            </CardContent>
          </Card>

          <Card className="gap-0 py-0">
            <CardHeader className="px-5 pt-5">
              <CardTitle className="text-sm">会话</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2.5 px-5 pb-5 text-xs">
              {session ? (
                <>
                  <KeyRow label="会话 ID">
                    <Link to={"/live/sessions?session=" + session.id} className="font-mono text-[11px] hover:underline">
                      {session.id}
                    </Link>
                  </KeyRow>
                  <KeyRow label="模式">
                    <Badge variant={session.mode === "managed" ? "secondary" : "outline"} className="font-normal">
                      {modeLabel[session.mode]}
                    </Badge>
                  </KeyRow>
                  <KeyRow label="状态">
                    <SessionStatusBadge status={session.status} />
                  </KeyRow>
                  <KeyRow label="harness_session_id">
                    <span className="font-mono text-[11px]">{session.harness_session_id ?? "—"}</span>
                  </KeyRow>
                  <KeyRow label="最近活动">
                    <span className="tabular-nums">{formatRelative(session.last_activity_at)}</span>
                  </KeyRow>
                </>
              ) : (
                <p className="text-[11px] text-muted-foreground">该任务尚未绑定会话。</p>
              )}
            </CardContent>
          </Card>

          <Card className="gap-0 py-0">
            <CardHeader className="px-5 pt-5">
              <CardTitle className="text-sm">任务信息</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2.5 px-5 pb-5 text-xs">
              <KeyRow label="工作目录">
                <span className="font-mono text-[11px]" title={task.cwd}>{task.cwd || "—"}</span>
              </KeyRow>
              <KeyRow label="提交时间">
                <span className="font-mono text-[11px]">{formatDateTime(task.created_at)}</span>
              </KeyRow>
              <KeyRow label="更新时间">
                <span className="font-mono text-[11px]">{formatDateTime(task.updated_at)}</span>
              </KeyRow>
              {session?.harness_session_id ? (
                <KeyRow label="harness_session_id">
                  <span className="font-mono text-[11px]">{session.harness_session_id}</span>
                </KeyRow>
              ) : null}
              <Separator />
              <div>
                <div className="mb-1 text-[11px] text-muted-foreground">原始指令</div>
                <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted/40 p-2.5 text-[11px] leading-relaxed">
                  {task.request}
                </pre>
              </div>
            </CardContent>
          </Card>

          <div className="rounded-lg border border-dashed px-4 py-3 text-[11px] leading-relaxed text-muted-foreground">
            <div className="mb-1 font-medium text-foreground">状态口径</div>
            取消先记录为 cancel_requested，由 Edge 确认后才是 cancelled；设备离线、指令过期与任务失败分开表示。
          </div>
        </div>
      </div>
    </div>
  );
}

function FollowUpDialog({
  open,
  onOpenChange,
  task,
  disabled,
  onDone,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  task: LiveTask;
  disabled: boolean;
  onDone: (text: string) => void;
}) {
  const [draft, setDraft] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const close = () => {
    onOpenChange(false);
    setDraft("");
    setError(null);
  };

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (pending || draft.trim().length === 0) return;
    setPending(true);
    setError(null);
    try {
      await liveApi.addTurn(task.id, draft.trim());
      setDraft("");
      close();
      onDone("追问已排队；同一会话的输入串行执行，上一轮结束后才会开始。");
    } catch (followUpError) {
      setError(errorMessage(followUpError));
    } finally {
      setPending(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (next) onOpenChange(true);
        else close();
      }}
    >
      <DialogTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          disabled={disabled}
          title={disabled ? "仅托管且未结束的任务可追问" : "向同一会话追加一轮输入"}
        >
          <MessageSquare className="size-3.5" />
          追问
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>给会话追加一轮输入</DialogTitle>
          <DialogDescription>
            输入会序列化到同一会话；上一轮未完成时中心返回 409 turn_in_progress。
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="space-y-3">
          <Textarea
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            placeholder="例如：先只跑前 2 条，输出到 transcripts/ 目录。"
            rows={4}
            className="resize-none"
          />
          {error ? (
            <p className="rounded-md bg-rose-50 px-3 py-2 text-xs leading-relaxed text-rose-700 dark:bg-rose-500/10 dark:text-rose-400">
              {error}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={close}>
              取消
            </Button>
            <Button type="submit" disabled={pending || draft.trim().length === 0}>
              {pending ? <Loader2 className="size-4 animate-spin" /> : null}
              {pending ? "提交中…" : "提交追问"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function CancelDialog({
  open,
  onOpenChange,
  task,
  disabled,
  onDone,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  task: LiveTask;
  disabled: boolean;
  onDone: (text: string) => void;
}) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const close = () => {
    onOpenChange(false);
    setError(null);
  };

  const submit = async () => {
    if (pending) return;
    setPending(true);
    setError(null);
    try {
      const cancelled = await liveApi.cancelTask(task.id);
      close();
      onDone(
        cancelled.cancelled_immediately
          ? "指令尚未投递，任务已直接取消。"
          : "取消请求已记录为 cancel_requested，等待 Edge 确认后转为 cancelled。"
      );
    } catch (cancelError) {
      setError(errorMessage(cancelError));
    } finally {
      setPending(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (next) onOpenChange(true);
        else close();
      }}
    >
      <DialogTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          disabled={disabled}
          title={disabled ? "仅排队、运行中或等待输入的任务可取消" : "记录取消请求"}
        >
          <XCircle className="size-3.5" />
          取消任务
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>确认取消该任务？</DialogTitle>
          <DialogDescription>
            取消请求会先记录为 cancel_requested，由 Edge 确认后才转为 cancelled；后台作业可能需要单独取消。
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <p className="rounded-md bg-rose-50 px-3 py-2 text-xs leading-relaxed text-rose-700 dark:bg-rose-500/10 dark:text-rose-400">
            {error}
          </p>
        ) : null}
        <DialogFooter>
          <Button variant="outline" onClick={close}>
            保留任务
          </Button>
          <Button variant="destructive" onClick={() => void submit()} disabled={pending}>
            {pending ? <Loader2 className="size-4 animate-spin" /> : null}
            {pending ? "提交中…" : "提交取消请求"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}