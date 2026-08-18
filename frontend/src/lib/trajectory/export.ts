/**
 * 轨迹导出 JSONL（P3-2）：EventStore 的 chat.sse.* 事件 → 下载文件。
 *
 * 行格式：`{seq, ts, kind, payload}`（payload 不含游标字段 seq）。
 * 复用后端 ListEvents 契约：事件 payload 带注入的持久化 seq。
 */
import type { SessionRuntimeEvent } from "@/types/runtime";

import { CHAT_SSE_EVENT_PREFIX } from "./recovery";

export type TrajectoryExportEntry = {
  seq: number;
  ts: string;
  kind: string;
  payload: Record<string, unknown>;
};

/** 脱敏标记：替换敏感字段值（保留字段名与结构，便于分析脚本继续使用）。 */
export const REDACT_MARKER = "<redacted>";

/**
 * 掩码敏感值：保留对象/数组结构（键名不泄露值），叶子值替换为标记。
 * 例如 {query: "paris"} → {query: "<redacted>"}；字符串 JSON 整体替换。
 */
function redactSensitiveValue(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map((item) => redactSensitiveValue(item));
  }
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [key, item] of Object.entries(value)) {
      out[key] = redactSensitiveValue(item);
    }
    return out;
  }
  return REDACT_MARKER;
}

/**
 * 递归脱敏工具容器（tool/tool_call/delta/tool_calls/tool_events 元素）：
 * id/name/status 等身份字段保留，args/arguments/content 值替换为标记。
 */
function redactToolContainer(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map((item) => redactToolContainer(item));
  }
  if (!value || typeof value !== "object") {
    return value;
  }
  const out: Record<string, unknown> = {};
  for (const [key, item] of Object.entries(value)) {
    if (key === "args" || key === "arguments") {
      out[key] = redactSensitiveValue(item);
    } else if (key === "content") {
      out[key] = REDACT_MARKER;
    } else {
      out[key] = redactToolContainer(item);
    }
  }
  return out;
}

/**
 * 递归脱敏 result 容器（done/result 事件）：
 * tool_events/tool_calls 走工具容器脱敏；正文/用量/编排等元信息保留。
 */
function redactResultContainer(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map((item) => redactResultContainer(item));
  }
  if (!value || typeof value !== "object") {
    return value;
  }
  const out: Record<string, unknown> = {};
  for (const [key, item] of Object.entries(value)) {
    if (key === "tool_events" || key === "tool_calls") {
      out[key] = Array.isArray(item)
        ? item.map((entry) => redactToolContainer(entry))
        : redactToolContainer(item);
    } else if (
      key === "output" ||
      key === "reasoning" ||
      key === "usage" ||
      key === "orchestration" ||
      key === "planning" ||
      key === "model" ||
      key === "source" ||
      key === "success" ||
      key === "kind" ||
      key === "tool_call_count"
    ) {
      out[key] = item;
    } else {
      out[key] = redactResultContainer(item);
    }
  }
  return out;
}

/**
 * 导出脱敏（R4）：替换事件载荷中的工具参数与工具输出，
 * 保留结构/身份字段/正文/用量，使导出的日志可安全分享且分析脚本可用。
 */
export function redactExportPayload(
  payload: Record<string, unknown>,
): Record<string, unknown> {
  const out = { ...payload };
  for (const key of ["tool", "tool_call", "delta"]) {
    if (key in out) {
      out[key] = redactToolContainer(out[key]);
    }
  }
  for (const key of ["tool_events", "tool_calls"]) {
    if (key in out) {
      out[key] = Array.isArray(out[key])
        ? out[key].map((entry) => redactToolContainer(entry))
        : redactToolContainer(out[key]);
    }
  }
  if ("result" in out) {
    out.result = redactResultContainer(out.result);
  }
  return out;
}

/** chat SSE 事件 → 导出行（非轨迹事件返回 null；seq 取 payload.seq）。 */
export function chatSseEventToExportEntry(
  event: SessionRuntimeEvent,
): TrajectoryExportEntry | null {
  if (!event.type.startsWith(CHAT_SSE_EVENT_PREFIX)) {
    return null;
  }
  const payload = { ...(event.payload ?? {}) };
  const rawSeq = payload.seq;
  const seq =
    typeof rawSeq === "number" && Number.isFinite(rawSeq) && rawSeq > 0
      ? Math.floor(rawSeq)
      : 0;
  delete payload.seq;
  return {
    seq,
    ts: event.timestamp,
    kind: event.type.slice(CHAT_SSE_EVENT_PREFIX.length),
    payload,
  };
}

/** 事件列表 → JSONL 文本（按 seq 升序，seq=0 排末尾）。 */
export function eventsToTrajectoryJsonl(
  events: SessionRuntimeEvent[],
  options?: { redact?: boolean },
): string {
  const entries: TrajectoryExportEntry[] = [];
  const zeroSeq: TrajectoryExportEntry[] = [];
  for (const event of events) {
    const entry = chatSseEventToExportEntry(event);
    if (!entry) {
      continue;
    }
    if (entry.seq > 0) {
      entries.push(entry);
    } else {
      zeroSeq.push(entry);
    }
  }
  if (options?.redact) {
    for (const entry of [...entries, ...zeroSeq]) {
      entry.payload = redactExportPayload(entry.payload);
    }
  }
  entries.sort((left, right) => left.seq - right.seq);
  const ordered = [...entries, ...zeroSeq];
  return ordered.map((entry) => JSON.stringify(entry)).join("\n");
}

/** 导出文件名（含会话与时间戳；脱敏导出带 -redacted 后缀）。 */
export function buildTrajectoryExportFilename(
  sessionId: string,
  now = new Date(),
  redacted = false,
): string {
  const stamp = now.toISOString().replace(/[:.]/g, "-").slice(0, 19);
  return `trajectory-${sessionId}${redacted ? "-redacted" : ""}-${stamp}.jsonl`;
}

/** 浏览器端触发下载。 */
export function downloadTrajectoryJsonl(jsonl: string, filename: string) {
  const blob = new Blob([jsonl], { type: "application/x-ndjson" });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}
