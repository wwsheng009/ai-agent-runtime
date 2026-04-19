import { describe, expect, it } from "vitest";

import type { Thread } from "@/data/mock";
import {
  describeThreadSession,
  summarizeSidebarSessions,
} from "@/components/workspace/workspace-sidebar-shared";

function createThread(
  id: string,
  overrides: Partial<Thread> = {},
): Thread {
  return {
    id,
    title: `Thread ${id}`,
    summary: `Summary ${id}`,
    updatedAt: "2026-03-31T00:00:00Z",
    status: "active",
    tags: [],
    prompts: [],
    messages: [],
    artifacts: [],
    ...overrides,
  };
}

describe("workspace sidebar session helpers", () => {
  it("describes pending, restored, attached, and errored threads", () => {
    expect(
      describeThreadSession(createThread("thread-pending")),
    ).toMatchObject({
      label: "pending",
    });

    expect(
      describeThreadSession(
        createThread("thread-restored", {
          sessionId: "session-restored",
          tags: ["restored"],
          transport: "live",
        }),
      ),
    ).toMatchObject({
      label: "restored",
    });

    expect(
      describeThreadSession(
        createThread("thread-attached", {
          sessionId: "session-live",
          tags: ["workspace"],
          transport: "live",
        }),
      ),
    ).toMatchObject({
      label: "attached",
    });

    expect(
      describeThreadSession(
        createThread("thread-error", {
          sessionId: "session-error",
          transport: "error",
        }),
      ),
    ).toMatchObject({
      label: "error",
    });
  });

  it("summarizes recoverable threads in reverse chronological order", () => {
    const summary = summarizeSidebarSessions([
      createThread("thread-pending"),
      createThread("thread-live", {
        sessionId: "session-live",
        updatedAt: "2026-03-31T10:00:00Z",
      }),
      createThread("thread-restored", {
        sessionId: "session-restored",
        tags: ["runtime-session", "restored"],
        updatedAt: "2026-03-31T11:00:00Z",
      }),
      createThread("thread-error", {
        sessionId: "session-error",
        transport: "error",
        updatedAt: "2026-03-31T09:00:00Z",
      }),
    ]);

    expect(summary.attachedCount).toBe(3);
    expect(summary.pendingCount).toBe(1);
    expect(summary.restoredCount).toBe(1);
    expect(summary.errorCount).toBe(1);
    expect(summary.recentRecoverableThreads.map((thread) => thread.id)).toEqual([
      "thread-restored",
      "thread-live",
      "thread-error",
    ]);
  });
});
