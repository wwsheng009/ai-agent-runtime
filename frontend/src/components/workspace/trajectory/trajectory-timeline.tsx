/**
 * 轨迹时间线条状图（P2-2 概览 + P2-8 交互增强 + P2-9 消息类型轴与联动）。
 *
 * - 轴：item 带墙钟时间（`_event.timestamp` → `TrajectoryItem.at`）时用**时间轴**，
 *   缺失（会话历史兜底投影）时退化为序号轴——两者共用同一套缩放/分桶数学；
 * - **第一个时间轴（主泳道）按消息类型着色**：用户消息 / 助手消息 / 工具 / 推理 /
 *   编排类 / 系统——看清「这段时间在发生什么」，而不是「哪条错了」；
 *   窗口稀疏时桶内逐条堆叠（列表几条消息就几个块，每块可点），条目密集时按桶
 *   聚合（桶高 ∝ 事件密度，桶色 = 桶内主导类型），见 `trajectory-timeline-marks`；
 * - **状态泳道（细条）**：同一批桶按运行状态着色（失败/运行中/完成…），
 *   正确/错误信息退到第二个时间轴，需要定位失败时看它；
 * - 可缩放的时间区间：滚轮（锚点=指针位置）/ 拖拽框选 / 按钮 / 键盘；
 * - 时间间隔预设：最近 1m / 5m / 15m / 1h（点选即缩放到轴尾部区间）；
 * - 工具泳道：工具调用单独高亮车道（金色）；
 * - 联动：`onViewportChange` 把 `{axis, window}` 透给父级（明细列表同步过滤），
 *   类型图例点击 = 只看该类消息（同样由父级同步到列表）；
 * - 刻度（时间/序号）+ 类型图例（可点击）+ 状态图例（计数）+ 可见条数提示；
 * - 点击色块或桶 → 明细列表滚动到对应行（联动）。
 */
import {
  ChevronLeftIcon,
  ChevronRightIcon,
  Maximize2Icon,
  MinusIcon,
  PlusIcon,
} from "lucide-react";
import { useEffect, useId, useMemo } from "react";
import { useTranslation } from "react-i18next";

import {
  bucketTrajectoryItems,
  formatTrajectoryAxisTick,
  formatTrajectoryDuration,
  trajectoryAxisRatio,
  trajectoryKindCounts,
  trajectoryStatusCounts,
  trajectoryTimelineTicks,
  type TrajectoryTimelineAxis,
  type TrajectoryTimelineWindow,
} from "@/lib/trajectory/timeline-window";
import type {
  TrajectoryItem,
  TrajectoryItemKind,
} from "@/lib/trajectory/types";
import { cn } from "@/lib/utils";

import {
  TrajectoryTimelineStatusLane,
  TrajectoryTimelineToolLane,
} from "./trajectory-timeline-lane";
import {
  TrajectoryTimelineKindLegend,
  TrajectoryTimelineStatusLegend,
} from "./trajectory-timeline-legend";
import {
  STACK_MARK_LIMIT,
  TrajectoryTimelineBucketMarks,
} from "./trajectory-timeline-marks";
import { useTrajectoryTimelineWindow } from "./use-trajectory-timeline-window";

/** 单屏最大桶数：超过则聚合（651 条事件也不会退化成色带）。 */
const MAX_BUCKETS = 160;
/** 时间间隔预设（时间轴专用）：最近 1m / 5m / 15m / 1h。 */
const SPAN_PRESETS_MS: readonly number[] = [
  60_000,
  5 * 60_000,
  15 * 60_000,
  60 * 60_000,
];
/** 图表视窗（父级用它与明细列表同步过滤）。 */
export type TrajectoryTimelineViewport = {
  axis: TrajectoryTimelineAxis;
  window: TrajectoryTimelineWindow;
};

type TrajectoryTimelineProps = {
  items: TrajectoryItem[];
  onJumpToItem: (itemId: string) => void;
  /** 当前选中的消息类型（null = 全部）；图表与列表由父级同步。 */
  activeKind?: TrajectoryItemKind | null;
  /** 点击类型图例：父级切换 `activeKind`（图表 + 列表一起过滤）。 */
  onToggleKind?: (kind: TrajectoryItemKind) => void;
  /** 视窗变化回调（必须是稳定引用，避免每次渲染触发 effect）。 */
  onViewportChange?: (viewport: TrajectoryTimelineViewport | null) => void;
  className?: string;
};

export function TrajectoryTimeline({
  items,
  onJumpToItem,
  activeKind = null,
  onToggleKind,
  onViewportChange,
  className,
}: TrajectoryTimelineProps) {
  const { t } = useTranslation("workspace");
  const hintId = useId();
  // 类型筛选只改变图形几何：类型图例的计数仍取全量 items（否则选中后无法切回）。
  const chartItems = useMemo(
    () => (activeKind ? items.filter((item) => item.kind === activeKind) : items),
    [activeKind, items],
  );
  const {
    laneRef,
    axis,
    window,
    selection,
    zoomed,
    zoomPercent,
    handlePointerDown,
    handlePointerMove,
    handlePointerUp,
    handlePointerCancel,
    handleZoomIn,
    handleZoomOut,
    handleReset,
    handlePanLeft,
    handlePanRight,
    applyTrailingSpan,
    handleKeyDown,
  } = useTrajectoryTimelineWindow(chartItems);

  // 视窗透出：父级据此同步过滤明细列表（缩放/平移/框选/预设都走这里）。
  useEffect(() => {
    onViewportChange?.({ axis, window });
  }, [axis, onViewportChange, window]);
  useEffect(() => {
    if (!onViewportChange) {
      return;
    }
    // 卸载（时间线隐藏 / 列表已空）时清空视窗，避免父级残留过期区间。
    return () => onViewportChange(null);
  }, [onViewportChange]);

  const selectionBox = selection
    ? {
        left: Math.min(selection.startRatio, selection.endRatio) * 100,
        width: Math.abs(selection.endRatio - selection.startRatio) * 100,
      }
    : null;

  const buckets = useMemo(
    () => bucketTrajectoryItems(chartItems, axis.positions, window, MAX_BUCKETS),
    [axis, chartItems, window],
  );
  const visibleCount = useMemo(
    () => buckets.reduce((sum, bucket) => sum + bucket.count, 0),
    [buckets],
  );
  const maxBucketCount = useMemo(
    () => buckets.reduce((max, bucket) => Math.max(max, bucket.count), 1),
    [buckets],
  );
  const ticks = useMemo(
    () => trajectoryTimelineTicks(axis, window, chartItems, 4),
    [axis, chartItems, window],
  );
  const kindCounts = useMemo(() => trajectoryKindCounts(items), [items]);
  const statusCounts = useMemo(
    () => trajectoryStatusCounts(chartItems),
    [chartItems],
  );
  const toolPoints = useMemo(
    () =>
      chartItems
        .map((item, index) => ({
          item,
          ratio: trajectoryAxisRatio(window, axis.positions[index] ?? 0),
        }))
        .filter((point) => point.item.kind === "tool" && point.ratio >= 0 && point.ratio <= 1),
    [axis, chartItems, window],
  );

  const rangeFrom = formatTrajectoryAxisTick(axis, window.start, chartItems);
  const rangeTo = formatTrajectoryAxisTick(axis, window.end, chartItems);
  // 预设只在时间轴且确实比全轴短时出现——点了必然产生缩放，不出现空操作。
  const spanPresets = useMemo(() => {
    if (axis.kind !== "time") {
      return [];
    }
    const full = axis.max - axis.min;
    return SPAN_PRESETS_MS.filter((span) => span < full);
  }, [axis]);
  const activePreset = useMemo(() => {
    if (spanPresets.length === 0 || window.end < axis.max - 1) {
      return null;
    }
    const span = window.end - window.start;
    return (
      spanPresets.find((preset) => Math.abs(preset - span) <= 1000) ?? null
    );
  }, [axis.max, spanPresets, window]);

  if (chartItems.length === 0) {
    return null;
  }

  return (
    <div
      className={cn("flex flex-col gap-1", className)}
      data-testid="trajectory-timeline"
    >
      <span className="sr-only" id={hintId}>
        {t("panels.shell.trajectory.timeline.selectionHint")}
      </span>
      <div className="flex items-center justify-between gap-2">
        <span
          className="truncate text-[11px] text-muted-foreground"
          data-testid="trajectory-timeline-range"
          title={t("panels.shell.trajectory.timeline.selectionHint")}
        >
          {t("panels.shell.trajectory.timeline.rangeLabel", {
            axis: t(
              axis.kind === "time"
                ? "panels.shell.trajectory.timeline.axisTime"
                : "panels.shell.trajectory.timeline.axisSeq",
            ),
            from: rangeFrom,
            to: rangeTo,
            visible: visibleCount,
            total: chartItems.length,
            zoom: zoomPercent,
          })}
        </span>
        <div
          aria-label={t("panels.shell.trajectory.timeline.controls")}
          className="flex items-center gap-0.5"
          role="group"
        >
          <TimelineIconButton
            disabled={!zoomed}
            label={t("panels.shell.trajectory.timeline.panLeft")}
            testId="trajectory-timeline-pan-left"
            onClick={handlePanLeft}
          >
            <ChevronLeftIcon size={13} />
          </TimelineIconButton>
          <TimelineIconButton
            label={t("panels.shell.trajectory.timeline.zoomOut")}
            testId="trajectory-timeline-zoom-out"
            onClick={handleZoomOut}
          >
            <MinusIcon size={13} />
          </TimelineIconButton>
          <TimelineIconButton
            label={t("panels.shell.trajectory.timeline.zoomIn")}
            testId="trajectory-timeline-zoom-in"
            onClick={handleZoomIn}
          >
            <PlusIcon size={13} />
          </TimelineIconButton>
          <TimelineIconButton
            disabled={!zoomed}
            label={t("panels.shell.trajectory.timeline.zoomReset")}
            testId="trajectory-timeline-zoom-reset"
            onClick={handleReset}
          >
            <Maximize2Icon size={13} />
          </TimelineIconButton>
          <TimelineIconButton
            disabled={!zoomed}
            label={t("panels.shell.trajectory.timeline.panRight")}
            testId="trajectory-timeline-pan-right"
            onClick={handlePanRight}
          >
            <ChevronRightIcon size={13} />
          </TimelineIconButton>
        </div>
      </div>

      {spanPresets.length > 0 ? (
        <div
          aria-label={t("panels.shell.trajectory.timeline.presetGroup")}
          className="flex flex-wrap items-center gap-1 text-[10px] text-muted-foreground"
          data-testid="trajectory-timeline-presets"
          role="group"
        >
          {spanPresets.map((span) => (
            <button
              key={span}
              aria-label={t("panels.shell.trajectory.timeline.presetAriaLabel", {
                span: formatTrajectoryDuration(span),
              })}
              aria-pressed={activePreset === span}
              className={cn(
                "cursor-pointer rounded-[4px] border px-1.5 py-0.5 transition hover:text-foreground",
                activePreset === span
                  ? "border-accent-teal text-foreground"
                  : "border-border",
              )}
              data-span-ms={span}
              data-testid="trajectory-timeline-preset"
              onClick={() => applyTrailingSpan(span)}
              type="button"
            >
              {formatTrajectoryDuration(span)}
            </button>
          ))}
        </div>
      ) : null}

      <div
        ref={laneRef}
        aria-describedby={hintId}
        aria-label={t("panels.shell.trajectory.timeline.ariaLabel")}
        className="relative h-9 w-full cursor-crosshair touch-none overflow-hidden rounded-md border border-border bg-surface-solid select-none"
        data-axis-kind={axis.kind}
        data-active-kind={activeKind ?? ""}
        data-total-count={chartItems.length}
        data-visible-count={visibleCount}
        data-window-end={window.end}
        data-window-start={window.start}
        data-zoomed={zoomed ? "true" : "false"}
        onKeyDown={handleKeyDown}
        onPointerCancel={handlePointerCancel}
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={handlePointerUp}
        role="group"
        tabIndex={0}
      >
        <TrajectoryTimelineBucketMarks
          buckets={buckets}
          maxBucketCount={maxBucketCount}
          onJumpToItem={onJumpToItem}
          stackMarks={visibleCount <= STACK_MARK_LIMIT}
        />
        {selectionBox ? (
          <div
            aria-hidden="true"
            className="pointer-events-none absolute inset-y-0 border-x border-accent-teal bg-accent-teal/20"
            data-testid="trajectory-timeline-selection"
            style={{ left: `${selectionBox.left}%`, width: `${selectionBox.width}%` }}
          />
        ) : null}
      </div>

      <div className="relative h-4" data-testid="trajectory-timeline-ticks">
        {ticks.map((tick, index) => (
          <span
            key={`${tick.value}-${index}`}
            className={cn(
              "absolute top-0 text-[10px] whitespace-nowrap text-muted-foreground",
              index === 0
                ? "left-0"
                : index === ticks.length - 1
                  ? "right-0"
                  : "-translate-x-1/2",
            )}
            style={index === 0 || index === ticks.length - 1 ? undefined : { left: `${tick.ratio * 100}%` }}
          >
            {tick.label}
          </span>
        ))}
      </div>

      {/* 第一个时间轴的图例（消息类型，可点选过滤）：紧跟色块与刻度。 */}
      <TrajectoryTimelineKindLegend
        activeKind={activeKind}
        counts={kindCounts}
        onToggleKind={onToggleKind}
      />

      {/* 分组发丝线：三条泳道各自成组，避免视觉上粘成一整块。 */}
      <div
        aria-hidden="true"
        className="-my-0.5 h-px w-full bg-border/60"
        data-testid="trajectory-timeline-divider"
      />
      {/* 第二个时间轴：运行状态（失败/运行中/取消/待定/完成）。
          第一个时间轴只看消息类型，正确/错误信息放在这里，需要时再看。 */}
      <TrajectoryTimelineStatusLane buckets={buckets} onJumpToItem={onJumpToItem} />

      <TrajectoryTimelineStatusLegend counts={statusCounts} />

      {toolPoints.length > 0 ? (
        <>
          <div
            aria-hidden="true"
            className="-my-0.5 h-px w-full bg-border/60"
            data-testid="trajectory-timeline-divider"
          />
          {/* 第三个时间轴：工具调用单独成组（无工具时整组不渲染）。 */}
          <TrajectoryTimelineToolLane points={toolPoints} onJumpToItem={onJumpToItem} />
        </>
      ) : null}
    </div>
  );
}

function TimelineIconButton({
  children,
  disabled,
  label,
  onClick,
  testId,
}: {
  children: React.ReactNode;
  disabled?: boolean;
  label: string;
  onClick: () => void;
  testId: string;
}) {
  return (
    <button
      aria-label={label}
      className="rounded-[4px] p-0.5 text-muted-foreground transition hover:bg-surface-solid hover:text-foreground disabled:cursor-default disabled:opacity-35 disabled:hover:bg-transparent"
      data-testid={testId}
      disabled={disabled}
      onClick={onClick}
      title={label}
      type="button"
    >
      {children}
    </button>
  );
}


