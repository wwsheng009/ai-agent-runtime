// P1-6：工具行行内摘要（失败文本 / 文件路径链接 / 命令 / 查询 / URL / diff 行数）。

import { ExternalLinkIcon } from "lucide-react";
import { Fragment } from "react";
import { useTranslation } from "react-i18next";

import { isExternalUrl, type FilePathLink } from "@/lib/tool-row";
import { type ToolRowSummaryPart } from "@/lib/tool-row/state";
import { cn } from "@/lib/utils";

type ToolRowSummaryViewProps = {
  mono: boolean;
  fileLinkDisabled: boolean;
  filePathLink: FilePathLink | null;
  parts: ToolRowSummaryPart[];
  tone: "default" | "danger";
};

function stopRowActivation(event: { stopPropagation: () => void }) {
  // 链接是行内的嵌套交互：显式阻断冒泡，避免宿主将来给整行加点击/键盘行为时被联动。
  event.stopPropagation();
}

export function ToolRowSummaryView({
  mono,
  fileLinkDisabled,
  filePathLink,
  parts,
  tone,
}: ToolRowSummaryViewProps) {
  if (parts.length === 0) {
    return null;
  }

  return (
    <span
      className={cn(
        "flex min-w-0 items-center gap-1.5 overflow-hidden",
        tone === "danger" ? "text-accent-gold" : "text-muted-foreground",
      )}
      data-tool-row-summary="true"
    >
      {parts.map((part, index) => (
        <Fragment key={`${part.type}-${index}`}>
          {index > 0 ? (
            <span aria-hidden="true" className="shrink-0 text-muted-foreground/50">
              ·
            </span>
          ) : null}
          <ToolRowSummaryPartView
            fileLinkDisabled={fileLinkDisabled}
            filePathLink={filePathLink}
            mono={mono}
            part={part}
          />
        </Fragment>
      ))}
    </span>
  );
}

function ToolRowSummaryPartView({
  fileLinkDisabled,
  filePathLink,
  mono,
  part,
}: {
  fileLinkDisabled: boolean;
  filePathLink: FilePathLink | null;
  mono: boolean;
  part: ToolRowSummaryPart;
}) {
  const { t } = useTranslation("workspace");

  if (part.type === "path") {
    if (filePathLink && filePathLink.path === part.path) {
      return (
        <button
          aria-label={t("panels.messages.toolRow.openFile", { path: part.path })}
          className="min-w-0 truncate rounded-sm font-mono underline decoration-dotted underline-offset-2 transition hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-teal/45"
          data-tool-row-file-link="true"
          onClick={(event) => {
            stopRowActivation(event);
            filePathLink.activate();
          }}
          onKeyDown={stopRowActivation}
          title={part.path}
          type="button"
        >
          {part.path}
        </button>
      );
    }
    return (
      <span
        className={cn("min-w-0 truncate font-mono", fileLinkDisabled && "opacity-70")}
        data-tool-row-file-link="false"
        data-tool-row-link-disabled={fileLinkDisabled ? "true" : undefined}
        title={part.path}
      >
        {part.path}
      </span>
    );
  }

  if (part.type === "url") {
    if (isExternalUrl(part.url)) {
      return (
        <a
          className="inline-flex min-w-0 items-center gap-1 truncate underline decoration-dotted underline-offset-2 transition hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-teal/45"
          data-tool-row-url-link="true"
          href={part.url}
          onClick={stopRowActivation}
          onKeyDown={stopRowActivation}
          rel="noreferrer noopener"
          target="_blank"
          title={part.url}
        >
          <span className="min-w-0 truncate">{part.url}</span>
          <ExternalLinkIcon size={11} className="shrink-0" />
        </a>
      );
    }
    return (
      <span className="min-w-0 truncate font-mono" title={part.url}>
        {part.url}
      </span>
    );
  }

  if (part.type === "diff") {
    return (
      <span className="flex shrink-0 items-center gap-1.5 font-mono">
        {part.additions !== undefined ? (
          <span className="text-accent-teal" data-tool-row-diff-additions={String(part.additions)}>
            {t("panels.messages.toolRow.diffAdditions", { value: String(part.additions) })}
          </span>
        ) : null}
        {part.removals !== undefined ? (
          <span className="text-accent-gold" data-tool-row-diff-removals={String(part.removals)}>
            {t("panels.messages.toolRow.diffRemovals", { value: String(part.removals) })}
          </span>
        ) : null}
      </span>
    );
  }

  if (part.type === "exitCode") {
    return (
      <span className="shrink-0 font-mono text-accent-gold" data-tool-row-exit-code={String(part.code)}>
        {t("panels.messages.toolRow.exitCode", { code: String(part.code) })}
      </span>
    );
  }

  return (
    <span className={cn("min-w-0 truncate", mono && "font-mono")} title={part.text}>
      {part.text}
    </span>
  );
}
