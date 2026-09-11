// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Thread } from "@/data/mock";
import { readSessionReasoningEffort } from "@/lib/reasoning-effort";
import { normalizeSessionId } from "@/lib/session-id";
import { type RuntimeSessionRecord, type SessionRuntimeEvent } from "@/types/runtime";

import { mergeUniqueStrings } from "./shared";

export function mergeRuntimeSessionsIntoThreads(
  threads: Thread[],
  sessions: RuntimeSessionRecord[],
) {
  const nextThreads = [...threads];
  let changed = false;

  for (const session of sessions) {
    const sessionId = normalizeSessionId(session?.id);
    if (!sessionId) {
      continue;
    }
    const existingIndex = nextThreads.findIndex(
      (thread) =>
        normalizeSessionId(thread.sessionId) === sessionId ||
        normalizeSessionId(thread.id) === sessionId,
    );
    const title =
      session.metadata?.title?.trim() || `Runtime session ${sessionId.slice(0, 10)}`;
    const summary =
      session.metadata?.summary?.trim() ||
      "Restored runtime session from /api/runtime/sessions.";
    const updatedAt = session.updatedAt || session.createdAt || new Date().toISOString();
    const reasoningEffort = readSessionReasoningEffort(
      session.metadata?.context,
    );
    const tags = mergeUniqueStrings(
      "runtime-session",
      session.state ? `state:${session.state}` : null,
      ...(session.metadata?.lastAgent ? [`agent:${session.metadata.lastAgent}`] : []),
      ...(session.metadata?.lastSkill ? [`skill:${session.metadata.lastSkill}`] : []),
      ...(session.metadata?.title ? ["restored"] : []),
    );

    if (existingIndex < 0) {
      changed = true;
      nextThreads.push({
        id: sessionId,
        title,
        summary,
        updatedAt,
        status: mapSessionStateToThreadStatus(session.state),
        sessionId,
        transport: "live",
        runtimeSource: session.metadata?.lastAgent || session.metadata?.lastSkill || "runtime",
        lastError: null,
        reasoningEffort,
        tags,
        prompts: [
          "Sync the latest authoritative session history",
          "Inspect runtime evidence and restore points",
          "Continue this restored runtime session",
        ],
        messages: [],
        artifacts: [],
      });
      continue;
    }

    const current = nextThreads[existingIndex];
    const merged = {
      ...current,
      id: current.sessionId ? current.id : sessionId,
      title,
      summary,
      updatedAt,
      status: mapSessionStateToThreadStatus(session.state),
      sessionId,
      transport: current.transport === "error" ? "error" : "live",
      runtimeSource:
        current.runtimeSource ||
        session.metadata?.lastAgent ||
        session.metadata?.lastSkill ||
        "runtime",
      reasoningEffort,
      tags,
    } satisfies Thread;

    if (
      merged.title !== current.title ||
      merged.summary !== current.summary ||
      merged.updatedAt !== current.updatedAt ||
      merged.status !== current.status ||
      merged.sessionId !== current.sessionId ||
      merged.transport !== current.transport ||
      merged.runtimeSource !== current.runtimeSource ||
      merged.reasoningEffort !== current.reasoningEffort ||
      merged.tags.join("|") !== current.tags.join("|")
    ) {
      changed = true;
      nextThreads[existingIndex] = merged;
    }
  }

  if (!changed) {
    return threads;
  }

  return [...nextThreads].sort((left, right) => {
    const leftTime = Date.parse(left.updatedAt);
    const rightTime = Date.parse(right.updatedAt);
    return rightTime - leftTime;
  });
}

export function getRuntimeEventSeq(event: SessionRuntimeEvent) {
  const rawSeq = event.payload?.seq;
  if (typeof rawSeq === "number" && Number.isFinite(rawSeq)) {
    return rawSeq;
  }
  if (typeof rawSeq === "string") {
    const parsed = Number(rawSeq);
    if (Number.isFinite(parsed)) {
      return parsed;
    }
  }
  return 0;
}

function mapSessionStateToThreadStatus(
  state: string | undefined,
): Thread["status"] {
  switch ((state || "").trim().toLowerCase()) {
    case "archived":
    case "closed":
      return "review";
    case "draft":
      return "draft";
    default:
      return "active";
  }
}
