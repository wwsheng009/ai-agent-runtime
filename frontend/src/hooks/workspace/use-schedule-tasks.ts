// P3：自动化任务（Schedule）面板数据 hook。
//
// 刷新语义（与 use-background-jobs 同构）：
// 1. 打开面板时拉取一次 `/schedule/catalog`（跨会话全部任务）；
// 2. `schedule_*` 运行时事件到达后延迟一拍再刷新；
// 3. 有 active 任务时按固定间隔轮询（到点时间变化）；
// 4. 编辑/删除成功后立即刷新。
//
// 与 dsh 的 readRequest/readSettled 序号语义对应：本 hook 用 requestSeq
// 计数器保证"删除后重拉"晚于删除请求本身（不比较时钟）。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  createScheduleTask,
  deleteScheduleTask,
  fetchScheduleCatalog,
  updateScheduleTask,
} from "@/api/runtime/schedule";
import type {
  RuntimeScheduleCreateRequest,
  RuntimeScheduleTask,
  RuntimeScheduleUpdateRequest,
} from "@/types/runtime";

// 事件后延迟刷新：等后端把状态落库。
const runtimeEventRefreshDelayMs = 800;

// 有 active 任务时的兜底轮询间隔。
const activeRefreshIntervalMs = 15_000;

export type UseScheduleTasksOptions = {
  enabled: boolean;
  /** 可选：按会话过滤（面板级用；全局页不传）。 */
  sessionId?: string;
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
};

export type ScheduleDeleteOutcome =
  | { ok: true }
  | { ok: false; message: string };

export function useScheduleTasks({
  enabled,
  lastRuntimeEventType,
  runtimeEventCount,
  sessionId,
}: UseScheduleTasksOptions) {
  const [tasks, setTasks] = useState<RuntimeScheduleTask[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [refreshToken, setRefreshToken] = useState(0);
  const [eventRefreshKey, setEventRefreshKey] = useState("");
  const [deletingId, setDeletingId] = useState("");
  const [savingId, setSavingId] = useState("");
  const [creating, setCreating] = useState(false);
  const requestIdRef = useRef(0);
  const sid = sessionId?.trim() ?? "";

  const runtimeEventKey =
    lastRuntimeEventType && lastRuntimeEventType.startsWith("schedule")
      ? `${lastRuntimeEventType}:${runtimeEventCount ?? 0}`
      : "";

  const hasActiveTasks = useMemo(
    () => tasks.some((task) => task.status === "active"),
    [tasks],
  );

  const activeCount = useMemo(
    () => tasks.filter((task) => task.status === "active").length,
    [tasks],
  );

  useEffect(() => {
    if (!runtimeEventKey) {
      return;
    }
    const timer = setTimeout(() => {
      setEventRefreshKey(runtimeEventKey);
    }, runtimeEventRefreshDelayMs);
    return () => {
      clearTimeout(timer);
    };
  }, [runtimeEventKey]);

  useEffect(() => {
    if (!enabled || !hasActiveTasks) {
      return;
    }
    const timer = setInterval(() => {
      setRefreshToken((token) => token + 1);
    }, activeRefreshIntervalMs);
    return () => {
      clearInterval(timer);
    };
  }, [enabled, hasActiveTasks]);

  const refresh = useCallback(() => {
    setRefreshToken((token) => token + 1);
  }, []);

  useEffect(() => {
    if (!enabled) {
      return;
    }
    const requestId = requestIdRef.current + 1;
    requestIdRef.current = requestId;

    async function load() {
      setLoading(true);
      try {
        const response = await fetchScheduleCatalog();
        if (requestIdRef.current === requestId) {
          const all = response.tasks;
          setTasks(sid ? all.filter((task) => task.sessionId === sid) : all);
          setError(null);
        }
      } catch (caught) {
        if (requestIdRef.current === requestId) {
          // 失败保留最近已知数据（方案 S11：error 态不清空）。
          setError(caught instanceof Error ? caught.message : String(caught));
        }
      } finally {
        if (requestIdRef.current === requestId) {
          setLoading(false);
        }
      }
    }

    void load();
  }, [enabled, sid, refreshToken, eventRefreshKey]);

  /** 删除：硬删除（含历史）；成功后重拉目录。 */
  const removeTask = useCallback(
    async (id: string): Promise<ScheduleDeleteOutcome> => {
      const target = tasks.find((task) => task.id === id);
      const taskSession = target?.sessionId ?? sid;
      if (!taskSession) {
        return { ok: false, message: "session id unavailable" };
      }
      setDeletingId(id);
      try {
        const response = await deleteScheduleTask(taskSession, id);
        if (!response.deleted && response.code && response.code !== "schedule_not_found") {
          return { ok: false, message: response.code };
        }
        setRefreshToken((token) => token + 1);
        return { ok: true };
      } catch (caught) {
        return {
          ok: false,
          message: caught instanceof Error ? caught.message : String(caught),
        };
      } finally {
        setDeletingId("");
      }
    },
    [tasks, sid],
  );

  /** 保存：完整 expected 记录 compare-and-set（方案 S5）。 */
  const saveTask = useCallback(
    async (
      request: Omit<RuntimeScheduleUpdateRequest, "sessionId">,
    ): Promise<ScheduleDeleteOutcome> => {
      const target = tasks.find((task) => task.id === request.id);
      const taskSession = target?.sessionId ?? sid;
      if (!taskSession) {
        return { ok: false, message: "session id unavailable" };
      }
      setSavingId(request.id);
      try {
        const response = await updateScheduleTask({
          ...request,
          sessionId: taskSession,
        });
        if (!response.updated && response.code) {
          return {
            ok: false,
            message: response.message || response.code,
          };
        }
        setRefreshToken((token) => token + 1);
        return { ok: true };
      } catch (caught) {
        return {
          ok: false,
          message: caught instanceof Error ? caught.message : String(caught),
        };
      } finally {
        setSavingId("");
      }
    },
    [tasks, sid],
  );

  /** 人工创建：绑定指定会话（origin=manual）；成功后重拉目录。 */
  const createTask = useCallback(
    async (
      request: Omit<RuntimeScheduleCreateRequest, "sessionId"> & { sessionId: string },
    ): Promise<ScheduleDeleteOutcome> => {
      if (!request.sessionId.trim()) {
        return { ok: false, message: "session id unavailable" };
      }
      setCreating(true);
      try {
        await createScheduleTask(request);
        setRefreshToken((token) => token + 1);
        return { ok: true };
      } catch (caught) {
        return {
          ok: false,
          message: caught instanceof Error ? caught.message : String(caught),
        };
      } finally {
        setCreating(false);
      }
    },
    [],
  );

  return {
    tasks,
    loading,
    error,
    hasActiveTasks,
    activeCount,
    deletingId,
    savingId,
    creating,
    refresh,
    removeTask,
    saveTask,
    createTask,
  };
}

/** 面板与全局页共享的控制器（由 owner 创建一次）。 */
export type ScheduleTasksController = ReturnType<typeof useScheduleTasks>;
