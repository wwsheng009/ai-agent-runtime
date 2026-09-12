// 由 pages/cache-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { CacheRequestRecord } from "@/types/runtime";

import { cacheStatusKey, cacheStatusTone } from "./cache-status";
import {
  formatCacheNumber,
  formatCacheRatio,
  formatCacheReportedNumber,
  formatCacheTime,
} from "./format";

export function RequestsTable({ requests, total, offset, pageSize, loading, onPage, onTrace }: {
  requests: CacheRequestRecord[];
  total: number;
  offset: number;
  pageSize: number;
  loading: boolean;
  onPage: (offset: number) => void;
  onTrace: (record: CacheRequestRecord) => void;
}) {
  const { t } = useTranslation("usageAnalytics");
  const canPrev = offset > 0;
  const canNext = offset + pageSize < total;
  return (
    <section aria-label={t("cache.requestsTitle")} className="surface-panel rounded-[0.95rem] p-3 sm:p-4">
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-sm font-semibold">{t("cache.requestsTitle")}</h2>
        <div className="flex items-center gap-1 text-xs text-[var(--muted-foreground)]">
          <Button variant="ghost" size="sm" disabled={!canPrev || loading} onClick={() => onPage(Math.max(0, offset - pageSize))}>
            {t("sessions.previous")}
          </Button>
          <span className="tabular-nums">{t("sessions.pageRange", { from: String(total === 0 ? 0 : offset + 1), to: String(Math.min(total, offset + pageSize)), total: String(total) })}</span>
          <Button variant="ghost" size="sm" disabled={!canNext || loading} onClick={() => onPage(offset + pageSize)}>
            {t("sessions.next")}
          </Button>
        </div>
      </div>

      <div className="mt-2 w-full max-w-full overflow-x-auto">
        <table className="w-full min-w-[1080px] border-collapse text-left text-sm">
          <thead className="text-xs text-[var(--muted-foreground)]">
            <tr className="border-b border-[var(--border)]">
              <th className="px-2 py-2 font-medium">{t("cache.columns.time")}</th>
              <th className="px-2 py-2 font-medium">{t("cache.columns.providerModel")}</th>
              <th className="px-2 py-2 font-medium">{t("cache.columns.message")}</th>
              <th className="px-2 py-2 text-right font-medium">{t("cache.columns.tokens")}</th>
              <th className="px-2 py-2 text-right font-medium">{t("cache.columns.cacheRead")}</th>
              <th className="px-2 py-2 text-right font-medium">{t("cache.columns.cacheWrite")}</th>
              <th className="px-2 py-2 text-right font-medium">{t("cache.columns.hitRatio")}</th>
              <th className="px-2 py-2 font-medium">{t("cache.columns.cacheStatus")}</th>
            </tr>
          </thead>
          <tbody>
            {requests.length === 0 && !loading ? (
              <tr>
                <td colSpan={8} className="px-2 py-8 text-center text-sm text-[var(--muted-foreground)]">
                  {t("cache.emptyRequests")}
                </td>
              </tr>
            ) : null}
            {requests.map((record) => {
              const messageId = record.assistant_message_id || record.user_message_id || "";
              const traceable = Boolean(messageId);
              return (
                <tr
                  key={record.llm_request_id}
                  className={cn(
                    "border-b border-[var(--border)] last:border-b-0",
                    traceable && "cursor-pointer hover:bg-[var(--surface-soft)]",
                  )}
                  title={traceable ? t("cache.traceHint") : undefined}
                  onClick={traceable ? () => onTrace(record) : undefined}
                >
                  <td className="whitespace-nowrap px-2 py-2.5 tabular-nums">{formatCacheTime(record.started_at)}</td>
                  <td className="max-w-52 px-2 py-2.5">
                    <div className="truncate">{record.provider || "-"}</div>
                    <div className="truncate text-xs text-[var(--muted-foreground)]">{record.model || "-"}</div>
                  </td>
                  <td className="max-w-56 px-2 py-2.5">
                    {messageId ? (
                      <div className="truncate font-mono text-xs" title={messageId}>{messageId}</div>
                    ) : (
                      <span className="text-xs text-[var(--muted-foreground)]">-</span>
                    )}
                    {record.correlation_source === "history_inferred" ? (
                      <Badge className="ml-1 border-[var(--analytics-info-border)] bg-[var(--analytics-info-soft)] text-[var(--analytics-info)]">{t("cache.inferredBadge")}</Badge>
                    ) : null}
                  </td>
                  <td className="whitespace-nowrap px-2 py-2.5 text-right tabular-nums">
                    {formatCacheNumber(record.usage?.prompt_tokens ?? 0)} / {formatCacheNumber(record.usage?.completion_tokens ?? 0)}
                  </td>
                  <td className="whitespace-nowrap px-2 py-2.5 text-right tabular-nums">{formatCacheReportedNumber(record.usage?.cache_read_tokens, record.usage?.cache_read_reported)}</td>
                  <td className="whitespace-nowrap px-2 py-2.5 text-right tabular-nums">{formatCacheReportedNumber(record.usage?.cache_creation_tokens, record.usage?.cache_creation_reported)}</td>
                  <td className="whitespace-nowrap px-2 py-2.5 text-right tabular-nums">{formatCacheRatio(record.cache_hit_ratio)}</td>
                  <td className="px-2 py-2.5">
                    <Badge className={cacheStatusTone(record.cache_status)}>{t(cacheStatusKey(record.cache_status))}</Badge>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}
