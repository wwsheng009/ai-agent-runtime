// 左侧栏宽度契约：展开态可调宽度与「只显示图标的列」收起态共用本组常量，
// 网格模板（workspace-shell 的 `--workspace-sidebar-width`）与宽度手柄都读这里。

/** 展开态默认值：与既有网格默认值 16rem 保持一致（回归红线：不改变默认视觉）。 */
export const WORKSPACE_SIDEBAR_WIDTH_DEFAULT_PX = 256;
export const WORKSPACE_SIDEBAR_WIDTH_MIN_PX = 208;
export const WORKSPACE_SIDEBAR_WIDTH_MAX_PX = 480;
/** 保证调整侧栏后主对话区仍有可用宽度；有右栏时还需为其预留空间。 */
export const WORKSPACE_SIDEBAR_MIN_MAIN_WIDTH_PX = 512;

/** 收起态：容纳 `size-8` 图标按钮 + 两侧 `px-2` 留白（VS Code 活动栏量级）。 */
export const WORKSPACE_SIDEBAR_COLLAPSED_WIDTH = "3.5rem";

/** 按主区剩余空间动态收窄上界；极窄窗口始终保留合法范围。 */
export function resolveWorkspaceSidebarWidthMax(
  viewportWidth: number,
  reservedRightRailWidth = 0,
): number {
  const safeViewport =
    Number.isFinite(viewportWidth) && viewportWidth > 0 ? viewportWidth : 1280;
  const safeReserved =
    Number.isFinite(reservedRightRailWidth) && reservedRightRailWidth > 0
      ? reservedRightRailWidth
      : 0;

  return Math.max(
    WORKSPACE_SIDEBAR_WIDTH_MIN_PX,
    Math.min(
      WORKSPACE_SIDEBAR_WIDTH_MAX_PX,
      Math.round(
        safeViewport - safeReserved - WORKSPACE_SIDEBAR_MIN_MAIN_WIDTH_PX,
      ),
    ),
  );
}

/** 持久化值、拖拽与键盘的共同边界收口。 */
export function clampWorkspaceSidebarWidth(
  widthPx: number,
  viewportWidth: number,
  reservedRightRailWidth = 0,
): number {
  const safeWidth = Number.isFinite(widthPx)
    ? widthPx
    : WORKSPACE_SIDEBAR_WIDTH_DEFAULT_PX;
  return Math.round(
    Math.min(
      Math.max(safeWidth, WORKSPACE_SIDEBAR_WIDTH_MIN_PX),
      resolveWorkspaceSidebarWidthMax(viewportWidth, reservedRightRailWidth),
    ),
  );
}
