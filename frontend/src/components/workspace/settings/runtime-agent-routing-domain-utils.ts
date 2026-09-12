// 由 components/workspace/settings/runtime-agent-routing-domain-utils.ts 机械拆分而来（P0-2），仅搬迁不改语义。
// 对外导出面保持不变（18 个符号），消费方 import 路径零改动；实现见 ./runtime-agent-routing-domain-utils/ 各域模块。

export { agentRoutingDifficulties } from "./runtime-agent-routing-domain-utils/types";
export type {
  AgentRoutingDifficulty,
  AgentRoutingScope,
  RuntimeAgentRouteHealth,
  RuntimeAgentRouteHealthIssue,
  RuntimeAgentRouteHealthIssueCode,
  RuntimeAgentRouteHealthMode,
  RuntimeAgentRouteProfile,
  RuntimeAgentRoutingConfigSummary,
  RuntimeAgentRoutingHealthSummary,
  RuntimeAgentRoutingSettings,
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
