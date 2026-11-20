// P3：自动化任务（Schedule）单行：状态/摘要/运行历史/操作栏。
// 编辑改由独立表单对话框承载；行内保留暂停、恢复与删除确认。

import {
  HistoryIcon,
  LoaderCircleIcon,
  MessageSquareIcon,
  PauseIcon,
  PencilIcon,
  PlayIcon,
  Trash2Icon,
} from "lucide-react";
import type { Dispatch, SetStateAction } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  formatScheduleRelative,
  formatScheduleTimestamp,
  scheduleFrequencySummary,
  scheduleKindLabelKey,
  scheduleStatusLabelKey,
} from "@/components/workspace/schedule-panel-shared";
import { cn } from "@/lib/utils";
import type { RuntimeScheduleJobRun, RuntimeScheduleTask } from "@/types/runtime";

import { ScheduleRunHistory } from "./run-history";

export type ScheduleTaskRowProps = {
  confirmingId: string;
  control: (task: RuntimeScheduleTask, action: "pause" | "resume") => void;
  controllingAction: "pause" | "resume" | null;
  controllingId: string;
  deleteLocked?: boolean;
  deletingId: string;
  editingId: string;
  expandedRunId: string;
  live: boolean;
  onOpenSession?: (sessionId: string) => void;
  onViewHistory?: (task: RuntimeScheduleTask) => void;
  remove: (task: RuntimeScheduleTask) => void;
  runsByTask: Record<string, RuntimeScheduleJobRun[]>;
  runsStatus: Record<string, "loading" | "error">;
  setConfirmingId: Dispatch<SetStateAction<string>>;
  startEdit: (task: RuntimeScheduleTask) => void;
  task: RuntimeScheduleTask;
  toggleRuns: (taskId: string) => void;
};

export function ScheduleTaskRow({
  confirmingId,
  control,
  controllingAction,
  controllingId,
  deleteLocked = false,
  deletingId,
  editingId,
  expandedRunId,
  live,
  onOpenSession,
  onViewHistory,
  remove,
  runsByTask,
  runsStatus,
  setConfirmingId,
  startEdit,
  task,
  toggleRuns,
}: ScheduleTaskRowProps) {
  const { i18n, t } = useTranslation("workspace");
  const controlling = controllingId === task.id;
  const active = task.status === "active";
  const paused = task.status === "paused";
  const blocked = task.status === "blocked";
  const failed = task.status === "failed";
  const editing = editingId === task.id;
  const deleting = deletingId === task.id;
  const pauseBusy = controlling && controllingAction === "pause";
  const resumeBusy = controlling && controllingAction === "resume";
  const targetSessionId =
    (task.targetKind ?? "session") === "session"
      ? task.sessionId?.trim() || ""
      : "";
  const creatorSessionId = task.createdBySessionId?.trim() || "";
  const associatedSessionId = targetSessionId || creatorSessionId;
  const sessionLabelKey = targetSessionId
    ? "panels.schedule.associatedSession"
    : "panels.schedule.creatorSession";

  return (
    <li className="rounded-panel border border-border p-3">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2">
          <Badge
            className={cn(
              active &&
                "border-emerald-500/40 text-emerald-600 dark:text-emerald-400",
              paused &&
                "border-amber-500/40 text-amber-600 dark:text-amber-400",
              blocked &&
                "border-destructive/50 bg-destructive/10 text-destructive",
              failed &&
                "border-destructive/50 bg-destructive/10 text-destructive",
            )}
          >
            {t(scheduleStatusLabelKey(task.status))}
          </Badge>
          {task.origin === "manual" ? (
            <Badge className="border-amber-500/40 text-amber-600 dark:text-amber-400">
              {t("panels.schedule.originManual")}
            </Badge>
          ) : null}
          <span className="min-w-0 truncate text-sm font-medium">
            {task.title}
          </span>
        </div>

        <div className="mt-1 text-xs text-muted-foreground">
          {t(scheduleKindLabelKey(task.kind))}
          {" · "}
          {scheduleFrequencySummary(task, i18n.language)}
          {task.timeZone ? ` · ${task.timeZone}` : ""}
        </div>
        {live ? (
          <div className="mt-1 text-xs">
            {t("panels.schedule.nextRun")}:{" "}
            {formatScheduleTimestamp(task.scheduledAt)}
            <span className="ml-2 text-muted-foreground">
              ({formatScheduleRelative(task.scheduledAt, Date.now(), i18n.language)})
            </span>
          </div>
        ) : paused ? (
          <div className="mt-1 text-xs">
            {t("panels.schedule.pausedNextRun")}:{" "}
            {formatScheduleTimestamp(task.scheduledAt)}
            <span className="ml-2 text-muted-foreground">
              ({formatScheduleRelative(task.scheduledAt, Date.now(), i18n.language)})
            </span>
          </div>
        ) : blocked || failed ? (
          <div className="mt-1 text-xs">
            {t("panels.schedule.scheduledTarget")}:{" "}
            {formatScheduleTimestamp(task.scheduledAt)}
            <span className="ml-2 text-muted-foreground">
              ({formatScheduleRelative(task.scheduledAt, Date.now(), i18n.language)})
            </span>
          </div>
        ) : null}

        {!live && task.lastDelivery ? (
          <div className="mt-1 text-xs text-muted-foreground">
            {t("panels.schedule.lastDelivery")}:{" "}
            {formatScheduleTimestamp(task.lastDelivery.deliveredAt)}
          </div>
        ) : null}

        {task.lastAttemptAt ? (
          <div className="mt-1 text-xs text-muted-foreground">
            {t("panels.schedule.lastAttempt")}:{" "}
            {formatScheduleTimestamp(task.lastAttemptAt)}
          </div>
        ) : null}

        {onOpenSession && associatedSessionId ? (
          <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
            <span>{t(sessionLabelKey)}:</span>
            <code className="break-all rounded bg-surface-soft px-1.5 py-0.5 font-mono text-[11px] text-foreground">
              {associatedSessionId}
            </code>
            {onOpenSession ? (
              <Button
                aria-label={t("panels.schedule.openSessionFor", {
                  title: task.title || task.id,
                })}
                className="h-6 px-2 text-xs"
                onClick={() => onOpenSession(associatedSessionId)}
                size="sm"
                variant="ghost"
              >
                <MessageSquareIcon aria-hidden="true" className="size-3.5" />
                {t("panels.schedule.openSession")}
              </Button>
            ) : null}
          </div>
        ) : null}

        {task.goal ||
        (task.plan && task.plan.length > 0) ||
        (task.targetKind && task.targetKind !== "session") ? (
          <div className="mt-1 space-y-1 text-xs text-muted-foreground">
            {task.goal ? (
              <div>
                {t("panels.schedule.goalLabel")}: {task.goal}
              </div>
            ) : null}
            {task.plan && task.plan.length > 0 ? (
              <div>
                {t("panels.schedule.planLabel")}:
                <ol className="ml-4 list-decimal">
                  {task.plan.map((step, index) => (
                    <li key={`${task.id}-plan-${index}`}>{step}</li>
                  ))}
                </ol>
              </div>
            ) : null}
            {task.targetKind === "workspace" ? (
              <div>
                {t("panels.schedule.targetLabel")}:{" "}
                {t("panels.schedule.target.workspace")}
                {task.targetWorkspace ? ` · ${task.targetWorkspace}` : ""}
              </div>
            ) : task.targetKind === "headless" ? (
              <div>
                {t("panels.schedule.targetLabel")}:{" "}
                {t("panels.schedule.target.headless")}
              </div>
            ) : null}
          </div>
        ) : null}

        {!onViewHistory ? (
          <ScheduleRunHistory
            expanded={expandedRunId === task.id}
            onToggle={() => void toggleRuns(task.id)}
            runs={runsByTask[task.id] ?? []}
            status={runsStatus[task.id]}
          />
        ) : null}

        {task.lastError || task.lastErrorCode ? (
          <p className="mt-1 rounded border border-destructive/30 bg-destructive/5 px-2 py-1 text-xs text-destructive">
            {task.lastErrorCode === "headless_executor_unavailable"
              ? t("panels.schedule.errorCode.headless_executor_unavailable")
              : task.lastErrorCode === "delivery_failed"
                ? t(
                    blocked || failed
                      ? "panels.schedule.errorCode.delivery_failed"
                      : "panels.schedule.errorCode.delivery_retrying",
                  )
              : t("panels.schedule.errorCode.generic")}
          </p>
        ) : null}

        <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">
          {task.prompt}
        </p>
      </div>

      <div className="mt-2 flex flex-wrap items-center gap-1.5 border-t border-border/70 pt-2">
        {active ? (
          <Button
            disabled={controlling}
            onClick={() => control(task, "pause")}
            size="sm"
            variant="ghost"
          >
            {pauseBusy ? (
              <LoaderCircleIcon
                aria-hidden="true"
                className="size-3.5 animate-spin"
              />
            ) : (
              <PauseIcon aria-hidden="true" className="size-3.5" />
            )}
            {pauseBusy
              ? t("panels.schedule.pausing")
              : t("panels.schedule.pause")}
          </Button>
        ) : null}

        {paused || blocked || failed ? (
          <Button
            disabled={controlling}
            onClick={() => control(task, "resume")}
            size="sm"
            variant="ghost"
          >
            {resumeBusy ? (
              <LoaderCircleIcon
                aria-hidden="true"
                className="size-3.5 animate-spin"
              />
            ) : (
              <PlayIcon aria-hidden="true" className="size-3.5" />
            )}
            {resumeBusy
              ? t("panels.schedule.resuming")
              : blocked || failed
                ? t("panels.schedule.retry")
                : t("panels.schedule.resume")}
          </Button>
        ) : null}

        {task.status === "inactive" ? (
          <Button
            disabled={controlling}
            onClick={() => control(task, "resume")}
            size="sm"
            variant="ghost"
          >
            {resumeBusy ? (
              <LoaderCircleIcon
                aria-hidden="true"
                className="size-3.5 animate-spin"
              />
            ) : (
              <PlayIcon aria-hidden="true" className="size-3.5" />
            )}
            {resumeBusy
              ? t("panels.schedule.enabling")
              : t("panels.schedule.enable")}
          </Button>
        ) : null}

        {onViewHistory ? (
          <Button
            aria-label={t("panels.schedule.viewHistoryFor", {
              title: task.title || task.id,
            })}
            onClick={() => onViewHistory(task)}
            size="sm"
            variant="ghost"
          >
            <HistoryIcon aria-hidden="true" className="size-3.5" />
            {t("panels.schedule.viewHistory")}
          </Button>
        ) : null}

        <Button
          aria-expanded={editing}
          aria-haspopup="dialog"
          disabled={controlling}
          onClick={() => startEdit(task)}
          size="sm"
          variant="ghost"
        >
          <PencilIcon aria-hidden="true" className="size-3.5" />
          {t("panels.schedule.edit")}
        </Button>

        <div className="ml-auto">
          {confirmingId === task.id ? (
            <div className="flex items-center gap-1">
              <Button
                disabled={deleting || deleteLocked}
                onClick={() => void remove(task)}
                size="sm"
                variant="destructive"
              >
                {deleting
                  ? t("panels.schedule.deleting")
                  : t("panels.schedule.confirmDelete")}
              </Button>
              <Button
                disabled={deleting || deleteLocked}
                onClick={() => setConfirmingId("")}
                size="sm"
                variant="ghost"
              >
                {t("panels.schedule.cancel")}
              </Button>
            </div>
          ) : (
            <Button
              aria-label={t("panels.schedule.delete")}
              disabled={deleting || deleteLocked}
              onClick={() => setConfirmingId(task.id)}
              size="icon"
              title={t("panels.schedule.delete")}
              variant="ghost"
            >
              <Trash2Icon aria-hidden="true" className="size-3.5" />
            </Button>
          )}
        </div>
      </div>
    </li>
  );
}