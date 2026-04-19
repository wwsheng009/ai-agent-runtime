import { isConfigRecord } from "./runtime-provider-config-utils";

const KNOWN_PROVIDER_QUEUE_PROVIDER_KEYS = new Set([
  "max_concurrency",
  "queue_size",
  "queue_timeout",
]);

export type RuntimeProviderQueueConfigSummary = {
  defaultMaxConcurrency: string;
  defaultOverflowStrategy: string;
  defaultQueueSize: string;
  defaultQueueTimeout: string;
  enabled: boolean;
  overflowEnabled: boolean;
  overflowMaxAttempts: string;
  overflowStrategy: string;
  waitHeartbeatComment: string;
  waitHeartbeatEnabled: boolean;
  waitHeartbeatInterval: string;
  waitHeartbeatMaxWaitTime: string;
};

export type RuntimeProviderQueueProviderSummary = {
  extraFieldCount: number;
  id: string;
  maxConcurrency: string;
  provider: string;
  queueSize: string;
  queueTimeout: string;
  raw: Record<string, unknown>;
};

export function getRuntimeProviderQueueConfig(
  value: unknown,
): RuntimeProviderQueueConfigSummary {
  const providerQueueRoot = getProviderQueueRoot(value) ?? {};
  const defaultSlot = isConfigRecord(providerQueueRoot.default_slot)
    ? providerQueueRoot.default_slot
    : {};
  const overflow = isConfigRecord(providerQueueRoot.overflow)
    ? providerQueueRoot.overflow
    : {};
  const waitHeartbeat = isConfigRecord(providerQueueRoot.wait_heartbeat)
    ? providerQueueRoot.wait_heartbeat
    : {};

  return {
    enabled: Boolean(providerQueueRoot.enabled),
    defaultMaxConcurrency: readText(defaultSlot.max_concurrency),
    defaultQueueSize: readText(defaultSlot.queue_size),
    defaultQueueTimeout: readText(defaultSlot.queue_timeout),
    defaultOverflowStrategy: readText(defaultSlot.overflow_strategy),
    overflowEnabled: Boolean(overflow.enabled),
    overflowMaxAttempts: readText(overflow.max_attempts),
    overflowStrategy: readText(overflow.strategy),
    waitHeartbeatEnabled: Boolean(waitHeartbeat.enabled),
    waitHeartbeatInterval: readText(waitHeartbeat.interval),
    waitHeartbeatComment: readText(waitHeartbeat.comment),
    waitHeartbeatMaxWaitTime: readText(waitHeartbeat.max_wait_time),
  };
}

export function listRuntimeProviderQueueProviders(
  value: unknown,
): RuntimeProviderQueueProviderSummary[] {
  const providerQueueRoot = getProviderQueueRoot(value);
  const providers = isConfigRecord(providerQueueRoot?.providers)
    ? providerQueueRoot.providers
    : {};

  return Object.entries(providers)
    .sort(([left], [right]) => left.localeCompare(right))
    .flatMap(([provider, raw]) =>
      isConfigRecord(raw) ? [buildProviderSummary(provider, raw)] : [],
    );
}

function getProviderQueueRoot(value: unknown) {
  if (!isConfigRecord(value)) {
    return null;
  }
  return isConfigRecord(value.provider_queue) ? value.provider_queue : null;
}

function buildProviderSummary(
  provider: string,
  raw: Record<string, unknown>,
): RuntimeProviderQueueProviderSummary {
  return {
    id: provider,
    provider,
    raw,
    maxConcurrency: readText(raw.max_concurrency),
    queueSize: readText(raw.queue_size),
    queueTimeout: readText(raw.queue_timeout),
    extraFieldCount: Object.keys(raw).filter(
      (key) => !KNOWN_PROVIDER_QUEUE_PROVIDER_KEYS.has(key),
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
