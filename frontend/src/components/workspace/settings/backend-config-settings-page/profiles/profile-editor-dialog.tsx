import { InfoIcon, RefreshCcwIcon, XIcon } from "lucide-react";
import { createPortal } from "react-dom";
import { useEffect, useId, useRef, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import { useFocusTrap } from "@/components/ui/use-focus-trap";
import { useFocusRestore } from "@/hooks/workspace/use-focus-restore";
import { cn } from "@/lib/utils";

type ProfileEditorDialogProps = {
  children: ReactNode;
  changeCount: number;
  dirty: boolean;
  footer: ReactNode;
  isBusy: boolean;
  onClose: () => void;
  onReload: () => void;
  profileRef: string;
  title: string;
};

export function ProfileEditorDialog({
  children,
  changeCount,
  dirty,
  footer,
  isBusy,
  onClose,
  onReload,
  profileRef,
  title,
}: ProfileEditorDialogProps) {
  const { t } = useTranslation("runtimeConfig");
  const panelRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  useDialogLifecycle(true, onClose);
  useFocusTrap(panelRef, true);
  useFocusRestore(true);
  useEffect(() => {
    panelRef.current?.querySelector<HTMLButtonElement>("button:not([disabled])")?.focus();
  }, []);

  return createPortal(
    <DialogOverlay className="z-[140] px-3 py-4 sm:px-5" onDismiss={onClose}>
      <DialogPanel
        aria-labelledby={titleId}
        aria-modal="true"
        className="max-w-6xl"
        data-profile-editor={profileRef}
        ref={panelRef}
        role="dialog"
      >
        <div className="flex shrink-0 items-start justify-between gap-3 border-b border-border px-3.5 py-3 sm:px-4">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <span className="inline-flex size-6 items-center justify-center rounded-field border border-border bg-surface-solid text-accent-primary">
              <InfoIcon size={13} />
            </span>
            <h2 className="text-base font-semibold text-foreground" id={titleId}>
              {title}
            </h2>
            <Badge className={cn("normal-case", dirty ? "text-accent-orange" : undefined)}>
              {dirty ? t("profiles.editor.changeCount", { count: changeCount }) : t("profiles.editor.noChanges")}
            </Badge>
          </div>
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            <Button disabled={isBusy} size="sm" type="button" variant="secondary" onClick={onReload}>
              <RefreshCcwIcon size={14} />
              {t("profiles.editor.reload")}
            </Button>
            <Button disabled={isBusy} size="sm" type="button" variant="ghost" onClick={onClose}>
              <XIcon size={14} />
              {t("profiles.editor.close")}
            </Button>
          </div>
        </div>
        <div className="min-h-0 flex-1 space-y-3 overflow-y-auto px-3.5 py-3 sm:px-4">
          {children}
        </div>
        <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-t border-border px-3.5 py-3 sm:px-4">
          {footer}
        </div>
      </DialogPanel>
    </DialogOverlay>,
    document.body,
  );
}
