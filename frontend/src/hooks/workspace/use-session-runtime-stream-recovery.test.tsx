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
import { useSessionRuntimeStream } from "@/hooks/workspace/use-session-runtime-stream";
import {
  applyRuntimeDeltaToThread,
  applyRuntimeEventToThread,
  createRuntimeDeltaCoordinator,
  getRuntimeEventSeq,
  mergeRuntimeEvent,
} from "@/lib/workspace-thread-state";
import type { SessionRuntimeEvent } from "@/lib/runtime-api";
import type { ConnectionStatus } from "@/lib/connection-status";

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
  after: number;
  handlers: StreamHandlers;
  aborted: boolean;
  /** 正常收流（服务端/代理关闭长连接），区别于 abort 的强制中断。 */
  finish: () => void;
};

const calls: StreamCall[] = [];

function installControllableStream() {
  calls.length = 0;
  mockStream.mockImplementation((_sessionId, handlers) => {
    return new Promise<void>((resolve) => {
      const call: StreamCall = {
        after: handlers.after ?? 0,
        handlers,
        aborted: false,
        finish: () => resolve(),
      };
      calls.push(call);
      handlers.signal?.addEventListener("abort", () => {
        call.aborted = true;
        resolve();
      });
    });
  });
}

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
        runtimeTurnId: "turn-1",
        segments: [{ type: "text", content: "" }],
      },
    ],
    artifacts: [],
  };
}

function textDelta(sequence: number, delta: string): SessionRuntimeEvent {
  return {
    type: "assistant_delta",
    timestamp: "2026-08-30T00:00:01Z",
    // turn_id 对齐活动 turn（否则会被 turn gate 拦下）；
    // seq 是重连游标（after）；stream_id + sequence 是 delta 幂等身份。
    payload: {
      delta,
      stream_id: "stream-1",
      sequence,
      seq: sequence,
      turn_id: "turn-1",
    },
  };
}

type StreamApi = {
  connectionStatus: ConnectionStatus;
  retryConnection: () => void;
};

// 依赖数组里的函数必须保持稳定引用，否则每次 render 都会 abort/重启流。
function readErrorMessage(error: unknown, fallback: string) {
  return error instanceof Error ? error.message : fallback;
}

function messageText(thread: Thread) {
  return thread.messages
    .map((message) =>
      message.segments
        .map((segment) => (segment.type === "text" ? segment.content : ""))
        .join(""),
    )
    .join("\n");
}

function Harness({
  apiRef,
  deltaCoordinator,
  initialThread,
  onThreadsChange,
}: {
  apiRef: { current: StreamApi | null };
  deltaCoordinator: ReturnType<typeof createRuntimeDeltaCoordinator>;
  initialThread: Thread;
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
  const stream = useSessionRuntimeStream({
    applyRuntimeEventToThread,
    applyRuntimeDeltaToThread,
    getErrorMessage: readErrorMessage,
    getRuntimeEventSeq,
    mergeRuntimeEvent,
    activeTurnId: "turn-1",
    deltaCoordinator,
    renderLiveDeltas: true,
    selectedThread: thread,
    setThreads,
  });
  // ref 只能在提交阶段写入（react-hooks/refs）；测试断言发生在 act() 内的
  // effect 冲刷之后，读取到的始终是最新一次提交的流对象。
  useEffect(() => {
    apiRef.current = stream;
  }, [apiRef, stream]);
  return null;
}

describe("useSessionRuntimeStream connection status and manual retry", () => {
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

  function renderHarness() {
    const apiRef: { current: StreamApi | null } = { current: null };
    const texts: string[] = [];
    const thread = createThread();
    const deltaCoordinator = createRuntimeDeltaCoordinator();
    const onThreadsChange = (next: Thread) => {
      const content = messageText(next);
      // 只记录内容变化，避免无意义的重渲染干扰幂等断言。
      if (texts[texts.length - 1] !== content) {
        texts.push(content);
      }
    };
    act(() => {
      root.render(
        <Harness
          apiRef={apiRef}
          deltaCoordinator={deltaCoordinator}
          initialThread={thread}
          onThreadsChange={onThreadsChange}
        />,
      );
    });
    return { apiRef, texts };
  }

  /**
   * 运行时通道的提交是**合帧**的（复用 agent-chat-turn/streaming-frame.ts 的
   * rAF + 最小提交间隔 120ms）：`onEvent` 只把事件入队，断言「渲染了几次」之前
   * 必须等一次提交兑现，否则读到的还是提交前的 `texts`。等待上限取 180ms
   * （120ms 最小间隔 + 一帧 rAF + 余量）。
   */
  async function flushRuntimeCommits() {
    await act(async () => {
      await new Promise<void>((resolve) => setTimeout(resolve, 180));
    });
  }

  it("moves from connecting to online when the stream delivers events", () => {
    const { apiRef } = renderHarness();
    expect(apiRef.current?.connectionStatus).toBe("connecting");
    expect(calls).toHaveLength(1);
    expect(calls[0].after).toBe(0);

    act(() => {
      calls[0].handlers.onEvent?.(textDelta(1, "Hello"));
    });

    expect(apiRef.current?.connectionStatus).toBe("online");
  });

  it("reports online once the stream is open, before any event (idle session)", () => {
    const { apiRef } = renderHarness();
    expect(apiRef.current?.connectionStatus).toBe("connecting");
    expect(calls).toHaveLength(1);

    act(() => {
      calls[0].handlers.onOpen?.();
    });

    // 空闲会话是健康的长连接：建连即在线，不再停在「连接中… 重试」。
    expect(apiRef.current?.connectionStatus).toBe("online");
  });

  it("keeps online when a healthy idle stream ends and the loop is about to reconnect", async () => {
    const { apiRef } = renderHarness();

    act(() => {
      calls[0].handlers.onOpen?.();
    });
    expect(apiRef.current?.connectionStatus).toBe("online");

    // 服务端/代理按空闲超时正常收流后进入退避重连窗口：不得按「本轮无事件」
    // 回落 connecting，否则每次重连都会闪回「连接中… 重试」假故障。
    await act(async () => {
      calls[0].finish();
    });

    expect(apiRef.current?.connectionStatus).toBe("online");
  });

  it("degrades to reconnecting then offline as consecutive failures pile up", () => {
    const { apiRef } = renderHarness();

    act(() => {
      calls[0].handlers.onErrorEvent?.({ error: "boom" });
    });
    expect(apiRef.current?.connectionStatus).toBe("reconnecting");

    act(() => {
      calls[0].handlers.onErrorEvent?.({ error: "boom" });
      calls[0].handlers.onErrorEvent?.({ error: "boom" });
    });
    expect(apiRef.current?.connectionStatus).toBe("offline");
  });

  it("manual retry reuses the local last seq and does not re-render consumed deltas", async () => {
    const { apiRef, texts } = renderHarness();

    act(() => {
      calls[0].handlers.onEvent?.(textDelta(7, "Hello"));
    });
    expect(calls[0].handlers.after).toBe(0);

    // 先等首帧增量提交落地，再以它为基准断言「重放不重复渲染」。
    await flushRuntimeCommits();

    const renderedBeforeRetry = texts.length;

    act(() => {
      apiRef.current?.retryConnection();
    });

    // 手动重试复用同一条重连循环：旧连接被中止，且只新增一次请求。
    expect(calls).toHaveLength(2);
    expect(calls[0].aborted).toBe(true);
    // 重试前以本地 last seq 拉齐：新请求的 after 游标即已消费的最大 seq。
    expect(calls[1].after).toBe(7);

    // 幂等：重放同一 delta（同 stream_id + sequence）不会再次渲染。
    act(() => {
      calls[1].handlers.onEvent?.(textDelta(7, "Hello"));
    });
    await flushRuntimeCommits();
    expect(texts.length).toBe(renderedBeforeRetry);
    expect(texts[texts.length - 1]).toBe("Hello");
  });
});
