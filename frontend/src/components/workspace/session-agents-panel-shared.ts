// P2-1A：子代理控制面面板的纯派生函数（与组件分离，便于单测）。
//
// 纪律：
//   * 展示名只做「昵称 → 类型 → agent_id」的如实回退，不臆造角色名；
//   * 动作可用性只看后端上报的精确状态：`active|stale` 可停，
//     `closed` 可恢复，`unknown` **不提供任何动作**（不知道就别动）；
//   * 分区沿用 jobs 面板口径（运行中 / 已结束），不按数组下标分页。

import type { RuntimeAgentRecord, RuntimeAgentStatus } from "@/types/runtime";

export type SessionAgentSplit = {
  /** 仍在跑（含失联 stale，需人工处置）。 */
  running: RuntimeAgentRecord[];
  /** 已结束（closed / unknown）。 */
  settled: RuntimeAgentRecord[];
};

/** 展示名：nickname → agentType → agentId。 */
export function agentDisplayName(agent: RuntimeAgentRecord): string {
  return agent.nickname ?? agent.agentType ?? agent.agentId;
}

export function agentStatusLabelKey(status: RuntimeAgentStatus): string {
  return `panels.agents.status.${status}`;
}

export function agentStatusToneClass(status: RuntimeAgentStatus): string {
  switch (status) {
    case "active":
      return "text-accent-primary";
    case "stale":
      return "text-analytics-warning";
    case "closed":
      return "text-muted-foreground";
    default:
      return "text-muted-foreground";
  }
}

/** active / stale 视为「运行中」（stale 是失联但未关闭，须人工干预）。 */
export function isAgentRunning(status: RuntimeAgentStatus): boolean {
  return status === "active" || status === "stale";
}

/** 只有精确的 active / stale 才允许 Stop；unknown 不给动作。 */
export function canStopAgent(status: RuntimeAgentStatus): boolean {
  return status === "active" || status === "stale";
}

/** 只有精确的 closed 才允许 Resume。 */
export function canResumeAgent(status: RuntimeAgentStatus): boolean {
  return status === "closed";
}

export function splitSessionAgents(agents: RuntimeAgentRecord[]): SessionAgentSplit {
  const running: RuntimeAgentRecord[] = [];
  const settled: RuntimeAgentRecord[] = [];
  for (const agent of agents) {
    if (isAgentRunning(agent.status)) {
      running.push(agent);
    } else {
      settled.push(agent);
    }
  }
  return { running, settled };
}

/** agent_path → 面包屑片段（空段剔除；无路径返回 []）。 */
export function agentPathSegments(agentPath: string | null): string[] {
  if (!agentPath) {
    return [];
  }
  return agentPath
    .split("/")
    .map((segment) => segment.trim())
    .filter((segment) => segment !== "");
}

export type AgentMetaFact = {
  key: "model" | "provider" | "workflow" | "team" | "warnings";
  value: string;
};

/**
 * 行内元数据事实：只上报后端真实带回来的字段，缺啥不显示啥
 * （不写「默认模型」之类推断）。
 */
export function agentMetaFacts(agent: RuntimeAgentRecord): AgentMetaFact[] {
  const facts: AgentMetaFact[] = [];
  if (agent.model) {
    facts.push({ key: "model", value: agent.model });
  }
  if (agent.provider) {
    facts.push({ key: "provider", value: agent.provider });
  }
  if (agent.workflow) {
    facts.push({ key: "workflow", value: agent.workflow });
  }
  if (agent.teamId) {
    facts.push({ key: "team", value: agent.teammateId || agent.teamId });
  }
  if (agent.routeWarnings.length > 0) {
    facts.push({ key: "warnings", value: String(agent.routeWarnings.length) });
  }
  return facts;
}
