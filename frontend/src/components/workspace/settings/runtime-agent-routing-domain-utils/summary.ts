// 由 components/workspace/settings/runtime-agent-routing-domain-utils.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { isConfigRecord } from "../runtime-provider-config-utils";

import { readText } from "./text-utils";
import {
  agentRoutingDifficulties,
  agentRoutingRoleAliases,
  type AgentRoutingDifficulty,
  type RuntimeAgentRouteProfile,
  type RuntimeAgentRoutingConfigSummary,
  type RuntimeAgentRoutingSettings,
  type RuntimeAgentRoutingTaskTypeEntry,
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
    taskTypes: readTaskTypeEntries(raw),
  };
}

/**
 * P4（plan §10.2 F-5）：`task_types` 显式优先；仅有旧 `roles` 时按兼容别名
 * 映射展示（verifier→verify、writer→implement、researcher→explore），无别名的
 * 自定义 role 以 legacy 条目原样保留。两者并存时，别名映射只在 task_types 缺少
 * 该键时补充——与后端「显式 task_types 优先，不被 roles 覆盖」语义一致。
 */
function readTaskTypeEntries(
  raw: Record<string, unknown>,
): RuntimeAgentRoutingTaskTypeEntry[] {
  const entries: RuntimeAgentRoutingTaskTypeEntry[] = [];
  const seen = new Set<string>();
  const taskTypes = isConfigRecord(raw.task_types) ? raw.task_types : {};
  for (const [key, value] of Object.entries(taskTypes)) {
    const trimmed = key.trim();
    if (!trimmed || seen.has(trimmed)) continue;
    seen.add(trimmed);
    entries.push(readTaskTypeEntry(trimmed, value, false));
  }

  const roles = isConfigRecord(raw.roles) ? raw.roles : {};
  for (const [role, value] of Object.entries(roles)) {
    const trimmedRole = role.trim();
    if (!trimmedRole) continue;
    const mapped = agentRoutingRoleAliases[trimmedRole.toLowerCase()];
    if (mapped) {
      if (seen.has(mapped)) continue;
      seen.add(mapped);
      entries.push(readTaskTypeEntry(mapped, value, false));
      continue;
    }
    if (seen.has(trimmedRole)) continue;
    seen.add(trimmedRole);
    entries.push(readTaskTypeEntry(trimmedRole, value, true));
  }
  return entries;
}

function readTaskTypeEntry(
  key: string,
  value: unknown,
  legacy: boolean,
): RuntimeAgentRoutingTaskTypeEntry {
  const rawLevels = isConfigRecord(value) ? value : {};
  return {
    key,
    legacy,
    raw: rawLevels,
    levels: Object.fromEntries(
      agentRoutingDifficulties.map((difficulty) => [
        difficulty,
        readRouteProfile(
          isConfigRecord(rawLevels[difficulty]) ? rawLevels[difficulty] : {},
        ),
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
    taskTypes: value.taskTypes.map((entry) => ({
      ...entry,
      raw: { ...entry.raw },
      levels: Object.fromEntries(
        agentRoutingDifficulties.map((difficulty) => [
          difficulty,
          { ...entry.levels[difficulty] },
        ]),
      ) as Record<AgentRoutingDifficulty, RuntimeAgentRouteProfile>,
    })),
  };
}
