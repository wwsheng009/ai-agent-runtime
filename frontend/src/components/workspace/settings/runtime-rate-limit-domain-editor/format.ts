import {
  createDefaultRateLimitApiKeyLimit,
  createDefaultRateLimitPathLimit,
  type RuntimeRateLimitApiKeyLimitSummary,
  type RuntimeRateLimitPathLimitSummary,
} from "../runtime-rate-limit-domain-utils";
import {
  type RateLimitApiKeyDraftInput,
  type RateLimitPathDraftInput,
} from "../runtime-rate-limit-domain-form-utils";

const KNOWN_API_KEY_LIMIT_KEYS = new Set([
  "api_key_pattern",
  "qps",
  "qpd",
  "qpm",
  "block_duration",
]);

const KNOWN_PATH_LIMIT_KEYS = new Set(["requests_per_minute", "burst"]);

export function createApiKeyDraftInput(
  limit: RuntimeRateLimitApiKeyLimitSummary | null,
): RateLimitApiKeyDraftInput {
  if (!limit) {
    const defaults = createDefaultRateLimitApiKeyLimit();
    return {
      apiKeyPattern:
        typeof defaults.api_key_pattern === "string"
          ? defaults.api_key_pattern
          : "",
      qps: stringifyEditableValue(defaults.qps),
      qpd: stringifyEditableValue(defaults.qpd),
      qpm: stringifyEditableValue(defaults.qpm),
      blockDuration:
        typeof defaults.block_duration === "string" ? defaults.block_duration : "",
      extraJson: "{}",
    };
  }

  const extraFields = Object.fromEntries(
    Object.entries(limit.raw).filter(([key]) => !KNOWN_API_KEY_LIMIT_KEYS.has(key)),
  );

  return {
    apiKeyPattern: limit.apiKeyPattern,
    qps: limit.qps,
    qpd: limit.qpd,
    qpm: limit.qpm,
    blockDuration: limit.blockDuration,
    extraJson: JSON.stringify(extraFields, null, 2),
  };
}

export function createPathDraftInput(
  limit: RuntimeRateLimitPathLimitSummary | null,
): RateLimitPathDraftInput {
  if (!limit) {
    const defaults = createDefaultRateLimitPathLimit();
    return {
      path: "",
      requestsPerMinute: stringifyEditableValue(defaults.requests_per_minute),
      burst: stringifyEditableValue(defaults.burst),
      extraJson: "{}",
    };
  }

  const extraFields = Object.fromEntries(
    Object.entries(limit.raw).filter(([key]) => !KNOWN_PATH_LIMIT_KEYS.has(key)),
  );

  return {
    path: limit.path,
    requestsPerMinute: limit.requestsPerMinute,
    burst: limit.burst,
    extraJson: JSON.stringify(extraFields, null, 2),
  };
}

export function stringifyEditableValue(value: unknown) {
  if (typeof value === "number") {
    return String(value);
  }
  return typeof value === "string" ? value : "";
}

