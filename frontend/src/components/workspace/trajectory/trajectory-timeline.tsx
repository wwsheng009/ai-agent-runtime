/**
 * 轨迹时间线概览（P2-2，sequence 模式先行；duration 模式为开放问题 Q2）。
 *
 * - 主泳道：每个 Item 一个色块，位置按事件 seq 比例分布（一眼区分工具密集段与纯对话段）；
 * - tool 泳道：工具调用单独高亮车道；
 * - 点击色块 → 明细列表滚动到对应行（联动）。
 */
import { useMemo } from "react";

import type { TrajectoryItem } from "@/lib/trajectory/types";
import { cn } from "@/lib/utils";

import { trajectoryItemKindLabel, trajectoryItemSummary } from "./trajectory-view-shared";

const KIND_COLORS: Record<TrajectoryItem["kind"], string> = {
  assistant: "bg-[#6ea8fe]",
  reasoning: "bg-[#8fd0c6]",
  tool: "bg-[#f0c77b]",
  planning: "bg-[#a78bfa]",
  orchestration: "bg-[#a78bfa]",
  route: "bg-[#a78bfa]",
  observation: "bg-[#a78bfa]",
  subagent: "bg-[#a78bfa]",
  result: "bg-[#a78bfa]",
  system: "bg-[#8a8f98]",
};

function itemColor(item: TrajectoryItem): string {
  return KIND_COLORS[item.kind] ?? "bg-[#8a8f98]";
}

type TrajectoryTimelineProps = {
  items: TrajectoryItem[];
  onJumpToItem: (itemId: string) => void;
  className?: string;
};

export function TrajectoryTimeline({
  items,
  onJumpToItem,
  className,
}: TrajectoryTimelineProps) {
  const segments = useMemo(() => {
    if (items.length === 0) {
      return [];
    }
    const minSeq = items[0].seq;
    const maxSeq = items[items.length - 1].seq;
    const span = Math.max(1, maxSeq - minSeq);
    return items.map((item, index) => {
      const left = ((item.seq - minSeq) / span) * 100;
      // 色块宽度：至少 2px；与下一事件间距相关（避免 100+ 事件全屏堆叠）
      const nextSeq = index + 1 < items.length ? items[index + 1].seq : item.seq;
      const rawWidth = ((nextSeq - item.seq) / span) * 100;
      const width = Math.max(2, Math.min(rawWidth, 100 - left));
      return { item, left, width };
    });
  }, [items]);

  if (items.length === 0) {
    return null;
  }

  const toolItems = items.filter((item) => item.kind === "tool");

  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      <div
        aria-label="Trajectory timeline"
        className="relative h-7 w-full overflow-hidden rounded-md border border-[var(--border)] bg-[var(--surface-solid)]"
        role="img"
      >
        {segments.map(({ item, left, width }) => (
          <button
            key={item.id}
            aria-label={`${trajectoryItemKindLabel(item.kind)} ${item.seq}`}
            className={cn(
              "absolute top-0.5 h-5 cursor-pointer rounded-[3px] opacity-80 transition hover:opacity-100",
              itemColor(item),
            )}
            onClick={() => onJumpToItem(item.id)}
            style={{ left: `${left}%`, width: `${width}%` }}
            title={`#${item.seq} ${trajectoryItemKindLabel(item.kind)} — ${trajectoryItemSummary(item, 80)}`}
            type="button"
          />
        ))}
      </div>
      {toolItems.length > 0 ? (
        <div
          aria-label="Tool lane"
          className="relative h-2.5 w-full overflow-hidden rounded-sm bg-[var(--surface-solid)]"
          role="img"
        >
          {toolItems.map((item) => {
            const minSeq = items[0].seq;
            const maxSeq = items[items.length - 1].seq;
            const span = Math.max(1, maxSeq - minSeq);
            const left = ((item.seq - minSeq) / span) * 100;
            return (
              <button
                key={item.id}
                aria-label={`tool ${item.head.kind === "tool" ? item.head.name : item.seq}`}
                className="absolute top-0 h-2.5 min-w-1 cursor-pointer rounded-[2px] bg-[#f0c77b] transition hover:opacity-90"
                onClick={() => onJumpToItem(item.id)}
                style={{ left: `${left}%` }}
                title={`tool ${item.head.kind === "tool" ? item.head.name : ""} (#${item.seq})`}
                type="button"
              />
            );
          })}
        </div>
      ) : null}
    </div>
  );
}
