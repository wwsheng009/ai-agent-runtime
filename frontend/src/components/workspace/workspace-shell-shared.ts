import { type Thread } from "@/data/mock";

export type WorkspaceThreadTransportLabels = {
  live: string;
  error: string;
  seeded: string;
};

export type WorkspaceThreadCommandStateLabels = {
  runtimeStreamActive: string;
  readyForNextTurn: string;
  readyToStartRuntimeSession: string;
  readyToStartNewSession: string;
};

export type WorkspaceThreadStatusLabels = {
  sessionAttached: string;
  previewThread: string;
  newThread: string;
};

export type WorkspaceThreadSubtitleLabels = {
  needsRestoreWithSession: (sessionId: string) => string;
  needsRestore: string;
  viaSource: (transportLabel: string, source: string) => string;
  session: (sessionId: string) => string;
};

const defaultTransportLabels: WorkspaceThreadTransportLabels = {
  live: "Live runtime",
  error: "Runtime degraded",
  seeded: "Seeded preview",
};

const defaultCommandStateLabels: WorkspaceThreadCommandStateLabels = {
  runtimeStreamActive: "Runtime stream active",
  readyForNextTurn: "Ready for the next turn",
  readyToStartRuntimeSession: "Ready to start runtime session",
  readyToStartNewSession: "Ready to start a new session",
};

const defaultStatusLabels: WorkspaceThreadStatusLabels = {
  sessionAttached: "Session attached",
  previewThread: "Preview thread",
  newThread: "New thread",
};

const defaultSubtitleLabels: WorkspaceThreadSubtitleLabels = {
  needsRestoreWithSession: (sessionId) =>
    `Session ${sessionId} needs restore attention`,
  needsRestore: "Runtime restore needs attention",
  viaSource: (transportLabel, source) => `${transportLabel} via ${source}`,
  session: (sessionId) => `Session ${sessionId}`,
};

export function getThreadTransportLabel(
  thread: Thread,
  labels: WorkspaceThreadTransportLabels = defaultTransportLabels,
) {
  if (thread.transport === "live") {
    return labels.live;
  }
  if (thread.transport === "error") {
    return labels.error;
  }
  return labels.seeded;
}

export function getCommandStateLabel(
  thread: Thread,
  isResponding: boolean,
  labels: WorkspaceThreadCommandStateLabels = defaultCommandStateLabels,
) {
  if (isResponding) {
    return labels.runtimeStreamActive;
  }
  if (thread.sessionId) {
    return labels.readyForNextTurn;
  }
  if (thread.messages.length > 0) {
    return labels.readyToStartRuntimeSession;
  }
  return labels.readyToStartNewSession;
}

export function getThreadStatusLabel(
  thread: Thread,
  labels: WorkspaceThreadStatusLabels = defaultStatusLabels,
) {
  if (thread.sessionId) {
    return labels.sessionAttached;
  }
  if (thread.messages.length > 0) {
    return labels.previewThread;
  }
  return labels.newThread;
}

export function getThreadTopbarSubtitle(
  thread: Thread,
  transportLabel: string,
  labels: WorkspaceThreadSubtitleLabels = defaultSubtitleLabels,
) {
  if (thread.transport === "error") {
    return thread.sessionId
      ? labels.needsRestoreWithSession(thread.sessionId)
      : labels.needsRestore;
  }

  if (thread.sessionId && thread.runtimeSource) {
    return labels.viaSource(transportLabel, thread.runtimeSource);
  }

  if (thread.sessionId) {
    return labels.session(thread.sessionId);
  }

  return transportLabel;
}
