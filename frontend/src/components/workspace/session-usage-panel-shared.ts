// 工作台右侧「会话用量」面板的纯逻辑：派生会话用量摘要、格式化与链接构造。
// 数据源：GET /api/runtime/analytics/sessions/{sessionId}（runtime.analytics.v1）。
// 面板只消费会话级 rollup（token/请求/轮次/覆盖率），明细仍由 /usage/sessions/:id 承载。

import type { AnalyticsSessionRollup, AnalyticsSessionUsageDetail, AnalyticsStepUsage } from "@/types/runtime";

export type SessionUsageSummary = {
  sessionId: string;
  title: string;
  provider: string;
  model: string;
  status: string;
  totalTokens: number;
  promptTokens: number;
  completionTokens: number;
  cachedTokens: number;
  reasoningTokens: number;
  llmRequests: number;
  turns: number;
  failedTurns: number;
  llmErrors: number;
  cacheHitRatio: number | null;
  averageResponseTimeMs: number | null;
  totalDurationMs: number | null;
  usageCoverage: number;
  usageQuality: string;
  partial: boolean;
  observedAt?: string;
};

function toFiniteNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function pickNumber(value: unknown): number {
  return toFiniteNumber(value) ?? 0;
}

function pickText(value: unknown, fallback = ""): string {
  return typeof value === "string" && value.trim() ? value : fallback;
}

function clampRatio(value: number): number {
  return Math.min(Math.max(value, 0), 1);
}

function resolveCacheHitRatio(
  cachedTokens: number,
  promptTokens: number,
  steps: AnalyticsStepUsage[],
): number | null {
  if (cachedTokens > 0 && promptTokens > 0) {
    return clampRatio(cachedTokens / promptTokens);
  }

  const ratios = steps
    .map((step) => toFiniteNumber(step?.cache_hit_ratio))
    .filter((ratio): ratio is number => ratio !== null);
  if (ratios.length === 0) {
    return null;
  }

  const average = ratios.reduce((total, ratio) => total + ratio, 0) / ratios.length;
  return clampRatio(average);
}

// resolveSessionUsageSummary 容忍后端字段缺失（例如代理返回 `200 {}`），
// 不抛错、不崩溃：缺 session 时返回 null，其余字段一律走兜底默认值。
export function resolveSessionUsageSummary(
  detail: AnalyticsSessionUsageDetail | null | undefined,
): SessionUsageSummary | null {
  const session = detail?.session as AnalyticsSessionRollup | undefined;
  if (!session || typeof session !== "object") {
    return null;
  }

  const steps = Array.isArray(detail?.steps) ? detail.steps : [];
  const totalTokens = pickNumber(session.total_tokens);
  const promptTokens = pickNumber(session.prompt_tokens);
  const completionTokens = pickNumber(session.completion_tokens);
  const cachedTokens = pickNumber(session.cached_tokens);

  return {
    sessionId: pickText(session.session_id),
    title: pickText(session.title),
    provider: pickText(session.provider),
    model: pickText(session.model),
    status: pickText(session.status),
    totalTokens,
    promptTokens,
    completionTokens,
    cachedTokens,
    reasoningTokens: pickNumber(session.reasoning_tokens),
    llmRequests: pickNumber(session.llm_requests) || pickNumber(session.total_requests),
    turns: pickNumber(session.turn_count),
    failedTurns: pickNumber(session.failed_turns),
    llmErrors: pickNumber(session.llm_errors),
    cacheHitRatio: resolveCacheHitRatio(cachedTokens, promptTokens, steps),
    averageResponseTimeMs: toFiniteNumber(session.average_response_time_ms),
    totalDurationMs: toFiniteNumber(session.total_duration_ms),
    usageCoverage: clampRatio(pickNumber(session.usage_coverage)),
    usageQuality: pickText(session.usage_quality),
    partial: session.partial === true,
    observedAt:
      pickText(session.last_observed_at) || pickText(session.end_time) || undefined,
  };
}

export function hasSessionUsageData(summary: SessionUsageSummary | null): boolean {
  if (!summary) {
    return false;
  }

  return (
    summary.totalTokens > 0 ||
    summary.promptTokens > 0 ||
    summary.completionTokens > 0 ||
    summary.llmRequests > 0 ||
    summary.turns > 0
  );
}

function trimDecimal(value: number): string {
  return value.toFixed(1).replace(/\.0$/, "");
}

export function formatUsageNumber(value?: number | null): string {
  const numeric = toFiniteNumber(value);
  if (numeric === null) {
    return "—";
  }

  const abs = Math.abs(numeric);
  if (abs >= 1_000_000_000) {
    return `${trimDecimal(numeric / 1_000_000_000)}B`;
  }
  if (abs >= 1_000_000) {
    return `${trimDecimal(numeric / 1_000_000)}M`;
  }
  if (abs >= 10_000) {
    return `${trimDecimal(numeric / 1_000)}k`;
  }
  return new Intl.NumberFormat("en-US").format(numeric);
}

export function formatUsageRate(value?: number | null): string {
  const numeric = toFiniteNumber(value);
  if (numeric === null) {
    return "—";
  }

  const clamped = clampRatio(numeric);
  return `${(clamped * 100).toFixed(clamped > 0 && clamped < 0.01 ? 2 : 1)}%`;
}

export function formatUsageDuration(value?: number | null): string {
  const milliseconds = toFiniteNumber(value);
  if (milliseconds === null) {
    return "—";
  }

  if (milliseconds < 1000) {
    return `${Math.round(milliseconds)} ms`;
  }
  if (milliseconds < 60_000) {
    return `${(milliseconds / 1000).toFixed(1)} s`;
  }
  if (milliseconds < 3_600_000) {
    return `${(milliseconds / 60_000).toFixed(1)} min`;
  }
  return `${(milliseconds / 3_600_000).toFixed(1)} h`;
}

export function formatUsageTimestamp(value?: string | null): string {
  if (!value) {
    return "—";
  }

  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export function sessionUsageHref(sessionId: string): string {
  const trimmed = sessionId?.trim();
  return trimmed ? `/usage/sessions/${encodeURIComponent(trimmed)}` : "/usage";
}

// 会话用量在每次 LLM 请求结束（后端 usageanalytics 订阅 llm_request_finished
// 后同步 upsert usage_requests/usage_sessions）与会话终态时落库。
//
// 但前端消费的是 /api/runtime/sessions/{id}/runtime/stream，它只投递会话事件
// 存储里被 shouldPersistRuntimeSessionEvent 白名单放行的事件；llm_request_*
// 不在白名单内，因此事件流里不存在任何名字带 turn 的事件。这里改为按「用量
// 一定已经变化」的可观测代理事件触发重取：
//   - assistant_message：一次 LLM 请求已完成 → 用量行已写入
//   - tool_started/tool_finished（含 tool.requested/tool.completed 别名）：
//     工具开始/结束必然发生在上一轮 LLM 请求完成之后
//   - session_end/session_interrupted：会话终态，汇总行落库
// 同时保留 turn* 前缀匹配，兼容 chat SSE 面（/api/agent/chat 的
// turn_start/turn_end）与未来若把 llm_request_* 纳入事件流的情形。
const usageRelevantRuntimeEventTypes = new Set([
  "assistant_message",
  "tool_started",
  "tool_finished",
  "tool.requested",
  "tool.completed",
  "session_end",
  "session_interrupted",
]);

export function isUsageRelevantRuntimeEvent(type?: string | null): boolean {
  if (typeof type !== "string") {
    return false;
  }

  const normalized = type.trim().toLowerCase();
  if (!normalized) {
    return false;
  }

  return (
    normalized.includes("turn") || usageRelevantRuntimeEventTypes.has(normalized)
  );
}

export function buildSessionUsageReloadKey(type?: string | null, count?: number): string {
  return isUsageRelevantRuntimeEvent(type) ? `${type}:${count ?? 0}` : "";
}
