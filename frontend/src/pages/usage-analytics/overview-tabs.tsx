import { ArchiveIcon, BarChart3Icon, CpuIcon, GaugeIcon, ListIcon, RouteIcon } from "lucide-react";
import type { KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";
import { overviewViews, type OverviewView } from "./overview-navigation";

const viewIcons = {
  overview: BarChart3Icon,
  models: CpuIcon,
  sessions: ListIcon,
  quota: GaugeIcon,
  routing: RouteIcon,
  artifacts: ArchiveIcon,
};

export function OverviewTabs({ view, onChange }: {
  view: OverviewView;
  onChange: (view: OverviewView) => void;
}) {
  const { t } = useTranslation("usageAnalytics");
  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    let nextIndex: number;
    switch (event.key) {
      case "ArrowRight": nextIndex = (index + 1) % overviewViews.length; break;
      case "ArrowLeft": nextIndex = (index + overviewViews.length - 1) % overviewViews.length; break;
      case "Home": nextIndex = 0; break;
      case "End": nextIndex = overviewViews.length - 1; break;
      default: return;
    }
    event.preventDefault();
    const next = overviewViews[nextIndex];
    const target = event.currentTarget.parentElement?.querySelector<HTMLElement>(`#usage-view-${next}-tab`);
    target?.focus({ preventScroll: true });
    target?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
    onChange(next);
  };

  return (
    <div
      role="tablist"
      aria-label={t("overviewTabs.label")}
      className="surface-panel flex min-w-0 gap-1 overflow-x-auto rounded-panel-lg p-1.5"
    >
      {overviewViews.map((key, index) => {
        const Icon = viewIcons[key];
        const active = view === key;
        return (
          <button
            key={key}
            type="button"
            role="tab"
            id={`usage-view-${key}-tab`}
            aria-controls={`usage-view-${key}-panel`}
            aria-selected={active}
            tabIndex={active ? 0 : -1}
            onClick={() => onChange(key)}
            onKeyDown={(event) => onKeyDown(event, index)}
            className={cn(
              "flex shrink-0 items-center gap-2 whitespace-nowrap rounded-control border px-3 py-2 text-sm font-medium transition focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              active
                ? "border-accent-primary-border bg-accent-primary-soft text-foreground"
                : "border-transparent text-muted-foreground hover:bg-surface-soft-hover hover:text-foreground",
            )}
          >
            <Icon size={15} aria-hidden="true" />
            {t(`overviewTabs.${key}`)}
          </button>
        );
      })}
    </div>
  );
}
