// 由 lib/thread-state/events-live.ts 机械拆分而来（P0-2「单文件 ≤ 500 非空行」门禁），
// 仅搬迁不改语义。本文件承载「runtime live 帧 → 助手消息写入目标」的定位与准入：
// 1) 复用本回合仍在 streaming 的助手消息（isLiveAssistantMessage 判定）；
// 2) 断流 / 重放后按 turn 归属补建占位消息（createInFlightAssistantTarget）。

import { type ChatMessage, type Thread } from "@/data/mock";

import { createStreamingAssistantMessage } from "./events";

export function findLatestAssistantMessage(
  thread: Thread,
  predicate: (message: ChatMessage) => boolean = () => true,
) {
  for (let index = thread.messages.length - 1; index >= 0; index--) {
    const message = thread.messages[index];
    if (message.role !== "assistant" || !predicate(message)) {
      continue;
    }
    return { index, message };
  }

  return null;
}

/** 一帧的写入目标：目标消息 + 承载它的 thread（补建时 thread 已含新消息）。 */
export type LiveAssistantTarget = {
  thread: Thread;
  index: number;
  message: ChatMessage;
};

/**
 * 判定「帧的回合」是否就是本 thread 当前在途的回合：**两侧身份都明确且相等**才
 * 返回该 turn id，否则返回空串（归属不明）——补建消息的准入契约。
 *
 * 这里刻意比 `matchesActiveTurn` 严格：写**已存在**的消息时「未知」必须放行
 * （真后端 loop.go 只在 turnID != "" 时注入 turn_id，严格相等会让打字机整条丢
 * 失），但**新建**消息会改变对话结构：旧回合的帧若据它建消息，面板里会凭空多出
 * 一条空占位，因此必须确证。
 */
export function resolveInFlightTurnId(
  expectedTurnId: string | undefined,
  eventTurnId: string,
): string {
  const expected = expectedTurnId?.trim() ?? "";
  const eventTurn = eventTurnId.trim();
  return expected && eventTurn && expected === eventTurn ? eventTurn : "";
}

/**
 * 为「本 thread 在途回合」补建一条 streaming 助手占位消息，返回承载它的 thread。
 * 返回 null = 保持旧行为（帧被丢弃）：
 * - 回合归属不明（见 resolveInFlightTurnId）；
 * - 同 id 的消息已存在（直连通道的占位还在 / 该回合已定稿）：避免同 id 的重复消息
 *   （React key 与 updateThreadMessage 都按 id 定位）。
 * 新消息由 `createStreamingAssistantMessage` 构造（streaming + runtimeTurnId +
 * label "streaming"，id 与直连通道同形），因此后续帧仍会被
 * `isLiveAssistantMessage` 命中，正文 / 推理 / 工具行都继续落在这条消息上。
 */
export function createInFlightAssistantTarget(
  thread: Thread,
  expectedTurnId: string | undefined,
  eventTurnId: string,
): LiveAssistantTarget | null {
  const turnId = resolveInFlightTurnId(expectedTurnId, eventTurnId);
  if (!turnId) {
    return null;
  }
  const messageId = `turn-${turnId}-assistant`;
  if (thread.messages.some((message) => message.id === messageId)) {
    return null;
  }
  const message = createStreamingAssistantMessage(messageId, [], turnId);
  return {
    thread: { ...thread, messages: [...thread.messages, message] },
    index: thread.messages.length,
    message,
  };
}

/**
 * 定位一帧的写入目标：优先复用本回合仍在 streaming 的助手消息；没有时按「回合
 * 归属可判定 + allowCreate」补建占位消息（见 createInFlightAssistantTarget）。
 *
 * allowCreate=false 的帧只推进/收尾既有消息，不改变对话结构（阶段帧不携带可渲染
 * 内容；工具帧无 id 无真名时也落不了行）。
 */
export function resolveLiveAssistantTarget(
  thread: Thread,
  eventTurnId: string,
  expectedTurnId?: string,
  allowCreate = false,
): LiveAssistantTarget | null {
  const existing = findLatestAssistantMessage(thread, (message) =>
    isLiveAssistantMessage(message, eventTurnId),
  );
  if (existing) {
    return { thread, index: existing.index, message: existing.message };
  }
  return allowCreate ? createInFlightAssistantTarget(thread, expectedTurnId, eventTurnId) : null;
}

export function isLiveAssistantMessage(
  message: ChatMessage,
  eventTurnId: string,
): boolean {
  // New messages carry an explicit streaming bit. The label fallback keeps
  // compatibility with callers/tests created before the bit was introduced.
  if (message.streaming === false || message.interrupted) {
    return false;
  }
  if (message.streaming !== true && message.label !== "streaming") {
    return false;
  }
  // 与 matchesActiveTurn / RuntimeDeltaCoordinator.claim 共用同一语义：
  // 「未知」不等于「其他 turn」，只有两边都明确且不一致才拒绝。
  //
  // 旧实现要求「两边都为空」才放行，于是真后端最常见的两种形态——消息带
  // chat turn 身份而 runtime 事件缺 turn_id，或反过来（两条通道的 turn 身份
  // 空间本就不同）——会被整条拒绝：增量帧全部到达却一帧也写不进消息，
  // 打字机退化成「turn 结束后一次性定型」（实测两路各约 1000 帧、DOM 全程
  // 不动，24s 时整块蹦出）。回放安全由 hook 层 renderLiveDeltas 闸门兜底：
  // 只在请求进行中应用增量，历史回放/reload 不走这条路径。
  if (message.runtimeTurnId && eventTurnId) {
    return message.runtimeTurnId === eventTurnId;
  }
  return true;
}
