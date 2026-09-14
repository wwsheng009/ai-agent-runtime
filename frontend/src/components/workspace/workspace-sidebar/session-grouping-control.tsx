// P2-6 子片 2：侧栏会话分组视图切换（按目录 / 平铺）。
// 两个选项都是真实行为：按目录提供组头与跨组移动，平铺不显示组头（跨组移动不可表达）。
// 平铺与分组共用同一份组内顺序账目——切换视图不分叉顺序。

import { type TFunction } from "i18next";

import { cn } from "@/lib/utils";
import {
  SESSION_GROUPING_MODES,
  type SessionGroupingMode,
} from "@/lib/workspace/session-grouping";

type WorkspaceSidebarSessionGroupingControlProps = {
  mode: SessionGroupingMode;
  onSelect: (mode: SessionGroupingMode) => void;
  t: TFunction<"workspace">;
};

function modeLabelKey(mode: SessionGroupingMode) {
  return mode === "directory"
    ? "sidebar.sessionGrouping.directory"
    : "sidebar.sessionGrouping.flat";
}

function modeHintKey(mode: SessionGroupingMode) {
  return mode === "directory"
    ? "sidebar.sessionGrouping.directoryHint"
    : "sidebar.sessionGrouping.flatHint";
}

export function WorkspaceSidebarSessionGroupingControl({
  mode,
  onSelect,
  t,
}: WorkspaceSidebarSessionGroupingControlProps) {
  return (
    <div
      className="flex items-center justify-between gap-2 px-1"
      data-testid="sidebar-session-grouping-control"
    >
      <span className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
        {t("sidebar.sessionGrouping.label")}
      </span>
      <div
        role="group"
        aria-label={t("sidebar.sessionGrouping.groupLabel")}
        className="inline-flex items-center gap-0.5 rounded-control border border-border bg-surface-softer p-0.5"
      >
        {SESSION_GROUPING_MODES.map((option) => {
          const selected = option === mode;
          return (
            <button
              key={option}
              type="button"
              aria-pressed={selected}
              title={t(modeHintKey(option))}
              onClick={() => onSelect(option)}
              className={cn(
                "rounded-[0.45rem] px-2 py-0.5 app-text-10 transition focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent-primary-border",
                selected
                  ? "bg-surface-solid text-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {t(modeLabelKey(option))}
            </button>
          );
        })}
      </div>
    </div>
  );
}
