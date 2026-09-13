// 由 components/workspace/message-list.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { HistoryIcon, LoaderCircleIcon, PencilLineIcon, User2Icon } from "lucide-react";

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
  return (
                <div
                  className={cn(
                    "relative w-full max-w-[42rem] overflow-hidden rounded-[1rem] border px-4 py-3.5 shadow-[0_16px_40px_rgba(0,0,0,0.12)] transition",
                    isNavigationSelected
                      ? "border-[#f0c77b]/55 bg-[linear-gradient(180deg,rgba(240,199,123,0.16),rgba(240,199,123,0.06))] ring-2 ring-[#f0c77b]/25"
                      : "border-[#f0c77b]/16 bg-[linear-gradient(180deg,rgba(240,199,123,0.08),rgba(240,199,123,0.03))]",
                    backtrackNavigationActive ? "cursor-pointer hover:border-[#f0c77b]/40" : null,
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
                      <div className="inline-flex size-7 items-center justify-center rounded-[0.7rem] border border-[#f0c77b]/20 bg-[#f0c77b]/10 text-[#f0c77b]">
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
                            aria-label="Edit this user turn before backtrack"
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
                            Edit
                          </Button>
                          <Button
                            aria-label="Backtrack to this user turn"
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
                            Backtrack
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
                          aria-label="Edit user turn prompt"
                          className="min-h-[7rem] w-full resize-y rounded-[0.85rem] border border-[#f0c77b]/25 bg-black/10 px-3 py-2.5 text-sm leading-6 text-foreground outline-none transition placeholder:text-muted-foreground focus:border-[#f0c77b]/45 focus:bg-black/15"
                          onChange={(event) => setInlineEditDraft(event.target.value)}
                          onClick={(event) => event.stopPropagation()}
                          placeholder="Edit this user prompt, then continue to backtrack…"
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
                            Cancel
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
                            Continue to backtrack
                          </Button>
                        </div>
                        <p className="text-xs leading-5 text-muted-foreground">
                          Inline edit seeds the backtrack dialog. Confirm there to
                          truncate later turns and prefill the composer.
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
