import { type Thread } from "@/data/mock";

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
