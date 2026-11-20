import { ActivityIcon, AlertCircleIcon, CalendarClockIcon, PauseCircleIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { RuntimeScheduleTask } from "@/types/runtime";

import { splitScheduleTasks } from "@/components/workspace/schedule-panel-shared";

function Metric({
  label,
  value,
  icon: Icon,
  accent,
}: {
  label: string;
  value: number;
  icon: typeof ActivityIcon;
  accent: string;
}) {
  return (
    <div className="surface-panel min-w-0 rounded-panel-lg px-3 py-2.5 sm:px-3.5">
      <div className="flex items-center justify-between gap-2">
        <div className="truncate text-xs font-medium text-muted-foreground">{label}</div>
        <Icon aria-hidden="true" className={`size-4 shrink-0 ${accent}`} />
      </div>
      <div className="mt-1 text-2xl font-semibold leading-none tracking-[-0.03em] tabular-nums">
        {value}
      </div>
    </div>
  );
}

export function AutomationSummary({ tasks }: { tasks: RuntimeScheduleTask[] }) {
  const { t } = useTranslation("workspace");
  const { active, paused, blocked, failed } = splitScheduleTasks(tasks);

  return (
    <section
      aria-label={t("panels.schedule.metrics.label")}
      className="grid grid-cols-2 gap-2 xl:grid-cols-4"
    >
      <Metric
        accent="text-accent-secondary"
        icon={CalendarClockIcon}
        label={t("panels.schedule.metrics.total")}
        value={tasks.length}
      />
      <Metric
        accent="text-emerald-700 dark:text-emerald-300"
        icon={ActivityIcon}
        label={t("panels.schedule.metrics.active")}
        value={active.length}
      />
      <Metric
        accent="text-amber-700 dark:text-amber-300"
        icon={PauseCircleIcon}
        label={t("panels.schedule.metrics.paused")}
        value={paused.length}
      />
      <Metric
        accent="text-destructive"
        icon={AlertCircleIcon}
        label={t("panels.schedule.metrics.attention")}
        value={blocked.length + failed.length}
      />
    </section>
  );
}
