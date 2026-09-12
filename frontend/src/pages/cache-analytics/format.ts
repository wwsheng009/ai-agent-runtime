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
