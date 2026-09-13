/**
 * P1-7：「待交互」（pending interaction）统一生命周期模型。
 *
 * 审批（approval）、提问（question）与计划评审（plan_review）三种形态共享同一条
 * 生命周期：注册 → 可取消（signal / 会话收敛）→ 结果回填；渲染层只消费统一的
 * 状态快照，不再各自维护 pending 判定。
 */

/** 交互形态：审批 / 提问 / 计划评审。 */
export type PendingInteractionKind = "approval" | "question" | "plan_review";

/**
 * 生命周期状态。
 * - `pending`：已注册，等待用户决定（可操作）。
 * - `resolving`：决定已投递，等待结果回填（不可重复提交）。
 * - `resolved`：结果已回填（`resolution` 说明是 allow / deny / answered / 计划决定）。
 * - `cancelled`：会话取消 / 断开 / 卸载时的优雅收敛（delegate-on-remove）。
 * - `expired`：后端 30min 超时终态（`approval_resolved.resolution === "expired"`）。
 */
export type PendingInteractionStatus =
  | "pending"
  | "resolving"
  | "resolved"
  | "cancelled"
  | "expired";

type PendingInteractionBase = {
  /** 稳定身份：审批 = request_id；提问 = question_id；计划评审 = plan-review:<sessionId>。 */
  id: string;
  status: PendingInteractionStatus;
  sessionId?: string;
  /** 注册时间（事件 timestamp 或本地兜底）。 */
  createdAt?: string;
  /** 关联 turn（事件 payload 的 turn_id）。 */
  turnId?: string;
  /** 结果回填说明（allow / deny / answered / approved / request_changes / quit …）。 */
  resolution?: string;
  /** 结果回填的失败原因（网络 / 后端拒绝），仅用于展示。 */
  error?: string;
};

export type PendingApprovalInteraction = PendingInteractionBase & {
  kind: "approval";
  toolName: string;
  reason: string;
  riskLevel: string;
  toolCallId?: string;
  /** ISO 时间；后端 30min 超时终态的唯一依据。 */
  expiresAt?: string;
};

export type PendingQuestionInteraction = PendingInteractionBase & {
  kind: "question";
  prompt: string;
  required: boolean;
  suggestions: string[];
};

export type PendingPlanReviewInteraction = PendingInteractionBase & {
  kind: "plan_review";
  planPath?: string;
  notes?: string;
};

export type PendingInteraction =
  | PendingApprovalInteraction
  | PendingQuestionInteraction
  | PendingPlanReviewInteraction;

/** 待交互快照（纯数据，渲染层只读）。 */
export type PendingInteractionState = {
  items: PendingInteraction[];
};

/** 会话收敛原因（delegate-on-remove / 断线 / 超时）。 */
export type PendingInteractionConvergeReason =
  | "session_end"
  | "session_interrupted"
  | "session_removed"
  | "stream_lost"
  | "expired";

export function emptyPendingInteractionState(): PendingInteractionState {
  return { items: [] };
}

/** 是否仍可作为「当前待交互」呈现（pending / resolving）。 */
export function isActionablePendingInteraction(
  interaction: PendingInteraction,
): boolean {
  return (
    interaction.status === "pending" || interaction.status === "resolving"
  );
}

/**
 * 选择当前呈现的待交互：
 * 优先本会话的 pending（注册序稳定），无 pending 时回落 resolving（等待回填）。
 * 已 resolved / cancelled / expired 的条目不再出现在呈现位。
 */
export function selectPendingInteraction(
  state: PendingInteractionState,
  sessionId?: string,
): PendingInteraction | null {
  const scoped = state.items.filter(
    (item) =>
      isActionablePendingInteraction(item) &&
      (!sessionId || !item.sessionId || item.sessionId === sessionId),
  );
  return (
    scoped.find((item) => item.status === "pending") ??
    scoped.find((item) => item.status === "resolving") ??
    null
  );
}

/** 按身份取条目（测试 / 调试用）。 */
export function findPendingInteraction(
  state: PendingInteractionState,
  id: string,
): PendingInteraction | null {
  return state.items.find((item) => item.id === id) ?? null;
}
