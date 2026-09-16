// 观测 store 单测：写入累加、环形缓冲、闸门上报、快照引用稳定与合并通知。
// 这里断言的是「面板拿到的数据是否可信」，不涉及 React。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  beginLiveChannel,
  getLiveDiagnosticsSnapshot,
  LIVE_DIAGNOSTICS_NOTIFY_MS,
  reportBlockedDelta,
  reportRenderGate,
  reportSnapshotRefresh,
  reportUnownedTurn,
  resetLiveDiagnostics,
  subscribeLiveDiagnostics,
} from "./store";
import { LIVE_FRAME_BUFFER } from "./types";

const SESSION = "session-net-1";

describe("live-diagnostics store", () => {
  beforeEach(() => {
    resetLiveDiagnostics();
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-16T12:00:00.000Z"));
  });

  afterEach(() => {
    vi.useRealTimers();
    resetLiveDiagnostics();
  });

  it("累加传输计数并保留最近帧（新→旧）与游标", () => {
    const sink = beginLiveChannel({ channel: "runtime", sessionId: SESSION });
    sink.open();
    sink.bytes(1024);
    sink.bytes(24);
    sink.event("runtime_event", { type: "delta", seq: 10 });
    vi.setSystemTime(new Date("2026-09-16T12:00:01.000Z"));
    sink.event("runtime_event", { type: "reasoning", seq: 12 });
    sink.keepalive();

    const { runtime } = getLiveDiagnosticsSnapshot(SESSION);
    expect(runtime.active).toBe(true);
    expect(runtime.opens).toBe(1);
    expect(runtime.events).toBe(2);
    expect(runtime.keepalives).toBe(1);
    expect(runtime.bytes).toBe(1048);
    expect(runtime.cursor).toBe(12);
    expect(runtime.lastEventName).toBe("reasoning");
    expect(runtime.frames.map((frame) => frame.kind)).toEqual([
      "keepalive",
      "event",
      "event",
    ]);
    expect(runtime.frames[1].seq).toBe(12);
  });

  it("error 帧单独记账且不覆盖业务事件计数", () => {
    const sink = beginLiveChannel({ channel: "chat", sessionId: SESSION });
    sink.open();
    sink.event("error", { error: "upstream reset" });
    const { chat } = getLiveDiagnosticsSnapshot(SESSION);
    expect(chat.errors).toBe(1);
    expect(chat.lastError).toBe("upstream reset");
    expect(chat.events).toBe(1);
    expect(chat.frames[0].kind).toBe("error");
  });

  it("序号只前进不回退，且 live 帧（无 seq）不改游标", () => {
    const sink = beginLiveChannel({ channel: "runtime", sessionId: SESSION });
    sink.event("runtime_event", { type: "delta", seq: 40 });
    sink.event("runtime_event", { type: "tool.progress" });
    sink.event("runtime_event", { type: "delta", seq: 12 });
    const { runtime } = getLiveDiagnosticsSnapshot(SESSION);
    expect(runtime.cursor).toBe(40);
    expect(runtime.frames[0].seq).toBe(12);
    expect(runtime.frames[1].seq).toBeNull();
  });

  it("最近帧环形缓冲有上限，不会随回合长度无界增长", () => {
    const sink = beginLiveChannel({ channel: "runtime", sessionId: SESSION });
    for (let seq = 1; seq <= LIVE_FRAME_BUFFER + 15; seq += 1) {
      sink.event("runtime_event", { type: "delta", seq });
    }
    const { runtime } = getLiveDiagnosticsSnapshot(SESSION);
    expect(runtime.frames).toHaveLength(LIVE_FRAME_BUFFER);
    expect(runtime.frames[0].seq).toBe(LIVE_FRAME_BUFFER + 15);
    expect(runtime.events).toBe(LIVE_FRAME_BUFFER + 15);
  });

  it("闸门上报只在字段变化时写入（避免每个 render 作废快照）", () => {
    reportRenderGate(SESSION, {
      liveTurnId: "turn-1",
      resumedTurnId: null,
      localResponding: true,
      rendering: true,
    });
    const first = getLiveDiagnosticsSnapshot(SESSION);
    expect(first.gate.rendering).toBe(true);
    expect(first.gate.lastGateChangeAt).not.toBeNull();

    reportRenderGate(SESSION, {
      liveTurnId: "turn-1",
      resumedTurnId: null,
      localResponding: true,
      rendering: true,
    });
    expect(getLiveDiagnosticsSnapshot(SESSION).revision).toBe(first.revision);

    vi.setSystemTime(new Date("2026-09-16T12:00:02.000Z"));
    reportRenderGate(SESSION, {
      liveTurnId: null,
      resumedTurnId: null,
      localResponding: false,
      rendering: false,
    });
    const third = getLiveDiagnosticsSnapshot(SESSION);
    expect(third.revision).toBeGreaterThan(first.revision);
    expect(third.gate.rendering).toBe(false);
    expect(third.gate.lastGateChangeAt).toBe(Date.parse("2026-09-16T12:00:02.000Z"));
  });

  it("被拦增量 / 未认领回合 / 快照刷新各自计数", () => {
    reportBlockedDelta(SESSION);
    reportBlockedDelta(SESSION);
    reportUnownedTurn(SESSION, "turn-9");
    reportSnapshotRefresh(SESSION);

    const { counters } = getLiveDiagnosticsSnapshot(SESSION);
    expect(counters.blockedDeltas).toBe(2);
    expect(counters.unownedTurns).toBe(1);
    expect(counters.snapshotRefreshes).toBe(1);
    expect(counters.lastUnownedTurnId).toBe("turn-9");
    expect(counters.lastBlockedAt).toBe(Date.now());
  });

  it("无线程数据时快照引用稳定，写入后才换新引用", () => {
    const first = getLiveDiagnosticsSnapshot(SESSION);
    expect(getLiveDiagnosticsSnapshot(SESSION)).toBe(first);

    // 只累加字节（热路径不通知、不作废快照）：读数在下次事件落地时一并刷新。
    const sink = beginLiveChannel({ channel: "runtime", sessionId: SESSION });
    sink.bytes(2048);
    expect(getLiveDiagnosticsSnapshot(SESSION)).toBe(first);

    sink.event("runtime_event", { type: "delta", seq: 1 });
    const second = getLiveDiagnosticsSnapshot(SESSION);
    expect(second).not.toBe(first);
    expect(second.runtime.bytes).toBe(2048);
  });

  it("会话之间互不串数据，缺 sessionId 落的兜底键不冒充任何会话", () => {
    const other = beginLiveChannel({ channel: "runtime", sessionId: "session-2" });
    other.event("runtime_event", { type: "delta", seq: 3 });
    const orphan = beginLiveChannel({ channel: "chat" });
    orphan.event("chunk", {});

    expect(getLiveDiagnosticsSnapshot(SESSION).runtime.events).toBe(0);
    expect(getLiveDiagnosticsSnapshot("session-2").runtime.events).toBe(1);
    expect(getLiveDiagnosticsSnapshot("session-2").sessionId).toBe("session-2");
    // 兜底键不会把孤儿数据挂到任意会话上。
    expect(getLiveDiagnosticsSnapshot(SESSION).chat.events).toBe(0);
    expect(getLiveDiagnosticsSnapshot("").sessionId).toBe("");
  });

  it("通知按 250ms 窗口合并：一个窗口内多次写入只通知一次", () => {
    const listener = vi.fn();
    subscribeLiveDiagnostics(listener);
    const sink = beginLiveChannel({ channel: "runtime", sessionId: SESSION });
    sink.open();
    sink.event("runtime_event", { type: "delta", seq: 1 });
    sink.event("runtime_event", { type: "delta", seq: 2 });
    expect(listener).not.toHaveBeenCalled();

    vi.advanceTimersByTime(LIVE_DIAGNOSTICS_NOTIFY_MS);
    expect(listener).toHaveBeenCalledTimes(1);

    sink.event("runtime_event", { type: "delta", seq: 3 });
    vi.advanceTimersByTime(LIVE_DIAGNOSTICS_NOTIFY_MS);
    expect(listener).toHaveBeenCalledTimes(2);
  });
});
