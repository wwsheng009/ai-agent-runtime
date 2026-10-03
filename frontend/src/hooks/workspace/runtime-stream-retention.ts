// 运行时流的内存收尾辅助（P0/P1 治理，从 use-session-runtime-stream 抽出）。
//
// 三件事：
// 1. 回合终态帧判定（runtime 原生终态 ∪ chat.sse.* 终态）与「仍在 streaming 的
//    助手消息 id」收集——runtime 通道没有 chat-turn 的 finalize 路径，不清理时
//    live 文本记录（整段正文 + 推理）会永久驻留并持续覆盖 store 副本；
// 2. 终态提交兑现后统一清 live 文本（与 finalize-turn 同口径：先写 store 再清）；
// 3. 挂起提交队列上限：隐藏标签页零工作策略下事件持续入队，不封顶会随隐藏时长
//    无界增长；超限优先丢弃最旧的纯增量条目（其 live 副作用已在事件到达时同步
//    写入，不会丢文本），持久事件保留。

import { type Thread } from "@/data/mock";
import { clearLiveStreamText } from "@/lib/live-stream-text";
import { type SessionRuntimeEvent } from "@/lib/runtime-api";
import { isSessionTurnTerminalEvent } from "@/lib/session-runtime/entry";
import { isChatSseTerminalFrame } from "@/lib/thread-state/events-live";
import { getRuntimeEventTurnId } from "@/lib/workspace-thread-state";

/** 挂起提交队列上限（见文件头第 3 条）。 */
export const MAX_PENDING_RUNTIME_COMMITS = 200;

/** 回合终态帧判定（类型大小写不敏感）。 */
export function isFinalizedTurnFrame(event: SessionRuntimeEvent): boolean {
  return (
    isSessionTurnTerminalEvent(event.type) ||
    isChatSseTerminalFrame(event.type)
  );
}

/**
 * 终态帧触达时仍在 streaming 的助手消息 id（归属口径与
 * `finalizeRuntimeTurnInThread` 一致：两侧都明确且不一致才跳过）。
 */
export function collectFinalizedLiveMessageIds(
  thread: Thread | undefined,
  event: SessionRuntimeEvent,
): string[] {
  const eventTurn = getRuntimeEventTurnId(event);
  const ids: string[] = [];
  for (const message of thread?.messages ?? []) {
    if (
      message.role === "assistant" &&
      message.streaming === true &&
      (!message.runtimeTurnId || !eventTurn || message.runtimeTurnId === eventTurn)
    ) {
      ids.push(message.id);
    }
  }
  return ids;
}

/** 终态提交兑现后清 live 文本记录（调用方必须先 flush 挂起提交）。 */
export function clearFinalizedLiveStreamText(
  messageIds: readonly string[],
): void {
  for (const messageId of messageIds) {
    clearLiveStreamText(messageId);
  }
}

/** 挂起提交队列超限淘汰：优先最旧的纯增量条目；整队皆持久事件时丢弃最旧条目保底。 */
export function trimPendingRuntimeCommits<
  T extends { shouldApplyLiveDelta: boolean },
>(queue: T[]): void {
  if (queue.length <= MAX_PENDING_RUNTIME_COMMITS) {
    return;
  }
  const droppableIndex = queue.findIndex((item) => item.shouldApplyLiveDelta);
  queue.splice(droppableIndex >= 0 ? droppableIndex : 0, 1);
}
