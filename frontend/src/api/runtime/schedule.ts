// P3：自动化任务（Schedule）REST 客户端。
//
// 端点（backend/internal/api/runtimeapi/schedule_handlers.go）：
//   GET  /api/runtime/schedule/catalog
//   GET  /api/runtime/schedule/list?session_id=
//   GET  /api/runtime/schedule/history?session_id=&id=&limit=&before=
//   POST /api/runtime/schedule/create
//   POST /api/runtime/schedule/update
//   POST /api/runtime/schedule/pause
//   POST /api/runtime/schedule/resume
//   POST /api/runtime/schedule/delete
//
// 归一化说明：后端有 json tag（camelCase），normalize 仍宽容 PascalCase/snake_case。

import type {
  RuntimeScheduleCatalogResponse,
  RuntimeScheduleCreateRequest,
  RuntimeScheduleCreateResponse,
  RuntimeScheduleDeleteResponse,
  RuntimeScheduleHistoryResponse,
  RuntimeScheduleJobRun,
  RuntimeScheduleJobRunsResponse,
  RuntimeScheduleJobRunsClearAllResponse,
  RuntimeScheduleJobRunsClearResponse,
  RuntimeScheduleJobRunStatus,
  RuntimeScheduleKind,
  RuntimeScheduleListResponse,
  RuntimeScheduleOrigin,
  RuntimeScheduleRecord,
  RuntimeScheduleTask,
  RuntimeScheduleTargetKind,
  RuntimeScheduleTemplate,
  RuntimeScheduleTemplateDeleteResponse,
  RuntimeScheduleTemplateRequest,
  RuntimeScheduleTemplateResponse,
  RuntimeScheduleTemplatesResponse,
  RuntimeScheduleUpdateRequest,
  RuntimeScheduleUpdateResponse,
} from "@/types/runtime";

import { buildRuntimeUrlWithQuery, fetchRuntimeJson } from "./shared";

const SCHEDULE_PATH = "/api/runtime/schedule";

/**
 * 调度写请求（create/update/pause/resume/delete）的超时窗口。
 * 默认 10s 是给“可重试的读请求”设计的；create 需要服务端同步落库并计算首次
 * 调度时间，共享 SQLite 排队时可能超过 10s。超时抛出的 TimeoutError 没有
 * error code，面板会落到 createFailedGeneric（“请检查输入并重试”），
 * 但任务其实已经写入 —— 用户会误以为失败并重复提交。放宽到 30s。
 */
const RUNTIME_WRITE_TIMEOUT_MS = 30_000;

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

function pickNumber(record: RawRecord, ...keys: string[]): number | undefined {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "number" && Number.isFinite(value)) {
      return value;
    }
  }
  return undefined;
}

const scheduleKinds: RuntimeScheduleKind[] = [
  "after",
  "at",
  "every",
  "daily",
  "weekly",
  "cron",
];

/** 未知 kind 不进联合类型：归一化回落 "at"（一次性语义最保守）。 */
export function normalizeScheduleKind(value: unknown): RuntimeScheduleKind {
  const raw = typeof value === "string" ? value.trim().toLowerCase() : "";
  return (scheduleKinds as string[]).includes(raw)
    ? (raw as RuntimeScheduleKind)
    : "at";
}

function normalizeWeekdays(value: unknown): number[] | undefined {
  if (!Array.isArray(value)) {
    return undefined;
  }
  const days = value
    .map((entry) =>
      typeof entry === "number" && Number.isFinite(entry) ? entry : null,
    )
    .filter((entry): entry is number => entry !== null)
    .sort((a, b) => a - b);
  return days.length > 0 ? Array.from(new Set(days)) : undefined;
}

/** 归一化字符串数组（J0：plan）；非数组/空数组回落 undefined。 */
function normalizeStringList(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) {
    return undefined;
  }
  const items = value
    .map((entry) => (typeof entry === "string" ? entry.trim() : ""))
    .filter((entry) => entry.length > 0);
  return items.length > 0 ? items : undefined;
}

function normalizeTemplateWeekdays(value: unknown): number[] {
  const raw =
    typeof value === "string"
      ? (() => {
          try {
            return JSON.parse(value) as unknown;
          } catch {
            return [];
          }
        })()
      : value;
  return normalizeWeekdays(raw) ?? [];
}

function normalizeTemplatePlan(value: unknown): string {
  if (typeof value === "string") {
    return value;
  }
  if (Array.isArray(value)) {
    return value
      .map((entry) => (typeof entry === "string" ? entry.trim() : ""))
      .filter(Boolean)
      .join("\n");
  }
  return "";
}

/** 归一化执行上下文（J1）；未知值回落 session。 */
function normalizeTargetKind(value: unknown): RuntimeScheduleTargetKind {
  const raw = typeof value === "string" ? value.trim().toLowerCase() : "";
  return raw === "workspace" || raw === "headless" ? raw : "session";
}

function normalizeScheduleStatus(
  value: unknown,
): RuntimeScheduleTask["status"] {
  const raw = typeof value === "string" ? value.trim().toLowerCase() : "";
  if (
    raw === "inactive" ||
    raw === "paused" ||
    raw === "blocked" ||
    raw === "failed"
  ) {
    return raw;
  }
  return "active";
}

/** 归一化一条记录（`Record` 与 `CatalogEntry` 共用的规则字段部分）。 */
export function normalizeScheduleRecord(raw: unknown): RuntimeScheduleRecord {
  const record = asRecord(raw) ?? {};
  const normalized: RuntimeScheduleRecord = {
    id: pickString(record, "id", "ID"),
    kind: normalizeScheduleKind(record.kind ?? record.Kind),
    title: pickString(record, "title", "Title"),
    prompt: pickString(record, "prompt", "Prompt"),
    goal: pickString(record, "goal", "Goal") || undefined,
    plan: normalizeStringList(record.plan ?? record.Plan),
    scheduledAt: pickString(record, "scheduledAt", "ScheduledAt"),
    afterSeconds: pickNumber(record, "afterSeconds", "AfterSeconds"),
    everySeconds: pickNumber(record, "everySeconds", "EverySeconds"),
    time: pickString(record, "time", "Time") || undefined,
    timeZone: pickString(record, "timeZone", "TimeZone") || undefined,
    weekdays: normalizeWeekdays(record.weekdays ?? record.Weekdays),
    expression: pickString(record, "expression", "Expression") || undefined,
    provider:
      pickString(
        record,
        "target_provider",
        "TargetProvider",
        "provider",
        "Provider",
        "provider_name",
      ) || undefined,
    model:
      pickString(record, "target_model", "TargetModel", "model", "Model") ||
      undefined,
    reasoningEffort:
      pickString(
        record,
        "target_reasoning_effort",
        "TargetReasoningEffort",
        "reasoningEffort",
        "ReasoningEffort",
        "reasoning_effort",
      ) || undefined,
  };
  return normalized;
}

/** 归一化目录条目（记录 + 会话绑定 + 生命周期 + 最近回执）。 */
export function normalizeScheduleTask(raw: unknown): RuntimeScheduleTask {
  const record = asRecord(raw) ?? {};
  const base = normalizeScheduleRecord(raw);
  const lastDeliveryRaw = asRecord(record.lastDelivery ?? record.LastDelivery);
  const origin = pickString(record, "origin", "Origin");
  const status = normalizeScheduleStatus(
    pickString(record, "status", "Status"),
  );
  return {
    ...base,
    sessionId: pickString(record, "sessionId", "SessionID", "session_id"),
    status,
    lastErrorCode:
      pickString(record, "lastErrorCode", "LastErrorCode", "last_error_code") ||
      undefined,
    lastError:
      pickString(record, "lastError", "LastError", "last_error") || undefined,
    attemptCount: pickNumber(
      record,
      "attemptCount",
      "AttemptCount",
      "attempt_count",
    ),
    lastAttemptAt:
      pickString(record, "lastAttemptAt", "LastAttemptAt", "last_attempt_at") ||
      undefined,
    deadLetteredAt:
      pickString(
        record,
        "deadLetteredAt",
        "DeadLetteredAt",
        "dead_lettered_at",
      ) || undefined,
    origin: (origin === "manual" ? "manual" : "model") as RuntimeScheduleOrigin,
    targetKind: normalizeTargetKind(
      record.targetKind ?? record.TargetKind ?? record.target_kind,
    ),
    targetWorkspace:
      pickString(
        record,
        "targetWorkspace",
        "TargetWorkspace",
        "target_workspace",
      ) || undefined,
    targetProfile:
      pickString(record, "targetProfile", "TargetProfile", "target_profile") ||
      undefined,
    createdBySessionId:
      pickString(
        record,
        "createdBySessionId",
        "CreatedBySessionID",
        "created_by_session_id",
      ) || undefined,
    lastDelivery: lastDeliveryRaw
      ? {
          scheduledAt: pickString(
            lastDeliveryRaw,
            "scheduledAt",
            "ScheduledAt",
          ),
          deliveredAt: pickString(
            lastDeliveryRaw,
            "deliveredAt",
            "DeliveredAt",
          ),
          messageId: pickString(lastDeliveryRaw, "messageId", "MessageID"),
        }
      : null,
  };
}

/** Normalize persisted template records at the HTTP boundary. */
export function normalizeScheduleTemplate(
  raw: unknown,
): RuntimeScheduleTemplate {
  const record = asRecord(raw) ?? {};
  return {
    id: pickString(record, "id", "ID"),
    title: pickString(record, "title", "Title"),
    description: pickString(record, "description", "Description"),
    taskTitle: pickString(record, "taskTitle", "TaskTitle", "task_title"),
    category: pickString(record, "category", "Category"),
    prompt: pickString(record, "prompt", "Prompt"),
    kind: normalizeScheduleKind(record.kind ?? record.Kind),
    amount: pickString(record, "amount", "Amount"),
    atTime: pickString(record, "atTime", "AtTime", "at_time"),
    clockTime: pickString(record, "clockTime", "ClockTime", "clock_time"),
    timeZone: pickString(record, "timeZone", "TimeZone", "time_zone"),
    weekdays: normalizeTemplateWeekdays(
      record.weekdays ?? record.Weekdays ?? record.weekdays_json,
    ),
    cron: pickString(record, "cron", "Cron"),
    goal: pickString(record, "goal", "Goal"),
    plan: normalizeTemplatePlan(record.plan ?? record.Plan),
    targetKind: normalizeTargetKind(
      record.targetKind ?? record.TargetKind ?? record.target_kind,
    ),
    targetWorkspace: pickString(
      record,
      "targetWorkspace",
      "TargetWorkspace",
      "target_workspace",
    ),
    targetProfile: pickString(
      record,
      "targetProfile",
      "TargetProfile",
      "target_profile",
    ),
    provider: pickString(
      record,
      "provider",
      "Provider",
      "targetProvider",
      "TargetProvider",
      "target_provider",
    ),
    model: pickString(
      record,
      "model",
      "Model",
      "targetModel",
      "TargetModel",
      "target_model",
    ),
    reasoningEffort: pickString(
      record,
      "reasoningEffort",
      "ReasoningEffort",
      "TargetReasoningEffort",
      "reasoning_effort",
      "target_reasoning_effort",
    ),
    createdAt:
      pickString(record, "createdAt", "CreatedAt", "created_at") || undefined,
    updatedAt:
      pickString(record, "updatedAt", "UpdatedAt", "updated_at") || undefined,
  };
}

export async function fetchScheduleTemplates(): Promise<RuntimeScheduleTemplatesResponse> {
  const payload = await fetchRuntimeJson<unknown>(
    `${SCHEDULE_PATH}/templates`,
    {
      headers: { Accept: "application/json" },
    },
  );
  const record = asRecord(payload) ?? {};
  const templates = Array.isArray(record.templates)
    ? record.templates.map((entry) => normalizeScheduleTemplate(entry))
    : [];
  return {
    templates,
    count: pickNumber(record, "count") ?? templates.length,
  };
}

export async function createScheduleTemplate(
  request: RuntimeScheduleTemplateRequest,
): Promise<RuntimeScheduleTemplateResponse> {
  const payload = await fetchRuntimeJson<unknown>(
    `${SCHEDULE_PATH}/templates`,
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
  const record = asRecord(payload) ?? {};
  return {
    template: normalizeScheduleTemplate(record.template ?? record.Template),
  };
}

export async function updateScheduleTemplate(
  id: string,
  request: RuntimeScheduleTemplateRequest,
): Promise<RuntimeScheduleTemplateResponse> {
  const payload = await fetchRuntimeJson<unknown>(
    `${SCHEDULE_PATH}/templates/${encodeURIComponent(id)}`,
    {
      method: "PUT",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
  const record = asRecord(payload) ?? {};
  return {
    template: normalizeScheduleTemplate(record.template ?? record.Template),
  };
}

export async function deleteScheduleTemplate(
  id: string,
): Promise<RuntimeScheduleTemplateDeleteResponse> {
  const payload = await fetchRuntimeJson<unknown>(
    `${SCHEDULE_PATH}/templates/${encodeURIComponent(id)}`,
    {
      method: "DELETE",
      headers: { Accept: "application/json" },
    },
  );
  const record = asRecord(payload) ?? {};
  return {
    id: pickString(record, "id", "ID") || id,
    deleted: record.deleted === true,
  };
}

export async function fetchScheduleCatalog(): Promise<RuntimeScheduleCatalogResponse> {
  const payload = await fetchRuntimeJson<unknown>(`${SCHEDULE_PATH}/catalog`, {
    headers: { Accept: "application/json" },
  });
  const record = asRecord(payload) ?? {};
  const tasks = Array.isArray(record.tasks)
    ? record.tasks.map((entry) => normalizeScheduleTask(entry))
    : [];
  return { tasks, count: pickNumber(record, "count") ?? tasks.length };
}

export async function fetchScheduleList(
  sessionId: string,
): Promise<RuntimeScheduleListResponse> {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrlWithQuery(`${SCHEDULE_PATH}/list`, {
      session_id: sessionId,
    }),
    { headers: { Accept: "application/json" } },
  );
  const record = asRecord(payload) ?? {};
  const tasks = Array.isArray(record.tasks)
    ? record.tasks.map((entry) => normalizeScheduleRecord(entry))
    : [];
  return { tasks, count: pickNumber(record, "count") ?? tasks.length };
}

export async function fetchScheduleHistory(
  sessionId: string,
  id: string,
  query: { limit: number; before?: string },
): Promise<RuntimeScheduleHistoryResponse> {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrlWithQuery(`${SCHEDULE_PATH}/history`, {
      session_id: sessionId,
      id,
      limit: query.limit,
      before: query.before,
    }),
    { headers: { Accept: "application/json" } },
  );
  const record = asRecord(payload) ?? {};
  const records = Array.isArray(record.records)
    ? record.records.map((entry) => {
        const item = asRecord(entry) ?? {};
        const receipt = normalizeScheduleRecord(entry);
        return {
          scheduledAt: receipt.scheduledAt,
          deliveredAt: pickString(item, "deliveredAt", "DeliveredAt"),
          messageId: pickString(item, "messageId", "MessageID"),
          prompt: pickString(item, "prompt", "Prompt") || undefined,
        };
      })
    : [];
  return {
    id: pickString(record, "id", "ID"),
    records,
    nextBefore: pickString(record, "nextBefore", "NextBefore") || undefined,
    earlierRecordsUnavailable: record.earlierRecordsUnavailable === true,
    earlierRecordsPruned: record.earlierRecordsPruned === true,
    retentionDays: pickNumber(record, "retentionDays") ?? 0,
    retentionRecords: pickNumber(record, "retentionRecords") ?? 0,
  };
}

/** 读取某作业的运行历史（J3；新到旧，limit 有界）。 */
export async function fetchScheduleJobRuns(
  sessionId: string | undefined,
  id: string,
  limit = 20,
): Promise<RuntimeScheduleJobRunsResponse> {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrlWithQuery(`${SCHEDULE_PATH}/runs`, {
      session_id: sessionId,
      id,
      limit,
    }),
    { headers: { Accept: "application/json" } },
  );
  const record = asRecord(payload) ?? {};
  const runs = Array.isArray(record.runs)
    ? record.runs.map((entry) => normalizeJobRun(entry))
    : [];
  return {
    id: pickString(record, "id", "ID"),
    runs,
    count: pickNumber(record, "count") ?? runs.length,
  };
}

/** 清理某作业的全部运行历史，但保留作业本身。 */
export async function clearScheduleJobRuns(
  sessionId: string | undefined,
  id: string,
): Promise<RuntimeScheduleJobRunsClearResponse> {
  const payload = await fetchRuntimeJson<unknown>(
    `${SCHEDULE_PATH}/runs/clear`,
    {
      method: "POST",
      headers: { Accept: "application/json", "Content-Type": "application/json" },
      body: JSON.stringify({
        session_id: sessionId ?? "",
        id,
      }),
    },
  );
  const record = asRecord(payload) ?? {};
  return {
    id: pickString(record, "id", "ID"),
    deletedCount:
      pickNumber(record, "deletedCount", "DeletedCount", "deleted_count") ?? 0,
  };
}

/** 清理全部自动化任务的运行历史，任务和投递状态保持不变。 */
export async function clearAllScheduleJobRuns(): Promise<RuntimeScheduleJobRunsClearAllResponse> {
  const payload = await fetchRuntimeJson<unknown>(
    `${SCHEDULE_PATH}/runs/clear-all`,
    {
      method: "POST",
      headers: { Accept: "application/json" },
    },
  );
  const record = asRecord(payload) ?? {};
  return {
    deletedCount:
      pickNumber(record, "deletedCount", "DeletedCount", "deleted_count") ?? 0,
  };
}

function normalizeJobRun(raw: unknown): RuntimeScheduleJobRun {
  const run = asRecord(raw) ?? {};
  const status = pickString(run, "status", "Status");
  return {
    id: pickNumber(run, "id", "ID") ?? 0,
    scheduleId: pickString(run, "scheduleId", "ScheduleID", "schedule_id"),
    status: (status === "failed"
      ? "failed"
      : "done") as RuntimeScheduleJobRunStatus,
    output: pickString(run, "output", "Output") || undefined,
    error: pickString(run, "error", "Error") || undefined,
    errorCode:
      pickString(run, "errorCode", "ErrorCode", "error_code") || undefined,
    sessionId:
      pickString(run, "sessionId", "SessionID", "session_id") || undefined,
    startedAt: pickString(run, "startedAt", "StartedAt"),
    finishedAt: pickString(run, "finishedAt", "FinishedAt"),
  };
}

export async function updateScheduleTask(
  request: RuntimeScheduleUpdateRequest,
): Promise<RuntimeScheduleUpdateResponse> {
  const payload = await fetchRuntimeJson<unknown>(`${SCHEDULE_PATH}/update`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({
      session_id: request.sessionId,
      id: request.id,
      expected: request.expected,
      title: request.title,
      prompt: request.prompt,
      goal: request.goal,
      plan: request.plan,
      target_kind: request.target_kind,
      target_workspace: request.target_workspace,
      target_profile: request.target_profile,
      target_provider: request.provider,
      target_model: request.model,
      target_reasoning_effort: request.reasoning_effort,
      target_session_id: request.target_session_id,
      change: request.change,
    }),
  });
  return normalizeScheduleUpdateResponse(payload);
}

function normalizeScheduleUpdateResponse(
  payload: unknown,
): RuntimeScheduleUpdateResponse {
  const record = asRecord(payload) ?? {};
  const rawStatus = pickString(record, "status", "Status").trim().toLowerCase();
  return {
    id: pickString(record, "id", "ID"),
    updated: record.updated === true,
    record:
      record.record !== undefined
        ? normalizeScheduleRecord(record.record)
        : undefined,
    status:
      rawStatus === "active" ||
      rawStatus === "paused" ||
      rawStatus === "blocked" ||
      rawStatus === "failed" ||
      rawStatus === "inactive"
        ? rawStatus
        : undefined,
    code: pickString(record, "code", "Code") || undefined,
    message: pickString(record, "message", "Message") || undefined,
  };
}

async function controlScheduleTask(
  action: "pause" | "resume",
  sessionId: string,
  id: string,
): Promise<RuntimeScheduleUpdateResponse> {
  const payload = await fetchRuntimeJson<unknown>(
    `${SCHEDULE_PATH}/${action}`,
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ session_id: sessionId, id }),
    },
  );
  return normalizeScheduleUpdateResponse(payload);
}

export async function pauseScheduleTask(
  sessionId: string,
  id: string,
): Promise<RuntimeScheduleUpdateResponse> {
  return controlScheduleTask("pause", sessionId, id);
}

export async function resumeScheduleTask(
  sessionId: string,
  id: string,
): Promise<RuntimeScheduleUpdateResponse> {
  return controlScheduleTask("resume", sessionId, id);
}

export async function deleteScheduleTask(
  sessionId: string,
  id: string,
): Promise<RuntimeScheduleDeleteResponse> {
  const payload = await fetchRuntimeJson<unknown>(`${SCHEDULE_PATH}/delete`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ session_id: sessionId, id }),
  });
  const record = asRecord(payload) ?? {};
  return {
    id: pickString(record, "id", "ID"),
    deleted: record.deleted === true,
    code: pickString(record, "code", "Code") || undefined,
  };
}

/** 人工创建（origin=manual）：选择器校验在后端服务层，与模型工具同一条管线。 */
export async function createScheduleTask(
  request: RuntimeScheduleCreateRequest,
): Promise<RuntimeScheduleCreateResponse> {
  // Keep the public form/request model camelCase where it is convenient for
  // React, but make the HTTP boundary explicit. The schedule handler only
  // accepts snake_case JSON (notably session_id); serializing request directly
  // makes the default session target fail validation.
  const requestBody = {
    session_id: request.sessionId,
    title: request.title,
    prompt: request.prompt,
    after_seconds: request.after_seconds,
    at: request.at,
    every_seconds: request.every_seconds,
    daily: request.daily,
    weekly: request.weekly,
    cron: request.cron,
    goal: request.goal,
    plan: request.plan,
    target_kind: request.target_kind ?? "session",
    target_workspace: request.target_workspace,
    target_profile: request.target_profile,
    target_provider: request.provider,
    target_model: request.model,
    target_reasoning_effort: request.reasoning_effort,
  };
  // 写请求单独放宽超时：create 会在服务端同步落库并触发首次调度计算，
  // 共享 SQLite 偶发排队时可能超过默认 10s。若沿用默认超时，前端会抛
  // TimeoutError（无错误码）→ 面板落到 createFailedGeneric「请检查输入并重试」，
  // 而任务其实已经创建成功，用户会重复提交。故这里给写请求更长的窗口。
  const payload = await fetchRuntimeJson<unknown>(
    `${SCHEDULE_PATH}/create`,
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(requestBody),
    },
    RUNTIME_WRITE_TIMEOUT_MS,
  );
  const record = asRecord(payload) ?? {};
  const created = normalizeScheduleRecord(record.record ?? record.Record);
  return {
    record: created,
    origin: "manual",
  };
}
