import { afterEach, describe, expect, it } from "vitest";
import WebSocket from "ws";
import type { AddressInfo } from "node:net";
import { adminHeaders, createToken, registerEdge, testContext } from "./helpers";
import type { TestContext } from "./helpers";

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
      await Promise.race([closed, new Promise((resolve) => setTimeout(resolve, 300))]);
    }
  }
  if (current) {
    await current.app.close();
    current.db.close();
    current = undefined;
  }
});

function nextMessage(socket: WebSocket, timeoutMs = 3000): Promise<Record<string, unknown>> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("timeout waiting for ws message")), timeoutMs);
    socket.once("message", (raw) => {
      clearTimeout(timer);
      resolve(JSON.parse(raw.toString()) as Record<string, unknown>);
    });
  });
}

function nextClose(socket: WebSocket, timeoutMs = 3000): Promise<{ code: number; reason: string }> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("timeout waiting for ws close")), timeoutMs);
    socket.once("close", (code, reason) => {
      clearTimeout(timer);
      resolve({ code, reason: reason.toString() });
    });
  });
}

async function start(): Promise<{ ctx: TestContext; baseUrl: string; registered: { device_id: string; device_secret: string } }> {
  const ctx = testContext();
  current = ctx;
  await ctx.app.listen({ host: "127.0.0.1", port: 0 });
  const address = ctx.app.server.address() as AddressInfo;
  const token = await createToken(ctx.app);
  const registered = await registerEdge(ctx.app, token.token);
  return { ctx, baseUrl: "ws://127.0.0.1:" + address.port + "/api/edges/ws", registered };
}

function connect(url: string): Promise<WebSocket> {
  return new Promise((resolve, reject) => {
    const socket = new WebSocket(url);
    sockets.push(socket);
    socket.once("open", () => resolve(socket));
    socket.once("error", reject);
  });
}

describe("edge websocket", () => {
  it("authenticates, acks heartbeat and ingests metrics", async () => {
    const { ctx, baseUrl, registered } = await start();
    const socket = await connect(baseUrl);

    socket.send(
      JSON.stringify({
        type: "hello",
        protocol_version: "1",
        device_id: registered.device_id,
        device_secret: registered.device_secret,
        edge_version: "0.1.0-test",
        boot_id: "boot_test01",
        capabilities: ["metrics"],
      })
    );
    const helloAck = await nextMessage(socket);
    expect(helloAck.type).toBe("hello.ack");
    expect(helloAck.device_id).toBe(registered.device_id);
    expect(helloAck.metrics_interval_ms).toBe(5000);

    socket.send(JSON.stringify({ type: "heartbeat", seq: 1, sent_at: new Date().toISOString() }));
    const heartbeatAck = await nextMessage(socket);
    expect(heartbeatAck).toEqual(expect.objectContaining({ type: "heartbeat.ack", seq: 1 }));

    socket.send(
      JSON.stringify({
        type: "metrics.batch",
        stream_id: "str_test",
        seq: 1,
        sampled_at: new Date().toISOString(),
        metrics: {
          cpu_pct: 18.4,
          mem_used_bytes: 1024,
          mem_total_bytes: 4096,
          disk_used_bytes: 100,
          disk_total_bytes: 1000,
          gpu_util_pct: null,
          gpu_mem_used_bytes: null,
          gpu_mem_total_bytes: null,
          gpu_temp_c: null,
        },
      })
    );
    const metricsAck = await nextMessage(socket);
    expect(metricsAck.type).toBe("metrics.ack");
    expect(metricsAck.seq).toBe(1);

    const list = await ctx.app.inject({ method: "GET", url: "/api/devices", headers: adminHeaders });
    const device = list.json().devices[0];
    expect(device.status).toBe("online");
    expect(device.metrics.cpu_pct).toBeCloseTo(18.4);
    expect(device.metrics.mem_total_bytes).toBe(4096);
    expect(device.last_metrics_at).toBeTruthy();

    const metrics = await ctx.app.inject({
      method: "GET",
      url: "/api/devices/" + registered.device_id + "/metrics?limit=10",
      headers: adminHeaders,
    });
    expect(metrics.json().samples).toHaveLength(1);
    expect(metrics.json().samples[0].cpu_pct).toBeCloseTo(18.4);
  });

  it("rejects a bad device secret with 4401", async () => {
    const { baseUrl, registered } = await start();
    const socket = await connect(baseUrl);
    const closed = nextClose(socket);
    socket.send(
      JSON.stringify({
        type: "hello",
        protocol_version: "1",
        device_id: registered.device_id,
        device_secret: "0".repeat(64),
        edge_version: "0.1.0-test",
        boot_id: "boot_test01",
        capabilities: [],
      })
    );
    const message = await nextMessage(socket);
    expect(message.type).toBe("error");
    expect(message.code).toBe("auth_failed");
    const close = await closed;
    expect(close.code).toBe(4401);
  });

  it("closes the socket and records events when a device is revoked", async () => {
    const { ctx, baseUrl, registered } = await start();
    const socket = await connect(baseUrl);
    socket.send(
      JSON.stringify({
        type: "hello",
        protocol_version: "1",
        device_id: registered.device_id,
        device_secret: registered.device_secret,
        edge_version: "0.1.0-test",
        boot_id: "boot_test01",
        capabilities: ["metrics"],
      })
    );
    const ack = await nextMessage(socket);
    expect(ack.type).toBe("hello.ack");

    const closed = nextClose(socket);
    const revoke = await ctx.app.inject({
      method: "POST",
      url: "/api/devices/" + registered.device_id + "/revoke",
      headers: adminHeaders,
    });
    expect(revoke.statusCode).toBe(200);
    const close = await closed;
    expect(close.code).toBe(4401);

    const events = await ctx.app.inject({
      method: "GET",
      url: "/api/events?limit=10",
      headers: adminHeaders,
    });
    const types = events.json().events.map((event: { type: string }) => event.type);
    expect(types).toContain("device.revoked");
    expect(types).toContain("device.disconnected");
  });

  it("requires hello first", async () => {
    const { baseUrl } = await start();
    const socket = await connect(baseUrl);
    const closed = nextClose(socket);
    socket.send(JSON.stringify({ type: "heartbeat", seq: 1 }));
    const message = await nextMessage(socket);
    expect(message.type).toBe("error");
    expect(message.code).toBe("hello_required");
    const close = await closed;
    expect(close.code).toBe(4400);
  });
});
