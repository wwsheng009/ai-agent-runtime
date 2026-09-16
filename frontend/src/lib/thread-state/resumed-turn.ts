// P4-刷新续传：把「刷新时仍在本会话执行的在途回合」挂载回新页面的线程。
//
// 背景（用户可见症状）：刷新会 abort 在途的 `/api/agent/chat` POST。旧语义下服务端
// 回合随 `r.Context()` 取消而中止，刷新后的页面只剩一份被截断的助手消息，而
// `/runtime/stream` 虽然按游标自动重连，却因为本地没有任何在途回合身份
// （`renderLiveDeltas` 闸门关闭、`matchesActiveTurn` 无参照）把落库的增量帧全部
// 过滤掉——用户看到的就是「刷新后整个 SSE live 断开」。
//
// 服务端那半（`session_active_turn.go` / handler.go 的 `resume_on_disconnect`）：
// 客户端断开后回合继续执行，历史与增量照常落库，并通过 `GET /runtime` 的
// `active_turn` 告诉新页面「还有回合在跑、它叫什么」。
//
// 本文件是前端那半：把这条身份挂到线程上。核心是**认领**服务端中途 checkpoint
// 落库的那条半截助手消息（`persistAgentChatTurnHistory` 在回合中写权威转录），
// 就地转成 streaming，后续增量继续写同一条消息——否则增量起来时会另建一条
// `turn-<turnId>-assistant` 占位，同一轮答复被劈成「刷新前的半截 + 刷新后的余下」，
// 且要等下一次权威历史同步才可能收敛。
//
// 安全网：认领只改 `streaming` / `runtimeTurnId` 两个标记，正文永远由消息自己
// 承载；即便认领到的是上一回合已定稿的助手消息（刷新早于本回合首次中途落库的
// 窗口），下一次权威历史同步也会按 message_id 把该消息的正文恢复为落库版本，
// 不会把新回合的文本永久写进上一回合的答复。

import { type ChatMessage, type Thread } from "@/data/mock";
import { isSystemPromptMessage } from "@/lib/chat-view/project";

function normalizeTurnId(turnId: string | null | undefined): string {
  return turnId?.trim() ?? "";
}

/** 该回合是否已经挂在本线程上（streaming 或已认领过）。 */
export function threadHoldsResumedTurn(
  thread: Thread | undefined,
  turnId: string | null | undefined,
): boolean {
  const normalized = normalizeTurnId(turnId);
  if (!thread || !normalized) {
    return false;
  }
  return thread.messages.some(
    (message) => normalizeTurnId(message.runtimeTurnId) === normalized,
  );
}

/**
 * 把服务端在途回合认领到线程末尾那条尚未定稿的助手消息上（幂等）。
 *
 * 认领条件（缺一不可）：
 * - 线程末尾就是助手消息：权威历史里本回合的 user 消息先落库，半截答复随后；
 *   末尾是 user 消息说明本回合还没有可续写的正文，此时应保持「等增量补建占位」
 *   的旧行为；
 * - 该消息没有 `runtimeTurnId`：已经被别的回合认领过（含上一轮续传）就不抢；
 * - 未定稿：`streaming === false` 是显式定稿标记（本地回合收尾 / 增量定稿写入，
 *   与 `live-assistant-target` 的判定口径一致——那里也把 `streaming === false`
 *   视为不可写的 live 目标），`interrupted` 是用户主动中止的上一回合，都不能续写。
 *
 * 不做的事：不改正文、不改 label/author（保持权威历史的展示口径），只补两个
 * 在途标记——`isLiveAssistantMessage` / `isThreadResponding` / 增量定位都按它们
 * 判定，因此认领后这一帧的增量会直接续写同一条消息。
 */
export function adoptResumedTurnInThread(
  thread: Thread,
  turnId: string | null | undefined,
): Thread {
  const normalized = normalizeTurnId(turnId);
  if (!normalized || thread.messages.length === 0) {
    return thread;
  }
  const index = thread.messages.length - 1;
  const tail = thread.messages[index];

  // 已认领（同回合幂等）：只在标记丢失时补回 streaming。
  if (normalizeTurnId(tail.runtimeTurnId) === normalized) {
    if (tail.streaming === true || tail.interrupted) {
      return thread;
    }
    return replaceMessage(thread, index, { ...tail, streaming: true });
  }

  if (
    tail.role !== "assistant" ||
    // 提示基础设施行（System prompt / 系统提示词卡片）在形状上与「未定稿的助手
    // 消息」完全一致（role=assistant、无 runtimeTurnId、未置 streaming），但它的
    // 渲染口径是折叠标题而不是 segments 正文。认领它 = 把整轮增量写进一张永远
    // 不显示正文的卡片（实测刷新后正文涨到 8k 字符、DOM 停在 16 字符不动）。
    // 跳过它，第一帧会按既有回退路径补建 `turn-<turnId>-assistant` 占位
    // （见 live-assistant-target.ts 的 createInFlightAssistantTarget），增量才
    // 落在可见气泡上。
    isSystemPromptMessage(tail) ||
    tail.streaming === true ||
    tail.streaming === false ||
    tail.interrupted ||
    normalizeTurnId(tail.runtimeTurnId)
  ) {
    return thread;
  }

  return replaceMessage(thread, index, {
    ...tail,
    streaming: true,
    runtimeTurnId: normalized,
  });
}

/**
 * 撤销续传挂载：回合结束（`/runtime` 的 `active_turn` 消失或换成别的回合）时清掉
 * streaming，避免刷新页面上的气泡永远转圈。正文与 runtimeTurnId 都保留——回合身份
 * 是后续「同回合的迟到帧仍写这条消息」的钥匙，正文则由权威历史同步接管。
 */
export function releaseResumedTurnInThread(
  thread: Thread,
  turnId: string | null | undefined,
): Thread {
  const normalized = normalizeTurnId(turnId);
  if (!normalized) {
    return thread;
  }
  let changed = false;
  const messages = thread.messages.map((message) => {
    if (normalizeTurnId(message.runtimeTurnId) !== normalized) {
      return message;
    }
    if (message.streaming !== true) {
      return message;
    }
    changed = true;
    return { ...message, streaming: false };
  });
  return changed ? { ...thread, messages } : thread;
}

function replaceMessage(
  thread: Thread,
  index: number,
  message: ChatMessage,
): Thread {
  const messages = thread.messages.slice();
  messages[index] = message;
  return { ...thread, messages };
}
