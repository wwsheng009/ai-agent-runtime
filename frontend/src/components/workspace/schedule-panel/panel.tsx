// P3：自动化任务（Schedule）面板。
//
// 数据全部来自 `/api/runtime/schedule/*`（无假数据）。活动/未运行分区展示；
// 编辑通过独立对话框完成（完整 expected 记录 compare-and-set，冲突时保留草稿
// 并提示——方案 S5）；暂停/恢复、删除等动作在任务行内就地反馈。
// 数据由 hook `useScheduleTasks` 提供（面板只渲染与转发动作）。
// P0-2 机械拆分：子组件/表单纯函数已拆至 ./schedule-panel/*，仅搬迁不改语义。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { splitScheduleTasks } from "@/components/workspace/schedule-panel-shared";
import {
  useScheduleTasks,
  type ScheduleTasksController,
} from "@/hooks/workspace/use-schedule-tasks";
import type { RuntimeScheduleTask } from "@/types/runtime";

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
import { SchedulePanelHeader } from "./panel-header";
import { SchedulePanelNotices } from "./panel-notices";
import { ScheduleTaskSections } from "./panel-task-sections";
import { useSchedulePanelRunHistory } from "./use-panel-run-history";

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
  const { expandedRunId, runsByTask, runsStatus, toggleRuns } =
    useSchedulePanelRunHistory(tasks);

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
      <SchedulePanelHeader
        createOpen={createForm !== null}
        loading={loading}
        onCreate={openCreate}
        onRefresh={refresh}
        showGlobalAutomationLink={showGlobalAutomationLink}
        taskCount={visibleTasks.length}
      />

      <SchedulePanelNotices
        errorMessage={
          error ? outcomeMessage({ code: error, message: "" }) : null
        }
        notice={notice}
        onUndoDelete={undoDelete}
        pendingDelete={pendingDelete}
        pendingDeleteCommitting={pendingDeleteCommitting}
      />

      {visibleTasks.length === 0 && !loading ? (
        <p className="text-sm text-muted-foreground">
          {emptyMessage ?? t("panels.schedule.empty")}
        </p>
      ) : null}

      <ScheduleTaskSections
        groups={[
          {
            live: true,
            tasks: active,
            title: t("panels.schedule.activeSection"),
          },
          {
            live: false,
            tasks: paused,
            title: t("panels.schedule.pausedSection"),
          },
          {
            live: false,
            tasks: inactive,
            title: t("panels.schedule.inactiveSection"),
          },
          {
            destructive: true,
            live: false,
            tasks: blocked,
            title: t("panels.schedule.blockedSection"),
          },
          {
            destructive: true,
            live: false,
            tasks: failed,
            title: t("panels.schedule.failedSection"),
          },
        ]}
        rowProps={rowProps}
      />

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
