// 流式性能：消息行的记忆化边界。
//
// `message-list.tsx` 在流式期间每个 chunk（现在还有打字机逐帧揭示）都会重渲染，
// 行的 `article` + 内层卡片如果跟着重建，React 就要对**整段历史**做一次协调，
// 大会话下这就是「页面卡住」的第二个来源。
//
// 这里把「一行」抽成 `memo` 组件，并让 props 全部是可浅比较的稳定值：
// - `message` 对象：`updateLatestAssistantMessage` / `updateThreadMessage` 只替换
//   命中的那一条，其余消息对象身份保持不变（库层保证）；
// - 回调：父层直接透传宿主回调，行内需要用 id 绑定的那个（`onBranch`）在行内
//   `useCallback` 里建一次，父层不再每帧造闭包；
// - 派生量（labelId / relatedEvidence / 行类型判定）在行内计算，父层不传新对象。
// 因此流式期间只有「正在增长的那一行」以及 active 标记切换涉及的两行重渲染。

import { memo, useCallback, useMemo } from "react";
import type { RefObject } from "react";

import { type Artifact, type ChatMessage } from "@/data/mock";
import {
  isContextMessage,
  isSystemPromptMessage,
  isToolReceiptMessage,
} from "@/lib/chat-view";
import { cn } from "@/lib/utils";
import { isArtifactEvidence } from "@/lib/workspace-artifacts";

import { AssistantMessageCard } from "./assistant-message-card";
import { HistoryContextMessageCard } from "./history-context-message-card";
import { HistoryToolMessageRow } from "./history-tool-message-row";
import type { MessageListProps } from "./types";
import { UserMessageBubble } from "./user-message-bubble";

export type MessageRowProps = {
  /** 行内动作（回溯 / 分支 / 重试）是否整体禁用（在途操作或流式响应中）。 */
  actionsDisabled: boolean;
  artifactMap: ReadonlyMap<string, Artifact>;
  backtrackNavigationActive: boolean;
  backtrackPending: boolean;
  /** 本行所属回合的分支请求在途（`branchPendingMessageId` 命中本行）。 */
  branchPending: boolean;
  canBacktrack: boolean;
  /** 本行是分支锚点（宿主提供 `onBranchFromMessage` 时才可能为真）。 */
  canBranch: boolean;
  /** 仅编辑中的行会拿到草稿（未编辑的行传空串，避免逐字输入打穿整列 memo）。 */
  editDraft: string;
  index: number;
  isActive: boolean;
  isEditing: boolean;
  isNavigationSelected: boolean;
  message: ChatMessage;
  onBacktrackToMessage?: MessageListProps["onBacktrackToMessage"];
  onBranchFromMessage?: MessageListProps["onBranchFromMessage"];
  onPreviewFilePath?: MessageListProps["onPreviewFilePath"];
  onSelectArtifact: (artifactId: string) => void;
  onSelectBacktrackNavigationMessage?: MessageListProps["onSelectBacktrackNavigationMessage"];
  rowRef: RefObject<HTMLElement | null>;
  setEditingMessageId: (messageId: string | null) => void;
  setInlineEditDraft: (draft: string) => void;
  /** 本行是当前流式的助手消息（`aria-busy`）。 */
  streaming: boolean;
  totalCount: number;
};

function MessageRowImpl({
  actionsDisabled,
  artifactMap,
  backtrackNavigationActive,
  backtrackPending,
  branchPending,
  canBacktrack,
  canBranch,
  editDraft,
  index,
  isActive,
  isEditing,
  isNavigationSelected,
  message,
  onBacktrackToMessage,
  onBranchFromMessage,
  onPreviewFilePath,
  onSelectArtifact,
  onSelectBacktrackNavigationMessage,
  rowRef,
  setEditingMessageId,
  setInlineEditDraft,
  streaming,
  totalCount,
}: MessageRowProps) {
  const isUser = message.role === "user";
  // E1：消息分类以 `lib/chat-view` 的 flow 判据为准（单一事实源），渲染层不重复
  // 维护 label/author 的组合条件（§8.4 规则 2/3）。
  const isHistoryContextMessage =
    isSystemPromptMessage(message) || isContextMessage(message);
  const labelId = `${message.id}-label`;
  const metaId = `${message.id}-meta`;
  const statusId = `${message.id}-status`;
  const describedBy = [metaId, statusId].join(" ");

  const relatedEvidence = useMemo(() => {
    const ids = message.relatedArtifactIds;
    if (!ids || ids.length === 0) {
      return [];
    }
    return ids
      .map((artifactId) => artifactMap.get(artifactId))
      .filter((artifact): artifact is Artifact => artifact !== undefined)
      .filter((artifact) => isArtifactEvidence(artifact));
  }, [artifactMap, message.relatedArtifactIds]);

  const branchEnabled = canBranch && typeof onBranchFromMessage === "function";
  const onBranch = useCallback(() => {
    onBranchFromMessage?.(message.id);
  }, [message.id, onBranchFromMessage]);

  const showBacktrack =
    isUser && canBacktrack && typeof onBacktrackToMessage === "function";

  return (
    <article
      aria-busy={streaming ? "true" : undefined}
      aria-current={isNavigationSelected ? "true" : undefined}
      aria-describedby={describedBy}
      aria-labelledby={labelId}
      aria-setsize={totalCount}
      aria-posinset={index + 1}
      data-active-turn={isActive ? "true" : undefined}
      data-backtrack-selected={isNavigationSelected ? "true" : undefined}
      data-message-id={message.id}
      ref={isNavigationSelected ? rowRef : undefined}
      className={cn("flex w-full", isUser ? "justify-end" : "justify-start")}
    >
      {isUser ? (
        <UserMessageBubble
          actionsDisabled={actionsDisabled}
          backtrackNavigationActive={backtrackNavigationActive}
          backtrackPending={backtrackPending}
          inlineEditDraft={editDraft}
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
          streamingMessageId={streaming ? message.id : null}
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
          streamingMessageId={streaming ? message.id : null}
        />
      ) : (
        <AssistantMessageCard
          branchPending={branchPending}
          labelId={labelId}
          message={message}
          metaId={metaId}
          onBranch={branchEnabled ? onBranch : undefined}
          onPreviewFilePath={onPreviewFilePath}
          onSelectArtifact={onSelectArtifact}
          relatedEvidence={relatedEvidence}
          statusId={statusId}
          streamingMessageId={streaming ? message.id : null}
        />
      )}
    </article>
  );
}

export const MessageRow = memo(MessageRowImpl);
