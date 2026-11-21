import type { AnalyticsGroupBy } from "@/types/runtime";

export type TokenCompositionItem = {
  name: string;
  value: number;
  color: string;
};

export function formatNumber(value?: number | null) {
  return new Intl.NumberFormat().format(
    typeof value === "number" && Number.isFinite(value) ? value : 0,
  );
}

export function formatPercent(value?: number | null) {
  const normalized =
    typeof value === "number" && Number.isFinite(value) ? value : 0;
  return `${(normalized * 100).toFixed(normalized > 0 && normalized < 0.01 ? 2 : 1)}%`;
}

export function formatCompactNumber(value: number | string) {
  const number = Number(value) || 0;
  if (Math.abs(number) >= 1_000_000) {
    return `${(number / 1_000_000).toFixed(number >= 10_000_000 ? 0 : 1)}M`;
  }
  if (Math.abs(number) >= 1_000) {
    return `${(number / 1_000).toFixed(number >= 10_000 ? 0 : 1)}K`;
  }
  return String(number);
}

export function formatDimensionTick(value: string, groupBy: AnalyticsGroupBy) {
  const normalized =
    groupBy === "project"
      ? (value.split(/[\\/]/).filter(Boolean).at(-1) ?? value)
      : value;
  return normalized.length > 16 ? `${normalized.slice(0, 14)}...` : normalized;
}

export function groupByKey(
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
