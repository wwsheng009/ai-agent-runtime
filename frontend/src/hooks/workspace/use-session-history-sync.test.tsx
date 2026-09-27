// @vitest-environment jsdom

import { type Dispatch, type SetStateAction, act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ChatMessage, Thread } from "@/data/mock";
import {
  getSessionHistory,
  getSessionRuntimeState,
  type SessionHistoryResponse,
} from "@/lib/runtime-api";
import {
  HISTORY_OLDER_PAGE_SIZE,
  type HistoryEarlierLoader,
} from "@/lib/thread-state/history-paging";

import {
  shouldReconcileDegradedThread,
  shouldReloadHistoryForRuntimeEvent,
  shouldSyncSessionHistory,
  useSessionHistorySync,
} from "@/hooks/workspace/use-session-history-sync";

vi.mock("@/lib/runtime-api", () => ({
  getSessionHistory: vi.fn(),
  getSessionRuntimeState: vi.fn(),
}));

const mockGetSessionHistory = vi.mocked(getSessionHistory);
const mockGetSessionRuntimeState = vi.mocked(getSessionRuntimeState);

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

function historyResponse(): SessionHistoryResponse {
  return {
    session_id: "session-1",
    history: [],
  } as unknown as SessionHistoryResponse;
}

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

type HarnessProps = {
  applySessionHistoryToThread: (
    thread: Thread,
    response: SessionHistoryResponse,
  ) => Thread;
  isResponding: boolean;
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
  selectedThread: Thread | undefined;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
};

function Harness(props: HarnessProps) {
  useSessionHistorySync(props);
  return null;
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

  // 断流自愈：降级态且无在途回合时必须补一次权威历史（否则原地页签永久停在截断前缀，
  // 而重开的页签直接读 /history 是完整的）。
  it("reconciles a degraded thread only when no turn is in flight", () => {
    expect(
      shouldReconcileDegradedThread(createThread({ transport: "error" }), false),
    ).toBe(true);
    expect(
      shouldReconcileDegradedThread(createThread({ transport: "error" }), true),
    ).toBe(false);
    // 健康线程不走这条兜底路径（常规同步负责）。
    expect(
      shouldReconcileDegradedThread(createThread(), false),
    ).toBe(false);
    expect(
      shouldReconcileDegradedThread(createThread({ sessionId: undefined, transport: "error" }), false),
    ).toBe(false);
  });

  // 历史自身的请求失败（加载更早 / 同步 / 恢复探活）不再自动探活：那会变成重试风暴，
  // 交还连接徽标的「重试」。流链路 / 轨迹恢复导致的降级才是自动收敛的对象。
  it("does not auto-probe when the degradation is a history request failure", () => {
    expect(
      shouldReconcileDegradedThread(
        createThread({ transport: "error", lastError: "Session history load failed: boom" }),
        false,
      ),
    ).toBe(false);
    expect(
      shouldReconcileDegradedThread(
        createThread({ transport: "error", lastError: "Session history sync failed: boom" }),
        false,
      ),
    ).toBe(false);
    expect(
      shouldReconcileDegradedThread(
        createThread({ transport: "error", lastError: "Runtime stream failed: network error" }),
        false,
      ),
    ).toBe(true);
    expect(
      shouldReconcileDegradedThread(
        createThread({ transport: "error", lastError: "Trajectory recovery failed: boom" }),
        false,
      ),
    ).toBe(true);
  });

  it("treats only rewind-style events as history rewrites", () => {
    expect(shouldReloadHistoryForRuntimeEvent("rewind_finished")).toBe(true);
    expect(shouldReloadHistoryForRuntimeEvent("backtrack_finished")).toBe(true);
    expect(shouldReloadHistoryForRuntimeEvent("assistant_delta")).toBe(false);
    expect(shouldReloadHistoryForRuntimeEvent(undefined)).toBe(false);
  });
});

// 用户诉求：还原/回溯后消息列表必须跟着回滚，不能停留在旧内容。
describe("useSessionHistorySync runtime-event resync", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let threads: Thread[];
  let setThreads: Dispatch<SetStateAction<Thread[]>>;
  let applySessionHistoryToThread: (thread: Thread) => Thread;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
    threads = [createThread()];
    setThreads = (value) => {
      threads = typeof value === "function" ? value(threads) : value;
    };
    applySessionHistoryToThread = (thread) => ({ ...thread, title: "synced" });
    mockGetSessionHistory.mockReset();
    mockGetSessionHistory.mockResolvedValue(historyResponse());
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => {
        root?.unmount();
      });
    }
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function renderSync(
    lastRuntimeEventType: string,
    runtimeEventCount: number,
  ) {
    await act(async () => {
      const element = (
        <Harness
          applySessionHistoryToThread={applySessionHistoryToThread}
          isResponding={false}
          lastRuntimeEventType={lastRuntimeEventType}
          runtimeEventCount={runtimeEventCount}
          selectedThread={threads[0]}
          setThreads={setThreads}
        />
      );
      if (root) {
        root.render(element);
        return;
      }
      root = createRoot(container);
      root.render(element);
    });
  }

  it("re-fetches authoritative history once a rewind event arrives", async () => {
    await renderSync("turn_finished", 1);
    expect(mockGetSessionHistory).toHaveBeenCalledTimes(1);
    expect(threads[0]?.title).toBe("synced");

    await renderSync("rewind_finished", 2);
    expect(mockGetSessionHistory).toHaveBeenCalledTimes(2);
    expect(mockGetSessionHistory).toHaveBeenCalledWith("session-1");
  });

  it("does not re-sync for unrelated runtime events", async () => {
    await renderSync("turn_finished", 1);
    await renderSync("assistant_delta", 2);

    expect(mockGetSessionHistory).toHaveBeenCalledTimes(1);
  });
});

// 用户诉求：降级态（transport=error）下点连接徽标「重试」必须有可见恢复——
// 只重启会话流时，直连 chat / 轨迹恢复等来源的降级会一直挂着，按钮形同失效。
describe("useSessionHistorySync manual recovery", () => {
  // 稳定引用：`applySessionHistoryToThread` 每帧新建会让 `syncHistory` 换身份，
  // 常规同步 effect 随之在每次渲染后重跑（本文件其它 harness 同口径）。
  const keepSyncedTitle = (thread: Thread) => ({ ...thread, title: "synced" });

  let container: HTMLDivElement;
  let root: Root | null;
  let threads: Thread[];
  let setThreads: Dispatch<SetStateAction<Thread[]>>;
  let recoveryRef: { current: (() => Promise<boolean>) | null };

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
    threads = [
      createThread({ transport: "error", lastError: "agent chat stream failed" }),
    ];
    setThreads = (value) => {
      threads = typeof value === "function" ? value(threads) : value;
    };
    recoveryRef = { current: null };
    mockGetSessionHistory.mockReset();
    mockGetSessionRuntimeState.mockReset();
    mockGetSessionRuntimeState.mockResolvedValue(null);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => {
        root?.unmount();
      });
    }
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function RecoveryHarness() {
    const { recoverSessionHistory } = useSessionHistorySync({
      applySessionHistoryToThread: keepSyncedTitle,
      isResponding: false,
      selectedThread: threads[0],
      setThreads,
    });
    useEffect(() => {
      recoveryRef.current = recoverSessionHistory;
    }, [recoverSessionHistory]);
    return null;
  }

  function renderRecovery() {
    act(() => {
      if (root) {
        root.render(<RecoveryHarness />);
        return;
      }
      root = createRoot(container);
      root.render(<RecoveryHarness />);
    });
  }

  // 用户诉求（断流自愈）：回合中途 SSE 断掉后，原地页签必须自己收敛到权威历史——
  // 否则它会永久停在截断前缀上，而重开的页签（直接读 /history）却是完整的。
  it("auto-converges a degraded thread once the turn is over", async () => {
    mockGetSessionHistory.mockResolvedValue(historyResponse());
    renderRecovery();
    await act(async () => {});

    expect(mockGetSessionHistory).toHaveBeenCalledTimes(1);
    expect(mockGetSessionHistory).toHaveBeenCalledWith("session-1");
    expect(threads[0]?.transport).toBe("live");
    expect(threads[0]?.lastError).toBeNull();
    expect(threads[0]?.title).toBe("synced");
  });

  // 权威在途闸门：chat POST 被掐断后本地立即空闲，但服务端回合仍在跑
  // （resume_on_disconnect）——此时历史里没有最终消息，必须等回合收尾再对齐，
  // 否则会「清了降级却留下截断」，且增量游标已越过被丢弃的尾部帧，永远补不回。
  it("waits for the server turn to finish before reconciling", async () => {
    vi.useFakeTimers();
    try {
      mockGetSessionRuntimeState.mockResolvedValue({
        activeTurn: { turnId: "turn-1" },
      } as Awaited<ReturnType<typeof getSessionRuntimeState>>);
      mockGetSessionHistory.mockResolvedValue(historyResponse());

      renderRecovery();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      // 服务端回合在跑：不拉历史，且保持降级（不清 error）。
      expect(mockGetSessionHistory).not.toHaveBeenCalled();
      expect(threads[0]?.transport).toBe("error");

      // 回合收尾（快照 active_turn 消失）→ 下一个探测周期自动收敛。
      mockGetSessionRuntimeState.mockResolvedValue(null);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(4000);
      });
      expect(mockGetSessionHistory).toHaveBeenCalledTimes(1);
      expect(threads[0]?.transport).toBe("live");
      expect(threads[0]?.lastError).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("makes at most one automatic attempt per degradation, manual retry still works", async () => {
    mockGetSessionHistory.mockRejectedValue(new Error("runtime offline"));
    renderRecovery();
    await act(async () => {});

    expect(mockGetSessionHistory).toHaveBeenCalledTimes(1);
    // 同一降级段再次渲染不产生第二次自动请求（失败后交给人工入口，避免重试风暴）。
    renderRecovery();
    await act(async () => {});
    expect(mockGetSessionHistory).toHaveBeenCalledTimes(1);
    expect(threads[0]?.transport).toBe("error");

    let recovered = true;
    await act(async () => {
      recovered = await recoveryRef.current!();
    });

    expect(recovered).toBe(false);
    expect(mockGetSessionHistory).toHaveBeenCalledTimes(2);
    expect(threads[0]?.transport).toBe("error");
    expect(threads[0]?.lastError).toBe(
      "Session history recovery failed: runtime offline",
    );
  });
});

// 用户诉求（本轮）：「加载更早」必须真的看得见、点得动，不能点了没反应。
// 对话面分页 = 权威历史尾部优先（/history 的 before_seq 排他上界），与轨迹窗口同语义。
describe("useSessionHistorySync earlier paging", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let threads: Thread[];
  let setThreads: Dispatch<SetStateAction<Thread[]>>;
  let loaderRef: { current: HistoryEarlierLoader | null };

  function historyMessage(id: string, content: string) {
    return { role: "user", content, metadata: { message_id: id } };
  }

  function historyPage(
    history: ReturnType<typeof historyMessage>[],
    hasMore: boolean,
    nextBeforeSeq: number,
    sessionId = "session-1",
  ): SessionHistoryResponse {
    return {
      session_id: sessionId,
      history,
      count: history.length,
      has_more: hasMore,
      next_before_seq: nextBeforeSeq,
    } as unknown as SessionHistoryResponse;
  }

  function chatMessage(id: string): ChatMessage {
    return {
      id,
      role: "assistant",
      author: "Runtime history",
      label: "history",
      segments: [{ type: "text", content: id }],
    };
  }

  // 稳定引用：applySessionHistoryToThread 每帧新建会让 syncHistory 换身份，
  // 挂载同步 effect 就会在每次渲染后重跑，请求计数失去意义（本文件其他 harness 同理）。
  const keepHistory = (thread: Thread) => thread;

  function PagingHarness({ lastRuntimeEventType }: { lastRuntimeEventType?: string }) {
    const { earlierLoader } = useSessionHistorySync({
      applySessionHistoryToThread: keepHistory,
      isResponding: false,
      lastRuntimeEventType,
      runtimeEventCount: 1,
      selectedThread: threads[0],
      setThreads,
    });
    useEffect(() => {
      loaderRef.current = earlierLoader;
    }, [earlierLoader]);
    return null;
  }

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
    threads = [createThread({ messages: [chatMessage("m-3")] })];
    setThreads = (value) => {
      threads = typeof value === "function" ? value(threads) : value;
    };
    loaderRef = { current: null };
    mockGetSessionHistory.mockReset();
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => {
        root?.unmount();
      });
    }
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function flushPending() {
    // 让 loadEarlier 内部的 await 链（分页请求 → setState）跑完再断言。
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }

  async function renderPaging(lastRuntimeEventType?: string) {
    await act(async () => {
      const element = <PagingHarness lastRuntimeEventType={lastRuntimeEventType} />;
      if (root) {
        root.render(element);
        return;
      }
      root = createRoot(container);
      root.render(element);
    });
    await flushPending();
  }

  async function loadEarlierOnce() {
    await act(async () => {
      loaderRef.current?.onLoadEarlier();
    });
    await flushPending();
  }

  it("exposes the entry from the first page and prepends one older page per click", async () => {
    mockGetSessionHistory
      .mockResolvedValueOnce(historyPage([historyMessage("m-3", "newest")], true, 3))
      .mockResolvedValueOnce(historyPage([historyMessage("m-1", "older")], false, 0));

    await renderPaging();
    expect(loaderRef.current?.hasMore).toBe(true);
    expect(loaderRef.current?.loading).toBe(false);

    await loadEarlierOnce();
    // 游标来自首屏的 next_before_seq（3），页大小与后端默认对齐。
    expect(mockGetSessionHistory).toHaveBeenLastCalledWith("session-1", {
      beforeSeq: 3,
      limit: HISTORY_OLDER_PAGE_SIZE,
    });
    // 更早一页前插到消息列表最前，且翻到头后入口收敛。
    expect(threads[0]?.messages.map((message) => message.id)).toEqual(["m-1", "m-3"]);
    expect(loaderRef.current?.hasMore).toBe(false);
    expect(loaderRef.current?.loading).toBe(false);
  });

  it("continues to the next page when the fetched page overlaps the resident window", async () => {
    // 重同步会把游标退回「最新一页」的边界，首次点击可能与常住窗口整页重叠：
    // 没有可见新增时不能停手，否则用户看到的就是「点了没反应」。
    threads = [
      createThread({ messages: [chatMessage("m-3"), chatMessage("m-4")] }),
    ];
    mockGetSessionHistory
      .mockResolvedValueOnce(
        historyPage([historyMessage("m-3", "a"), historyMessage("m-4", "b")], true, 5),
      )
      .mockResolvedValueOnce(
        historyPage([historyMessage("m-3", "a"), historyMessage("m-4", "b")], true, 3),
      )
      .mockResolvedValueOnce(historyPage([historyMessage("m-1", "old")], false, 0));

    await renderPaging();
    await loadEarlierOnce();

    expect(mockGetSessionHistory).toHaveBeenCalledTimes(3);
    expect(mockGetSessionHistory).toHaveBeenNthCalledWith(2, "session-1", {
      beforeSeq: 5,
      limit: HISTORY_OLDER_PAGE_SIZE,
    });
    expect(mockGetSessionHistory).toHaveBeenNthCalledWith(3, "session-1", {
      beforeSeq: 3,
      limit: HISTORY_OLDER_PAGE_SIZE,
    });
    expect(threads[0]?.messages.map((message) => message.id)).toEqual([
      "m-1",
      "m-3",
      "m-4",
    ]);
    expect(loaderRef.current?.hasMore).toBe(false);
  });

  it("ignores repeated clicks while a page is still in flight", async () => {
    let resolveSecond: ((value: SessionHistoryResponse) => void) | null = null;
    mockGetSessionHistory
      .mockResolvedValueOnce(historyPage([historyMessage("m-3", "a")], true, 3))
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveSecond = resolve;
          }),
      );

    await renderPaging();
    await act(async () => {
      loaderRef.current?.onLoadEarlier();
      loaderRef.current?.onLoadEarlier();
    });

    // 连点只发一次请求（在途 ref 兜住状态提交前的第二次触发）。
    expect(mockGetSessionHistory).toHaveBeenCalledTimes(2);
    await act(async () => {
      resolveSecond?.(historyPage([historyMessage("m-1", "old")], false, 0));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(threads[0]?.messages.map((message) => message.id)).toEqual(["m-1", "m-3"]);
  });

  it("keeps the cursor and reports the failure when the older page cannot be loaded", async () => {
    mockGetSessionHistory
      .mockResolvedValueOnce(historyPage([historyMessage("m-3", "a")], true, 3))
      .mockRejectedValueOnce(new Error("boom"))
      .mockResolvedValueOnce(historyPage([historyMessage("m-1", "old")], false, 0));

    await renderPaging();
    await loadEarlierOnce();

    expect(threads[0]?.transport).toBe("error");
    expect(threads[0]?.lastError).toBe("Session history load failed: boom");
    // 游标原地不动：入口仍在，再点一次就是原样重试，而不是静默失效。
    expect(loaderRef.current?.hasMore).toBe(true);
    expect(loaderRef.current?.loading).toBe(false);

    await loadEarlierOnce();
    expect(mockGetSessionHistory).toHaveBeenLastCalledWith("session-1", {
      beforeSeq: 3,
      limit: HISTORY_OLDER_PAGE_SIZE,
    });
    expect(threads[0]?.messages.map((message) => message.id)).toEqual(["m-1", "m-3"]);
  });

  it("drops the previous session cursor after switching sessions", async () => {
    mockGetSessionHistory.mockResolvedValueOnce(
      historyPage([historyMessage("m-3", "a")], true, 3),
    );
    await renderPaging();
    expect(loaderRef.current?.hasMore).toBe(true);

    mockGetSessionHistory.mockResolvedValueOnce(
      historyPage([], false, 0, "session-2"),
    );
    threads = [createThread({ id: "thread-2", sessionId: "session-2" })];
    await renderPaging();

    // 新会话没有更早页：不得沿用旧会话的游标（否则入口会去翻别的会话）。
    expect(loaderRef.current?.hasMore).toBe(false);
    mockGetSessionHistory.mockClear();
    await loadEarlierOnce();
    expect(mockGetSessionHistory).not.toHaveBeenCalled();
  });

  it("rewinds the cursor to the newest page boundary after a history rewrite", async () => {
    mockGetSessionHistory
      .mockResolvedValueOnce(historyPage([historyMessage("m-3", "a")], true, 5))
      .mockResolvedValueOnce(historyPage([historyMessage("m-1", "old")], true, 3))
      .mockResolvedValueOnce(historyPage([historyMessage("m-2", "oldest")], true, 1))
      .mockResolvedValueOnce(historyPage([historyMessage("m-5", "keep")], false, 0));

    await renderPaging();
    await loadEarlierOnce();
    // 第一次点击用首屏边界（5），服务端把游标推进到 3。
    expect(mockGetSessionHistory).toHaveBeenNthCalledWith(2, "session-1", {
      beforeSeq: 5,
      limit: HISTORY_OLDER_PAGE_SIZE,
    });

    // 回滚事件重同步：走「最新一页」（不带 before_seq），游标随之退回该页边界。
    await renderPaging("rewind_finished");
    expect(mockGetSessionHistory).toHaveBeenNthCalledWith(3, "session-1");

    await loadEarlierOnce();
    expect(mockGetSessionHistory).toHaveBeenLastCalledWith("session-1", {
      beforeSeq: 1,
      limit: HISTORY_OLDER_PAGE_SIZE,
    });
  });
});
