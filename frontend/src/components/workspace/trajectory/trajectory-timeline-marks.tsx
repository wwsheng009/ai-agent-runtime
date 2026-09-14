/**
 * 轨迹时间线第一个时间轴的色块层（从 `trajectory-timeline.tsx` 抽出，满足行数预算）。
 *
 * 为什么需要「逐条渲染」：时间轴按等宽桶聚合时，**同一时刻集中到达的多条消息**
 * （一轮收尾时的 orchestration / route / tool / observation / result 只相隔几毫秒）
 * 会落进同一个桶，明细列表有 7 条而时间轴只剩 3 个块，读起来像「少了几条」。
 *
 * 规则：
 * - **稀疏窗口**（窗口内条目数 ≤ `STACK_MARK_LIMIT`）：桶内每条消息各渲染一个色块，
 *   在桶内自下而上堆叠——列表有几条消息，时间轴上就有几个块，且每块各自可点击
 *   跳转到对应行（不再只跳桶内首条）；
 * - **密集窗口**：维持按桶聚合（651 条事件 ≈ 160 个块），避免时间轴退化成色带；
 * - 桶高仍 ∝ 事件密度（`sqrt` 缩放），所以「哪里密」一眼可见。
 */
import { useTranslation } from "react-i18next";

import {
  TRAJECTORY_KIND_PRIORITY,
  type TrajectoryTimelineBucket,
} from "@/lib/trajectory/timeline-window";
import type { TrajectoryItemKind } from "@/lib/trajectory/types";
import { cn } from "@/lib/utils";

import { KIND_BAR_CLASS } from "./trajectory-timeline-colors";
import {
  trajectoryItemKindKey,
  trajectoryItemSummary,
} from "./trajectory-view-shared";

/** 逐条渲染的窗口内条目数上限：超过则回到分桶聚合（DOM 节点数有界）。 */
export const STACK_MARK_LIMIT = 200;
/** 单个桶最多堆叠的色块数（更密的桶只保留前 N 块，总数仍在 tooltip 里）。 */
export const MAX_BUCKET_MARKS = 8;

type BucketMarksProps = {
  buckets: TrajectoryTimelineBucket[];
  /** 最密桶的条数（桶高按 sqrt 缩放到它）。 */
  maxBucketCount: number;
  /** 是否逐条渲染（窗口稀疏时）而不是一桶一块。 */
  stackMarks: boolean;
  onJumpToItem: (itemId: string) => void;
};

export function TrajectoryTimelineBucketMarks({
  buckets,
  maxBucketCount,
  stackMarks,
  onJumpToItem,
}: BucketMarksProps) {
  const { t } = useTranslation("workspace");

  return (
    <>
      {buckets.map((bucket) => {
        const left = `${bucket.left * 100}%`;
        const width = `calc(${bucket.width * 100}% - 1px)`;
        const height = `${8 + 20 * Math.sqrt(bucket.count / maxBucketCount)}px`;
        const kindLabel = t(trajectoryItemKindKey(bucket.kind));
        const marks = stackMarks ? bucket.items.slice(0, MAX_BUCKET_MARKS) : [];

        if (marks.length <= 1) {
          const single = bucket.count === 1 ? bucket.items[0] : undefined;
          return (
            <button
              key={bucket.key}
              aria-label={
                single
                  ? t("panels.shell.trajectory.timeline.pointAriaLabel", {
                      kind: t(trajectoryItemKindKey(single.kind)),
                      seq: single.seq,
                    })
                  : t("panels.shell.trajectory.timeline.bucketKindAriaLabel", {
                      count: bucket.count,
                      kind: kindLabel,
                    })
              }
              className={cn(
                "absolute bottom-0 cursor-pointer rounded-[3px] opacity-85 transition hover:opacity-100 focus-visible:ring-1 focus-visible:ring-accent-teal focus-visible:outline-none",
                KIND_BAR_CLASS[bucket.kind],
                bucket.status === "running" && "animate-pulse",
              )}
              data-count={bucket.count}
              data-kind={bucket.kind}
              data-status={bucket.status}
              data-testid="trajectory-timeline-bar"
              onClick={() => onJumpToItem(bucket.firstItemId)}
              style={{ left, width, minWidth: "2px", height }}
              title={
                single
                  ? t("panels.shell.trajectory.timeline.pointTitle", {
                      seq: single.seq,
                      kind: t(trajectoryItemKindKey(single.kind)),
                      summary: trajectoryItemSummary(single, 80),
                    })
                  : t("panels.shell.trajectory.timeline.bucketKindTitle", {
                      count: bucket.count,
                      kind: kindLabel,
                      breakdown: kindBreakdown(bucket.kindCounts, (kind) =>
                        t(trajectoryItemKindKey(kind)),
                      ),
                    })
              }
              type="button"
            />
          );
        }

        // 桶内多条：自下而上逐条堆叠（桶内顺序 = 时间顺序），每块独立可点。
        return (
          <div
            key={bucket.key}
            aria-label={t("panels.shell.trajectory.timeline.bucketKindAriaLabel", {
              count: bucket.count,
              kind: kindLabel,
            })}
            className="absolute bottom-0 flex flex-col-reverse gap-px"
            data-count={bucket.count}
            data-kind={bucket.kind}
            data-overflow={bucket.count - marks.length}
            data-status={bucket.status}
            data-testid="trajectory-timeline-bucket"
            style={{ left, width, minWidth: "2px", height }}
            title={t("panels.shell.trajectory.timeline.bucketKindTitle", {
              count: bucket.count,
              kind: kindLabel,
              breakdown: kindBreakdown(bucket.kindCounts, (kind) =>
                t(trajectoryItemKindKey(kind)),
              ),
            })}
          >
            {marks.map((item) => (
              <button
                key={item.id}
                aria-label={t("panels.shell.trajectory.timeline.pointAriaLabel", {
                  kind: t(trajectoryItemKindKey(item.kind)),
                  seq: item.seq,
                })}
                className={cn(
                  "min-h-0.5 w-full flex-1 cursor-pointer rounded-[2px] opacity-85 transition hover:opacity-100 focus-visible:ring-1 focus-visible:ring-accent-teal focus-visible:outline-none",
                  KIND_BAR_CLASS[item.kind],
                  item.status === "running" && "animate-pulse",
                )}
                data-count="1"
                data-kind={item.kind}
                data-status={item.status}
                data-testid="trajectory-timeline-bar"
                onClick={() => onJumpToItem(item.id)}
                title={t("panels.shell.trajectory.timeline.pointTitle", {
                  seq: item.seq,
                  kind: t(trajectoryItemKindKey(item.kind)),
                  summary: trajectoryItemSummary(item, 80),
                })}
                type="button"
              />
            ))}
          </div>
        );
      })}
    </>
  );
}

/** 桶内消息类型明细（tooltip 用）：按展示优先级排序。 */
function kindBreakdown(
  counts: Partial<Record<TrajectoryItemKind, number>>,
  labelOf: (kind: TrajectoryItemKind) => string,
): string {
  return TRAJECTORY_KIND_PRIORITY.filter((kind) => (counts[kind] ?? 0) > 0)
    .map((kind) => `${labelOf(kind)} ${counts[kind] ?? 0}`)
    .join(" · ");
}
