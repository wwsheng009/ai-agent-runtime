import { type TFunction } from "i18next";

import {
  agentRoutingDifficulties,
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
  };
}

export function previewTranslation(
  t: TFunction<"runtimeConfig">,
  group: "difficulties" | "routingSources" | "sources" | "warnings",
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
