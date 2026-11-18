// Composer 浮层仲裁的共享状态（非组件模块，供 Provider 与各浮层 hook 共用）。
//
// 单独成文件的原因：react-refresh 要求组件文件只导出组件；Provider 留在
// `composer-surface-context.tsx`，这里只放 context 与消费 hook。

import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
} from "react";

export type ComposerSurfaceId =
  | "menu"
  | "model"
  | "permission"
  | "profile"
  | "context";

export type ComposerSurfaceContextValue = {
  activeSurface: ComposerSurfaceId | null;
  openSurface: (surface: ComposerSurfaceId) => void;
  closeSurface: (surface: ComposerSurfaceId) => void;
};

export const ComposerSurfaceContext =
  createContext<ComposerSurfaceContextValue | null>(null);

export type ComposerSurfaceController = {
  /** 本浮层是否展开。 */
  open: boolean;
  /** 当前展开的浮层 id（无 Provider 时为 null）。 */
  activeSurface: ComposerSurfaceId | null;
  openSelf: () => void;
  closeSelf: () => void;
  toggleSelf: () => void;
};

export function useComposerSurface(
  surface: ComposerSurfaceId,
): ComposerSurfaceController {
  const context = useContext(ComposerSurfaceContext);
  const [fallbackOpen, setFallbackOpen] = useState(false);

  const openSelf = useCallback(() => {
    if (context) {
      context.openSurface(surface);
      return;
    }
    setFallbackOpen(true);
  }, [context, surface]);

  const closeSelf = useCallback(() => {
    if (context) {
      context.closeSurface(surface);
      return;
    }
    setFallbackOpen(false);
  }, [context, surface]);

  const toggleSelf = useCallback(() => {
    if (context) {
      if (context.activeSurface === surface) {
        context.closeSurface(surface);
      } else {
        context.openSurface(surface);
      }
      return;
    }
    setFallbackOpen((current) => !current);
  }, [context, surface]);

  const open = context ? context.activeSurface === surface : fallbackOpen;

  return useMemo(
    () => ({
      open,
      activeSurface: context?.activeSurface ?? null,
      openSelf,
      closeSelf,
      toggleSelf,
    }),
    [context?.activeSurface, open, openSelf, closeSelf, toggleSelf],
  );
}