import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { isUsageQuotaAdminForbidden, isUsageQuotaLedgerUnavailable } from "@/hooks/use-usage-quota";
import { cn } from "@/lib/utils";
import {
  ActivityIcon,
  CalendarDaysIcon,
  RefreshCwIcon,
  RotateCcwIcon,
} from "lucide-react";
import { useCallback, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import { formatNumber, formatPercent } from "./format";
import { FilterInput, FilterSelect, Metric } from "./primitives";
import { RequestBreakdown } from "./request-breakdown";
import { buildActiveFilterLabels } from "./request-filter-labels";
import {
  buildCacheStatusBreakdown,
  buildRequestBreakdown,
  clampRequestLedgerLimit,
  defaultRequestLedgerLimit,
  localDateValue,
  normalizeCacheBand,
  normalizeCachePercentInput,
  normalizeOutcome,
  parseCachePercent,
  requestLedgerLimitOptions,
  requestSummaryFromAnalytics,
  requestTablePageSize,
  type RequestAnalysisFilters,
} from "./request-analysis-data";
import { RequestTable } from "./request-table";
import { useRequestAnalysis } from "./use-request-analysis";

const requestFilterKeys = [
  "request_outcome",
  "request_cache",
  "request_cache_min",
  "request_cache_max",
  "request_limit",
  "request_page",
] as const;

const scopeFilterKeys = [
  "from",
  "to",
  "provider",
  "model",
  "q",
  "directory",
  "project",
  "status",
] as const;

export function RequestAnalysisPanel({ adminToken }: { adminToken: string }) {
  const { t } = useTranslation("usageAnalytics");
  const [searchParams, setSearchParams] = useSearchParams();
  const limit = clampRequestLedgerLimit(searchParams.get("request_limit"));
  const filters = useMemo<RequestAnalysisFilters>(
    () => ({
      from: searchParams.get("from") ?? "",
      to: searchParams.get("to") ?? "",
      provider: searchParams.get("provider") ?? "",
      model: searchParams.get("model") ?? "",
      q: searchParams.get("q") ?? "",
      directory: searchParams.get("directory") ?? "",
      project: searchParams.get("project") ?? "",
      sessionStatus: searchParams.get("status") ?? "",
      outcome: normalizeOutcome(searchParams.get("request_outcome")),
      cacheMin: normalizeCachePercentInput(searchParams.get("request_cache_min") ?? ""),
      cacheMax: normalizeCachePercentInput(searchParams.get("request_cache_max") ?? ""),
      cacheBand: normalizeCacheBand(searchParams.get("request_cache")),
    }),
    [searchParams],
  );
  const success = filters.outcome === "" ? undefined : filters.outcome === "success";
  const hasCustomCacheRange = filters.cacheMin !== "" || filters.cacheMax !== "";
  const cacheMinPercent = parseCachePercent(filters.cacheMin);
  const cacheMaxPercent = parseCachePercent(filters.cacheMax);
  const cacheHitRatioMin =
    hasCustomCacheRange && cacheMinPercent !== undefined
      ? cacheMinPercent / 100
      : undefined;
  const cacheHitRatioMax =
    hasCustomCacheRange && cacheMaxPercent !== undefined
      ? cacheMaxPercent / 100
      : undefined;
  const cacheHitRatioLt =
    !hasCustomCacheRange && filters.cacheBand === "low" ? 0.5 : undefined;
  const cacheHitRatioGte =
    !hasCustomCacheRange && filters.cacheBand === "high" ? 0.5 : undefined;
  const { response, loading, error, refresh } = useRequestAnalysis({
    adminToken,
    from: filters.from,
    to: filters.to,
    provider: filters.provider,
    model: filters.model,
    q: filters.q,
    directory: filters.directory,
    project: filters.project,
    status: filters.sessionStatus,
    success,
    cache_hit_ratio_min: cacheHitRatioMin,
    cache_hit_ratio_max: cacheHitRatioMax,
    cache_hit_ratio_lt: cacheHitRatioLt,
    cache_hit_ratio_gte: cacheHitRatioGte,
    limit,
  });

  const records = useMemo(() => response?.requests ?? [], [response]);
  const matchedTotal = response?.total ?? 0;
  const summary = useMemo(
    () => requestSummaryFromAnalytics(response?.summary, records),
    [records, response?.summary],
  );
  const providerRows = useMemo(
    () => buildRequestBreakdown(records, "provider"),
    [records],
  );
  const modelRows = useMemo(
    () => buildRequestBreakdown(records, "model"),
    [records],
  );
  const cacheRows = useMemo(
    () => buildCacheStatusBreakdown(summary),
    [summary],
  );
  const requestedPage = Math.max(
    0,
    Number.parseInt(searchParams.get("request_page") ?? "0", 10) || 0,
  );
  const pageCount = Math.max(1, Math.ceil(records.length / requestTablePageSize));
  const page = Math.min(requestedPage, pageCount - 1);
  const pageRecords = records.slice(
    page * requestTablePageSize,
    (page + 1) * requestTablePageSize,
  );

  const updateParam = useCallback(
    (key: string, value: string) => {
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current);
          const normalized = value.trim();
          if (normalized) next.set(key, normalized);
          else next.delete(key);
          next.delete("request_page");
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams],
  );

  const applyTodayLowCache = useCallback(() => {
    const today = localDateValue();
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current);
        next.set("from", today);
        next.set("to", today);
        next.set("request_cache", "low");
        next.delete("request_cache_min");
        next.delete("request_cache_max");
        next.delete("request_page");
        return next;
      },
      { replace: true },
    );
  }, [setSearchParams]);

  const resetRequestFilters = useCallback(() => {
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current);
        for (const key of [...requestFilterKeys, ...scopeFilterKeys]) {
          next.delete(key);
        }
        return next;
      },
      { replace: true },
    );
  }, [setSearchParams]);

  const activeFilters = useMemo(
    () => buildActiveFilterLabels(filters, t),
    [filters, t],
  );
  const hasRequestFilters =
    filters.outcome !== "" ||
    filters.cacheMin !== "" ||
    filters.cacheMax !== "" ||
    filters.cacheBand !== "" ||
    limit !== defaultRequestLedgerLimit ||
    activeFilters.length > 0;
  const forbidden = isUsageQuotaAdminForbidden(error);
  const unavailable = isUsageQuotaLedgerUnavailable(error);
  const errorLabel = unavailable
    ? t("requestAnalysis.errors.unavailable")
    : t("requestAnalysis.errors.load");

  return (
    <section
      aria-label={t("requestAnalysis.title")}
      className="surface-panel rounded-panel-lg p-3 sm:p-4"
    >
      <div className="flex flex-col gap-3 xl:flex-row xl:items-start xl:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <Badge className="border-accent-primary-border bg-accent-primary-soft text-accent-primary">
              <ActivityIcon size={13} aria-hidden="true" />
              {t("requestAnalysis.badge")}
            </Badge>
            <Badge>
              {t("requestAnalysis.scope", {
                loaded: formatNumber(records.length),
                matched: formatNumber(matchedTotal),
              })}
            </Badge>
          </div>
          <h2 className="mt-2 text-sm font-semibold">{t("requestAnalysis.title")}</h2>
          <p className="mt-1 max-w-4xl text-xs leading-5 text-muted-foreground">
            {t("requestAnalysis.description")}
          </p>
          <p className="mt-1 max-w-4xl text-xs leading-5 text-muted-foreground">
            {t("requestAnalysis.windowNote")}
          </p>
        </div>
        <Button
          type="button"
          variant="secondary"
          size="sm"
          onClick={refresh}
          disabled={loading}
          aria-label={t("requestAnalysis.refresh")}
          title={t("requestAnalysis.refresh")}
        >
          <RefreshCwIcon
            size={14}
            className={cn(loading && "animate-spin")}
            aria-hidden="true"
          />
          <span className="hidden sm:inline">{t("requestAnalysis.refresh")}</span>
        </Button>
      </div>

      {!adminToken.trim() || forbidden ? (
        <p
          role="status"
          className="mt-3 rounded-panel border border-analytics-warning-border bg-analytics-warning-soft px-3 py-2 text-xs leading-5 text-analytics-warning"
        >
          {t("requestAnalysis.adminTokenRequired")}
        </p>
      ) : null}

      {error && !forbidden ? (
        <p
          role="alert"
          className="mt-3 rounded-panel border border-analytics-danger-border bg-analytics-danger-soft px-3 py-2 text-xs leading-5 text-analytics-danger"
        >
          {errorLabel}: {error.message}
        </p>
      ) : null}

      <div className="mt-3 rounded-panel border border-border bg-surface-softer p-2">
        <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            {t("requestAnalysis.filters.title")}
          </h3>
          <div className="flex flex-wrap gap-1">
            <Button
              type="button"
              variant="secondary"
              size="sm"
              className="h-7 gap-1 px-2 text-xs"
              onClick={applyTodayLowCache}
            >
              <CalendarDaysIcon size={12} aria-hidden="true" />
              {t("requestAnalysis.filters.todayLowCache")}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 gap-1 px-2 text-xs"
              onClick={resetRequestFilters}
              disabled={!hasRequestFilters}
            >
              <RotateCcwIcon size={12} aria-hidden="true" />
              {t("requestAnalysis.filters.reset")}
            </Button>
          </div>
        </div>
        <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
          <FilterInput
            label={t("requestAnalysis.filters.cacheMin")}
            value={filters.cacheMin}
            placeholder={t("requestAnalysis.filters.cacheUnbounded")}
            type="number"
            min={0}
            max={100}
            step={0.1}
            onChange={(value) => {
              setSearchParams(
                (current) => {
                  const next = new URLSearchParams(current);
                  const normalized = normalizeCachePercentInput(value);
                  if (normalized) next.set("request_cache_min", normalized);
                  else next.delete("request_cache_min");
                  next.delete("request_cache");
                  next.delete("request_page");
                  return next;
                },
                { replace: true },
              );
            }}
          />
          <FilterInput
            label={t("requestAnalysis.filters.cacheMax")}
            value={filters.cacheMax}
            placeholder={t("requestAnalysis.filters.cacheUnbounded")}
            type="number"
            min={0}
            max={100}
            step={0.1}
            onChange={(value) => {
              setSearchParams(
                (current) => {
                  const next = new URLSearchParams(current);
                  const normalized = normalizeCachePercentInput(value);
                  if (normalized) next.set("request_cache_max", normalized);
                  else next.delete("request_cache_max");
                  next.delete("request_cache");
                  next.delete("request_page");
                  return next;
                },
                { replace: true },
              );
            }}
          />
          <FilterSelect
            label={t("requestAnalysis.filters.outcome")}
            value={filters.outcome}
            options={[
              { value: "", label: t("requestAnalysis.filters.all") },
              { value: "success", label: t("requestAnalysis.filters.success") },
              { value: "failure", label: t("requestAnalysis.filters.failure") },
            ]}
            onChange={(value) => updateParam("request_outcome", value)}
          />
          <FilterSelect
            label={t("requestAnalysis.filters.limit")}
            value={String(limit)}
            options={requestLedgerLimitOptions}
            onChange={(value) => updateParam("request_limit", value)}
          />
        </div>
        <div className="mt-2 flex flex-wrap gap-1.5" aria-label={t("requestAnalysis.filters.applied")}>
          {activeFilters.length > 0 ? (
            activeFilters.map((label) => <Badge key={label}>{label}</Badge>)
          ) : (
            <span className="text-xs text-muted-foreground">
              {t("requestAnalysis.filters.allRange")}
            </span>
          )}
        </div>
      </div>

      {error && !response ? null : loading && !response ? (
        <p className="mt-3 text-sm text-muted-foreground">{t("requestAnalysis.loading")}</p>
      ) : matchedTotal === 0 ? (
        <p className="mt-3 rounded-panel border border-dashed border-border px-3 py-8 text-center text-sm text-muted-foreground">
          {t("requestAnalysis.empty")}
        </p>
      ) : (
        <>
          <section
            aria-label={t("requestAnalysis.metrics.title")}
            className="mt-3 grid gap-2 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-6"
          >
            <Metric
              label={t("requestAnalysis.metrics.requests")}
              value={formatNumber(summary.requests)}
              detail={t("requestAnalysis.metrics.requestDetail", {
                success: formatNumber(summary.successes),
                failure: formatNumber(summary.failures),
              })}
            />
            <Metric
              label={t("requestAnalysis.metrics.successRate")}
              value={formatPercent(summary.successRate)}
              detail={t("requestAnalysis.metrics.successDetail", {
                failure: formatNumber(summary.failures),
              })}
              tone={summary.failures > 0 ? "warning" : "default"}
            />
            <Metric
              label={t("requestAnalysis.metrics.tokens")}
              value={formatNumber(summary.totalTokens)}
              detail={t("requestAnalysis.metrics.tokenDetail", {
                input: formatNumber(summary.inputTokens),
                output: formatNumber(summary.outputTokens),
              })}
            />
            <Metric
              label={t("requestAnalysis.metrics.averageTokens")}
              value={formatNumber(summary.averageTokens)}
              detail={t("requestAnalysis.metrics.averageDetail")}
            />
            <Metric
              label={t("requestAnalysis.metrics.cacheHitRate")}
              value={
                summary.averageCacheHitRatio === null
                  ? t("requestAnalysis.unavailable")
                  : formatPercent(summary.averageCacheHitRatio)
              }
              detail={t("requestAnalysis.metrics.cacheDetail", {
                reported: formatNumber(summary.cacheReported),
                requests: formatNumber(summary.requests),
              })}
              tone={
                summary.averageCacheHitRatio !== null &&
                summary.averageCacheHitRatio < 0.5
                  ? "warning"
                  : "default"
              }
            />
            <Metric
              label={t("requestAnalysis.metrics.sessions")}
              value={formatNumber(summary.sessionCount)}
              detail={t("requestAnalysis.metrics.sessionDetail", {
                providers: formatNumber(summary.providerCount),
                models: formatNumber(summary.modelCount),
              })}
            />
          </section>

          <div className="mt-2 grid gap-2 xl:grid-cols-3">
            <RequestBreakdown
              title={t("requestAnalysis.breakdown.provider")}
              rows={providerRows}
              dimension="provider"
            />
            <RequestBreakdown
              title={t("requestAnalysis.breakdown.model")}
              rows={modelRows}
              dimension="model"
            />
            <RequestBreakdown
              title={t("requestAnalysis.breakdown.cache")}
              rows={cacheRows}
              dimension="cache_status"
            />
          </div>

          <RequestTable
            records={pageRecords}
            total={records.length}
            matchedTotal={matchedTotal}
            page={page}
            pageCount={pageCount}
            onPage={(nextPage) =>
              setSearchParams(
                (current) => {
                  const next = new URLSearchParams(current);
                  next.set("request_page", String(nextPage));
                  return next;
                },
                { replace: true },
              )
            }
          />
        </>
      )}
    </section>
  );
}
