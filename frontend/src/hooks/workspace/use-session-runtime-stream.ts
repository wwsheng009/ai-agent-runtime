import { type Dispatch, type SetStateAction, useEffect, useRef } from "react";

import { type Thread } from "@/data/mock";
import {
  streamSessionRuntime,
  type SessionRuntimeEvent,
} from "@/lib/runtime-api";

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
  selectedThread: Thread | undefined;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
};

export function useSessionRuntimeStream({
  applyRuntimeEventToThread,
  getErrorMessage,
  getRuntimeEventSeq,
  mergeRuntimeEvent,
  onTrajectoryEvent,
  selectedThread,
  setThreads,
}: SessionRuntimeStreamOptions) {
  const runtimeEventsRef = useRef<Record<string, SessionRuntimeEvent[]>>({});
  const runtimeSeqRef = useRef<Record<string, number>>({});
  // Q4：轨迹投递回调经 ref 转发——调用方每次 render 产生新引用时
  // 不触发 effect 重跑（否则 onEvent 内 setThreads → render → 新 arrow →
  // abort 重连 → 无限重连循环）。
  const onTrajectoryEventRef = useRef(onTrajectoryEvent);
  onTrajectoryEventRef.current = onTrajectoryEvent;
  const threadId = selectedThread?.id;
  const sessionId = selectedThread?.sessionId;

  useEffect(() => {
    if (!threadId || !sessionId) {
      return;
    }

    const controller = new AbortController();

    void (async () => {
      try {
        await streamSessionRuntime(sessionId, {
          after: runtimeSeqRef.current[sessionId] ?? 0,
          pollMs: 500,
          signal: controller.signal,
          onEvent: (event) => {
            onTrajectoryEventRef.current?.(event);

            const nextSeq = getRuntimeEventSeq(event);
            if (nextSeq > 0) {
              runtimeSeqRef.current[sessionId] = nextSeq;
            }

            const nextEvents = mergeRuntimeEvent(
              runtimeEventsRef.current[sessionId] ?? [],
              event,
            );
            runtimeEventsRef.current[sessionId] = nextEvents;

            setThreads((current) =>
              current.map((thread) =>
                thread.id === threadId
                  ? applyRuntimeEventToThread(thread, sessionId, nextEvents, event)
                  : thread,
              ),
            );
          },
          onErrorEvent: (payload) => {
            const message =
              typeof payload.error === "string" && payload.error.trim()
                ? payload.error.trim()
                : "runtime stream reported an error";
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
          },
        });
      } catch (error) {
        if (controller.signal.aborted) {
          return;
        }
        const message = getErrorMessage(error, "failed to connect runtime stream");
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
    })();

    return () => {
      controller.abort();
    };
  }, [
    applyRuntimeEventToThread,
    getErrorMessage,
    getRuntimeEventSeq,
    mergeRuntimeEvent,
    setThreads,
    sessionId,
    threadId,
  ]);
}
