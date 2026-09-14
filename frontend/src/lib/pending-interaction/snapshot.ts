/**
 * P2-1A：运行时状态快照 → 待交互注册表重建（纯函数，无 React / 无 DOM）。
 *
 * 事件流（P1-7）只在「页面已打开且连接在线」期间维护注册表；快照是重载 /
 * 重连后的重建来源：
 * - 重建：`state.pending_approval` / `state.pending_question` 幂等注册（身份同
 *   `request_id` / `question_id`，与事件路径完全一致）；
 * - 回放保护：本地已 `resolving` 或终态（resolved / cancelled / expired）的条目
 *   不被快照回退为 pending（避免用陈旧快照复活已决交互）；
 * - 缺席结算：本地 pending 条目的创建时间早于快照 `updated_at` 而快照未列出它，
 *   说明后端已结算（如断线期间过期 / 被拒），本地收敛为 cancelled，卡片不悬挂；
 * - 超时守卫：`expires_at` 已过的审批置 `expired`（后端 30min 超时终态的本地兜底）。
 */

import type { RuntimeSessionState } from "@/types/runtime";

import {
  expirePendingInteractions,
  registerPendingInteraction,
  settlePendingInteraction,
} from "./events";
import type {
  PendingApprovalInteraction,
  PendingInteraction,
  PendingInteractionState,
  PendingQuestionInteraction,
} from "./types";
import { findPendingInteraction } from "./types";

/** 快照里的未决项 → 待交互条目（缺 id 不建入口，绝不伪造身份）。 */
export function pendingInteractionsFromRuntimeState(
  state: RuntimeSessionState | null | undefined,
): PendingInteraction[] {
  if (!state) {
    return [];
  }
  const sessionId = state.sessionId.trim();
  const createdAt = state.updatedAt;
  const items: PendingInteraction[] = [];

  const approval = state.pendingApproval;
  const approvalId = approval?.id.trim() ?? "";
  if (approval && approvalId) {
    const item: PendingApprovalInteraction = {
      kind: "approval",
      id: approvalId,
      status: "pending",
      ...(sessionId ? { sessionId } : {}),
      ...(createdAt ? { createdAt } : {}),
      toolName: approval.toolName,
      reason: approval.reason,
      riskLevel: approval.riskLevel,
      ...(approval.expiresAt ? { expiresAt: approval.expiresAt } : {}),
    };
    items.push(item);
  }

  const question = state.pendingQuestion;
  const questionId = question?.id.trim() ?? "";
  if (question && questionId) {
    const item: PendingQuestionInteraction = {
      kind: "question",
      id: questionId,
      status: "pending",
      ...(sessionId ? { sessionId } : {}),
      ...(createdAt ? { createdAt } : {}),
      prompt: question.prompt,
      required: question.required,
      suggestions: [...question.suggestions],
    };
    items.push(item);
  }

  return items;
}

export type HydratePendingInteractionsOptions = {
  /** 超时判定时钟（默认 `Date.now()`；测试可注入固定值）。 */
  now?: number;
};

/**
 * 用快照重建待交互注册表（幂等：同 id 重复水合结果稳定）。
 *
 * `state` 为空（404 / 未取到）时原样返回，不做清空 —— 事件流仍是主数据源。
 */
export function hydratePendingInteractions(
  current: PendingInteractionState,
  state: RuntimeSessionState | null | undefined,
  options: HydratePendingInteractionsOptions = {},
): PendingInteractionState {
  if (!state) {
    return current;
  }

  const fromSnapshot = pendingInteractionsFromRuntimeState(state);
  const presentIds = new Set(fromSnapshot.map((item) => item.id));
  const snapshotMs = state.updatedAt ? Date.parse(state.updatedAt) : Number.NaN;

  let next = current;
  if (Number.isFinite(snapshotMs)) {
    for (const item of current.items) {
      if (presentIds.has(item.id)) {
        continue;
      }
      if (!isAbsentSettled(item, state, snapshotMs)) {
        continue;
      }
      next = settlePendingInteraction(next, item.id, {
        status: "cancelled",
        resolution: "snapshot_absent",
      });
    }
  }

  for (const item of fromSnapshot) {
    const existing = findPendingInteraction(next, item.id);
    // 本地 resolving / 终态优先：陈旧快照不得回退状态或复活已决条目。
    if (existing && existing.status !== "pending") {
      continue;
    }
    next = registerPendingInteraction(next, item);
  }

  return expirePendingInteractions(next, options.now ?? Date.now());
}

/**
 * 缺席结算判定：本地仍 pending、属于该会话、且在快照生成时点之前就已创建，
 * 却未出现在快照里 → 后端已结算。`resolving` 不判定（决定在途，等回填）。
 */
function isAbsentSettled(
  item: PendingInteraction,
  state: RuntimeSessionState,
  snapshotMs: number,
): boolean {
  if (item.kind === "plan_review" || item.status !== "pending") {
    return false;
  }
  if (item.sessionId && state.sessionId && item.sessionId !== state.sessionId) {
    return false;
  }
  if (!item.createdAt) {
    return false;
  }
  const createdMs = Date.parse(item.createdAt);
  return Number.isFinite(createdMs) && createdMs < snapshotMs;
}
