// P2-6 子片 1：侧栏会话排序模式切换（最近更新 / 手动）。
// 两个选项都是当前可用的真实行为：手动模式提供拖拽重排，不含未接线的占位项。

import { type TFunction } from "i18next";

import { cn } from "@/lib/utils";
import {
  SESSION_ORDER_MODES,
  type SessionOrderMode,
} from "@/lib/workspace/session-order";

type WorkspaceSidebarSessionOrderControlProps = {
  mode: SessionOrderMode;
  onSelect: (mode: SessionOrderMode) => void;
  t: TFunction<"workspace">;
};

function modeLabelKey(mode: SessionOrderMode) {
  return mode === "updated"
    ? "sidebar.sessionOrder.updated"
    : "sidebar.sessionOrder.manual";
}

function modeHintKey(mode: SessionOrderMode) {
  return mode === "updated"
    ? "sidebar.sessionOrder.updatedHint"
    : "sidebar.sessionOrder.manualHint";
}

export function WorkspaceSidebarSessionOrderControl({
  mode,
  onSelect,
  t,
}: WorkspaceSidebarSessionOrderControlProps) {
  return (
    <div
      className="flex items-center justify-between gap-2 px-1"
      data-testid="sidebar-session-order-control"
    >
      <span className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
        {t("sidebar.sessionOrder.label")}
      </span>
      <div
        role="group"
        aria-label={t("sidebar.sessionOrder.groupLabel")}
        className="inline-flex items-center gap-0.5 rounded-control border border-border bg-surface-softer p-0.5"
      >
        {SESSION_ORDER_MODES.map((option) => {
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
