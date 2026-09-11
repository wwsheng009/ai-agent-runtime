import { useEffect, useRef, useState } from "react";

import {
  createWorkspaceDirectory,
  deleteWorkspaceDirectory,
  listWorkspaceDirectories,
  updateWorkspaceDirectory,
  type RuntimeWorkspaceDirectory,
} from "@/lib/runtime-api";

const directoriesRetryDelaysMs = [1200, 2500, 5000, 8000];

export function resolveDirectoriesRetryDelay(attempt: number) {
  if (attempt <= 0) {
    return directoriesRetryDelaysMs[0];
  }

  return directoriesRetryDelaysMs[
    Math.min(attempt, directoriesRetryDelaysMs.length - 1)
  ];
}

function normalizeDirectories(
  directories: RuntimeWorkspaceDirectory[] | null | undefined,
) {
  if (!Array.isArray(directories)) {
    return [];
  }
  const normalized: RuntimeWorkspaceDirectory[] = [];
  const seen = new Set<string>();
  for (const directory of directories) {
    const id = directory?.id?.trim();
    const path = directory?.path?.trim();
    if (!directory || !id || !path || seen.has(id)) {
      continue;
    }
    seen.add(id);
    normalized.push({ ...directory, id, path });
  }
  return normalized;
}

type UseRuntimeWorkspaceDirectoriesResult = {
  directories: RuntimeWorkspaceDirectory[];
  loading: boolean;
  refreshing: boolean;
  error: string | null;
  refresh: () => void;
  /** Fails with the backend message (e.g. directory missing → 400). */
  addDirectory: (
    path: string,
    name?: string,
  ) => Promise<RuntimeWorkspaceDirectory>;
  renameDirectory: (directoryId: string, name: string) => Promise<void>;
  removeDirectory: (directoryId: string) => Promise<void>;
};

export function useRuntimeWorkspaceDirectories(): UseRuntimeWorkspaceDirectoriesResult {
  const [directories, setDirectories] = useState<RuntimeWorkspaceDirectory[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reloadToken, setReloadToken] = useState(0);

  const directoriesRef = useRef<RuntimeWorkspaceDirectory[]>([]);
  const refreshingRef = useRef(false);
  const retryAttemptRef = useRef(0);
  const retryTimeoutRef = useRef<number | null>(null);

  directoriesRef.current = directories;

  function clearPendingRetry() {
    if (retryTimeoutRef.current !== null) {
      window.clearTimeout(retryTimeoutRef.current);
      retryTimeoutRef.current = null;
    }
  }

  function scheduleReloadToken() {
    setRefreshing(true);
    setReloadToken((current) => current + 1);
  }

  useEffect(() => {
    let cancelled = false;

    void (async () => {
      if (!refreshingRef.current) {
        setLoading(directoriesRef.current.length === 0);
      }

      try {
        const response = await listWorkspaceDirectories();
        if (cancelled) {
          return;
        }
        setDirectories(normalizeDirectories(response.directories));
        setError(null);
        retryAttemptRef.current = 0;
        clearPendingRetry();
      } catch (loadError) {
        if (cancelled) {
          return;
        }
        setError(
          loadError instanceof Error
            ? loadError.message
            : "failed to load workspace directories",
        );

        clearPendingRetry();
        const delay = resolveDirectoriesRetryDelay(retryAttemptRef.current);
        retryAttemptRef.current += 1;
        retryTimeoutRef.current = window.setTimeout(() => {
          scheduleReloadToken();
        }, delay);
      } finally {
        if (!cancelled) {
          setLoading(false);
          setRefreshing(false);
          refreshingRef.current = false;
        }
      }
    })();

    return () => {
      cancelled = true;
      clearPendingRetry();
    };
  }, [reloadToken]);

  async function addDirectory(
    path: string,
    name?: string,
  ): Promise<RuntimeWorkspaceDirectory> {
    const response = await createWorkspaceDirectory({
      path: path.trim(),
      name: name?.trim() || undefined,
    });
    scheduleReloadToken();
    return response.directory;
  }

  async function renameDirectory(directoryId: string, name: string) {
    await updateWorkspaceDirectory(directoryId, { name: name.trim() });
    scheduleReloadToken();
  }

  async function removeDirectory(directoryId: string) {
    await deleteWorkspaceDirectory(directoryId);
    scheduleReloadToken();
  }

  function refresh() {
    clearPendingRetry();
    retryAttemptRef.current = 0;
    refreshingRef.current = true;
    setError(null);
    scheduleReloadToken();
  }

  return {
    directories,
    loading,
    refreshing,
    error,
    refresh,
    addDirectory,
    renameDirectory,
    removeDirectory,
  };
}
