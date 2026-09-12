// 由 components/workspace/settings/runtime-agent-routing-domain-utils.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { isConfigRecord } from "../runtime-provider-config-utils";

import { readText } from "./text-utils";
import {
  agentRoutingDifficulties,
  type AgentRoutingDifficulty,
  type RuntimeAgentRouteProfile,
  type RuntimeAgentRoutingConfigSummary,
  type RuntimeAgentRoutingSettings,
} from "./types";

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

function readNumberText(value: unknown, fallback: string) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return String(value);
  }
  const text = readText(value);
  return text || fallback;
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
