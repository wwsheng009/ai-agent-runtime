/**
 * Batch 2（多会话并发运行时）：单会话订阅状态机回归。
 *
 * 覆盖方案要求的不变量：
 * - IN1/IN2：建连游标 `after = max(lastSeq, 轨迹窗口 seq)`，重连不重复消费；
 * - seq 单调：迟到/乱序事件不把游标回退（"切回无空洞、无重复"的判定基础）；
 * - 轻量投影：待交互计数 / 子代理计数 / 在途回合（不写 threads 的 D1-B 前提）；
 * - poll 退避：3s 起、1.5× 升至上限，成功后回落；
 * - 生命周期：idle/dispose 停止循环，重试复用同一游标。
 */

import { afterEach, describe, expect, it, vi } from "vitest";

import { createSessionRuntimeEntry } from "@/lib/session-runtime/entry";
import type { SessionRuntimeEntryConfig } from "@/lib/session-runtime/entry";
import type { SessionRuntimeEvent } from "@/types/runtime";

type FakeHandlers = {
  after?: number;
  live?: boolean;
  signal?: AbortSignal;
  onOpen?: () => void;
  onEvent?: (event: SessionRuntimeEvent) => void;
  onErrorEvent?: (payload: Record<string, unknown>) => void;
};

function event(patch: Partial<SessionRuntimeEvent> & { type: string }): SessionRuntimeEvent {
  return { timestamp: new Date().toISOString(), ...patch };
}

/** 常驻连接：调用方通过 onCall 拿到 handlers（用于注入事件），abort 时结束。 */
function hangingStream(onCall: (handlers: FakeHandlers) => void) {
  return (async (_sessionId: string, handlers: FakeHandlers) => {
    onCall(handlers);
    handlers.onOpen?.();
    await new Promise<void>((resolve) => {
      if (!handlers.signal || handlers.signal.aborted) {
        resolve();
        return;
      }
      handlers.signal.addEventListener("abort", () => resolve(), { once: true });
    });
  }) as unknown as SessionRuntimeEntryConfig["streamRuntime"];
}

async function flush() {
  await Promise.resolve();
  await Promise.resolve();
}

afterEach(() => {
  vi.useRealTimers();
});

describe("createSessionRuntimeEntry（live）", () => {
  it("建连游标取 max(已消费 seq, 轨迹窗口 seq)，重连复用最新 seq", async () => {
    const calls: number[] = [];
    // 类型断言形式：`let emit: T | null = null` 会被 TS 收窄成 null，回调里的
    // 赋值不会被纳入流分析，`emit?.()` 因此报 "not callable"。
    let emit = null as ((event: SessionRuntimeEvent) => void) | null;
    const entry = createSessionRuntimeEntry({
      sessionId: "session-a",
      getReplayCursor: () => 120,
      streamRuntime: hangingStream((handlers) => {
        calls.push(handlers.after ?? -1);
        emit = handlers.onEvent ?? null;
      }),
    });

    entry.setMode("live");
    await vi.waitFor(() => expect(calls.length).toBe(1));
    expect(calls[0]).toBe(120);
    expect(entry.snapshot().mode).toBe("live");

    emit?.(event({ type: "assistant_delta", payload: { seq: 130 } }));
    expect(entry.snapshot().lastSeq).toBe(130);

    entry.retry();
    await vi.waitFor(() => expect(calls.length).toBe(2));
    // 轨迹窗口 120 < 本地已消费 130：重连只拉新事件。
    expect(calls[1]).toBe(130);
  });

  it("seq 只增不减：迟到事件不回退游标", async () => {
    // 类型断言形式：`let emit: T | null = null` 会被 TS 收窄成 null，回调里的
    // 赋值不会被纳入流分析，`emit?.()` 因此报 "not callable"。
    let emit = null as ((event: SessionRuntimeEvent) => void) | null;
    const entry = createSessionRuntimeEntry({
      sessionId: "session-a",
      streamRuntime: hangingStream((handlers) => {
        emit = handlers.onEvent ?? null;
      }),
    });
    entry.setMode("live");
    await vi.waitFor(() => expect(emit).not.toBeNull());

    emit?.(event({ type: "assistant_delta", payload: { seq: 42 } }));
    emit?.(event({ type: "assistant_delta", payload: { seq: 41 } }));
    expect(entry.snapshot().lastSeq).toBe(42);
  });

  it("投影待交互计数 / 子代理计数 / 在途回合（终态清空）", async () => {
    // 类型断言形式：`let emit: T | null = null` 会被 TS 收窄成 null，回调里的
    // 赋值不会被纳入流分析，`emit?.()` 因此报 "not callable"。
    let emit = null as ((event: SessionRuntimeEvent) => void) | null;
    const entry = createSessionRuntimeEntry({
      sessionId: "session-a",
      streamRuntime: hangingStream((handlers) => {
        emit = handlers.onEvent ?? null;
      }),
    });
    entry.setMode("live");
    await vi.waitFor(() => expect(emit).not.toBeNull());

    emit?.(
      event({
        type: "approval_requested",
        payload: { request_id: "req-1", tool_name: "shell" },
      }),
    );
    emit?.(event({ type: "question_asked", payload: { question_id: "q-1" } }));
    expect(entry.snapshot().pending).toEqual({
      approvals: 1,
      questions: 1,
      planPending: false,
    });

    emit?.(event({ type: "subagent.started", payload: { agent_id: "agent-1" } }));
    emit?.(event({ type: "subagent.started", payload: { agent_id: "agent-2" } }));
    expect(entry.snapshot().runningAgents).toBe(2);
    emit?.(event({ type: "subagent.completed", payload: { agent_id: "agent-1" } }));
    expect(entry.snapshot().runningAgents).toBe(1);

    emit?.(event({ type: "tool_call", payload: { seq: 7, turn_id: "turn-1" } }));
    expect(entry.snapshot().activeTurn?.turnId).toBe("turn-1");
    expect(entry.snapshot().detached).toBe(false);

    emit?.(
      event({
        type: "approval_resolved",
        payload: { request_id: "req-1", resolution: "allow" },
      }),
    );
    emit?.(event({ type: "question_answered", payload: { question_id: "q-1" } }));
    expect(entry.snapshot().pending).toEqual({
      approvals: 0,
      questions: 0,
      planPending: false,
    });

    emit?.(event({ type: "session_end", payload: { seq: 9 } }));
    expect(entry.snapshot().activeTurn).toBeNull();
  });

  it("错误帧计数到阈值才 offline，收到事件即恢复 online", async () => {
    let handlersRef: FakeHandlers | null = null;
    const entry = createSessionRuntimeEntry({
      sessionId: "session-a",
      streamRuntime: hangingStream((handlers) => {
        handlersRef = handlers;
      }),
    });
    entry.setMode("live");
    await vi.waitFor(() => expect(handlersRef).not.toBeNull());

    const handlers = handlersRef as unknown as FakeHandlers;
    handlers.onErrorEvent?.({ error: "boom-1" });
    expect(entry.snapshot().status).toBe("reconnecting");
    expect(entry.snapshot().lastError).toBe("boom-1");

    handlers.onEvent?.(event({ type: "assistant_delta", payload: { seq: 3 } }));
    expect(entry.snapshot().status).toBe("online");
    expect(entry.snapshot().lastError).toBeNull();
  });

  it("idle / dispose 停止循环且不再重连", async () => {
    const calls: number[] = [];
    const entry = createSessionRuntimeEntry({
      sessionId: "session-a",
      streamRuntime: hangingStream((handlers) => {
        calls.push(handlers.after ?? 0);
      }),
    });
    entry.setMode("live");
    await vi.waitFor(() => expect(calls.length).toBe(1));

    entry.setMode("idle");
    await flush();
    expect(entry.snapshot().mode).toBe("idle");
    expect(entry.snapshot().status).toBe("idle");

    entry.setMode("live");
    await vi.waitFor(() => expect(calls.length).toBe(2));
    entry.dispose();
    await flush();
    expect(calls.length).toBe(2);
  });
});

describe("createSessionRuntimeEntry（poll）", () => {
  it("失败按 1.5× 退避到上限，成功后回落且状态 online", async () => {
    vi.useFakeTimers();
    let failing = true;
    const fetchSnapshot = vi.fn(async () => {
      if (failing) {
        throw new Error("store unavailable");
      }
      return null;
    });
    const entry = createSessionRuntimeEntry({
      sessionId: "session-b",
      fetchSnapshot:
        fetchSnapshot as unknown as SessionRuntimeEntryConfig["fetchSnapshot"],
      pollInitialMs: 100,
      pollMaxMs: 250,
      pollBackoffFactor: 1.5,
    });

    entry.setMode("poll");
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchSnapshot).toHaveBeenCalledTimes(1);
    expect(entry.snapshot().status).toBe("reconnecting");
    expect(entry.snapshot().lastError).toBe("store unavailable");

    // 失败即先退避：首次重试间隔 = 100 × 1.5 = 150，再 225，随后封顶 250。
    await vi.advanceTimersByTimeAsync(150);
    expect(fetchSnapshot).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(225);
    expect(fetchSnapshot).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(250);
    expect(fetchSnapshot).toHaveBeenCalledTimes(4);
    await vi.advanceTimersByTimeAsync(250);
    expect(fetchSnapshot).toHaveBeenCalledTimes(5);

    failing = false;
    await vi.advanceTimersByTimeAsync(250);
    expect(fetchSnapshot).toHaveBeenCalledTimes(6);
    expect(entry.snapshot().status).toBe("online");
    expect(entry.snapshot().lastError).toBeNull();

    // 成功后回到起点周期（100ms）。
    await vi.advanceTimersByTimeAsync(100);
    expect(fetchSnapshot).toHaveBeenCalledTimes(7);
  });

  it("快照归约 active_turn 与待交互，但不发事件出口", async () => {
    vi.useFakeTimers();
    const onEvent = vi.fn();
    const fetchSnapshot = vi.fn(async () => ({
      state: {
        sessionId: "session-b",
        status: "waiting_approval",
        pendingApproval: {
          id: "req-9",
          sessionId: "session-b",
          toolName: "shell",
          reason: "",
          riskLevel: "low",
        },
        pendingQuestion: null,
        headOffset: 5,
        activeJobIds: [],
      },
      activeTurn: {
        sessionId: "session-b",
        turnId: "turn-9",
        source: "agent_chat_stream",
        detached: true,
      },
    }));
    const entry = createSessionRuntimeEntry({
      sessionId: "session-b",
      fetchSnapshot:
        fetchSnapshot as unknown as SessionRuntimeEntryConfig["fetchSnapshot"],
      onEvent,
      pollInitialMs: 50,
    });

    entry.setMode("poll");
    await vi.advanceTimersByTimeAsync(0);
    const snapshot = entry.snapshot();
    expect(snapshot.status).toBe("online");
    expect(snapshot.activeTurn?.turnId).toBe("turn-9");
    expect(snapshot.detached).toBe(true);
    expect(snapshot.pending.approvals).toBe(1);
    expect(onEvent).not.toHaveBeenCalled();
  });
});
