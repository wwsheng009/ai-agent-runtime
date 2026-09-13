// P1-6：工具行状态机视图。呈现结论全部来自 lib/tool-row/state；这里只做布局、图标与交互接线。

import {
  BracesIcon,
  CheckCircle2Icon,
  ChevronDownIcon,
  CircleIcon,
  FileDiffIcon,
  FileTextIcon,
  GlobeIcon,
  ImageIcon,
  LoaderCircleIcon,
  SearchIcon,
  SquareTerminalIcon,
  WrenchIcon,
  XCircleIcon,
} from "lucide-react";
import { useId, useState, type ComponentType } from "react";
import { useTranslation } from "react-i18next";

import { type ToolMessageSegment } from "@/lib/thread-state/messages";
import { type FilePathLink } from "@/lib/tool-row";
import { type ToolCardKind } from "@/lib/tool-row/kind";
import {
  resolveToolRowPresentation,
  STATUS_LABEL_KEY,
  STATUS_TONE,
  type ToolStatus,
  type ToolStatusTone,
} from "@/lib/tool-row/state";
import { cn } from "@/lib/utils";

import { ToolRowPanels } from "./tool-row/tool-row-panels";
import { ToolRowSummaryView } from "./tool-row/tool-row-summary";

type MessageToolRowProps = {
  segment: ToolMessageSegment;
  /**
   * 宿主路径解析：返回可激活回调才渲染链接，返回 null 即无可跳转目标（不渲染死按钮）。
   * 必须为纯查找，不得产生副作用。
   */
  resolveFilePathLink?: (path: string) => (() => void) | null;
};

const STATUS_ICON: Record<ToolStatus, ComponentType<{ size?: number; className?: string }>> = {
  started: CircleIcon,
  running: LoaderCircleIcon,
  finished: CheckCircle2Icon,
  error: XCircleIcon,
};

const KIND_ICON: Record<ToolCardKind, ComponentType<{ size?: number; className?: string }>> = {
  read: FileTextIcon,
  diff: FileDiffIcon,
  terminal: SquareTerminalIcon,
  search: SearchIcon,
  web: GlobeIcon,
  image: ImageIcon,
  json: BracesIcon,
  generic: WrenchIcon,
};

const TONE_BADGE: Record<ToolStatusTone, string> = {
  pending: "border-border bg-surface-soft text-muted-foreground",
  running: "border-accent-teal/20 bg-accent-teal/10 text-accent-teal",
  success: "border-accent-teal/20 bg-accent-teal/10 text-accent-teal",
  danger: "border-accent-gold/24 bg-accent-gold/12 text-accent-gold",
};

const TONE_ICON: Record<ToolStatusTone, string> = {
  pending: "text-muted-foreground",
  running: "animate-spin text-accent-teal",
  success: "text-accent-teal",
  danger: "text-accent-gold",
};

export function MessageToolRow({ segment, resolveFilePathLink }: MessageToolRowProps) {
  const { t } = useTranslation("workspace");
  const [open, setOpen] = useState(false);
  const baseId = useId();
  const titleId = `${baseId}-title`;
  const panelId = `${baseId}-panel`;

  const presentation = resolveToolRowPresentation(segment);
  const tone = STATUS_TONE[segment.status];
  const StatusIcon = STATUS_ICON[segment.status];
  const KindIcon = KIND_ICON[presentation.kind];
  const statusLabel = t(STATUS_LABEL_KEY[segment.status]);

  const activate =
    presentation.filePath && !presentation.fileLinkDisabled
      ? (resolveFilePathLink?.(presentation.filePath) ?? null)
      : null;
  const filePathLink: FilePathLink | null =
    presentation.filePath && activate ? { path: presentation.filePath, activate } : null;
  const mono = presentation.kind === "terminal" || presentation.kind === "search";

  return (
    <section
      aria-labelledby={titleId}
      className={cn(
        "mt-2 overflow-hidden rounded-card-lg border bg-surface-softer",
        presentation.isFailure ? "border-accent-gold/24" : "border-border",
      )}
      {...presentation.attributes}
    >
      <div className="flex items-center gap-2.5 px-3 py-2.5">
        <KindIcon size={14} className="shrink-0 text-muted-foreground" />
        <span
          className="max-w-[40%] shrink-0 truncate app-text-13 font-semibold text-foreground"
          id={titleId}
        >
          {segment.name}
        </span>
        <span className="sr-only" role="status">
          {t("panels.messages.toolRow.announcement", {
            name: segment.name,
            status: statusLabel,
          })}
        </span>
        <div className="min-w-0 flex-1">
          <ToolRowSummaryView
            fileLinkDisabled={presentation.fileLinkDisabled}
            filePathLink={filePathLink}
            mono={mono}
            parts={presentation.summary.parts}
            tone={presentation.summary.tone}
          />
        </div>
        <span
          className={cn(
            "inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 app-text-10 uppercase tracking-[0.12em]",
            TONE_BADGE[tone],
          )}
        >
          <StatusIcon size={11} className={TONE_ICON[tone]} />
          {statusLabel}
        </span>
        {presentation.expandable ? (
          <button
            aria-controls={panelId}
            aria-expanded={open}
            aria-label={t(
              open
                ? "panels.messages.toolRow.collapseLabel"
                : "panels.messages.toolRow.expandLabel",
            )}
            className="shrink-0 rounded-chip p-1 text-muted-foreground transition hover:bg-surface-soft hover:text-foreground"
            onClick={() => setOpen((current) => !current)}
            type="button"
          >
            <ChevronDownIcon
              size={14}
              className={cn("transition-transform duration-200", open ? "rotate-0" : "-rotate-90")}
            />
          </button>
        ) : null}
      </div>

      <ToolRowPanels
        expandable={presentation.expandable}
        isFailure={presentation.isFailure}
        kind={presentation.kind}
        open={open}
        panelId={panelId}
        segment={segment}
      />
    </section>
  );
}
