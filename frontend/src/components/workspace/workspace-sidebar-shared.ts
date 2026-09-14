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

/** P2-6 子片 2：跨组移动的乐观覆盖（sessionId → 目标工作目录路径）。 */
export type SessionMoveOverrides = Readonly<Record<string, string>>;

export type SessionMoveTarget = {
  key: string;
  label: string;
  path: string;
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
      tone: "border-white/10 bg-white/6 text-muted-foreground",
    };
  }

  if (thread.transport === "error") {
    return {
      detail: labels.error,
      label: "error",
      tone: "border-accent-orange/24 bg-accent-orange/10 text-accent-orange",
    };
  }

  if (thread.tags.includes("runtime-session") || thread.tags.includes("restored")) {
    return {
      detail: labels.restored,
      label: "restored",
      tone: "border-accent-teal/24 bg-accent-teal/10 text-accent-teal",
    };
  }

  return {
    detail: labels.attached,
    label: "attached",
    tone: "border-accent-gold/24 bg-accent-gold/10 text-accent-gold",
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

export function normalizeRuntimeDirectoryPath(value: string) {
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
  directories: readonly RuntimeWorkspaceDirectory[],
  sessions: readonly RuntimeSessionRecord[],
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

/**
 * P2-6 子片 2：把跨组移动的**乐观覆盖**套用到会话集合上——只改写
 * `metadata.context.workspace_path`，其余字段（metadata 其它键、context
 * 其它键）原样保留。无覆盖时返回入参同一引用，避免无谓的重算与重渲染。
 */
export function applySessionWorkspaceOverrides(
  sessions: readonly RuntimeSessionRecord[],
  overrides: SessionMoveOverrides,
): RuntimeSessionRecord[] {
  const entries = Object.entries(overrides);
  if (entries.length === 0) {
    return sessions as RuntimeSessionRecord[];
  }

  const targets = new Map(entries);
  return sessions.map((session) => {
    const targetPath = targets.get(session.id);
    if (targetPath === undefined) {
      return session;
    }
    return {
      ...session,
      metadata: {
        ...(session.metadata ?? {}),
        context: {
          ...(session.metadata?.context ?? {}),
          workspace_path: targetPath,
        },
      },
    };
  });
}

/**
 * 覆盖回收：Host 数据已经反映目标路径（或会话已消失）时清掉对应覆盖。
 * 返回入参同一引用表示无需变更。
 */
export function pruneSessionMoveOverrides(
  overrides: SessionMoveOverrides,
  sessions: readonly RuntimeSessionRecord[],
): SessionMoveOverrides {
  const entries = Object.entries(overrides);
  if (entries.length === 0) {
    return overrides;
  }

  const byId = new Map(sessions.map((session) => [session.id, session]));
  let changed = false;
  const next: Record<string, string> = {};
  for (const [sessionId, targetPath] of entries) {
    const session = byId.get(sessionId);
    if (!session) {
      changed = true;
      continue;
    }
    const applied =
      resolveRuntimeSessionDirectory(session).fullPath ===
      normalizeRuntimeDirectoryPath(targetPath);
    if (applied) {
      changed = true;
      continue;
    }
    next[sessionId] = targetPath;
  }

  return changed ? next : overrides;
}

/**
 * 解析某个分组能否作为跨组移动的落点：
 * - 无归属路径（Unscoped）不可作落点——清空归属不在本批范围；
 * - 注册目录在宿主机上已不存在（`exists === false`）时不可作落点，
 *   避免把会话改绑到打不开的目录；
 * - 注册目录优先写回注册表里的**原始路径**，不把展示用的规范化路径写回 Host。
 */
export function resolveSessionMoveTarget(
  group: Pick<MergedDirectoryGroup, "key" | "label" | "fullPath" | "directoryId">,
  directories: readonly RuntimeWorkspaceDirectory[],
): SessionMoveTarget | null {
  if (!group.fullPath.trim()) {
    return null;
  }

  if (group.directoryId) {
    const directory = directories.find((item) => item.id === group.directoryId);
    if (directory?.exists === false) {
      return null;
    }
    return {
      key: group.key,
      label: directory?.name?.trim() || group.label,
      path: directory?.path?.trim() || group.fullPath,
    };
  }

  return { key: group.key, label: group.label, path: group.fullPath };
}
