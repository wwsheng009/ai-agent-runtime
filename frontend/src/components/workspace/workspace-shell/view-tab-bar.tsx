// 由 workspace-shell/main-section.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";

import { type WorkspaceViewMode } from "@/components/workspace/workspace-shell/types";
import { cn } from "@/lib/utils";

type WorkspaceViewTabBarProps = {
  /** 选中新页签（技能 / 轨迹 / 对话）。 */
  onSelectViewMode: (mode: WorkspaceViewMode) => void;
  t: TFunction<"workspace">;
  /** 轨迹 store 存在时才渲染「轨迹」页签。 */
  trajectoryAvailable: boolean;
  viewMode: WorkspaceViewMode;
};

export function WorkspaceViewTabBar({
  onSelectViewMode,
  t,
  trajectoryAvailable,
  viewMode,
}: WorkspaceViewTabBarProps) {
  const tabClass = (active: boolean) =>
    cn(
      "rounded-t-md border border-b-0 px-3 py-1.5 app-text-12 transition",
      active
        ? "border-border bg-surface-softer text-foreground"
        : "border-transparent text-muted-foreground hover:text-foreground",
    );

  return (
    <div
      aria-label={t("panels.shell.viewTabs.ariaLabel")}
      className="flex items-center gap-1 border-b border-border px-3 pt-2"
      role="tablist"
    >
      <button
        aria-selected={viewMode === "chat"}
        className={tabClass(viewMode === "chat")}
        data-testid="workspace-view-tab-chat"
        onClick={() => onSelectViewMode("chat")}
        role="tab"
        type="button"
      >
        {t("panels.shell.viewTabs.chat")}
      </button>
      <button
        aria-selected={viewMode === "skills"}
        className={tabClass(viewMode === "skills")}
        data-testid="workspace-view-tab-skills"
        onClick={() => onSelectViewMode("skills")}
        role="tab"
        type="button"
      >
        {t("panels.shell.viewTabs.skills")}
      </button>
      {trajectoryAvailable ? (
        <button
          aria-selected={viewMode === "trajectory"}
          className={tabClass(viewMode === "trajectory")}
          data-testid="workspace-view-tab-trajectory"
          onClick={() => onSelectViewMode("trajectory")}
          role="tab"
          type="button"
        >
          {t("panels.shell.viewTabs.trajectory")}
        </button>
      ) : null}
    </div>
  );
}
