// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。
//
// 终态收敛层：在 SSE 结束/中断/错误路径上统一收口 turn 状态——合并终态文本与推理、
// 生成最终产物、写回线程与消息段、做 DEV 双跑校验，并同步会话目录与完成通知。
// 本模块不持有 React 状态，全部通过 deps 注入。

import { type AppSettings } from "@/core/settings/local";
import { type Thread } from "@/data/mock";
import { buildFinalArtifacts } from "@/hooks/workspace/agent-chat-turn/final-artifacts";
import {
  buildNotificationBody,
  maybeShowDesktopNotification,
} from "@/hooks/workspace/agent-chat-turn/notifications";
import { type TrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";
import { debugTrajectoryConsistency } from "@/lib/trajectory/projection";
import {
  buildAssistantMessageSegments,
  buildGeneratedImageAttachments,
  getAssistantMessageReasoning,
  getAssistantMessageText,
  mergeUniqueStrings,
  reconcileRuntimeText,
  updateThreadMessage,
  upsertArtifacts,
} from "@/lib/workspace-thread-state";
import type {
  AgentChatStreamDonePayload,
  ChatStreamPhase,
} from "@/types/runtime";

import { type ChatTurnRuntimeState } from "./turn-state";

export type TurnFinalizer = (
  payload?: Partial<AgentChatStreamDonePayload>,
  options?: {
    stopped?: boolean;
  },
) => void;

export type TurnFinalizerDeps = {
  activeTurnIdRef: { current: string | null };
  assistantMessageId: string;
  frameScheduler: { cancel: () => void };
  onSessionTouched?: () => void;
  setActiveTurnId: (turnId: string | null) => void;
  setPhaseAndRef: (nextPhase: ChatStreamPhase | null) => void;
  setSelectedArtifactId: (artifactId: string | null) => void;
  settings: AppSettings;
  threadId: string;
  threadSnapshot: Thread;
  trajectoryStore: TrajectoryStore;
  turnId: string;
  turnState: ChatTurnRuntimeState;
  updateCurrentThread: (updater: (thread: Thread) => Thread) => void;
};

export function createTurnFinalizer(deps: TurnFinalizerDeps): TurnFinalizer {
  const {
    activeTurnIdRef,
    assistantMessageId,
    frameScheduler,
    onSessionTouched,
    setActiveTurnId,
    setPhaseAndRef,
    setSelectedArtifactId,
    settings,
    threadId,
    threadSnapshot,
    trajectoryStore,
    turnId,
    turnState,
    updateCurrentThread,
  } = deps;

  return (
    payload: Partial<AgentChatStreamDonePayload> = {},
    options?: {
      stopped?: boolean;
    },
  ) => {
    if (turnState.turnFinalized) {
      return;
    }

    turnState.turnFinalized = true;
    if (activeTurnIdRef.current === turnId) {
      activeTurnIdRef.current = null;
      setActiveTurnId(null);
    }
    frameScheduler.cancel();
    setPhaseAndRef("finalizing");

    if (payload.session_id) {
      turnState.currentSessionId = payload.session_id;
    }
    if (payload.source) {
      turnState.currentSource = payload.source;
    }
    if (payload.result) {
      turnState.finalResult = payload.result;
      if (payload.result.source) {
        turnState.currentSource = payload.result.source;
      }
      if (payload.result.kind) {
        turnState.currentKind = payload.result.kind;
      }
      if (payload.result.reasoning && payload.result.reasoning.trim()) {
        turnState.reasoningText = reconcileRuntimeText(
          turnState.reasoningText,
          payload.result.reasoning,
        );
      }
    }
    if (payload.content && payload.content.trim()) {
      turnState.streamedText = reconcileRuntimeText(turnState.streamedText, payload.content);
    }

    const generatedImageAttachments = buildGeneratedImageAttachments(
      turnState.currentSessionId,
      turnState.finalResult?.metadata ?? null,
    );

    const stopped = options?.stopped === true;
    const terminalPayload: AgentChatStreamDonePayload = {
      session_id: turnState.currentSessionId,
      agent_id: payload.agent_id,
      source: turnState.currentSource,
      status: payload.status ?? (stopped ? "stopped" : "completed"),
      content: turnState.streamedText,
      result: turnState.finalResult ?? payload.result,
      turn_id: payload.turn_id ?? turnId,
    };

    const artifacts = buildFinalArtifacts(
      turnId,
      terminalPayload,
      turnState.currentSessionId,
      turnState.currentSource,
      turnState.finalResult,
      turnState.planningPayload,
      turnState.orchestrationPayload,
      turnState.routePayload,
      turnState.observationPayloads,
      turnState.subagentPayloads,
      turnState.toolPayloads,
      generatedImageAttachments.artifacts,
    );

    const terminalText = reconcileRuntimeText(
      turnState.streamedText,
      turnState.finalResult?.output ?? payload.content,
    );
    const finalText =
      terminalText ||
      (stopped
        ? "Response stopped before any text was returned."
        : "Runtime request completed, but no textual output was returned.");

    // 冲刷轨迹批处理器：确保快照包含全部 SSE 事件后再做双跑校验。
    trajectoryStore.flush();

    updateCurrentThread((thread) => {
      let nextThread: Thread = {
        ...thread,
        updatedAt: new Date().toISOString(),
        sessionId: turnState.currentSessionId || thread.sessionId,
        transport:
          thread.transport === "error"
            ? "error"
            : turnState.currentSessionId || turnState.receivedRuntimeActivity
              ? "live"
              : thread.transport,
        runtimeSource: turnState.currentSource || thread.runtimeSource,
        lastError: null,
        lastRuntimeEventType: stopped
          ? "agent_chat:stopped"
          : thread.lastRuntimeEventType,
      };

      nextThread = updateThreadMessage(nextThread, assistantMessageId, (message) => {
        // The runtime/stream path may have rendered deltas independently of
        // the request SSE path.  Read that message *inside* the functional
        // updater so the terminal snapshot cannot erase a longer live
        // buffer (and so reasoning follows the same reconciliation rules).
        const renderedText = getAssistantMessageText(message);
        const renderedReasoning = getAssistantMessageReasoning(message);
        const reconciledText = reconcileRuntimeText(
          renderedText,
          terminalText,
        );
        const reconciledReasoning = reconcileRuntimeText(
          renderedReasoning,
          reconcileRuntimeText(
            turnState.reasoningText,
            turnState.finalResult?.reasoning,
          ),
        );
        const resolvedText = reconciledText || finalText;
        const finalSegments = buildAssistantMessageSegments(
          resolvedText,
          turnState.currentSource,
          reconciledReasoning,
          {
            status: stopped ? "stopped" : undefined,
            existingSegments: message.segments,
            generatedImages: generatedImageAttachments,
          },
        );
        // DEV 双跑校验：轨迹快照 vs 现有渲染路径（确认无回归后切换唯一路径）。
        debugTrajectoryConsistency(trajectoryStore.getSnapshot(), finalSegments);
        return {
          ...message,
          interrupted: stopped ? true : undefined,
          streaming: false,
          author:
            turnState.finalResult?.kind === "agent" || turnState.currentKind === "agent"
              ? "Runtime agent"
              : "Runtime stream",
          label: stopped ? "stopped" : turnState.currentSource || "runtime",
          segments: finalSegments,
          relatedArtifactIds: mergeUniqueStrings(
            ...(message.relatedArtifactIds ?? []),
            ...artifacts.map((artifact) => artifact.id),
          ),
        };
      });

      nextThread.artifacts = upsertArtifacts(nextThread.artifacts, artifacts);
      return nextThread;
    });

    if (artifacts[0]) {
      setSelectedArtifactId(artifacts[0].id);
    }
    if (turnState.currentSessionId) {
      onSessionTouched?.();
    }

    if (!stopped) {
      maybeShowDesktopNotification(
        settings.notification.enabled && settings.notification.desktop,
        `${threadSnapshot.title} completed`,
        buildNotificationBody(finalText, turnState.currentSource),
        turnState.currentSessionId || threadId,
      );
    }
  };
}
