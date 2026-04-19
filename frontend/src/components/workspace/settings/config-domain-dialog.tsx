import { XIcon } from "lucide-react";
import { useEffect, type ReactNode } from "react";
import { createPortal } from "react-dom";

import { Button } from "@/components/ui/button";

type ConfigDomainDialogProps = {
  children: ReactNode;
  description?: string;
  footer?: ReactNode;
  onClose: () => void;
  open: boolean;
  title: string;
  widthClassName?: string;
};

export function ConfigDomainDialog({
  children,
  description,
  footer,
  onClose,
  open,
  title,
  widthClassName = "max-w-5xl",
}: ConfigDomainDialogProps) {
  useEffect(() => {
    if (!open || typeof document === "undefined") {
      return;
    }

    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    return () => {
      document.body.style.overflow = previousOverflow;
    };
  }, [open]);

  useEffect(() => {
    if (!open) {
      return;
    }

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [onClose, open]);

  if (!open || typeof document === "undefined") {
    return null;
  }

  return createPortal(
    <div
      className="fixed inset-0 z-[140] flex items-center justify-center bg-[var(--dialog-backdrop)] px-3 py-4"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onClose();
        }
      }}
    >
      <div
        className={`flex max-h-[calc(100vh-1.5rem)] w-full flex-col overflow-hidden rounded-[0.9rem] border border-[var(--border)] [background:var(--dialog-bg)] shadow-[0_14px_36px_rgba(0,0,0,0.2)] ${widthClassName}`}
      >
        <div className="flex items-start justify-between gap-4 border-b border-[var(--border)] px-3 py-3 sm:px-4">
          <div>
            <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--accent-secondary)]">
              Config editor
            </div>
            <h2 className="mt-1 text-base font-semibold tracking-[-0.02em] text-[var(--foreground)]">
              {title}
            </h2>
            {description ? (
              <p className="mt-1 max-w-3xl text-sm leading-6 text-[var(--muted-foreground)]">
                {description}
              </p>
            ) : null}
          </div>
          <Button variant="ghost" size="icon" onClick={onClose} aria-label={`关闭${title}`}>
            <XIcon size={16} />
          </Button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto px-3 py-3 sm:px-4">{children}</div>

        {footer ? (
          <div className="border-t border-[var(--border)] px-3 py-3 sm:px-4">{footer}</div>
        ) : null}
      </div>
    </div>,
    document.body,
  );
}
