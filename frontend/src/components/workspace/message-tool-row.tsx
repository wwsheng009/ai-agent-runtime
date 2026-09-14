// P1-6 / 批次 B4（§8.5 tool-call）：24px 单行 = 图标 + 工具名 + 分隔点 + 富摘要 + 状态词缀。
// 呈现结论全部来自 lib/tool-row/state；这里只做布局、图标与交互接线。
// 失败态：图标 + 摘要尾缀表达（不整行变红底，行高不随错误内容增长）；详情在展开面板。

import {
  BracesIcon,
  CheckCircle2Icon,
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

import { ChatProcessRow } from "@/components/workspace/chat-process-row";
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

import { ToolRowPanels } from "./tool-row/tool-row-panels";
import { ToolRowSummaryView } from "./tool-row/tool-row-summary";

type MessageToolRowProps = {
  anchorKey?: string;
  flowKey?: string;
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

const TONE_ICON: Record<ToolStatusTone, string> = {
  pending: "text-muted-foreground",
  running: "animate-spin text-accent-teal",
  success: "text-accent-teal",
  danger: "text-accent-orange",
};

const TONE_TEXT: Record<ToolStatusTone, string> = {
  pending: "text-muted-foreground",
  running: "text-accent-teal",
  success: "text-accent-teal",
  danger: "text-accent-orange",
};

export function MessageToolRow({
  anchorKey,
  flowKey,
  segment,
  resolveFilePathLink,
}: MessageToolRowProps) {
  const { t } = useTranslation("workspace");
  const [open, setOpen] = useState(false);
  const baseId = useId();
  const panelId = `${baseId}-panel`;

  const presentation = resolveToolRowPresentation(segment);
  const tone = STATUS_TONE[segment.status];
  const StatusIcon = STATUS_ICON[segment.status];
  const KindIcon = KIND_ICON[presentation.kind];
  const statusLabel = t(STATUS_LABEL_KEY[segment.status]);
  // 标题始终是工具名（§5.5 / §8.5：图标 + 工具名 + 分隔点 + 摘要；
  // 参考站实测样例 `Grep | resource-manager/retry`）。kind 只决定图标与摘要语义，
  // 不参与标题——否则会与运行时工具身份脱节、也无法被 e2e 以工具名定位。
  const title = segment.name;

  const activate =
    presentation.filePath && !presentation.fileLinkDisabled
      ? (resolveFilePathLink?.(presentation.filePath) ?? null)
      : null;
  const filePathLink: FilePathLink | null =
    presentation.filePath && activate ? { path: presentation.filePath, activate } : null;
  const mono = presentation.kind === "terminal" || presentation.kind === "search";
  const failureSuffix = presentation.isFailure
    ? (segment.errorMessage?.trim() ?? "")
    : "";
  const hasSummary =
    presentation.summary.parts.length > 0 || failureSuffix.length > 0;

  return (
    <div {...presentation.attributes}>
      <ChatProcessRow
        anchorKey={anchorKey}
        expandable={presentation.expandable}
        expanded={open}
        flowKey={flowKey}
        icon={<KindIcon className="size-4 text-muted-foreground" />}
        interactiveSummary
        onToggle={() => setOpen((current) => !current)}
        panelId={panelId}
        rowKind="tool"
        statusLabel={t("panels.messages.toolRow.announcement", {
          name: segment.name,
          status: statusLabel,
        })}
        summary={
          hasSummary ? (
            <>
              <ToolRowSummaryView
                fileLinkDisabled={presentation.fileLinkDisabled}
                filePathLink={filePathLink}
                mono={mono}
                parts={presentation.summary.parts}
                tone={presentation.summary.tone}
              />
              {failureSuffix ? (
                <span className="shrink-0 truncate text-accent-orange">
                  {failureSuffix}
                </span>
              ) : null}
            </>
          ) : undefined
        }
        title={title}
        tone={presentation.isFailure ? "danger" : "default"}
        toggleLabel={t(
          open
            ? "panels.messages.toolRow.collapseLabel"
            : "panels.messages.toolRow.expandLabel",
        )}
        trailing={
          <span
            className={`inline-flex shrink-0 items-center gap-1 app-text-11 ${TONE_TEXT[tone]}`}
            data-tool-row-status={segment.status}
          >
            <StatusIcon className={TONE_ICON[tone]} size={12} />
            {statusLabel}
          </span>
        }
      />

      <ToolRowPanels
        expandable={presentation.expandable}
        isFailure={presentation.isFailure}
        kind={presentation.kind}
        open={open}
        panelId={panelId}
        segment={segment}
      />
    </div>
  );
}
