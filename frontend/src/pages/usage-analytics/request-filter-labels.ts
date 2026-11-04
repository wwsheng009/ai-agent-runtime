import type { TFunction } from "i18next";

import type { RequestAnalysisFilters } from "./request-analysis-data";

export function buildActiveFilterLabels(
  filters: RequestAnalysisFilters,
  t: TFunction<"usageAnalytics">,
): string[] {
  const labels: string[] = [];
  if (filters.from && filters.to) {
    labels.push(t("requestAnalysis.filters.tokens.date", {
      from: filters.from,
      to: filters.to,
    }));
  } else if (filters.from) {
    labels.push(t("requestAnalysis.filters.tokens.from", { from: filters.from }));
  } else if (filters.to) {
    labels.push(t("requestAnalysis.filters.tokens.to", { to: filters.to }));
  }
  if (filters.provider) {
    labels.push(t("requestAnalysis.filters.tokens.provider", { value: filters.provider }));
  }
  if (filters.model) {
    labels.push(t("requestAnalysis.filters.tokens.model", { value: filters.model }));
  }
  if (filters.q) {
    labels.push(t("requestAnalysis.filters.tokens.search", { value: filters.q }));
  }
  if (filters.directory) {
    labels.push(t("requestAnalysis.filters.tokens.directory", { value: filters.directory }));
  }
  if (filters.project) {
    labels.push(t("requestAnalysis.filters.tokens.project", { value: filters.project }));
  }
  if (filters.sessionStatus) {
    labels.push(t("requestAnalysis.filters.tokens.status", { value: filters.sessionStatus }));
  }
  if (filters.cacheMin !== "") {
    labels.push(t("requestAnalysis.filters.tokens.cacheMin", { value: filters.cacheMin }));
  }
  if (filters.cacheMax !== "") {
    labels.push(t("requestAnalysis.filters.tokens.cacheMax", { value: filters.cacheMax }));
  }
  if (filters.cacheMin === "" && filters.cacheMax === "" && filters.cacheBand === "low") {
    labels.push(t("requestAnalysis.filters.tokens.cacheLow"));
  } else if (
    filters.cacheMin === "" &&
    filters.cacheMax === "" &&
    filters.cacheBand === "high"
  ) {
    labels.push(t("requestAnalysis.filters.tokens.cacheHigh"));
  }
  if (filters.outcome === "success") {
    labels.push(t("requestAnalysis.filters.tokens.success"));
  } else if (filters.outcome === "failure") {
    labels.push(t("requestAnalysis.filters.tokens.failure"));
  }
  return labels;
}
