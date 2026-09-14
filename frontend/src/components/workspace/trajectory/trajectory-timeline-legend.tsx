/**
 * 轨迹时间线图例（P2-9）。
 *
 * - **消息类型图例**：第一个时间轴的说明，可点击 → 只看该类（父级同步过滤明细列表）；
 * - **状态图例**：第二个时间轴（状态泳道）的说明，只读计数。
 */
import { useTranslation } from "react-i18next";

import {
  TRAJECTORY_KIND_PRIORITY,
  TRAJECTORY_STATUS_PRIORITY,
} from "@/lib/trajectory/timeline-window";
import type {
  TrajectoryItemKind,
  TrajectoryItemStatus,
} from "@/lib/trajectory/types";
import { cn } from "@/lib/utils";

import { KIND_BAR_CLASS, STATUS_BAR_CLASS } from "./trajectory-timeline-colors";
import {
  trajectoryItemKindKey,
  trajectoryItemStatusKey,
} from "./trajectory-view-shared";

export function TrajectoryTimelineKindLegend({
  counts,
  activeKind = null,
  onToggleKind,
}: {
  counts: Record<TrajectoryItemKind, number>;
  activeKind?: TrajectoryItemKind | null;
  onToggleKind?: (kind: TrajectoryItemKind) => void;
}) {
  const { t } = useTranslation("workspace");
  return (
    <div
      aria-label={t("panels.shell.trajectory.timeline.kindLegend")}
      className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[10px] text-muted-foreground"
      data-testid="trajectory-timeline-kind-legend"
      role="group"
    >
      {TRAJECTORY_KIND_PRIORITY.filter((kind) => counts[kind] > 0).map((kind) => {
        const active = activeKind === kind;
        const label = t(trajectoryItemKindKey(kind));
        const shared = {
          className: cn(
            "inline-flex items-center gap-1 rounded-[3px] px-1",
            onToggleKind && "cursor-pointer transition hover:text-foreground",
            active && "bg-surface-soft text-foreground ring-1 ring-accent-teal",
          ),
          "data-count": counts[kind],
          "data-kind": kind,
          "data-testid": "trajectory-timeline-kind-legend-item",
          title: t("panels.shell.trajectory.timeline.kindLegendToggle", {
            kind: label,
            count: counts[kind],
          }),
        };
        const content = (
          <>
            <span
              aria-hidden="true"
              className={cn("inline-block h-2 w-2 rounded-[2px]", KIND_BAR_CLASS[kind])}
            />
            {t("panels.shell.trajectory.timeline.kindLegendItem", {
              kind: label,
              count: counts[kind],
            })}
          </>
        );
        return onToggleKind ? (
          <button
            key={kind}
            {...shared}
            aria-pressed={active}
            onClick={() => onToggleKind(kind)}
            type="button"
          >
            {content}
          </button>
        ) : (
          <span key={kind} {...shared}>
            {content}
          </span>
        );
      })}
    </div>
  );
}

export function TrajectoryTimelineStatusLegend({
  counts,
}: {
  counts: Record<TrajectoryItemStatus, number>;
}) {
  const { t } = useTranslation("workspace");
  return (
    <div
      aria-label={t("panels.shell.trajectory.timeline.statusLegend")}
      className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[10px] text-muted-foreground"
      data-testid="trajectory-timeline-legend"
      role="group"
    >
      {TRAJECTORY_STATUS_PRIORITY.filter((status) => counts[status] > 0).map(
        (status) => (
          <span
            key={status}
            className="inline-flex items-center gap-1"
            data-count={counts[status]}
            data-status={status}
            data-testid="trajectory-timeline-legend-item"
          >
            <span
              aria-hidden="true"
              className={cn("inline-block h-2 w-2 rounded-[2px]", STATUS_BAR_CLASS[status])}
            />
            {t("panels.shell.trajectory.timeline.legendItem", {
              status: t(trajectoryItemStatusKey(status)),
              count: counts[status],
            })}
          </span>
        ),
      )}
    </div>
  );
}
