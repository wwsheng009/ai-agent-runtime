// 由 components/workspace/message-list.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { HistoryIcon, LoaderCircleIcon, PencilLineIcon, User2Icon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { type ChatMessage, type MessageSegment } from "@/data/mock";
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
  return (
                <div
                  className={cn(
                    "relative w-full max-w-[42rem] overflow-hidden rounded-[1rem] border px-4 py-3.5 shadow-[0_16px_40px_rgba(0,0,0,0.12)] transition",
                    isNavigationSelected
                      ? "border-accent-gold/55 bg-[linear-gradient(180deg,rgba(240,199,123,0.16),rgba(240,199,123,0.06))] ring-2 ring-accent-gold/25"
                      : "border-accent-gold/16 bg-[linear-gradient(180deg,rgba(240,199,123,0.08),rgba(240,199,123,0.03))]",
                    backtrackNavigationActive ? "cursor-pointer hover:border-accent-gold/40" : null,
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
                  <div className="flex items-start justify-between gap-3">
                    <div className="flex items-center gap-2.5">
                      <div className="inline-flex size-7 items-center justify-center rounded-field border border-accent-gold/20 bg-accent-gold/10 text-accent-gold">
                        <User2Icon size={14} />
                      </div>
                      <div>
                        <div
                          className="app-text-13 font-semibold text-foreground"
                          id={labelId}
                        >
                          {message.author}
                        </div>
                        <div
                          className="mt-0.5 app-text-10 uppercase tracking-[0.14em] text-muted-foreground"
                          id={metaId}
                        >
                          {message.label}
                          {isNavigationSelected ? " · selected" : ""}
                        </div>
                      </div>
                    </div>
                    <div className="flex flex-wrap items-center justify-end gap-2">
                      {showBacktrack ? (
                        <>
                          <Button
                            aria-label={t("panels.messages.userBubble.editAriaLabel")}
                            disabled={actionsDisabled}
                            onClick={(event) => {
                              event.stopPropagation();
                              setEditingMessageId(message.id);
                              setInlineEditDraft(extractUserBubbleText(message));
                            }}
                            size="sm"
                            type="button"
                            variant="ghost"
                            className="h-7 gap-1.5 px-2 text-[11px] uppercase tracking-[0.12em]"
                          >
                            <PencilLineIcon size={12} />
                            {t("panels.messages.userBubble.edit")}
                          </Button>
                          <Button
                            aria-label={t("panels.messages.userBubble.backtrackAriaLabel")}
                            disabled={actionsDisabled}
                            onClick={(event) => {
                              event.stopPropagation();
                              onBacktrackToMessage?.(message.id, "conversation");
                            }}
                            size="sm"
                            type="button"
                            variant="ghost"
                            className="h-7 gap-1.5 px-2 text-[11px] uppercase tracking-[0.12em]"
                          >
                            {backtrackPending ? (
                              <LoaderCircleIcon size={12} className="animate-spin" />
                            ) : (
                              <HistoryIcon size={12} />
                            )}
                            {t("panels.messages.userBubble.backtrack")}
                          </Button>
                        </>
                      ) : null}
                      <Badge className="border-transparent bg-surface-soft text-foreground">
                        {message.role}
                      </Badge>
                    </div>
                  </div>

                  <div className="mt-3 space-y-3" id={statusId}>
                    {isEditing ? (
                      <div className="space-y-3">
                        <textarea
                          aria-label={t("panels.messages.userBubble.editPromptAriaLabel")}
                          className="min-h-[7rem] w-full resize-y rounded-card-lg border border-accent-gold/25 bg-black/10 px-3 py-2.5 text-sm leading-6 text-foreground outline-none transition placeholder:text-muted-foreground focus:border-accent-gold/45 focus:bg-black/15"
                          onChange={(event) => setInlineEditDraft(event.target.value)}
                          onClick={(event) => event.stopPropagation()}
                          placeholder={t(
                            "panels.messages.userBubble.editPromptPlaceholder",
                          )}
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
                        <p className="text-xs leading-5 text-muted-foreground">
                          {t("panels.messages.userBubble.editHint")}
                        </p>
                      </div>
                    ) : (
                      message.segments.map((segment, index) => (
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
  );
}
