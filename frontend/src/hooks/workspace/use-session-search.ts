// P2-1A：会话元数据检索状态机（idle → loading → ready / error）。
//
// 竞态与生命周期：
//   * 每次 run 递增请求序号，只有最新一次的结果能写回 state（旧响应丢弃）；
//   * 新一次检索与组件卸载都会中止在途请求（AbortController）；
//   * 主动取消（AbortError）不落错误态，超时与真实失败照常呈现。
//
// 降级：HTTP 404/405/501/503 视为「服务端检索不可用」（unavailable=true），
// 由 UI 如实提示（不清空用户输入、不伪造空结果）。

import { useCallback, useEffect, useRef, useState } from "react";

import {
  isSessionSearchUnavailable,
  searchRuntimeSessions,
} from "@/api/runtime/session-search";
import type {
  RuntimeSessionSearchFilters,
  RuntimeSessionSearchResponse,
} from "@/types/runtime";

export type SessionSearchStatus = "idle" | "loading" | "ready" | "error";

export type SessionSearchSnapshot = {
  status: SessionSearchStatus;
  result: RuntimeSessionSearchResponse | null;
  error: unknown;
  unavailable: boolean;
};

const initialState: SessionSearchSnapshot = {
  status: "idle",
  result: null,
  error: null,
  unavailable: false,
};

export type UseSessionSearchResult = SessionSearchSnapshot & {
  run: (filters: RuntimeSessionSearchFilters) => Promise<void>;
  reset: () => void;
};

export function useSessionSearch(): UseSessionSearchResult {
  const [snapshot, setSnapshot] = useState<SessionSearchSnapshot>(initialState);
  const requestSeq = useRef(0);
  const activeController = useRef<AbortController | null>(null);

  const abortActive = useCallback(() => {
    activeController.current?.abort();
    activeController.current = null;
  }, []);

  useEffect(() => abortActive, [abortActive]);

  const run = useCallback(
    async (filters: RuntimeSessionSearchFilters) => {
      abortActive();
      const seq = requestSeq.current + 1;
      requestSeq.current = seq;

      const controller = new AbortController();
      activeController.current = controller;
      setSnapshot({
        status: "loading",
        result: null,
        error: null,
        unavailable: false,
      });

      try {
        const result = await searchRuntimeSessions(filters, {
          signal: controller.signal,
        });
        if (requestSeq.current !== seq) {
          return;
        }
        setSnapshot({
          status: "ready",
          result,
          error: null,
          unavailable: false,
        });
      } catch (caught) {
        if (requestSeq.current !== seq || isAbortError(caught)) {
          return;
        }
        setSnapshot({
          status: "error",
          result: null,
          error: caught,
          unavailable: isSessionSearchUnavailable(caught),
        });
      } finally {
        if (activeController.current === controller) {
          activeController.current = null;
        }
      }
    },
    [abortActive],
  );

  const reset = useCallback(() => {
    abortActive();
    requestSeq.current += 1;
    setSnapshot(initialState);
  }, [abortActive]);

  return { ...snapshot, run, reset };
}

/** 主动取消（AbortController.abort）不落错误态；超时（TimeoutError）照常报错。 */
function isAbortError(error: unknown): boolean {
  if (typeof DOMException !== "undefined" && error instanceof DOMException) {
    return error.name === "AbortError";
  }
  return error instanceof Error && error.name === "AbortError";
}
