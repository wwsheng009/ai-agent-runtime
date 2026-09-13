// 由 components/workspace/runtime-teams/team-details-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
import { statusTone, truncateIdentifier } from "@/components/workspace/runtime-teams/shared";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

import { detailsCardClass, detailsPanelClass, detailsPillClass } from "./format";
import { type TeamDetailsPanelProps } from "./types";

type TeamDetailsPanelTaskQueueProps = Pick<
  TeamDetailsPanelProps,
  | "visibleTasks"
>;

export function TeamDetailsPanelTaskQueue({
  visibleTasks,
}: TeamDetailsPanelTaskQueueProps) {
  const { t } = useTranslation("workspace");

  return (
    <div className={detailsPanelClass}>
      <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-muted-foreground">
        {t("panels.teamsPanels.details.taskQueue.title")}
      </div>
      <div className="mt-3 space-y-2">
        {visibleTasks.length > 0 ? (
          visibleTasks.map((task) => (
            <div key={task.id} className={detailsCardClass}>
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate text-sm font-semibold text-foreground">
                    {task.title || truncateIdentifier(task.id, 18)}
                  </div>
                  <div className="truncate text-xs text-muted-foreground">
                    {task.goal || task.id}
                  </div>
                </div>
                <span
                  className={cn(
                    detailsPillClass,
                    statusTone(task.status),
                  )}
                >
                  {task.status || t("panels.teamsPanels.details.statusUnknown")}
                </span>
              </div>
              <div className="mt-2 flex flex-wrap gap-3 text-xs text-muted-foreground">
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
    </div>
  );
}
