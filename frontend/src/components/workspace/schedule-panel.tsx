// P3：自动化任务（Schedule）面板。
//
// 数据全部来自 `/api/runtime/schedule/*`（无假数据）。活动/未运行分区展示；
// 行内动作：删除（确认）与编辑（名称/指令/时间的草稿保存，完整 expected
// 记录 compare-and-set，冲突时保留草稿并提示——方案 S5）。
// 数据由 hook `useScheduleTasks` 提供（面板只渲染与转发动作）。

import {
  CalendarClockIcon,
  RefreshCwIcon,
  Trash2Icon,
} from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  formatScheduleRelative,
  formatScheduleTimestamp,
  scheduleFrequencySummary,
  scheduleKindLabelKey,
  splitScheduleTasks,
} from "@/components/workspace/schedule-panel-shared";
import { useScheduleTasks } from "@/hooks/workspace/use-schedule-tasks";
import type {
  RuntimeScheduleTask,
  RuntimeScheduleTimingChange,
} from "@/types/runtime";
import { cn } from "@/lib/utils";

export type SchedulePanelProps = {
  sessionId: string;
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
  workspacePath?: string;
};

type DraftState = {
  title: string;
  prompt: string;
  time: string;
  timeZone: string;
};

function draftFromTask(task: RuntimeScheduleTask): DraftState {
  return {
    title: task.title,
    prompt: task.prompt,
    time: (task.time ?? "").slice(0, 8),
    timeZone: task.timeZone ?? "",
  };
}

/** 从记录派生时间替换负载（仅钟表规则可编辑时间；after/at/every 保持原 kind）。 */
function timingChangeFrom(
  task: RuntimeScheduleTask,
  draft: DraftState,
): RuntimeScheduleTimingChange | undefined {
  if (task.kind !== "daily" && task.kind !== "weekly") {
    return undefined;
  }
  if (!draft.time.trim() || !draft.timeZone.trim()) {
    return undefined;
  }
  if (task.kind === "daily") {
    return {
      kind: "daily",
      daily: { time: draft.time.trim(), time_zone: draft.timeZone.trim() },
    };
  }
  return {
    kind: "weekly",
    weekly: {
      time: draft.time.trim(),
      time_zone: draft.timeZone.trim(),
      weekdays: task.weekdays ?? [],
    },
  };
}

export function SchedulePanel({
  lastRuntimeEventType,
  runtimeEventCount,
}: SchedulePanelProps) {
  const { t } = useTranslation("workspace");
  const controller = useScheduleTasks({
    enabled: true,
    lastRuntimeEventType,
    runtimeEventCount,
  });
  const {
    deletingId,
    error,
    loading,
    refresh,
    removeTask,
    saveTask,
    savingId,
    tasks,
  } = controller;
  const [editingId, setEditingId] = useState("");
  const [draft, setDraft] = useState<DraftState | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [confirmingId, setConfirmingId] = useState("");

  const { active, inactive } = useMemo(
    () => splitScheduleTasks(tasks),
    [tasks],
  );

  const startEdit = useCallback((task: RuntimeScheduleTask) => {
    setEditingId(task.id);
    setDraft(draftFromTask(task));
    setNotice(null);
  }, []);

  const cancelEdit = useCallback(() => {
    setEditingId("");
    setDraft(null);
  }, []);

  const save = useCallback(
    async (task: RuntimeScheduleTask) => {
      if (!draft) {
        return;
      }
      const change = timingChangeFrom(task, draft);
      const outcome = await saveTask({
        id: task.id,
        expected: {
          id: task.id,
          kind: task.kind,
          title: task.title,
          prompt: task.prompt,
          scheduledAt: task.scheduledAt,
          afterSeconds: task.afterSeconds,
          everySeconds: task.everySeconds,
          time: task.time,
          timeZone: task.timeZone,
          weekdays: task.weekdays,
          expression: task.expression,
        },
        ...(draft.title.trim() !== task.title ? { title: draft.title.trim() } : {}),
        ...(draft.prompt.trim() !== task.prompt ? { prompt: draft.prompt.trim() } : {}),
        ...(change ? { change } : {}),
      });
      if (outcome.ok) {
        setNotice(t("panels.schedule.saved"));
        cancelEdit();
      } else {
        // schedule_conflict：保留草稿让用户重试（方案 S5）。
        setNotice(t("panels.schedule.saveFailed", { message: outcome.message }));
      }
    },
    [cancelEdit, draft, saveTask, t],
  );

  const remove = useCallback(
    async (task: RuntimeScheduleTask) => {
      setConfirmingId("");
      const outcome = await removeTask(task.id);
      setNotice(
        outcome.ok
          ? t("panels.schedule.deleted")
          : t("panels.schedule.deleteFailed", { message: outcome.message }),
      );
    },
    [removeTask, t],
  );

  const renderRow = (task: RuntimeScheduleTask, live: boolean) => {
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
  };

  return (
    <div className="flex h-full flex-col gap-3 overflow-y-auto p-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <CalendarClockIcon className="size-4" />
          <h2 className="text-sm font-semibold">
            {t("panels.schedule.title")}
          </h2>
          {active.length > 0 && (
              <Badge>{active.length}</Badge>
          )}
        </div>
        <Button
          aria-label={t("panels.schedule.refresh")}
          disabled={loading}
          onClick={refresh}
          size="icon"
          variant="ghost"
        >
          <RefreshCwIcon className={cn("size-4", loading && "animate-spin")} />
        </Button>
      </div>

      {notice && (
        <p className="rounded border border-border px-2 py-1 text-xs text-muted-foreground">
          {notice}
        </p>
      )}
      {error && (
        <p className="rounded border border-destructive/40 px-2 py-1 text-xs text-destructive">
          {error}
        </p>
      )}

      {tasks.length === 0 && !loading ? (
        <p className="text-sm text-muted-foreground">
          {t("panels.schedule.empty")}
        </p>
      ) : null}

      {active.length > 0 && (
        <section>
          <h3 className="mb-1 text-xs font-medium uppercase text-muted-foreground">
            {t("panels.schedule.activeSection")}
          </h3>
          <ul className="space-y-2">{active.map((task) => renderRow(task, true))}</ul>
        </section>
      )}
      {inactive.length > 0 && (
        <section>
          <h3 className="mb-1 text-xs font-medium uppercase text-muted-foreground">
            {t("panels.schedule.inactiveSection")}
          </h3>
          <ul className="space-y-2">{inactive.map((task) => renderRow(task, false))}</ul>
        </section>
      )}
    </div>
  );
}
