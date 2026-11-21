// 工作区壳层的尺寸观测：composer 浮层高度与视口宽度。由 workspace-shell.tsx
// 机械拆分（P0-2），仅搬迁不改语义；同一个 ResizeObserver 兼听 composer 与根容器。

import { useEffect, useRef, useState } from "react";

import { RAIL_GRID_MIN_VIEWPORT_PX } from "@/lib/layout/rail-width";

/** P0-2：视口宽度兜底读取（无 window 时按 xl 断点处理，避免 NaN 宽度）。 */
function readViewportWidth() {
  if (typeof window === "undefined") {
    return RAIL_GRID_MIN_VIEWPORT_PX;
  }

  return Math.round(window.innerWidth);
}

export function useShellViewport(isNewThread: boolean) {
  const composerOverlayRef = useRef<HTMLDivElement | null>(null);
  /** 根容器：复用既有 ResizeObserver 监听它，拿到视口宽度的变化时机。 */
  const shellRef = useRef<HTMLDivElement | null>(null);
  const [composerOverlayHeight, setComposerOverlayHeight] = useState(220);
  /** P0-2：视口宽度（右栏 auto 宽度与 `<xl` 降级判定共用）。 */
  const [shellViewportWidth, setShellViewportWidth] =
    useState(readViewportWidth);

  useEffect(() => {
    if (isNewThread) {
      return;
    }

    const node = composerOverlayRef.current;
    if (!node || typeof ResizeObserver === "undefined") {
      return;
    }

    // P0-2：同一个 observer 兼听根容器，视口尺寸变化时重算右栏 auto 宽度（不新增监听）。
    const shellNode = shellRef.current;

    const observer = new ResizeObserver(() => {
      const nextHeight = Math.ceil(node.getBoundingClientRect().height);
      setComposerOverlayHeight((currentHeight) =>
        currentHeight === nextHeight ? currentHeight : nextHeight,
      );
      const nextViewportWidth = readViewportWidth();
      setShellViewportWidth((currentWidth) =>
        currentWidth === nextViewportWidth ? currentWidth : nextViewportWidth,
      );
    });

    observer.observe(node);
    if (shellNode) {
      observer.observe(shellNode);
    }

    return () => {
      observer.disconnect();
    };
  }, [isNewThread]);

  useEffect(() => {
    const handleViewportResize = () => {
      const nextViewportWidth = readViewportWidth();
      setShellViewportWidth((currentWidth) =>
        currentWidth === nextViewportWidth ? currentWidth : nextViewportWidth,
      );
    };
    window.addEventListener("resize", handleViewportResize);
    return () => window.removeEventListener("resize", handleViewportResize);
  }, []);

  return {
    composerOverlayHeight,
    composerOverlayRef,
    shellRef,
    shellViewportWidth,
  };
}