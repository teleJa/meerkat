import { z } from "zod";

export const PROTOCOL_VERSION = "1";

export const deviceInfoSchema = z.object({
  name: z.string().min(1).max(120),
  hostname: z.string().min(1).max(120),
  os: z.string().min(1).max(120),
  arch: z.string().min(1).max(40),
  role: z.string().min(1).max(80),
  capabilities: z.array(z.string().min(1).max(64)).max(32).default([]),
  boot_id: z.string().min(1).max(120),
});

export const registerRequestSchema = z.object({
  pairing_token: z.string().min(8).max(256),
  protocol_version: z.string().min(1).max(16),
  edge_version: z.string().min(1).max(64),
  device: deviceInfoSchema,
});

const nullablePercent = z.number().min(0).max(100).nullable();
const nullableBytes = z.number().int().nonnegative().nullable();

export const metricsPayloadSchema = z.object({
  cpu_pct: nullablePercent,
  mem_used_bytes: nullableBytes,
  mem_total_bytes: nullableBytes,
  disk_used_bytes: nullableBytes,
  disk_total_bytes: nullableBytes,
  gpu_util_pct: nullablePercent.optional(),
  gpu_mem_used_bytes: nullableBytes.optional(),
  gpu_mem_total_bytes: nullableBytes.optional(),
  gpu_temp_c: z.number().nullable().optional(),
});

export const helloMessageSchema = z.object({
  type: z.literal("hello"),
  protocol_version: z.string().min(1).max(16),
  device_id: z.string().min(1).max(64),
  device_secret: z.string().min(16).max(256),
  edge_version: z.string().min(1).max(64).optional(),
  boot_id: z.string().min(1).max(120).optional(),
  capabilities: z.array(z.string().min(1).max(64)).max(32).optional(),
});

export const heartbeatMessageSchema = z.object({
  type: z.literal("heartbeat"),
  seq: z.number().int().nonnegative(),
  sent_at: z.string().optional(),
});

export const metricsBatchMessageSchema = z.object({
  type: z.literal("metrics.batch"),
  stream_id: z.string().min(1).max(64),
  seq: z.number().int().nonnegative(),
  sampled_at: z.string().min(1),
  metrics: metricsPayloadSchema,
});

export const commandAckMessageSchema = z.object({
  type: z.literal("command.ack"),
  command_id: z.string().min(1).max(64),
  status: z.enum(["accepted", "rejected"]),
  detail: z.string().max(2000).optional().nullable(),
  at: z.string().optional(),
});

export const sessionStartedMessageSchema = z.object({
  type: z.literal("session.started"),
  command_id: z.string().min(1).max(64).optional(),
  task_id: z.string().min(1).max(64),
  session_id: z.string().min(1).max(64),
  harness_session_id: z.string().max(200).optional(),
  harness_version: z.string().max(80).optional(),
  cwd: z.string().max(1024).optional(),
  at: z.string().optional(),
});

export const turnStartedMessageSchema = z.object({
  type: z.literal("turn.started"),
  command_id: z.string().max(64).optional(),
  task_id: z.string().min(1).max(64),
  session_id: z.string().min(1).max(64),
  turn_id: z.string().min(1).max(64),
  at: z.string().optional(),
});

export const eventBatchMessageSchema = z.object({
  type: z.literal("event.batch"),
  stream_id: z.string().min(1).max(64),
  seq: z.number().int().nonnegative(),
  events: z
    .array(
      z.object({
        event_id: z.string().min(1).max(128),
        task_id: z.string().max(64).optional().nullable(),
        session_id: z.string().max(64).optional().nullable(),
        turn_id: z.string().max(64).optional().nullable(),
        type: z.string().min(1).max(80),
        at: z.string().min(1),
        payload: z.unknown().optional(),
      })
    )
    .max(200),
});

export const turnCompletedMessageSchema = z.object({
  type: z.literal("turn.completed"),
  task_id: z.string().min(1).max(64),
  session_id: z.string().min(1).max(64),
  turn_id: z.string().min(1).max(64),
  status: z.enum(["succeeded", "failed", "cancelled"]),
  exit_code: z.number().int().nullable().optional(),
  duration_ms: z.number().int().nonnegative().nullable().optional(),
  result: z.string().max(8000).nullable().optional(),
  error: z.string().max(4000).nullable().optional(),
  at: z.string().optional(),
});

export const wsMessageSchema = z.discriminatedUnion("type", [
  helloMessageSchema,
  heartbeatMessageSchema,
  metricsBatchMessageSchema,
  commandAckMessageSchema,
  sessionStartedMessageSchema,
  turnStartedMessageSchema,
  eventBatchMessageSchema,
  turnCompletedMessageSchema,
]);

export type DeviceInfo = z.infer<typeof deviceInfoSchema>;
export type RegisterRequest = z.infer<typeof registerRequestSchema>;
export type MetricsPayload = z.infer<typeof metricsPayloadSchema>;
export type WsMessage = z.infer<typeof wsMessageSchema>;
