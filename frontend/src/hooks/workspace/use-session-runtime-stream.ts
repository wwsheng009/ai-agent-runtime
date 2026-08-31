import { type Dispatch, type SetStateAction, useEffect, useRef } from "react";

import { type Thread } from "@/data/mock";
import {
  streamSessionRuntime,
  type SessionRuntimeEvent,
} from "@/lib/runtime-api";
import {
  getRuntimeDeltaKeyFromEvent,
  getRuntimeDeltaKind,
  getRuntimeEventTurnId,
  type RuntimeDeltaCoordinator,
} from "@/lib/workspace-thread-state";

/** 方案B：重连循环连续失败达到该阈值才把 thread 标记为降级（防瞬断抖动）。 */
const STREAM_FAILURE_THRESHOLD = 3;

type SessionRuntimeStreamOptions = {
  applyRuntimeEventToThread: (
    thread: Thread,
    sessionId: string,
    events: SessionRuntimeEvent[],
    event: SessionRuntimeEvent,
  ) => Thread;
  getErrorMessage: (error: unknown, fallback: string) => string;
  getRuntimeEventSeq: (event: SessionRuntimeEvent) => number;
  mergeRuntimeEvent: (
    existingEvents: SessionRuntimeEvent[],
    nextEvent: SessionRuntimeEvent,
  ) => SessionRuntimeEvent[];
  /** Q4：可选轨迹投递——运行时生命周期事件到达时通知（由调用方转成轨迹 push）。 */
  onTrajectoryEvent?: (event: SessionRuntimeEvent) => void;
  /** 方案B：请求进行中才渲染增量文本（历史回放/reload 不渲染）。 */
  renderLiveDeltas?: boolean;
  /** 当前活动 turn（外部调用方透传；预留用于按 turn 维度对齐 delta）。 */
  activeTurnId?: string | null;
  /** Shares delta claims with the direct /api/agent/chat SSE request. */
  deltaCoordinator?: RuntimeDeltaCoordinator;
  /** 方案B：打字机增量事件（assistant_delta/reasoning/image_progress）应用函数。 */
  applyRuntimeDeltaToThread: (
    thread: Thread,
    event: SessionRuntimeEvent,
    expectedTurnId?: string,
  ) => Thread;
  selectedThread: Thread | undefined;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
};

export function useSessionRuntimeStream({
  applyRuntimeEventToThread,
  applyRuntimeDeltaToThread,
  getErrorMessage,
  getRuntimeEventSeq,
  mergeRuntimeEvent,
  onTrajectoryEvent,
  renderLiveDeltas = false,
  activeTurnId,
  deltaCoordinator,
  selectedThread,
  setThreads,
}: SessionRuntimeStreamOptions) {
  const runtimeEventsRef = useRef<Record<string, SessionRuntimeEvent[]>>({});
  const runtimeSeqRef = useRef<Record<string, number>>({});
  const activeTurnIdRef = useRef(activeTurnId);
  useEffect(() => {
    activeTurnIdRef.current = activeTurnId;
  }, [activeTurnId]);
  const deltaCoordinatorRef = useRef(deltaCoordinator);
  useEffect(() => {
    deltaCoordinatorRef.current = deltaCoordinator;
  }, [deltaCoordinator]);
  // Q4：轨迹投递回调经 ref 转发——调用方每次 render 产生新引用时
  // 不触发 effect 重跑（否则 onEvent 内 setThreads → render → 新 arrow →
  // abort 重连 → 无限重连循环）。ref 写入放在 effect（react-hooks/refs）。
  const onTrajectoryEventRef = useRef(onTrajectoryEvent);
  useEffect(() => {
    onTrajectoryEventRef.current = onTrajectoryEvent;
  }, [onTrajectoryEvent]);
  const renderLiveDeltasRef = useRef(renderLiveDeltas);
  useEffect(() => {
    renderLiveDeltasRef.current = renderLiveDeltas;
  }, [renderLiveDeltas]);
  const threadId = selectedThread?.id;
  const sessionId = selectedThread?.sessionId;

  useEffect(() => {
    if (!threadId || !sessionId) {
      return;
    }

    const controller = new AbortController();

    const sleep = (ms: number) =>
      new Promise<void>((resolve) => {
        const timer = setTimeout(resolve, ms);
        controller.signal.addEventListener(
          "abort",
          () => {
            clearTimeout(timer);
            resolve();
          },
          { once: true },
        );
      });

    void (async () => {
      // 方案B：常驻长轮询循环——单次连接结束（空流/断开/会话切换）后
      // 以 after 游标续传重连，保证请求进行中的增量事件始终可达。
      // 防抖：连续失败达到阈值才标记 thread error，偶发瞬断（循环重连
      // 场景的常见噪音）不会把 thread 粘滞在降级状态。
      let consecutiveFailures = 0;
      while (!controller.signal.aborted) {
        let streamFailed = false;
        try {
          await streamSessionRuntime(sessionId, {
            after: runtimeSeqRef.current[sessionId] ?? 0,
            pollMs: 500,
            signal: controller.signal,
            onEvent: (event) => {
              // 收到事件 = 通道已恢复；重置连续失败计数。
              consecutiveFailures = 0;
              onTrajectoryEventRef.current?.(event);

              const nextSeq = getRuntimeEventSeq(event);
              if (
                nextSeq > 0 &&
                nextSeq > (runtimeSeqRef.current[sessionId] ?? 0)
              ) {
                runtimeSeqRef.current[sessionId] = nextSeq;
              }

              const nextEvents = mergeRuntimeEvent(
                runtimeEventsRef.current[sessionId] ?? [],
                event,
              );
              runtimeEventsRef.current[sessionId] = nextEvents;

              // Claim the shared provider identity outside the React state
              // updater. Functional updaters may be replayed by StrictMode or
              // concurrent rendering; making the claim there would consume a
              // dedupe key even when React discards the update.
              const deltaKind = getRuntimeDeltaKind(event.type);
              const activeTurn = activeTurnIdRef.current?.trim() ?? "";
              const eventTurn = getRuntimeEventTurnId(event);
              const turnMatches =
                !activeTurn || eventTurn === activeTurn;
              let shouldApplyLiveDelta = false;
              if (renderLiveDeltasRef.current && deltaKind && turnMatches) {
                const deltaKey = getRuntimeDeltaKeyFromEvent(event);
                const coordinator = deltaCoordinatorRef.current;
                shouldApplyLiveDelta =
                  Boolean(deltaKey) && coordinator
                    ? coordinator.claim(deltaKey)
                    : Boolean(deltaKey) || !coordinator;
              }

              setThreads((current) =>
                current.map((thread) => {
                  if (thread.id !== threadId) {
                    return thread;
                  }
                  // 通道恢复：收到 stream 事件说明连接已恢复，清除由
                  // stream 自身产生的降级标记（recovery 等其他来源的
                  // lastError 保持，不被误清）。
                  const recovered =
                    thread.transport === "error" &&
                    thread.lastError?.startsWith("Runtime stream failed")
                      ? { ...thread, transport: "live" as const, lastError: null }
                      : thread;
                  // 方案B：请求进行中，打字机增量事件直接渲染到消息；
                  // 否则只进事件快照（历史回放/reload 不误渲染）。
                  // A durable event from another turn never mutates the
                  // currently streaming assistant message.  The claim above
                  // is intentionally shared by both transport paths.
                  if (shouldApplyLiveDelta) {
                    return applyRuntimeDeltaToThread(
                      recovered,
                      event,
                      activeTurn || undefined,
                    );
                  }
                  return applyRuntimeEventToThread(
                    recovered,
                    sessionId,
                    nextEvents,
                    event,
                  );
                }),
              );
            },
            onErrorEvent: (payload) => {
              streamFailed = true;
              // 方案B 防抖：SSE error 事件同样计入连续失败，达到阈值才标记降级。
              consecutiveFailures += 1;
              const message =
                typeof payload.error === "string" && payload.error.trim()
                  ? payload.error.trim()
                  : "runtime stream reported an error";
              if (consecutiveFailures >= STREAM_FAILURE_THRESHOLD) {
                setThreads((current) =>
                  current.map((thread) =>
                    thread.id === threadId
                      ? {
                          ...thread,
                          updatedAt: new Date().toISOString(),
                          transport: "error",
                          lastError: `Runtime stream failed: ${message}`,
                        }
                      : thread,
                  ),
                );
              }
            },
          });
        } catch (error) {
          if (controller.signal.aborted) {
            return;
          }
          streamFailed = true;
          const message = getErrorMessage(error, "failed to connect runtime stream");
          consecutiveFailures += 1;
          if (consecutiveFailures >= STREAM_FAILURE_THRESHOLD) {
            setThreads((current) =>
              current.map((thread) =>
                thread.id === threadId
                  ? {
                      ...thread,
                      updatedAt: new Date().toISOString(),
                      transport: "error",
                      lastError: `Runtime stream failed: ${message}`,
                    }
                  : thread,
              ),
            );
          }
        }
        if (controller.signal.aborted) {
          return;
        }
        // 退避重连：错误 1s；正常空流 2s（长轮询后端无空流——事件到达即返回；
        // mock/空后端下降低轮询频率，避免共享测试服务被高频请求拖慢）。
        await sleep(streamFailed ? 1000 : 2000);
      }
    })();

    return () => {
      controller.abort();
    };
  }, [
    applyRuntimeEventToThread,
    applyRuntimeDeltaToThread,
    getErrorMessage,
    getRuntimeEventSeq,
    mergeRuntimeEvent,
    setThreads,
    sessionId,
    threadId,
  ]);
}
