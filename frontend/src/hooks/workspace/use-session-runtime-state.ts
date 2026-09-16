import { useCallback, useEffect, useState } from "react";

import {
  getSessionRuntimeState,
  type RuntimeSessionActiveTurn,
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
  /**
   * P4-刷新续传：本会话此刻的在途回合（快照 `active_turn`），无则 null。
   *
   * 与 `state` 相互独立：web 直连会话没有 durable state（`state` 恒为 null），
   * 但在途回合仍会出现在这里——刷新后据此重新挂载回合身份，续传增量流。
   */
  activeTurn: RuntimeSessionActiveTurn | null;
  /**
   * 拉取失败。以下两种「无 runtime state」不计入 error（都收敛为快照空态）：
   * 后端显式空快照（200 + `state: null`：会话存在、从未进入 durable actor）与
   * 404（会话不存在 / 已删除——调用方动作与空态一致，且旧后端只会回 404）。
   * 503/504 等存储故障属于可重试错误，照常暴露。
   */
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
        // snapshot === null 是后端显式空态（会话存在、无 durable runtime state）：
        // 清掉本会话的陈旧快照，避免把上一轮结果当成当前状态。
        setEntry(
          snapshot ? { sessionId: trimmedSessionId, snapshot } : null,
        );
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
    activeTurn: snapshot?.activeTurn ?? null,
    error:
      failure && failure.sessionId === trimmedSessionId ? failure.error : null,
    refresh,
  };
}
