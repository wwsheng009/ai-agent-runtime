import {
  ArrowUpRightIcon,
  ChevronDownIcon,
  CheckIcon,
  InfoIcon,
  LoaderCircleIcon,
  PaperclipIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { useId, useState } from "react";

import { CodeBlock } from "@/components/ui/code-block";
import { MessageMarkdown } from "@/components/workspace/message-markdown";
import { MessageReasoningRow } from "@/components/workspace/message-reasoning-row";
import { MessageToolRow } from "@/components/workspace/message-tool-row";
import { type Artifact, type MessageSegment } from "@/data/mock";
import { cn } from "@/lib/utils";

type RichMessageSegment = Exclude<MessageSegment, { type: "text" }>;

type MessageRichSegmentProps = {
  segment: RichMessageSegment;
  onSelectArtifact?: (artifactId: string) => void;
};

type MessageRelatedArtifactsProps = {
  onSelectArtifact: (artifactId: string) => void;
  relatedArtifacts: Artifact[];
};

export function MessageRichSegment({
  onSelectArtifact,
  segment,
}: MessageRichSegmentProps) {
  const baseId = useId();
  const titleId = `${baseId}-title`;
  const descriptionId = `${baseId}-description`;

  if (segment.type === "reasoning") {
    return <MessageReasoningRow segment={segment} />;
  }

  if (segment.type === "tool") {
    return <MessageToolRow segment={segment} />;
  }

  if (segment.type === "image-placeholder") {
    const isFailed = segment.phase === "failed";
    const progress =
      typeof segment.progress === "number" && Number.isFinite(segment.progress)
        ? Math.max(0, Math.min(1, segment.progress))
        : null;
    const phaseLabel =
      segment.phase === "started"
        ? "图片正在生成"
        : segment.phase === "partial"
          ? "图片生成中"
          : segment.phase === "completed"
            ? "图片已生成，正在保存"
            : "图片生成失败";

    return (
      <section
        aria-describedby={descriptionId}
        aria-labelledby={titleId}
        className={cn(
          "mt-2 overflow-hidden rounded-card-lg border p-3",
          isFailed
            ? "border-accent-gold/16 bg-[linear-gradient(180deg,rgba(240,199,123,0.08),rgba(240,199,123,0.03))]"
            : "border-border bg-surface-softer",
        )}
        role="status"
      >
        <div className="flex items-start gap-3">
          <div
            className={cn(
              "inline-flex size-10 shrink-0 items-center justify-center rounded-card-lg border",
              isFailed
                ? "border-accent-gold/24 bg-accent-gold/12 text-accent-gold"
                : "border-accent-teal/18 bg-accent-teal/10 text-accent-teal",
            )}
          >
            {isFailed ? (
              <TriangleAlertIcon size={16} />
            ) : (
              <LoaderCircleIcon className="animate-spin" size={16} />
            )}
          </div>
          <div className="min-w-0 flex-1">
            <div
              className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground"
              id={titleId}
            >
              {phaseLabel}
            </div>
            <div
              className="mt-1.5 app-text-13 font-semibold text-foreground"
              id={descriptionId}
            >
              {segment.caption || "等待生成结果写入会话。"}
            </div>
            {progress !== null ? (
              <div className="mt-2.5 h-2 overflow-hidden rounded-full bg-surface-soft">
                <div
                  className={cn(
                    "h-full rounded-full transition-[width] duration-300",
                    isFailed
                      ? "bg-accent-gold"
                      : "bg-[linear-gradient(90deg,var(--accent-teal),var(--accent-gold))]",
                  )}
                  style={{ width: `${Math.max(progress, 0.04) * 100}%` }}
                />
              </div>
            ) : (
              <div className="mt-2.5 h-2 overflow-hidden rounded-full bg-surface-soft">
                <div className="h-full w-1/3 animate-pulse rounded-full bg-[linear-gradient(90deg,rgba(143,208,198,0.15),rgba(240,199,123,0.35),rgba(143,208,198,0.15))]" />
              </div>
            )}
            {segment.errorMessage ? (
              <div className="mt-2.5 rounded-field border border-accent-gold/16 bg-accent-gold/8 px-3 py-2 app-text-11 text-accent-gold">
                {segment.errorMessage}
              </div>
            ) : null}
          </div>
        </div>
      </section>
    );
  }

  if (segment.type === "image") {
    const isClickable = Boolean(segment.artifactId && onSelectArtifact);

    return (
      <figure className="mt-2 overflow-hidden rounded-card border border-border bg-surface-softer">
        <button
          type="button"
          onClick={() => {
            if (segment.artifactId && onSelectArtifact) {
              onSelectArtifact(segment.artifactId);
            }
          }}
          disabled={!isClickable}
          className="block w-full text-left disabled:cursor-default"
        >
          <img
            alt={segment.alt ?? segment.caption ?? "Generated image"}
            className="block h-auto w-full max-h-[18rem] bg-black/40 object-contain"
            loading="lazy"
            src={segment.src}
          />
        </button>
        {segment.caption ? (
          <figcaption className="px-3 py-2 app-text-11 text-muted-foreground">
            {segment.caption}
          </figcaption>
        ) : null}
      </figure>
    );
  }

  if (segment.type === "code") {
    return (
      <CodeBlock
        collapsible
        code={segment.code}
        language={segment.language}
        title={segment.title}
      />
    );
  }

  if (segment.type === "receipt") {
    return (
      <section
        aria-labelledby={titleId}
        className="rounded-card border border-border bg-surface-softer p-3"
      >
        <div
          className="mb-2 app-text-10 uppercase tracking-[0.14em] text-muted-foreground"
          id={titleId}
        >
          {segment.title}
        </div>
        <dl className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
          {segment.items.map((item) => (
            <div
              key={`${segment.title}-${item.label}`}
              className="rounded-field border border-border bg-surface-solid px-3 py-2.5"
            >
              <dt className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                {item.label}
              </dt>
              <dd
                className={cn(
                  "app-chat-copy mt-1.5 font-semibold text-foreground",
                  item.tone === "accent" && "text-accent-teal",
                  item.tone === "warning" && "text-accent-gold",
                  item.tone === "muted" && "text-muted-foreground",
                )}
              >
                {item.value}
              </dd>
            </div>
          ))}
        </dl>
      </section>
    );
  }

  if (segment.type === "checklist") {
    return (
      <section
        aria-labelledby={titleId}
        className="rounded-card border border-border bg-surface-softer p-3"
      >
        <div
          className="mb-2 app-text-10 uppercase tracking-[0.14em] text-muted-foreground"
          id={titleId}
        >
          {segment.title}
        </div>
        <ul className="space-y-2.5" role="list">
          {segment.items.map((item) => (
            <li
              key={`${segment.title}-${item}`}
              className="app-chat-copy flex items-start gap-3 text-foreground"
            >
              <span className="mt-0.5 inline-flex size-[1.125rem] shrink-0 items-center justify-center rounded-control border border-accent-teal/20 bg-accent-teal/10 text-accent-teal">
                <CheckIcon size={12} />
              </span>
              <span>{item}</span>
            </li>
          ))}
        </ul>
      </section>
    );
  }

  const toneStyles =
    segment.tone === "warning"
      ? {
          wrapper:
            "border-accent-gold/16 bg-[linear-gradient(180deg,rgba(240,199,123,0.08),rgba(240,199,123,0.03))]",
          iconClass: "border-accent-gold/24 bg-accent-gold/12 text-accent-gold",
          Icon: TriangleAlertIcon,
        }
      : segment.tone === "success"
        ? {
            wrapper:
              "border-accent-teal/16 bg-[linear-gradient(180deg,rgba(143,208,198,0.08),rgba(143,208,198,0.03))]",
            iconClass: "border-accent-teal/24 bg-accent-teal/12 text-accent-teal",
            Icon: CheckIcon,
          }
        : {
            wrapper: "border-border bg-surface-softer",
            iconClass:
              "border-border bg-surface-soft text-foreground",
            Icon: InfoIcon,
          };

  return (
    <section
      aria-describedby={descriptionId}
      aria-labelledby={titleId}
      className={cn(
        "flex items-start gap-3 rounded-card border px-3 py-3",
        toneStyles.wrapper,
      )}
      role="note"
    >
      <div
        className={cn(
          "inline-flex size-7 shrink-0 items-center justify-center rounded-field border",
          toneStyles.iconClass,
        )}
      >
        <toneStyles.Icon size={14} />
      </div>
      <div className="min-w-0">
        <div
          className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground"
          id={titleId}
        >
          {segment.title}
        </div>
        <div id={descriptionId}>
          <MessageMarkdown className="mt-1.5" content={segment.content} />
        </div>
      </div>
    </section>
  );
}

export function MessageRelatedArtifacts({
  onSelectArtifact,
  relatedArtifacts,
}: MessageRelatedArtifactsProps) {
  const [open, setOpen] = useState(false);
  const baseId = useId();
  const titleId = `${baseId}-title`;
  const descriptionId = `${baseId}-description`;
  const panelId = `${baseId}-panel`;
  const evidenceLabel = `${relatedArtifacts.length} related evidence item${
    relatedArtifacts.length === 1 ? "" : "s"
  }`;
  const summary = open ? `${evidenceLabel} available` : `${evidenceLabel} hidden`;

  return (
    <section
      aria-describedby={descriptionId}
      aria-labelledby={titleId}
      className="mt-3 rounded-card border border-border bg-surface-softer p-3"
    >
      <button
        type="button"
        onClick={() => setOpen((current) => !current)}
        aria-controls={panelId}
        aria-describedby={descriptionId}
        aria-expanded={open}
        className="flex w-full items-center justify-between gap-3 text-left"
      >
        <div className="min-w-0">
          <div
            className="flex items-center gap-2 app-text-10 uppercase tracking-[0.14em] text-muted-foreground"
            id={titleId}
          >
            <PaperclipIcon size={14} />
            Related evidence
          </div>
          <div className="mt-1 app-text-11 text-muted-foreground">
            {summary}
          </div>
        </div>
        <ChevronDownIcon
          size={16}
          className={cn(
            "shrink-0 text-muted-foreground transition-transform duration-200",
            open ? "rotate-0" : "-rotate-90",
          )}
        />
      </button>
      <div className="sr-only" id={descriptionId}>
        {`${evidenceLabel}; section is ${open ? "expanded" : "collapsed"}`}
      </div>
      <div className="mt-2.5" hidden={!open} id={panelId}>
        {open ? (
          <ul className="grid gap-1.5" role="list">
            {relatedArtifacts.map((artifact) => (
              <li key={artifact.id}>
                <button
                  aria-describedby={`${baseId}-${artifact.id}-summary`}
                  type="button"
                  onClick={() => onSelectArtifact(artifact.id)}
                  className="flex w-full items-center justify-between gap-3 rounded-field border border-border bg-transparent px-3 py-2.5 text-left transition hover:border-border-strong hover:bg-surface-soft"
                >
                  <div className="min-w-0">
                    <div className="truncate app-text-13 font-semibold text-foreground">
                      {artifact.name}
                    </div>
                    <div
                      className="truncate app-text-11 text-muted-foreground"
                      id={`${baseId}-${artifact.id}-summary`}
                    >
                      {artifact.summary}
                    </div>
                  </div>
                  <ArrowUpRightIcon
                    size={14}
                    className="shrink-0 text-muted-foreground"
                  />
                </button>
              </li>
            ))}
          </ul>
        ) : null}
      </div>
    </section>
  );
}
