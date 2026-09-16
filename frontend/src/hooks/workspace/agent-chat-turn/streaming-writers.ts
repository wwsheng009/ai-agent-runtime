// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。
//
// 流式写入层：把 SSE 事件映射为线程消息段/阶段状态与运行时元数据的写入闭包
// （工具段 upsert、工具结束、阶段切换、流式错误落盘）。本模块不持有 React 状态。

import { type Artifact, type Thread } from "@/data/mock";
import {
  buildAssistantMessageSegments,
  buildToolSegmentFromPayload,
  buildTurnJsonArtifact,
  closeRunningReasoningSegments,
  getToolName,
  getToolErrorMessage,
  updateThreadMessage,
  upsertToolSegment,
  type ToolMessageSegment,
} from "@/lib/workspace-thread-state";
import type {
  AgentChatStreamChunkPayload,
  ChatStreamPhase,
} from "@/types/runtime";
import { clearLiveStreamText } from "@/lib/live-stream-text";

import { type ChatTurnRuntimeState } from "./turn-state";

export type StreamingWriterDeps = {
  assistantMessageId: string;
  attachTurnArtifact: (artifact: Artifact) => void;
  frameScheduler: { cancel: () => void };
  phaseRef: { current: ChatStreamPhase | null };
  setPhase: (phase: ChatStreamPhase | null) => void;
  turnId: string;
  turnState: ChatTurnRuntimeState;
  updateCurrentThread: (updater: (thread: Thread) => Thread) => void;
};

export type StreamingWriters = {
  handleToolEnd: (payload: AgentChatStreamChunkPayload) => void;
  setPhaseAndRef: (nextPhase: ChatStreamPhase | null) => void;
  updateStreamingError: (message: string, heading?: string) => void;
  upsertLiveToolSegment: (
    payload: AgentChatStreamChunkPayload,
    status: ToolMessageSegment["status"],
    eventType: string,
  ) => void;
};

export function createStreamingWriters(
  deps: StreamingWriterDeps,
): StreamingWriters {
  const {
    assistantMessageId,
    attachTurnArtifact,
    frameScheduler,
    phaseRef,
    setPhase,
    turnId,
    turnState,
    updateCurrentThread,
  } = deps;

  const upsertLiveToolSegment = (
    payload: AgentChatStreamChunkPayload,
    status: ToolMessageSegment["status"],
    eventType: string,
  ) => {
    turnState.toolPayloads.push(payload);
    // 工具帧 = 推理块边界（见 turn-state 的 reasoningBlockToolCounts）：之后的推理
    // 增量属于新的一块，结构快照据此把新块插到这行工具之后。
    turnState.toolFrameCount += 1;
    setPhaseAndRef("tool");
    attachTurnArtifact(
      buildTurnJsonArtifact(
        turnId,
        "tool-events",
        "Tool events observed during agent chat SSE.",
        turnState.toolPayloads,
      ),
    );
    // 工具帧 = 推理阶段已结束：收尾消息里仍在跑的推理段，并让后续流式帧按
    // 新阶段渲染（渲染层曾把 reasoningRunning 写死为 true，推理行会一直转圈）。
    turnState.reasoningRunning = false;
    updateCurrentThread((thread) =>
      updateThreadMessage(thread, assistantMessageId, (message) => ({
        ...message,
        segments: upsertToolSegment(
          closeRunningReasoningSegments(message.segments),
          buildToolSegmentFromPayload(payload, status),
        ),
      })),
    );
    updateCurrentThread((thread) => ({
      ...thread,
      lastRuntimeEventType: `${eventType}:${getToolName(payload)}`,
    }));
  };

  const handleToolEnd = (payload: AgentChatStreamChunkPayload) => {
    const hasError = Boolean(getToolErrorMessage(payload));
    upsertLiveToolSegment(payload, hasError ? "error" : "finished", "tool_end");
    if (hasError) {
      setPhaseAndRef("tool");
    }
  };

  const setPhaseAndRef = (nextPhase: ChatStreamPhase | null) => {
    phaseRef.current = nextPhase;
    setPhase(nextPhase);
  };

  const updateStreamingError = (
    message: string,
    heading = "Runtime stream failed.",
  ) => {
    frameScheduler.cancel();
    const hasStreamedText = turnState.streamedText.trim().length > 0;
    updateCurrentThread((thread) =>
      updateThreadMessage(thread, assistantMessageId, (currentMessage) => {
        const segments = buildAssistantMessageSegments(
          hasStreamedText ? turnState.streamedText : `${heading}\n\n${message}`,
          turnState.currentSource,
          turnState.reasoningText,
          {
            existingSegments: currentMessage.segments,
            reasoningBlocks: turnState.reasoningBlocks,
            reasoningBlockToolCounts: turnState.reasoningBlockToolCounts,
          },
        );
        if (hasStreamedText) {
          segments.push({
            type: "callout",
            title: heading,
            tone: "warning",
            content: message,
          });
        }
        return {
          ...currentMessage,
          author: "Runtime error",
          label: "error",
          streaming: false,
          segments,
        };
      }),
    );
    // 错误定稿同样写回了完整文本，live 记录作废（见 finalize-turn 同处注释）。
    clearLiveStreamText(assistantMessageId);
  };
  return {
    handleToolEnd,
    setPhaseAndRef,
    updateStreamingError,
    upsertLiveToolSegment,
  };
}
