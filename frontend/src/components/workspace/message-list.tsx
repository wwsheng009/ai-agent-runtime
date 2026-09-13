// 由 components/workspace/message-list.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { ScrollTextIcon } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";

import { type Artifact } from "@/data/mock";
import { isArtifactEvidence } from "@/lib/workspace-artifacts";
import { cn } from "@/lib/utils";
import { type ChatStreamPhase } from "@/types/runtime";

import { AssistantMessageCard } from "./message-list/assistant-message-card";
import { HistoryContextMessageCard } from "./message-list/history-context-message-card";
import type { MessageListProps } from "./message-list/types";
import { UserMessageBubble } from "./message-list/user-message-bubble";

export type { MessageBacktrackOptions } from "./message-list/types";

const SCROLL_FOLLOW_THRESHOLD = 120;

const PHASE_LABELS: Record<ChatStreamPhase, string> = {
  connecting: "Connecting to runtime…",
  "first-token": "Waiting for first output…",
  streaming: "Streaming output…",
  tool: "Calling tools…",
  finalizing: "Finalizing turn…",
};

export function MessageList({
  artifacts,
  backtrackError = null,
  backtrackNotice = null,
  backtrackPendingMessageId = null,
  backtrackNavigationActive = false,
  backtrackSelectedMessageId = null,
  canBacktrack = false,
  className,
  contentClassName,
  isResponding,
  messages,
  onBacktrackToMessage,
  onSelectBacktrackNavigationMessage,
  onSelectArtifact,
  phase,
  style,
}: MessageListProps) {
  const artifactMap = new Map(artifacts.map((artifact) => [artifact.id, artifact]));
  const lastMessage = messages[messages.length - 1];
  const streamingMessageId =
    isResponding && lastMessage?.role === "assistant" ? lastMessage.id : null;
  const logLabel =
    messages.length > 0 ? "Workspace conversation timeline" : "Empty workspace conversation timeline";
  const [editingMessageId, setEditingMessageId] = useState<string | null>(null);
  const [inlineEditDraft, setInlineEditDraft] = useState("");
  const selectedMessageRef = useRef<HTMLElement | null>(null);
  const scrollContainerRef = useRef<HTMLDivElement | null>(null);
  // Tracks whether the user has scrolled away from the bottom. Streaming
  // follow stays active until the user actively scrolls up (a single large
  // content flush must not be mistaken for leaving the bottom), and resumes
  // as soon as they scroll back down.
  const [userScrolledAway, setUserScrolledAway] = useState(false);
  // Mirror of the state above for layout/frame callbacks, which must observe
  // the latest scroll intent without waiting for a re-render.
  const userScrolledAwayRef = useRef(false);
  // Tracks the previous "responding" flag so the turn's final content flush
  // (which often arrives in the same commit that clears isResponding) still
  // follows, without hijacking history restore / backtrack navigation.
  const wasRespondingRef = useRef(isResponding);

  const handleScroll = () => {
    const container = scrollContainerRef.current;
    if (!container) {
      return;
    }
    const distanceFromBottom =
      container.scrollHeight - container.scrollTop - container.clientHeight;
    const next = distanceFromBottom > SCROLL_FOLLOW_THRESHOLD;
    userScrolledAwayRef.current = next;
    setUserScrolledAway(next);
  };

  useLayoutEffect(() => {
    // 流式期间保持贴底；收尾那一帧（isResponding 刚翻 false、内容与 done
    // 同批提交）也要贴底，否则列表会停在顶部。历史回放/回溯定位由
    // backtrackNavigationActive 分支单独接管滚动位置。
    const wasResponding = wasRespondingRef.current;
    wasRespondingRef.current = isResponding;
    if (
      (!isResponding && !wasResponding) ||
      userScrolledAway ||
      backtrackNavigationActive
    ) {
      return;
    }
    const container = scrollContainerRef.current;
    if (!container) {
      return;
    }
    container.scrollTop = container.scrollHeight;
    // 提交后内容仍可能再长高一帧（markdown/代码高亮/字体回流），补一次贴底。
    const frame = requestAnimationFrame(() => {
      const next = scrollContainerRef.current;
      if (!next || userScrolledAwayRef.current) {
        return;
      }
      next.scrollTop = next.scrollHeight;
    });
    return () => cancelAnimationFrame(frame);
  }, [backtrackNavigationActive, isResponding, messages, userScrolledAway]);

  useEffect(() => {
    if (!editingMessageId) {
      return;
    }
    if (!messages.some((message) => message.id === editingMessageId)) {
      // P0-2 机械搬迁：保留原「编辑目标不在列表时同步复位编辑态」语义。
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setEditingMessageId(null);
      setInlineEditDraft("");
    }
  }, [editingMessageId, messages]);

  useEffect(() => {
    if ((isResponding || backtrackNavigationActive) && editingMessageId) {
      // P0-2 机械搬迁：保留原「流式响应/回溯导航期间同步退出内联编辑」语义。
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setEditingMessageId(null);
      setInlineEditDraft("");
    }
  }, [backtrackNavigationActive, editingMessageId, isResponding]);

  useEffect(() => {
    if (!backtrackNavigationActive || !backtrackSelectedMessageId) {
      return;
    }
    selectedMessageRef.current?.scrollIntoView({
      behavior: "smooth",
      block: "nearest",
    });
  }, [backtrackNavigationActive, backtrackSelectedMessageId]);

  return (
    <div
      ref={scrollContainerRef}
      onScroll={handleScroll}
      className={cn("flex-1 overflow-y-auto px-3 py-4 sm:px-4", className)}
      style={style}
    >
      <div
        aria-atomic="false"
        aria-busy={isResponding ? "true" : undefined}
        aria-label={logLabel}
        aria-live="polite"
        aria-relevant="additions text"
        className={cn("mx-auto flex max-w-[52rem] flex-col gap-6", contentClassName)}
        role="log"
      >
        {messages.length === 0 ? (
          <div className="rounded-[0.95rem] border border-dashed border-border bg-surface-softer px-4 py-8 text-center">
            <div className="mx-auto inline-flex size-10 items-center justify-center rounded-[0.8rem] border border-accent-teal/18 bg-accent-teal/10 text-accent-teal">
              <ScrollTextIcon size={18} />
            </div>
            <div className="mt-3 text-sm font-semibold text-foreground">
              The thread timeline is empty
            </div>
            <p className="mt-2 text-sm leading-6 text-muted-foreground">
              Start a turn to populate the workspace timeline. Runtime evidence,
              related items, and streamed output will attach back to the
              messages that produced them.
            </p>
          </div>
        ) : null}

        {backtrackNavigationActive ? (
          <div className="rounded-[0.85rem] border border-accent-gold/20 bg-accent-gold/8 px-3.5 py-3 text-sm leading-6 text-muted-foreground">
            Backtrack navigation active — use ↑/↓ (or j/k) to choose a user turn,
            Enter to open the confirm dialog, Esc to exit.
          </div>
        ) : null}

        {backtrackError ? (
          <div className="rounded-[0.85rem] border border-accent-orange/18 bg-accent-orange/8 px-3.5 py-3 text-sm leading-6 text-muted-foreground">
            {backtrackError}
          </div>
        ) : null}

        {backtrackNotice ? (
          <div className="rounded-[0.85rem] border border-accent-teal/18 bg-accent-teal/10 px-3.5 py-3 text-sm leading-6 text-muted-foreground">
            {backtrackNotice}
          </div>
        ) : null}

        {messages.map((message, messageIndex) => {
          const relatedEvidence = (message.relatedArtifactIds ?? [])
            .map((artifactId) => artifactMap.get(artifactId))
            .filter((artifact): artifact is Artifact => artifact !== undefined)
            .filter((artifact) => isArtifactEvidence(artifact));
          const isUser = message.role === "user";
          const isHistoryContextMessage =
            message.label === "system" ||
            message.label === "tool" ||
            message.author === "System context" ||
            message.author === "Tool receipt";
          const labelId = `${message.id}-label`;
          const metaId = `${message.id}-meta`;
          const statusId = `${message.id}-status`;
          const describedBy = [metaId, statusId].join(" ");
          const backtrackPending = backtrackPendingMessageId === message.id;
          const showBacktrack =
            isUser && canBacktrack && typeof onBacktrackToMessage === "function";
          const isEditing = isUser && editingMessageId === message.id;
          const isNavigationSelected =
            isUser &&
            backtrackNavigationActive &&
            backtrackSelectedMessageId === message.id;
          const actionsDisabled =
            Boolean(backtrackPendingMessageId) ||
            isResponding ||
            backtrackNavigationActive;

          return (
            <article
              aria-busy={message.id === streamingMessageId ? "true" : undefined}
              aria-current={isNavigationSelected ? "true" : undefined}
              aria-describedby={describedBy}
              aria-labelledby={labelId}
              aria-setsize={messages.length}
              aria-posinset={messageIndex + 1}
              data-backtrack-selected={isNavigationSelected ? "true" : undefined}
              key={message.id}
              ref={isNavigationSelected ? selectedMessageRef : undefined}
              className={cn("flex w-full", isUser ? "justify-end" : "justify-start")}
            >
              {isUser ? (
                <UserMessageBubble
                  actionsDisabled={actionsDisabled}
                  backtrackNavigationActive={backtrackNavigationActive}
                  backtrackPending={backtrackPending}
                  inlineEditDraft={inlineEditDraft}
                  isEditing={isEditing}
                  isNavigationSelected={isNavigationSelected}
                  labelId={labelId}
                  message={message}
                  metaId={metaId}
                  onBacktrackToMessage={onBacktrackToMessage}
                  onSelectArtifact={onSelectArtifact}
                  onSelectBacktrackNavigationMessage={onSelectBacktrackNavigationMessage}
                  setEditingMessageId={setEditingMessageId}
                  setInlineEditDraft={setInlineEditDraft}
                  showBacktrack={showBacktrack}
                  statusId={statusId}
                />
              ) : isHistoryContextMessage ? (
                <HistoryContextMessageCard
                  labelId={labelId}
                  message={message}
                  metaId={metaId}
                  onSelectArtifact={onSelectArtifact}
                  relatedEvidence={relatedEvidence}
                  statusId={statusId}
                  streamingMessageId={streamingMessageId}
                />
              ) : (
                <AssistantMessageCard
                  labelId={labelId}
                  message={message}
                  metaId={metaId}
                  onSelectArtifact={onSelectArtifact}
                  relatedEvidence={relatedEvidence}
                  statusId={statusId}
                  streamingMessageId={streamingMessageId}
                />
              )}
            </article>
          );
        })}

        {isResponding ? (
          <div
            aria-atomic="true"
            aria-live="polite"
            className="inline-flex items-center gap-2 app-text-10 uppercase tracking-[0.14em] text-muted-foreground"
            role="status"
          >
            <span className="size-2 rounded-full animate-pulse bg-accent-teal" />
            {phase ? PHASE_LABELS[phase] : "Runtime stream active"}
          </div>
        ) : null}
      </div>
    </div>
  );
}
