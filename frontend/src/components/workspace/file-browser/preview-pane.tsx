// 工作区右侧栏「文件浏览器面」→ 预览面板（P2-3，分流口径见规划文档 §4.5）。
//
// 后端契约：`GET /api/runtime/fs/preview`（`FsPreview`，见 api/runtime/fs-preview.ts）：
//   kind = text | image | binary | too_large | unknown；text 给 `text`，image 给 `dataBase64`，
//   binary/too_large 给 `reason`（nul-byte / invalid-utf8 / too_large…）与 `limitBytes`。
//
// 归一化纪律 / 竞态纪律（照 hooks/workspace/use-file-preview.ts）：
//   * 请求序号 + AbortController：只有最新一次结果写回；切换选中文件或卸载时中止在途请求；
//   * 主动取消（AbortError）**不落 error 态**，避免把「用户换了个文件」显示成失败；
//   * 后端没给的字段不猜：`reason`/`limitBytes` 缺失时只说「未返回内容」，不编造原因。
//
// 降级判据（分流，禁止类型伪装）：
//   * text + .md → react-markdown 渲染（`img` 被改写为 alt 文本，不加载外部资源）；
//   * text 其它 → TextViewer（行号 + 仅可视行高亮）；**SVG 不内联注入**，改为文本视图；
//   * image → `data:${mime};base64,…`；binary / too_large / unknown → 只给大小、原因与下载入口。
import { useEffect, useMemo, useRef, useState } from "react";
import { AlertTriangleIcon, DownloadIcon, FileWarningIcon, ImageIcon, LoaderCircleIcon, RotateCwIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

import { fetchFsPreview, isFsPreviewUnavailable } from "@/api/runtime/fs-preview";
import { TextViewer } from "@/components/workspace/file-browser/text-viewer";
import {
  ExpandedPreviewDialog,
  ExpandPreviewButton,
} from "@/components/workspace/expanded-preview";
import { isAbortError } from "@/hooks/workspace/use-file-browser";
import { RuntimeApiError } from "@/api/runtime/shared";
import { countPreviewLines, decodeFilePreview, formatByteSize } from "@/lib/file-preview/decode";
import { formatEntryMtime, guessPrismLanguage, isMarkdownPath, isSvgPath } from "@/lib/file-browser/path-utils";
import { cn } from "@/lib/utils";

import type { FsEntry, FsPreview } from "@/types/runtime/fs-browser";

export type PreviewState =
  | { status: "idle" }
  | { status: "loading"; path: string }
  | { status: "ready"; path: string; preview: FsPreview }
  | { status: "error"; path: string; error: unknown };

/** 是否值得向后端要预览：目录/无权限项直接跳过（预览是文件级能力）。 */
function shouldPreviewEntry(entry: FsEntry | null): boolean {
  return entry !== null && entry.type !== "dir" && entry.type !== "inaccessible";
}

export type PreviewPaneProps = {
  scope: string;
  entry: FsEntry | null;
  onDownload: (entry: FsEntry) => void;
  className?: string;
};

export function PreviewPane({ className, entry, onDownload, scope }: PreviewPaneProps) {
  const { t } = useTranslation("workspace");
  const [reloadToken, setReloadToken] = useState(0);
  /** 放大面板开关：小窗口与放大面板共用同一份取数与正文渲染（放大不重复请求）。 */
  const [expanded, setExpanded] = useState(false);
  // 结果与「请求身份」绑定：key 不匹配即视为陈旧结果（竞态与取消都不需要再写状态）。
  const [result, setResult] = useState<{ key: string; state: PreviewState } | null>(null);
  const activeRequestRef = useRef("");
  const targetPath = shouldPreviewEntry(entry) ? (entry?.path ?? "") : "";
  const requestKey = targetPath && scope.trim() ? `${scope}\u0000${targetPath}\u0000${reloadToken}` : "";

  useEffect(() => {
    activeRequestRef.current = requestKey;
    if (!requestKey) {
      return;
    }
    const controller = new AbortController();
    void fetchFsPreview({ scope, path: targetPath }, { signal: controller.signal })
      .then((preview) => {
        if (activeRequestRef.current === requestKey) {
          setResult({ key: requestKey, state: { status: "ready", path: targetPath, preview } });
        }
      })
      .catch((error: unknown) => {
        if (activeRequestRef.current !== requestKey || isAbortError(error)) {
          return;
        }
        setResult({ key: requestKey, state: { status: "error", path: targetPath, error } });
      });
    return () => controller.abort();
  }, [requestKey, scope, targetPath]);

  // 状态在渲染期派生（不在 effect 里同步 setState）：无请求 → idle；结果对应当前请求 → 用它；否则还在途中。
  // 用 useMemo 收口：下游 useMemo 依赖 `state`，否则每次渲染都是新对象、依赖恒变（exhaustive-deps 会告警）。
  const state: PreviewState = useMemo(
    () =>
      !requestKey
        ? { status: "idle" }
        : result?.key === requestKey
          ? result.state
          : { status: "loading", path: targetPath },
    [requestKey, result, targetPath],
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

  const meta = entry;
  const activePreview = state.status === "ready" && state.path === targetPath ? state.preview : null;

  /** 放大面板头部副标题：小窗口头部已展示的次要信息（大小 / 修改时间），不另造口径。 */
  const metaFacts = meta
    ? [formatByteSize(meta.size), meta.mtime > 0 ? formatEntryMtime(meta.mtime) : ""]
        .filter((part) => part !== "")
        .join(" · ")
    : "";

  // 正文按容器尺寸复用：右侧栏小窗口给 `flex-1`，放大面板给 `h-full`；分支与文案只有这一份，
  // 放大面板不重新取数（请求仍由上面这一个实例持有），因此不会出现两份不一致的预览。
  const renderBody = (wrapperClassName: string) => (
    <div className={wrapperClassName}>
      {!meta ? (
        <p className="px-3 py-4 text-xs text-muted-foreground">{t("panels.fileBrowser.preview.empty")}</p>
      ) : state.status === "loading" ? (
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
          <DownloadEntryButton entry={meta} onDownload={onDownload} />
        </div>
      ) : activePreview ? (
        <PreviewBody entry={meta} onDownload={onDownload} preview={activePreview} />
      ) : null}
    </div>
  );

  return (
    <section
      aria-label={t("panels.fileBrowser.preview.ariaLabel")}
      className={cn("flex min-h-0 flex-col", className)}
      data-testid="file-browser-preview"
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border/60 px-2 py-1 text-[11px] text-muted-foreground">
        {meta ? (
          <>
            <span className="truncate font-mono text-foreground/80" title={meta.path}>
              {meta.path}
            </span>
            <span>{formatByteSize(meta.size)}</span>
            {meta.mtime > 0 ? <span>{formatEntryMtime(meta.mtime)}</span> : null}
          </>
        ) : (
          <span>{t("panels.fileBrowser.preview.empty")}</span>
        )}
        {meta ? (
          <span className="ml-auto flex shrink-0 items-center gap-1">
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
            {shouldPreviewEntry(meta) ? (
              <ExpandPreviewButton
                label={t("panels.fileBrowser.preview.expand")}
                onClick={() => setExpanded(true)}
                testId="file-preview-expand"
              />
            ) : null}
          </span>
        ) : null}
      </div>

      {renderBody("min-h-0 flex-1 overflow-hidden")}

      <ExpandedPreviewDialog
        ariaLabel={t("panels.fileBrowser.preview.expand")}
        closeLabel={t("panels.preview.close")}
        eyebrow={t("panels.preview.eyebrow")}
        hint={t("panels.preview.hint")}
        onClose={() => setExpanded(false)}
        open={expanded && meta !== null}
        subtitle={metaFacts}
        testId="file-preview-expanded"
        title={meta?.path ?? ""}
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
}: {
  entry: FsEntry;
  onDownload: (entry: FsEntry) => void;
  preview: FsPreview;
}) {
  const { t } = useTranslation("workspace");
  const truncatedNote = t("panels.fileBrowser.preview.truncated", {
    size: formatByteSize(preview.limitBytes && preview.limitBytes > 0 ? preview.limitBytes : preview.size),
  });
  const svgAsText = preview.kind === "image" && (isSvgPath(preview.path || entry.path) || preview.mime === "image/svg+xml");
  const markdown = preview.kind === "text" && isMarkdownPath(preview.path || entry.path);
  const svgText = useMemo(() => {
    if (!svgAsText || !preview.dataBase64) {
      return null;
    }
    const body = decodeFilePreview(preview.dataBase64);
    return body.kind === "text" ? body.text : null;
  }, [preview.dataBase64, svgAsText]);

  if (preview.kind === "text" && markdown) {
    return (
      <div className="app-scrollbar h-full overflow-auto px-3 py-2 text-xs leading-5" data-testid="file-browser-preview-markdown">
        <ReactMarkdown
          components={{
            a: ({ children, ...anchorProps }) => (
              <a {...anchorProps} rel="noreferrer noopener" target="_blank">
                {children}
              </a>
            ),
            img: ({ alt }) => <span className="text-muted-foreground">{alt ?? ""}</span>,
          }}
          remarkPlugins={[remarkGfm]}
        >
          {preview.text ?? ""}
        </ReactMarkdown>
        {preview.truncated ? <p className="mt-2 text-accent-gold">{truncatedNote}</p> : null}
      </div>
    );
  }

  if (preview.kind === "text") {
    return (
      <TextViewer
        className="h-full"
        language={guessPrismLanguage(preview.path || entry.path)}
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
        {preview.truncated ? <p className="mt-2 text-[11px] text-accent-gold">{truncatedNote}</p> : null}
      </div>
    );
  }

  if (svgAsText && svgText !== null) {
    return (
      <div className="flex h-full flex-col">
        <p className="border-b border-border/60 px-2 py-1 text-[11px] text-muted-foreground">
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
      <DownloadEntryButton entry={entry} onDownload={onDownload} />
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
