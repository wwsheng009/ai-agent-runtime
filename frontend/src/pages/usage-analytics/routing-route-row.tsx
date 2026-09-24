// P0-2 拆分：路由事件明细行（原 routing-observability-panel.tsx L476-L566）。

import { Badge } from "@/components/ui/badge";
import type { AnalyticsRouteEvent } from "@/types/runtime";
import { useTranslation } from "react-i18next";

import { formatNumber, formatTimestamp, shortID } from "./format";
import {
  difficultySourceLabel,
  kindLabel,
  kindTone,
  reasonLabel,
  routeFlagKeys,
  scopeLabel,
  sourceLabel,
  taskTypeLabel,
  triStateLabel,
  warningLabel,
} from "./routing-labels";

export function RouteRow({ event }: { event: AnalyticsRouteEvent }) {
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
