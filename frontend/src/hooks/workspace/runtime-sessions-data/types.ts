// 由 hooks/workspace/use-runtime-sessions-data.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type RuntimeSessionRecord } from "@/lib/runtime-api";

export type RuntimeSessionsDataOptions = {
  pinnedSessionId?: string;
  userId?: string;
};

export type RuntimeSessionsSummary = {
  activeCount: number;
  archivedCount: number;
  latestSessionId?: string;
  latestUpdatedAt?: string;
  recoverableCount: number;
  totalCount: number;
};

export type StoredRuntimeSessionsPayload = {
  sessions: RuntimeSessionRecord[];
  storedAt: string;
  userId: string;
};
