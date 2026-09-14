/**
 * 批次 2（§5.4）：会话分支锚点可用性 —— 纯逻辑，在整条 flow 上求解**唯一**可分支锚点。
 *
 * 语义（对齐参照实现的「completed transcript tail」，`apps/web/tests/message-actions.e2e.ts:190-203`）：
 * 1. 流式输出中 / 等待审批中一律不可用（本轮上下文仍在演进，前缀不是快照）；
 * 2. 锚点 = **transcript 末尾**那一轮的轮末尾消息。`projectMessageFlow` 恒把 `turn-tail`
 *    追加为所属消息的最后一个 item（`flow.ts:299-304`），因此「流末尾 item 是 `turn-tail`」
 *    等价于「末尾是已完成轮次的最后一条内容消息，其后没有该轮新增内容」——末尾若是用户消息
 *    （该轮尚未回答）、工具回执或上下文行，都不产生锚点；
 * 3. 锚点必须是可寻址的真实消息：合成退化 id（`history-mapping.ts:264` 的
 *    `` `${sessionId}-history-${index}` ``）在服务端没有对应 `message_id`；`streaming` /
 *    `interrupted` 的实时消息则是 `history-artifacts.ts:33-41` 追加在权威历史之后的 live-only
 *    产物（用户中止回合时后端只落盘了用户消息），二者都排除。
 *
 * 为什么必须在整条流上求一次：live-only 消息是**追加在历史之后**的，尾部上下文决定
 * 「谁才是最后一条」；下发给行组件逐行自判既退化成 O(n²)，也会漏判这种实时边界。
 */
import type { ChatFlowItem } from "./flow";

/** 不可用原因文案键（`workspace` 命名空间下）；i18n 资源已就位，渲染层只做 `t(reasonKey)`。 */
export const BRANCH_UNAVAILABLE_REASON_KEY =
  "panels.messages.branch.unavailable";

/** 字面量键类型：i18n 资源是 `satisfies` 对齐的强类型字典，`t()` 只接受已知键。 */
export type BranchUnavailableReasonKey = typeof BRANCH_UNAVAILABLE_REASON_KEY;

export type BranchAvailability =
  | { kind: "available"; messageId: string }
  | { kind: "unavailable"; reasonKey: BranchUnavailableReasonKey };

export type BranchAnchorOptions = {
  /** 会话是否仍在流式输出（含等待首块）。 */
  isResponding: boolean;
  /** 是否有挂起的审批 / 提问 / 计划评审；挂起期间本轮未定型，不可分支。 */
  hasPendingApproval?: boolean;
};

/** 合成退化 id（`` `${sessionId}-history-${index}` ``）不是服务端可寻址的消息 id。 */
export function isSyntheticHistoryMessageId(messageId: string): boolean {
  return /-history-\d+$/.test(messageId.trim());
}

function unavailable(): BranchAvailability {
  return { kind: "unavailable", reasonKey: BRANCH_UNAVAILABLE_REASON_KEY };
}

/**
 * 求当前 flow 的可分支锚点（整条流一次求解，渲染层把结果下发给每一行）。
 *
 * 复杂度：O(1) 额外工作（`turn-tail` 恒为所属消息的末项，故只需看末尾 item）；
 * 若上游将来改变 flow 形状，改动点收敛在本函数。
 */
export function resolveBranchAnchor(
  items: readonly ChatFlowItem[],
  options: BranchAnchorOptions,
): BranchAvailability {
  if (options.isResponding || options.hasPendingApproval) {
    return unavailable();
  }

  const tail = items[items.length - 1];
  if (!tail || tail.kind !== "turn-tail") {
    return unavailable();
  }

  const { message } = tail;
  // live-only 边界：正在流式 / 被中断的消息尚未（或不会）成为服务端历史。
  if (message.streaming === true || message.interrupted === true) {
    return unavailable();
  }

  const messageId = typeof message.id === "string" ? message.id.trim() : "";
  if (!messageId || isSyntheticHistoryMessageId(messageId)) {
    return unavailable();
  }

  return { kind: "available", messageId };
}
