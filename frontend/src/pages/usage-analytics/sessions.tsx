// 由 pages/usage-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { getAnalyticsSessionUsage } from "@/lib/runtime-api";
import { cn } from "@/lib/utils";
import { CacheAnalyticsPanel } from "@/pages/cache-analytics-page";
import type { AnalyticsDiagnostic, AnalyticsSessionRollup, AnalyticsSessionUsageDetail, AnalyticsTurnUsage } from "@/types/runtime";
import { AlertTriangleIcon, CheckCircle2Icon, ChevronLeftIcon, ChevronRightIcon, RefreshCwIcon } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useParams, useSearchParams } from "react-router-dom";

import { diagnosticDetailKey, diagnosticTitleKey, errorCategoryKey, errorRate, formatDimensionTick, formatDuration, formatNumber, formatPercent, formatTimestamp, outcomeKey, outcomeTone, readAdminToken, reconciliationKey, shortID, statusTone, titleSourceKey } from "./format";
import { ErrorPatternsPanel } from "./error-patterns-panel";
import { AnalyticsHeader, Metric, QualityBadge, QualityNotice, TabButton } from "./primitives";
import { SubagentStatsPanel } from "./subagent-stats-panel";
import { ToolStatsPanel } from "./tool-stats-panel";

// 批次 7.2：会话观测 tab 清单。tab 状态只存在 URL query（刷新/分享可复现），
// 面板数据在对应 tab 激活（组件挂载）时才拉取。
const sessionTabs = ["overview", "tokens", "tools", "subagents", "diagnostics"] as const;

type SessionTab = (typeof sessionTabs)[number];

function isSessionTab(value: string | null): value is SessionTab {
  return value !== null && (sessionTabs as readonly string[]).includes(value);
}

export function SessionTable({ sessions, total, loading, search, offset, pageSize, onPage }: {
  sessions: AnalyticsSessionRollup[];
  total: number;
  loading: boolean;
  search: string;
  offset: number;
  pageSize: number;
  onPage: (offset: number) => void;
}) {
  const { t } = useTranslation("usageAnalytics");
  return (
    <section aria-labelledby="usage-sessions-title" className="surface-panel min-w-0 rounded-panel-lg p-3 sm:p-4">
      <div className="mb-3 flex items-end justify-between gap-3">
        <div>
          <h2 id="usage-sessions-title" className="text-sm font-semibold">{t("sessions.title")}</h2>
          <p className="text-xs text-muted-foreground">{t("sessions.subtitle", { count: total })}</p>
        </div>
        {loading ? <RefreshCwIcon size={15} className="animate-spin text-muted-foreground" /> : null}
      </div>
      <div className="grid gap-2 lg:hidden">
        {sessions.length === 0 ? (
          <div className="rounded-card border border-border bg-surface-softer px-3 py-10 text-center text-sm text-muted-foreground">
            {loading ? t("loading") : t("sessions.empty")}
          </div>
        ) : sessions.map((session) => {
          const href = `/usage/sessions/${encodeURIComponent(session.session_id)}${search ? `?${search}` : ""}`;
          return (
            <article key={session.session_id} className="rounded-card-lg border border-border bg-surface-softer p-3">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <Link to={href} className="block truncate font-medium hover:text-accent-primary">
                    {session.title || t("sessions.untitled")}
                  </Link>
                  <div className="mt-0.5 truncate font-mono text-xs text-muted-foreground">
                    {shortID(session.session_id)}
                  </div>
                </div>
                <Badge className={statusTone(session.status)}>{session.status || t("status.unknown")}</Badge>
              </div>
              <dl className="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-4">
                <div>
                  <dt className="text-xs uppercase tracking-[0.12em] text-muted-foreground">{t("sessions.columns.provider")}</dt>
                  <dd className="mt-1 truncate text-sm" title={`${session.provider || "-"} / ${session.model || "-"}`}>{session.provider || "-"} / {session.model || "-"}</dd>
                </div>
                <div>
                  <dt className="text-xs uppercase tracking-[0.12em] text-muted-foreground">{t("sessions.columns.tokens")}</dt>
                  <dd className="mt-1 text-sm font-medium tabular-nums">{formatNumber(session.total_tokens)}</dd>
                </div>
                <div>
                  <dt className="text-xs uppercase tracking-[0.12em] text-muted-foreground">{t("sessions.columns.turns")}</dt>
                  <dd className="mt-1 text-sm font-medium tabular-nums">{formatNumber(session.turn_count)}</dd>
                </div>
                <div>
                  <dt className="text-xs uppercase tracking-[0.12em] text-muted-foreground">{t("sessions.columns.coverage")}</dt>
                  <dd className="mt-1"><QualityBadge quality={session.usage_quality} coverage={session.usage_coverage} partial={session.partial} /></dd>
                </div>
              </dl>
              <div className="mt-3 flex items-center justify-between border-t border-border pt-2 text-xs text-muted-foreground">
                <span>{formatTimestamp(session.start_time)}</span>
                <Link to={href} className={cn(buttonVariants({ variant: "ghost", size: "sm" }), "h-7 px-2")} aria-label={t("sessions.open", { id: session.session_id })}>
                  {t("detail.title")}
                  <ChevronRightIcon size={14} />
                </Link>
              </div>
            </article>
          );
        })}
      </div>

      <div className="hidden w-full max-w-full overflow-x-auto rounded-card border border-border lg:block">
        <table className="w-full min-w-[1100px] border-collapse text-left text-sm">
          <thead className="bg-surface-softer text-xs text-muted-foreground">
            <tr className="border-b border-border">
              <th className="px-3 py-2 font-medium">{t("sessions.columns.session")}</th>
              <th className="px-3 py-2 font-medium">{t("sessions.columns.title")}</th>
              <th className="px-3 py-2 font-medium">{t("sessions.columns.provider")}</th>
              <th className="px-3 py-2 font-medium">{t("sessions.columns.project")}</th>
              <th className="px-3 py-2 font-medium">{t("sessions.columns.tokens")}</th>
              <th className="px-3 py-2 font-medium">{t("sessions.columns.turns")}</th>
              <th className="px-3 py-2 font-medium">{t("sessions.columns.errors")}</th>
              <th className="px-3 py-2 font-medium">{t("sessions.columns.coverage")}</th>
              <th className="px-3 py-2 font-medium">{t("sessions.columns.status")}</th>
              <th className="relative w-10 px-2 py-2"><span className="sr-only">{t("sessions.columns.action")}</span></th>
            </tr>
          </thead>
          <tbody>
            {sessions.length === 0 ? (
              <tr><td colSpan={10} className="px-3 py-10 text-center text-muted-foreground">{loading ? t("loading") : t("sessions.empty")}</td></tr>
            ) : sessions.map((session) => {
              const href = `/usage/sessions/${encodeURIComponent(session.session_id)}${search ? `?${search}` : ""}`;
              return (
                <tr key={session.session_id} className="border-b border-border last:border-b-0 hover:bg-surface-soft-hover">
                  <td className="px-3 py-2.5">
                    <Link to={href} className="font-medium hover:text-accent-primary">{shortID(session.session_id)}</Link>
                    <div className="mt-0.5 text-xs text-muted-foreground">{formatTimestamp(session.start_time)}</div>
                  </td>
                  <td className="max-w-72 px-3 py-2.5"><div className="truncate font-medium" title={session.title || session.session_id}>{session.title || t("sessions.untitled")}</div><div className="truncate text-xs text-muted-foreground">{t(titleSourceKey(session.title_source))}</div></td>
                  <td className="px-3 py-2.5"><div>{session.provider || "-"}</div><div className="max-w-44 truncate text-xs text-muted-foreground">{session.model || "-"}</div></td>
                  <td className="max-w-56 px-3 py-2.5"><div className="truncate" title={session.project}>{session.project ? formatDimensionTick(session.project, "project") : t("status.unknown")}</div><div className="truncate text-xs text-muted-foreground" title={session.project}>{session.project || "-"}</div></td>
                  <td className="px-3 py-2.5 tabular-nums">{formatNumber(session.total_tokens)}</td>
                  <td className="px-3 py-2.5 tabular-nums">{formatNumber(session.turn_count)}</td>
                  <td className="px-3 py-2.5"><span className={cn("tabular-nums", session.failed_turns > 0 && "text-analytics-danger")}>{formatNumber(session.failed_turns)}</span><div className="text-xs text-muted-foreground">{t("sessions.llmErrors", { count: session.llm_errors ?? 0 })}</div></td>
                  <td className="px-3 py-2.5"><QualityBadge quality={session.usage_quality} coverage={session.usage_coverage} partial={session.partial} /></td>
                  <td className="px-3 py-2.5"><Badge className={statusTone(session.status)}>{session.status || t("status.unknown")}</Badge></td>
                  <td className="px-2 py-2.5"><Link to={href} className={cn(buttonVariants({ variant: "ghost", size: "sm" }), "h-8 w-8 px-0")} aria-label={t("sessions.open", { id: session.session_id })}><ChevronRightIcon size={15} /></Link></td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <div className="mt-2 flex items-center justify-between gap-3 text-xs text-muted-foreground">
        <span>{t("sessions.pageRange", { from: String(total === 0 ? 0 : offset + 1), to: String(Math.min(total, offset + sessions.length)), total: String(total) })}</span>
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="sm" className="h-8 w-8 px-0" onClick={() => onPage(Math.max(0, offset - pageSize))} disabled={loading || offset === 0} aria-label={t("sessions.previous")} title={t("sessions.previous")}><ChevronLeftIcon size={15} /></Button>
          <Button variant="ghost" size="sm" className="h-8 w-8 px-0" onClick={() => onPage(offset + pageSize)} disabled={loading || offset + sessions.length >= total} aria-label={t("sessions.next")} title={t("sessions.next")}><ChevronRightIcon size={15} /></Button>
        </div>
      </div>
    </section>
  );
}

export function SessionDetail() {
  const { t } = useTranslation("usageAnalytics");
  const { sessionId = "" } = useParams();
  const [searchParams, setSearchParams] = useSearchParams();
  const [detail, setDetail] = useState<AnalyticsSessionUsageDetail | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const adminToken = readAdminToken();
  const tabParam = searchParams.get("tab");
  const tab: SessionTab = isSessionTab(tabParam) ? tabParam : "overview";
  const errorCategory = (searchParams.get("error_category") ?? "").trim();

  const load = useCallback(async () => {
    if (!sessionId) return;
    setLoading(true);
    setError(null);
    try {
      const raw = await getAnalyticsSessionUsage(sessionId, { adminToken });
      // 后端（Go）空切片可能序列化为 null，统一归一化为空数组，
      // 避免 QualityNotice/Diagnostics/TurnTable 取 .length 时崩溃白屏。
      setDetail({
        ...raw,
        partial_reasons: raw.partial_reasons ?? [],
        steps: raw.steps ?? [],
        turns: raw.turns ?? [],
        diagnostics: raw.diagnostics ?? [],
        error_categories: raw.error_categories ?? {},
      });
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setLoading(false);
    }
  }, [adminToken, sessionId]);

  useEffect(() => { void load(); }, [load]);
  const selectTab = (next: SessionTab) => {
    setSearchParams((current) => {
      const params = new URLSearchParams(current);
      if (next === "overview") params.delete("tab");
      else params.set("tab", next);
      // 失败分类过滤只属于诊断 tab；切走时清理，避免污染其他 tab 的分享链接。
      if (next !== "diagnostics") params.delete("error_category");
      return params;
    }, { replace: true });
  };

  const drillIntoDiagnostics = (pattern: { failure_category?: string; error_code?: string }) => {
    const target = (pattern.failure_category ?? pattern.error_code ?? "").trim();
    setSearchParams((current) => {
      const params = new URLSearchParams(current);
      params.set("tab", "diagnostics");
      if (target) params.set("error_category", target);
      else params.delete("error_category");
      return params;
    }, { replace: true });
  };

  return (
    <div className="min-h-screen min-w-0 overflow-x-hidden bg-[var(--workspace-shell-bg)] text-foreground">
      <div className="mx-auto flex min-h-screen w-full max-w-[1480px] flex-col gap-2 px-2.5 py-2.5 sm:px-3">
        <AnalyticsHeader onRefresh={() => void load()} refreshing={loading} />
        <main className="flex min-w-0 flex-1 flex-col gap-2">
          {error ? (
            <div role="alert" className="rounded-panel border border-analytics-danger-border bg-analytics-danger-soft px-3 py-2.5 text-sm text-analytics-danger">{t("loadError")}: {error}</div>
          ) : !detail ? (
            <div className="surface-panel flex min-h-60 items-center justify-center rounded-panel-lg text-sm text-muted-foreground"><RefreshCwIcon size={16} className={cn("mr-2", loading && "animate-spin")} />{t("loading")}</div>
          ) : (
            <>
              <section className="surface-panel flex flex-col gap-3 rounded-panel-lg p-3 sm:p-4 lg:flex-row lg:items-end lg:justify-between">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <h2 className="break-all text-base font-semibold">{detail.session.title || detail.session.session_id}</h2>
                    <Badge className={statusTone(detail.session.status)}>{detail.session.status || t("status.unknown")}</Badge>
                    <QualityBadge quality={detail.session.usage_quality} coverage={detail.session.usage_coverage} partial={detail.partial} />
                  </div>
                  <p className="mt-1 break-all text-xs leading-5 text-muted-foreground">{detail.session.session_id} · {detail.session.provider || "-"} / {detail.session.model || "-"} · {detail.session.project || t("status.unknown")} · {detail.session.directory || "-"} · {formatTimestamp(detail.session.start_time)}</p>
                </div>
                <div className="flex items-center gap-1 rounded-[0.75rem] border border-border bg-surface-softer p-1" role="tablist" aria-label={t("detail.tabs.label")}>
                  <TabButton active={tab === "overview"} onClick={() => selectTab("overview")}>{t("detail.tabs.overview")}</TabButton>
                  <TabButton active={tab === "tokens"} onClick={() => selectTab("tokens")}>{t("detail.tabs.tokens")}</TabButton>
                  <TabButton active={tab === "tools"} onClick={() => selectTab("tools")}>{t("detail.tabs.tools")}</TabButton>
                  <TabButton active={tab === "subagents"} onClick={() => selectTab("subagents")}>{t("detail.tabs.subagents")}</TabButton>
                  <TabButton active={tab === "diagnostics"} onClick={() => selectTab("diagnostics")}>{t("detail.tabs.diagnostics")}</TabButton>
                </div>
              </section>
              <QualityNotice coverage={detail.coverage} partial={detail.partial} reasons={detail.partial_reasons} />
              {tab === "overview" ? <SessionOverview detail={detail} /> : null}
              {tab === "tokens" ? <SessionTokens detail={detail} /> : null}
              {tab === "tools" && sessionId ? (
                <ToolStatsPanel sessionId={sessionId} adminToken={adminToken} />
              ) : null}
              {tab === "subagents" && sessionId ? (
                <SubagentStatsPanel sessionId={sessionId} adminToken={adminToken} />
              ) : null}
              {tab === "diagnostics" ? (
                <>
                  <SessionDiagnostics
                    detail={detail}
                    errorCategory={errorCategory}
                    onClearFilter={() => selectTab("diagnostics")}
                  />
                  {sessionId ? (
                    <ErrorPatternsPanel
                      sessionId={sessionId}
                      adminToken={adminToken}
                      selectedCategory={errorCategory}
                      onDrilldown={drillIntoDiagnostics}
                    />
                  ) : null}
                </>
              ) : null}
              {sessionId ? <CacheAnalyticsPanel key={sessionId} sessionId={sessionId} /> : null}
            </>
          )}
        </main>
      </div>
    </div>
  );
}

function SessionOverview({ detail }: { detail: AnalyticsSessionUsageDetail }) {
  const { t } = useTranslation("usageAnalytics");
  const session = detail.session;
  return (
    <>
      <section aria-label={t("metrics.title")} className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
        <Metric label={t("metrics.tokens")} value={formatNumber(session.total_tokens)} detail={t("metrics.tokenBreakdown", { prompt: formatNumber(session.prompt_tokens), completion: formatNumber(session.completion_tokens) })} />
        <Metric label={t("metrics.turnsLabel")} value={formatNumber(session.turn_count)} detail={t("metrics.failedTurnsDetail", { count: session.failed_turns })} tone={session.failed_turns > 0 ? "danger" : "default"} />
        <Metric label={t("metrics.requests")} value={formatNumber(session.llm_requests)} detail={t("metrics.coveredRequests", { count: session.llm_requests_with_usage ?? 0 })} />
        <Metric label={t("metrics.llmErrorRate")} value={formatPercent(errorRate(session.llm_errors ?? 0, session.llm_requests ?? 0))} detail={t("metrics.llmErrors", { count: session.llm_errors ?? 0 })} tone={(session.llm_errors ?? 0) > 0 ? "warning" : "default"} />
        <Metric label={t("metrics.toolErrorRate")} value={formatPercent(errorRate(session.tool_errors, session.tool_results_observed))} detail={t("metrics.observedTools", { count: session.tool_results_observed })} tone={session.tool_errors > 0 ? "warning" : "default"} />
        <Metric label={t("metrics.duration")} value={formatDuration(session.total_duration_ms)} detail={t("detail.reconciliation", { status: t(reconciliationKey(session.reconciliation_status)), delta: formatNumber(Math.abs(session.reconciliation_delta)) })} />
      </section>
      <TurnTable turns={detail.turns} compact />
    </>
  );
}

// 批次 7.2：诊断 tab 内容（原 Overview 内嵌区块升级为独立 tab）。
// `error_category` 来自失败模式 Top-N 的下钻；只做客户端过滤，不发明新的服务端语义。
function SessionDiagnostics({
  detail,
  errorCategory,
  onClearFilter,
}: {
  detail: AnalyticsSessionUsageDetail;
  errorCategory: string;
  onClearFilter: () => void;
}) {
  const { t } = useTranslation("usageAnalytics");
  const diagnostics = errorCategory
    ? detail.diagnostics.filter(
        (diagnostic) => (diagnostic.error_category ?? "").trim() === errorCategory,
      )
    : detail.diagnostics;
  return (
    <>
      {errorCategory ? (
        <div className="flex items-center justify-between gap-3 rounded-panel border border-border bg-surface-softer px-3 py-2 text-sm">
          <span className="min-w-0 truncate">
            {t("observability.errors.filteredBy", {
              key: t(errorCategoryKey(errorCategory)),
            })}
          </span>
          <Button variant="ghost" size="sm" className="h-7 px-2" onClick={onClearFilter}>
            {t("observability.errors.clearFilter")}
          </Button>
        </div>
      ) : null}
      <Diagnostics diagnostics={diagnostics} />
    </>
  );
}

function Diagnostics({ diagnostics }: { diagnostics: AnalyticsDiagnostic[] }) {
  const { t } = useTranslation("usageAnalytics");
  return (
    <section aria-labelledby="diagnostics-title" className="surface-panel rounded-panel-lg p-3 sm:p-4">
      <div className="mb-2 flex items-center justify-between gap-3">
        <div><h3 id="diagnostics-title" className="text-sm font-semibold">{t("diagnostics.title")}</h3><p className="text-xs text-muted-foreground">{t("diagnostics.subtitle")}</p></div>
        <Badge>{t("diagnostics.count", { count: diagnostics.length })}</Badge>
      </div>
      {diagnostics.length === 0 ? (
        <div className="flex items-center gap-2 py-3 text-sm text-analytics-success"><CheckCircle2Icon size={16} />{t("diagnostics.empty")}</div>
      ) : (
        <div className="overflow-hidden rounded-card border border-border divide-y divide-border">
          {diagnostics.map((diagnostic, index) => (
            <div key={`${diagnostic.code}-${diagnostic.turn_id ?? index}`} className="flex items-start gap-3 px-3 py-2.5">
              <AlertTriangleIcon size={15} className={cn("mt-0.5 shrink-0", diagnostic.severity === "error" ? "text-analytics-danger" : diagnostic.severity === "warning" ? "text-analytics-warning" : "text-analytics-info")} />
              <div className="min-w-0 flex-1"><div className="text-sm font-medium">{t(diagnosticTitleKey(diagnostic.code))}</div><div className="mt-0.5 text-xs text-muted-foreground">{t(diagnosticDetailKey(diagnostic.code), { count: diagnostic.count, rate: formatPercent(diagnostic.rate), turn: diagnostic.turn_id || "-" })}</div></div>
              <Badge>{diagnostic.severity}</Badge>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function SessionTokens({ detail }: { detail: AnalyticsSessionUsageDetail }) {
  // 「LLM 请求明细」已并入缓存面板的「请求明细」表（新增 Trace / 轮次、耗时、结果三列），
  // 避免同一批逐请求事实在一页内重复渲染（步骤级 steps 仍由 analytics API 提供）。
  return <TurnTable turns={detail.turns} />;
}

function TurnTable({ turns, compact = false }: { turns: AnalyticsTurnUsage[]; compact?: boolean }) {
  const { t } = useTranslation("usageAnalytics");
  const visibleTurns = compact ? turns.slice(-20) : turns;
  return (
    <section aria-labelledby={compact ? "recent-turns-title" : "turns-title"} className="surface-panel rounded-panel-lg p-3 sm:p-4">
      <div className="mb-2"><h3 id={compact ? "recent-turns-title" : "turns-title"} className="text-sm font-semibold">{compact ? t("turns.recentTitle") : t("turns.title")}</h3><p className="text-xs text-muted-foreground">{t("turns.subtitle", { count: turns.length })}</p></div>
      <div className="w-full max-w-full overflow-x-auto rounded-card border border-border">
        <table className="w-full min-w-[860px] border-collapse text-left text-sm">
          <thead className="bg-surface-softer text-xs text-muted-foreground"><tr className="border-b border-border"><th className="px-3 py-2 font-medium">{t("turns.columns.turn")}</th><th className="px-3 py-2 font-medium">{t("turns.columns.time")}</th><th className="px-3 py-2 font-medium">{t("turns.columns.tokens")}</th><th className="px-3 py-2 font-medium">{t("turns.columns.requests")}</th><th className="px-3 py-2 font-medium">{t("turns.columns.errors")}</th><th className="px-3 py-2 font-medium">{t("turns.columns.context")}</th><th className="px-3 py-2 font-medium">{t("turns.columns.outcome")}</th></tr></thead>
          <tbody>{visibleTurns.length === 0 ? <tr><td colSpan={7} className="px-3 py-8 text-center text-muted-foreground">{t("turns.empty")}</td></tr> : visibleTurns.map((turn) => <tr key={`${turn.trace_id}-${turn.ordinal}`} className="border-b border-border/70 last:border-b-0"><td className="px-3 py-2"><div className="font-medium">#{turn.ordinal}</div><div className="max-w-44 truncate font-mono text-xs text-muted-foreground" title={turn.turn_id}>{shortID(turn.turn_id || turn.trace_id)}</div></td><td className="px-3 py-2"><div>{formatTimestamp(turn.started_at)}</div><div className="text-xs text-muted-foreground">{formatDuration(turn.duration_ms)}</div></td><td className="px-3 py-2 tabular-nums"><div>{formatNumber(turn.usage.total_tokens)}</div><div className="text-xs text-muted-foreground">{formatPercent(turn.usage_coverage)}</div></td><td className="px-3 py-2 tabular-nums">{formatNumber(turn.llm_requests)}</td><td className="px-3 py-2 tabular-nums">{formatNumber(turn.llm_errors)}</td><td className="px-3 py-2 tabular-nums">{formatPercent(turn.max_context_utilization)}</td><td className="px-3 py-2"><Badge className={outcomeTone(turn.outcome)}>{t(outcomeKey(turn.outcome))}</Badge></td></tr>)}</tbody>
        </table>
      </div>
    </section>
  );
}
