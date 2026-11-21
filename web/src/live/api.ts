import type {
  Evidence,
  MetricPoint,
  SessionMode,
  SessionStatus,
  TaskSource,
  TaskStatus,
} from "@/data/types";

export type LiveDeviceStatus = "online" | "stale" | "offline";

export interface LiveMetrics {
  sampled_at: string;
  received_at: string | null;
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

export interface DshProfile {
  name: string;
  available: boolean;
  running: boolean;
}

export type DshStatus = "stopped" | "starting" | "running" | "error" | string;

export interface DshState {
  profile: string;
  mode: string;
  status: DshStatus;
  owner?: string;
  error?: string;
  installed: boolean;
  profiles: DshProfile[];
  has_url: boolean;
}

export interface LiveDevice {
  id: string;
  name: string;
  hostname: string;
  os: string;
  arch: string;
  role: string;
  status: LiveDeviceStatus;
  status_age_seconds: number | null;
  last_seen_at: string | null;
  last_metrics_at: string | null;
  edge_version: string;
  protocol_version: string;
  boot_id: string;
  capabilities: string[];
  created_at: string;
  revoked_at: string | null;
  metrics: LiveMetrics | null;
  dsh?: DshState | null;
  /** 旧 Hub 字段：该设备已有可代理的 DSH URL。 */
  harness_available?: boolean;
}

export interface MobileCredential {
  id: string;
  label: string;
  platform: string;
  model: string;
  app_version: string;
  scopes: string[];
  created_at: string;
  last_used_at: string | null;
  expires_at: string;
  // 撤销是硬删除：撤销后这行直接从库里消失，所以没有 revoked_at，也没有
  // "revoked" 状态——只可能是 active / expiring / expired。
  status: "active" | "expiring" | "expired" | string;
  days_to_expiry?: number | null;
}

export interface MobilePairingCode {
  code: string;
  expires_at: string;
  label: string | null;
  scopes: string[];
  ttl_seconds: number;
}

export interface LiveEvent {
  id: number;
  at: string;
  device_id: string | null;
  type: string;
  detail: string | null;
}

/** P3：任务与会话（字段命名与 shared/protocol/PROTOCOL-v1-p3.md 保持一致，均为 snake_case）。 */

export type LiveTurnStatus = "queued" | "running" | "succeeded" | "failed" | "cancelled";

export interface LiveTask {
  id: string;
  title: string;
  source: TaskSource;
  device_id: string;
  session_id: string | null;
  status: TaskStatus;
  stage: string;
  cwd: string;
  request: string;
  created_at: string;
  updated_at: string;
  evidence_kind: Evidence | null;
  evidence_note: string | null;
  session_mode?: SessionMode;
}

export interface LiveTurn {
  id: string;
  seq: number;
  request: string;
  status: LiveTurnStatus | string;
  started_at: string | null;
  ended_at: string | null;
  exit_code: number | null;
  result: string | null;
  error: string | null;
}

export interface LiveCommand {
  id: string;
  kind: "start_turn" | "send_turn" | "cancel" | (string & {});
  status:
    | "pending"
    | "delivered"
    | "accepted"
    | "done"
    | "expired"
    | "cancelled"
    | "rejected"
    | (string & {});
  created_at: string | null;
  delivered_at: string | null;
  acked_at: string | null;
  expires_at?: string | null;
  detail?: string | null;
  error?: string | null;
}

export interface LiveTaskEvent {
  event_id: string;
  type: string;
  at: string;
  turn_id: string | null;
  payload: Record<string, unknown> | null;
}

export interface LiveSession {
  id: string;
  task_id?: string | null;
  device_id?: string | null;
  mode: SessionMode;
  status: SessionStatus;
  cwd: string;
  harness_session_id: string | null;
  created_at: string;
  last_activity_at: string | null;
}

export interface LiveSessionSummary extends LiveSession {
  turn_count: number;
}

export interface LiveArtifact {
  id: string;
  task_id: string;
  device_id: string;
  turn_id: string | null;
  name: string;
  path: string;
  mime: string;
  size: number;
  sha256: string;
  created_at: string;
  url: string;
}

export interface LiveAttachment {
  id: string;
  task_id: string | null;
  turn_id: string | null;
  name: string;
  mime: string;
  size: number;
  sha256: string;
  created_at: string;
}

export interface LiveTaskDetailResponse {
  task: LiveTask;
  session: LiveSession | null;
  turns: LiveTurn[];
  commands: LiveCommand[];
  events: LiveTaskEvent[];
  artifacts: LiveArtifact[];
  attachments: LiveAttachment[];
}

export interface LiveSessionDetailResponse {
  session: LiveSession;
  turns: LiveTurn[];
  commands?: LiveCommand[];
  events: LiveTaskEvent[];
}

export interface CreateTaskInput {
  request: string;
  device_id: string;
  /** 中心必填：Edge 会校验目录存在且位于允许根目录下。 */
  cwd: string;
  title?: string;
  run_profile?: string;
  attachment_ids?: string[];
}

export interface CreateTaskResponse {
  task: LiveTask;
  session_id: string;
  command_id: string;
}

export interface AddTurnResponse {
  turn?: LiveTurn;
  command?: LiveCommand;
  task?: LiveTask;
}

export interface CancelTaskResponse {
  task?: LiveTask;
  command?: LiveCommand;
  /** 指令尚未投递时中心会直接撤销并标记任务 cancelled。 */
  cancelled_immediately?: boolean;
}

/** 桌面通知策略：Hub 侧保存，桌面端投递前复用同一份配置。 */
export interface TurnNotificationSettings {
  enabled: boolean;
  quiet_hours_enabled: boolean;
  quiet_start: string;
  quiet_end: string;
}

export type NotificationPermissionStatus = "granted" | "denied" | "unavailable";


export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

interface SessionInfo {
  authenticated: boolean;
  via: "session" | "bearer";
  expires_at?: string;
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    credentials: "same-origin",
    ...init,
    headers: { ...(init.headers ?? {}) },
  });
  if (!response.ok) {
    let code = "http_" + response.status;
    let message = response.statusText || "请求失败";
    try {
      const body = (await response.json()) as { error?: { code?: string; message?: string } };
      if (body.error?.code) code = body.error.code;
      if (body.error?.message) message = body.error.message;
    } catch {
      /* not json */
    }
    throw new ApiError(response.status, code, message);
  }
  if (response.status === 204) return undefined as T;
  return (await response.json()) as T;
}

function post<T>(path: string, body?: unknown): Promise<T> {
  return request<T>(path, {
    method: "POST",
    headers: { "content-type": "application/json", "x-meerkat-request": "1" },
    body: body === undefined ? "{}" : JSON.stringify(body),
  });
}

function queryString(params: object): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (typeof value === "string" && value.length > 0) search.set(key, value);
    if (typeof value === "number") search.set(key, String(value));
  }
  const query = search.toString();
  return query.length > 0 ? "?" + query : "";
}

export type PoolStatus = "unassigned" | "processing" | "blocked" | "review" | "completed" | "cancelled";

export interface PoolTarget {
  kind: "bot" | "group";
  id: string;
  name: string;
}

interface PoolProgress {
  note?: string;
  evidence?: string[];
}

export interface PoolItem {
  id: string;
  title: string;
  request: string;
  status: PoolStatus;
  assignee_kind?: PoolTarget["kind"];
  assignee_id?: string;
  revision: number;
  attachment_ids: string[];
  updated_at: string;
  latest_progress?: PoolProgress;
  delivery?: {
    status: "pending" | "sending" | "queued" | "uncertain" | "failed" | "acknowledged" | "superseded";
    error_code?: string;
    can_retry: boolean;
  };
}

interface PoolEvent {
  event_id: string;
  actor_id: string;
  assignment_rev: number;
  type: string;
  payload: PoolProgress;
  at: string;
}

const poolPath = "/api/task-pool";

export const taskPoolApi = {
  list: () => request<{ items: PoolItem[] }>(poolPath),
  detail: (id: string) => request<{ item: PoolItem; events: PoolEvent[] }>(`${poolPath}/${encodeURIComponent(id)}`),
  targets: () => request<{ targets: PoolTarget[] }>(`${poolPath}/targets`),
  create: (text: string, attachments: string[], key: string, target?: PoolTarget) => request<{ item: PoolItem }>(poolPath, {
    method: "POST",
    headers: { "content-type": "application/json", "x-meerkat-request": "1", "idempotency-key": key },
    body: JSON.stringify({ request: text, attachment_ids: attachments, assignee_kind: target?.kind, assignee_id: target?.id }),
  }),
  assign: (item: PoolItem, target: PoolTarget) => post<{ item: PoolItem }>(`${poolPath}/${encodeURIComponent(item.id)}/assign`, {
    expected_revision: item.revision, assignee_kind: target.kind, assignee_id: target.id,
  }),
  retry: (item: PoolItem) => post<{ item: PoolItem }>(`${poolPath}/${encodeURIComponent(item.id)}/retry`, { expected_revision: item.revision }),
};

export const liveApi = {
  session: () => request<SessionInfo>("/api/session"),
  login: (token: string) =>
    request<SessionInfo>("/api/session", {
      method: "POST",
      headers: { "content-type": "application/json", "x-meerkat-request": "1" },
      body: JSON.stringify({ token }),
    }),
  logout: () => request<{ authenticated: boolean }>("/api/session", { method: "DELETE", headers: { "x-meerkat-request": "1" } }),
  devices: () => request<{ server_time: string; devices: LiveDevice[] }>("/api/devices"),
  device: (id: string) => request<{ server_time: string; device: LiveDevice }>("/api/devices/" + id),
  metrics: (id: string, limit = 120) =>
    request<{ device_id: string; samples: LiveMetrics[] }>("/api/devices/" + id + "/metrics?limit=" + limit),
  events: (limit = 50) => request<{ events: LiveEvent[] }>("/api/events?limit=" + limit),
  createPairingToken: (label = "web-panel") =>
    request<{ token: string; expires_at: string; label: string | null }>("/api/pairing-tokens", {
      method: "POST",
      headers: { "content-type": "application/json", "x-meerkat-request": "1" },
      body: JSON.stringify({ label }),
    }),
  revokeDevice: (id: string) =>
    request<{ device_id: string; revoked: boolean }>("/api/devices/" + id + "/revoke", {
      method: "POST",
      headers: { "x-meerkat-request": "1" },
    }),

  /** 移动设备配对码与凭证管理（需要管理员 session）。 */
  createMobilePairingToken: (label = "mobile-app") =>
    post<MobilePairingCode>("/api/mobile/pairing-tokens", { label }),
  mobileCredentials: () =>
    request<{ credentials: MobileCredential[]; server_time: string }>("/api/mobile/credentials"),
  revokeMobileCredential: (id: string) =>
    post<{ ok: boolean; credential_id: string; revoked: boolean }>(
      "/api/mobile/credentials/" + encodeURIComponent(id) + "/revoke",
    ),

  /** P3：派发任务。成功返回 task / session_id / command_id 回执。 */
  createTask: (input: CreateTaskInput) => post<CreateTaskResponse>("/api/tasks", input),
  uploadAttachment: async (file: File): Promise<LiveAttachment> => {
    const body = new FormData();
    body.append("file", file);
    const response = await fetch("/api/attachments", { method: "POST", headers: { "x-meerkat-request": "1" }, body });
    if (!response.ok) throw new Error("上传附件失败");
    return (await response.json()) as LiveAttachment;
  },
  attachmentURL: (id: string) => "/api/attachments/" + encodeURIComponent(id),
  listTasks: (params: { limit?: number; status?: string; device_id?: string } = {}) =>
    request<{ tasks: LiveTask[] }>("/api/tasks" + queryString(params)),
  getTask: (id: string) => request<LiveTaskDetailResponse>("/api/tasks/" + encodeURIComponent(id)),
  addTurn: (id: string, requestText: string) =>
    post<AddTurnResponse>("/api/tasks/" + encodeURIComponent(id) + "/turns", { request: requestText }),
  cancelTask: (id: string) =>
    post<CancelTaskResponse>("/api/tasks/" + encodeURIComponent(id) + "/cancel"),
  listSessions: (params: { limit?: number; mode?: string } = {}) =>
    request<{ sessions: LiveSessionSummary[] }>("/api/sessions" + queryString(params)),
  getSession: (id: string) =>
    request<LiveSessionDetailResponse>("/api/sessions/" + encodeURIComponent(id)),

  /** 每台设备的 DSH profile 状态与动作。 */
  dshStatus: (id: string) =>
    request<{ device_id: string; online: boolean; dsh: DshState }>(
      "/api/devices/" + encodeURIComponent(id) + "/dsh",
    ),
  setDshProfile: (id: string, profile: string, mode?: string) =>
    post<{ ok: boolean; profile: string; mode: string }>(
      "/api/devices/" + encodeURIComponent(id) + "/dsh/profile",
      { profile, mode },
    ),
  dshAction: (id: string, action: "connect" | "launch" | "stop", profile?: string) =>
    post<{ ok: boolean; action: string; profile: string; mode: string }>(
      "/api/devices/" + encodeURIComponent(id) + "/dsh/action",
      { action, profile },
    ),
  installDsh: (id: string) =>
    post<{ command_id?: string; already?: boolean }>(
      "/api/devices/" + encodeURIComponent(id) + "/dsh/install",
    ),
  createDshProfile: (id: string, name: string) =>
    post<{ ok: boolean; name: string }>(
      "/api/devices/" + encodeURIComponent(id) + "/dsh/profiles",
      { name },
    ),

  /** 桌面通知：只有策略读写。回合结束的通知不落库，没有历史列表。 */
  notificationSettings: () => request<TurnNotificationSettings>("/api/notifications/settings"),
  updateNotificationSettings: (settings: TurnNotificationSettings) =>
    post<TurnNotificationSettings>("/api/notifications/settings", settings),
};

/** 桌面端通知权限（仅 Meerkat App 内可用；纯浏览器预览时返回 unavailable）。 */
export async function desktopNotificationPermission(): Promise<{
  status: NotificationPermissionStatus;
  supported: boolean;
}> {
  try {
    const response = await fetch("/desktop/notifications/permission");
    if (!response.ok) return { status: "unavailable", supported: false };
    return (await response.json()) as { status: NotificationPermissionStatus; supported: boolean };
  } catch {
    return { status: "unavailable", supported: false };
  }
}

export async function requestDesktopNotificationPermission(): Promise<NotificationPermissionStatus> {
  try {
    const response = await fetch("/desktop/notifications/permission", { method: "POST" });
    if (!response.ok) return "unavailable";
    const body = (await response.json()) as { status: NotificationPermissionStatus };
    return body.status;
  } catch {
    return "unavailable";
  }
}


export function artifactContentURL(id: string): string {
  return "/api/artifacts/" + encodeURIComponent(id);
}

export function pct(part: number | null, total: number | null): number | null {
  if (part === null || total === null || total <= 0) return null;
  return Math.min(100, Math.max(0, (part / total) * 100));
}

export function formatBytes(bytes: number | null): string {
  if (bytes === null) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return (value >= 100 || unit === 0 ? value.toFixed(0) : value.toFixed(1)) + " " + units[unit];
}

export function formatClock(iso: string | null): string {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleTimeString("zh-CN", { hour12: false });
}

export function formatRelative(iso: string | null): string {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  const seconds = Math.max(0, Math.round((Date.now() - date.getTime()) / 1000));
  if (seconds < 5) return "刚刚";
  if (seconds < 60) return seconds + " 秒前";
  if (seconds < 3600) return Math.floor(seconds / 60) + " 分钟前";
  if (seconds < 86400) return Math.floor(seconds / 3600) + " 小时前";
  return Math.floor(seconds / 86400) + " 天前";
}

export function toChartPoints(samples: LiveMetrics[]): MetricPoint[] {
  return samples.map((sample) => ({
    t: formatClock(sample.sampled_at).slice(0, 5),
    cpu: sample.cpu_pct ?? 0,
    memory: pct(sample.mem_used_bytes, sample.mem_total_bytes) ?? 0,
    gpuUtil: sample.gpu_util_pct ?? null,
  }));
}

export function hasGpu(device: LiveDevice): boolean {
  return device.metrics?.gpu_util_pct != null || device.metrics?.gpu_mem_used_bytes != null;
}

export function formatDateTime(iso: string | null): string {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleString("zh-CN", { hour12: false });
}

/** 任务是否已到终态（成功/失败/取消）；取消请求中仍不是终态。 */
export function isTerminalStatus(status: TaskStatus): boolean {
  return status === "succeeded" || status === "failed" || status === "cancelled";
}

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message;
  if (error instanceof Error) return error.message;
  return "未知错误";
}