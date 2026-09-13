// 由 components/workspace/runtime-teams/dispatch-console.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { LoaderCircleIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

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
  const { t } = useTranslation("workspace");

  return (
    <>
      <div>
        <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
          {t("panels.teamsDispatch.taskComposer.title")}
        </div>
        <input
          value={dispatchTaskTitleDraft}
          onChange={(event) => onDispatchTaskTitleDraftChange(event.target.value)}
          placeholder={t("panels.teamsDispatch.taskComposer.titlePlaceholder")}
          className={consoleInputClass}
        />
      </div>

      <div>
        <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
          {t("panels.teamsDispatch.taskComposer.goal")}
        </div>
        <textarea
          value={dispatchTaskGoalDraft}
          onChange={(event) => onDispatchTaskGoalDraftChange(event.target.value)}
          placeholder={t("panels.teamsDispatch.taskComposer.goalPlaceholder")}
          className={cn(consoleInputClass, "min-h-24 resize-y leading-6")}
        />
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            {t("panels.teamsDispatch.taskComposer.inputs")}
          </div>
          <textarea
            value={dispatchTaskInputsDraft}
            onChange={(event) => onDispatchTaskInputsDraftChange(event.target.value)}
            placeholder={t("panels.teamsDispatch.taskComposer.inputsPlaceholder")}
            className={cn(consoleInputClass, "min-h-20 resize-y leading-6")}
          />
        </div>
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            {t("panels.teamsDispatch.taskComposer.deliverables")}
          </div>
          <textarea
            value={dispatchTaskDeliverablesDraft}
            onChange={(event) =>
              onDispatchTaskDeliverablesDraftChange(event.target.value)
            }
            placeholder={t("panels.teamsDispatch.taskComposer.deliverablesPlaceholder")}
            className={cn(consoleInputClass, "min-h-20 resize-y leading-6")}
          />
        </div>
      </div>

      <div className="grid gap-3 sm:grid-cols-[160px_1fr]">
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            {t("panels.teamsDispatch.taskComposer.priority")}
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
            {t("panels.teamsDispatch.taskComposer.statusHint")}
          </div>
        </div>
      </div>

      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="text-xs text-muted-foreground">
          {t("panels.teamsDispatch.taskComposer.submitHint")}
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
          {t("panels.teamsDispatch.taskComposer.submit")}
        </Button>
      </div>

      {dispatchTaskError ? (
        <div className="rounded-card border border-accent-orange/18 bg-accent-orange/8 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
          {dispatchTaskError}
        </div>
      ) : null}
    </>
  );
}
