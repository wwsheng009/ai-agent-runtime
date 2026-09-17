// 批次 7.2：Provider & 模型分析维度面板。
// 独立于主 group_by 视图，始终并行展示 provider 与 model 的 Token 分布，
// 用户可点击条形直接下钻为 provider / model 过滤。

import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import {
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

import type {
  AnalyticsGroupBucket,
} from "@/types/runtime";
import { formatNumber } from "./format";
import { TruncatedTick } from "./chart-tick";

type ProviderModelAnalysisProps = {
  providerGroups: AnalyticsGroupBucket[];
  modelGroups: AnalyticsGroupBucket[];
  onSelectProvider: (key: string) => void;
  onSelectModel: (key: string) => void;
};

export function ProviderModelAnalysis({
  providerGroups,
  modelGroups,
  onSelectProvider,
  onSelectModel,
}: ProviderModelAnalysisProps) {
  const { t } = useTranslation("usageAnalytics");

  const providerBuckets = useMemo(
    () =>
      [...providerGroups]
        .sort((left, right) => right.total_tokens - left.total_tokens)
        .slice(0, 10),
    [providerGroups],
  );

  const modelBuckets = useMemo(
    () =>
      [...modelGroups]
        .sort((left, right) => right.total_tokens - left.total_tokens)
        .slice(0, 10),
    [modelGroups],
  );

  return (
    <section
      aria-label={t("charts.providerModel.title")}
      className="surface-panel min-w-0 rounded-panel-lg p-3 sm:p-4"
    >
      <div className="mb-3 min-w-0">
        <h2 className="text-sm font-semibold">{t("charts.providerModel.title")}</h2>
        <p className="text-xs text-muted-foreground">
          {t("charts.providerModel.subtitle")}
        </p>
      </div>

      <div className="grid min-w-0 gap-4 lg:grid-cols-2">
        {/* Provider distribution */}
        <div className="min-w-0">
          <h3 className="mb-2 text-xs font-medium uppercase tracking-[0.1em] text-muted-foreground">
            {t("charts.providerModel.provider.title")}
          </h3>
          {providerBuckets.length === 0 ? (
            <div className="flex h-[200px] items-center justify-center text-sm text-muted-foreground">
              {t("charts.providerModel.provider.empty")}
            </div>
          ) : (
            <div className="h-[200px] min-w-0" data-testid="analytics-provider-chart">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart
                  data={providerBuckets}
                  layout="vertical"
                  margin={{ top: 4, right: 12, left: 8, bottom: 4 }}
                >
                  <CartesianGrid horizontal={false} stroke="var(--analytics-chart-grid)" />
                  <XAxis
                    type="number"
                    tickFormatter={formatCompactTokens}
                    tick={{ fill: "var(--analytics-chart-axis)" }}
                    tickLine={false}
                    axisLine={{ stroke: "var(--analytics-chart-grid)" }}
                  />
                  <YAxis
                    type="category"
                    dataKey="key"
                    width={160}
                    interval={0}
                    tick={TruncatedTick}
                    tickLine={false}
                    axisLine={false}
                  />
                  <Tooltip
                    cursor={{ fill: "var(--analytics-chart-hover)" }}
                    content={<ProviderModelTooltip />}
                  />
                  <Bar
                    dataKey="total_tokens"
                    radius={[0, 3, 3, 0]}
                    maxBarSize={22}
                    fill="var(--analytics-chart-primary)"
                  >
                    {providerBuckets.map((bucket) => (
                      <Cell
                        key={bucket.key}
                        cursor="pointer"
                        role="button"
                        tabIndex={0}
                        aria-label={t("charts.providerModel.filterAction", {
                          dimension: t("filters.provider"),
                          key: bucket.key,
                        })}
                        onClick={() => onSelectProvider(bucket.key)}
                        onKeyDown={(event) => {
                          if (event.key === "Enter" || event.key === " ") {
                            event.preventDefault();
                            onSelectProvider(bucket.key);
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

        {/* Model distribution */}
        <div className="min-w-0">
          <h3 className="mb-2 text-xs font-medium uppercase tracking-[0.1em] text-muted-foreground">
            {t("charts.providerModel.model.title")}
          </h3>
          {modelBuckets.length === 0 ? (
            <div className="flex h-[200px] items-center justify-center text-sm text-muted-foreground">
              {t("charts.providerModel.model.empty")}
            </div>
          ) : (
            <div className="h-[200px] min-w-0" data-testid="analytics-model-chart">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart
                  data={modelBuckets}
                  layout="vertical"
                  margin={{ top: 4, right: 12, left: 8, bottom: 4 }}
                >
                  <CartesianGrid horizontal={false} stroke="var(--analytics-chart-grid)" />
                  <XAxis
                    type="number"
                    tickFormatter={formatCompactTokens}
                    tick={{ fill: "var(--analytics-chart-axis)" }}
                    tickLine={false}
                    axisLine={{ stroke: "var(--analytics-chart-grid)" }}
                  />
                  <YAxis
                    type="category"
                    dataKey="key"
                    width={180}
                    interval={0}
                    tick={TruncatedTick}
                    tickLine={false}
                    axisLine={false}
                  />
                  <Tooltip
                    cursor={{ fill: "var(--analytics-chart-hover)" }}
                    content={<ProviderModelTooltip />}
                  />
                  <Bar
                    dataKey="total_tokens"
                    radius={[0, 3, 3, 0]}
                    maxBarSize={22}
                    fill="var(--analytics-chart-secondary)"
                  >
                    {modelBuckets.map((bucket) => (
                      <Cell
                        key={bucket.key}
                        cursor="pointer"
                        role="button"
                        tabIndex={0}
                        aria-label={t("charts.providerModel.filterAction", {
                          dimension: t("filters.model"),
                          key: bucket.key,
                        })}
                        onClick={() => onSelectModel(bucket.key)}
                        onKeyDown={(event) => {
                          if (event.key === "Enter" || event.key === " ") {
                            event.preventDefault();
                            onSelectModel(bucket.key);
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
      </div>
    </section>
  );
}

function ProviderModelTooltip({
  active,
  label,
  payload,
}: {
  active?: boolean;
  label?: string | number;
  payload?: readonly { value?: number | string; payload?: AnalyticsGroupBucket }[];
}) {
  const { t } = useTranslation("usageAnalytics");
  if (!active || !payload?.length) return null;
  const bucket = payload[0]?.payload;
  const value = Number(payload[0]?.value ?? 0);

  return (
    <div className="max-w-[min(82vw,360px)] border border-border-strong bg-surface-overlay px-3 py-2 text-xs shadow-xl">
      <div className="break-all font-medium">{String(label ?? "-")}</div>
      <div className="mt-1 flex justify-between gap-5 text-muted-foreground">
        <span>{t("groups.columns.tokens")}</span>
        <span className="tabular-nums text-foreground">
          {formatNumber(value)}
        </span>
      </div>
      <div className="flex justify-between gap-5 text-muted-foreground">
        <span>{t("groups.columns.sessions")}</span>
        <span className="tabular-nums text-foreground">
          {formatNumber(bucket?.sessions ?? 0)}
        </span>
      </div>
    </div>
  );
}

function formatCompactTokens(value: number | string) {
  const number = Number(value) || 0;
  if (Math.abs(number) >= 1_000_000) {
    return `${(number / 1_000_000).toFixed(number >= 10_000_000 ? 0 : 1)}M`;
  }
  if (Math.abs(number) >= 1_000) {
    return `${(number / 1_000).toFixed(number >= 10_000 ? 0 : 1)}K`;
  }
  return String(number);
}