export type DeviceStatus = "online" | "stale" | "offline";

export type TaskStatus =
  | "queued"
  | "running"
  | "waiting_input"
  | "cancel_requested"
  | "succeeded"
  | "failed"
  | "cancelled"
  | "needs_review";

export type TaskSource = "managed" | "external";

export type SessionMode = "managed" | "observed";

export type SessionStatus =
  | "idle"
  | "running"
  | "waiting_input"
  | "waiting_permission"
  | "ended"
  | "error";

export type Evidence = "job_result" | "explicit_agent_report" | "user_confirmation";

export interface MetricSnapshot {
  cpu: number | null;
  memory: number | null;
  gpuUtil: number | null;
  gpuMem: number | null;
  disk: number | null;
  memoryTotalGb: number;
  gpuMemTotalGb?: number;
  gpuTempC?: number | null;
}

export interface MetricPoint {
  t: string;
  cpu: number;
  memory: number;
  gpuUtil: number | null;
}

export interface Device {
  id: string;
  name: string;
  hostname: string;
  os: string;
  arch: string;
  role: string;
  status: DeviceStatus;
  lastSeenLabel: string;
  edgeVersion: string;
  protocol: string;
  bootId: string;
  capabilities: string[];
  metrics: MetricSnapshot;
  history: MetricPoint[];
  runningTasks: number;
  sessions: number;
  note?: string;
}

export interface TaskJob {
  kind: string;
  progress: number | null;
  phase: string;
  externalId?: string;
}

export interface Task {
  id: string;
  title: string;
  source: TaskSource;
  deviceId: string;
  sessionId: string;
  status: TaskStatus;
  stage: string;
  updatedAt: string;
  createdAt: string;
  cwd: string;
  evidence?: Evidence;
  evidenceNote?: string;
  job?: TaskJob;
  request: string;
  turn: string;
}

export interface Session {
  id: string;
  title: string;
  mode: SessionMode;
  deviceId: string;
  cwd: string;
  status: SessionStatus;
  harness: string;
  harnessSessionId: string;
  lastActivityAt: string;
  turns: number;
  capabilities: {
    sendTurn: boolean;
    cancel: boolean;
    permissionResponse: boolean;
  };
}

export interface TaskEvent {
  id: string;
  at: string;
  type:
    | "command.delivered"
    | "command.ack"
    | "session.started"
    | "turn.started"
    | "tool.pre"
    | "tool.post"
    | "job.started"
    | "job.progress"
    | "turn.stop"
    | "session.ended";
  title: string;
  detail?: string;
  tone?: "neutral" | "success" | "warning" | "danger";
}

export interface LogLine {
  at: string;
  kind: "system" | "user" | "tool" | "stderr" | "result";
  text: string;
}

export interface Artifact {
  id: string;
  name: string;
  kind: "text" | "srt" | "image" | "log" | "video";
  size: string;
  available: boolean;
  note?: string;
}

export interface FeedItem {
  id: string;
  at: string;
  deviceId: string;
  type: string;
  text: string;
  tone?: "neutral" | "success" | "warning" | "danger";
}
