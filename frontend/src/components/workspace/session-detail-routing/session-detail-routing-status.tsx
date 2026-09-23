// 「路由」区块的状态摘要卡：启用态 + 生效档位 + 生效时机 + 摘要字段。
//
// 语义纪律（I-6）：所有值直接取自后端投影（§6.1），空值统一显示「—」，
// 前端不推导、不补默认值；`effective_from` 只把已知枚举 next_turn 本地化，
// 其它取值原样展示（后端加了新时机也不用改前端）。

import { Fragment } from "react";
import { useTranslation } from "react-i18next";

import {
  SESSION_DETAIL_CHIP_CLASS,
  SESSION_DETAIL_SECTION_LABEL_CLASS,
  SESSION_DETAIL_SUBCARD_CLASS,
} from "@/components/workspace/session-detail-panel-shared";
import { cn } from "@/lib/utils";
import type { RoutingStatusProjection } from "@/types/runtime";

const SUMMARY_ROWS = [
  { key: "provider", labelKey: "panels.sessionDetail.routing.summary.provider" },
  { key: "model", labelKey: "panels.sessionDetail.routing.summary.model" },
  { key: "effort", labelKey: "panels.sessionDetail.routing.summary.effort" },
  { key: "source", labelKey: "panels.sessionDetail.routing.summary.source" },
  { key: "revision", labelKey: "panels.sessionDetail.routing.summary.revision" },
] as const;

export type SessionDetailRoutingStatusProps = {
  routing: RoutingStatusProjection;
};

export function SessionDetailRoutingStatus({
  routing,
}: SessionDetailRoutingStatusProps) {
  const { t } = useTranslation("workspace");

  return (
    <div className={cn(SESSION_DETAIL_SUBCARD_CLASS, "grid gap-1")}>
      <div className="flex flex-wrap items-center gap-1.5">
        <span
          className={cn(
            SESSION_DETAIL_CHIP_CLASS,
            routing.enabled
              ? "border-accent-primary/30 bg-accent-primary/10 text-accent-primary"
              : "border-border bg-surface-soft text-muted-foreground",
          )}
          data-testid="routing-state"
        >
          {routing.enabled
            ? t("panels.sessionDetail.routing.state.enabled")
            : t("panels.sessionDetail.routing.state.disabled")}
        </span>
        {routing.level ? (
          <span className="min-w-0 truncate app-text-11 text-foreground">
            {routing.level}
          </span>
        ) : null}
        {routing.effectiveFrom ? (
          <span className="app-text-10 text-muted-foreground">
            {routing.effectiveFrom === "next_turn"
              ? t("panels.sessionDetail.routing.summary.effectiveFromNextTurn")
              : routing.effectiveFrom}
          </span>
        ) : null}
      </div>
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-2 gap-y-0.5">
        {SUMMARY_ROWS.map((row) => (
          <Fragment key={row.key}>
            <dt className={SESSION_DETAIL_SECTION_LABEL_CLASS}>{t(row.labelKey)}</dt>
            <dd
              className="min-w-0 break-all app-text-10 text-foreground"
              data-testid={`routing-summary-${row.key}`}
            >
              {readSummaryValue(routing, row.key) ||
                t("panels.sessionDetail.routing.summary.none")}
            </dd>
          </Fragment>
        ))}
      </dl>
    </div>
  );
}

function readSummaryValue(
  routing: RoutingStatusProjection,
  key: (typeof SUMMARY_ROWS)[number]["key"],
): string {
  switch (key) {
    case "provider":
      return routing.provider;
    case "model":
      return routing.model;
    case "effort":
      return routing.reasoning;
    case "source":
      return routing.source;
    default:
      return routing.revision;
  }
}
