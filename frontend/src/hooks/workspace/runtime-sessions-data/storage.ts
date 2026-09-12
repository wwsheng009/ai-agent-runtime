// 由 hooks/workspace/use-runtime-sessions-data.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type RuntimeSessionRecord } from "@/lib/runtime-api";

import { normalizeRuntimeSessions } from "./normalize";
import { sortRuntimeSessions } from "./sorting";
import { type StoredRuntimeSessionsPayload } from "./types";

const runtimeSessionsStorageKeyPrefix = "workspace.runtime.sessions";
const runtimeSessionsSelectedUserStorageKey =
  "workspace.runtime.sessions.selectedUser";

export function getBrowserStorage() {
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
