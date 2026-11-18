// Composer 浮层仲裁（P1-1）：同一时刻只允许一个浮层展开。
//
// 背景：输入菜单 / 模型与推理 / 权限 / Profile / 上下文用量过去各自持有独立开关，
// 两层浮层可以同时展开并互相遮挡。这里用一个上下文级「当前浮层」枚举收口：
// 任何组件打开自己前先把 activeSurface 指向自己，其余组件观察到后立即收起。
//
// 状态与消费 hook 在 `composer-surface-store.ts`（组件文件只导出组件，
// 规避 react-refresh 的 only-export-components 约束）。

import { useCallback, useMemo, useState, type ReactNode } from "react";

import {
  ComposerSurfaceContext,
  type ComposerSurfaceId,
} from "@/components/workspace/composer-surface-store";

export function ComposerSurfaceProvider({ children }: { children: ReactNode }) {
  const [activeSurface, setActiveSurface] = useState<ComposerSurfaceId | null>(
    null,
  );

  const openSurface = useCallback((surface: ComposerSurfaceId) => {
    setActiveSurface(surface);
  }, []);
  const closeSurface = useCallback((surface: ComposerSurfaceId) => {
    setActiveSurface((current) => (current === surface ? null : current));
  }, []);

  const value = useMemo(
    () => ({ activeSurface, openSurface, closeSurface }),
    [activeSurface, closeSurface, openSurface],
  );

  return (
    <ComposerSurfaceContext.Provider value={value}>
      {children}
    </ComposerSurfaceContext.Provider>
  );
}