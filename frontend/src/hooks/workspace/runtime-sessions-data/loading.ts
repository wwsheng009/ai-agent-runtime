// 由 hooks/workspace/use-runtime-sessions-data.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  getRuntimeSession,
  listRuntimeSessions,
  type RuntimeSessionRecord,
} from "@/lib/runtime-api";
import { normalizeSessionId } from "@/lib/session-id";

import { normalizeRuntimeSessions } from "./normalize";
import { sortRuntimeSessions } from "./sorting";

export function mergePinnedRuntimeSession(
  sessions: RuntimeSessionRecord[],
  pinnedSession: RuntimeSessionRecord | null | undefined,
) {
  const pinnedSessionId = normalizeSessionId(pinnedSession?.id);
  if (!pinnedSession || !pinnedSessionId) {
    return sessions;
  }

  if (
    sessions.some(
      (session) => normalizeSessionId(session.id) === pinnedSessionId,
    )
  ) {
    return sessions;
  }

  return [
    ...sessions,
    pinnedSession.id === pinnedSessionId
      ? pinnedSession
      : { ...pinnedSession, id: pinnedSessionId },
  ];
}

export async function loadRuntimeSessions(
  userId: string,
  pinnedSessionId?: string,
) {
  const response = await listRuntimeSessions({ userId });
  const listedSessions = normalizeRuntimeSessions(response.sessions);
  const resolvedPinnedSessionId = normalizeSessionId(pinnedSessionId);

  if (
    !resolvedPinnedSessionId ||
    listedSessions.some((session) => session.id === resolvedPinnedSessionId)
  ) {
    return sortRuntimeSessions(listedSessions);
  }

  try {
    const response = await getRuntimeSession(resolvedPinnedSessionId);
    return sortRuntimeSessions(
      mergePinnedRuntimeSession(listedSessions, response.session),
    );
  } catch {
    return sortRuntimeSessions(listedSessions);
  }
}
