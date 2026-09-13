// 由 pages/usage-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// 契约（2026-09-13）：/usage/cache 已删除且不做重定向，缓存分析并入会话详情。

import { useParams } from "react-router-dom";

import { UsageOverview } from "./usage-analytics/overview";
import { SessionDetail } from "./usage-analytics/sessions";

export function UsageAnalyticsPage() {
  const { sessionId } = useParams();
  return sessionId ? <SessionDetail /> : <UsageOverview />;
}
