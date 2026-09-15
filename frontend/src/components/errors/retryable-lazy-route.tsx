// P1-10：lazy 路由元素外壳。chunk 加载失败时：
// 1) loader 内部按「上限 + 递增退避」自动重试（loadWithRetry）；
// 2) 仍然失败 → ChunkLoadError 冒泡到路由级错误边界，显示可手动重试的错误面；
// 3) 手动重试 → generation +1 → 重建 lazy 组件与边界（换 key 清错误态），
//    重新发起一次受上限约束的加载，不会形成自动死循环。
// 4) 路由切换 → <Routes> 会在同一位置复用本组件实例（props 变、state 不变），
//    因此 lazy 组件的身份必须由 loader 决定：否则会继续渲染上一条路由的页面，
//    表现为「URL 变了但页面没变」的假跳转。
// 5) 同理，错误边界的 key 也必须带上 loader 身份：否则上一条路由遗留的错误态
//    （ErrorBoundary 不随 props 变化复位）会盖住新路由，用户看到的是旧的失败面。
//
// 关键不变量：同一 (loader, generation) 必须得到同一个 lazy 组件实例。
// 渲染期每次调用 lazyWithRetry 都会产出新组件类型，React 会卸载重挂并再次挂起，
// 直接变成无限重挂循环（StrictMode 双调用下更明显），所以这里用模块级缓存兜底。

import {
  Suspense,
  useCallback,
  useState,
  type ComponentType,
  type LazyExoticComponent,
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
  /** 首次创建该 loader 对应 lazy 组件时生效；后续渲染复用缓存。 */
  retryOptions?: LoadWithRetryOptions;
}

interface LazySurfaceEntry {
  /** 稳定 id：只用于拼接错误边界 key，路由切换时重置上一条路由的错误态。 */
  id: number;
  components: Map<number, LazyExoticComponent<ComponentType>>;
}

const surfaceCache = new WeakMap<LazySurfaceLoader, LazySurfaceEntry>();

let nextSurfaceId = 0;

function resolveSurfaceEntry(loader: LazySurfaceLoader): LazySurfaceEntry {
  let entry = surfaceCache.get(loader);
  if (!entry) {
    nextSurfaceId += 1;
    entry = { id: nextSurfaceId, components: new Map() };
    surfaceCache.set(loader, entry);
  }
  return entry;
}

function resolveSurfaceComponent(
  loader: LazySurfaceLoader,
  retryOptions: LoadWithRetryOptions | undefined,
  generation: number,
): { component: LazyExoticComponent<ComponentType>; boundaryKey: string } {
  const entry = resolveSurfaceEntry(loader);

  const cached = entry.components.get(generation);
  if (cached) {
    return { component: cached, boundaryKey: `${entry.id}:${generation}` };
  }

  const Component = lazyWithRetry(loader, retryOptions);
  entry.components.set(generation, Component);
  return { component: Component, boundaryKey: `${entry.id}:${generation}` };
}

export function RetryableLazyRoute({
  loader,
  fallback = null,
  retryOptions,
}: RetryableLazyRouteProps) {
  const [generation, setGeneration] = useState(0);

  const { component: LazyComponent, boundaryKey } = resolveSurfaceComponent(
    loader,
    retryOptions,
    generation,
  );

  const handleRetry = useCallback(() => {
    setGeneration((current) => current + 1);
  }, []);

  return (
    <RouteErrorBoundary key={boundaryKey} onRetry={handleRetry}>
      <Suspense fallback={fallback}>
        <LazyComponent />
      </Suspense>
    </RouteErrorBoundary>
  );
}
