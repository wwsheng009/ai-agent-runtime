import {
  CheckCircle2Icon,
  ChevronDownIcon,
  CircleIcon,
  LoaderCircleIcon,
  WrenchIcon,
  XCircleIcon,
} from "lucide-react";
import { useId, useState, type ComponentType } from "react";

import { type ToolMessageSegment } from "@/lib/workspace-thread-state";
import { cn } from "@/lib/utils";

type ToolStatus = ToolMessageSegment["status"];

type StatusMeta = {
  label: string;
  icon: ComponentType<{ size?: number; className?: string }>;
  iconClassName: string;
  badgeClassName: string;
};

const STATUS_META: Record<ToolStatus, StatusMeta> = {
  started: {
    label: "Started",
    icon: CircleIcon,
    iconClassName: "text-[var(--muted-foreground)]",
    badgeClassName: "border-[var(--border)] bg-[var(--surface-soft)] text-[var(--muted-foreground)]",
  },
  running: {
    label: "Running",
    icon: LoaderCircleIcon,
    iconClassName: "animate-spin text-[#8fd0c6]",
    badgeClassName: "border-[#8fd0c6]/20 bg-[#8fd0c6]/10 text-[#8fd0c6]",
  },
  finished: {
    label: "Finished",
    icon: CheckCircle2Icon,
    iconClassName: "text-[#8fd0c6]",
    badgeClassName: "border-[#8fd0c6]/20 bg-[#8fd0c6]/10 text-[#8fd0c6]",
  },
  error: {
    label: "Failed",
    icon: XCircleIcon,
    iconClassName: "text-[#f0c77b]",
    badgeClassName: "border-[#f0c77b]/24 bg-[#f0c77b]/12 text-[#f0c77b]",
  },
};

type MessageToolRowProps = {
  segment: ToolMessageSegment;
};

export function MessageToolRow({ segment }: MessageToolRowProps) {
  const [open, setOpen] = useState(false);
  const baseId = useId();
  const titleId = `${baseId}-title`;
  const panelId = `${baseId}-panel`;
  const meta = STATUS_META[segment.status];
  const StatusIcon = meta.icon;
  const hasArgs = Boolean(segment.argsSummary?.trim());

  return (
    <section
      aria-labelledby={titleId}
      className="mt-2 overflow-hidden rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)]"
    >
      <div className="flex items-center gap-2.5 px-3 py-2.5">
        <WrenchIcon size={14} className="shrink-0 text-[var(--muted-foreground)]" />
        <span
          className="min-w-0 flex-1 truncate app-text-13 font-semibold text-[var(--foreground)]"
          id={titleId}
        >
          {segment.name}
        </span>
        <span
          className={cn(
            "inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 app-text-10 uppercase tracking-[0.12em]",
            meta.badgeClassName,
          )}
        >
          <StatusIcon size={11} className={meta.iconClassName} />
          {meta.label}
        </span>
        {hasArgs ? (
          <button
            type="button"
            aria-controls={panelId}
            aria-expanded={open}
            onClick={() => setOpen((current) => !current)}
            className="shrink-0 rounded-[0.5rem] p-1 text-[var(--muted-foreground)] transition hover:bg-[var(--surface-soft)] hover:text-[var(--foreground)]"
          >
            <ChevronDownIcon
              size={14}
              className={cn(
                "transition-transform duration-200",
                open ? "rotate-0" : "-rotate-90",
              )}
            />
          </button>
        ) : null}
      </div>

      {hasArgs ? (
        <div className={cn("border-t border-[var(--border)]", !open && "hidden")} hidden={!open} id={panelId}>
          <div className="px-3 py-2.5">
            <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              Input
            </div>
            <pre className="mt-1.5 max-h-48 overflow-y-auto whitespace-pre-wrap break-words app-text-12 app-chat-copy text-[var(--muted-foreground)]">
              {segment.argsSummary}
            </pre>
          </div>
        </div>
      ) : null}

      {segment.resultSummary?.trim() ? (
        <div className="border-t border-[var(--border)] px-3 py-2.5">
          <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            Output
          </div>
          <pre className="mt-1.5 max-h-48 overflow-y-auto whitespace-pre-wrap break-words app-text-12 app-chat-copy text-[var(--foreground)]">
            {segment.resultSummary}
          </pre>
        </div>
      ) : null}

      {segment.errorMessage ? (
        <div className="border-t border-[#f0c77b]/16 bg-[#f0c77b]/8 px-3 py-2.5 app-text-11 text-[#f0c77b]">
          {segment.errorMessage}
        </div>
      ) : null}
    </section>
  );
}
