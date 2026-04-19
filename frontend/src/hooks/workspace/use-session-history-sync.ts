import { type Dispatch, type SetStateAction, useEffect } from "react";

import { type Thread } from "@/data/mock";
import {
  getSessionHistory,
  type SessionHistoryResponse,
} from "@/lib/runtime-api";

type SessionHistorySyncOptions = {
  applySessionHistoryToThread: (
    thread: Thread,
    response: SessionHistoryResponse,
  ) => Thread;
  isResponding: boolean;
  selectedThread: Thread | undefined;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
};

export function shouldSyncSessionHistory(
  selectedThread: Thread | undefined,
  isResponding: boolean,
) {
  return Boolean(
    selectedThread?.sessionId &&
      !isResponding &&
      selectedThread.transport !== "error",
  );
}

export function useSessionHistorySync({
  applySessionHistoryToThread,
  isResponding,
  selectedThread,
  setThreads,
}: SessionHistorySyncOptions) {
  const threadId = selectedThread?.id;
  const sessionId = selectedThread?.sessionId;
  const threadTransport = selectedThread?.transport;

  useEffect(() => {
    if (
      !sessionId ||
      !threadId ||
      isResponding ||
      threadTransport === "error"
    ) {
      return;
    }

    let cancelled = false;

    void (async () => {
      try {
        const response = await getSessionHistory(sessionId);
        if (cancelled) {
          return;
        }
        setThreads((current) =>
          current.map((thread) =>
            thread.id === threadId
              ? applySessionHistoryToThread(thread, response)
              : thread,
          ),
        );
      } catch (error) {
        if (cancelled) {
          return;
        }

        const message =
          error instanceof Error ? error.message : "failed to load session history";
        setThreads((current) =>
          current.map((thread) =>
            thread.id === threadId
              ? {
                  ...thread,
                  updatedAt: new Date().toISOString(),
                  transport: "error",
                  lastError: `Session history sync failed: ${message}`,
                }
              : thread,
          ),
        );
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [
    applySessionHistoryToThread,
    isResponding,
    setThreads,
    sessionId,
    threadTransport,
    threadId,
  ]);
}
