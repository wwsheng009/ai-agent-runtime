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
import { NEW_THREAD_ID } from "@/hooks/workspace/use-workspace-thread-selection";
import {
  streamAgentChat,
  type AgentChatResult,
  type AgentChatStreamDonePayload,
  type AgentChatStreamMetaPayload,
} from "@/lib/runtime-api";
import {
  appendArtifactToMessage,
  buildAssistantMessageSegments,
  buildGeneratedImageAttachments,
  buildGeneratedImagePlaceholderSegment,
  buildTurnJsonArtifact,
  createStreamingAssistantMessage,
  getErrorMessage,
  getStreamTextDelta,
  getToolName,
  isRuntimePayload,
  mergeUniqueStrings,
  upsertGeneratedImageSegment,
  updateThreadMessage,
  upsertArtifact,
  upsertArtifacts,
} from "@/lib/workspace-thread-state";
import type { AgentChatStreamChunkPayload } from "@/types/runtime";

type WorkspaceAgentChatTurnOptions = {
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

export function useWorkspaceAgentChatTurn({
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
  const activeRequestControllerRef = useRef<AbortController | null>(null);
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
    let pendingStreamingFrame: number | null = null;
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
              existingSegments: message.segments,
            },
          ),
        })),
      );
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
    };

    const flushStreamingMessage = () => {
      cancelStreamingFrame();
      setStreamingMessage(
        currentSource,
        currentKind === "agent" ? "Runtime agent" : "Runtime stream",
        streamedText,
      );
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
        setStreamingMessage(
          currentSource,
          currentKind === "agent" ? "Runtime agent" : "Runtime stream",
          streamedText,
        );
      });
    };

    const updateStreamingError = (message: string) => {
      cancelStreamingFrame();
      const content = streamedText.trim()
        ? `${streamedText}\n\n[stream interrupted]\n${message}`
        : `Runtime stream failed.\n\n${message}`;
      updateCurrentThread((thread) =>
        updateThreadMessage(thread, assistantMessageId, (currentMessage) => ({
          ...currentMessage,
          author: "Runtime error",
          label: "error",
          segments: buildAssistantMessageSegments(
            content,
            currentSource,
            reasoningText,
            {
              existingSegments: currentMessage.segments,
            },
          ),
        })),
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
      cancelStreamingFrame();

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
          reasoningText = payload.result.reasoning;
        }
      }
      if (payload.content && payload.content.trim()) {
        streamedText = payload.content;
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

      const finalText =
        streamedText ||
        finalResult?.output?.trim() ||
        (stopped
          ? "Response stopped before any text was returned."
          : "Runtime request completed, but no textual output was returned.");

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

        nextThread = updateThreadMessage(nextThread, assistantMessageId, (message) => ({
          ...message,
          author:
            finalResult?.kind === "agent" || currentKind === "agent"
              ? "Runtime agent"
              : "Runtime stream",
          label: stopped ? "stopped" : currentSource || "runtime",
          segments: buildAssistantMessageSegments(
            finalText,
            currentSource,
            reasoningText,
            {
              status: stopped ? "stopped" : undefined,
              existingSegments: message.segments,
              generatedImages: generatedImageAttachments,
            },
          ),
          relatedArtifactIds: mergeUniqueStrings(
            ...(message.relatedArtifactIds ?? []),
            ...artifacts.map((artifact) => artifact.id),
          ),
        }));

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
    setIsResponding(true);
    activeRequestControllerRef.current = controller;
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
        createStreamingAssistantMessage(assistantMessageId, [requestArtifact.id]),
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
              receivedRuntimeActivity = true;
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

                nextThread = updateThreadMessage(nextThread, assistantMessageId, (message) => ({
                  ...message,
                  author:
                    payload.kind === "agent" ? "Runtime agent" : "Runtime stream",
                  label: payload.source ?? "streaming",
                }));

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
              receivedRuntimeActivity = true;
              if (payload.type === "image") {
                const imageProgress = buildGeneratedImagePlaceholderSegment(
                  payload.metadata,
                );
                if (imageProgress) {
                  updateCurrentThread((thread) =>
                    updateThreadMessage(thread, assistantMessageId, (message) => ({
                      ...message,
                      segments: upsertGeneratedImageSegment(
                        message.segments,
                        imageProgress,
                      ),
                    })),
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
              streamedText += delta;
              scheduleStreamingMessage();
            },
            onReasoning: (payload) => {
              receivedRuntimeActivity = true;
              const delta =
                typeof payload.content === "string"
                  ? payload.content
                  : payload.reasoning && typeof payload.reasoning.content === "string"
                    ? payload.reasoning.content
                    : "";
              if (delta) {
                reasoningText += delta;
                if (streamedText) {
                  scheduleStreamingMessage();
                }
              }
            },
            onToolStart: (payload) => {
              receivedRuntimeActivity = true;
              toolPayloads.push(payload);
              attachTurnArtifact(
                buildTurnJsonArtifact(
                  turnId,
                  "tool-events",
                  "Tool events observed during agent chat SSE.",
                  toolPayloads,
                ),
              );
              updateCurrentThread((thread) => ({
                ...thread,
                lastRuntimeEventType: `tool_start:${getToolName(payload)}`,
              }));
            },
            onToolCall: (payload) => {
              receivedRuntimeActivity = true;
              toolPayloads.push(payload);
              attachTurnArtifact(
                buildTurnJsonArtifact(
                  turnId,
                  "tool-events",
                  "Tool events observed during agent chat SSE.",
                  toolPayloads,
                ),
              );
              updateCurrentThread((thread) => ({
                ...thread,
                lastRuntimeEventType: `tool_call:${getToolName(payload)}`,
              }));
            },
            onToolEnd: (payload) => {
              receivedRuntimeActivity = true;
              toolPayloads.push(payload);
              attachTurnArtifact(
                buildTurnJsonArtifact(
                  turnId,
                  "tool-events",
                  "Tool events observed during agent chat SSE.",
                  toolPayloads,
                ),
              );
              updateCurrentThread((thread) => ({
                ...thread,
                lastRuntimeEventType: `tool_end:${getToolName(payload)}`,
              }));
            },
            onPlanning: (payload) => {
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
              receivedRuntimeActivity = true;
              finalResult = payload;
              if (payload.source) {
                currentSource = payload.source;
              }
              if (payload.kind) {
                currentKind = payload.kind;
              }
              if (payload.reasoning && payload.reasoning.trim()) {
                reasoningText = payload.reasoning;
              }
              if (payload.output && payload.output.trim()) {
                streamedText = payload.output;
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
      } finally {
        cancelStreamingFrame();
        if (activeRequestControllerRef.current === controller) {
          activeRequestControllerRef.current = null;
        }
        setIsResponding(false);
      }
    })();
  }

  function stopResponding() {
    activeRequestControllerRef.current?.abort();
  }

  return {
    draft,
    isResponding,
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
