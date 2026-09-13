import { describe, expect, it } from "vitest";

import type { ChatMessage, Thread } from "@/data/mock";
import {
  NEW_THREAD_ID,
  buildWorkspaceThreadPath,
  isThreadResponding,
  resolveArtifactSelection,
  resolveSelectedThread,
  shouldConfirmThreadSwitch,
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

function createMessage(overrides: Partial<ChatMessage> & { id: string }): ChatMessage {
  return {
    author: "Runtime stream",
    label: "streaming",
    role: "assistant",
    segments: [],
    ...overrides,
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
    expect(
      buildWorkspaceThreadPath({
        ...createThread("thread?#%", []),
        sessionId: undefined,
      }),
    ).toBe("/workspace/chats/thread%3F%23%25");
    expect(
      buildWorkspaceThreadPath({
        ...createThread("thread-encoded", []),
        sessionId: "session%2Fvalue",
      }),
    ).toBe("/workspace/sessions/session%252Fvalue");
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

  it("keeps the local live thread when a restored session placeholder claims the same session id", () => {
    const liveThread: Thread = {
      ...createThread("thread-live", []),
      sessionId: "session-42",
      messages: [
        {
          id: "live-1",
          role: "assistant" as const,
          author: "Runtime",
          label: "streaming",
          segments: [{ type: "text" as const, content: "Streaming reply" }],
        },
      ],
    };
    // The runtime sessions list materializes the session under its own id first;
    // the streamed turn lives on the local thread. Both must not survive as
    // separate threads, otherwise the selection can land on the empty placeholder.
    const restoredPlaceholder: Thread = {
      ...createThread("session-42", []),
      sessionId: "session-42",
      updatedAt: "2026-03-31T10:00:00Z",
    };

    const merged = mergeRuntimeSessionsIntoThreads(
      [restoredPlaceholder, liveThread],
      [{ id: "session-42", state: "active", metadata: { title: "Restored" } }],
    );

    expect(merged).toHaveLength(1);
    expect(merged[0]).toMatchObject({
      id: "thread-live",
      sessionId: "session-42",
      messages: [{ id: "live-1" }],
    });
  });
});

describe("workspace thread selection session id variants", () => {
  it("matches route session ids across whitespace, trailing-slash and path variants", () => {
    const first = createThread("thread-1", ["artifact-a"]);
    const second = {
      ...createThread("thread-2", ["artifact-b"]),
      sessionId: "session-2",
    };

    for (const variant of ["session-2/", "dir/session-2", " session-2 "]) {
      expect(
        resolveSelectedThread([first, second], { routeSessionId: variant })
          ?.id,
      ).toBe("thread-2");
    }
  });

  it("keeps an unmatched deep-linked session selected while it loads", () => {
    const cached = createThread("cached-thread", []);
    const selected = resolveSelectedThread([cached], {
      routeSessionId: "dir/session-pinned/",
    });

    expect(selected).toMatchObject({
      id: "session-pinned",
      sessionId: "session-pinned",
      tags: ["runtime-session", "loading"],
    });
  });

  it("matches canonical runtime sessions against stored thread aliases", () => {
    const aliased = {
      ...createThread("thread-aliased", []),
      sessionId: "dir/session-2/",
    };
    const merged = mergeRuntimeSessionsIntoThreads(
      [aliased],
      [{ id: "session-2", state: "active" }],
    );

    expect(merged).toHaveLength(1);
    expect(merged[0]).toMatchObject({
      id: "thread-aliased",
      sessionId: "session-2",
    });
  });
});

describe("shouldConfirmThreadSwitch", () => {
  it("confirms switching away while the current session is still responding", () => {
    expect(shouldConfirmThreadSwitch("thread-1", "thread-2", true)).toBe(true);
  });

  it("switches immediately when the current session is idle", () => {
    expect(shouldConfirmThreadSwitch("thread-1", "thread-2", false)).toBe(false);
  });

  it("skips the confirmation when re-selecting the current session", () => {
    expect(shouldConfirmThreadSwitch("thread-1", "thread-1", true)).toBe(false);
  });

  it("skips the confirmation for the new-chat entry and before any selection", () => {
    expect(shouldConfirmThreadSwitch("thread-1", NEW_THREAD_ID, true)).toBe(false);
    expect(shouldConfirmThreadSwitch(undefined, "thread-2", true)).toBe(false);
  });
});

describe("isThreadResponding", () => {
  it("detects the streaming message owned by the live turn", () => {
    const thread: Thread = {
      ...createThread("thread-1", []),
      messages: [
        createMessage({ id: "message-user", role: "user", label: "user" }),
        createMessage({
          id: "message-assistant",
          runtimeTurnId: "turn-1",
          streaming: true,
        }),
      ],
    };

    expect(isThreadResponding(thread, "turn-1")).toBe(true);
  });

  it("ignores finished messages, other turns and missing threads", () => {
    const finished: Thread = {
      ...createThread("thread-1", []),
      messages: [
        createMessage({
          id: "message-1",
          runtimeTurnId: "turn-1",
          streaming: false,
        }),
      ],
    };
    const otherTurn: Thread = {
      ...createThread("thread-1", []),
      messages: [
        createMessage({
          id: "message-1",
          runtimeTurnId: "turn-2",
          streaming: true,
        }),
      ],
    };

    expect(isThreadResponding(finished, "turn-1")).toBe(false);
    expect(isThreadResponding(otherTurn, "turn-1")).toBe(false);
    expect(isThreadResponding(createThread("thread-1", []), "turn-1")).toBe(false);
    expect(isThreadResponding(undefined, "turn-1")).toBe(false);
    expect(isThreadResponding(otherTurn, null)).toBe(false);
  });
});
