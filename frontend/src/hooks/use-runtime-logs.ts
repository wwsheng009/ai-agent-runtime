import {
  useDeferredValue,
  useEffect,
  useEffectEvent,
  useRef,
  useState,
  type SetStateAction,
} from "react";

import {
  listRuntimeLogs,
  streamRuntimeLogs,
  type RuntimeLogEntry,
} from "@/lib/runtime-api";
import type { RuntimeLogLevelFilter } from "@/pages/logs-page-shared";

const runtimeLogAdminTokenStorageKey = "runtime.logs.adminToken";
const maxVisibleRuntimeLogs = 1000;

export type RuntimeLogsConnectionState =
  | "idle"
  | "connecting"
  | "open"
  | "reconnecting"
  | "error";

type RuntimeLogStreamSeed = {
  cursor: number;
  version: number;
};

type UseRuntimeLogsOptions = {
  follow: boolean;
  level: RuntimeLogLevelFilter;
  onSelectedCursorChange: (value: SetStateAction<number | null>) => void;
  query: string;
  selectedCursor: number | null;
};

function getBrowserStorage() {
  if (typeof window === "undefined") {
    return null;
  }
  return window.localStorage;
}

function readStoredRuntimeLogAdminToken(
  storage: Storage | null | undefined,
) {
  if (!storage) {
    return "";
  }
  return storage.getItem(runtimeLogAdminTokenStorageKey)?.trim() ?? "";
}

function writeStoredRuntimeLogAdminToken(
  storage: Storage | null | undefined,
  value: string,
) {
  if (!storage) {
    return;
  }
  const normalizedValue = value.trim();
  if (!normalizedValue) {
    storage.removeItem(runtimeLogAdminTokenStorageKey);
    return;
  }
  storage.setItem(runtimeLogAdminTokenStorageKey, normalizedValue);
}

function isAbortError(error: unknown) {
  return error instanceof Error && error.name === "AbortError";
}

function mergeRuntimeLogEntry(
  entries: RuntimeLogEntry[],
  incomingEntry: RuntimeLogEntry,
) {
  const nextEntries = [
    incomingEntry,
    ...entries.filter((entry) => entry.cursor !== incomingEntry.cursor),
  ];
  if (nextEntries.length <= maxVisibleRuntimeLogs) {
    return nextEntries;
  }
  return nextEntries.slice(0, maxVisibleRuntimeLogs);
}

export function useRuntimeLogs({
  follow,
  level,
  onSelectedCursorChange,
  query,
  selectedCursor,
}: UseRuntimeLogsOptions) {
  const [adminToken, setAdminTokenState] = useState(() =>
    readStoredRuntimeLogAdminToken(getBrowserStorage()),
  );
  const [entries, setEntries] = useState<RuntimeLogEntry[]>([]);
  const [filePath, setFilePath] = useState("");
  const [logFileExists, setLogFileExists] = useState(true);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [streamError, setStreamError] = useState<string | null>(null);
  const [connectionState, setConnectionState] =
    useState<RuntimeLogsConnectionState>("idle");
  const [reloadKey, setReloadKey] = useState(0);
  const [streamSeed, setStreamSeed] = useState<RuntimeLogStreamSeed | null>(null);

  const deferredQuery = useDeferredValue(query.trim());
  const followRef = useRef(follow);
  const hasLoadedOnceRef = useRef(false);
  const updateSelectedCursor = useEffectEvent(
    (value: SetStateAction<number | null>) => {
      onSelectedCursorChange(value);
    },
  );

  useEffect(() => {
    followRef.current = follow;
  }, [follow]);

  useEffect(() => {
    writeStoredRuntimeLogAdminToken(getBrowserStorage(), adminToken);
  }, [adminToken]);

  useEffect(() => {
    let cancelled = false;

    if (hasLoadedOnceRef.current) {
      setRefreshing(true);
    } else {
      setLoading(true);
    }
    setError(null);
    setStreamError(null);

    void (async () => {
      try {
        const response = await listRuntimeLogs({
          adminToken,
          level: level || undefined,
          limit: 200,
          query: deferredQuery || undefined,
        });
        if (cancelled) {
          return;
        }

        setEntries(response.entries);
        setFilePath(response.file_path ?? "");
        setLogFileExists(response.exists ?? true);
        updateSelectedCursor((currentCursor) =>
          response.entries.some((entry) => entry.cursor === currentCursor)
            ? currentCursor
            : (response.entries[0]?.cursor ?? null),
        );
        setStreamSeed({
          cursor: response.next_cursor ?? 0,
          version: Date.now(),
        });
        setConnectionState("idle");
      } catch (loadError) {
        if (cancelled) {
          return;
        }
        setEntries([]);
        updateSelectedCursor(null);
        setStreamSeed(null);
        setConnectionState("error");
        setError(
          loadError instanceof Error
            ? loadError.message
            : "failed to load runtime logs",
        );
      } finally {
        if (!cancelled) {
          setLoading(false);
          setRefreshing(false);
          hasLoadedOnceRef.current = true;
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [adminToken, deferredQuery, level, reloadKey]);

  useEffect(() => {
    if (loading || error || !streamSeed) {
      return;
    }

    let cancelled = false;
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
    let cursor = streamSeed.cursor;
    const controller = new AbortController();

    const scheduleReconnect = () => {
      if (cancelled || controller.signal.aborted) {
        return;
      }
      setConnectionState("reconnecting");
      reconnectTimer = setTimeout(() => {
        void connectStream(true);
      }, 1500);
    };

    const connectStream = async (isReconnect: boolean) => {
      if (cancelled || controller.signal.aborted) {
        return;
      }

      setConnectionState(isReconnect ? "reconnecting" : "connecting");
      setStreamError(null);

      try {
        await streamRuntimeLogs({
          adminToken,
          after: cursor,
          level: level || undefined,
          onLog: (entry) => {
            cursor = Math.max(cursor, entry.cursor);
            setEntries((currentEntries) =>
              mergeRuntimeLogEntry(currentEntries, entry),
            );
            updateSelectedCursor((currentCursor) =>
              followRef.current || currentCursor === null
                ? entry.cursor
                : currentCursor,
            );
          },
          onOpen: () => {
            setConnectionState("open");
          },
          onReady: (payload) => {
            if (typeof payload.cursor === "number") {
              cursor = payload.cursor;
            }
            if (typeof payload.exists === "boolean") {
              setLogFileExists(payload.exists);
            }
            if (payload.file_path?.trim()) {
              setFilePath(payload.file_path.trim());
            }
          },
          onReset: () => {
            controller.abort();
            setReloadKey((currentValue) => currentValue + 1);
          },
          pollMs: 750,
          query: deferredQuery || undefined,
          signal: controller.signal,
        });

        if (cancelled || controller.signal.aborted) {
          return;
        }
        scheduleReconnect();
      } catch (streamFailure) {
        if (cancelled || controller.signal.aborted || isAbortError(streamFailure)) {
          return;
        }

        setStreamError(
          streamFailure instanceof Error
            ? streamFailure.message
            : "failed to stream runtime logs",
        );
        setConnectionState("error");
        scheduleReconnect();
      }
    };

    void connectStream(false);

    return () => {
      cancelled = true;
      controller.abort();
      if (reconnectTimer) {
        clearTimeout(reconnectTimer);
      }
    };
  }, [adminToken, deferredQuery, error, level, loading, streamSeed]);

  const selectedEntry =
    entries.find((entry) => entry.cursor === selectedCursor) ?? entries[0] ?? null;

  function refresh() {
    setReloadKey((currentValue) => currentValue + 1);
  }

  function setAdminToken(value: string) {
    setAdminTokenState(value);
  }

  return {
    adminToken,
    connectionState,
    entries,
    error,
    filePath,
    loading,
    logFileExists,
    refreshing,
    refresh,
    selectedEntry,
    setAdminToken,
    streamError,
  };
}
