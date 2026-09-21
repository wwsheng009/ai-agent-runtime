// 由 pages/cache-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

export function formatCacheNumber(value: number) {
  if (!Number.isFinite(value)) return "0";
  return new Intl.NumberFormat().format(value);
}

/** 未上报（not_reported）时显示 "—" 而非 0，避免误读（§6.1 降级语义）。 */
export function formatCacheReportedNumber(value: number | undefined, reported: boolean | undefined) {
  if (!reported || value === undefined) return "—";
  return formatCacheNumber(value);
}

export function formatCacheRatio(value: number | undefined) {
  if (value === undefined || value === null || !Number.isFinite(value)) return "—";
  return `${Math.round(value * 100)}%`;
}

export function formatCacheTime(value: string | undefined) {
  if (!value) return "-";
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed)) return value;
  return new Date(parsed).toLocaleString();
}

/**
 * 延迟展示（总耗时 / 首字时间）：0/缺省 = 未采集（非流式请求、历史记录或首个
 * 增量前就失败），返回 fallback 由调用方显示本地化「未采集」，不得回退成 0 ms。
 */
export function formatCacheLatency(value: number | undefined, fallback = "—") {
  if (value === undefined || value === null || !Number.isFinite(value) || value <= 0) return fallback;
  if (value < 1000) return `${value} ms`;
  if (value < 60_000) return `${(value / 1000).toFixed(1)} s`;
  return `${(value / 60_000).toFixed(1)} min`;
}
