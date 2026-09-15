/**
 * 批次 2（§5.4）：会话分支锚点可用性 —— 纯逻辑，在整段消息历史上求解**每个已完成轮次**的锚点。
 *
 * 语义（对齐后端 `ListUserTurns` 的轮边界，`backend/internal/chat/turns.go`）：
 * 1. 流式输出中 / 等待审批中一律不可用（本轮上下文仍在演进，前缀不是快照）；
 * 2. 锚点 = 每个已完成轮次的**末条消息**：轮从 `role="user"` 开始，到下一条 user 消息
 *    前结束（`EndMessageIndex` 排他），因此「该消息是其所在轮的末尾」⇔「其后第一条消息
 *    是 user，或它已是历史末尾」。早期实现只取整条 transcript 的末尾（单锚点），
 *    把中间轮次误判为不可用；后端 `branch.go` 本就接受任一已完成轮次的末条消息，
 *    409 只针对轮内中间消息。
 * 3. 锚点必须是可寻址且可复现的真实回答消息：
 *    - 合成退化 id（`history-mapping.ts` 的 `` `${sessionId}-history-${index}` ``）在服务端
 *      没有对应 `message_id`；`streaming` / `interrupted` 的实时消息是
 *      `history-artifacts.ts:33-41` 追加在权威历史之后的 live-only 产物（用户中止回合时
 *      后端只落盘了用户消息），二者都排除；
 *    - system prompt / 历史上下文 / 工具回执行不产出 `turn-tail`（`flow.ts:219-269`），排除；
 *    - 只有推理或只有工具调用的消息没有可复现的正文，即使恰好在轮末尾也不作为锚点
 *      （渲染层对非锚点不渲染「禁用但常驻」的入口，这类轮次整体没有分支按钮）。
 *
 * 为什么必须整条历史一次求解：live-only 消息是**追加在历史之后**的，尾部上下文决定哪些
 * 消息已定型；下发给行组件逐行自判会退化成 O(n²) 且漏判实时边界。
 */
import { type ChatMessage } from "@/data/mock";

import { isContextMessage, isToolReceiptMessage } from "./flow";
import { isSystemPromptMessage } from "./project";
import { answerTextOf, hasVisibleText } from "./visible-text";

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

/** 消息是否是所在轮的末尾（后端轮边界：下一条 user 消息开启新轮）。 */
export function isTurnTailMessage(
  messages: readonly ChatMessage[],
  index: number,
): boolean {
  const next = messages[index + 1];
  return !next || next.role === "user";
}

/** 能否作为回答锚点：角色 + 消息分类（system / context / 工具回执行不产出 turn-tail）。 */
function isAnswerAnchorMessage(message: ChatMessage): boolean {
  if (message.role !== "assistant") {
    return false;
  }
  return (
    !isSystemPromptMessage(message) &&
    !isContextMessage(message) &&
    !isToolReceiptMessage(message)
  );
}

/**
 * 求当前历史里所有可分支锚点的消息 id（整条历史一次求解，渲染层按 id 下发入口）。
 *
 * 不可用的消息**不进入结果集**：渲染层据此不渲染按钮，而不是渲染一颗禁用图标 ——
 * 入口只在能真正分支的消息尾部出现（含「只有推理的消息不出现」）。
 */
export function resolveBranchAnchors(
  messages: readonly ChatMessage[],
  options: BranchAnchorOptions,
): Set<string> {
  if (options.isResponding || options.hasPendingApproval) {
    return new Set();
  }

  const anchors = new Set<string>();
  messages.forEach((message, index) => {
    if (!isAnswerAnchorMessage(message)) {
      return;
    }
    // live-only 边界：正在流式 / 被中断的消息尚未（或不会）成为服务端历史。
    if (message.streaming === true || message.interrupted === true) {
      return;
    }
    // 轮内中间消息不是锚点（后端 409）：其后还有本轮的其它消息。
    if (!isTurnTailMessage(messages, index)) {
      return;
    }
    // 纯推理 / 纯工具调用没有可复现正文：入口不出现（文案与复制入口同口径）。
    if (!hasVisibleText(answerTextOf(message))) {
      return;
    }
    const messageId = typeof message.id === "string" ? message.id.trim() : "";
    if (!messageId || isSyntheticHistoryMessageId(messageId)) {
      return;
    }
    anchors.add(messageId);
  });

  return anchors;
}
