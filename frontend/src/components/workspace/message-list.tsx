// 由 components/workspace/message-list.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { ScrollTextIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { ConnectionStatusBadge } from "@/components/ui/connection-status-badge";
import { type Artifact } from "@/data/mock";
import { useConnectionStatusLabels } from "@/hooks/workspace/use-connection-status-labels";
import { useConversationScroll } from "@/hooks/workspace/use-conversation-scroll";
import {
  isContextMessage,
  isSystemPromptMessage,
  isToolReceiptMessage,
} from "@/lib/chat-view";
import { isArtifactEvidence } from "@/lib/workspace-artifacts";
import { cn } from "@/lib/utils";
import { type ChatStreamPhase } from "@/types/runtime";

import { AssistantMessageCard } from "./message-list/assistant-message-card";
import { HistoryContextMessageCard } from "./message-list/history-context-message-card";
import { HistoryToolMessageRow } from "./message-list/history-tool-message-row";
import { NoticeRow } from "./message-list/notice-row";
import type { MessageListProps } from "./message-list/types";
import { UserMessageBubble } from "./message-list/user-message-bubble";

export type { MessageBacktrackOptions } from "./message-list/types";

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
  connectionStatus = null,
  contentClassName,
  isResponding,
  messages,
  onBacktrackToMessage,
  onPreviewFilePath,
  onRetryConnection,
  onSelectBacktrackNavigationMessage,
  onSelectArtifact,
  phase,
  scrollMemoryKey = null,
  style,
}: MessageListProps) {
  const { t } = useTranslation("workspace");
  const { labels: connectionLabels, retryLabel } = useConnectionStatusLabels();
  const artifactMap = new Map(artifacts.map((artifact) => [artifact.id, artifact]));
  const lastMessage = messages[messages.length - 1];
  const streamingMessageId =
    isResponding && lastMessage?.role === "assistant" ? lastMessage.id : null;
  const logLabel =
    messages.length > 0 ? "Workspace conversation timeline" : "Empty workspace conversation timeline";
  // P1-8：只有非在线态才在流尾提示，避免在线时增加噪声。
  const showConnectionNotice =
    connectionStatus === "connecting" ||
    connectionStatus === "reconnecting" ||
    connectionStatus === "offline";
  const [editingMessageId, setEditingMessageId] = useState<string | null>(null);
  const [inlineEditDraft, setInlineEditDraft] = useState("");
  const selectedMessageRef = useRef<HTMLElement | null>(null);
  const scrollContainerRef = useRef<HTMLDivElement | null>(null);
  const contentRef = useRef<HTMLDivElement | null>(null);
  // P1-3：滚动所有权收口到 useConversationScroll（贴底跟随 / 阅读保顶 /
  // 跨挂载语义锚点），组件只消费 reading-line 命中的 active 消息。
  const { activeMessageId } = useConversationScroll({
    containerRef: scrollContainerRef,
    contentRef,
    revision: messages,
    suspended: backtrackNavigationActive,
    memoryKey: scrollMemoryKey,
  });

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

  // P1-3：宿主禁用原生 scroll anchoring —— 它是另一套滚动所有权，会和语义
  // 锚点抢位置；贴底/保顶由 useConversationScroll 统一决定。
  return (
    <div
      ref={scrollContainerRef}
      className={cn("flex-1 overflow-y-auto px-3 py-4 sm:px-4", className)}
      style={{ overflowAnchor: "none", ...style }}
    >
      <div
        ref={contentRef}
        aria-atomic="false"
        aria-busy={isResponding ? "true" : undefined}
        aria-label={logLabel}
        aria-live="polite"
        aria-relevant="additions text"
        className={cn(
          // 批次 A1/F2：列宽走宽度轴（W = clamp(680px, 列宽×64%, 920px)），不再写死 52rem。
          "mx-auto flex w-full max-w-[var(--app-chat-content-width)] flex-col gap-4",
          contentClassName,
        )}
        role="log"
      >
        {messages.length === 0 ? (
          <div className="flex flex-col items-center gap-2 py-8 text-center">
            <ScrollTextIcon aria-hidden="true" className="text-accent-teal" size={18} />
            <div className="text-sm font-semibold text-foreground">
              {t("panels.messages.messageList.emptyTitle")}
            </div>
            <p className="max-w-[32rem] text-sm leading-6 text-muted-foreground">
              {t("panels.messages.messageList.emptyHint")}
            </p>
          </div>
        ) : null}

        {backtrackNavigationActive ? (
          <NoticeRow tone="warn">
            {t("panels.messages.messageList.backtrackNavHint")}
          </NoticeRow>
        ) : null}

        {backtrackError ? (
          <NoticeRow tone="error">{backtrackError}</NoticeRow>
        ) : null}

        {backtrackNotice ? (
          <NoticeRow>{backtrackNotice}</NoticeRow>
        ) : null}

        {messages.map((message, messageIndex) => {
          const relatedEvidence = (message.relatedArtifactIds ?? [])
            .map((artifactId) => artifactMap.get(artifactId))
            .filter((artifact): artifact is Artifact => artifact !== undefined)
            .filter((artifact) => isArtifactEvidence(artifact));
          const isUser = message.role === "user";
          // E1：消息分类以 `lib/chat-view` 的 flow 判据为准（单一事实源），
          // 渲染层不重复维护 label/author 的组合条件（§8.4 规则 2/3）。
          const isHistoryContextMessage =
            isSystemPromptMessage(message) || isContextMessage(message);
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
              data-active-turn={activeMessageId === message.id ? "true" : undefined}
              data-backtrack-selected={isNavigationSelected ? "true" : undefined}
              data-message-id={message.id}
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
              ) : isToolReceiptMessage(message) ? (
                <HistoryToolMessageRow
                  labelId={labelId}
                  message={message}
                  metaId={metaId}
                  onPreviewFilePath={onPreviewFilePath}
                  onSelectArtifact={onSelectArtifact}
                  relatedEvidence={relatedEvidence}
                  statusId={statusId}
                  streamingMessageId={streamingMessageId}
                />
              ) : isHistoryContextMessage ? (
                <HistoryContextMessageCard
                  labelId={labelId}
                  message={message}
                  metaId={metaId}
                  onPreviewFilePath={onPreviewFilePath}
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
                  onPreviewFilePath={onPreviewFilePath}
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

        {showConnectionNotice && connectionStatus ? (
          <div className="pt-1">
            <ConnectionStatusBadge
              onRetry={onRetryConnection}
              retryLabel={retryLabel}
              status={connectionStatus}
              labels={connectionLabels}
            />
          </div>
        ) : null}
      </div>
    </div>
  );
}
