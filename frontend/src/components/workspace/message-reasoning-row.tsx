import { BrainCircuitIcon, ChevronDownIcon } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { displayReasoningText } from "@/lib/trajectory/reasoning-window";
import { type ReasoningMessageSegment } from "@/lib/workspace-thread-state";
import { cn } from "@/lib/utils";

type MessageReasoningRowProps = {
  segment: ReasoningMessageSegment;
  streaming?: boolean;
};

function summarizeReasoning(content: string): string {
  const compact = content.replace(/\s+/g, " ").trim();
  return compact.length > 96 ? `${compact.slice(0, 96).trimEnd()}…` : compact;
}

export function MessageReasoningRow({
  segment,
  streaming = false,
}: MessageReasoningRowProps) {
  const { t } = useTranslation("workspace");
  const [open, setOpen] = useState(false);
  const baseId = useId();
  const titleId = `${baseId}-title`;
  const panelId = `${baseId}-panel`;
  const running = streaming && segment.running !== false;
  const summary = summarizeReasoning(segment.content);
  const hasContent = segment.content.trim().length > 0;
  const reasoningDisplay = displayReasoningText(segment.content);
  const trimmed = reasoningDisplay.droppedChars > 0;

  return (
    <section
      aria-labelledby={titleId}
      className="mt-2 overflow-hidden rounded-card-lg border border-border bg-surface-softer"
    >
      <button
        type="button"
        aria-controls={panelId}
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
        className="flex w-full items-center gap-2.5 px-3 py-2.5 text-left transition hover:bg-surface-soft"
      >
        <BrainCircuitIcon size={14} className="shrink-0 text-accent-teal" />
        <span
          className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground"
          id={titleId}
        >
          {t("panels.messages.reasoningRow.title")}
          {running ? "…" : ""}
        </span>
        {running ? (
          <span className="size-1.5 shrink-0 animate-pulse rounded-full bg-accent-teal" />
        ) : null}
        <span className="min-w-0 flex-1 truncate app-text-11 text-muted-foreground">
          {hasContent ? summary : "Waiting for reasoning output…"}
        </span>
        <ChevronDownIcon
          size={14}
          className={cn(
            "shrink-0 text-muted-foreground transition-transform duration-200",
            open ? "rotate-0" : "-rotate-90",
          )}
        />
      </button>
      <div
        className={cn("border-t border-border", !open && "hidden")}
        hidden={!open}
        id={panelId}
      >
        {trimmed ? (
          <div className="border-b border-border px-3 py-1.5 app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
            {t("panels.messages.reasoningRow.trimmed", {
              chars: reasoningDisplay.droppedChars.toLocaleString(),
            })}
          </div>
        ) : null}
        <pre className="max-h-72 overflow-y-auto whitespace-pre-wrap break-words px-3 py-3 app-text-12 app-chat-copy text-muted-foreground">
          {reasoningDisplay.visible}
        </pre>
      </div>
    </section>
  );
}
