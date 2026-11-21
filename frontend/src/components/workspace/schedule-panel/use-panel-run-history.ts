// Schedule 面板的行内运行历史状态：展开、按需拉取与缓存（折叠不丢缓存）。

import { useCallback, useState } from "react";

import { fetchScheduleJobRuns } from "@/api/runtime/schedule";
import type { RuntimeScheduleJobRun, RuntimeScheduleTask } from "@/types/runtime";

export function useSchedulePanelRunHistory(tasks: RuntimeScheduleTask[]) {
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

  return {
    expandedRunId,
    runsByTask,
    runsStatus,
    toggleRuns,
  };
}