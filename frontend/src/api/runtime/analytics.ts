import type {
  AnalyticsArtifactFlow,
  AnalyticsErrorPatternsQuery,
  AnalyticsErrorPatternsResponse,
  AnalyticsDimensionsResponse,
  AnalyticsOverviewResponse,
  AnalyticsRouteEventsResponse,
  AnalyticsRouteStatsResponse,
  AnalyticsRoutingQuery,
  AnalyticsRuntimeStatusEnvelope,
  AnalyticsRequestsQuery,
  AnalyticsRequestsResponse,
  AnalyticsSessionUsageDetail,
  AnalyticsSessionsQuery,
  AnalyticsSessionsResponse,
  AnalyticsSubagentStatsQuery,
  AnalyticsSubagentStatsResponse,
  AnalyticsSummaryQuery,
  AnalyticsSummaryResponse,
  AnalyticsToolEfficiencySnapshot,
  AnalyticsToolStatsQuery,
  AnalyticsToolStatsResponse,
  AnalyticsUsageHealth,
} from "@/types/runtime";

import { buildRuntimeUrlWithQuery, fetchRuntimeJson } from "./shared";

// 分析库可能较大（本机实测 usage_analytics.sqlite 超过 2GB），冷查询或 WAL
// checkpoint 期间会明显超过通用 10s 截止时间。分析查询单独放宽到 30s；后端
// 侧已用独立只读连接池避免被采集写事务无限阻塞（仍保留此兜底）。
const ANALYTICS_FETCH_TIMEOUT_MS = 30_000;

function fetchAnalyticsJson<T>(input: string, init?: RequestInit): Promise<T> {
  return fetchRuntimeJson<T>(input, init, ANALYTICS_FETCH_TIMEOUT_MS);
}

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
  return fetchAnalyticsJson<AnalyticsSessionsResponse>(
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
  return fetchAnalyticsJson<AnalyticsSummaryResponse>(
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
 * Cross-session LLM request facts. Cache filters are applied by the backend
 * before pagination, so "today + cache ratio below 50%" does not depend on the
 * first page of an unfiltered request window.
 */
export async function listAnalyticsRequests(
  options: AnalyticsRequestsQuery & AnalyticsRequestOptions = {},
): Promise<AnalyticsRequestsResponse> {
  return fetchAnalyticsJson<AnalyticsRequestsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/analytics/requests", {
      from: options.from,
      to: options.to,
      q: options.q,
      provider: options.provider,
      model: options.model,
      directory: options.directory,
      project: options.project,
      status: options.status,
      limit: options.limit,
      offset: options.offset,
      session_id: options.session_id,
      request_status: options.request_status,
      success: options.success === undefined ? undefined : String(options.success),
      cache_status: options.cache_status,
      cache_reported: options.cache_reported ? "true" : undefined,
      cache_hit_ratio_min: options.cache_hit_ratio_min,
      cache_hit_ratio_max: options.cache_hit_ratio_max,
      cache_hit_ratio_lt: options.cache_hit_ratio_lt,
      cache_hit_ratio_gte: options.cache_hit_ratio_gte,
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
  return fetchAnalyticsJson<AnalyticsOverviewResponse>(
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
  const value = await fetchAnalyticsJson<AnalyticsDimensionsResponse>(
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
  return fetchAnalyticsJson<AnalyticsSessionUsageDetail>(
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
//   GET /api/runtime/analytics/errors?session=&source=&top=&from=&to=&q=&provider=&model=&directory=&project=&status=
// 三个端点均受 authorizeUsageAdmin 限制（Bearer admin token），空库返回空数组。
// ============================================================================

export async function listAnalyticsTools(
  options: AnalyticsToolStatsQuery & AnalyticsRequestOptions = {},
): Promise<AnalyticsToolStatsResponse> {
  return fetchAnalyticsJson<AnalyticsToolStatsResponse>(
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
  return fetchAnalyticsJson<AnalyticsSubagentStatsResponse>(
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

/**
 * 失败模式 Top-N。会话级过滤（from/to/q/provider/model/directory/project/status）
 * 与 /analytics/sessions 同口径：命中所属会话的失败事件才参与聚合，
 * 供概览页「失败分类分布」跟随页面筛选。
 */
export async function listAnalyticsErrors(
  options: AnalyticsErrorPatternsQuery & AnalyticsRequestOptions = {},
): Promise<AnalyticsErrorPatternsResponse> {
  return fetchAnalyticsJson<AnalyticsErrorPatternsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/analytics/errors", {
      session: options.session,
      source: options.source,
      top: options.top,
      from: options.from,
      to: options.to,
      q: options.q,
      provider: options.provider,
      model: options.model,
      directory: options.directory,
      project: options.project,
      status: options.status,
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
    const payload = await fetchAnalyticsJson<AnalyticsRuntimeStatusEnvelope>(
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

// ============================================================================
// F-4a：tool_efficiency 观测快照客户端（挂在 /api/runtime/status 的
// `runtime.tool_efficiency`，后端 internal/api/skills/handler.go:9765）。
//
// 该块是**辅助观测信息**而非页面主数据：端点 403（未填 admin token）/网络失败/
// 快照缺块时一律返回 null，由调用方静默降级（不显示横幅、不打断主数据渲染），
// 绝不把「读不到观测快照」伪造成零计数。
// ============================================================================

export async function getToolEfficiencySnapshot(
  options: AnalyticsRequestOptions = {},
): Promise<AnalyticsToolEfficiencySnapshot | null> {
  try {
    const payload = await fetchAnalyticsJson<AnalyticsRuntimeStatusEnvelope>(
      buildRuntimeUrlWithQuery("/api/runtime/status", {}),
      {
        headers: buildAnalyticsHeaders(options.adminToken),
      },
    );
    return normalizeToolEfficiencySnapshot(payload?.runtime?.tool_efficiency);
  } catch {
    return null;
  }
}

export function normalizeToolEfficiencySnapshot(
  raw: unknown,
): AnalyticsToolEfficiencySnapshot | null {
  if (raw === null || typeof raw !== "object" || Array.isArray(raw)) {
    return null;
  }
  const source = raw as Record<string, unknown>;
  if (typeof source.captured_at !== "string") {
    return null;
  }
  const flow = normalizeArtifactFlow(source.artifact_flow);
  if (!flow) {
    return null;
  }
  return {
    captured_at: source.captured_at,
    preflight: normalizePreflight(source.preflight),
    outcomes: normalizeOutcomes(source.outcomes),
    disposition_replays: normalizeReplays(source.disposition_replays),
    artifact_flow: flow,
    fail_categories: readRateMap(source.fail_categories),
    inefficiency_flags: readStringArray(source.inefficiency_flags),
  };
}

export function normalizeArtifactFlow(raw: unknown): AnalyticsArtifactFlow | null {
  if (raw === null || typeof raw !== "object" || Array.isArray(raw)) {
    return null;
  }
  const source = raw as Record<string, unknown>;
  const archives = isRecord(source.archives) ? source.archives : {};
  const truncations = isRecord(source.truncations) ? source.truncations : {};
  const deref = isRecord(source.deref) ? source.deref : {};
  return {
    archives: {
      total: readCount(archives.total),
      by_layer: readRateMap(archives.by_layer),
      by_disposition: readRateMap(archives.by_disposition),
    },
    truncations: {
      total: readCount(truncations.total),
      by_layer: readRateMap(truncations.by_layer),
      by_truncated_by: readRateMap(truncations.by_truncated_by),
    },
    pointer_notice: readRateMap(source.pointer_notice),
    deref: {
      total: readCount(deref.total),
      followup_ratio: readCount(deref.followup_ratio),
      miss_by_reason: readRateMap(deref.miss_by_reason),
    },
    l1_l4_gap_ratio: readCount(source.l1_l4_gap_ratio),
  };
}

function normalizePreflight(raw: unknown): AnalyticsToolEfficiencySnapshot["preflight"] {
  const source = isRecord(raw) ? raw : {};
  return {
    total: readCount(source.total),
    allow: readCount(source.allow),
    deny: readCount(source.deny),
    allow_rate: readCount(source.allow_rate),
    by_reason: readRateMap(source.by_reason),
    by_decision: readRateMap(source.by_decision),
  };
}

function normalizeOutcomes(raw: unknown): AnalyticsToolEfficiencySnapshot["outcomes"] {
  const source = isRecord(raw) ? raw : {};
  return {
    total: readCount(source.total),
    by_outcome: readRateMap(source.by_outcome),
    by_error_code: readRateMap(source.by_error_code),
    success_rate: readCount(source.success_rate),
    non_fail_rate: readCount(source.non_fail_rate),
  };
}

function normalizeReplays(
  raw: unknown,
): AnalyticsToolEfficiencySnapshot["disposition_replays"] {
  const source = isRecord(raw) ? raw : {};
  return {
    total: readCount(source.total),
    by_outcome: readRateMap(source.by_outcome),
    by_repeat: readRateMap(source.by_repeat),
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function readRateMap(value: unknown): Record<string, number> {
  if (!isRecord(value)) {
    return {};
  }
  const out: Record<string, number> = {};
  for (const [key, entry] of Object.entries(value)) {
    if (typeof entry === "number" && Number.isFinite(entry) && entry !== 0) {
      out[key] = entry;
    }
  }
  return out;
}

function readStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.filter((entry): entry is string => typeof entry === "string");
}

// ============================================================================
// 路由切换观测（主 Agent / 子 Agent）客户端。
//
// 端点（backend/internal/api/skills/handler.go，实现 routing_analytics_handlers.go）：
//   GET /api/runtime/analytics/routing?scope=&kind=&source=&provider=&model=&difficulty=&session=&warnings_only=&from=&to=&limit=&offset=
//   GET /api/runtime/analytics/routing/events?...
// 两个端点均受 authorizeUsageAdmin 限制（Bearer admin token），空库返回空数组（不 500）。
// ============================================================================

export async function getAnalyticsRoutingStats(
  options: AnalyticsRoutingQuery & AnalyticsRequestOptions = {},
): Promise<AnalyticsRouteStatsResponse> {
  return fetchAnalyticsJson<AnalyticsRouteStatsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/analytics/routing", {
      from: options.from,
      to: options.to,
      scope: options.scope,
      kind: options.kind,
      source: options.source,
      provider: options.provider,
      model: options.model,
      difficulty: options.difficulty,
      session: options.session,
      warnings_only: options.warnings_only,
      limit: options.limit,
      offset: options.offset,
    }),
    {
      headers: buildAnalyticsHeaders(options.adminToken),
    },
  );
}

export async function listAnalyticsRoutingEvents(
  options: AnalyticsRoutingQuery & AnalyticsRequestOptions = {},
): Promise<AnalyticsRouteEventsResponse> {
  return fetchAnalyticsJson<AnalyticsRouteEventsResponse>(
    buildRuntimeUrlWithQuery("/api/runtime/analytics/routing/events", {
      from: options.from,
      to: options.to,
      scope: options.scope,
      kind: options.kind,
      source: options.source,
      provider: options.provider,
      model: options.model,
      difficulty: options.difficulty,
      session: options.session,
      warnings_only: options.warnings_only,
      limit: options.limit,
      offset: options.offset,
    }),
    {
      headers: buildAnalyticsHeaders(options.adminToken),
    },
  );
}
