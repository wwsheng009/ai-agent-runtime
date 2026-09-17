// 上下文进度环：composer 触发按钮（32px）与面板概览（56px）共用同一个环。
//
// 为什么合并成一个组件：两处本来各写了一份 SVG，结果同一次占用在两处
// strokeWidth/字号口径不一致（同一百分比粗细不同）。统一按 32 的 viewBox 绘制、
// 由 size 决定实际渲染尺寸，颜色随 level 切换，两处只会差字号。

import { cn } from "@/lib/utils";

import type { ContextUsageLevel } from "@/components/workspace/composer-context-usage-shared";

const RING_VIEWBOX = 32;
const RING_STROKE = 3;
const RING_RADIUS = (RING_VIEWBOX - RING_STROKE) / 2;
const RING_CIRCUMFERENCE = 2 * Math.PI * RING_RADIUS;

/** 环颜色与占用等级绑定：unknown 走中性灰（窗口未知时不假装有进度）。 */
const LEVEL_RING_CLASS: Record<ContextUsageLevel, string> = {
  unknown: "text-muted-foreground/45",
  normal: "text-accent-primary",
  warning: "text-accent-gold",
  critical: "text-accent-orange",
};

export type ContextUsageRingProps = {
  className?: string;
  /** 进度变化时是否补间：触发按钮要（后台刷新会变），面板内的静态概览不需要。 */
  animated?: boolean;
  level: ContextUsageLevel;
  percentText: string;
  progress: number;
  /** 渲染边长（px）：32 = 触发按钮，56 = 面板概览。 */
  size?: number;
  /** 环内文字样式由调用方定（尺寸不同字号不同）。 */
  textClassName?: string;
};

export function ContextUsageRing({
  animated = false,
  className,
  level,
  percentText,
  progress,
  size = RING_VIEWBOX,
  textClassName,
}: ContextUsageRingProps) {
  const clamped = Math.min(Math.max(progress, 0), 1);

  return (
    <span
      className={cn(
        "relative inline-flex shrink-0 items-center justify-center",
        LEVEL_RING_CLASS[level],
        className,
      )}
      style={{ height: size, width: size }}
      data-testid="composer-context-usage-ring"
    >
      <svg
        aria-hidden="true"
        viewBox={`0 0 ${RING_VIEWBOX} ${RING_VIEWBOX}`}
        className="absolute inset-0 size-full"
      >
        <circle
          cx={RING_VIEWBOX / 2}
          cy={RING_VIEWBOX / 2}
          r={RING_RADIUS}
          fill="none"
          strokeWidth={RING_STROKE}
          className="stroke-current opacity-25"
        />
        <circle
          cx={RING_VIEWBOX / 2}
          cy={RING_VIEWBOX / 2}
          r={RING_RADIUS}
          fill="none"
          strokeWidth={RING_STROKE}
          strokeLinecap="round"
          strokeDasharray={RING_CIRCUMFERENCE}
          strokeDashoffset={RING_CIRCUMFERENCE * (1 - clamped)}
          transform={`rotate(-90 ${RING_VIEWBOX / 2} ${RING_VIEWBOX / 2})`}
          className={cn(
            "stroke-current",
            animated && "transition-[stroke-dashoffset] duration-300",
          )}
        />
      </svg>
      <span className={cn("relative tabular-nums text-foreground", textClassName)}>
        {percentText}
      </span>
    </span>
  );
}
