// 由 components/workspace/runtime-teams/team-details-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
import { statusTone, truncateIdentifier } from "@/components/workspace/runtime-teams/shared";
import { cn } from "@/lib/utils";

import { detailsCardClass, detailsPanelClass, detailsPillClass } from "./format";
import { type TeamDetailsPanelProps } from "./types";

type TeamDetailsPanelTaskQueueProps = Pick<
  TeamDetailsPanelProps,
  | "visibleTasks"
>;

export function TeamDetailsPanelTaskQueue({
  visibleTasks,
}: TeamDetailsPanelTaskQueueProps) {
  return (
    <div className={detailsPanelClass}>
      <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
        Task queue
      </div>
      <div className="mt-3 space-y-2">
        {visibleTasks.length > 0 ? (
          visibleTasks.map((task) => (
            <div key={task.id} className={detailsCardClass}>
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate text-sm font-semibold text-[var(--foreground)]">
                    {task.title || truncateIdentifier(task.id, 18)}
                  </div>
                  <div className="truncate text-xs text-[var(--muted-foreground)]">
                    {task.goal || task.id}
                  </div>
                </div>
                <span
                  className={cn(
                    detailsPillClass,
                    statusTone(task.status),
                  )}
                >
                  {task.status || "unknown"}
                </span>
              </div>
              <div className="mt-2 flex flex-wrap gap-3 text-xs text-[var(--muted-foreground)]">
                <span>priority {task.priority ?? 0}</span>
                {task.assignee ? <span>assignee {task.assignee}</span> : null}
                {task.parent_task_id ? (
                  <span>parent {truncateIdentifier(task.parent_task_id, 12)}</span>
                ) : (
                  <span>root</span>
                )}
              </div>
            </div>
          ))
        ) : (
          <div className="text-sm text-[var(--muted-foreground)]">
            No tasks available.
          </div>
        )}
      </div>
    </div>
  );
}
