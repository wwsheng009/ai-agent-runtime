import {
  startTransition,
  type Dispatch,
  type SetStateAction,
  useEffect,
  useRef,
  useState,
} from "react";
import { useNavigate } from "react-router-dom";

import { useAppSettings } from "@/core/settings";
import { type Artifact, type ChatMessage, type Thread } from "@/data/mock";
import { createTurnFinalizer } from "@/hooks/workspace/agent-chat-turn/finalize-turn";
import { maybeShowDesktopNotification } from "@/hooks/workspace/agent-chat-turn/notifications";
import {
  RUNTIME_CONNECT_TIMEOUT_MS,
  resolveChatTurnWorkspacePath,
  shouldIgnoreTerminalStreamError,
} from "@/hooks/workspace/agent-chat-turn/shared";
import { createThreadFromPrompt } from "@/hooks/workspace/agent-chat-turn/thread-factory";
import { useComposerDraft } from "@/hooks/workspace/composer/use-composer-draft";
import { createAgentChatStreamHandlers } from "@/hooks/workspace/agent-chat-turn/stream-handlers";
import { createStreamingFrameScheduler } from "@/hooks/workspace/agent-chat-turn/streaming-frame";
import { createStreamingWriters } from "@/hooks/workspace/agent-chat-turn/streaming-writers";
import { createTurnRuntimeState } from "@/hooks/workspace/agent-chat-turn/turn-state";
import { useChatTurnReasoningEffort } from "@/hooks/workspace/agent-chat-turn/use-reasoning-effort";
import {
  useRuntimeModelCatalog,
} from "@/hooks/workspace/use-runtime-model-catalog";
import {
  createTrajectoryStore,
  type TrajectoryStore,
} from "@/hooks/workspace/use-trajectory-snapshot";
import { NEW_THREAD_ID } from "@/hooks/workspace/use-workspace-thread-selection";
import {
  streamAgentChat,
} from "@/lib/runtime-api";
import { getSessionLeaseConflictTitle } from "@/api/runtime/shared";
import { normalizeSessionId } from "@/lib/session-id";
import type { TrajectoryEventKind } from "@/lib/trajectory/types";
import {
  appendArtifactToMessage,
  buildAssistantMessageSegments,
  buildTurnJsonArtifact,
  createStreamingAssistantMessage,
  getErrorMessage,
  updateThreadMessage,
  upsertArtifact,
  type RuntimeDeltaCoordinator,
} from "@/lib/workspace-thread-state";
import type { ChatStreamPhase } from "@/types/runtime";

type WorkspaceAgentChatTurnOptions = {
  deltaCoordinator?: RuntimeDeltaCoordinator;
  onSessionTouched?: () => void;
  selectedThread: Thread | undefined;
  setSelectedArtifactId: (artifactId: string | null) => void;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
  userId?: string;
  workspacePath?: string;
};

export {
  resolveChatTurnWorkspacePath,
  shouldIgnoreTerminalStreamError,
} from "@/hooks/workspace/agent-chat-turn/shared";

export function useWorkspaceAgentChatTurn({
  deltaCoordinator,
  onSessionTouched,
  selectedThread,
  setSelectedArtifactId,
  setThreads,
  userId,
  workspacePath,
}: WorkspaceAgentChatTurnOptions) {
  const { settings } = useAppSettings();
  const navigate = useNavigate();
  // P1-4：草稿按会话持久化（sessionId 优先、线程 id 兜底），提交/清空即删除条目。
  const { draft, setDraft } = useComposerDraft({ thread: selectedThread });
  const [isResponding, setIsResponding] = useState(false);
  const [activeTurnId, setActiveTurnId] = useState<string | null>(null);
  const activeTurnIdRef = useRef<string | null>(null);
  const [phase, setPhase] = useState<ChatStreamPhase | null>(null);
  const phaseRef = useRef<ChatStreamPhase | null>(null);
  const activeRequestControllerRef = useRef<AbortController | null>(null);
  const trajectoryStoreRef = useRef<TrajectoryStore | null>(null);
  if (!trajectoryStoreRef.current) {
    trajectoryStoreRef.current = createTrajectoryStore();
  }
  const trajectoryStore = trajectoryStoreRef.current;
  const {
    modelOptions,
    providerOptions,
    runtimeModels,
    runtimeModelsError,
    runtimeModelsLoading,
    selectedModel,
    selectedProvider,
    setSelectedModel,
    setSelectedProvider,
  } = useRuntimeModelCatalog();

  const {
    flushDraftReasoningEffort,
    reasoningEffortDefault,
    reasoningEffortError,
    reasoningEffortOptions,
    selectedReasoningEffort,
    setReasoningEffort,
  } = useChatTurnReasoningEffort({
    onSessionTouched,
    runtimeModels,
    selectedModel,
    selectedProvider,
    selectedThread,
  });

  useEffect(() => {
    return () => {
      activeRequestControllerRef.current?.abort();
      trajectoryStoreRef.current?.dispose();
    };
  }, []);

  function submitPrompt() {
    const prompt = draft.trim();
    if (!prompt || !selectedThread || isResponding) {
      return;
    }

    const threadSnapshot =
      selectedThread.id === NEW_THREAD_ID
        ? createThreadFromPrompt(prompt)
        : selectedThread;
    const threadId = threadSnapshot.id;
    const sessionIdBeforeTurn = normalizeSessionId(threadSnapshot.sessionId ?? "");
    const turnId = crypto.randomUUID();
    const assistantMessageId = `turn-${turnId}-assistant`;
    deltaCoordinator?.beginTurn(turnId);
    const userMessage: ChatMessage = {
      id: crypto.randomUUID(),
      role: "user",
      author: "You",
      label: "draft",
      segments: [{ type: "text", content: prompt }],
    };
    const requestPayload = {
      messages: [{ role: "user" as const, content: prompt }],
      session_id: threadSnapshot.sessionId,
      turn_id: turnId,
      user_id: userId || undefined,
      workspace_path: resolveChatTurnWorkspacePath(
        threadSnapshot.sessionId,
        workspacePath,
      ),
      provider: selectedProvider || undefined,
      model: selectedModel || undefined,
      reasoning_effort: selectedReasoningEffort || undefined,
      enable_react: settings.chat.enableReact,
      enable_routing: true,
      max_steps: settings.chat.maxSteps,
    };

    const requestArtifact = buildTurnJsonArtifact(
      turnId,
      "agent-chat-request",
      "Streaming request payload sent from the Vite workspace to /api/agent/chat.",
      {
        ...requestPayload,
        stream: true,
      },
    );

    const turnState = createTurnRuntimeState(threadSnapshot);
    const controller = new AbortController();

    const updateCurrentThread = (updater: (thread: Thread) => Thread) => {
      setThreads((current) => {
        const existingIndex = current.findIndex((thread) => thread.id === threadId);
        if (existingIndex === -1) {
          return [updater(threadSnapshot), ...current];
        }

        return current.map((thread) =>
          thread.id === threadId ? updater(thread) : thread,
        );
      });
    };

    const attachTurnArtifact = (artifact: Artifact) => {
      updateCurrentThread((thread) =>
        appendArtifactToMessage(thread, assistantMessageId, artifact),
      );
    };

    /** 轨迹事件入口：SSE 回调 → 轨迹快照（reducer 幂等，记录失败不阻断转发）。 */
    const pushTrajectory = (
      kind: TrajectoryEventKind,
      payload: Record<string, unknown> | null | undefined,
    ) => {
      trajectoryStore.push(kind, payload);
    };

    const setStreamingMessage = (label: string, author: string, content: string) => {
      updateCurrentThread((thread) =>
        updateThreadMessage(thread, assistantMessageId, (message) => ({
          ...message,
          author,
          label,
          segments: buildAssistantMessageSegments(
            content,
            turnState.currentSource,
            turnState.reasoningText,
            {
              reasoningRunning: true,
              existingSegments: message.segments,
            },
          ),
        })),
      );
    };

    const renderStreamingMessage = () => {
      setStreamingMessage(
        turnState.currentSource,
        turnState.currentKind === "agent" ? "Runtime agent" : "Runtime stream",
        turnState.streamedText,
      );
    };

    const frameScheduler = createStreamingFrameScheduler(renderStreamingMessage);

    const notifyFailure = (message: string) => {
      maybeShowDesktopNotification(
        settings.notification.enabled && settings.notification.desktop,
        `${threadSnapshot.title} failed`,
        message,
        turnState.currentSessionId || threadId,
      );
    };

    const {
      handleToolEnd,
      setPhaseAndRef,
      updateStreamingError,
      upsertLiveToolSegment,
    } = createStreamingWriters({
      assistantMessageId,
      attachTurnArtifact,
      frameScheduler,
      phaseRef,
      setPhase,
      turnId,
      turnState,
      updateCurrentThread,
    });

    const finalizeTurn = createTurnFinalizer({
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
    });

    setDraft("");
    // 新 turn 开始：轨迹快照 reset（同步于首个 SSE 事件之前，避免
    // 导航渲染迟到的 effect reset 打断流事件收集造成 seq gap）。
    trajectoryStore.reset();
    activeTurnIdRef.current = turnId;
    setActiveTurnId(turnId);
    setIsResponding(true);
    setPhaseAndRef("connecting");
    frameScheduler.attachVisibilityListener();
    activeRequestControllerRef.current = controller;
    // 连接超时保护：后端可能因共享 SQLite 被其他进程锁住而无法在有限时间内
    // 建立 SSE 流（表现为 "Connecting to runtime…" 无限旋转）。若 N 秒内未收到
    // 任何 runtime 事件（onMeta 等会置 turnState.receivedRuntimeActivity），主动中止请求
    // 并进入错误状态，而不是让前端永远卡在 connecting 阶段。
    let connectTimeoutId: number | undefined;
    const startConnectTimeout = () => {
      connectTimeoutId = window.setTimeout(() => {
        if (turnState.receivedRuntimeActivity || turnState.turnFinalized || controller.signal.aborted) {
          return;
        }
        turnState.connectTimedOut = true;
        controller.abort();
        const message =
          "连接 runtime 超时（可能被其他 aicli 进程占用的共享数据库锁定），请稍后重试。";
        updateStreamingError(message);
        updateCurrentThread((thread) => ({
          ...thread,
          updatedAt: new Date().toISOString(),
          transport: "error",
          lastError: message,
        }));
      }, RUNTIME_CONNECT_TIMEOUT_MS);
    };
    const clearConnectTimeout = () => {
      if (connectTimeoutId !== undefined && typeof window !== "undefined") {
        window.clearTimeout(connectTimeoutId);
      }
      connectTimeoutId = undefined;
    };
    startConnectTimeout();
    if (selectedThread.id === NEW_THREAD_ID) {
      startTransition(() => {
        navigate(`/workspace/chats/${threadId}`);
      });
    }
    updateCurrentThread((thread) => ({
      ...thread,
      updatedAt: new Date().toISOString(),
      transport: thread.transport === "live" ? "live" : "mock",
      lastError: null,
      artifacts: upsertArtifact(thread.artifacts, requestArtifact),
      messages: [
        ...thread.messages,
        userMessage,
        createStreamingAssistantMessage(
          assistantMessageId,
          [requestArtifact.id],
          turnId,
        ),
      ],
    }));
    setSelectedArtifactId(requestArtifact.id);

    void (async () => {
      try {
        await streamAgentChat(
          requestPayload,
          {
            signal: controller.signal,
            ...createAgentChatStreamHandlers({
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
            }),
          },
        );
        if (!turnState.turnFinalized && !turnState.receivedErrorEvent) {
          finalizeTurn();
        }
      } catch (error) {
        if (turnState.connectTimedOut) {
          // 连接超时错误已在超时回调中写入线程状态（transport/lastError 及
          // 消息段错误提示），此处直接返回，避免 finalizeTurn 清空 lastError。
          return;
        }
        if (controller.signal.aborted) {
          finalizeTurn({}, { stopped: true });
          return;
        }
        if (
          shouldIgnoreTerminalStreamError({
            finalized: turnState.turnFinalized,
            aborted: false,
          })
        ) {
          return;
        }
        const message = getErrorMessage(error, "agent chat stream failed");
        updateStreamingError(
          message,
          getSessionLeaseConflictTitle(error),
        );
        updateCurrentThread((thread) => ({
          ...thread,
          updatedAt: new Date().toISOString(),
          transport: "error",
          lastError: message,
        }));
        notifyFailure(message);
      } finally {
        clearConnectTimeout();
        frameScheduler.cancel();
        frameScheduler.detachVisibilityListener();
        if (activeRequestControllerRef.current === controller) {
          activeRequestControllerRef.current = null;
        }
        if (activeTurnIdRef.current === turnId) {
          activeTurnIdRef.current = null;
          setActiveTurnId(null);
        }
        deltaCoordinator?.endTurn(turnId);
        setIsResponding(false);
        setPhaseAndRef(null);

        // 草稿线程首轮结束后会话才落库：补写用户预先选择的档位。
        if (!sessionIdBeforeTurn) {
          flushDraftReasoningEffort(normalizeSessionId(turnState.currentSessionId));
        }
      }
    })();
  }

  function stopResponding() {
    activeRequestControllerRef.current?.abort();
  }

  return {
    draft,
    isResponding,
    activeTurnId,
    phase,
    trajectoryStore,
    modelOptions,
    providerOptions,
    runtimeModelsError,
    runtimeModelsLoading,
    reasoningEffortError,
    reasoningEffortOptions,
    reasoningEffortDefault,
    selectedModel,
    selectedProvider,
    selectedReasoningEffort,
    setDraft,
    setReasoningEffort,
    setSelectedModel,
    setSelectedProvider,
    stopResponding,
    submitPrompt,
  };
}
