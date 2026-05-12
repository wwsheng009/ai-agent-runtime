import { useEffect, useRef, useState } from "react";

import {
  getRuntimeSession,
  listRuntimeSessionUsers,
  listRuntimeSessions,
  type RuntimeSessionRecord,
  type RuntimeSessionUserSummary,
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
const runtimeSessionsSelectedUserStorageKey =
  "workspace.runtime.sessions.selectedUser";
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

export function readStoredRuntimeSessionUserId(
  storage: Storage | null | undefined,
) {
  if (!storage) {
    return "";
  }

  try {
    return storage.getItem(runtimeSessionsSelectedUserStorageKey)?.trim() ?? "";
  } catch {
    return "";
  }
}

export function writeStoredRuntimeSessionUserId(
  storage: Storage | null | undefined,
  userId: string,
) {
  const resolvedUserId = userId.trim();
  if (!storage || !resolvedUserId) {
    return;
  }

  storage.setItem(runtimeSessionsSelectedUserStorageKey, resolvedUserId);
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

export function normalizeRuntimeSessionUsers(
  users: RuntimeSessionUserSummary[] | null | undefined,
) {
  return Array.isArray(users)
    ? users.filter((user) => user.user_id?.trim())
    : [];
}

export function chooseRuntimeSessionUserId(
  users: RuntimeSessionUserSummary[],
  defaultUserId: string | null | undefined,
  currentUserId: string | null | undefined,
  fallbackUserId: string,
) {
  const normalizedUsers = normalizeRuntimeSessionUsers(users);
  const userIds = new Set(normalizedUsers.map((user) => user.user_id.trim()));
  const current = currentUserId?.trim() ?? "";
  if (current && userIds.has(current)) {
    return current;
  }

  const defaultUser = defaultUserId?.trim() ?? "";
  if (
    defaultUser &&
    normalizedUsers.some(
      (user) => user.user_id.trim() === defaultUser && (user.session_count ?? 0) > 0,
    )
  ) {
    return defaultUser;
  }

  const withSessions = normalizedUsers.filter((user) => (user.session_count ?? 0) > 0);
  if (withSessions.length > 0) {
    return [...withSessions].sort((left, right) => {
      const leftTime = Date.parse(left.latest_updated_at ?? "");
      const rightTime = Date.parse(right.latest_updated_at ?? "");
      if (Number.isFinite(leftTime) && Number.isFinite(rightTime) && leftTime !== rightTime) {
        return rightTime - leftTime;
      }
      if (Number.isFinite(leftTime) && !Number.isFinite(rightTime)) {
        return -1;
      }
      if (!Number.isFinite(leftTime) && Number.isFinite(rightTime)) {
        return 1;
      }
      if ((left.session_count ?? 0) !== (right.session_count ?? 0)) {
        return (right.session_count ?? 0) - (left.session_count ?? 0);
      }
      return left.user_id.localeCompare(right.user_id);
    })[0]?.user_id.trim() ?? fallbackUserId;
  }

  if (defaultUser) {
    return defaultUser;
  }

  return fallbackUserId.trim();
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
  const fallbackUserId = userId?.trim() || getRuntimeClientIdentity().userId;
  const initialSelectedUserId =
    readStoredRuntimeSessionUserId(getBrowserStorage()) || fallbackUserId;
  const resolvedPinnedSessionId = pinnedSessionId?.trim();
  const initialStoredSessions = readStoredRuntimeSessions(
    getBrowserStorage(),
    initialSelectedUserId,
  );
  const [reloadToken, setReloadToken] = useState(0);
  const retryTimeoutRef = useRef<number | null>(null);
  const retryAttemptRef = useRef(0);
  const runtimeSessionsRef = useRef<RuntimeSessionRecord[]>(initialStoredSessions);
  const runtimeSessionsRefreshingRef = useRef(false);
  const sessionUsersRef = useRef<RuntimeSessionUserSummary[]>([]);
  const [runtimeSessionUsers, setRuntimeSessionUsers] = useState<
    RuntimeSessionUserSummary[]
  >([]);
  const [runtimeSessionUsersError, setRuntimeSessionUsersError] =
    useState<string | null>(null);
  const [runtimeSessionUsersLoading, setRuntimeSessionUsersLoading] = useState(true);
  const [runtimeSessionDefaultUserId, setRuntimeSessionDefaultUserId] = useState("");
  const [selectedRuntimeSessionUserId, setSelectedRuntimeSessionUserIdState] =
    useState(initialSelectedUserId);
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
    sessionUsersRef.current = runtimeSessionUsers;
  }, [runtimeSessionUsers]);

  useEffect(() => {
    let cancelled = false;
    setRuntimeSessionUsersLoading(sessionUsersRef.current.length === 0);

    void (async () => {
      try {
        const response = await listRuntimeSessionUsers();
        if (cancelled) {
          return;
        }
        const users = normalizeRuntimeSessionUsers(response.users);
        const defaultUserId = response.default_user_id?.trim() ?? "";
        setRuntimeSessionUsers(users);
        setRuntimeSessionDefaultUserId(defaultUserId);
        setRuntimeSessionUsersError(null);
        setSelectedRuntimeSessionUserIdState((currentUserId) => {
          const nextUserId = chooseRuntimeSessionUserId(
            users,
            defaultUserId,
            currentUserId,
            fallbackUserId,
          );
          if (nextUserId) {
            writeStoredRuntimeSessionUserId(getBrowserStorage(), nextUserId);
          }
          return nextUserId || currentUserId || fallbackUserId;
        });
      } catch (error) {
        if (cancelled) {
          return;
        }
        setRuntimeSessionUsersError(
          error instanceof Error ? error.message : "failed to load runtime session users",
        );
        setSelectedRuntimeSessionUserIdState((currentUserId) =>
          currentUserId.trim() || fallbackUserId,
        );
      } finally {
        if (!cancelled) {
          setRuntimeSessionUsersLoading(false);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [fallbackUserId, reloadToken]);

  useEffect(() => {
    if (!selectedRuntimeSessionUserId.trim()) {
      return;
    }
    const cachedSessions = readStoredRuntimeSessions(
      getBrowserStorage(),
      selectedRuntimeSessionUserId,
    );
    setRuntimeSessions(cachedSessions);
    setRuntimeSessionsLoading(cachedSessions.length === 0);
    setRuntimeSessionsError(null);
    retryAttemptRef.current = 0;
    clearPendingRetry();

    return () => {
      clearPendingRetry();
    };
  }, [selectedRuntimeSessionUserId]);

  useEffect(() => {
    let cancelled = false;
    const userIdForRequest = selectedRuntimeSessionUserId.trim();
    if (!userIdForRequest) {
      setRuntimeSessions([]);
      setRuntimeSessionsLoading(false);
      return () => {
        cancelled = true;
      };
    }

    void (async () => {
      if (!runtimeSessionsRefreshingRef.current) {
        setRuntimeSessionsLoading(runtimeSessionsRef.current.length === 0);
      }

      try {
        const sessions = await loadRuntimeSessions(
          userIdForRequest,
          resolvedPinnedSessionId,
        );
        if (cancelled) {
          return;
        }
        setRuntimeSessions(sessions);
        writeStoredRuntimeSessions(getBrowserStorage(), userIdForRequest, sessions);
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
    selectedRuntimeSessionUserId,
  ]);

  function selectRuntimeSessionUserId(userId: string) {
    const nextUserId = userId.trim();
    if (!nextUserId) {
      return;
    }
    writeStoredRuntimeSessionUserId(getBrowserStorage(), nextUserId);
    clearPendingRetry();
    retryAttemptRef.current = 0;
    setRuntimeSessionsRefreshing(true);
    setRuntimeSessionsError(null);
    setSelectedRuntimeSessionUserIdState(nextUserId);
  }

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
    runtimeSessionDefaultUserId,
    runtimeSessionUsers,
    runtimeSessionUsersError,
    runtimeSessionUsersLoading,
    selectedRuntimeSessionUserId,
    selectRuntimeSessionUserId,
  };
}
