// 由 components/workspace/settings/runtime-agent-routing-domain-utils.ts 机械拆分而来（P0-2），仅搬迁不改语义。
// 对外导出面保持稳定（18 个基础符号 + P4 新增的 task_type 枚举/别名/条目类型），
// 消费方 import 路径零改动；实现见 ./runtime-agent-routing-domain-utils/ 各域模块。

export {
  agentRoutingDifficulties,
  agentRoutingRoleAliases,
  agentRoutingTaskTypes,
} from "./runtime-agent-routing-domain-utils/types";
export type {
  AgentRoutingDifficulty,
  AgentRoutingScope,
  AgentRoutingTaskType,
  RuntimeAgentRouteHealth,
  RuntimeAgentRouteHealthIssue,
  RuntimeAgentRouteHealthIssueCode,
  RuntimeAgentRouteHealthMode,
  RuntimeAgentRouteProfile,
  RuntimeAgentRoutingConfigSummary,
  RuntimeAgentRoutingHealthSummary,
  RuntimeAgentRoutingSettings,
  RuntimeAgentRoutingTaskTypeEntry,
} from "./runtime-agent-routing-domain-utils/types";

export { getRuntimeAgentRoutingSettings } from "./runtime-agent-routing-domain-utils/summary";

export {
  buildRuntimeAgentRoutingRecord,
  updateRuntimeAgentRoutingConfig,
  updateRuntimeTeamRoutingInheritance,
} from "./runtime-agent-routing-domain-utils/build";

export {
  providerModelOptions,
  providerReasoningEffortOptions,
} from "./runtime-agent-routing-domain-utils/options";

export { analyzeRuntimeAgentRoutingConfig } from "./runtime-agent-routing-domain-utils/health";
