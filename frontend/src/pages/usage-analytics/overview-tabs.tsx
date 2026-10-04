import { ArchiveIcon, BarChart3Icon, CpuIcon, GaugeIcon, ListIcon, RouteIcon, ServerIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { TabSwitcher } from "@/components/ui/tab-switcher";
import { overviewViews, type OverviewView } from "./overview-navigation";

const viewIcons = {
  overview: BarChart3Icon,
  models: CpuIcon,
  sessions: ListIcon,
  quota: GaugeIcon,
  routing: RouteIcon,
  lsp: ServerIcon,
  artifacts: ArchiveIcon,
};

export function OverviewTabs({ view, onChange }: {
  view: OverviewView;
  onChange: (view: OverviewView) => void;
}) {
  const { t } = useTranslation("usageAnalytics");
  return (
    <TabSwitcher
      ariaLabel={t("overviewTabs.label")}
      className="surface-panel min-w-0 flex-nowrap overflow-x-auto rounded-panel-lg p-1.5"
      items={overviewViews.map((key) => {
        const Icon = viewIcons[key];
        return {
          id: key,
          label: t(`overviewTabs.${key}`),
          icon: <Icon size={15} aria-hidden="true" />,
          tabId: `usage-view-${key}-tab`,
          panelId: `usage-view-${key}-panel`,
        };
      })}
      onChange={onChange}
      size="md"
      value={view}
      variant="plain"
    />
  );
}
