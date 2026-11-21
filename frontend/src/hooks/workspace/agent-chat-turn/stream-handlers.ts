// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。
//
// SSE 消费层：把 /api/agent/chat 的事件回调（meta/chunk/reasoning/tool/planning/
// orchestration/route/observation/subagent/result/done/error）聚合为单个工厂；
// 渲染与终态收敛回调由发送编排注入，本模块不持有 React 状态。

import type { Artifact, Thread } from "@/data/mock";
import {
  buildTurnJsonArtifact,
  reconcileRuntimeText,
  type RuntimeDeltaCoordinator,
  type ToolMessageSegment,
} from "@/lib/workspace-thread-state";
import { recordGoalToolEnd } from "@/lib/session-goal/store";
import type { TrajectoryEventKind } from "@/lib/trajectory/types";
import type {
  AgentChatResult,
  AgentChatStreamChunkPayload,
  AgentChatStreamDonePayload,
  ChatStreamPhase,
} from "@/types/runtime";

import { createAgentChatContentStreamHandlers } from "./stream-handlers-content";
import { shouldIgnoreTerminalStreamError } from "./shared";
import {
  getFrameLeaseConflictTitle,
  type FrameScheduler,
} from "./stream-handlers-helpers";
import type { ChatTurnRuntimeState } from "./turn-state";

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
  /** P2-9：goal 只读投影的会话键（runtime session_id；新会话可能尚未登记）。 */
  sessionId?: string;
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
    sessionId,
    setPhaseAndRef,
    turnId,
    turnState,
    updateCurrentThread,
    updateStreamingError,
    upsertLiveToolSegment,
  } = deps;

  return {
    ...createAgentChatContentStreamHandlers({
      assistantMessageId,
      deltaCoordinator,
      frameScheduler,
      pushTrajectory,
      sessionId,
      setPhaseAndRef,
      turnId,
      turnState,
      updateCurrentThread,
    }),
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
      // P2-9：goal 工具结果只在这里是完整的（`tool.content` 全量）；线程层
      // 截断到 240 字符、轨迹层不保留，故在 SSE 边界直接投影。
      recordGoalToolEnd(sessionId, payload);
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
      // 会话租约冲突：附上与 CLI 路径一致的短标题（其余错误帧不带标题，行为不变）。
      updateStreamingError(message, getFrameLeaseConflictTitle(payload));
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
