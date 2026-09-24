// P0-2 拆分：路由分布桶列表（原 routing-observability-panel.tsx L568-L606）。

import { formatNumber } from "./format";

export function DistributionList({
  title,
  empty,
  entries,
}: {
  title: string;
  empty: string;
  entries: { key: string; count: number; label: string; detail?: string }[];
}) {
  const max = entries.reduce((current, entry) => Math.max(current, entry.count), 0);
  return (
    <div className="rounded-card border border-border bg-surface-softer p-3">
      <div className="mb-2 text-xs font-medium text-muted-foreground">{title}</div>
      {entries.length === 0 ? (
        <div className="py-2 text-xs text-muted-foreground">{empty}</div>
      ) : (
        <ul className="space-y-1.5">
          {entries.map((entry) => (
            <li key={entry.key} className="min-w-0">
              <div className="flex items-baseline justify-between gap-2 text-xs">
                <span className="min-w-0 truncate" title={entry.label}>{entry.label}</span>
                <span className="shrink-0 tabular-nums text-muted-foreground">{formatNumber(entry.count)}</span>
              </div>
              <div className="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-surface-soft">
                <div
                  className="h-full rounded-full bg-accent-primary"
                  style={{ width: max > 0 ? `${Math.max(4, Math.round((entry.count / max) * 100))}%` : "0%" }}
                />
              </div>
              {entry.detail ? (
                <div className="mt-0.5 text-[0.7rem] text-muted-foreground">{entry.detail}</div>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
