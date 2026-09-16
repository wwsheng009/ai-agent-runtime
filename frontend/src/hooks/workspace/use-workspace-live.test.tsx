// @vitest-environment jsdom

// P4-刷新续传（集成）：用「刷新后的页面」夹具直接驱动生产接线 `useWorkspaceLive`
// （= workspace-page 的 live 通道入口：回合认领 → 身份统一 → /runtime/stream），
// 验证用户可见的行为——
//   权威历史里只剩本回合的半截答复、快照报告服务端仍在跑 turn-9；
//   落库的 assistant_delta 帧必须继续写进**同一条**被认领的消息
//   （而不是另起一条占位、也不是整帧被闸门过滤）；
//   运行时生命周期事件同时桥接到轨迹快照（Q4 同一转换）。
//
// 链路各环节分别有单测（resumed-turn / use-resumed-session-turn /
// use-session-runtime-stream / deltas），本文件只验证它们在真实组合下的接线：
// 刷新后闸门（renderLiveDeltas + activeTurnId）确实被续传身份打开，页面消费的
// 身份（liveTurnId / currentSessionResponding）与之一致。

import {
  act,
  type Dispatch,
  type SetStateAction,
  useCallback,
  useEffect,
  useState,
} from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Thread } from "@/data/mock";
import type {
  RuntimeSessionActiveTurn,
  SessionRuntimeEvent,
} from "@/lib/runtime-api";
import { createRuntimeDeltaCoordinator } from "@/lib/workspace-thread-state";
import {
  useWorkspaceLive,
  type UseWorkspaceLiveResult,
} from "./use-workspace-live";
import {
  createTrajectoryStore,
  type TrajectoryStore,
} from "./use-trajectory-snapshot";

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return {
    ...actual,
    streamSessionRuntime: vi.fn(),
  };
});

import { streamSessionRuntime } from "@/lib/runtime-api";

const mockStream = vi.mocked(streamSessionRuntime);

type StreamHandlers = Parameters<typeof streamSessionRuntime>[1];

type StreamCall = {
  handlers: StreamHandlers;
  /** 正常收流（服务端/代理关闭长连接），区别于 abort 的强制中断。 */
  finish: () => void;
};

const calls: StreamCall[] = [];

function installControllableStream() {
  calls.length = 0;
  mockStream.mockImplementation((_sessionId, handlers) => {
    return new Promise<void>((resolve) => {
      const call: StreamCall = { handlers, finish: () => resolve() };
      calls.push(call);
      handlers.signal?.addEventListener("abort", () => resolve());
    });
  });
}

/** 刷新后的会话：权威历史已落库本回合的 user 消息与半截答复。 */
function refreshedThread(): Thread {
  return {
    id: "thread-1",
    title: "Thread",
    summary: "Summary",
    updatedAt: "2026-09-16T00:00:00Z",
    status: "active",
    sessionId: "session-1",
    transport: "live",
    runtimeSource: "runtime",
    lastError: null,
    tags: [],
    prompts: [],
    artifacts: [],
    messages: [
      {
        id: "msg-user-1",
        role: "user",
        author: "You",
        label: "user",
        segments: [{ type: "text", content: "继续写完" }],
      },
      {
        id: "msg-assistant-1",
        role: "assistant",
        author: "Runtime stream",
        label: "assistant",
        segments: [{ type: "text", content: "前半截答案" }],
      },
    ],
  };
}

function activeTurn(turnId = "turn-9"): RuntimeSessionActiveTurn {
  return {
    sessionId: "session-1",
    turnId,
    source: "agent_chat_stream",
    detached: true,
  };
}

/** 与真实落库帧同形：turn_id 对齐在途回合，stream_id + sequence 是去重身份。 */
function textDelta(sequence: number, delta: string): SessionRuntimeEvent {
  return {
    type: "assistant_delta",
    timestamp: "2026-09-16T00:00:01Z",
    payload: {
      delta,
      stream_id: "stream-1",
      sequence,
      seq: sequence,
      turn_id: "turn-9",
    },
  };
}

function textOf(thread: Thread): string {
  return thread.messages
    .filter((message) => message.role === "assistant")
    .map((message) =>
      message.segments
        .map((segment) => (segment.type === "text" ? segment.content : ""))
        .join(""),
    )
    .join("\n");
}

type LiveApi = Pick<
  UseWorkspaceLiveResult,
  "connectionStatus" | "currentSessionResponding" | "liveTurnId" | "retryConnection"
>;

function Harness({
  apiRef,
  activeTurn: serverTurn,
  deltaCoordinator,
  initialThread,
  trajectoryStore,
  onThreadsChange,
}: {
  apiRef: { current: LiveApi | null };
  activeTurn: RuntimeSessionActiveTurn | null;
  deltaCoordinator: ReturnType<typeof createRuntimeDeltaCoordinator>;
  initialThread: Thread;
  trajectoryStore: TrajectoryStore;
  onThreadsChange: (thread: Thread) => void;
}) {
  const [thread, setThread] = useState(initialThread);
  const setThreads = useCallback<Dispatch<SetStateAction<Thread[]>>>(
    (updater) => {
      setThread((current) => {
        const result =
          typeof updater === "function" ? updater([current]) : updater;
        const next = Array.isArray(result) ? result[0] : result;
        onThreadsChange(next);
        return next;
      });
    },
    [onThreadsChange],
  );

  // 与 workspace-page 完全同源：live 通道（续传身份 + 流建连）只有一个入口。
  const live = useWorkspaceLive({
    deltaCoordinator,
    localResponding: false,
    localTurnId: null,
    onRuntimeEvent: () => {},
    selectedThread: thread,
    sessionActiveTurn: serverTurn,
    sessionId: "session-1",
    setThreads,
    trajectoryReady: true,
    trajectoryStore,
  });
  useEffect(() => {
    apiRef.current = live;
  }, [apiRef, live]);
  return null;
}

type RenderedHarness = {
  apiRef: { current: LiveApi | null };
  store: TrajectoryStore;
  thread: () => Thread;
};

describe("刷新后续传（runtime/stream 增量接续到被认领的消息）", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    installControllableStream();
    container = document.createElement("div");
    document.body.appendChild(container);
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => {
      root.unmount();
    });
    container.remove();
    vi.clearAllMocks();
  });

  function renderHarness(serverTurn: RuntimeSessionActiveTurn | null): RenderedHarness {
    const apiRef: { current: LiveApi | null } = { current: null };
    let latest: Thread = refreshedThread();
    const deltaCoordinator = createRuntimeDeltaCoordinator();
    const trajectoryStore = createTrajectoryStore();
    act(() => {
      root.render(
        <Harness
          apiRef={apiRef}
          activeTurn={serverTurn}
          deltaCoordinator={deltaCoordinator}
          initialThread={latest}
          trajectoryStore={trajectoryStore}
          onThreadsChange={(next) => {
            latest = next;
          }}
        />,
      );
    });
    return { apiRef, store: trajectoryStore, thread: () => latest };
  }

  /** 运行时通道按 120ms 最小间隔合帧提交：等一次兑现再断言。 */
  async function flushRuntimeCommits() {
    await act(async () => {
      await new Promise<void>((resolve) => setTimeout(resolve, 180));
    });
  }

  it("认领历史半截消息，并把后续增量写进同一条（不另起占位、不丢帧）", async () => {
    const { apiRef, thread } = renderHarness(activeTurn());

    const claimed = thread().messages[thread().messages.length - 1];
    expect(claimed).toMatchObject({
      id: "msg-assistant-1",
      streaming: true,
      runtimeTurnId: "turn-9",
    });
    // 页面消费的身份与闸门一致：有在途回合 = 当前会话在生成回复。
    expect(apiRef.current).toMatchObject({
      currentSessionResponding: true,
      liveTurnId: "turn-9",
    });

    act(() => {
      calls[0].handlers.onEvent?.(textDelta(1, "，继续"));
    });
    await flushRuntimeCommits();

    expect(thread().messages).toHaveLength(2);
    expect(thread().messages[1]).toMatchObject({
      id: "msg-assistant-1",
      streaming: true,
    });
    expect(textOf(thread())).toBe("前半截答案，继续");

    act(() => {
      calls[0].handlers.onEvent?.(textDelta(2, "写完后半截"));
    });
    await flushRuntimeCommits();

    // 仍是一条消息：续传回合的增量不允许把同一轮答复劈成两条。
    expect(thread().messages).toHaveLength(2);
    expect(textOf(thread())).toBe("前半截答案，继续写完后半截");
  });

  it("重复帧被去重（同一 stream_id + sequence 只写一次）", async () => {
    const { thread } = renderHarness(activeTurn());

    act(() => {
      calls[0].handlers.onEvent?.(textDelta(1, "，继续"));
      calls[0].handlers.onEvent?.(textDelta(1, "，继续"));
    });
    await flushRuntimeCommits();

    expect(textOf(thread())).toBe("前半截答案，继续");
  });

  it("没有续传身份时不渲染增量（对照：这正是刷新后「整条流都断了」的表现）", async () => {
    const { apiRef, thread } = renderHarness(null);

    expect(apiRef.current).toMatchObject({
      currentSessionResponding: false,
      liveTurnId: null,
    });

    act(() => {
      calls[0].handlers.onEvent?.(textDelta(1, "，继续"));
    });
    await flushRuntimeCommits();

    expect(thread().messages).toHaveLength(2);
    expect(textOf(thread())).toBe("前半截答案");
  });

  it("运行时生命周期事件桥接到轨迹快照（可渲染事件 push / 过滤事件前移空洞游标）", async () => {
    const { store } = renderHarness(activeTurn());

    act(() => {
      calls[0].handlers.onEvent?.({
        type: "approval_requested",
        timestamp: "t",
        payload: { seq: 1, tool_name: "bash" },
      } as SessionRuntimeEvent);
    });
    store.flush();
    expect(store.getSnapshot().items).toHaveLength(1);

    act(() => {
      calls[0].handlers.onEvent?.({
        type: "tool_started",
        timestamp: "t",
        payload: { seq: 2 },
      } as SessionRuntimeEvent);
    });
    // skip 也必须前移游标：否则后续事件的空洞会让轨迹永久卡 pending。
    expect(store.getSnapshot().lastEventSeq).toBe(2);
  });
});
