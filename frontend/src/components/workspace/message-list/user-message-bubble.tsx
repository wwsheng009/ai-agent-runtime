// 批次 A3/C1（§5.2 / §8.5 user|steering）：右对齐气泡 + hover 动作区。
// - 一层底色、无边框、无阴影、无渐变；圆角 22px；
// - 限宽按列宽比例（525/748 ≈ 0.702，与 82% 取小），随宽度轴同步；
// - 元数据不上屏：作者/label 只留 sr-only（保留 aria-labelledby 契约）；
// - 动作区（复制 / 编辑 / 回溯）28×28 圆图标按钮，默认隐藏，hover / 键盘焦点显现。

import { CopyIcon, HistoryIcon, LoaderCircleIcon, PencilLineIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { type ChatMessage, type MessageSegment } from "@/data/mock";
import { isSteeringMessage } from "@/lib/chat-view";
import { hasVisibleText } from "@/lib/chat-view/visible-text";
import { cn } from "@/lib/utils";

import { renderMessageSegment } from "./segment-rendering";
import type { MessageListProps } from "./types";

type UserMessageBubbleProps = {
  actionsDisabled: boolean;
  backtrackNavigationActive: boolean;
  backtrackPending: boolean;
  inlineEditDraft: string;
  isEditing: boolean;
  isNavigationSelected: boolean;
  labelId: string;
  message: ChatMessage;
  metaId: string;
  onBacktrackToMessage?: MessageListProps["onBacktrackToMessage"];
  onSelectArtifact: (artifactId: string) => void;
  onSelectBacktrackNavigationMessage?: (messageId: string) => void;
  setEditingMessageId: (messageId: string | null) => void;
  setInlineEditDraft: (draft: string) => void;
  showBacktrack: boolean;
  statusId: string;
};

function extractUserBubbleText(message: ChatMessage): string {
  return message.segments
    .filter(
      (segment): segment is Extract<MessageSegment, { type: "text" }> =>
        segment.type === "text",
    )
    .map((segment) => segment.content)
    .join("\n")
    .replace(/\r\n/g, "\n");
}

/** 动作按钮统一形制：28×28 圆图标，默认三级文本，hover 升二级。 */
const ACTION_BUTTON_CLASS =
  "inline-flex size-7 shrink-0 items-center justify-center rounded-full text-muted-foreground transition hover:bg-surface-soft hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none disabled:opacity-40";

export function UserMessageBubble({
  actionsDisabled,
  backtrackNavigationActive,
  backtrackPending,
  inlineEditDraft,
  isEditing,
  isNavigationSelected,
  labelId,
  message,
  metaId,
  onBacktrackToMessage,
  onSelectArtifact,
  onSelectBacktrackNavigationMessage,
  setEditingMessageId,
  setInlineEditDraft,
  showBacktrack,
  statusId,
}: UserMessageBubbleProps) {
  const { t } = useTranslation("workspace");
  const [copied, setCopied] = useState(false);
  const text = extractUserBubbleText(message);
  const flowKind = isSteeringMessage(message) ? "steering" : "user";

  const copy = async () => {
    if (!hasVisibleText(text)) {
      return;
    }
    try {
      await navigator.clipboard?.writeText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1200);
    } catch {
      // 剪贴板不可用（权限 / 非安全上下文）时静默：动作区不阻塞阅读。
    }
  };

  return (
    <div
      className="group flex w-full flex-col items-end gap-1.5"
      data-chat-anchor-key={message.id}
      data-chat-flow-key={`${flowKind}:${message.id}`}
      data-chat-flow-kind={flowKind}
    >
      <span className="sr-only" id={labelId}>
        {message.author}
      </span>
      <span className="sr-only" id={metaId}>
        {message.label}
      </span>

      <div
        className={cn(
          "app-chat-bubble max-w-[min(calc(var(--app-chat-content-width)*0.702),82%)] rounded-[22px] px-4 py-2.5 transition",
          isNavigationSelected
            ? "bg-accent-gold/16 ring-2 ring-accent-gold/25"
            : "bg-surface-strong",
          backtrackNavigationActive
            ? "cursor-pointer hover:ring-2 hover:ring-accent-gold/25"
            : null,
        )}
        onClick={() => {
          if (
            backtrackNavigationActive &&
            typeof onSelectBacktrackNavigationMessage === "function"
          ) {
            onSelectBacktrackNavigationMessage(message.id);
          }
        }}
        onDoubleClick={(event) => {
          if (!showBacktrack) {
            return;
          }
          event.stopPropagation();
          // Double-click confirms the anchor both in normal and Esc-nav mode.
          onBacktrackToMessage?.(message.id, "conversation");
        }}
      >
        <div className="min-w-0" id={statusId}>
          {isEditing ? (
            <div className="space-y-2">
              <textarea
                aria-label={t("panels.messages.userBubble.editPromptAriaLabel")}
                className="min-h-[7rem] w-full resize-y rounded-card-lg border border-border bg-surface-softer px-3 py-2.5 app-chat-bubble text-foreground outline-none transition placeholder:text-muted-foreground focus:border-accent-gold/45 focus-visible:ring-2 focus-visible:ring-ring"
                onChange={(event) => setInlineEditDraft(event.target.value)}
                onClick={(event) => event.stopPropagation()}
                placeholder={t("panels.messages.userBubble.editPromptPlaceholder")}
                value={inlineEditDraft}
              />
              <div className="flex flex-wrap items-center justify-end gap-2">
                <Button
                  disabled={actionsDisabled}
                  onClick={(event) => {
                    event.stopPropagation();
                    setEditingMessageId(null);
                    setInlineEditDraft("");
                  }}
                  size="sm"
                  type="button"
                  variant="ghost"
                >
                  {t("panels.messages.userBubble.cancel")}
                </Button>
                <Button
                  disabled={actionsDisabled}
                  onClick={(event) => {
                    event.stopPropagation();
                    onBacktrackToMessage?.(message.id, "conversation", {
                      editPrompt: inlineEditDraft,
                    });
                    setEditingMessageId(null);
                    setInlineEditDraft("");
                  }}
                  size="sm"
                  type="button"
                >
                  {t("panels.messages.userBubble.continue")}
                </Button>
              </div>
              <p className="app-text-11 leading-5 text-muted-foreground">
                {t("panels.messages.userBubble.editHint")}
              </p>
            </div>
          ) : (
            // §12.1.4：空文本段整段不渲染（含外层包裹 div）——空 flex item 会吃掉
            // 气泡内的 gap，撑出一条空行。
            message.segments
              .filter(
                (segment) =>
                  segment.type !== "text" || hasVisibleText(segment.content),
              )
              .map((segment, index) => (
                <div key={`${message.id}-${segment.type}-${index}`}>
                  {renderMessageSegment(segment, {
                    interrupted: message.interrupted === true,
                    streaming: false,
                    onSelectArtifact,
                  })}
                </div>
              ))
          )}
        </div>
      </div>

      {/* §12.1.4：动作区同样按内容产出——无复制文本且无回溯入口时整行不渲染，
          避免每条用户消息下面吊着一条 28px 空行。 */}
      {hasVisibleText(text) || showBacktrack || isNavigationSelected ? (
        <div className="flex h-7 items-center gap-1 pr-1 app-hover-reveal">
          {/* 元数据不上屏（§5.2）：选中态只保留无障碍标记 + aria-current。 */}
          {isNavigationSelected ? (
            <span className="sr-only">
              {t("panels.messages.userBubble.selected")}
            </span>
          ) : null}
          <span aria-live="polite" className="sr-only">
            {copied ? t("panels.messages.turnTail.copied") : ""}
          </span>
          {/* 复制图标按内容显示（§12.1.4）：纯附件 / 空正文的用户消息没有可复制文本，
              不再渲染常驻的禁用图标。 */}
          {hasVisibleText(text) ? (
            <button
              aria-label={t("panels.messages.userBubble.copyAriaLabel")}
              className={ACTION_BUTTON_CLASS}
              onClick={(event) => {
                event.stopPropagation();
                void copy();
              }}
              type="button"
            >
              <CopyIcon aria-hidden="true" size={15} />
            </button>
          ) : null}
          {showBacktrack ? (
            <>
              <button
                aria-label={t("panels.messages.userBubble.editAriaLabel")}
                className={ACTION_BUTTON_CLASS}
                disabled={actionsDisabled}
                onClick={(event) => {
                  event.stopPropagation();
                  setEditingMessageId(message.id);
                  setInlineEditDraft(extractUserBubbleText(message));
                }}
                type="button"
              >
                <PencilLineIcon aria-hidden="true" size={15} />
              </button>
              <button
                aria-label={t("panels.messages.userBubble.backtrackAriaLabel")}
                className={ACTION_BUTTON_CLASS}
                disabled={actionsDisabled}
                onClick={(event) => {
                  event.stopPropagation();
                  onBacktrackToMessage?.(message.id, "conversation");
                }}
                type="button"
              >
                {backtrackPending ? (
                  <LoaderCircleIcon aria-hidden="true" className="animate-spin" size={15} />
                ) : (
                  <HistoryIcon aria-hidden="true" size={15} />
                )}
              </button>
            </>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
