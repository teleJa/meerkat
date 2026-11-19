import type { FastifyInstance, FastifyRequest } from "fastify";
import type { CenterConfig } from "./config";
import type { Db } from "./db";
import { deriveStatus } from "./status";
import { ApiError, errorBody } from "./errors";
import { registerRequestSchema, PROTOCOL_VERSION } from "./protocol";
import {
  cleanupExpiredSessions,
  consumePairingToken,
  createPairingToken,
  createSession,
  deleteSession,
  getDevice,
  getSession,
  latestMetric,
  listEvents,
  listMetrics,
  registerDevice,
  revokeDevice,
  type DeviceRow,
  type MetricRow,
} from "./repo";
import { sha256Hex } from "./auth";

export interface RouteDeps {
  config: CenterConfig;
  db: Db;
  startedAt: number;
  connections: Map<string, { close: (code?: number, reason?: string) => void }>;
}

function deviceDto(row: DeviceRow, latest: MetricRow | undefined, config: CenterConfig, now: Date) {
  const { status, ageSeconds } = deriveStatus(
    row.last_seen_at,
    now,
    config.onlineThresholdSeconds,
    config.staleThresholdSeconds
  );
  let capabilities: string[] = [];
  try {
    capabilities = JSON.parse(row.capabilities) as string[];
  } catch {
    capabilities = [];
  }
  return {
    id: row.id,
    name: row.name,
    hostname: row.hostname,
    os: row.os,
    arch: row.arch,
    role: row.role,
    // 凭据已撤销的设备不再视为在线，避免与“已撤销”徽标矛盾。
    status: row.revoked_at ? "offline" : status,
    status_age_seconds: ageSeconds,
    last_seen_at: row.last_seen_at,
    last_metrics_at: row.last_metrics_at,
    edge_version: row.edge_version,
    protocol_version: row.protocol_version,
    boot_id: row.boot_id,
    capabilities,
    created_at: row.created_at,
    revoked_at: row.revoked_at,
    metrics: latest
      ? {
          sampled_at: latest.sampled_at,
          received_at: latest.received_at,
          cpu_pct: latest.cpu_pct,
          mem_used_bytes: latest.mem_used_bytes,
          mem_total_bytes: latest.mem_total_bytes,
          disk_used_bytes: latest.disk_used_bytes,
          disk_total_bytes: latest.disk_total_bytes,
          gpu_util_pct: latest.gpu_util_pct,
          gpu_mem_used_bytes: latest.gpu_mem_used_bytes,
          gpu_mem_total_bytes: latest.gpu_mem_total_bytes,
          gpu_temp_c: latest.gpu_temp_c,
        }
      : null,
  };
}

function issuesText(error: { issues: Array<{ path: PropertyKey[]; message: string }> }): string {
  return error.issues
    .slice(0, 5)
    .map((issue) => issue.path.map(String).join(".") + ": " + issue.message)
    .join("; ");
}

export interface AuthResult {
  via: "bearer" | "session";
  sessionId?: string;
  expiresAt?: string;
}

function parseCookies(header: string | undefined): Record<string, string> {
  const cookies: Record<string, string> = {};
  if (!header) return cookies;
  for (const part of header.split(";")) {
    const index = part.indexOf("=");
    if (index <= 0) continue;
    const key = part.slice(0, index).trim();
    const value = part.slice(index + 1).trim();
    if (key) cookies[key] = decodeURIComponent(value);
  }
  return cookies;
}

export function sessionCookie(config: CenterConfig, value: string, maxAgeSeconds: number): string {
  const parts = [
    config.cookieName + "=" + value,
    "Path=/",
    "HttpOnly",
    "SameSite=Lax",
    "Max-Age=" + maxAgeSeconds,
  ];
  if (config.cookieSecure) parts.push("Secure");
  return parts.join("; ");
}

export function authenticate(
  request: FastifyRequest,
  config: CenterConfig,
  db: Db
): AuthResult | null {
  const header = request.headers.authorization;
  const bearer =
    typeof header === "string" && header.startsWith("Bearer ") ? header.slice(7).trim() : "";
  if (bearer && sha256Hex(bearer) === sha256Hex(config.adminToken)) {
    return { via: "bearer" };
  }
  const cookieHeader = request.headers.cookie;
  const cookies = parseCookies(typeof cookieHeader === "string" ? cookieHeader : undefined);
  const sessionId = cookies[config.cookieName];
  if (!sessionId) return null;
  const session = getSession(db, sessionId);
  if (!session) return null;
  return { via: "session", sessionId, expiresAt: session.expires_at };
}

export function requireAuth(config: CenterConfig, db: Db) {
  return async (request: FastifyRequest) => {
    const auth = authenticate(request, config, db);
    if (!auth) {
      throw new ApiError(401, "unauthorized", "缺少或错误的管理凭据");
    }
    const method = request.method.toUpperCase();
    if (auth.via === "session" && method !== "GET" && method !== "HEAD" && method !== "OPTIONS") {
      const csrf = request.headers["x-meerkat-request"];
      if (csrf !== "1") {
        throw new ApiError(403, "csrf_required", "缺少 x-meerkat-request 请求头");
      }
    }
    return auth;
  };
}

export function registerRoutes(app: FastifyInstance, deps: RouteDeps): void {
  const { config, db } = deps;

  app.get("/api/health", async () => ({
    ok: true,
    version: config.centerVersion,
    time: new Date().toISOString(),
    uptime_seconds: Math.round((Date.now() - deps.startedAt) / 1000),
  }));

  app.post("/api/session", async (request, reply) => {
    const csrf = request.headers["x-meerkat-request"];
    if (csrf !== "1") {
      throw new ApiError(403, "csrf_required", "缺少 x-meerkat-request 请求头");
    }
    const body = (request.body ?? {}) as { token?: unknown };
    const token = typeof body.token === "string" ? body.token.trim() : "";
    if (!token || sha256Hex(token) !== sha256Hex(config.adminToken)) {
      throw new ApiError(401, "invalid_credentials", "访问令牌无效");
    }
    cleanupExpiredSessions(db);
    const session = createSession(db, config.sessionTtlSeconds);
    reply.header("set-cookie", sessionCookie(config, session.id, config.sessionTtlSeconds));
    return { authenticated: true, expires_at: session.expiresAt };
  });

  app.get("/api/session", async (request) => {
    const auth = authenticate(request, config, db);
    if (!auth) throw new ApiError(401, "unauthorized", "未登录");
    if (auth.via === "session") return { authenticated: true, via: "session", expires_at: auth.expiresAt };
    return { authenticated: true, via: "bearer" };
  });

  app.delete("/api/session", { preHandler: requireAuth(config, db) }, async (request, reply) => {
    const auth = authenticate(request, config, db);
    if (auth?.sessionId) deleteSession(db, auth.sessionId);
    reply.header("set-cookie", sessionCookie(config, "", 0));
    return { authenticated: false };
  });

  app.post("/api/pairing-tokens", { preHandler: requireAuth(config, db) }, async (request, reply) => {
    const body = (request.body ?? {}) as { label?: unknown; ttl_seconds?: unknown };
    const label = typeof body.label === "string" ? body.label.slice(0, 120) : undefined;
    const ttlRaw = typeof body.ttl_seconds === "number" ? body.ttl_seconds : config.pairingTtlSeconds;
    const ttlSeconds = Math.min(3600, Math.max(60, Math.round(ttlRaw)));
    const created = createPairingToken(db, { label, ttlSeconds });
    return reply.code(201).send(created);
  });

  app.post("/api/edges/register", async (request, reply) => {
    const parsed = registerRequestSchema.safeParse(request.body);
    if (!parsed.success) {
      throw new ApiError(400, "invalid_device", issuesText(parsed.error));
    }
    const body = parsed.data;
    if (body.protocol_version !== PROTOCOL_VERSION) {
      throw new ApiError(400, "unsupported_protocol", "仅支持协议版本 " + PROTOCOL_VERSION);
    }
    const consumed = consumePairingToken(db, body.pairing_token);
    if (!consumed) {
      throw new ApiError(401, "pairing_token_invalid", "配对令牌无效、已使用或已过期");
    }
    const { device, secret } = registerDevice(db, {
      device: { ...body.device, capabilities: body.device.capabilities ?? [] },
      edgeVersion: body.edge_version,
      protocolVersion: body.protocol_version,
    });
    app.log.info({ deviceId: device.id, hostname: device.hostname }, "edge registered");
    return reply.code(200).send({
      device_id: device.id,
      device_secret: secret,
      protocol_version: PROTOCOL_VERSION,
      center_time: new Date().toISOString(),
      heartbeat_interval_ms: config.heartbeatIntervalMs,
      metrics_interval_ms: config.metricsIntervalMs,
    });
  });

  app.get("/api/devices", { preHandler: requireAuth(config, db) }, async () => {
    const now = new Date();
    const rows = db.prepare("SELECT * FROM devices ORDER BY created_at ASC").all() as unknown as DeviceRow[];
    const devices = rows.map((row) => deviceDto(row, latestMetric(db, row.id), config, now));
    return { server_time: now.toISOString(), devices };
  });

  app.get("/api/devices/:id", { preHandler: requireAuth(config, db) }, async (request) => {
    const { id } = request.params as { id: string };
    const row = getDevice(db, id);
    if (!row) throw new ApiError(404, "device_not_found", "设备不存在");
    return { server_time: new Date().toISOString(), device: deviceDto(row, latestMetric(db, row.id), config, new Date()) };
  });

  app.get("/api/devices/:id/metrics", { preHandler: requireAuth(config, db) }, async (request) => {
    const { id } = request.params as { id: string };
    const query = request.query as { limit?: string };
    const limit = Math.min(500, Math.max(1, Number(query.limit ?? "60") || 60));
    const row = getDevice(db, id);
    if (!row) throw new ApiError(404, "device_not_found", "设备不存在");
    const samples = listMetrics(db, id, limit).map((sample) => ({
      sampled_at: sample.sampled_at,
      received_at: sample.received_at,
      cpu_pct: sample.cpu_pct,
      mem_used_bytes: sample.mem_used_bytes,
      mem_total_bytes: sample.mem_total_bytes,
      disk_used_bytes: sample.disk_used_bytes,
      disk_total_bytes: sample.disk_total_bytes,
      gpu_util_pct: sample.gpu_util_pct,
      gpu_mem_used_bytes: sample.gpu_mem_used_bytes,
      gpu_mem_total_bytes: sample.gpu_mem_total_bytes,
      gpu_temp_c: sample.gpu_temp_c,
    }));
    return { device_id: id, samples };
  });

  app.post("/api/devices/:id/revoke", { preHandler: requireAuth(config, db) }, async (request) => {
    const { id } = request.params as { id: string };
    const row = getDevice(db, id);
    if (!row) throw new ApiError(404, "device_not_found", "设备不存在");
    revokeDevice(db, id);
    const socket = deps.connections.get(id);
    if (socket) {
      try {
        socket.close(4401, "revoked");
      } catch {
        /* ignore */
      }
      deps.connections.delete(id);
    }
    return { device_id: id, revoked: true };
  });

  app.get("/api/events", { preHandler: requireAuth(config, db) }, async (request) => {
    const query = request.query as { limit?: string };
    const limit = Math.min(500, Math.max(1, Number(query.limit ?? "50") || 50));
    return { events: listEvents(db, limit) };
  });

  app.setErrorHandler((error, request, reply) => {
    if (error instanceof ApiError) {
      return reply.code(error.statusCode).send(errorBody(error.code, error.message));
    }
    const statusCode = (error as { statusCode?: number }).statusCode;
    const message = error instanceof Error ? error.message : String(error);
    if (typeof statusCode === "number" && statusCode < 500) {
      return reply.code(statusCode).send(errorBody("invalid_request", message));
    }
    request.log.error({ err: error }, "unhandled error");
    return reply.code(500).send(errorBody("internal_error", "中心内部错误"));
  });

  app.setNotFoundHandler((request, reply) => {
    return reply.code(404).send(errorBody("not_found", "接口不存在: " + request.method + " " + request.url));
  });
}
