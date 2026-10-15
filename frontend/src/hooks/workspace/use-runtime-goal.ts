/**
 * 会话目标（goal）权威快照 + 管理动作。
 *
 * 背景：指示条此前只有 SSE 派生投影（`lib/session-goal`），因为后端未暴露 REST；
 * 2026-10-15 起 `GET/POST /api/runtime/sessions/{id}/goal` 已就绪（与 CLI `/goal`
 * 同语义），本 hook 负责：
 *
 * 1. 进入会话时拉取权威快照并写入 store（`setAuthoritativeSessionGoal`）；
 * 2. 暴露 `run(action)` 执行 set/pause/resume/complete/clear/budget，写回新快照；
 * 3. 如实暴露 `applied=false` 的服务端守卫结果（如 complete 终态、预算未提高的
 *    budget_limited）——不得当成成功。
 *
 * 并发/切换保护：请求返回后只在 sessionId 未变化时写回，避免慢响应覆盖新会话。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import {
  fetchSessionGoal,
  updateSessionGoal,
  type UpdateSessionGoalRequest,
} from "@/api/runtime/goal";
import { RuntimeApiError, readErrorEnvelope } from "@/api/runtime/shared";
import {
  setAuthoritativeSessionGoal,
  useSessionGoal,
  useSessionGoalAuthoritative,
} from "@/lib/session-goal/store";

export type SessionGoalRunResult = {
  /** 服务端是否采纳（false 时 `message` 给出守卫原因）。 */
  applied: boolean;
  message: string;
};

export type UseRuntimeSessionGoalResult = {
  goal: ReturnType<typeof useSessionGoal>;
  /** 当前展示是否来自后端权威快照（false = SSE 派生投影）。 */
  authoritative: boolean;
  pending: boolean;
  /** 最近一次请求失败（人读文案）。 */
  error: string | null;
  /** 最近一次服务端返回的 message（含 applied=false 的守卫原因）。 */
  notice: string | null;
  refresh: () => Promise<void>;
  /** 执行动作；失败返回 null 并把原因写入 `error`。 */
  run: (request: UpdateSessionGoalRequest) => Promise<SessionGoalRunResult | null>;
};

function describeGoalError(error: unknown): string {
  if (error instanceof RuntimeApiError) {
    const { message } = readErrorEnvelope(error.payload);
    if (message) {
      return message;
    }
    return `HTTP ${error.status}`;
  }
  if (error instanceof Error && error.message.trim()) {
    return error.message.trim();
  }
  return String(error);
}

export function useRuntimeSessionGoal(
  sessionId?: string,
): UseRuntimeSessionGoalResult {
  const goal = useSessionGoal(sessionId);
  const authoritative = useSessionGoalAuthoritative(sessionId);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const currentSession = useRef(sessionId);
  currentSession.current = sessionId;

  const refresh = useCallback(async () => {
    const target = sessionId;
    if (!target) {
      return;
    }
    try {
      const observation = await fetchSessionGoal(target);
      if (currentSession.current !== target) {
        return;
      }
      setAuthoritativeSessionGoal(target, observation);
      setError(null);
    } catch (fetchError) {
      if (currentSession.current !== target) {
        return;
      }
      setError(describeGoalError(fetchError));
    }
  }, [sessionId]);

  useEffect(() => {
    setError(null);
    setNotice(null);
    void refresh();
  }, [refresh]);

  const run = useCallback(
    async (request: UpdateSessionGoalRequest): Promise<SessionGoalRunResult | null> => {
      const target = sessionId;
      if (!target) {
        return null;
      }
      setPending(true);
      setError(null);
      try {
        const result = await updateSessionGoal(target, request);
        if (currentSession.current !== target) {
          return null;
        }
        setAuthoritativeSessionGoal(target, result.observation);
        setNotice(result.message || null);
        return { applied: result.applied, message: result.message };
      } catch (runError) {
        if (currentSession.current !== target) {
          return null;
        }
        setError(describeGoalError(runError));
        return null;
      } finally {
        if (currentSession.current === target) {
          setPending(false);
        }
      }
    },
    [sessionId],
  );

  return { goal, authoritative, pending, error, notice, refresh, run };
}
