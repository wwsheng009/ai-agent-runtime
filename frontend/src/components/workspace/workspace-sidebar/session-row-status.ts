// P1-9 会话列表状态与整理：行状态 / 归档可见性纯函数（不依赖 React，便于单测）。
// 状态来源：会话快照（RuntimeSessionRecord.state）+ 本地流状态（可选合并，见 SidebarSessionActivity）。

import { type RuntimeSessionRecord } from "@/lib/runtime-api";

export type SidebarSessionRowState = "active" | "archived" | "closed";

/** 会话行状态（等待类最醒目，运行中次之，归档/关闭为静默态）。 */
export type SidebarSessionRowStatusKind =
  | "waitingApproval"
  | "planPending"
  | "waitingAnswer"
  | "running"
  | "subagents"
  | "archived"
  | "closed"
  | "idle";

export type SidebarSessionActivity = {
  /** 挂起中的审批请求数（等待类）。 */
  pendingApprovals?: number;
  /** 计划评审待确认（等待类）。 */
  planPending?: boolean;
  /** 等待用户回答的提问（等待类）。 */
  waitingAnswer?: boolean;
  /** 本地已知的运行中子代理数量。 */
  runningAgents?: number;
  /** 主流程正在生成/运行。 */
  running?: boolean;
};

export type SidebarSessionRowStatus = {
  kind: SidebarSessionRowStatusKind;
  /** 是否需要用户注意（等待类为 true，用于「哪个在等我」的一眼识别）。 */
  waiting: boolean;
};

export function normalizeRuntimeSessionState(
  session: Pick<RuntimeSessionRecord, "state">,
): string {
  return (session.state ?? "").trim().toLowerCase();
}

export function isArchivedRuntimeSession(
  session: Pick<RuntimeSessionRecord, "state">,
): boolean {
  return normalizeRuntimeSessionState(session) === "archived";
}

export function isClosedRuntimeSession(
  session: Pick<RuntimeSessionRecord, "state">,
): boolean {
  return normalizeRuntimeSessionState(session) === "closed";
}

export function resolveSidebarSessionRowState(
  session: Pick<RuntimeSessionRecord, "state">,
): SidebarSessionRowState {
  if (isArchivedRuntimeSession(session)) {
    return "archived";
  }
  if (isClosedRuntimeSession(session)) {
    return "closed";
  }
  return "active";
}

/**
 * 状态优先级：等待审批 > 计划待审 > 等待回答 > 运行中 > 子代理 > 归档/关闭 > 空闲。
 * 快照状态（archived/closed）优先于本地活动，避免已归档会话仍显示「在等我」。
 */
export function resolveSidebarSessionRowStatus(
  session: Pick<RuntimeSessionRecord, "state">,
  activity?: SidebarSessionActivity,
): SidebarSessionRowStatus {
  const rowState = resolveSidebarSessionRowState(session);
  if (rowState === "archived") {
    return { kind: "archived", waiting: false };
  }
  if (rowState === "closed") {
    return { kind: "closed", waiting: false };
  }

  if ((activity?.pendingApprovals ?? 0) > 0) {
    return { kind: "waitingApproval", waiting: true };
  }
  if (activity?.planPending) {
    return { kind: "planPending", waiting: true };
  }
  if (activity?.waitingAnswer) {
    return { kind: "waitingAnswer", waiting: true };
  }
  if (activity?.running) {
    return { kind: "running", waiting: false };
  }
  if ((activity?.runningAgents ?? 0) > 0) {
    return { kind: "subagents", waiting: false };
  }
  return { kind: "idle", waiting: false };
}

export type RuntimeSessionVisibilityOptions = {
  /** 是否显示归档会话（默认隐藏，可恢复）。 */
  showArchived: boolean;
};

export type RuntimeSessionVisibility = {
  /** 列表中应当渲染的会话。 */
  visible: RuntimeSessionRecord[];
  /** 被隐藏的归档会话。 */
  archived: RuntimeSessionRecord[];
  /** 被隐藏的归档数量（用于「显示归档 (N)」入口）。 */
  hiddenArchivedCount: number;
};

export function splitRuntimeSessionsByVisibility(
  sessions: RuntimeSessionRecord[],
  options: RuntimeSessionVisibilityOptions,
): RuntimeSessionVisibility {
  const archived = sessions.filter(isArchivedRuntimeSession);
  const visible = options.showArchived
    ? sessions
    : sessions.filter((session) => !isArchivedRuntimeSession(session));

  return {
    visible,
    archived,
    hiddenArchivedCount: options.showArchived ? 0 : archived.length,
  };
}

/**
 * 把「当前会话」的本地已知信号（运行中 / 待交互）投影为侧栏活动快照。
 * 侧栏只消费键值对，来源可以是本地流派生（本函数）或未来的全局事件源；
 * 无任何信号时返回空对象，避免给无关会话渲染伪造状态。
 */
export function buildSidebarSessionActivity(input: {
  sessionId?: string;
  pendingInteractionKind?: "approval" | "question" | "plan_review" | null;
  responding?: boolean;
  runningAgents?: number;
}): Record<string, SidebarSessionActivity> {
  const sessionId = input.sessionId?.trim();
  if (!sessionId) {
    return {};
  }

  const activity: SidebarSessionActivity = {};
  if (input.pendingInteractionKind === "approval") {
    activity.pendingApprovals = 1;
  } else if (input.pendingInteractionKind === "plan_review") {
    activity.planPending = true;
  } else if (input.pendingInteractionKind === "question") {
    activity.waitingAnswer = true;
  }
  if (input.responding) {
    activity.running = true;
  }
  if ((input.runningAgents ?? 0) > 0) {
    activity.runningAgents = input.runningAgents;
  }

  return Object.keys(activity).length > 0 ? { [sessionId]: activity } : {};
}
