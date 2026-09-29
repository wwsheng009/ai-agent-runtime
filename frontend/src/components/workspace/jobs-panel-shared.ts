// P2-1A / P3：后台任务面板的纯函数层（可单测，不触碰 React / fetch）。
//
// 语义约定：
// - live = pending | running | paused（非终态，面板显示 elapsed 走秒与可用动作）；
// - settled = completed | failed | timed_out | cancelled | orphaned | interrupted
//   | expired | abandoned（终态，显示时长与退出码，可重排队）。

import type { RuntimeJob, RuntimeJobStatus } from "@/types/runtime";

export const liveJobStatuses: RuntimeJobStatus[] = ["pending", "running", "paused"];

export const settledJobStatuses: RuntimeJobStatus[] = [
  "completed",
  "failed",
  "timed_out",
  "cancelled",
  "orphaned",
  "interrupted",
  "expired",
  "abandoned",
];

/** 触发 jobs 面板刷新的运行时事件（后端 job_* 事件流）。 */
export const jobsRelevantRuntimeEvents = [
  "job_started",
  "job_output",
  "job_finished",
  "job_cancelled",
  "job_timed_out",
  "job_orphaned",
  "job_paused",
  "job_resumed",
  "job_requeued",
  "job_abandoned",
  "background_task",
];

export function isLiveJobStatus(status: RuntimeJobStatus): boolean {
  return liveJobStatuses.includes(status);
}

export function isTerminalJobStatus(status: RuntimeJobStatus): boolean {
  return settledJobStatuses.includes(status);
}

/** 任务行动作（与后端状态机端点一一对应）。 */
export type JobAction = "pause" | "resume" | "cancel" | "abandon" | "requeue";

/** 在途动作（面板据此禁用按钮并显示进行中文案）。 */
export type JobActionPending = { jobId: string; action: JobAction } | null;

/**
 * 动作矩阵（纯函数便于单测）：
 * - pending → pause / cancel / abandon；
 * - paused  → resume / cancel / abandon；
 * - running → cancel；
 * - 终态    → requeue。
 */
export function jobAvailableActions(status: RuntimeJobStatus): JobAction[] {
  switch (status) {
    case "pending":
      return ["pause", "cancel", "abandon"];
    case "paused":
      return ["resume", "cancel", "abandon"];
    case "running":
      return ["cancel"];
    default:
      return isTerminalJobStatus(status) ? ["requeue"] : [];
  }
}

export function splitRuntimeJobs(jobs: RuntimeJob[]): {
  live: RuntimeJob[];
  settled: RuntimeJob[];
} {
  const live: RuntimeJob[] = [];
  const settled: RuntimeJob[] = [];
  for (const job of jobs) {
    if (isLiveJobStatus(job.status)) {
      live.push(job);
    } else {
      settled.push(job);
    }
  }
  return { live: sortJobsByRecency(live), settled: sortJobsByRecency(settled) };
}

/** 最近活动优先：live 以 createdAt 升序（最早排队在前），settled 以结束时间倒序。 */
export function sortJobsByRecency(jobs: RuntimeJob[]): RuntimeJob[] {
  return [...jobs].sort((left, right) => {
    const leftLive = isLiveJobStatus(left.status);
    const rightLive = isLiveJobStatus(right.status);
    if (leftLive !== rightLive) {
      return leftLive ? -1 : 1;
    }
    const leftTime = resolveJobSortTime(left);
    const rightTime = resolveJobSortTime(right);
    if (leftTime === rightTime) {
      return left.id.localeCompare(right.id);
    }
    return leftLive ? leftTime - rightTime : rightTime - leftTime;
  });
}

function resolveJobSortTime(job: RuntimeJob): number {
  const primary = isLiveJobStatus(job.status)
    ? job.createdAt
    : job.finishedAt || job.createdAt;
  const parsed = Date.parse(primary);
  return Number.isFinite(parsed) ? parsed : 0;
}

/**
 * 任务「进行/总」时长：
 * - live：startedAt（未启动则 createdAt）→ now；
 * - settled：startedAt（未启动则 createdAt）→ finishedAt。
 * 时间戳缺失或非法时返回 null，由 UI 显示占位符。
 */
export function resolveJobElapsedMs(job: RuntimeJob, nowMs: number): number | null {
  const start = parseTimestamp(job.startedAt) ?? parseTimestamp(job.createdAt);
  if (start === null) {
    return null;
  }
  const end = isLiveJobStatus(job.status)
    ? nowMs
    : (parseTimestamp(job.finishedAt) ?? nowMs);
  return Math.max(0, end - start);
}

export function parseTimestamp(value: string): number | null {
  if (!value) {
    return null;
  }
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : null;
}

/** 时长格式化：`9s` / `1m 05s` / `1h 02m`。 */
export function formatJobDuration(ms: number | null): string {
  if (ms === null) {
    return "--";
  }
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  if (totalSeconds < 60) {
    return `${totalSeconds}s`;
  }
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes < 60) {
    return `${minutes}m ${String(seconds).padStart(2, "0")}s`;
  }
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${String(minutes % 60).padStart(2, "0")}m`;
}

export function formatJobTimestamp(value: string): string {
  const parsed = parseTimestamp(value);
  if (parsed === null) {
    return "--";
  }
  return new Date(parsed).toLocaleString();
}

export function isJobsRelevantRuntimeEvent(
  runtimeEventType: string | undefined,
): boolean {
  const normalized = runtimeEventType?.trim().toLowerCase() ?? "";
  return normalized !== "" && jobsRelevantRuntimeEvents.includes(normalized);
}

/**
 * 事件驱动的刷新键：仅当事件类型与 jobs 相关时非空，避免任意事件都重拉任务列表。
 * count 参与键值，保证「同类型事件连续到达」也能触发一次刷新。
 */
export function buildJobsReloadKey(
  runtimeEventType: string | undefined,
  runtimeEventCount: number | undefined,
): string {
  if (!isJobsRelevantRuntimeEvent(runtimeEventType)) {
    return "";
  }
  return `${runtimeEventType}:${runtimeEventCount ?? 0}`;
}

/** i18n 键后缀：`panels.jobs.status.<status>`。 */
export function jobStatusLabelKey(status: RuntimeJobStatus): string {
  return `panels.jobs.status.${status}`;
}

/** i18n 键：动作标签 `panels.jobs.<action>`。 */
export function jobActionLabelKey(action: JobAction): `panels.jobs.${JobAction}` {
  return `panels.jobs.${action}`;
}

const jobActionPendingKeys = {
  abandon: "abandoning",
  cancel: "cancelling",
  pause: "pausing",
  requeue: "requeuing",
  resume: "resuming",
} as const;

/** i18n 键：动作在途标签 `panels.jobs.<action+ing>`（cancel → cancelling）。 */
export function jobActionPendingLabelKey(
  action: JobAction,
): `panels.jobs.${(typeof jobActionPendingKeys)[JobAction]}` {
  return `panels.jobs.${jobActionPendingKeys[action]}`;
}
