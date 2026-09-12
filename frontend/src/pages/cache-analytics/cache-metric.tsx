// 由 pages/cache-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

export function CacheMetric({ label, value, detail }: { label: string; value: string; detail: string }) {
  return (
    <div className="surface-panel rounded-[0.9rem] px-3 py-2.5">
      <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">{label}</div>
      <div className="mt-1 truncate text-2xl font-semibold tracking-[-0.03em] tabular-nums">{value}</div>
      <div className="mt-0.5 truncate text-xs text-[var(--muted-foreground)]">{detail}</div>
    </div>
  );
}
