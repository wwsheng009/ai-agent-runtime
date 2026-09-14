// P2-1A：运行时文件预览弹层（只读，POST /api/runtime/fs/read-file）。
//
// 唯一内容来源是运行时进程返回的字节：空文件 / 二进制 / 超限 / 读失败都如实呈现，
// 不用本地缓存或占位文本兜底。交互：Esc / 遮罩点击关闭，关闭后焦点回到触发元素，
// 关闭时中止在途请求（由 use-file-preview 收口）。

import { FileWarningIcon, LoaderCircleIcon, RefreshCwIcon, XIcon } from "lucide-react";
import { type TFunction } from "i18next";
import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { createPortal } from "react-dom";

import { FILE_PREVIEW_MAX_BYTES } from "@/api/runtime/files";
import { RuntimeApiError } from "@/api/runtime/shared";
import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import { useFocusRestore } from "@/hooks/workspace/use-focus-restore";
import type { UseFilePreviewResult } from "@/hooks/workspace/use-file-preview";
import { formatByteSize } from "@/lib/file-preview/decode";

export type FilePreviewDialogProps = {
  preview: UseFilePreviewResult;
};

export function FilePreviewDialog({ preview }: FilePreviewDialogProps) {
  const { t } = useTranslation("workspace");
  const open = preview.status !== "closed";
  const panelRef = useRef<HTMLDivElement | null>(null);

  useDialogLifecycle(open, preview.close);
  useFocusRestore(open);

  // 模态焦点收口：打开时把焦点移进面板。
  // 触发元素（工具行文件链接）自身会吞掉 keydown 冒泡，焦点留在外面会让 Esc 失效；
  // 关闭时由 useFocusRestore 把焦点还给触发元素。
  useEffect(() => {
    if (!open) {
      return;
    }
    panelRef.current?.focus();
  }, [open]);

  if (!open) {
    return null;
  }

  const { body, result } = preview;

  return createPortal(
    <DialogOverlay onDismiss={preview.close}>
      <DialogPanel
        aria-label={t("panels.filePreview.ariaLabel")}
        aria-modal="true"
        className="max-w-4xl"
        data-testid="file-preview-dialog"
        ref={panelRef}
        role="dialog"
        tabIndex={-1}
      >
        <div className="flex items-start justify-between gap-3 border-b border-border px-3.5 py-3 sm:px-4">
          <div className="min-w-0">
            <div className="app-text-11 uppercase tracking-[0.16em] text-accent-secondary">
              {t("panels.filePreview.eyebrow")}
            </div>
            <h2 className="mt-1 truncate text-lg font-semibold tracking-[-0.03em] text-foreground">
              {basename(preview.requestedPath)}
            </h2>
            <p className="mt-1 truncate font-mono app-text-11 text-muted-foreground">
              {preview.requestedPath}
            </p>
            <p className="mt-1 max-w-2xl text-sm leading-6 text-muted-foreground">
              {t("panels.filePreview.description")}
            </p>
          </div>
          <Button
            aria-label={t("panels.filePreview.close")}
            onClick={preview.close}
            size="icon"
            title={t("panels.filePreview.close")}
            variant="ghost"
          >
            <XIcon size={16} />
          </Button>
        </div>

        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-b border-border px-3.5 py-2 app-text-11 text-muted-foreground sm:px-4">
          {result ? (
            <span className="truncate" data-testid="file-preview-resolved-path">
              <span className="text-foreground/70">
                {t("panels.filePreview.meta.resolvedPath")}:
              </span>{" "}
              <span className="font-mono">{result.path}</span>
            </span>
          ) : null}
          {result ? (
            <span data-testid="file-preview-bytes">
              {t("panels.filePreview.meta.bytes", { count: result.byteCount })}
              {result.byteCount > 0
                ? ` · ${t("panels.filePreview.meta.size", {
                    size: formatByteSize(result.byteCount),
                  })}`
                : ""}
            </span>
          ) : null}
          {body?.kind === "text" ? (
            <span data-testid="file-preview-lines">
              {t("panels.filePreview.meta.lines", { count: body.lineCount })}
            </span>
          ) : null}
        </div>

        <div className="min-h-0 flex-1 overflow-auto px-3.5 py-3.5 sm:px-4">
          {preview.status === "loading" ? (
            <div
              className="flex items-center gap-2 rounded-card border border-border bg-surface-softer px-2.5 py-2 text-xs text-muted-foreground"
              data-testid="file-preview-loading"
            >
              <LoaderCircleIcon size={13} className="animate-spin" />
              {t("panels.filePreview.body.loading")}
            </div>
          ) : null}

          {preview.status === "error" ? (
            <div
              className="rounded-card border border-accent-gold/24 bg-accent-gold/10 px-2.5 py-2 text-xs text-foreground"
              data-testid="file-preview-error"
              role="alert"
            >
              <div className="flex items-center gap-2 font-medium text-accent-gold">
                <FileWarningIcon size={13} />
                {t("panels.filePreview.error.title")}
              </div>
              <p className="mt-1 leading-5">{errorMessage(preview, t)}</p>
              <button
                className="mt-2 inline-flex items-center gap-1.5 font-medium text-foreground underline underline-offset-2"
                onClick={preview.retry}
                type="button"
              >
                <RefreshCwIcon size={12} />
                {t("panels.filePreview.error.retry")}
              </button>
            </div>
          ) : null}

          {preview.tooLarge && result ? (
            <div
              className="rounded-card border border-accent-gold/24 bg-accent-gold/10 px-2.5 py-2 text-xs leading-5 text-foreground"
              data-testid="file-preview-too-large"
            >
              {t("panels.filePreview.body.tooLarge", {
                size: formatByteSize(result.byteCount),
                limit: formatByteSize(FILE_PREVIEW_MAX_BYTES),
              })}
            </div>
          ) : null}

          {body?.kind === "empty" ? (
            <div
              className="rounded-card border border-border bg-surface-softer px-2.5 py-2 text-xs text-muted-foreground"
              data-testid="file-preview-empty"
            >
              {t("panels.filePreview.body.empty")}
            </div>
          ) : null}

          {body?.kind === "binary" ? (
            <div
              className="rounded-card border border-border bg-surface-softer px-2.5 py-2 text-xs text-muted-foreground"
              data-testid="file-preview-binary"
            >
              {t("panels.filePreview.body.binary", {
                reason: t(
                  body.reason === "nul-byte"
                    ? "panels.filePreview.body.binaryNul"
                    : "panels.filePreview.body.binaryUtf8",
                ),
              })}
            </div>
          ) : null}

          {body?.kind === "text" ? (
            <pre
              className="max-h-[60vh] overflow-auto rounded-card border border-border bg-surface-solid px-3 py-2.5 font-mono app-text-12 leading-5 text-foreground"
              data-testid="file-preview-text"
            >
              {body.text}
            </pre>
          ) : null}
        </div>
      </DialogPanel>
    </DialogOverlay>,
    document.body,
  );
}

function errorMessage(
  preview: UseFilePreviewResult,
  t: TFunction<"workspace">,
): string {
  if (preview.unavailable) {
    const status = preview.error instanceof RuntimeApiError ? preview.error.status : null;
    return status === null
      ? t("panels.filePreview.error.unavailableUnknown")
      : t("panels.filePreview.error.unavailable", { status: String(status) });
  }
  if (preview.error instanceof Error && preview.error.message.trim()) {
    return preview.error.message.trim();
  }
  return t("panels.filePreview.error.title");
}

function basename(path: string): string {
  const normalized = path.trim().replace(/\\/g, "/");
  const segments = normalized.split("/").filter(Boolean);
  return segments.at(-1) ?? normalized;
}
