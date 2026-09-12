// 由 components/workspace/settings/runtime-agent-routing-domain-utils.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  removeConfigValueAtPath,
  setConfigValueAtPath,
} from "../runtime-config-editor-utils";
import { isConfigRecord } from "../runtime-provider-config-utils";

import { getRuntimeAgentRoutingSettings } from "./summary";
import {
  parseNonNegativeInteger,
  readText,
  setOptionalText,
} from "./text-utils";
import {
  agentRoutingDifficulties,
  type AgentRoutingScope,
  type RuntimeAgentRoutingConfigSummary,
} from "./types";

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
