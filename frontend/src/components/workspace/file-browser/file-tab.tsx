// 文件管理器「文件页签」正文（P5）：把预览从右侧栏底部的_小窗_升级成文件管理器里的_独立页签_。
// 与旧 `preview-pane.tsx` 同源：同一份 `/fs/preview` 契约与正文渲染，只是宿主从「选中即看」变成
// 「点开即签」——请求身份 = 页签快照（scope+path），切作用域不清、重复打开不重取。
//
// 归一化纪律 / 竞态纪律（照 hooks/workspace/use-file-preview.ts 与旧 preview-pane）：
//   * 请求序号 + AbortController：只有最新一次结果写回，切页签 / 卸载中止在途请求；
//   * 主动取消（AbortError）不落 error 态，避免把「用户切了页签」显示成失败；
//   * 结果与「请求身份」绑定（requestKey 校验），陈旧结果不写状态。
//
// 降级判据（分流，禁止类型伪装）：
//   * text + .md → 与聊天/文件预览弹层共用 MessageMarkdown 渲染（同一套标题/列表/表格/代码块样式，
//     图片与链接沿用白名单策略），并提供「Markdown / 文本」切换开关；默认渲染，可切回带行号的原文；
//   * text 其它 → TextViewer（行号 + 仅可视行高亮）；**SVG 不内联注入**，改为文本视图；
//   * image → `data:${mime};base64,…`；binary / too_large / unknown → 只给大小、原因与下载入口。

import { useEffect, useMemo, useRef, useState } from "react";
import { AlertTriangleIcon, DownloadIcon, FileWarningIcon, ImageIcon, LoaderCircleIcon, RotateCwIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { fetchFsPreview, isFsPreviewUnavailable } from "@/api/runtime/fs-preview";
import { RuntimeApiError } from "@/api/runtime/shared";
import { ExpandedPreviewDialog, ExpandPreviewButton } from "@/components/workspace/expanded-preview";
import { FilePreviewMarkdownBody } from "@/components/workspace/file-preview/tabbed-body";
import { TextViewer } from "@/components/workspace/file-browser/text-viewer";
import { countPreviewLines, decodeFilePreview, formatByteSize } from "@/lib/file-preview/decode";
import { formatEntryMtime, guessPrismLanguage, isMarkdownPath, isSvgPath } from "@/lib/file-browser/path-utils";
import { cn } from "@/lib/utils";

import { type FileManagerFileTab } from "./file-manager-tabs";
import type { FsEntry, FsPreview } from "@/types/runtime/fs-browser";

export type FilePreviewState =
  | { status: "loading" }
  | { status: "ready"; preview: FsPreview }
  | { status: "error"; error: unknown };

/** Markdown 文件的正文视图：渲染后的 Markdown 与带行号的原始文本。 */
type FileTabViewMode = "markdown" | "text";

export type FileTabPaneProps = {
  /** 文件页签（含 scope/path 快照；正文按快照取数，不受外部作用域变化影响）。 */
  tab: FileManagerFileTab;
  /** 下载入口（错误 / 二进制 / 超限时给出）；entry 用页签打开时的快照。 */
  onDownload?: (entry: FsEntry) => void;
  className?: string;
};

export function FileTabPane({ className, onDownload, tab }: FileTabPaneProps) {
  const { t } = useTranslation("workspace");
  const [reloadToken, setReloadToken] = useState(0);
  const [expanded, setExpanded] = useState(false);
  /**
   * Markdown 文件的正文视图（默认渲染 Markdown，可切回原始文本）。视图跟随页签：
   * 换文件（tab.id 变化）时同步回到默认，避免上一份文件的「文本」态串到下一份。
   * 渲染期纠正而不是放进 effect：换页签后不能先按上一份文件的视图渲染一帧。
   */
  const [viewMode, setViewMode] = useState<FileTabViewMode>("markdown");
  const [viewModeTabId, setViewModeTabId] = useState(tab.id);
  if (viewModeTabId !== tab.id) {
    setViewModeTabId(tab.id);
    setViewMode("markdown");
  }
  // 结果与「请求身份」绑定：key 不匹配即视为陈旧结果（竞态与取消都不再写状态）。
  const [result, setResult] = useState<{ key: string; state: FilePreviewState } | null>(null);
  const activeRequestRef = useRef("");
  const requestKey = `${tab.scope}\u0000${tab.path}\u0000${reloadToken}`;

  useEffect(() => {
    activeRequestRef.current = requestKey;
    const controller = new AbortController();
    void fetchFsPreview({ scope: tab.scope, path: tab.path }, { signal: controller.signal })
      .then((preview) => {
        if (activeRequestRef.current === requestKey) {
          setResult({ key: requestKey, state: { status: "ready", preview } });
        }
      })
      .catch((error: unknown) => {
        if (activeRequestRef.current !== requestKey || isAbortError(error)) {
          return;
        }
        setResult({ key: requestKey, state: { status: "error", error } });
      });
    return () => controller.abort();
  }, [requestKey, tab.path, tab.scope]);

  // 状态在渲染期派生：结果对应当前请求 → 用它；否则还在途中。
  const state: FilePreviewState = useMemo(
    () => (result?.key === requestKey ? result.state : { status: "loading" }),
    [requestKey, result],
  );

  const errorMessage = useMemo(() => {
    if (state.status !== "error") {
      return "";
    }
    const error = state.error;
    if (error instanceof RuntimeApiError) {
      return `${error.status}`;
    }
    return error instanceof Error ? error.message : String(error);
  }, [state]);

  const activePreview = state.status === "ready" ? state.preview : null;
  /** 只有「Markdown 文本」给出视图切换：其余分支（图片/二进制/超限/非 Markdown 文本）没有第二种视图。 */
  const markdownFile =
    activePreview?.kind === "text" &&
    isMarkdownPath(activePreview.path || tab.path);
  const meta = tab.entry;
  // 页签里没有打开的条目可能已变化：大小/修改时间以「打开时的快照」为准，正文始终重新取数。
  const metaFacts = [
    meta.size >= 0 ? formatByteSize(meta.size) : "",
    meta.mtime > 0 ? formatEntryMtime(meta.mtime) : "",
  ]
    .filter((part) => part !== "")
    .join(" · ");

  /** 正文渲染：页签内联与放大面板共用同一份实现，放大只换容器尺寸（禁止二次实现）。 */
  const renderBody = (bodyClassName: string) => (
    <div className={bodyClassName} data-testid="file-browser-preview">
      {state.status === "loading" ? (
        <p className="inline-flex items-center gap-2 px-3 py-4 text-xs text-muted-foreground">
          <LoaderCircleIcon aria-hidden className="size-3.5 animate-spin" />
          {t("panels.fileBrowser.preview.loading")}
        </p>
      ) : state.status === "error" ? (
        <div className="grid gap-2 px-3 py-3 text-xs" data-testid="file-browser-preview-error">
          <p className="inline-flex items-center gap-2 text-accent-gold">
            <AlertTriangleIcon aria-hidden className="size-3.5" />
            {t("panels.fileBrowser.preview.error", { message: errorMessage })}
          </p>
          {isFsPreviewUnavailable(state.error) ? (
            <p className="text-muted-foreground">
              {t("panels.fileBrowser.preview.unavailable", { status: errorMessage })}
            </p>
          ) : null}
          {onDownload ? <DownloadEntryButton entry={meta} onDownload={onDownload} /> : null}
        </div>
      ) : activePreview ? (
        <PreviewBody
          entry={meta}
          onDownload={onDownload}
          preview={activePreview}
          tabPath={tab.path}
          viewMode={viewMode}
        />
      ) : null}
    </div>
  );

  return (
    <section
      aria-label={t("panels.fileBrowser.preview.ariaLabel")}
      className={cn("flex min-h-0 flex-col", className)}
      data-testid="file-manager-file-tab"
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border/60 px-2 py-1 app-text-11 text-muted-foreground">
        <span className="min-w-0 truncate font-mono text-foreground/80" title={tab.path}>
          {tab.path}
        </span>
        {metaFacts ? <span>{metaFacts}</span> : null}
        <span className="ml-auto flex shrink-0 items-center gap-1">
          {markdownFile ? (
            <MarkdownViewToggle
              onChange={setViewMode}
              testId="file-browser-preview-view"
              value={viewMode}
            />
          ) : null}
          {state.status === "error" ? (
            <button
              aria-label={t("panels.fileBrowser.preview.retry")}
              className="inline-flex items-center gap-1 rounded border border-border/60 px-1.5 py-0.5 text-foreground hover:bg-white/5"
              onClick={() => setReloadToken((value) => value + 1)}
              title={t("panels.fileBrowser.preview.retry")}
              type="button"
            >
              <RotateCwIcon aria-hidden className="size-3" />
              {t("panels.fileBrowser.preview.retry")}
            </button>
          ) : null}
          {/* 没有可预览目标（目录 / 无权限项）时不提供放大入口：点开必然是个空面板。 */}
          {canPreviewEntry(meta) ? (
            <ExpandPreviewButton
              label={t("panels.fileBrowser.preview.expand")}
              onClick={() => setExpanded(true)}
              testId="file-preview-expand"
            />
          ) : null}
        </span>
      </div>

      {renderBody("min-h-0 flex-1 overflow-hidden")}

      <ExpandedPreviewDialog
        actions={
          markdownFile ? (
            <MarkdownViewToggle
              onChange={setViewMode}
              testId="file-preview-expanded-view"
              value={viewMode}
            />
          ) : undefined
        }
        ariaLabel={t("panels.fileBrowser.preview.expand")}
        closeLabel={t("panels.preview.close")}
        eyebrow={t("panels.preview.eyebrow")}
        hint={t("panels.preview.hint")}
        onClose={() => setExpanded(false)}
        open={expanded && canPreviewEntry(meta)}
        subtitle={metaFacts}
        testId="file-preview-expanded"
        title={tab.path}
      >
        {renderBody("h-full")}
      </ExpandedPreviewDialog>
    </section>
  );
}

function PreviewBody({
  entry,
  onDownload,
  preview,
  tabPath,
  viewMode,
}: {
  entry: FsEntry;
  onDownload?: (entry: FsEntry) => void;
  preview: FsPreview;
  tabPath: string;
  viewMode: FileTabViewMode;
}) {
  const { t } = useTranslation("workspace");
  const truncatedNote = t("panels.fileBrowser.preview.truncated", {
    size: formatByteSize(preview.limitBytes && preview.limitBytes > 0 ? preview.limitBytes : preview.size),
  });
  const svgAsText = preview.kind === "image" && (isSvgPath(preview.path || tabPath) || preview.mime === "image/svg+xml");
  const markdown = preview.kind === "text" && isMarkdownPath(preview.path || tabPath);
  const svgText = useMemo(() => {
    if (!svgAsText || !preview.dataBase64) {
      return null;
    }
    const body = decodeFilePreview(preview.dataBase64);
    return body.kind === "text" ? body.text : null;
  }, [preview.dataBase64, svgAsText]);

  if (preview.kind === "text" && markdown && viewMode === "markdown") {
    return (
      <div
        className="app-scrollbar h-full overflow-auto px-3 py-2.5 text-sm leading-6"
        data-testid="file-browser-preview-markdown"
      >
        {/* 与聊天 / 文件预览弹层共用同一份 Markdown 渲染（标题、列表、表格、代码块样式一致）。 */}
        <FilePreviewMarkdownBody text={preview.text ?? ""} />
        {preview.truncated ? <p className="mt-2 text-accent-gold">{truncatedNote}</p> : null}
      </div>
    );
  }

  if (preview.kind === "text") {
    return (
      <TextViewer
        className="h-full"
        language={guessPrismLanguage(preview.path || tabPath)}
        text={preview.text ?? ""}
        totalLines={countPreviewLines(preview.text ?? "")}
        truncated={preview.truncated}
        truncatedNote={truncatedNote}
      />
    );
  }

  if (preview.kind === "image" && !svgAsText) {
    return (
      <div className="app-scrollbar h-full overflow-auto px-3 py-2" data-testid="file-browser-preview-image">
        <img
          alt={entry.name}
          className="max-w-full rounded border border-border/60"
          src={`data:${preview.mime && preview.mime !== "" ? preview.mime : "image/png"};base64,${preview.dataBase64 ?? ""}`}
        />
        {preview.truncated ? <p className="mt-2 app-text-11 text-accent-gold">{truncatedNote}</p> : null}
      </div>
    );
  }

  if (svgAsText && svgText !== null) {
    return (
      <div className="flex h-full flex-col">
        <p className="border-b border-border/60 px-2 py-1 app-text-11 text-muted-foreground">
          {t("panels.fileBrowser.preview.svgAsText")}
        </p>
        <TextViewer className="min-h-0 flex-1" language="markup" text={svgText} />
      </div>
    );
  }

  const note =
    preview.kind === "too_large"
      ? t("panels.fileBrowser.preview.tooLarge", {
          size: formatByteSize(preview.size),
          limit: formatByteSize(preview.limitBytes && preview.limitBytes > 0 ? preview.limitBytes : preview.size),
        })
      : preview.kind === "binary"
        ? t("panels.fileBrowser.preview.binary")
        : preview.kind === "unknown"
          ? t("panels.fileBrowser.preview.unknownKind")
          : t("panels.fileBrowser.preview.binary");

  return (
    <div className="grid gap-2 px-3 py-3 text-xs" data-testid={`file-browser-preview-${preview.kind}`}>
      <p className="inline-flex items-center gap-2 text-muted-foreground">
        {preview.kind === "image" ? (
          <ImageIcon aria-hidden className="size-3.5" />
        ) : (
          <FileWarningIcon aria-hidden className="size-3.5" />
        )}
        {note}
      </p>
      <p className="text-muted-foreground">
        {t("panels.fileBrowser.preview.size")}: {formatByteSize(preview.size)}
      </p>
      {preview.reason ? (
        <p className="text-muted-foreground" data-testid="file-browser-preview-reason">
          {t("panels.fileBrowser.preview.reason", { reason: preview.reason })}
        </p>
      ) : null}
      {onDownload ? <DownloadEntryButton entry={entry} onDownload={onDownload} /> : null}
    </div>
  );
}

function DownloadEntryButton({ entry, onDownload }: { entry: FsEntry; onDownload: (entry: FsEntry) => void }) {
  const { t } = useTranslation("workspace");
  const label = t("panels.fileBrowser.preview.download", { name: entry.name });
  return (
    <button
      aria-label={label}
      className="inline-flex w-fit items-center gap-1.5 rounded border border-border/60 px-2 py-1 text-xs text-foreground hover:bg-white/5"
      onClick={() => onDownload(entry)}
      title={label}
      type="button"
    >
      <DownloadIcon aria-hidden className="size-3.5" />
      {label}
    </button>
  );
}

/**
 * 「Markdown / 文本」切换开关（只有 Markdown 文本预览出现）。
 *
 * 形态取分段按钮而不是页签：它切换的是**同一份正文的两种视图**，不新增面板，
 * 也不占用 aria tablist（页签条已经属于文件管理器）。
 * testId 由调用方给出：同一份视图在页签头部与放大面板头部各挂一次，
 * 两个实例需要可区分的定位锚点（行为与状态完全一致，切换即两边同步）。
 */
function MarkdownViewToggle({
  onChange,
  testId,
  value,
}: {
  onChange: (mode: FileTabViewMode) => void;
  testId: string;
  value: FileTabViewMode;
}) {
  const { t } = useTranslation("workspace");
  const options: Array<{ label: string; mode: FileTabViewMode }> = [
    { label: t("panels.fileBrowser.preview.viewMode.markdown"), mode: "markdown" },
    { label: t("panels.fileBrowser.preview.viewMode.text"), mode: "text" },
  ];
  return (
    <span
      aria-label={t("panels.fileBrowser.preview.viewMode.ariaLabel")}
      className="inline-flex shrink-0 items-center gap-0.5 rounded-control border border-border/60 p-0.5"
      data-testid={testId}
      role="group"
    >
      {options.map((option) => {
        const active = value === option.mode;
        return (
          <button
            aria-pressed={active}
            className={cn(
              "rounded-[4px] px-1.5 py-0.5 app-text-11 transition",
              active
                ? "bg-accent-gold/12 text-accent-gold"
                : "text-muted-foreground hover:bg-white/5 hover:text-foreground",
            )}
            data-testid={`${testId}-${option.mode}`}
            key={option.mode}
            onClick={() => onChange(option.mode)}
            type="button"
          >
            {option.label}
          </button>
        );
      })}
    </span>
  );
}

/** 主动取消（AbortController.abort）不落错误态；超时（TimeoutError）照常报错。 */
function canPreviewEntry(entry: FsEntry): boolean {
  return entry.type !== "dir" && entry.type !== "inaccessible";
}

function isAbortError(error: unknown): boolean {
  if (typeof DOMException !== "undefined" && error instanceof DOMException) {
    return error.name === "AbortError";
  }
  return error instanceof Error && error.name === "AbortError";
}