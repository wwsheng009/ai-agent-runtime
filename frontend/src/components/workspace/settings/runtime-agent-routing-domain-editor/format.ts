import { type TFunction } from "i18next";

import {
  agentRoutingDifficulties,
  agentRoutingTaskTypes,
  type AgentRoutingDifficulty,
  type RuntimeAgentRouteHealth,
  type RuntimeAgentRouteHealthIssue,
  type RuntimeAgentRouteProfile,
  type RuntimeAgentRoutingConfigSummary,
} from "../runtime-agent-routing-domain-utils";

export const reasoningPolicyValues = ["ignore", "downgrade", "fail"] as const;

export function difficultyOptions(t: TFunction<"runtimeConfig">) {
  return agentRoutingDifficulties.map((difficulty) => ({
    value: difficulty,
    label: difficultyLabel(t, difficulty),
  }));
}

export function difficultyLabel(
  t: TFunction<"runtimeConfig">,
  difficulty: AgentRoutingDifficulty,
) {
  return t(`editor.agentRouting.difficulties.${difficulty}`);
}

/** 12 类封闭枚举的展示名；未收录的自定义键（兼容展示）原样回显。 */
export function taskTypeLabel(t: TFunction<"runtimeConfig">, key: string) {
  const i18nKey = `editor.agentRouting.taskTypes.keys.${key}`;
  const translated = t(i18nKey as never);
  return translated === i18nKey ? key : translated;
}

export function taskTypeOptions(t: TFunction<"runtimeConfig">) {
  return agentRoutingTaskTypes.map((key) => ({
    value: key,
    label: taskTypeLabel(t, key),
  }));
}

/** 新增 task_type 条目时的空白难度表（空串 = 不覆盖，走父级/档位回退）。 */
export function emptyRouteLevels(): RuntimeAgentRoutingConfigSummary["levels"] {
  return Object.fromEntries(
    agentRoutingDifficulties.map((difficulty) => [
      difficulty,
      { provider: "", model: "", reasoningEffort: "" },
    ]),
  ) as RuntimeAgentRoutingConfigSummary["levels"];
}

export function cloneRoutingConfig(
  config: RuntimeAgentRoutingConfigSummary,
): RuntimeAgentRoutingConfigSummary {
  return {
    ...config,
    raw: { ...config.raw },
    levels: Object.fromEntries(
      agentRoutingDifficulties.map((difficulty) => [
        difficulty,
        { ...config.levels[difficulty] },
      ]),
    ) as RuntimeAgentRoutingConfigSummary["levels"],
    // P4（F-5）：task_types 是嵌套结构，必须逐层拷贝，否则编辑器改的是原对象。
    taskTypes: config.taskTypes.map((entry) => ({
      ...entry,
      raw: { ...entry.raw },
      levels: Object.fromEntries(
        agentRoutingDifficulties.map((difficulty) => [
          difficulty,
          { ...entry.levels[difficulty] },
        ]),
      ) as RuntimeAgentRoutingConfigSummary["levels"],
    })),
  };
}

export function previewTranslation(
  t: TFunction<"runtimeConfig">,
  group:
    | "difficulties"
    | "difficultySources"
    | "routingSources"
    | "sources"
    | "warnings",
  value: string,
) {
  const key = `editor.agentRouting.preview.${group}.${value}`;
  const translated = t(key as never);
  return translated === key ? value : translated;
}

export function routeHealthIssueMessage(
  t: TFunction<"runtimeConfig">,
  issue: RuntimeAgentRouteHealthIssue,
  health: RuntimeAgentRouteHealth,
  profile: RuntimeAgentRouteProfile,
) {
  return t(`editor.agentRouting.health.issues.${issue.code}`, {
    effort: profile.reasoningEffort,
    model: health.effectiveModel || profile.model,
    provider: profile.provider,
    resolvedProvider: health.effectiveProvider,
  });
}
