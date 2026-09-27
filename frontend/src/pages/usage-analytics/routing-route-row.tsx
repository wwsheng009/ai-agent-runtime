// 路由事件仅在表格内展示单行摘要，完整信息由明细面板承载。

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { AnalyticsRouteEvent } from "@/types/runtime";
import { useTranslation } from "react-i18next";

import { formatTimestamp, shortID } from "./format";
import {
  kindLabel,
  kindTone,
  routeFlagKeys,
  scopeLabel,
  taskTypeLabel,
  triStateLabel,
} from "./routing-labels";

export function RouteRow({
  event,
  onViewDetails,
}: {
  event: AnalyticsRouteEvent;
  onViewDetails: (event: AnalyticsRouteEvent) => void;
}) {
  const { t } = useTranslation("usageAnalytics");
  const routeText = [event.provider, event.model].filter(Boolean).join(" / ") || t("observability.routing.flags.notRecorded");
  const warnings = event.warnings ?? [];
  const openDetails = (target: HTMLElement) => {
    // 行点击也以明细按钮为焦点锚点，关闭面板后可继续使用键盘浏览表格。
    target.closest("tr")?.querySelector("button")?.focus({ preventScroll: true });
    onViewDetails(event);
  };

  return (
    <tr
      className="cursor-pointer border-b border-border/70 text-xs last:border-b-0 hover:bg-surface-soft-hover focus-within:bg-surface-soft-hover"
      onClick={(click) => openDetails(click.currentTarget)}
    >
      <td className="px-3 py-2.5 text-muted-foreground">
        <div className="truncate">{formatTimestamp(event.recorded_at)}</div>
      </td>
      <td className="px-3 py-2.5">
        <div className="truncate">{scopeLabel(t, event.scope)}</div>
      </td>
      <td className="px-3 py-2.5">
        <Badge className={`max-w-full ${kindTone(event.kind)}`}>
          <span className="truncate">{kindLabel(t, event.kind)}</span>
        </Badge>
      </td>
      <td className="px-3 py-2.5">
        <div className="truncate font-mono">
          {event.agent_id ? shortID(event.agent_id) : event.role || "-"}
        </div>
      </td>
      <td className="px-3 py-2.5">
        <div className="truncate">{event.task_type ? taskTypeLabel(t, event.task_type) : "-"}</div>
      </td>
      <td className="px-3 py-2.5">
        <div className="truncate">{event.difficulty?.trim() || t("observability.routing.flags.notRecorded")}</div>
      </td>
      <td className="px-3 py-2.5">
        <div className="truncate">{routeText}</div>
      </td>
      <td className="px-3 py-2.5">
        <div className="truncate">
          {triStateLabel(t, event.route_changed, routeFlagKeys.routeChanged, routeFlagKeys.routeUnchanged)}
          {" / "}
          {triStateLabel(t, event.fallback_used, routeFlagKeys.fallbackUsed, routeFlagKeys.fallbackUnused)}
        </div>
      </td>
      <td className="whitespace-nowrap px-3 py-2.5">
        {warnings.length > 0 ? (
          <Badge className="border-analytics-warning-border bg-analytics-warning-soft text-analytics-warning">
            {t("observability.routing.warningCount", { count: warnings.length })}
          </Badge>
        ) : (
          "-"
        )}
      </td>
      <td className="px-3 py-2.5">
        <Button
          variant="ghost"
          size="sm"
          className="h-7 px-2 text-xs"
          aria-label={t("observability.routing.details.open")}
          aria-haspopup="dialog"
          onClick={(click) => {
            click.stopPropagation();
            openDetails(click.currentTarget);
          }}
        >
          {t("observability.routing.columns.details")}
        </Button>
      </td>
    </tr>
  );
}
