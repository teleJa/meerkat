import { randomBytes } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";

export interface CenterConfig {
  host: string;
  port: number;
  dataDir: string;
  dbPath: string;
  adminToken: string;
  protocolVersion: string;
  centerVersion: string;
  pairingTtlSeconds: number;
  heartbeatIntervalMs: number;
  metricsIntervalMs: number;
  onlineThresholdSeconds: number;
  staleThresholdSeconds: number;
  wsHelloTimeoutMs: number;
  wsInactivityTimeoutMs: number;
  sessionTtlSeconds: number;
  cookieName: string;
  cookieSecure: boolean;
  webDistPath: string;
}

function resolveAdminToken(dataDir: string): string {
  const fromEnv = process.env.MEERKAT_ADMIN_TOKEN?.trim();
  if (fromEnv) return fromEnv;

  const file = path.join(dataDir, "admin-token");
  if (existsSync(file)) {
    const value = readFileSync(file, "utf8").trim();
    if (value.length >= 16) return value;
  }

  const token = randomBytes(24).toString("hex");
  mkdirSync(dataDir, { recursive: true });
  writeFileSync(file, token + "\n", { mode: 0o600 });
  chmodSync(file, 0o600);
  return token;
}

export function createConfig(overrides: Partial<CenterConfig> = {}): CenterConfig {
  const dataDir =
    overrides.dataDir ?? process.env.MEERKAT_DATA_DIR ?? path.resolve(process.cwd(), "data");
  const base: CenterConfig = {
    host: process.env.MEERKAT_HOST ?? "0.0.0.0",
    port: Number(process.env.MEERKAT_PORT ?? 8787),
    dataDir,
    dbPath: process.env.MEERKAT_DB_PATH ?? path.join(dataDir, "meerkat.db"),
    adminToken: "",
    protocolVersion: "1",
    centerVersion: "0.1.0",
    pairingTtlSeconds: 600,
    heartbeatIntervalMs: 10_000,
    metricsIntervalMs: 5_000,
    onlineThresholdSeconds: 30,
    staleThresholdSeconds: 60,
    wsHelloTimeoutMs: 10_000,
    wsInactivityTimeoutMs: 90_000,
    sessionTtlSeconds: 12 * 60 * 60,
    cookieName: "meerkat_session",
    cookieSecure: process.env.MEERKAT_COOKIE_SECURE === "1",
    webDistPath: process.env.MEERKAT_WEB_DIST ?? path.resolve(process.cwd(), "..", "web", "dist"),
  };
  const merged = { ...base, ...overrides };
  merged.adminToken = overrides.adminToken ?? resolveAdminToken(merged.dataDir);
  return merged;
}

export function loadConfig(): CenterConfig {
  return createConfig();
}
