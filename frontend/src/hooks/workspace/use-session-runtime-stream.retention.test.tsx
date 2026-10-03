// @vitest-environment jsdom

// 运行时通道终态收尾的内存回归：终态帧提交兑现后必须清理 live 文本记录。
// 不清理时整段正文 / 推理会永久驻留在模块级 Map，并持续覆盖 store 副本。

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
  getLiveStreamEntry,
  resetLiveStreamTextStore,
} from "@/lib/live-stream-text";
import {
  applyRuntimeDeltaToThread,
  applyRuntimeEventToThread,
  getRuntimeEventSeq,
  mergeRuntimeEvent,
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

function createStreamingThread(): Thread {
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
        streaming: true,
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
  initialThread,
  onThreadsChange,
}: {
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
    activeTurnId: "turn-1",
    renderLiveDeltas: true,
    selectedThread: thread,
    setThreads,
  });
  return null;
}

describe("useSessionRuntimeStream terminal retention", () => {
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
    resetLiveStreamTextStore();
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
    resetLiveStreamTextStore();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("clears live stream text after a runtime terminal frame is committed", async () => {
    let threads: Thread[] = [];
    act(() => {
      root.render(
        <Harness
          initialThread={createStreamingThread()}
          onThreadsChange={(next) => {
            threads = next;
          }}
        />,
      );
    });
    await vi.waitFor(() => {
      expect(mockStream).toHaveBeenCalledTimes(1);
    });
    const handlers = mockStream.mock.calls[0][1];

    act(() => {
      handlers.onEvent?.(
        deltaEvent({ delta: "Hello", turn_id: "turn-1", sequence: 3 }),
      );
    });
    expect(getLiveStreamEntry("assistant-1")).not.toBeNull();

    act(() => {
      handlers.onEvent?.({
        type: "turn.finished",
        timestamp: "2026-08-30T00:00:10Z",
        payload: { turn_id: "turn-1", seq: 10 },
      });
    });
    // 合帧提交：终态帧的提交与 live 清理在同一任务内先后兑现。
    await act(async () => {
      await new Promise<void>((resolve) => setTimeout(resolve, 180));
    });

    expect(getLiveStreamEntry("assistant-1")).toBeNull();
    // turn.finished 只负责收尾 live 文本；streaming 标记的收敛由 chat.sse.* 终态
    // 路径（finalizeRuntimeTurnInThread）承担，不在本用例的断言范围。
    expect(threads[0]?.messages[0]?.id).toBe("assistant-1");
  });
});
