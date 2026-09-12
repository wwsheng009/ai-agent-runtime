// 由 components/workspace/settings/runtime-agent-routing-domain-utils.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { isConfigRecord, type RuntimeProviderSummary } from "../runtime-provider-config-utils";

export function modelMappingTargets(
  provider: RuntimeProviderSummary,
  model: string,
  includeWildcard = false,
) {
  const mappings = isConfigRecord(provider.raw.model_mappings)
    ? provider.raw.model_mappings
    : {};
  const exact = mappings[model];
  if (typeof exact === "string" && exact.trim()) return [exact.trim()];
  const wildcard = mappings["*"];
  return includeWildcard && typeof wildcard === "string" && wildcard.trim()
    ? [wildcard.trim()]
    : [];
}

export function findConfiguredModelCapability(
  provider: RuntimeProviderSummary,
  model: string,
) {
  const capabilities = isConfigRecord(provider.raw.model_capabilities)
    ? provider.raw.model_capabilities
    : null;
  if (!capabilities) return null;

  const candidates = [model, ...modelMappingTargets(provider, model)];
  for (const candidate of candidates) {
    if (isConfigRecord(capabilities[candidate])) {
      return capabilities[candidate];
    }
  }
  return isConfigRecord(capabilities["*"]) ? capabilities["*"] : null;
}

export function supportsReasoningEffort(
  capability: Record<string, unknown>,
  effort: string,
) {
  const supportedEfforts = readCapabilityReasoningEfforts(capability).map((item) =>
    item.toLowerCase(),
  );
  if (supportedEfforts.length === 0) return capability.reasoning_model === true;
  return supportedEfforts.includes(effort.toLowerCase());
}

export function readCapabilityReasoningEfforts(
  capability: Record<string, unknown> | null,
) {
  if (!capability || !Array.isArray(capability.reasoning_efforts)) return [];
  return capability.reasoning_efforts
    .filter((item): item is string => typeof item === "string")
    .map((item) => item.trim())
    .filter(Boolean);
}
