// P2-1A：子代理控制面契约（AgentControl 身份图）。
//
// 数据源是后端 AgentControl 投影 / 注册表（`agentcontrol.AgentRecord`，
// `backend/internal/agentcontrol/agent_registry.go:31-67`），与 chat 会话记录
// 相互独立：**会话是执行容器，AgentRecord 是控制面身份**。前端只消费本文件
// 收口的字段子集；未列出的路由细节字段保持后端原样即可，前端不臆造。
//
// 状态口径（`agent_registry.go:17-26`）：
//   - `active`：默认的开放身份状态；
//   - `stale`：执行容器 / 租约已无法支撑路由的失联身份（终态，但不等同于
//     有序关闭，仅用于诊断与一致性审计）；
//   - `closed`：已关闭、不应再接收路由的身份。
// 前端把空值 / 未知值收口为 `unknown`，**不把未知状态当作 active 呈现**。

/** 后端已定义的三态 + 前端收口的未知态。 */
export type RuntimeAgentStatus = "active" | "stale" | "closed" | "unknown";

export type RuntimeAgentRecord = {
  agentId: string;
  rootSessionId: string | null;
  parentAgentId: string | null;
  parentSessionId: string | null;
  sessionId: string | null;
  /** 形如 `/root/child-1` 的稳定身份路径（`normalizeAgentPath`）。 */
  agentPath: string | null;
  depth: number | null;
  agentType: string | null;
  nickname: string | null;
  workflow: string | null;
  teamId: string | null;
  teammateId: string | null;
  /** 生效 provider / model（回退到 `provider` / `model` 字段）。 */
  provider: string | null;
  model: string | null;
  difficulty: string | null;
  status: RuntimeAgentStatus;
  createdAt: string | null;
  updatedAt: string | null;
  closedAt: string | null;
  routeWarnings: string[];
};

/**
 * `GET /api/runtime/agent-control/agents` 的归一化结果。
 *
 * - `count` 保留**后端上报值**（可能大于 `agents.length`：后端按 limit 截断，
 *   或服务端另有过滤口径）；仅当后端未上报合法数字时才回退为数组长度；
 * - `source` 是后端标注的数据来源（`agent_control_agents` 注册表投影 /
 *   `agent_control_projection` 兼容投影），用于诊断，不作为 UI 语义；
 * - `limit` / `includeClosed` 回显本次请求口径，供「触顶提示」使用。
 */
export type RuntimeAgentCatalog = {
  agents: RuntimeAgentRecord[];
  count: number;
  source: string | null;
  limit: number | null;
  includeClosed: boolean;
};

/** close / resume 成功后的单条身份（`{agent: {...}}` 响应体）。 */
export type RuntimeAgentMutation = {
  agent: RuntimeAgentRecord;
};
