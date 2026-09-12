import { useEffect } from "react";

// P0-3：设置域对话框共享生命周期（body 滚动锁定 + Esc 关闭）。
// 原先 settings-dialog 与 config-domain-dialog 各自重复两份同构 effect。
export function useDialogLifecycle(open: boolean, onClose: () => void) {
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
}
