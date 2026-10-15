// P3：自动化任务（Schedule）单行：状态/摘要/行内编辑草稿/删除确认/运行历史。
// 由 components/workspace/schedule-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Trash2Icon } from "lucide-react";
import type { Dispatch, SetStateAction } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  formatScheduleRelative,
  formatScheduleTimestamp,
  scheduleFrequencySummary,
  scheduleKindLabelKey,
} from "@/components/workspace/schedule-panel-shared";
import { cn } from "@/lib/utils";
import type { RuntimeScheduleJobRun, RuntimeScheduleTask } from "@/types/runtime";

import type { DraftState } from "./form-state";
import { ScheduleRunHistory } from "./run-history";

export type ScheduleTaskRowProps = {
  cancelEdit: () => void;
  confirmingId: string;
  deletingId: string;
  draft: DraftState | null;
  editingId: string;
  expandedRunId: string;
  live: boolean;
  remove: (task: RuntimeScheduleTask) => void;
  runsByTask: Record<string, RuntimeScheduleJobRun[]>;
  runsStatus: Record<string, "loading" | "error">;
  save: (task: RuntimeScheduleTask) => void;
  savingId: string;
  setConfirmingId: Dispatch<SetStateAction<string>>;
  setDraft: Dispatch<SetStateAction<DraftState | null>>;
  startEdit: (task: RuntimeScheduleTask) => void;
  task: RuntimeScheduleTask;
  toggleRuns: (taskId: string) => void;
};

export function ScheduleTaskRow({
  cancelEdit,
  confirmingId,
  deletingId,
  draft,
  editingId,
  expandedRunId,
  live,
  remove,
  runsByTask,
  runsStatus,
  save,
  savingId,
  setConfirmingId,
  setDraft,
  startEdit,
  task,
  toggleRuns,
}: ScheduleTaskRowProps) {
  const { t } = useTranslation("workspace");
  const editing = editingId === task.id;
  return (
    <li
      className="rounded-panel border border-border p-3"
      key={task.id}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <Badge
              className={cn(
                live && "border-emerald-500/40 text-emerald-600 dark:text-emerald-400",
              )}
            >
              {t(
                live
                  ? "panels.schedule.statusActive"
                  : "panels.schedule.statusInactive",
              )}
            </Badge>
            {task.origin === "manual" && (
              <Badge className="border-amber-500/40 text-amber-600 dark:text-amber-400">
                {t("panels.schedule.originManual")}
              </Badge>
            )}
            <span className="truncate text-sm font-medium">
              {editing && draft ? (
                <input
                  aria-label={t("panels.schedule.titleLabel")}
                  className="w-full rounded border border-border bg-transparent px-1 py-0.5"
                  onChange={(event) =>
                    setDraft({ ...draft, title: event.target.value })
                  }
                  value={draft.title}
                />
              ) : (
                task.title
              )}
            </span>
          </div>
          <div className="mt-1 text-xs text-muted-foreground">
            {t(scheduleKindLabelKey(task.kind))}
            {" · "}
            {scheduleFrequencySummary(task)}
            {task.timeZone ? ` · ${task.timeZone}` : ""}
          </div>
          {live ? (
            <div className="mt-1 text-xs">
              {t("panels.schedule.nextRun")}:{" "}
              {formatScheduleTimestamp(task.scheduledAt)}
              <span className="ml-2 text-muted-foreground">
                ({formatScheduleRelative(task.scheduledAt, Date.now())})
              </span>
            </div>
          ) : task.lastDelivery ? (
            <div className="mt-1 text-xs text-muted-foreground">
              {t("panels.schedule.lastDelivery")}:{" "}
              {formatScheduleTimestamp(task.lastDelivery.deliveredAt)}
            </div>
          ) : null}
          {/* J0/J1：作业目标、计划与执行上下文（仅在有值时展示）。 */}
          {task.goal || (task.plan && task.plan.length > 0) || (task.targetKind && task.targetKind !== "session") ? (
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
                  {t("panels.schedule.targetLabel")}: {t("panels.schedule.target.workspace")}
                </div>
              ) : task.targetKind === "headless" ? (
                <div>
                  {t("panels.schedule.targetLabel")}: {t("panels.schedule.target.headless")}
                </div>
              ) : null}
            </div>
          ) : null}
          {/* J3：运行历史（按需展开）。 */}
          <ScheduleRunHistory
            expanded={expandedRunId === task.id}
            onToggle={() => void toggleRuns(task.id)}
            runs={runsByTask[task.id] ?? []}
            status={runsStatus[task.id]}
          />
          {editing && draft ? (
            <div className="mt-2 space-y-2">
              <textarea
                aria-label={t("panels.schedule.promptLabel")}
                className="w-full rounded border border-border bg-transparent px-2 py-1 text-xs"
                onChange={(event) =>
                  setDraft({ ...draft, prompt: event.target.value })
                }
                rows={2}
                value={draft.prompt}
              />
              {(task.kind === "daily" || task.kind === "weekly") && (
                <div className="flex gap-2">
                  <input
                    aria-label={t("panels.schedule.timeLabel")}
                    className="w-28 rounded border border-border bg-transparent px-2 py-1 text-xs"
                    onChange={(event) =>
                      setDraft({ ...draft, time: event.target.value })
                    }
                    placeholder={t("panels.schedule.timePlaceholder")}
                    value={draft.time}
                  />
                  <input
                    aria-label={t("panels.schedule.zoneLabel")}
                    className="flex-1 rounded border border-border bg-transparent px-2 py-1 text-xs"
                    onChange={(event) =>
                      setDraft({ ...draft, timeZone: event.target.value })
                    }
                    placeholder={t("panels.schedule.zonePlaceholder")}
                    value={draft.timeZone}
                  />
                </div>
              )}
            </div>
          ) : (
            <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">
              {task.prompt}
            </p>
          )}
        </div>
        <div className="flex shrink-0 flex-col gap-1">
          {editing ? (
            <>
              <Button
                disabled={savingId === task.id}
                onClick={() => void save(task)}
                size="sm"
              >
                {t("panels.schedule.save")}
              </Button>
              <Button onClick={cancelEdit} size="sm" variant="ghost">
                {t("panels.schedule.cancel")}
              </Button>
            </>
          ) : (
            <>
              <Button
                onClick={() => startEdit(task)}
                size="sm"
                variant="ghost"
              >
                {t("panels.schedule.edit")}
              </Button>
              {confirmingId === task.id ? (
                <div className="flex gap-1">
                  <Button
                    disabled={deletingId === task.id}
                    onClick={() => void remove(task)}
                    size="sm"
                    variant="destructive"
                  >
                    {t("panels.schedule.confirmDelete")}
                  </Button>
                  <Button
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
                  onClick={() => setConfirmingId(task.id)}
                  size="sm"
                  variant="ghost"
                >
                  <Trash2Icon className="size-4" />
                </Button>
              )}
            </>
          )}
        </div>
      </div>
    </li>
  );
}
