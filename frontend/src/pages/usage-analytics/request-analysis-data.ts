import type {
  AnalyticsRequestSummary,
  CacheRequestRecord,
} from "@/types/runtime";

export const requestLedgerLimitOptions = [50, 100, 200].map((value) => ({
  value: String(value),
  label: String(value),
}));

export const defaultRequestLedgerLimit = 200;
export const requestTablePageSize = 25;

export type RequestOutcomeFilter = "" | "success" | "failure";
export type RequestCacheBand = "" | "low" | "high";
export type RequestDimension = "provider" | "model" | "cache_status";

export type RequestAnalysisFilters = {
  from: string;
  to: string;
  provider: string;
  model: string;
  q: string;
  directory: string;
  project: string;
  sessionStatus: string;
  outcome: RequestOutcomeFilter;
  /** 0–100 percentage values from URL; converted to 0–1 API ratios in the panel. */
  cacheMin: string;
  cacheMax: string;
  /** Legacy one-click band. Explicit min/max take precedence. */
  cacheBand: RequestCacheBand;
};

export type RequestBreakdownRow = {
  key: string;
  requests: number;
  failures: number;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
};

export type RequestAnalysisSummary = {
  requests: number;
  successes: number;
  failures: number;
  successRate: number;
  inputTokens: number;
  outputTokens: number;
  totalTokens: number;
  averageTokens: number;
  sessionCount: number;
  providerCount: number;
  modelCount: number;
  cacheReported: number;
  averageCacheHitRatio: number | null;
  cacheStatusDistribution: AnalyticsRequestSummary["cache_status_distribution"];
};

const reportedCacheStatuses = new Set([
  "hit",
  "write",
  "confirmed_miss",
  "reported_zero",
]);

export function clampRequestLedgerLimit(raw: string | null | undefined): number {
  const parsed = Number.parseInt(raw ?? "", 10);
  return requestLedgerLimitOptions.some((option) => Number(option.value) === parsed)
    ? parsed
    : defaultRequestLedgerLimit;
}

export function normalizeOutcome(value: string | null): RequestOutcomeFilter {
  return value === "success" || value === "failure" ? value : "";
}

export function normalizeCacheBand(value: string | null): RequestCacheBand {
  return value === "low" || value === "high" ? value : "";
}

export function parseCachePercent(value: string | null | undefined): number | undefined {
  const parsed = Number.parseFloat(value?.trim() ?? "");
  return Number.isFinite(parsed) && parsed >= 0 && parsed <= 100
    ? parsed
    : undefined;
}

export function normalizeCachePercentInput(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) return "";
  const parsed = Number.parseFloat(trimmed);
  if (!Number.isFinite(parsed)) return "";
  return String(Math.min(100, Math.max(0, parsed)));
}

export function localDateValue(date = new Date()): string {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

export function requestSessionID(record: CacheRequestRecord): string {
  return record.session_id?.trim() ?? "";
}

export function requestProvider(record: CacheRequestRecord): string {
  return record.provider?.trim() ?? "";
}

export function requestModel(record: CacheRequestRecord): string {
  return record.model?.trim() ?? "";
}

export function requestRecordSuccess(record: CacheRequestRecord): boolean {
  return record.status.toLowerCase() === "success";
}

export function requestHasReportedCache(record: CacheRequestRecord): boolean {
  return reportedCacheStatuses.has(record.cache_status.toLowerCase());
}

export function requestCacheRatio(record: CacheRequestRecord): number | null {
  if (!requestHasReportedCache(record)) {
    return null;
  }
  return typeof record.cache_hit_ratio === "number" &&
    Number.isFinite(record.cache_hit_ratio)
    ? record.cache_hit_ratio
    : null;
}

export function requestInputTokens(record: CacheRequestRecord): number {
  return record.usage?.prompt_tokens ?? 0;
}

export function requestOutputTokens(record: CacheRequestRecord): number {
  return record.usage?.completion_tokens ?? 0;
}

export function requestTotalTokens(record: CacheRequestRecord): number {
  return record.usage?.total_tokens ?? 0;
}

function emptyDistribution(): AnalyticsRequestSummary["cache_status_distribution"] {
  return {
    hit: 0,
    write: 0,
    confirmed_miss: 0,
    reported_zero: 0,
    not_reported: 0,
    error: 0,
  };
}

export function summarizeRequestRecords(
  records: readonly CacheRequestRecord[],
): RequestAnalysisSummary {
  let successes = 0;
  let inputTokens = 0;
  let outputTokens = 0;
  let totalTokens = 0;
  let cacheReported = 0;
  let cacheRatioTotal = 0;
  const sessions = new Set<string>();
  const providers = new Set<string>();
  const models = new Set<string>();
  const cacheStatusDistribution = emptyDistribution();

  for (const record of records) {
    if (requestRecordSuccess(record)) {
      successes += 1;
    }
    inputTokens += requestInputTokens(record);
    outputTokens += requestOutputTokens(record);
    totalTokens += requestTotalTokens(record);
    const session = requestSessionID(record);
    if (session) {
      sessions.add(session);
    }
    providers.add(requestProvider(record));
    models.add(requestModel(record));
    const cacheStatus = record.cache_status.toLowerCase();
    if (cacheStatus in cacheStatusDistribution) {
      cacheStatusDistribution[cacheStatus as keyof typeof cacheStatusDistribution] += 1;
    }
    const ratio = requestCacheRatio(record);
    if (ratio !== null) {
      cacheReported += 1;
      cacheRatioTotal += ratio;
    }
  }

  const requests = records.length;
  return {
    requests,
    successes,
    failures: requests - successes,
    successRate: requests > 0 ? successes / requests : 0,
    inputTokens,
    outputTokens,
    totalTokens,
    averageTokens: requests > 0 ? Math.round(totalTokens / requests) : 0,
    sessionCount: sessions.size,
    providerCount: providers.size,
    modelCount: models.size,
    cacheReported,
    averageCacheHitRatio:
      cacheReported > 0 ? cacheRatioTotal / cacheReported : null,
    cacheStatusDistribution,
  };
}

export function requestSummaryFromAnalytics(
  summary: AnalyticsRequestSummary | null | undefined,
  records: readonly CacheRequestRecord[],
): RequestAnalysisSummary {
  if (!summary || !Number.isFinite(summary.requests)) {
    return summarizeRequestRecords(records);
  }
  return {
    requests: summary.requests,
    successes: summary.successes,
    failures: summary.failures,
    successRate:
      summary.requests > 0 ? summary.successes / summary.requests : 0,
    inputTokens: summary.input_tokens,
    outputTokens: summary.output_tokens,
    totalTokens: summary.total_tokens,
    averageTokens:
      summary.requests > 0
        ? Math.round(summary.total_tokens / summary.requests)
        : 0,
    sessionCount: summary.sessions,
    providerCount: summary.providers,
    modelCount: summary.models,
    cacheReported: summary.cache_reported,
    averageCacheHitRatio:
      typeof summary.average_cache_hit_ratio === "number"
        ? summary.average_cache_hit_ratio
        : null,
    cacheStatusDistribution:
      summary.cache_status_distribution ?? emptyDistribution(),
  };
}

function requestDimensionKey(
  record: CacheRequestRecord,
  dimension: RequestDimension,
): string {
  switch (dimension) {
    case "provider":
      return requestProvider(record);
    case "model":
      return requestModel(record);
    default:
      return record.cache_status.toLowerCase();
  }
}

export function buildRequestBreakdown(
  records: readonly CacheRequestRecord[],
  dimension: RequestDimension,
): RequestBreakdownRow[] {
  const groups = new Map<string, RequestBreakdownRow>();
  for (const record of records) {
    const key = requestDimensionKey(record, dimension);
    const current = groups.get(key) ?? {
      key,
      requests: 0,
      failures: 0,
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 0,
    };
    current.requests += 1;
    current.failures += requestRecordSuccess(record) ? 0 : 1;
    current.input_tokens += requestInputTokens(record);
    current.output_tokens += requestOutputTokens(record);
    current.total_tokens += requestTotalTokens(record);
    groups.set(key, current);
  }

  return [...groups.values()].sort(
    (left, right) =>
      right.requests - left.requests ||
      right.total_tokens - left.total_tokens ||
      left.key.localeCompare(right.key),
  );
}

export function buildCacheStatusBreakdown(
  summary: RequestAnalysisSummary,
): RequestBreakdownRow[] {
  const distribution = summary.cacheStatusDistribution;
  return (Object.entries(distribution) as Array<
    [keyof typeof distribution, number]
  >)
    .filter(([, count]) => count > 0)
    .map(([key, count]) => ({
      key,
      requests: count,
      failures: 0,
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 0,
    }))
    .sort((left, right) => right.requests - left.requests);
}
