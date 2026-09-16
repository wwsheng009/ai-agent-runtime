// 由 components/workspace/message-list.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { ScrollTextIcon } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { ConnectionStatusBadge } from "@/components/ui/connection-status-badge";
import { useConnectionStatusLabels } from "@/hooks/workspace/use-connection-status-labels";
import { useConversationScroll } from "@/hooks/workspace/use-conversation-scroll";
import { hasVisibleMessageContent, isSystemPromptMessage } from "@/lib/chat-view";
import { resolveBranchAnchors } from "@/lib/chat-view/branch-availability";
import { isArtifactEvidence } from "@/lib/workspace-artifacts";
import { cn } from "@/lib/utils";
import { type ChatStreamPhase } from "@/types/runtime";

import { MessageRow } from "./message-list/message-row";
import { NoticeRow } from "./message-list/notice-row";
import { LoadEarlierRow } from "./message-list/load-earlier-row";
import type { MessageListProps } from "./message-list/types";
import { useMessageListEarlierEntry } from "./message-list/use-message-list-earlier-entry";

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
  branchError = null,
  branchPendingMessageId = null,
  canBacktrack = false,
  className,
  connectionStatus = null,
  contentClassName,
  earlierLoader,
  hasPendingApproval = false,
  isResponding,
  messages,
  onBacktrackToMessage,
  onBranchFromMessage,
  onPreviewFilePath,
  onRetryConnection,
  onSelectBacktrackNavigationMessage,
  onSelectArtifact,
  phase,
  scrollMemoryKey = null,
  streamStalled = false,
  style,
}: MessageListProps) {
  const { t } = useTranslation("workspace");
  const { labels: connectionLabels, retryLabel } = useConnectionStatusLabels();
  // 产物索引：`artifacts` 身份在文本流式期间保持稳定，缓存后行组件可以安全 memo
  // （否则每帧新建的 Map 会让整列历史行全部重渲染）。
  const artifactMap = useMemo(
    () => new Map(artifacts.map((artifact) => [artifact.id, artifact])),
    [artifacts],
  );
  // §12.1.4：无可见内容的助手消息不产出 <article>。外层是 `flex flex-col gap-4`，
  // 空壳 article 自身高度为 0 却仍是一个 flex item，会在相邻消息间撑出一条空白行
  // （典型来源：回合开始到首块到达之间的流式空壳、纯工具回合）。
  const visibleMessages = messages.filter((message) => {
    if (message.role !== "assistant") {
      return true;
    }
    const relatedCount = (message.relatedArtifactIds ?? []).filter((artifactId) => {
      const artifact = artifactMap.get(artifactId);
      return artifact !== undefined && isArtifactEvidence(artifact);
    }).length;
    return hasVisibleMessageContent(message, relatedCount);
  });
  // 批次 24：对话区只剩「提示基础设施」行（system prompt）时，时间线同样是空的。
  // CLI / 子代理批次驱动的会话不落库对话消息（后端 `prompt_rows=0`），此时
  // `messages.length` 仍为 1，沿用旧口径会让页面留下一条折叠的 System prompt 行
  // 却既没有正文、也没有任何解释 —— 用户看到的就是「整页什么都没有」。
  // 判空必须按**对话行**（非提示基础设施行）口径。
  const hasConversationRows = visibleMessages.some(
    (message) => !isSystemPromptMessage(message),
  );
  const lastMessage = messages[messages.length - 1];
  const streamingMessageId =
    isResponding && lastMessage?.role === "assistant" ? lastMessage.id : null;
  // 批次 2（§5.4）：分支锚点在**整段历史**上求一次（O(n)）：每个已完成轮次的末条消息
  // 各自成锚点（对齐后端 `ListUserTurns` 的轮边界）；只有锚点会拿到 `onBranch`，
  // 非锚点（含只有推理的消息）不渲染按钮。
  const branchAnchors = useMemo(
    () => resolveBranchAnchors(messages, { isResponding, hasPendingApproval }),
    [messages, isResponding, hasPendingApproval],
  );
  const logLabel = hasConversationRows
    ? "Workspace conversation timeline"
    : "Empty workspace conversation timeline";
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
  // 行内动作（回溯 / 分支）在这些情况下整体禁用。只依赖行间共享状态，所以在
  // 列表层算一次：行组件拿到的是稳定布尔值，memo 不会因为重算被打破。
  const actionsDisabled =
    Boolean(backtrackPendingMessageId) ||
    Boolean(branchPendingMessageId) ||
    isResponding ||
    backtrackNavigationActive;
  // P1-3：滚动所有权收口到 useConversationScroll（贴底跟随 / 阅读保顶 /
  // 跨挂载语义锚点），组件只消费 reading-line 命中的 active 消息。
  const { activeMessageId } = useConversationScroll({
    containerRef: scrollContainerRef,
    contentRef,
    revision: messages,
    suspended: backtrackNavigationActive,
    memoryKey: scrollMemoryKey,
  });
  // 会话历史尾部优先分页：滚到消息流顶端自动续页；入口只在**视口贴近顶部**时露面
  // （贴底读最新消息时不常驻遮挡正文，见 hook 头注释）。两者走同一条幂等入口，
  // 因此内容不足一屏（没有滚动事件）时入口依旧是兜底，不会出现「翻不动」死角。
  const showEarlierEntry = useMessageListEarlierEntry({
    containerRef: scrollContainerRef,
    hasMore: earlierLoader?.hasMore ?? false,
    loading: earlierLoader?.loading ?? false,
    onLoadEarlier: earlierLoader?.onLoadEarlier,
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
          // app-chat-scope：转录列整体纳入聊天字号轴作用域，列内一切 `.app-text-N`
          // （含过程行统计、更早记录按钮、编辑提示等散点）都随「聊天字号」滑杆位移。
          "app-chat-scope mx-auto flex w-full max-w-[var(--app-chat-content-width)] flex-col gap-4",
          contentClassName,
        )}
        role="log"
      >
        <LoadEarlierRow
          loading={earlierLoader?.loading ?? false}
          onLoad={earlierLoader?.onLoadEarlier}
          visible={showEarlierEntry}
        />

        {!hasConversationRows ? (
          <div className="flex flex-col items-center gap-2 py-8 text-center">
            <ScrollTextIcon aria-hidden="true" className="text-accent-teal" size={18} />
            <div className="text-sm font-semibold text-foreground">
              {t("panels.messages.messageList.emptyTitle")}
            </div>
            <p className="max-w-[32rem] text-sm leading-6 text-muted-foreground">
              {t("panels.messages.messageList.emptyHint")}
            </p>
            {messages.length > 0 ? (
              <p className="max-w-[32rem] text-sm leading-6 text-muted-foreground">
                {t("panels.messages.messageList.emptyInfraOnlyHint")}
              </p>
            ) : null}
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

        {branchError ? <NoticeRow tone="error">{branchError}</NoticeRow> : null}

        {backtrackNotice ? (
          <NoticeRow>{backtrackNotice}</NoticeRow>
        ) : null}

        {visibleMessages.map((message, messageIndex) => (
          <MessageRow
            actionsDisabled={actionsDisabled}
            artifactMap={artifactMap}
            backtrackNavigationActive={backtrackNavigationActive}
            backtrackPending={backtrackPendingMessageId === message.id}
            branchPending={branchPendingMessageId === message.id}
            canBacktrack={canBacktrack}
            // 批次 2（§5.4）：只有可分支锚点下发入口（其余行不渲染按钮）。
            canBranch={branchAnchors.has(message.id)}
            editDraft={editingMessageId === message.id ? inlineEditDraft : ""}
            index={messageIndex}
            isActive={activeMessageId === message.id}
            isEditing={message.role === "user" && editingMessageId === message.id}
            isNavigationSelected={
              message.role === "user" &&
              backtrackNavigationActive &&
              backtrackSelectedMessageId === message.id
            }
            key={message.id}
            message={message}
            onBacktrackToMessage={onBacktrackToMessage}
            onBranchFromMessage={onBranchFromMessage}
            onPreviewFilePath={onPreviewFilePath}
            onSelectArtifact={onSelectArtifact}
            onSelectBacktrackNavigationMessage={onSelectBacktrackNavigationMessage}
            rowRef={selectedMessageRef}
            setEditingMessageId={setEditingMessageId}
            setInlineEditDraft={setInlineEditDraft}
            streaming={streamingMessageId === message.id}
            totalCount={visibleMessages.length}
          />
        ))}

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

        {/* 本页流已死但回合未必结束：不静默收场，明确告知并给恢复入口。
            文案走 i18n（`panels.messages.messageList.streamStalledNotice`），
            与同区块的连接状态徽标保持同一套语义。 */}
        {streamStalled && !isResponding ? (
          <div
            aria-atomic="true"
            aria-live="polite"
            className="inline-flex flex-wrap items-center gap-2 app-text-10 uppercase tracking-[0.14em] text-muted-foreground"
            role="status"
          >
            <span className="size-2 rounded-full bg-destructive" />
            {t("panels.messages.messageList.streamStalledNotice")}
            {onRetryConnection ? (
              <button
                className="underline underline-offset-2 transition-colors hover:text-foreground"
                onClick={onRetryConnection}
                type="button"
              >
                {retryLabel}
              </button>
            ) : null}
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
