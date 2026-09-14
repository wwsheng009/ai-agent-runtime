// P2-1A：会话统计（GET /api/runtime/sessions/stats）状态机。
//
// 刷新语义：
//   * userId 变化或 refresh() 调用即重新拉取；重跑与卸载都中止在途请求
//     （AbortController + 请求序号，旧响应一律丢弃）；
//   * 主动取消（AbortError）不落错误态，超时与真实失败照常呈现；
//   * HTTP 404/405/501/503 归「统计不可用」（unavailable=true），与真实失败分列，
//     UI 如实提示而不是伪造 0 计数；
//   * 失败时保留上一次成功数据（若有），错误标记不清除，由 UI 决定呈现口径。

import { useCallback, useEffect, useRef, useState } from "react";

import {
  fetchRuntimeSessionStats,
  isSessionStatsUnavailable,
} from "@/api/runtime/session-stats";
import type { RuntimeSessionStats } from "@/types/runtime";

export type SessionStatsStatus = "idle" | "loading" | "ready" | "error";

export type SessionStatsSnapshot = {
  status: SessionStatsStatus;
  stats: RuntimeSessionStats | null;
  error: unknown;
  unavailable: boolean;
};

const initialState: SessionStatsSnapshot = {
  status: "idle",
  stats: null,
  error: null,
  unavailable: false,
};

export type UseSessionStatsResult = SessionStatsSnapshot & {
  refresh: () => void;
};

/**
 * @param userId 目标用户；空串 / undefined 表示交给服务端取默认用户。
 *               侧栏按用户过滤会话时传入同一 userId，保证统计与列表口径一致。
 */
export function useSessionStats(userId?: string): UseSessionStatsResult {
  const [snapshot, setSnapshot] = useState<SessionStatsSnapshot>(initialState);
  const [refreshToken, setRefreshToken] = useState(0);
  const requestSeq = useRef(0);
  const activeController = useRef<AbortController | null>(null);

  const normalizedUserId = userId?.trim() ?? "";

  const refresh = useCallback(() => {
    setRefreshToken((current) => current + 1);
  }, []);

  useEffect(() => {
    const seq = requestSeq.current + 1;
    requestSeq.current = seq;

    const controller = new AbortController();
    activeController.current?.abort();
    activeController.current = controller;

    async function load() {
      setSnapshot((current) => ({
        ...current,
        status: "loading",
        error: null,
        unavailable: false,
      }));

      try {
        const response = await fetchRuntimeSessionStats(
          normalizedUserId || undefined,
          { signal: controller.signal },
        );
        if (requestSeq.current !== seq) {
          return;
        }
        setSnapshot({
          status: "ready",
          stats: response.stats,
          error: null,
          unavailable: false,
        });
      } catch (caught: unknown) {
        if (requestSeq.current !== seq || isAbortError(caught)) {
          return;
        }
        setSnapshot((current) => ({
          status: "error",
          stats: current.stats,
          error: caught,
          unavailable: isSessionStatsUnavailable(caught),
        }));
      } finally {
        if (activeController.current === controller) {
          activeController.current = null;
        }
      }
    }

    void load();

    return () => {
      controller.abort();
    };
  }, [normalizedUserId, refreshToken]);

  return { ...snapshot, refresh };
}

/** 主动取消（AbortController.abort）不落错误态；超时（TimeoutError）照常报错。 */
function isAbortError(error: unknown): boolean {
  if (typeof DOMException !== "undefined" && error instanceof DOMException) {
    return error.name === "AbortError";
  }
  return error instanceof Error && error.name === "AbortError";
}
