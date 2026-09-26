// §6.8 托管挂起归约：挂起建条 / 恢复清除 / agent.turn.finished 不清除 / 会话隔离。

import { describe, expect, it } from "vitest";

import {
  applyParkedTurnEvent,
  emptyParkedTurnState,
  parkedTurnSessionId,
  selectParkedTurn,
  type ParkedTurnState,
} from "./events";

type ParkedEvent = {
  type: string;
  session_id?: string;
  payload?: Record<string, unknown>;
};

function suspended(
  sessionId: string,
  options: { turnId?: string; obligationCount?: number; resumeQueueCount?: number } = {},
): ParkedEvent {
  return {
    type: "turn.suspended",
    session_id: sessionId,
    payload: {
      turn_id: options.turnId ?? "turn-1",
      session_id: sessionId,
      batch_id: "batch-1",
      obligation_count: options.obligationCount ?? 3,
      resume_queue_count: options.resumeQueueCount ?? 1,
      parked_at: "2026-09-26T00:00:00Z",
    },
  };
}

function resumed(sessionId: string, turnId = "turn-1"): ParkedEvent {
  return {
    type: "turn.resumed",
    session_id: sessionId,
    payload: {
      session_id: sessionId,
      turn_id: turnId,
      trigger: "terminal",
      pending_count: 0,
      status: "completed",
      terminal: true,
    },
  };
}

function fold(events: readonly ParkedEvent[]): ParkedTurnState {
  return events.reduce<ParkedTurnState>(
    (state, event) => applyParkedTurnEvent(state, event),
    emptyParkedTurnState(),
  );
}

describe("applyParkedTurnEvent", () => {
  it("turn.suspended 建条：义务数 / turn 身份 / 停靠时间如实投影", () => {
    const state = fold([suspended("sess-1", { obligationCount: 4 })]);

    expect(selectParkedTurn(state, "sess-1")).toEqual({
      sessionId: "sess-1",
      turnId: "turn-1",
      batchId: "batch-1",
      obligationCount: 4,
      resumeQueueCount: 1,
      parkedAt: "2026-09-26T00:00:00Z",
    });
  });

  it("turn.suspended 缺 session_id 不建条目（不把无法归属的挂起态挂到别家会话）", () => {
    const state = applyParkedTurnEvent(emptyParkedTurnState(), {
      type: "turn.suspended",
      payload: { turn_id: "turn-1", obligation_count: 2 },
    });

    expect(state.bySession).toEqual({});
  });

  it("同一 turn 的重复投递（重连重放）保持原引用，不产生无意义重渲染", () => {
    const first = fold([suspended("sess-1")]);
    const second = applyParkedTurnEvent(first, suspended("sess-1"));

    expect(second).toBe(first);
  });

  it("turn.resumed 清除同会话挂起态", () => {
    const state = fold([suspended("sess-1"), resumed("sess-1")]);

    expect(selectParkedTurn(state, "sess-1")).toBeNull();
  });

  it("agent.turn.finished 不清除挂起态（本次 run 结束 ≠ 义务终态）", () => {
    const state = fold([
      suspended("sess-1"),
      { type: "agent.turn.finished", session_id: "sess-1", payload: { turn_id: "turn-1" } },
    ]);

    expect(selectParkedTurn(state, "sess-1")?.obligationCount).toBe(3);
  });

  it("会话隔离：A 的挂起态不出现在 B，B 的恢复也不清除 A", () => {
    const state = fold([suspended("sess-1"), resumed("sess-2")]);

    expect(selectParkedTurn(state, "sess-2")).toBeNull();
    expect(selectParkedTurn(state, "sess-1")?.sessionId).toBe("sess-1");

    const both = fold([suspended("sess-1"), suspended("sess-2", { turnId: "turn-2" })]);
    expect(selectParkedTurn(both, "sess-1")?.turnId).toBe("turn-1");
    expect(selectParkedTurn(both, "sess-2")?.turnId).toBe("turn-2");

    const cleared = applyParkedTurnEvent(both, resumed("sess-2", "turn-2"));
    expect(selectParkedTurn(cleared, "sess-1")?.turnId).toBe("turn-1");
    expect(selectParkedTurn(cleared, "sess-2")).toBeNull();
  });

  it("迟到的 resume（携另一个 turn_id）不误清当前挂起的 turn", () => {
    const state = fold([suspended("sess-1", { turnId: "turn-new" })]);
    const stale = applyParkedTurnEvent(state, resumed("sess-1", "turn-old"));

    expect(selectParkedTurn(stale, "sess-1")?.turnId).toBe("turn-new");
  });

  it("无会话 id 的 select 恒为 null", () => {
    const state = fold([suspended("sess-1")]);

    expect(selectParkedTurn(state, undefined)).toBeNull();
    expect(selectParkedTurn(state, "  ")).toBeNull();
  });
});

describe("parkedTurnSessionId", () => {
  it("载荷优先，回落事件信封", () => {
    expect(
      parkedTurnSessionId({ session_id: "envelope", payload: { session_id: "payload" } }),
    ).toBe("payload");
    expect(parkedTurnSessionId({ session_id: "envelope" })).toBe("envelope");
    expect(parkedTurnSessionId({ payload: {} })).toBe("");
  });
});
