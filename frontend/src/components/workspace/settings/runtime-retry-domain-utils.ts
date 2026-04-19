import {
  isConfigRecord,
  normalizeStringArrayInput,
} from "./runtime-provider-config-utils";

const KNOWN_RETRY_RULE_KEYS = new Set([
  "name",
  "description",
  "enabled",
  "max_retries",
  "retry_delay_ms",
  "backoff_multiplier",
  "error_code",
  "keyword",
  "status_code",
]);

export type RuntimeRetryConfigSummary = {
  defaultBackoffMultiplier: string;
  defaultMaxRetries: string;
  defaultRetryDelayMs: string;
  enabled: boolean;
  enhancedStrategyEnabled: boolean;
  enhancedStrategyFallbackThreshold: string;
  enhancedStrategyPrimaryMinScore: string;
  enhancedStrategySecondaryExcludedScore: string;
  enhancedStrategySecondaryThreshold: string;
  invalidEncryptedContentStripClientStateOnce: boolean;
};

export type RuntimeRetryRuleSummary = {
  backoffMultiplier: string;
  description: string;
  enabled: boolean;
  errorCodeCodesText: string;
  errorCodePattern: string;
  extraFieldCount: number;
  id: string;
  index: number;
  keywordCaseSensitive: boolean;
  keywordPatternsText: string;
  keywordValuesText: string;
  maxRetries: string;
  name: string;
  raw: Record<string, unknown>;
  retryDelayMs: string;
  statusCodeRange: string;
};

export function getRuntimeRetryConfig(value: unknown): RuntimeRetryConfigSummary {
  const retryRoot = getRetryRoot(value) ?? {};
  const recovery = isConfigRecord(retryRoot.invalid_encrypted_content_recovery)
    ? retryRoot.invalid_encrypted_content_recovery
    : {};
  const enhancedStrategy = isConfigRecord(retryRoot.enhanced_strategy)
    ? retryRoot.enhanced_strategy
    : {};

  return {
    enabled: Boolean(retryRoot.enabled),
    defaultMaxRetries: readText(retryRoot.default_max_retries),
    defaultRetryDelayMs: readText(retryRoot.default_retry_delay_ms),
    defaultBackoffMultiplier: readText(retryRoot.default_backoff_multiplier),
    invalidEncryptedContentStripClientStateOnce: Boolean(
      recovery.strip_client_state_once,
    ),
    enhancedStrategyEnabled: Boolean(enhancedStrategy.enabled),
    enhancedStrategySecondaryThreshold: readText(
      enhancedStrategy.secondary_threshold,
    ),
    enhancedStrategyFallbackThreshold: readText(
      enhancedStrategy.fallback_threshold,
    ),
    enhancedStrategyPrimaryMinScore: readText(
      enhancedStrategy.primary_min_score,
    ),
    enhancedStrategySecondaryExcludedScore: readText(
      enhancedStrategy.secondary_excluded_score,
    ),
  };
}

export function listRuntimeRetryRules(value: unknown): RuntimeRetryRuleSummary[] {
  const retryRoot = getRetryRoot(value);
  const rules = Array.isArray(retryRoot?.rules) ? retryRoot.rules : [];

  return rules.flatMap((rule, index) =>
    isConfigRecord(rule) ? [buildRetryRuleSummary(rule, index)] : [],
  );
}

export function normalizeRetryMatchList(value: string) {
  return normalizeStringArrayInput(value);
}

function getRetryRoot(value: unknown) {
  if (!isConfigRecord(value)) {
    return null;
  }
  return isConfigRecord(value.retry) ? value.retry : null;
}

function buildRetryRuleSummary(
  raw: Record<string, unknown>,
  index: number,
): RuntimeRetryRuleSummary {
  const errorCode = isConfigRecord(raw.error_code) ? raw.error_code : {};
  const keyword = isConfigRecord(raw.keyword) ? raw.keyword : {};
  const statusCode = isConfigRecord(raw.status_code) ? raw.status_code : {};

  return {
    id: `${index}:${readText(raw.name)}`,
    index,
    raw,
    name: readText(raw.name),
    description: readText(raw.description),
    enabled: raw.enabled !== false,
    maxRetries: readText(raw.max_retries),
    retryDelayMs: readText(raw.retry_delay_ms),
    backoffMultiplier: readText(raw.backoff_multiplier),
    errorCodeCodesText: readTextArray(errorCode.codes).join("\n"),
    errorCodePattern: readText(errorCode.pattern),
    keywordValuesText: readTextArray(keyword.values).join("\n"),
    keywordPatternsText: readTextArray(keyword.patterns).join("\n"),
    keywordCaseSensitive: Boolean(keyword.case_sensitive),
    statusCodeRange: readText(statusCode.range),
    extraFieldCount: Object.keys(raw).filter(
      (key) => !KNOWN_RETRY_RULE_KEYS.has(key),
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

function readTextArray(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((item) => (typeof item === "string" ? item.trim() : ""))
    .filter(Boolean);
}
