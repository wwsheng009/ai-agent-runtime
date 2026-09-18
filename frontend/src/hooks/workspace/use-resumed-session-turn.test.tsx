// @vitest-environment jsdom

import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Thread } from "@/data/mock";
import type { RuntimeSessionActiveTurn } from "@/lib/runtime-api";
import { createThread } from "@/lib/thread-state/test-fixtures";

import {
  RESUMED_TURN_HEARTBEAT_MS,
  useResumedSessionTurn,
  type UseResumedSessionTurnResult,
} from "./use-resumed-session-turn";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

/** 刷新后的会话：权威历史里已有本回合的半截答复（无 streaming 标记）。 */
function refreshedThread(): Thread {
  return {
    ...createThread(),
    sessionId: "session-1",
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

type HarnessState = {
  threads: Thread[];
  result: UseResumedSessionTurnResult;
};

function Harness(props: {
  threads: Thread[];
  sessionId?: string;
  activeTurn: RuntimeSessionActiveTurn | null;
  localTurnId?: string | null;
  localResponding?: boolean;
  locallyFinalizedTurnIds?: ReadonlySet<string>;
  refreshRuntimeState?: () => void;
  onState: (state: HarnessState) => void;
}) {
  const [threads, setThreads] = useState<Thread[]>(props.threads);
  const result = useResumedSessionTurn({
    sessionId: props.sessionId,
    activeTurn: props.activeTurn,
    localTurnId: props.localTurnId ?? null,
    localResponding: props.localResponding ?? false,
    locallyFinalizedTurnIds: props.locallyFinalizedTurnIds,
    setThreads,
    refreshRuntimeState: props.refreshRuntimeState,
  });
  props.onState({ threads, result });
  return null;
}

describe("useResumedSessionTurn", () => {
  let container: HTMLDivElement;
  let root: Root;
  let latest: HarnessState | null;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    latest = null;
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
    vi.useRealTimers();
  });

  function render(props: {
    threads: Thread[];
    sessionId?: string;
    activeTurn: RuntimeSessionActiveTurn | null;
    localTurnId?: string | null;
    localResponding?: boolean;
    locallyFinalizedTurnIds?: ReadonlySet<string>;
    refreshRuntimeState?: () => void;
  }) {
    act(() => {
      root.render(
        <Harness
          {...props}
          onState={(state) => {
            latest = state;
          }}
        />,
      );
    });
  }

  function current(): HarnessState {
    if (!latest) {
      throw new Error("hook not rendered");
    }
    return latest;
  }

  function tailMessage(): Thread["messages"][number] {
    const messages = current().threads[0].messages;
    return messages[messages.length - 1];
  }

  it("刷新后把在途回合认领到末尾半截助手消息上", () => {
    render({ threads: [refreshedThread()], sessionId: "session-1", activeTurn: activeTurn() });

    expect(current().result.resumedTurnId).toBe("turn-9");
    expect(current().result.resumedTurnActive).toBe(true);
    expect(tailMessage()).toMatchObject({
      id: "msg-assistant-1",
      streaming: true,
      runtimeTurnId: "turn-9",
    });
    // 正文仍由权威历史承载，认领只补在途标记。
    expect(tailMessage().segments).toEqual([
      { type: "text", content: "前半截答案" },
    ]);
  });

  it("本地直连回合在跑时不抢（续传身份让位本地 POST）", () => {
    render({
      threads: [refreshedThread()],
      sessionId: "session-1",
      activeTurn: activeTurn(),
      localTurnId: "turn-local",
      localResponding: true,
    });

    expect(current().result.resumedTurnId).toBeNull();
    expect(current().result.resumedTurnActive).toBe(false);
    expect(tailMessage().streaming).toBeUndefined();
    expect(tailMessage().runtimeTurnId).toBeUndefined();
  });

  it("快照没有在途回合时不挂载", () => {
    const thread = refreshedThread();
    render({ threads: [thread], sessionId: "session-1", activeTurn: null });

    expect(current().result.resumedTurnId).toBeNull();
    expect(tailMessage().streaming).toBeUndefined();
  });

  it("回合结束后撤销 streaming（保留正文与回合身份）", () => {
    render({ threads: [refreshedThread()], sessionId: "session-1", activeTurn: activeTurn() });
    expect(tailMessage().streaming).toBe(true);

    render({
      threads: [refreshedThread()],
      sessionId: "session-1",
      activeTurn: null,
    });

    expect(current().result.resumedTurnId).toBeNull();
    expect(tailMessage().streaming).toBe(false);
    expect(tailMessage().runtimeTurnId).toBe("turn-9");
    expect(tailMessage().segments).toEqual([
      { type: "text", content: "前半截答案" },
    ]);
  });

  it("回合换成新 id 时撤销旧回合（不留永远转圈的消息）并改用新身份", () => {
    render({ threads: [refreshedThread()], sessionId: "session-1", activeTurn: activeTurn("turn-1") });
    expect(tailMessage()).toMatchObject({
      streaming: true,
      runtimeTurnId: "turn-1",
    });

    render({
      threads: [refreshedThread()],
      sessionId: "session-1",
      activeTurn: activeTurn("turn-2"),
    });

    expect(current().result.resumedTurnId).toBe("turn-2");
    expect(tailMessage()).toMatchObject({
      // 旧回合的半截消息退回「未 streaming」的定稿态：新回合的正文会由
      // 权威历史同步 / 增量帧（按 turn-2 补建占位）接管。
      streaming: false,
      runtimeTurnId: "turn-1",
    });
  });

  it("续传期间按心跳重新拉取快照，收敛后停止", () => {
    vi.useFakeTimers();
    const refresh = vi.fn();
    render({
      threads: [refreshedThread()],
      sessionId: "session-1",
      activeTurn: activeTurn(),
      refreshRuntimeState: refresh,
    });

    act(() => {
      vi.advanceTimersByTime(RESUMED_TURN_HEARTBEAT_MS * 2 + 1);
    });
    expect(refresh).toHaveBeenCalledTimes(2);

    render({
      threads: [refreshedThread()],
      sessionId: "session-1",
      activeTurn: null,
      refreshRuntimeState: refresh,
    });
    act(() => {
      vi.advanceTimersByTime(RESUMED_TURN_HEARTBEAT_MS * 2);
    });
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it("本地已终态的回合不被陈旧快照重新认领，但心跳继续拉到收敛", () => {
    vi.useFakeTimers();
    const refresh = vi.fn();
    render({
      threads: [refreshedThread()],
      sessionId: "session-1",
      activeTurn: activeTurn(),
      locallyFinalizedTurnIds: new Set(["turn-9"]),
      refreshRuntimeState: refresh,
    });

    // 不认领：消息保持权威历史的定稿态，续传身份为空。
    expect(current().result.resumedTurnId).toBeNull();
    expect(current().result.resumedTurnActive).toBe(false);
    expect(tailMessage().streaming).toBeUndefined();
    expect(tailMessage().runtimeTurnId).toBeUndefined();

    // 快照仍报同一回合（服务端 release 尚未可见）：心跳照常拉，直到调用方
    // 在快照收敛后清理抑制集（见 lib/thread-state/locally-finalized-turns.ts）。
    act(() => {
      vi.advanceTimersByTime(RESUMED_TURN_HEARTBEAT_MS * 2 + 1);
    });
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it("认领后收到终态（抑制）立即撤销 streaming 并让出续传身份", () => {
    render({
      threads: [refreshedThread()],
      sessionId: "session-1",
      activeTurn: activeTurn(),
    });
    expect(tailMessage().streaming).toBe(true);

    render({
      threads: [refreshedThread()],
      sessionId: "session-1",
      activeTurn: activeTurn(),
      locallyFinalizedTurnIds: new Set(["turn-9"]),
    });

    expect(current().result.resumedTurnId).toBeNull();
    expect(tailMessage()).toMatchObject({
      streaming: false,
      runtimeTurnId: "turn-9",
    });
  });

  it("会话 id 为空（草稿会话）时不动线程", () => {
    render({ threads: [refreshedThread()], sessionId: "   ", activeTurn: activeTurn() });

    expect(current().result.resumedTurnId).toBeNull();
    expect(tailMessage().streaming).toBeUndefined();
  });
});
