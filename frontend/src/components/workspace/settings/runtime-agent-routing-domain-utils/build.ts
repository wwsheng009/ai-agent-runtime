// 由 components/workspace/settings/runtime-agent-routing-domain-utils.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  removeConfigValueAtPath,
  setConfigValueAtPath,
} from "../runtime-config-editor-utils";
import { isConfigRecord } from "../runtime-provider-config-utils";

import { getRuntimeAgentRoutingSettings } from "./summary";
import {
  parseExpertConcurrency,
  readText,
  setOptionalText,
} from "./text-utils";
import {
  agentRoutingDifficulties,
  type AgentRoutingScope,
  type RuntimeAgentRoutingConfigSummary,
  type RuntimeAgentRoutingTaskTypeEntry,
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

  const taskTypes: Record<string, unknown> = {};
  const legacyRoles: Record<string, unknown> = {};
  for (const entry of config.taskTypes) {
    const entryLevels = buildTaskTypeLevels(entry);
    // 一条难度覆盖都没有的条目等价「未配置」：不写空对象噪音，也不把空的自定义
    // role 留在配置里。
    if (Object.keys(entryLevels).length === 0) continue;
    const target = entry.legacy ? legacyRoles : taskTypes;
    target[entry.key] = entryLevels;
  }

  const record: Record<string, unknown> = {
    ...config.raw,
    enabled: config.enabled,
    compatibility_mode: readText(config.raw.compatibility_mode) || "permissive",
    default_difficulty: config.defaultDifficulty,
    inherit_parent_when_missing: config.inheritParentWhenMissing,
    validate_model_capabilities: config.validateModelCapabilities,
    unsupported_reasoning_policy:
      config.unsupportedReasoningPolicy || "downgrade",
    max_expert_concurrency: parseExpertConcurrency(config.maxExpertConcurrency),
    levels,
  };
  // P4（plan §10.2 F-5）：提交产出 task_types；旧 roles 里的别名键在映射后不再
  // 回写（否则每次保存都触发后端 deprecation warning），仅保留无别名的自定义
  // role 到 roles 兜底。
  if (Object.keys(taskTypes).length > 0) {
    record.task_types = taskTypes;
  } else {
    delete record.task_types;
  }
  if (Object.keys(legacyRoles).length > 0) {
    record.roles = legacyRoles;
  } else {
    delete record.roles;
  }
  return record;
}

function buildTaskTypeLevels(
  entry: RuntimeAgentRoutingTaskTypeEntry,
): Record<string, unknown> {
  return Object.fromEntries(
    agentRoutingDifficulties.flatMap((difficulty) => {
      const profile = entry.levels[difficulty];
      const currentRaw = isConfigRecord(entry.raw[difficulty])
        ? entry.raw[difficulty]
        : {};
      const nextProfile: Record<string, unknown> = { ...currentRaw };
      setOptionalText(nextProfile, "provider", profile.provider);
      setOptionalText(nextProfile, "model", profile.model);
      setOptionalText(nextProfile, "reasoning_effort", profile.reasoningEffort);
      delete nextProfile.thinking_effort;
      // 整条难度覆盖被清空 ⇒ 省略该难度（等价「不覆盖」），不写空对象噪音。
      return Object.keys(nextProfile).length > 0
        ? [[difficulty, nextProfile] as const]
        : [];
    }),
  );
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
