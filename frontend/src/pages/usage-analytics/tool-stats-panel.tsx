// 批次 7.2：工具维度观测面板（GET /api/runtime/analytics/tools）。
//
// 契约：{ schema_version, generated_at, tools: ToolStat[], totals: ToolStat }，
// 其中 ToolStat = { tool_name, calls, failures, failure_rate, empty_results,
// retried_calls, average_duration_ms, p50_duration_ms, p95_duration_ms, error_top }。
// 空库返回空数组（不是 500）→ 渲染「暂无数据」；失败沿用本地 loading/error + role="alert"。

import { listAnalyticsTools } from "@/api/runtime/analytics";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import type { AnalyticsToolStat, AnalyticsToolStatsResponse } from "@/types/runtime";
import type { TFunction } from "i18next";
import { ChevronDownIcon, ChevronRightIcon, RefreshCwIcon } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { formatDuration, formatNumber, formatPercent } from "./format";
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

const EMPTY_TOOL_STAT: AnalyticsToolStat = {
  tool_name: "",
  calls: 0,
  failures: 0,
  failure_rate: 0,
  empty_results: 0,
  retried_calls: 0,
  average_duration_ms: 0,
  p50_duration_ms: 0,
  p95_duration_ms: 0,
};

export function ToolStatsPanel({
  sessionId,
  adminToken,
}: {
  sessionId: string;
  adminToken?: string;
}) {
  const { t } = useTranslation("usageAnalytics");
  const [response, setResponse] = useState<AnalyticsToolStatsResponse | null>(null);
  const [outcome, setOutcome] = useState("");
  const [expanded, setExpanded] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const raw = await listAnalyticsTools({
        session: sessionId,
        outcome: outcome || undefined,
        adminToken,
      });
      // 后端（Go）空切片可能序列化为 null，统一归一化为空数组/零值，
      // 避免面板在 .length / .map 上崩溃（与 sessions.tsx 同口径）。
      setResponse({
        ...raw,
        tools: raw.tools ?? [],
        totals: raw.totals ?? EMPTY_TOOL_STAT,
      });
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setLoading(false);
    }
  }, [adminToken, outcome, sessionId]);

  useEffect(() => {
    void load();
  }, [load]);

  const tools = response?.tools ?? [];
  const totals = response?.totals ?? EMPTY_TOOL_STAT;
  const outcomeOptions = [
    { value: "", label: t("observability.tools.outcomeAll") },
    { value: "failed", label: t("observability.tools.outcomeFailed") },
    { value: "empty", label: t("observability.tools.outcomeEmpty") },
    { value: "partial", label: t("observability.tools.outcomePartial") },
  ];

  return (
    <section
      aria-labelledby="tool-stats-title"
      className="surface-panel min-w-0 rounded-panel-lg p-3 sm:p-4"
    >
      <div className="mb-3 flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h3 id="tool-stats-title" className="text-sm font-semibold">
            {t("observability.tools.title")}
          </h3>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t("observability.tools.subtitle")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Select
            ariaLabel={t("observability.tools.outcomeFilter")}
            value={outcome}
            options={outcomeOptions}
            onChange={(value) => setOutcome(value)}
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

      <div className="mb-3 grid grid-cols-2 gap-2 lg:grid-cols-4">
        <Metric
          label={t("observability.tools.metrics.calls")}
          value={formatNumber(totals.calls)}
          detail={t("observability.tools.columns.tool")}
        />
        <Metric
          label={t("observability.tools.metrics.failures")}
          value={formatNumber(totals.failures)}
          detail={t("observability.tools.metrics.failureRate")}
          tone={totals.failures > 0 ? "danger" : "default"}
        />
        <Metric
          label={t("observability.tools.metrics.failureRate")}
          value={formatPercent(totals.failure_rate)}
          detail={t("observability.tools.columns.calls")}
          tone={totals.failures > 0 ? "warning" : "default"}
        />
        <Metric
          label={t("observability.tools.metrics.p95")}
          value={formatDuration(totals.p95_duration_ms)}
          detail={t("observability.tools.columns.average")}
        />
      </div>

      {loading && tools.length === 0 ? (
        <div className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
          <RefreshCwIcon size={15} className="animate-spin" />
          {t("observability.loading")}
        </div>
      ) : tools.length === 0 ? (
        <div
          data-testid="tool-stats-empty"
          className="rounded-card border border-border bg-surface-softer px-3 py-8 text-center text-sm text-muted-foreground"
        >
          {t("observability.tools.empty")}
        </div>
      ) : (
        <div className="w-full max-w-full overflow-x-auto rounded-card border border-border">
          <table className="w-full min-w-[920px] border-collapse text-left text-sm">
            <thead className="bg-surface-softer text-xs text-muted-foreground">
              <tr className="border-b border-border">
                <th className="px-3 py-2 font-medium">{t("observability.tools.columns.tool")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.tools.columns.calls")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.tools.columns.failures")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.tools.columns.failureRate")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.tools.columns.emptyResults")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.tools.columns.retried")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.tools.columns.average")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.tools.columns.p50")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.tools.columns.p95")}</th>
                <th className="relative w-12 px-2 py-2">
                  <span className="sr-only">{t("observability.tools.columns.actions")}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {tools.map((tool) => {
                const open = expanded === tool.tool_name;
                const samples = tool.error_top ?? [];
                return (
                  <ToolRow
                    key={tool.tool_name}
                    tool={tool}
                    open={open}
                    samples={samples}
                    onToggle={() => setExpanded(open ? null : tool.tool_name)}
                  />
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function ToolRow({
  tool,
  open,
  samples,
  onToggle,
}: {
  tool: AnalyticsToolStat;
  open: boolean;
  samples: NonNullable<AnalyticsToolStat["error_top"]>;
  onToggle: () => void;
}) {
  const { t } = useTranslation("usageAnalytics");
  return (
    <>
      <tr className="border-b border-border/70 last:border-b-0 hover:bg-surface-soft-hover">
        <td className="px-3 py-2.5">
          <div className="font-medium">{tool.tool_name}</div>
        </td>
        <td className="px-3 py-2.5 tabular-nums">{formatNumber(tool.calls)}</td>
        <td className="px-3 py-2.5 tabular-nums">
          <span className={tool.failures > 0 ? "text-analytics-danger" : undefined}>
            {formatNumber(tool.failures)}
          </span>
        </td>
        <td className="px-3 py-2.5 tabular-nums">{formatPercent(tool.failure_rate)}</td>
        <td className="px-3 py-2.5 tabular-nums">{formatNumber(tool.empty_results)}</td>
        <td className="px-3 py-2.5 tabular-nums">{formatNumber(tool.retried_calls)}</td>
        <td className="px-3 py-2.5 tabular-nums">{formatDuration(tool.average_duration_ms)}</td>
        <td className="px-3 py-2.5 tabular-nums">{formatDuration(tool.p50_duration_ms)}</td>
        <td className="px-3 py-2.5 tabular-nums">{formatDuration(tool.p95_duration_ms)}</td>
        <td className="px-2 py-2.5">
          <Button
            variant="ghost"
            size="sm"
            className="h-7 w-7 px-0"
            aria-expanded={open}
            aria-label={
              open
                ? t("observability.tools.collapse", { tool: tool.tool_name })
                : t("observability.tools.expand", { tool: tool.tool_name })
            }
            title={t("observability.tools.columns.actions")}
            onClick={onToggle}
          >
            {open ? <ChevronDownIcon size={15} /> : <ChevronRightIcon size={15} />}
          </Button>
        </td>
      </tr>
      {open ? (
        <tr className="border-b border-border/70 bg-surface-softer last:border-b-0">
          <td colSpan={10} className="px-3 py-3">
            <div className="mb-2 text-xs font-medium text-muted-foreground">
              {t("observability.tools.detailsTitle", { count: samples.length })}
            </div>
            {samples.length === 0 ? (
              <div className="text-sm text-muted-foreground">
                {t("observability.tools.detailsEmpty")}
              </div>
            ) : (
              <ul className="divide-y divide-border rounded-card border border-border bg-surface">
                {samples.map((sample, index) => (
                  <li
                    key={`${sample.source}-${sample.error_code ?? index}-${sample.failure_category ?? ""}`}
                    className="flex flex-wrap items-center justify-between gap-2 px-3 py-2 text-sm"
                  >
                    <span className="min-w-0 break-all font-mono text-xs">
                      {sample.error_code || t("observability.errors.unknown")}
                    </span>
                    <span className="flex items-center gap-2 text-xs text-muted-foreground">
                      <Badge>{failureCategoryLabel(t, sample.failure_category)}</Badge>
                      <span className="tabular-nums">
                        {t("observability.tools.detailsColumns.count")} {formatNumber(sample.count)}
                      </span>
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </td>
        </tr>
      ) : null}
    </>
  );
}
