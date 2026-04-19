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
  selectedThread: Thread | undefined;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
};

export function useSessionRuntimeStream({
  applyRuntimeEventToThread,
  getErrorMessage,
  getRuntimeEventSeq,
  mergeRuntimeEvent,
  selectedThread,
  setThreads,
}: SessionRuntimeStreamOptions) {
  const runtimeEventsRef = useRef<Record<string, SessionRuntimeEvent[]>>({});
  const runtimeSeqRef = useRef<Record<string, number>>({});
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
