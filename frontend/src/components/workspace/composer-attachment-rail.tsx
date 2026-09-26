import { FileIcon, RotateCcwIcon, XIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  formatComposerAttachmentSize,
  type ComposerAttachment,
} from "@/lib/composer-attachments";
import { cn } from "@/lib/utils";

// P1-4 子片 2 / S5：附件草稿轨的展示层。
// 组件只渲染：数据、校验与上传状态由 `useComposerAttachments` 持有。
// 状态语义：`uploading`（在途）/ `uploaded`（服务端已给 path）/ `error`（可重试）。
// 失败原因优先展示后端原文；后端没给原因时按分类给本地化兜底，绝不显示「已上传」。

const STATUS_LABEL_KEY: Record<
  ComposerAttachment["status"],
  | "composer.attachments.uploading"
  | "composer.attachments.uploaded"
  | "composer.attachments.uploadFailed"
> = {
  uploading: "composer.attachments.uploading",
  uploaded: "composer.attachments.uploaded",
  error: "composer.attachments.uploadFailed",
};

export function ComposerAttachmentRail({
  attachments,
  isCompact,
  onRemove,
  onRetry,
}: {
  attachments: ComposerAttachment[];
  isCompact: boolean;
  onRemove: (id: string) => void;
  onRetry: (id: string) => void;
}) {
  const { t } = useTranslation("workspace");

  if (attachments.length === 0) {
    return null;
  }

  return (
    <ul
      data-composer-attachment-rail
      className={cn(
        "flex flex-wrap items-center gap-2 border-b border-border px-3",
        isCompact ? "py-1.5" : "py-2",
      )}
    >
      {attachments.map((item) => {
        const failureDetail =
          item.error && item.error.length > 0
            ? item.error
            : item.errorKind
              ? t(`composer.attachments.error.${item.errorKind}`)
              : "";
        return (
          <li
            key={item.id}
            data-composer-attachment
            data-attachment-kind={item.kind}
            data-attachment-status={item.status}
            className="inline-flex max-w-[15rem] items-center gap-2 rounded-[0.6rem] border border-border bg-surface-soft px-2 py-1"
          >
            {item.previewUrl ? (
              <img
                src={item.previewUrl}
                alt={t("composer.attachments.previewAlt", { name: item.name })}
                data-composer-attachment-preview
                className="size-8 shrink-0 rounded-[0.4rem] object-cover"
              />
            ) : (
              <FileIcon
                size={14}
                aria-hidden="true"
                className="shrink-0 text-muted-foreground"
              />
            )}
            <span className="min-w-0 flex-1">
              <span className="block truncate app-text-10 text-foreground">
                {item.name}
              </span>
              <span
                className={cn(
                  "block app-text-9 uppercase tracking-[0.12em]",
                  item.status === "error"
                    ? "text-accent-gold"
                    : "text-muted-foreground",
                )}
                {...(item.status === "uploaded" && item.remoteNote
                  ? { title: item.remoteNote }
                  : {})}
              >
                {formatComposerAttachmentSize(item.size)} ·{" "}
                {t(STATUS_LABEL_KEY[item.status])}
              </span>
              {item.status === "error" && failureDetail ? (
                <span
                  data-composer-attachment-error={item.errorKind ?? "failed"}
                  className="block truncate app-text-9 text-accent-gold"
                  title={failureDetail}
                >
                  {failureDetail}
                </span>
              ) : null}
            </span>
            {item.status === "error" ? (
              <button
                type="button"
                data-composer-attachment-retry={item.id}
                aria-label={t("composer.attachments.retry", { name: item.name })}
                title={t("composer.attachments.retry", { name: item.name })}
                onClick={() => onRetry(item.id)}
                className="shrink-0 rounded-[0.35rem] p-0.5 text-muted-foreground transition-colors hover:bg-surface-soft-hover hover:text-foreground"
              >
                <RotateCcwIcon size={12} aria-hidden="true" />
              </button>
            ) : null}
            <button
              type="button"
              data-composer-attachment-remove={item.id}
              aria-label={t("composer.attachments.remove", { name: item.name })}
              title={t("composer.attachments.remove", { name: item.name })}
              onClick={() => onRemove(item.id)}
              className="shrink-0 rounded-[0.35rem] p-0.5 text-muted-foreground transition-colors hover:bg-surface-soft-hover hover:text-foreground"
            >
              <XIcon size={12} aria-hidden="true" />
            </button>
          </li>
        );
      })}
    </ul>
  );
}

/** 全视口拖放邀请：拖拽文件进入窗口时覆盖显示，`pointer-events-none` 不拦截落点。 */
export function ComposerDropInvitation({ visible }: { visible: boolean }) {
  const { t } = useTranslation("workspace");

  if (!visible) {
    return null;
  }

  return (
    <div
      data-composer-drop-invitation
      aria-hidden="true"
      className="pointer-events-none fixed inset-0 z-[60] p-3"
    >
      <div className="flex h-full w-full items-center justify-center rounded-panel-lg border-2 border-dashed border-accent-secondary-border bg-[var(--workspace-composer-bg)]">
        <span className="app-text-11 uppercase tracking-[0.14em] text-foreground">
          {t("composer.attachments.dropInvitation")}
        </span>
      </div>
    </div>
  );
}
