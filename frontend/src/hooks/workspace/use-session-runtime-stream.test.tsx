import {
  act,
  useCallback,
  useState,
  type Dispatch,
  type SetStateAction,
} from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Thread } from "@/data/mock";
import { useSessionRuntimeStream } from "@/hooks/workspace/use-session-runtime-stream";
import {
  applyRuntimeDeltaToThread,
  applyRuntimeEventToThread,
  createRuntimeDeltaCoordinator,
  getRuntimeEventSeq,
  mergeRuntimeEvent,
  type RuntimeDeltaCoordinator,
} from "@/lib/workspace-thread-state";
import type { SessionRuntimeEvent } from "@/lib/runtime-api";

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return {
    ...actual,
    streamSessionRuntime: vi.fn(),
  };
});

import { streamSessionRuntime } from "@/lib/runtime-api";

const mockStream = vi.mocked(streamSessionRuntime);

function createThread(): Thread {
  return {
    id: "thread-1",
    title: "Thread",
    summary: "Summary",
    updatedAt: "2026-08-30T00:00:00Z",
    status: "active",
    sessionId: "session-1",
    transport: "live",
    runtimeSource: "runtime",
    lastError: null,
    tags: [],
    prompts: [],
    messages: [
      {
        id: "assistant-1",
        role: "assistant",
        author: "Runtime stream",
        label: "streaming",
        segments: [{ type: "text", content: "..." }],
      },
    ],
    artifacts: [],
  };
}

function deltaEvent(payload: Record<string, unknown>): SessionRuntimeEvent {
  return {
    type: "assistant_delta",
    timestamp: "2026-08-30T00:00:01Z",
    payload: { delta: "Hello", stream_id: "stream-1", sequence: 1, ...payload },
  };
}

function Harness({
  activeTurnId,
  deltaCoordinator,
  renderLiveDeltas,
  initialThread,
  onThreadsChange,
}: {
  activeTurnId?: string | null;
  deltaCoordinator?: RuntimeDeltaCoordinator;
  renderLiveDeltas: boolean;
  initialThread: Thread;
  onThreadsChange: (threads: Thread[]) => void;
}) {
  const [thread, setThread] = useState(initialThread);
  const getErrorMessage = useCallback(
    (error: unknown, fallback: string) =>
      error instanceof Error ? error.message : fallback,
    [],
  );
  const setThreads = useCallback<Dispatch<SetStateAction<Thread[]>>>(
    (updater) => {
      setThread((current) => {
        const result =
          typeof updater === "function" ? updater([current]) : updater;
        const next = Array.isArray(result) ? result[0] : result;
        onThreadsChange([next]);
        return next;
      });
    },
    [onThreadsChange],
  );
  useSessionRuntimeStream({
    applyRuntimeEventToThread,
    applyRuntimeDeltaToThread,
    getErrorMessage,
    getRuntimeEventSeq,
    mergeRuntimeEvent,
    activeTurnId,
    deltaCoordinator,
    renderLiveDeltas,
    selectedThread: thread,
    setThreads,
  });
  return null;
}

describe("useSessionRuntimeStream delta gate", () => {
  let container: HTMLDivElement;
  let root: Root;
  type ReactActEnvironmentGlobal = typeof globalThis & {
    IS_REACT_ACT_ENVIRONMENT?: boolean;
  };

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    mockStream.mockReset();
    mockStream.mockImplementation(
      async (_sessionId, handlers) =>
        new Promise<void>((resolve) => {
          // 保持连接挂起，仅由测试直接触发 onEvent。
          handlers.signal?.addEventListener("abort", () => resolve());
        }),
    );
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function renderWith(
    renderLiveDeltas: boolean,
    thread: Thread,
    options?: {
      activeTurnId?: string | null;
      deltaCoordinator?: RuntimeDeltaCoordinator;
    },
  ) {
    let threads: Thread[] = [thread];
    const onThreadsChange = (next: Thread[]) => {
      threads = next;
    };
    act(() => {
      root.render(
        <Harness
          activeTurnId={options?.activeTurnId}
          deltaCoordinator={options?.deltaCoordinator}
          renderLiveDeltas={renderLiveDeltas}
          initialThread={thread}
          onThreadsChange={onThreadsChange}
        />,
      );
    });
    await vi.waitFor(() => {
      expect(mockStream).toHaveBeenCalledTimes(1);
    });
    const handlers = mockStream.mock.calls[0][1];
    return { handlers, getThreads: () => threads };
  }

  it("appends assistant_delta to the message text when renderLiveDeltas=true", async () => {
    const { handlers, getThreads } = await renderWith(true, createThread());
    act(() => {
      handlers.onEvent?.(deltaEvent({ delta: "Hello " }));
    });
    act(() => {
      handlers.onEvent?.(deltaEvent({ delta: "World", sequence: 2 }));
    });

    const textSegment = getThreads()[0].messages[0].segments.find(
      (s) => s.type === "text",
    );
    expect(textSegment?.type === "text" ? textSegment.content : "").toBe(
      "Hello World",
    );
  });

  it("does NOT render deltas when renderLiveDeltas=false (history replay)", async () => {
    const { handlers, getThreads } = await renderWith(false, createThread());
    act(() => {
      handlers.onEvent?.(deltaEvent({ delta: "World", sequence: 2 }));
    });

    const textSegment = getThreads()[0].messages[0].segments.find(
      (s) => s.type === "text",
    );
    expect(textSegment?.type === "text" ? textSegment.content : "").toBe("...");
  });

  it("still applies non-delta events through the snapshot path", async () => {
    const { handlers, getThreads } = await renderWith(false, createThread());
    act(() => {
      handlers.onEvent?.({
        type: "session_start",
        timestamp: "2026-08-30T00:00:02Z",
        payload: { status: "running", seq: 5 },
      });
    });

    const nextThread = getThreads()[0];
    expect(nextThread.lastRuntimeEventType).toBe("session_start");
    expect(nextThread.runtimeEventCount).toBe(1);
  });

  it("ignores a durable delta from another turn", async () => {
    const coordinator = createRuntimeDeltaCoordinator();
    coordinator.beginTurn("turn-current");
    const { handlers, getThreads } = await renderWith(true, createThread(), {
      activeTurnId: "turn-current",
      deltaCoordinator: coordinator,
    });

    act(() => {
      handlers.onEvent?.(
        deltaEvent({
          delta: "stale",
          turn_id: "turn-previous",
        }),
      );
    });

    const textSegment = getThreads()[0].messages[0].segments.find(
      (segment) => segment.type === "text",
    );
    expect(textSegment?.type === "text" ? textSegment.content : "").toBe("...");
  });

  it("uses the latest turn ref without reconnecting the runtime stream", async () => {
    let threads: Thread[] = [createThread()];
    const onThreadsChange = (next: Thread[]) => {
      threads = next;
    };
    const coordinator = createRuntimeDeltaCoordinator();
    coordinator.beginTurn("turn-1");
    const initialThread: Thread = {
      ...threads[0],
      messages: threads[0].messages.map((message) =>
        message.role === "assistant"
          ? { ...message, runtimeTurnId: "turn-2" }
          : message,
      ),
    };

    act(() => {
      root.render(
        <Harness
          activeTurnId="turn-1"
          deltaCoordinator={coordinator}
          renderLiveDeltas
          initialThread={initialThread}
          onThreadsChange={onThreadsChange}
        />,
      );
    });
    await vi.waitFor(() => {
      expect(mockStream).toHaveBeenCalledTimes(1);
    });
    const handlers = mockStream.mock.calls[0][1];

    coordinator.endTurn("turn-1");
    coordinator.beginTurn("turn-2");
    act(() => {
      root.render(
        <Harness
          activeTurnId="turn-2"
          deltaCoordinator={coordinator}
          renderLiveDeltas
          initialThread={initialThread}
          onThreadsChange={onThreadsChange}
        />,
      );
    });
    await act(async () => {
      await Promise.resolve();
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
    });
    expect(mockStream).toHaveBeenCalledTimes(1);

    act(() => {
      handlers.onEvent?.(
        deltaEvent({
          delta: "fresh",
          turn_id: "turn-2",
        }),
      );
    });
    const textSegment = threads[0].messages[0].segments.find(
      (segment) => segment.type === "text",
    );
    expect(textSegment?.type === "text" ? textSegment.content : "").toBe(
      "fresh",
    );
  });

  it("deduplicates a delta already claimed by the request SSE path", async () => {
    const coordinator = createRuntimeDeltaCoordinator();
    coordinator.beginTurn("turn-1");
    expect(
      coordinator.claim("runtime-delta|turn-1|stream-1|text|1"),
    ).toBe(true);
    const { handlers, getThreads } = await renderWith(true, createThread(), {
      activeTurnId: "turn-1",
      deltaCoordinator: coordinator,
    });

    act(() => {
      handlers.onEvent?.(
        deltaEvent({
          delta: "duplicate",
          turn_id: "turn-1",
        }),
      );
    });

    const textSegment = getThreads()[0].messages[0].segments.find(
      (segment) => segment.type === "text",
    );
    expect(textSegment?.type === "text" ? textSegment.content : "").toBe("...");
  });
});
