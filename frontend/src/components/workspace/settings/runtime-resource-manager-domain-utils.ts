import { isConfigRecord } from "./runtime-provider-config-utils";

export type RuntimeResourceManagerConfigSummary = {
  crossProviderKeySelection: boolean;
  defaultGroupAlgorithm: string;
  defaultKeyAlgorithm: string;
  defaultProviderAlgorithm: string;
  enableStats: boolean;
  enabled: boolean;
  healthCheckAutoRecovery: boolean;
  healthCheckEnabled: boolean;
  healthCheckInterval: string;
  healthCheckRecoveryThreshold: string;
  statsRetention: string;
};

export function getRuntimeResourceManagerConfig(
  value: unknown,
): RuntimeResourceManagerConfigSummary {
  const resourceManagerRoot = getResourceManagerRoot(value) ?? {};
  const healthCheck = isConfigRecord(resourceManagerRoot.health_check)
    ? resourceManagerRoot.health_check
    : {};

  return {
    enabled: Boolean(resourceManagerRoot.enabled),
    defaultGroupAlgorithm: readText(resourceManagerRoot.default_group_algorithm),
    defaultProviderAlgorithm: readText(
      resourceManagerRoot.default_provider_algorithm,
    ),
    defaultKeyAlgorithm: readText(resourceManagerRoot.default_key_algorithm),
    crossProviderKeySelection: Boolean(
      resourceManagerRoot.cross_provider_key_selection,
    ),
    healthCheckEnabled: Boolean(healthCheck.enabled),
    healthCheckInterval: readText(healthCheck.interval),
    healthCheckAutoRecovery: Boolean(healthCheck.auto_recovery),
    healthCheckRecoveryThreshold: readText(healthCheck.recovery_threshold),
    enableStats: Boolean(resourceManagerRoot.enable_stats),
    statsRetention: readText(resourceManagerRoot.stats_retention),
  };
}

function getResourceManagerRoot(value: unknown) {
  if (!isConfigRecord(value)) {
    return null;
  }
  return isConfigRecord(value.resource_manager) ? value.resource_manager : null;
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
