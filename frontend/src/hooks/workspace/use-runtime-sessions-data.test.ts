import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  buildStoredRuntimeSessionsKey,
  chooseRuntimeSessionUserId,
  loadRuntimeSessions,
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
import { getRuntimeSession, listRuntimeSessions } from "@/lib/runtime-api";

vi.mock("@/lib/runtime-api", () => ({
  getRuntimeSession: vi.fn(),
  listRuntimeSessions: vi.fn(),
}));

const mockGetRuntimeSession = vi.mocked(getRuntimeSession);
const mockListRuntimeSessions = vi.mocked(listRuntimeSessions);

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

  it("reuses sort and summary results for an unchanged sessions reference", () => {
    const sessions = [
      {
        createdAt: "2026-05-01T08:00:00Z",
        id: "session-reuse-older",
        state: "active",
        updatedAt: "2026-05-01T08:30:00Z",
      },
      {
        createdAt: "2026-05-01T09:00:00Z",
        id: "session-reuse-newer",
        state: "idle",
        updatedAt: "2026-05-01T09:30:00Z",
      },
    ];

    const sortedFirst = sortRuntimeSessions(sessions);
    expect(sortRuntimeSessions(sessions)).toBe(sortedFirst);

    const summaryFirst = summarizeRuntimeSessions(sessions);
    expect(summarizeRuntimeSessions(sessions)).toBe(summaryFirst);

    // 新引用、同内容：结果等值但会重算（不影响正确性，只影响开销）。
    const sortedClone = sortRuntimeSessions([...sessions]);
    expect(sortedClone).not.toBe(sortedFirst);
    expect(sortedClone.map((session) => session.id)).toEqual([
      "session-reuse-newer",
      "session-reuse-older",
    ]);
  });

  it("resolves each session timestamp once per sort", () => {
    const sessions = Array.from({ length: 40 }, (_unused, index) => ({
      createdAt: `2026-05-0${(index % 9) + 1}T08:00:00Z`,
      id: `session-parse-${String(index).padStart(2, "0")}`,
      state: "active",
      updatedAt: `2026-05-0${(index % 9) + 1}T09:00:00Z`,
    }));
    const parseSpy = vi.spyOn(Date, "parse");

    try {
      sortRuntimeSessions(sessions);

      expect(parseSpy).toHaveBeenCalledTimes(sessions.length);
    } finally {
      parseSpy.mockRestore();
    }
  });

  it("orders sessions without a parsable timestamp by id", () => {
    const sorted = sortRuntimeSessions([
      { id: "session-order-c", state: "active" },
      { createdAt: "n/a", id: "session-order-a", state: "active" },
      {
        id: "session-order-b",
        state: "active",
        updatedAt: "2026-05-01T09:00:00Z",
      },
    ]);

    expect(sorted.map((session) => session.id)).toEqual([
      "session-order-a",
      "session-order-b",
      "session-order-c",
    ]);
  });

  it("reuses the parsed cache array for unchanged stored sessions", () => {
    const storage = new MemoryStorage();
    const cachedSession = {
      createdAt: "2026-05-02T08:00:00Z",
      id: "session-read-cache",
      state: "active",
      updatedAt: "2026-05-02T08:30:00Z",
    };
    writeStoredRuntimeSessions(storage, "user-read-cache", [cachedSession]);

    const first = readStoredRuntimeSessions(storage, "user-read-cache");
    expect(readStoredRuntimeSessions(storage, "user-read-cache")).toBe(first);
    // 空 storage：命中「没有缓存」分支。
    expect(readStoredRuntimeSessions(new MemoryStorage(), "user-read-cache")).toEqual(
      [],
    );
    // 另一个 storage 实例、同样内容：必须重新解析，不能复用上个实例的数组。
    const otherStorage = new MemoryStorage();
    writeStoredRuntimeSessions(otherStorage, "user-read-cache", [cachedSession]);
    const otherRead = readStoredRuntimeSessions(otherStorage, "user-read-cache");
    expect(otherRead).not.toBe(first);
    expect(otherRead).toEqual(first);
  });

  it("skips rewriting an unchanged stored sessions payload", () => {
    const storage = new MemoryStorage();
    const sessions = [
      {
        createdAt: "2026-05-03T08:00:00Z",
        id: "session-write-skip",
        state: "active",
        updatedAt: "2026-05-03T08:30:00Z",
      },
    ];
    const setItemSpy = vi.spyOn(storage, "setItem");

    writeStoredRuntimeSessions(storage, "user-write-skip", sessions);
    expect(setItemSpy).toHaveBeenCalledTimes(1);

    writeStoredRuntimeSessions(storage, "user-write-skip", [...sessions]);
    expect(setItemSpy).toHaveBeenCalledTimes(1);

    writeStoredRuntimeSessions(storage, "user-write-skip", [
      { ...sessions[0], state: "closed", updatedAt: "2026-05-03T09:30:00Z" },
    ]);
    expect(setItemSpy).toHaveBeenCalledTimes(2);
    expect(
      readStoredRuntimeSessions(storage, "user-write-skip")[0]?.state,
    ).toBe("closed");

    setItemSpy.mockRestore();
  });

  it("uses a capped retry backoff for runtime session reloads", () => {
    expect(resolveRuntimeSessionsRetryDelay(0)).toBe(1200);
    expect(resolveRuntimeSessionsRetryDelay(1)).toBe(2500);
    expect(resolveRuntimeSessionsRetryDelay(2)).toBe(5000);
    expect(resolveRuntimeSessionsRetryDelay(3)).toBe(8000);
    expect(resolveRuntimeSessionsRetryDelay(99)).toBe(8000);
  });
});

describe("loadRuntimeSessions pinned session id normalization", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("matches a listed session when the pinned id is a variant", async () => {
    mockListRuntimeSessions.mockResolvedValue({
      count: 1,
      sessions: [
        {
          createdAt: "2026-03-31T08:00:00Z",
          id: "session-1",
          state: "active",
          updatedAt: "2026-03-31T08:30:00Z",
        },
      ],
    });

    const result = await loadRuntimeSessions("user-1", "session-1/");

    expect(result.map((session) => session.id)).toEqual(["session-1"]);
    expect(mockGetRuntimeSession).not.toHaveBeenCalled();
  });

  it("fetches a missing pinned session with its canonical id", async () => {
    mockListRuntimeSessions.mockResolvedValue({
      count: 1,
      sessions: [
        {
          createdAt: "2026-03-31T08:00:00Z",
          id: "session-1",
          state: "active",
          updatedAt: "2026-03-31T08:30:00Z",
        },
      ],
    });
    mockGetRuntimeSession.mockResolvedValue({
      session: {
        createdAt: "2026-03-31T09:00:00Z",
        id: "session-pinned",
        state: "idle",
        updatedAt: "2026-03-31T09:30:00Z",
      },
    });

    const result = await loadRuntimeSessions("user-1", " dir/session-pinned ");

    expect(mockGetRuntimeSession).toHaveBeenCalledWith("session-pinned");
    expect(result.map((session) => session.id)).toEqual([
      "session-pinned",
      "session-1",
    ]);
  });

  it("falls back to the listed sessions when the pinned session cannot be fetched", async () => {
    mockListRuntimeSessions.mockResolvedValue({
      count: 1,
      sessions: [
        {
          createdAt: "2026-03-31T08:00:00Z",
          id: "session-1",
          state: "active",
          updatedAt: "2026-03-31T08:30:00Z",
        },
      ],
    });
    mockGetRuntimeSession.mockRejectedValue(new Error("not found"));

    const result = await loadRuntimeSessions("user-1", "dir/session-missing");

    expect(result.map((session) => session.id)).toEqual(["session-1"]);
  });
});

describe("runtime session record normalization", () => {
  it("canonicalizes ids, drops invalid entries and deduplicates aliases", () => {
    expect(
      normalizeRuntimeSessions([
        {
          id: "dir/session-1/",
          state: "active",
        },
        {
          id: "session-1",
          state: "idle",
        },
        null,
        {
          id: " / ",
        },
      ]),
    ).toEqual([
      {
        id: "session-1",
        state: "active",
      },
    ]);
  });

  it("does not append a pinned alias that is already listed", () => {
    const listed = [{ id: "session-1" }];
    expect(
      mergePinnedRuntimeSession(listed, { id: "dir/session-1/" }),
    ).toBe(listed);
  });
});
