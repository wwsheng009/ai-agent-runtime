// 由 pages/cache-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { cn } from "@/lib/utils";

export function DistributionChip({ tone, label, count }: { tone: "success" | "info" | "warning" | "muted" | "danger"; label: string; count: number }) {
  const toneClass = {
    success: "border-analytics-success-border bg-analytics-success-soft text-analytics-success",
    info: "border-analytics-info-border bg-analytics-info-soft text-analytics-info",
    warning: "border-analytics-warning-border bg-analytics-warning-soft text-analytics-warning",
    danger: "border-analytics-danger-border bg-analytics-danger-soft text-analytics-danger",
    muted: "border-border bg-surface-softer text-muted-foreground",
  }[tone];
  return (
    <span className={cn("inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium", toneClass)}>
      {label}
      <span className="tabular-nums">{count}</span>
    </span>
  );
}
