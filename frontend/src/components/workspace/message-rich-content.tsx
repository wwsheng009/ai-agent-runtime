import {
  ArrowUpRightIcon,
  ChevronDownIcon,
  CheckIcon,
  InfoIcon,
  PaperclipIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { useId, useState } from "react";

import { CodeBlock } from "@/components/ui/code-block";
import { MessageMarkdown } from "@/components/workspace/message-markdown";
import { type Artifact, type MessageSegment } from "@/data/mock";
import { cn } from "@/lib/utils";

type RichMessageSegment = Exclude<MessageSegment, { type: "text" }>;

type MessageRichSegmentProps = {
  segment: RichMessageSegment;
};

type MessageRelatedArtifactsProps = {
  onSelectArtifact: (artifactId: string) => void;
  relatedArtifacts: Artifact[];
};

export function MessageRichSegment({ segment }: MessageRichSegmentProps) {
  const baseId = useId();
  const titleId = `${baseId}-title`;
  const descriptionId = `${baseId}-description`;

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
        className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3"
      >
        <div
          className="mb-2 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]"
          id={titleId}
        >
          {segment.title}
        </div>
        <dl className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
          {segment.items.map((item) => (
            <div
              key={`${segment.title}-${item.label}`}
              className="rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2.5"
            >
              <dt className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                {item.label}
              </dt>
              <dd
                className={cn(
                  "app-chat-copy mt-1.5 font-semibold text-[var(--foreground)]",
                  item.tone === "accent" && "text-[#8fd0c6]",
                  item.tone === "warning" && "text-[#f0c77b]",
                  item.tone === "muted" && "text-[var(--muted-foreground)]",
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
        className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3"
      >
        <div
          className="mb-2 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]"
          id={titleId}
        >
          {segment.title}
        </div>
        <ul className="space-y-2.5" role="list">
          {segment.items.map((item) => (
            <li
              key={`${segment.title}-${item}`}
              className="app-chat-copy flex items-start gap-3 text-[var(--foreground)]"
            >
              <span className="mt-0.5 inline-flex size-[1.125rem] shrink-0 items-center justify-center rounded-[0.65rem] border border-[#8fd0c6]/20 bg-[#8fd0c6]/10 text-[#8fd0c6]">
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
            "border-[#f0c77b]/16 bg-[linear-gradient(180deg,rgba(240,199,123,0.08),rgba(240,199,123,0.03))]",
          iconClass: "border-[#f0c77b]/24 bg-[#f0c77b]/12 text-[#f0c77b]",
          Icon: TriangleAlertIcon,
        }
      : segment.tone === "success"
        ? {
            wrapper:
              "border-[#8fd0c6]/16 bg-[linear-gradient(180deg,rgba(143,208,198,0.08),rgba(143,208,198,0.03))]",
            iconClass: "border-[#8fd0c6]/24 bg-[#8fd0c6]/12 text-[#8fd0c6]",
            Icon: CheckIcon,
          }
        : {
            wrapper: "border-[var(--border)] bg-[var(--surface-softer)]",
            iconClass:
              "border-[var(--border)] bg-[var(--surface-soft)] text-[var(--foreground)]",
            Icon: InfoIcon,
          };

  return (
    <section
      aria-describedby={descriptionId}
      aria-labelledby={titleId}
      className={cn(
        "flex items-start gap-3 rounded-[0.8rem] border px-3 py-3",
        toneStyles.wrapper,
      )}
      role="note"
    >
      <div
        className={cn(
          "inline-flex size-7 shrink-0 items-center justify-center rounded-[0.7rem] border",
          toneStyles.iconClass,
        )}
      >
        <toneStyles.Icon size={14} />
      </div>
      <div className="min-w-0">
        <div
          className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]"
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
      className="mt-3 rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3"
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
            className="flex items-center gap-2 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]"
            id={titleId}
          >
            <PaperclipIcon size={14} />
            Related evidence
          </div>
          <div className="mt-1 app-text-11 text-[var(--muted-foreground)]">
            {summary}
          </div>
        </div>
        <ChevronDownIcon
          size={16}
          className={cn(
            "shrink-0 text-[var(--muted-foreground)] transition-transform duration-200",
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
                  className="flex w-full items-center justify-between gap-3 rounded-[0.7rem] border border-[var(--border)] bg-transparent px-3 py-2.5 text-left transition hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]"
                >
                  <div className="min-w-0">
                    <div className="truncate app-text-13 font-semibold text-[var(--foreground)]">
                      {artifact.name}
                    </div>
                    <div
                      className="truncate app-text-11 text-[var(--muted-foreground)]"
                      id={`${baseId}-${artifact.id}-summary`}
                    >
                      {artifact.summary}
                    </div>
                  </div>
                  <ArrowUpRightIcon
                    size={14}
                    className="shrink-0 text-[var(--muted-foreground)]"
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
