import { describe, expect, it } from "vitest";

import {
  buildStoredRuntimeSessionsKey,
  chooseRuntimeSessionUserId,
  mergePinnedRuntimeSession,
  normalizeRuntimeSessionUsers,
  normalizeRuntimeSessions,
  readStoredRuntimeSessionUserId,
  readStoredRuntimeSessions,
  resolveRuntimeSessionsRetryDelay,
  sortRuntimeSessions,
  summarizeRuntimeSessions,
  writeStoredRuntimeSessionUserId,
  writeStoredRuntimeSessions,
} from "@/hooks/workspace/use-runtime-sessions-data";

class MemoryStorage implements Storage {
  private values = new Map<string, string>();

  get length() {
    return this.values.size;
  }

  clear() {
    this.values.clear();
  }

  getItem(key: string) {
    return this.values.has(key) ? this.values.get(key) ?? null : null;
  }

  key(index: number) {
    return Array.from(this.values.keys())[index] ?? null;
  }

  removeItem(key: string) {
    this.values.delete(key);
  }

  setItem(key: string, value: string) {
    this.values.set(key, value);
  }
}

describe("use-runtime-sessions-data helpers", () => {
  it("builds a user-scoped cache key for stored runtime sessions", () => {
    expect(buildStoredRuntimeSessionsKey("user/a")).toBe(
      "workspace.runtime.sessions:user%2Fa",
    );
  });

  it("normalizes null or missing session payloads to an empty array", () => {
    expect(normalizeRuntimeSessions(null)).toEqual([]);
    expect(normalizeRuntimeSessions(undefined)).toEqual([]);
  });

  it("normalizes session user payloads and drops blank user ids", () => {
    expect(
      normalizeRuntimeSessionUsers([
        { user_id: "cli-user", session_count: 1 },
        { user_id: " ", session_count: 2 },
      ]),
    ).toEqual([{ user_id: "cli-user", session_count: 1 }]);
    expect(normalizeRuntimeSessionUsers(null)).toEqual([]);
  });

  it("chooses the best runtime session user from discovered users", () => {
    const users = [
      {
        user_id: "older-user",
        session_count: 5,
        latest_updated_at: "2026-03-31T08:30:00Z",
      },
      {
        user_id: "newer-user",
        session_count: 1,
        latest_updated_at: "2026-03-31T09:30:00Z",
      },
    ];

    expect(
      chooseRuntimeSessionUserId(users, "anonymous", "missing-user", "web-user"),
    ).toBe("newer-user");
    expect(
      chooseRuntimeSessionUserId(users, "anonymous", "older-user", "web-user"),
    ).toBe("older-user");
    expect(
      chooseRuntimeSessionUserId(
        [{ user_id: "anonymous", session_count: 2 }],
        "anonymous",
        "missing-user",
        "web-user",
      ),
    ).toBe("anonymous");
  });

  it("merges a pinned route session when it is missing from the current list", () => {
    const listed = [
      {
        createdAt: "2026-03-31T08:00:00Z",
        id: "session-listed",
        state: "active",
        updatedAt: "2026-03-31T08:30:00Z",
      },
    ];
    const pinned = {
      createdAt: "2026-03-31T09:00:00Z",
      id: "session-pinned",
      state: "idle",
      updatedAt: "2026-03-31T09:30:00Z",
    };

    expect(mergePinnedRuntimeSession(listed, pinned)).toEqual([listed[0], pinned]);
    expect(mergePinnedRuntimeSession([...listed, pinned], pinned)).toEqual([
      listed[0],
      pinned,
    ]);
  });

  it("sorts sessions by latest update descending", () => {
    const sorted = sortRuntimeSessions([
      {
        createdAt: "2026-03-31T08:00:00Z",
        id: "session-older",
        state: "active",
        updatedAt: "2026-03-31T08:30:00Z",
      },
      {
        createdAt: "2026-03-31T09:00:00Z",
        id: "session-newer",
        state: "idle",
        updatedAt: "2026-03-31T09:30:00Z",
      },
    ]);

    expect(sorted.map((session) => session.id)).toEqual([
      "session-newer",
      "session-older",
    ]);
  });

  it("summarizes recoverable and archived runtime sessions", () => {
    const summary = summarizeRuntimeSessions([
      {
        createdAt: "2026-03-31T09:00:00Z",
        id: "session-active",
        state: "active",
        updatedAt: "2026-03-31T09:15:00Z",
      },
      {
        createdAt: "2026-03-31T10:00:00Z",
        id: "session-closed",
        state: "closed",
        updatedAt: "2026-03-31T10:15:00Z",
      },
      {
        createdAt: "2026-03-31T11:00:00Z",
        id: "session-idle",
        state: "idle",
        updatedAt: "2026-03-31T11:15:00Z",
      },
    ]);

    expect(summary.totalCount).toBe(3);
    expect(summary.activeCount).toBe(2);
    expect(summary.archivedCount).toBe(1);
    expect(summary.recoverableCount).toBe(2);
    expect(summary.latestSessionId).toBe("session-idle");
    expect(summary.latestUpdatedAt).toBe("2026-03-31T11:15:00Z");
  });

  it("persists and restores cached runtime sessions per user", () => {
    const storage = new MemoryStorage();
    writeStoredRuntimeSessions(storage, "user-1", [
      {
        createdAt: "2026-03-31T08:00:00Z",
        id: "session-older",
        state: "active",
        updatedAt: "2026-03-31T08:30:00Z",
      },
      {
        createdAt: "2026-03-31T09:00:00Z",
        id: "session-newer",
        state: "idle",
        updatedAt: "2026-03-31T09:30:00Z",
      },
    ]);
    writeStoredRuntimeSessions(storage, "user-2", [
      {
        createdAt: "2026-04-01T09:00:00Z",
        id: "session-other-user",
        state: "active",
        updatedAt: "2026-04-01T09:15:00Z",
      },
    ]);

    expect(readStoredRuntimeSessions(storage, "user-1").map((session) => session.id)).toEqual([
      "session-newer",
      "session-older",
    ]);
    expect(readStoredRuntimeSessions(storage, "user-2").map((session) => session.id)).toEqual([
      "session-other-user",
    ]);
  });

  it("persists and restores the selected runtime session user", () => {
    const storage = new MemoryStorage();
    writeStoredRuntimeSessionUserId(storage, "thinkbook14\\wangweisheng");

    expect(readStoredRuntimeSessionUserId(storage)).toBe("thinkbook14\\wangweisheng");
  });

  it("falls back to an empty cache when stored runtime sessions are invalid", () => {
    const storage = new MemoryStorage();
    storage.setItem(buildStoredRuntimeSessionsKey("user-1"), "{not-json");

    expect(readStoredRuntimeSessions(storage, "user-1")).toEqual([]);
  });

  it("uses a capped retry backoff for runtime session reloads", () => {
    expect(resolveRuntimeSessionsRetryDelay(0)).toBe(1200);
    expect(resolveRuntimeSessionsRetryDelay(1)).toBe(2500);
    expect(resolveRuntimeSessionsRetryDelay(2)).toBe(5000);
    expect(resolveRuntimeSessionsRetryDelay(3)).toBe(8000);
    expect(resolveRuntimeSessionsRetryDelay(99)).toBe(8000);
  });
});
