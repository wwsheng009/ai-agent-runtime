// P3：自动化任务（Schedule）REST 客户端。
//
// 端点（backend/internal/api/runtimeapi/schedule_handlers.go）：
//   GET  /api/runtime/schedule/catalog
//   GET  /api/runtime/schedule/list?session_id=
//   GET  /api/runtime/schedule/history?session_id=&id=&limit=&before=
//   POST /api/runtime/schedule/create
//   POST /api/runtime/schedule/update
//   POST /api/runtime/schedule/delete
//
// 归一化说明：后端有 json tag（camelCase），normalize 仍宽容 PascalCase/snake_case。

import type {
  RuntimeScheduleCatalogResponse,
  RuntimeScheduleCreateRequest,
  RuntimeScheduleCreateResponse,
  RuntimeScheduleDeleteResponse,
  RuntimeScheduleHistoryResponse,
  RuntimeScheduleKind,
  RuntimeScheduleListResponse,
  RuntimeScheduleOrigin,
  RuntimeScheduleRecord,
  RuntimeScheduleTask,
  RuntimeScheduleUpdateRequest,
  RuntimeScheduleUpdateResponse,
} from "@/types/runtime";

import {
  buildRuntimeUrlWithQuery,
  fetchRuntimeJson,
} from "./shared";

const SCHEDULE_PATH = "/api/runtime/schedule";

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
    .map((entry) => (typeof entry === "number" && Number.isFinite(entry) ? entry : null))
    .filter((entry): entry is number => entry !== null)
    .sort((a, b) => a - b);
  return days.length > 0 ? Array.from(new Set(days)) : undefined;
}

/** 归一化一条记录（`Record` 与 `CatalogEntry` 共用的规则字段部分）。 */
export function normalizeScheduleRecord(raw: unknown): RuntimeScheduleRecord {
  const record = asRecord(raw) ?? {};
  const normalized: RuntimeScheduleRecord = {
    id: pickString(record, "id", "ID"),
    kind: normalizeScheduleKind(record.kind ?? record.Kind),
    title: pickString(record, "title", "Title"),
    prompt: pickString(record, "prompt", "Prompt"),
    scheduledAt: pickString(record, "scheduledAt", "ScheduledAt"),
    afterSeconds: pickNumber(record, "afterSeconds", "AfterSeconds"),
    everySeconds: pickNumber(record, "everySeconds", "EverySeconds"),
    time: pickString(record, "time", "Time") || undefined,
    timeZone: pickString(record, "timeZone", "TimeZone") || undefined,
    weekdays: normalizeWeekdays(record.weekdays ?? record.Weekdays),
    expression: pickString(record, "expression", "Expression") || undefined,
  };
  return normalized;
}

/** 归一化目录条目（记录 + 会话绑定 + 生命周期 + 最近回执）。 */
export function normalizeScheduleTask(raw: unknown): RuntimeScheduleTask {
  const record = asRecord(raw) ?? {};
  const base = normalizeScheduleRecord(raw);
  const lastDeliveryRaw = asRecord(record.lastDelivery ?? record.LastDelivery);
  const origin = pickString(record, "origin", "Origin");
  return {
    ...base,
    sessionId: pickString(record, "sessionId", "SessionID", "session_id"),
    status: pickString(record, "status", "Status") === "inactive" ? "inactive" : "active",
    origin: (origin === "manual" ? "manual" : "model") as RuntimeScheduleOrigin,
    lastDelivery: lastDeliveryRaw
      ? {
          scheduledAt: pickString(lastDeliveryRaw, "scheduledAt", "ScheduledAt"),
          deliveredAt: pickString(lastDeliveryRaw, "deliveredAt", "DeliveredAt"),
          messageId: pickString(lastDeliveryRaw, "messageId", "MessageID"),
        }
      : null,
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
    buildRuntimeUrlWithQuery(`${SCHEDULE_PATH}/list`, { session_id: sessionId }),
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
    earlierRecordsUnavailable:
      record.earlierRecordsUnavailable === true,
    earlierRecordsPruned: record.earlierRecordsPruned === true,
    retentionDays: pickNumber(record, "retentionDays") ?? 0,
    retentionRecords: pickNumber(record, "retentionRecords") ?? 0,
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
      change: request.change,
    }),
  });
  const record = asRecord(payload) ?? {};
  return {
    id: pickString(record, "id", "ID"),
    updated: record.updated === true,
    record:
      record.record !== undefined ? normalizeScheduleRecord(record.record) : undefined,
    code: pickString(record, "code", "Code") || undefined,
    message: pickString(record, "message", "Message") || undefined,
  };
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
  const payload = await fetchRuntimeJson<unknown>(`${SCHEDULE_PATH}/create`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify(request),
  });
  const record = asRecord(payload) ?? {};
  const created = normalizeScheduleRecord(record.record ?? record.Record);
  return {
    record: created,
    origin: "manual",
  };
}
