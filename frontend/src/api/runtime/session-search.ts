// P2-1A：会话元数据检索客户端（POST /api/runtime/sessions/search）。
//
// 后端契约（backend/internal/api/skills/handler.go:2587-2648 SearchSessions）：
//   请求体（JSON，均可空，snake_case）：{user_id?, tags?[], state?, limit?, offset?}
//     —— 空 user_id/state/limit/offset 会回退到同名 URL query 参数。
//   响应体：{sessions: chat.Session[], count, filters: chat.SessionSearchOptions}
//     —— filters 的 json tag 是 camelCase（manager.go:581-588），与请求体不同名。
//
// 语义（backend/internal/chat）：tags 为 AND 过滤，state 为全等匹配
// （active/idle/closed/archived）；user_id 为空时需要存储层支持 ListAll，
// 不支持时后端返回 503 STORE_UNAVAILABLE（handler.go:8573-8581）。
//
// 归一化纪律：
//   * `sessions` 不是数组 → 抛错（不把结构异常伪装成空结果）；
//   * 单条记录缺 `id` → 丢弃该条（不构造占位 id，也不让整页失败）；
//   * 降级分类只看 HTTP 状态码（isSessionSearchUnavailable）。

import {
  RUNTIME_FETCH_TIMEOUT_MS,
  RuntimeApiError,
  buildRuntimeUrl,
  fetchRuntimeJson,
} from "./shared";

import type {
  RuntimeSessionRecord,
  RuntimeSessionSearchEcho,
  RuntimeSessionSearchFilters,
  RuntimeSessionSearchResponse,
} from "@/types/runtime";

export const SESSION_SEARCH_PATH = "/api/runtime/sessions/search";

/** 未显式传 limit 时的单页条数（后端默认不设上限，由调用方收口）。 */
export const DEFAULT_SESSION_SEARCH_LIMIT = 50;

type RawRecord = Record<string, unknown>;

function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

function readString(record: RawRecord, ...keys: string[]): string {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "string") {
      return value;
    }
  }
  return "";
}

function readStringArray(record: RawRecord, key: string): string[] {
  const value = record[key];
  if (!Array.isArray(value)) {
    return [];
  }
  return value.filter((entry): entry is string => typeof entry === "string");
}

function readOptionalString(record: RawRecord, ...keys: string[]): string | undefined {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "string" && value.trim()) {
      return value;
    }
  }
  return undefined;
}

function readOptionalNumber(record: RawRecord, ...keys: string[]): number | undefined {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "number" && Number.isFinite(value)) {
      return value;
    }
  }
  return undefined;
}

/**
 * 归一化筛选条件：tag 去空白 / 去重 / 去空串，limit、offset 取非负整数。
 * 空 tags 数组与空 user_id / state 一律省略，避免后端把空串当筛选值。
 */
export function normalizeSessionSearchFilters(
  filters: RuntimeSessionSearchFilters = {},
): RuntimeSessionSearchEcho {
  const tags = (filters.tags ?? [])
    .map((tag) => tag.trim())
    .filter((tag) => tag.length > 0);

  const normalized: RuntimeSessionSearchEcho = {};
  const userId = filters.userId?.trim();
  const state = filters.state?.trim();
  const limit = filters.limit ?? DEFAULT_SESSION_SEARCH_LIMIT;
  const offset = filters.offset ?? 0;

  if (userId) {
    normalized.userId = userId;
  }
  if (tags.length > 0) {
    normalized.tags = Array.from(new Set(tags));
  }
  if (state) {
    normalized.state = state;
  }
  normalized.limit = Number.isFinite(limit) && limit > 0 ? Math.floor(limit) : DEFAULT_SESSION_SEARCH_LIMIT;
  normalized.offset = Number.isFinite(offset) && offset > 0 ? Math.floor(offset) : 0;
  return normalized;
}

/** 请求体（snake_case）：只发送非空字段。 */
export function buildSessionSearchBody(
  filters: RuntimeSessionSearchEcho,
): RawRecord {
  const body: RawRecord = {};
  if (filters.userId) {
    body.user_id = filters.userId;
  }
  if (filters.tags && filters.tags.length > 0) {
    body.tags = filters.tags;
  }
  if (filters.state) {
    body.state = filters.state;
  }
  body.limit = filters.limit;
  body.offset = filters.offset;
  return body;
}

/** 单条会话记录：`id` 缺失即视为不可用记录，返回 null 交由调用方丢弃。 */
export function normalizeSessionSearchRecord(raw: unknown): RuntimeSessionRecord | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const id = readString(record, "id", "ID");
  if (!id.trim()) {
    return null;
  }

  const metadata = asRecord(record.metadata);
  const normalized: RuntimeSessionRecord = { id };

  const userId = readOptionalString(record, "userId", "user_id");
  if (userId) {
    normalized.userId = userId;
  }
  const state = readOptionalString(record, "state", "State");
  if (state) {
    normalized.state = state;
  }
  const createdAt = readOptionalString(record, "createdAt", "created_at");
  if (createdAt) {
    normalized.createdAt = createdAt;
  }
  const updatedAt = readOptionalString(record, "updatedAt", "updated_at");
  if (updatedAt) {
    normalized.updatedAt = updatedAt;
  }
  if (record.expiresAt === null) {
    normalized.expiresAt = null;
  } else {
    const expiresAt = readOptionalString(record, "expiresAt", "expires_at");
    if (expiresAt) {
      normalized.expiresAt = expiresAt;
    }
  }

  if (metadata) {
    const title = readOptionalString(metadata, "title");
    const titleSource = readOptionalString(metadata, "titleSource", "title_source");
    const summary = readOptionalString(metadata, "summary");
    const tags = readStringArray(metadata, "tags");
    const lastAgent = readOptionalString(metadata, "lastAgent", "last_agent");
    const lastSkill = readOptionalString(metadata, "lastSkill", "last_skill");
    const lastModel = readOptionalString(metadata, "lastModel", "last_model");
    const createdBy = readOptionalString(metadata, "createdBy", "created_by");
    const totalTurns = readOptionalNumber(metadata, "totalTurns", "total_turns");
    const context = asRecord(metadata.context);

    normalized.metadata = {
      ...(title ? { title } : {}),
      ...(titleSource ? { titleSource } : {}),
      ...(summary ? { summary } : {}),
      ...(tags.length > 0 ? { tags } : {}),
      ...(typeof totalTurns === "number" ? { totalTurns } : {}),
      ...(lastAgent ? { lastAgent } : {}),
      ...(lastSkill ? { lastSkill } : {}),
      ...(lastModel ? { lastModel } : {}),
      ...(createdBy ? { createdBy } : {}),
      ...(context ? { context } : {}),
    };
  }

  return normalized;
}

/**
 * 归一化检索响应；`sessions` 结构不符合契约时抛错（调用方转成错误态）。
 * `fallbackFilters` 用于后端未回显 filters 时保持 UI 与请求一致。
 */
export function normalizeSessionSearchResponse(
  raw: unknown,
  fallbackFilters: RuntimeSessionSearchEcho = {},
): RuntimeSessionSearchResponse {
  const record = asRecord(raw);
  if (!record || !Array.isArray(record.sessions)) {
    throw new Error("runtime session search response is missing a sessions array");
  }
  const rawSessions = record.sessions;

  const sessions = rawSessions
    .map((entry) => normalizeSessionSearchRecord(entry))
    .filter((entry): entry is RuntimeSessionRecord => entry !== null);

  const echo = asRecord(record.filters);
  const filters: RuntimeSessionSearchEcho = echo
    ? {
        ...(readOptionalString(echo, "userId", "user_id")
          ? { userId: readOptionalString(echo, "userId", "user_id") }
          : {}),
        ...(readStringArray(echo, "tags").length > 0
          ? { tags: readStringArray(echo, "tags") }
          : {}),
        ...(readOptionalString(echo, "state")
          ? { state: readOptionalString(echo, "state") }
          : {}),
        limit: readOptionalNumber(echo, "limit") ?? fallbackFilters.limit,
        offset: readOptionalNumber(echo, "offset") ?? fallbackFilters.offset,
      }
    : { ...fallbackFilters };

  return {
    sessions,
    count: readOptionalNumber(record, "count") ?? sessions.length,
    filters,
  };
}

/**
 * 服务端检索不可用的降级判据：路由缺失（404/405）、未实现（501）、
 * 存储不可用（503 STORE_UNAVAILABLE）。其余错误按真实失败呈现。
 */
export function isSessionSearchUnavailable(error: unknown): boolean {
  if (!(error instanceof RuntimeApiError)) {
    return false;
  }
  return (
    error.status === 404 ||
    error.status === 405 ||
    error.status === 501 ||
    error.status === 503
  );
}

export type SessionSearchRequestOptions = {
  signal?: AbortSignal;
};

export async function searchRuntimeSessions(
  filters: RuntimeSessionSearchFilters = {},
  options: SessionSearchRequestOptions = {},
): Promise<RuntimeSessionSearchResponse> {
  const normalized = normalizeSessionSearchFilters(filters);
  const payload = await fetchRuntimeJson<unknown>(buildRuntimeUrl(SESSION_SEARCH_PATH), {
    method: "POST",
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    body: JSON.stringify(buildSessionSearchBody(normalized)),
    signal: combineAbortSignals(options.signal),
  });
  return normalizeSessionSearchResponse(payload, normalized);
}

/**
 * fetchRuntimeJson 只在调用方未传 signal 时注入超时；传入 signal 时
 * 这里自行把调用方 signal 与超时信号合并，避免丢失超时保护。
 */
function combineAbortSignals(signal?: AbortSignal): AbortSignal | undefined {
  if (!signal) {
    return undefined;
  }
  // 运行期/测试环境可能缺少 AbortSignal.timeout|any（旧 jsdom），此时退回调用方 signal。
  if (typeof AbortSignal?.timeout !== "function") {
    return signal;
  }
  const timeout = AbortSignal.timeout(RUNTIME_FETCH_TIMEOUT_MS);
  if (typeof AbortSignal.any === "function") {
    return AbortSignal.any([signal, timeout]);
  }
  return signal;
}
