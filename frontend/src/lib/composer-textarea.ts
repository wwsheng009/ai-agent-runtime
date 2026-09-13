// P1-4 子片 1：Composer 输入框「14 行封顶 + 超出后内部滚动」的几何计算。
//
// 拆分理由：真实布局需要浏览器排版（jsdom 无排版），因此把可判定的几何逻辑
// 抽成纯函数单测；组件只负责在 layout effect 里读取 DOM 度量并写回样式。
//
// 行为对齐目标契约：
// - 内容不足 `maxLines` 行 → 高度贴内容（自动增高），不出现滚动条；
// - 超过 `maxLines` 行 → 高度封顶为 `maxLines * lineHeight + padding`，
//   内部出现滚动条（`overflow-y: auto`），页面布局不再被草稿撑高。

export const COMPOSER_MAX_VISIBLE_LINES = 14;

/** line-height 无法解析时的兜底系数（与常见 body 行高一致）。 */
export const COMPOSER_FALLBACK_LINE_HEIGHT_RATIO = 1.5;
/** fontSize 无法解析时的兜底字号（px）。 */
export const COMPOSER_FALLBACK_FONT_SIZE_PX = 13;
const COMPOSER_FALLBACK_LINE_HEIGHT_PX = 20;

export type ComposerTextareaMetrics = {
  scrollHeight: number;
  /** 计算后的 line-height（px）；无法解析时为 null。 */
  lineHeight: number | null;
  /** 计算后的 font-size（px）；无法解析时为 null。 */
  fontSize: number | null;
  paddingTop: number;
  paddingBottom: number;
};

export type ComposerTextareaLayout = {
  height: number;
  maxHeight: number;
  overflowY: "auto" | "hidden";
  capped: boolean;
};

export function resolveComposerTextareaLineHeight(
  metrics: Pick<ComposerTextareaMetrics, "lineHeight" | "fontSize">,
): number {
  if (metrics.lineHeight !== null && metrics.lineHeight > 0) {
    return metrics.lineHeight;
  }
  if (metrics.fontSize !== null && metrics.fontSize > 0) {
    return metrics.fontSize * COMPOSER_FALLBACK_LINE_HEIGHT_RATIO;
  }
  return COMPOSER_FALLBACK_LINE_HEIGHT_PX;
}

export function resolveComposerTextareaLayout(
  metrics: ComposerTextareaMetrics,
  maxLines: number = COMPOSER_MAX_VISIBLE_LINES,
): ComposerTextareaLayout {
  const lineHeight = resolveComposerTextareaLineHeight(metrics);
  const verticalChrome =
    Math.max(0, metrics.paddingTop) + Math.max(0, metrics.paddingBottom);
  const maxHeight = Math.ceil(lineHeight * Math.max(1, maxLines) + verticalChrome);
  const scrollHeight = Math.max(0, metrics.scrollHeight);
  const naturalHeight = Math.max(scrollHeight, lineHeight + verticalChrome);
  const capped = naturalHeight > maxHeight;
  const height = capped ? maxHeight : naturalHeight;
  return {
    height,
    maxHeight,
    overflowY: capped ? "auto" : "hidden",
    capped,
  };
}

function parseCssPixels(value: string | null | undefined): number | null {
  if (!value) {
    return null;
  }
  const parsed = Number.parseFloat(value);
  return Number.isFinite(parsed) ? parsed : null;
}

/** 从真实 textarea 读取度量（浏览器环境）。 */
export function readComposerTextareaMetrics(
  element: HTMLTextAreaElement,
): ComposerTextareaMetrics {
  const style = window.getComputedStyle(element);
  return {
    scrollHeight: element.scrollHeight,
    lineHeight: parseCssPixels(style.lineHeight),
    fontSize: parseCssPixels(style.fontSize),
    paddingTop: parseCssPixels(style.paddingTop) ?? 0,
    paddingBottom: parseCssPixels(style.paddingBottom) ?? 0,
  };
}

/**
 * 写入布局（幂等）：1px 内不重写样式，避免 auto-grow 与 scrollHeight 互相
 * 反馈造成的抖动；返回最终布局便于测试断言。
 */
export function applyComposerTextareaLayout(
  element: HTMLTextAreaElement,
  maxLines: number = COMPOSER_MAX_VISIBLE_LINES,
): ComposerTextareaLayout {
  const layout = resolveComposerTextareaLayout(
    readComposerTextareaMetrics(element),
    maxLines,
  );
  const nextHeight = `${layout.height}px`;
  if (element.style.height !== nextHeight) {
    element.style.height = nextHeight;
  }
  if (element.style.overflowY !== layout.overflowY) {
    element.style.overflowY = layout.overflowY;
  }
  return layout;
}
