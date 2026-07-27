import {
  AlertTriangleIcon,
  ArrowLeftIcon,
  BarChart3Icon,
  CheckCircle2Icon,
  ChevronLeftIcon,
  ChevronRightIcon,
  DatabaseIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  SearchIcon,
  ShieldIcon,
  TerminalSquareIcon,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { Select } from "@/components/ui/select";
import {
  getAnalyticsDimensions,
  getAnalyticsSessionUsage,
  getAnalyticsSummary,
  listAnalyticsSessions,
} from "@/lib/runtime-api";
import { cn } from "@/lib/utils";
import type {
  AnalyticsCoverage,
  AnalyticsDiagnostic,
  AnalyticsDimensionsResponse,
  AnalyticsGlobalTotals,
  AnalyticsGroupBucket,
  AnalyticsGroupBy,
  AnalyticsSessionRollup,
  AnalyticsSessionUsageDetail,
  AnalyticsTurnUsage,
} from "@/types/runtime";

const adminTokenStorageKey = "runtime.logs.adminToken";
const analyticsFilterKeys = ["from", "to", "q", "provider", "model", "directory", "project", "status"] as const;

const emptyTotals: AnalyticsGlobalTotals = {
  sessions: 0,
  total_requests: 0,
  total_responses: 0,
  total_tool_calls: 0,
  llm_requests: 0,
  llm_successes: 0,
  llm_errors: 0,
  turns: 0,
  failed_turns: 0,
  recovered_turns: 0,
  tool_results_observed: 0,
  tool_errors: 0,
  total_duration_ms: 0,
  total_tokens: 0,
  prompt_tokens: 0,
  completion_tokens: 0,
  cached_tokens: 0,
  reasoning_tokens: 0,
};

const emptyCoverage: AnalyticsCoverage = {
  sessions: 0,
  sessions_with_usage: 0,
  usage_session_rate: 0,
  llm_requests: 0,
  llm_requests_with_usage: 0,
  usage_request_rate: 0,
  tool_results_observed: 0,
  dropped_messages: 0,
};

const emptyDimensions: AnalyticsDimensionsResponse = {
  schema_version: "runtime.analytics.v1",
  generated_at: "",
  providers: [],
  models: [],
  directories: [],
  projects: [],
  statuses: [],
};

function readAdminToken() {
  return typeof window === "undefined"
    ? ""
    : window.localStorage.getItem(adminTokenStorageKey)?.trim() ?? "";
}

function formatNumber(value?: number | null) {
  return new Intl.NumberFormat().format(
    typeof value === "number" && Number.isFinite(value) ? value : 0,
  );
}

function formatPercent(value?: number | null) {
  const normalized = typeof value === "number" && Number.isFinite(value) ? value : 0;
  return `${(normalized * 100).toFixed(normalized > 0 && normalized < 0.01 ? 2 : 1)}%`;
}

function formatDuration(value?: number | null) {
  const milliseconds = typeof value === "number" && Number.isFinite(value) ? value : 0;
  if (milliseconds < 1000) return `${milliseconds} ms`;
  if (milliseconds < 60_000) return `${(milliseconds / 1000).toFixed(1)} s`;
  if (milliseconds < 3_600_000) return `${(milliseconds / 60_000).toFixed(1)} min`;
  return `${(milliseconds / 3_600_000).toFixed(1)} h`;
}

function formatTimestamp(value?: string) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function shortID(value: string) {
  return value.length <= 28 ? value : `${value.slice(0, 18)}...${value.slice(-6)}`;
}

function errorRate(errors: number, total: number) {
  return total > 0 ? errors / total : 0;
}

function AnalyticsHeader({ onRefresh, refreshing }: { onRefresh: () => void; refreshing: boolean }) {
  const { t } = useTranslation("usageAnalytics");
  const { sessionId } = useParams();
  const [searchParams] = useSearchParams();
  const listSearch = new URLSearchParams(searchParams);
  listSearch.delete("tab");
  const backTo = sessionId
    ? `/usage${listSearch.size > 0 ? `?${listSearch.toString()}` : ""}`
    : "/workspace/chats/new";
  return (
    <header className="border-b border-[var(--border)] bg-[var(--surface-softer)]">
      <div className="mx-auto flex min-h-14 w-full max-w-[1480px] items-center gap-3 px-3 py-2 sm:px-5">
        <Link
          to={backTo}
          className={cn(buttonVariants({ variant: "ghost", size: "sm" }), "shrink-0")}
          aria-label={sessionId ? t("actions.backToList") : t("backToWorkspace")}
        >
          <ArrowLeftIcon size={15} />
          <span className="hidden sm:inline">
            {sessionId ? t("actions.backToList") : t("backToWorkspace")}
          </span>
        </Link>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <BarChart3Icon size={17} className="text-[var(--accent-primary)]" />
            <h1 className="truncate text-sm font-semibold">{t("title")}</h1>
          </div>
          <p className="truncate text-xs text-[var(--muted-foreground)]">
            {sessionId ? shortID(sessionId) : t("description")}
          </p>
        </div>
        <nav className="flex items-center gap-1" aria-label={t("navigation")}>
          <Link to="/logs" className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}>
            <TerminalSquareIcon size={15} />
            <span className="hidden md:inline">{t("logs")}</span>
          </Link>
          <Link
            to="/runtime/config"
            className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
          >
            <DatabaseIcon size={15} />
            <span className="hidden md:inline">Runtime</span>
          </Link>
          <Button variant="secondary" size="sm" onClick={onRefresh} disabled={refreshing}>
            <RefreshCwIcon size={15} className={cn(refreshing && "animate-spin")} />
            <span className="hidden sm:inline">{t("actions.refresh")}</span>
          </Button>
        </nav>
      </div>
    </header>
  );
}

function Metric({ label, value, detail, tone }: {
  label: string;
  value: string;
  detail: string;
  tone?: "default" | "warning" | "danger";
}) {
  return (
    <div className="min-w-0 border-l-2 border-[var(--border)] px-3 py-1 first:border-l-0 first:pl-0 sm:first:border-l-2 sm:first:pl-3">
      <div className="text-xs text-[var(--muted-foreground)]">{label}</div>
      <div className={cn(
        "mt-0.5 truncate text-xl font-semibold tabular-nums",
        tone === "warning" && "text-[var(--analytics-warning)]",
        tone === "danger" && "text-[var(--analytics-danger)]",
      )}>
        {value}
      </div>
      <div className="mt-0.5 text-xs leading-4 text-[var(--muted-foreground)]">{detail}</div>
    </div>
  );
}

function QualityNotice({ coverage, partial, reasons }: {
  coverage: AnalyticsCoverage;
  partial: boolean;
  reasons: string[];
}) {
  const { t } = useTranslation("usageAnalytics");
  return (
    <div className={cn(
      "flex items-start gap-2 border px-3 py-2 text-sm",
      partial
        ? "border-[var(--analytics-warning-border)] bg-[var(--analytics-warning-soft)] text-[var(--analytics-warning)]"
        : "border-[var(--analytics-success-border)] bg-[var(--analytics-success-soft)] text-[var(--analytics-success)]",
    )}>
      {partial ? <AlertTriangleIcon size={16} className="mt-0.5 shrink-0" /> : <CheckCircle2Icon size={16} className="mt-0.5 shrink-0" />}
      <div className="min-w-0">
        <div className="font-medium">
          {partial ? t("quality.partialTitle") : t("quality.completeTitle")}
        </div>
        <div className="mt-0.5 text-xs text-[var(--muted-foreground)]">
          {t("quality.coverage", {
            sessions: formatPercent(coverage.usage_session_rate),
            requests: formatPercent(coverage.usage_request_rate),
          })}
          {reasons.length > 0 ? ` · ${reasons.map((reason) => t(partialReasonKey(reason))).join(", ")}` : ""}
        </div>
      </div>
    </div>
  );
}

function dimensionOptions(values: string[], current: string, allLabel: string) {
  const available = current && !values.some((value) => value === current)
    ? [current, ...values]
    : values;
  return [
    { value: "", label: allLabel },
    ...available.map((value) => ({ value, label: value })),
  ];
}

function UsageOverview() {
  const { t } = useTranslation("usageAnalytics");
  const [searchParams, setSearchParams] = useSearchParams();
  const [adminToken, setAdminToken] = useState(readAdminToken);
  const [sessions, setSessions] = useState<AnalyticsSessionRollup[]>([]);
  const [groups, setGroups] = useState<AnalyticsGroupBucket[]>([]);
  const [dimensions, setDimensions] = useState(emptyDimensions);
  const [totals, setTotals] = useState(emptyTotals);
  const [coverage, setCoverage] = useState(emptyCoverage);
  const [partial, setPartial] = useState(false);
  const [partialReasons, setPartialReasons] = useState<string[]>([]);
  const [matched, setMatched] = useState(0);
  const [scanned, setScanned] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const groupBy = (searchParams.get("group_by") || "day") as AnalyticsGroupBy;
  const pageOffset = Math.max(0, Number.parseInt(searchParams.get("offset") || "0", 10) || 0);
  const hasActiveFilters = analyticsFilterKeys.some((key) => searchParams.has(key)) || groupBy !== "day" || pageOffset > 0;
  const query = useMemo(() => ({
    from: searchParams.get("from") || undefined,
    to: searchParams.get("to") || undefined,
    q: searchParams.get("q") || undefined,
    provider: searchParams.get("provider") || undefined,
    model: searchParams.get("model") || undefined,
    directory: searchParams.get("directory") || undefined,
    project: searchParams.get("project") || undefined,
    status: searchParams.get("status") || undefined,
    limit: 50,
    offset: pageOffset,
  }), [pageOffset, searchParams]);

  const updateFilter = useCallback((key: string, value: string) => {
    setSearchParams((current) => {
      const next = new URLSearchParams(current);
      const normalized = value.trim();
      if (normalized) next.set(key, normalized);
      else next.delete(key);
      if (key !== "offset") next.delete("offset");
      return next;
    }, { replace: true });
  }, [setSearchParams]);

  const resetFilters = useCallback(() => {
    setSearchParams(new URLSearchParams(), { replace: true });
  }, [setSearchParams]);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [list, summary, availableDimensions] = await Promise.all([
        listAnalyticsSessions({ ...query, adminToken }),
        getAnalyticsSummary({ ...query, group_by: groupBy, adminToken }),
        getAnalyticsDimensions({ adminToken }),
      ]);
      setSessions(list.sessions ?? []);
      setGroups(summary.groups ?? []);
      setDimensions(availableDimensions ?? emptyDimensions);
      setTotals(list.totals ?? summary.totals ?? emptyTotals);
      setCoverage(list.coverage ?? emptyCoverage);
      setPartial(Boolean(list.partial));
      setPartialReasons(list.partial_reasons ?? []);
      setMatched(list.total ?? 0);
      setScanned(list.scanned ?? 0);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setLoading(false);
    }
  }, [adminToken, groupBy, query]);

  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    if (adminToken.trim()) window.localStorage.setItem(adminTokenStorageKey, adminToken.trim());
    else window.localStorage.removeItem(adminTokenStorageKey);
  }, [adminToken]);

  const groupOptions = [
    { value: "day", label: t("groupBy.day") },
    { value: "provider", label: t("groupBy.provider") },
    { value: "model", label: t("groupBy.model") },
    { value: "directory", label: t("groupBy.directory") },
    { value: "project", label: t("groupBy.project") },
    { value: "status", label: t("groupBy.status") },
  ] as const;

  return (
    <div className="min-h-screen min-w-0 overflow-x-hidden bg-[var(--workspace-shell-bg)] text-[var(--foreground)]">
      <AnalyticsHeader onRefresh={() => void load()} refreshing={loading} />
      <main className="mx-auto flex min-w-0 w-full max-w-[1480px] flex-col gap-4 px-3 py-4 sm:px-5">
        <form aria-label={t("filters.title")} className="border-b border-[var(--border)] pb-4" onSubmit={(event) => event.preventDefault()}>
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4 xl:grid-cols-9">
            <FilterInput icon={<SearchIcon size={14} />} label={t("filters.search")} value={query.q ?? ""} placeholder={t("filters.searchPlaceholder")} onChange={(value) => updateFilter("q", value)} />
            <FilterSelect label={t("filters.provider")} value={query.provider ?? ""} options={dimensionOptions(dimensions.providers, query.provider ?? "", t("filters.allProviders"))} onChange={(value) => updateFilter("provider", value)} />
            <FilterSelect label={t("filters.model")} value={query.model ?? ""} options={dimensionOptions(dimensions.models, query.model ?? "", t("filters.allModels"))} onChange={(value) => updateFilter("model", value)} />
            <FilterSelect label={t("filters.directory")} value={query.directory ?? ""} options={dimensionOptions(dimensions.directories, query.directory ?? "", t("filters.allDirectories"))} onChange={(value) => updateFilter("directory", value)} />
            <FilterSelect label={t("filters.project")} value={query.project ?? ""} options={dimensionOptions(dimensions.projects, query.project ?? "", t("filters.allProjects"))} onChange={(value) => updateFilter("project", value)} />
            <FilterSelect label={t("filters.status")} value={query.status ?? ""} options={dimensionOptions(dimensions.statuses, query.status ?? "", t("filters.allStatuses"))} onChange={(value) => updateFilter("status", value)} />
            <FilterInput type="date" label={t("filters.from")} value={query.from ?? ""} placeholder="" onChange={(value) => updateFilter("from", value)} />
            <FilterInput type="date" label={t("filters.to")} value={query.to ?? ""} placeholder="" onChange={(value) => updateFilter("to", value)} />
          </div>
          <div className="mt-2 flex flex-wrap items-end gap-2">
            <label className="w-44">
              <span className="mb-1 block text-xs text-[var(--muted-foreground)]">{t("filters.groupBy")}</span>
              <Select ariaLabel={t("filters.groupBy")} value={groupBy} options={groupOptions} onChange={(value) => updateFilter("group_by", value)} triggerClassName="h-9 rounded-[6px]" />
            </label>
            <label className="min-w-[220px] flex-1 sm:max-w-[320px]">
              <span className="mb-1 flex items-center gap-1 text-xs text-[var(--muted-foreground)]"><ShieldIcon size={12} />{t("filters.token")}</span>
              <input type="password" autoComplete="off" value={adminToken} onChange={(event) => setAdminToken(event.target.value)} placeholder={t("filters.tokenPlaceholder")} className="h-9 w-full rounded-[6px] border border-[var(--border)] bg-[var(--surface-softer)] px-3 text-sm outline-none focus:border-[var(--accent-primary-border)]" />
            </label>
            <Button type="button" variant="ghost" size="sm" className="h-9" onClick={resetFilters} disabled={!hasActiveFilters}>
              <RotateCcwIcon size={14} />
              {t("actions.resetFilters")}
            </Button>
            <div className="ml-auto text-xs text-[var(--muted-foreground)]">{t("meta.scannedMatched", { scanned: String(scanned), matched: String(matched) })}</div>
          </div>
        </form>

        {error ? (
          <div role="alert" className="border border-[var(--analytics-danger-border)] bg-[var(--analytics-danger-soft)] px-3 py-2 text-sm text-[var(--analytics-danger)]">
            {t("loadError")}: {error}
          </div>
        ) : (
          <QualityNotice coverage={coverage} partial={partial} reasons={partialReasons} />
        )}

        <section aria-label={t("metrics.title")} className="grid grid-cols-2 gap-y-4 border-b border-[var(--border)] pb-4 sm:grid-cols-3 xl:grid-cols-6">
          <Metric label={t("metrics.sessions")} value={formatNumber(totals.sessions)} detail={t("metrics.turns", { count: totals.turns })} />
          <Metric label={t("metrics.tokens")} value={formatNumber(totals.total_tokens)} detail={t("metrics.tokenBreakdown", { prompt: formatNumber(totals.prompt_tokens), completion: formatNumber(totals.completion_tokens) })} />
          <Metric label={t("metrics.requests")} value={formatNumber(totals.llm_requests || totals.total_requests)} detail={t("metrics.coveredRequests", { count: coverage.llm_requests_with_usage })} />
          <Metric label={t("metrics.llmErrorRate")} value={formatPercent(errorRate(totals.llm_errors, totals.llm_requests))} detail={t("metrics.llmErrors", { count: totals.llm_errors })} tone={totals.llm_errors > 0 ? "warning" : "default"} />
          <Metric label={t("metrics.failedTurns")} value={formatNumber(totals.failed_turns)} detail={t("metrics.recoveredTurns", { count: totals.recovered_turns })} tone={totals.failed_turns > 0 ? "danger" : "default"} />
          <Metric label={t("metrics.toolErrorRate")} value={formatPercent(errorRate(totals.tool_errors, totals.tool_results_observed))} detail={t("metrics.observedTools", { count: totals.tool_results_observed })} tone={totals.tool_errors > 0 ? "warning" : "default"} />
        </section>

        <UsageCharts totals={totals} groups={groups} groupBy={groupBy} onSelect={(key) => {
          if (groupBy === "day") updateFilter("directory", key.replaceAll("-", "/"));
          else if (groupBy === "provider") updateFilter("provider", key === "(unknown)" ? "" : key);
          else if (groupBy === "model") updateFilter("model", key === "(unknown)" ? "" : key);
          else if (groupBy === "directory") updateFilter("directory", key === "." ? "" : key);
          else if (groupBy === "project") updateFilter("project", key === "(unknown)" ? "" : key);
          else updateFilter("status", key === "(unknown)" ? "" : key);
        }} />

        <SessionTable sessions={sessions} total={matched} loading={loading} search={searchParams.toString()} offset={pageOffset} pageSize={50} onPage={(offset) => updateFilter("offset", String(offset))} />
      </main>
    </div>
  );
}

function FilterInput({ label, value, placeholder, onChange, icon, type = "text" }: {
  label: string;
  value: string;
  placeholder: string;
  onChange: (value: string) => void;
  icon?: ReactNode;
  type?: "text" | "date";
}) {
  return (
    <label className={cn("min-w-0", icon && "xl:col-span-2")}>
      <span className="mb-1 block text-xs text-[var(--muted-foreground)]">{label}</span>
      <div className="relative">
        {icon ? <span className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--muted-foreground)]">{icon}</span> : null}
        <input type={type} value={value} onChange={(event) => onChange(event.target.value)} placeholder={placeholder} className={cn("h-9 w-full rounded-[6px] border border-[var(--border)] bg-[var(--surface-softer)] px-3 text-sm outline-none focus:border-[var(--accent-primary-border)]", icon && "pl-8")} />
      </div>
    </label>
  );
}

function FilterSelect({ label, value, options, onChange }: {
  label: string;
  value: string;
  options: readonly { value: string; label: string }[];
  onChange: (value: string) => void;
}) {
  return (
    <label className="min-w-0">
      <span className="mb-1 block text-xs text-[var(--muted-foreground)]">{label}</span>
      <Select
        ariaLabel={label}
        value={value}
        options={options}
        onChange={onChange}
        className="w-full min-w-0 max-w-full"
        triggerClassName="h-9 w-full min-w-0 max-w-full overflow-hidden rounded-[6px]"
        menuClassName="max-w-[min(92vw,560px)]"
        optionClassName="truncate"
      />
    </label>
  );
}

function UsageCharts({ totals, groups, groupBy, onSelect }: {
  totals: AnalyticsGlobalTotals;
  groups: AnalyticsGroupBucket[];
  groupBy: AnalyticsGroupBy;
  onSelect: (key: string) => void;
}) {
  const { t } = useTranslation("usageAnalytics");
  const chartGroups = useMemo(() => {
    const sorted = [...groups];
    if (groupBy === "day") {
      sorted.sort((left, right) => left.key.localeCompare(right.key));
      return sorted.slice(-12);
    }
    return sorted.sort((left, right) => right.total_tokens - left.total_tokens).slice(0, 12);
  }, [groupBy, groups]);
  const promptTokens = Math.max(0, totals.prompt_tokens);
  const completionTokens = Math.max(0, totals.completion_tokens);
  const cachedTokens = Math.min(promptTokens, Math.max(0, totals.cached_tokens));
  const reasoningTokens = Math.min(completionTokens, Math.max(0, totals.reasoning_tokens));
  const tokenComposition = [
    { name: t("charts.tokens.uncachedInput"), value: promptTokens - cachedTokens, color: "var(--analytics-chart-primary)" },
    { name: t("charts.tokens.cachedInput"), value: cachedTokens, color: "var(--analytics-chart-secondary)" },
    { name: t("charts.tokens.output"), value: completionTokens - reasoningTokens, color: "var(--analytics-chart-tertiary)" },
    { name: t("charts.tokens.reasoning"), value: reasoningTokens, color: "var(--analytics-chart-quaternary)" },
  ];
  const compositionTotal = tokenComposition.reduce((sum, item) => sum + item.value, 0);
  return (
    <section aria-label={t("charts.title")} className="grid min-w-0 gap-5 border-b border-[var(--border)] pb-4 lg:grid-cols-[minmax(0,1.7fr)_minmax(280px,0.8fr)]">
      <div className="min-w-0">
        <ChartHeading title={t("charts.trend.title")} subtitle={t("groups.subtitle", { groupBy: t(groupByKey(groupBy)) })} badge={t("groups.bucketCount", { count: groups.length })} />
        {chartGroups.length === 0 ? <ChartEmpty /> : (
          <div className="h-[280px] min-w-0" data-testid="analytics-bar-chart">
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={chartGroups} margin={{ top: 12, right: 8, left: 0, bottom: 4 }}>
                <CartesianGrid vertical={false} stroke="var(--analytics-chart-grid)" />
                <XAxis dataKey="key" tickFormatter={(value) => formatDimensionTick(String(value), groupBy)} tick={{ fill: "var(--analytics-chart-axis)", fontSize: 11 }} tickLine={false} axisLine={{ stroke: "var(--analytics-chart-grid)" }} />
                <YAxis width={54} tickFormatter={formatCompactNumber} tick={{ fill: "var(--analytics-chart-axis)", fontSize: 11 }} tickLine={false} axisLine={false} />
                <Tooltip cursor={{ fill: "var(--analytics-chart-hover)" }} content={<AnalyticsChartTooltip />} />
                <Bar dataKey="total_tokens" radius={[3, 3, 0, 0]} maxBarSize={54}>
                  {chartGroups.map((group) => <Cell key={group.key} fill="var(--analytics-chart-primary)" cursor="pointer" onClick={() => onSelect(group.key)} />)}
                </Bar>
              </BarChart>
            </ResponsiveContainer>
          </div>
        )}
      </div>
      <div className="min-w-0 lg:border-l lg:border-[var(--border)] lg:pl-5">
        <ChartHeading title={t("charts.tokens.title")} subtitle={t("charts.tokens.subtitle")} />
        <TokenCompositionChart data={tokenComposition} total={compositionTotal} />
      </div>
    </section>
  );
}

function ChartHeading({ title, subtitle, badge }: { title: string; subtitle: string; badge?: string }) {
  return (
    <div className="mb-2 flex min-h-10 items-start justify-between gap-3">
      <div className="min-w-0"><h2 className="text-sm font-semibold">{title}</h2><p className="truncate text-xs text-[var(--muted-foreground)]">{subtitle}</p></div>
      {badge ? <Badge>{badge}</Badge> : null}
    </div>
  );
}

function ChartEmpty() {
  const { t } = useTranslation("usageAnalytics");
  return <div className="flex h-[280px] items-center justify-center text-sm text-[var(--muted-foreground)]">{t("groups.empty")}</div>;
}

type TokenCompositionItem = { name: string; value: number; color: string };

function TokenCompositionChart({ data, total }: { data: TokenCompositionItem[]; total: number }) {
  const { t } = useTranslation("usageAnalytics");
  const visibleData = data.filter((item) => item.value > 0);
  return (
    <div data-testid="analytics-token-chart">
      <div className="relative mx-auto h-[190px] w-full max-w-[320px]">
        {visibleData.length === 0 ? (
          <div className="flex h-full items-center justify-center text-sm text-[var(--muted-foreground)]">{t("charts.tokens.empty")}</div>
        ) : (
          <>
            <ResponsiveContainer width="100%" height="100%">
              <PieChart>
                <Pie data={visibleData} dataKey="value" nameKey="name" cx="50%" cy="50%" innerRadius={54} outerRadius={82} paddingAngle={1.5} stroke="var(--analytics-chart-surface)" strokeWidth={2}>
                  {visibleData.map((item) => <Cell key={item.name} fill={item.color} />)}
                </Pie>
                <Tooltip content={<TokenChartTooltip total={total} />} />
              </PieChart>
            </ResponsiveContainer>
            <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center">
              <span className="text-[11px] text-[var(--muted-foreground)]">{t("charts.tokens.centerLabel")}</span>
              <strong className="text-base tabular-nums">{formatCompactNumber(total)}</strong>
            </div>
          </>
        )}
      </div>
      <div className="grid grid-cols-2 gap-x-4 gap-y-2">
        {data.map((item) => (
          <div key={item.name} className="min-w-0">
            <div className="flex items-center gap-2 text-xs">
              <span className="h-2.5 w-2.5 shrink-0" style={{ backgroundColor: item.color }} />
              <span className="truncate text-[var(--muted-foreground)]" title={item.name}>{item.name}</span>
            </div>
            <div className="ml-[18px] text-xs font-medium tabular-nums">{formatNumber(item.value)} <span className="text-[var(--muted-foreground)]">{total > 0 ? formatPercent(item.value / total) : "0.0%"}</span></div>
          </div>
        ))}
      </div>
    </div>
  );
}

function AnalyticsChartTooltip({ active, label, payload }: {
  active?: boolean;
  label?: string | number;
  payload?: readonly { value?: number | string; payload?: AnalyticsGroupBucket }[];
}) {
  const { t } = useTranslation("usageAnalytics");
  if (!active || !payload?.length) return null;
  const bucket = payload[0]?.payload;
  return (
    <div className="max-w-[min(82vw,360px)] border border-[var(--border-strong)] bg-[var(--surface-overlay)] px-3 py-2 text-xs shadow-xl">
      <div className="break-all font-medium">{String(label ?? bucket?.key ?? "-")}</div>
      <div className="mt-1 flex justify-between gap-5 text-[var(--muted-foreground)]"><span>{t("groups.columns.tokens")}</span><span className="tabular-nums text-[var(--foreground)]">{formatNumber(Number(payload[0]?.value ?? 0))}</span></div>
      <div className="flex justify-between gap-5 text-[var(--muted-foreground)]"><span>{t("groups.columns.sessions")}</span><span className="tabular-nums text-[var(--foreground)]">{formatNumber(bucket?.sessions)}</span></div>
    </div>
  );
}

function TokenChartTooltip({ active, payload, total }: {
  active?: boolean;
  payload?: readonly { name?: string; value?: number | string }[];
  total: number;
}) {
  if (!active || !payload?.length) return null;
  const value = Number(payload[0]?.value ?? 0);
  return (
    <div className="border border-[var(--border-strong)] bg-[var(--surface-overlay)] px-3 py-2 text-xs shadow-xl">
      <div className="font-medium">{payload[0]?.name}</div>
      <div className="mt-1 tabular-nums text-[var(--muted-foreground)]">{formatNumber(value)} · {total > 0 ? formatPercent(value / total) : "0.0%"}</div>
    </div>
  );
}

function formatCompactNumber(value: number | string) {
  const number = Number(value) || 0;
  if (Math.abs(number) >= 1_000_000) return `${(number / 1_000_000).toFixed(number >= 10_000_000 ? 0 : 1)}M`;
  if (Math.abs(number) >= 1_000) return `${(number / 1_000).toFixed(number >= 10_000 ? 0 : 1)}K`;
  return String(number);
}

function formatDimensionTick(value: string, groupBy: AnalyticsGroupBy) {
  const normalized = groupBy === "project" ? value.split(/[\\/]/).filter(Boolean).at(-1) ?? value : value;
  return normalized.length > 16 ? `${normalized.slice(0, 14)}...` : normalized;
}

function SessionTable({ sessions, total, loading, search, offset, pageSize, onPage }: {
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
    <section aria-labelledby="usage-sessions-title" className="min-w-0">
      <div className="mb-2 flex items-end justify-between gap-3">
        <div>
          <h2 id="usage-sessions-title" className="text-sm font-semibold">{t("sessions.title")}</h2>
          <p className="text-xs text-[var(--muted-foreground)]">{t("sessions.subtitle", { count: total })}</p>
        </div>
        {loading ? <RefreshCwIcon size={15} className="animate-spin text-[var(--muted-foreground)]" /> : null}
      </div>
      <div className="w-full max-w-full overflow-x-auto border border-[var(--border)]">
        <table className="w-full min-w-[1240px] border-collapse text-left text-sm">
          <thead className="bg-[var(--surface-softer)] text-xs text-[var(--muted-foreground)]">
            <tr className="border-b border-[var(--border)]">
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
              <tr><td colSpan={10} className="px-3 py-10 text-center text-[var(--muted-foreground)]">{loading ? t("loading") : t("sessions.empty")}</td></tr>
            ) : sessions.map((session) => {
              const href = `/usage/sessions/${encodeURIComponent(session.session_id)}${search ? `?${search}` : ""}`;
              return (
                <tr key={session.session_id} className="border-b border-[var(--border)]/70 last:border-b-0 hover:bg-[var(--surface-soft-hover)]">
                  <td className="px-3 py-2.5">
                    <Link to={href} className="font-medium hover:text-[var(--accent-primary)]">{shortID(session.session_id)}</Link>
                    <div className="mt-0.5 text-xs text-[var(--muted-foreground)]">{formatTimestamp(session.start_time)}</div>
                  </td>
                  <td className="max-w-72 px-3 py-2.5"><div className="truncate font-medium" title={session.title || session.session_id}>{session.title || t("sessions.untitled")}</div><div className="truncate text-xs text-[var(--muted-foreground)]">{t(titleSourceKey(session.title_source))}</div></td>
                  <td className="px-3 py-2.5"><div>{session.provider || "-"}</div><div className="max-w-44 truncate text-xs text-[var(--muted-foreground)]">{session.model || "-"}</div></td>
                  <td className="max-w-56 px-3 py-2.5"><div className="truncate" title={session.project}>{session.project ? formatDimensionTick(session.project, "project") : t("status.unknown")}</div><div className="truncate text-xs text-[var(--muted-foreground)]" title={session.project}>{session.project || "-"}</div></td>
                  <td className="px-3 py-2.5 tabular-nums">{formatNumber(session.total_tokens)}</td>
                  <td className="px-3 py-2.5 tabular-nums">{formatNumber(session.turn_count)}</td>
                  <td className="px-3 py-2.5"><span className={cn("tabular-nums", session.failed_turns > 0 && "text-[var(--analytics-danger)]")}>{formatNumber(session.failed_turns)}</span><div className="text-xs text-[var(--muted-foreground)]">LLM {formatNumber(session.llm_errors)}</div></td>
                  <td className="px-3 py-2.5"><QualityBadge quality={session.usage_quality} coverage={session.usage_coverage} partial={session.partial} /></td>
                  <td className="px-3 py-2.5"><Badge className={statusTone(session.status)}>{session.status || t("status.unknown")}</Badge></td>
                  <td className="px-2 py-2.5"><Link to={href} className={cn(buttonVariants({ variant: "ghost", size: "sm" }), "h-8 w-8 px-0")} aria-label={t("sessions.open", { id: session.session_id })}><ChevronRightIcon size={15} /></Link></td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <div className="mt-2 flex items-center justify-between gap-3 text-xs text-[var(--muted-foreground)]">
        <span>{t("sessions.pageRange", { from: String(total === 0 ? 0 : offset + 1), to: String(Math.min(total, offset + sessions.length)), total: String(total) })}</span>
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="sm" className="h-8 w-8 px-0" onClick={() => onPage(Math.max(0, offset - pageSize))} disabled={loading || offset === 0} aria-label={t("sessions.previous")} title={t("sessions.previous")}><ChevronLeftIcon size={15} /></Button>
          <Button variant="ghost" size="sm" className="h-8 w-8 px-0" onClick={() => onPage(offset + pageSize)} disabled={loading || offset + sessions.length >= total} aria-label={t("sessions.next")} title={t("sessions.next")}><ChevronRightIcon size={15} /></Button>
        </div>
      </div>
    </section>
  );
}

function QualityBadge({ quality, coverage, partial }: { quality: string; coverage: number; partial: boolean }) {
  const { t } = useTranslation("usageAnalytics");
  return (
    <div>
      <Badge className={partial ? "border-[var(--analytics-warning-border)] bg-[var(--analytics-warning-soft)] text-[var(--analytics-warning)]" : "border-[var(--analytics-success-border)] bg-[var(--analytics-success-soft)] text-[var(--analytics-success)]"}>
        {t(qualityKey(quality))}
      </Badge>
      <div className="mt-1 text-xs tabular-nums text-[var(--muted-foreground)]">{formatPercent(coverage)}</div>
    </div>
  );
}

function qualityKey(quality: string): "qualityLabels.providerReported" | "qualityLabels.localEstimate" | "qualityLabels.mixed" | "qualityLabels.summaryOnly" | "qualityLabels.missing" | "qualityLabels.reported" {
  switch (quality) {
    case "provider_reported": return "qualityLabels.providerReported";
    case "local_estimate": return "qualityLabels.localEstimate";
    case "mixed": return "qualityLabels.mixed";
    case "summary_only": return "qualityLabels.summaryOnly";
    case "missing": return "qualityLabels.missing";
    default: return "qualityLabels.reported";
  }
}

function titleSourceKey(source?: string): "sessions.titleSources.chatLog" | "sessions.titleSources.sessionHistory" | "sessions.titleSources.initialMessage" | "sessions.titleSources.unknown" {
  switch (source) {
    case "chat_log": return "sessions.titleSources.chatLog";
    case "session_history_id":
    case "session_history_time_match": return "sessions.titleSources.sessionHistory";
    case "initial_message": return "sessions.titleSources.initialMessage";
    default: return "sessions.titleSources.unknown";
  }
}

function statusTone(status?: string) {
  switch ((status ?? "").toLowerCase()) {
    case "completed": return "border-[var(--analytics-success-border)] bg-[var(--analytics-success-soft)] text-[var(--analytics-success)]";
    case "failed": return "border-[var(--analytics-danger-border)] bg-[var(--analytics-danger-soft)] text-[var(--analytics-danger)]";
    case "interrupted": return "border-[var(--analytics-warning-border)] bg-[var(--analytics-warning-soft)] text-[var(--analytics-warning)]";
    default: return "";
  }
}

function SessionDetail() {
  const { t } = useTranslation("usageAnalytics");
  const { sessionId = "" } = useParams();
  const [searchParams, setSearchParams] = useSearchParams();
  const [detail, setDetail] = useState<AnalyticsSessionUsageDetail | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const adminToken = readAdminToken();
  const tab = searchParams.get("tab") === "tokens" ? "tokens" : "overview";

  const load = useCallback(async () => {
    if (!sessionId) return;
    setLoading(true);
    setError(null);
    try {
      setDetail(await getAnalyticsSessionUsage(sessionId, { adminToken }));
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setLoading(false);
    }
  }, [adminToken, sessionId]);

  useEffect(() => { void load(); }, [load]);
  const selectTab = (next: "overview" | "tokens") => {
    setSearchParams((current) => {
      const params = new URLSearchParams(current);
      if (next === "overview") params.delete("tab");
      else params.set("tab", next);
      return params;
    }, { replace: true });
  };

  return (
    <div className="min-h-screen min-w-0 overflow-x-hidden bg-[var(--workspace-shell-bg)] text-[var(--foreground)]">
      <AnalyticsHeader onRefresh={() => void load()} refreshing={loading} />
      <main className="mx-auto flex min-w-0 w-full max-w-[1280px] flex-col gap-4 px-3 py-4 sm:px-5">
        {error ? (
          <div role="alert" className="border border-[var(--analytics-danger-border)] bg-[var(--analytics-danger-soft)] px-3 py-2 text-sm text-[var(--analytics-danger)]">{t("loadError")}: {error}</div>
        ) : !detail ? (
          <div className="flex min-h-60 items-center justify-center text-sm text-[var(--muted-foreground)]"><RefreshCwIcon size={16} className={cn("mr-2", loading && "animate-spin")} />{t("loading")}</div>
        ) : (
          <>
            <section className="flex flex-col gap-3 border-b border-[var(--border)] pb-4 lg:flex-row lg:items-end lg:justify-between">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <h2 className="break-all text-base font-semibold">{detail.session.title || detail.session.session_id}</h2>
                  <Badge className={statusTone(detail.session.status)}>{detail.session.status || t("status.unknown")}</Badge>
                  <QualityBadge quality={detail.session.usage_quality} coverage={detail.session.usage_coverage} partial={detail.partial} />
                </div>
                <p className="mt-1 break-all text-xs text-[var(--muted-foreground)]">{detail.session.session_id} · {detail.session.provider || "-"} / {detail.session.model || "-"} · {detail.session.project || t("status.unknown")} · {detail.session.directory || "-"} · {formatTimestamp(detail.session.start_time)}</p>
              </div>
              <div className="flex items-center gap-1 border-b border-[var(--border)]" role="tablist" aria-label={t("detail.tabs.label")}>
                <TabButton active={tab === "overview"} onClick={() => selectTab("overview")}>{t("detail.tabs.overview")}</TabButton>
                <TabButton active={tab === "tokens"} onClick={() => selectTab("tokens")}>{t("detail.tabs.tokens")}</TabButton>
              </div>
            </section>
            <QualityNotice coverage={detail.coverage} partial={detail.partial} reasons={detail.partial_reasons} />
            {tab === "overview" ? <SessionOverview detail={detail} /> : <SessionTokens detail={detail} />}
          </>
        )}
      </main>
    </div>
  );
}

function TabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return <button type="button" role="tab" aria-selected={active} onClick={onClick} className={cn("border-b-2 px-3 py-2 text-sm", active ? "border-[var(--accent-primary)] text-[var(--foreground)]" : "border-transparent text-[var(--muted-foreground)] hover:text-[var(--foreground)]")}>{children}</button>;
}

function SessionOverview({ detail }: { detail: AnalyticsSessionUsageDetail }) {
  const { t } = useTranslation("usageAnalytics");
  const session = detail.session;
  return (
    <>
      <section aria-label={t("metrics.title")} className="grid grid-cols-2 gap-y-4 border-b border-[var(--border)] pb-4 md:grid-cols-3 xl:grid-cols-6">
        <Metric label={t("metrics.tokens")} value={formatNumber(session.total_tokens)} detail={t("metrics.tokenBreakdown", { prompt: formatNumber(session.prompt_tokens), completion: formatNumber(session.completion_tokens) })} />
        <Metric label={t("metrics.turnsLabel")} value={formatNumber(session.turn_count)} detail={t("metrics.failedTurnsDetail", { count: session.failed_turns })} tone={session.failed_turns > 0 ? "danger" : "default"} />
        <Metric label={t("metrics.requests")} value={formatNumber(session.llm_requests)} detail={t("metrics.coveredRequests", { count: session.llm_requests_with_usage ?? 0 })} />
        <Metric label={t("metrics.llmErrorRate")} value={formatPercent(errorRate(session.llm_errors ?? 0, session.llm_requests ?? 0))} detail={t("metrics.llmErrors", { count: session.llm_errors ?? 0 })} tone={(session.llm_errors ?? 0) > 0 ? "warning" : "default"} />
        <Metric label={t("metrics.toolErrorRate")} value={formatPercent(errorRate(session.tool_errors, session.tool_results_observed))} detail={t("metrics.observedTools", { count: session.tool_results_observed })} tone={session.tool_errors > 0 ? "warning" : "default"} />
        <Metric label={t("metrics.duration")} value={formatDuration(session.total_duration_ms)} detail={t("detail.reconciliation", { status: t(reconciliationKey(session.reconciliation_status)), delta: formatNumber(Math.abs(session.reconciliation_delta)) })} />
      </section>
      <Diagnostics diagnostics={detail.diagnostics} />
      <TurnTable turns={detail.turns} compact />
    </>
  );
}

function Diagnostics({ diagnostics }: { diagnostics: AnalyticsDiagnostic[] }) {
  const { t } = useTranslation("usageAnalytics");
  return (
    <section aria-labelledby="diagnostics-title" className="border-b border-[var(--border)] pb-4">
      <div className="mb-2 flex items-center justify-between gap-3">
        <div><h3 id="diagnostics-title" className="text-sm font-semibold">{t("diagnostics.title")}</h3><p className="text-xs text-[var(--muted-foreground)]">{t("diagnostics.subtitle")}</p></div>
        <Badge>{t("diagnostics.count", { count: diagnostics.length })}</Badge>
      </div>
      {diagnostics.length === 0 ? (
        <div className="flex items-center gap-2 py-3 text-sm text-[var(--analytics-success)]"><CheckCircle2Icon size={16} />{t("diagnostics.empty")}</div>
      ) : (
        <div className="divide-y divide-[var(--border)] border border-[var(--border)]">
          {diagnostics.map((diagnostic, index) => (
            <div key={`${diagnostic.code}-${diagnostic.turn_id ?? index}`} className="flex items-start gap-3 px-3 py-2.5">
              <AlertTriangleIcon size={15} className={cn("mt-0.5 shrink-0", diagnostic.severity === "error" ? "text-[var(--analytics-danger)]" : diagnostic.severity === "warning" ? "text-[var(--analytics-warning)]" : "text-[var(--analytics-info)]")} />
              <div className="min-w-0 flex-1"><div className="text-sm font-medium">{t(diagnosticTitleKey(diagnostic.code))}</div><div className="mt-0.5 text-xs text-[var(--muted-foreground)]">{t(diagnosticDetailKey(diagnostic.code), { count: diagnostic.count, rate: formatPercent(diagnostic.rate), turn: diagnostic.turn_id || "-" })}</div></div>
              <Badge>{diagnostic.severity}</Badge>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function SessionTokens({ detail }: { detail: AnalyticsSessionUsageDetail }) {
  const { t } = useTranslation("usageAnalytics");
  return (
    <>
      <TurnTable turns={detail.turns} />
      <section aria-labelledby="requests-title">
        <div className="mb-2"><h3 id="requests-title" className="text-sm font-semibold">{t("requests.title")}</h3><p className="text-xs text-[var(--muted-foreground)]">{t("requests.subtitle", { count: detail.step_count })}</p></div>
        <div className="w-full max-w-full overflow-x-auto border border-[var(--border)]">
          <table className="w-full min-w-[820px] border-collapse text-left text-sm">
            <thead className="bg-[var(--surface-softer)] text-xs text-[var(--muted-foreground)]"><tr className="border-b border-[var(--border)]"><th className="px-3 py-2 font-medium">{t("requests.columns.turn")}</th><th className="px-3 py-2 font-medium">{t("requests.columns.step")}</th><th className="px-3 py-2 font-medium">{t("requests.columns.tokens")}</th><th className="px-3 py-2 font-medium">{t("requests.columns.cache")}</th><th className="px-3 py-2 font-medium">{t("requests.columns.context")}</th><th className="px-3 py-2 font-medium">{t("requests.columns.duration")}</th><th className="px-3 py-2 font-medium">{t("requests.columns.outcome")}</th></tr></thead>
            <tbody>{detail.steps.slice(-250).map((step, index) => <tr key={`${step.trace_id}-${step.step}-${index}`} className="border-b border-[var(--border)]/70 last:border-b-0"><td className="max-w-52 truncate px-3 py-2 font-mono text-xs" title={step.trace_id}>{shortID(step.trace_id || "-")}</td><td className="px-3 py-2 tabular-nums">{step.step ?? index + 1}</td><td className="px-3 py-2 tabular-nums">{step.usage_available ? formatNumber(step.total_tokens) : t("unavailable")}</td><td className="px-3 py-2 tabular-nums">{formatNumber(step.cached_tokens)}</td><td className="px-3 py-2 tabular-nums">{formatPercent(step.context_utilization)}</td><td className="px-3 py-2 tabular-nums">{formatDuration(step.duration_ms)}</td><td className="px-3 py-2"><Badge className={step.success ? "border-[var(--analytics-success-border)] bg-[var(--analytics-success-soft)] text-[var(--analytics-success)]" : "border-[var(--analytics-danger-border)] bg-[var(--analytics-danger-soft)] text-[var(--analytics-danger)]"}>{step.success ? t("outcomes.success") : t(errorCategoryKey(step.error_category))}</Badge></td></tr>)}</tbody>
          </table>
        </div>
        {detail.steps.length > 250 ? <p className="mt-2 text-xs text-[var(--muted-foreground)]">{t("requests.capped", { count: 250, total: String(detail.steps.length) })}</p> : null}
      </section>
    </>
  );
}

function TurnTable({ turns, compact = false }: { turns: AnalyticsTurnUsage[]; compact?: boolean }) {
  const { t } = useTranslation("usageAnalytics");
  const visibleTurns = compact ? turns.slice(-20) : turns;
  return (
    <section aria-labelledby={compact ? "recent-turns-title" : "turns-title"} className="border-b border-[var(--border)] pb-4 last:border-b-0">
      <div className="mb-2"><h3 id={compact ? "recent-turns-title" : "turns-title"} className="text-sm font-semibold">{compact ? t("turns.recentTitle") : t("turns.title")}</h3><p className="text-xs text-[var(--muted-foreground)]">{t("turns.subtitle", { count: turns.length })}</p></div>
      <div className="w-full max-w-full overflow-x-auto border border-[var(--border)]">
        <table className="w-full min-w-[860px] border-collapse text-left text-sm">
          <thead className="bg-[var(--surface-softer)] text-xs text-[var(--muted-foreground)]"><tr className="border-b border-[var(--border)]"><th className="px-3 py-2 font-medium">{t("turns.columns.turn")}</th><th className="px-3 py-2 font-medium">{t("turns.columns.time")}</th><th className="px-3 py-2 font-medium">{t("turns.columns.tokens")}</th><th className="px-3 py-2 font-medium">{t("turns.columns.requests")}</th><th className="px-3 py-2 font-medium">{t("turns.columns.errors")}</th><th className="px-3 py-2 font-medium">{t("turns.columns.context")}</th><th className="px-3 py-2 font-medium">{t("turns.columns.outcome")}</th></tr></thead>
          <tbody>{visibleTurns.length === 0 ? <tr><td colSpan={7} className="px-3 py-8 text-center text-[var(--muted-foreground)]">{t("turns.empty")}</td></tr> : visibleTurns.map((turn) => <tr key={`${turn.trace_id}-${turn.ordinal}`} className="border-b border-[var(--border)]/70 last:border-b-0"><td className="px-3 py-2"><div className="font-medium">#{turn.ordinal}</div><div className="max-w-44 truncate font-mono text-xs text-[var(--muted-foreground)]" title={turn.turn_id}>{shortID(turn.turn_id || turn.trace_id)}</div></td><td className="px-3 py-2"><div>{formatTimestamp(turn.started_at)}</div><div className="text-xs text-[var(--muted-foreground)]">{formatDuration(turn.duration_ms)}</div></td><td className="px-3 py-2 tabular-nums"><div>{formatNumber(turn.usage.total_tokens)}</div><div className="text-xs text-[var(--muted-foreground)]">{formatPercent(turn.usage_coverage)}</div></td><td className="px-3 py-2 tabular-nums">{formatNumber(turn.llm_requests)}</td><td className="px-3 py-2 tabular-nums">{formatNumber(turn.llm_errors)}</td><td className="px-3 py-2 tabular-nums">{formatPercent(turn.max_context_utilization)}</td><td className="px-3 py-2"><Badge className={outcomeTone(turn.outcome)}>{t(outcomeKey(turn.outcome))}</Badge></td></tr>)}</tbody>
        </table>
      </div>
    </section>
  );
}

function groupByKey(groupBy: AnalyticsGroupBy): "groupBy.day" | "groupBy.provider" | "groupBy.model" | "groupBy.directory" | "groupBy.project" | "groupBy.status" {
  return `groupBy.${groupBy}`;
}

function partialReasonKey(reason: string): "partialReasons.requestHistory" | "partialReasons.usageMissing" | "partialReasons.messagesDropped" | "partialReasons.reconciliation" | "partialReasons.unknown" {
  switch (reason) {
    case "llm_request_history_incomplete": return "partialReasons.requestHistory";
    case "usage_missing": return "partialReasons.usageMissing";
    case "chat_messages_dropped": return "partialReasons.messagesDropped";
    case "usage_reconciliation_mismatch": return "partialReasons.reconciliation";
    default: return "partialReasons.unknown";
  }
}

function reconciliationKey(status: string): "reconciliation.matched" | "reconciliation.partial" | "reconciliation.mismatch" | "reconciliation.unavailable" {
  switch (status) {
    case "matched": return "reconciliation.matched";
    case "partial": return "reconciliation.partial";
    case "mismatch": return "reconciliation.mismatch";
    default: return "reconciliation.unavailable";
  }
}

function diagnosticTitleKey(code: string): "diagnostics.codes.llmFailures.title" | "diagnostics.codes.failedTurns.title" | "diagnostics.codes.toolErrors.title" | "diagnostics.codes.contextPressure.title" | "diagnostics.codes.reconciliation.title" | "diagnostics.codes.partial.title" {
  switch (code) {
    case "llm_failures": return "diagnostics.codes.llmFailures.title";
    case "failed_turns": return "diagnostics.codes.failedTurns.title";
    case "tool_errors_observed": return "diagnostics.codes.toolErrors.title";
    case "context_pressure": return "diagnostics.codes.contextPressure.title";
    case "usage_reconciliation_mismatch": return "diagnostics.codes.reconciliation.title";
    default: return "diagnostics.codes.partial.title";
  }
}

function diagnosticDetailKey(code: string): "diagnostics.codes.llmFailures.detail" | "diagnostics.codes.failedTurns.detail" | "diagnostics.codes.toolErrors.detail" | "diagnostics.codes.contextPressure.detail" | "diagnostics.codes.reconciliation.detail" | "diagnostics.codes.partial.detail" {
  return diagnosticTitleKey(code).replace(".title", ".detail") as ReturnType<typeof diagnosticDetailKey>;
}

function errorCategoryKey(category?: string): "errors.cancelled" | "errors.modelNotFound" | "errors.authentication" | "errors.rateLimit" | "errors.upstream" | "errors.timeout" | "errors.network" | "errors.contextLimit" | "errors.invalidRequest" | "errors.unknown" {
  switch (category) {
    case "cancelled": return "errors.cancelled";
    case "model_not_found": return "errors.modelNotFound";
    case "authentication": return "errors.authentication";
    case "rate_limit": return "errors.rateLimit";
    case "upstream_unavailable": return "errors.upstream";
    case "timeout": return "errors.timeout";
    case "network": return "errors.network";
    case "context_limit": return "errors.contextLimit";
    case "invalid_request": return "errors.invalidRequest";
    default: return "errors.unknown";
  }
}

function outcomeKey(outcome: string): "outcomes.success" | "outcomes.recovered" | "outcomes.failed" | "outcomes.cancelled" | "outcomes.unknown" {
  switch (outcome) {
    case "success": return "outcomes.success";
    case "recovered": return "outcomes.recovered";
    case "failed": return "outcomes.failed";
    case "cancelled": return "outcomes.cancelled";
    default: return "outcomes.unknown";
  }
}

function outcomeTone(outcome: string) {
  switch (outcome) {
    case "success": return "border-[var(--analytics-success-border)] bg-[var(--analytics-success-soft)] text-[var(--analytics-success)]";
    case "recovered": return "border-[var(--analytics-warning-border)] bg-[var(--analytics-warning-soft)] text-[var(--analytics-warning)]";
    case "failed": return "border-[var(--analytics-danger-border)] bg-[var(--analytics-danger-soft)] text-[var(--analytics-danger)]";
    case "cancelled": return "border-[var(--analytics-info-border)] bg-[var(--analytics-info-soft)] text-[var(--analytics-info)]";
    default: return "";
  }
}

export function UsageAnalyticsPage() {
  const { sessionId } = useParams();
  return sessionId ? <SessionDetail /> : <UsageOverview />;
}
