import type { TFunction } from "i18next";

export function cacheStatusLabel(
  t: TFunction<"usageAnalytics">,
  value?: string,
): string {
  switch (value?.toLowerCase()) {
    case "hit":
      return t("requestAnalysis.cacheStatus.hit");
    case "write":
      return t("requestAnalysis.cacheStatus.write");
    case "confirmed_miss":
      return t("requestAnalysis.cacheStatus.confirmed_miss");
    case "reported_zero":
      return t("requestAnalysis.cacheStatus.reported_zero");
    case "not_reported":
      return t("requestAnalysis.cacheStatus.not_reported");
    case "error":
      return t("requestAnalysis.cacheStatus.error");
    default:
      return t("requestAnalysis.cacheStatus.unknown");
  }
}
