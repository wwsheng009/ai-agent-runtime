// 工作区设置 ·「右侧栏宽度」区块（P0-2 的等价入口：滑块 + 当前值 + 恢复自适应）。
//
// 设计规则（docs/plan/workspace-right-panel-file-browser-and-git-diff-plan.md §4.2.1）：
// - 与拖拽手柄共用**同一出口** `lib/layout/rail-width.ts`：范围取 `resolveRailWidthBounds()`，
//   写入值先过 `clampRailWidth()`，因此滑块上界随视口收窄（不会把主区挤没）。
// - 拖动滑块即转 `manual`（与拖拽语义一致）；「恢复自适应」回到 `auto` 并保留宽度意图。
// - 文案统一取 workspace 命名空间的 `panels.shell.rightRail.*`（本批只改这一个 i18n 模块）。
//
// 回归红线：不改变右栏开合语义；auto 下内容型面仍为 288px（本区块只显示模式，不写宽度）。

import { SlidersHorizontalIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { SettingsPanelCard } from "@/components/workspace/settings/settings-panel-card";
import { useAppSettings } from "@/core/settings";
import {
  RAIL_GRID_MIN_VIEWPORT_PX,
  clampRailWidth,
  resolveRailWidthBounds,
} from "@/lib/layout/rail-width";
import { cn } from "@/lib/utils";

/** 视口宽度兜底读取（无 window 时按 xl 断点处理）。 */
function readViewportWidth() {
  if (typeof window === "undefined") {
    return RAIL_GRID_MIN_VIEWPORT_PX;
  }

  return Math.round(window.innerWidth);
}

export function RailWidthSection() {
  const { t } = useTranslation("workspace");
  const { settings, updateSection } = useAppSettings();
  const mode = settings.workspace.rightRailWidthMode;
  const isAuto = mode === "auto";
  const viewportWidth = readViewportWidth();
  const { min, max } = resolveRailWidthBounds(viewportWidth);
  // 滑块展示的是「手动宽度意图」；auto 下只显示模式标签（实际宽度由当前面决定）。
  const value = clampRailWidth(
    settings.workspace.rightRailWidthPx,
    viewportWidth,
  );
  const resetDisabledClass =
    "cursor-not-allowed border-border bg-surface-solid text-muted-foreground opacity-50";
  const resetEnabledClass =
    "border-border bg-surface-solid text-foreground hover:border-border-strong hover:bg-surface-soft";

  return (
    <SettingsPanelCard
      description={t("panels.shell.rightRail.widthDescription")}
      headerAside={
        <div
          className="rounded-control border border-border bg-black/10 px-2 py-0.5 font-mono app-text-11 text-foreground"
          data-testid="rail-width-value"
        >
          {isAuto
            ? t("panels.shell.rightRail.modeAuto")
            : `${t("panels.shell.rightRail.modeManual")} · ${t(
                "panels.shell.rightRail.widthValue",
                { px: value },
              )}`}
        </div>
      }
      icon={<SlidersHorizontalIcon size={16} />}
      title={
        <span className="text-base">
          {t("panels.shell.rightRail.widthLabel")}
        </span>
      }
    >
      <div className="flex items-center gap-3">
        <input
          aria-label={t("panels.shell.rightRail.sliderLabel")}
          className="h-2 w-full accent-accent-primary"
          max={max}
          min={min}
          onChange={(event) =>
            updateSection("workspace", {
              rightRailWidthMode: "manual",
              rightRailWidthPx: clampRailWidth(
                Number(event.target.value),
                viewportWidth,
              ),
            })
          }
          step={16}
          type="range"
          value={value}
        />
        <button
          type="button"
          className={cn(
            "inline-flex h-9 shrink-0 items-center justify-center rounded-field border px-3 text-base transition",
            isAuto ? resetDisabledClass : resetEnabledClass,
          )}
          data-testid="rail-width-reset-auto"
          disabled={isAuto}
          onClick={() =>
            updateSection("workspace", { rightRailWidthMode: "auto" })
          }
        >
          {t("panels.shell.rightRail.resetAuto")}
        </button>
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        {t("panels.shell.rightRail.resetAutoHint")}
      </p>
    </SettingsPanelCard>
  );
}
