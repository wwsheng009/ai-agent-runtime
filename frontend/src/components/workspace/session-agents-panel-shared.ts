// P2-1A：子代理控制面面板的纯派生函数（与组件分离，便于单测）。
//
// 纪律：
//   * 展示名只做「昵称 → 类型 → agent_id」的如实回退，不臆造角色名；
//   * 动作可用性只看后端上报的精确状态：`active|stale` 可停，
//     `closed` 可恢复，`unknown` **不提供任何动作**（不知道就别动）；
//   * 分区沿用 jobs 面板口径（运行中 / 已结束），不按数组下标分页。

import type { SubagentSessionTarget } from "@/components/workspace/trajectory/subagent-session-target";
import type { ParkedTurnTaskCounts } from "@/lib/parked-turn";
import type {
  RuntimeAgentDisplayStatus,
  RuntimeAgentRecord,
  RuntimeAgentStatus,
} from "@/types/runtime";

export type SessionAgentSplit = {
  /** 仍在跑（含失联 stale 与等待审批 / 输入——在跑但被人阻塞，需人工处置）。 */
  running: RuntimeAgentRecord[];
  /** 已结束（closed / unknown，以及容器已停但身份行还没关的 ended）。 */
  settled: RuntimeAgentRecord[];
};

/** 展示名：nickname → agentType → agentId。 */
export function agentDisplayName(agent: RuntimeAgentRecord): string {
  return agent.nickname ?? agent.agentType ?? agent.agentId;
}

export function agentStatusLabelKey(status: RuntimeAgentDisplayStatus): string {
  return `panels.agents.status.${status}`;
}

export function agentStatusToneClass(status: RuntimeAgentDisplayStatus): string {
  switch (status) {
    case "active":
      return "text-accent-primary";
    case "stale":
    case "waiting_approval":
    case "waiting_input":
      return "text-analytics-warning";
    case "closed":
    case "ended":
      return "text-muted-foreground";
    default:
      return "text-muted-foreground";
  }
}

/**
 * active / stale 视为「运行中」（stale 是失联但未关闭，须人工干预）。
 *
 * 等待审批 / 等待输入是「在跑但被人阻塞」，同样不能落进「已结束」分区；
 * `ended` / `closed` / `unknown` 才落到已结束分区。
 */
export function isAgentRunning(status: RuntimeAgentDisplayStatus): boolean {
  return (
    status === "active" ||
    status === "stale" ||
    status === "waiting_approval" ||
    status === "waiting_input"
  );
}

/** 根容器行（当前会话自己）：会话在轮次之间本来就是 idle，不能算「已结束」。 */
export function isRootAgentRecord(agent: RuntimeAgentRecord): boolean {
  return agent.agentType === "root";
}

/**
 * 展示状态：身份状态 + 运行态推导出的 `ended`。
 *
 * 收口纪律：
 *   * 身份终态 / 未知优先（`closed` / `stale` / `unknown` 原样透传）：
 *     `ended` 只描述「容器没在跑」，不得覆盖显式关闭与失联；
 *   * 根容器行不做 `ended` 收敛（轮次之间的 idle 不代表主代理结束）；
 *   * 等待审批 / 等待输入原样透出（在跑但被人阻塞），不得收敛成 `running`
 *     或 `ended`；
 *   * 运行态 `unknown`（后端未上报）→ 回退身份状态，**不臆断已结束**。
 *
 * 修复背景：后端身份行只有在显式 close / reclaim 时才变终态，子代理跑完一轮后
 * 会话行仍是 open → 身份行长期 `active`，面板会一直显示「运行中」。
 */
export function agentDisplayStatus(agent: RuntimeAgentRecord): RuntimeAgentDisplayStatus {
  if (agent.status !== "active") {
    return agent.status;
  }
  if (isRootAgentRecord(agent)) {
    return agent.status;
  }
  if (
    agent.runtimeState === "waiting_approval" ||
    agent.runtimeState === "waiting_input"
  ) {
    return agent.runtimeState;
  }
  if (agent.runtimeState === "idle" || agent.runtimeState === "stopped") {
    return "ended";
  }
  return agent.status;
}

/**
 * 身份行 → 子会话下钻目标（G8：会话 agents 面板的第二下钻入口）。
 *
 * 数据边界：只有后端上报了 `sessionId` 的行才给入口——`agentId` / `agentPath`
 * 是身份图的键，不是 `runtime/events` 的会话键，缺失时宁可不显示入口。
 * 标题栏口径与轨迹入口一致：优先稳定身份路径，回退 agentId；role 取身份类型；
 * status 用展示状态（含 `ended` 收敛），仅作标题栏兜底，不参与数据请求。
 */
export function agentTranscriptTarget(agent: RuntimeAgentRecord): SubagentSessionTarget | null {
  const sessionId = agent.sessionId?.trim();
  if (!sessionId) {
    return null;
  }
  return {
    sessionId,
    agentId: agent.agentPath?.trim() || agent.agentId,
    role: agent.agentType?.trim() || undefined,
    status: agentDisplayStatus(agent),
  };
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
    // 分区看**展示状态**：身份行仍是 active 但容器已结束的子代理属于「已结束」，
    // 否则面板会把跑完的子代理一直挂在「运行中」。
    if (isAgentRunning(agentDisplayStatus(agent))) {
      running.push(agent);
    } else {
      settled.push(agent);
    }
  }
  return { running, settled };
}

/**
 * §6.8 托管挂起：把身份行目录投影成「运行中 / 完成 / 异常」三档计数。
 *
 * 口径（只使用后端真实上报的字段，不猜）：
 *   * 运行中 = 展示状态 `active` / `waiting_approval` / `waiting_input`
 *     （容器仍在跑，含等人处置的等待态）；
 *   * 完成 = 展示状态 `closed` / `ended`（身份已关，或容器已停）；
 *   * 异常 = 展示状态 `stale`（失联但未关闭，需人工处置）；
 *   * `unknown`（后端未上报状态）**不归入任何一档**——三档之和允许小于目录
 *     行数，宁可少算也不把「未知」说成「异常」。
 *
 * 边界：这是身份行目录的投影，不是 obligation 的权威计数——挂起文案里的
 * 义务数仍以 `turn.suspended` 的 `obligation_count` 为准。
 */
export function projectParkedTurnTaskCounts(
  agents: readonly RuntimeAgentRecord[],
): ParkedTurnTaskCounts {
  let running = 0;
  let completed = 0;
  let failed = 0;
  for (const agent of agents) {
    const status = agentDisplayStatus(agent);
    if (
      status === "active" ||
      status === "waiting_approval" ||
      status === "waiting_input"
    ) {
      running += 1;
    } else if (status === "closed" || status === "ended") {
      completed += 1;
    } else if (status === "stale") {
      failed += 1;
    }
  }
  return { running, completed, failed };
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

/* ------------------------------------------------------------------ *
 * P2-9 子片 2：耗时格式化 / 子代理树 / 只读原因
 *
 * 数据事实（决定能力边界，勿凭空补字段）：
 *   * 后端身份行有 `created_at` / `updated_at` / `closed_at` → 可算「记录跨度」；
 *   * 后端**未上报**逐回合活跃时长、token 用量、one-shot 标记 → 这三项不做
 *     （目标实现的「总活跃耗时 / token / 一次性记录只读」在本仓库无数据源）。
 * ------------------------------------------------------------------ */

function parseAgentTime(value: string | null): number | null {
  if (!value) {
    return null;
  }
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : null;
}

export type AgentDurationSpan = {
  /** 跨度毫秒（终点 ≥ 起点）。 */
  ms: number;
  /** 起点 ISO（原样透传，供 title 展示）。 */
  from: string;
  /** 终点 ISO（原样透传，供 title 展示）。 */
  to: string;
};

/**
 * 记录跨度：`createdAt` → `closedAt ?? updatedAt`。
 *
 * 纪律：端点缺失 / 无法解析 / 终点早于起点 → `null`，UI **不显示、不推算、不补零**。
 * 口径：这是「记录跨度」而非「活跃耗时」（后端未上报活跃时长），
 * 文案与 title 必须如实标注，不得借用目标实现的 `duration.exactTitle` 说辞。
 */
export function agentDurationSpan(agent: RuntimeAgentRecord): AgentDurationSpan | null {
  const endIso = agent.closedAt ?? agent.updatedAt;
  const from = parseAgentTime(agent.createdAt);
  const to = parseAgentTime(endIso);
  if (from === null || to === null || to < from || !agent.createdAt || !endIso) {
    return null;
  }
  return { ms: to - from, from: agent.createdAt, to: endIso };
}

/**
 * 跨度文案（判别联合：每个档位的占位符类型固定，调用侧可静态取到 i18n 键与
 * 精确插值参数，避免 `t(key, values)` 的动态键类型逃逸）。
 */
export type AgentDurationLabel =
  | { key: "duration.seconds"; values: { seconds: string } }
  | { key: "duration.minutes"; values: { minutes: string; seconds: string } }
  | { key: "duration.hours"; values: { hours: string; minutes: string; seconds: string } }
  | { key: "duration.days"; values: { days: string } }
  | { key: "duration.daysHours"; values: { days: string; hours: string } }
  | { key: "duration.months"; values: { months: string } }
  | { key: "duration.monthsDays"; values: { months: string; days: string } }
  | { key: "duration.years"; values: { years: string } }
  | { key: "duration.yearsMonths"; values: { years: string; months: string } }
  | {
      key: "duration.exactDays";
      values: { days: string; hours: string; minutes: string; seconds: string };
    };

/** 与目标实现同梯度的跨度文案：秒 → 分 → 时 → 天 → 月 → 年（越粗精度越低）。 */
export function formatAgentDuration(ms: number): AgentDurationLabel | null {
  if (!Number.isFinite(ms) || ms < 0) {
    return null;
  }
  const totalSeconds = Math.floor(ms / 1000);
  const totalMinutes = Math.floor(totalSeconds / 60);
  const totalHours = Math.floor(totalMinutes / 60);
  const seconds = totalSeconds % 60;
  const minutes = totalMinutes % 60;
  const hours = totalHours % 24;
  const days = Math.floor(totalHours / 24);
  const pad = (value: number) => String(value).padStart(2, "0");
  const text = (value: number) => String(value);

  if (days >= 365) {
    const years = Math.floor(days / 365);
    const months = Math.floor((days % 365) / 30);
    return months === 0
      ? { key: "duration.years", values: { years: text(years) } }
      : { key: "duration.yearsMonths", values: { years: text(years), months: text(months) } };
  }
  if (days >= 30) {
    const months = Math.floor(days / 30);
    const remainingDays = days % 30;
    return remainingDays === 0
      ? { key: "duration.months", values: { months: text(months) } }
      : {
          key: "duration.monthsDays",
          values: { months: text(months), days: text(remainingDays) },
        };
  }
  if (days > 0) {
    return hours === 0
      ? { key: "duration.days", values: { days: text(days) } }
      : { key: "duration.daysHours", values: { days: text(days), hours: text(hours) } };
  }
  if (totalHours > 0) {
    return {
      key: "duration.hours",
      values: { hours: text(totalHours), minutes: pad(minutes), seconds: pad(seconds) },
    };
  }
  if (totalMinutes > 0) {
    return {
      key: "duration.minutes",
      values: { minutes: text(totalMinutes), seconds: pad(seconds) },
    };
  }
  return { key: "duration.seconds", values: { seconds: text(seconds) } };
}

/** 精确到秒的跨度（悬停 / 无障碍名称）；不足一天时与紧凑文案一致。 */
export function formatAgentDurationExact(ms: number): AgentDurationLabel | null {
  const compact = formatAgentDuration(ms);
  if (compact === null) {
    return null;
  }
  const totalSeconds = Math.floor(ms / 1000);
  const days = Math.floor(totalSeconds / 86400);
  if (days === 0) {
    return compact;
  }
  const pad = (value: number) => String(value).padStart(2, "0");
  return {
    key: "duration.exactDays",
    values: {
      days: String(days),
      hours: pad(Math.floor(totalSeconds / 3600) % 24),
      minutes: pad(Math.floor(totalSeconds / 60) % 60),
      seconds: pad(totalSeconds % 60),
    },
  };
}

export type AgentTreeNode = {
  agent: RuntimeAgentRecord;
  /** 树内派生的层级（0 = 当前会话的直接下级）。 */
  depth: number;
  children: AgentTreeNode[];
  /** 全后代数量（折叠文案用）。 */
  descendantCount: number;
  /** 解析出的父身份行；无（孤儿 / 顶层）为 null。 */
  parent: RuntimeAgentRecord | null;
};

export type AgentTreeRow = {
  agent: RuntimeAgentRecord;
  depth: number;
  parent: RuntimeAgentRecord | null;
  childCount: number;
  /** 折叠该节点会隐藏的末级后代数量。 */
  hiddenDescendantCount: number;
};

/**
 * 把扁平后代列表折成树。
 *
 * 解析顺序：`parentAgentId`（在集合内）→ `agentPath` 的最长前缀祖先（后端缺
 * `parent_agent_id` 时仍能还原层级）。两者都解析不到的身份行**保留为顶层节点**
 * （宁可显示成孤儿，也不丢弃），并做环检测：出现环时断开闭环那条边。
 */
export function buildAgentForest(descendants: RuntimeAgentRecord[]): AgentTreeNode[] {
  const byId = new Map<string, RuntimeAgentRecord>();
  const byPath = new Map<string, RuntimeAgentRecord>();
  for (const agent of descendants) {
    byId.set(agent.agentId, agent);
    if (agent.agentPath) {
      byPath.set(agent.agentPath, agent);
    }
  }

  const parentOf = new Map<string, RuntimeAgentRecord | null>();
  const resolveParent = (agent: RuntimeAgentRecord): RuntimeAgentRecord | null => {
    if (agent.parentAgentId) {
      const direct = byId.get(agent.parentAgentId);
      if (direct && direct.agentId !== agent.agentId) {
        return direct;
      }
    }
    let path = agent.agentPath ?? "";
    while (path.includes("/")) {
      path = path.slice(0, path.lastIndexOf("/"));
      if (!path) {
        break;
      }
      const candidate = byPath.get(path);
      if (candidate && candidate.agentId !== agent.agentId) {
        return candidate;
      }
    }
    return null;
  };
  const reaches = (start: RuntimeAgentRecord, target: string): boolean => {
    const seen = new Set<string>();
    let cursor: RuntimeAgentRecord | null | undefined = start;
    while (cursor && !seen.has(cursor.agentId)) {
      if (cursor.agentId === target) {
        return true;
      }
      seen.add(cursor.agentId);
      cursor = parentOf.get(cursor.agentId) ?? null;
    }
    return false;
  };

  for (const agent of descendants) {
    const resolved = resolveParent(agent);
    // 环检测：这条边若让父链绕回自己，则断开（该行退化为顶层）。
    parentOf.set(agent.agentId, resolved && !reaches(resolved, agent.agentId) ? resolved : null);
  }

  const nodes = new Map<string, AgentTreeNode>();
  for (const agent of descendants) {
    nodes.set(agent.agentId, {
      agent,
      depth: 0,
      children: [],
      descendantCount: 0,
      parent: parentOf.get(agent.agentId) ?? null,
    });
  }
  const roots: AgentTreeNode[] = [];
  for (const agent of descendants) {
    const node = nodes.get(agent.agentId)!;
    const parent = node.parent ? nodes.get(node.parent.agentId) : undefined;
    if (parent) {
      parent.children.push(node);
    } else {
      roots.push(node);
    }
  }

  const order = (list: AgentTreeNode[]): AgentTreeNode[] =>
    list.sort((a, b) => {
      const path = (a.agent.agentPath ?? "").localeCompare(b.agent.agentPath ?? "");
      return path !== 0 ? path : a.agent.agentId.localeCompare(b.agent.agentId);
    });

  const finalize = (node: AgentTreeNode, depth: number): number => {
    node.depth = depth;
    order(node.children);
    let total = 0;
    for (const child of node.children) {
      total += 1 + finalize(child, depth + 1);
    }
    node.descendantCount = total;
    return total;
  };
  order(roots);
  for (const root of roots) {
    finalize(root, 0);
  }
  return roots;
}

/** 按折叠集合摊平成可见行（前序）：被折叠节点的后代一律不产出。 */
export function flattenAgentTree(
  forest: AgentTreeNode[],
  collapsedIds: ReadonlySet<string> = new Set<string>(),
): AgentTreeRow[] {
  const rows: AgentTreeRow[] = [];
  const walk = (nodes: AgentTreeNode[]) => {
    for (const node of nodes) {
      rows.push({
        agent: node.agent,
        depth: node.depth,
        parent: node.parent,
        childCount: node.children.length,
        hiddenDescendantCount: node.descendantCount,
      });
      if (!collapsedIds.has(node.agent.agentId)) {
        walk(node.children);
      }
    }
  };
  walk(forest);
  return rows;
}

/**
 * 只读原因：只回答**能从数据证实**的两件事。
 *
 *   * `closed-record`：身份行已关闭 → 历史记录只读；
 *   * `parent-offline`：父身份行存在且不在 `active`（含 stale / closed / unknown）
 *     → 父会话不在线，此代理暂不可继续。
 * 其余（one-shot 记录 / 活跃耗时）后端未上报，本函数**不猜**，返回 null。
 */
export type AgentReadOnlyReason = "closed-record" | "parent-offline";

export function agentReadOnlyReason(
  agent: RuntimeAgentRecord,
  parent: RuntimeAgentRecord | null,
): AgentReadOnlyReason | null {
  if (agent.status === "closed") {
    return "closed-record";
  }
  if (parent && parent.status !== "active") {
    return "parent-offline";
  }
  return null;
}
