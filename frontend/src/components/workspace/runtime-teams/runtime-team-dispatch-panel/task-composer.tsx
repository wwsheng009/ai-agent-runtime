import { LoaderCircleIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  type MultiTeamDispatchResult,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared";
import { cn } from "@/lib/utils";

import { dispatchControlClass, dispatchStatusPillClass } from "./format";

type DispatchTaskComposerProps = {
  dispatchTaskDeliverablesDraft: string;
  dispatchTaskError: string | null;
  dispatchTaskGoalDraft: string;
  dispatchTaskInputsDraft: string;
  dispatchTaskPriorityDraft: string;
  dispatchTaskResults: MultiTeamDispatchResult[];
  dispatchTaskTitleDraft: string;
  isDispatchingTask: boolean;
  onDispatchTaskDeliverablesDraftChange: (value: string) => void;
  onDispatchTaskGoalDraftChange: (value: string) => void;
  onDispatchTaskInputsDraftChange: (value: string) => void;
  onDispatchTaskPriorityDraftChange: (value: string) => void;
  onDispatchTaskTitleDraftChange: (value: string) => void;
  onDispatchTaskToTeams: () => void | Promise<void>;
  selectedTeamCount: number;
};

export function DispatchTaskComposer({
  dispatchTaskDeliverablesDraft,
  dispatchTaskError,
  dispatchTaskGoalDraft,
  dispatchTaskInputsDraft,
  dispatchTaskPriorityDraft,
  dispatchTaskResults,
  dispatchTaskTitleDraft,
  isDispatchingTask,
  onDispatchTaskDeliverablesDraftChange,
  onDispatchTaskGoalDraftChange,
  onDispatchTaskInputsDraftChange,
  onDispatchTaskPriorityDraftChange,
  onDispatchTaskTitleDraftChange,
  onDispatchTaskToTeams,
  selectedTeamCount,
}: DispatchTaskComposerProps) {
  return (
    <>
      <div>
        <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
          Task title
        </div>
        <input
          value={dispatchTaskTitleDraft}
          onChange={(event) => onDispatchTaskTitleDraftChange(event.target.value)}
          placeholder="Parallel review of runtime stream stability"
          className={dispatchControlClass}
        />
      </div>

      <div>
        <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
          Goal
        </div>
        <textarea
          value={dispatchTaskGoalDraft}
          onChange={(event) => onDispatchTaskGoalDraftChange(event.target.value)}
          placeholder="Have each selected team tackle the same next task from a different angle and report outcomes independently."
          className={`min-h-24 ${dispatchControlClass} resize-y leading-6`}
        />
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            Inputs
          </div>
          <textarea
            value={dispatchTaskInputsDraft}
            onChange={(event) => onDispatchTaskInputsDraftChange(event.target.value)}
            placeholder={"spec.md\nopen questions\nexpected risks"}
            className={`min-h-20 ${dispatchControlClass} resize-y leading-6`}
          />
        </div>
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            Deliverables
          </div>
          <textarea
            value={dispatchTaskDeliverablesDraft}
            onChange={(event) =>
              onDispatchTaskDeliverablesDraftChange(event.target.value)
            }
            placeholder={"summary.md\npatch.diff\nvalidation notes"}
            className={`min-h-20 ${dispatchControlClass} resize-y leading-6`}
          />
        </div>
      </div>

      <div className="grid gap-3 sm:grid-cols-[160px_1fr]">
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            Priority
          </div>
          <input
            value={dispatchTaskPriorityDraft}
            onChange={(event) => onDispatchTaskPriorityDraftChange(event.target.value)}
            placeholder="50"
            className={dispatchControlClass}
          />
        </div>
        <div className="flex items-end">
          <div className="text-xs text-muted-foreground">
            Tasks are created with `status=ready`, so active team orchestrators can
            claim and execute them.
          </div>
        </div>
      </div>

      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="text-xs text-muted-foreground">
          Use this to fan out the same next task across multiple executable teams
          for parallel execution.
        </div>
        <Button
          variant="primary"
          size="sm"
          onClick={onDispatchTaskToTeams}
          disabled={isDispatchingTask || selectedTeamCount === 0}
        >
          {isDispatchingTask ? (
            <LoaderCircleIcon size={14} className="animate-spin" />
          ) : null}
          Dispatch next task
        </Button>
      </div>

      {dispatchTaskError ? (
        <div className="rounded-[0.75rem] border border-accent-orange/18 bg-accent-orange/8 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
          {dispatchTaskError}
        </div>
      ) : null}

      {dispatchTaskResults.length > 0 ? (
        <div className="space-y-1.5">
          {dispatchTaskResults.map((result) => (
            <div
              key={`dispatch-result-${result.teamId}`}
              className="rounded-[0.8rem] border border-white/8 bg-white/4 px-3 py-2.5"
            >
              <div className="flex items-center justify-between gap-3">
                <div className="text-[13px] font-semibold text-foreground">
                  {truncateIdentifier(result.teamId, 18)}
                </div>
                <span
                  className={cn(
                    dispatchStatusPillClass,
                    result.status === "created"
                      ? "border-accent-teal/24 bg-accent-teal/10 text-accent-teal"
                      : "border-accent-orange/24 bg-accent-orange/10 text-accent-orange",
                  )}
                >
                  {result.status}
                </span>
              </div>
              <div className="mt-2 text-sm text-muted-foreground">
                {result.status === "created"
                  ? `task ${truncateIdentifier(result.taskId, 18)} created`
                  : result.error || "dispatch failed"}
              </div>
            </div>
          ))}
        </div>
      ) : null}
    </>
  );
}
