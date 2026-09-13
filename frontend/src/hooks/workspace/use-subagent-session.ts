/**
 * 子会话下钻（P1-5）：为父轨迹里选中的子会话建立独立的实时轨迹流。
 *
 * - 数据源复用按 session 的既有接口：先 `GET /runtime/events` 增量回填，
 *   再 `GET /runtime/stream?live=1` 持续跟随（after = 已收最大 seq），
 *   `live=1` 额外投递 `tool.progress` 等不落库的高频进度事件；
 * - 隔离：本 hook 持有独立的 TrajectoryStore，绝不写入父会话 store，
 *   父 transcript 不会被子的工具/审批事件污染（P1-5 验收②）；
 * - 断线可恢复：流结束/失败后按退避重连（默认 3 次），重连沿用游标，
 *   不重放已渲染事件；`reconnect()` 供 UI 手动重试；
 * - 终态收敛：读到子会话的 `session_end` / `chat.sse.done` 后停止跟随，
 *   状态置为 `closed`（子会话可能再次被输入，用户可手动刷新）；
 * - 审批联动（P1-5 方案 4）：从子会话事件里跟随 pending 审批
 *   （`approval_requested` → `approval_resolved`），下钻对话框据此渲染
 *   inline 批准/拒绝入口，动作仍走 actor 的 `approve_tool` 命令；
 * - 生命周期：sessionId 变化或卸载时 abort 流并 dispose store。
 */
import { useCallback, useEffect, useState } from "react";

import {
  fetchSessionRuntimeEvents,
  resolveSessionToolApproval,
} from "@/api/runtime/sessions";
import { streamSessionRuntime } from "@/api/runtime/sse";
import {
  createTrajectoryStore,
  type TrajectoryStore,
} from "@/hooks/workspace/use-trajectory-snapshot";
import {
  nextRecoveryAfter,
  trajectoryEventAction,
  TRAJECTORY_RECOVERY_PAGE_SIZE,
} from "@/lib/trajectory/recovery";
import type { SessionRuntimeEvent } from "@/types/runtime";

export type SubagentSessionStatus =
  | "idle"
  | "loading"
  | "live"
  | "reconnecting"
  | "closed"
  | "error";

export type SubagentSessionHandle = {
  /** 独立轨迹 store；会话未选中/未启用时为 null（组件用子组件消费）。 */
  store: TrajectoryStore | null;
  status: SubagentSessionStatus;
  error: string | null;
  /** 子会话当前待审批（无 pending 时为 null），供 inline 审批入口渲染。 */
  pendingApproval: PendingSubagentApproval | null;
  /** inline 审批提交中。 */
  resolvingApproval: boolean;
  /** 最近一次 inline 审批失败原因（成功或重连后清空）。 */
  approvalError: string | null;
  /** 提交审批决定；返回是否成功（失败原因见 `approvalError`）。 */
  resolveApproval: (allow: boolean) => Promise<boolean>;
  /** 手动重连（closed / error 状态可用；沿用当前游标增量续传）。 */
  reconnect: () => void;
};

/** 子会话 pending 工具审批（来自 `approval_requested` 事件载荷）。 */
export type PendingSubagentApproval = {
  requestId: string;
  toolName?: string;
  toolCallId?: string;
  reason?: string;
  riskLevel?: string;
};

const DEFAULT_MAX_RECONNECTS = 3;
const RECONNECT_BASE_DELAY_MS = 400;

function readTrimmed(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const trimmed = value.trim();
  return trimmed === "" ? undefined : trimmed;
}

/**
 * 事件 → pending 审批状态迁移。
 *
 * - `approval_requested`：缺 `request_id` 时**不建入口**（无法 resolve，宁可不显示）；
 * - `approval_resolved`：按 `request_id` 精确清除，缺 request_id 时保守清除
 *   （同一会话一次只可能有一个 pending 审批）；
 * - 其他事件：原样返回，避免无关事件（如 tool.progress）触发重渲染。
 */
export function nextPendingApproval(
  current: PendingSubagentApproval | null,
  event: SessionRuntimeEvent,
): PendingSubagentApproval | null {
  if (event.type === "approval_requested") {
    const payload = event.payload ?? {};
    const requestId = readTrimmed(payload.request_id);
    if (!requestId) {
      return current;
    }
    return {
      requestId,
      toolName: readTrimmed(payload.tool_name) ?? event.tool_name ?? undefined,
      toolCallId: readTrimmed(payload.tool_call_id),
      reason: readTrimmed(payload.reason),
      riskLevel: readTrimmed(payload.risk_level),
    };
  }
  if (event.type === "approval_resolved") {
    if (!current) {
      return null;
    }
    const requestId = readTrimmed(event.payload?.request_id);
    if (!requestId || requestId === current.requestId) {
      return null;
    }
    return current;
  }
  return current;
}

/** 子会话当前 turn 已结束的事件（停止跟随，避免长连接空转）。 */
function isTerminalChildEvent(event: SessionRuntimeEvent): boolean {
  if (event.type === "session_end") {
    return true;
  }
  return event.type === "chat.sse.done";
}

function errorMessageOf(error: unknown): string {
  if (error instanceof Error && error.message) {
    return error.message;
  }
  return String(error);
}

function delay(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    function onAbort() {
      clearTimeout(timer);
      reject(new Error("aborted"));
    }
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

export function useSubagentSession(options: {
  sessionId: string | null;
  /** 关闭对话框时置 false，暂停拉取（默认 true）。 */
  enabled?: boolean;
  maxReconnects?: number;
}): SubagentSessionHandle {
  const { sessionId, enabled = true, maxReconnects = DEFAULT_MAX_RECONNECTS } = options;
  const [store, setStore] = useState<TrajectoryStore | null>(null);
  const [status, setStatus] = useState<SubagentSessionStatus>("idle");
  const [error, setError] = useState<string | null>(null);
  const [pendingApproval, setPendingApproval] =
    useState<PendingSubagentApproval | null>(null);
  const [resolvingApproval, setResolvingApproval] = useState(false);
  const [approvalError, setApprovalError] = useState<string | null>(null);
  const [generation, setGeneration] = useState(0);

  const reconnect = useCallback(() => {
    setGeneration((current) => current + 1);
  }, []);

  /**
   * 提交审批决定：成功后立刻收掉 inline 入口（durable `approval_resolved`
   * 到达时是 no-op），失败则保留入口并把原因交给 UI（刷新可重新同步）。
   */
  const resolveApproval = useCallback(
    async (allow: boolean): Promise<boolean> => {
      const requestId = pendingApproval?.requestId ?? "";
      if (!sessionId || !requestId) {
        return false;
      }
      setResolvingApproval(true);
      setApprovalError(null);
      try {
        await resolveSessionToolApproval(sessionId, { requestId, allow });
        setPendingApproval((current) =>
          current?.requestId === requestId ? null : current,
        );
        return true;
      } catch (caught) {
        setApprovalError(errorMessageOf(caught));
        return false;
      } finally {
        setResolvingApproval(false);
      }
    },
    [pendingApproval, sessionId],
  );

  useEffect(() => {
    if (!enabled || !sessionId) {
      setStore(null);
      setStatus("idle");
      setError(null);
      setPendingApproval(null);
      setApprovalError(null);
      return;
    }

    let cancelled = false;
    const sessionStore = createTrajectoryStore();
    const abort = new AbortController();
    setStore(sessionStore);
    setStatus("loading");
    setError(null);
    setPendingApproval(null);
    setApprovalError(null);

    const applyEvent = (event: SessionRuntimeEvent) => {
      setPendingApproval((current) => nextPendingApproval(current, event));
      const action = trajectoryEventAction(event);
      if (action.kind === "push") {
        sessionStore.push(action.push.kind, action.push.payload);
      } else if (action.kind === "skip") {
        sessionStore.advanceCursor(action.seq);
      }
    };

    const backfill = async (): Promise<number> => {
      let after = 0;
      for (;;) {
        const page = await fetchSessionRuntimeEvents(sessionId, {
          after,
          limit: TRAJECTORY_RECOVERY_PAGE_SIZE,
        });
        if (cancelled) {
          return after;
        }
        for (const event of page.events) {
          applyEvent(event);
        }
        const next = nextRecoveryAfter(page.events, after);
        const exhausted =
          page.events.length === 0 ||
          page.events.length < TRAJECTORY_RECOVERY_PAGE_SIZE;
        after = next;
        if (exhausted) {
          break;
        }
      }
      sessionStore.flush();
      return after;
    };

    const follow = async (after: number, attempt: number): Promise<void> => {
      let followError: unknown = null;
      try {
        await streamSessionRuntime(sessionId, {
          after,
          live: true,
          signal: abort.signal,
          onEvent: (event) => {
            if (cancelled) {
              return;
            }
            applyEvent(event);
            if (isTerminalChildEvent(event)) {
              // 子会话当前 turn 已收尾：主动断开，避免长连接空转。
              setPendingApproval(null);
              sessionStore.flush();
              setStatus("closed");
              abort.abort();
            }
          },
          onErrorEvent: (payload) => {
            const message =
              typeof payload?.message === "string" ? payload.message : null;
            if (message && !cancelled) {
              setError(message);
            }
          },
        });
      } catch (caught) {
        followError = caught;
      }

      if (cancelled || abort.signal.aborted) {
        return;
      }
      if (followError === null) {
        // 服务端正常结束（例如会话归档）：仍有重试额度时按退避续传。
        if (attempt >= maxReconnects) {
          setStatus("closed");
          return;
        }
      } else if (attempt >= maxReconnects) {
        setStatus("error");
        setError(errorMessageOf(followError));
        return;
      }

      setStatus("reconnecting");
      try {
        await delay(RECONNECT_BASE_DELAY_MS * (attempt + 1), abort.signal);
      } catch {
        return;
      }
      if (cancelled || abort.signal.aborted) {
        return;
      }
      setStatus("live");
      await follow(sessionStore.getSnapshot().lastEventSeq, attempt + 1);
    };

    void (async () => {
      try {
        const after = await backfill();
        if (cancelled || abort.signal.aborted) {
          return;
        }
        setStatus("live");
        await follow(after, 0);
      } catch (caught) {
        if (cancelled || abort.signal.aborted) {
          return;
        }
        setStatus("error");
        setError(errorMessageOf(caught));
      }
    })();

    return () => {
      cancelled = true;
      abort.abort();
      sessionStore.dispose();
      setStore((current) => (current === sessionStore ? null : current));
    };
  }, [enabled, generation, maxReconnects, sessionId]);

  return {
    store,
    status,
    error,
    pendingApproval,
    resolvingApproval,
    approvalError,
    resolveApproval,
    reconnect,
  };
}
