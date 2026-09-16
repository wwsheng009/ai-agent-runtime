// @vitest-environment jsdom

// live 通道建连契约（tail-first）回归：建连闸门 + 建连游标。
//
// 两条契约（2026-09-16 锁定现状，改动前必须有意识地更新本用例）：
// 1. 建连闸门：轨迹首屏窗口就绪前不建连——否则 `/runtime/stream?after=0` 会把
//    整份事件日志按 SSE dump 重放一遍，与窗口回放重复解析同一份日志；
// 2. 建连游标：`after = max(本 hook 已消费 seq, 轨迹已回放 seq)`——首连跳过已
//    回放的窗口，重连沿用本地已消费游标、不重复消费增量。
//
// 与 `use-session-runtime-stream.test.tsx`（delta 闸门）分开，避免单文件超
// 500 非空行门禁（P0-2）。

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
  getRuntimeEventSeq,
  mergeRuntimeEvent,
} from "@/lib/workspace-thread-state";

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return {
    ...actual,
    streamSessionRuntime: vi.fn(),
  };
});

import { streamSessionRuntime } from "@/lib/runtime-api";

const mockStream = vi.mocked(streamSessionRuntime);

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

type StreamHookApi = ReturnType<typeof useSessionRuntimeStream>;

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

/** 最小接线：把 hook 的建连参数（enabled / getReplayCursor）暴露给用例。 */
function Harness({
  enabled,
  getReplayCursor,
  onHookResult,
}: {
  enabled?: boolean;
  getReplayCursor?: () => number;
  onHookResult?: (api: StreamHookApi) => void;
}) {
  const [thread, setThread] = useState(createThread);
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
        return Array.isArray(result) ? result[0] : result;
      });
    },
    [],
  );
  const api = useSessionRuntimeStream({
    applyRuntimeEventToThread,
    applyRuntimeDeltaToThread,
    getErrorMessage,
    getRuntimeEventSeq,
    mergeRuntimeEvent,
    enabled,
    getReplayCursor,
    renderLiveDeltas: true,
    selectedThread: thread,
    setThreads,
  });
  onHookResult?.(api);
  return null;
}

describe("useSessionRuntimeStream 建连闸门与建连游标（tail-first）", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    mockStream.mockReset();
    mockStream.mockImplementation(
      async (_sessionId, handlers) =>
        new Promise<void>((resolve) => {
          handlers.signal?.addEventListener("abort", () => resolve());
        }),
    );
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("轨迹窗口未就绪不建连；就绪后以轨迹已回放 seq 作为首连 after", async () => {
    act(() => {
      root.render(<Harness enabled={false} getReplayCursor={() => 120} />);
    });
    await act(async () => {
      await new Promise<void>((resolve) => setTimeout(resolve, 20));
    });
    // 闸门关闭：窗口回放完成前不建连（否则整份日志会被 dump 重放）。
    expect(mockStream).not.toHaveBeenCalled();

    act(() => {
      root.render(<Harness enabled getReplayCursor={() => 120} />);
    });
    await vi.waitFor(() => {
      expect(mockStream).toHaveBeenCalledTimes(1);
    });

    // 首连游标 = max(本地已消费 0, 轨迹已回放 120) = 120：只订阅窗口之后的新事件。
    expect(mockStream.mock.calls[0][0]).toBe("session-1");
    expect(mockStream.mock.calls[0][1].after).toBe(120);
  });

  it("本地已消费 seq 领先时，手动重连沿用本地游标（max 取本地）", async () => {
    const apiRef: { current?: StreamHookApi } = {};
    act(() => {
      root.render(
        <Harness
          enabled
          getReplayCursor={() => 120}
          onHookResult={(api) => {
            apiRef.current = api;
          }}
        />,
      );
    });
    await vi.waitFor(() => {
      expect(mockStream).toHaveBeenCalledTimes(1);
    });
    expect(mockStream.mock.calls[0][1].after).toBe(120);

    // 消费一条已落库事件（seq=130 > 轨迹回放游标）：本地游标前进到 130。
    const handlers = mockStream.mock.calls[0][1];
    act(() => {
      handlers.onEvent?.({
        type: "session_start",
        timestamp: "2026-08-30T00:00:04Z",
        payload: { status: "running", seq: 130 },
      });
    });
    await act(async () => {
      await new Promise<void>((resolve) => setTimeout(resolve, 180));
    });

    act(() => {
      apiRef.current?.retryConnection();
    });
    await vi.waitFor(() => {
      expect(mockStream).toHaveBeenCalledTimes(2);
    });

    // 重连游标 = max(本地已消费 130, 轨迹已回放 120) = 130：不重复消费已渲染的增量。
    expect(mockStream.mock.calls[1][1].after).toBe(130);
  });
});
