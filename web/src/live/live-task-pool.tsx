import { useRef, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { PageHeader } from "@/components/page-header";
import { LiveGate } from "@/live/live-gate";
import { errorMessage, formatRelative, liveApi, taskPoolApi, type PoolItem, type PoolStatus, type PoolTarget } from "@/live/api";

const columns: { title: string; statuses: PoolStatus[] }[] = [
  { title: "待分配", statuses: ["unassigned"] },
  { title: "处理中", statuses: ["processing"] },
  { title: "卡住", statuses: ["blocked"] },
  { title: "待你验收", statuses: ["review"] },
  { title: "已完成／已取消", statuses: ["completed", "cancelled"] },
];

const statusLabels: Record<PoolStatus, string> = {
  unassigned: "待分配", processing: "处理中", blocked: "卡住", review: "待你验收", completed: "已完成", cancelled: "已取消",
};

const eventLabels: Record<string, string> = {
  created: "创建任务", assigned: "分派", accepted: "接手", progress: "进展", blocked: "报告阻塞", delivered: "交付",
  approved: "验收通过", returned: "打回", cancelled: "取消", delivery_retried: "重发通知",
};

const poolQueryKey = ["live", "task-pool"] as const;
const fieldClass = "block min-h-12 w-full rounded-md border bg-background px-3 py-2 text-base focus-visible:outline-2 focus-visible:outline-ring";
const cardPath = (id: string) => `/live/task-pool/${encodeURIComponent(id)}`;
const targetKey = (target: PoolTarget) => `${target.kind}:${target.id}`;

function poolStatus(item: PoolItem) {
  if (item.delivery?.status === "uncertain") return item.status === "blocked" ? "可能未送达" : "待确认送达";
  if (item.delivery?.status === "failed") return "通知发送失败";
  if (item.status === "processing") {
    if (item.delivery?.status === "pending" || item.delivery?.status === "sending") return "待投递";
    if (item.delivery?.status === "queued") return "已投递，等待接手";
  }
  return statusLabels[item.status];
}

function assigneeName(item: PoolItem, targets: PoolTarget[]) {
  if (!item.assignee_id) return "Chief 待分配";
  return targets.find(target => target.kind === item.assignee_kind && target.id === item.assignee_id)?.name ?? item.assignee_id;
}

function useTargets() {
  return useQuery({ queryKey: [...poolQueryKey, "targets"], queryFn: taskPoolApi.targets, staleTime: 30_000 });
}

export function TaskPoolBoard() {
  const items = useQuery({ queryKey: poolQueryKey, queryFn: taskPoolApi.list, refetchInterval: 5000 });
  const targets = useTargets();
  const cards = items.data?.items ?? [];
  return (
    <section className="space-y-4" aria-label="任务池">
      <PageHeader title="任务看板" actions={<Button asChild><Link to="/live/task-pool/new">新任务</Link></Button>} />
      {items.isPending && <p role="status" className="text-sm text-muted-foreground">正在读取任务…</p>}
      {items.isError && <p role="alert" className="text-sm text-destructive">{errorMessage(items.error)}</p>}
      {!items.isPending && !items.isError && (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-5">
          {columns.map((column, index) => {
            const matches = cards.filter(item => column.statuses.includes(item.status));
            return (
              <section key={column.title} aria-labelledby={`pool-column-${index}`} className="min-w-0 rounded-xl border bg-card">
                <div className="flex items-center justify-between gap-2 border-b px-4 py-3">
                  <h2 id={`pool-column-${index}`} className="text-sm font-semibold">{column.title}</h2>
                  <span className="text-xs tabular-nums text-muted-foreground">{matches.length}</span>
                </div>
                <ul className="space-y-2 p-2">
                  {matches.map(item => (
                    <li key={item.id}>
                      <Link className="block rounded-lg border bg-background p-3 hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring" to={cardPath(item.id)}>
                        <span className="block break-words text-sm font-medium">{item.title}</span>
                        <span className="mt-1 block break-words text-xs text-muted-foreground">{assigneeName(item, targets.data?.targets ?? [])} · {poolStatus(item)}</span>
                        {item.latest_progress?.note && <span className="mt-2 line-clamp-2 break-words text-xs">{item.latest_progress.note}</span>}
                      </Link>
                    </li>
                  ))}
                  {matches.length === 0 && <li className="p-3 text-xs text-muted-foreground">暂无任务</li>}
                </ul>
              </section>
            );
          })}
        </div>
      )}
    </section>
  );
}

export function NewTaskPoolPage() {
  return <LiveGate><NewTaskPoolForm /></LiveGate>;
}

export function NewTaskPoolForm() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const targets = useTargets();
  const idempotency = useRef<{ fingerprint: string; key: string; attachments: string[] } | null>(null);
  const [files, setFiles] = useState<File[]>([]);
  const [fileRevision, setFileRevision] = useState(0);
  const [textError, setTextError] = useState("");
  const create = useMutation({
    mutationFn: async ({ text, target }: { text: string; target?: PoolTarget }) => {
      if (files.length > 4 || files.some(file => file.size === 0 || file.size > 4 * 1024 * 1024) ||
        files.reduce((total, file) => total + file.size, 0) > 8 * 1024 * 1024) {
        throw new Error("最多 4 个附件，每份须在 4 MB 以内且不能为空，合计最多 8 MB。 ");
      }
      const fingerprint = JSON.stringify([text, target && targetKey(target), fileRevision]);
      if (idempotency.current?.fingerprint !== fingerprint) {
        const key = Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, "0")).join("");
        idempotency.current = { fingerprint, key, attachments: [] };
      }
      const pending = idempotency.current;
      for (const file of files.slice(pending.attachments.length)) {
        pending.attachments.push((await liveApi.uploadAttachment(file)).id);
      }
      return taskPoolApi.create(text, pending.attachments, pending.key, target);
    },
    onSuccess: ({ item }) => {
      void queryClient.invalidateQueries({ queryKey: poolQueryKey });
      navigate(cardPath(item.id));
    },
  });
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (create.isPending) return;
    const form = new FormData(event.currentTarget);
    const text = String(form.get("request") ?? "").trim();
    if (!text || new TextEncoder().encode(text).length > 8192) {
      setTextError(text ? "任务内容太长，请缩短后再提交。" : "请填写要处理的任务。");
      (event.currentTarget.elements.namedItem("request") as HTMLTextAreaElement).focus();
      return;
    }
    const target = targets.data?.targets.find(option => targetKey(option) === form.get("target"));
    create.mutate({ text, target });
  }
  return (
    <div className="max-w-2xl space-y-4">
      <Link className="text-sm text-muted-foreground hover:underline" to="/live/board">返回任务看板</Link>
      <h1 id="new-task-title" className="text-xl font-semibold">新任务</h1>
      <form className="space-y-4 rounded-xl border bg-card p-4" action="/api/task-pool" method="post" aria-labelledby="new-task-title" onSubmit={submit}>
        <div className="space-y-1">
          <label htmlFor="task-pool-request" className="text-sm font-medium">任务内容（必填）</label>
          <textarea className={fieldClass} rows={4} id="task-pool-request" name="request" required aria-invalid={Boolean(textError)}
            aria-describedby={textError ? "task-pool-request-error" : undefined} onInput={() => setTextError("")} />
          <p id="task-pool-request-error" className="text-sm text-destructive" aria-live="polite">{textError}</p>
        </div>
        <div className="space-y-1">
          <label htmlFor="task-pool-target" className="text-sm font-medium">负责人</label>
          <select id="task-pool-target" name="target" className={fieldClass}>
            <option value="">交给 Chief 分配</option>
            {targets.data?.targets.map(target => <option key={targetKey(target)} value={targetKey(target)}>{target.name}（{target.kind === "group" ? "群" : "Bot"}）</option>)}
          </select>
          {targets.isError && <p className="text-sm text-muted-foreground">暂时无法读取 Bot 和群。可以先提交到待分配。</p>}
        </div>
        <div className="space-y-1">
          <label htmlFor="task-pool-files" className="text-sm font-medium">附件</label>
          <input className={fieldClass} id="task-pool-files" name="files" type="file" multiple disabled={create.isPending} onChange={event => {
            setFiles(Array.from(event.target.files ?? []));
            setFileRevision(revision => revision + 1);
          }} />
        </div>
        <p aria-live="polite" className="text-sm text-destructive">{create.isError ? errorMessage(create.error) : ""}</p>
        <Button type="submit" disabled={create.isPending}>{create.isPending ? "正在创建…" : "创建任务"}</Button>
      </form>
    </div>
  );
}

export function TaskPoolDetailPage() {
  const { itemId = "" } = useParams();
  return <LiveGate><TaskPoolDetail key={itemId} id={itemId} /></LiveGate>;
}

function TaskPoolDetail({ id }: { id: string }) {
  const queryClient = useQueryClient();
  const [selection, setSelection] = useState("");
  const detail = useQuery({ queryKey: [...poolQueryKey, id], queryFn: () => taskPoolApi.detail(id), refetchInterval: 3000 });
  const targets = useTargets();
  const action = useMutation({
    mutationFn: ({ item, target }: { item: PoolItem; target?: PoolTarget }) => target ? taskPoolApi.assign(item, target) : taskPoolApi.retry(item),
    onSettled: () => queryClient.invalidateQueries({ queryKey: poolQueryKey }),
    onSuccess: () => setSelection(""),
  });
  if (detail.isPending) return <p role="status">正在读取任务…</p>;
  if (detail.isError) return <p role="alert">{errorMessage(detail.error)}</p>;
  const { item, events } = detail.data;
  const options = targets.data?.targets ?? [];
  const canAssign = item.status === "unassigned" || item.status === "processing" || item.status === "blocked";
  return (
    <div className="max-w-3xl space-y-4">
      <Link className="text-sm text-muted-foreground hover:underline" to="/live/board">返回任务看板</Link>
      <PageHeader title={item.title} description={`${poolStatus(item)} · ${assigneeName(item, options)}`} />
      <section aria-label="任务内容" className="space-y-3 rounded-xl border bg-card p-4">
        <p className="whitespace-pre-wrap break-words text-sm">{item.request}</p>
        {item.attachment_ids.length > 0 && <ul className="space-y-1 text-sm">{item.attachment_ids.map((attachment, index) => (
          <li key={attachment}><a className="underline" href={taskPoolApi.attachmentURL(item.id, attachment)} download>附件 {index + 1}</a></li>
        ))}</ul>}
        {item.delivery?.can_retry && <Button type="button" variant="outline" disabled={action.isPending} onClick={() => action.mutate({ item })}>重新发送通知</Button>}
        {canAssign && options.length > 0 && (
          <form className="space-y-2 border-t pt-3" method="post" action={`/api/task-pool/${encodeURIComponent(item.id)}/assign`} onSubmit={event => {
            event.preventDefault();
            const target = options.find(option => targetKey(option) === selection);
            if (target && !action.isPending) action.mutate({ item, target });
          }}>
            <label htmlFor="task-pool-reassign" className="text-sm font-medium">{item.assignee_id ? "改派给" : "分派给"}</label>
            <select className={fieldClass} id="task-pool-reassign" name="target" required value={selection} onChange={event => setSelection(event.target.value)}>
              <option value="">选择 Bot 或群</option>
              {options.map(target => <option key={targetKey(target)} value={targetKey(target)}>{target.name}（{target.kind === "group" ? "群" : "Bot"}）</option>)}
            </select>
            <Button type="submit" disabled={action.isPending}>{item.assignee_id ? "确认改派" : "确认分派"}</Button>
          </form>
        )}
        {canAssign && targets.isError && <p className="text-sm text-muted-foreground">暂时无法读取可分派的 Bot 和群。</p>}
        <p aria-live="polite" className="text-sm text-destructive">{action.isError ? errorMessage(action.error) : ""}</p>
      </section>
      <section aria-labelledby="pool-history" className="rounded-xl border bg-card p-4">
        <h2 id="pool-history" className="text-sm font-semibold">进展与证据</h2>
        <ol className="mt-3 divide-y">
          {[...events].reverse().map(event => <li key={`${event.assignment_rev}:${event.event_id}`} className="space-y-1 py-3 text-sm">
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <span className="font-medium">{eventLabels[event.type] ?? event.type}</span>
              <span className="text-xs text-muted-foreground">{event.actor_id === "user" ? "你" : options.find(target => target.kind === "bot" && target.id === event.actor_id)?.name ?? event.actor_id} · <time dateTime={event.at}>{formatRelative(event.at)}</time></span>
            </div>
            {event.payload.note && <p className="whitespace-pre-wrap break-words">{event.payload.note}</p>}
            {event.payload.evidence && <ul className="list-disc space-y-1 pl-5 text-muted-foreground">{event.payload.evidence.map((evidence, index) => <li className="break-words" key={index}>{evidence}</li>)}</ul>}
          </li>)}
        </ol>
      </section>
    </div>
  );
}
