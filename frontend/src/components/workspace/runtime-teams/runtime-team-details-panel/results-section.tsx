import { Badge } from "@/components/ui/badge";
import {
  sortTasks,
  statusTone,
  truncateIdentifier,
  type TeamDetailsState,
} from "@/components/workspace/runtime-teams/shared";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

import { detailCardClass, detailStatusPillClass } from "./format";
import { TeamDetailsSection } from "./primitives";

type RuntimeTeamResultsSectionProps = {
  details: TeamDetailsState;
  onToggle: () => void;
  open: boolean;
  visibleTasks: ReturnType<typeof sortTasks>;
};

/**
 * Results intentionally expose only the task's current Runtime projection.
 * `result_ref` is an opaque legacy reference; this surface does not imply
 * attempt history, artifact metadata, authorization, retention, or download.
 */
export function RuntimeTeamResultsSection({
  details,
  onToggle,
  open,
  visibleTasks,
}: RuntimeTeamResultsSectionProps) {
  const { t } = useTranslation("workspace");

  return (
    <TeamDetailsSection
      title={t("panels.teamsPanels.details.results.title")}
      subtitle={t("panels.teamsPanels.details.results.subtitle")}
      badge={<Badge>{details.tasks.length}</Badge>}
      open={open}
      onToggle={onToggle}
    >
      <div className="space-y-1.5">
        {visibleTasks.length > 0 ? (
          visibleTasks.map((task) => (
            <div key={task.id} className={detailCardClass}>
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate app-text-13 font-semibold text-foreground">
                    {task.title || truncateIdentifier(task.id, 18)}
                  </div>
                  <div className="truncate text-xs text-muted-foreground">
                    {task.id}
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
                {task.attempt !== undefined ? (
                  <span>
                    {t("panels.teamsPanels.details.results.attempt", {
                      count: task.attempt,
                    })}
                  </span>
                ) : null}
                {task.retry_count !== undefined ? (
                  <span>
                    {t("panels.teamsPanels.details.results.retries", {
                      count: task.retry_count,
                    })}
                  </span>
                ) : null}
              </div>
              {task.summary ? (
                <p className="mt-2 text-sm leading-6 text-foreground">
                  {task.summary}
                </p>
              ) : null}
              {task.result_ref ? (
                <div className="mt-2 rounded-control border border-border bg-surface-solid px-2.5 py-2 text-xs text-muted-foreground">
                  <div className="app-text-10 uppercase tracking-[0.12em]">
                    {t("panels.teamsPanels.details.results.referenceLabel")}
                  </div>
                  <code className="mt-1 block break-all text-foreground">
                    {task.result_ref}
                  </code>
                  <div className="mt-1">
                    {t("panels.teamsPanels.details.results.opaqueReference")}
                  </div>
                </div>
              ) : (
                <p className="mt-2 text-xs text-muted-foreground">
                  {t("panels.teamsPanels.details.results.noReference")}
                </p>
              )}
            </div>
          ))
        ) : (
          <div className="text-sm text-muted-foreground">
            {t("panels.teamsPanels.details.results.empty")}
          </div>
        )}
      </div>
    </TeamDetailsSection>
  );
}
