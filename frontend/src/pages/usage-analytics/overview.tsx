// 由 pages/usage-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { getAnalyticsDimensions, getAnalyticsSummary, listAnalyticsSessions } from "@/lib/runtime-api";
import { buildAnalyticsGroupSelectionSearchParams } from "@/pages/usage-analytics-filters";
import type { AnalyticsGroupBucket, AnalyticsGroupBy, AnalyticsSessionRollup } from "@/types/runtime";
import { RotateCcwIcon, SearchIcon, ShieldIcon } from "lucide-react";
import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import { adminTokenStorageKey, analyticsFilterKeys, dimensionOptions, errorRate, formatNumber, formatPercent, normalizeDimensions, readAdminToken } from "./format";
import { emptyCoverage, emptyDimensions, emptyTotals } from "./defaults";
import { AnalyticsHeader, FilterInput, FilterSelect, Metric, QualityNotice, UsageAnalyticsChartsFallback } from "./primitives";
import { SessionTable } from "./sessions";

const UsageAnalyticsCharts = lazy(() =>
  import("@/pages/usage-analytics-charts").then((module) => ({
    default: module.UsageAnalyticsCharts,
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

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [list, summary, availableDimensions] = await Promise.all([
        listAnalyticsSessions({ ...query, adminToken }),
        getAnalyticsSummary({ ...query, group_by: groupBy, adminToken }),
        getAnalyticsDimensions({ adminToken }),
      ]);
      setSessions(list.sessions ?? []);
      setGroups(summary.groups ?? []);
      setDimensions(normalizeDimensions(availableDimensions));
      setTotals(list.totals ?? summary.totals ?? emptyTotals);
      setCoverage(list.coverage ?? emptyCoverage);
      setPartial(Boolean(list.partial));
      setPartialReasons(list.partial_reasons ?? []);
      setMatched(list.total ?? 0);
      setScanned(list.scanned ?? 0);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setLoading(false);
    }
  }, [adminToken, groupBy, query]);

  useEffect(() => { void load(); }, [load]);
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

            <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4 2xl:grid-cols-9">
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
          ) : (
            <QualityNotice coverage={coverage} partial={partial} reasons={partialReasons} />
          )}

          <section aria-label={t("metrics.title")} className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-6">
            <Metric label={t("metrics.sessions")} value={formatNumber(totals.sessions)} detail={t("metrics.turns", { count: totals.turns })} />
            <Metric label={t("metrics.tokens")} value={formatNumber(totals.total_tokens)} detail={t("metrics.tokenBreakdown", { prompt: formatNumber(totals.prompt_tokens), completion: formatNumber(totals.completion_tokens) })} />
            <Metric label={t("metrics.requests")} value={formatNumber(totals.llm_requests || totals.total_requests)} detail={t("metrics.coveredRequests", { count: coverage.llm_requests_with_usage })} />
            <Metric label={t("metrics.llmErrorRate")} value={formatPercent(errorRate(totals.llm_errors, totals.llm_requests))} detail={t("metrics.llmErrors", { count: totals.llm_errors })} tone={totals.llm_errors > 0 ? "warning" : "default"} />
            <Metric label={t("metrics.failedTurns")} value={formatNumber(totals.failed_turns)} detail={t("metrics.recoveredTurns", { count: totals.recovered_turns })} tone={totals.failed_turns > 0 ? "danger" : "default"} />
            <Metric label={t("metrics.toolErrorRate")} value={formatPercent(errorRate(totals.tool_errors, totals.tool_results_observed))} detail={t("metrics.observedTools", { count: totals.tool_results_observed })} tone={totals.tool_errors > 0 ? "warning" : "default"} />
          </section>

          <Suspense fallback={<UsageAnalyticsChartsFallback />}>
            <UsageAnalyticsCharts
              totals={totals}
              groups={groups}
              groupBy={groupBy}
              onSelect={selectGroup}
            />
          </Suspense>

          <SessionTable sessions={sessions} total={matched} loading={loading} search={searchParams.toString()} offset={pageOffset} pageSize={50} onPage={(offset) => updateFilter("offset", String(offset))} />
        </main>
      </div>
    </div>
  );
}
