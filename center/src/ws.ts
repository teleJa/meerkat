import type { WebSocket } from "ws";
import type { CenterConfig } from "./config";
import type { Db } from "./db";
import { sha256Hex } from "./auth";
import { PROTOCOL_VERSION, wsMessageSchema } from "./protocol";
import { getDevice, insertMetric, logEvent, touchDevice } from "./repo";
import {
  handleCommandAck,
  handleEventBatch,
  handleSessionStarted,
  handleTurnCompleted,
  handleTurnStarted,
} from "./task-repo";
import { deliverPendingCommands, type CommandTransport } from "./task-delivery";

export interface GatewayLogger {
  info: (obj: unknown, msg?: string) => void;
  warn: (obj: unknown, msg?: string) => void;
  error: (obj: unknown, msg?: string) => void;
}

export interface GatewayDeps {
  config: CenterConfig;
  db: Db;
  log: GatewayLogger;
  connections: Map<string, WebSocket>;
  transport: CommandTransport;
  remoteAddress?: string;
}

const OPEN = 1;

function issuesText(error: { issues: Array<{ path: PropertyKey[]; message: string }> }): string {
  return error.issues
    .slice(0, 5)
    .map((issue) => issue.path.map(String).join(".") + ": " + issue.message)
    .join("; ");
}

export function handleEdgeConnection(socket: WebSocket, deps: GatewayDeps): void {
  const { config, db, log, connections } = deps;
  let deviceId: string | null = null;
  let helloSettled = false;
  let timer: NodeJS.Timeout | undefined;

  const send = (message: unknown) => {
    if (socket.readyState === OPEN) socket.send(JSON.stringify(message));
  };

  const schedule = (ms: number) => {
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => {
      fail(4400, "inactivity_timeout", "长时间未收到 Edge 消息");
    }, ms);
  };

  const fail = (closeCode: number, code: string, message: string) => {
    send({ type: "error", code, message });
    try {
      socket.close(closeCode, code);
    } catch {
      /* socket already closed */
    }
  };

  const markSeen = (patch: { last_metrics_at?: string }) => {
    if (!deviceId) return;
    touchDevice(db, deviceId, {
      last_seen_at: new Date().toISOString(),
      ...(patch.last_metrics_at ? { last_metrics_at: patch.last_metrics_at } : {}),
    });
  };

  schedule(config.wsHelloTimeoutMs);

  socket.on("message", (raw) => {
    let parsedJson: unknown;
    try {
      parsedJson = JSON.parse(typeof raw === "string" ? raw : raw.toString("utf8"));
    } catch {
      fail(4400, "invalid_json", "消息不是合法 JSON");
      return;
    }

    const parsed = wsMessageSchema.safeParse(parsedJson);
    if (!parsed.success) {
      fail(4400, "invalid_message", issuesText(parsed.error));
      return;
    }
    const message = parsed.data;

    try {
      if (!helloSettled) {
        if (message.type !== "hello") {
          fail(4400, "hello_required", "首条消息必须为 hello");
          return;
        }
        if (message.protocol_version !== PROTOCOL_VERSION) {
          fail(4400, "unsupported_protocol", "仅支持协议版本 " + PROTOCOL_VERSION);
          return;
        }
        const device = getDevice(db, message.device_id);
        if (!device || device.revoked_at) {
          fail(4401, "auth_failed", "设备不存在或凭据已撤销");
          return;
        }
        if (sha256Hex(message.device_secret) !== device.secret_hash) {
          fail(4401, "auth_failed", "device_secret 无效");
          return;
        }

        helloSettled = true;
        deviceId = device.id;

        const previous = connections.get(device.id);
        if (previous && previous !== socket) {
          try {
            previous.close(1000, "replaced");
          } catch {
            /* ignore */
          }
        }
        connections.set(device.id, socket);

        touchDevice(db, device.id, {
          last_seen_at: new Date().toISOString(),
          boot_id: message.boot_id ?? device.boot_id,
          edge_version: message.edge_version ?? device.edge_version,
          capabilities: message.capabilities ? JSON.stringify(message.capabilities) : device.capabilities,
        });
        logEvent(
          db,
          "device.connected",
          device.id,
          JSON.stringify({ remote: deps.remoteAddress ?? null, edge_version: message.edge_version ?? null })
        );

        send({
          type: "hello.ack",
          protocol_version: PROTOCOL_VERSION,
          server_time: new Date().toISOString(),
          device_id: device.id,
          heartbeat_interval_ms: config.heartbeatIntervalMs,
          metrics_interval_ms: config.metricsIntervalMs,
        });
        schedule(config.wsInactivityTimeoutMs);
        // 重连补发未确认的命令（Edge 按 command_id 去重）。
        try {
          const delivered = deliverPendingCommands(db, device.id, deps.transport);
          if (delivered > 0) log.info({ deviceId: device.id, delivered }, "pending commands re-delivered");
        } catch (error) {
          log.warn({ err: error, deviceId: device.id }, "failed to deliver pending commands");
        }
        log.info({ deviceId: device.id, hostname: device.hostname }, "edge websocket connected");
        return;
      }

      if (message.type === "hello") {
        fail(4400, "duplicate_hello", "连接已初始化，不能重复 hello");
        return;
      }

      if (message.type === "heartbeat") {
        markSeen({});
        schedule(config.wsInactivityTimeoutMs);
        send({ type: "heartbeat.ack", seq: message.seq, received_at: new Date().toISOString() });
        return;
      }

      if (message.type === "metrics.batch") {
        const sampledAt = Date.parse(message.sampled_at);
        if (Number.isNaN(sampledAt)) {
          fail(4400, "invalid_timestamp", "sampled_at 不是合法时间");
          return;
        }
        if (!deviceId) return;
        const receivedAt = new Date();
        insertMetric(db, deviceId, message.sampled_at, message.metrics, receivedAt);
        markSeen({ last_metrics_at: receivedAt.toISOString() });
        schedule(config.wsInactivityTimeoutMs);
        send({ type: "metrics.ack", seq: message.seq, received_at: receivedAt.toISOString() });
        return;
      }

      if (message.type === "command.ack") {
        handleCommandAck(db, message);
        schedule(config.wsInactivityTimeoutMs);
        return;
      }

      if (message.type === "session.started") {
        handleSessionStarted(db, message);
        schedule(config.wsInactivityTimeoutMs);
        return;
      }

      if (message.type === "turn.started") {
        handleTurnStarted(db, message);
        schedule(config.wsInactivityTimeoutMs);
        return;
      }

      if (message.type === "event.batch") {
        if (!deviceId) return;
        const inserted = handleEventBatch(db, deviceId, message.events);
        schedule(config.wsInactivityTimeoutMs);
        send({
          type: "event.ack",
          stream_id: message.stream_id,
          seq: message.seq,
          inserted,
          received_at: new Date().toISOString(),
        });
        return;
      }

      if (message.type === "turn.completed") {
        handleTurnCompleted(db, message);
        schedule(config.wsInactivityTimeoutMs);
        return;
      }
    } catch (error) {
      log.error({ err: error, deviceId }, "edge websocket handler failed");
      fail(1011, "internal_error", "中心处理消息失败");
    }
  });

  socket.on("close", (code: number, reason: Buffer) => {
    if (timer) clearTimeout(timer);
    const isCurrent = deviceId ? connections.get(deviceId) === socket : false;
    if (deviceId && (isCurrent || code === 4401)) {
      connections.delete(deviceId);
      try {
        logEvent(db, "device.disconnected", deviceId, JSON.stringify({ code, reason: reason.toString() }));
      } catch (error) {
        // 中心关闭过程中数据库可能已关闭；连接日志失败不应影响退出。
        log.warn({ err: error, deviceId }, "failed to persist device.disconnected");
      }
      log.info({ deviceId, code }, "edge websocket disconnected");
    }
  });

  socket.on("error", (error: Error) => {
    log.warn({ err: error, deviceId }, "edge websocket error");
  });
}
