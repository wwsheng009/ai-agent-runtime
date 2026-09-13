// 由 components/workspace/runtime-teams/dispatch-console.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { LoaderCircleIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

import { consoleInputClass } from "./console-styles";
import type { DispatchConsoleProps } from "./types";

type DispatchTaskComposerProps = Pick<
  DispatchConsoleProps,
  | "dispatchTaskDeliverablesDraft"
  | "dispatchTaskError"
  | "dispatchTaskGoalDraft"
  | "dispatchTaskInputsDraft"
  | "dispatchTaskPriorityDraft"
  | "dispatchTaskTitleDraft"
  | "isDispatchingTask"
  | "onDispatchTaskDeliverablesDraftChange"
  | "onDispatchTaskGoalDraftChange"
  | "onDispatchTaskInputsDraftChange"
  | "onDispatchTaskPriorityDraftChange"
  | "onDispatchTaskTitleDraftChange"
  | "onDispatchTaskToTeams"
  | "selectedDispatchTeamIds"
>;

export function DispatchTaskComposer({
  dispatchTaskDeliverablesDraft,
  dispatchTaskError,
  dispatchTaskGoalDraft,
  dispatchTaskInputsDraft,
  dispatchTaskPriorityDraft,
  dispatchTaskTitleDraft,
  isDispatchingTask,
  onDispatchTaskDeliverablesDraftChange,
  onDispatchTaskGoalDraftChange,
  onDispatchTaskInputsDraftChange,
  onDispatchTaskPriorityDraftChange,
  onDispatchTaskTitleDraftChange,
  onDispatchTaskToTeams,
  selectedDispatchTeamIds,
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
          className={consoleInputClass}
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
          className={cn(consoleInputClass, "min-h-24 resize-y leading-6")}
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
            placeholder="spec.md&#10;open questions&#10;expected risks"
            className={cn(consoleInputClass, "min-h-20 resize-y leading-6")}
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
            placeholder="summary.md&#10;patch.diff&#10;validation notes"
            className={cn(consoleInputClass, "min-h-20 resize-y leading-6")}
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
            className={consoleInputClass}
          />
        </div>
        <div className="flex items-end">
          <div className="text-xs text-muted-foreground">
            Tasks are created with `status=ready`, so active team orchestrators can claim and execute them.
          </div>
        </div>
      </div>

      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="text-xs text-muted-foreground">
          Use this to fan out the same next task across multiple executable teams for parallel execution.
        </div>
        <Button
          variant="primary"
          size="sm"
          onClick={() => onDispatchTaskToTeams()}
          disabled={isDispatchingTask || selectedDispatchTeamIds.length === 0}
        >
          {isDispatchingTask ? (
            <LoaderCircleIcon size={14} className="animate-spin" />
          ) : null}
          Dispatch next task
        </Button>
      </div>

      {dispatchTaskError ? (
        <div className="rounded-[0.8rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
          {dispatchTaskError}
        </div>
      ) : null}
    </>
  );
}
