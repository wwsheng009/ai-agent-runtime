// 由 pages/cache-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { cn } from "@/lib/utils";

export function DistributionChip({ tone, label, count }: { tone: "success" | "info" | "warning" | "muted" | "danger"; label: string; count: number }) {
  const toneClass = {
    success: "border-[var(--analytics-success-border)] bg-[var(--analytics-success-soft)] text-[var(--analytics-success)]",
    info: "border-[var(--analytics-info-border)] bg-[var(--analytics-info-soft)] text-[var(--analytics-info)]",
    warning: "border-[var(--analytics-warning-border)] bg-[var(--analytics-warning-soft)] text-[var(--analytics-warning)]",
    danger: "border-[var(--analytics-danger-border)] bg-[var(--analytics-danger-soft)] text-[var(--analytics-danger)]",
    muted: "border-[var(--border)] bg-[var(--surface-softer)] text-[var(--muted-foreground)]",
  }[tone];
  return (
    <span className={cn("inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium", toneClass)}>
      {label}
      <span className="tabular-nums">{count}</span>
    </span>
  );
}
