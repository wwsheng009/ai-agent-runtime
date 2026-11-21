// 移动侧栏抽屉的焦点生命周期：打开时把焦点移到关闭按钮，Tab 在抽屉内循环，
// 关闭后把焦点还给触发元素。由 workspace-sidebar.tsx 机械拆分（P0-2），仅搬迁不改语义。

import { type RefObject, useEffect } from "react";

type UseSidebarMobileFocusTrapParams = {
  asideRef: RefObject<HTMLElement | null>;
  mobileOpen: boolean;
};

export function useSidebarMobileFocusTrap({
  asideRef,
  mobileOpen,
}: UseSidebarMobileFocusTrapParams) {
  useEffect(() => {
    if (!mobileOpen) {
      return;
    }
    const node = asideRef.current;
    if (!node) {
      return;
    }

    const previousFocus =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    const focusableSelector = [
      "button:not([disabled])",
      "a[href]",
      "input:not([disabled])",
      "select:not([disabled])",
      "textarea:not([disabled])",
      "[tabindex]:not([tabindex='-1'])",
    ].join(",");
    const getFocusable = () =>
      Array.from(node.querySelectorAll<HTMLElement>(focusableSelector)).filter(
        (element) =>
          element.getClientRects().length > 0 &&
          element.getAttribute("aria-hidden") !== "true",
      );
    const closeButton =
      node.querySelector<HTMLElement>("[data-sidebar-close]") ??
      getFocusable()[0];
    const focusFrame = requestAnimationFrame(() => closeButton?.focus());

    const handleTab = (event: KeyboardEvent) => {
      if (event.key !== "Tab") {
        return;
      }
      const focusable = getFocusable();
      if (focusable.length === 0) {
        event.preventDefault();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };

    node.addEventListener("keydown", handleTab);
    return () => {
      cancelAnimationFrame(focusFrame);
      node.removeEventListener("keydown", handleTab);
      if (previousFocus?.isConnected) {
        previousFocus.focus();
      }
    };
  }, [asideRef, mobileOpen]);
}