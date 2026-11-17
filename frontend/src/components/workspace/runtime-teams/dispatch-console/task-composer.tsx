// 由 components/workspace/runtime-teams/dispatch-console.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { LoaderCircleIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";

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
        <label className="block">
          <span className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            {t("panels.teamsDispatch.taskComposer.title")}
          </span>
          <Input
            size="md"
            surface="dark"
            className="mt-2"
            value={dispatchTaskTitleDraft}
            onChange={(event) => onDispatchTaskTitleDraftChange(event.target.value)}
            placeholder={t("panels.teamsDispatch.taskComposer.titlePlaceholder")}
          />
        </label>
      </div>

      <div>
        <label className="block">
          <span className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            {t("panels.teamsDispatch.taskComposer.goal")}
          </span>
          <Textarea
            size="sm"
            surface="dark"
            className="mt-2 leading-6"
            value={dispatchTaskGoalDraft}
            onChange={(event) => onDispatchTaskGoalDraftChange(event.target.value)}
            placeholder={t("panels.teamsDispatch.taskComposer.goalPlaceholder")}
          />
        </label>
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <div>
          <label className="block">
            <span className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.teamsDispatch.taskComposer.inputs")}
            </span>
            <Textarea
              size="sm"
              surface="dark"
              className="mt-2 min-h-20 leading-6"
              value={dispatchTaskInputsDraft}
              onChange={(event) => onDispatchTaskInputsDraftChange(event.target.value)}
              placeholder={t("panels.teamsDispatch.taskComposer.inputsPlaceholder")}
            />
          </label>
        </div>
        <div>
          <label className="block">
            <span className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.teamsDispatch.taskComposer.deliverables")}
            </span>
            <Textarea
              size="sm"
              surface="dark"
              className="mt-2 min-h-20 leading-6"
              value={dispatchTaskDeliverablesDraft}
              onChange={(event) =>
                onDispatchTaskDeliverablesDraftChange(event.target.value)
              }
              placeholder={t("panels.teamsDispatch.taskComposer.deliverablesPlaceholder")}
            />
          </label>
        </div>
      </div>

      <div className="grid gap-3 sm:grid-cols-[160px_1fr]">
        <div>
          <label className="block">
            <span className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.teamsDispatch.taskComposer.priority")}
            </span>
            <Input
              size="md"
              surface="dark"
              className="mt-2"
              value={dispatchTaskPriorityDraft}
              onChange={(event) => onDispatchTaskPriorityDraftChange(event.target.value)}
              placeholder="50"
            />
          </label>
        </div>
      </div>

      <div className="flex justify-end">
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
