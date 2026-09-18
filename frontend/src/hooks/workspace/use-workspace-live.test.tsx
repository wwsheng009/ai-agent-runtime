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
// 身份（liveTurnId / currentSessionResponding）与之一致，以及刷新后的停止入口
// （无本地回合 → 必须显式投递 interrupt，见「建议 3」后端 cancel 契约）。

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
import {
  getLiveDiagnosticsSnapshot,
  resetLiveDiagnostics,
} from "@/lib/live-diagnostics/store";
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

vi.mock("@/api/runtime/session-turn-control", () => ({
  requestSessionTurnInterrupt: vi.fn().mockResolvedValue(null),
}));

import { requestSessionTurnInterrupt } from "@/api/runtime/session-turn-control";
import { streamSessionRuntime } from "@/lib/runtime-api";

const mockStream = vi.mocked(streamSessionRuntime);
const mockInterrupt = vi.mocked(requestSessionTurnInterrupt);

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
  | "connectionStatus"
  | "currentSessionResponding"
  | "liveTurnId"
  | "retryConnection"
  | "stopResumedTurn"
>;

function Harness({
  apiRef,
  activeTurn: serverTurn,
  deltaCoordinator,
  initialThread,
  localResponding = false,
  localTurnId = null,
  trajectoryStore,
  onThreadsChange,
  onRefreshRuntimeState,
}: {
  apiRef: { current: LiveApi | null };
  activeTurn: RuntimeSessionActiveTurn | null;
  deltaCoordinator: ReturnType<typeof createRuntimeDeltaCoordinator>;
  initialThread: Thread;
  localResponding?: boolean;
  localTurnId?: string | null;
  trajectoryStore: TrajectoryStore;
  onThreadsChange: (thread: Thread) => void;
  onRefreshRuntimeState?: () => void;
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
    localResponding,
    localTurnId,
    onRuntimeEvent: () => {},
    refreshRuntimeState: onRefreshRuntimeState,
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
  /** 模拟 `/runtime` 快照刷新返回的新在途回合（续传认领的唯一入口）。 */
  rerenderServerTurn: (turn: RuntimeSessionActiveTurn | null) => void;
  /** 模拟直连回合身份建立 / 清空（看门狗、错误路径会清空）。 */
  rerenderLocalTurn: (
    localTurnId: string | null,
    localResponding: boolean,
  ) => void;
};

describe("刷新后续传（runtime/stream 增量接续到被认领的消息）", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    installControllableStream();
    // 「网络详情」观测 store 是模块级单例：用例之间必须隔离，否则计数会串台。
    resetLiveDiagnostics();
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

  function renderHarness(
    serverTurn: RuntimeSessionActiveTurn | null,
    options: {
      initialThread?: Thread;
      localResponding?: boolean;
      localTurnId?: string | null;
      onRefreshRuntimeState?: () => void;
    } = {},
  ): RenderedHarness {
    const apiRef: { current: LiveApi | null } = { current: null };
    let latest: Thread = options.initialThread ?? refreshedThread();
    let localTurnId = options.localTurnId ?? null;
    let localResponding = options.localResponding ?? false;
    let currentServerTurn = serverTurn;
    // 与 workspace-page 同源：增量认领协调器跨重渲染保持同一实例
    // （快照刷新只换 activeTurn prop，认领账目不能被重置）。
    const deltaCoordinator = createRuntimeDeltaCoordinator();
    const trajectoryStore = createTrajectoryStore();
    const renderWith = (turn: RuntimeSessionActiveTurn | null) => {
      currentServerTurn = turn;
      act(() => {
        root.render(
          <Harness
            apiRef={apiRef}
            activeTurn={turn}
            deltaCoordinator={deltaCoordinator}
            initialThread={latest}
            localResponding={localResponding}
            localTurnId={localTurnId}
            trajectoryStore={trajectoryStore}
            onRefreshRuntimeState={options.onRefreshRuntimeState}
            onThreadsChange={(next) => {
              latest = next;
            }}
          />,
        );
      });
    };
    renderWith(serverTurn);
    return {
      apiRef,
      store: trajectoryStore,
      thread: () => latest,
      rerenderServerTurn: renderWith,
      rerenderLocalTurn: (nextTurnId, nextResponding) => {
        localTurnId = nextTurnId;
        localResponding = nextResponding;
        renderWith(currentServerTurn);
      },
    };
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

  it("直连身份清空后，本地已终态的回合不被仍在途的快照重新认领（re-adopt）", async () => {
    // 直连回合在跑：消息由本地直连路径创建（streaming + runtimeTurnId），
    // `/runtime` 快照也报告同一回合。
    const inFlight = refreshedThread();
    inFlight.messages[1] = {
      ...inFlight.messages[1],
      streaming: true,
      runtimeTurnId: "turn-9",
    };
    const { apiRef, thread, rerenderLocalTurn } = renderHarness(activeTurn(), {
      initialThread: inFlight,
      localResponding: true,
      localTurnId: "turn-9",
    });

    expect(apiRef.current?.liveTurnId).toBe("turn-9");

    // runtime 通道先到终态帧（直连流此时已无数据）——本地立即定稿。
    act(() => {
      calls[0].handlers.onEvent?.({
        type: "chat.sse.done",
        timestamp: "2026-09-16T00:00:05Z",
        payload: { turn_id: "turn-9", status: "completed", seq: 42 },
      });
    });
    await flushRuntimeCommits();
    expect(thread().messages[1]).toMatchObject({
      streaming: false,
      runtimeTurnId: "turn-9",
    });

    // 直连身份随后清空（看门狗 / 错误路径），而快照尚未收敛：adoptable 由
    // false 翻 true。没有本地终态仲裁时，adopt 会把刚定稿的消息补回 streaming。
    rerenderLocalTurn(null, false);
    await flushRuntimeCommits();

    expect(thread().messages[1]).toMatchObject({ streaming: false });
    expect(apiRef.current).toMatchObject({
      liveTurnId: null,
      currentSessionResponding: false,
    });
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

  it("没有认领身份时不渲染增量，但会主动触发一次快照刷新（未认领回合发现）", async () => {
    const refresh = vi.fn();
    const { apiRef, thread } = renderHarness(null, {
      onRefreshRuntimeState: refresh,
    });

    expect(apiRef.current).toMatchObject({
      currentSessionResponding: false,
      liveTurnId: null,
    });

    act(() => {
      calls[0].handlers.onEvent?.(textDelta(1, "，继续"));
    });
    await flushRuntimeCommits();

    // 闸门仍关着：归属未定前不往消息上写（这一帧也可能属于别的回合）。
    expect(thread().messages).toHaveLength(2);
    expect(textOf(thread())).toBe("前半截答案");
    // 但「本页没认领、服务端在产增量」必须触发认领：刷新快照是唯一入口
    // （续传心跳只在认领之后才启动，见 use-resumed-session-turn）。
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("未认领增量触发节流刷新；快照认领后续传增量写回同一条消息", async () => {
    const refresh = vi.fn();
    const { rerenderServerTurn, thread } = renderHarness(null, {
      onRefreshRuntimeState: refresh,
    });

    act(() => {
      calls[0].handlers.onEvent?.(textDelta(1, "，继续"));
      calls[0].handlers.onEvent?.(textDelta(2, "写完后半截"));
    });
    await flushRuntimeCommits();

    // 同一回合的连续帧只换一次快照刷新（发现即可，不能变成每帧一次 GET）。
    expect(refresh).toHaveBeenCalledTimes(1);

    // 「网络详情」观测：未认领回合 / 快照刷新 / 被拦增量在接线层记账——
    // 这是把「页面不动」归因到「事件到了但闸门关着」的证据（而不是「SSE 没有事件」）。
    const observed = getLiveDiagnosticsSnapshot("session-1");
    expect(observed.counters.unownedTurns).toBe(1);
    expect(observed.counters.snapshotRefreshes).toBe(1);
    expect(observed.counters.blockedDeltas).toBe(2);
    expect(observed.gate.rendering).toBe(false);

    // 快照（真实环境里由 refresh 拉回）报告服务端仍在跑 turn-9：续传认领挂载。
    rerenderServerTurn(activeTurn());
    expect(thread().messages[thread().messages.length - 1]).toMatchObject({
      id: "msg-assistant-1",
      streaming: true,
      runtimeTurnId: "turn-9",
    });

    act(() => {
      calls[0].handlers.onEvent?.(textDelta(3, "，收尾"));
    });
    await flushRuntimeCommits();

    // 认领之后闸门重新打开：后续增量继续写进同一条（不另起占位、不再丢帧）。
    expect(thread().messages).toHaveLength(2);
    expect(textOf(thread())).toBe("前半截答案，收尾");
    // 闸门开启后，同一回合的增量不再计入「被拦增量」。
    const claimed = getLiveDiagnosticsSnapshot("session-1");
    expect(claimed.gate.rendering).toBe(true);
    expect(claimed.counters.blockedDeltas).toBe(2);
  });

  it("刷新后停止：投递 interrupt（带续传回合身份）并主动刷新一次快照", async () => {
    const refresh = vi.fn();
    const { apiRef } = renderHarness(activeTurn(), {
      onRefreshRuntimeState: refresh,
    });

    await act(async () => {
      await apiRef.current?.stopResumedTurn();
    });

    // 回合身份必须带上：后端据此校验，迟到的 stop 不得误伤新回合（409 turn_mismatch）。
    expect(mockInterrupt).toHaveBeenCalledWith("session-1", "turn-9");
    // 刷新一次快照让 active_turn 收敛，不必等续传心跳（5s）按钮才变回发送态。
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("没有续传回合时停止是空操作（本地回合停止路径不重复投递）", async () => {
    const refresh = vi.fn();
    const { apiRef } = renderHarness(null, { onRefreshRuntimeState: refresh });

    await act(async () => {
      await apiRef.current?.stopResumedTurn();
    });

    expect(mockInterrupt).not.toHaveBeenCalled();
    expect(refresh).not.toHaveBeenCalled();
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
