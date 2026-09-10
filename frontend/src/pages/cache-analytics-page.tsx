import {
  AlertTriangleIcon,
  ArrowLeftIcon,
  DatabaseIcon,
  RefreshCwIcon,
  XIcon,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
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

function CacheMetric({ label, value, detail }: { label: string; value: string; detail: string }) {
  return (
    <div className="surface-panel rounded-[0.9rem] px-3 py-2.5">
      <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">{label}</div>
      <div className="mt-1 truncate text-2xl font-semibold tracking-[-0.03em] tabular-nums">{value}</div>
      <div className="mt-0.5 truncate text-xs text-[var(--muted-foreground)]">{detail}</div>
    </div>
  );
}

function DistributionChip({ tone, label, count }: { tone: "success" | "info" | "warning" | "muted" | "danger"; label: string; count: number }) {
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

function cacheStatusTone(status: string) {
  switch (status) {
    case "hit": return "border-[var(--analytics-success-border)] bg-[var(--analytics-success-soft)] text-[var(--analytics-success)]";
    case "write": return "border-[var(--analytics-info-border)] bg-[var(--analytics-info-soft)] text-[var(--analytics-info)]";
    case "reported_zero": return "border-[var(--analytics-warning-border)] bg-[var(--analytics-warning-soft)] text-[var(--analytics-warning)]";
    case "error": return "border-[var(--analytics-danger-border)] bg-[var(--analytics-danger-soft)] text-[var(--analytics-danger)]";
    default: return "border-[var(--border)] bg-[var(--surface-softer)] text-[var(--muted-foreground)]";
  }
}

function RequestsTable({ requests, total, offset, pageSize, loading, onPage, onTrace }: {
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

function TraceSidebar({ trace, loading, error, onClose }: {
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
      className="fixed inset-y-0 right-0 z-40 flex w-full max-w-md flex-col border-l border-[var(--border)] bg-[var(--surface)] shadow-[0_0_40px_rgba(0,0,0,0.25)]"
    >
      <div className="flex items-center justify-between border-b border-[var(--border)] px-4 py-3">
        <h2 className="text-sm font-semibold">{t("cache.trace.title")}</h2>
        <Button variant="ghost" size="sm" onClick={onClose} aria-label={t("cache.trace.close")}>
          <XIcon size={14} />
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3 text-sm">
        {loading ? (
          <div className="flex items-center gap-2 text-[var(--muted-foreground)]">
            <RefreshCwIcon size={14} className="animate-spin" />
            {t("loading")}
          </div>
        ) : error ? (
          <div role="alert" className="rounded-[0.9rem] border border-[var(--analytics-danger-border)] bg-[var(--analytics-danger-soft)] px-3 py-2.5 text-[var(--analytics-danger)]">
            {t("loadError")}: {error}
          </div>
        ) : trace ? (
          <div className="flex flex-col gap-3">
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">{t("cache.trace.message")}</div>
              <div className="mt-1 break-all font-mono text-xs">{trace.message_id}</div>
              <div className="mt-1 flex flex-wrap items-center gap-1.5">
                {trace.message_role ? <Badge>{trace.message_role}</Badge> : null}
                {trace.turn_id ? <Badge>{t("cache.trace.turn", { id: trace.turn_id })}</Badge> : null}
                {trace.correlation_source === "history_inferred" ? (
                  <Badge className="border-[var(--analytics-info-border)] bg-[var(--analytics-info-soft)] text-[var(--analytics-info)]">{t("cache.inferredBadge")}</Badge>
                ) : null}
              </div>
            </div>

            {trace.produced_by ? (
              <div className="rounded-[0.9rem] border border-[var(--border)] p-3">
                <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">{t("cache.trace.producedBy")}</div>
                <div className="mt-1 break-all font-mono text-xs">{trace.produced_by.llm_request_id}</div>
                <dl className="mt-2 grid grid-cols-2 gap-x-3 gap-y-1.5 text-xs">
                  <dt className="text-[var(--muted-foreground)]">{t("cache.columns.cacheStatus")}</dt>
                  <dd>{trace.produced_by.cache_status ? t(cacheStatusKey(trace.produced_by.cache_status)) : "-"}</dd>
                  <dt className="text-[var(--muted-foreground)]">{t("cache.columns.hitRatio")}</dt>
                  <dd className="tabular-nums">{formatCacheRatio(trace.produced_by.cache_hit_ratio)}</dd>
                  <dt className="text-[var(--muted-foreground)]">{t("cache.trace.prompt")}</dt>
                  <dd className="tabular-nums">{formatCacheNumber(trace.produced_by.usage?.prompt_tokens ?? 0)}</dd>
                  <dt className="text-[var(--muted-foreground)]">{t("cache.trace.completion")}</dt>
                  <dd className="tabular-nums">{formatCacheNumber(trace.produced_by.usage?.completion_tokens ?? 0)}</dd>
                  <dt className="text-[var(--muted-foreground)]">{t("cache.columns.cacheRead")}</dt>
                  <dd className="tabular-nums">{formatCacheNumber(trace.produced_by.usage?.cache_read_tokens ?? 0)}</dd>
                  <dt className="text-[var(--muted-foreground)]">{t("cache.columns.cacheWrite")}</dt>
                  <dd className="tabular-nums">{formatCacheNumber(trace.produced_by.usage?.cache_creation_tokens ?? 0)}</dd>
                </dl>
              </div>
            ) : null}

            <div>
              <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">{t("cache.trace.consumedBy")}</div>
              {(trace.consumed_by ?? []).length === 0 ? (
                <p className="mt-1 text-xs text-[var(--muted-foreground)]">{t("cache.trace.noConsumers")}</p>
              ) : (
                <ul className="mt-1 flex flex-col gap-1.5">
                  {(trace.consumed_by ?? []).map((consumer) => (
                    <li key={`${consumer.llm_request_id}-${consumer.step ?? 0}`} className="flex items-center justify-between gap-2 rounded-[0.7rem] border border-[var(--border)] px-2.5 py-1.5 text-xs">
                      <span className="min-w-0 break-all font-mono">{consumer.llm_request_id}</span>
                      <span className="shrink-0 tabular-nums text-[var(--muted-foreground)]">
                        {consumer.cache_status ? t(cacheStatusKey(consumer.cache_status)) : ""}
                        {consumer.cache_hit_ratio !== undefined ? ` · ${formatCacheRatio(consumer.cache_hit_ratio)}` : ""}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </div>

            <div className="rounded-[0.9rem] border border-[var(--border)] p-3 text-xs">
              <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">{t("cache.trace.neighbors")}</div>
              <div className="mt-1.5 flex flex-col gap-1">
                <div className="break-all"><span className="text-[var(--muted-foreground)]">{t("cache.trace.prev")}: </span><span className="font-mono">{trace.neighbors.prev_message_id || "-"}</span></div>
                <div className="break-all"><span className="text-[var(--muted-foreground)]">{t("cache.trace.next")}: </span><span className="font-mono">{trace.neighbors.next_message_id || "-"}</span></div>
              </div>
              {trace.history_available ? (
                <p className="mt-2 text-[var(--muted-foreground)]">{t("cache.trace.historyAvailable")}</p>
              ) : null}
            </div>
          </div>
        ) : null}
      </div>
    </aside>
  );
}

type CacheStatusI18nKey =
  | "cache.status.hit"
  | "cache.status.write"
  | "cache.status.reportedZero"
  | "cache.status.notReported"
  | "cache.status.error";

function cacheStatusKey(status: string): CacheStatusI18nKey {
  switch (status) {
    case "hit": return "cache.status.hit";
    case "write": return "cache.status.write";
    case "reported_zero": return "cache.status.reportedZero";
    case "not_reported": return "cache.status.notReported";
    case "error": return "cache.status.error";
    default: return "cache.status.notReported";
  }
}

function formatCacheNumber(value: number) {
  if (!Number.isFinite(value)) return "0";
  return new Intl.NumberFormat().format(value);
}

/** 未上报（not_reported）时显示 "—" 而非 0，避免误读（§6.1 降级语义）。 */
function formatCacheReportedNumber(value: number | undefined, reported: boolean | undefined) {
  if (!reported || value === undefined) return "—";
  return formatCacheNumber(value);
}

function formatCacheRatio(value: number | undefined) {
  if (value === undefined || value === null || !Number.isFinite(value)) return "—";
  return `${Math.round(value * 100)}%`;
}

function formatCacheTime(value: string | undefined) {
  if (!value) return "-";
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed)) return value;
  return new Date(parsed).toLocaleString();
}
