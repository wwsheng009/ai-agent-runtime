// 由 hooks/workspace/agent-chat-turn/stream-handlers.ts 机械拆分而来（P0-2），
// 仅搬迁内容帧（meta/chunk/reasoning/reset）处理，不改语义。
//
// 内容帧语义：权威全文（mode=replace）覆盖、增量追加、跨通道帧闸门与 provider
// 去重键 claim。工具/规划/终态事件仍由 stream-handlers.ts 组装。

import type { Thread } from "@/data/mock";
import { admitTransportFrame, readChatFrameSeq } from "@/hooks/workspace/frame-intake";
import {
  appendLiveStreamReasoning,
  appendLiveStreamText,
  setLiveStreamReasoning,
  setLiveStreamText,
} from "@/lib/live-stream-text";
import type { TrajectoryEventKind } from "@/lib/trajectory/types";
import {
  appendArtifactToMessage,
  buildGeneratedImagePlaceholderSegment,
  buildTurnJsonArtifact,
  getRuntimeDeltaKey,
  getStreamTextDelta,
  isReplaceStreamChunk,
  isRuntimePayload,
  updateThreadMessage,
  upsertGeneratedImageSegment,
  type RuntimeDeltaCoordinator,
} from "@/lib/workspace-thread-state";
import type {
  AgentChatStreamChunkPayload,
  AgentChatStreamMetaPayload,
  ChatStreamPhase,
} from "@/types/runtime";

import {
  appendReasoningDeltaToTurn,
  type ChatTurnRuntimeState,
} from "./turn-state";
import {
  chatStreamIDOf,
  ensureReasoningStreamStart,
  ensureTextStreamStart,
  type FrameScheduler,
} from "./stream-handlers-helpers";

export type AgentChatContentStreamHandlersDeps = {
  assistantMessageId: string;
  deltaCoordinator?: RuntimeDeltaCoordinator;
  frameScheduler: FrameScheduler;
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
};

export function createAgentChatContentStreamHandlers(
  deps: AgentChatContentStreamHandlersDeps,
) {
  const {
    assistantMessageId,
    deltaCoordinator,
    frameScheduler,
    pushTrajectory,
    sessionId,
    setPhaseAndRef,
    turnId,
    turnState,
    updateCurrentThread,
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
      if (!replace) {
        const earlyDelta = getStreamTextDelta(payload);
        if (earlyDelta) {
          ensureTextStreamStart(turnState, chatStreamIDOf(payload));
        }
      }
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
      const delta =
        typeof payload.content === "string"
          ? payload.content
          : payload.reasoning && typeof payload.reasoning.content === "string"
            ? payload.reasoning.content
            : "";
      if (delta) {
        ensureReasoningStreamStart(turnState, chatStreamIDOf(payload));
      }
      if (!admitContentFrame("reasoning", payload)) {
        return;
      }
      pushTrajectory("reasoning", payload);
      turnState.receivedRuntimeActivity = true;
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
    onReset: (payload: AgentChatStreamChunkPayload) => {
      const streamID = chatStreamIDOf(payload);
      if (!streamID) {
        return;
      }
      const textStart = turnState.streamTextStart[streamID];
      const reasoningStart = turnState.streamReasoningStart[streamID];
      if (textStart !== undefined) {
        turnState.streamedText = turnState.streamedText.slice(0, textStart);
      }
      if (reasoningStart) {
        turnState.reasoningText = turnState.reasoningText.slice(
          0,
          reasoningStart.text,
        );
        turnState.reasoningBlocks = turnState.reasoningBlocks.slice(
          0,
          reasoningStart.block,
        );
        turnState.reasoningBlockToolCounts =
          turnState.reasoningBlockToolCounts.slice(0, reasoningStart.block);
        // Force the next reasoning chunk to open a fresh block rather than
        // appending to the failed attempt's last block.
        turnState.reasoningBlockBoundaryCount = -1;
      }
      turnState.reasoningRunning = false;
      setLiveStreamText(assistantMessageId, turnState.streamedText);
      setLiveStreamReasoning(assistantMessageId, "");
      // The next attempt reuses this stream_id; its contribution starts at the
      // truncated baseline.
      turnState.streamTextStart[streamID] = turnState.streamedText.length;
      turnState.streamReasoningStart[streamID] = {
        text: turnState.reasoningText.length,
        block: turnState.reasoningBlocks.length,
      };
      setPhaseAndRef("streaming");
      frameScheduler.schedule();
    },
  };
}