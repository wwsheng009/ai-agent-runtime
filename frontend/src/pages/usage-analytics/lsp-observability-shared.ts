// LSP 面板的纯展示逻辑（与 React 解耦，便于单测）：
//   * 只做"事实整理"——把观测面事件按会话/服务器/结果分组、计数、取最新；
//   * 不计算比率类指标（覆盖率/fallback 等口径单源于 §3.3，见 api/runtime/lsp.ts 注释）。

import type { LspObserveEvent } from "@/api/runtime/lsp";

export const LSP_REQUEST_EVENT = "lsp.request.finished";
export const LSP_SERVER_STATE_EVENT = "lsp.server.state";

export type LspServerStateRow = {
  key: string;
  sessionId: string;
  server: string;
  state: string;
  reason: string;
  reasonCategory: string;
  firstPublishMs: number | null;
  at: string;
  observationSeq: number;
};

export type LspCountRow = {
  key: string;
  count: number;
};

function asRecord(value: unknown): Record<string, unknown> | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return null;
  }
  return value as Record<string, unknown>;
}

export function eventPayload(event: LspObserveEvent): Record<string, unknown> {
  return asRecord(event.payload) ?? {};
}

export function eventSessionId(event: LspObserveEvent): string {
  const correlation = asRecord(event.correlation) ?? {};
  const candidates = [
    correlation.session_id,
    correlation.SessionID,
    eventPayload(event).session_id,
  ];
  for (const value of candidates) {
    if (typeof value === "string" && value.trim()) {
      return value.trim();
    }
  }
  return "";
}

export function eventString(event: LspObserveEvent, key: string): string {
  const value = eventPayload(event)[key];
  return typeof value === "string" ? value.trim() : "";
}

export function eventNumber(event: LspObserveEvent, key: string): number | null {
  const value = eventPayload(event)[key];
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  if (typeof value === "string" && value.trim()) {
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : null;
  }
  return null;
}

/** 每个（会话, 服务器）取最新一条 `lsp.server.state`（事件已按 seq 倒序）。 */
export function latestServerStates(events: LspObserveEvent[]): LspServerStateRow[] {
  const rows = new Map<string, LspServerStateRow>();
  for (const event of events) {
    if (event.type !== LSP_SERVER_STATE_EVENT) {
      continue;
    }
    const sessionId = eventSessionId(event);
    const server = eventString(event, "server") || eventString(event, "name");
    if (!server) {
      continue;
    }
    const key = `${sessionId}::${server}`;
    if (rows.has(key)) {
      continue;
    }
    rows.set(key, {
      key,
      sessionId,
      server,
      state: eventString(event, "state"),
      reason: eventString(event, "reason"),
      reasonCategory: eventString(event, "reason_category"),
      firstPublishMs: eventNumber(event, "first_publish_ms"),
      at: event.timestamp,
      observationSeq: event.observation_seq,
    });
  }
  return [...rows.values()];
}

/** 窗口内请求计数（按 outcome 与 reason_category 两个维度，均为纯计数事实）。 */
export function requestOutcomeCounts(events: LspObserveEvent[]): LspCountRow[] {
  return countBy(events, LSP_REQUEST_EVENT, (event) => eventString(event, "outcome"));
}

export function requestReasonCounts(events: LspObserveEvent[]): LspCountRow[] {
  return countBy(events, LSP_REQUEST_EVENT, (event) => eventString(event, "reason_category"));
}

export function coldFastFailCount(events: LspObserveEvent[]): number {
  let count = 0;
  for (const event of events) {
    if (event.type === LSP_REQUEST_EVENT && eventPayload(event).cold_fast_fail === true) {
      count += 1;
    }
  }
  return count;
}

function countBy(
  events: LspObserveEvent[],
  eventType: string,
  pick: (event: LspObserveEvent) => string,
): LspCountRow[] {
  const counts = new Map<string, number>();
  for (const event of events) {
    if (event.type !== eventType) {
      continue;
    }
    const key = pick(event);
    if (!key) {
      continue;
    }
    counts.set(key, (counts.get(key) ?? 0) + 1);
  }
  return [...counts.entries()]
    .map(([key, count]) => ({ key, count }))
    .sort((a, b) => (b.count - a.count) || a.key.localeCompare(b.key));
}

/** 最近 N 条（事件已倒序；仅做截断，不改顺序）。 */
export function recentLspEvents(events: LspObserveEvent[], limit: number): LspObserveEvent[] {
  if (limit <= 0) {
    return [];
  }
  return events.slice(0, limit);
}

/** 事件行的短类型标签（去掉 `lsp.` 前缀，供表格紧凑展示）。 */
export function shortEventType(type: string): string {
  return type.startsWith("lsp.") ? type.slice(4) : type;
}

/** 事件行取值：请求看 outcome，状态事件退回 state；缺失给 "—"。 */
export function eventOutcomeLabel(event: LspObserveEvent): string {
  return eventString(event, "outcome") || eventString(event, "state") || "—";
}

export function eventDurationLabel(event: LspObserveEvent): string {
  const value = eventNumber(event, "duration_ms");
  return value === null ? "—" : `${value} ms`;
}

export function eventSessionLabel(event: LspObserveEvent): string {
  return eventSessionId(event) || "—";
}
