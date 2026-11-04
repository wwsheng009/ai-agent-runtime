import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";

import { formatNumber } from "./format";
import type { RequestBreakdownRow, RequestDimension } from "./request-analysis-data";
import { cacheStatusLabel } from "./request-labels";

export function RequestBreakdown({
  title,
  rows,
  dimension,
}: {
  title: string;
  rows: RequestBreakdownRow[];
  dimension: RequestDimension;
}) {
  const { t } = useTranslation("usageAnalytics");
  const visibleRows = rows.slice(0, 6);
  const maxRequests = visibleRows[0]?.requests ?? 0;

  return (
    <article className="min-w-0 rounded-panel border border-border bg-surface-softer p-3">
      <div className="flex items-baseline justify-between gap-2">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {title}
        </h3>
        <span className="text-xs text-muted-foreground">
          {t("requestAnalysis.breakdown.total", { value: formatNumber(rows.length) })}
        </span>
      </div>
      {visibleRows.length === 0 ? (
        <p className="mt-4 text-xs text-muted-foreground">
          {t("requestAnalysis.breakdown.empty")}
        </p>
      ) : (
        <ul className="mt-3 space-y-2.5">
          {visibleRows.map((row) => {
            const label = dimension === "cache_status"
              ? cacheStatusLabel(t, row.key)
              : row.key || t(`requestAnalysis.breakdown.unknown.${dimension}`);
            return (
              <li key={row.key || "unknown"} className="min-w-0">
                <div className="flex items-baseline justify-between gap-2 text-xs">
                  <span className="min-w-0 truncate font-medium" title={label}>
                    {label}
                  </span>
                  <span className="shrink-0 tabular-nums text-muted-foreground">
                    {t("requestAnalysis.breakdown.count", {
                      value: formatNumber(row.requests),
                    })}
                  </span>
                </div>
                <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-surface-solid">
                  <div
                    className="h-full rounded-full bg-accent-primary"
                    style={{
                      width: `${maxRequests > 0 ? Math.max(4, (row.requests / maxRequests) * 100) : 0}%`,
                    }}
                  />
                </div>
                {dimension === "cache_status" ? null : (
                  <div className="mt-1 flex flex-wrap gap-x-3 text-xs text-muted-foreground">
                    <span>
                      {t("requestAnalysis.breakdown.tokens", {
                        value: formatNumber(row.total_tokens),
                      })}
                    </span>
                    <span className={cn(row.failures > 0 && "text-analytics-danger")}>
                      {t("requestAnalysis.breakdown.failures", {
                        value: formatNumber(row.failures),
                      })}
                    </span>
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </article>
  );
}
