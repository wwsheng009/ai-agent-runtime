// 由 pages/usage-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import {
  getAnalyticsSubagents,
  getToolEfficiencySnapshot,
  getUsageAnalyticsHealth,
  listAnalyticsErrors,
} from "@/api/runtime/analytics";
import { getAnalyticsOverview, getAnalyticsSummary } from "@/lib/runtime-api";
import { buildAnalyticsGroupSelectionSearchParams } from "@/pages/usage-analytics-filters";
import type {
  AnalyticsErrorPattern,
  AnalyticsGroupBucket,
  AnalyticsGroupBy,
  AnalyticsSessionRollup,
  AnalyticsSubagentStatsSummary,
  AnalyticsToolEfficiencySnapshot,
  AnalyticsUsageHealth,
} from "@/types/runtime";
import { AlertTriangleIcon, RotateCcwIcon, SearchIcon, ShieldIcon } from "lucide-react";
import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useSearchParams } from "react-router-dom";

import { adminTokenStorageKey, analyticsFilterKeys, dimensionOptions, errorRate, formatFirstToken, formatNumber, formatPercent, formatTimestamp, normalizeDimensions, readAdminToken } from "./format";
import { ArtifactFlowPanel } from "./artifact-flow-panel";
import { emptyCoverage, emptyDimensions, emptyTotals } from "./defaults";
import { AnalyticsHeader, FilterInput, FilterSelect, Metric, QualityNotice, UsageAnalyticsChartsFallback } from "./primitives";
import { UsageQuotaPanel } from "./quota";
import { RoutingObservabilityPanel } from "./routing-observability-panel";
import { SessionTable } from "./sessions";

const UsageAnalyticsCharts = lazy(() =>
  import("@/pages/usage-analytics-charts").then((module) => ({
    default: module.UsageAnalyticsCharts,
  })),
);

const ProviderModelAnalysis = lazy(() =>
  import("@/pages/usage-analytics/provider-model-analysis").then((module) => ({
    default: module.ProviderModelAnalysis,
  })),
);

export function UsageOverview() {
  const { t } = useTranslation("usageAnalytics");
  const [searchParams, setSearchParams] = useSearchParams();
  const [adminToken, setAdminToken] = useState(readAdminToken);
  const [sessions, setSessions] = useState<AnalyticsSessionRollup[]>([]);
  const [groups, setGroups] = useState<AnalyticsGroupBucket[]>([]);
  const [dimensions, setDimensions] = useState(emptyDimensions);
  const [totals, setTotals] = useState(emptyTotals);
  const [coverage, setCoverage] = useState(emptyCoverage);
  const [partial, setPartial] = useState(false);
  const [partialReasons, setPartialReasons] = useState<string[]>([]);
  const [matched, setMatched] = useState(0);
  const [scanned, setScanned] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // 批次 7.2 全局失败视图：子代理摘要 + 失败分类分布 + 采集健康（3.2 快照）。
  const [subagentSummary, setSubagentSummary] = useState<AnalyticsSubagentStatsSummary | null>(null);
  const [failurePatterns, setFailurePatterns] = useState<AnalyticsErrorPattern[]>([]);
  const [failureFilter, setFailureFilter] = useState<string | null>(null);
  const [observabilityError, setObservabilityError] = useState<string | null>(null);
  const [health, setHealth] = useState<AnalyticsUsageHealth | null>(null);
  // F-4：Artifact Flow 观测快照（runtime.tool_efficiency，静默降级 null）。
  const [toolEfficiency, setToolEfficiency] = useState<AnalyticsToolEfficiencySnapshot | null>(null);
  // 批次 7.2：Provider & 模型分布（独立于主 group_by 视图）。
  const [providerGroups, setProviderGroups] = useState<AnalyticsGroupBucket[]>([]);
  const [modelGroups, setModelGroups] = useState<AnalyticsGroupBucket[]>([]);

  const groupBy = (searchParams.get("group_by") || "day") as AnalyticsGroupBy;
  const pageOffset = Math.max(0, Number.parseInt(searchParams.get("offset") || "0", 10) || 0);
  const hasActiveFilters = analyticsFilterKeys.some((key) => searchParams.has(key)) || groupBy !== "day" || pageOffset > 0;
  const query = useMemo(() => ({
    from: searchParams.get("from") || undefined,
    to: searchParams.get("to") || undefined,
    q: searchParams.get("q") || undefined,
    provider: searchParams.get("provider") || undefined,
    model: searchParams.get("model") || undefined,
    directory: searchParams.get("directory") || undefined,
    project: searchParams.get("project") || undefined,
    status: searchParams.get("status") || undefined,
    limit: 50,
    offset: pageOffset,
  }), [pageOffset, searchParams]);

  const updateFilter = useCallback((key: string, value: string) => {
    setSearchParams((current) => {
      const next = new URLSearchParams(current);
      const normalized = value.trim();
      if (normalized) next.set(key, normalized);
      else next.delete(key);
      if (key !== "offset") next.delete("offset");
      return next;
    }, { replace: true });
  }, [setSearchParams]);

  const resetFilters = useCallback(() => {
    setSearchParams(new URLSearchParams(), { replace: true });
  }, [setSearchParams]);

  // 观测区块（子代理 / 失败模式 / 采集健康）：不进首屏 Promise.all，
  // 由独立 effect 延迟加载（Phase 4 首屏瘦身）。
  const loadObservability = useCallback(async () => {
    setObservabilityError(null);
    const [subagents, errors, usageHealth, toolEfficiency] = await Promise.all([
      // 子代理摘要用于「子代理失败率」指标卡：summary 由后端按 limit 内记录聚合，
      // 取最大行数（200）保证全局口径，而不是只取 1 行的假摘要。
      getAnalyticsSubagents({ adminToken, limit: 200 }).catch((caught) => {
        setObservabilityError(caught instanceof Error ? caught.message : String(caught));
        return null;
      }),
      listAnalyticsErrors({ adminToken, top: 10 }).catch((caught) => {
        setObservabilityError(caught instanceof Error ? caught.message : String(caught));
        return null;
      }),
      getUsageAnalyticsHealth({ adminToken }),
      getToolEfficiencySnapshot({ adminToken }),
    ]);
    setSubagentSummary(subagents?.summary ?? null);
    setFailurePatterns(errors?.patterns ?? []);
    setHealth(usageHealth);
    setToolEfficiency(toolEfficiency);
  }, [adminToken]);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      // Phase 2：首屏主数据合并为 1 个引导请求（sessions + summary + dimensions）。
      const overview = await getAnalyticsOverview({ ...query, group_by: groupBy, adminToken });
      const list = overview.sessions;
      setSessions(list.sessions ?? []);
      setGroups(overview.summary?.groups ?? []);
      setDimensions(normalizeDimensions(overview.dimensions));
      setTotals(list.totals ?? overview.summary?.totals ?? emptyTotals);
      setCoverage(list.coverage ?? overview.summary?.coverage ?? emptyCoverage);
      setPartial(Boolean(list.partial ?? overview.summary?.partial));
      setPartialReasons(list.partial_reasons ?? overview.summary?.partial_reasons ?? []);
      setMatched(list.total ?? overview.matched ?? 0);
      setScanned(list.scanned ?? 0);
      // 批次 7.2：Provider & 模型分布延迟加载（不阻塞首屏）。
      // 这些数据独立渲染，在首屏数据就位后异步获取。
      void Promise.all([
        getAnalyticsSummary({ ...query, group_by: "provider", adminToken }).catch(() => null),
        getAnalyticsSummary({ ...query, group_by: "model", adminToken }).catch(() => null),
      ]).then(([prov, mod]) => {
        setProviderGroups(prov?.groups ?? []);
        setModelGroups(mod?.groups ?? []);
      });
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setLoading(false);
    }
  }, [adminToken, groupBy, query]);

  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    const timer = window.setTimeout(() => { void loadObservability(); }, 300);
    return () => window.clearTimeout(timer);
  }, [loadObservability]);
  useEffect(() => {
    if (adminToken.trim()) window.localStorage.setItem(adminTokenStorageKey, adminToken.trim());
    else window.localStorage.removeItem(adminTokenStorageKey);
  }, [adminToken]);

  const groupOptions = [
    { value: "day", label: t("groupBy.day") },
    { value: "provider", label: t("groupBy.provider") },
    { value: "model", label: t("groupBy.model") },
    { value: "directory", label: t("groupBy.directory") },
    { value: "project", label: t("groupBy.project") },
    { value: "status", label: t("groupBy.status") },
  ] as const;
  const selectGroup = useCallback((key: string) => {
    setSearchParams(
      (current) => buildAnalyticsGroupSelectionSearchParams(current, groupBy, key),
      { replace: true },
    );
  }, [groupBy, setSearchParams]);

  return (
    <div className="min-h-screen min-w-0 overflow-x-hidden bg-[var(--workspace-shell-bg)] text-foreground">
      <div className="mx-auto flex min-h-screen w-full max-w-[1760px] flex-col gap-2 px-2.5 py-2.5 sm:px-3">
        <AnalyticsHeader onRefresh={() => void load()} refreshing={loading} />
        <main className="flex min-w-0 flex-1 flex-col gap-2">
          <form
            aria-label={t("filters.title")}
            className="surface-panel rounded-panel-lg p-3 sm:p-4"
            onSubmit={(event) => event.preventDefault()}
          >
            <div className="mb-3 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
              <div>
                <h2 className="text-sm font-semibold">{t("filters.title")}</h2>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  {t("meta.scannedMatched", { scanned: String(scanned), matched: String(matched) })}
                </p>
              </div>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="self-start sm:self-auto"
                onClick={resetFilters}
                disabled={!hasActiveFilters}
              >
                <RotateCcwIcon size={14} />
                {t("actions.resetFilters")}
              </Button>
            </div>

            <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4 xl:grid-cols-6 2xl:grid-cols-9">
              <FilterInput icon={<SearchIcon size={14} />} label={t("filters.search")} value={query.q ?? ""} placeholder={t("filters.searchPlaceholder")} onChange={(value) => updateFilter("q", value)} />
              <FilterSelect label={t("filters.provider")} value={query.provider ?? ""} options={dimensionOptions(dimensions.providers, query.provider ?? "", t("filters.allProviders"))} onChange={(value) => updateFilter("provider", value)} />
              <FilterSelect label={t("filters.model")} value={query.model ?? ""} options={dimensionOptions(dimensions.models, query.model ?? "", t("filters.allModels"))} onChange={(value) => updateFilter("model", value)} />
              <FilterSelect label={t("filters.directory")} value={query.directory ?? ""} options={dimensionOptions(dimensions.directories, query.directory ?? "", t("filters.allDirectories"))} onChange={(value) => updateFilter("directory", value)} />
              <FilterSelect label={t("filters.project")} value={query.project ?? ""} options={dimensionOptions(dimensions.projects, query.project ?? "", t("filters.allProjects"))} onChange={(value) => updateFilter("project", value)} />
              <FilterSelect label={t("filters.status")} value={query.status ?? ""} options={dimensionOptions(dimensions.statuses, query.status ?? "", t("filters.allStatuses"))} onChange={(value) => updateFilter("status", value)} />
              <FilterInput type="date" label={t("filters.from")} value={query.from ?? ""} placeholder="" onChange={(value) => updateFilter("from", value)} />
              <FilterInput type="date" label={t("filters.to")} value={query.to ?? ""} placeholder="" onChange={(value) => updateFilter("to", value)} />
            </div>

            <div className="mt-3 grid gap-2 md:grid-cols-[11rem_minmax(0,20rem)]">
              <label className="min-w-0">
                <span className="mb-1 block text-xs text-muted-foreground">{t("filters.groupBy")}</span>
                <Select ariaLabel={t("filters.groupBy")} value={groupBy} options={groupOptions} onChange={(value) => updateFilter("group_by", value)} triggerClassName="h-9 rounded-field" />
              </label>
              <label className="min-w-0">
                <span className="mb-1 flex items-center gap-1 text-xs text-muted-foreground"><ShieldIcon size={12} />{t("filters.token")}</span>
                <input type="password" autoComplete="off" value={adminToken} onChange={(event) => setAdminToken(event.target.value)} placeholder={t("filters.tokenPlaceholder")} className="h-9 w-full rounded-field border border-border bg-surface-softer px-3 text-sm outline-none transition focus:border-accent-primary-border focus:ring-2 focus:ring-ring" />
              </label>
            </div>
          </form>

          {error ? (
            <div role="alert" className="rounded-panel border border-analytics-danger-border bg-analytics-danger-soft px-3 py-2.5 text-sm text-analytics-danger">
              {t("loadError")}: {error}
            </div>
          ) : health && (!health.attached || health.degraded) ? (
            <AnalyticsHealthBanner health={health} />
          ) : (
            <QualityNotice coverage={coverage} partial={partial} reasons={partialReasons} />
          )}

          {observabilityError ? (
            <div role="alert" className="rounded-panel border border-analytics-warning-border bg-analytics-warning-soft px-3 py-2 text-xs text-analytics-warning">
              {t("observability.errors.title")}: {observabilityError}
            </div>
          ) : null}

          <section aria-label={t("metrics.title")} className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-8">
            <Metric label={t("metrics.sessions")} value={formatNumber(totals.sessions)} detail={t("metrics.turns", { count: totals.turns })} />
            <Metric label={t("metrics.tokens")} value={formatNumber(totals.total_tokens)} detail={t("metrics.tokenBreakdown", { prompt: formatNumber(totals.prompt_tokens), completion: formatNumber(totals.completion_tokens) })} />
            <Metric label={t("metrics.requests")} value={formatNumber(totals.llm_requests || totals.total_requests)} detail={t("metrics.coveredRequests", { count: coverage.llm_requests_with_usage })} />
            <Metric label={t("metrics.llmErrorRate")} value={formatPercent(errorRate(totals.llm_errors, totals.llm_requests))} detail={t("metrics.llmErrors", { count: totals.llm_errors })} tone={totals.llm_errors > 0 ? "warning" : "default"} />
            <Metric label={t("metrics.failedTurns")} value={formatNumber(totals.failed_turns)} detail={t("metrics.recoveredTurns", { count: totals.recovered_turns })} tone={totals.failed_turns > 0 ? "danger" : "default"} />
            <Metric label={t("metrics.toolErrorRate")} value={formatPercent(errorRate(totals.tool_errors, totals.tool_results_observed))} detail={t("metrics.observedTools", { count: totals.tool_results_observed })} tone={totals.tool_errors > 0 ? "warning" : "default"} />
            <Metric label={t("metrics.firstToken")} value={formatFirstToken(totals.average_first_token_ms) ?? t("metrics.notCollected")} detail={t("metrics.firstTokenDetail", { count: totals.first_token_samples ?? 0 })} />
            <Metric
              label={t("metrics.subagentFailureRate")}
              value={subagentSummary ? formatPercent(subagentSummary.failure_rate) : "--"}
              detail={
                subagentSummary
                  ? t("metrics.subagentFailureDetail", {
                      failed: formatNumber(subagentSummary.failed),
                      total: formatNumber(subagentSummary.succeeded + subagentSummary.failed),
                    })
                  : t("unavailable")
              }
              tone={(subagentSummary?.failed ?? 0) > 0 ? "warning" : "default"}
            />
          </section>

          <UsageQuotaPanel adminToken={adminToken} />

          <ArtifactFlowPanel snapshot={toolEfficiency} loading={false} />

          {/* 路由切换观测（主/子 Agent）：全局视图，独立拉取，不阻塞首屏。 */}
          <RoutingObservabilityPanel adminToken={adminToken} />

          <Suspense fallback={<UsageAnalyticsChartsFallback />}>
            <UsageAnalyticsCharts
              totals={totals}
              groups={groups}
              groupBy={groupBy}
              onSelect={selectGroup}
              failurePatterns={failurePatterns}
              selectedFailure={failureFilter}
              onSelectFailure={(key) => setFailureFilter((current) => (current === key ? null : key))}
            />
          </Suspense>

           <Suspense fallback={<UsageAnalyticsChartsFallback />}>
             <ProviderModelAnalysis
               providerGroups={providerGroups}
               modelGroups={modelGroups}
               onSelectProvider={(key) => updateFilter("provider", key)}
               onSelectModel={(key) => updateFilter("model", key)}
             />
           </Suspense>

          <SessionTable sessions={sessions} total={matched} loading={loading} search={searchParams.toString()} offset={pageOffset} pageSize={50} onPage={(offset) => updateFilter("offset", String(offset))} />
        </main>
      </div>
    </div>
  );
}

// 批次 7.2：采集健康横幅（批次 3.2 快照）。attached=false / degraded=true 时
// 取代 QualityNotice 显示，并提供到 /logs 的取证链；attached 且健康时不显示。
function AnalyticsHealthBanner({ health }: { health: AnalyticsUsageHealth }) {
  const { t } = useTranslation("usageAnalytics");
  const detached = !health.attached;
  return (
    <div
      data-testid="usage-analytics-health-banner"
      className="flex items-start gap-2 rounded-panel border border-analytics-warning-border bg-analytics-warning-soft px-3 py-2.5 text-sm shadow-[var(--elevation-panel)] text-analytics-warning"
    >
      <AlertTriangleIcon size={16} className="mt-0.5 shrink-0" />
      <div className="min-w-0 flex-1">
        <div className="font-medium">
          {detached ? t("observability.health.detachedTitle") : t("observability.health.degradedTitle")}
        </div>
        <div className="mt-0.5 text-xs">
          {detached ? t("observability.health.detachedDetail") : t("observability.health.degradedDetail")}
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
          {health.db_path ? (
            <span className="min-w-0 break-all font-mono">
              {t("observability.health.dbPath")}: {health.db_path}
            </span>
          ) : null}
          <span className="tabular-nums">
            {t("observability.health.ingested", { count: health.ingested_total ?? 0 })}
          </span>
          {health.last_ingest_at ? (
            <span>{t("observability.health.lastIngest", { time: formatTimestamp(health.last_ingest_at) })}</span>
          ) : null}
          {(health.conflict_total ?? 0) > 0 ? (
            <span className="tabular-nums">
              {t("observability.health.conflicts", { count: health.conflict_total ?? 0 })}
            </span>
          ) : null}
          <Link className="underline underline-offset-2" to="/logs">
            {t("observability.health.openLogs")}
          </Link>
        </div>
      </div>
    </div>
  );
}
