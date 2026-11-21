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
  RuntimeScheduleJobRunsResponse,
  RuntimeScheduleJobRunsClearAllResponse,
  RuntimeScheduleJobRunsClearResponse,
  RuntimeScheduleListResponse,
  RuntimeScheduleTemplateDeleteResponse,
  RuntimeScheduleTemplateRequest,
  RuntimeScheduleTemplateResponse,
  RuntimeScheduleTemplatesResponse,
  RuntimeScheduleUpdateRequest,
  RuntimeScheduleUpdateResponse,
} from "@/types/runtime";

import { buildRuntimeUrlWithQuery, fetchRuntimeJson } from "./shared";
import {
  asRecord,
  normalizeJobRun,
  normalizeScheduleRecord,
  normalizeScheduleTask,
  normalizeScheduleTemplate,
  normalizeScheduleUpdateResponse,
  pickNumber,
  pickString,
} from "./schedule-normalize";

export {
  normalizeScheduleKind,
  normalizeScheduleRecord,
  normalizeScheduleTask,
  normalizeScheduleTemplate,
} from "./schedule-normalize";

const SCHEDULE_PATH = "/api/runtime/schedule";

/**
 * 调度写请求（create/update/pause/resume/delete）的超时窗口。
 * 默认 10s 是给“可重试的读请求”设计的；create 需要服务端同步落库并计算首次
 * 调度时间，共享 SQLite 排队时可能超过 10s。超时抛出的 TimeoutError 没有
 * error code，面板会落到 createFailedGeneric（“请检查输入并重试”），
 * 但任务其实已经写入 —— 用户会误以为失败并重复提交。放宽到 30s。
 */
const RUNTIME_WRITE_TIMEOUT_MS = 30_000;

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
