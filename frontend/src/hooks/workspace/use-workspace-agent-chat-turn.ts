import {
  startTransition,
  type Dispatch,
  type SetStateAction,
  useEffect,
} from "react";
import { useNavigate } from "react-router-dom";

import { useAppSettings } from "@/core/settings";
import { type Artifact, type Thread } from "@/data/mock";
import { createConnectTimeoutGuard } from "@/hooks/workspace/agent-chat-turn/connect-timeout";
import { createTurnFinalizer } from "@/hooks/workspace/agent-chat-turn/finalize-turn";
import { maybeShowDesktopNotification } from "@/hooks/workspace/agent-chat-turn/notifications";
import {
  CHAT_STREAM_IDLE_TIMEOUT_MS,
  shouldIgnoreTerminalStreamError,
} from "@/hooks/workspace/agent-chat-turn/shared";
import { applyChatStreamStall } from "@/hooks/workspace/agent-chat-turn/stall";
import { useComposerAttachments } from "@/hooks/workspace/composer/use-composer-attachments";
import { useComposerDraft } from "@/hooks/workspace/composer/use-composer-draft";
import { createAgentChatStreamHandlers } from "@/hooks/workspace/agent-chat-turn/stream-handlers";
import { createStreamingFrameScheduler, STRUCTURAL_COMMIT_INTERVAL_MS } from "@/hooks/workspace/agent-chat-turn/streaming-frame";
import { createStreamingWriters } from "@/hooks/workspace/agent-chat-turn/streaming-writers";
import {
  bindSessionTurn,
} from "@/hooks/workspace/agent-chat-turn/session-turn-registry";
import { prepareAgentChatTurn, type AgentChatSubmitOptions } from "@/hooks/workspace/agent-chat-turn/turn-bootstrap";
import { useSessionTurnView } from "@/hooks/workspace/agent-chat-turn/use-session-turn-view";
import { useChatTurnReasoningEffort } from "@/hooks/workspace/agent-chat-turn/use-reasoning-effort";
import {
  useRuntimeModelCatalog,
} from "@/hooks/workspace/use-runtime-model-catalog";
import {
  useRuntimeSkillCatalog,
} from "@/hooks/workspace/use-runtime-skill-catalog";
import {
  type TrajectoryStore,
} from "@/hooks/workspace/use-trajectory-snapshot";
import {
  useOwnedTrajectoryStore,
} from "@/hooks/workspace/use-trajectory-store-pool";
import { NEW_THREAD_ID } from "@/hooks/workspace/use-workspace-thread-selection";
import {
  streamAgentChat,
} from "@/lib/runtime-api";
import { isSseIdleTimeoutError } from "@/api/runtime/sse";
import { getSessionLeaseConflictTitle } from "@/api/runtime/shared";
import { requestSessionTurnInterrupt } from "@/api/runtime/session-turn-control";
import { normalizeSessionId } from "@/lib/session-id";
import { hasVisibleText } from "@/lib/chat-view/visible-text";
import {
  setLiveStreamReasoning,
  setLiveStreamText,
} from "@/lib/live-stream-text";
import type { TrajectoryEventKind } from "@/lib/trajectory/types";
import type { TrajectoryStorePool } from "@/lib/trajectory/store-pool";
import {
  appendArtifactToMessage,
  buildAssistantMessageSegments,
  createStreamingAssistantMessage,
  getErrorMessage,
  updateThreadMessage,
  upsertArtifact,
  type RuntimeDeltaCoordinator,
} from "@/lib/workspace-thread-state";

type WorkspaceAgentChatTurnOptions = {
  deltaCoordinator?: RuntimeDeltaCoordinator;
  onSessionTouched?: () => void;
  selectedThread: Thread | undefined;
  setSelectedArtifactId: (artifactId: string | null) => void;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
  /** 页面级池化 store（Batch 1）；缺省由本 hook 自持单实例（旧行为）。 */
  trajectoryStore?: TrajectoryStore;
  /** 页面级池：回合按**发起会话**的键取 store（后台/草稿回合不写进选中会话）。 */
  trajectoryStorePool?: TrajectoryStorePool;
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
  trajectoryStore: providedTrajectoryStore,
  trajectoryStorePool,
  userId,
  workspacePath,
}: WorkspaceAgentChatTurnOptions) {
  const { settings } = useAppSettings();
  const navigate = useNavigate();
  // P1-4：草稿按会话持久化（sessionId 优先、线程 id 兜底），提交/清空即删除条目。
  const { draft, setDraft, threadKey } = useComposerDraft({
    thread: selectedThread,
  });
  // P1-4 子片 2：附件草稿轨与草稿同口径（sessionId 优先、线程 id 兜底）。
  const composerAttachments = useComposerAttachments({ threadKey });
  // Batch 1（去单例）：回合状态按会话分键，页面只消费「选中会话」的那份视图。
  const {
    activeSessionKeys,
    activeTurnId,
    clearStreamStall,
    isResponding,
    phase,
    selectedTurnKey,
    streamStalled,
    turnRegistry,
  } = useSessionTurnView(selectedThread);
  const trajectoryStore = useOwnedTrajectoryStore(providedTrajectoryStore);
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
    runtimeSkills,
    runtimeSkillsError,
    runtimeSkillsLoading,
  } = useRuntimeSkillCatalog();

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
      // 卸载中止所有在途会话请求（单会话时与旧实现等价）。
      turnRegistry.abortAll();
    };
  }, [turnRegistry]);

  /** 提交回合；options 供 `/skill` 覆盖 prompt 与 expose_skills。返回是否已启动。 */
  function submitPrompt(options?: AgentChatSubmitOptions): boolean {
    const prompt = (options?.prompt ?? draft).trim();
    if (!prompt || !selectedThread) {
      return false;
    }
    // 提交闸门按会话（Batch 1 G1）：同会话单飞（服务端 ErrSessionBusy 的镜像），
    // 跨会话并行——A 在跑不再阻塞 B 的提交。
    if (turnRegistry.isBusy(selectedTurnKey)) {
      return false;
    }

    const {
      assistantMessageId,
      controller,
      requestArtifact,
      requestPayload,
      sessionIdBeforeTurn,
      threadId,
      threadSnapshot,
      turnId,
      turnKey,
      turnState,
      turnTrajectoryStore,
      userMessage,
    } = prepareAgentChatTurn({
      deltaCoordinator,
      prompt,
      selectedModel,
      selectedProvider,
      selectedReasoningEffort,
      selectedThread,
      settings,
      trajectoryStore,
      trajectoryStorePool,
      userId,
      workspacePath,
      exposeSkills: options?.exposeSkills,
    });
    turnRegistry.beginTurn({
      key: turnKey,
      threadId,
      turnId,
      controller,
      turnState,
    });
    // 既有写入器按 ref / setter 形状接收相位与在途身份：桥接到本回合的条目。
    const { activeTurnIdRef, phaseRef, setActiveTurnId, setPhase } =
      bindSessionTurn(turnRegistry, turnId);

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
      turnTrajectoryStore.push(kind, payload);
    };

    const setStreamingMessage = (label: string, author: string, content: string) => {
      // 结构快照落 store 的同时把 live 记录对齐到同一份正文/推理。live 记录是
      // 「比 store 更新的那一份」，但并非所有文本来源都走 append 增量：`onResult`
      // 的 output/reasoning 是**整体快照**（`reconcileRuntimeText` 取更长者），
      // 只更新 canonical。渲染层优先用 live，若不在这里对齐，气泡会一直显示旧
      // live 文本直到下一次快照或定稿。文本不变时 set* 是幂等的（不通知订阅方）。
      setLiveStreamText(assistantMessageId, content);
      // live 记录按块寻址：只对齐**当前块**（整轮拼接会让尾行把前面几块的内容也
      // 显示出来——观感就是「所有推理并成一段」）。更早的块已定稿在各自的推理段里。
      setLiveStreamReasoning(
        assistantMessageId,
        turnState.reasoningBlocks[turnState.reasoningBlocks.length - 1] ?? "",
      );
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
              reasoningRunning: turnState.reasoningRunning,
              existingSegments: message.segments,
              reasoningBlocks: turnState.reasoningBlocks,
              reasoningBlockToolCounts: turnState.reasoningBlockToolCounts,
            },
          ),
        })),
      );
    };

    // 结构快照节流（见 streaming-frame.ts / lib/live-stream-text.ts）：正文与推理
    // 增量走 live 通道（stream-handlers 里按到达顺序 append），thread store 只按
    // STRUCTURAL_COMMIT_INTERVAL_MS 写一次完整副本。每次写 store 都会让整棵工作区树
    // （topbar / 侧栏 / composer / 消息列）重渲染一次，因此这里的频率就是流式期的
    // 页面级提交频率。
    let lastStructuralCommitAt = 0;
    // 结构快照里是否已经存在「可挂 live 文本/推理的行节点」。live 通道只能渲染到已
    // 存在的 segment 行上（渲染层对空文本段不产出行），所以首块必须先落一次 store。
    let storeHasTextRow = false;
    let storeHasReasoningRow = false;

    const renderStreamingMessage = (options?: { force?: boolean }) => {
      const nowMs =
        typeof performance !== "undefined" ? performance.now() : Date.now();
      const liveHasText = hasVisibleText(turnState.streamedText);
      const liveHasReasoning = hasVisibleText(turnState.reasoningText);
      const missingRow =
        (liveHasText && !storeHasTextRow) ||
        (liveHasReasoning && !storeHasReasoningRow);

      if (
        !options?.force &&
        !missingRow &&
        nowMs - lastStructuralCommitAt < STRUCTURAL_COMMIT_INTERVAL_MS
      ) {
        return;
      }

      lastStructuralCommitAt = nowMs;
      storeHasTextRow = storeHasTextRow || liveHasText;
      storeHasReasoningRow = storeHasReasoningRow || liveHasReasoning;
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
      trajectoryStore: turnTrajectoryStore,
      turnId,
      turnState,
      updateCurrentThread,
    });

    setDraft("");
    // 新 turn 开始：轨迹快照 reset（同步于首个 SSE 事件之前，避免
    // 导航渲染迟到的 effect reset 打断流事件收集造成 seq gap）。
    turnTrajectoryStore.reset();
    turnRegistry.setStalled(turnKey, false);
    setPhaseAndRef("connecting");
    frameScheduler.attachVisibilityListener();
    const connectTimeout = createConnectTimeoutGuard({
      controller,
      onTimeout: () => {
        const message =
          "连接 runtime 超时（可能被其他 aicli 进程占用的共享数据库锁定），请稍后重试。";
        updateStreamingError(message);
        updateCurrentThread((thread) => ({
          ...thread,
          updatedAt: new Date().toISOString(),
          transport: "error",
          lastError: message,
        }));
      },
      turnState,
    });
    connectTimeout.start();
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
            idleTimeoutMs: CHAT_STREAM_IDLE_TIMEOUT_MS,
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
              sessionId: threadSnapshot.sessionId,
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
        if (isSseIdleTimeoutError(error)) {
          // 读侧静默看门狗命中：本页流已死，但回合 detached，服务端可能仍在跑。
          turnRegistry.setStalled(turnKey, true);
          applyChatStreamStall({ notifyFailure, updateCurrentThread, updateStreamingError });
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
        connectTimeout.clear();
        frameScheduler.cancel();
        frameScheduler.detachVisibilityListener();
        if (turnRegistry.isTurnRunning(turnId)) {
          activeTurnIdRef.current = null;
          setActiveTurnId(null);
        }
        deltaCoordinator?.endTurn(turnId);
        // 条目删除 = isResponding→false、phase→null（与旧 finally 同步）。
        turnRegistry.finishTurn(turnId);
        setPhaseAndRef(null);

        // 草稿线程首轮结束后会话才落库：补写用户预先选择的档位。
        if (!sessionIdBeforeTurn) {
          flushDraftReasoningEffort(normalizeSessionId(turnState.currentSessionId));
        }
      }
    })();
    return true;
  }

  function stopResponding() {
    // 建议 3（后端 cancel 契约）：abort 只让本地 UI 收尾，服务端 detached 回合仍在跑，
    // 因此带回合身份投递 interrupt（best-effort，失败不阻塞本地停止）。
    // 按**选中会话**定位：停止按钮只作用于当前会话，后台会话的回合不受影响。
    const entry = turnRegistry.getSnapshot(selectedTurnKey).entry;
    void requestSessionTurnInterrupt(
      entry?.turnState.currentSessionId,
      entry ? (entry.activeTurnId ?? entry.turnId) : null,
    );
    entry?.controller.abort();
  }

  return {
    activeSessionKeys,
    composerAttachments,
    draft,
    isResponding,
    streamStalled,
    activeTurnId,
    phase,
    clearStreamStall,
    trajectoryStore,
    modelOptions,
    providerOptions,
    // P2-7 子片 3：原始运行时目录（`/model` 弹窗按 provider 分组的唯一数据源）。
    runtimeModels,
    runtimeModelsError,
    runtimeModelsLoading,
    runtimeSkills,
    runtimeSkillsError,
    runtimeSkillsLoading,
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
