// §6.8 托管挂起归约：挂起建条 / 恢复清除 / agent.turn.finished 不清除 / 会话隔离；
// gap 3b 迟到唤醒：恢复通知建条 / 清除 / 会话隔离 / 缺 session_id / 串 turn。

import { describe, expect, it } from "vitest";

import {
  applyParkedTurnEvent,
  clearResumedTurnNotice,
  emptyParkedTurnState,
  parkedTurnSessionId,
  selectParkedTurn,
  selectResumedTurnNotice,
  type ParkedTurnState,
} from "./events";

type ParkedEvent = {
  type: string;
  session_id?: string;
  payload?: Record<string, unknown>;
  timestamp?: string;
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

function resumed(
  sessionId: string,
  options: {
    turnId?: string;
    trigger?: string;
    terminal?: boolean;
    wakeReasons?: string[];
    timestamp?: string;
    omitSessionId?: boolean;
  } = {},
): ParkedEvent {
  const payload: Record<string, unknown> = {
    turn_id: options.turnId ?? "turn-1",
    trigger: options.trigger ?? "terminal",
    pending_count: 0,
    status: "completed",
    terminal: options.terminal ?? true,
    wake_reasons: options.wakeReasons ?? [],
  };
  if (!options.omitSessionId) {
    payload.session_id = sessionId;
  }
  return {
    type: "turn.resumed",
    payload,
    ...(options.omitSessionId ? {} : { session_id: sessionId }),
    ...(options.timestamp ? { timestamp: options.timestamp } : {}),
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

    const cleared = applyParkedTurnEvent(both, resumed("sess-2", { turnId: "turn-2" }));
    expect(selectParkedTurn(cleared, "sess-1")?.turnId).toBe("turn-1");
    expect(selectParkedTurn(cleared, "sess-2")).toBeNull();
  });

  it("迟到的 resume（携另一个 turn_id）不误清当前挂起的 turn", () => {
    const state = fold([suspended("sess-1", { turnId: "turn-new" })]);
    const stale = applyParkedTurnEvent(state, resumed("sess-1", { turnId: "turn-old" }));

    expect(selectParkedTurn(stale, "sess-1")?.turnId).toBe("turn-new");
  });

  it("无会话 id 的 select 恒为 null", () => {
    const state = fold([suspended("sess-1")]);

    expect(selectParkedTurn(state, undefined)).toBeNull();
    expect(selectParkedTurn(state, "  ")).toBeNull();
  });

  it("迟到唤醒（gap 3b）：从未 suspended 的 turn.resumed 也记录恢复通知", () => {
    const state = fold([
      resumed("sess-1", {
        trigger: "terminal",
        terminal: true,
        wakeReasons: [" child_finished ", "team_settled", ""],
        timestamp: "2026-09-26T00:10:00Z",
      }),
    ]);

    expect(selectParkedTurn(state, "sess-1")).toBeNull();
    expect(selectResumedTurnNotice(state, "sess-1")).toEqual({
      sessionId: "sess-1",
      turnId: "turn-1",
      trigger: "terminal",
      terminal: true,
      wakeReasons: ["child_finished", "team_settled"],
      resumedAt: "2026-09-26T00:10:00Z",
    });
  });

  it("匹配的 turn.resumed：清挂起条目的同时记一条恢复通知", () => {
    const state = fold([
      suspended("sess-1"),
      resumed("sess-1", { timestamp: "2026-09-26T01:00:00Z" }),
    ]);

    expect(selectParkedTurn(state, "sess-1")).toBeNull();
    expect(selectResumedTurnNotice(state, "sess-1")?.turnId).toBe("turn-1");
    expect(selectResumedTurnNotice(state, "sess-1")?.resumedAt).toBe(
      "2026-09-26T01:00:00Z",
    );
  });

  it("新一轮 turn.suspended（同会话）清除旧恢复通知", () => {
    const state = fold([resumed("sess-1"), suspended("sess-1", { turnId: "turn-2" })]);

    expect(selectResumedTurnNotice(state, "sess-1")).toBeNull();
    expect(selectParkedTurn(state, "sess-1")?.turnId).toBe("turn-2");
  });

  it("恢复通知按会话隔离：B 的新一轮挂起只清 B 的通知", () => {
    const state = fold([resumed("sess-1"), resumed("sess-2", { turnId: "turn-2" })]);

    expect(selectResumedTurnNotice(state, "sess-1")?.turnId).toBe("turn-1");
    expect(selectResumedTurnNotice(state, "sess-2")?.turnId).toBe("turn-2");

    const clearedB = applyParkedTurnEvent(state, suspended("sess-2", { turnId: "turn-3" }));
    expect(selectResumedTurnNotice(clearedB, "sess-2")).toBeNull();
    expect(selectResumedTurnNotice(clearedB, "sess-1")?.turnId).toBe("turn-1");
  });

  it("缺 session_id 的 turn.resumed 不建恢复通知（无法归属绝不挂到别家会话）", () => {
    const state = applyParkedTurnEvent(
      emptyParkedTurnState(),
      resumed("sess-1", { omitSessionId: true }),
    );

    expect(selectResumedTurnNotice(state, "sess-1")).toBeNull();
    expect(state.lastResumeBySession).toEqual({});
  });

  it("串 turn 的迟到 resume 既不清挂起条目，也不写恢复通知", () => {
    const state = fold([suspended("sess-1", { turnId: "turn-new" })]);
    const stale = applyParkedTurnEvent(state, resumed("sess-1", { turnId: "turn-old" }));

    expect(stale).toBe(state);
    expect(selectParkedTurn(stale, "sess-1")?.turnId).toBe("turn-new");
    expect(selectResumedTurnNotice(stale, "sess-1")).toBeNull();
  });

  it("agent.turn.finished 不参与恢复通知的清除（只表示本次 run 结束）", () => {
    const state = fold([
      resumed("sess-1"),
      { type: "agent.turn.finished", session_id: "sess-1", payload: { turn_id: "turn-1" } },
    ]);

    expect(selectResumedTurnNotice(state, "sess-1")?.turnId).toBe("turn-1");
  });

  it("同一恢复通知的重复投递（重连重放）保持原引用，不产生无意义重渲染", () => {
    const first = fold([resumed("sess-1")]);
    const second = applyParkedTurnEvent(first, resumed("sess-1"));

    expect(second).toBe(first);
  });

  it("缺会话 id 的通知 select 恒为 null", () => {
    const state = fold([resumed("sess-1")]);

    expect(selectResumedTurnNotice(state, undefined)).toBeNull();
    expect(selectResumedTurnNotice(state, "  ")).toBeNull();
  });
});

describe("clearResumedTurnNotice", () => {
  it("按引用清除仍存活的当前通知", () => {
    const state = fold([resumed("sess-1")]);
    const notice = selectResumedTurnNotice(state, "sess-1");
    if (!notice) {
      throw new Error("resumed notice was not recorded");
    }

    const cleared = clearResumedTurnNotice(state, notice);

    expect(selectResumedTurnNotice(cleared, "sess-1")).toBeNull();
  });

  it("迟到的定时器不误删新通知：引用不匹配时原样返回", () => {
    const first = fold([resumed("sess-1", { timestamp: "2026-09-26T00:00:00Z" })]);
    const staleNotice = selectResumedTurnNotice(first, "sess-1");
    if (!staleNotice) {
      throw new Error("resumed notice was not recorded");
    }
    const second = applyParkedTurnEvent(
      first,
      resumed("sess-1", { timestamp: "2026-09-26T00:05:00Z" }),
    );

    const cleared = clearResumedTurnNotice(second, staleNotice);

    expect(cleared).toBe(second);
    expect(selectResumedTurnNotice(cleared, "sess-1")?.resumedAt).toBe(
      "2026-09-26T00:05:00Z",
    );
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
