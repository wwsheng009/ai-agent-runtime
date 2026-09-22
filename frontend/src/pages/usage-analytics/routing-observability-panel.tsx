// 路由切换观测面板（主 Agent / 子 Agent）：
//   GET /api/runtime/analytics/routing         总览（totals + 维度分布桶）
//   GET /api/runtime/analytics/routing/events  明细（时间倒序分页）
//
// 契约见 types/runtime/routing-analytics.ts（后端 contracts_routes.go）。
// 语义约定：route_changed / fallback_used 是三态，只计 true；分布桶与 totals
// 同源同过滤集（全量 SQL 聚合），桶计数之和恒等于 totals。
// 空库返回空数组 → 渲染「暂无数据」；403/网络错误 → role="alert" 降级。

import { getAnalyticsRoutingStats, listAnalyticsRoutingEvents } from "@/api/runtime/analytics";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import type {
  AnalyticsRouteBucket,
  AnalyticsRouteEvent,
  AnalyticsRouteStatsResponse,
  AnalyticsRouteTotals,
} from "@/types/runtime";
import type { TFunction } from "i18next";
import { RefreshCwIcon } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { formatNumber, formatTimestamp, shortID } from "./format";
import { Metric } from "./primitives";

const PAGE_SIZE = 50;

// 归一表：后端取值 → i18n key（本文件导出 React 组件，helper 不导出）。
const scopeKeys = {
  main_agent: "observability.routing.scopeMain",
  subagent: "observability.routing.scopeSubagent",
} as const;

const kindKeys = {
  applied: "observability.routing.kindApplied",
  cleared: "observability.routing.kindCleared",
  warning: "observability.routing.kindWarning",
} as const;

const sourceKeys = {
  explicit: "observability.routing.difficultySources.explicit",
  explicit_promoted: "observability.routing.difficultySources.explicitPromoted",
  heuristic: "observability.routing.difficultySources.heuristic",
  role: "observability.routing.difficultySources.role",
  // P4：profile source 新值 task_type_override；历史值 role_override 继续映射。
  role_override: "observability.routing.difficultySources.role",
  task_type_override: "observability.routing.difficultySources.taskTypeOverride",
  read_only: "observability.routing.difficultySources.readOnly",
  default: "observability.routing.difficultySources.default",
} as const;

// 难度来源（difficulty_source）归一表：后端 resolver.go 产出的取值 + P4 新增的
// task_type_floor / task_type_downgrade，与 route_source 的 sourceKeys 是两套
// 词表，勿混用（后者含 parent_inherit 等）。
const difficultySourceKeys = {
  explicit: "observability.routing.difficultySources.explicit",
  explicit_promoted: "observability.routing.difficultySources.explicitPromoted",
  inferred: "observability.routing.difficultySources.inferred",
  default: "observability.routing.difficultySources.default",
  task_type_floor: "observability.routing.difficultySources.taskTypeFloor",
  task_type_downgrade: "observability.routing.difficultySources.taskTypeDowngrade",
} as const;

// 任务类型（12 类封闭枚举）归一表：与配置编辑器 task_types 键集一致。
const taskTypeKeys = {
  config: "observability.routing.taskTypes.config",
  explore: "observability.routing.taskTypes.explore",
  generate: "observability.routing.taskTypes.generate",
  implement: "observability.routing.taskTypes.implement",
  integration: "observability.routing.taskTypes.integration",
  migrate: "observability.routing.taskTypes.migrate",
  modify: "observability.routing.taskTypes.modify",
  refactor: "observability.routing.taskTypes.refactor",
  security: "observability.routing.taskTypes.security",
  test: "observability.routing.taskTypes.test",
  understand: "observability.routing.taskTypes.understand",
  verify: "observability.routing.taskTypes.verify",
} as const;

// 护栏告警 token 归一表：精确 token + 「prefix:value」前缀 token（用 {{value}} 插值）。
// 未收录 token 原样回显，后端扩词表时页面不空白。
const warningKeys = {
  difficulty_missing_defaulted: "observability.routing.warningLabels.difficulty_missing_defaulted",
  difficulty_invalid_defaulted: "observability.routing.warningLabels.difficulty_invalid_defaulted",
  difficulty_promoted_by_heuristic: "observability.routing.warningLabels.difficulty_promoted_by_heuristic",
  difficulty_promoted_over_explicit: "observability.routing.warningLabels.difficulty_promoted_over_explicit",
  difficulty_promotion_warn_only: "observability.routing.warningLabels.difficulty_promotion_warn_only",
  max_expert_concurrency_zero_means_unlimited:
    "observability.routing.warningLabels.max_expert_concurrency_zero_means_unlimited",
} as const;

const warningPrefixKeys = {
  difficulty_promoted_by_keyword: "observability.routing.warningLabels.difficulty_promoted_by_keyword",
  difficulty_promoted_by_role: "observability.routing.warningLabels.difficulty_promoted_by_role",
  difficulty_floor_by_task_type: "observability.routing.warningLabels.difficulty_floor_by_task_type",
  difficulty_downgraded_by_task_type: "observability.routing.warningLabels.difficulty_downgraded_by_task_type",
  task_type_unknown: "observability.routing.warningLabels.task_type_unknown",
} as const;

const reasonKeys = {
  resolved: "observability.routing.reasons.resolved",
} as const;

const routeFlagKeys = {
  routeChanged: "observability.routing.flags.routeChanged",
  routeUnchanged: "observability.routing.flags.routeUnchanged",
  fallbackUsed: "observability.routing.flags.fallbackUsed",
  fallbackUnused: "observability.routing.flags.fallbackUnused",
} as const;

type ScopeKey = (typeof scopeKeys)[keyof typeof scopeKeys];
type KindKey = (typeof kindKeys)[keyof typeof kindKeys];
type SourceKey = (typeof sourceKeys)[keyof typeof sourceKeys];
type DifficultySourceKey = (typeof difficultySourceKeys)[keyof typeof difficultySourceKeys];
type TaskTypeKey = (typeof taskTypeKeys)[keyof typeof taskTypeKeys];
type WarningKey = (typeof warningKeys)[keyof typeof warningKeys];
type WarningPrefixKey = (typeof warningPrefixKeys)[keyof typeof warningPrefixKeys];
type ReasonKey = (typeof reasonKeys)[keyof typeof reasonKeys];
type RouteFlagKey = (typeof routeFlagKeys)[keyof typeof routeFlagKeys];

function scopeLabel(t: TFunction<"usageAnalytics">, raw?: string): string {
  const normalized = (raw ?? "").trim().toLowerCase();
  const key = scopeKeys[normalized as keyof typeof scopeKeys] as ScopeKey | undefined;
  if (key) return t(key);
  return (raw ?? "").trim() || t("observability.routing.scopeUnknown");
}

function kindLabel(t: TFunction<"usageAnalytics">, raw?: string): string {
  const normalized = (raw ?? "").trim().toLowerCase();
  const key = kindKeys[normalized as keyof typeof kindKeys] as KindKey | undefined;
  if (key) return t(key);
  return (raw ?? "").trim() || t("observability.routing.kindUnknown");
}

function sourceLabel(t: TFunction<"usageAnalytics">, raw?: string): string {
  const normalized = (raw ?? "").trim().toLowerCase();
  const key = sourceKeys[normalized as keyof typeof sourceKeys] as SourceKey | undefined;
  if (key) return t(key);
  return (raw ?? "").trim() || t("observability.routing.difficultySources.unknown");
}

// 难度来源标签（词表见 difficultySourceKeys）：未记录的取值回显原值。
function difficultySourceLabel(t: TFunction<"usageAnalytics">, raw?: string): string {
  const normalized = (raw ?? "").trim().toLowerCase();
  const key = difficultySourceKeys[normalized as keyof typeof difficultySourceKeys] as DifficultySourceKey | undefined;
  if (key) return t(key);
  return (raw ?? "").trim() || t("observability.routing.difficultySources.unknown");
}

// 任务类型标签（词表见 taskTypeKeys）：12 类封闭枚举走 i18n，未知取值原样回显。
function taskTypeLabel(t: TFunction<"usageAnalytics">, raw?: string): string {
  const normalized = (raw ?? "").trim().toLowerCase();
  const key = taskTypeKeys[normalized as keyof typeof taskTypeKeys] as TaskTypeKey | undefined;
  if (key) return t(key);
  return (raw ?? "").trim() || t("observability.routing.flags.notRecorded");
}

// 护栏告警 token → 标签：精确 token 优先；否则按 ":" 拆前缀做 {{value}} 插值；
// 两者都不命中时回显原始 token（保留排障可读性）。
function warningLabel(t: TFunction<"usageAnalytics">, raw?: string): string {
  const token = (raw ?? "").trim();
  if (!token) return t("observability.routing.flags.notRecorded");
  const exact = warningKeys[token as keyof typeof warningKeys] as WarningKey | undefined;
  if (exact) return t(exact);
  const separator = token.indexOf(":");
  if (separator > 0) {
    const prefix = token.slice(0, separator).trim().toLowerCase();
    const key = warningPrefixKeys[prefix as keyof typeof warningPrefixKeys] as WarningPrefixKey | undefined;
    if (key) return t(key, { value: token.slice(separator + 1).trim() });
  }
  return token;
}

function reasonLabel(t: TFunction<"usageAnalytics">, raw?: string): string {
  const normalized = (raw ?? "").trim().toLowerCase();
  const key = reasonKeys[normalized as keyof typeof reasonKeys] as ReasonKey | undefined;
  if (key) return t(key);
  return (raw ?? "").trim() || t("observability.routing.kindUnknown");
}

// 维度桶里没有归一表可用的取值（provider / model / difficulty / role / 告警词）直接回显；
// 空值统一显示「未记录」，避免图上出现无名桶。
function rawOrNotRecorded(t: TFunction<"usageAnalytics">, raw?: string): string {
  return (raw ?? "").trim() || t("observability.routing.flags.notRecorded");
}

function kindTone(kind: string): string {
  switch ((kind ?? "").trim().toLowerCase()) {
    case "applied":
      return "border-analytics-success-border bg-analytics-success-soft text-analytics-success";
    case "warning":
      return "border-analytics-warning-border bg-analytics-warning-soft text-analytics-warning";
    default:
      return "";
  }
}

// 三态布尔 → 文本：undefined 表示事件未携带该字段，显示「未记录」。
function triStateLabel(
  t: TFunction<"usageAnalytics">,
  value: boolean | undefined,
  trueKey: RouteFlagKey,
  falseKey: RouteFlagKey,
): string {
  if (value === true) return t(trueKey);
  if (value === false) return t(falseKey);
  return t("observability.routing.flags.notRecorded");
}

function bucketsToEntries(
  t: TFunction<"usageAnalytics">,
  buckets: AnalyticsRouteBucket[] | undefined,
  label: (t: TFunction<"usageAnalytics">, raw?: string) => string,
) {
  return (buckets ?? []).map((bucket) => ({
    key: bucket.key,
    count: bucket.count,
    label: label(t, bucket.key),
    detail: bucket.route_changed > 0 || bucket.fallback_used > 0
      ? t("observability.routing.metricDetail.appliedCleared", {
          applied: String(bucket.route_changed),
          cleared: String(bucket.fallback_used),
        })
      : undefined,
  }));
}

export function RoutingObservabilityPanel({
  sessionId,
  adminToken,
}: {
  sessionId?: string;
  adminToken?: string;
}) {
  const { t } = useTranslation("usageAnalytics");
  const [stats, setStats] = useState<AnalyticsRouteStatsResponse | null>(null);
  const [events, setEvents] = useState<AnalyticsRouteEvent[]>([]);
  const [total, setTotal] = useState(0);
  const [scope, setScope] = useState("");
  const [warningsOnly, setWarningsOnly] = useState(false);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(
    async (nextOffset: number, append: boolean) => {
      setLoading(true);
      setError(null);
      try {
        const query = {
          session: sessionId,
          scope: scope || undefined,
          warnings_only: warningsOnly || undefined,
          adminToken,
        };
        const [statsResponse, eventsResponse] = await Promise.all([
          getAnalyticsRoutingStats(query),
          listAnalyticsRoutingEvents({ ...query, limit: PAGE_SIZE, offset: nextOffset }),
        ]);
        const rows = eventsResponse.events ?? [];
        setStats(statsResponse);
        setTotal(eventsResponse.count ?? rows.length);
        setOffset(nextOffset + rows.length);
        setEvents((current) => (append ? [...current, ...rows] : rows));
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : String(caught));
      } finally {
        setLoading(false);
      }
    },
    [adminToken, scope, sessionId, warningsOnly],
  );

  useEffect(() => {
    void load(0, false);
  }, [load]);

  const totals: AnalyticsRouteTotals | undefined = stats?.totals;
  const distributionGroups = useMemo(() => {
    if (!stats) return [];
    return [
      { title: t("observability.routing.distributions.scope"), entries: bucketsToEntries(t, stats.by_scope, scopeLabel) },
      { title: t("observability.routing.distributions.kind"), entries: bucketsToEntries(t, stats.by_kind, kindLabel) },
      { title: t("observability.routing.distributions.reason"), entries: bucketsToEntries(t, stats.by_reason, reasonLabel) },
      { title: t("observability.routing.distributions.source"), entries: bucketsToEntries(t, stats.by_source, sourceLabel) },
      { title: t("observability.routing.distributions.difficulty"), entries: bucketsToEntries(t, stats.by_difficulty, rawOrNotRecorded) },
      { title: t("observability.routing.distributions.difficultySource"), entries: bucketsToEntries(t, stats.by_difficulty_source, difficultySourceLabel) },
      { title: t("observability.routing.distributions.taskType"), entries: bucketsToEntries(t, stats.by_task_type, taskTypeLabel) },
      { title: t("observability.routing.distributions.role"), entries: bucketsToEntries(t, stats.by_role, rawOrNotRecorded) },
      { title: t("observability.routing.distributions.provider"), entries: bucketsToEntries(t, stats.by_provider, rawOrNotRecorded) },
      { title: t("observability.routing.distributions.model"), entries: bucketsToEntries(t, stats.by_model, rawOrNotRecorded) },
      { title: t("observability.routing.distributions.warnings"), entries: bucketsToEntries(t, stats.warnings, warningLabel) },
    ];
  }, [stats, t]);

  const scopeOptions = [
    { value: "", label: t("observability.routing.scopeAll") },
    { value: "main_agent", label: t("observability.routing.scopeMain") },
    { value: "subagent", label: t("observability.routing.scopeSubagent") },
  ];

  return (
    <section
      aria-labelledby="routing-observability-title"
      className="surface-panel min-w-0 rounded-panel-lg p-3 sm:p-4"
      data-testid="routing-observability-panel"
    >
      <div className="mb-3 flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h3 id="routing-observability-title" className="text-sm font-semibold">
            {t("observability.routing.title")}
          </h3>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t("observability.routing.subtitle")}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <label className="flex items-center gap-1 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={warningsOnly}
              onChange={(event) => setWarningsOnly(event.target.checked)}
              className="size-3.5 accent-current"
            />
            {t("observability.routing.warningsOnly")}
          </label>
          <Select
            ariaLabel={t("observability.routing.scopeLabel")}
            value={scope}
            options={scopeOptions}
            onChange={(value) => setScope(value)}
            triggerClassName="h-8 rounded-field"
          />
          <Button
            variant="ghost"
            size="sm"
            className="h-8 px-2"
            onClick={() => void load(0, false)}
            disabled={loading}
            aria-label={t("actions.refresh")}
            title={t("actions.refresh")}
          >
            <RefreshCwIcon size={14} className={loading ? "animate-spin" : undefined} />
          </Button>
        </div>
      </div>

      {error ? (
        <div
          role="alert"
          className="mb-3 rounded-panel border border-analytics-danger-border bg-analytics-danger-soft px-3 py-2.5 text-sm text-analytics-danger"
        >
          {t("loadError")}: {error}
        </div>
      ) : null}

      <div className="mb-3 grid grid-cols-2 gap-2 lg:grid-cols-3 2xl:grid-cols-6">
        <Metric
          label={t("observability.routing.metrics.total")}
          value={formatNumber(totals?.total)}
          detail={t("observability.routing.metricDetail.appliedCleared", {
            applied: formatNumber(totals?.applied),
            cleared: formatNumber(totals?.cleared),
          })}
        />
        <Metric
          label={t("observability.routing.metrics.mainAgent")}
          value={formatNumber(totals?.main_agent)}
          detail={t("observability.routing.metricDetail.candidateTotal", { count: totals?.candidate_total ?? 0 })}
        />
        <Metric
          label={t("observability.routing.metrics.subagent")}
          value={formatNumber(totals?.subagent)}
          detail={t("observability.routing.metricDetail.distinctSessions", { count: totals?.distinct_sessions ?? 0 })}
        />
        <Metric
          label={t("observability.routing.metrics.routeChanged")}
          value={formatNumber(totals?.route_changed)}
          detail={t("observability.routing.metricDetail.distinctModels", { count: totals?.distinct_models ?? 0 })}
        />
        <Metric
          label={t("observability.routing.metrics.fallbackUsed")}
          value={formatNumber(totals?.fallback_used)}
          detail={t("observability.routing.metrics.total")}
          tone={(totals?.fallback_used ?? 0) > 0 ? "warning" : "default"}
        />
        <Metric
          label={t("observability.routing.metrics.warnings")}
          value={formatNumber(totals?.warnings)}
          detail={t("observability.routing.metrics.total")}
          tone={(totals?.warnings ?? 0) > 0 ? "warning" : "default"}
        />
      </div>

      {distributionGroups.length > 0 ? (
        <div className="mb-3 grid gap-2 lg:grid-cols-2 2xl:grid-cols-3">
          {distributionGroups.map((group) => (
            <DistributionList
              key={group.title}
              title={group.title}
              empty={t("observability.routing.empty")}
              entries={group.entries}
            />
          ))}
        </div>
      ) : null}

      <div className="mb-2 flex items-center justify-between gap-2">
        <h4 className="text-xs font-medium text-muted-foreground">{t("observability.routing.eventsTitle")}</h4>
        <span className="text-xs text-muted-foreground">
          {t("observability.routing.eventsShown", {
            shown: formatNumber(events.length),
            total: formatNumber(total),
          })}
        </span>
      </div>

      {loading && events.length === 0 ? (
        <div className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
          <RefreshCwIcon size={15} className="animate-spin" />
          {t("observability.loading")}
        </div>
      ) : events.length === 0 ? (
        <div
          data-testid="routing-observability-empty"
          className="rounded-card border border-border bg-surface-softer px-3 py-8 text-center text-sm text-muted-foreground"
        >
          {t("observability.routing.empty")}
        </div>
      ) : (
        <div className="w-full max-w-full overflow-x-auto rounded-card border border-border">
          <table className="w-full min-w-[1440px] border-collapse text-left text-sm">
            <thead className="bg-surface-softer text-xs text-muted-foreground">
              <tr className="border-b border-border">
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.time")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.scope")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.kind")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.agent")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.taskType")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.goal")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.reason")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.difficulty")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.route")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.flags")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.warnings")}</th>
                <th className="px-3 py-2 font-medium">{t("observability.routing.columns.attempt")}</th>
              </tr>
            </thead>
            <tbody>
              {events.map((event, index) => (
                <RouteRow key={`${event.recorded_at}-${event.agent_id ?? index}-${index}`} event={event} />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {events.length < total ? (
        <div className="mt-2 flex justify-center">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => void load(offset, true)}
            disabled={loading}
          >
            {t("observability.routing.loadMore")}
          </Button>
        </div>
      ) : null}
    </section>
  );
}

function RouteRow({ event }: { event: AnalyticsRouteEvent }) {
  const { t } = useTranslation("usageAnalytics");
  const routeText = [event.provider, event.model].filter(Boolean).join(" / ") || t("observability.routing.flags.notRecorded");
  const warnings = event.warnings ?? [];
  return (
    <tr className="border-b border-border/70 last:border-b-0 hover:bg-surface-soft-hover">
      <td className="px-3 py-2.5 text-xs text-muted-foreground">{formatTimestamp(event.recorded_at)}</td>
      <td className="px-3 py-2.5">{scopeLabel(t, event.scope)}</td>
      <td className="px-3 py-2.5">
        <Badge className={kindTone(event.kind)}>{kindLabel(t, event.kind)}</Badge>
      </td>
      <td className="px-3 py-2.5">
        <div className="font-mono text-xs" title={event.agent_id}>
          {event.agent_id ? shortID(event.agent_id) : "-"}
        </div>
        <div className="mt-0.5 text-xs text-muted-foreground">
          {event.role || sourceLabel(t, event.source)}
        </div>
        {event.child_session_id ? (
          <div className="mt-0.5 font-mono text-xs text-muted-foreground" title={event.child_session_id}>
            {shortID(event.child_session_id)}
          </div>
        ) : null}
      </td>
      <td className="px-3 py-2.5">
        <div>{event.task_type ? taskTypeLabel(t, event.task_type) : "-"}</div>
        {event.task_subject ? (
          <div
            className="mt-0.5 max-w-[16rem] truncate text-xs text-muted-foreground"
            title={event.task_subject}
          >
            {event.task_subject}
          </div>
        ) : null}
      </td>
      <td className="px-3 py-2.5">
        <div className="max-w-[16rem] truncate text-xs" title={event.goal || undefined}>
          {event.goal || "-"}
        </div>
      </td>
      <td className="px-3 py-2.5">
        <div>{reasonLabel(t, event.reason)}</div>
        <div className="mt-0.5 text-xs text-muted-foreground">{sourceLabel(t, event.source)}</div>
      </td>
      <td className="px-3 py-2.5">
        <div>{(event.difficulty ?? "").trim() || t("observability.routing.flags.notRecorded")}</div>
        {event.difficulty_source ? (
          <div className="mt-0.5 text-xs text-muted-foreground">{difficultySourceLabel(t, event.difficulty_source)}</div>
        ) : null}
        {event.reasoning_effort ? (
          <div className="mt-0.5 text-xs text-muted-foreground">{`effort=${event.reasoning_effort}`}</div>
        ) : null}
      </td>
      <td className="px-3 py-2.5">
        <div className="text-xs">{routeText}</div>
        {typeof event.candidate_count === "number" && event.candidate_count > 0 ? (
          <div className="mt-0.5 text-xs text-muted-foreground">
            {t("observability.routing.metricDetail.candidateTotal", { count: event.candidate_count })}
          </div>
        ) : null}
      </td>
      <td className="px-3 py-2.5 text-xs">
        <div>{triStateLabel(t, event.route_changed, routeFlagKeys.routeChanged, routeFlagKeys.routeUnchanged)}</div>
        <div className="mt-0.5 text-muted-foreground">
          {triStateLabel(t, event.fallback_used, routeFlagKeys.fallbackUsed, routeFlagKeys.fallbackUnused)}
          {event.fallback_reason ? ` · ${event.fallback_reason}` : ""}
        </div>
      </td>
      <td className="px-3 py-2.5 text-xs">
        {warnings.length > 0 ? (
          <div title={warnings.join("\n")} className="text-analytics-warning">
            {t("observability.routing.warningCount", { count: warnings.length })}
            <div className="mt-0.5 font-mono text-[0.7rem] break-all text-muted-foreground">
              {warningLabel(t, warnings[0])}
            </div>
          </div>
        ) : (
          "-"
        )}
      </td>
      <td className="px-3 py-2.5 tabular-nums text-xs">
        {event.max_attempts && event.max_attempts > 1
          ? t("observability.routing.attemptBadge", {
              attempt: String(event.attempt ?? 0),
              max: String(event.max_attempts),
            })
          : formatNumber(event.attempt ?? 0)}
      </td>
    </tr>
  );
}

function DistributionList({
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
