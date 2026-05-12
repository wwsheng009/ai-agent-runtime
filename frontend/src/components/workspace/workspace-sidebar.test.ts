import { describe, expect, it } from "vitest";

import type { Thread } from "@/data/mock";
import {
  describeThreadSession,
  groupRuntimeSessionsByDirectory,
  resolveRuntimeSessionDirectory,
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

  it("groups runtime sessions by workspace directory", () => {
    const groups = groupRuntimeSessionsByDirectory([
      {
        id: "session-workspace-new",
        metadata: {
          context: {
            workspace_path: "E:\\projects\\ai\\runtime",
          },
        },
        updatedAt: "2026-03-31T11:00:00Z",
      },
      {
        id: "session-other",
        metadata: {
          context: {
            workspacePath: "E:/projects/ai/other",
          },
        },
        updatedAt: "2026-03-31T10:00:00Z",
      },
      {
        id: "session-workspace-old",
        metadata: {
          context: {
            workspace_path: "E:/projects/ai/runtime/",
          },
        },
        updatedAt: "2026-03-31T09:00:00Z",
      },
    ]);

    expect(groups.map((group) => group.label)).toEqual(["runtime", "other"]);
    expect(groups[0]?.fullPath).toBe("E:/projects/ai/runtime");
    expect(groups[0]?.sessions.map((session) => session.id)).toEqual([
      "session-workspace-new",
      "session-workspace-old",
    ]);
  });

  it("uses an unscoped directory bucket when session metadata has no path", () => {
    expect(
      resolveRuntimeSessionDirectory({
        id: "session-unscoped",
        metadata: {},
      }),
    ).toMatchObject({
      label: "Unscoped sessions",
      fullPath: "",
    });
  });
});
