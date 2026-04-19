import { useEffect, useRef, useState } from "react";

import {
  getRuntimeSession,
  listRuntimeSessions,
  type RuntimeSessionRecord,
} from "@/lib/runtime-api";
import { getRuntimeClientIdentity } from "@/lib/runtime-client";

type RuntimeSessionsDataOptions = {
  pinnedSessionId?: string;
  userId?: string;
};

export type RuntimeSessionsSummary = {
  activeCount: number;
  archivedCount: number;
  latestSessionId?: string;
  latestUpdatedAt?: string;
  recoverableCount: number;
  totalCount: number;
};

type StoredRuntimeSessionsPayload = {
  sessions: RuntimeSessionRecord[];
  storedAt: string;
  userId: string;
};

const runtimeSessionsStorageKeyPrefix = "workspace.runtime.sessions";
const runtimeSessionsRetryDelaysMs = [1200, 2500, 5000, 8000];

function getBrowserStorage() {
  if (typeof window === "undefined") {
    return null;
  }

  return window.localStorage;
}

export function buildStoredRuntimeSessionsKey(userId: string) {
  const resolvedUserId = userId.trim();
  if (!resolvedUserId) {
    return runtimeSessionsStorageKeyPrefix;
  }

  return `${runtimeSessionsStorageKeyPrefix}:${encodeURIComponent(resolvedUserId)}`;
}

export function readStoredRuntimeSessions(
  storage: Storage | null | undefined,
  userId: string,
) {
  const resolvedUserId = userId.trim();
  if (!storage || !resolvedUserId) {
    return [] as RuntimeSessionRecord[];
  }

  try {
    const raw = storage.getItem(buildStoredRuntimeSessionsKey(resolvedUserId));
    if (!raw) {
      return [];
    }

    const parsed = JSON.parse(raw) as Partial<StoredRuntimeSessionsPayload>;
    return normalizeRuntimeSessions(parsed.sessions);
  } catch {
    return [];
  }
}

export function writeStoredRuntimeSessions(
  storage: Storage | null | undefined,
  userId: string,
  sessions: RuntimeSessionRecord[],
) {
  const resolvedUserId = userId.trim();
  if (!storage || !resolvedUserId) {
    return;
  }

  const payload: StoredRuntimeSessionsPayload = {
    sessions: sortRuntimeSessions(normalizeRuntimeSessions(sessions)),
    storedAt: new Date().toISOString(),
    userId: resolvedUserId,
  };
  storage.setItem(
    buildStoredRuntimeSessionsKey(resolvedUserId),
    JSON.stringify(payload),
  );
}

export function resolveRuntimeSessionsRetryDelay(attempt: number) {
  if (attempt <= 0) {
    return runtimeSessionsRetryDelaysMs[0];
  }

  return runtimeSessionsRetryDelaysMs[
    Math.min(attempt, runtimeSessionsRetryDelaysMs.length - 1)
  ];
}

export function sortRuntimeSessions(sessions: RuntimeSessionRecord[]) {
  return [...sessions].sort((left, right) => {
    const leftTime = Date.parse(left.updatedAt || left.createdAt || "");
    const rightTime = Date.parse(right.updatedAt || right.createdAt || "");

    if (Number.isFinite(leftTime) && Number.isFinite(rightTime) && leftTime !== rightTime) {
      return rightTime - leftTime;
    }

    return left.id.localeCompare(right.id);
  });
}

export function summarizeRuntimeSessions(
  sessions: RuntimeSessionRecord[],
): RuntimeSessionsSummary {
  const sorted = sortRuntimeSessions(sessions);
  const activeCount = sorted.filter((session) => {
    const state = (session.state || "").trim().toLowerCase();
    return state === "" || state === "active" || state === "running" || state === "idle";
  }).length;
  const archivedCount = sorted.filter((session) => {
    const state = (session.state || "").trim().toLowerCase();
    return state === "archived" || state === "closed";
  }).length;

  return {
    activeCount,
    archivedCount,
    latestSessionId: sorted[0]?.id,
    latestUpdatedAt: sorted[0]?.updatedAt || sorted[0]?.createdAt,
    recoverableCount: activeCount,
    totalCount: sorted.length,
  };
}

export function normalizeRuntimeSessions(
  sessions: RuntimeSessionRecord[] | null | undefined,
) {
  return Array.isArray(sessions) ? sessions : [];
}

export function mergePinnedRuntimeSession(
  sessions: RuntimeSessionRecord[],
  pinnedSession: RuntimeSessionRecord | null | undefined,
) {
  if (!pinnedSession) {
    return sessions;
  }

  if (sessions.some((session) => session.id === pinnedSession.id)) {
    return sessions;
  }

  return [...sessions, pinnedSession];
}

export async function loadRuntimeSessions(
  userId: string,
  pinnedSessionId?: string,
) {
  const response = await listRuntimeSessions({ userId });
  const listedSessions = normalizeRuntimeSessions(response.sessions);
  const resolvedPinnedSessionId = pinnedSessionId?.trim();

  if (
    !resolvedPinnedSessionId ||
    listedSessions.some((session) => session.id === resolvedPinnedSessionId)
  ) {
    return sortRuntimeSessions(listedSessions);
  }

  try {
    const response = await getRuntimeSession(resolvedPinnedSessionId);
    return sortRuntimeSessions(
      mergePinnedRuntimeSession(listedSessions, response.session),
    );
  } catch {
    return sortRuntimeSessions(listedSessions);
  }
}

export function useRuntimeSessionsData({
  pinnedSessionId,
  userId,
}: RuntimeSessionsDataOptions = {}) {
  const resolvedUserId = userId?.trim() || getRuntimeClientIdentity().userId;
  const resolvedPinnedSessionId = pinnedSessionId?.trim();
  const initialStoredSessions = readStoredRuntimeSessions(
    getBrowserStorage(),
    resolvedUserId,
  );
  const [reloadToken, setReloadToken] = useState(0);
  const retryTimeoutRef = useRef<number | null>(null);
  const retryAttemptRef = useRef(0);
  const runtimeSessionsRef = useRef<RuntimeSessionRecord[]>(initialStoredSessions);
  const runtimeSessionsRefreshingRef = useRef(false);
  const [runtimeSessions, setRuntimeSessions] = useState<RuntimeSessionRecord[]>(
    initialStoredSessions,
  );
  const [runtimeSessionsError, setRuntimeSessionsError] = useState<string | null>(null);
  const [runtimeSessionsLoading, setRuntimeSessionsLoading] = useState(
    initialStoredSessions.length === 0,
  );
  const [runtimeSessionsRefreshing, setRuntimeSessionsRefreshing] = useState(false);
  const runtimeSessionsSummary = summarizeRuntimeSessions(runtimeSessions);

  function clearPendingRetry() {
    if (retryTimeoutRef.current !== null && typeof window !== "undefined") {
      window.clearTimeout(retryTimeoutRef.current);
    }
    retryTimeoutRef.current = null;
  }

  useEffect(() => {
    runtimeSessionsRef.current = runtimeSessions;
  }, [runtimeSessions]);

  useEffect(() => {
    runtimeSessionsRefreshingRef.current = runtimeSessionsRefreshing;
  }, [runtimeSessionsRefreshing]);

  useEffect(() => {
    const cachedSessions = readStoredRuntimeSessions(
      getBrowserStorage(),
      resolvedUserId,
    );
    setRuntimeSessions(cachedSessions);
    setRuntimeSessionsLoading(cachedSessions.length === 0);
    setRuntimeSessionsError(null);
    retryAttemptRef.current = 0;
    clearPendingRetry();

    return () => {
      clearPendingRetry();
    };
  }, [resolvedUserId]);

  useEffect(() => {
    let cancelled = false;

    void (async () => {
      if (!runtimeSessionsRefreshingRef.current) {
        setRuntimeSessionsLoading(runtimeSessionsRef.current.length === 0);
      }

      try {
        const sessions = await loadRuntimeSessions(
          resolvedUserId,
          resolvedPinnedSessionId,
        );
        if (cancelled) {
          return;
        }
        setRuntimeSessions(sessions);
        writeStoredRuntimeSessions(getBrowserStorage(), resolvedUserId, sessions);
        setRuntimeSessionsError(null);
        retryAttemptRef.current = 0;
        clearPendingRetry();
      } catch (error) {
        if (cancelled) {
          return;
        }
        setRuntimeSessionsError(
          error instanceof Error ? error.message : "failed to load runtime sessions",
        );

        clearPendingRetry();
        if (typeof window !== "undefined") {
          const delay = resolveRuntimeSessionsRetryDelay(retryAttemptRef.current);
          retryAttemptRef.current += 1;
          retryTimeoutRef.current = window.setTimeout(() => {
            setReloadToken((current) => current + 1);
          }, delay);
        }
      } finally {
        if (!cancelled) {
          setRuntimeSessionsLoading(false);
          setRuntimeSessionsRefreshing(false);
        }
      }
    })();

    return () => {
      cancelled = true;
      clearPendingRetry();
    };
  }, [
    reloadToken,
    resolvedPinnedSessionId,
    resolvedUserId,
  ]);

  function refreshRuntimeSessions() {
    clearPendingRetry();
    retryAttemptRef.current = 0;
    setRuntimeSessionsRefreshing(true);
    setRuntimeSessionsError(null);
    setReloadToken((current) => current + 1);
  }

  return {
    refreshRuntimeSessions,
    runtimeSessions,
    runtimeSessionsError,
    runtimeSessionsLoading,
    runtimeSessionsRefreshing,
    runtimeSessionsSummary,
  };
}
