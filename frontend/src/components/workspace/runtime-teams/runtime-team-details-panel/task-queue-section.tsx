import { Badge } from "@/components/ui/badge";
import {
  sortTasks,
  statusTone,
  type TeamDetailsState,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

import { detailCardClass, detailStatusPillClass } from "./format";
import { TeamDetailsSection } from "./primitives";

type RuntimeTeamTaskQueueSectionProps = {
  details: TeamDetailsState;
  onToggle: () => void;
  open: boolean;
  visibleTasks: ReturnType<typeof sortTasks>;
};

export function RuntimeTeamTaskQueueSection({
  details,
  onToggle,
  open,
  visibleTasks,
}: RuntimeTeamTaskQueueSectionProps) {
  const { t } = useTranslation("workspace");

  return (
    <TeamDetailsSection
      title={t("panels.teamsPanels.details.taskQueue.title")}
      badge={<Badge>{details.tasks.length}</Badge>}
      open={open}
      onToggle={onToggle}
    >
      <div className="space-y-1.5">
        {visibleTasks.length > 0 ? (
          visibleTasks.map((task) => (
            <div
              key={task.id}
              className={detailCardClass}
            >
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate text-[13px] font-semibold text-foreground">
                    {task.title || truncateIdentifier(task.id, 18)}
                  </div>
                  <div className="truncate text-xs text-muted-foreground">
                    {task.goal || task.id}
                  </div>
                </div>
                <span
                  className={cn(
                    detailStatusPillClass,
                    statusTone(task.status),
                  )}
                >
                  {task.status || t("panels.teamsPanels.details.statusUnknown")}
                </span>
              </div>
              <div className="mt-1.5 flex flex-wrap gap-2.5 text-xs text-muted-foreground">
                <span>
                  {t("panels.teamsPanels.details.taskQueue.priority", {
                    count: task.priority ?? 0,
                  })}
                </span>
                {task.assignee ? (
                  <span>
                    {t("panels.teamsPanels.details.taskQueue.assignee", {
                      name: task.assignee,
                    })}
                  </span>
                ) : null}
                {task.parent_task_id ? (
                  <span>
                    {t("panels.teamsPanels.details.taskQueue.parent", {
                      id: truncateIdentifier(task.parent_task_id, 12),
                    })}
                  </span>
                ) : (
                  <span>{t("panels.teamsPanels.details.taskQueue.root")}</span>
                )}
              </div>
            </div>
          ))
        ) : (
          <div className="text-sm text-muted-foreground">
            {t("panels.teamsPanels.details.taskQueue.empty")}
          </div>
        )}
      </div>
    </TeamDetailsSection>
  );
}
