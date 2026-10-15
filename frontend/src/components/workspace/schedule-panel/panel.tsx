// P3：自动化任务（Schedule）面板。
//
// 数据全部来自 `/api/runtime/schedule/*`（无假数据）。活动/未运行分区展示；
// 行内动作：删除（确认）与编辑（名称/指令/时间的草稿保存，完整 expected
// 记录 compare-and-set，冲突时保留草稿并提示——方案 S5）。
// 数据由 hook `useScheduleTasks` 提供（面板只渲染与转发动作）。
// P0-2 机械拆分：子组件/表单纯函数已拆至 ./schedule-panel/*，仅搬迁不改语义。

import { CalendarClockIcon, PlusIcon, RefreshCwIcon } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { fetchScheduleJobRuns } from "@/api/runtime/schedule";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { splitScheduleTasks } from "@/components/workspace/schedule-panel-shared";
import { useScheduleTasks } from "@/hooks/workspace/use-schedule-tasks";
import { cn } from "@/lib/utils";
import type {
  RuntimeScheduleJobRun,
  RuntimeScheduleTask,
} from "@/types/runtime";

import { ScheduleCreateForm } from "./create-form";
import {
  createRequestFrom,
  draftFromTask,
  emptyCreateForm,
  timingChangeFrom,
} from "./form-state";
import type { CreateFormState, DraftState } from "./form-state";
import { ScheduleTaskRow } from "./task-row";

export type SchedulePanelProps = {
  sessionId: string;
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
  workspacePath?: string;
};

export function SchedulePanel({
  sessionId,
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
  // J3：运行历史（按需拉取；展开即取一次，折叠不丢缓存）。
  const [expandedRunId, setExpandedRunId] = useState("");
  const [runsByTask, setRunsByTask] = useState<Record<string, RuntimeScheduleJobRun[]>>({});
  const [runsStatus, setRunsStatus] = useState<Record<string, "loading" | "error">>({});

  const toggleRuns = useCallback(
    async (taskId: string) => {
      if (expandedRunId === taskId) {
        setExpandedRunId("");
        return;
      }
      setExpandedRunId(taskId);
      if (runsByTask[taskId]) {
        return;
      }
      setRunsStatus((prev) => ({ ...prev, [taskId]: "loading" }));
      try {
        const response = await fetchScheduleJobRuns(sessionId, taskId, 20);
        setRunsByTask((prev) => ({ ...prev, [taskId]: response.runs }));
        setRunsStatus((prev) => {
          const next = { ...prev };
          delete next[taskId];
          return next;
        });
      } catch {
        setRunsStatus((prev) => ({ ...prev, [taskId]: "error" }));
      }
    },
    [expandedRunId, runsByTask, sessionId],
  );

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
      setNotice(
        t(
          createForm.targetKind === "session"
            ? "panels.schedule.createIncomplete"
            : "panels.schedule.createIncompleteJob",
        ),
      );
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

  const rowProps = {
    cancelEdit,
    confirmingId,
    deletingId,
    draft,
    editingId,
    expandedRunId,
    remove,
    runsByTask,
    runsStatus,
    save,
    savingId,
    setConfirmingId,
    setDraft,
    startEdit,
    toggleRuns,
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
        <ScheduleCreateForm
          createForm={createForm}
          creating={creating}
          setCreateForm={setCreateForm}
          submitCreate={submitCreate}
        />
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
          <ul className="space-y-2">
            {active.map((task) => (
              <ScheduleTaskRow key={task.id} live={true} task={task} {...rowProps} />
            ))}
          </ul>
        </section>
      )}
      {inactive.length > 0 && (
        <section>
          <h3 className="mb-1 text-xs font-medium uppercase text-muted-foreground">
            {t("panels.schedule.inactiveSection")}
          </h3>
          <ul className="space-y-2">
            {inactive.map((task) => (
              <ScheduleTaskRow key={task.id} live={false} task={task} {...rowProps} />
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}
