import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import {
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

import { Badge } from "@/components/ui/badge";
import type {
  AnalyticsErrorPattern,
  AnalyticsGlobalTotals,
  AnalyticsGroupBucket,
  AnalyticsGroupBy,
} from "@/types/runtime";
import { TruncatedTick } from "./usage-analytics/chart-tick";

type UsageAnalyticsChartsProps = {
  totals: AnalyticsGlobalTotals;
  groups: AnalyticsGroupBucket[];
  groupBy: AnalyticsGroupBy;
  onSelect: (key: string) => void;
  /** 批次 7.2：全局失败分类分布（GET /api/runtime/analytics/errors）。 */
  failurePatterns?: AnalyticsErrorPattern[];
  selectedFailure?: string | null;
  onSelectFailure?: (key: string) => void;
};

export function UsageAnalyticsCharts({
  totals,
  groups,
  groupBy,
  onSelect,
  failurePatterns,
  selectedFailure,
  onSelectFailure,
}: UsageAnalyticsChartsProps) {
  const { t } = useTranslation("usageAnalytics");
  const chartGroups = useMemo(() => {
    const sorted = [...groups];
    if (groupBy === "day") {
      sorted.sort((left, right) => left.key.localeCompare(right.key));
      return sorted.slice(-12);
    }
    return sorted
      .sort((left, right) => right.total_tokens - left.total_tokens)
      .slice(0, 12);
  }, [groupBy, groups]);
  const promptTokens = Math.max(0, totals.prompt_tokens);
  const completionTokens = Math.max(0, totals.completion_tokens);
  const cachedTokens = Math.min(promptTokens, Math.max(0, totals.cached_tokens));
  const reasoningTokens = Math.min(
    completionTokens,
    Math.max(0, totals.reasoning_tokens),
  );
  const tokenComposition = [
    {
      name: t("charts.tokens.uncachedInput"),
      value: promptTokens - cachedTokens,
      color: "var(--analytics-chart-primary)",
    },
    {
      name: t("charts.tokens.cachedInput"),
      value: cachedTokens,
      color: "var(--analytics-chart-secondary)",
    },
    {
      name: t("charts.tokens.output"),
      value: completionTokens - reasoningTokens,
      color: "var(--analytics-chart-tertiary)",
    },
    {
      name: t("charts.tokens.reasoning"),
      value: reasoningTokens,
      color: "var(--analytics-chart-quaternary)",
    },
  ];
  const compositionTotal = tokenComposition.reduce(
    (sum, item) => sum + item.value,
    0,
  );
  // 失败分类键与 sessions.tsx 的下钻口径一致：优先 failure_category，回退 error_code。
  const failureBuckets = useMemo(() => {
    return (failurePatterns ?? [])
      .map((pattern) => {
        const key = failureKey(pattern);
        return {
          key,
          label: failurePatternLabel(t, pattern),
          count: pattern.count,
        };
      })
      .filter((bucket) => bucket.key.length > 0)
      .sort((left, right) => right.count - left.count)
      .slice(0, 10);
  }, [failurePatterns, t]);
  const visibleFailures =
    selectedFailure && selectedFailure.length > 0
      ? failureBuckets.filter((bucket) => bucket.key === selectedFailure)
      : failureBuckets;

  return (
    <section
      aria-label={t("charts.title")}
      // app-analytics-chart：坐标轴刻度字号交给 CSS 统一字号轴（见 base.css），
      // 不再用 recharts 的数值型 `tick.fontSize`（那会绕开 token）。
      className="app-analytics-chart grid min-w-0 gap-2 lg:grid-cols-[minmax(0,1.6fr)_minmax(300px,0.9fr)]"
    >
      <div className="surface-panel min-w-0 rounded-panel-lg p-3 sm:p-4">
        <ChartHeading
          title={t("charts.trend.title")}
          subtitle={t("groups.subtitle", { groupBy: t(groupByKey(groupBy)) })}
          badge={t("groups.bucketCount", { count: groups.length })}
        />
        {chartGroups.length === 0 ? (
          <ChartEmpty />
        ) : (
          <div className="h-[280px] min-w-0" data-testid="analytics-bar-chart">
            <ResponsiveContainer width="100%" height="100%">
              <BarChart
                data={chartGroups}
                margin={{ top: 12, right: 8, left: 0, bottom: 4 }}
              >
                <CartesianGrid
                  vertical={false}
                  stroke="var(--analytics-chart-grid)"
                />
                <XAxis
                  dataKey="key"
                  interval={0}
                  tickFormatter={(value) =>
                    formatDimensionTick(String(value), groupBy)
                  }
                  tick={{ fill: "var(--analytics-chart-axis)" }}
                  tickLine={false}
                  axisLine={{ stroke: "var(--analytics-chart-grid)" }}
                />
                <YAxis
                  width={54}
                  tickFormatter={formatCompactNumber}
                  tick={{ fill: "var(--analytics-chart-axis)" }}
                  tickLine={false}
                  axisLine={false}
                />
                <Tooltip
                  cursor={{ fill: "var(--analytics-chart-hover)" }}
                  content={<AnalyticsChartTooltip />}
                />
                <Bar
                  dataKey="total_tokens"
                  radius={[3, 3, 0, 0]}
                  maxBarSize={54}
                >
                  {chartGroups.map((group) => (
                    <Cell
                      key={group.key}
                      fill="var(--analytics-chart-primary)"
                      cursor="pointer"
                      role="button"
                      tabIndex={0}
                      aria-label={t("groups.filterAction", { key: group.key })}
                      onClick={() => onSelect(group.key)}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" || event.key === " ") {
                          event.preventDefault();
                          onSelect(group.key);
                        }
                      }}
                    />
                  ))}
                </Bar>
              </BarChart>
            </ResponsiveContainer>
          </div>
        )}
      </div>

      <div className="surface-panel min-w-0 rounded-panel-lg p-3 sm:p-4">
        <ChartHeading
          title={t("charts.tokens.title")}
          subtitle={t("charts.tokens.subtitle")}
        />
        <TokenCompositionChart data={tokenComposition} total={compositionTotal} />
      </div>

      <div className="surface-panel min-w-0 rounded-panel-lg p-3 sm:p-4 lg:col-span-2">
        <ChartHeading title={t("charts.failures.title")} subtitle={t("charts.failures.subtitle")} badge={selectedFailure ? t("charts.failures.selected", { key: selectedFailure }) : undefined} />
        {failureBuckets.length === 0 ? (
          <div className="flex h-[220px] items-center justify-center text-sm text-muted-foreground">{t("charts.failures.empty")}</div>
        ) : (
          <div className="grid min-w-0 gap-3 lg:grid-cols-[minmax(0,1.2fr)_minmax(240px,0.8fr)]">
            <div className="h-[220px] min-w-0" data-testid="analytics-failure-chart">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart layout="vertical" data={failureBuckets} margin={{ top: 4, right: 12, left: 8, bottom: 4 }}>
                  <CartesianGrid horizontal={false} stroke="var(--analytics-chart-grid)" />
                  <XAxis type="number" allowDecimals={false} tick={{ fill: "var(--analytics-chart-axis)" }} tickLine={false} axisLine={{ stroke: "var(--analytics-chart-grid)" }} />
                  <YAxis type="category" dataKey="label" width={160} interval={0} tick={TruncatedTick} tickLine={false} axisLine={false} />
                  <Tooltip cursor={{ fill: "var(--analytics-chart-hover)" }} content={<FailureChartTooltip />} />
                  <Bar dataKey="count" radius={[0, 3, 3, 0]} maxBarSize={22}>
                    {failureBuckets.map((bucket) => (
                      <Cell key={bucket.key} fill="var(--analytics-chart-primary)" fillOpacity={selectedFailure && selectedFailure !== bucket.key ? 0.35 : 1} cursor="pointer" role="button" tabIndex={0} aria-label={t("charts.failures.filterAction", { key: bucket.label })} onClick={() => onSelectFailure?.(bucket.key)} onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); onSelectFailure?.(bucket.key); } }} />
                    ))}
                  </Bar>
                </BarChart>
              </ResponsiveContainer>
            </div>
            <ul className="divide-y divide-border rounded-card border border-border">
              {visibleFailures.map((bucket) => (
                <li key={bucket.key} className="flex items-center justify-between gap-3 px-3 py-2 text-sm">
                  <span className="min-w-0 truncate" title={bucket.label}>{bucket.label}</span>
                  <span className="tabular-nums text-muted-foreground">{formatNumber(bucket.count)}</span>
                </li>
              ))}
            </ul>
          </div>
        )}
        {selectedFailure ? (
          <button type="button" className="mt-2 rounded-field border border-border px-2 py-1 text-xs transition hover:bg-surface-soft" onClick={() => onSelectFailure?.(selectedFailure)}>{t("charts.failures.clear")}</button>
        ) : null}
      </div>
    </section>
  );
}

const failureCategoryKeyMap = {
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

type FailureCategoryI18nKey = (typeof failureCategoryKeyMap)[keyof typeof failureCategoryKeyMap];

function failureCategoryI18nKey(category: string): FailureCategoryI18nKey | null {
  return failureCategoryKeyMap[category as keyof typeof failureCategoryKeyMap] ?? null;
}

function failureKey(pattern: AnalyticsErrorPattern) {
  return (pattern.failure_category ?? pattern.error_code ?? "").trim();
}

function failurePatternLabel(t: TFunction<"usageAnalytics">, pattern: AnalyticsErrorPattern) {
  const code = (pattern.error_code ?? "").trim();
  const category = (pattern.failure_category ?? "").trim().toLowerCase();
  const categoryKey = failureCategoryI18nKey(category);
  const categoryLabel = categoryKey ? t(categoryKey) : category;
  if (code && categoryLabel) return `${code} · ${categoryLabel}`;
  return code || categoryLabel || "";
}

function FailureChartTooltip({
  active,
  payload,
}: {
  active?: boolean;
  payload?: readonly {
    value?: number | string;
    payload?: { label?: string; count?: number };
  }[];
}) {
  const { t } = useTranslation("usageAnalytics");
  if (!active || !payload?.length) return null;
  const bucket = payload[0]?.payload;
  return (
    <div className="max-w-[min(82vw,360px)] border border-border-strong bg-surface-overlay px-3 py-2 text-xs shadow-xl">
      <div className="break-all font-medium">{bucket?.label ?? "-"}</div>
      <div className="mt-1 flex justify-between gap-5 text-muted-foreground">
        <span>{t("observability.errors.columns.count")}</span>
        <span className="tabular-nums text-foreground">
          {formatNumber(Number(payload[0]?.value ?? 0))}
        </span>
      </div>
    </div>
  );
}

function ChartHeading({
  title,
  subtitle,
  badge,
}: {
  title: string;
  subtitle: string;
  badge?: string;
}) {
  return (
    <div className="mb-2 flex min-h-10 items-start justify-between gap-3">
      <div className="min-w-0">
        <h2 className="text-sm font-semibold">{title}</h2>
        <p className="truncate text-xs text-muted-foreground">
          {subtitle}
        </p>
      </div>
      {badge ? <Badge>{badge}</Badge> : null}
    </div>
  );
}

function ChartEmpty() {
  const { t } = useTranslation("usageAnalytics");
  return (
    <div className="flex h-[280px] items-center justify-center text-sm text-muted-foreground">
      {t("groups.empty")}
    </div>
  );
}

type TokenCompositionItem = {
  name: string;
  value: number;
  color: string;
};

function TokenCompositionChart({
  data,
  total,
}: {
  data: TokenCompositionItem[];
  total: number;
}) {
  const { t } = useTranslation("usageAnalytics");
  const visibleData = data.filter((item) => item.value > 0);

  return (
    <div data-testid="analytics-token-chart">
      <div className="relative mx-auto h-[190px] w-full max-w-[320px]">
        {visibleData.length === 0 ? (
          <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
            {t("charts.tokens.empty")}
          </div>
        ) : (
          <>
            <ResponsiveContainer width="100%" height="100%">
              <PieChart>
                <Pie
                  data={visibleData}
                  dataKey="value"
                  nameKey="name"
                  cx="50%"
                  cy="50%"
                  innerRadius={54}
                  outerRadius={82}
                  paddingAngle={1.5}
                  stroke="var(--analytics-chart-surface)"
                  strokeWidth={2}
                >
                  {visibleData.map((item) => (
                    <Cell key={item.name} fill={item.color} />
                  ))}
                </Pie>
                <Tooltip content={<TokenChartTooltip total={total} />} />
              </PieChart>
            </ResponsiveContainer>
            <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center">
              <span className="app-text-11 text-muted-foreground">
                {t("charts.tokens.centerLabel")}
              </span>
              <strong className="text-base tabular-nums">
                {formatCompactNumber(total)}
              </strong>
            </div>
          </>
        )}
      </div>

      <div className="grid grid-cols-2 gap-x-4 gap-y-2">
        {data.map((item) => (
          <div key={item.name} className="min-w-0">
            <div className="flex items-center gap-2 text-xs">
              <span
                className="h-2.5 w-2.5 shrink-0"
                style={{ backgroundColor: item.color }}
              />
              <span
                className="truncate text-muted-foreground"
                title={item.name}
              >
                {item.name}
              </span>
            </div>
            <div className="ml-[18px] text-xs font-medium tabular-nums">
              {formatNumber(item.value)}{" "}
              <span className="text-muted-foreground">
                {total > 0 ? formatPercent(item.value / total) : "0.0%"}
              </span>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function AnalyticsChartTooltip({
  active,
  label,
  payload,
}: {
  active?: boolean;
  label?: string | number;
  payload?: readonly {
    value?: number | string;
    payload?: AnalyticsGroupBucket;
  }[];
}) {
  const { t } = useTranslation("usageAnalytics");
  if (!active || !payload?.length) return null;
  const bucket = payload[0]?.payload;

  return (
    <div className="max-w-[min(82vw,360px)] border border-border-strong bg-surface-overlay px-3 py-2 text-xs shadow-xl">
      <div className="break-all font-medium">
        {String(label ?? bucket?.key ?? "-")}
      </div>
      <div className="mt-1 flex justify-between gap-5 text-muted-foreground">
        <span>{t("groups.columns.tokens")}</span>
        <span className="tabular-nums text-foreground">
          {formatNumber(Number(payload[0]?.value ?? 0))}
        </span>
      </div>
      <div className="flex justify-between gap-5 text-muted-foreground">
        <span>{t("groups.columns.sessions")}</span>
        <span className="tabular-nums text-foreground">
          {formatNumber(bucket?.sessions)}
        </span>
      </div>
    </div>
  );
}

function TokenChartTooltip({
  active,
  payload,
  total,
}: {
  active?: boolean;
  payload?: readonly { name?: string; value?: number | string }[];
  total: number;
}) {
  if (!active || !payload?.length) return null;
  const value = Number(payload[0]?.value ?? 0);

  return (
    <div className="border border-border-strong bg-surface-overlay px-3 py-2 text-xs shadow-xl">
      <div className="font-medium">{payload[0]?.name}</div>
      <div className="mt-1 tabular-nums text-muted-foreground">
        {formatNumber(value)} · {total > 0 ? formatPercent(value / total) : "0.0%"}
      </div>
    </div>
  );
}

function formatNumber(value?: number | null) {
  return new Intl.NumberFormat().format(
    typeof value === "number" && Number.isFinite(value) ? value : 0,
  );
}

function formatPercent(value?: number | null) {
  const normalized =
    typeof value === "number" && Number.isFinite(value) ? value : 0;
  return `${(normalized * 100).toFixed(normalized > 0 && normalized < 0.01 ? 2 : 1)}%`;
}

function formatCompactNumber(value: number | string) {
  const number = Number(value) || 0;
  if (Math.abs(number) >= 1_000_000) {
    return `${(number / 1_000_000).toFixed(number >= 10_000_000 ? 0 : 1)}M`;
  }
  if (Math.abs(number) >= 1_000) {
    return `${(number / 1_000).toFixed(number >= 10_000 ? 0 : 1)}K`;
  }
  return String(number);
}

function formatDimensionTick(value: string, groupBy: AnalyticsGroupBy) {
  const normalized = groupBy === "project"
    ? value.split(/[\\/]/).filter(Boolean).at(-1) ?? value
    : value;
  return normalized.length > 16 ? `${normalized.slice(0, 14)}...` : normalized;
}

function groupByKey(
  groupBy: AnalyticsGroupBy,
):
  | "groupBy.day"
  | "groupBy.provider"
  | "groupBy.model"
  | "groupBy.directory"
  | "groupBy.project"
  | "groupBy.status" {
  return `groupBy.${groupBy}`;
}
