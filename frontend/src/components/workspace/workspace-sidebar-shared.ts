import { type Thread } from "@/data/mock";
import { type RuntimeSessionRecord } from "@/types/runtime";

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
    .sort((left, right) => {
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
    });
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
