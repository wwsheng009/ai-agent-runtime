// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。
//
// SSE 消费层：把 /api/agent/chat 的事件回调（meta/chunk/reasoning/tool/planning/
// orchestration/route/observation/subagent/result/done/error）聚合为单个工厂；
// 渲染与终态收敛回调由发送编排注入，本模块不持有 React 状态。

import type { Artifact, Thread } from "@/data/mock";
import {
  appendArtifactToMessage,
  buildGeneratedImagePlaceholderSegment,
  buildTurnJsonArtifact,
  getRuntimeDeltaKey,
  getStreamTextDelta,
  isRuntimePayload,
  reconcileRuntimeText,
  updateThreadMessage,
  upsertGeneratedImageSegment,
  type RuntimeDeltaCoordinator,
  type ToolMessageSegment,
} from "@/lib/workspace-thread-state";
import type { TrajectoryEventKind } from "@/lib/trajectory/types";
import type {
  AgentChatResult,
  AgentChatStreamChunkPayload,
  AgentChatStreamDonePayload,
  AgentChatStreamMetaPayload,
  ChatStreamPhase,
} from "@/types/runtime";

import { shouldIgnoreTerminalStreamError } from "./shared";
import type { ChatTurnRuntimeState } from "./turn-state";

type FrameScheduler = {
  flush: () => void;
  schedule: () => void;
};

export type AgentChatStreamHandlersDeps = {
  assistantMessageId: string;
  attachTurnArtifact: (artifact: Artifact) => void;
  controller: AbortController;
  deltaCoordinator?: RuntimeDeltaCoordinator;
  finalizeTurn: (
    payload?: Partial<AgentChatStreamDonePayload>,
    options?: { stopped?: boolean },
  ) => void;
  frameScheduler: FrameScheduler;
  handleToolEnd: (payload: AgentChatStreamChunkPayload) => void;
  notifyFailure: (message: string) => void;
  pushTrajectory: (
    kind: TrajectoryEventKind,
    payload: Record<string, unknown> | null | undefined,
  ) => void;
  setPhaseAndRef: (nextPhase: ChatStreamPhase | null) => void;
  turnId: string;
  turnState: ChatTurnRuntimeState;
  updateCurrentThread: (updater: (thread: Thread) => Thread) => void;
  updateStreamingError: (message: string, heading?: string) => void;
  upsertLiveToolSegment: (
    payload: AgentChatStreamChunkPayload,
    status: ToolMessageSegment["status"],
    eventType: string,
  ) => void;
};

export function createAgentChatStreamHandlers(
  deps: AgentChatStreamHandlersDeps,
) {
  const {
    assistantMessageId,
    attachTurnArtifact,
    controller,
    deltaCoordinator,
    finalizeTurn,
    frameScheduler,
    handleToolEnd,
    notifyFailure,
    pushTrajectory,
    setPhaseAndRef,
    turnId,
    turnState,
    updateCurrentThread,
    updateStreamingError,
    upsertLiveToolSegment,
  } = deps;

  return {
    onMeta: (payload: AgentChatStreamMetaPayload) => {
      pushTrajectory("meta", payload);
      turnState.receivedRuntimeActivity = true;
      setPhaseAndRef("first-token");
      if (payload.session_id) {
        turnState.currentSessionId = payload.session_id;
      }
      if (payload.source) {
        turnState.currentSource = payload.source;
      }
      if (payload.kind) {
        turnState.currentKind = payload.kind;
      }

      updateCurrentThread((thread) => {
        let nextThread: Thread = {
          ...thread,
          updatedAt: new Date().toISOString(),
          sessionId: turnState.currentSessionId || thread.sessionId,
          transport: "live",
          runtimeSource: turnState.currentSource,
          lastError: null,
        };

        nextThread = updateThreadMessage(
          nextThread,
          assistantMessageId,
          (message) => ({
            ...message,
            author:
              payload.kind === "agent"
                ? "Runtime agent"
                : "Runtime stream",
            label: payload.source ?? "streaming",
          }),
        );

        if (isRuntimePayload(payload.orchestration)) {
          turnState.orchestrationPayload = payload.orchestration;
          nextThread = appendArtifactToMessage(
            nextThread,
            assistantMessageId,
            buildTurnJsonArtifact(
              turnId,
              "orchestration",
              "Structured orchestration summary emitted during SSE startup.",
              payload.orchestration,
            ),
          );
        }

        if (isRuntimePayload(payload.planning)) {
          turnState.planningPayload = payload.planning;
          nextThread = appendArtifactToMessage(
            nextThread,
            assistantMessageId,
            buildTurnJsonArtifact(
              turnId,
              "planning",
              "Planning payload emitted during SSE startup.",
              payload.planning,
            ),
          );
        }

        return nextThread;
      });
    },
    onChunk: (payload: AgentChatStreamChunkPayload) => {
      pushTrajectory("chunk", payload);
      turnState.receivedRuntimeActivity = true;
      if (payload.type === "image") {
        const imageKey = getRuntimeDeltaKey(
          payload as unknown as Record<string, unknown>,
          "image",
        );
        if (deltaCoordinator && !deltaCoordinator.claim(imageKey)) {
          return;
        }
        const imageProgress = buildGeneratedImagePlaceholderSegment(
          payload.metadata,
        );
        if (imageProgress) {
          updateCurrentThread((thread) =>
            updateThreadMessage(
              thread,
              assistantMessageId,
              (message) => ({
                ...message,
                segments: upsertGeneratedImageSegment(
                  message.segments,
                  imageProgress,
                ),
              }),
            ),
          );
          updateCurrentThread((thread) => ({
            ...thread,
            lastRuntimeEventType: `assistant.image_progress:${imageProgress.phase}`,
          }));
        }
        return;
      }
      const delta = getStreamTextDelta(payload);
      if (!delta) {
        return;
      }
      const textKey = getRuntimeDeltaKey(
        payload as unknown as Record<string, unknown>,
        "text",
      );
      if (deltaCoordinator && !deltaCoordinator.claim(textKey)) {
        return;
      }
      turnState.streamedText += delta;
      setPhaseAndRef("streaming");
      frameScheduler.schedule();
    },
    onReasoning: (payload: AgentChatStreamChunkPayload) => {
      pushTrajectory("reasoning", payload);
      turnState.receivedRuntimeActivity = true;
      const delta =
        typeof payload.content === "string"
          ? payload.content
          : payload.reasoning &&
              typeof payload.reasoning.content === "string"
            ? payload.reasoning.content
            : "";
      if (delta) {
        const reasoningKey = getRuntimeDeltaKey(
          payload as unknown as Record<string, unknown>,
          "reasoning",
        );
        if (deltaCoordinator && !deltaCoordinator.claim(reasoningKey)) {
          return;
        }
        // 直接拼接 delta：reasoning 增量是连续 token 片段，逐段强制
        // 插入换行会让每个 delta 独占一行（显示异常）。与 runtime
        // 流路径 appendReasoningToMessageSegments 一致，保持原始
        // chunk 边界即可。
        turnState.reasoningText += delta;
        setPhaseAndRef("streaming");
        frameScheduler.schedule();
      }
    },
    onToolStart: (payload: AgentChatStreamChunkPayload) => {
      pushTrajectory("tool_start", payload);
      turnState.receivedRuntimeActivity = true;
      upsertLiveToolSegment(payload, "started", "tool_start");
    },
    onToolCall: (payload: AgentChatStreamChunkPayload) => {
      pushTrajectory("tool_call", payload);
      turnState.receivedRuntimeActivity = true;
      upsertLiveToolSegment(payload, "running", "tool_call");
    },
    onToolEnd: (payload: AgentChatStreamChunkPayload) => {
      pushTrajectory("tool_end", payload);
      turnState.receivedRuntimeActivity = true;
      handleToolEnd(payload);
    },
    onPlanning: (payload: Record<string, unknown>) => {
      pushTrajectory("planning", payload);
      turnState.receivedRuntimeActivity = true;
      turnState.planningPayload = payload ?? null;
      if (!payload) {
        return;
      }
      attachTurnArtifact(
        buildTurnJsonArtifact(
          turnId,
          "planning",
          "Planning payload emitted by /api/agent/chat SSE.",
          payload,
        ),
      );
    },
    onOrchestration: (payload: Record<string, unknown>) => {
      pushTrajectory("orchestration", payload);
      turnState.receivedRuntimeActivity = true;
      turnState.orchestrationPayload = payload ?? null;
      if (!payload) {
        return;
      }
      attachTurnArtifact(
        buildTurnJsonArtifact(
          turnId,
          "orchestration",
          "Structured orchestration summary emitted by /api/agent/chat SSE.",
          payload,
        ),
      );
    },
    onRoute: (payload: Record<string, unknown>) => {
      pushTrajectory("route", payload);
      turnState.receivedRuntimeActivity = true;
      turnState.routePayload = payload ?? null;
      if (!payload) {
        return;
      }
      attachTurnArtifact(
        buildTurnJsonArtifact(
          turnId,
          "route",
          "Route metadata emitted by static SSE execution.",
          payload,
        ),
      );
    },
    onObservation: (payload: Record<string, unknown>) => {
      pushTrajectory("observation", payload);
      turnState.receivedRuntimeActivity = true;
      if (!payload) {
        return;
      }
      turnState.observationPayloads.push(payload);
      attachTurnArtifact(
        buildTurnJsonArtifact(
          turnId,
          "observations",
          "Observation events emitted by static SSE execution.",
          turnState.observationPayloads,
        ),
      );
    },
    onSubagent: (payload: Record<string, unknown>) => {
      pushTrajectory("subagent", payload);
      turnState.receivedRuntimeActivity = true;
      if (!payload) {
        return;
      }
      turnState.subagentPayloads.push(payload);
      attachTurnArtifact(
        buildTurnJsonArtifact(
          turnId,
          "subagents",
          "Subagent events emitted by static SSE execution.",
          turnState.subagentPayloads,
        ),
      );
    },
    onResult: (payload: AgentChatResult) => {
      pushTrajectory("result", payload);
      turnState.receivedRuntimeActivity = true;
      turnState.finalResult = payload;
      if (payload.source) {
        turnState.currentSource = payload.source;
      }
      if (payload.kind) {
        turnState.currentKind = payload.kind;
      }
      if (payload.reasoning && payload.reasoning.trim()) {
        turnState.reasoningText = reconcileRuntimeText(
          turnState.reasoningText,
          payload.reasoning,
        );
      }
      if (payload.output && payload.output.trim()) {
        turnState.streamedText = reconcileRuntimeText(
          turnState.streamedText,
          payload.output,
        );
        frameScheduler.flush();
      }

      attachTurnArtifact(
        buildTurnJsonArtifact(
          turnId,
          "agent-chat-result",
          "Structured result payload emitted by /api/agent/chat SSE.",
          payload,
        ),
      );
    },
    onDone: (payload: AgentChatStreamDonePayload) => {
      pushTrajectory("done", payload);
      turnState.receivedRuntimeActivity = true;
      finalizeTurn(payload);
    },
    onErrorEvent: (payload: Record<string, unknown>) => {
      if (
        shouldIgnoreTerminalStreamError({
          finalized: turnState.turnFinalized,
          aborted: controller.signal.aborted,
        })
      ) {
        return;
      }
      pushTrajectory("error", payload);
      turnState.receivedErrorEvent = true;
      const message =
        typeof payload.message === "string" && payload.message.trim()
          ? payload.message.trim()
          : typeof payload.error === "string" && payload.error.trim()
            ? payload.error.trim()
            : "agent chat stream failed";
      updateStreamingError(message);
      updateCurrentThread((thread) => ({
        ...thread,
        updatedAt: new Date().toISOString(),
        transport: "error",
        lastError: message,
      }));
      notifyFailure(message);
    },
  };
}
