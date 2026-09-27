import { ShieldAlertIcon } from "lucide-react";
import { useEffect, useId } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

type PermissionModeConfirmDialogProps = {
  onCancel: () => void;
  onConfirm: () => void;
  open: boolean;
  /** 待切换的危险模式展示名（已走 i18n 或后端 label）。 */
  modeLabel: string;
};

// composer 权限选择器的危险模式二次确认：只在选中的模式带
// `dangerous: true`（后端 supported_modes，即 bypass_permissions）时弹出；
// 点「确认」才会带 confirm: true 调切换接口，取消则不发请求、快照选择不变。
export function PermissionModeConfirmDialog({
  onCancel,
  onConfirm,
  open,
  modeLabel,
}: PermissionModeConfirmDialogProps) {
  const { t } = useTranslation("workspace");
  const titleId = useId();

  useEffect(() => {
    if (!open || typeof document === "undefined") {
      return;
    }

    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onCancel();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => {
      document.body.style.overflow = previousOverflow;
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [onCancel, open]);

  if (!open || typeof document === "undefined") {
    return null;
  }

  return createPortal(
    <div
      className="fixed inset-0 z-[120] flex items-center justify-center bg-dialog-backdrop px-3 py-4 backdrop-blur-sm"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onCancel();
        }
      }}
    >
      <div
        aria-labelledby={titleId}
        aria-modal="true"
        role="dialog"
        className="w-full max-w-md overflow-hidden rounded-panel border border-border [background:var(--dialog-bg)] shadow-[0_12px_36px_rgba(0,0,0,0.22)]"
      >
        <div className="px-4 py-4">
          <h2
            className="flex items-center gap-2 text-lg font-semibold tracking-[-0.03em] text-foreground"
            id={titleId}
          >
            <ShieldAlertIcon size={16} className="shrink-0 text-accent-gold" />
            {t("composer.permission.confirmDialog.title")}
          </h2>
          <p className="mt-3 rounded-field border border-border bg-surface-softer px-3 py-2 text-sm leading-6 text-foreground">
            {t("composer.permission.confirmDialog.message", {
              mode: modeLabel,
            })}
          </p>

          <div className="mt-4 flex items-center justify-end gap-2">
            <Button variant="ghost" size="sm" onClick={onCancel}>
              {t("composer.permission.confirmDialog.cancel")}
            </Button>
            <Button
              autoFocus
              size="sm"
              type="button"
              variant="primary"
              onClick={onConfirm}
            >
              {t("composer.permission.confirmDialog.confirm")}
            </Button>
          </div>
        </div>
      </div>
    </div>,
    document.body,
  );
}
