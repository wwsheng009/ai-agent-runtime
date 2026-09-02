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
import { useRuntimeModelCatalog } from "@/hooks/workspace/use-runtime-model-catalog";
import {
  createTrajectoryStore,
  type TrajectoryStore,
} from "@/hooks/workspace/use-trajectory-snapshot";
import { NEW_THREAD_ID } from "@/hooks/workspace/use-workspace-thread-selection";
import {
  streamAgentChat,
  type AgentChatResult,
  type AgentChatStreamDonePayload,
  type AgentChatStreamMetaPayload,
} from "@/lib/runtime-api";
import { getSessionLeaseConflictTitle } from "@/api/runtime/shared";
import { debugTrajectoryConsistency } from "@/lib/trajectory/projection";
import type { TrajectoryEventKind } from "@/lib/trajectory/types";
import {
  appendArtifactToMessage,
  buildToolSegmentFromPayload,
  buildAssistantMessageSegments,
  buildGeneratedImageAttachments,
  buildGeneratedImagePlaceholderSegment,
  buildTurnJsonArtifact,
  createStreamingAssistantMessage,
  getAssistantMessageReasoning,
  getAssistantMessageText,
  getErrorMessage,
  getStreamTextDelta,
  getToolName,
  getToolErrorMessage,
  isRuntimePayload,
  mergeUniqueStrings,
  reconcileRuntimeText,
  getRuntimeDeltaKey,
  upsertGeneratedImageSegment,
  upsertToolSegment,
  updateThreadMessage,
  upsertArtifact,
  upsertArtifacts,
  type RuntimeDeltaCoordinator,
  type ToolMessageSegment,
} from "@/lib/workspace-thread-state";
import type {
  AgentChatStreamChunkPayload,
  ChatStreamPhase,
} from "@/types/runtime";

type WorkspaceAgentChatTurnOptions = {
  deltaCoordinator?: RuntimeDeltaCoordinator;
  onSessionTouched?: () => void;
  selectedThread: Thread | undefined;
  setSelectedArtifactId: (artifactId: string | null) => void;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
  userId?: string;
  workspacePath?: string;
};

export function shouldIgnoreTerminalStreamError(options: {
  finalized: boolean;
  aborted: boolean;
}) {
  return options.finalized || options.aborted;
}

/** 连接 runtime 的软超时：超过该时长仍未收到任何 SSE 事件即判定为连接失败。 */
const RUNTIME_CONNECT_TIMEOUT_MS = 15_000;

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
  const [draft, setDraft] = useState("");
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
    runtimeModelsError,
    runtimeModelsLoading,
    selectedModel,
    selectedProvider,
    setSelectedModel,
    setSelectedProvider,
  } = useRuntimeModelCatalog();

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
      workspace_path: workspacePath || undefined,
      provider: selectedProvider || undefined,
      model: selectedModel || undefined,
      reasoning_effort: settings.chat.reasoningEffort || undefined,
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

    let currentSessionId = threadSnapshot.sessionId ?? "";
    let currentSource = threadSnapshot.runtimeSource ?? "runtime";
    let currentKind = "llm";
    let streamedText = "";
    let reasoningText = "";
    let planningPayload: Record<string, unknown> | null = null;
    let orchestrationPayload: Record<string, unknown> | null = null;
    let routePayload: Record<string, unknown> | null = null;
    const observationPayloads: Record<string, unknown>[] = [];
    const subagentPayloads: Record<string, unknown>[] = [];
    const toolPayloads: AgentChatStreamChunkPayload[] = [];
    let finalResult: AgentChatResult | null = null;
    let turnFinalized = false;
    let receivedRuntimeActivity = false;
    let receivedErrorEvent = false;
    let connectTimedOut = false;
    let pendingStreamingFrame: number | null = null;
    let pendingStreamingTimeout: number | null = null;
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
            currentSource,
            reasoningText,
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
        currentSource,
        currentKind === "agent" ? "Runtime agent" : "Runtime stream",
        streamedText,
      );
    };

    const clearPendingStreamingTimeout = () => {
      if (
        pendingStreamingTimeout !== null &&
        typeof window !== "undefined" &&
        typeof window.clearTimeout === "function"
      ) {
        window.clearTimeout(pendingStreamingTimeout);
      }
      pendingStreamingTimeout = null;
    };

    const cancelStreamingFrame = () => {
      if (
        pendingStreamingFrame !== null &&
        typeof window !== "undefined" &&
        typeof window.cancelAnimationFrame === "function"
      ) {
        window.cancelAnimationFrame(pendingStreamingFrame);
      }
      pendingStreamingFrame = null;
      clearPendingStreamingTimeout();
    };

    const flushStreamingMessage = () => {
      cancelStreamingFrame();
      renderStreamingMessage();
    };

    const scheduleStreamingMessage = () => {
      if (pendingStreamingFrame !== null) {
        return;
      }

      if (
        typeof window === "undefined" ||
        typeof window.requestAnimationFrame !== "function"
      ) {
        flushStreamingMessage();
        return;
      }

      pendingStreamingFrame = window.requestAnimationFrame(() => {
        pendingStreamingFrame = null;
        clearPendingStreamingTimeout();
        renderStreamingMessage();
      });

      // Background tabs do not fire rAF; a setTimeout fallback keeps the
      // stream rendering alive (G3) and flushes as soon as the tab returns.
      pendingStreamingTimeout = window.setTimeout(() => {
        if (
          pendingStreamingFrame !== null &&
          typeof window.cancelAnimationFrame === "function"
        ) {
          window.cancelAnimationFrame(pendingStreamingFrame);
          pendingStreamingFrame = null;
        }
        pendingStreamingTimeout = null;
        renderStreamingMessage();
      }, 100);
    };

    const handleVisibilityChange = () => {
      if (document.visibilityState === "visible") {
        flushStreamingMessage();
      }
    };

    const upsertLiveToolSegment = (
      payload: AgentChatStreamChunkPayload,
      status: ToolMessageSegment["status"],
      eventType: string,
    ) => {
      toolPayloads.push(payload);
      setPhaseAndRef("tool");
      attachTurnArtifact(
        buildTurnJsonArtifact(
          turnId,
          "tool-events",
          "Tool events observed during agent chat SSE.",
          toolPayloads,
        ),
      );
      updateCurrentThread((thread) =>
        updateThreadMessage(thread, assistantMessageId, (message) => ({
          ...message,
          segments: upsertToolSegment(
            message.segments,
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

    const attachVisibilityListener = () => {
      if (typeof document === "undefined") {
        return;
      }
      document.addEventListener("visibilitychange", handleVisibilityChange);
    };

    const detachVisibilityListener = () => {
      if (typeof document === "undefined") {
        return;
      }
      document.removeEventListener("visibilitychange", handleVisibilityChange);
    };

    const updateStreamingError = (
      message: string,
      heading = "Runtime stream failed.",
    ) => {
      cancelStreamingFrame();
      const hasStreamedText = streamedText.trim().length > 0;
      updateCurrentThread((thread) =>
        updateThreadMessage(thread, assistantMessageId, (currentMessage) => {
          const segments = buildAssistantMessageSegments(
            hasStreamedText ? streamedText : `${heading}\n\n${message}`,
            currentSource,
            reasoningText,
            {
              existingSegments: currentMessage.segments,
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
    };

    const finalizeTurn = (
      payload: Partial<AgentChatStreamDonePayload> = {},
      options?: {
        stopped?: boolean;
      },
    ) => {
      if (turnFinalized) {
        return;
      }

      turnFinalized = true;
      if (activeTurnIdRef.current === turnId) {
        activeTurnIdRef.current = null;
        setActiveTurnId(null);
      }
      cancelStreamingFrame();
      setPhaseAndRef("finalizing");

      if (payload.session_id) {
        currentSessionId = payload.session_id;
      }
      if (payload.source) {
        currentSource = payload.source;
      }
      if (payload.result) {
        finalResult = payload.result;
        if (payload.result.source) {
          currentSource = payload.result.source;
        }
        if (payload.result.kind) {
          currentKind = payload.result.kind;
        }
        if (payload.result.reasoning && payload.result.reasoning.trim()) {
          reasoningText = reconcileRuntimeText(
            reasoningText,
            payload.result.reasoning,
          );
        }
      }
      if (payload.content && payload.content.trim()) {
        streamedText = reconcileRuntimeText(streamedText, payload.content);
      }

      const generatedImageAttachments = buildGeneratedImageAttachments(
        currentSessionId,
        finalResult?.metadata ?? null,
      );

      const stopped = options?.stopped === true;
      const terminalPayload: AgentChatStreamDonePayload = {
        session_id: currentSessionId,
        agent_id: payload.agent_id,
        source: currentSource,
        status: payload.status ?? (stopped ? "stopped" : "completed"),
        content: streamedText,
        result: finalResult ?? payload.result,
        turn_id: payload.turn_id ?? turnId,
      };

      const artifacts = buildFinalArtifacts(
        turnId,
        terminalPayload,
        currentSessionId,
        currentSource,
        finalResult,
        planningPayload,
        orchestrationPayload,
        routePayload,
        observationPayloads,
        subagentPayloads,
        toolPayloads,
        generatedImageAttachments.artifacts,
      );

      const terminalText = reconcileRuntimeText(
        streamedText,
        finalResult?.output ?? payload.content,
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
          sessionId: currentSessionId || thread.sessionId,
          transport:
            thread.transport === "error"
              ? "error"
              : currentSessionId || receivedRuntimeActivity
                ? "live"
                : thread.transport,
          runtimeSource: currentSource || thread.runtimeSource,
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
              reasoningText,
              finalResult?.reasoning,
            ),
          );
          const resolvedText = reconciledText || finalText;
          const finalSegments = buildAssistantMessageSegments(
            resolvedText,
            currentSource,
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
              finalResult?.kind === "agent" || currentKind === "agent"
                ? "Runtime agent"
                : "Runtime stream",
            label: stopped ? "stopped" : currentSource || "runtime",
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
      if (currentSessionId) {
        onSessionTouched?.();
      }

      if (!stopped) {
        maybeShowDesktopNotification(
          settings.notification.enabled && settings.notification.desktop,
          `${threadSnapshot.title} completed`,
          buildNotificationBody(finalText, currentSource),
          currentSessionId || threadId,
        );
      }
    };

    setDraft("");
    // 新 turn 开始：轨迹快照 reset（同步于首个 SSE 事件之前，避免
    // 导航渲染迟到的 effect reset 打断流事件收集造成 seq gap）。
    trajectoryStore.reset();
    activeTurnIdRef.current = turnId;
    setActiveTurnId(turnId);
    setIsResponding(true);
    setPhaseAndRef("connecting");
    attachVisibilityListener();
    activeRequestControllerRef.current = controller;
    // 连接超时保护：后端可能因共享 SQLite 被其他进程锁住而无法在有限时间内
    // 建立 SSE 流（表现为 "Connecting to runtime…" 无限旋转）。若 N 秒内未收到
    // 任何 runtime 事件（onMeta 等会置 receivedRuntimeActivity），主动中止请求
    // 并进入错误状态，而不是让前端永远卡在 connecting 阶段。
    let connectTimeoutId: number | undefined;
    const startConnectTimeout = () => {
      connectTimeoutId = window.setTimeout(() => {
        if (receivedRuntimeActivity || turnFinalized || controller.signal.aborted) {
          return;
        }
        connectTimedOut = true;
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
            onMeta: (payload: AgentChatStreamMetaPayload) => {
              pushTrajectory("meta", payload);
              receivedRuntimeActivity = true;
              setPhaseAndRef("first-token");
              if (payload.session_id) {
                currentSessionId = payload.session_id;
              }
              if (payload.source) {
                currentSource = payload.source;
              }
              if (payload.kind) {
                currentKind = payload.kind;
              }

              updateCurrentThread((thread) => {
                let nextThread: Thread = {
                  ...thread,
                  updatedAt: new Date().toISOString(),
                  sessionId: currentSessionId || thread.sessionId,
                  transport: "live",
                  runtimeSource: currentSource,
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
                  orchestrationPayload = payload.orchestration;
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
                  planningPayload = payload.planning;
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
            onChunk: (payload) => {
              pushTrajectory("chunk", payload);
              receivedRuntimeActivity = true;
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
              streamedText += delta;
              setPhaseAndRef("streaming");
              scheduleStreamingMessage();
            },
            onReasoning: (payload) => {
              pushTrajectory("reasoning", payload);
              receivedRuntimeActivity = true;
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
                reasoningText += delta;
                setPhaseAndRef("streaming");
                scheduleStreamingMessage();
              }
            },
            onToolStart: (payload) => {
              pushTrajectory("tool_start", payload);
              receivedRuntimeActivity = true;
              upsertLiveToolSegment(payload, "started", "tool_start");
            },
            onToolCall: (payload) => {
              pushTrajectory("tool_call", payload);
              receivedRuntimeActivity = true;
              upsertLiveToolSegment(payload, "running", "tool_call");
            },
            onToolEnd: (payload) => {
              pushTrajectory("tool_end", payload);
              receivedRuntimeActivity = true;
              handleToolEnd(payload);
            },
            onPlanning: (payload) => {
              pushTrajectory("planning", payload);
              receivedRuntimeActivity = true;
              planningPayload = payload ?? null;
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
            onOrchestration: (payload) => {
              pushTrajectory("orchestration", payload);
              receivedRuntimeActivity = true;
              orchestrationPayload = payload ?? null;
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
            onRoute: (payload) => {
              pushTrajectory("route", payload);
              receivedRuntimeActivity = true;
              routePayload = payload ?? null;
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
            onObservation: (payload) => {
              pushTrajectory("observation", payload);
              receivedRuntimeActivity = true;
              if (!payload) {
                return;
              }
              observationPayloads.push(payload);
              attachTurnArtifact(
                buildTurnJsonArtifact(
                  turnId,
                  "observations",
                  "Observation events emitted by static SSE execution.",
                  observationPayloads,
                ),
              );
            },
            onSubagent: (payload) => {
              pushTrajectory("subagent", payload);
              receivedRuntimeActivity = true;
              if (!payload) {
                return;
              }
              subagentPayloads.push(payload);
              attachTurnArtifact(
                buildTurnJsonArtifact(
                  turnId,
                  "subagents",
                  "Subagent events emitted by static SSE execution.",
                  subagentPayloads,
                ),
              );
            },
            onResult: (payload) => {
              pushTrajectory("result", payload);
              receivedRuntimeActivity = true;
              finalResult = payload;
              if (payload.source) {
                currentSource = payload.source;
              }
              if (payload.kind) {
                currentKind = payload.kind;
              }
              if (payload.reasoning && payload.reasoning.trim()) {
                reasoningText = reconcileRuntimeText(
                  reasoningText,
                  payload.reasoning,
                );
              }
              if (payload.output && payload.output.trim()) {
                streamedText = reconcileRuntimeText(
                  streamedText,
                  payload.output,
                );
                flushStreamingMessage();
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
              receivedRuntimeActivity = true;
              finalizeTurn(payload);
            },
            onErrorEvent: (payload) => {
              if (
                shouldIgnoreTerminalStreamError({
                  finalized: turnFinalized,
                  aborted: controller.signal.aborted,
                })
              ) {
                return;
              }
              pushTrajectory("error", payload);
              receivedErrorEvent = true;
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
              maybeShowDesktopNotification(
                settings.notification.enabled && settings.notification.desktop,
                `${threadSnapshot.title} failed`,
                message,
                currentSessionId || threadId,
              );
            },
          },
        );
        if (!turnFinalized && !receivedErrorEvent) {
          finalizeTurn();
        }
      } catch (error) {
        if (connectTimedOut) {
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
            finalized: turnFinalized,
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
        maybeShowDesktopNotification(
          settings.notification.enabled && settings.notification.desktop,
          `${threadSnapshot.title} failed`,
          message,
          currentSessionId || threadId,
        );
      } finally {
        clearConnectTimeout();
        cancelStreamingFrame();
        detachVisibilityListener();
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
    selectedModel,
    selectedProvider,
    setDraft,
    setSelectedModel,
    setSelectedProvider,
    stopResponding,
    submitPrompt,
  };
}

function buildNotificationBody(content: string, source: string) {
  const normalized = content.replace(/\s+/g, " ").trim();
  if (!normalized) {
    return `Runtime stream from ${source || "runtime"} finished.`;
  }

  return normalized.length > 140
    ? `${normalized.slice(0, 137).trimEnd()}...`
    : normalized;
}

function maybeShowDesktopNotification(
  enabled: boolean,
  title: string,
  body: string,
  tag: string,
) {
  if (!enabled || typeof Notification === "undefined" || typeof document === "undefined") {
    return;
  }

  if (Notification.permission !== "granted" || document.visibilityState === "visible") {
    return;
  }

  try {
    new Notification(title, {
      body,
      tag,
    });
  } catch {
    // Ignore browser notification failures and keep the chat flow intact.
  }
}

function createThreadFromPrompt(prompt: string): Thread {
  const now = new Date().toISOString();
  const normalized = prompt.replace(/\s+/g, " ").trim();
  const title =
    normalized.length > 56 ? `${normalized.slice(0, 56).trimEnd()}...` : normalized;

  return {
    id: `thread-${crypto.randomUUID()}`,
    title: title || "New chat",
    summary:
      normalized.length > 120
        ? `${normalized.slice(0, 120).trimEnd()}...`
        : normalized,
    updatedAt: now,
    status: "draft",
    transport: "mock",
    runtimeEventCount: 0,
    lastError: null,
    tags: ["new", "workspace"],
    prompts: [],
    messages: [],
    artifacts: [],
  };
}

function buildFinalArtifacts(
  turnId: string,
  payload: AgentChatStreamDonePayload,
  currentSessionId: string,
  currentSource: string,
  finalResult: AgentChatResult | null,
  planningPayload: Record<string, unknown> | null,
  orchestrationPayload: Record<string, unknown> | null,
  routePayload: Record<string, unknown> | null,
  observationPayloads: Record<string, unknown>[],
  subagentPayloads: Record<string, unknown>[],
  toolPayloads: AgentChatStreamChunkPayload[],
  generatedImageArtifacts: Artifact[],
) {
  const artifacts: Artifact[] = [
    buildTurnJsonArtifact(
      turnId,
      "agent-chat-response",
      "Final SSE response envelope reconstructed in the frontend workspace.",
      {
        session_id: currentSessionId,
        agent_id: payload.agent_id ?? "",
        source: payload.source ?? currentSource,
        status: payload.status ?? "completed",
        result: finalResult,
        planning: planningPayload,
        orchestration: orchestrationPayload,
        route: routePayload,
        observations: observationPayloads,
        subagents: subagentPayloads,
        tool_events: toolPayloads,
      },
    ),
  ];

  if (planningPayload) {
    artifacts.push(
      buildTurnJsonArtifact(
        turnId,
        "planning",
        "Planning payload emitted by /api/agent/chat SSE.",
        planningPayload,
      ),
    );
  }
  if (orchestrationPayload) {
    artifacts.push(
      buildTurnJsonArtifact(
        turnId,
        "orchestration",
        "Structured orchestration summary emitted by /api/agent/chat SSE.",
        orchestrationPayload,
      ),
    );
  }
  if (toolPayloads.length > 0) {
    artifacts.push(
      buildTurnJsonArtifact(
        turnId,
        "tool-events",
        "Tool events observed during agent chat SSE.",
        toolPayloads,
      ),
    );
  }
  if (routePayload) {
    artifacts.push(
      buildTurnJsonArtifact(
        turnId,
        "route",
        "Route metadata emitted by static SSE execution.",
        routePayload,
      ),
    );
  }
  if (observationPayloads.length > 0) {
    artifacts.push(
      buildTurnJsonArtifact(
        turnId,
        "observations",
        "Observation events emitted by static SSE execution.",
        observationPayloads,
      ),
    );
  }
  if (subagentPayloads.length > 0) {
    artifacts.push(
      buildTurnJsonArtifact(
        turnId,
        "subagents",
        "Subagent events emitted by static SSE execution.",
        subagentPayloads,
      ),
    );
  }

  if (generatedImageArtifacts.length > 0) {
    artifacts.push(...generatedImageArtifacts);
  }

  return artifacts;
}
