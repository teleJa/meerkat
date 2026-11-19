import { afterEach, describe, expect, it } from "vitest";
import WebSocket from "ws";
import type { AddressInfo } from "node:net";
import { adminHeaders, createToken, registerEdge, testContext } from "./helpers";
import type { TestContext } from "./helpers";

type Message = Record<string, unknown>;

let current: TestContext | undefined;
const sockets: WebSocket[] = [];

afterEach(async () => {
  for (const socket of sockets.splice(0)) {
    if (socket.readyState === socket.OPEN || socket.readyState === socket.CONNECTING) {
      const closed = new Promise<void>((resolve) => socket.once("close", () => resolve()));
      try {
        socket.close();
      } catch {
        /* ignore */
      }
      await Promise.race([closed, new Promise((resolve) => setTimeout(resolve, 200))]);
    }
  }
  if (current) {
    await current.app.close();
    current.db.close();
    current = undefined;
  }
});

class Harness {
  private buffered: Message[] = [];
  private waiters: Array<{ predicate: (m: Message) => boolean; resolve: (m: Message) => void; timer: NodeJS.Timeout }> = [];

  constructor(private readonly socket: WebSocket) {
    socket.on("message", (raw) => {
      const message = JSON.parse(raw.toString()) as Message;
      const index = this.waiters.findIndex((waiter) => waiter.predicate(message));
      if (index >= 0) {
        const waiter = this.waiters.splice(index, 1)[0];
        if (waiter) {
          clearTimeout(waiter.timer);
          waiter.resolve(message);
        }
      } else {
        this.buffered.push(message);
      }
    });
  }

  send(message: Message): void {
    this.socket.send(JSON.stringify(message));
  }

  waitFor(predicate: (m: Message) => boolean, label: string, timeoutMs = 5000): Promise<Message> {
    const index = this.buffered.findIndex(predicate);
    if (index >= 0) return Promise.resolve(this.buffered.splice(index, 1)[0] as Message);
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.waiters = this.waiters.filter((waiter) => waiter.timer !== timer);
        reject(new Error("timeout waiting for " + label));
      }, timeoutMs);
      this.waiters.push({ predicate, resolve, timer });
    });
  }
}

async function setup() {
  const ctx = testContext();
  current = ctx;
  await ctx.app.listen({ host: "127.0.0.1", port: 0 });
  const address = ctx.app.server.address() as AddressInfo;
  const token = await createToken(ctx.app);
  const device = await registerEdge(ctx.app, token.token);
  return { ctx, wsUrl: "ws://127.0.0.1:" + address.port + "/api/edges/ws", device };
}

async function connect(wsUrl: string, device: { device_id: string; device_secret: string }): Promise<Harness> {
  const socket = await new Promise<WebSocket>((resolve, reject) => {
    const ws = new WebSocket(wsUrl);
    sockets.push(ws);
    ws.once("open", () => resolve(ws));
    ws.once("error", reject);
  });
  const harness = new Harness(socket);
  harness.send({
    type: "hello",
    protocol_version: "1",
    device_id: device.device_id,
    device_secret: device.device_secret,
    edge_version: "0.1.0-test",
    boot_id: "boot_test01",
    capabilities: ["metrics"],
  });
  await harness.waitFor((m) => m.type === "hello.ack", "hello.ack");
  return harness;
}

async function createTask(deviceId: string, request = "Reply OK") {
  const response = await current!.app.inject({
    method: "POST",
    url: "/api/tasks",
    headers: adminHeaders,
    payload: { request, device_id: deviceId, cwd: "/tmp" },
  });
  return response;
}

async function getTask(taskId: string) {
  const response = await current!.app.inject({
    method: "GET",
    url: "/api/tasks/" + taskId,
    headers: adminHeaders,
  });
  return response.json();
}

function nowIso() {
  return new Date().toISOString();
}

describe("task dispatch over websocket", () => {
  it("dispatches a command, ingests events and completes the turn", async () => {
    const { wsUrl, device } = await setup();
    const harness = await connect(wsUrl, device);
    const response = await createTask(device.device_id);
    expect(response.statusCode).toBe(201);
    const created = response.json();
    expect(created.task.status).toBe("queued");

    const command = await harness.waitFor((m) => m.type === "command", "command");
    expect(command.kind).toBe("start_turn");
    expect(command.command_id).toBe(created.command_id);
    expect((command.payload as Record<string, unknown>).request).toBe("Reply OK");

    const taskId = created.task.id as string;
    const sessionId = created.session_id as string;
    const turnId = command.turn_id as string;

    harness.send({ type: "command.ack", command_id: created.command_id, status: "accepted" });
    harness.send({
      type: "session.started",
      command_id: created.command_id,
      task_id: taskId,
      session_id: sessionId,
      harness_session_id: "claude_test_1",
      harness_version: "2.1.304",
    });
    harness.send({ type: "turn.started", task_id: taskId, session_id: sessionId, turn_id: turnId });
    harness.send({
      type: "event.batch",
      stream_id: "str_test",
      seq: 1,
      events: [
        {
          event_id: "evt_1",
          task_id: taskId,
          session_id: sessionId,
          turn_id: turnId,
          type: "assistant.message",
          at: nowIso(),
          payload: { text: "OK" },
        },
      ],
    });
    const ack = await harness.waitFor((m) => m.type === "event.ack", "event.ack");
    expect(ack.seq).toBe(1);
    expect(ack.inserted).toBe(1);

    harness.send({
      type: "turn.completed",
      task_id: taskId,
      session_id: sessionId,
      turn_id: turnId,
      status: "succeeded",
      exit_code: 0,
      duration_ms: 1234,
      result: "OK",
    });

    await new Promise((resolve) => setTimeout(resolve, 120));
    const detail = await getTask(taskId);
    expect(detail.task.status).toBe("succeeded");
    expect(detail.task.evidence_kind).toBe("explicit_agent_report");
    expect(detail.session.harness_session_id).toBe("claude_test_1");
    expect(detail.turns).toHaveLength(1);
    expect(detail.turns[0].status).toBe("succeeded");
    expect(detail.turns[0].result).toBe("OK");
    expect(detail.events).toHaveLength(1);
    expect(detail.commands[0].status).toBe("done");
  });

  it("queues commands for offline devices and delivers on reconnect", async () => {
    const { wsUrl, device } = await setup();
    const first = await connect(wsUrl, device);
    await new Promise((resolve) => setTimeout(resolve, 50));
    first.send({ type: "heartbeat", seq: 1 });
    // 断线
    for (const socket of sockets.splice(0)) socket.close();
    await new Promise((resolve) => setTimeout(resolve, 100));

    const response = await createTask(device.device_id, "Offline task");
    expect(response.statusCode).toBe(201);
    const created = response.json();
    expect(created.task.status).toBe("queued");

    const detailBefore = await getTask(created.task.id);
    expect(detailBefore.commands[0].status).toBe("pending");

    const harness = await connect(wsUrl, device);
    const command = await harness.waitFor((m) => m.type === "command", "delivered command");
    expect(command.command_id).toBe(created.command_id);
    expect(command.kind).toBe("start_turn");
  });

  it("expires stale commands and fails the task", async () => {
    const { device } = await setup();
    const response = await createTask(device.device_id, "Stale task");
    const created = response.json();
    current!.db
      .prepare("UPDATE commands SET expires_at = ? WHERE task_id = ?")
      .run("2020-01-01T00:00:00.000Z", created.task.id);

    const list = await current!.app.inject({ method: "GET", url: "/api/tasks", headers: adminHeaders });
    const task = list.json().tasks.find((item: { id: string }) => item.id === created.task.id);
    expect(task.status).toBe("failed");
    expect(task.stage).toContain("过期");
  });

  it("rejects dispatch for revoked devices", async () => {
    const { device } = await setup();
    await current!.app.inject({
      method: "POST",
      url: "/api/devices/" + device.device_id + "/revoke",
      headers: adminHeaders,
    });
    const response = await createTask(device.device_id);
    expect(response.statusCode).toBe(409);
    expect(response.json().error.code).toBe("device_revoked");
  });
});

describe("follow-up turns and cancellation", () => {
  it("adds a follow-up turn with the harness session id", async () => {
    const { wsUrl, device } = await setup();
    const harness = await connect(wsUrl, device);
    const created = (await createTask(device.device_id)).json();
    const first = await harness.waitFor((m) => m.type === "command", "first command");
    const taskId = created.task.id as string;
    const sessionId = created.session_id as string;

    harness.send({ type: "command.ack", command_id: created.command_id, status: "accepted" });
    harness.send({
      type: "session.started",
      task_id: taskId,
      session_id: sessionId,
      harness_session_id: "claude_test_2",
    });
    harness.send({ type: "turn.started", task_id: taskId, session_id: sessionId, turn_id: first.turn_id });
    harness.send({
      type: "turn.completed",
      task_id: taskId,
      session_id: sessionId,
      turn_id: first.turn_id,
      status: "succeeded",
      result: "done-1",
    });
    await new Promise((resolve) => setTimeout(resolve, 100));

    const follow = await current!.app.inject({
      method: "POST",
      url: "/api/tasks/" + taskId + "/turns",
      headers: adminHeaders,
      payload: { request: "再补充一句" },
    });
    expect(follow.statusCode).toBe(200);
    const followBody = follow.json();
    expect(followBody.turn_id).toBeTruthy();

    const second = await harness.waitFor((m) => m.type === "command" && m.kind === "send_turn", "send_turn command");
    expect((second.payload as Record<string, unknown>).harness_session_id).toBe("claude_test_2");
    expect((second.payload as Record<string, unknown>).request).toBe("再补充一句");

    harness.send({ type: "command.ack", command_id: followBody.command_id, status: "accepted" });
    harness.send({ type: "turn.started", task_id: taskId, session_id: sessionId, turn_id: followBody.turn_id });
    harness.send({
      type: "turn.completed",
      task_id: taskId,
      session_id: sessionId,
      turn_id: followBody.turn_id,
      status: "succeeded",
      result: "done-2",
    });
    await new Promise((resolve) => setTimeout(resolve, 100));

    const detail = await getTask(taskId);
    expect(detail.turns).toHaveLength(2);
    expect(detail.turns[1].request).toBe("再补充一句");
    expect(detail.task.status).toBe("succeeded");
  });

  it("cancels a running task through the edge", async () => {
    const { wsUrl, device } = await setup();
    const harness = await connect(wsUrl, device);
    const created = (await createTask(device.device_id)).json();
    const first = await harness.waitFor((m) => m.type === "command", "start command");
    const taskId = created.task.id as string;
    const sessionId = created.session_id as string;

    harness.send({ type: "command.ack", command_id: created.command_id, status: "accepted" });
    harness.send({ type: "turn.started", task_id: taskId, session_id: sessionId, turn_id: first.turn_id });
    await new Promise((resolve) => setTimeout(resolve, 80));

    const cancel = await current!.app.inject({
      method: "POST",
      url: "/api/tasks/" + taskId + "/cancel",
      headers: adminHeaders,
    });
    expect(cancel.statusCode).toBe(200);
    expect(cancel.json().task.status).toBe("cancel_requested");
    const cancelCommand = await harness.waitFor((m) => m.type === "command" && m.kind === "cancel", "cancel command");
    expect(cancelCommand.command_id).toBe(cancel.json().command_id);

    harness.send({ type: "command.ack", command_id: cancelCommand.command_id, status: "accepted" });
    harness.send({
      type: "turn.completed",
      task_id: taskId,
      session_id: sessionId,
      turn_id: first.turn_id,
      status: "cancelled",
      error: "cancelled by user",
    });
    await new Promise((resolve) => setTimeout(resolve, 120));
    const detail = await getTask(taskId);
    expect(detail.task.status).toBe("cancelled");
    expect(detail.turns[0].status).toBe("cancelled");
  });

  it("marks the task failed when the edge rejects a command", async () => {
    const { wsUrl, device } = await setup();
    const harness = await connect(wsUrl, device);
    const created = (await createTask(device.device_id)).json();
    await harness.waitFor((m) => m.type === "command", "command");
    harness.send({
      type: "command.ack",
      command_id: created.command_id,
      status: "rejected",
      detail: "cwd_not_allowed",
    });
    await new Promise((resolve) => setTimeout(resolve, 120));
    const detail = await getTask(created.task.id);
    expect(detail.task.status).toBe("failed");
    expect(detail.task.stage).toContain("cwd_not_allowed");
  });
});
