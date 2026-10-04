// @vitest-environment jsdom

// §6.8 托管挂起 hook：挂起事件出现（N=obligation_count）/ 恢复事件清除 /
// agent.turn.finished 不清除 / 会话切换隔离；gap 3b 迟到唤醒通知及其 TTL。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  ParkedTurnSnapshot,
  ParkedTurnSurface,
  ParkedTurnView,
} from "@/lib/parked-turn";
import type { RuntimeAgentRecord, SessionRuntimeEvent } from "@/types/runtime";

import {
  RESUMED_TURN_NOTICE_TTL_MS,
  useParkedTurnView,
  useParkedTurns,
} from "./use-parked-turns";

type HookSnapshot = ReturnType<typeof useParkedTurns>;
type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function runtimeEvent(
  type: string,
  payload: Record<string, unknown>,
  sessionId?: string,
): SessionRuntimeEvent {
  return {
    type,
    timestamp: "2026-09-26T00:00:00Z",
    ...(sessionId ? { session_id: sessionId } : {}),
    payload,
  };
}

function suspended(sessionId: string, obligationCount: number): SessionRuntimeEvent {
  return runtimeEvent(
    "turn.suspended",
    {
      turn_id: "turn-1",
      session_id: sessionId,
      batch_id: "batch-1",
      obligation_count: obligationCount,
      resume_queue_count: 0,
      parked_at: "2026-09-26T00:00:00Z",
    },
    sessionId,
  );
}

function resumed(sessionId: string): SessionRuntimeEvent {
  return runtimeEvent(
    "turn.resumed",
    {
      session_id: sessionId,
      turn_id: "turn-1",
      trigger: "terminal",
      pending_count: 0,
      status: "completed",
      terminal: true,
    },
    sessionId,
  );
}

function Harness({
  sessionId,
  onSnapshot,
}: {
  sessionId?: string;
  onSnapshot: (snapshot: HookSnapshot) => void;
}) {
  const snapshot = useParkedTurns({ sessionId });
  onSnapshot(snapshot);
  return null;
}

describe("useParkedTurns", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderHook(sessionId: string | undefined) {
    let latest: HookSnapshot | null = null;
    act(() => {
      root.render(
        <Harness
          sessionId={sessionId}
          onSnapshot={(snapshot) => {
            latest = snapshot;
          }}
        />,
      );
    });
    return {
      current: () => {
        if (!latest) {
          throw new Error("hook snapshot was not captured");
        }
        return latest;
      },
    };
  }

  it("收到 turn.suspended 后当前会话出现挂起态，N = obligation_count", () => {
    const hook = renderHook("sess-1");

    act(() => {
      hook.current().applyRuntimeEvent(suspended("sess-1", 5));
    });

    expect(hook.current().parkedTurn?.obligationCount).toBe(5);
    expect(hook.current().parkedTurn?.sessionId).toBe("sess-1");
  });

  it("收到同一会话的 turn.resumed 后清除", () => {
    const hook = renderHook("sess-1");

    act(() => {
      hook.current().applyRuntimeEvent(suspended("sess-1", 2));
      hook.current().applyRuntimeEvent(resumed("sess-1"));
    });

    expect(hook.current().parkedTurn).toBeNull();
  });

  it("agent.turn.finished 不清除挂起态", () => {
    const hook = renderHook("sess-1");

    act(() => {
      hook.current().applyRuntimeEvent(suspended("sess-1", 2));
      hook.current().applyRuntimeEvent(
        runtimeEvent("agent.turn.finished", { turn_id: "turn-1" }, "sess-1"),
      );
    });

    expect(hook.current().parkedTurn?.obligationCount).toBe(2);
  });

  it("会话隔离：切到他会话不显示 A 的挂起态，切回仍能看到 A 的挂起态", () => {
    const hook = renderHook("sess-1");

    act(() => {
      hook.current().applyRuntimeEvent(suspended("sess-1", 3));
    });
    expect(hook.current().parkedTurn?.sessionId).toBe("sess-1");

    // 切换到 sess-2：同一份归约状态，按会话 select → 不串台。
    const other = renderHook("sess-2");
    expect(other.current().parkedTurn).toBeNull();

    const back = renderHook("sess-1");
    expect(back.current().parkedTurn?.obligationCount).toBe(3);
  });

  it("迟到唤醒：无 suspended 的 turn.resumed 也产生恢复通知，并在 TTL 后自动消失", () => {
    vi.useFakeTimers();
    try {
      const hook = renderHook("sess-1");

      act(() => {
        hook.current().applyRuntimeEvent(resumed("sess-1"));
      });

      expect(hook.current().parkedTurn).toBeNull();
      expect(hook.current().resumedNotice?.turnId).toBe("turn-1");
      expect(hook.current().resumedNotice?.trigger).toBe("terminal");
      expect(hook.current().resumedNotice?.terminal).toBe(true);

      act(() => {
        vi.advanceTimersByTime(RESUMED_TURN_NOTICE_TTL_MS);
      });

      expect(hook.current().resumedNotice).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("新一轮 turn.suspended 清除同会话旧恢复通知", () => {
    const hook = renderHook("sess-1");

    act(() => {
      hook.current().applyRuntimeEvent(resumed("sess-1"));
      hook.current().applyRuntimeEvent(suspended("sess-1", 2));
    });

    expect(hook.current().resumedNotice).toBeNull();
    expect(hook.current().parkedTurn?.obligationCount).toBe(2);
  });

  it("agent.turn.finished 不清除恢复通知（只表示本次 run 结束）", () => {
    const hook = renderHook("sess-1");

    act(() => {
      hook.current().applyRuntimeEvent(resumed("sess-1"));
      hook.current().applyRuntimeEvent(
        runtimeEvent("agent.turn.finished", { turn_id: "turn-1" }, "sess-1"),
      );
    });

    expect(hook.current().resumedNotice?.turnId).toBe("turn-1");
  });

  it("恢复通知按会话隔离：切到他会话不显示 A 的通知，切回仍能看到", () => {
    const hook = renderHook("sess-1");

    act(() => {
      hook.current().applyRuntimeEvent(resumed("sess-1"));
    });
    expect(hook.current().resumedNotice?.sessionId).toBe("sess-1");

    const other = renderHook("sess-2");
    expect(other.current().resumedNotice).toBeNull();

    const back = renderHook("sess-1");
    expect(back.current().resumedNotice?.turnId).toBe("turn-1");
  });

  it("缺 session_id 的 turn.resumed 不建恢复通知", () => {
    const hook = renderHook("sess-1");

    act(() => {
      hook.current().applyRuntimeEvent(
        runtimeEvent("turn.resumed", {
          turn_id: "turn-1",
          trigger: "terminal",
          terminal: true,
        }),
      );
    });

    expect(hook.current().resumedNotice).toBeNull();
  });
});

describe("useParkedTurnView", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function agent(partial: Partial<RuntimeAgentRecord> & { agentId: string }): RuntimeAgentRecord {
    return {
      rootSessionId: "sess-1",
      parentAgentId: null,
      parentSessionId: null,
      sessionId: "sess-1",
      agentPath: null,
      depth: null,
      agentType: "child",
      workspacePath: null,
      nickname: null,
      workflow: null,
      teamId: null,
      teammateId: null,
      provider: null,
      model: null,
      difficulty: null,
      status: "active",
      runtimeState: "running",
      createdAt: null,
      updatedAt: null,
      closedAt: null,
      routeWarnings: [],
      ...partial,
    };
  }

  function snapshot(turnId = "turn-1"): ParkedTurnSnapshot {
    return {
      sessionId: "sess-1",
      turnId,
      batchId: "batch-1",
      obligationCount: 3,
      resumeQueueCount: 0,
      parkedAt: "2026-09-26T00:00:00Z",
    };
  }

  function surface(
    turn: ParkedTurnSnapshot | null,
    resumedNotice: ParkedTurnSurface["resumedNotice"] = null,
  ): ParkedTurnSurface {
    return { turn, resumedNotice };
  }

  function renderView(props: {
    agents: RuntimeAgentRecord[];
    parkedTurn: ParkedTurnSurface | null;
    refresh: () => void;
    onView: (view: ParkedTurnView | null) => void;
  }) {
    act(() => {
      root.render(
        <ViewHarness
          agents={props.agents}
          parkedTurn={props.parkedTurn}
          refresh={props.refresh}
          onView={props.onView}
        />,
      );
    });
  }

  it("挂起时投影任务计数（运行中 / 完成 / 异常），并在挂起边沿补刷一次目录", () => {
    const refresh = vi.fn();
    const agents = [
      agent({ agentId: "live" }),
      agent({ agentId: "closed", status: "closed" }),
      agent({ agentId: "stale", status: "stale" }),
    ];
    const captured: { view: ParkedTurnView | null } = { view: null };

    renderView({
      agents,
      parkedTurn: surface(snapshot()),
      refresh,
      onView: (next) => {
        captured.view = next;
      },
    });

    expect(captured.view?.taskCounts).toEqual({ running: 1, completed: 1, failed: 1 });
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("同一 turn 的重复快照不重复补刷；未挂起时为 null 且不刷新", () => {
    const refresh = vi.fn();
    const captured: { view: ParkedTurnView | null } = { view: null };
    const onView = (next: ParkedTurnView | null) => {
      captured.view = next;
    };

    renderView({ agents: [], parkedTurn: surface(snapshot()), refresh, onView });
    renderView({ agents: [], parkedTurn: surface(snapshot()), refresh, onView });
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(captured.view?.taskCounts).toEqual({ running: 0, completed: 0, failed: 0 });

    renderView({ agents: [], parkedTurn: null, refresh, onView });
    expect(captured.view).toBeNull();
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("仅迟到唤醒通知时也产出视图：turn/taskCounts 为 null，且不触发目录补刷", () => {
    const refresh = vi.fn();
    const captured: { view: ParkedTurnView | null } = { view: null };

    renderView({
      agents: [],
      parkedTurn: surface(null, {
        sessionId: "sess-1",
        turnId: "turn-1",
        trigger: "terminal",
        terminal: true,
        wakeReasons: [],
        resumedAt: "2026-09-26T00:10:00Z",
      }),
      refresh,
      onView: (next) => {
        captured.view = next;
      },
    });

    expect(captured.view?.turn).toBeNull();
    expect(captured.view?.taskCounts).toBeNull();
    expect(captured.view?.resumedNotice?.trigger).toBe("terminal");
    expect(refresh).not.toHaveBeenCalled();
  });
});

function ViewHarness({
  agents,
  parkedTurn,
  refresh,
  onView,
}: {
  agents: RuntimeAgentRecord[];
  parkedTurn: ParkedTurnSurface | null;
  refresh: () => void;
  onView: (view: ParkedTurnView | null) => void;
}) {
  const view = useParkedTurnView(parkedTurn, agents, refresh);
  onView(view);
  return null;
}
