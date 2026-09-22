// 由 components/workspace/settings/runtime-agent-routing-domain-utils.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  agentRoutingTaskTypes,
  type AgentRoutingTaskType,
} from "@/types/runtime";

// P4（plan §10.2 F-5）：task_types 的编辑键必须是 12 类封闭枚举，直接复用
// 观测契约里的同一份常量，避免前后端两份枚举漂移。
export { agentRoutingTaskTypes };
export type { AgentRoutingTaskType };

/**
 * 旧 roles.<role> → task_types.<task_type> 的兼容别名（plan K-3）。
 * 与后端 agentconfig 的 subagentRoleTaskTypeAlias 保持一致；其余自定义
 * role 无别名，读入后原样保留展示并写回 roles 兜底。
 */
export const agentRoutingRoleAliases: Record<string, AgentRoutingTaskType> = {
  researcher: "explore",
  verifier: "verify",
  writer: "implement",
};

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

/**
 * 一个 task_type（或兼容读入的旧 role）的难度覆盖表。
 * - legacy=false：写入 `routing.task_types.<key>`；
 * - legacy=true：来自旧 `routing.roles` 且无别名映射的自定义 role，写回
 *   `routing.roles.<key>` 兜底，不污染封闭枚举。
 */
export type RuntimeAgentRoutingTaskTypeEntry = {
  key: string;
  legacy: boolean;
  levels: Record<AgentRoutingDifficulty, RuntimeAgentRouteProfile>;
  /** 原始子记录：保留 timeout 等未在编辑器暴露的字段。 */
  raw: Record<string, unknown>;
};

export type RuntimeAgentRoutingConfigSummary = {
  defaultDifficulty: AgentRoutingDifficulty;
  enabled: boolean;
  inheritParentWhenMissing: boolean;
  levels: Record<AgentRoutingDifficulty, RuntimeAgentRouteProfile>;
  maxExpertConcurrency: string;
  raw: Record<string, unknown>;
  taskTypes: RuntimeAgentRoutingTaskTypeEntry[];
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
