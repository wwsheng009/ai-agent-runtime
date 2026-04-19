import { describe, expect, it } from "vitest";

import type { Thread } from "@/data/mock";
import { shouldSyncSessionHistory } from "@/hooks/workspace/use-session-history-sync";

function createThread(overrides: Partial<Thread> = {}): Thread {
  return {
    id: "thread-1",
    title: "Thread",
    summary: "Summary",
    updatedAt: "2026-04-05T00:00:00Z",
    status: "active",
    sessionId: "session-1",
    transport: "live",
    lastError: null,
    tags: [],
    prompts: [],
    messages: [],
    artifacts: [],
    ...overrides,
  };
}

describe("use-session-history-sync helpers", () => {
  it("syncs only when the thread has a session and is idle", () => {
    expect(shouldSyncSessionHistory(createThread(), false)).toBe(true);
    expect(shouldSyncSessionHistory(createThread(), true)).toBe(false);
    expect(shouldSyncSessionHistory(createThread({ sessionId: undefined }), false)).toBe(
      false,
    );
  });

  it("skips automatic sync after the thread entered the error state", () => {
    expect(
      shouldSyncSessionHistory(createThread({ transport: "error" }), false),
    ).toBe(false);
  });
});
