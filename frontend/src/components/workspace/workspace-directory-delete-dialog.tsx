import { LoaderCircleIcon, TriangleAlertIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

type WorkspaceDirectoryDeleteDialogProps = {
  directory: {
    id: string;
    label: string;
    fullPath: string;
  } | null;
  onClose: () => void;
  /** Registry-only removal; bound sessions and server files are untouched. */
  onConfirm: (directoryId: string) => Promise<unknown>;
  open: boolean;
  sessionCount: number;
};

export function WorkspaceDirectoryDeleteDialog({
  directory,
  onClose,
  onConfirm,
  open,
  sessionCount,
}: WorkspaceDirectoryDeleteDialogProps) {
  const { t } = useTranslation("workspace");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open || typeof document === "undefined") {
      return;
    }

    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => {
      document.body.style.overflow = previousOverflow;
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [onClose, open]);

  if (!open || !directory || typeof document === "undefined") {
    return null;
  }

  async function handleConfirm() {
    if (!directory || submitting) {
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      await onConfirm(directory.id);
      onClose();
    } catch (confirmError) {
      setError(
        confirmError instanceof Error
          ? confirmError.message
          : String(confirmError),
      );
    } finally {
      setSubmitting(false);
    }
  }

  return createPortal(
    <div
      className="fixed inset-0 z-[120] flex items-center justify-center bg-[var(--dialog-backdrop)] px-3 py-4 backdrop-blur-sm"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onClose();
        }
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={t("sidebar.directories.deleteTitle")}
        className="w-full max-w-md overflow-hidden rounded-[0.9rem] border border-[var(--border)] [background:var(--dialog-bg)] shadow-[0_12px_36px_rgba(0,0,0,0.22)]"
      >
        <div className="px-4 py-4">
          <h2 className="text-lg font-semibold tracking-[-0.03em] text-[var(--foreground)]">
            {t("sidebar.directories.deleteTitle")}
          </h2>
          <p className="mt-2 text-sm font-medium text-[var(--foreground)]">
            {directory.label}
          </p>
          <p className="truncate text-xs leading-5 text-[var(--muted-foreground)]" title={directory.fullPath}>
            {directory.fullPath}
          </p>
          <p className="mt-3 rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3 py-2 text-sm leading-6 text-[var(--muted-foreground)]">
            {t("sidebar.directories.deleteConfirm", { count: sessionCount })}
          </p>
          <p className="mt-2 inline-flex items-center gap-1.5 text-xs leading-5 text-[var(--muted-foreground)]">
            <TriangleAlertIcon size={13} className="text-[#f0c77b]" />
            {t("sidebar.directories.deleteHint")}
          </p>

          {error ? (
            <div className="mt-3 rounded-[0.7rem] border border-[#f59e7d]/24 bg-[#f59e7d]/10 px-3 py-2 text-sm leading-6 text-[#f59e7d]">
              {error}
            </div>
          ) : null}

          <div className="mt-4 flex items-center justify-end gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={onClose}
              disabled={submitting}
            >
              {t("sidebar.directories.cancel")}
            </Button>
            <Button
              variant="destructive"
              size="sm"
              onClick={handleConfirm}
              disabled={submitting}
            >
              {submitting ? (
                <LoaderCircleIcon size={14} className="animate-spin" />
              ) : null}
              {t("sidebar.directories.deleteConfirmButton")}
            </Button>
          </div>
        </div>
      </div>
    </div>,
    document.body,
  );
}
