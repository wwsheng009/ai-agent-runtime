// P1-1：composer 输入菜单与其它浮层的互斥仲裁。
//
// 菜单的展开态由 useComposerMenu 内部派生（打字触发 / `+` 按钮），不在本 hook 重启一套状态：
// 这里只做两件事——
// 1. 菜单开合边沿把仲裁器的 activeSurface 切到 `menu` / 清空；
// 2. 仲裁器被其它浮层占用时，关闭仍打开的菜单。
//
// 用「上一帧 open」而不是直接依赖 activeSurface 打开菜单，避免在 effect 里把刚切到
// 模型面板的 activeSurface 又抢回菜单（点击模型按钮 → 菜单被关掉 → 又自动重开）。

import { useEffect, useRef } from "react";

import { type ComposerSurfaceController } from "@/components/workspace/composer-surface-store";

export type ComposerMenuArbitrationSource = {
  open: boolean;
  close: () => void;
};

export function useComposerMenuArbitration(
  menu: ComposerMenuArbitrationSource,
  surface: ComposerSurfaceController,
) {
  const { activeSurface, closeSelf, openSelf } = surface;
  const { close: closeMenu, open: menuOpen } = menu;
  const menuOpenRef = useRef(menuOpen);

  useEffect(() => {
    if (menuOpenRef.current === menuOpen) {
      return;
    }
    menuOpenRef.current = menuOpen;
    if (menuOpen) {
      openSelf();
    } else {
      closeSelf();
    }
  }, [closeSelf, menuOpen, openSelf]);

  useEffect(() => {
    if (
      menuOpen &&
      activeSurface !== null &&
      activeSurface !== "menu"
    ) {
      closeMenu();
    }
  }, [activeSurface, closeMenu, menuOpen]);
}