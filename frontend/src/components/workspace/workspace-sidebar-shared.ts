import { type Thread } from "@/data/mock";
import {
  type RuntimeSessionRecord,
  type RuntimeWorkspaceDirectory,
} from "@/types/runtime";

export type ThreadSessionDescriptor = {
  detail: string;
  label: string;
  tone: string;
};

export type ThreadSessionDetailLabels = {
  pending: string;
  error: string;
  restored: string;
  attached: string;
};

export type SessionRailSummary = {
  attachedCount: number;
  errorCount: number;
  pendingCount: number;
  recentRecoverableThreads: Thread[];
  restoredCount: number;
};

export type RuntimeSessionDirectoryGroup = {
  key: string;
  label: string;
  fullPath: string;
  sessions: RuntimeSessionRecord[];
  latestUpdatedAt?: string;
};

export type MergedDirectoryGroup = {
  /** Registered groups use directory.id; derived groups use the path key. */
  key: string;
  directoryId?: string;
  label: string;
  fullPath: string;
  registered: boolean;
  exists?: boolean;
  sessions: RuntimeSessionRecord[];
  latestUpdatedAt?: string;
};

const unknownRuntimeSessionDirectory = "__runtime-session-directory-unknown__";

const defaultThreadSessionDetailLabels: ThreadSessionDetailLabels = {
  pending: "No runtime session attached yet.",
  error: "The session exists, but the latest sync failed and needs another restore attempt.",
  restored: "Recovered from runtime session history and ready to continue.",
  attached: "Attached to a live runtime session from the active workspace flow.",
};

export function describeThreadSession(
  thread: Thread,
  labels: ThreadSessionDetailLabels = defaultThreadSessionDetailLabels,
): ThreadSessionDescriptor {
  if (!thread.sessionId) {
    return {
      detail: labels.pending,
      label: "pending",
      tone: "border-white/10 bg-white/6 text-[var(--muted-foreground)]",
    };
  }

  if (thread.transport === "error") {
    return {
      detail: labels.error,
      label: "error",
      tone: "border-[#f59e7d]/24 bg-[#f59e7d]/10 text-[#f59e7d]",
    };
  }

  if (thread.tags.includes("runtime-session") || thread.tags.includes("restored")) {
    return {
      detail: labels.restored,
      label: "restored",
      tone: "border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#8fd0c6]",
    };
  }

  return {
    detail: labels.attached,
    label: "attached",
    tone: "border-[#f0c77b]/24 bg-[#f0c77b]/10 text-[#f0c77b]",
  };
}

export function summarizeSidebarSessions(threads: Thread[]): SessionRailSummary {
  const attachedThreads = threads.filter((thread) => Boolean(thread.sessionId));
  const restoredThreads = attachedThreads.filter(
    (thread) => thread.tags.includes("runtime-session") || thread.tags.includes("restored"),
  );

  return {
    attachedCount: attachedThreads.length,
    errorCount: attachedThreads.filter((thread) => thread.transport === "error").length,
    pendingCount: threads.length - attachedThreads.length,
    recentRecoverableThreads: [...attachedThreads]
      .sort((left, right) => Date.parse(right.updatedAt) - Date.parse(left.updatedAt))
      .slice(0, 3),
    restoredCount: restoredThreads.length,
  };
}

export function groupRuntimeSessionsByDirectory(
  sessions: RuntimeSessionRecord[],
): RuntimeSessionDirectoryGroup[] {
  const groups = new Map<string, RuntimeSessionDirectoryGroup>();

  for (const session of sessions) {
    const directory = resolveRuntimeSessionDirectory(session);
    const group = groups.get(directory.key) ?? {
      key: directory.key,
      label: directory.label,
      fullPath: directory.fullPath,
      sessions: [],
      latestUpdatedAt: undefined,
    };
    group.sessions.push(session);
    const updatedAt = session.updatedAt || session.createdAt;
    if (
      updatedAt &&
      (!group.latestUpdatedAt ||
        Date.parse(updatedAt) > Date.parse(group.latestUpdatedAt))
    ) {
      group.latestUpdatedAt = updatedAt;
    }
    groups.set(directory.key, group);
  }

  return [...groups.values()]
    .map((group) => ({
      ...group,
      sessions: [...group.sessions].sort(compareRuntimeSessionsByUpdated),
    }))
    .sort(compareGroupsByLatestUpdated);
}

export function resolveRuntimeSessionDirectory(session: RuntimeSessionRecord) {
  const rawPath = readRuntimeSessionDirectoryPath(session);
  if (!rawPath) {
    return {
      key: unknownRuntimeSessionDirectory,
      label: "Unscoped sessions",
      fullPath: "",
    };
  }

  const normalizedPath = normalizeRuntimeDirectoryPath(rawPath);
  return {
    key: normalizedPath.toLowerCase(),
    label: runtimeDirectoryBaseName(normalizedPath),
    fullPath: normalizedPath,
  };
}

function readRuntimeSessionDirectoryPath(session: RuntimeSessionRecord) {
  const context = session.metadata?.context;
  if (!context || typeof context !== "object") {
    return "";
  }

  return readFirstContextText(
    context,
    "workspace_path",
    "workspacePath",
    "cwd",
    "workdir",
    "working_dir",
    "profile_root",
    "profileRoot",
    "aicli_profile_root",
  );
}

function readFirstContextText(
  context: Record<string, unknown>,
  ...keys: string[]
) {
  for (const key of keys) {
    const value = context[key];
    if (typeof value === "string" && value.trim()) {
      return value.trim();
    }
  }
  return "";
}

function normalizeRuntimeDirectoryPath(value: string) {
  return value.trim().replace(/\\/g, "/").replace(/\/+$/, "") || value.trim();
}

function runtimeDirectoryBaseName(value: string) {
  const normalized = normalizeRuntimeDirectoryPath(value);
  if (normalized === "/" || /^[A-Za-z]:$/.test(normalized)) {
    return normalized;
  }
  return normalized.split("/").filter(Boolean).pop() || normalized;
}

function compareRuntimeSessionsByUpdated(
  left: RuntimeSessionRecord,
  right: RuntimeSessionRecord,
) {
  const leftTime = Date.parse(left.updatedAt || left.createdAt || "");
  const rightTime = Date.parse(right.updatedAt || right.createdAt || "");
  if (Number.isFinite(leftTime) && Number.isFinite(rightTime) && leftTime !== rightTime) {
    return rightTime - leftTime;
  }
  return left.id.localeCompare(right.id);
}

function compareGroupsByLatestUpdated(
  left: { label: string; latestUpdatedAt?: string },
  right: { label: string; latestUpdatedAt?: string },
) {
  const leftTime = Date.parse(left.latestUpdatedAt ?? "");
  const rightTime = Date.parse(right.latestUpdatedAt ?? "");
  if (Number.isFinite(leftTime) && Number.isFinite(rightTime) && leftTime !== rightTime) {
    return rightTime - leftTime;
  }
  if (Number.isFinite(leftTime) && !Number.isFinite(rightTime)) {
    return -1;
  }
  if (!Number.isFinite(leftTime) && Number.isFinite(rightTime)) {
    return 1;
  }
  return left.label.localeCompare(right.label);
}

/**
 * Merges registered workspace directories with session-derived groups.
 *
 * Matching is exact on the normalized path key (case-folded, mirroring the
 * backend registry): sessions created in sub-directories stay in their own
 * derived groups instead of being swallowed by a registered parent.
 * Registered groups always appear (even with zero sessions); pathless
 * sessions keep the "Unscoped sessions" bucket.
 */
export function mergeDirectoryGroups(
  directories: RuntimeWorkspaceDirectory[],
  sessions: RuntimeSessionRecord[],
): MergedDirectoryGroup[] {
  const registeredByKey = new Map<string, MergedDirectoryGroup>();
  const registeredOrder = new Map<string, number>();

  for (const directory of directories) {
    const id = directory.id?.trim();
    const fullPath = normalizeRuntimeDirectoryPath(directory.path || "");
    if (!id || !fullPath) {
      continue;
    }
    const pathKey = fullPath.toLowerCase();
    if (registeredByKey.has(pathKey)) {
      continue;
    }
    registeredByKey.set(pathKey, {
      key: id,
      directoryId: id,
      label: directory.name?.trim() || runtimeDirectoryBaseName(fullPath),
      fullPath,
      registered: true,
      exists: directory.exists,
      sessions: [],
      latestUpdatedAt: undefined,
    });
    registeredOrder.set(
      id,
      directory.last_used_at ?? directory.created_at ?? 0,
    );
  }

  const derivedGroups = new Map<string, MergedDirectoryGroup>();

  for (const session of sessions) {
    const resolved = resolveRuntimeSessionDirectory(session);
    let group = registeredByKey.get(resolved.key);
    if (!group) {
      group = derivedGroups.get(resolved.key);
      if (!group) {
        group = {
          key: resolved.key,
          label: resolved.label,
          fullPath: resolved.fullPath,
          registered: false,
          sessions: [],
          latestUpdatedAt: undefined,
        };
        derivedGroups.set(resolved.key, group);
      }
    }
    group.sessions.push(session);
    const updatedAt = session.updatedAt || session.createdAt;
    if (
      updatedAt &&
      (!group.latestUpdatedAt ||
        Date.parse(updatedAt) > Date.parse(group.latestUpdatedAt))
    ) {
      group.latestUpdatedAt = updatedAt;
    }
  }

  const registeredGroups = [...registeredByKey.values()]
    .map((group) => ({
      ...group,
      sessions: [...group.sessions].sort(compareRuntimeSessionsByUpdated),
    }))
    .sort((left, right) => {
      const leftOrder = registeredOrder.get(left.key) ?? 0;
      const rightOrder = registeredOrder.get(right.key) ?? 0;
      if (leftOrder !== rightOrder) {
        return rightOrder - leftOrder;
      }
      return left.label.localeCompare(right.label);
    });

  const derived = [...derivedGroups.values()]
    .map((group) => ({
      ...group,
      sessions: [...group.sessions].sort(compareRuntimeSessionsByUpdated),
    }))
    .sort(compareGroupsByLatestUpdated);

  return [...registeredGroups, ...derived];
}
