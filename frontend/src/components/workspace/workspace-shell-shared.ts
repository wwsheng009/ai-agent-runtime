import { type Thread } from "@/data/mock";

export type WorkspaceThreadTransportLabels = {
  live: string;
  error: string;
  seeded: string;
};

/** 传输通道三态：live=在线运行时；error=降级；seeded=预置预览。 */
export type WorkspaceThreadTransportKind = "live" | "error" | "seeded";

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
  viaSource: (source: string) => string;
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
  viaSource: (source) => `via ${source}`,
  session: (sessionId) => `Session ${sessionId}`,
};

/**
 * 传输三态的唯一判据：顶栏图标 / tooltip 与文本标签共用，
 * 避免「图标说在线、文案说降级」这类两处各判一次造成的漂移。
 */
export function getThreadTransportKind(
  thread: Thread,
): WorkspaceThreadTransportKind {
  if (thread.transport === "live") {
    return "live";
  }
  if (thread.transport === "error") {
    return "error";
  }
  return "seeded";
}

export function getThreadTransportLabel(
  thread: Thread,
  labels: WorkspaceThreadTransportLabels = defaultTransportLabels,
) {
  return labels[getThreadTransportKind(thread)];
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

/**
 * 线程状态三态：已附着运行时会话 / 仅本地预览 / 空白新会话。
 * 顶栏状态图标的图形、tooltip 与文本标签共用本判据，避免「图标说已附着、文案说预览」。
 */
export type WorkspaceThreadStatusKind = "attached" | "preview" | "new";

export function getThreadStatusKind(thread: Thread): WorkspaceThreadStatusKind {
  if (thread.sessionId) {
    return "attached";
  }
  return thread.messages.length > 0 ? "preview" : "new";
}

const statusLabelKeys: Record<
  WorkspaceThreadStatusKind,
  keyof WorkspaceThreadStatusLabels
> = {
  attached: "sessionAttached",
  preview: "previewThread",
  new: "newThread",
};

export function getThreadStatusLabel(
  thread: Thread,
  labels: WorkspaceThreadStatusLabels = defaultStatusLabels,
) {
  return labels[statusLabelKeys[getThreadStatusKind(thread)]];
}

/**
 * 本地线程 ↔ 运行时会话的关联四态（与传输三态正交）：
 * - `pending`：尚未附着任何运行时会话；
 * - `error`：会话已附着，但最新同步失败，需要再次尝试恢复；
 * - `restored`：由运行时会话历史物化进来，可继续推进；
 * - `attached`：当前工作区流程直接附着的运行时会话。
 * 侧栏行状态与「会话详情」面共用本判据（`describeThreadSession` 也据此派生）。
 */
export type WorkspaceThreadRelationKind =
  | "attached"
  | "restored"
  | "error"
  | "pending";

export function getThreadRelationKind(
  thread: Thread,
): WorkspaceThreadRelationKind {
  if (!thread.sessionId) {
    return "pending";
  }
  if (thread.transport === "error") {
    return "error";
  }
  if (
    thread.tags.includes("runtime-session") ||
    thread.tags.includes("restored")
  ) {
    return "restored";
  }
  return "attached";
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

  // 传输状态（在线运行时 / 运行时降级 / 预置预览）已由顶栏图标 + tooltip 承载，
  // 副标题不再复述状态名，只留「这一轮消息从哪来」的来源信息。
  if (thread.sessionId && thread.runtimeSource) {
    return labels.viaSource(thread.runtimeSource);
  }

  if (thread.sessionId) {
    return labels.session(thread.sessionId);
  }

  // 无会话的预览线程没有来源可讲，退回传输名作兜底（此时它就是唯一元信息）。
  return transportLabel;
}
