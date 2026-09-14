// P1-10：lazy 路由元素外壳。chunk 加载失败时：
// 1) loader 内部按「上限 + 递增退避」自动重试（loadWithRetry）；
// 2) 仍然失败 → ChunkLoadError 冒泡到路由级错误边界，显示可手动重试的错误面；
// 3) 手动重试 → generation +1 → 重建 lazy 组件与边界（换 key 清错误态），
//    重新发起一次受上限约束的加载，不会形成自动死循环。

import {
  Suspense,
  useCallback,
  useState,
  type ReactNode,
} from "react";

import {
  lazyWithRetry,
  type LazySurfaceLoader,
  type LoadWithRetryOptions,
} from "@/lib/lazy-retry";

import { RouteErrorBoundary } from "./boundaries";

export interface RetryableLazyRouteProps {
  /** 必须是模块级稳定引用（route 元素在 App.tsx 顶层定义），避免每次渲染重建。 */
  loader: LazySurfaceLoader;
  fallback?: ReactNode;
  retryOptions?: LoadWithRetryOptions;
}

export function RetryableLazyRoute({
  loader,
  fallback = null,
  retryOptions,
}: RetryableLazyRouteProps) {
  const [surface, setSurface] = useState(() => ({
    generation: 0,
    Component: lazyWithRetry(loader, retryOptions),
  }));

  const handleRetry = useCallback(() => {
    setSurface((current) => ({
      generation: current.generation + 1,
      Component: lazyWithRetry(loader, retryOptions),
    }));
  }, [loader, retryOptions]);

  const LazyComponent = surface.Component;

  return (
    <RouteErrorBoundary key={surface.generation} onRetry={handleRetry}>
      <Suspense fallback={fallback}>
        <LazyComponent />
      </Suspense>
    </RouteErrorBoundary>
  );
}
