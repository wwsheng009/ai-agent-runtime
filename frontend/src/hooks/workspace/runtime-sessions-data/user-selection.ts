// 由 hooks/workspace/use-runtime-sessions-data.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type RuntimeSessionUserSummary } from "@/lib/runtime-api";

import { normalizeRuntimeSessionUsers } from "./normalize";

export function chooseRuntimeSessionUserId(
  users: RuntimeSessionUserSummary[],
  defaultUserId: string | null | undefined,
  currentUserId: string | null | undefined,
  fallbackUserId: string,
) {
  const normalizedUsers = normalizeRuntimeSessionUsers(users);
  const userIds = new Set(normalizedUsers.map((user) => user.user_id.trim()));
  const current = currentUserId?.trim() ?? "";
  if (current && userIds.has(current)) {
    return current;
  }

  const defaultUser = defaultUserId?.trim() ?? "";
  if (
    defaultUser &&
    normalizedUsers.some(
      (user) => user.user_id.trim() === defaultUser && (user.session_count ?? 0) > 0,
    )
  ) {
    return defaultUser;
  }

  const withSessions = normalizedUsers.filter((user) => (user.session_count ?? 0) > 0);
  if (withSessions.length > 0) {
    return [...withSessions].sort((left, right) => {
      const leftTime = Date.parse(left.latest_updated_at ?? "");
      const rightTime = Date.parse(right.latest_updated_at ?? "");
      if (Number.isFinite(leftTime) && Number.isFinite(rightTime) && leftTime !== rightTime) {
        return rightTime - leftTime;
      }
      if (Number.isFinite(leftTime) && !Number.isFinite(rightTime)) {
        return -1;
      }
      if (!Number.isFinite(leftTime) && Number.isFinite(rightTime)) {
        return 1;
      }
      if ((left.session_count ?? 0) !== (right.session_count ?? 0)) {
        return (right.session_count ?? 0) - (left.session_count ?? 0);
      }
      return left.user_id.localeCompare(right.user_id);
    })[0]?.user_id.trim() ?? fallbackUserId;
  }

  if (defaultUser) {
    return defaultUser;
  }

  return fallbackUserId.trim();
}
