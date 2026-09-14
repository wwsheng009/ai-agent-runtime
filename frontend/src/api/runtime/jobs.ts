// P2-1A：后台任务（Jobs）REST 客户端。
//
// 端点（backend/internal/api/skills/handler.go:720-724）：
//   GET  /api/runtime/background/jobs?session_id=&status=&limit=&offset=
//   GET  /api/runtime/background/jobs/{id}
//   POST /api/runtime/background/jobs/{id}/cancel
//   GET  /api/runtime/background/jobs/{id}/events?after=&limit=
//   GET  /api/runtime/background/jobs/{id}/output?offset=&limit=
//
// 归一化说明：`background.Job` 没有 json tag，实际序列化键为 Go 字段名（PascalCase）；
// normalize* 同时接受 PascalCase / snake_case，避免后端补 tag 时前端二次改动。

import type {
  RuntimeJob,
  RuntimeJobEvent,
  RuntimeJobEventsQuery,
  RuntimeJobEventsResponse,
  RuntimeJobListQuery,
  RuntimeJobListResponse,
  RuntimeJobOutput,
  RuntimeJobOutputQuery,
  RuntimeJobStatus,
} from "@/types/runtime";

import {
  buildRuntimeUrlWithQuery,
  fetchRuntimeJson,
} from "./shared";

const JOBS_PATH = "/api/runtime/background/jobs";

type RawRecord = Record<string, unknown>;

function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

function pickString(record: RawRecord, ...keys: string[]): string {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "string") {
      return value;
    }
  }
  return "";
}

function pickNumber(record: RawRecord, ...keys: string[]): number | null {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "number" && Number.isFinite(value)) {
      return value;
    }
  }
  return null;
}

const jobStatuses: RuntimeJobStatus[] = [
  "pending",
  "running",
  "completed",
  "failed",
  "timed_out",
  "cancelled",
  "orphaned",
];

/** 未知状态回落 `pending`（后端状态机新增枚举时不至于让整行失真）。 */
export function normalizeRuntimeJobStatus(value: unknown): RuntimeJobStatus {
  const raw = typeof value === "string" ? value.trim().toLowerCase() : "";
  return (jobStatuses as string[]).includes(raw)
    ? (raw as RuntimeJobStatus)
    : "pending";
}

export function normalizeRuntimeJob(raw: unknown): RuntimeJob {
  const record = asRecord(raw) ?? {};
  return {
    id: pickString(record, "ID", "id", "job_id"),
    sessionId: pickString(record, "SessionID", "session_id"),
    kind: pickString(record, "Kind", "kind"),
    command: pickString(record, "Command", "command"),
    cwd: pickString(record, "Cwd", "cwd"),
    priority: pickNumber(record, "Priority", "priority") ?? 0,
    restartPolicy: pickString(record, "RestartPolicy", "restart_policy"),
    status: normalizeRuntimeJobStatus(record.Status ?? record.status),
    message: pickString(record, "Message", "message"),
    createdAt: pickString(record, "CreatedAt", "created_at"),
    startedAt: pickString(record, "StartedAt", "started_at"),
    finishedAt: pickString(record, "FinishedAt", "finished_at"),
    exitCode: pickNumber(record, "ExitCode", "exit_code"),
    logPath: pickString(record, "LogPath", "log_path"),
  };
}

export function normalizeRuntimeJobList(raw: unknown): RuntimeJob[] {
  if (!Array.isArray(raw)) {
    return [];
  }
  return raw.map((entry) => normalizeRuntimeJob(entry));
}

export function normalizeRuntimeJobEvent(raw: unknown): RuntimeJobEvent {
  const record = asRecord(raw) ?? {};
  const payload = asRecord(record.payload);
  return {
    seq: pickNumber(record, "seq", "Seq") ?? 0,
    jobId: pickString(record, "job_id", "JobID", "JobId"),
    type: pickString(record, "type", "Type"),
    payload,
    createdAt: pickString(record, "created_at", "CreatedAt"),
  };
}

export function normalizeRuntimeJobOutput(raw: unknown): RuntimeJobOutput {
  const record = asRecord(raw) ?? {};
  return {
    jobId: pickString(record, "job_id", "JobID"),
    status: pickString(record, "status", "Status"),
    output: pickString(record, "output", "Output"),
    nextOffset: pickNumber(record, "next_offset", "NextOffset") ?? 0,
    exitCode: pickNumber(record, "exit_code", "ExitCode"),
    message: pickString(record, "message", "Message"),
    errorCode: pickString(record, "error_code", "ErrorCode"),
  };
}

export async function listRuntimeJobs(
  query: RuntimeJobListQuery = {},
): Promise<RuntimeJobListResponse> {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrlWithQuery(JOBS_PATH, {
      session_id: query.sessionId,
      status: query.status,
      limit: query.limit,
      offset: query.offset,
    }),
    { headers: { Accept: "application/json" } },
  );
  const record = asRecord(payload) ?? {};
  return {
    jobs: normalizeRuntimeJobList(record.jobs),
    count: pickNumber(record, "count") ?? 0,
  };
}

export async function getRuntimeJob(jobId: string): Promise<RuntimeJob> {
  const payload = await fetchRuntimeJson<unknown>(
    `${JOBS_PATH}/${encodeURIComponent(jobId)}`,
    { headers: { Accept: "application/json" } },
  );
  const record = asRecord(payload) ?? {};
  return normalizeRuntimeJob(record.job);
}

export async function cancelRuntimeJob(jobId: string): Promise<RuntimeJob> {
  const payload = await fetchRuntimeJson<unknown>(
    `${JOBS_PATH}/${encodeURIComponent(jobId)}/cancel`,
    { method: "POST", headers: { Accept: "application/json" } },
  );
  const record = asRecord(payload) ?? {};
  return normalizeRuntimeJob(record.job);
}

export async function listRuntimeJobEvents(
  jobId: string,
  query: RuntimeJobEventsQuery = {},
): Promise<RuntimeJobEventsResponse> {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrlWithQuery(
      `${JOBS_PATH}/${encodeURIComponent(jobId)}/events`,
      { after: query.after, limit: query.limit },
    ),
    { headers: { Accept: "application/json" } },
  );
  const record = asRecord(payload) ?? {};
  const events = Array.isArray(record.events)
    ? record.events.map((entry) => normalizeRuntimeJobEvent(entry))
    : [];
  return { events, count: pickNumber(record, "count") ?? events.length };
}

export async function getRuntimeJobOutput(
  jobId: string,
  query: RuntimeJobOutputQuery = {},
): Promise<RuntimeJobOutput> {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrlWithQuery(
      `${JOBS_PATH}/${encodeURIComponent(jobId)}/output`,
      { offset: query.offset, limit: query.limit },
    ),
    { headers: { Accept: "application/json" } },
  );
  const record = asRecord(payload) ?? {};
  return normalizeRuntimeJobOutput(record.output);
}
