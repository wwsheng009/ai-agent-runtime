// 连接器运行日志（GET /api/connectors/{id}/logs）的查询边界与归一化。
//
// 从 connectors.ts 拆出（P0-2：单文件 ≤500 非空行），对外经
// @/api/runtime/connectors 重导出，调用方不需要改导入路径。
//
// 归一化纪律（与 connectors.ts 一致）：
//   * `entries` 结构缺失（非数组）→ 抛错，不降级成「空列表」；
//   * 单条无有效时间 → 只丢该条，其余照常呈现；
//   * stats 缺失/非法 → 零值（安全降级，不阻塞日志展示）；
//   * 未知 level/direction 原样保留，渲染器负责降级展示。

import type {
  ConnectorLogEntry,
  ConnectorLogsPayload,
  ConnectorRuntimeView,
  ConnectorStats,
} from "@/types/runtime";

/** 运行日志查询的缺省/下限条数（与后端 DefaultLogQueryLimit 一致）。 */
export const CONNECTOR_LOGS_DEFAULT_LIMIT = 50;
/** 运行日志查询的单次上限（与后端 MaxLogQueryLimit 一致）。 */
export const CONNECTOR_LOGS_MAX_LIMIT = 200;

/**
 * 运行日志载荷归一化：`entries` 结构缺失（非数组）→ 抛错（不降级成空列表）；
 * 单条无有效时间 → 只丢该条；stats 缺失/非法 → 零值（安全降级，不阻塞日志展示）。
 */
export function normalizeConnectorLogs(payload: unknown): ConnectorLogsPayload {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime connector logs payload is not an object");
  }
  if (!Array.isArray(record.entries)) {
    throw new Error("runtime connector logs payload is missing `entries`");
  }
  return {
    entries: record.entries.flatMap((item) => {
      const entry = normalizeLogEntry(item);
      return entry ? [entry] : [];
    }),
    stats: normalizeConnectorStats(record.stats),
  };
}

/**
 * 视图 runtime 快照归一化：缺失/非对象/无 stats → undefined（additive 字段，
 * 页面按「无运行数据」降级展示，而不是当作零计数）。
 */
export function normalizeConnectorRuntime(
  payload: unknown,
): ConnectorRuntimeView | undefined {
  const record = asRecord(payload);
  if (!record || asRecord(record.stats) === null) {
    return undefined;
  }
  return { stats: normalizeConnectorStats(record.stats) };
}

/** stats 归一化：计数取非负有限数，其余按 0；时间零值归一为空串。 */
export function normalizeConnectorStats(payload: unknown): ConnectorStats {
  const record = asRecord(payload);
  if (!record) {
    return emptyConnectorStats();
  }
  return {
    inbound: readCount(record.inbound),
    outbound: readCount(record.outbound),
    errors: readCount(record.errors),
    dedup_skipped: readCount(record.dedup_skipped),
    queue_full: readCount(record.queue_full),
    last_error: readString(record.last_error) || undefined,
    last_error_at: readTimestamp(record.last_error_at),
    last_activity_at: readTimestamp(record.last_activity_at),
  };
}

/** 日志查询 limit 归一：非有限数按缺省 50；否则取整并收敛到 [50,200]。 */
export function normalizeConnectorLogsLimit(limit?: number): number {
  if (typeof limit !== "number" || !Number.isFinite(limit)) {
    return CONNECTOR_LOGS_DEFAULT_LIMIT;
  }
  const truncated = Math.trunc(limit);
  if (truncated <= CONNECTOR_LOGS_DEFAULT_LIMIT) {
    return CONNECTOR_LOGS_DEFAULT_LIMIT;
  }
  if (truncated >= CONNECTOR_LOGS_MAX_LIMIT) {
    return CONNECTOR_LOGS_MAX_LIMIT;
  }
  return truncated;
}

function emptyConnectorStats(): ConnectorStats {
  return {
    inbound: 0,
    outbound: 0,
    errors: 0,
    dedup_skipped: 0,
    queue_full: 0,
    last_error: undefined,
    last_error_at: "",
    last_activity_at: "",
  };
}

function normalizeLogEntry(payload: unknown): ConnectorLogEntry | null {
  const record = asRecord(payload);
  if (!record) {
    return null;
  }
  const time = readTimestamp(record.time);
  if (!time) {
    // 无有效时间的条目无法参与「最新在前」排序，丢弃该条但不拖垮整页。
    return null;
  }
  return {
    time,
    // 未知级别/方向保留原样：渲染器按已知枚举映射，未知降级为中性展示。
    level: readString(record.level),
    direction: readString(record.direction),
    connector_id: readString(record.connector_id),
    chat: readString(record.chat) || undefined,
    summary: readString(record.summary),
  };
}

function readCount(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value > 0
    ? value
    : 0;
}

/** RFC3339 时间：非字符串/空串/Go 零值（0001-01-01T00:00:00Z）→ 空串。 */
function readTimestamp(value: unknown): string {
  const text = readString(value);
  return text && !text.startsWith("0001-01-01T00:00:00") ? text : "";
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function readString(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}
