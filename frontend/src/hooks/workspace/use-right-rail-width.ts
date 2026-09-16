// 工作区右栏宽度状态源（P0-2）：模式解析 + 视口重算 + 提交持久化。
//
// 设计规则（docs/plan/...-plan.md §4.2）：
// - 所有宽度算术只走 `lib/layout/rail-width.ts`，本 hook 不复制任何系数/上下界。
// - `auto`：按当前激活面的 `widthClass` 自适应（内容型面 288px；files/git 宽内容面按视口）。
// - `manual`：用持久化 `rightRailWidthPx` 并过 `clampRailWidth()`；视口变小只改显示，**不回写设置**。
// - 拖拽过程零 setState（几何与 CSS 变量由 rail-resize-handle 直接操作 DOM），
//   只有 `pointerup` / 键盘 / 设置页才调用 `commitWidth`（一次写入，一次 re-render）。
//
// 回归红线：`auto` + 内容型面必须解析为 288px；本 hook 不参与右栏开合语义（topbar 开关不变）。

import { useCallback, useMemo } from "react";

import {
  WORKSPACE_PANEL_SURFACES,
  type WorkspacePanelSurfaceId,
} from "@/components/workspace/panel-registry";
import { useAppSettings } from "@/core/settings";
import {
  RAIL_GRID_MIN_VIEWPORT_PX,
  clampRailWidth,
  isRailOverlayViewport,
  resolveRailWidth,
  resolveRailWidthBounds,
  type RailWidthMode,
  type RailWidthSurface,
} from "@/lib/layout/rail-width";

/** 面板未接线激活面（父代理侧改造未完成）时的兜底宽度类别：内容型面 288px。 */
const FALLBACK_WIDTH_CLASS: RailWidthSurface = "content";

/**
 * 由 panel-registry 的 `widthClass` 解析面的宽度类别。
 * 注册表是唯一事实来源；未知/空 id 一律按内容型面兜底，保证 UI 不白屏。
 */
export function resolveRailSurfaceWidthClass(
  surface: WorkspacePanelSurfaceId | null | undefined,
): RailWidthSurface {
  if (!surface) {
    return FALLBACK_WIDTH_CLASS;
  }

  return (
    WORKSPACE_PANEL_SURFACES.find((spec) => spec.id === surface)?.widthClass ??
    FALLBACK_WIDTH_CLASS
  );
}

export type UseRightRailWidthOptions = {
  /** 当前激活面；面板未接线时为 null → 按内容型面兜底。 */
  surface: WorkspacePanelSurfaceId | null;
  /** 视口宽度（px）：由 workspace-shell 复用既有 ResizeObserver 提供。 */
  viewportWidth: number;
};

export type RightRailWidthController = {
  mode: RailWidthMode;
  /** 当前应渲染的右栏宽度（px，已过 clamp）。 */
  widthPx: number;
  minWidthPx: number;
  maxWidthPx: number;
  /** 计算所用的视口宽度：拖拽手柄用它做同一口径的 clamp。 */
  viewportWidth: number;
  widthClass: RailWidthSurface;
  /** 视口低于 xl 或主区放不下最小右栏 → 由调用方决定是否降级为覆盖层抽屉。 */
  overlay: boolean;
  /** 拖拽 / 键盘提交一次（写 settings：mode=manual）。 */
  commitWidth: (px: number) => void;
  /** 回到自适应（保留 widthPx 供再次切回 manual）。 */
  resetToAuto: () => void;
};

export function useRightRailWidth({
  surface,
  viewportWidth,
}: UseRightRailWidthOptions): RightRailWidthController {
  const { settings, updateSection } = useAppSettings();
  const mode = settings.workspace.rightRailWidthMode;
  const storedWidthPx = settings.workspace.rightRailWidthPx;
  const widthClass = resolveRailSurfaceWidthClass(surface);

  const widthPx = useMemo(
    () =>
      resolveRailWidth({
        mode,
        widthPx: storedWidthPx,
        surface: widthClass,
        viewportWidth,
      }),
    [mode, storedWidthPx, widthClass, viewportWidth],
  );

  const { min, max } = resolveRailWidthBounds(viewportWidth);

  const commitWidth = useCallback(
    (px: number) => {
      updateSection("workspace", {
        // 与 CSS 变量同口径先收窄再落盘，避免把越界值（或 NaN）写进 localStorage。
        rightRailWidthMode: "manual",
        rightRailWidthPx: clampRailWidth(px, viewportWidth),
      });
    },
    [updateSection, viewportWidth],
  );

  const resetToAuto = useCallback(() => {
    updateSection("workspace", { rightRailWidthMode: "auto" });
  }, [updateSection]);

  return {
    mode,
    widthPx,
    minWidthPx: min,
    maxWidthPx: max,
    viewportWidth,
    widthClass,
    overlay:
      isRailOverlayViewport(viewportWidth) ||
      viewportWidth < RAIL_GRID_MIN_VIEWPORT_PX,
    commitWidth,
    resetToAuto,
  };
}
