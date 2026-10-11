// P3：自动化任务（Schedule）面板。
//
// 数据全部来自 `/api/runtime/schedule/*`（无假数据）。活动/未运行分区展示；
// 行内动作：删除（确认）与编辑（名称/指令/时间的草稿保存，完整 expected
// 记录 compare-and-set，冲突时保留草稿并提示——方案 S5）。
// 数据由 hook `useScheduleTasks` 提供（面板只渲染与转发动作）。

import {
  CalendarClockIcon,
  PlusIcon,
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
  RuntimeScheduleCreateRequest,
  RuntimeScheduleKind,
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

/** 人工创建表单的 kind 选项：覆盖全部六种规则（after 用分钟数表达）。 */
const createKinds: RuntimeScheduleKind[] = [
  "after",
  "at",
  "every",
  "daily",
  "weekly",
  "cron",
];

type CreateFormState = {
  sessionId: string;
  title: string;
  prompt: string;
  kind: RuntimeScheduleKind;
  /** after 分钟数 / every 分钟数 / at 本地时间输入（转 RFC3339）。 */
  amount: string;
  time: string;
  timeZone: string;
  weekdays: number[];
  cron: string;
};

function emptyCreateForm(): CreateFormState {
  return {
    sessionId: "",
    title: "",
    prompt: "",
    kind: "after",
    amount: "30",
    time: "",
    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
    weekdays: [],
    cron: "",
  };
}

/** 由表单状态派生创建负载（恰好一个选择器；后端服务层做最终校验）。 */
function createRequestFrom(
  form: CreateFormState,
): RuntimeScheduleCreateRequest | null {
  const sessionId = form.sessionId.trim();
  const title = form.title.trim();
  const prompt = form.prompt.trim();
  if (!sessionId || !title || !prompt) {
    return null;
  }
  const base = { sessionId, title, prompt };
  switch (form.kind) {
    case "after": {
      const minutes = Number(form.amount);
      if (!Number.isFinite(minutes) || minutes <= 0) {
        return null;
      }
      return {
        ...base,
        after_seconds: Math.round(minutes * 60),
      };
    }
    case "every": {
      const minutes = Number(form.amount);
      if (!Number.isFinite(minutes) || minutes < 1) {
        return null;
      }
      return {
        ...base,
        every_seconds: Math.round(minutes * 60),
      };
    }
    case "at": {
      // 本地时间输入（datetime-local 语义）转带偏移的 RFC3339。
      const parsed = new Date(form.time);
      if (Number.isNaN(parsed.getTime())) {
        return null;
      }
      return { ...base, at: parsed.toISOString() };
    }
    case "daily": {
      if (!form.time.trim() || !form.timeZone.trim()) {
        return null;
      }
      return {
        ...base,
        daily: { time: form.time.trim(), time_zone: form.timeZone.trim() },
      };
    }
    case "weekly": {
      if (!form.time.trim() || !form.timeZone.trim() || form.weekdays.length === 0) {
        return null;
      }
      return {
        ...base,
        weekly: {
          time: form.time.trim(),
          time_zone: form.timeZone.trim(),
          weekdays: [...form.weekdays].sort((a, b) => a - b),
        },
      };
    }
    case "cron": {
      if (!form.cron.trim() || !form.timeZone.trim()) {
        return null;
      }
      return {
        ...base,
        cron: { expression: form.cron.trim(), time_zone: form.timeZone.trim() },
      };
    }
    default:
      return null;
  }
}

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
    createTask,
    creating,
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
  const [createForm, setCreateForm] = useState<CreateFormState | null>(null);

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

  const submitCreate = useCallback(async () => {
    if (!createForm) {
      return;
    }
    const request = createRequestFrom(createForm);
    if (!request) {
      setNotice(t("panels.schedule.createIncomplete"));
      return;
    }
    const outcome = await createTask(request);
    if (outcome.ok) {
      setCreateForm(null);
      setNotice(t("panels.schedule.created"));
    } else {
      // 后端校验错误（invalid_selector/not_future 等）保留表单供修正。
      setNotice(t("panels.schedule.createFailed", { message: outcome.message }));
    }
  }, [createForm, createTask, t]);

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
        <div className="flex items-center gap-1">
          <Button
            onClick={() =>
              setCreateForm((form) =>
                form ? null : emptyCreateForm(),
              )
            }
            size="sm"
            variant={createForm ? "ghost" : "secondary"}
          >
            <PlusIcon className="size-4" />
            {t("panels.schedule.create")}
          </Button>
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
      </div>

      {createForm ? (
        <section className="rounded-panel border border-border p-3">
          <h3 className="mb-2 text-xs font-semibold uppercase text-muted-foreground">
            {t("panels.schedule.createTitle")}
          </h3>
          <div className="grid gap-2 sm:grid-cols-2">
            <label className="grid gap-1 text-xs">
              <span className="text-muted-foreground">{t("panels.schedule.sessionLabel")}</span>
              <input
                className="rounded border border-border bg-transparent px-2 py-1"
                onChange={(event) =>
                  setCreateForm({ ...createForm, sessionId: event.target.value })
                }
                placeholder={t("panels.schedule.sessionPlaceholder")}
                value={createForm.sessionId}
              />
            </label>
            <label className="grid gap-1 text-xs">
              <span className="text-muted-foreground">{t("panels.schedule.titleLabel")}</span>
              <input
                className="rounded border border-border bg-transparent px-2 py-1"
                onChange={(event) =>
                  setCreateForm({ ...createForm, title: event.target.value })
                }
                value={createForm.title}
              />
            </label>
          </div>
          <label className="mt-2 grid gap-1 text-xs">
            <span className="text-muted-foreground">{t("panels.schedule.promptLabel")}</span>
            <textarea
              className="rounded border border-border bg-transparent px-2 py-1 text-xs"
              onChange={(event) =>
                setCreateForm({ ...createForm, prompt: event.target.value })
              }
              rows={2}
              value={createForm.prompt}
            />
          </label>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <label className="grid gap-1 text-xs">
              <span className="text-muted-foreground">{t("panels.schedule.kindLabel")}</span>
              <select
                className="rounded border border-border bg-transparent px-2 py-1"
                onChange={(event) =>
                  setCreateForm({
                    ...createForm,
                    kind: event.target.value as RuntimeScheduleKind,
                  })
                }
                value={createForm.kind}
              >
                {createKinds.map((kind) => (
                  <option key={kind} value={kind}>
                    {t(scheduleKindLabelKey(kind))}
                  </option>
                ))}
              </select>
            </label>
            {(createForm.kind === "after" || createForm.kind === "every") && (
              <label className="grid gap-1 text-xs">
                <span className="text-muted-foreground">
                  {t(
                    createForm.kind === "after"
                      ? "panels.schedule.minutesAfter"
                      : "panels.schedule.minutesEvery",
                  )}
                </span>
                <input
                  className="w-24 rounded border border-border bg-transparent px-2 py-1"
                  inputMode="numeric"
                  onChange={(event) =>
                    setCreateForm({ ...createForm, amount: event.target.value })
                  }
                  value={createForm.amount}
                />
              </label>
            )}
            {createForm.kind === "at" && (
              <label className="grid gap-1 text-xs">
                <span className="text-muted-foreground">{t("panels.schedule.atLabel")}</span>
                <input
                  className="rounded border border-border bg-transparent px-2 py-1"
                  onChange={(event) =>
                    setCreateForm({ ...createForm, time: event.target.value })
                  }
                  type="datetime-local"
                  value={createForm.time}
                />
              </label>
            )}
            {(createForm.kind === "daily" || createForm.kind === "weekly" || createForm.kind === "cron") && (
              <>
                <label className="grid gap-1 text-xs">
                  <span className="text-muted-foreground">{t("panels.schedule.timeLabel")}</span>
                  <input
                    className="w-28 rounded border border-border bg-transparent px-2 py-1"
                    onChange={(event) =>
                      setCreateForm({ ...createForm, time: event.target.value })
                    }
                    placeholder={t("panels.schedule.timePlaceholder")}
                    value={createForm.time}
                  />
                </label>
                <label className="grid gap-1 text-xs">
                  <span className="text-muted-foreground">{t("panels.schedule.zoneLabel")}</span>
                  <input
                    className="rounded border border-border bg-transparent px-2 py-1"
                    onChange={(event) =>
                      setCreateForm({ ...createForm, timeZone: event.target.value })
                    }
                    placeholder={t("panels.schedule.zonePlaceholder")}
                    value={createForm.timeZone}
                  />
                </label>
              </>
            )}
            {createForm.kind === "weekly" && (
              <div className="flex flex-wrap items-center gap-1">
                {([1, 2, 3, 4, 5, 6, 7] as const).map((day) => (
                  <button
                    aria-pressed={createForm.weekdays.includes(day)}
                    className={cn(
                      "rounded border border-border px-1.5 py-0.5 text-xs",
                      createForm.weekdays.includes(day) && "border-accent-primary-border text-accent-primary",
                    )}
                    key={day}
                    onClick={() =>
                      setCreateForm((form) =>
                        form
                          ? {
                              ...form,
                              weekdays: form.weekdays.includes(day)
                                ? form.weekdays.filter((d) => d !== day)
                                : [...form.weekdays, day],
                            }
                          : form,
                      )
                    }
                    type="button"
                  >
                    {t(`panels.schedule.weekday.${day}`)}
                  </button>
                ))}
              </div>
            )}
            {createForm.kind === "cron" && (
              <label className="grid gap-1 text-xs">
                <span className="text-muted-foreground">{t("panels.schedule.cronLabel")}</span>
                <input
                  className="rounded border border-border bg-transparent px-2 py-1"
                  onChange={(event) =>
                    setCreateForm({ ...createForm, cron: event.target.value })
                  }
                  placeholder="*/5 * * * *"
                  value={createForm.cron}
                />
              </label>
            )}
          </div>
          <div className="mt-3 flex items-center gap-2">
            <Button disabled={creating} onClick={() => void submitCreate()} size="sm">
              {t("panels.schedule.createSubmit")}
            </Button>
            <Button
              onClick={() => setCreateForm(null)}
              size="sm"
              variant="ghost"
            >
              {t("panels.schedule.cancel")}
            </Button>
          </div>
        </section>
      ) : null}

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
