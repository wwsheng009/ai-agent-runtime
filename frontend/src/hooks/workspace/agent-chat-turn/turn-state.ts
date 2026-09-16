// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import type { Thread } from "@/data/mock";
import type {
  AgentChatResult,
  AgentChatStreamChunkPayload,
} from "@/types/runtime";

/** 单轮 agent chat 的可变运行时状态：SSE 消费与终态收敛共享同一实例。 */
export type ChatTurnRuntimeState = {
  currentSessionId: string;
  currentSource: string;
  currentKind: string;
  streamedText: string;
  reasoningText: string;
  /**
   * 分块推理：一个回合里推理与工具是**交替**出现的（推理 → 工具 → 工具 → 推理 → …），
   * `reasoningBlocks[k]` 是第 k 块推理的完整文本，`reasoningBlockToolCounts[k]` 是这一块
   * 开始时已经出现过的工具帧数（即它该落在第几个工具行之后）。
   *
   * `reasoningText` 仍是整轮拼接（定稿协调 / 兜底用）；渲染用的块结构以本字段为准，
   * 否则「推理 → 工具 → 推理」在结构提交时会被重建回一段（页面上只剩一段推理）。
   */
  reasoningBlocks: string[];
  reasoningBlockToolCounts: number[];
  /** 已出现过的工具帧数：推理块边界的计量单位（见 `reasoningBlockToolCounts`）。 */
  toolFrameCount: number;
  /**
   * 推理段是否仍在生成。渲染层此前把它写死为 true（见 setStreamingMessage），
   * 于是整轮回复期间推理行都挂着「推理中…」转圈，直到终态快照才结束——
   * 模型进入工具/正文阶段后并不会自动收尾。改由 SSE 边界维护：推理增量置真，
   * 首个正文分片或首个工具帧置假。
   */
  reasoningRunning: boolean;
  planningPayload: Record<string, unknown> | null;
  orchestrationPayload: Record<string, unknown> | null;
  routePayload: Record<string, unknown> | null;
  observationPayloads: Record<string, unknown>[];
  subagentPayloads: Record<string, unknown>[];
  toolPayloads: AgentChatStreamChunkPayload[];
  finalResult: AgentChatResult | null;
  turnFinalized: boolean;
  receivedRuntimeActivity: boolean;
  receivedErrorEvent: boolean;
  connectTimedOut: boolean;
};

export function createTurnRuntimeState(
  threadSnapshot: Thread,
): ChatTurnRuntimeState {
  return {
    currentSessionId: threadSnapshot.sessionId ?? "",
    currentSource: threadSnapshot.runtimeSource ?? "runtime",
    currentKind: "llm",
    streamedText: "",
    reasoningText: "",
    reasoningBlocks: [],
    reasoningBlockToolCounts: [],
    toolFrameCount: 0,
    reasoningRunning: false,
    planningPayload: null,
    orchestrationPayload: null,
    routePayload: null,
    observationPayloads: [],
    subagentPayloads: [],
    toolPayloads: [],
    finalResult: null,
    turnFinalized: false,
    receivedRuntimeActivity: false,
    receivedErrorEvent: false,
    connectTimedOut: false,
  };
}

/**
 * 追加一段推理增量，返回本增量是否**新开了一块**。
 *
 * 工具帧出现即视为把当前块关上：工具执行完之后到达的推理文本属于新的一块。live
 * 层与结构快照都按块寻址，因此这里同时维护 `reasoningBlocks` 与整轮拼接的
 * `reasoningText`（后者只用于定稿协调 / 兜底）。
 */
export function appendReasoningDeltaToTurn(
  state: ChatTurnRuntimeState,
  delta: string,
): boolean {
  const lastBlockIndex = state.reasoningBlocks.length - 1;
  const blockStart =
    lastBlockIndex < 0 ||
    state.reasoningBlockToolCounts[lastBlockIndex] !== state.toolFrameCount;
  if (blockStart) {
    state.reasoningBlocks.push(delta);
    state.reasoningBlockToolCounts.push(state.toolFrameCount);
  } else {
    state.reasoningBlocks[lastBlockIndex] += delta;
  }
  state.reasoningText += delta;
  return blockStart;
}
