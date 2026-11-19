import { randomUUID } from "node:crypto";
import type { Db } from "./db";
import {
  generateDeviceId,
  generateDeviceSecret,
  generatePairingToken,
  generateSessionId,
  sha256Hex,
} from "./auth";
import type { DeviceInfo, MetricsPayload } from "./protocol";

export interface DeviceRow {
  id: string;
  name: string;
  hostname: string;
  os: string;
  arch: string;
  role: string;
  boot_id: string;
  capabilities: string;
  edge_version: string;
  protocol_version: string;
  secret_hash: string;
  created_at: string;
  last_seen_at: string | null;
  last_metrics_at: string | null;
  revoked_at: string | null;
}

export interface MetricRow {
  id: number;
  device_id: string;
  sampled_at: string;
  received_at: string;
  cpu_pct: number | null;
  mem_used_bytes: number | null;
  mem_total_bytes: number | null;
  disk_used_bytes: number | null;
  disk_total_bytes: number | null;
  gpu_util_pct: number | null;
  gpu_mem_used_bytes: number | null;
  gpu_mem_total_bytes: number | null;
  gpu_temp_c: number | null;
}

export function logEvent(db: Db, type: string, deviceId: string | null, detail?: string): void {
  db.prepare("INSERT INTO events (at, device_id, type, detail) VALUES (?, ?, ?, ?)").run(
    new Date().toISOString(),
    deviceId,
    type,
    detail ?? null
  );
}

export function createPairingToken(
  db: Db,
  options: { label?: string; ttlSeconds: number; now?: Date }
): { token: string; expires_at: string; label: string | null } {
  const now = options.now ?? new Date();
  const token = generatePairingToken();
  const expiresAt = new Date(now.getTime() + options.ttlSeconds * 1000).toISOString();
  const label = options.label?.trim() || null;
  db.prepare(
    "INSERT INTO pairing_tokens (token_hash, label, created_at, expires_at) VALUES (?, ?, ?, ?)"
  ).run(sha256Hex(token), label, now.toISOString(), expiresAt);
  return { token, expires_at: expiresAt, label };
}

export function consumePairingToken(db: Db, token: string, now = new Date()): boolean {
  const hash = sha256Hex(token);
  db.exec("BEGIN IMMEDIATE");
  try {
    const row = db
      .prepare("SELECT token_hash, expires_at, used_at FROM pairing_tokens WHERE token_hash = ?")
      .get(hash) as { token_hash: string; expires_at: string; used_at: string | null } | undefined;
    if (!row || row.used_at) {
      db.exec("ROLLBACK");
      return false;
    }
    if (Date.parse(row.expires_at) <= now.getTime()) {
      db.exec("ROLLBACK");
      return false;
    }
    db.prepare("UPDATE pairing_tokens SET used_at = ? WHERE token_hash = ?").run(
      now.toISOString(),
      hash
    );
    db.exec("COMMIT");
    return true;
  } catch (error) {
    db.exec("ROLLBACK");
    throw error;
  }
}

export interface RegisteredDevice {
  device: DeviceRow;
  secret: string;
}

export function registerDevice(
  db: Db,
  input: {
    device: DeviceInfo;
    edgeVersion: string;
    protocolVersion: string;
    now?: Date;
  }
): RegisteredDevice {
  const now = input.now ?? new Date();
  const id = generateDeviceId();
  const secret = generateDeviceSecret();
  db.prepare(
    `INSERT INTO devices
      (id, name, hostname, os, arch, role, boot_id, capabilities, edge_version, protocol_version, secret_hash, created_at, last_seen_at, last_metrics_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)`
  ).run(
    id,
    input.device.name,
    input.device.hostname,
    input.device.os,
    input.device.arch,
    input.device.role,
    input.device.boot_id,
    JSON.stringify(input.device.capabilities),
    input.edgeVersion,
    input.protocolVersion,
    sha256Hex(secret),
    now.toISOString()
  );
  const device = getDevice(db, id);
  if (!device) throw new Error("device_insert_failed");
  logEvent(db, "device.registered", id, JSON.stringify({ name: device.name, hostname: device.hostname }));
  return { device, secret };
}

export function getDevice(db: Db, id: string): DeviceRow | undefined {
  return db.prepare("SELECT * FROM devices WHERE id = ?").get(id) as DeviceRow | undefined;
}

export function listDevices(db: Db): DeviceRow[] {
  return db.prepare("SELECT * FROM devices ORDER BY created_at ASC").all() as unknown as DeviceRow[];
}

export function touchDevice(
  db: Db,
  id: string,
  patch: Partial<Pick<DeviceRow, "last_seen_at" | "last_metrics_at" | "boot_id" | "edge_version" | "capabilities">>
): void {
  const fields: string[] = [];
  const values: (string | null)[] = [];
  for (const [key, value] of Object.entries(patch)) {
    fields.push(key + " = ?");
    values.push(value ?? null);
  }
  if (fields.length === 0) return;
  db.prepare(`UPDATE devices SET ${fields.join(", ")} WHERE id = ?`).run(...values, id);
}

export function revokeDevice(db: Db, id: string): void {
  db.prepare("UPDATE devices SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL").run(
    new Date().toISOString(),
    id
  );
  logEvent(db, "device.revoked", id);
}

export function insertMetric(
  db: Db,
  deviceId: string,
  sampledAt: string,
  metrics: MetricsPayload,
  receivedAt = new Date()
): number {
  const result = db
    .prepare(
      `INSERT INTO metrics
        (device_id, sampled_at, received_at, cpu_pct, mem_used_bytes, mem_total_bytes, disk_used_bytes, disk_total_bytes, gpu_util_pct, gpu_mem_used_bytes, gpu_mem_total_bytes, gpu_temp_c)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
    )
    .run(
      deviceId,
      sampledAt,
      receivedAt.toISOString(),
      metrics.cpu_pct,
      metrics.mem_used_bytes,
      metrics.mem_total_bytes,
      metrics.disk_used_bytes,
      metrics.disk_total_bytes,
      metrics.gpu_util_pct ?? null,
      metrics.gpu_mem_used_bytes ?? null,
      metrics.gpu_mem_total_bytes ?? null,
      metrics.gpu_temp_c ?? null
    );
  return Number(result.lastInsertRowid);
}

export function latestMetric(db: Db, deviceId: string): MetricRow | undefined {
  return db
    .prepare("SELECT * FROM metrics WHERE device_id = ? ORDER BY id DESC LIMIT 1")
    .get(deviceId) as MetricRow | undefined;
}

export function listMetrics(db: Db, deviceId: string, limit: number): MetricRow[] {
  const rows = db
    .prepare("SELECT * FROM metrics WHERE device_id = ? ORDER BY id DESC LIMIT ?")
    .all(deviceId, limit) as unknown as MetricRow[];
  return rows.reverse();
}

export function listEvents(db: Db, limit: number): Array<{ id: number; at: string; device_id: string | null; type: string; detail: string | null }> {
  return db.prepare("SELECT id, at, device_id, type, detail FROM events ORDER BY id DESC LIMIT ?").all(limit) as unknown as Array<{
    id: number;
    at: string;
    device_id: string | null;
    type: string;
    detail: string | null;
  }>;
}

export function createSession(db: Db, ttlSeconds: number, now = new Date()): { id: string; expiresAt: string } {
  const id = generateSessionId();
  const expiresAt = new Date(now.getTime() + ttlSeconds * 1000).toISOString();
  db.prepare(
    "INSERT INTO web_sessions (id_hash, created_at, expires_at, last_seen_at) VALUES (?, ?, ?, ?)"
  ).run(sha256Hex(id), now.toISOString(), expiresAt, now.toISOString());
  return { id, expiresAt };
}

export function getSession(
  db: Db,
  id: string,
  now = new Date()
): { expires_at: string; last_seen_at: string } | null {
  const hash = sha256Hex(id);
  const row = db
    .prepare("SELECT expires_at, last_seen_at FROM web_sessions WHERE id_hash = ?")
    .get(hash) as { expires_at: string; last_seen_at: string } | undefined;
  if (!row) return null;
  if (Date.parse(row.expires_at) <= now.getTime()) {
    db.prepare("DELETE FROM web_sessions WHERE id_hash = ?").run(hash);
    return null;
  }
  db.prepare("UPDATE web_sessions SET last_seen_at = ? WHERE id_hash = ?").run(now.toISOString(), hash);
  return row;
}

export function deleteSession(db: Db, id: string): void {
  db.prepare("DELETE FROM web_sessions WHERE id_hash = ?").run(sha256Hex(id));
}

export function cleanupExpiredSessions(db: Db, now = new Date()): void {
  db.prepare("DELETE FROM web_sessions WHERE expires_at <= ?").run(now.toISOString());
}

export function cleanupOldMetrics(db: Db, retentionHours: number, now = new Date()): number {
  const cutoff = new Date(now.getTime() - retentionHours * 3600 * 1000).toISOString();
  const result = db.prepare("DELETE FROM metrics WHERE sampled_at < ?").run(cutoff);
  return Number(result.changes);
}

export function metricIdSeed(): string {
  return randomUUID();
}
