import { describe, expect, it } from "vitest";

import type { Thread } from "@/data/mock";
import {
  NEW_THREAD_ID,
  buildWorkspaceThreadPath,
  resolveArtifactSelection,
  resolveSelectedThread,
} from "@/hooks/workspace/use-workspace-thread-selection";
import { mergeRuntimeSessionsIntoThreads } from "@/lib/workspace-thread-state";
import type { RuntimeSessionRecord } from "@/types/runtime";

function createThread(id: string, artifactIds: string[]): Thread {
  return {
    id,
    title: `Thread ${id}`,
    summary: "Summary",
    updatedAt: "2026-03-31T00:00:00Z",
    status: "active",
    tags: [],
    prompts: [],
    messages: [],
    artifacts: artifactIds.map((artifactId) => ({
      id: artifactId,
      name: `${artifactId}.json`,
      path: `runtime/${artifactId}.json`,
      summary: "artifact",
      kind: "json",
      language: "json",
      content: "{}",
    })),
  };
}

describe("workspace thread selection helpers", () => {
  it("picks the route thread when it exists or falls back to the first thread", () => {
    const first = createThread("thread-1", ["artifact-a"]);
    const second = {
      ...createThread("thread-2", ["artifact-b"]),
      sessionId: "session-2",
    };

    expect(
      resolveSelectedThread([first, second], { routeThreadId: "thread-2" })?.id,
    ).toBe("thread-2");
    expect(
      resolveSelectedThread([first, second], { routeSessionId: "session-2" })?.id,
    ).toBe("thread-2");
    expect(
      resolveSelectedThread([first, second], { routeThreadId: "session-2" })?.id,
    ).toBe("thread-2");
    expect(
      resolveSelectedThread([first, second], { routeThreadId: NEW_THREAD_ID })?.id,
    ).toBe(NEW_THREAD_ID);
    expect(
      resolveSelectedThread([first, second], { routeThreadId: "missing" })?.id,
    ).toBe("thread-1");
    expect(resolveSelectedThread([], { routeThreadId: "thread-2" })).toBeUndefined();
  });

  it("builds canonical workspace paths for chat threads and restored sessions", () => {
    expect(buildWorkspaceThreadPath(undefined)).toBe("/workspace/chats/new");
    expect(
      buildWorkspaceThreadPath(createThread(NEW_THREAD_ID, [])),
    ).toBe("/workspace/chats/new");
    expect(buildWorkspaceThreadPath(createThread("thread-1", ["artifact-a"]))).toBe(
      "/workspace/chats/thread-1",
    );
    expect(
      buildWorkspaceThreadPath({
        ...createThread("thread-2", ["artifact-b"]),
        sessionId: "session-2",
      }),
    ).toBe("/workspace/sessions/session-2");
  });

  it("resolves artifact selection to a valid artifact or null for empty threads", () => {
    const thread = createThread("thread-1", ["artifact-a", "artifact-b"]);

    expect(resolveArtifactSelection(undefined, "artifact-a")).toEqual({
      resolvedSelectedArtifactId: null,
      selectedArtifact: null,
    });

    expect(resolveArtifactSelection(thread, "artifact-b")).toMatchObject({
      resolvedSelectedArtifactId: "artifact-b",
      selectedArtifact: { id: "artifact-b" },
    });

    expect(resolveArtifactSelection(thread, "artifact-missing")).toMatchObject({
      resolvedSelectedArtifactId: "artifact-a",
      selectedArtifact: { id: "artifact-a" },
    });
  });

  it("merges runtime sessions into thread state so restored sessions become addressable", () => {
    const seed = [createThread("thread-1", ["artifact-a"])];
    const restoredSession: RuntimeSessionRecord = {
      id: "session-42",
      state: "active",
      metadata: {
        title: "Restored runtime session",
        summary: "Recovered from runtime sessions API.",
        lastAgent: "planner",
      },
      updatedAt: "2026-03-31T10:00:00Z",
    };

    const nextThreads = mergeRuntimeSessionsIntoThreads(seed, [restoredSession]);
    const restoredThread = nextThreads.find((thread) => thread.sessionId === "session-42");

    expect(restoredThread).toMatchObject({
      id: "session-42",
      sessionId: "session-42",
      title: "Restored runtime session",
      runtimeSource: "planner",
      transport: "live",
    });
  });

  it("preserves later local message updates after a runtime session is materialized into thread state", () => {
    const restoredSession: RuntimeSessionRecord = {
      id: "session-42",
      state: "active",
      metadata: {
        title: "Restored runtime session",
        summary: "Recovered from runtime sessions API.",
      },
      updatedAt: "2026-03-31T10:00:00Z",
    };

    const seededThreads = mergeRuntimeSessionsIntoThreads([], [restoredSession]);
    const locallyUpdatedThreads = seededThreads.map((thread) =>
      thread.sessionId === "session-42"
        ? {
            ...thread,
            messages: [
              {
                id: "history-1",
                role: "assistant" as const,
                author: "Runtime",
                label: "history",
                segments: [{ type: "text" as const, content: "Recovered history" }],
              },
            ],
          }
        : thread,
    );

    const reconciledThreads = mergeRuntimeSessionsIntoThreads(
      locallyUpdatedThreads,
      [restoredSession],
    );

    expect(reconciledThreads.find((thread) => thread.sessionId === "session-42")).toMatchObject({
      messages: [
        {
          id: "history-1",
        },
      ],
    });
  });
});
