// 由 pages/usage-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import type { AnalyticsDimensionsResponse, AnalyticsGroupBy } from "@/types/runtime";

export const adminTokenStorageKey = "runtime.logs.adminToken";

export const analyticsFilterKeys = ["from", "to", "q", "provider", "model", "directory", "project", "status"] as const;

export function readAdminToken() {
  return typeof window === "undefined"
    ? ""
    : window.localStorage.getItem(adminTokenStorageKey)?.trim() ?? "";
}

export function formatNumber(value?: number | null) {
  return new Intl.NumberFormat().format(
    typeof value === "number" && Number.isFinite(value) ? value : 0,
  );
}

export function formatPercent(value?: number | null) {
  const normalized = typeof value === "number" && Number.isFinite(value) ? value : 0;
  return `${(normalized * 100).toFixed(normalized > 0 && normalized < 0.01 ? 2 : 1)}%`;
}

export function formatDuration(value?: number | null) {
  const milliseconds = typeof value === "number" && Number.isFinite(value) ? value : 0;
  if (milliseconds < 1000) return `${milliseconds} ms`;
  if (milliseconds < 60_000) return `${(milliseconds / 1000).toFixed(1)} s`;
  if (milliseconds < 3_600_000) return `${(milliseconds / 60_000).toFixed(1)} min`;
  return `${(milliseconds / 3_600_000).toFixed(1)} h`;
}

export function formatTimestamp(value?: string) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export function shortID(value: string) {
  return value.length <= 28 ? value : `${value.slice(0, 18)}...${value.slice(-6)}`;
}

export function errorRate(errors: number, total: number) {
  return total > 0 ? errors / total : 0;
}

function dimensionValues(values: unknown): string[] {
  return Array.isArray(values)
    ? values.filter((value): value is string => typeof value === "string")
    : [];
}

// normalizeDimensions 兜底后端字段缺失/类型异常（例如代理指向未实现 analytics 路由的
// 桩服务时返回 `200 {}`），保证过滤控件永远拿到数组，避免渲染期崩溃。
export function normalizeDimensions(raw: unknown): AnalyticsDimensionsResponse {
  const source = (raw ?? {}) as Partial<AnalyticsDimensionsResponse>;
  return {
    schema_version: typeof source.schema_version === "string"
      ? source.schema_version
      : "runtime.analytics.v1",
    generated_at: typeof source.generated_at === "string" ? source.generated_at : "",
    providers: dimensionValues(source.providers),
    models: dimensionValues(source.models),
    directories: dimensionValues(source.directories),
    projects: dimensionValues(source.projects),
    statuses: dimensionValues(source.statuses),
  };
}

export function dimensionOptions(values: readonly string[] | null | undefined, current: string, allLabel: string) {
  const list = dimensionValues(values);
  const available = current && !list.some((value) => value === current)
    ? [current, ...list]
    : list;
  return [
    { value: "", label: allLabel },
    ...available.map((value) => ({ value, label: value })),
  ];
}

export function formatDimensionTick(value: string, groupBy: AnalyticsGroupBy) {
  const normalized = groupBy === "project" ? value.split(/[\\/]/).filter(Boolean).at(-1) ?? value : value;
  return normalized.length > 16 ? `${normalized.slice(0, 14)}...` : normalized;
}

export function qualityKey(quality: string): "qualityLabels.providerReported" | "qualityLabels.localEstimate" | "qualityLabels.mixed" | "qualityLabels.summaryOnly" | "qualityLabels.missing" | "qualityLabels.reported" {
  switch (quality) {
    case "provider_reported": return "qualityLabels.providerReported";
    case "local_estimate": return "qualityLabels.localEstimate";
    case "mixed": return "qualityLabels.mixed";
    case "summary_only": return "qualityLabels.summaryOnly";
    case "missing": return "qualityLabels.missing";
    default: return "qualityLabels.reported";
  }
}

export function titleSourceKey(source?: string): "sessions.titleSources.chatLog" | "sessions.titleSources.sessionHistory" | "sessions.titleSources.initialMessage" | "sessions.titleSources.unknown" {
  switch (source) {
    case "chat_log": return "sessions.titleSources.chatLog";
    case "session_history_id":
    case "session_history_time_match": return "sessions.titleSources.sessionHistory";
    case "initial_message": return "sessions.titleSources.initialMessage";
    default: return "sessions.titleSources.unknown";
  }
}

export function statusTone(status?: string) {
  switch ((status ?? "").toLowerCase()) {
    case "completed": return "border-[var(--analytics-success-border)] bg-[var(--analytics-success-soft)] text-[var(--analytics-success)]";
    case "failed": return "border-[var(--analytics-danger-border)] bg-[var(--analytics-danger-soft)] text-[var(--analytics-danger)]";
    case "interrupted": return "border-[var(--analytics-warning-border)] bg-[var(--analytics-warning-soft)] text-[var(--analytics-warning)]";
    default: return "";
  }
}

export function partialReasonKey(reason: string): "partialReasons.requestHistory" | "partialReasons.usageMissing" | "partialReasons.messagesDropped" | "partialReasons.reconciliation" | "partialReasons.unknown" {
  switch (reason) {
    case "llm_request_history_incomplete": return "partialReasons.requestHistory";
    case "usage_missing": return "partialReasons.usageMissing";
    case "chat_messages_dropped": return "partialReasons.messagesDropped";
    case "usage_reconciliation_mismatch": return "partialReasons.reconciliation";
    default: return "partialReasons.unknown";
  }
}

export function reconciliationKey(status: string): "reconciliation.matched" | "reconciliation.partial" | "reconciliation.mismatch" | "reconciliation.unavailable" {
  switch (status) {
    case "matched": return "reconciliation.matched";
    case "partial": return "reconciliation.partial";
    case "mismatch": return "reconciliation.mismatch";
    default: return "reconciliation.unavailable";
  }
}

export function diagnosticTitleKey(code: string): "diagnostics.codes.llmFailures.title" | "diagnostics.codes.failedTurns.title" | "diagnostics.codes.toolErrors.title" | "diagnostics.codes.contextPressure.title" | "diagnostics.codes.reconciliation.title" | "diagnostics.codes.partial.title" {
  switch (code) {
    case "llm_failures": return "diagnostics.codes.llmFailures.title";
    case "failed_turns": return "diagnostics.codes.failedTurns.title";
    case "tool_errors_observed": return "diagnostics.codes.toolErrors.title";
    case "context_pressure": return "diagnostics.codes.contextPressure.title";
    case "usage_reconciliation_mismatch": return "diagnostics.codes.reconciliation.title";
    default: return "diagnostics.codes.partial.title";
  }
}

export function diagnosticDetailKey(code: string): "diagnostics.codes.llmFailures.detail" | "diagnostics.codes.failedTurns.detail" | "diagnostics.codes.toolErrors.detail" | "diagnostics.codes.contextPressure.detail" | "diagnostics.codes.reconciliation.detail" | "diagnostics.codes.partial.detail" {
  return diagnosticTitleKey(code).replace(".title", ".detail") as ReturnType<typeof diagnosticDetailKey>;
}

export function errorCategoryKey(category?: string): "errors.cancelled" | "errors.modelNotFound" | "errors.authentication" | "errors.rateLimit" | "errors.upstream" | "errors.timeout" | "errors.network" | "errors.contextLimit" | "errors.invalidRequest" | "errors.unknown" {
  switch (category) {
    case "cancelled": return "errors.cancelled";
    case "model_not_found": return "errors.modelNotFound";
    case "authentication": return "errors.authentication";
    case "rate_limit": return "errors.rateLimit";
    case "upstream_unavailable": return "errors.upstream";
    case "timeout": return "errors.timeout";
    case "network": return "errors.network";
    case "context_limit": return "errors.contextLimit";
    case "invalid_request": return "errors.invalidRequest";
    default: return "errors.unknown";
  }
}

export function outcomeKey(outcome: string): "outcomes.success" | "outcomes.recovered" | "outcomes.failed" | "outcomes.cancelled" | "outcomes.unknown" {
  switch (outcome) {
    case "success": return "outcomes.success";
    case "recovered": return "outcomes.recovered";
    case "failed": return "outcomes.failed";
    case "cancelled": return "outcomes.cancelled";
    default: return "outcomes.unknown";
  }
}

export function outcomeTone(outcome: string) {
  switch (outcome) {
    case "success": return "border-[var(--analytics-success-border)] bg-[var(--analytics-success-soft)] text-[var(--analytics-success)]";
    case "recovered": return "border-[var(--analytics-warning-border)] bg-[var(--analytics-warning-soft)] text-[var(--analytics-warning)]";
    case "failed": return "border-[var(--analytics-danger-border)] bg-[var(--analytics-danger-soft)] text-[var(--analytics-danger)]";
    case "cancelled": return "border-[var(--analytics-info-border)] bg-[var(--analytics-info-soft)] text-[var(--analytics-info)]";
    default: return "";
  }
}
