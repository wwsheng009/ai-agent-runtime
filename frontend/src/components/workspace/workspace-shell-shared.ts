import { type Thread } from "@/data/mock";

export function getThreadTransportLabel(thread: Thread) {
  if (thread.transport === "live") {
    return "Live runtime";
  }
  if (thread.transport === "error") {
    return "Runtime degraded";
  }
  return "Seeded preview";
}

export function getCommandStateLabel(
  thread: Thread,
  isResponding: boolean,
) {
  if (isResponding) {
    return "Runtime stream active";
  }
  if (thread.sessionId) {
    return "Ready for the next turn";
  }
  if (thread.messages.length > 0) {
    return "Ready to start runtime session";
  }
  return "Ready to start a new session";
}

export function getThreadStatusLabel(thread: Thread) {
  if (thread.sessionId) {
    return "Session attached";
  }
  if (thread.messages.length > 0) {
    return "Preview thread";
  }
  return "New thread";
}

export function getThreadTopbarSubtitle(
  thread: Thread,
  transportLabel: string,
) {
  if (thread.transport === "error") {
    return thread.sessionId
      ? `Session ${thread.sessionId} needs restore attention`
      : "Runtime restore needs attention";
  }

  if (thread.sessionId && thread.runtimeSource) {
    return `${transportLabel} via ${thread.runtimeSource}`;
  }

  if (thread.sessionId) {
    return `Session ${thread.sessionId}`;
  }

  return transportLabel;
}
