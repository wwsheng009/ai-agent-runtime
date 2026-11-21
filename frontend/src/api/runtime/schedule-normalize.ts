// Schedule HTTP 边界归一化：记录、模板、运行历史与写响应。
// 拆出本模块是为了让 API 端点实现保持轻薄；从 ./schedule 继续 re-export，
// 既有调用方无需改 import。

import type {
  RuntimeScheduleJobRun,
  RuntimeScheduleJobRunStatus,
  RuntimeScheduleKind,
  RuntimeScheduleRecord,
  RuntimeScheduleTargetKind,
  RuntimeScheduleTask,
  RuntimeScheduleTemplate,
  RuntimeScheduleUpdateResponse,
} from "@/types/runtime";

type RawRecord = Record<string, unknown>;

export function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

export function pickString(record: RawRecord, ...keys: string[]): string {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "string") {
      return value;
    }
  }
  return "";
}

export function pickNumber(record: RawRecord, ...keys: string[]): number | undefined {
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
  return {
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
    origin: (origin === "manual" ? "manual" : "model") as RuntimeScheduleTask["origin"],
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

export function normalizeJobRun(raw: unknown): RuntimeScheduleJobRun {
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

export function normalizeScheduleUpdateResponse(
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