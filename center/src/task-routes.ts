import { z } from "zod";
import type { FastifyInstance } from "fastify";
import type { CenterConfig } from "./config";
import type { Db } from "./db";
import { ApiError } from "./errors";
import { requireAuth } from "./routes";
import { getDevice } from "./repo";
import {
  cancelTask,
  createTask,
  createTurn,
  expireCommands,
  getSession,
  getTask,
  handleEventBatch,
  listCommandsForTask,
  listSessionEvents,
  listSessions,
  listTaskEvents,
  listTasks,
  listTurns,
  type CommandRow,
  type SessionRow,
  type TaskEventRow,
  type TaskRow,
  type TurnRow,
} from "./task-repo";
import { deliverCommand, type CommandTransport } from "./task-delivery";

const createTaskSchema = z.object({
  request: z.string().min(1).max(20000),
  device_id: z.string().min(1).max(64),
  cwd: z.string().min(1).max(1024),
  title: z.string().max(120).optional(),
  run_profile: z.string().max(64).optional(),
  idempotency_key: z.string().min(8).max(128).optional(),
  timeout_seconds: z.number().int().min(30).max(7200).optional(),
});

const turnSchema = z.object({
  request: z.string().min(1).max(20000),
  idempotency_key: z.string().min(8).max(128).optional(),
});

function issuesText(error: { issues: Array<{ path: PropertyKey[]; message: string }> }): string {
  return error.issues
    .slice(0, 5)
    .map((issue) => issue.path.map(String).join(".") + ": " + issue.message)
    .join("; ");
}

function taskDto(task: TaskRow) {
  return {
    id: task.id,
    title: task.title,
    source: task.source,
    device_id: task.device_id,
    session_id: task.session_id,
    status: task.status,
    stage: task.stage,
    cwd: task.cwd,
    request: task.request,
    created_at: task.created_at,
    updated_at: task.updated_at,
    evidence_kind: task.evidence_kind,
    evidence_note: task.evidence_note,
  };
}

function sessionDto(session: SessionRow) {
  return {
    id: session.id,
    task_id: session.task_id,
    device_id: session.device_id,
    mode: session.mode,
    status: session.status,
    cwd: session.cwd,
    harness: session.harness,
    harness_version: session.harness_version,
    harness_session_id: session.harness_session_id,
    created_at: session.created_at,
    last_activity_at: session.last_activity_at,
  };
}

function turnDto(turn: TurnRow) {
  return {
    id: turn.id,
    seq: turn.seq,
    request: turn.request,
    status: turn.status,
    started_at: turn.started_at,
    ended_at: turn.ended_at,
    exit_code: turn.exit_code,
    result: turn.result,
    error: turn.error,
    created_at: turn.created_at,
  };
}

function commandDto(command: CommandRow) {
  return {
    id: command.id,
    kind: command.kind,
    status: command.status,
    created_at: command.created_at,
    delivered_at: command.delivered_at,
    acked_at: command.acked_at,
    error: command.error,
    expires_at: command.expires_at,
  };
}

function eventDto(event: TaskEventRow) {
  let payload: unknown = {};
  try {
    payload = JSON.parse(event.payload);
  } catch {
    payload = {};
  }
  return {
    event_id: event.event_id,
    type: event.type,
    at: event.at,
    turn_id: event.turn_id,
    payload,
  };
}

export interface TaskRouteDeps {
  config: CenterConfig;
  db: Db;
  transport: CommandTransport;
}

export function registerTaskRoutes(app: FastifyInstance, deps: TaskRouteDeps): void {
  const { config, db, transport } = deps;

  app.post("/api/tasks", { preHandler: requireAuth(config, db) }, async (request, reply) => {
    expireCommands(db);
    const parsed = createTaskSchema.safeParse(request.body);
    if (!parsed.success) throw new ApiError(400, "invalid_request", issuesText(parsed.error));
    const device = getDevice(db, parsed.data.device_id);
    if (!device) throw new ApiError(404, "device_not_found", "设备不存在");
    if (device.revoked_at) throw new ApiError(409, "device_revoked", "设备凭据已撤销，不能派发任务");

    const existing =
      parsed.data.idempotency_key != null
        ? db
            .prepare("SELECT id FROM tasks WHERE idempotency_key = ?")
            .get(parsed.data.idempotency_key) as unknown as { id: string } | undefined
        : undefined;

    const created = createTask(db, {
      request: parsed.data.request,
      title: parsed.data.title,
      deviceId: parsed.data.device_id,
      cwd: parsed.data.cwd,
      runProfile: parsed.data.run_profile,
      idempotencyKey: parsed.data.idempotency_key,
      timeoutSeconds: parsed.data.timeout_seconds,
    });

    if (!existing) {
      deliverCommand(db, created.command, transport);
    } else {
      const command = db.prepare("SELECT * FROM commands WHERE task_id = ? AND status = 'pending'").get(created.task.id) as unknown as CommandRow | undefined;
      if (command) deliverCommand(db, command, transport);
    }

    return reply.code(201).send({
      task: taskDto(getTask(db, created.task.id)!),
      session_id: created.session.id,
      command_id: created.command.id,
    });
  });

  app.get("/api/tasks", { preHandler: requireAuth(config, db) }, async (request) => {
    expireCommands(db);
    const query = request.query as { limit?: string; status?: string; device_id?: string };
    const limit = Math.min(500, Math.max(1, Number(query.limit ?? "100") || 100));
    let tasks = listTasks(db, 500);
    if (query.status) tasks = tasks.filter((task) => task.status === query.status);
    if (query.device_id) tasks = tasks.filter((task) => task.device_id === query.device_id);
    tasks = tasks.slice(0, limit);
    return { tasks: tasks.map(taskDto) };
  });

  app.get("/api/tasks/:id", { preHandler: requireAuth(config, db) }, async (request) => {
    expireCommands(db);
    const { id } = request.params as { id: string };
    const task = getTask(db, id);
    if (!task) throw new ApiError(404, "task_not_found", "任务不存在");
    const session = task.session_id ? getSession(db, task.session_id) : undefined;
    return {
      task: taskDto(task),
      session: session ? sessionDto(session) : null,
      turns: session ? listTurns(db, session.id).map(turnDto) : [],
      commands: listCommandsForTask(db, task.id).map(commandDto),
      events: listTaskEvents(db, task.id).map(eventDto),
    };
  });

  app.post("/api/tasks/:id/turns", { preHandler: requireAuth(config, db) }, async (request) => {
    const { id } = request.params as { id: string };
    const parsed = turnSchema.safeParse(request.body);
    if (!parsed.success) throw new ApiError(400, "invalid_request", issuesText(parsed.error));
    const result = createTurn(db, id, parsed.data.request, parsed.data.idempotency_key);
    if ("error" in result) {
      const statusByError: Record<string, number> = {
        task_not_found: 404,
        task_not_managed: 409,
        task_finished: 409,
        turn_in_progress: 409,
        session_missing: 409,
      };
      throw new ApiError(statusByError[result.error] ?? 409, result.error, "当前状态不允许追加输入");
    }
    deliverCommand(db, result.command, transport);
    return { task: taskDto(result.task), session_id: result.session.id, turn_id: result.turn.id, command_id: result.command.id };
  });

  app.post("/api/tasks/:id/cancel", { preHandler: requireAuth(config, db) }, async (request) => {
    const { id } = request.params as { id: string };
    const result = cancelTask(db, id);
    if ("error" in result) {
      if (result.error === "task_not_found") throw new ApiError(404, "task_not_found", "任务不存在");
      throw new ApiError(409, "task_finished", "任务已结束，不能取消");
    }
    if (result.command) deliverCommand(db, result.command, transport);
    return {
      task: taskDto(result.task),
      command_id: result.command?.id ?? null,
      cancelled_immediately: result.cancelledImmediately,
    };
  });

  app.get("/api/sessions", { preHandler: requireAuth(config, db) }, async (request) => {
    const query = request.query as { limit?: string; mode?: string };
    const limit = Math.min(500, Math.max(1, Number(query.limit ?? "100") || 100));
    let sessions = listSessions(db, 500);
    if (query.mode) sessions = sessions.filter((session) => session.mode === query.mode);
    sessions = sessions.slice(0, limit);
    return {
      sessions: sessions.map((session) => {
        const turns = db.prepare("SELECT COUNT(*) AS count FROM turns WHERE session_id = ?").get(session.id) as unknown as { count: number };
        return { ...sessionDto(session), turn_count: turns.count };
      }),
    };
  });

  app.get("/api/sessions/:id", { preHandler: requireAuth(config, db) }, async (request) => {
    const { id } = request.params as { id: string };
    const session = getSession(db, id);
    if (!session) throw new ApiError(404, "session_not_found", "会话不存在");
    const task = getTask(db, session.task_id);
    return {
      session: sessionDto(session),
      task: task ? taskDto(task) : null,
      turns: listTurns(db, session.id).map(turnDto),
      events: listSessionEvents(db, session.id).map(eventDto),
    };
  });

  // 供测试与内部调用复用：记录 Edge 上报的事件批（WS 走同一函数）
  app.post("/api/_internal/task-events", { preHandler: requireAuth(config, db) }, async (request) => {
    const body = request.body as { device_id?: string; events?: unknown[] };
    if (!Array.isArray(body.events)) throw new ApiError(400, "invalid_request", "events 必须是数组");
    const inserted = handleEventBatch(db, body.device_id ?? "unknown", body.events as never[]);
    return { inserted };
  });
}
