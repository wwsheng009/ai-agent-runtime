// 批次 7.2：子代理维度观测面板（GET /api/runtime/analytics/subagents）。
//
// 契约：{ schema_version, generated_at, summary, subagents }，
// summary = { total, succeeded, failed, unknown, failure_rate, timeouts, retried,
// failure_categories, sources }；SubagentStat 见 types/runtime/analytics.ts。
// 空库返回空数组 → 渲染「暂无数据」；summary 由后端按当前过滤结果聚合（limit 内）。

import { getAnalyticsSubagents } from "@/api/runtime/analytics";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import type { AnalyticsSubagentStatsResponse, AnalyticsSubagentStat } from "@/types/runtime";
import type { TFunction } from "i18next";
import { CheckCircle2Icon, RefreshCwIcon } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { formatDuration, formatNumber, formatPercent, formatTimestamp, shortID } from "./format";
import { Metric } from "./primitives";

// 失败分类归一表（backend/internal/llm/failure_category.go）→ i18n key。
// 本文件导出 React 组件，helper 不导出（react-refresh/only-export-components）。
const failureCategoryKeys = {
  provider_error: "observability.failureCategories.providerError",
  rate_limited: "observability.failureCategories.rateLimited",
  timeout: "observability.failureCategories.timeout",
  context_overflow: "observability.failureCategories.contextOverflow",
  tool_error: "observability.failureCategories.toolError",
  budget_exceeded: "observability.failureCategories.budgetExceeded",
  cancelled: "observability.failureCategories.cancelled",
  interrupted: "observability.failureCategories.interrupted",
  unknown: "observability.failureCategories.unknown",
} as const;

type FailureCategoryKey = (typeof failureCategoryKeys)[keyof typeof failureCategoryKeys];

function failureCategoryLabel(t: TFunction<"usageAnalytics">, category?: string): string {
  const normalized = (category ?? "").trim().toLowerCase();
  const key = failureCategoryKeys[normalized as keyof typeof failureCategoryKeys] as
    | FailureCategoryKey
    | undefined;
  if (key) return t(key);
  return (category ?? "").trim() || t("observability.errors.unknown");
}

const subagentSourceKeys = {
  scheduler: "observability.subagents.sourceLabels.scheduler",
  spawn_team: "observability.subagents.sourceLabels.spawnTeam",
  "spawn-team": "observability.subagents.sourceLabels.spawnTeam",
  team: "observability.subagents.sourceLabels.spawnTeam",
} as const;

function subagentSourceLabel(t: TFunction<"usageAnalytics">, source?: string): string {
  const normalized = (source ?? "").trim().toLowerCase();
  const key = subagentSourceKeys[normalized as keyof typeof subagentSourceKeys];
  if (key) return t(key);
  return (source ?? "").trim() || t("observability.subagents.sourceLabels.unknown");
}

function subagentStatusKey(
  success: boolean | null,
): "observability.subagents.status.succeeded" | "observability.subagents.status.failed" | "observability.subagents.status.unknown" {
  if (success === true) return "observability.subagents.status.succeeded";
  if (success === false) return "observability.subagents.status.failed";
  return "observability.subagents.status.unknown";
}

function subagentStatusTone(success: boolean | null): string {
  if (success === true) {
    return "border-analytics-success-border bg-analytics-success-soft text-analytics-success";
  }
  if (success === false) {
    return "border-analytics-danger-border bg-analytics-danger-soft text-analytics-danger";
  }
  return "";
}

export function SubagentStatsPanel({
  sessionId,
  adminToken,
}: {
  sessionId: string;
  adminToken?: string;
}) {
  const { t } = useTranslation("usageAnalytics");
  const [response, setResponse] = useState<AnalyticsSubagentStatsResponse | null>(null);
  const [failedOnly, setFailedOnly] = useState(false);
  const [failureCategory, setFailureCategory] = useState("");
  const [knownCategories, setKnownCategories] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const raw = await getAnalyticsSubagents({
        session: sessionId,
        failed_only: failedOnly || undefined,
        failure_category: failureCategory || undefined,
        limit: 200,
        adminToken,
      });
      setResponse({
        ...raw,
        summary: raw.summary ?? {
          total: 0,
          succeeded: 0,
          failed: 0,
          unknown: 0,
          failure_rate: 0,
          timeouts: 0,
          retried: 0,
          failure_categories: {},
          sources: {},
        },
        subagents: raw.subagents ?? [],
      });
      // 过滤选项取历史并集：服务端按 failure_category 过滤后，响应里只剩命中的
      // 分类，如果只看当前响应，切换过滤后就再也选不回其他分类了。
      const categories = Object.keys(raw.summary?.failure_categories ?? {});
      if (categories.length > 0) {
        setKnownCategories((current) => Array.from(new Set([...current, ...categories])).sort());
      }
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setLoading(false);
    }
  }, [adminToken, failedOnly, failureCategory, sessionId]);

  useEffect(() => {
    void load();
  }, [load]);

  const summary = response?.summary;
  const subagents = response?.subagents ?? [];
  const categoryEntries = useMemo(
    () =>
      Object.entries(summary?.failure_categories ?? {}).sort(
        (left, right) => right[1] - left[1],
      ),
    [summary],
  );
  const sourceEntries = useMemo(
    () =>
      Object.entries(summary?.sources ?? {}).sort((left, right) => right[1] - left[1]),
    [summary],
  );
  const categoryOptions = [
    { value: "", label: t("observability.subagents.categoryAll") },
    ...knownCategories.map((value) => ({
      value,
      label: failureCategoryLabel(t, value),
    })),
  ];

  return (
    <section
      aria-labelledby="subagent-stats-title"
      className="surface-panel min-w-0 rounded-panel-lg p-3 sm:p-4"
    >
      <div className="mb-3 flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h3 id="subagent-stats-title" className="text-sm font-semibold">
            {t("observability.subagents.title")}
          </h3>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t("observability.subagents.subtitle")}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <label className="flex items-center gap-1 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={failedOnly}
              onChange={(event) => setFailedOnly(event.target.checked)}
              className="size-3.5 accent-current"
            />
            {t("observability.subagents.failedOnly")}
          </label>
          <Select
            ariaLabel={t("observability.subagents.failureCategories")}
            value={failureCategory}
            options={categoryOptions}
            onChange={(value) => setFailureCategory(value)}
            triggerClassName="h-8 rounded-field"
          />
          <Button
            variant="ghost"
            size="sm"
            className="h-8 px-2"
            onClick={() => void load()}
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

      <div className="mb-3 grid grid-cols-2 gap-2 lg:grid-cols-4 2xl:grid-cols-7">
        <Metric
          label={t("observability.subagents.metrics.total")}
          value={formatNumber(summary?.total)}
          detail={t("observability.subagents.metrics.succeeded")}
        />
        <Metric
          label={t("observability.subagents.metrics.succeeded")}
          value={formatNumber(summary?.succeeded)}
          detail={t("observability.subagents.metrics.total")}
        />
        <Metric
          label={t("observability.subagents.metrics.failed")}
          value={formatNumber(summary?.failed)}
          detail={t("observability.subagents.metrics.failureRate")}
          tone={(summary?.failed ?? 0) > 0 ? "danger" : "default"}
        />
        <Metric
          label={t("observability.subagents.metrics.failureRate")}
          value={formatPercent(summary?.failure_rate)}
          detail={t("observability.subagents.metrics.failed")}
          tone={(summary?.failed ?? 0) > 0 ? "warning" : "default"}
        />
        <Metric
          label={t("observability.subagents.metrics.unknown")}
          value={formatNumber(summary?.unknown)}
          detail={t("observability.subagents.metrics.total")}
        />
        <Metric
          label={t("observability.subagents.metrics.timeouts")}
          value={formatNumber(summary?.timeouts)}
          detail={t("observability.subagents.metrics.failureRate")}
        />
        <Metric
          label={t("observability.subagents.metrics.retried")}
          value={formatNumber(summary?.retried)}
          detail={t("observability.subagents.retriedDetail")}
        />
      </div>

      <div className="mb-3 grid gap-2 lg:grid-cols-2">
        <DistributionList
          title={t("observability.subagents.failureCategories")}
          empty={t("observability.subagents.empty")}
          entries={categoryEntries.map(([key, count]) => ({
            key,
            count,
            label: failureCategoryLabel(t, key),
          }))}
        />
        <DistributionList
          title={t("observability.subagents.sourceDistribution")}
          empty={t("observability.subagents.empty")}
          entries={sourceEntries.map(([key, count]) => ({
            key,
            count,
            label: subagentSourceLabel(t, key),
          }))}
        />
      </div>

      {loading && subagents.length === 0 ? (
        <div className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
          <RefreshCwIcon size={15} className="animate-spin" />
          {t("observability.loading")}
        </div>
      ) : subagents.length === 0 ? (
        <div
          data-testid="subagent-stats-empty"
          className="rounded-card border border-border bg-surface-softer px-3 py-8 text-center text-sm text-muted-foreground"
        >
          {t("observability.subagents.empty")}
        </div>
      ) : (
        <div className="w-full max-w-full overflow-x-auto rounded-card border border-border">
          <table className="w-full min-w-[1080px] border-collapse text-left text-sm">
            <thead className="bg-surface-softer text-xs text-muted-foreground">
              <tr className="border-b border-border">
                <th className="px-3 py-2 font-medium">{t("observability.subagents.columns.subagent")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.subagents.columns.role")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.subagents.columns.source")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.subagents.columns.status")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.subagents.columns.failureCategory")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.subagents.columns.attempt")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.subagents.columns.duration")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.subagents.columns.tokens")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.subagents.columns.conflict")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.subagents.columns.completedAt")}</th>
              </tr>
            </thead>
            <tbody>
              {subagents.map((stat) => (
                <SubagentRow key={stat.subagent_id} stat={stat} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function SubagentRow({ stat }: { stat: AnalyticsSubagentStat }) {
  const { t } = useTranslation("usageAnalytics");
  return (
    <tr className="border-b border-border/70 last:border-b-0 hover:bg-surface-soft-hover">
      <td className="px-3 py-2.5">
        <div className="font-mono text-xs" title={stat.subagent_id}>
          {shortID(stat.subagent_id)}
        </div>
        {stat.child_session_id ? (
          <div className="mt-0.5 font-mono text-xs text-muted-foreground" title={stat.child_session_id}>
            {shortID(stat.child_session_id)}
          </div>
        ) : null}
      </td>
      <td className="px-3 py-2.5">{stat.role || "-"}</td>
      <td className="px-3 py-2.5">{subagentSourceLabel(t, stat.source)}</td>
      <td className="px-3 py-2.5">
        <Badge className={subagentStatusTone(stat.success)}>
          {t(subagentStatusKey(stat.success))}
        </Badge>
      </td>
      <td className="px-3 py-2.5">
        <div>{failureCategoryLabel(t, stat.failure_category)}</div>
        {stat.error_code ? (
          <div className="mt-0.5 font-mono text-xs text-muted-foreground">{stat.error_code}</div>
        ) : null}
        {stat.retry_reason ? (
          <div className="mt-0.5 text-xs text-muted-foreground">{stat.retry_reason}</div>
        ) : null}
      </td>
      <td className="px-3 py-2.5 tabular-nums">
        {stat.max_attempts > 1
          ? t("observability.subagents.retryBadge", {
              attempt: String(stat.attempt),
              max: String(stat.max_attempts),
            })
          : formatNumber(stat.attempt)}
      </td>
      <td className="px-3 py-2.5 tabular-nums">{formatDuration(stat.duration_ms)}</td>
      <td className="px-3 py-2.5 tabular-nums">{formatNumber(stat.usage_total_tokens)}</td>
      <td className="px-3 py-2.5 tabular-nums">
        {stat.conflict_count > 0 ? (
          <span className="text-analytics-warning">{formatNumber(stat.conflict_count)}</span>
        ) : (
          formatNumber(0)
        )}
      </td>
      <td className="px-3 py-2.5 text-xs text-muted-foreground">
        {formatTimestamp(stat.completed_at)}
      </td>
    </tr>
  );
}

function DistributionList({
  title,
  empty,
  entries,
}: {
  title: string;
  empty: string;
  entries: { key: string; count: number; label: string }[];
}) {
  const max = entries.reduce((current, entry) => Math.max(current, entry.count), 0);
  return (
    <div className="rounded-card border border-border bg-surface-softer p-3">
      <div className="mb-2 text-xs font-medium text-muted-foreground">{title}</div>
      {entries.length === 0 ? (
        <div className="flex items-center gap-2 py-2 text-sm text-muted-foreground">
          <CheckCircle2Icon size={14} />
          {empty}
        </div>
      ) : (
        <ul className="space-y-1.5">
          {entries.map((entry) => (
            <li key={entry.key} className="flex items-center gap-2 text-sm">
              <span className="w-32 min-w-0 truncate" title={entry.label}>
                {entry.label}
              </span>
              <span className="h-1.5 min-w-0 flex-1 rounded-full bg-surface-soft">
                <span
                  className="block h-full rounded-full bg-analytics-chart-secondary"
                  style={{ width: max > 0 ? `${Math.max(4, (entry.count / max) * 100)}%` : "0%" }}
                />
              </span>
              <span className="w-10 text-right tabular-nums text-muted-foreground">
                {formatNumber(entry.count)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
