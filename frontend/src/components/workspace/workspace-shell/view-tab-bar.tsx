// 由 workspace-shell/main-section.tsx 机械拆分而来（P0-2）；页签样式与键盘逻辑收敛到 ui/tab-switcher。

import { type TFunction } from "i18next";

import { TabSwitcher, type TabSwitcherItem } from "@/components/ui/tab-switcher";
import { type WorkspaceViewMode } from "@/components/workspace/workspace-shell/types";

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
  const items: TabSwitcherItem<WorkspaceViewMode>[] = [
    {
      id: "chat",
      label: t("panels.shell.viewTabs.chat"),
      testId: "workspace-view-tab-chat",
    },
    {
      id: "skills",
      label: t("panels.shell.viewTabs.skills"),
      testId: "workspace-view-tab-skills",
    },
  ];
  if (trajectoryAvailable) {
    items.push({
      id: "trajectory",
      label: t("panels.shell.viewTabs.trajectory"),
      testId: "workspace-view-tab-trajectory",
    });
  }

  return (
    <TabSwitcher
      ariaLabel={t("panels.shell.viewTabs.ariaLabel")}
      className="px-3 pt-2"
      items={items}
      onChange={onSelectViewMode}
      value={viewMode}
      variant="underline"
    />
  );
}
