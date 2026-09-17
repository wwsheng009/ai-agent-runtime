import type {
  AnalyticsErrorPatternsQuery,
  AnalyticsErrorPatternsResponse,
  AnalyticsDimensionsResponse,
  AnalyticsOverviewResponse,
  AnalyticsRuntimeStatusEnvelope,
  AnalyticsSessionUsageDetail,
  AnalyticsSessionsQuery,
  AnalyticsSessionsResponse,
  AnalyticsSubagentStatsQuery,
  AnalyticsSubagentStatsResponse,
  AnalyticsSummaryQuery,
  AnalyticsSummaryResponse,
  AnalyticsToolStatsQuery,
  AnalyticsToolStatsResponse,
  AnalyticsUsageHealth,
} from "@/types/runtime";

import { buildRuntimeUrlWithQuery, fetchRuntimeJson } from "./shared";

type AnalyticsRequestOptions = {
  adminToken?: string;
};

function buildAnalyticsHeaders(adminToken?: string) {
  const token = adminToken?.trim();
  if (!token) {
    return {} as Record<string, string>;
  }
  return {
    Authorization: `Bearer ${token}`,
  } satisfies Record<string, string>;
}

export async function listAnalyticsSessions(
  options: AnalyticsSessionsQuery & AnalyticsRequestOptions = {},
): Promise<AnalyticsSessionsResponse> {
  return fetchRuntimeJson<AnalyticsSessionsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/analytics/sessions", {
      from: options.from,
      to: options.to,
      provider: options.provider,
      model: options.model,
      directory: options.directory,
      project: options.project,
      status: options.status,
      q: options.q,
      limit: options.limit,
      offset: options.offset,
      max_scan: options.max_scan,
    }),
    {
      headers: buildAnalyticsHeaders(options.adminToken),
    },
  );
}

export async function getAnalyticsSummary(
  options: AnalyticsSummaryQuery & AnalyticsRequestOptions = {},
): Promise<AnalyticsSummaryResponse> {
  return fetchRuntimeJson<AnalyticsSummaryResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/analytics/summary", {
      from: options.from,
      to: options.to,
      provider: options.provider,
      model: options.model,
      directory: options.directory,
      project: options.project,
      status: options.status,
      q: options.q,
      group_by: options.group_by,
      limit: options.limit,
      offset: options.offset,
      max_scan: options.max_scan,
    }),
    {
      headers: buildAnalyticsHeaders(options.adminToken),
    },
  );
}

/**
 * 首屏引导端点（方案 Phase 2）：一次返回 sessions/summary/dimensions，
 * 前端首屏从 3 个主数据请求合并为 1 个；其余区块延迟加载。
 */
export async function getAnalyticsOverview(
  options: AnalyticsSummaryQuery & AnalyticsRequestOptions = {},
): Promise<AnalyticsOverviewResponse> {
  return fetchRuntimeJson<AnalyticsOverviewResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/analytics/overview", {
      from: options.from,
      to: options.to,
      provider: options.provider,
      model: options.model,
      directory: options.directory,
      project: options.project,
      status: options.status,
      q: options.q,
      group_by: options.group_by,
      limit: options.limit,
      offset: options.offset,
      max_scan: options.max_scan,
    }),
    {
      headers: buildAnalyticsHeaders(options.adminToken),
    },
  );
}

// Dimensions 缓存：60s TTL，避免每次加载重复 5 条 DISTINCT 查询。
let dimensionsCache: { expiresAt: number; value: AnalyticsDimensionsResponse | null } | null = null;
const DIMENSIONS_TTL_MS = 60_000;

export async function getAnalyticsDimensions(
  options: Pick<AnalyticsSessionsQuery, "max_scan"> & AnalyticsRequestOptions = {},
): Promise<AnalyticsDimensionsResponse> {
  const now = Date.now();
  if (dimensionsCache && dimensionsCache.expiresAt > now) {
    return dimensionsCache.value!;
  }
  const value = await fetchRuntimeJson<AnalyticsDimensionsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/analytics/dimensions", {
      max_scan: options.max_scan,
    }),
    {
      headers: buildAnalyticsHeaders(options.adminToken),
    },
  );
  dimensionsCache = { expiresAt: now + DIMENSIONS_TTL_MS, value };
  return value;
}

export function invalidateDimensionsCache(): void {
  dimensionsCache = null;
}

export async function getAnalyticsSessionUsage(
  sessionId: string,
  options: AnalyticsRequestOptions = {},
): Promise<AnalyticsSessionUsageDetail> {
  const encoded = encodeURIComponent(sessionId);
  return fetchRuntimeJson<AnalyticsSessionUsageDetail>(
    buildRuntimeUrlWithQuery(`/api/runtime/analytics/sessions/${encoded}`, {}),
    {
      headers: buildAnalyticsHeaders(options.adminToken),
    },
  );
}

// ============================================================================
// 批次 7.2：会话观测（工具 / 子代理 / 失败模式）客户端。
//
// 端点（backend/internal/api/skills/handler.go:770-772，实现 analytics_handlers.go）：
//   GET /api/runtime/analytics/tools?session=&tool=&outcome=&from=&to=&limit=
//   GET /api/runtime/analytics/subagents?session=&failure_category=&failed_only=&from=&to=&limit=
//   GET /api/runtime/analytics/errors?session=&source=&top=
// 三个端点均受 authorizeUsageAdmin 限制（Bearer admin token），空库返回空数组。
// ============================================================================

export async function listAnalyticsTools(
  options: AnalyticsToolStatsQuery & AnalyticsRequestOptions = {},
): Promise<AnalyticsToolStatsResponse> {
  return fetchRuntimeJson<AnalyticsToolStatsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/analytics/tools", {
      session: options.session,
      tool: options.tool,
      outcome: options.outcome,
      from: options.from,
      to: options.to,
      limit: options.limit,
    }),
    {
      headers: buildAnalyticsHeaders(options.adminToken),
    },
  );
}

export async function getAnalyticsSubagents(
  options: AnalyticsSubagentStatsQuery & AnalyticsRequestOptions = {},
): Promise<AnalyticsSubagentStatsResponse> {
  return fetchRuntimeJson<AnalyticsSubagentStatsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/analytics/subagents", {
      session: options.session,
      failure_category: options.failure_category,
      // 仅在显式要求失败视图时下发，避免把默认值掺进 URL（分享/复现视图更干净）。
      failed_only: options.failed_only ? true : undefined,
      from: options.from,
      to: options.to,
      limit: options.limit,
    }),
    {
      headers: buildAnalyticsHeaders(options.adminToken),
    },
  );
}

export async function listAnalyticsErrors(
  options: AnalyticsErrorPatternsQuery & AnalyticsRequestOptions = {},
): Promise<AnalyticsErrorPatternsResponse> {
  return fetchRuntimeJson<AnalyticsErrorPatternsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/analytics/errors", {
      session: options.session,
      source: options.source,
      top: options.top,
    }),
    {
      headers: buildAnalyticsHeaders(options.adminToken),
    },
  );
}

/**
 * 读取采集健康块（批次 3.2 挂在 /api/runtime/status 的 `runtime.usage_analytics`）。
 *
 * 该块是**辅助信息**而非页面主数据：端点 403（未填 admin token）/网络失败/
 * 快照缺块时一律返回 null，由调用方静默降级（不显示横幅、不打断主数据渲染），
 * 绝不把「读不到健康块」伪造成 attached=false。
 */
export async function getUsageAnalyticsHealth(
  options: AnalyticsRequestOptions = {},
): Promise<AnalyticsUsageHealth | null> {
  try {
    const payload = await fetchRuntimeJson<AnalyticsRuntimeStatusEnvelope>(
      buildRuntimeUrlWithQuery("/api/runtime/status", {}),
      {
        headers: buildAnalyticsHeaders(options.adminToken),
      },
    );
    return normalizeUsageAnalyticsHealth(payload?.runtime?.usage_analytics);
  } catch {
    return null;
  }
}

export function normalizeUsageAnalyticsHealth(
  raw: unknown,
): AnalyticsUsageHealth | null {
  if (raw === null || typeof raw !== "object" || Array.isArray(raw)) {
    return null;
  }
  const source = raw as Record<string, unknown>;
  if (typeof source.attached !== "boolean") {
    return null;
  }
  return {
    attached: source.attached,
    db_path: typeof source.db_path === "string" ? source.db_path : "",
    ingested_total: readCount(source.ingested_total),
    conflict_total: readCount(source.conflict_total),
    last_ingest_at:
      typeof source.last_ingest_at === "string" ? source.last_ingest_at : null,
    degraded: source.degraded === true,
  };
}

function readCount(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}
