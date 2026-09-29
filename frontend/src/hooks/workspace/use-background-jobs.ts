// P2-1A：后台任务（Jobs）面板数据 hook。
//
// 刷新语义（与 use-session-usage 同构，事件为代理信号 + 兜底轮询）：
// 1. 打开面板时按会话拉取一次 `/background/jobs?session_id=...`；
// 2. job_* 运行时事件到达后延迟一拍再刷新（事件先于落库）；
// 3. 存在 live 任务时按固定间隔轮询，终态任务不轮询；
// 4. 任务动作（暂停/恢复/取消/放弃/重排队）成功后立即刷新。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

// 既有 list/cancel 沿用 runtime-api barrel；P3 新动作端点直接取自 jobs REST 客户端，
// 避免扩大 runtime-api barrel 的改动面。
import {
  abandonRuntimeJob,
  pauseRuntimeJob,
  requeueRuntimeJob,
  resumeRuntimeJob,
} from "@/api/runtime/jobs";
import {
  buildJobsReloadKey,
  isLiveJobStatus,
  type JobAction,
  type JobActionPending,
} from "@/components/workspace/jobs-panel-shared";
import { cancelRuntimeJob, listRuntimeJobs } from "@/lib/runtime-api";
import type { RuntimeJob } from "@/types/runtime";

// 事件后延迟刷新：等后端把状态落库，避免读到旧值。
const runtimeEventRefreshDelayMs = 800;

// live 任务兜底轮询间隔。
const liveRefreshIntervalMs = 5_000;

export type UseBackgroundJobsOptions = {
  enabled: boolean;
  sessionId: string;
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
};

export function useBackgroundJobs({
  enabled,
  lastRuntimeEventType,
  runtimeEventCount,
  sessionId,
}: UseBackgroundJobsOptions) {
  const [jobs, setJobs] = useState<RuntimeJob[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [refreshToken, setRefreshToken] = useState(0);
  const [eventRefreshKey, setEventRefreshKey] = useState("");
  const [pendingAction, setPendingAction] = useState<JobActionPending>(null);
  const requestIdRef = useRef(0);
  const sid = sessionId?.trim() ?? "";
  const runtimeEventKey = buildJobsReloadKey(
    lastRuntimeEventType,
    runtimeEventCount,
  );

  const hasLiveJobs = useMemo(
    () => jobs.some((job) => isLiveJobStatus(job.status)),
    [jobs],
  );

  // 常驻状态条与弹层共用同一份数据（单一事实源），计数与 live 语义一致
  // （pending/running/paused；paused 仍可能被恢复，继续纳入轮询与计数）。
  const liveCount = useMemo(
    () => jobs.filter((job) => isLiveJobStatus(job.status)).length,
    [jobs],
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
    if (!enabled || !sid || !hasLiveJobs) {
      return;
    }

    const timer = setInterval(() => {
      setRefreshToken((token) => token + 1);
    }, liveRefreshIntervalMs);
    return () => {
      clearInterval(timer);
    };
  }, [enabled, hasLiveJobs, sid]);

  const refresh = useCallback(() => {
    setRefreshToken((token) => token + 1);
  }, []);

  useEffect(() => {
    if (!enabled || !sid) {
      return;
    }

    const requestId = requestIdRef.current + 1;
    requestIdRef.current = requestId;

    async function load() {
      setLoading(true);
      try {
        const response = await listRuntimeJobs({
          sessionId: sid,
          limit: 50,
        });
        if (requestIdRef.current === requestId) {
          setJobs(response.jobs);
          setError(null);
        }
      } catch (caught) {
        if (requestIdRef.current === requestId) {
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

  /** 任务动作统一入口：成功后立刻刷新列表；失败把原因暴露给面板（不清空已有数据）。 */
  const runAction = useCallback(
    async (jobId: string, action: JobAction): Promise<boolean> => {
      const target = jobId.trim();
      if (!target) {
        return false;
      }
      setPendingAction({ jobId: target, action });
      try {
        switch (action) {
          case "pause":
            await pauseRuntimeJob(target);
            break;
          case "resume":
            await resumeRuntimeJob(target);
            break;
          case "abandon":
            await abandonRuntimeJob(target);
            break;
          case "requeue":
            await requeueRuntimeJob(target);
            break;
          case "cancel":
            await cancelRuntimeJob(target);
            break;
        }
        setRefreshToken((token) => token + 1);
        return true;
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : String(caught));
        return false;
      } finally {
        setPendingAction(null);
      }
    },
    [],
  );

  // P2-9 兼容入口：既有调用方（顶栏/旧测试）仍可用 cancel + cancellingId；
  // 面板统一走 runAction / pendingAction，两者共享同一份在途状态。
  const cancel = useCallback(
    (jobId: string) => runAction(jobId, "cancel"),
    [runAction],
  );
  const cancellingId =
    pendingAction && pendingAction.action === "cancel" ? pendingAction.jobId : "";

  return {
    cancel,
    cancellingId,
    error,
    hasLiveJobs,
    jobs,
    liveCount,
    loading,
    pendingAction,
    refresh,
    runAction,
  };
}

/** 弹层与顶栏状态条共享的控制器（由 shell owner 创建一次）。 */
export type BackgroundJobsController = ReturnType<typeof useBackgroundJobs>;
