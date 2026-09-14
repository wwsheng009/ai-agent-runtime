// 由 pages/usage-analytics/quota.tsx 机械拆分而来（P0-2 A2 复检处置），仅搬迁不改语义：
// 用量 / 配额面板的原子展示组件（无状态、无副作用）。

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

import { formatNumber } from "./format";

export function PolicyBadge({ label, enabled }: { label: string; enabled: boolean }) {
  return (
    <Badge
      className={cn(
        enabled
          ? "border-analytics-success-border bg-analytics-success-soft text-analytics-success"
          : "border-border bg-surface-softer text-muted-foreground",
      )}
    >
      {label}
    </Badge>
  );
}

export function StatRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex min-w-0 items-baseline justify-between gap-2">
      <dt className="truncate text-muted-foreground">{label}</dt>
      <dd className="shrink-0 font-medium tabular-nums">{value}</dd>
    </div>
  );
}

export function QuotaBar({
  label,
  remaining,
  limit,
  unlimitedLabel,
}: {
  label: string;
  remaining: number;
  limit: number;
  unlimitedLabel: string;
}) {
  if (limit <= 0) {
    return (
      <div className="space-y-1">
        <div className="flex items-baseline justify-between gap-2 text-xs">
          <span className="text-muted-foreground">{label}</span>
          <span className="font-medium">{unlimitedLabel}</span>
        </div>
      </div>
    );
  }

  const used = Math.max(0, limit - Math.max(0, remaining));
  const usedRatio = Math.min(1, used / limit);
  return (
    <div className="space-y-1">
      <div className="flex items-baseline justify-between gap-2 text-xs">
        <span className="text-muted-foreground">{label}</span>
        <span className="font-medium tabular-nums">
          {formatNumber(remaining)} / {formatNumber(limit)}
        </span>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full bg-surface-softer">
        <div
          className={cn(
            "h-full rounded-full",
            usedRatio >= 1
              ? "bg-analytics-danger"
              : usedRatio >= 0.8
                ? "bg-analytics-warning"
                : "bg-accent-primary",
          )}
          style={{ width: `${Math.round(usedRatio * 100)}%` }}
        />
      </div>
    </div>
  );
}
