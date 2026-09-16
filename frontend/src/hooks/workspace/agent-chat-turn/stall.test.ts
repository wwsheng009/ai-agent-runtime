import { afterEach, describe, expect, it, vi } from "vitest";

import type { Thread } from "@/data/mock";
import { createConnectTimeoutGuard } from "@/hooks/workspace/agent-chat-turn/connect-timeout";
import { RUNTIME_CONNECT_TIMEOUT_MS } from "@/hooks/workspace/agent-chat-turn/shared";
import { applyChatStreamStall } from "@/hooks/workspace/agent-chat-turn/stall";
import { createTurnRuntimeState } from "@/hooks/workspace/agent-chat-turn/turn-state";

function createThread(): Thread {
  return {
    id: "thread-1",
    title: "Thread",
    summary: "",
    updatedAt: "2026-08-30T00:00:00Z",
    status: "active",
    sessionId: "session-1",
    transport: "live",
    runtimeSource: "runtime",
    lastError: null,
    tags: [],
    prompts: [],
    messages: [],
    artifacts: [],
  };
}

describe("chat stream stall handling", () => {
  it("degrades the thread without finalizing the turn", () => {
    let thread = createThread();
    const notifyFailure = vi.fn();
    const updateStreamingError = vi.fn();

    const message = applyChatStreamStall({
      notifyFailure,
      updateCurrentThread: (updater) => {
        thread = updater(thread);
      },
      updateStreamingError,
    });

    // 文案说真话：本页已停止接收，但服务端回合可能仍在跑。
    expect(message).toContain("连接中断");
    expect(message).toContain("服务端回合可能仍在运行");
    expect(updateStreamingError).toHaveBeenCalledWith(message);
    expect(notifyFailure).toHaveBeenCalledWith(message);
    expect(thread.transport).toBe("error");
    expect(thread.lastError).toBe(message);
    // 不调 finalizeTurn：尾巴消息保持 streaming，续传通道才能续写它。
    expect(thread.messages).toHaveLength(0);
  });
});

describe("connect timeout guard", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("aborts the request and reports when no runtime activity arrives", () => {
    vi.useFakeTimers();
    const controller = new AbortController();
    const turnState = createTurnRuntimeState(createThread());
    const onTimeout = vi.fn();
    const guard = createConnectTimeoutGuard({ controller, onTimeout, turnState });

    guard.start();
    vi.advanceTimersByTime(RUNTIME_CONNECT_TIMEOUT_MS);

    expect(onTimeout).toHaveBeenCalledOnce();
    expect(turnState.connectTimedOut).toBe(true);
    expect(controller.signal.aborted).toBe(true);
  });

  it("stays quiet once runtime activity has arrived", () => {
    vi.useFakeTimers();
    const controller = new AbortController();
    const turnState = createTurnRuntimeState(createThread());
    turnState.receivedRuntimeActivity = true;
    const onTimeout = vi.fn();
    const guard = createConnectTimeoutGuard({ controller, onTimeout, turnState });

    guard.start();
    vi.advanceTimersByTime(RUNTIME_CONNECT_TIMEOUT_MS);

    expect(onTimeout).not.toHaveBeenCalled();
    expect(controller.signal.aborted).toBe(false);
  });
});
