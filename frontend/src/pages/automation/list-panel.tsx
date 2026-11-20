import { FilterIcon, SearchIcon, SlidersHorizontalIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Input } from "@/components/ui/input";
import { Select, type SelectOption } from "@/components/ui/select";
import { SchedulePanel } from "@/components/workspace/schedule-panel";
import type { ScheduleTasksController } from "@/hooks/workspace/use-schedule-tasks";
import type { RuntimeScheduleTask } from "@/types/runtime";

import type {
  AutomationCreateRequest,
  AutomationStatusFilter,
  AutomationTargetFilter,
} from "./types";

type AutomationListPanelProps = {
  controller: ScheduleTasksController;
  query: string;
  onQueryChange: (value: string) => void;
  statusFilter: AutomationStatusFilter;
  onStatusFilterChange: (value: AutomationStatusFilter) => void;
  targetFilter: AutomationTargetFilter;
  onTargetFilterChange: (value: AutomationTargetFilter) => void;
  taskFilter: (task: RuntimeScheduleTask) => boolean;
  filteredCount: number;
  emptyMessage?: string;
  createRequest: AutomationCreateRequest | null;
  onCreateRequestHandled: (key: string) => void;
  onOpenSession?: (sessionId: string) => void;
  onViewHistory?: (task: RuntimeScheduleTask) => void;
};

export function AutomationListPanel({
  controller,
  query,
  onQueryChange,
  statusFilter,
  onStatusFilterChange,
  targetFilter,
  onTargetFilterChange,
  taskFilter,
  filteredCount,
  emptyMessage,
  createRequest,
  onCreateRequestHandled,
  onOpenSession,
  onViewHistory,
}: AutomationListPanelProps) {
  const { t } = useTranslation("workspace");
  const statusOptions: SelectOption[] = [
    { value: "all", label: t("panels.schedule.filters.allStatuses") },
    { value: "active", label: t("panels.schedule.statusActive") },
    { value: "paused", label: t("panels.schedule.statusPaused") },
    { value: "inactive", label: t("panels.schedule.statusInactive") },
    { value: "blocked", label: t("panels.schedule.statusBlocked") },
    { value: "failed", label: t("panels.schedule.statusFailed") },
  ];
  const targetOptions: SelectOption[] = [
    { value: "all", label: t("panels.schedule.filters.allTargets") },
    { value: "session", label: t("panels.schedule.target.session") },
    { value: "headless", label: t("panels.schedule.target.headless") },
    { value: "workspace", label: t("panels.schedule.target.workspace") },
  ];

  return (
    <section
      aria-labelledby="automation-list-title"
      className="surface-panel min-h-0 rounded-panel-lg p-3 sm:p-4"
    >
      <div className="flex flex-col gap-3 border-b border-border pb-3 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <SlidersHorizontalIcon aria-hidden="true" className="size-4 text-accent-secondary" />
            <h2 id="automation-list-title" className="text-sm font-semibold">
              {t("panels.schedule.listTitle")}
            </h2>
            <span className="rounded-full border border-border bg-surface-soft px-2 py-0.5 text-xs tabular-nums text-muted-foreground">
              {filteredCount}
            </span>
          </div>
          <p className="mt-1 max-w-2xl text-xs leading-5 text-muted-foreground">
            {t("panels.schedule.listDescription")}
          </p>
        </div>
      </div>

      <div className="mt-3 grid gap-2 md:grid-cols-[minmax(0,1fr)_11rem_11rem]">
        <label className="relative block">
          <span className="sr-only">{t("panels.schedule.filters.searchLabel")}</span>
          <SearchIcon
            aria-hidden="true"
            className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
          />
          <Input
            aria-label={t("panels.schedule.filters.searchLabel")}
            className="pl-9"
            onChange={(event) => onQueryChange(event.target.value)}
            placeholder={t("panels.schedule.filters.searchPlaceholder")}
            value={query}
          />
        </label>
        <Select
          ariaLabel={t("panels.schedule.filters.statusLabel")}
          onChange={(value) => onStatusFilterChange(value as AutomationStatusFilter)}
          options={statusOptions}
          triggerPrefix={<FilterIcon aria-hidden="true" className="size-3.5" />}
          value={statusFilter}
        />
        <Select
          ariaLabel={t("panels.schedule.filters.targetLabel")}
          onChange={(value) => onTargetFilterChange(value as AutomationTargetFilter)}
          options={targetOptions}
          value={targetFilter}
        />
      </div>

      <div className="mt-3 rounded-panel border border-border/70 bg-surface/30">
        <SchedulePanel
          controller={controller}
          createRequest={createRequest ?? undefined}
          emptyMessage={emptyMessage}
          onCreateRequestHandled={onCreateRequestHandled}
          onOpenSession={onOpenSession}
          onViewHistory={onViewHistory}
          sessionId=""
          taskFilter={taskFilter}
        />
      </div>
    </section>
  );
}
