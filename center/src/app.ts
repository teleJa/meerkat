import { existsSync } from "node:fs";
import path from "node:path";
import Fastify, { type FastifyInstance } from "fastify";
import fastifyStatic from "@fastify/static";
import websocket from "@fastify/websocket";
import type { WebSocket } from "ws";
import type { CenterConfig } from "./config";
import type { Db } from "./db";
import { cleanupOldMetrics } from "./repo";
import { registerRoutes } from "./routes";
import { registerTaskRoutes } from "./task-routes";
import type { CommandTransport } from "./task-delivery";
import { handleEdgeConnection } from "./ws";

export interface BuildAppOptions {
  config: CenterConfig;
  db: Db;
  logger?: boolean | { level?: string };
}

export function buildApp(options: BuildAppOptions): FastifyInstance {
  const { config, db } = options;
  const app = Fastify({ logger: options.logger ?? false });
  const startedAt = Date.now();
  const connections = new Map<string, WebSocket>();
  const transport: CommandTransport = {
    isConnected: (deviceId) => connections.has(deviceId),
    send: (deviceId, message) => {
      const socket = connections.get(deviceId);
      if (!socket || socket.readyState !== 1) return false;
      socket.send(JSON.stringify(message));
      return true;
    },
  };

  app.register(websocket);

  // 必须在 websocket 插件的 onRoute hook 生效后注册，否则升级请求会 500。
  app.register(async (instance) => {
    instance.get("/api/edges/ws", { websocket: true }, (socket, request) => {
      handleEdgeConnection(socket as unknown as WebSocket, {
        config,
        db,
        log: instance.log,
        connections,
        transport,
        remoteAddress: request.socket.remoteAddress,
      });
    });
  });

  registerRoutes(app, { config, db, startedAt, connections });
  registerTaskRoutes(app, { config, db, transport });

  // 指标保留 24 小时：启动时清理一次，之后每 10 分钟清理；unref 不阻止退出。
  const retentionTimer = setInterval(() => {
    try {
      cleanupOldMetrics(db, 24);
    } catch (error) {
      app.log.warn({ err: error }, "metrics retention cleanup failed");
    }
  }, 10 * 60 * 1000);
  retentionTimer.unref();
  app.addHook("onClose", async () => {
    clearInterval(retentionTimer);
  });

  if (existsSync(path.join(config.webDistPath, "index.html"))) {
    app.register(fastifyStatic, { root: config.webDistPath, prefix: "/" });
    app.log.info({ webDistPath: config.webDistPath }, "serving web panel");
  } else {
    app.log.warn({ webDistPath: config.webDistPath }, "web dist not found; API only");
  }

  return app;
}
