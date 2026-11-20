// P3：自动化任务（Schedule）面板。
//
// 数据全部来自 `/api/runtime/schedule/*`（无假数据）。活动/未运行分区展示；
// 编辑通过独立对话框完成（完整 expected 记录 compare-and-set，冲突时保留草稿
// 并提示——方案 S5）；暂停/恢复、删除等动作在任务行内就地反馈。
// 数据由 hook `useScheduleTasks` 提供（面板只渲染与转发动作）。
// P0-2 机械拆分：子组件/表单纯函数已拆至 ./schedule-panel/*，仅搬迁不改语义。

import {
  CalendarClockIcon,
  ExternalLinkIcon,
  PlusIcon,
  RefreshCwIcon,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { fetchScheduleJobRuns } from "@/api/runtime/schedule";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { splitScheduleTasks } from "@/components/workspace/schedule-panel-shared";
import {
  useScheduleTasks,
  type ScheduleTasksController,
} from "@/hooks/workspace/use-schedule-tasks";
import { cn } from "@/lib/utils";
import type {
  RuntimeScheduleJobRun,
  RuntimeScheduleTask,
} from "@/types/runtime";

import {
  createRequestFrom,
  editFormFromTask,
  emptyCreateForm,
  scheduleErrorFields,
  validateCreateForm,
  updateRequestFrom,
} from "./form-state";
import type { CreateFormState, ScheduleFormErrors } from "./form-state";
import { ScheduleFormDialog } from "./form-dialog";
import { ScheduleTaskRow } from "./task-row";

const scheduleInputErrorKeys: Record<string, string> = {
  invalid_prompt: "panels.schedule.createErrorCode.invalid_prompt",
  invalid_selector: "panels.schedule.createErrorCode.invalid_selector",
  invalid_rule: "panels.schedule.createErrorCode.invalid_rule",
  invalid_time_zone: "panels.schedule.createErrorCode.invalid_time_zone",
  not_future: "panels.schedule.createErrorCode.not_future",
  time_out_of_range: "panels.schedule.createErrorCode.time_out_of_range",
  frequency_too_high: "panels.schedule.createErrorCode.frequency_too_high",
  subagent_session: "panels.schedule.createErrorCode.subagent_session",
  request_timeout: "panels.schedule.createErrorCode.request_timeout",
};

export type SchedulePanelProps = {
  sessionId: string;
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
  workspacePath?: string;
  /** 全局页可向列表和汇总视图共享同一份目录数据。 */
  controller?: ScheduleTasksController;
  /** 全局页可用：只展示符合当前筛选器的任务。 */
  taskFilter?: (task: RuntimeScheduleTask) => boolean;
  emptyMessage?: string;
  /** 从模板页发起一次预填创建。key 变化时打开创建对话框。 */
  createRequest?: {
    key: string;
    preset?: Partial<CreateFormState>;
  };
  onCreateRequestHandled?: (key: string) => void;
  /** Optional session link for hosts that can navigate the workspace. */
  onOpenSession?: (sessionId: string) => void;
  /** Optional rich-history entry point for hosts with a dedicated runs view. */
  onViewHistory?: (task: RuntimeScheduleTask) => void;
  /** Show the global automation catalog shortcut (right-rail host only). */
  showGlobalAutomationLink?: boolean;
};

export function SchedulePanel({
  sessionId,
  lastRuntimeEventType,
  runtimeEventCount,
  controller: suppliedController,
  taskFilter,
  emptyMessage,
  createRequest,
  onCreateRequestHandled,
  onOpenSession,
  onViewHistory,
  showGlobalAutomationLink = false,
}: SchedulePanelProps) {
  const { t } = useTranslation("workspace");
  const ownedController = useScheduleTasks({
    enabled: suppliedController === undefined,
    lastRuntimeEventType,
    runtimeEventCount,
    sessionId,
  });
  const controller = suppliedController ?? ownedController;
  const {
    controlTask,
    controllingAction,
    controllingId,
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
  const [notice, setNotice] = useState<string | null>(null);
  const [confirmingId, setConfirmingId] = useState("");
  const [pendingDelete, setPendingDelete] = useState<RuntimeScheduleTask | null>(
    null,
  );
  const [pendingDeleteCommitting, setPendingDeleteCommitting] = useState(false);
  const deleteTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const noticeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [createForm, setCreateForm] = useState<CreateFormState | null>(null);
  const [createError, setCreateError] = useState<string | null>(null);
  const [createFieldErrors, setCreateFieldErrors] =
    useState<ScheduleFormErrors>({});
  const [editingTask, setEditingTask] = useState<RuntimeScheduleTask | null>(
    null,
  );
  const [editForm, setEditForm] = useState<CreateFormState | null>(null);
  const [editError, setEditError] = useState<string | null>(null);
  const [editFieldErrors, setEditFieldErrors] = useState<ScheduleFormErrors>(
    {},
  );
  // J3：运行历史（按需拉取；展开即取一次，折叠不丢缓存）。
  const [expandedRunId, setExpandedRunId] = useState("");
  const [runsByTask, setRunsByTask] = useState<Record<string, RuntimeScheduleJobRun[]>>({});
  const [runsStatus, setRunsStatus] = useState<Record<string, "loading" | "error">>({});

  const outcomeMessage = useCallback(
    (outcome: { code?: string; message: string }) => {
      if (outcome.code === "session_required") {
        return t("panels.schedule.sessionRequired");
      }
      if (outcome.code === "task_required") {
        return t("panels.schedule.taskRequired");
      }
      if (outcome.code === "schedule_conflict") {
        return t("panels.schedule.conflict");
      }
      if (outcome.code === "schedule_not_found") {
        return t("panels.schedule.notFound");
      }
      if (outcome.code === "schedule_ended") {
        return t("panels.schedule.ended");
      }
      if (outcome.code === "request_failed") {
        return t("panels.schedule.requestFailed");
      }
      return t("panels.schedule.requestFailed");
    },
    [t],
  );

  useEffect(() => {
    if (!notice || pendingDelete) return;
    noticeTimerRef.current = setTimeout(() => setNotice(null), 4_000);
    return () => {
      if (noticeTimerRef.current) clearTimeout(noticeTimerRef.current);
      noticeTimerRef.current = null;
    };
  }, [notice, pendingDelete]);

  useEffect(
    () => () => {
      if (deleteTimerRef.current) clearTimeout(deleteTimerRef.current);
      if (noticeTimerRef.current) clearTimeout(noticeTimerRef.current);
    },
    [],
  );

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
        const task = tasks.find((candidate) => candidate.id === taskId);
        const ownerSessionId =
          task?.sessionId?.trim() || task?.createdBySessionId?.trim() || undefined;
        const response = await fetchScheduleJobRuns(ownerSessionId, taskId, 20);
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
    [expandedRunId, runsByTask, tasks],
  );

  const visibleTasks = useMemo(
    () =>
      pendingDelete
        ? tasks.filter(
            (task) => task.id !== pendingDelete.id && taskFilter?.(task) !== false,
          )
        : tasks.filter((task) => taskFilter?.(task) !== false),
    [pendingDelete, taskFilter, tasks],
  );
  const { active, paused, inactive, blocked, failed } = useMemo(
    () => splitScheduleTasks(visibleTasks),
    [visibleTasks],
  );

  const startEdit = useCallback(
    (task: RuntimeScheduleTask) => {
      setEditingTask(task);
      setEditForm(editFormFromTask(task, sessionId));
      setEditError(null);
      setEditFieldErrors({});
      setNotice(null);
    },
    [sessionId],
  );

  const closeEdit = useCallback(() => {
    if (editingTask && savingId === editingTask.id) {
      return;
    }
    setEditingTask(null);
    setEditForm(null);
    setEditError(null);
    setEditFieldErrors({});
  }, [editingTask, savingId]);

  const saveEdit = useCallback(async () => {
    if (!editingTask || !editForm) {
      return;
    }
    setEditError(null);
    const validation = validateCreateForm(editForm);
    setEditFieldErrors(validation);
    if (Object.keys(validation).length > 0) {
      setEditError(t("panels.schedule.createValidationFailed"));
      return;
    }
    const request = updateRequestFrom(editForm, editingTask);
    if (!request) {
      setEditError(
        t(
          editForm.targetKind === "session"
            ? "panels.schedule.editIncomplete"
            : "panels.schedule.editIncompleteJob",
        ),
      );
      return;
    }
    const outcome = await saveTask(request);
    if (outcome.ok) {
      setEditingTask(null);
      setEditForm(null);
      setEditError(null);
      setEditFieldErrors({});
      setNotice(t("panels.schedule.saved"));
    } else {
      // schedule_conflict：保留草稿让用户重试（方案 S5）。
      setEditFieldErrors(scheduleErrorFields(outcome.code, editForm));
      const errorKey = outcome.code
        ? scheduleInputErrorKeys[outcome.code]
        : undefined;
      setEditError(
        errorKey
          ? (t(errorKey as never) as string)
          : t("panels.schedule.saveFailed", { message: outcomeMessage(outcome) }),
      );
    }
  }, [editForm, editingTask, outcomeMessage, saveTask, t]);

  const remove = useCallback(
    (task: RuntimeScheduleTask) => {
      setConfirmingId("");
      setNotice(null);
      if (deleteTimerRef.current) clearTimeout(deleteTimerRef.current);
      if (pendingDelete && !pendingDeleteCommitting) {
        void removeTask(pendingDelete.id).then((outcome) => {
          if (!outcome.ok) {
            setNotice(
               t("panels.schedule.deleteFailed", {
                 message: outcomeMessage(outcome),
               }),
            );
          }
        });
      }
      setPendingDelete(task);
      setPendingDeleteCommitting(false);
      deleteTimerRef.current = setTimeout(() => {
        deleteTimerRef.current = null;
        setPendingDeleteCommitting(true);
        void removeTask(task.id).then((outcome) => {
          setPendingDelete(null);
          setPendingDeleteCommitting(false);
          setNotice(
            outcome.ok
              ? t("panels.schedule.deleted")
               : t("panels.schedule.deleteFailed", {
                   message: outcomeMessage(outcome),
                 }),
          );
        });
      }, 5_000);
    },
     [pendingDelete, pendingDeleteCommitting, removeTask, t, outcomeMessage],
  );

  const undoDelete = useCallback(() => {
    if (pendingDeleteCommitting) return;
    if (deleteTimerRef.current) clearTimeout(deleteTimerRef.current);
    deleteTimerRef.current = null;
    setPendingDelete(null);
    setNotice(null);
  }, [pendingDeleteCommitting]);

  const control = useCallback(
    async (task: RuntimeScheduleTask, action: "pause" | "resume") => {
      const outcome = await controlTask(task.id, action);
      setNotice(
        outcome.ok
          ? t(
              action === "pause"
                ? "panels.schedule.paused"
                : task.status === "inactive"
                  ? "panels.schedule.enabled"
                  : "panels.schedule.resumed",
            )
          : t("panels.schedule.controlFailed", {
              message: outcomeMessage(outcome),
            }),
      );
    },
    [controlTask, outcomeMessage, t],
  );

  const submitCreate = useCallback(async () => {
    if (!createForm) {
      return;
    }
    setCreateError(null);
    const validation = validateCreateForm(createForm);
    setCreateFieldErrors(validation);
    if (Object.keys(validation).length > 0) {
      setCreateError(t("panels.schedule.createValidationFailed"));
      return;
    }
    const request = createRequestFrom(createForm);
    if (!request) {
      setCreateError(
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
      setCreateError(null);
      setCreateFieldErrors({});
      setNotice(t("panels.schedule.created"));
    } else {
      const errorKey = outcome.code
        ? scheduleInputErrorKeys[outcome.code]
        : undefined;
      setCreateFieldErrors(scheduleErrorFields(outcome.code, createForm));
      setCreateError(
        errorKey
          ? (t(errorKey as never) as string)
          : t("panels.schedule.createFailedGeneric"),
      );
    }
  }, [createForm, createTask, t]);

  const openCreate = useCallback(() => {
    setCreateError(null);
    setCreateFieldErrors({});
    setCreateForm(emptyCreateForm(sessionId));
  }, [sessionId]);

  useEffect(() => {
    if (!createRequest?.key) {
      return;
    }
    setCreateError(null);
    setCreateFieldErrors({});
    setCreateForm({
      ...emptyCreateForm(sessionId),
      ...createRequest.preset,
    });
    onCreateRequestHandled?.(createRequest.key);
  }, [
    createRequest?.key,
    createRequest?.preset,
    onCreateRequestHandled,
    sessionId,
  ]);

  const closeCreate = useCallback(() => {
    if (creating) {
      return;
    }
    setCreateForm(null);
    setCreateError(null);
    setCreateFieldErrors({});
  }, [creating]);

  const clearCreateError = useCallback(() => {
    setCreateError(null);
    setCreateFieldErrors({});
  }, []);

  const handleCreateSubmit = useCallback(() => {
    void submitCreate();
  }, [submitCreate]);

  const clearEditError = useCallback(() => {
    setEditError(null);
    setEditFieldErrors({});
  }, []);

  const handleEditSubmit = useCallback(() => {
    void saveEdit();
  }, [saveEdit]);

  const rowProps = {
    confirmingId,
    control,
    controllingAction,
    controllingId,
    deleteLocked: pendingDeleteCommitting,
    deletingId,
    editingId: editingTask?.id ?? "",
    expandedRunId,
    remove,
    runsByTask,
    runsStatus,
    setConfirmingId,
    startEdit,
    toggleRuns,
    onOpenSession,
    onViewHistory,
  };

  return (
    <div className="flex h-full flex-col gap-3 overflow-y-auto p-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <CalendarClockIcon className="size-4" />
          <h2 className="text-sm font-semibold">
            {t("panels.schedule.title")}
          </h2>
          {visibleTasks.length > 0 && (
            <Badge>{visibleTasks.length}</Badge>
          )}
        </div>
        <div className="flex items-center gap-1">
          <Button
            aria-expanded={createForm !== null}
            aria-haspopup="dialog"
            onClick={openCreate}
            size="sm"
            variant="secondary"
          >
            <PlusIcon aria-hidden="true" className="size-4" />
            {t("panels.schedule.create")}
          </Button>
          <Button
            aria-label={t("panels.schedule.refresh")}
            disabled={loading}
            onClick={refresh}
            size="icon"
            variant="ghost"
          >
            <RefreshCwIcon
              aria-hidden="true"
              className={cn("size-4", loading && "animate-spin")}
            />
          </Button>
          {showGlobalAutomationLink ? (
            <a
              aria-label={t("panels.schedule.openGlobalAutomation")}
              className={cn(buttonVariants({ variant: "ghost", size: "icon" }))}
              data-testid="schedule-global-automation"
              href="/automation"
              title={t("panels.schedule.openGlobalAutomation")}
            >
              <ExternalLinkIcon aria-hidden="true" className="size-4" />
            </a>
          ) : null}
        </div>
      </div>

      {notice && (
        <p
          aria-live="polite"
          className="rounded border border-border px-2 py-1 text-xs text-muted-foreground"
          role="status"
        >
          {notice}
        </p>
      )}
      {pendingDelete ? (
        <p
          aria-live="polite"
          className="flex items-center justify-between gap-2 rounded border border-border px-2 py-1 text-xs text-muted-foreground"
          role="status"
        >
          <span>
            {t(
              pendingDeleteCommitting
                ? "panels.schedule.deleting"
                : "panels.schedule.deleteUndoPending",
              pendingDeleteCommitting ? undefined : { seconds: "5" },
            )}
          </span>
          {!pendingDeleteCommitting ? (
            <Button onClick={undoDelete} size="sm" variant="ghost">
              {t("panels.schedule.undoDelete")}
            </Button>
          ) : null}
        </p>
      ) : null}
      {error && (
        <p
          className="rounded border border-destructive/40 px-2 py-1 text-xs text-destructive"
          role="alert"
        >
          {outcomeMessage({ code: error, message: "" })}
        </p>
      )}

      {visibleTasks.length === 0 && !loading ? (
        <p className="text-sm text-muted-foreground">
          {emptyMessage ?? t("panels.schedule.empty")}
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
      {paused.length > 0 && (
        <section>
          <h3 className="mb-1 text-xs font-medium uppercase text-muted-foreground">
            {t("panels.schedule.pausedSection")}
          </h3>
          <ul className="space-y-2">
            {paused.map((task) => (
              <ScheduleTaskRow key={task.id} live={false} task={task} {...rowProps} />
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
      {blocked.length > 0 && (
        <section>
          <h3 className="mb-1 text-xs font-medium uppercase text-destructive">
            {t("panels.schedule.blockedSection")}
          </h3>
          <ul className="space-y-2">
            {blocked.map((task) => (
              <ScheduleTaskRow key={task.id} live={false} task={task} {...rowProps} />
            ))}
          </ul>
        </section>
      )}
      {failed.length > 0 && (
        <section>
          <h3 className="mb-1 text-xs font-medium uppercase text-destructive">
            {t("panels.schedule.failedSection")}
          </h3>
          <ul className="space-y-2">
            {failed.map((task) => (
              <ScheduleTaskRow key={task.id} live={false} task={task} {...rowProps} />
            ))}
          </ul>
        </section>
      )}

      {createForm ? (
        <ScheduleFormDialog
          busy={creating}
          createForm={createForm}
          error={createError}
          fieldErrors={createFieldErrors}
          mode="create"
          onClearError={clearCreateError}
          onClose={closeCreate}
          onSubmit={handleCreateSubmit}
          setCreateForm={setCreateForm}
        />
      ) : null}

      {editForm && editingTask ? (
        <ScheduleFormDialog
          busy={savingId === editingTask.id}
          createForm={editForm}
          error={editError}
          fieldErrors={editFieldErrors}
          mode="edit"
          onClearError={clearEditError}
          onClose={closeEdit}
          onSubmit={handleEditSubmit}
          setCreateForm={setEditForm}
        />
      ) : null}
    </div>
  );
}
