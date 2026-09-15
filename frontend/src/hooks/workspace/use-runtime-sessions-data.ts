// 由 hooks/workspace/use-runtime-sessions-data.ts 机械拆分而来（P0-2），仅搬迁不改语义。
// 对外导出面保持不变，消费方 import 路径零改动；实现见 ./runtime-sessions-data/ 各模块。

import { useEffect, useMemo, useRef, useState } from "react";

import {
  listRuntimeSessionUsers,
  type RuntimeSessionRecord,
  type RuntimeSessionUserSummary,
} from "@/lib/runtime-api";
import { getRuntimeClientIdentity } from "@/lib/runtime-client";
import { normalizeSessionId } from "@/lib/session-id";

import { loadRuntimeSessions } from "./runtime-sessions-data/loading";
import { normalizeRuntimeSessionUsers } from "./runtime-sessions-data/normalize";
import { resolveRuntimeSessionsRetryDelay } from "./runtime-sessions-data/retry";
import { summarizeRuntimeSessions } from "./runtime-sessions-data/sorting";
import {
  getBrowserStorage,
  readStoredRuntimeSessionUserId,
  readStoredRuntimeSessions,
  writeStoredRuntimeSessionUserId,
  writeStoredRuntimeSessions,
} from "./runtime-sessions-data/storage";
import { type RuntimeSessionsDataOptions } from "./runtime-sessions-data/types";
import { chooseRuntimeSessionUserId } from "./runtime-sessions-data/user-selection";

export type { RuntimeSessionsSummary } from "./runtime-sessions-data/types";
export { normalizeRuntimeSessions, normalizeRuntimeSessionUsers } from "./runtime-sessions-data/normalize";
export { sortRuntimeSessions, summarizeRuntimeSessions } from "./runtime-sessions-data/sorting";
export {
  buildStoredRuntimeSessionsKey,
  readStoredRuntimeSessionUserId,
  readStoredRuntimeSessions,
  writeStoredRuntimeSessionUserId,
  writeStoredRuntimeSessions,
} from "./runtime-sessions-data/storage";
export { resolveRuntimeSessionsRetryDelay } from "./runtime-sessions-data/retry";
export { chooseRuntimeSessionUserId } from "./runtime-sessions-data/user-selection";
export {
  loadRuntimeSessions,
  mergePinnedRuntimeSession,
} from "./runtime-sessions-data/loading";

/** 挂载时一次性读取的本地缓存快照（见 `useRuntimeSessionsData` 热路径说明）。 */
type InitialSessionsSnapshot = {
  selectedUserId: string;
  sessions: RuntimeSessionRecord[];
};

export function useRuntimeSessionsData({
  pinnedSessionId,
  userId,
}: RuntimeSessionsDataOptions = {}) {
  // 热路径说明：本 hook 由 workspace 页调用，流式回复期间每帧都会重渲染。原来的
  // 身份解析（localStorage 读 + 正则归一化）与本地缓存读取（186KB JSON.parse +
  // 300+ 会话归一化）直接写在渲染体里，等于**每帧重算一次**。下面分别用 useMemo
  // （跟随 userId 变化）与惰性 ref（挂载时读一次缓存）收敛。
  const fallbackUserId = useMemo(
    () => userId?.trim() || getRuntimeClientIdentity().userId,
    [userId],
  );
  const initialSnapshotRef = useRef<InitialSessionsSnapshot | null>(null);
  if (initialSnapshotRef.current === null) {
    const selectedUserId =
      readStoredRuntimeSessionUserId(getBrowserStorage()) || fallbackUserId;
    initialSnapshotRef.current = {
      selectedUserId,
      sessions: readStoredRuntimeSessions(getBrowserStorage(), selectedUserId),
    };
  }
  const {
    selectedUserId: initialSelectedUserId,
    sessions: initialStoredSessions,
  } = initialSnapshotRef.current;
  const resolvedPinnedSessionId = normalizeSessionId(pinnedSessionId);
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
  // 汇总同样每帧被调用；输入引用不变时 `summarizeRuntimeSessions` 会命中内部记忆。
  const runtimeSessionsSummary = useMemo(
    () => summarizeRuntimeSessions(runtimeSessions),
    [runtimeSessions],
  );

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
