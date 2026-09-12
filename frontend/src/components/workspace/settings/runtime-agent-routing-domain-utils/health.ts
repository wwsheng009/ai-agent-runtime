// 由 components/workspace/settings/runtime-agent-routing-domain-utils.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { isConfigRecord, type RuntimeProviderSummary } from "../runtime-provider-config-utils";

import {
  findConfiguredModelCapability,
  modelMappingTargets,
  supportsReasoningEffort,
} from "./capabilities";
import {
  agentRoutingDifficulties,
  type AgentRoutingDifficulty,
  type RuntimeAgentRouteHealth,
  type RuntimeAgentRouteHealthIssue,
  type RuntimeAgentRouteHealthMode,
  type RuntimeAgentRoutingConfigSummary,
  type RuntimeAgentRoutingHealthSummary,
} from "./types";

export function analyzeRuntimeAgentRoutingConfig(
  config: RuntimeAgentRoutingConfigSummary,
  providers: RuntimeProviderSummary[],
): RuntimeAgentRoutingHealthSummary {
  const routes = Object.fromEntries(
    agentRoutingDifficulties.map((difficulty) => [
      difficulty,
      analyzeRouteHealth(difficulty, config, providers),
    ]),
  ) as Record<AgentRoutingDifficulty, RuntimeAgentRouteHealth>;
  const values = Object.values(routes);

  return {
    routes,
    configuredCount: values.filter(
      (route) => route.mode === "configured" || route.mode === "providerDefault",
    ).length,
    inheritedCount: values.filter(
      (route) => route.mode === "disabled" || route.mode === "inherited",
    ).length,
    errorCount: values.reduce(
      (count, route) =>
        count + route.issues.filter((issue) => issue.severity === "error").length,
      0,
    ),
    warningCount: values.reduce(
      (count, route) =>
        count + route.issues.filter((issue) => issue.severity === "warning").length,
      0,
    ),
  };
}

function analyzeRouteHealth(
  difficulty: AgentRoutingDifficulty,
  config: RuntimeAgentRoutingConfigSummary,
  providers: RuntimeProviderSummary[],
): RuntimeAgentRouteHealth {
  const profile = config.levels[difficulty];
  const providerName = profile.provider.trim();
  const configuredModel = profile.model.trim();
  const issues: RuntimeAgentRouteHealthIssue[] = [];

  if (!config.enabled) {
    return routeHealth(difficulty, "disabled", "", "", issues);
  }

  if (!providerName && !configuredModel) {
    if (!config.inheritParentWhenMissing) {
      issues.push({ code: "missingRequiredRoute", severity: "error" });
    }
    return routeHealth(difficulty, "inherited", "", "", issues);
  }

  if (!providerName) {
    issues.push({
      code: config.inheritParentWhenMissing
        ? "modelWithoutProvider"
        : "missingRequiredRoute",
      severity: config.inheritParentWhenMissing ? "warning" : "error",
    });
    return routeHealth(difficulty, "partial", "", configuredModel, issues);
  }

  const providerResolution = resolveProviderReference(providerName, providers);
  const provider = providerResolution.provider;
  if (providerResolution.kind === "alias") {
    issues.push({ code: "providerAlias", severity: "warning" });
  } else if (providerResolution.kind === "ambiguousAlias") {
    issues.push({
      code: "ambiguousProviderAlias",
      severity: config.inheritParentWhenMissing ? "warning" : "error",
    });
  } else if (!provider) {
    issues.push({
      code: "unknownProvider",
      severity: config.inheritParentWhenMissing ? "warning" : "error",
    });
  }
  if (provider && !provider.enabled) {
    issues.push({
      code: "disabledProvider",
      severity: config.inheritParentWhenMissing ? "warning" : "error",
    });
  }

  const effectiveModel = configuredModel || provider?.defaultModel.trim() || "";
  if (!effectiveModel) {
    issues.push({
      code: config.inheritParentWhenMissing
        ? "providerWithoutModel"
        : "missingRequiredRoute",
      severity: config.inheritParentWhenMissing ? "warning" : "error",
    });
  } else if (provider?.enabled) {
    analyzeModelHealth(config, provider, effectiveModel, profile.reasoningEffort, issues);
  }

  const mode: RuntimeAgentRouteHealthMode = configuredModel
    ? "configured"
    : effectiveModel
      ? "providerDefault"
      : "partial";
  return routeHealth(
    difficulty,
    mode,
    provider?.name ?? providerName,
    effectiveModel,
    issues,
  );
}

function routeHealth(
  difficulty: AgentRoutingDifficulty,
  mode: RuntimeAgentRouteHealthMode,
  effectiveProvider: string,
  effectiveModel: string,
  issues: RuntimeAgentRouteHealthIssue[],
): RuntimeAgentRouteHealth {
  return { difficulty, effectiveModel, effectiveProvider, issues, mode };
}

function analyzeModelHealth(
  config: RuntimeAgentRoutingConfigSummary,
  provider: RuntimeProviderSummary,
  model: string,
  reasoningEffort: string,
  issues: RuntimeAgentRouteHealthIssue[],
) {
  if (
    provider.supportedModels.length > 0 &&
    !provider.supportedModels.some((item) => item === model) &&
    !modelMappingTargets(provider, model, true).some((item) =>
      provider.supportedModels.includes(item),
    )
  ) {
    issues.push({ code: "modelNotListed", severity: "warning" });
  }

  const effort = reasoningEffort.trim();
  if (!config.validateModelCapabilities || !effort) return;
  const capability = findConfiguredModelCapability(provider, model);
  if (!capability || supportsReasoningEffort(capability, effort)) return;
  issues.push({
    code: "reasoningUnsupported",
    severity: config.unsupportedReasoningPolicy === "fail" ? "error" : "warning",
  });
}

function resolveProviderReference(
  reference: string,
  providers: RuntimeProviderSummary[],
): {
  kind: "alias" | "ambiguousAlias" | "exact" | "missing";
  provider?: RuntimeProviderSummary;
} {
  const exact = providers.find((provider) => provider.name === reference);
  if (exact) return { kind: "exact", provider: exact };

  const aliasMatches = providers.filter((provider) =>
    providerAliases(provider).includes(reference),
  );
  const enabledAliasMatches = aliasMatches.filter((provider) => provider.enabled);
  if (enabledAliasMatches.length === 1) {
    return { kind: "alias", provider: enabledAliasMatches[0] };
  }
  if (enabledAliasMatches.length > 1) return { kind: "ambiguousAlias" };
  if (aliasMatches.length === 1) {
    return { kind: "alias", provider: aliasMatches[0] };
  }
  if (aliasMatches.length > 1) return { kind: "ambiguousAlias" };
  return { kind: "missing" };
}

function providerAliases(provider: RuntimeProviderSummary) {
  const aliases = new Set<string>();
  if (provider.defaultModel.trim()) aliases.add(provider.defaultModel.trim());
  for (const model of provider.supportedModels) aliases.add(model);
  const mappings = isConfigRecord(provider.raw.model_mappings)
    ? provider.raw.model_mappings
    : {};
  for (const [source, target] of Object.entries(mappings)) {
    if (source.trim() && source !== "*") aliases.add(source.trim());
    if (typeof target === "string" && target.trim()) aliases.add(target.trim());
  }
  return [...aliases];
}
