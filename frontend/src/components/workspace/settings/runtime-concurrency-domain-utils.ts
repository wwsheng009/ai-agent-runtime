import { isConfigRecord } from "./runtime-provider-config-utils";

export type RuntimeConcurrencyConfigSummary = {
  enabled: boolean;
  maxConcurrentRequests: string;
  queueSize: string;
  queueTimeout: string;
};

export type RuntimeConcurrencyProviderLimitSummary = {
  id: string;
  limit: string;
  provider: string;
};

export function getRuntimeConcurrencyConfig(
  value: unknown,
): RuntimeConcurrencyConfigSummary {
  const concurrencyRoot = getConcurrencyRoot(value) ?? {};

  return {
    enabled: Boolean(concurrencyRoot.enabled),
    maxConcurrentRequests: readText(concurrencyRoot.max_concurrent_requests),
    queueSize: readText(concurrencyRoot.queue_size),
    queueTimeout: readText(concurrencyRoot.queue_timeout),
  };
}

export function listRuntimeConcurrencyProviderLimits(
  value: unknown,
): RuntimeConcurrencyProviderLimitSummary[] {
  const concurrencyRoot = getConcurrencyRoot(value);
  const limits = isConfigRecord(concurrencyRoot?.per_provider_limits)
    ? concurrencyRoot.per_provider_limits
    : {};

  return Object.entries(limits)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([provider, limit]) => ({
      id: provider,
      provider,
      limit: readText(limit),
    }));
}

function getConcurrencyRoot(value: unknown) {
  if (!isConfigRecord(value)) {
    return null;
  }
  return isConfigRecord(value.concurrency) ? value.concurrency : null;
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
