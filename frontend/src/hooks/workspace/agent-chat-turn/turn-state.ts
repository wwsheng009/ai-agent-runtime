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
