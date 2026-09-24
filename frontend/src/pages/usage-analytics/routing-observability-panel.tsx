// 路由切换观测面板（主 Agent / 子 Agent）：
//   GET /api/runtime/analytics/routing         总览（totals + 维度分布桶）
//   GET /api/runtime/analytics/routing/events  明细（时间倒序分页）
//
// 契约见 types/runtime/routing-analytics.ts（后端 contracts_routes.go）。
// 语义约定：route_changed / fallback_used 是三态，只计 true；分布桶与 totals
// 同源同过滤集（全量 SQL 聚合），桶计数之和恒等于 totals。
// 空库返回空数组 → 渲染「暂无数据」；403/网络错误 → role="alert" 降级。

import { getAnalyticsRoutingStats, listAnalyticsRoutingEvents } from "@/api/runtime/analytics";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import type {
  AnalyticsRouteEvent,
  AnalyticsRouteStatsResponse,
  AnalyticsRouteTotals,
} from "@/types/runtime";
import { RefreshCwIcon } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { formatNumber } from "./format";
import { Metric } from "./primitives";
import { DistributionList } from "./routing-distribution-list";
import {
  bucketsToEntries,
  difficultySourceLabel,
  kindLabel,
  rawOrNotRecorded,
  reasonLabel,
  scopeLabel,
  sourceLabel,
  taskTypeLabel,
  warningLabel,
} from "./routing-labels";
import { RouteRow } from "./routing-route-row";

const PAGE_SIZE = 50;

export function RoutingObservabilityPanel({
  sessionId,
  adminToken,
}: {
  sessionId?: string;
  adminToken?: string;
}) {
  const { t } = useTranslation("usageAnalytics");
  const [stats, setStats] = useState<AnalyticsRouteStatsResponse | null>(null);
  const [events, setEvents] = useState<AnalyticsRouteEvent[]>([]);
  const [total, setTotal] = useState(0);
  const [scope, setScope] = useState("");
  const [warningsOnly, setWarningsOnly] = useState(false);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(
    async (nextOffset: number, append: boolean) => {
      setLoading(true);
      setError(null);
      try {
        const query = {
          session: sessionId,
          scope: scope || undefined,
          warnings_only: warningsOnly || undefined,
          adminToken,
        };
        const [statsResponse, eventsResponse] = await Promise.all([
          getAnalyticsRoutingStats(query),
          listAnalyticsRoutingEvents({ ...query, limit: PAGE_SIZE, offset: nextOffset }),
        ]);
        const rows = eventsResponse.events ?? [];
        setStats(statsResponse);
        setTotal(eventsResponse.count ?? rows.length);
        setOffset(nextOffset + rows.length);
        setEvents((current) => (append ? [...current, ...rows] : rows));
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : String(caught));
      } finally {
        setLoading(false);
      }
    },
    [adminToken, scope, sessionId, warningsOnly],
  );

  useEffect(() => {
    void load(0, false);
  }, [load]);

  const totals: AnalyticsRouteTotals | undefined = stats?.totals;
  const distributionGroups = useMemo(() => {
    if (!stats) return [];
    return [
      { title: t("observability.routing.distributions.scope"), entries: bucketsToEntries(t, stats.by_scope, scopeLabel) },
      { title: t("observability.routing.distributions.kind"), entries: bucketsToEntries(t, stats.by_kind, kindLabel) },
      { title: t("observability.routing.distributions.reason"), entries: bucketsToEntries(t, stats.by_reason, reasonLabel) },
      { title: t("observability.routing.distributions.source"), entries: bucketsToEntries(t, stats.by_source, sourceLabel) },
      { title: t("observability.routing.distributions.difficulty"), entries: bucketsToEntries(t, stats.by_difficulty, rawOrNotRecorded) },
      { title: t("observability.routing.distributions.difficultySource"), entries: bucketsToEntries(t, stats.by_difficulty_source, difficultySourceLabel) },
      { title: t("observability.routing.distributions.taskType"), entries: bucketsToEntries(t, stats.by_task_type, taskTypeLabel) },
      { title: t("observability.routing.distributions.role"), entries: bucketsToEntries(t, stats.by_role, rawOrNotRecorded) },
      { title: t("observability.routing.distributions.provider"), entries: bucketsToEntries(t, stats.by_provider, rawOrNotRecorded) },
      { title: t("observability.routing.distributions.model"), entries: bucketsToEntries(t, stats.by_model, rawOrNotRecorded) },
      { title: t("observability.routing.distributions.warnings"), entries: bucketsToEntries(t, stats.warnings, warningLabel) },
    ];
  }, [stats, t]);

  const scopeOptions = [
    { value: "", label: t("observability.routing.scopeAll") },
    { value: "main_agent", label: t("observability.routing.scopeMain") },
    { value: "subagent", label: t("observability.routing.scopeSubagent") },
  ];

  return (
    <section
      aria-labelledby="routing-observability-title"
      className="surface-panel min-w-0 rounded-panel-lg p-3 sm:p-4"
      data-testid="routing-observability-panel"
    >
      <div className="mb-3 flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h3 id="routing-observability-title" className="text-sm font-semibold">
            {t("observability.routing.title")}
          </h3>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t("observability.routing.subtitle")}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <label className="flex items-center gap-1 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={warningsOnly}
              onChange={(event) => setWarningsOnly(event.target.checked)}
              className="size-3.5 accent-current"
            />
            {t("observability.routing.warningsOnly")}
          </label>
          <Select
            ariaLabel={t("observability.routing.scopeLabel")}
            value={scope}
            options={scopeOptions}
            onChange={(value) => setScope(value)}
            triggerClassName="h-8 rounded-field"
          />
          <Button
            variant="ghost"
            size="sm"
            className="h-8 px-2"
            onClick={() => void load(0, false)}
            disabled={loading}
            aria-label={t("actions.refresh")}
            title={t("actions.refresh")}
          >
            <RefreshCwIcon size={14} className={loading ? "animate-spin" : undefined} />
          </Button>
        </div>
      </div>

      {error ? (
        <div
          role="alert"
          className="mb-3 rounded-panel border border-analytics-danger-border bg-analytics-danger-soft px-3 py-2.5 text-sm text-analytics-danger"
        >
          {t("loadError")}: {error}
        </div>
      ) : null}

      <div className="mb-3 grid grid-cols-2 gap-2 lg:grid-cols-3 2xl:grid-cols-6">
        <Metric
          label={t("observability.routing.metrics.total")}
          value={formatNumber(totals?.total)}
          detail={t("observability.routing.metricDetail.appliedCleared", {
            applied: formatNumber(totals?.applied),
            cleared: formatNumber(totals?.cleared),
          })}
        />
        <Metric
          label={t("observability.routing.metrics.mainAgent")}
          value={formatNumber(totals?.main_agent)}
          detail={t("observability.routing.metricDetail.candidateTotal", { count: totals?.candidate_total ?? 0 })}
        />
        <Metric
          label={t("observability.routing.metrics.subagent")}
          value={formatNumber(totals?.subagent)}
          detail={t("observability.routing.metricDetail.distinctSessions", { count: totals?.distinct_sessions ?? 0 })}
        />
        <Metric
          label={t("observability.routing.metrics.routeChanged")}
          value={formatNumber(totals?.route_changed)}
          detail={t("observability.routing.metricDetail.distinctModels", { count: totals?.distinct_models ?? 0 })}
        />
        <Metric
          label={t("observability.routing.metrics.fallbackUsed")}
          value={formatNumber(totals?.fallback_used)}
          detail={t("observability.routing.metrics.total")}
          tone={(totals?.fallback_used ?? 0) > 0 ? "warning" : "default"}
        />
        <Metric
          label={t("observability.routing.metrics.warnings")}
          value={formatNumber(totals?.warnings)}
          detail={t("observability.routing.metrics.total")}
          tone={(totals?.warnings ?? 0) > 0 ? "warning" : "default"}
        />
      </div>

      {distributionGroups.length > 0 ? (
        <div className="mb-3 grid gap-2 lg:grid-cols-2 2xl:grid-cols-3">
          {distributionGroups.map((group) => (
            <DistributionList
              key={group.title}
              title={group.title}
              empty={t("observability.routing.empty")}
              entries={group.entries}
            />
          ))}
        </div>
      ) : null}

      <div className="mb-2 flex items-center justify-between gap-2">
        <h4 className="text-xs font-medium text-muted-foreground">{t("observability.routing.eventsTitle")}</h4>
        <span className="text-xs text-muted-foreground">
          {t("observability.routing.eventsShown", {
            shown: formatNumber(events.length),
            total: formatNumber(total),
          })}
        </span>
      </div>

      {loading && events.length === 0 ? (
        <div className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
          <RefreshCwIcon size={15} className="animate-spin" />
          {t("observability.loading")}
        </div>
      ) : events.length === 0 ? (
        <div
          data-testid="routing-observability-empty"
          className="rounded-card border border-border bg-surface-softer px-3 py-8 text-center text-sm text-muted-foreground"
        >
          {t("observability.routing.empty")}
        </div>
      ) : (
        <div className="w-full max-w-full overflow-x-auto rounded-card border border-border">
          <table className="w-full min-w-[1440px] border-collapse text-left text-sm">
            <thead className="bg-surface-softer text-xs text-muted-foreground">
              <tr className="border-b border-border">
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.time")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.scope")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.kind")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.agent")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.taskType")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.goal")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.reason")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.difficulty")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.route")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.flags")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.warnings")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.attempt")}</th>
              </tr>
            </thead>
            <tbody>
              {events.map((event, index) => (
                <RouteRow key={`${event.recorded_at}-${event.agent_id ?? index}-${index}`} event={event} />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {events.length < total ? (
        <div className="mt-2 flex justify-center">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => void load(offset, true)}
            disabled={loading}
          >
            {t("observability.routing.loadMore")}
          </Button>
        </div>
      ) : null}
    </section>
  );
}
