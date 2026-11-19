import {
  Activity,
  AlertTriangle,
  CheckCheck,
  CheckCircle2,
  FileText,
  KeyRound,
  MessageSquare,
  Play,
  Send,
  TerminalSquare,
  Wrench,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { StatusBadge } from "@/components/status-badge";
import { cn } from "@/lib/utils";
import { EmptyState } from "@/live/live-shared";
import { formatClock, formatDateTime } from "@/live/api";
import type { LiveCommand, LiveTaskEvent, LiveTurn } from "@/live/api";

type Tone = NonNullable<Parameters<typeof StatusBadge>[0]["tone"]>;

interface StatusMeta {
  label: string;
  tone: Tone;
}

const turnStatusMeta: Record<string, StatusMeta> = {
  queued: { label: "排队", tone: "neutral" },
  running: { label: "运行中", tone: "sky" },
  succeeded: { label: "成功", tone: "emerald" },
  failed: { label: "失败", tone: "rose" },
  cancelled: { label: "已取消", tone: "neutral" },
};

const commandStatusMeta: Record<string, StatusMeta> = {
  pending: { label: "待投递", tone: "amber" },
  delivered: { label: "已投递", tone: "sky" },
  accepted: { label: "Edge 已接受", tone: "violet" },
  done: { label: "已完成", tone: "emerald" },
  expired: { label: "已过期", tone: "amber" },
  rejected: { label: "被拒绝", tone: "rose" },
  cancelled: { label: "已撤销", tone: "neutral" },
};

const commandKindLabel: Record<string, string> = {
  start_turn: "启动轮次",
  send_turn: "追加输入",
  cancel: "取消",
};

export function TurnStatusBadge({ status }: { status: string }) {
  const meta = turnStatusMeta[status] ?? { label: status, tone: "neutral" as Tone };
  return <StatusBadge label={meta.label} tone={meta.tone} pulse={status === "running"} />;
}

export function CommandStatusBadge({ status }: { status: string }) {
  const meta = commandStatusMeta[status] ?? { label: status, tone: "neutral" as Tone };
  return <StatusBadge label={meta.label} tone={meta.tone} />;
}

export function commandKindName(kind: string): string {
  return commandKindLabel[kind] ?? kind;
}

export function LiveTurnList({
  turns,
  emptyHint = "暂无轮次；任务开始执行后会出现这里。",
}: {
  turns: LiveTurn[];
  emptyHint?: string;
}) {
  if (turns.length === 0) {
    return <EmptyState icon={Play} title="暂无轮次" hint={emptyHint} />;
  }
  const ordered = [...turns].sort((a, b) => a.seq - b.seq);
  return (
    <div className="space-y-3">
      {ordered.map((turn) => (
        <div key={turn.id} className="rounded-lg border p-3">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant="outline" className="px-1.5 font-mono text-[10px]">
              #{turn.seq}
            </Badge>
            <TurnStatusBadge status={turn.status} />
            <span className="font-mono text-[10px] text-muted-foreground">{turn.id}</span>
            <span className="ml-auto text-[10px] tabular-nums text-muted-foreground">
              {formatDateTime(turn.started_at)} → {formatDateTime(turn.ended_at)}
            </span>
          </div>
          <div className="mt-2 whitespace-pre-wrap break-words rounded-md bg-muted/40 px-3 py-2 text-xs leading-relaxed">
            {turn.request}
          </div>
          {turn.result ? (
            <div className="mt-2 rounded-md border border-emerald-200 bg-emerald-50/60 px-3 py-2 dark:border-emerald-500/25 dark:bg-emerald-500/10">
              <div className="text-[10px] font-medium text-emerald-700 dark:text-emerald-400">结果</div>
              <div className="mt-1 whitespace-pre-wrap break-words font-mono text-[11px] leading-relaxed text-emerald-900 dark:text-emerald-200">
                {turn.result}
              </div>
            </div>
          ) : null}
          {turn.error ? (
            <div className="mt-2 rounded-md border border-rose-200 bg-rose-50/60 px-3 py-2 dark:border-rose-500/25 dark:bg-rose-500/10">
              <div className="text-[10px] font-medium text-rose-700 dark:text-rose-400">错误</div>
              <div className="mt-1 whitespace-pre-wrap break-words font-mono text-[11px] leading-relaxed text-rose-900 dark:text-rose-200">
                {turn.error}
              </div>
            </div>
          ) : null}
          <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[10px] text-muted-foreground">
            <span>exit_code {turn.exit_code ?? "—"}</span>
            <span>开始 {formatClock(turn.started_at)}</span>
            <span>结束 {formatClock(turn.ended_at)}</span>
          </div>
        </div>
      ))}
    </div>
  );
}

export function LiveCommandList({
  commands,
  emptyHint = "暂无指令记录。",
}: {
  commands: LiveCommand[];
  emptyHint?: string;
}) {
  if (commands.length === 0) {
    return <EmptyState icon={Send} title="暂无指令" hint={emptyHint} />;
  }
  return (
    <div className="divide-y overflow-hidden rounded-lg border">
      {commands.map((command) => (
        <div key={command.id} className="space-y-1.5 px-3 py-2.5">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant="outline" className="text-[10px] font-normal">
              {commandKindName(command.kind)}
            </Badge>
            <CommandStatusBadge status={command.status} />
            <span className="font-mono text-[10px] text-muted-foreground">{command.id}</span>
          </div>
          <div className="flex flex-wrap gap-x-4 gap-y-1 text-[10px] text-muted-foreground">
            <span>创建 {formatClock(command.created_at)}</span>
            <span>投递 {formatClock(command.delivered_at)}</span>
            <span>确认 {formatClock(command.acked_at)}</span>
            {command.expires_at ? <span>过期 {formatClock(command.expires_at)}</span> : null}
          </div>
          {command.detail || command.error ? (
            <p className="break-words text-[11px] leading-relaxed text-rose-600 dark:text-rose-400">
              {command.error ?? command.detail}
            </p>
          ) : null}
        </div>
      ))}
    </div>
  );
}

const eventMeta: Record<string, { label: string; tone: Tone; icon: typeof Activity }> = {
  "system.init": { label: "会话初始化", tone: "violet", icon: TerminalSquare },
  "assistant.message": { label: "Agent 输出", tone: "sky", icon: MessageSquare },
  "tool.use": { label: "工具调用", tone: "amber", icon: Wrench },
  "tool.result": { label: "工具结果", tone: "emerald", icon: CheckCheck },
  result: { label: "执行结果", tone: "emerald", icon: CheckCircle2 },
  stderr: { label: "标准错误", tone: "rose", icon: AlertTriangle },
  "permission.request": { label: "权限请求", tone: "orange", icon: KeyRound },
  "terminal.snapshot": { label: "终端快照", tone: "neutral", icon: TerminalSquare },
};

const ringTone: Record<Tone, string> = {
  emerald:
    "border-emerald-200 bg-emerald-50 text-emerald-600 dark:border-emerald-500/25 dark:bg-emerald-500/10 dark:text-emerald-400",
  amber:
    "border-amber-200 bg-amber-50 text-amber-600 dark:border-amber-500/25 dark:bg-amber-500/10 dark:text-amber-400",
  rose: "border-rose-200 bg-rose-50 text-rose-600 dark:border-rose-500/25 dark:bg-rose-500/10 dark:text-rose-400",
  sky: "border-sky-200 bg-sky-50 text-sky-600 dark:border-sky-500/25 dark:bg-sky-500/10 dark:text-sky-400",
  violet:
    "border-violet-200 bg-violet-50 text-violet-600 dark:border-violet-500/25 dark:bg-violet-500/10 dark:text-violet-400",
  orange:
    "border-orange-200 bg-orange-50 text-orange-600 dark:border-orange-500/25 dark:bg-orange-500/10 dark:text-orange-400",
  neutral: "border-border bg-muted text-muted-foreground",
};

export function eventLabel(type: string): string {
  return eventMeta[type]?.label ?? type;
}

function readText(payload: Record<string, unknown> | null, keys: string[]): string | null {
  if (!payload) return null;
  for (const key of keys) {
    const value = payload[key];
    if (typeof value === "string" && value.trim().length > 0) return value;
    if (typeof value === "number" || typeof value === "boolean") return String(value);
  }
  return null;
}

export function eventSummary(event: LiveTaskEvent): string | null {
  const payload = event.payload;
  switch (event.type) {
    case "tool.use":
      return [readText(payload, ["tool", "name"]), readText(payload, ["summary", "input", "detail"])]
        .filter(Boolean)
        .join(" · ") || null;
    case "tool.result":
      return readText(payload, ["summary", "text", "output", "detail"]);
    case "assistant.message":
      return readText(payload, ["text", "message", "summary"]);
    case "result":
      return readText(payload, ["result", "text", "summary"]);
    case "stderr":
      return readText(payload, ["text", "error", "detail"]);
    case "system.init":
      return [readText(payload, ["harness_version", "version"]), readText(payload, ["cwd"])]
        .filter(Boolean)
        .join(" · ") || null;
    case "permission.request":
      return readText(payload, ["tool", "request", "detail"]);
    case "terminal.snapshot":
      return readText(payload, ["text", "summary"]);
    default:
      return readText(payload, ["summary", "text", "message", "detail"]);
  }
}

function payloadEntries(payload: Record<string, unknown> | null): number {
  return payload ? Object.keys(payload).length : 0;
}

export function LiveEventTimeline({
  events,
  emptyHint = "暂未收到 Edge 上报的事件。",
}: {
  events: LiveTaskEvent[];
  emptyHint?: string;
}) {
  // assistant.delta 是给 Chat 页面做流式渲染的增量事件，时间线里不逐条展示。
  const visible = events.filter((event) => event.type !== "assistant.delta");
  if (visible.length === 0) {
    return <EmptyState icon={Activity} title="暂无事件" hint={emptyHint} />;
  }
  const ordered = [...visible].sort((a, b) => new Date(a.at).getTime() - new Date(b.at).getTime());
  return (
    <ol className="space-y-0">
      {ordered.map((event, index) => {
        const meta = eventMeta[event.type] ?? { label: event.type, tone: "neutral" as Tone, icon: FileText };
        const Icon = meta.icon;
        const summary = eventSummary(event);
        return (
          <li key={event.event_id} className="relative flex gap-3 pb-5 last:pb-0">
            <div className="flex flex-col items-center">
              <span
                className={cn(
                  "flex size-7 shrink-0 items-center justify-center rounded-full border",
                  ringTone[meta.tone]
                )}
              >
                <Icon className="size-3.5" />
              </span>
              {index < ordered.length - 1 ? <span className="mt-1 w-px flex-1 bg-border" /> : null}
            </div>
            <div className="min-w-0 flex-1 pt-0.5">
              <div className="flex flex-wrap items-baseline justify-between gap-2">
                <span className="text-sm font-medium">{meta.label}</span>
                <time
                  className="shrink-0 text-[11px] tabular-nums text-muted-foreground"
                  title={formatDateTime(event.at)}
                >
                  {formatClock(event.at)}
                </time>
              </div>
              {summary ? (
                <p className="mt-0.5 whitespace-pre-wrap break-words text-xs leading-relaxed text-muted-foreground">
                  {summary}
                </p>
              ) : null}
              <div className="mt-1 flex flex-wrap items-center gap-2 text-[10px] text-muted-foreground/70">
                <span className="font-mono">{event.type}</span>
                {event.turn_id ? <span className="font-mono">{event.turn_id}</span> : null}
              </div>
              {payloadEntries(event.payload) > 0 ? (
                <details className="mt-1.5">
                  <summary className="cursor-pointer text-[10px] text-muted-foreground/70 hover:text-foreground">
                    payload
                  </summary>
                  <pre className="mt-1 max-h-48 overflow-auto rounded-md border bg-muted/40 p-2 text-[10px] leading-relaxed">
                    {JSON.stringify(event.payload, null, 2)}
                  </pre>
                </details>
              ) : null}
            </div>
          </li>
        );
      })}
    </ol>
  );
}
