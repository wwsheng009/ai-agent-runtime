// 由 pages/cache-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

export function cacheStatusTone(status: string) {
  switch (status) {
    case "hit": return "border-[var(--analytics-success-border)] bg-[var(--analytics-success-soft)] text-[var(--analytics-success)]";
    case "write": return "border-[var(--analytics-info-border)] bg-[var(--analytics-info-soft)] text-[var(--analytics-info)]";
    case "reported_zero": return "border-[var(--analytics-warning-border)] bg-[var(--analytics-warning-soft)] text-[var(--analytics-warning)]";
    case "error": return "border-[var(--analytics-danger-border)] bg-[var(--analytics-danger-soft)] text-[var(--analytics-danger)]";
    default: return "border-[var(--border)] bg-[var(--surface-softer)] text-[var(--muted-foreground)]";
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
