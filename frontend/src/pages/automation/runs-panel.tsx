import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertTriangleIcon,
  ChevronRightIcon,
  Clock3Icon,
  HistoryIcon,
  LoaderCircleIcon,
  RefreshCwIcon,
  Trash2Icon,
  WorkflowIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  clearAllScheduleJobRuns,
  clearScheduleJobRuns,
  fetchScheduleJobRuns,
} from "@/api/runtime/schedule";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select, type SelectOption } from "@/components/ui/select";
import { formatScheduleTimestamp } from "@/components/workspace/schedule-panel-shared";
import type {
  RuntimeScheduleJobRun,
  RuntimeScheduleJobRunsResponse,
  RuntimeScheduleTask,
} from "@/types/runtime";

import { ClearRunsDialog, RunDetailsDialog } from "./runs-dialogs";

const RUNS_LIMIT = 50;
export const AUTOMATION_RUNS_ALL_TASKS = "__all_tasks__";
const ALL_TASKS = AUTOMATION_RUNS_ALL_TASKS;

type AutomationRunsPanelProps = {
  /** Optional controlled selection. Omit to keep the panel self-managed. */
  onSelectedTaskIdChange?: (taskId: string) => void;
  selectedTaskId?: string;
  tasks: RuntimeScheduleTask[];
};

function runDuration(run: RuntimeScheduleJobRun): string | null {
  const started = Date.parse(run.startedAt);
  const finished = Date.parse(run.finishedAt);
  if (!Number.isFinite(started) || !Number.isFinite(finished)) {
    return null;
  }
  const seconds = Math.max(0, Math.round((finished - started) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainder = seconds % 60;
  return remainder > 0 ? `${minutes}m ${remainder}s` : `${minutes}m`;
}

export function AutomationRunsPanel({
  onSelectedTaskIdChange,
  selectedTaskId: controlledTaskId,
  tasks,
}: AutomationRunsPanelProps) {
  const { t } = useTranslation("workspace");
  const [internalTaskId, setInternalTaskId] = useState(ALL_TASKS);
  const selectedTaskId = controlledTaskId ?? internalTaskId;
  const setSelectedTaskId = useCallback(
    (taskId: string) => {
      if (onSelectedTaskIdChange) {
        onSelectedTaskIdChange(taskId);
        return;
      }
      setInternalTaskId(taskId);
    },
    [onSelectedTaskIdChange],
  );
  const [refreshToken, setRefreshToken] = useState(0);
  const [runs, setRuns] = useState<RuntimeScheduleJobRun[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(false);
  const [partialError, setPartialError] = useState(false);
  const [selectedRun, setSelectedRun] =
    useState<RuntimeScheduleJobRun | null>(null);
  const [clearDialogOpen, setClearDialogOpen] = useState(false);
  const [clearDialogAllTasks, setClearDialogAllTasks] = useState(false);
  const [clearing, setClearing] = useState(false);
  const [clearError, setClearError] = useState(false);
  const tasksRef = useRef(tasks);
  tasksRef.current = tasks;
  const taskRequestSignature = useMemo(
    () =>
      tasks
        .map(
          (task) =>
            `${task.id}:${task.sessionId ?? ""}:${task.createdBySessionId ?? ""}`,
        )
        .join("|"),
    [tasks],
  );
  const taskOptions: SelectOption[] = useMemo(
    () =>
      [
        {
          value: ALL_TASKS,
          label: t("panels.schedule.runs.allTasks"),
        },
        ...tasks.map((task) => ({
          value: task.id,
          label: task.title || task.id,
        })),
      ],
    [t, tasks],
  );
  const taskById = useMemo(
    () => new Map(tasks.map((task) => [task.id, task])),
    [tasks],
  );
  const selectedTask =
    selectedTaskId === ALL_TASKS ? undefined : taskById.get(selectedTaskId);
  const selectedTaskTitle =
    selectedTask?.title?.trim() || selectedTask?.id || "";

  useEffect(() => {
    if (
      selectedTaskId !== ALL_TASKS &&
      !tasks.some((task) => task.id === selectedTaskId)
    ) {
      setSelectedTaskId(ALL_TASKS);
    }
  }, [selectedTaskId, setSelectedTaskId, tasks]);

  useEffect(() => {
    setSelectedRun(null);
    setClearDialogOpen(false);
    setClearDialogAllTasks(false);
    setClearError(false);
  }, [selectedTaskId]);

  useEffect(() => {
    const currentTasks = tasksRef.current;
    if (currentTasks.length === 0) {
      setRuns([]);
      setError(false);
      setPartialError(false);
      setLoading(false);
      return;
    }
    const targets =
      selectedTaskId === ALL_TASKS
        ? currentTasks
        : currentTasks.filter((task) => task.id === selectedTaskId);
    if (targets.length === 0) {
      setRuns([]);
      setError(false);
      setPartialError(false);
      setLoading(false);
      return;
    }
    let current = true;
    setLoading(true);
    setError(false);
    setPartialError(false);
    void Promise.allSettled(
      targets.map((task) =>
        fetchScheduleJobRuns(
          task.sessionId || task.createdBySessionId || undefined,
          task.id,
          RUNS_LIMIT,
        ),
      ),
    )
      .then((results) => {
        if (!current) return;
        const fulfilled = results.filter(
          (
            result,
          ): result is PromiseFulfilledResult<RuntimeScheduleJobRunsResponse> =>
            result.status === "fulfilled",
        );
        if (fulfilled.length === 0) {
          setRuns([]);
          setError(true);
          setPartialError(false);
          return;
        }
        const mergedRuns = fulfilled
          .flatMap((result) => result.value.runs)
          .sort((left, right) => right.id - left.id)
          .slice(0, RUNS_LIMIT);
        setRuns(mergedRuns);
        setPartialError(fulfilled.length < results.length);
      })
      .finally(() => {
        if (current) setLoading(false);
      });
    return () => {
      current = false;
    };
  }, [refreshToken, selectedTaskId, taskRequestSignature]);

  const handleTaskChange = (value: string) => {
    setSelectedTaskId(value);
  };

  const openClearDialog = (allTasks: boolean) => {
    setClearDialogAllTasks(allTasks);
    setClearError(false);
    setClearDialogOpen(true);
  };

  const handleClearRuns = async () => {
    if (clearing) {
      return;
    }
    const taskToClear = selectedTask;
    if (!clearDialogAllTasks && !taskToClear) {
      return;
    }
    setClearing(true);
    setClearError(false);
    try {
      if (clearDialogAllTasks) {
        await clearAllScheduleJobRuns();
        setRuns([]);
        setPartialError(false);
      } else if (taskToClear) {
        await clearScheduleJobRuns(
          taskToClear.sessionId ||
            taskToClear.createdBySessionId ||
            undefined,
          taskToClear.id,
        );
        setRuns((currentRuns) =>
          currentRuns.filter((run) => run.scheduleId !== taskToClear.id),
        );
      }
      setSelectedRun(null);
      setClearDialogOpen(false);
      setClearDialogAllTasks(false);
    } catch {
      setClearError(true);
    } finally {
      setClearing(false);
    }
  };

  return (
    <section
      aria-labelledby="automation-runs-title"
      className="surface-panel min-h-0 rounded-panel-lg p-3 sm:p-4"
    >
      <div className="flex flex-col gap-3 border-b border-border pb-3 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <div className="flex items-center gap-2">
            <HistoryIcon aria-hidden="true" className="size-4 text-accent-secondary" />
            <h2 id="automation-runs-title" className="text-sm font-semibold">
              {t("panels.schedule.runs.title")}
            </h2>
          </div>
          <p className="mt-1 max-w-2xl text-xs leading-5 text-muted-foreground">
            {t("panels.schedule.runs.description", { count: RUNS_LIMIT })}
          </p>
        </div>
        <div className="flex flex-wrap items-end gap-2">
          <label className="min-w-[min(100%,18rem)] flex-1 sm:flex-none sm:w-72">
            <span className="mb-1 block text-xs font-medium text-muted-foreground">
              {t("panels.schedule.runs.taskLabel")}
            </span>
            <Select
              ariaLabel={t("panels.schedule.runs.taskLabel")}
              disabled={tasks.length === 0}
              onChange={handleTaskChange}
              options={taskOptions}
              placeholder={t("panels.schedule.runs.allTasks")}
              triggerTestId="automation-runs-task-filter"
              value={selectedTaskId}
            />
          </label>
          {selectedTask ? (
            <Button
              aria-label={t("panels.schedule.runs.clear")}
              data-testid="automation-clear-task-runs"
              disabled={
                loading ||
                clearing ||
                !runs.some((run) => run.scheduleId === selectedTask.id)
              }
              onClick={() => openClearDialog(false)}
              size="sm"
              variant="destructive"
            >
              <Trash2Icon aria-hidden="true" size={14} />
              <span>{t("panels.schedule.runs.clear")}</span>
            </Button>
          ) : selectedTaskId === ALL_TASKS ? (
            <Button
              aria-label={t("panels.schedule.runs.clearAll")}
              data-testid="automation-clear-all-runs"
              disabled={loading || clearing}
              onClick={() => openClearDialog(true)}
              size="sm"
              variant="destructive"
            >
              <Trash2Icon aria-hidden="true" size={14} />
              <span>{t("panels.schedule.runs.clearAll")}</span>
            </Button>
          ) : null}
          <Button
            aria-label={t("panels.schedule.runs.refresh")}
            disabled={tasks.length === 0 || loading}
            onClick={() => setRefreshToken((value) => value + 1)}
            size="icon"
            variant="secondary"
          >
            <RefreshCwIcon
              aria-hidden="true"
              className={loading ? "size-4 animate-spin" : "size-4"}
            />
          </Button>
        </div>
      </div>

      {tasks.length === 0 ? (
        <div className="mt-4 rounded-panel border border-dashed border-border px-4 py-8 text-center">
          <WorkflowIcon aria-hidden="true" className="mx-auto size-6 text-muted-foreground" />
          <p className="mt-2 text-sm font-medium">{t("panels.schedule.runs.noTasks")}</p>
          <p className="mt-1 text-xs text-muted-foreground">
            {t("panels.schedule.runs.noTasksHint")}
          </p>
        </div>
      ) : loading ? (
        <div className="mt-4 flex items-center justify-center gap-2 rounded-panel border border-border/70 bg-surface/30 px-4 py-10 text-sm text-muted-foreground" role="status">
          <LoaderCircleIcon aria-hidden="true" className="size-4 animate-spin" />
          {t("panels.schedule.runs.loading")}
        </div>
      ) : error ? (
        <div className="mt-4 flex flex-col items-center gap-3 rounded-panel border border-destructive/30 bg-destructive/5 px-4 py-8 text-center" role="alert">
          <AlertTriangleIcon aria-hidden="true" className="size-5 text-destructive" />
          <p className="text-sm text-destructive">{t("panels.schedule.runs.loadError")}</p>
          <Button onClick={() => setRefreshToken((value) => value + 1)} size="sm" variant="secondary">
            {t("panels.schedule.runs.retry")}
          </Button>
        </div>
      ) : (
        <>
          {partialError ? (
            <div
              className="mt-3 flex items-start gap-2 rounded-field border border-accent-orange/30 bg-accent-orange/10 px-3 py-2.5 text-sm text-accent-orange"
              role="status"
            >
              <AlertTriangleIcon
                aria-hidden="true"
                className="mt-0.5 size-4 shrink-0"
              />
              <span>{t("panels.schedule.runs.partialLoadError")}</span>
            </div>
          ) : null}
          {runs.length === 0 ? (
            <div className="mt-4 rounded-panel border border-dashed border-border px-4 py-8 text-center">
              <Clock3Icon
                aria-hidden="true"
                className="mx-auto size-6 text-muted-foreground"
              />
              <p className="mt-2 text-sm font-medium">
                {t("panels.schedule.runs.empty")}
              </p>
              <p className="mt-1 text-xs text-muted-foreground">
                {t("panels.schedule.runs.emptyHint")}
              </p>
            </div>
          ) : (
            <ol
              aria-label={t("panels.schedule.runs.listLabel")}
              className="mt-3 divide-y divide-border overflow-hidden rounded-panel border border-border bg-surface-softer"
            >
              {runs.map((run) => {
                const task = taskById.get(run.scheduleId);
                const taskTitle = task?.title?.trim() || run.scheduleId;
                return (
                  <li className="min-w-0" key={`${run.scheduleId}:${run.id}`}>
                    <button
                      aria-label={t("panels.schedule.runs.openDetails", {
                        taskTitle,
                        runId: String(run.id),
                      })}
                      className="group flex min-h-11 w-full min-w-0 items-center gap-2 px-3 py-2 text-left transition-colors hover:bg-surface/70 focus-visible:z-10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
                      data-testid={`automation-run-row-${run.id}`}
                      onClick={() => setSelectedRun(run)}
                      type="button"
                    >
                      <Badge
                        className={
                          run.status === "failed"
                            ? "border-destructive/40 bg-destructive/5 text-destructive"
                            : "border-emerald-500/40 text-emerald-700 dark:text-emerald-300"
                        }
                      >
                        {run.status === "failed"
                          ? t("panels.schedule.runStatus.failed")
                          : t("panels.schedule.runStatus.done")}
                      </Badge>
                      <span className="min-w-0 flex-1 truncate text-xs font-medium text-foreground">
                        {taskTitle}
                      </span>
                      <span
                        className="hidden shrink-0 text-xs text-muted-foreground sm:inline"
                        title={t("panels.schedule.runs.finished")}
                      >
                        {formatScheduleTimestamp(run.finishedAt)}
                      </span>
                      <span className="hidden shrink-0 text-xs text-muted-foreground md:inline">
                        {runDuration(run) ?? "—"}
                      </span>
                      <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
                        #{run.id}
                      </span>
                      <ChevronRightIcon
                        aria-hidden="true"
                        className="size-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5"
                      />
                    </button>
                  </li>
                );
              })}
            </ol>
          )}
        </>
      )}
      <RunDetailsDialog
        duration={selectedRun ? runDuration(selectedRun) : null}
        onClose={() => setSelectedRun(null)}
        run={selectedRun}
        taskTitle={
          selectedRun
            ? taskById.get(selectedRun.scheduleId)?.title?.trim() ||
              selectedRun.scheduleId
            : ""
        }
      />
      <ClearRunsDialog
        allTasks={clearDialogAllTasks}
        busy={clearing}
        error={clearError}
        onCancel={() => {
          if (!clearing) setClearDialogOpen(false);
        }}
        onConfirm={() => void handleClearRuns()}
        open={clearDialogOpen}
        taskTitle={clearDialogAllTasks ? "" : selectedTaskTitle}
      />
    </section>
  );
}
