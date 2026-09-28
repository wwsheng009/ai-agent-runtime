// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。
//
// SSE 消费层：把 /api/agent/chat 的事件回调（meta/chunk/reasoning/tool/planning/
// orchestration/route/observation/subagent/result/done/error）聚合为单个工厂；
// 渲染与终态收敛回调由发送编排注入，本模块不持有 React 状态。

import {
  RuntimeApiError,
  getSessionLeaseConflictTitle,
} from "@/api/runtime/shared";
import type { Artifact, Thread } from "@/data/mock";
import {
  appendLiveStreamReasoning,
  appendLiveStreamText,
  setLiveStreamReasoning,
  setLiveStreamText,
} from "@/lib/live-stream-text";
import {
  appendArtifactToMessage,
  buildGeneratedImagePlaceholderSegment,
  buildTurnJsonArtifact,
  getRuntimeDeltaKey,
  getStreamTextDelta,
  isReplaceStreamChunk,
  isRuntimePayload,
  reconcileRuntimeText,
  updateThreadMessage,
  upsertGeneratedImageSegment,
  type RuntimeDeltaCoordinator,
  type ToolMessageSegment,
} from "@/lib/workspace-thread-state";
import { recordGoalToolEnd } from "@/lib/session-goal/store";
import type { TrajectoryEventKind } from "@/lib/trajectory/types";
import { admitTransportFrame, readChatFrameSeq } from "@/hooks/workspace/frame-intake";
import type {
  AgentChatResult,
  AgentChatStreamChunkPayload,
  AgentChatStreamDonePayload,
  AgentChatStreamMetaPayload,
  ChatStreamPhase,
} from "@/types/runtime";

import { shouldIgnoreTerminalStreamError } from "./shared";
import { appendReasoningDeltaToTurn, type ChatTurnRuntimeState } from "./turn-state";

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

/**
 * 租约冲突的 SSE error 帧 → 与 CLI 路径（use-workspace-agent-chat-turn.ts 的
 * getSessionLeaseConflictTitle）完全一致的短标题：文案单一真源在 api/runtime/
 * shared，这里只把帧内的 lease 上下文投影成它认识的错误体，避免两处文案漂移。
 */
function getFrameLeaseConflictTitle(
  payload: Record<string, unknown>,
): string | undefined {
  if (payload.error_type !== "session_lease_conflict") {
    return undefined;
  }
  const lease =
    asRecord(payload.lease) ?? asRecord(asRecord(payload.context)?.lease);
  const ownerKind =
    typeof lease?.owner_kind === "string" ? lease.owner_kind : "";
  return getSessionLeaseConflictTitle(
    new RuntimeApiError(409, {
      code: "SESSION_LEASE_CONFLICT",
      context: { lease: { owner_kind: ownerKind } },
    }),
  );
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === "object"
    ? (value as Record<string, unknown>)
    : null;
}

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

  // L3 帧闸门（@/hooks/workspace/frame-intake）：同一会话内带持久化 seq 的内容帧
  // 跨通道（本 chat 流 + /runtime/stream）只放行一次。chat 流的 wire-only 帧
  // （chunk/reasoning）不带 `_event.sequence`，seq=0 时闸门放行，保持既有
  // deltaCoordinator 按 provider key 去重的行为——缺身份不等于重复。
  const admitContentFrame = (
    kind: "text" | "reasoning" | "image",
    payload: unknown,
  ): boolean =>
    admitTransportFrame({
      sessionId,
      turnId,
      kind,
      seq: readChatFrameSeq(payload),
      // 与 /runtime/stream 入口共享同一单写者作用域（页面级 deltaCoordinator）。
      scope: deltaCoordinator ?? null,
    });

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
      // 权威全文帧（mode=replace）不参与两通道 claim（见下方注释），同样不参与
      // 闸门：它必须应用。其余内容帧先过闸门：被拒绝的重复帧整帧丢弃，且不消费
      // delta key（claim 在下方，被拒的帧走不到那里）。
      const replace = isReplaceStreamChunk(payload);
      const frameKind = payload.type === "image" ? "image" : "text";
      if (!replace && !admitContentFrame(frameKind, payload)) {
        return;
      }
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
      // 权威全文帧不参与两通道 claim：它是对既有文本的覆盖（没有、也不该有
      // 流式去重键），claim("") 是空操作，这里显式跳过以免表达错意图。
      if (!replace && deltaCoordinator && !deltaCoordinator.claim(textKey)) {
        return;
      }
      if (replace) {
        // 回合末静态快照（mode=replace）：增量可能已经走 runtime 通道渲染过，
        // 这里用权威全文覆盖，避免重复拼接（见 shared.ts isReplaceStreamChunk）。
        turnState.streamedText = delta;
        setLiveStreamText(assistantMessageId, delta);
      } else {
        turnState.streamedText += delta;
        // live 通道：增量先入外部 store（只惊动流式气泡），thread store 的正文
        // 改为低频结构快照（见 streaming-frame.ts 的 STRUCTURAL_COMMIT_INTERVAL_MS）。
        appendLiveStreamText(assistantMessageId, delta);
      }
      // 正文帧 = 推理块边界（见 turn-state 的 appendReasoningDeltaToTurn）：模型已经开口
      // 说正文之后又回来的推理属于新的一块，而不是并回上一行——「推理 → 正文 → 推理」
      // 在页面上应当是三行，与本帧同时收尾旧推理行的运行态。
      turnState.textFrameCount += 1;
      // 正文开始 = 推理阶段结束，推理行不再显示运行态。
      turnState.reasoningRunning = false;
      setPhaseAndRef("streaming");
      frameScheduler.schedule();
    },
    onReasoning: (payload: AgentChatStreamChunkPayload) => {
      if (!admitContentFrame("reasoning", payload)) {
        return;
      }
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
        //
        // 按块累计（工具帧 = 块边界）：结构快照按 `reasoningBlocks` 落成多段推理，
        // live 记录同样按块寻址——新块覆盖、块内追加。旧实现把整轮推理拼成一段并
        // 一律追加，工具之后的推理会并回上一行渲染成一段。
        if (appendReasoningDeltaToTurn(turnState, delta)) {
          setLiveStreamReasoning(assistantMessageId, delta);
        } else {
          appendLiveStreamReasoning(assistantMessageId, delta);
        }
        turnState.reasoningRunning = true;
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
