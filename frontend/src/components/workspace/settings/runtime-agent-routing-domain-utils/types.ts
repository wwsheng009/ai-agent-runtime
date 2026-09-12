// 由 components/workspace/settings/runtime-agent-routing-domain-utils.ts 机械拆分而来（P0-2），仅搬迁不改语义。

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
