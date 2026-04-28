import {
  BotIcon,
  ScrollTextIcon,
  User2Icon,
} from "lucide-react";
import { lazy, Suspense, type CSSProperties } from "react";

import { Badge } from "@/components/ui/badge";
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

type MessageListProps = {
  artifacts: Artifact[];
  className?: string;
  contentClassName?: string;
  isResponding: boolean;
  messages: ChatMessage[];
  onSelectArtifact: (artifactId: string) => void;
  style?: CSSProperties;
};

export function MessageList({
  artifacts,
  className,
  contentClassName,
  isResponding,
  messages,
  onSelectArtifact,
  style,
}: MessageListProps) {
  const artifactMap = new Map(artifacts.map((artifact) => [artifact.id, artifact]));
  const lastMessage = messages[messages.length - 1];
  const streamingMessageId =
    isResponding && lastMessage?.role === "assistant" ? lastMessage.id : null;
  const logLabel =
    messages.length > 0 ? "Workspace conversation timeline" : "Empty workspace conversation timeline";

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

          return (
            <article
              aria-busy={message.id === streamingMessageId ? "true" : undefined}
              aria-describedby={describedBy}
              aria-labelledby={labelId}
              aria-setsize={messages.length}
              aria-posinset={messageIndex + 1}
              key={message.id}
              className={cn("flex w-full", isUser ? "justify-end" : "justify-start")}
            >
              {isUser ? (
                <div className="relative w-full max-w-[42rem] overflow-hidden rounded-[1rem] border border-[#f0c77b]/16 bg-[linear-gradient(180deg,rgba(240,199,123,0.08),rgba(240,199,123,0.03))] px-4 py-3.5 shadow-[0_16px_40px_rgba(0,0,0,0.12)]">
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
                        </div>
                      </div>
                    </div>
                    <Badge className="border-transparent bg-[var(--surface-soft)] text-[var(--foreground)]">
                      {message.role}
                    </Badge>
                  </div>
                  <div className="sr-only" id={statusId}>
                    {message.id === streamingMessageId
                      ? "Streaming response in progress"
                      : `${message.role} message`}
                  </div>

                  <div className="mt-3 space-y-4">
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
              ) : (
                <div className="flex w-full max-w-[52rem] items-start gap-3">
                  <div className="mt-0.5 inline-flex size-7 shrink-0 items-center justify-center rounded-[0.7rem] border border-[#8fd0c6]/20 bg-[#8fd0c6]/10 text-[#8fd0c6]">
                    <BotIcon size={14} />
                  </div>
                  <div className="min-w-0 flex-1">
                    <div className="mb-2 flex flex-wrap items-center gap-x-3 gap-y-1">
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
                    </div>
                    <div className="sr-only" id={statusId}>
                      {message.id === streamingMessageId
                        ? "Streaming response in progress"
                        : `${message.role} message`}
                    </div>

                    <div className="relative overflow-hidden rounded-[1rem] border border-[var(--border)] bg-[var(--surface-softer)] px-4 py-3.5 shadow-[0_12px_30px_rgba(0,0,0,0.08)]">
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
