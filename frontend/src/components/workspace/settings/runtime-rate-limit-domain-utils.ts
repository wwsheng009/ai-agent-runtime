import { isConfigRecord } from "./runtime-provider-config-utils";

const KNOWN_API_KEY_LIMIT_KEYS = new Set([
  "api_key_pattern",
  "qps",
  "qpd",
  "qpm",
  "block_duration",
]);

const KNOWN_PATH_LIMIT_KEYS = new Set(["requests_per_minute", "burst"]);

export type RuntimeRateLimitConfigSummary = {
  algorithm: string;
  defaultDailyTokens: string;
  defaultMonthlyTokens: string;
  defaultQps: string;
  defaultTpm: string;
  enabled: boolean;
  globalDailyTokens: string;
  globalMonthlyTokens: string;
  globalQps: string;
  globalTpm: string;
  storage: string;
};

export type RuntimeRateLimitApiKeyLimitSummary = {
  apiKeyPattern: string;
  blockDuration: string;
  extraFieldCount: number;
  id: string;
  index: number;
  qpd: string;
  qpm: string;
  qps: string;
  raw: Record<string, unknown>;
};

export type RuntimeRateLimitPathLimitSummary = {
  burst: string;
  extraFieldCount: number;
  path: string;
  raw: Record<string, unknown>;
  requestsPerMinute: string;
};

export function getRuntimeRateLimitConfig(
  value: unknown,
): RuntimeRateLimitConfigSummary {
  const rateLimitRoot = getRateLimitRoot(value);
  const defaultLimits = isConfigRecord(rateLimitRoot?.default_limits)
    ? rateLimitRoot.default_limits
    : {};
  const globalLimits = isConfigRecord(rateLimitRoot?.global_limits)
    ? rateLimitRoot.global_limits
    : {};

  return {
    enabled: Boolean(rateLimitRoot?.enabled),
    storage: readText(rateLimitRoot?.storage),
    algorithm: readText(rateLimitRoot?.algorithm),
    defaultQps: readText(defaultLimits.qps),
    defaultTpm: readText(defaultLimits.tpm),
    defaultDailyTokens: readText(defaultLimits.daily_tokens),
    defaultMonthlyTokens: readText(defaultLimits.monthly_tokens),
    globalQps: readText(globalLimits.global_qps),
    globalTpm: readText(globalLimits.global_tpm),
    globalDailyTokens: readText(globalLimits.global_daily_tokens),
    globalMonthlyTokens: readText(globalLimits.global_monthly_tokens),
  };
}

export function listRuntimeRateLimitApiKeyLimits(
  value: unknown,
): RuntimeRateLimitApiKeyLimitSummary[] {
  const rateLimitRoot = getRateLimitRoot(value);
  const apiKeyLimits = Array.isArray(rateLimitRoot?.api_key_limits)
    ? rateLimitRoot.api_key_limits
    : [];

  return apiKeyLimits.flatMap((item, index) =>
    isConfigRecord(item) ? [buildApiKeyLimitSummary(item, index)] : [],
  );
}

export function listRuntimeRateLimitPathLimits(
  value: unknown,
): RuntimeRateLimitPathLimitSummary[] {
  const rateLimitRoot = getRateLimitRoot(value);
  const pathLimits = isConfigRecord(rateLimitRoot?.path_limits)
    ? rateLimitRoot.path_limits
    : {};

  return Object.entries(pathLimits)
    .sort(([left], [right]) => left.localeCompare(right))
    .flatMap(([path, limit]) =>
      isConfigRecord(limit) ? [buildPathLimitSummary(path, limit)] : [],
    );
}

export function createDefaultRateLimitApiKeyLimit() {
  return {
    api_key_pattern: "",
    qps: 10,
    qpd: 100000,
    qpm: 600,
    block_duration: "60s",
  };
}

export function createDefaultRateLimitPathLimit() {
  return {
    requests_per_minute: 60,
    burst: 10,
  };
}

function getRateLimitRoot(value: unknown) {
  if (!isConfigRecord(value)) {
    return null;
  }
  const rateLimit = value.rate_limit;
  return isConfigRecord(rateLimit) ? rateLimit : null;
}

function buildApiKeyLimitSummary(
  raw: Record<string, unknown>,
  index: number,
): RuntimeRateLimitApiKeyLimitSummary {
  return {
    id: `${index}:${readText(raw.api_key_pattern)}`,
    index,
    raw,
    apiKeyPattern: readText(raw.api_key_pattern),
    qps: readText(raw.qps),
    qpd: readText(raw.qpd),
    qpm: readText(raw.qpm),
    blockDuration: readText(raw.block_duration),
    extraFieldCount: Object.keys(raw).filter(
      (key) => !KNOWN_API_KEY_LIMIT_KEYS.has(key),
    ).length,
  };
}

function buildPathLimitSummary(
  path: string,
  raw: Record<string, unknown>,
): RuntimeRateLimitPathLimitSummary {
  return {
    path,
    raw,
    requestsPerMinute: readText(raw.requests_per_minute),
    burst: readText(raw.burst),
    extraFieldCount: Object.keys(raw).filter(
      (key) => !KNOWN_PATH_LIMIT_KEYS.has(key),
    ).length,
  };
}

function readText(value: unknown) {
  if (typeof value === "string") {
    return value;
  }
  if (typeof value === "number") {
    return String(value);
  }
  return "";
}
