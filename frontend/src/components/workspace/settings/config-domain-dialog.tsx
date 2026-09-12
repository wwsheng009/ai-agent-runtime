import { XIcon } from "lucide-react";
import { type ReactNode } from "react";
import { createPortal } from "react-dom";

import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";

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
  useDialogLifecycle(open, onClose);

  if (!open || typeof document === "undefined") {
    return null;
  }

  return createPortal(
    <DialogOverlay className="z-[140]" onDismiss={onClose}>
      <DialogPanel elevation="lg" className={widthClassName}>
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
      </DialogPanel>
    </DialogOverlay>,
    document.body,
  );
}
