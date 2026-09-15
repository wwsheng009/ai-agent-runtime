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
  getRuntimeDeltaKeyFromEvent,
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
  // 模拟父级在启动期重新归并线程数组：selectedThread 会换成新的对象（新 id、
  // 同 sessionId）。这里按 id 变化同步本地状态，贴近真实父级的替换行为。
  if (initialThread.id !== thread.id) {
    setThread(initialThread);
  }
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

  it("keeps the delta key unclaimed when the runtime path cannot write it", async () => {
    const coordinator = createRuntimeDeltaCoordinator();
    const finalized = createThread();
    finalized.messages[0] = {
      ...finalized.messages[0],
      label: "",
      streaming: false,
    };
    const { handlers, getThreads } = await renderWith(true, finalized, {
      deltaCoordinator: coordinator,
    });

    const event = deltaEvent({ delta: "Hello", sequence: 11 });
    act(() => {
      handlers.onEvent?.(event);
    });

    const textSegment = getThreads()[0].messages[0].segments.find(
      (s) => s.type === "text",
    );
    expect(textSegment?.type === "text" ? textSegment.content : "").toBe("...");
    // 运行时通道写不进这条消息（已定稿）→ 不得消费去重 key，否则
    // /api/agent/chat 通道拿到同一 key 会直接 return：两条通道互相让路，
    // 增量帧全部到达却一帧也不渲染（等 turn 结束由最终快照整块定型）。
    expect(coordinator.claim(getRuntimeDeltaKeyFromEvent(event))).toBe(true);
  });

  it("claims the delta key once the runtime path renders it", async () => {
    const coordinator = createRuntimeDeltaCoordinator();
    const { handlers, getThreads } = await renderWith(true, createThread(), {
      deltaCoordinator: coordinator,
    });

    const event = deltaEvent({ delta: " typed", sequence: 12 });
    act(() => {
      handlers.onEvent?.(event);
    });

    const textSegment = getThreads()[0].messages[0].segments.find(
      (s) => s.type === "text",
    );
    expect(textSegment?.type === "text" ? textSegment.content : "").toBe(
      " typed",
    );
    // 已渲染 → key 必须被消费，避免 /api/agent/chat 通道重复追加同一帧。
    expect(coordinator.claim(getRuntimeDeltaKeyFromEvent(event))).toBe(false);
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

  it("subscribes with live=true and keeps live-only subagent progress", async () => {
    const { handlers, getThreads } = await renderWith(false, createThread());

    // P1-5 方案 2：`subagent.progress` 是无持久化 seq 的 live-only 事件，
    // 服务端仅在 live=1 时订阅总线并投递；父流漏传该开关会让镜像静默丢失。
    expect(mockStream.mock.calls[0][1].live).toBe(true);

    act(() => {
      handlers.onEvent?.({
        type: "subagent.progress",
        timestamp: "2026-09-13T00:00:01Z",
        payload: {
          agent_id: "child-1",
          session_id: "child-session",
          live: true,
          state: "running",
        },
      });
    });

    const nextThread = getThreads()[0];
    expect(nextThread.lastRuntimeEventType).toBe("subagent.progress");
    expect(nextThread.runtimeEventCount).toBe(1);
  });

  it("线程身份重键（threadId 变、sessionId 不变）不重连 runtime stream", async () => {
    const initial = createThread();
    let threads: Thread[] = [initial];
    const onThreadsChange = (next: Thread[]) => {
      threads = next;
    };

    act(() => {
      root.render(
        <Harness
          renderLiveDeltas
          initialThread={initial}
          onThreadsChange={onThreadsChange}
        />,
      );
    });
    await vi.waitFor(() => {
      expect(mockStream).toHaveBeenCalledTimes(1);
    });
    const handlers = mockStream.mock.calls[0][1];

    // 启动期 `mergeRuntimeSessionsIntoThreads` 会丢弃占位线程、按 sessionId 重新
    // 归并：sessionId 不变、threadId 变化。旧实现把 threadId 放进 effect key，
    // 这里会 abort 重连（实测首屏 0.75s 内 6 条 SSE、5 条被 cleanup 掐断）。
    const rekeyed: Thread = { ...createThread(), id: "thread-9" };
    act(() => {
      root.render(
        <Harness
          renderLiveDeltas
          initialThread={rekeyed}
          onThreadsChange={onThreadsChange}
        />,
      );
    });
    await act(async () => {
      await Promise.resolve();
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
    });

    expect(mockStream).toHaveBeenCalledTimes(1);
    expect(mockStream.mock.calls[0][0]).toBe("session-1");

    // 事件回填必须落到「当前」线程：threadId 经 ref 读取，不做订阅依赖。
    act(() => {
      handlers.onEvent?.({
        type: "session_start",
        timestamp: "2026-08-30T00:00:03Z",
        payload: { status: "running", seq: 7 },
      });
    });
    expect(threads[0].id).toBe("thread-9");
    expect(threads[0].lastRuntimeEventType).toBe("session_start");
  });
});
