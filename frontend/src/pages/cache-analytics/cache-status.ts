// 由 pages/cache-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

export function cacheStatusTone(status: string) {
  switch (status) {
    case "hit": return "border-analytics-success-border bg-analytics-success-soft text-analytics-success";
    case "write": return "border-analytics-info-border bg-analytics-info-soft text-analytics-info";
    case "reported_zero": return "border-analytics-warning-border bg-analytics-warning-soft text-analytics-warning";
    case "error": return "border-analytics-danger-border bg-analytics-danger-soft text-analytics-danger";
    default: return "border-border bg-surface-softer text-muted-foreground";
  }
}

export type CacheStatusI18nKey =
  | "cache.status.hit"
  | "cache.status.write"
  | "cache.status.reportedZero"
  | "cache.status.notReported"
  | "cache.status.error";

export function cacheStatusKey(status: string): CacheStatusI18nKey {
  switch (status) {
    case "hit": return "cache.status.hit";
    case "write": return "cache.status.write";
    case "reported_zero": return "cache.status.reportedZero";
    case "not_reported": return "cache.status.notReported";
    case "error": return "cache.status.error";
    default: return "cache.status.notReported";
  }
}
