// 工作区右栏宽度计算的**唯一出口**（P0-2「可变宽度 + 自适应」）。
//
// 设计规则（docs/plan/workspace-right-panel-file-browser-and-git-diff-plan.md §4.2）：
// - 状态源是 `settings.workspace.rightRailWidthMode`（"auto" | "manual"）+ `rightRailWidthPx`；
//   hook / 组件 / 设置页共用本模块换算，任何地方都不得再写一套宽度算术。
// - auto：内容型面锁 288px（= 改造前的 18rem，逐像素一致）；宽内容面 clamp(0.32 × vw, 416, 672)。
// - manual：`widthPx` 再过 `clampRailWidth()`；视口变小只做「显示收窄」，**不改写持久化值**。
// - 主区保护：maxWidth = min(832, vw − 256(左栏) − 512(主区最小))、minWidth = 320；
//   maxWidth < minWidth 或视口 < Tailwind xl（80rem）→ 右栏不进三列网格，改走覆盖层抽屉。
//
// 回归红线：`auto` + 内容型面必须恒为 288px（既有测试基线与现状像素一致），
// 不得被 `RAIL_WIDTH_MIN_PX` 抬到 320px；纯函数、无副作用、无 IO，单测见 rail-width.test.ts。

/** 宽度模式：`auto` 按面与视口实时计算，`manual` 用用户拖拽/输入的持久化值。 */
export type RailWidthMode = "auto" | "manual";

/** 面的宽度类别；由 panel-registry 的 `widthClass` 派生（本模块不认识具体面 id）。 */
export type RailWidthSurface = "content" | "wide";

/** 内容型面在 auto 下的固定宽度：18rem，与改造前的固定列宽逐像素一致。 */
export const RAIL_WIDTH_CONTENT_PX = 288;
/** 可调范围下界（也是 ARIA `aria-valuemin`）。 */
export const RAIL_WIDTH_MIN_PX = 320;
/** 主区保护上界：52rem（也是 ARIA `aria-valuemax` 的上界）。 */
export const RAIL_WIDTH_MAX_PX = 832;
/** 左栏（会话列表）固定列宽：16rem，主区保护要从视口里扣掉。 */
export const RAIL_WIDTH_SIDEBAR_PX = 256;
/** 主区（对话区）最小宽度：32rem。 */
export const RAIL_WIDTH_MIN_MAIN_PX = 512;
/** 宽内容面 auto 系数：0.32 × 视口宽度。 */
export const RAIL_WIDTH_WIDE_RATIO = 0.32;
/** 宽内容面 auto 下界 26rem / 上界 42rem。 */
export const RAIL_WIDTH_WIDE_MIN_PX = 416;
export const RAIL_WIDTH_WIDE_MAX_PX = 672;
/** 覆盖层抽屉最多占视口宽度的比例（`<xl` 降级路径）。 */
export const RAIL_WIDTH_OVERLAY_MAX_RATIO = 0.92;
/** Tailwind `xl` 断点（80rem）：非 xl 视口不渲染右栏列。 */
export const RAIL_GRID_MIN_VIEWPORT_PX = 1280;

export type ResolveRailWidthInput = {
  mode: RailWidthMode;
  /** manual 模式下的持久化宽度意图（px）；auto 模式下不参与计算但会被保留。 */
  widthPx: number;
  surface: RailWidthSurface;
  viewportWidth: number;
};

/** 视口宽度兜底：非法值按 xl 断点处理（宁可窄一点也不要 NaN 宽度）。 */
function normalizeViewportWidth(viewportWidth: number): number {
  return Number.isFinite(viewportWidth) && viewportWidth > 0
    ? viewportWidth
    : RAIL_GRID_MIN_VIEWPORT_PX;
}

/** 主区保护上界：`min(832, vw − 256 − 512)`；极窄视口时可能 < RAIL_WIDTH_MIN_PX。 */
export function resolveRailWidthMax(viewportWidth: number): number {
  const vw = normalizeViewportWidth(viewportWidth);
  return Math.min(
    RAIL_WIDTH_MAX_PX,
    Math.round(vw - RAIL_WIDTH_SIDEBAR_PX - RAIL_WIDTH_MIN_MAIN_PX),
  );
}

/** 主区放不下最小宽度的右栏 → 覆盖层模式（不参与三列网格）。 */
export function isRailOverlayViewport(viewportWidth: number): boolean {
  return resolveRailWidthMax(viewportWidth) < RAIL_WIDTH_MIN_PX;
}

/** 可调范围（含主区保护收窄）；视口极窄时 max 退化为 min，避免出现空区间。 */
export function resolveRailWidthBounds(viewportWidth: number): {
  min: number;
  max: number;
} {
  return {
    min: RAIL_WIDTH_MIN_PX,
    max: Math.max(RAIL_WIDTH_MIN_PX, resolveRailWidthMax(viewportWidth)),
  };
}

/**
 * 所有来源（拖拽 / 键盘 / auto / 持久化值 / 设置页输入）的唯一收口。
 * 视口极窄时上界会退化为 minWidth（覆盖层模式由调用方决定是否使用）。
 */
export function clampRailWidth(px: number, viewportWidth: number): number {
  const { min, max } = resolveRailWidthBounds(viewportWidth);
  const safe = Number.isFinite(px) ? px : RAIL_WIDTH_CONTENT_PX;
  return Math.round(Math.min(Math.max(safe, min), max));
}

/** 宽内容面 auto 规则：clamp(0.32 × vw, 26rem, 42rem)。 */
function resolveAutoWideWidth(viewportWidth: number): number {
  return Math.round(
    Math.min(
      Math.max(viewportWidth * RAIL_WIDTH_WIDE_RATIO, RAIL_WIDTH_WIDE_MIN_PX),
      RAIL_WIDTH_WIDE_MAX_PX,
    ),
  );
}

/** 解析右栏最终显示宽度（px）：manual 过 clamp；auto 按面自适应。 */
export function resolveRailWidth({
  mode,
  widthPx,
  surface,
  viewportWidth,
}: ResolveRailWidthInput): number {
  const vw = normalizeViewportWidth(viewportWidth);

  if (mode === "manual") {
    return clampRailWidth(widthPx, vw);
  }

  if (surface === "wide") {
    return clampRailWidth(resolveAutoWideWidth(vw), vw);
  }

  // 内容型面恒 288px（回归红线）：该值本身小于所有 xl 视口下的主区保护上界。
  return RAIL_WIDTH_CONTENT_PX;
}

/** 覆盖层抽屉宽度：`min(宽度, 92vw)`，保证小屏也留出可关闭的遮罩区域。 */
export function resolveRailOverlayWidth(
  widthPx: number,
  viewportWidth: number,
): number {
  const vw = normalizeViewportWidth(viewportWidth);
  const safe = Number.isFinite(widthPx) ? widthPx : RAIL_WIDTH_CONTENT_PX;
  return Math.max(0, Math.min(Math.round(safe), Math.round(vw * RAIL_WIDTH_OVERLAY_MAX_RATIO)));
}
