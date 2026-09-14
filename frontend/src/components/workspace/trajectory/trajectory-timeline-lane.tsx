/**
 * 轨迹时间线的第二/第三条泳道（P2-9，从 `trajectory-timeline.tsx` 抽出满足行数预算）。
 *
 * - **状态泳道**：与第一个时间轴同一批桶，但按运行状态着色（失败/运行中/完成…），
 *   第一个时间轴只表达「消息类型」，正确/错误信息在这里看；
 * - **工具泳道**：工具调用逐条高亮（状态着色），带工具名 tooltip。
 */
import { useTranslation } from "react-i18next";

import type { TrajectoryTimelineBucket } from "@/lib/trajectory/timeline-window";
import type { TrajectoryItem } from "@/lib/trajectory/types";
import { cn } from "@/lib/utils";

import { STATUS_BAR_CLASS } from "./trajectory-timeline-colors";
import { trajectoryItemStatusKey } from "./trajectory-view-shared";

export function TrajectoryTimelineStatusLane({
  buckets,
  onJumpToItem,
}: {
  buckets: TrajectoryTimelineBucket[];
  onJumpToItem: (itemId: string) => void;
}) {
  const { t } = useTranslation("workspace");
  return (
    <div
      aria-label={t("panels.shell.trajectory.timeline.statusLane")}
      className="relative h-2.5 w-full overflow-hidden rounded-sm bg-surface-solid"
      data-testid="trajectory-timeline-status-lane"
      role="group"
    >
      {buckets.map((bucket) => (
        <button
          key={bucket.key}
          aria-label={t("panels.shell.trajectory.timeline.bucketAriaLabel", {
            count: bucket.count,
            status: t(trajectoryItemStatusKey(bucket.status)),
          })}
          className={cn(
            "absolute top-0 h-2.5 min-w-0.5 cursor-pointer rounded-[2px] transition hover:opacity-90",
            STATUS_BAR_CLASS[bucket.status],
          )}
          data-count={bucket.count}
          data-status={bucket.status}
          data-testid="trajectory-timeline-status-mark"
          onClick={() => onJumpToItem(bucket.firstItemId)}
          style={{
            left: `${bucket.left * 100}%`,
            width: `calc(${bucket.width * 100}% - 1px)`,
          }}
          title={t("panels.shell.trajectory.timeline.bucketTitle", {
            count: bucket.count,
            status: t(trajectoryItemStatusKey(bucket.status)),
            breakdown: statusBreakdown(bucket.statusCounts, (status) =>
              t(trajectoryItemStatusKey(status)),
            ),
          })}
          type="button"
        />
      ))}
    </div>
  );
}

export function TrajectoryTimelineToolLane({
  points,
  onJumpToItem,
}: {
  points: Array<{ item: TrajectoryItem; ratio: number }>;
  onJumpToItem: (itemId: string) => void;
}) {
  const { t } = useTranslation("workspace");
  if (points.length === 0) {
    return null;
  }
  return (
    <div
      aria-label={t("panels.shell.trajectory.timeline.toolLane")}
      className="relative h-2.5 w-full overflow-hidden rounded-sm bg-surface-solid"
      data-testid="trajectory-timeline-tool-lane"
      role="group"
    >
      {points.map(({ item, ratio }) => (
        <button
          key={item.id}
          aria-label={t("panels.shell.trajectory.timeline.toolAriaLabel", {
            name: item.head.kind === "tool" ? item.head.name : item.seq,
          })}
          className={cn(
            "absolute top-0 h-2.5 min-w-1 cursor-pointer rounded-[2px] transition hover:opacity-90",
            STATUS_BAR_CLASS[item.status],
          )}
          data-status={item.status}
          onClick={() => onJumpToItem(item.id)}
          style={{ left: `${ratio * 100}%` }}
          title={t("panels.shell.trajectory.timeline.toolTitle", {
            name: item.head.kind === "tool" ? item.head.name : "",
            seq: item.seq,
          })}
          type="button"
        />
      ))}
    </div>
  );
}

/** 桶内状态明细（tooltip 用）：按「需要被看见」优先级排序。 */
function statusBreakdown(
  counts: Partial<Record<TrajectoryItem["status"], number>>,
  labelOf: (status: TrajectoryItem["status"]) => string,
): string {
  const order: TrajectoryItem["status"][] = [
    "failed",
    "running",
    "canceled",
    "pending",
    "completed",
  ];
  return order
    .filter((status) => (counts[status] ?? 0) > 0)
    .map((status) => `${labelOf(status)} ${counts[status] ?? 0}`)
    .join(" · ");
}
