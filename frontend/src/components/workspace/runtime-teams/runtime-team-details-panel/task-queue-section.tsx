import { Badge } from "@/components/ui/badge";
import {
  sortTasks,
  statusTone,
  type TeamDetailsState,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared";
import { cn } from "@/lib/utils";

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
  return (
    <TeamDetailsSection
      title="Task queue"
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
                  <div className="truncate text-[13px] font-semibold text-[var(--foreground)]">
                    {task.title || truncateIdentifier(task.id, 18)}
                  </div>
                  <div className="truncate text-xs text-[var(--muted-foreground)]">
                    {task.goal || task.id}
                  </div>
                </div>
                <span
                  className={cn(
                    detailStatusPillClass,
                    statusTone(task.status),
                  )}
                >
                  {task.status || "unknown"}
                </span>
              </div>
              <div className="mt-1.5 flex flex-wrap gap-2.5 text-xs text-[var(--muted-foreground)]">
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
          <div className="text-sm text-[var(--muted-foreground)]">No tasks available.</div>
        )}
      </div>
    </TeamDetailsSection>
  );
}
