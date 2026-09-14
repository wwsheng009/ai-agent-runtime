// P2-1A：会话统计客户端（GET /api/runtime/sessions/stats）。
//
// 后端契约（backend/internal/api/skills/handler.go:2569-2590 GetSessionStats）：
//   查询参数：user_id?（空串回退到服务端默认用户，由 resolveServerSessionUserID 归一）
//   响应体：{user_id, stats: chat.SessionStatistics}
//     stats 字段的 json tag 见 internal/chat/storage.go:73-81：
//       total / active / idle / closed / archived / totalMessages（camelCase）/ tags
//   会话管理器未配置 → 503 ErrConfigInvalid；存储层不支持跨用户列举 → 503 STORE_UNAVAILABLE。
//
// 归一化纪律：
//   * `stats` 不是对象 → 抛错（不把结构异常伪装成「0 个会话」的假统计）；
//   * 计数字段只接受有限数，缺失/非法按 0 计（仅在 stats 对象确实存在时兜底）；
//   * tags 只保留「字符串键 → 有限数」的条目，不构造占位标签；
//   * 降级分类只看 HTTP 状态码（isSessionStatsUnavailable），不与真实失败混同。

import { RuntimeApiError, buildRuntimeUrlWithQuery, fetchRuntimeJson } from "./shared";

import type {
  RuntimeSessionStats,
  RuntimeSessionStatsResponse,
} from "@/types/runtime";

export const SESSION_STATS_PATH = "/api/runtime/sessions/stats";

type RawRecord = Record<string, unknown>;

function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

function readCount(record: RawRecord, key: string): number {
  const value = record[key];
  return typeof value === "number" && Number.isFinite(value) && value >= 0
    ? Math.floor(value)
    : 0;
}

function readTagCounts(record: RawRecord, key: string): Record<string, number> {
  const value = asRecord(record[key]);
  if (!value) {
    return {};
  }
  const tags: Record<string, number> = {};
  for (const [tag, count] of Object.entries(value)) {
    if (typeof count === "number" && Number.isFinite(count) && count >= 0) {
      tags[tag] = Math.floor(count);
    }
  }
  return tags;
}

/** 归一化 `chat.SessionStatistics`；结构不是对象即抛错。 */
export function normalizeSessionStats(raw: unknown): RuntimeSessionStats {
  const record = asRecord(raw);
  if (!record) {
    throw new Error("runtime session stats payload is not an object");
  }
  return {
    total: readCount(record, "total"),
    active: readCount(record, "active"),
    idle: readCount(record, "idle"),
    closed: readCount(record, "closed"),
    archived: readCount(record, "archived"),
    totalMessages: readCount(record, "totalMessages"),
    tags: readTagCounts(record, "tags"),
  };
}

/** 归一化统计响应；`stats` 缺失或结构异常即抛错（调用方转错误态）。 */
export function normalizeSessionStatsResponse(
  raw: unknown,
): RuntimeSessionStatsResponse {
  const record = asRecord(raw);
  if (!record) {
    throw new Error("runtime session stats response is not an object");
  }
  const userId = record.user_id;
  return {
    user_id: typeof userId === "string" ? userId : "",
    stats: normalizeSessionStats(record.stats),
  };
}

/** 会话统计（后端按 user_id 聚合；空 userId 交给服务端取默认用户）。 */
export async function fetchRuntimeSessionStats(
  userId?: string,
  options: { signal?: AbortSignal } = {},
): Promise<RuntimeSessionStatsResponse> {
  const normalizedUserId = userId?.trim();
  return fetchRuntimeJson<unknown>(
    buildRuntimeUrlWithQuery(SESSION_STATS_PATH, {
      user_id: normalizedUserId || undefined,
    }),
    {
      headers: {
        Accept: "application/json",
      },
      signal: options.signal,
    },
  ).then(normalizeSessionStatsResponse);
}

/**
 * 统计端点不可用（未实现 / 未启用 / 存储不支持），与真实失败区分：
 * 404/405/501/503 归「统计不可用」，500 等按真实失败呈现。
 */
export function isSessionStatsUnavailable(error: unknown): boolean {
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
