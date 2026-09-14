// P2-1A：弹层关闭后把焦点还给触发元素（配合 useDialogLifecycle 的 Esc 关闭）。
// 打开时记录 activeElement，关闭/卸载时恢复；触发元素已从 DOM 移除时静默跳过。

import { useEffect, useRef } from "react";

export function useFocusRestore(open: boolean) {
  const previouslyFocused = useRef<HTMLElement | null>(null);

  useEffect(() => {
    if (!open || typeof document === "undefined") {
      return;
    }

    previouslyFocused.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;

    return () => {
      const target = previouslyFocused.current;
      previouslyFocused.current = null;
      if (target && typeof target.focus === "function") {
        target.focus();
      }
    };
  }, [open]);
}
