import { ArrowRightLeftIcon } from "lucide-react";
import { useEffect, useId } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

type SessionSwitchConfirmDialogProps = {
  onCancel: () => void;
  onConfirm: () => void;
  open: boolean;
  /** 目标会话标题；未选中目标时为「会话」。 */
  sessionTitle: string;
};

// 左侧会话列表点击切换前的二次确认：只在当前会话仍在生成回复时弹出
// （门控在 workspace-page 的 shouldConfirmThreadSwitch），点「切换」才真正
// 导航到目标会话（轨迹重置也只发生在确认之后）。
export function SessionSwitchConfirmDialog({
  onCancel,
  onConfirm,
  open,
  sessionTitle,
}: SessionSwitchConfirmDialogProps) {
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
            className="text-lg font-semibold tracking-[-0.03em] text-foreground"
            id={titleId}
          >
            {t("sidebar.session.switchTitle")}
          </h2>
          <p className="mt-3 rounded-field border border-border bg-surface-softer px-3 py-2 text-sm leading-6 text-foreground">
            {t("sidebar.session.switchMessage", { title: sessionTitle })}
          </p>
          <p className="mt-2 inline-flex items-center gap-1.5 text-xs leading-5 text-muted-foreground">
            <ArrowRightLeftIcon size={13} className="text-accent-teal" />
            {t("sidebar.session.switchHint")}
          </p>

          <div className="mt-4 flex items-center justify-end gap-2">
            <Button variant="ghost" size="sm" onClick={onCancel}>
              {t("sidebar.session.switchCancel")}
            </Button>
            <Button
              autoFocus
              size="sm"
              type="button"
              variant="primary"
              onClick={onConfirm}
            >
              {t("sidebar.session.switchConfirmButton")}
            </Button>
          </div>
        </div>
      </div>
    </div>,
    document.body,
  );
}
