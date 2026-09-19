// Batch 2（多会话并发运行时）：单会话订阅状态机（headless，无 React / 无 DOM）。
//
// 职责边界（方案 §4.2 entry.ts）：
// - 建连：`live` 跑常驻 SSE 循环（`streamSessionRuntime`），`poll` 跑低频快照轮询
//   （`getSessionRuntimeState`），`idle` 不订阅；
// - 续传：建连游标 `after = max(本会话已消费 lastSeq, 轨迹窗口已回放 seq)`
//   —— IN1/IN2：后台会话与前台共用同一游标语义，切换不重复 dump、不丢事件；
// - 退避：失败重连 1s、正常收流 2s；连续失败达阈值才收敛 offline（与前台 hook 同口径）；
// - 归约：只维护轻量投影（lastSeq / activeTurn / pending / runningAgents /
//   lastEventAt / lastError），**不写 `threads`、不渲染消息**（D1-B，§4.3）。
//
// 页面层通过 `subscribe` 观察投影；事件本体经 `onEvent` 出口交给调用方
// （前台会话转成 thread 归约，后台会话只做投影与待交互登记）。

import {
  getSessionRuntimeState,
  streamSessionRuntime,
} from "@/lib/runtime-api";
import { type ConnectionStatus } from "@/lib/connection-status";
import {
  applyPendingInteractionEvent,
  emptyPendingInteractionState,
} from "@/lib/pending-interaction";
import { getRuntimeEventTurnId } from "@/lib/thread-state/events-live";
import { getRuntimeEventSeq } from "@/lib/thread-state/sessions";
import type {
  RuntimeSessionActiveTurn,
  RuntimeSessionSnapshot,
  SessionRuntimeEvent,
} from "@/types/runtime";

import {
  RUNTIME_STREAM_FAILURE_RECONNECT_MS,
  RUNTIME_STREAM_FAILURE_THRESHOLD,
  RUNTIME_STREAM_IDLE_RECONNECT_MS,
  RUNTIME_STREAM_IDLE_TIMEOUT_MS,
  SESSION_RUNTIME_POLL_BACKOFF_FACTOR,
  SESSION_RUNTIME_POLL_IDLE_MAX_MS,
  SESSION_RUNTIME_POLL_INITIAL_MS,
  SESSION_RUNTIME_POLL_MAX_MS,
} from "./constants";
import { sessionRuntimeLoopFailurePatch } from "./loop-failure";
import { countPending, samePendingCounts } from "./pending-counts";
import {
  createSessionRuntimePauseGate,
  runSessionRuntimePollLoop,
  sleepWithSignal,
} from "./poll-loop";
import type {
  SessionRuntimeEntryObserver,
  SessionRuntimeEntrySnapshot,
  SessionRuntimePendingCounts,
  SubscriptionMode,
} from "./types";

type StreamRuntimeFn = typeof streamSessionRuntime;
type FetchSnapshotFn = typeof getSessionRuntimeState;

export type SessionRuntimeEntryConfig = {
  sessionId: string;
  /**
   * IN1：本会话轨迹窗口已回放到的最大 seq（tail-first 建连去重）。
   * 缺省视为 0 —— 只依赖 entry 自己消费的 lastSeq。
   */
  getReplayCursor?: () => number;
  /** 事件出口：调用方决定"写不写 threads"（D1-B 的关键分岔点）。 */
  onEvent?: (event: SessionRuntimeEvent) => void;
  /** 注入点（单测用）；缺省走真实 SSE / REST。 */
  streamRuntime?: StreamRuntimeFn;
  fetchSnapshot?: FetchSnapshotFn;
  now?: () => number;
  idleReconnectMs?: number;
  failureReconnectMs?: number;
  failureThreshold?: number;
  pollInitialMs?: number;
  pollMaxMs?: number;
  pollBackoffFactor?: number;
  /** 空闲自适应上限（成功路径的退避上限；与失败退避 `pollMaxMs` 独立）。 */
  pollIdleMaxMs?: number;
};

export type SessionRuntimeEntry = {
  readonly sessionId: string;
  snapshot(): SessionRuntimeEntrySnapshot;
  subscribe(observer: SessionRuntimeEntryObserver): () => void;
  /** 由注册表按预算/策略调用：切换订阅强度（幂等）。 */
  setMode(mode: SubscriptionMode): void;
  /**
   * 页面隐藏时暂停 `poll` 循环（`live` 不受影响）：暂停期间不发起快照请求，
   * `setPaused(false)` 立即唤醒循环并拉取一次（恢复可见即刷新）。
   */
  setPaused(paused: boolean): void;
  /** 本地发起回合（提交流）时登记在途身份；回合终态传 null。 */
  noteActiveTurn(turn: RuntimeSessionActiveTurn | null): void;
  /** 手动重试（P1-8 语义）：重启当前循环，不新增第二套退避。 */
  retry(): void;
  dispose(): void;
};

const TURN_TERMINAL_TYPES = new Set([
  "session_end",
  "session.end",
  "session_interrupted",
  "session.interrupted",
  "turn_finished",
  "turn.finished",
  "turn_completed",
  "turn.completed",
  "turn_failed",
  "turn.failed",
  "turn_cancelled",
  "turn.cancelled",
]);

const AGENT_STARTED_TYPES = new Set([
  "subagent.started",
  "subagent.batch.started",
]);
const AGENT_COMPLETED_TYPES = new Set([
  "subagent.completed",
  "subagent.batch.completed",
]);

const EMPTY_PENDING_COUNTS: SessionRuntimePendingCounts = {
  approvals: 0,
  questions: 0,
  planPending: false,
};

function readString(
  payload: Record<string, unknown> | undefined,
  ...keys: string[]
): string {
  if (!payload) {
    return "";
  }
  for (const key of keys) {
    const value = payload[key];
    if (typeof value === "string" && value.trim()) {
      return value.trim();
    }
  }
  return "";
}

export function createSessionRuntimeEntry(
  config: SessionRuntimeEntryConfig,
): SessionRuntimeEntry {
  const {
    sessionId,
    getReplayCursor,
    onEvent,
    streamRuntime = streamSessionRuntime,
    fetchSnapshot = getSessionRuntimeState,
    now = () => Date.now(),
    idleReconnectMs = RUNTIME_STREAM_IDLE_RECONNECT_MS,
    failureReconnectMs = RUNTIME_STREAM_FAILURE_RECONNECT_MS,
    failureThreshold = RUNTIME_STREAM_FAILURE_THRESHOLD,
    pollInitialMs = SESSION_RUNTIME_POLL_INITIAL_MS,
    pollMaxMs = SESSION_RUNTIME_POLL_MAX_MS,
    pollBackoffFactor = SESSION_RUNTIME_POLL_BACKOFF_FACTOR,
    pollIdleMaxMs = SESSION_RUNTIME_POLL_IDLE_MAX_MS,
  } = config;

  const observers = new Set<SessionRuntimeEntryObserver>();
  let current: SessionRuntimeEntrySnapshot = {
    sessionId,
    mode: "idle",
    status: "idle",
    lastSeq: 0,
    activeTurn: null,
    detached: false,
    pending: EMPTY_PENDING_COUNTS,
    runningAgents: 0,
    lastEventAt: null,
    lastError: null,
    paused: false,
  };
  let pendingState = emptyPendingInteractionState();
  const runningAgents = new Set<string>();
  let disposed = false;
  let loopController: AbortController | null = null;
  let loopToken = 0;
  /**
   * 页面隐藏时的 poll 暂停（§4.6 降采样）：暂停期间不发起任何快照请求；
   * `setPaused(false)` 立即唤醒循环（恢复可见即刷新一次）。
   */
  const pauseGate = createSessionRuntimePauseGate((paused) =>
    commit({ paused }),
  );

  function commit(patch: Partial<SessionRuntimeEntrySnapshot>) {
    let changed = false;
    for (const key of Object.keys(patch) as Array<
      keyof SessionRuntimeEntrySnapshot
    >) {
      const next = patch[key];
      const previous = current[key];
      if (key === "pending") {
        if (!samePendingCounts(previous as SessionRuntimePendingCounts, next as SessionRuntimePendingCounts)) {
          changed = true;
          break;
        }
        continue;
      }
      if (!Object.is(previous, next)) {
        changed = true;
        break;
      }
    }
    if (!changed) {
      return;
    }
    current = { ...current, ...patch };
    // 订阅者错误隔离：单个观察者抛错不影响其它订阅者与订阅循环
    // （方案 §4.2 "订阅者通知（错误隔离 + 浅比较）"）。
    for (const observer of [...observers]) {
      try {
        observer(current);
      } catch {
        // 观察者是纯通知（React 绑定 / 策略层），异常不外溢到状态机。
      }
    }
  }

  function setStatus(status: ConnectionStatus) {
    commit({ status });
  }

  function updateAgentsFromEvent(
    event: SessionRuntimeEvent,
    patch: Partial<SessionRuntimeEntrySnapshot>,
  ) {
    const type = event.type?.trim().toLowerCase() ?? "";
    const started = AGENT_STARTED_TYPES.has(type);
    const completed = AGENT_COMPLETED_TYPES.has(type);
    if (!started && !completed) {
      return;
    }
    const key =
      readString(event.payload, "agent_id", "agentId", "batch_id", "id") ||
      event.agent_name?.trim() ||
      "";
    if (started) {
      runningAgents.add(key || `anonymous:${runningAgents.size}`);
    } else if (key) {
      runningAgents.delete(key);
    } else {
      const last = [...runningAgents].pop();
      if (last) {
        runningAgents.delete(last);
      }
    }
    patch.runningAgents = runningAgents.size;
  }

  function updateActiveTurnFromEvent(
    event: SessionRuntimeEvent,
    patch: Partial<SessionRuntimeEntrySnapshot>,
  ) {
    const type = event.type?.trim().toLowerCase() ?? "";
    const turnId = getRuntimeEventTurnId(event);
    const detachedFlag = event.payload?.detached;
    if (typeof detachedFlag === "boolean" && !TURN_TERMINAL_TYPES.has(type)) {
      patch.detached = detachedFlag;
    }
    if (TURN_TERMINAL_TYPES.has(type)) {
      // 终态事件：仅当它指向当前在途回合（或缺身份）时清空，避免旧回合的
      // 迟到终态误清掉新回合（多回合并发下的关键判定）。
      if (!turnId || current.activeTurn?.turnId === turnId) {
        patch.activeTurn = null;
        patch.detached = false;
      }
      return;
    }
    if (turnId && current.activeTurn?.turnId !== turnId) {
      patch.activeTurn = {
        sessionId,
        turnId,
        source: "runtime_stream",
        detached: patch.detached ?? false,
      };
    }
  }

  function ingestEvent(event: SessionRuntimeEvent) {
    const patch: Partial<SessionRuntimeEntrySnapshot> = {
      lastEventAt: now(),
      lastError: null,
      status: "online",
    };
    const seq = getRuntimeEventSeq(event);
    if (seq > 0 && seq > current.lastSeq) {
      patch.lastSeq = seq;
    }
    const nextPending = applyPendingInteractionEvent(pendingState, event);
    if (nextPending !== pendingState) {
      pendingState = nextPending;
      patch.pending = countPending(nextPending);
    }
    updateAgentsFromEvent(event, patch);
    updateActiveTurnFromEvent(event, patch);
    commit(patch);
    onEvent?.(event);
  }

  /** 快照轮询的归约：只取运行态与待交互（不发事件出口，不写 threads）。 */
  function applySnapshot(snapshot: RuntimeSessionSnapshot | null) {
    const state = snapshot?.state ?? null;
    const activeTurn = snapshot?.activeTurn ?? null;
    commit({
      status: "online",
      lastError: null,
      activeTurn,
      detached: activeTurn?.detached ?? false,
      pending: {
        approvals: state?.pendingApproval ? 1 : 0,
        questions: state?.pendingQuestion ? 1 : 0,
        // 计划评审不在 /runtime 快照里，保留事件归约结果（poll 会话通常无事件，
        // 因此该位在 poll 下保持上次已知值）。
        planPending: current.pending.planPending,
      },
      lastEventAt: snapshot ? now() : current.lastEventAt,
    });
  }

  async function runLiveLoop(token: number, controller: AbortController) {
    let consecutiveFailures = 0;
    while (!disposed && loopToken === token && !controller.signal.aborted) {
      let streamFailed = false;
      try {
        await streamRuntime(sessionId, {
          // IN1：游标 = max(本会话已消费 lastSeq, 轨迹窗口已回放 seq)。
          after: Math.max(current.lastSeq, getReplayCursor?.() ?? 0),
          idleTimeoutMs: RUNTIME_STREAM_IDLE_TIMEOUT_MS,
          live: true,
          pollMs: 500,
          signal: controller.signal,
          onOpen: () => {
            if (loopToken !== token) {
              return;
            }
            consecutiveFailures = 0;
            setStatus("online");
          },
          onEvent: (event) => {
            if (loopToken !== token) {
              return;
            }
            consecutiveFailures = 0;
            ingestEvent(event);
          },
          onErrorEvent: (payload) => {
            if (loopToken !== token) {
              return;
            }
            streamFailed = true;
            consecutiveFailures += 1;
            const message = readString(payload, "error", "message");
            commit({
              status:
                consecutiveFailures >= failureThreshold ? "offline" : "reconnecting",
              lastError: message || "runtime stream reported an error",
            });
          },
        });
      } catch (error) {
        if (controller.signal.aborted || loopToken !== token) {
          return;
        }
        streamFailed = true;
        consecutiveFailures += 1;
        commit({
          status:
            consecutiveFailures >= failureThreshold ? "offline" : "reconnecting",
          lastError:
            error instanceof Error && error.message.trim()
              ? error.message.trim()
              : "failed to connect runtime stream",
        });
      }
      if (disposed || loopToken !== token || controller.signal.aborted) {
        return;
      }
      await sleepWithSignal(streamFailed ? failureReconnectMs : idleReconnectMs, controller.signal);
    }
  }

  function runPollLoop(token: number, controller: AbortController) {
    return runSessionRuntimePollLoop({
      sessionId,
      signal: controller.signal,
      fetchSnapshot,
      pollInitialMs,
      pollMaxMs,
      pollIdleMaxMs,
      pollBackoffFactor,
      isActive: () => !disposed && loopToken === token,
      isPaused: pauseGate.isPaused,
      waitWhilePaused: pauseGate.waitWhilePaused,
      onPaused: () => setStatus("idle"),
      onSnapshot: applySnapshot,
      onError: (error) => {
        commit({
          status: "reconnecting",
          lastError:
            error instanceof Error && error.message.trim()
              ? error.message.trim()
              : "failed to poll session runtime state",
        });
      },
    });
  }

  function stopLoop() {
    loopToken += 1;
    loopController?.abort();
    loopController = null;
  }

  function startLoop(mode: SubscriptionMode) {
    const controller = new AbortController();
    loopController = controller;
    const token = loopToken;
    setStatus("connecting");
    const loop = mode === "live" ? runLiveLoop(token, controller) : runPollLoop(token, controller);
    // 中止/过期循环静默收尾；在任循环的意外拒绝收敛为可重试失败态（见 loop-failure.ts）。
    void loop.catch((error: unknown) => {
      if (disposed || loopToken !== token) {
        return;
      }
      commit(sessionRuntimeLoopFailurePatch(error));
    });
  }

  function setMode(mode: SubscriptionMode) {
    if (disposed || mode === current.mode) {
      return;
    }
    const previous = current.mode;
    commit({ mode });
    if (mode === "idle") {
      stopLoop();
      setStatus("idle");
      return;
    }
    if (previous !== "idle") {
      // 模式切换（live ↔ poll）：先停旧循环再起新循环，游标/账目全部保留。
      stopLoop();
    }
    startLoop(mode);
  }

  function noteActiveTurn(turn: RuntimeSessionActiveTurn | null) {
    if (disposed) {
      return;
    }
    commit({
      activeTurn: turn,
      detached: turn?.detached ?? false,
      ...(turn ? { lastError: null } : {}),
    });
  }

  function dispose() {
    if (disposed) {
      return;
    }
    disposed = true;
    stopLoop();
    observers.clear();
  }

  return {
    sessionId,
    snapshot: () => current,
    subscribe(observer: SessionRuntimeEntryObserver) {
      if (disposed) {
        return () => {};
      }
      observers.add(observer);
      return () => {
        observers.delete(observer);
      };
    },
    setMode,
    setPaused: pauseGate.setPaused,
    noteActiveTurn,
    retry() {
      if (disposed || current.mode === "idle") {
        return;
      }
      // 手动重试复用同一循环与退避：重建 controller 使旧连接 abort，游标保持。
      stopLoop();
      startLoop(current.mode);
    },
    dispose,
  };
}
