import { useCallback, useEffect, useState } from "react";

import {
  getSessionRuntimeState,
  type RuntimeSessionSnapshot,
  type RuntimeSessionState,
} from "@/lib/runtime-api";

type SessionRuntimeEntry = {
  sessionId: string;
  snapshot: RuntimeSessionSnapshot;
};

type SessionRuntimeFailure = {
  sessionId: string;
  error: unknown;
};

export type UseSessionRuntimeStateResult = {
  snapshot: RuntimeSessionSnapshot | null;
  state: RuntimeSessionState | null;
  /** 拉取失败（404 视为「该会话无 runtime state」，按空态处理，不计入 error）。 */
  error: unknown;
  /** 重新拉取（会话切换自动拉取；重连 / 决策后可主动刷新）。 */
  refresh: () => void;
};

function isNotFound(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    (error as { status?: unknown }).status === 404
  );
}

/**
 * P2-1A：会话运行时状态快照（`GET /api/runtime/sessions/{id}/runtime`）。
 *
 * 用途：把 P1-7 待交互注册表从「仅事件流」升级为「事件流 + 快照重建」——
 * 页面重载 / 重连后仍能恢复未决审批与提问；同时暴露会话状态与活跃后台任务 id。
 *
 * 取值按会话键存（陈旧会话的结果不串台），会话切换 / 卸载时 abort 在途请求；
 * effect 同步段零 setState（结果只在 fetch 回调里落库）。
 */
export function useSessionRuntimeState(
  sessionId?: string,
): UseSessionRuntimeStateResult {
  const trimmedSessionId = sessionId?.trim() ?? "";
  const [entry, setEntry] = useState<SessionRuntimeEntry | null>(null);
  const [failure, setFailure] = useState<SessionRuntimeFailure | null>(null);
  const [reloadToken, setReloadToken] = useState(0);

  useEffect(() => {
    if (!trimmedSessionId) {
      return;
    }
    const controller = new AbortController();
    let active = true;
    getSessionRuntimeState(trimmedSessionId, { signal: controller.signal })
      .then((snapshot) => {
        if (!active) {
          return;
        }
        setEntry({ sessionId: trimmedSessionId, snapshot });
        setFailure(null);
      })
      .catch((error: unknown) => {
        if (!active || controller.signal.aborted || isNotFound(error)) {
          return;
        }
        setEntry(null);
        setFailure({ sessionId: trimmedSessionId, error });
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [reloadToken, trimmedSessionId]);

  const refresh = useCallback(() => {
    setReloadToken((token) => token + 1);
  }, []);

  const snapshot =
    entry && entry.sessionId === trimmedSessionId ? entry.snapshot : null;

  return {
    snapshot,
    state: snapshot?.state ?? null,
    error:
      failure && failure.sessionId === trimmedSessionId ? failure.error : null,
    refresh,
  };
}
