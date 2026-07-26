import {
  BotIcon,
  HistoryIcon,
  LoaderCircleIcon,
  PencilLineIcon,
  ScrollTextIcon,
  User2Icon,
} from "lucide-react";
import {
  lazy,
  Suspense,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
} from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { MessageMarkdown } from "@/components/workspace/message-markdown";
import { type Artifact, type ChatMessage, type MessageSegment } from "@/data/mock";
import { isArtifactEvidence } from "@/lib/workspace-artifacts";
import { cn } from "@/lib/utils";

const MessageRichSegment = lazy(() =>
  import("@/components/workspace/message-rich-content").then((module) => ({
    default: module.MessageRichSegment,
  })),
);
const MessageRelatedArtifacts = lazy(() =>
  import("@/components/workspace/message-rich-content").then((module) => ({
    default: module.MessageRelatedArtifacts,
  })),
);

export type MessageBacktrackOptions = {
  editPrompt?: string;
};

type MessageListProps = {
  artifacts: Artifact[];
  backtrackError?: string | null;
  backtrackNotice?: string | null;
  backtrackPendingMessageId?: string | null;
  backtrackNavigationActive?: boolean;
  backtrackSelectedMessageId?: string | null;
  canBacktrack?: boolean;
  className?: string;
  contentClassName?: string;
  isResponding: boolean;
  messages: ChatMessage[];
  onBacktrackToMessage?: (
    messageId: string,
    mode?: "conversation" | "both",
    options?: MessageBacktrackOptions,
  ) => void;
  onSelectBacktrackNavigationMessage?: (messageId: string) => void;
  onSelectArtifact: (artifactId: string) => void;
  style?: CSSProperties;
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

  useEffect(() => {
    if (!editingMessageId) {
      return;
    }
    if (!messages.some((message) => message.id === editingMessageId)) {
      setEditingMessageId(null);
      setInlineEditDraft("");
    }
  }, [editingMessageId, messages]);

  useEffect(() => {
    if ((isResponding || backtrackNavigationActive) && editingMessageId) {
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
    <div className={cn("flex-1 overflow-y-auto px-3 py-4 sm:px-4", className)} style={style}>
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
          <div className="rounded-[0.95rem] border border-dashed border-[var(--border)] bg-[var(--surface-softer)] px-4 py-8 text-center">
            <div className="mx-auto inline-flex size-10 items-center justify-center rounded-[0.8rem] border border-[#8fd0c6]/18 bg-[#8fd0c6]/10 text-[#8fd0c6]">
              <ScrollTextIcon size={18} />
            </div>
            <div className="mt-3 text-sm font-semibold text-[var(--foreground)]">
              The thread timeline is empty
            </div>
            <p className="mt-2 text-sm leading-6 text-[var(--muted-foreground)]">
              Start a turn to populate the workspace timeline. Runtime evidence,
              related items, and streamed output will attach back to the
              messages that produced them.
            </p>
          </div>
        ) : null}

        {backtrackNavigationActive ? (
          <div className="rounded-[0.85rem] border border-[#f0c77b]/20 bg-[#f0c77b]/8 px-3.5 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
            Backtrack navigation active — use ↑/↓ (or j/k) to choose a user turn,
            Enter to open the confirm dialog, Esc to exit.
          </div>
        ) : null}

        {backtrackError ? (
          <div className="rounded-[0.85rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3.5 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
            {backtrackError}
          </div>
        ) : null}

        {backtrackNotice ? (
          <div className="rounded-[0.85rem] border border-[#8fd0c6]/18 bg-[#8fd0c6]/10 px-3.5 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
            {backtrackNotice}
          </div>
        ) : null}

        {messages.map((message, messageIndex) => {
          const relatedEvidence = (message.relatedArtifactIds ?? [])
            .map((artifactId) => artifactMap.get(artifactId))
            .filter((artifact): artifact is Artifact => artifact !== undefined)
            .filter((artifact) => isArtifactEvidence(artifact));
          const isUser = message.role === "user";
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
                          className="app-text-13 font-semibold text-[var(--foreground)]"
                          id={labelId}
                        >
                          {message.author}
                        </div>
                        <div
                          className="mt-0.5 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]"
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
                      <Badge className="border-transparent bg-[var(--surface-soft)] text-[var(--foreground)]">
                        {message.role}
                      </Badge>
                    </div>
                  </div>

                  <div className="mt-3 space-y-3" id={statusId}>
                    {isEditing ? (
                      <div className="space-y-3">
                        <textarea
                          aria-label="Edit user turn prompt"
                          className="min-h-[7rem] w-full resize-y rounded-[0.85rem] border border-[#f0c77b]/25 bg-black/10 px-3 py-2.5 text-sm leading-6 text-[var(--foreground)] outline-none transition placeholder:text-[var(--muted-foreground)] focus:border-[#f0c77b]/45 focus:bg-black/15"
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
                        <p className="text-xs leading-5 text-[var(--muted-foreground)]">
                          Inline edit seeds the backtrack dialog. Confirm there to
                          truncate later turns and prefill the composer.
                        </p>
                      </div>
                    ) : (
                      message.segments.map((segment, index) => (
                        <div key={`${message.id}-${segment.type}-${index}`}>
                          {renderMessageSegment(segment, {
                            streaming: false,
                            onSelectArtifact,
                          })}
                        </div>
                      ))
                    )}
                  </div>
                </div>
              ) : (
                <div className="relative w-full max-w-[48rem]">
                  <div className="overflow-hidden rounded-[1rem] border border-[#8fd0c6]/14 bg-[linear-gradient(180deg,rgba(143,208,198,0.08),rgba(143,208,198,0.02))] px-4 py-3.5 shadow-[0_16px_40px_rgba(0,0,0,0.12)]">
                    <div className="flex items-start gap-3">
                      <div className="mt-0.5 inline-flex size-7 shrink-0 items-center justify-center rounded-[0.7rem] border border-[#8fd0c6]/20 bg-[#8fd0c6]/10 text-[#8fd0c6]">
                        <BotIcon size={14} />
                      </div>
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <div
                            className="app-text-13 font-semibold text-[var(--foreground)]"
                            id={labelId}
                          >
                            {message.author}
                          </div>
                          <div
                            className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]"
                            id={metaId}
                          >
                            {message.label}
                          </div>
                          {message.id === streamingMessageId ? (
                            <Badge className="border-transparent bg-[#8fd0c6]/12 text-[#8fd0c6]">
                              Streaming response in progress
                            </Badge>
                          ) : null}
                        </div>

                        <div className="relative mt-3" id={statusId}>
                          <div className="pointer-events-none absolute left-0 top-4 bottom-4 w-px bg-gradient-to-b from-[#8fd0c6]/0 via-[#8fd0c6]/18 to-[#8fd0c6]/0" />

                          <div className="relative space-y-4">
                            {message.segments.map((segment, index) => (
                              <div key={`${message.id}-${segment.type}-${index}`}>
                                {renderMessageSegment(segment, {
                                  streaming: message.id === streamingMessageId,
                                  onSelectArtifact,
                                })}
                              </div>
                            ))}
                          </div>

                          {renderRelatedArtifactSection(
                            relatedEvidence,
                            onSelectArtifact,
                          )}
                        </div>
                      </div>
                    </div>
                  </div>
                </div>
              )}
            </article>
          );
        })}

        {isResponding ? (
          <div
            aria-atomic="true"
            aria-live="polite"
            className="inline-flex items-center gap-2 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]"
            role="status"
          >
            <span className="size-2 rounded-full animate-pulse bg-[#8fd0c6]" />
            Runtime stream active
          </div>
        ) : null}
      </div>
    </div>
  );
}

function renderMessageSegment(
  segment: MessageSegment,
  options?: {
    streaming?: boolean;
    onSelectArtifact?: (artifactId: string) => void;
  },
) {
  if (segment.type === "text") {
    return (
      <MessageMarkdown
        content={segment.content}
        streaming={options?.streaming}
      />
    );
  }

  return (
    <Suspense fallback={<MessageSegmentFallback segment={segment} />}>
      <MessageRichSegment
        onSelectArtifact={options?.onSelectArtifact}
        segment={segment}
      />
    </Suspense>
  );
}

function renderRelatedArtifactSection(
  relatedEvidence: Artifact[],
  onSelectArtifact: (artifactId: string) => void,
) {
  if (relatedEvidence.length === 0) {
    return null;
  }

  return (
    <Suspense fallback={<RelatedArtifactsFallback count={relatedEvidence.length} />}>
      <MessageRelatedArtifacts
        onSelectArtifact={onSelectArtifact}
        relatedArtifacts={relatedEvidence}
      />
    </Suspense>
  );
}

function MessageSegmentFallback({
  segment,
}: {
  segment: Exclude<MessageSegment, { type: "text" }>;
}) {
  const label =
    segment.type === "code"
      ? "代码块"
      : segment.type === "image"
        ? "图片"
        : segment.type === "image-placeholder"
          ? "图片生成占位"
        : segment.title;
  return (
    <div
      aria-atomic="true"
      aria-live="polite"
      className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3 py-3 text-sm text-[var(--muted-foreground)]"
      role="status"
    >
      正在加载 {label}…
    </div>
  );
}

function RelatedArtifactsFallback({ count }: { count: number }) {
  return (
    <div
      aria-atomic="true"
      aria-live="polite"
      className="mt-3 rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3 py-3 text-sm text-[var(--muted-foreground)]"
      role="status"
    >
      正在加载 {count} 条相关证据…
    </div>
  );
}
