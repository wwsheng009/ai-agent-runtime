// 由 hooks/workspace/use-runtime-sessions-data.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  type RuntimeSessionRecord,
  type RuntimeSessionUserSummary,
} from "@/lib/runtime-api";
import { normalizeSessionId } from "@/lib/session-id";

export function normalizeRuntimeSessions(
  sessions:
    | Array<RuntimeSessionRecord | null | undefined>
    | null
    | undefined,
) {
  if (!Array.isArray(sessions)) {
    return [];
  }
  const normalized: RuntimeSessionRecord[] = [];
  const seen = new Set<string>();
  for (const session of sessions) {
    const id = normalizeSessionId(session?.id);
    if (!session || !id || seen.has(id)) {
      continue;
    }
    seen.add(id);
    normalized.push(session.id === id ? session : { ...session, id });
  }
  return normalized;
}

export function normalizeRuntimeSessionUsers(
  users: RuntimeSessionUserSummary[] | null | undefined,
) {
  return Array.isArray(users)
    ? users.filter((user) => user.user_id?.trim())
    : [];
}
