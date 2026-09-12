// 由 pages/cache-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  AlertTriangleIcon,
  ArrowLeftIcon,
  DatabaseIcon,
  RefreshCwIcon,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { Select } from "@/components/ui/select";
import {
  getCacheCapabilities,
  getCacheOverview,
  getCacheRequests,
  getMessageTrace,
  listRuntimeSessions,
} from "@/lib/runtime-api";
import { cn } from "@/lib/utils";
import type {
  CacheMessageTrace,
  CacheOverview,
  CacheRequestRecord,
  CacheStatusDistribution,
  RuntimeSessionRecord,
} from "@/types/runtime";

import { CacheMetric } from "./cache-analytics/cache-metric";
import { DistributionChip } from "./cache-analytics/distribution-chip";
import {
  formatCacheNumber,
  formatCacheRatio,
  formatCacheTime,
} from "./cache-analytics/format";
import { RequestsTable } from "./cache-analytics/requests-table";
import { TraceSidebar } from "./cache-analytics/trace-sidebar";

// LLM 缓存分析视图（cache.analytics.v1，方案 §6.2）：
// 总览卡片 + 请求明细表 + 消息追溯侧栏。数据源为
// /api/runtime/sessions/{id}/cache/*（session 显式在路径，W2）。

const requestPageSize = 50;

const emptyDistribution: CacheStatusDistribution = {
  hit: 0,
  write: 0,
  reported_zero: 0,
  not_reported: 0,
  error: 0,
};

export function CacheAnalyticsView({ sessionId }: { sessionId?: string | null }) {
  const { t } = useTranslation("usageAnalytics");
  const [sessions, setSessions] = useState<RuntimeSessionRecord[]>([]);
  const [selectedSessionId, setSelectedSessionId] = useState(sessionId ?? "");
  const [overview, setOverview] = useState<CacheOverview | null>(null);
  const [maxRequests, setMaxRequests] = useState(0);
  const [requests, setRequests] = useState<CacheRequestRecord[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [trace, setTrace] = useState<CacheMessageTrace | null>(null);
  const [traceLoading, setTraceLoading] = useState(false);
  const [traceError, setTraceError] = useState<string | null>(null);

  // 路由参数变化时同步选中会话。
  useEffect(() => {
    if (sessionId) {
      setSelectedSessionId(sessionId);
    }
  }, [sessionId]);

  useEffect(() => {
    let cancelled = false;
    listRuntimeSessions()
      .then((response) => {
        if (cancelled) return;
        setSessions(response.sessions ?? []);
      })
      .catch(() => {
        // 会话列表加载失败不阻塞视图：仍可手动输入/路由指定会话 id。
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const load = useCallback(async () => {
    const sid = selectedSessionId.trim();
    if (!sid) return;
    setLoading(true);
    setError(null);
    try {
      const [capabilities, overviewData, requestsData] = await Promise.all([
        getCacheCapabilities(sid),
        getCacheOverview(sid),
        getCacheRequests(sid, { limit: requestPageSize, offset }),
      ]);
      setMaxRequests(capabilities.max_requests_per_session ?? 0);
      setOverview(overviewData);
      setRequests(requestsData.requests ?? []);
      setTotal(requestsData.total ?? 0);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setLoading(false);
    }
  }, [offset, selectedSessionId]);

  useEffect(() => { void load(); }, [load]);

  const selectSession = useCallback((next: string) => {
    setTrace(null);
    setTraceError(null);
    setOffset(0);
    setSelectedSessionId(next);
  }, []);

  const openTrace = useCallback(async (record: CacheRequestRecord) => {
    const messageId = record.assistant_message_id || record.user_message_id;
    if (!messageId) return;
    setTraceLoading(true);
    setTraceError(null);
    setTrace(null);
    try {
      setTrace(await getMessageTrace(selectedSessionId, messageId));
    } catch (caught) {
      setTraceError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setTraceLoading(false);
    }
  }, [selectedSessionId]);

  const distribution = overview?.cache_status_distribution ?? emptyDistribution;
  const partial = overview?.coverage.partial ?? false;

  const sessionOptions = useMemo(() => sessions.map((session) => ({
    value: session.id,
    label: session.metadata?.title?.trim() || session.id,
  })), [sessions]);

  return (
    <div className="min-h-screen min-w-0 overflow-x-hidden bg-[var(--workspace-shell-bg)] text-[var(--foreground)]">
      <div className="mx-auto flex min-h-screen w-full max-w-[1760px] flex-col gap-2 px-2.5 py-2.5 sm:px-3">
        <header className="surface-panel relative overflow-hidden rounded-[0.95rem] px-3 py-3 sm:px-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex min-w-0 items-center gap-2">
              <Link
                to="/usage"
                className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
                aria-label={t("cache.backToUsage")}
              >
                <ArrowLeftIcon size={14} />
                <span className="hidden sm:inline">{t("cache.backToUsage")}</span>
              </Link>
              <div className="min-w-0">
                <h1 className="truncate text-base font-semibold">{t("cache.title")}</h1>
                <p className="mt-0.5 text-xs text-[var(--muted-foreground)]">{t("cache.description")}</p>
              </div>
            </div>
            <div className="flex items-center gap-2">
              <div className="w-64 min-w-0">
                <Select
                  ariaLabel={t("cache.sessionSelect")}
                  value={selectedSessionId}
                  options={sessionOptions}
                  onChange={selectSession}
                  triggerClassName="h-9 rounded-[0.7rem]"
                />
              </div>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => void load()}
                disabled={loading || !selectedSessionId.trim()}
              >
                <RefreshCwIcon size={14} className={cn(loading && "animate-spin")} />
                {t("actions.refresh")}
              </Button>
            </div>
          </div>
        </header>

        <main className="flex min-w-0 flex-1 flex-col gap-2">
          {!selectedSessionId.trim() ? (
            <div className="surface-panel flex min-h-60 flex-col items-center justify-center gap-2 rounded-[0.95rem] text-sm text-[var(--muted-foreground)]">
              <DatabaseIcon size={20} />
              {t("cache.selectSessionFirst")}
            </div>
          ) : error ? (
            <div role="alert" className="rounded-[0.9rem] border border-[var(--analytics-danger-border)] bg-[var(--analytics-danger-soft)] px-3 py-2.5 text-sm text-[var(--analytics-danger)]">
              {t("loadError")}: {error}
            </div>
          ) : (
            <>
              {partial ? (
                <div className="flex items-start gap-2 rounded-[0.9rem] border border-[var(--analytics-warning-border)] bg-[var(--analytics-warning-soft)] px-3 py-2.5 text-sm text-[var(--analytics-warning)]">
                  <AlertTriangleIcon size={16} className="mt-0.5 shrink-0" />
                  <span>{t("cache.partialBanner", { max: String(maxRequests) })}</span>
                </div>
              ) : null}

              <section aria-label={t("cache.overviewTitle")} className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-6">
                <CacheMetric label={t("cache.metrics.requests")} value={formatCacheNumber(overview?.requests_total ?? 0)} detail={t("cache.metrics.withUsage", { count: overview?.requests_with_usage ?? 0 })} />
                <CacheMetric label={t("cache.metrics.hitRatio")} value={formatCacheRatio(overview?.cache_hit_ratio)} detail={t("cache.metrics.writeRatio", { value: formatCacheRatio(overview?.cache_write_ratio) })} />
                <CacheMetric label={t("cache.metrics.cacheRead")} value={formatCacheNumber(overview?.tokens.cache_read_tokens ?? 0)} detail={t("cache.metrics.cacheWrite", { value: formatCacheNumber(overview?.tokens.cache_creation_tokens ?? 0) })} />
                <CacheMetric label={t("cache.metrics.tokens")} value={formatCacheNumber(overview?.tokens.total_tokens ?? 0)} detail={t("cache.metrics.tokenBreakdown", { prompt: formatCacheNumber(overview?.tokens.prompt_tokens ?? 0), completion: formatCacheNumber(overview?.tokens.completion_tokens ?? 0) })} />
                <CacheMetric label={t("cache.metrics.reasoning")} value={formatCacheNumber(overview?.tokens.reasoning_tokens ?? 0)} detail={t("cache.metrics.cacheReported", { count: overview?.requests_cache_reported ?? 0 })} />
                <CacheMetric label={t("cache.metrics.generatedAt")} value={formatCacheTime(overview?.generated_at)} detail={t("cache.metrics.dataSourceLive")} />
              </section>

              <section aria-label={t("cache.distributionTitle")} className="surface-panel rounded-[0.95rem] p-3 sm:p-4">
                <h2 className="text-sm font-semibold">{t("cache.distributionTitle")}</h2>
                <div className="mt-2 flex flex-wrap gap-2">
                  <DistributionChip tone="success" label={t("cache.status.hit")} count={distribution.hit} />
                  <DistributionChip tone="info" label={t("cache.status.write")} count={distribution.write} />
                  <DistributionChip tone="warning" label={t("cache.status.reportedZero")} count={distribution.reported_zero} />
                  <DistributionChip tone="muted" label={t("cache.status.notReported")} count={distribution.not_reported} />
                  <DistributionChip tone="danger" label={t("cache.status.error")} count={distribution.error} />
                </div>
              </section>

              <RequestsTable
                requests={requests}
                total={total}
                offset={offset}
                pageSize={requestPageSize}
                loading={loading}
                onPage={setOffset}
                onTrace={(record) => void openTrace(record)}
              />
            </>
          )}
        </main>

        {trace || traceLoading || traceError ? (
          <TraceSidebar
            trace={trace}
            loading={traceLoading}
            error={traceError}
            onClose={() => {
              setTrace(null);
              setTraceError(null);
            }}
          />
        ) : null}
      </div>
    </div>
  );
}
