import type {
  DeviceStatus,
  Evidence,
  SessionMode,
  SessionStatus,
  TaskSource,
  TaskStatus,
} from "@/data/types";

type Tone = "emerald" | "amber" | "rose" | "sky" | "violet" | "orange" | "neutral";

export const toneBadge: Record<Tone, string> = {
  emerald:
    "border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-500/25 dark:bg-emerald-500/10 dark:text-emerald-400",
  amber:
    "border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-500/25 dark:bg-amber-500/10 dark:text-amber-400",
  rose: "border-rose-200 bg-rose-50 text-rose-700 dark:border-rose-500/25 dark:bg-rose-500/10 dark:text-rose-400",
  sky: "border-sky-200 bg-sky-50 text-sky-700 dark:border-sky-500/25 dark:bg-sky-500/10 dark:text-sky-400",
  violet:
    "border-violet-200 bg-violet-50 text-violet-700 dark:border-violet-500/25 dark:bg-violet-500/10 dark:text-violet-400",
  orange:
    "border-orange-200 bg-orange-50 text-orange-700 dark:border-orange-500/25 dark:bg-orange-500/10 dark:text-orange-400",
  neutral: "border-border bg-muted text-muted-foreground",
};

export const toneDot: Record<Tone, string> = {
  emerald: "bg-emerald-500",
  amber: "bg-amber-500",
  rose: "bg-rose-500",
  sky: "bg-sky-500",
  violet: "bg-violet-500",
  orange: "bg-orange-500",
  neutral: "bg-muted-foreground/50",
};

export const taskStatusLabel: Record<TaskStatus, string> = {
  queued: "排队",
  running: "运行中",
  waiting_input: "等待输入",
  cancel_requested: "取消中",
  succeeded: "成功",
  failed: "失败",
  cancelled: "已取消",
  needs_review: "结果待确认",
};

export const taskStatusTone: Record<TaskStatus, Tone> = {
  queued: "neutral",
  running: "sky",
  waiting_input: "violet",
  cancel_requested: "orange",
  succeeded: "emerald",
  failed: "rose",
  cancelled: "neutral",
  needs_review: "amber",
};

export const deviceStatusLabel: Record<DeviceStatus, string> = {
  online: "在线",
  stale: "状态陈旧",
  offline: "离线",
};

export const deviceStatusTone: Record<DeviceStatus, Tone> = {
  online: "emerald",
  stale: "amber",
  offline: "rose",
};

export const sessionStatusLabel: Record<SessionStatus, string> = {
  idle: "空闲",
  running: "运行中",
  waiting_input: "等待输入",
  waiting_permission: "等待权限",
  ended: "已结束",
  error: "异常",
};

export const sessionStatusTone: Record<SessionStatus, Tone> = {
  idle: "neutral",
  running: "sky",
  waiting_input: "violet",
  waiting_permission: "amber",
  ended: "neutral",
  error: "rose",
};

export const evidenceLabel: Record<Evidence, string> = {
  job_result: "作业结果",
  explicit_agent_report: "Agent 显式报告",
  user_confirmation: "用户确认",
};

export const sourceLabel: Record<TaskSource, string> = {
  managed: "中心派发",
  external: "外部终端",
};

export const modeLabel: Record<SessionMode, string> = {
  managed: "托管",
  observed: "观察",
};

export const capabilityLabel: Record<string, string> = {
  "harness.claude-code": "Claude Code Harness",
  hooks: "Hooks 上报",
  jobs: "后台作业",
  artifacts: "产物索引",
  metrics: "主机指标",
  "gpu.nvidia": "NVIDIA 指标",
  launchd: "launchd 常驻",
  wsl2: "WSL2 适配",
};

export function formatPercent(value: number | null): string {
  return value === null ? "—" : String(Math.round(value));
}
