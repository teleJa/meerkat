import { randomBytes } from "node:crypto";
import type { Db } from "./db";

export type TaskStatus =
  | "queued"
  | "running"
  | "waiting_input"
  | "cancel_requested"
  | "succeeded"
  | "failed"
  | "cancelled"
  | "needs_review";

export type SessionStatus = "idle" | "running" | "waiting_input" | "waiting_permission" | "ended" | "error";
export type CommandStatus = "pending" | "delivered" | "accepted" | "rejected" | "expired" | "cancelled" | "done";

export interface TaskRow {
  id: string;
  title: string;
  source: string;
  device_id: string;
  session_id: string | null;
  status: TaskStatus;
  stage: string | null;
  cwd: string;
  request: string;
  idempotency_key: string | null;
  evidence_kind: string | null;
  evidence_note: string | null;
  created_at: string;
  updated_at: string;
}

export interface SessionRow {
  id: string;
  task_id: string;
  device_id: string;
  mode: string;
  status: SessionStatus;
  cwd: string;
  harness: string | null;
  harness_version: string | null;
  harness_session_id: string | null;
  created_at: string;
  last_activity_at: string;
}

export interface TurnRow {
  id: string;
  task_id: string;
  session_id: string;
  seq: number;
  request: string;
  status: string;
  started_at: string | null;
  ended_at: string | null;
  exit_code: number | null;
  result: string | null;
  error: string | null;
  created_at: string;
}

export interface CommandRow {
  id: string;
  task_id: string;
  session_id: string | null;
  turn_id: string | null;
  device_id: string;
  kind: "start_turn" | "send_turn" | "cancel";
  status: CommandStatus;
  payload: string;
  error: string | null;
  created_at: string;
  delivered_at: string | null;
  acked_at: string | null;
  expires_at: string;
}

export interface TaskEventRow {
  event_id: string;
  task_id: string | null;
  session_id: string | null;
  turn_id: string | null;
  device_id: string;
  type: string;
  payload: string;
  at: string;
  received_at: string;
}

export function newId(prefix: string): string {
  return prefix + randomBytes(4).toString("hex");
}

function nowIso(now = new Date()): string {
  return now.toISOString();
}

function transaction<T>(db: Db, fn: () => T): T {
  db.exec("BEGIN IMMEDIATE");
  try {
    const result = fn();
    db.exec("COMMIT");
    return result;
  } catch (error) {
    db.exec("ROLLBACK");
    throw error;
  }
}

export interface CreateTaskInput {
  request: string;
  title?: string;
  deviceId: string;
  cwd: string;
  runProfile?: string;
  idempotencyKey?: string;
  timeoutSeconds?: number;
}

export interface CreatedTask {
  task: TaskRow;
  session: SessionRow;
  turn: TurnRow;
  command: CommandRow;
}

export function getTask(db: Db, id: string): TaskRow | undefined {
  return db.prepare("SELECT * FROM tasks WHERE id = ?").get(id) as unknown as TaskRow | undefined;
}

export function getTaskByIdempotencyKey(db: Db, key: string): TaskRow | undefined {
  return db.prepare("SELECT * FROM tasks WHERE idempotency_key = ?").get(key) as unknown as TaskRow | undefined;
}

export function getSession(db: Db, id: string): SessionRow | undefined {
  return db.prepare("SELECT * FROM sessions WHERE id = ?").get(id) as unknown as SessionRow | undefined;
}

export function listSessions(db: Db, limit = 100): SessionRow[] {
  return db
    .prepare("SELECT * FROM sessions ORDER BY last_activity_at DESC LIMIT ?")
    .all(limit) as unknown as SessionRow[];
}

export function listTasks(db: Db, limit = 100): TaskRow[] {
  return db
    .prepare("SELECT * FROM tasks ORDER BY created_at DESC LIMIT ?")
    .all(limit) as unknown as TaskRow[];
}

export function listTurns(db: Db, sessionId: string): TurnRow[] {
  return db
    .prepare("SELECT * FROM turns WHERE session_id = ? ORDER BY seq ASC")
    .all(sessionId) as unknown as TurnRow[];
}

export function listTaskEvents(db: Db, taskId: string, limit = 300): TaskEventRow[] {
  return db
    .prepare("SELECT * FROM task_events WHERE task_id = ? ORDER BY at ASC LIMIT ?")
    .all(taskId, limit) as unknown as TaskEventRow[];
}

export function listSessionEvents(db: Db, sessionId: string, limit = 300): TaskEventRow[] {
  return db
    .prepare("SELECT * FROM task_events WHERE session_id = ? ORDER BY at ASC LIMIT ?")
    .all(sessionId, limit) as unknown as TaskEventRow[];
}

export function listCommandsForTask(db: Db, taskId: string): CommandRow[] {
  return db
    .prepare("SELECT * FROM commands WHERE task_id = ? ORDER BY created_at ASC")
    .all(taskId) as unknown as CommandRow[];
}

export function listDeliverableCommands(db: Db, deviceId: string): CommandRow[] {
  return db
    .prepare(
      "SELECT * FROM commands WHERE device_id = ? AND status IN ('pending','delivered') ORDER BY created_at ASC"
    )
    .all(deviceId) as unknown as CommandRow[];
}

export function createTask(db: Db, input: CreateTaskInput, now = new Date()): CreatedTask {
  const at = nowIso(now);
  if (input.idempotencyKey) {
    const existing = getTaskByIdempotencyKey(db, input.idempotencyKey);
    if (existing) {
      const session = existing.session_id ? getSession(db, existing.session_id) : undefined;
      const turns = session ? listTurns(db, session.id) : [];
      const commands = listCommandsForTask(db, existing.id);
      if (session && turns[0] && commands[0]) {
        return { task: existing, session, turn: turns[0], command: commands[0] };
      }
    }
  }

  const taskId = newId("tsk_");
  const sessionId = newId("sess_");
  const turnId = newId("turn_");
  const commandId = newId("cmd_");
  const title = (input.title?.trim() || input.request.trim().slice(0, 40)).slice(0, 120);
  const payload = JSON.stringify({
    request: input.request,
    cwd: input.cwd,
    run_profile: input.runProfile ?? "default",
    harness_session_id: null,
    timeout_seconds: input.timeoutSeconds ?? 1800,
  });
  const expiresAt = new Date(now.getTime() + 10 * 60 * 1000).toISOString();

  transaction(db, () => {
    db.prepare(
      `INSERT INTO tasks
        (id, title, source, device_id, session_id, status, stage, cwd, request, idempotency_key, evidence_kind, evidence_note, created_at, updated_at)
       VALUES (?, ?, 'managed', ?, ?, 'queued', ?, ?, ?, ?, NULL, NULL, ?, ?)`
    ).run(
      taskId,
      title,
      input.deviceId,
      sessionId,
      "指令已持久化，等待 Edge 执行",
      input.cwd,
      input.request,
      input.idempotencyKey ?? null,
      at,
      at
    );
    db.prepare(
      `INSERT INTO sessions
        (id, task_id, device_id, mode, status, cwd, harness, harness_version, harness_session_id, created_at, last_activity_at)
       VALUES (?, ?, ?, 'managed', 'idle', ?, 'claude-code', NULL, NULL, ?, ?)`
    ).run(sessionId, taskId, input.deviceId, input.cwd, at, at);
    db.prepare(
      `INSERT INTO turns (id, task_id, session_id, seq, request, status, started_at, ended_at, exit_code, result, error, created_at)
       VALUES (?, ?, ?, 1, ?, 'queued', NULL, NULL, NULL, NULL, NULL, ?)`
    ).run(turnId, taskId, sessionId, input.request, at);
    db.prepare(
      `INSERT INTO commands (id, task_id, session_id, turn_id, device_id, kind, status, payload, error, created_at, delivered_at, acked_at, expires_at)
       VALUES (?, ?, ?, ?, ?, 'start_turn', 'pending', ?, NULL, ?, NULL, NULL, ?)`
    ).run(commandId, taskId, sessionId, turnId, input.deviceId, payload, at, expiresAt);
  });

  const task = getTask(db, taskId)!;
  const session = getSession(db, sessionId)!;
  const turns = listTurns(db, sessionId);
  const commands = listCommandsForTask(db, taskId);
  return { task, session, turn: turns[0]!, command: commands[0]! };
}

export interface CreateTurnResult {
  task: TaskRow;
  session: SessionRow;
  turn: TurnRow;
  command: CommandRow;
}

export function countActiveCommands(db: Db, taskId: string): number {
  const row = db
    .prepare(
      "SELECT COUNT(*) AS count FROM commands WHERE task_id = ? AND status IN ('pending','delivered','accepted')"
    )
    .get(taskId) as unknown as { count: number };
  return row.count;
}

export function createTurn(
  db: Db,
  taskId: string,
  request: string,
  idempotencyKey?: string,
  now = new Date()
): CreateTurnResult | { error: "task_not_found" | "task_not_managed" | "task_finished" | "turn_in_progress" | "session_missing" } {
  const task = getTask(db, taskId);
  if (!task) return { error: "task_not_found" };
  if (task.source !== "managed") return { error: "task_not_managed" };
  // succeeded 的任务允许继续追加输入（重新打开）；failed/cancelled 不再接受。
  if (["failed", "cancelled"].includes(task.status)) return { error: "task_finished" };
  if (countActiveCommands(db, taskId) > 0) return { error: "turn_in_progress" };
  const session = task.session_id ? getSession(db, task.session_id) : undefined;
  if (!session) return { error: "session_missing" };

  const at = nowIso(now);
  const turnId = newId("turn_");
  const commandId = newId("cmd_");
  const seqRow = db
    .prepare("SELECT COALESCE(MAX(seq), 0) AS seq FROM turns WHERE session_id = ?")
    .get(session.id) as unknown as { seq: number };
  const payload = JSON.stringify({
    request,
    cwd: task.cwd,
    run_profile: "default",
    harness_session_id: session.harness_session_id,
    timeout_seconds: 1800,
  });
  const expiresAt = new Date(now.getTime() + 10 * 60 * 1000).toISOString();

  transaction(db, () => {
    db.prepare(
      `INSERT INTO turns (id, task_id, session_id, seq, request, status, started_at, ended_at, exit_code, result, error, created_at)
       VALUES (?, ?, ?, ?, ?, 'queued', NULL, NULL, NULL, NULL, NULL, ?)`
    ).run(turnId, taskId, session.id, seqRow.seq + 1, request, at);
    db.prepare(
      `INSERT INTO commands (id, task_id, session_id, turn_id, device_id, kind, status, payload, error, created_at, delivered_at, acked_at, expires_at)
       VALUES (?, ?, ?, ?, ?, 'send_turn', 'pending', ?, NULL, ?, NULL, NULL, ?)`
    ).run(commandId, taskId, session.id, turnId, task.device_id, payload, at, expiresAt);
    db.prepare("UPDATE tasks SET status = 'running', stage = '追加输入已排队', updated_at = ? WHERE id = ?").run(
      at,
      taskId
    );
    db.prepare("UPDATE sessions SET last_activity_at = ? WHERE id = ?").run(at, session.id);
  });

  return {
    task: getTask(db, taskId)!,
    session: getSession(db, session.id)!,
    turn: db.prepare("SELECT * FROM turns WHERE id = ?").get(turnId) as unknown as TurnRow,
    command: db.prepare("SELECT * FROM commands WHERE id = ?").get(commandId) as unknown as CommandRow,
  };
}

export interface CancelResult {
  task: TaskRow;
  command: CommandRow | null;
  cancelledImmediately: boolean;
}

export function cancelTask(db: Db, taskId: string, now = new Date()): CancelResult | { error: "task_not_found" | "task_finished" } {
  const task = getTask(db, taskId);
  if (!task) return { error: "task_not_found" };
  if (["succeeded", "failed", "cancelled"].includes(task.status)) return { error: "task_finished" };
  const at = nowIso(now);
  const active = db
    .prepare("SELECT * FROM commands WHERE task_id = ? AND status IN ('pending','delivered','accepted')")
    .all(taskId) as unknown as CommandRow[];
  const deliveredActive = active.some((command) => command.status !== "pending");

  if (!deliveredActive) {
    transaction(db, () => {
      db.prepare("UPDATE commands SET status = 'cancelled', error = '未投递即取消' WHERE task_id = ? AND status = 'pending'").run(taskId);
      db.prepare(
        "UPDATE tasks SET status = 'cancelled', stage = '指令未投递，已直接取消', updated_at = ? WHERE id = ?"
      ).run(at, taskId);
      if (task.session_id) {
        db.prepare("UPDATE sessions SET status = 'ended', last_activity_at = ? WHERE id = ?").run(at, task.session_id);
      }
    });
    return { task: getTask(db, taskId)!, command: null, cancelledImmediately: true };
  }

  const commandId = newId("cmd_");
  const expiresAt = new Date(now.getTime() + 10 * 60 * 1000).toISOString();
  transaction(db, () => {
    db.prepare(
      `INSERT INTO commands (id, task_id, session_id, turn_id, device_id, kind, status, payload, error, created_at, delivered_at, acked_at, expires_at)
       VALUES (?, ?, ?, NULL, ?, 'cancel', 'pending', '{}', NULL, ?, NULL, NULL, ?)`
    ).run(commandId, taskId, task.session_id, task.device_id, at, expiresAt);
    db.prepare(
      "UPDATE tasks SET status = 'cancel_requested', stage = '取消已记录，等待 Edge 确认', updated_at = ? WHERE id = ?"
    ).run(at, taskId);
  });
  return {
    task: getTask(db, taskId)!,
    command: db.prepare("SELECT * FROM commands WHERE id = ?").get(commandId) as unknown as CommandRow,
    cancelledImmediately: false,
  };
}

export function markCommandDelivered(db: Db, commandId: string, now = new Date()): void {
  const at = nowIso(now);
  const command = db.prepare("SELECT * FROM commands WHERE id = ?").get(commandId) as unknown as CommandRow | undefined;
  if (!command) return;
  db.prepare("UPDATE commands SET status = 'delivered', delivered_at = ? WHERE id = ? AND status = 'pending'").run(
    at,
    commandId
  );
  if (command.kind === "start_turn" || command.kind === "send_turn") {
    db.prepare(
      "UPDATE tasks SET stage = '指令已投递，等待 Edge ACK', updated_at = ? WHERE id = ? AND status = 'queued'"
    ).run(at, command.task_id);
  }
}

export function handleCommandAck(
  db: Db,
  message: { command_id: string; status: "accepted" | "rejected"; detail?: string | null; at?: string },
  now = new Date()
): void {
  const command = db.prepare("SELECT * FROM commands WHERE id = ?").get(message.command_id) as unknown as CommandRow | undefined;
  if (!command) return;
  const at = message.at ?? nowIso(now);
  if (message.status === "accepted") {
    db.prepare("UPDATE commands SET status = 'accepted', acked_at = ? WHERE id = ?").run(at, command.id);
    if (command.kind !== "cancel") {
      db.prepare(
        "UPDATE tasks SET status = 'running', stage = 'Edge 已接受指令', updated_at = ? WHERE id = ? AND status IN ('queued','running')"
      ).run(at, command.task_id);
    } else {
      db.prepare(
        "UPDATE tasks SET status = 'cancel_requested', stage = 'Edge 已接受取消请求，等待进程结束', updated_at = ? WHERE id = ?"
      ).run(at, command.task_id);
    }
  } else {
    db.prepare("UPDATE commands SET status = 'rejected', acked_at = ?, error = ? WHERE id = ?").run(
      at,
      message.detail ?? null,
      command.id
    );
    db.prepare(
      "UPDATE tasks SET status = 'failed', stage = ?, updated_at = ? WHERE id = ? AND status NOT IN ('succeeded','cancelled')"
    ).run("Edge 拒绝执行：" + (message.detail ?? "未说明原因"), at, command.task_id);
  }
}

export function handleSessionStarted(
  db: Db,
  message: { task_id: string; session_id: string; harness_session_id?: string; harness_version?: string; at?: string },
  now = new Date()
): void {
  const at = message.at ?? nowIso(now);
  db.prepare(
    "UPDATE sessions SET status = 'running', harness_session_id = COALESCE(?, harness_session_id), harness_version = COALESCE(?, harness_version), last_activity_at = ? WHERE id = ?"
  ).run(message.harness_session_id ?? null, message.harness_version ?? null, at, message.session_id);
  db.prepare(
    "UPDATE tasks SET status = 'running', stage = '会话已启动', updated_at = ? WHERE id = ? AND status IN ('queued','running')"
  ).run(at, message.task_id);
}

export function handleTurnStarted(
  db: Db,
  message: { task_id: string; session_id: string; turn_id: string; at?: string },
  now = new Date()
): void {
  const at = message.at ?? nowIso(now);
  db.prepare("UPDATE turns SET status = 'running', started_at = COALESCE(started_at, ?) WHERE id = ?").run(
    at,
    message.turn_id
  );
  db.prepare("UPDATE sessions SET status = 'running', last_activity_at = ? WHERE id = ?").run(at, message.session_id);
  db.prepare(
    "UPDATE tasks SET status = 'running', stage = '执行中', updated_at = ? WHERE id = ? AND status IN ('queued','running')"
  ).run(at, message.task_id);
}

export interface IncomingTaskEvent {
  event_id: string;
  task_id?: string | null;
  session_id?: string | null;
  turn_id?: string | null;
  type: string;
  at: string;
  payload?: unknown;
}

export function handleEventBatch(
  db: Db,
  deviceId: string,
  events: IncomingTaskEvent[],
  now = new Date()
): number {
  const receivedAt = nowIso(now);
  let inserted = 0;
  transaction(db, () => {
    for (const event of events) {
      const result = db
        .prepare(
          `INSERT OR IGNORE INTO task_events (event_id, task_id, session_id, turn_id, device_id, type, payload, at, received_at)
           VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
        )
        .run(
          event.event_id,
          event.task_id ?? null,
          event.session_id ?? null,
          event.turn_id ?? null,
          deviceId,
          event.type,
          JSON.stringify(event.payload ?? {}),
          event.at,
          receivedAt
        );
      inserted += Number(result.changes);
      if (event.session_id) {
        db.prepare("UPDATE sessions SET last_activity_at = ? WHERE id = ?").run(event.at, event.session_id);
      }
      if (event.task_id) {
        db.prepare("UPDATE tasks SET updated_at = ? WHERE id = ?").run(event.at, event.task_id);
      }
    }
  });
  return inserted;
}

export interface TurnCompletedInput {
  task_id: string;
  session_id: string;
  turn_id: string;
  status: "succeeded" | "failed" | "cancelled";
  exit_code?: number | null;
  duration_ms?: number | null;
  result?: string | null;
  error?: string | null;
  at?: string;
}

export function handleTurnCompleted(db: Db, message: TurnCompletedInput, now = new Date()): void {
  const at = message.at ?? nowIso(now);
  const result = message.result ? message.result.slice(0, 8000) : null;
  const error = message.error ? message.error.slice(0, 4000) : null;
  transaction(db, () => {
    db.prepare(
      "UPDATE turns SET status = ?, ended_at = ?, exit_code = ?, result = ?, error = ? WHERE id = ?"
    ).run(message.status, at, message.exit_code ?? null, result, error, message.turn_id);
    db.prepare("UPDATE commands SET status = 'done' WHERE turn_id = ? AND status IN ('accepted','delivered','pending')").run(
      message.turn_id
    );
    db.prepare("UPDATE sessions SET status = 'idle', last_activity_at = ? WHERE id = ?").run(at, message.session_id);
    if (message.status === "succeeded") {
      db.prepare(
        "UPDATE tasks SET status = 'succeeded', stage = '已完成', evidence_kind = 'explicit_agent_report', evidence_note = ?, updated_at = ? WHERE id = ?"
      ).run(result ? result.slice(0, 500) : "Claude 进程正常结束", at, message.task_id);
    } else if (message.status === "cancelled") {
      db.prepare(
        "UPDATE tasks SET status = 'cancelled', stage = '已取消', evidence_note = ?, updated_at = ? WHERE id = ?"
      ).run(error, at, message.task_id);
    } else {
      db.prepare(
        "UPDATE tasks SET status = 'failed', stage = ?, evidence_note = ?, updated_at = ? WHERE id = ?"
      ).run("执行失败：" + (error ?? "未知错误"), error, at, message.task_id);
    }
  });
}

export function expireCommands(db: Db, now = new Date()): number {
  const at = nowIso(now);
  const stale = db
    .prepare(
      "SELECT * FROM commands WHERE status IN ('pending','delivered') AND expires_at <= ?"
    )
    .all(at) as unknown as CommandRow[];
  if (stale.length === 0) return 0;
  transaction(db, () => {
    for (const command of stale) {
      db.prepare("UPDATE commands SET status = 'expired', error = '指令过期未执行' WHERE id = ?").run(command.id);
      db.prepare(
        "UPDATE tasks SET status = 'failed', stage = '指令过期，Edge 未执行', updated_at = ? WHERE id = ? AND status IN ('queued','running','cancel_requested')"
      ).run(at, command.task_id);
    }
  });
  return stale.length;
}

export function commandMessage(command: CommandRow): Record<string, unknown> {
  const payload = JSON.parse(command.payload) as Record<string, unknown>;
  const message: Record<string, unknown> = {
    type: "command",
    command_id: command.id,
    kind: command.kind,
    task_id: command.task_id,
    session_id: command.session_id,
    created_at: command.created_at,
    expires_at: command.expires_at,
  };
  if (command.turn_id) message.turn_id = command.turn_id;
  if (command.kind !== "cancel") message.payload = payload;
  return message;
}
