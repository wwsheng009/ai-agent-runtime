import { isConfigRecord, type RuntimeProviderSummary } from "./runtime-provider-config-utils";
import {
  removeConfigValueAtPath,
  setConfigValueAtPath,
} from "./runtime-config-editor-utils";

export const agentRoutingDifficulties = [
  "easy",
  "normal",
  "hard",
  "expert",
] as const;

export type AgentRoutingDifficulty = (typeof agentRoutingDifficulties)[number];
export type AgentRoutingScope = "subagents" | "teams";

export type RuntimeAgentRouteProfile = {
  model: string;
  provider: string;
  reasoningEffort: string;
};

export type RuntimeAgentRoutingConfigSummary = {
  defaultDifficulty: AgentRoutingDifficulty;
  enabled: boolean;
  inheritParentWhenMissing: boolean;
  levels: Record<AgentRoutingDifficulty, RuntimeAgentRouteProfile>;
  maxExpertConcurrency: string;
  raw: Record<string, unknown>;
  unsupportedReasoningPolicy: string;
  validateModelCapabilities: boolean;
};

export type RuntimeAgentRoutingSettings = {
  subagents: RuntimeAgentRoutingConfigSummary;
  teamUsesSubagentRouting: boolean;
  teams: RuntimeAgentRoutingConfigSummary;
};

export type RuntimeAgentRouteHealthMode =
  | "configured"
  | "disabled"
  | "inherited"
  | "partial"
  | "providerDefault";

export type RuntimeAgentRouteHealthIssueCode =
  | "ambiguousProviderAlias"
  | "disabledProvider"
  | "missingRequiredRoute"
  | "modelNotListed"
  | "modelWithoutProvider"
  | "providerAlias"
  | "providerWithoutModel"
  | "reasoningUnsupported"
  | "unknownProvider";

export type RuntimeAgentRouteHealthIssue = {
  code: RuntimeAgentRouteHealthIssueCode;
  severity: "error" | "warning";
};

export type RuntimeAgentRouteHealth = {
  difficulty: AgentRoutingDifficulty;
  effectiveModel: string;
  effectiveProvider: string;
  issues: RuntimeAgentRouteHealthIssue[];
  mode: RuntimeAgentRouteHealthMode;
};

export type RuntimeAgentRoutingHealthSummary = {
  configuredCount: number;
  errorCount: number;
  inheritedCount: number;
  routes: Record<AgentRoutingDifficulty, RuntimeAgentRouteHealth>;
  warningCount: number;
};

export function getRuntimeAgentRoutingSettings(
  value: unknown,
): RuntimeAgentRoutingSettings {
  const aicli = isConfigRecord(value) && isConfigRecord(value.aicli) ? value.aicli : {};
  const subagents = isConfigRecord(aicli.subagents) ? aicli.subagents : {};
  const teams = isConfigRecord(aicli.teams) ? aicli.teams : {};
  const subagentRouting = isConfigRecord(subagents.routing) ? subagents.routing : {};
  const teamRouting = isConfigRecord(teams.routing) ? teams.routing : null;
  const subagentSummary = readRoutingSummary(subagentRouting);

  return {
    subagents: subagentSummary,
    teamUsesSubagentRouting: teamRouting == null,
    teams: teamRouting == null ? cloneRoutingSummary(subagentSummary) : readRoutingSummary(teamRouting),
  };
}

export function buildRuntimeAgentRoutingRecord(
  config: RuntimeAgentRoutingConfigSummary,
): Record<string, unknown> {
  const levels = Object.fromEntries(
    agentRoutingDifficulties.map((difficulty) => {
      const profile = config.levels[difficulty];
      const currentRaw =
        isConfigRecord(config.raw.levels) &&
        isConfigRecord(config.raw.levels[difficulty])
          ? config.raw.levels[difficulty]
          : {};
      const nextProfile: Record<string, unknown> = { ...currentRaw };
      setOptionalText(nextProfile, "provider", profile.provider);
      setOptionalText(nextProfile, "model", profile.model);
      setOptionalText(nextProfile, "reasoning_effort", profile.reasoningEffort);
      delete nextProfile.thinking_effort;
      return [difficulty, nextProfile];
    }),
  );

  return {
    ...config.raw,
    enabled: config.enabled,
    compatibility_mode: readText(config.raw.compatibility_mode) || "permissive",
    default_difficulty: config.defaultDifficulty,
    inherit_parent_when_missing: config.inheritParentWhenMissing,
    validate_model_capabilities: config.validateModelCapabilities,
    unsupported_reasoning_policy:
      config.unsupportedReasoningPolicy || "downgrade",
    max_expert_concurrency: parseNonNegativeInteger(config.maxExpertConcurrency),
    levels,
  };
}

export function updateRuntimeAgentRoutingConfig(
  value: unknown,
  scope: AgentRoutingScope,
  config: RuntimeAgentRoutingConfigSummary,
) {
  return setConfigValueAtPath(
    value,
    ["aicli", scope, "routing"],
    buildRuntimeAgentRoutingRecord(config),
  );
}

export function updateRuntimeTeamRoutingInheritance(
  value: unknown,
  inherit: boolean,
) {
  if (inherit) {
    return removeConfigValueAtPath(value, ["aicli", "teams", "routing"]);
  }
  const settings = getRuntimeAgentRoutingSettings(value);
  return updateRuntimeAgentRoutingConfig(value, "teams", settings.subagents);
}

export function providerModelOptions(
  providers: RuntimeProviderSummary[],
  providerName: string,
  currentModel: string,
) {
  const provider = providers.find((item) => item.name === providerName);
  const models = new Set<string>();
  if (currentModel.trim()) models.add(currentModel.trim());
  if (provider?.defaultModel.trim()) models.add(provider.defaultModel.trim());
  for (const model of provider?.supportedModels ?? []) {
    if (model.trim()) models.add(model.trim());
  }
  return [...models].map((model) => ({ label: model, value: model }));
}

export function providerReasoningEffortOptions(
  providers: RuntimeProviderSummary[],
  providerName: string,
  modelName: string,
  currentEffort: string,
) {
  const provider = providers.find((item) => item.name === providerName.trim());
  const model = modelName.trim() || provider?.defaultModel.trim() || "";
  const capability = provider && model
    ? findConfiguredModelCapability(provider, model)
    : null;
  const declared = readCapabilityReasoningEfforts(capability);
  const values = new Set<string>();

  if (declared.length > 0) {
    for (const effort of declared) values.add(effort);
  } else if (!capability || capability.reasoning_model === true) {
    for (const effort of ["none", "minimal", "low", "medium", "high", "xhigh", "max"]) {
      values.add(effort);
    }
  }
  if (currentEffort.trim()) values.add(currentEffort.trim());
  return [...values].map((value) => ({ label: value, value }));
}

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

function modelMappingTargets(
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

function findConfiguredModelCapability(
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

function supportsReasoningEffort(
  capability: Record<string, unknown>,
  effort: string,
) {
  const supportedEfforts = readCapabilityReasoningEfforts(capability).map((item) =>
    item.toLowerCase(),
  );
  if (supportedEfforts.length === 0) return capability.reasoning_model === true;
  return supportedEfforts.includes(effort.toLowerCase());
}

function readCapabilityReasoningEfforts(
  capability: Record<string, unknown> | null,
) {
  if (!capability || !Array.isArray(capability.reasoning_efforts)) return [];
  return capability.reasoning_efforts
    .filter((item): item is string => typeof item === "string")
    .map((item) => item.trim())
    .filter(Boolean);
}

function readRoutingSummary(raw: Record<string, unknown>): RuntimeAgentRoutingConfigSummary {
  const levels = isConfigRecord(raw.levels) ? raw.levels : {};
  return {
    raw,
    enabled: raw.enabled === true,
    defaultDifficulty: readDifficulty(raw.default_difficulty),
    inheritParentWhenMissing: raw.inherit_parent_when_missing !== false,
    validateModelCapabilities: raw.validate_model_capabilities !== false,
    unsupportedReasoningPolicy: readText(raw.unsupported_reasoning_policy) || "downgrade",
    maxExpertConcurrency: readNumberText(raw.max_expert_concurrency, "1"),
    levels: Object.fromEntries(
      agentRoutingDifficulties.map((difficulty) => [
        difficulty,
        readRouteProfile(isConfigRecord(levels[difficulty]) ? levels[difficulty] : {}),
      ]),
    ) as Record<AgentRoutingDifficulty, RuntimeAgentRouteProfile>,
  };
}

function readRouteProfile(raw: Record<string, unknown>): RuntimeAgentRouteProfile {
  return {
    provider: readText(raw.provider),
    model: readText(raw.model),
    reasoningEffort:
      readText(raw.reasoning_effort) || readText(raw.thinking_effort),
  };
}

function readDifficulty(value: unknown): AgentRoutingDifficulty {
  const normalized = readText(value).toLowerCase();
  return agentRoutingDifficulties.includes(normalized as AgentRoutingDifficulty)
    ? (normalized as AgentRoutingDifficulty)
    : "normal";
}

function readText(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

function readNumberText(value: unknown, fallback: string) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return String(value);
  }
  const text = readText(value);
  return text || fallback;
}

function parseNonNegativeInteger(value: string) {
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : 0;
}

function setOptionalText(
  target: Record<string, unknown>,
  key: string,
  value: string,
) {
  const normalized = value.trim();
  if (normalized) {
    target[key] = normalized;
  } else {
    delete target[key];
  }
}

function cloneRoutingSummary(
  value: RuntimeAgentRoutingConfigSummary,
): RuntimeAgentRoutingConfigSummary {
  return {
    ...value,
    raw: { ...value.raw },
    levels: Object.fromEntries(
      agentRoutingDifficulties.map((difficulty) => [
        difficulty,
        { ...value.levels[difficulty] },
      ]),
    ) as Record<AgentRoutingDifficulty, RuntimeAgentRouteProfile>,
  };
}
