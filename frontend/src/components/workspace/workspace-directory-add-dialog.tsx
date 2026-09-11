import { LoaderCircleIcon } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

type WorkspaceDirectoryAddDialogProps = {
  onClose: () => void;
  /** Backend addDirectory; failures surface as an inline form error. */
  onAdd: (path: string, name?: string) => Promise<unknown>;
  open: boolean;
};

export function WorkspaceDirectoryAddDialog({
  onClose,
  onAdd,
  open,
}: WorkspaceDirectoryAddDialogProps) {
  const { t } = useTranslation("workspace");
  const [path, setPath] = useState("");
  const [name, setName] = useState("");
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

  if (!open || typeof document === "undefined") {
    return null;
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmedPath = path.trim();
    if (!trimmedPath || submitting) {
      return;
    }

    setSubmitting(true);
    setError(null);
    try {
      await onAdd(trimmedPath, name.trim() || undefined);
      setPath("");
      setName("");
      onClose();
    } catch (submitError) {
      setError(
        submitError instanceof Error
          ? submitError.message
          : String(submitError),
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
        aria-label={t("sidebar.directories.addTitle")}
        className="w-full max-w-md overflow-hidden rounded-[0.9rem] border border-[var(--border)] [background:var(--dialog-bg)] shadow-[0_12px_36px_rgba(0,0,0,0.22)]"
      >
        <form onSubmit={handleSubmit} className="px-4 py-4">
          <h2 className="text-lg font-semibold tracking-[-0.03em] text-[var(--foreground)]">
            {t("sidebar.directories.addTitle")}
          </h2>
          <p className="mt-1 text-sm leading-6 text-[var(--muted-foreground)]">
            {t("sidebar.directories.addHint")}
          </p>

          <label className="mt-4 block">
            <span className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              {t("sidebar.directories.pathLabel")}
            </span>
            <input
              autoFocus
              required
              value={path}
              onChange={(event) => setPath(event.target.value)}
              placeholder={t("sidebar.directories.pathPlaceholder")}
              spellCheck={false}
              className="mt-1.5 w-full rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2 text-sm text-[var(--foreground)] outline-none transition focus:border-[var(--accent-primary-border)]"
            />
          </label>

          <label className="mt-3 block">
            <span className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              {t("sidebar.directories.nameLabel")}
            </span>
            <input
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder={t("sidebar.directories.namePlaceholder")}
              className="mt-1.5 w-full rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2 text-sm text-[var(--foreground)] outline-none transition focus:border-[var(--accent-primary-border)]"
            />
          </label>

          {error ? (
            <div className="mt-3 rounded-[0.7rem] border border-[#f59e7d]/24 bg-[#f59e7d]/10 px-3 py-2 text-sm leading-6 text-[#f59e7d]">
              {error}
            </div>
          ) : null}

          <div className="mt-4 flex items-center justify-end gap-2">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={onClose}
              disabled={submitting}
            >
              {t("sidebar.directories.cancel")}
            </Button>
            <Button type="submit" size="sm" disabled={submitting || !path.trim()}>
              {submitting ? (
                <LoaderCircleIcon size={14} className="animate-spin" />
              ) : null}
              {t("sidebar.directories.add")}
            </Button>
          </div>
        </form>
      </div>
    </div>,
    document.body,
  );
}
