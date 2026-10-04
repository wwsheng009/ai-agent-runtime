// 由 components/workspace/artifact-detail-dialog.tsx 机械拆分而来（P0-2）；
// 页签交互（roving tabindex + 方向键）收敛到 ui/tab-switcher，保留原 ids/aria-controls 契约。

import { EyeIcon, FileCode2Icon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { TabSwitcher, type TabSwitcherItem } from "@/components/ui/tab-switcher";
import type { ArtifactDetailView } from "./types";

type ArtifactReadingTabsProps = {
  onSelectView: (view: ArtifactDetailView) => void;
  previewPanelId: string;
  previewTabId: string;
  sourcePanelId: string;
  sourceTabId: string;
  view: ArtifactDetailView;
};

export function ArtifactReadingTabs({
  onSelectView,
  previewPanelId,
  previewTabId,
  sourcePanelId,
  sourceTabId,
  view,
}: ArtifactReadingTabsProps) {
  const { t } = useTranslation("workspace");
  const items: TabSwitcherItem<ArtifactDetailView>[] = [
    {
      id: "preview",
      label: t("panels.artifacts.detail.tabPreview"),
      icon: <EyeIcon size={14} />,
      tabId: previewTabId,
      panelId: previewPanelId,
    },
    {
      id: "source",
      label: t("panels.artifacts.detail.tabSource"),
      icon: <FileCode2Icon size={14} />,
      tabId: sourceTabId,
      panelId: sourcePanelId,
    },
  ];

  return (
    <div className="border-b border-border px-4 py-3">
      <TabSwitcher
        ariaLabel={t("panels.artifacts.detail.tabsLabel")}
        items={items}
        onChange={onSelectView}
        value={view}
        variant="plain"
      />
    </div>
  );
}
