// 由 pages/cache-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { RefreshCwIcon, XIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { CacheMessageTrace } from "@/types/runtime";

import { cacheStatusKey } from "./cache-status";
import { formatCacheNumber, formatCacheRatio } from "./format";

export function TraceSidebar({ trace, loading, error, onClose }: {
  trace: CacheMessageTrace | null;
  loading: boolean;
  error: string | null;
  onClose: () => void;
}) {
  const { t } = useTranslation("usageAnalytics");
  return (
    <aside
      role="dialog"
      aria-label={t("cache.trace.title")}
      className="fixed inset-y-0 right-0 z-40 flex w-full max-w-md flex-col border-l border-border bg-[var(--surface)] shadow-[0_0_40px_rgba(0,0,0,0.25)]"
    >
      <div className="flex items-center justify-between border-b border-border px-4 py-3">
        <h2 className="text-sm font-semibold">{t("cache.trace.title")}</h2>
        <Button variant="ghost" size="sm" onClick={onClose} aria-label={t("cache.trace.close")}>
          <XIcon size={14} />
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3 text-sm">
        {loading ? (
          <div className="flex items-center gap-2 text-muted-foreground">
            <RefreshCwIcon size={14} className="animate-spin" />
            {t("loading")}
          </div>
        ) : error ? (
          <div role="alert" className="rounded-panel border border-analytics-danger-border bg-analytics-danger-soft px-3 py-2.5 text-analytics-danger">
            {t("loadError")}: {error}
          </div>
        ) : trace ? (
          <div className="flex flex-col gap-3">
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">{t("cache.trace.message")}</div>
              <div className="mt-1 break-all font-mono text-xs">{trace.message_id}</div>
              <div className="mt-1 flex flex-wrap items-center gap-1.5">
                {trace.message_role ? <Badge>{trace.message_role}</Badge> : null}
                {trace.turn_id ? <Badge>{t("cache.trace.turn", { id: trace.turn_id })}</Badge> : null}
                {trace.correlation_source === "history_inferred" ? (
                  <Badge className="border-analytics-info-border bg-analytics-info-soft text-analytics-info">{t("cache.inferredBadge")}</Badge>
                ) : null}
              </div>
            </div>

            {trace.produced_by ? (
              <div className="rounded-panel border border-border p-3">
                <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">{t("cache.trace.producedBy")}</div>
                <div className="mt-1 break-all font-mono text-xs">{trace.produced_by.llm_request_id}</div>
                <dl className="mt-2 grid grid-cols-2 gap-x-3 gap-y-1.5 text-xs">
                  <dt className="text-muted-foreground">{t("cache.columns.cacheStatus")}</dt>
                  <dd>{trace.produced_by.cache_status ? t(cacheStatusKey(trace.produced_by.cache_status)) : "-"}</dd>
                  <dt className="text-muted-foreground">{t("cache.columns.hitRatio")}</dt>
                  <dd className="tabular-nums">{formatCacheRatio(trace.produced_by.cache_hit_ratio)}</dd>
                  <dt className="text-muted-foreground">{t("cache.trace.prompt")}</dt>
                  <dd className="tabular-nums">{formatCacheNumber(trace.produced_by.usage?.prompt_tokens ?? 0)}</dd>
                  <dt className="text-muted-foreground">{t("cache.trace.completion")}</dt>
                  <dd className="tabular-nums">{formatCacheNumber(trace.produced_by.usage?.completion_tokens ?? 0)}</dd>
                  <dt className="text-muted-foreground">{t("cache.columns.cacheRead")}</dt>
                  <dd className="tabular-nums">{formatCacheNumber(trace.produced_by.usage?.cache_read_tokens ?? 0)}</dd>
                  <dt className="text-muted-foreground">{t("cache.columns.cacheWrite")}</dt>
                  <dd className="tabular-nums">{formatCacheNumber(trace.produced_by.usage?.cache_creation_tokens ?? 0)}</dd>
                </dl>
              </div>
            ) : null}

            <div>
              <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">{t("cache.trace.consumedBy")}</div>
              {(trace.consumed_by ?? []).length === 0 ? (
                <p className="mt-1 text-xs text-muted-foreground">{t("cache.trace.noConsumers")}</p>
              ) : (
                <ul className="mt-1 flex flex-col gap-1.5">
                  {(trace.consumed_by ?? []).map((consumer) => (
                    <li key={`${consumer.llm_request_id}-${consumer.step ?? 0}`} className="flex items-center justify-between gap-2 rounded-field border border-border px-2.5 py-1.5 text-xs">
                      <span className="min-w-0 break-all font-mono">{consumer.llm_request_id}</span>
                      <span className="shrink-0 tabular-nums text-muted-foreground">
                        {consumer.cache_status ? t(cacheStatusKey(consumer.cache_status)) : ""}
                        {consumer.cache_hit_ratio !== undefined ? ` · ${formatCacheRatio(consumer.cache_hit_ratio)}` : ""}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </div>

            <div className="rounded-panel border border-border p-3 text-xs">
              <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">{t("cache.trace.neighbors")}</div>
              <div className="mt-1.5 flex flex-col gap-1">
                <div className="break-all"><span className="text-muted-foreground">{t("cache.trace.prev")}: </span><span className="font-mono">{trace.neighbors.prev_message_id || "-"}</span></div>
                <div className="break-all"><span className="text-muted-foreground">{t("cache.trace.next")}: </span><span className="font-mono">{trace.neighbors.next_message_id || "-"}</span></div>
              </div>
              {trace.history_available ? (
                <p className="mt-2 text-muted-foreground">{t("cache.trace.historyAvailable")}</p>
              ) : null}
            </div>
          </div>
        ) : null}
      </div>
    </aside>
  );
}
