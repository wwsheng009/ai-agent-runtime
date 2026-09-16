// 共享弹层定位：`Select` 候选面与 composer 模型面板都是「挂在触发器旁边的 fixed 浮层」，
// 视口夹取（左右不越界、上下按余量收最大高度）只应有一套实现，避免两个组件各自漂移。
//
// 与具体组件无关：只吃 `DOMRect` 与策略参数，吐可直接展开进 style 的固定定位值。

import { type CSSProperties } from "react";

export type PopoverAlign = "start" | "end";
export type PopoverSide = "top" | "bottom";

export type PopoverPosition = Pick<
  CSSProperties,
  "bottom" | "left" | "minWidth" | "top" | "right"
> & {
  maxHeight: number;
};

export type PopoverPositionOptions = {
  align?: PopoverAlign;
  /** 触发器与弹层之间的间距。 */
  gap?: number;
  /** 弹层最大高度上限；视口余量更小时取余量。 */
  maxHeight?: number;
  /** 最大高度下限：贴边时不塌成一条缝。 */
  minHeight?: number;
  /** 弹层最小宽度；未提供时跟随触发器宽度。 */
  minWidth?: number;
  side?: PopoverSide;
  /** 视口安全边距。 */
  viewportPadding?: number;
};

export const POPOVER_GAP = 8;
export const POPOVER_VIEWPORT_PADDING = 8;
export const POPOVER_MAX_HEIGHT = 240;
export const POPOVER_MIN_HEIGHT = 96;

export function resolvePopoverPosition(
  triggerRect: DOMRect,
  {
    align = "start",
    gap = POPOVER_GAP,
    maxHeight = POPOVER_MAX_HEIGHT,
    minHeight = POPOVER_MIN_HEIGHT,
    minWidth,
    side = "bottom",
    viewportPadding = POPOVER_VIEWPORT_PADDING,
  }: PopoverPositionOptions = {},
): PopoverPosition {
  const viewportWidth = window.innerWidth;
  const viewportHeight = window.innerHeight;
  const resolvedMinWidth = Math.min(
    Math.max(minWidth ?? 0, triggerRect.width),
    Math.max(0, viewportWidth - viewportPadding * 2),
  );
  const availableHeight =
    side === "top"
      ? triggerRect.top - gap - viewportPadding
      : viewportHeight - triggerRect.bottom - gap - viewportPadding;

  return {
    bottom: side === "top" ? viewportHeight - triggerRect.top + gap : "auto",
    left:
      align === "start"
        ? Math.max(triggerRect.left, viewportPadding)
        : "auto",
    maxHeight: Math.max(minHeight, Math.min(maxHeight, availableHeight)),
    minWidth: resolvedMinWidth,
    right:
      align === "end"
        ? Math.max(viewportWidth - triggerRect.right, viewportPadding)
        : "auto",
    top:
      side === "bottom"
        ? Math.max(triggerRect.bottom + gap, viewportPadding)
        : "auto",
  };
}
