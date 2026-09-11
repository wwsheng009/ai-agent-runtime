// 由 pages/usage-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { RefreshCwIcon } from "lucide-react";
import { lazy, Suspense } from "react";
import { useLocation, useParams, useSearchParams } from "react-router-dom";

import { UsageOverview } from "./usage-analytics/overview";
import { SessionDetail } from "./usage-analytics/sessions";

const CacheAnalyticsView = lazy(() =>
  import("@/pages/cache-analytics-page").then((module) => ({
    default: module.CacheAnalyticsView,
  })),
);

export function UsageAnalyticsPage() {
  const { sessionId } = useParams();
  const [searchParams] = useSearchParams();
  const { pathname } = useLocation();
  // 入口决策（§6.2）：缓存视图作为 /usage 页内 tab；直接访问 /usage/cache
  // 或 ?tab=cache 时默认选中缓存 tab，其余路径保持用量视图。
  const cacheTab = pathname.startsWith("/usage/cache") || searchParams.get("tab") === "cache";
  if (cacheTab) {
    return (
      <Suspense fallback={<CacheViewFallback />}>
        <CacheAnalyticsView sessionId={sessionId ?? null} />
      </Suspense>
    );
  }
  return sessionId ? <SessionDetail /> : <UsageOverview />;
}

function CacheViewFallback() {
  return (
    <div className="flex min-h-screen items-center justify-center [background:var(--workspace-shell-bg)] text-[var(--muted-foreground)]">
      <RefreshCwIcon size={16} className="mr-2 animate-spin" />
      正在加载缓存分析…
    </div>
  );
}
