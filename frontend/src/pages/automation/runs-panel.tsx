import { useEffect, useMemo, useRef, useState } from "react";
import {
  AlertTriangleIcon,
  Clock3Icon,
  HistoryIcon,
  LoaderCircleIcon,
  RefreshCwIcon,
  WorkflowIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { fetchScheduleJobRuns } from "@/api/runtime/schedule";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select, type SelectOption } from "@/components/ui/select";
import { formatScheduleTimestamp } from "@/components/workspace/schedule-panel-shared";
import type {
  RuntimeScheduleJobRun,
  RuntimeScheduleJobRunsResponse,
  RuntimeScheduleTask,
} from "@/types/runtime";

const RUNS_LIMIT = 50;
const ALL_TASKS = "__all_tasks__";

type AutomationRunsPanelProps = {
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

export function AutomationRunsPanel({ tasks }: AutomationRunsPanelProps) {
  const { t } = useTranslation("workspace");
  const [selectedTaskId, setSelectedTaskId] = useState(ALL_TASKS);
  const [refreshToken, setRefreshToken] = useState(0);
  const [runs, setRuns] = useState<RuntimeScheduleJobRun[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(false);
  const [partialError, setPartialError] = useState(false);
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

  useEffect(() => {
    if (
      selectedTaskId !== ALL_TASKS &&
      !tasks.some((task) => task.id === selectedTaskId)
    ) {
      setSelectedTaskId(ALL_TASKS);
    }
  }, [selectedTaskId, tasks]);

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
              onChange={setSelectedTaskId}
              options={taskOptions}
              placeholder={t("panels.schedule.runs.allTasks")}
              triggerTestId="automation-runs-task-filter"
              value={selectedTaskId}
            />
          </label>
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
            <ol className="mt-3 space-y-2">
              {runs.map((run) => {
                const task = taskById.get(run.scheduleId);
                const taskTitle = task?.title?.trim() || run.scheduleId;
                return (
                  <li
                    className="grid gap-3 rounded-panel border border-border bg-surface-softer p-3 sm:grid-cols-[minmax(11rem,0.7fr)_minmax(0,1.3fr)]"
                    key={run.id}
                  >
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-2">
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
                        <span className="text-xs text-muted-foreground">
                          #{run.id}
                        </span>
                        <span
                          className="max-w-full truncate rounded-full border border-border bg-surface-solid px-2 py-0.5 text-xs text-muted-foreground"
                          title={run.scheduleId}
                        >
                          {taskTitle}
                        </span>
                      </div>
                      <dl className="mt-2 space-y-1 text-xs leading-5 text-muted-foreground">
                        <div>
                          <dt className="inline font-medium text-foreground">
                            {t("panels.schedule.runs.started")}:{" "}
                          </dt>
                          <dd className="inline">
                            {formatScheduleTimestamp(run.startedAt)}
                          </dd>
                        </div>
                        <div>
                          <dt className="inline font-medium text-foreground">
                            {t("panels.schedule.runs.finished")}:{" "}
                          </dt>
                          <dd className="inline">
                            {formatScheduleTimestamp(run.finishedAt)}
                          </dd>
                        </div>
                        {runDuration(run) ? (
                          <div>
                            <dt className="inline font-medium text-foreground">
                              {t("panels.schedule.runs.duration")}:{" "}
                            </dt>
                            <dd className="inline">{runDuration(run)}</dd>
                          </div>
                        ) : null}
                        {run.sessionId ? (
                          <div className="break-all">
                            <dt className="inline font-medium text-foreground">
                              {t("panels.schedule.runs.session")}:{" "}
                            </dt>
                            <dd className="inline">{run.sessionId}</dd>
                          </div>
                        ) : null}
                      </dl>
                    </div>
                    <div className="min-w-0 space-y-2 border-t border-border/70 pt-2 text-xs leading-5 sm:border-l sm:border-t-0 sm:pl-3 sm:pt-0">
                      {run.output ? (
                        <div>
                          <h3 className="font-semibold text-foreground">
                            {t("panels.schedule.runOutput")}
                          </h3>
                          <pre className="mt-1 max-h-56 overflow-auto whitespace-pre-wrap break-words rounded-control border border-border bg-surface-solid p-2 font-sans text-muted-foreground">
                            {run.output}
                          </pre>
                        </div>
                      ) : null}
                      {run.error || run.errorCode ? (
                        <div className="rounded-control border border-destructive/30 bg-destructive/5 p-2 text-destructive">
                          <h3 className="font-semibold">
                            {t("panels.schedule.runError")}
                          </h3>
                          <p className="mt-1">
                            {run.errorCode === "headless_executor_unavailable"
                              ? t(
                                  "panels.schedule.errorCode.headless_executor_unavailable",
                                )
                              : t("panels.schedule.runFailure")}
                          </p>
                        </div>
                      ) : null}
                      {!run.output && !run.error && !run.errorCode ? (
                        <p className="text-muted-foreground">
                          {t("panels.schedule.runs.noOutput")}
                        </p>
                      ) : null}
                    </div>
                  </li>
                );
              })}
            </ol>
          )}
        </>
      )}
    </section>
  );
}
