import { describe, expect, it } from "vitest";

import type { SessionRuntimeEvent } from "@/types/runtime";

import {
  chatSseEventSeq,
  chatSseEventToTrajectoryPush,
  isChatSseEvent,
  isRuntimeTrajectoryEvent,
  nextRecoveryAfter,
  runtimeEventToTrajectoryPush,
  trajectoryEventAction,
  trajectoryRecoveryPushes,
} from "./recovery";

function chatSseEvent(
  kind: string,
  seq: number,
  extra: Record<string, unknown> = {},
): SessionRuntimeEvent {
  return {
    type: `chat.sse.${kind}`,
    timestamp: "2026-08-16T00:00:00Z",
    payload: { ...extra, seq },
  };
}

describe("trajectoryEventAction：可渲染 push / 过滤事件 skip 空洞 / 无 seq ignore", () => {
  it("chat.sse 事件 → push", () => {
    const action = trajectoryEventAction(
      chatSseEvent("chunk", 7, { content: "x" }),
    );
    expect(action.kind).toBe("push");
    if (action.kind === "push") {
      expect(action.push.kind).toBe("chunk");
      expect((action.push.payload._event as { sequence: number }).sequence).toBe(
        7,
      );
    }
  });

  it("白名单 runtime 生命周期事件（approval_requested）→ push", () => {
    const action = trajectoryEventAction({
      type: "approval_requested",
      timestamp: "t",
      payload: { seq: 3, tool_name: "bash" },
    } as SessionRuntimeEvent);
    expect(action.kind).toBe("push");
    if (action.kind === "push") {
      expect(action.push.kind).toBe("runtime");
    }
  });

  it("被过滤但已持久化的事件（tool_started/context.profile.injected）→ skip 空洞", () => {
    const started = trajectoryEventAction({
      type: "tool_started",
      timestamp: "t",
      payload: { seq: 4 },
    } as SessionRuntimeEvent);
    expect(started).toEqual({ kind: "skip", seq: 4 });

    const profile = trajectoryEventAction({
      type: "context.profile.injected",
      timestamp: "t",
      payload: { seq: 9 },
    } as SessionRuntimeEvent);
    expect(profile).toEqual({ kind: "skip", seq: 9 });
  });

  it("无持久化 seq 的暂态事件 → ignore", () => {
    const ephemeral = trajectoryEventAction({
      type: "some_transient",
      timestamp: "t",
      payload: {},
    } as SessionRuntimeEvent);
    expect(ephemeral).toEqual({ kind: "ignore" });
  });
});

describe("isChatSseEvent", () => {
  it("chat.sse.* 前缀命中", () => {
    expect(isChatSseEvent(chatSseEvent("chunk", 2))).toBe(true);
  });

  it("runtime 生命周期事件不命中", () => {
    expect(
      isChatSseEvent({
        type: "runtime.step",
        timestamp: "2026-08-16T00:00:00Z",
        payload: { seq: 1 },
      }),
    ).toBe(false);
  });
});

describe("chatSseEventSeq", () => {
  it("从 payload.seq 读取", () => {
    expect(chatSseEventSeq(chatSseEvent("chunk", 42))).toBe(42);
  });

  it("缺失/非法返回 0", () => {
    expect(chatSseEventSeq(chatSseEvent("chunk", 0))).toBe(0);
    expect(
      chatSseEventSeq({ type: "chat.sse.chunk", timestamp: "t", payload: {} }),
    ).toBe(0);
  });
});

describe("chatSseEventToTrajectoryPush", () => {
  it("转换 kind 并注入 _event.sequence，剥离游标 seq", () => {
    const push = chatSseEventToTrajectoryPush(
      chatSseEvent("tool_start", 7, { name: "web_search" }),
    );
    expect(push).toEqual({
      kind: "tool_start",
      payload: { name: "web_search", _event: { sequence: 7 } },
    });
  });

  it("非轨迹事件返回 null", () => {
    expect(
      chatSseEventToTrajectoryPush({
        type: "runtime.step",
        timestamp: "t",
        payload: { seq: 1 },
      }),
    ).toBeNull();
  });

  it("未知 kind 跳过", () => {
    expect(
      chatSseEventToTrajectoryPush(chatSseEvent("future_kind", 1)),
    ).toBeNull();
  });
});

describe("isRuntimeTrajectoryEvent / runtimeEventToTrajectoryPush（Q4）", () => {
  function runtimeEvent(type: string, seq: number, extra: Record<string, unknown> = {}) {
    return {
      type,
      timestamp: "2026-08-16T00:00:00Z",
      payload: { ...extra, seq },
    } as SessionRuntimeEvent;
  }

  it("白名单内的生命周期事件命中", () => {
    expect(isRuntimeTrajectoryEvent(runtimeEvent("approval_requested", 5))).toBe(true);
    expect(
      isRuntimeTrajectoryEvent(runtimeEvent("session_compact_completed", 6)),
    ).toBe(true);
    expect(isRuntimeTrajectoryEvent(runtimeEvent("checkpoint_created", 7))).toBe(true);
  });

  it("白名单外事件不命中（job_output/team 编排等高频内部事件）", () => {
    expect(isRuntimeTrajectoryEvent(runtimeEvent("job_output", 8))).toBe(false);
    expect(
      isRuntimeTrajectoryEvent(runtimeEvent("team.orchestrator.step", 9)),
    ).toBe(false);
    expect(isRuntimeTrajectoryEvent(runtimeEvent("chat.sse.chunk", 10))).toBe(false);
  });

  it("转换为 runtime push：保留 runtime_type 与字段，剥离游标 seq，注入 _event", () => {
    const push = runtimeEventToTrajectoryPush(
      runtimeEvent("approval_requested", 11, {
        tool_name: "shell",
        request_id: "req-1",
      }),
    );
    expect(push).toEqual({
      kind: "runtime",
      payload: {
        runtime_type: "approval_requested",
        tool_name: "shell",
        request_id: "req-1",
        _event: { sequence: 11 },
      },
    });
  });

  it("白名单外事件转换为 null", () => {
    expect(runtimeEventToTrajectoryPush(runtimeEvent("job_output", 12))).toBeNull();
    expect(
      runtimeEventToTrajectoryPush(runtimeEvent("chat.sse.chunk", 13)),
    ).toBeNull();
  });
});

describe("trajectoryRecoveryPushes 双事件源（Q4）", () => {
  it("chat.sse.* 与 runtime 生命周期事件混合按 seq 升序", () => {
    const pushes = trajectoryRecoveryPushes([
      { type: "approval_requested", timestamp: "t", payload: { seq: 2, tool_name: "shell" } },
      { type: "chat.sse.meta", timestamp: "t", payload: { seq: 1 } },
      { type: "chat.sse.done", timestamp: "t", payload: { seq: 3 } },
      { type: "job_output", timestamp: "t", payload: { seq: 4 } }, // 不映射
    ]);
    expect(pushes.map((push) => push.kind)).toEqual([
      "meta",
      "runtime",
      "done",
    ]);
    expect(pushes[1].kind).toBe("runtime");
    expect(
      (pushes[1].payload._event as { sequence: number }).sequence,
    ).toBe(2);
  });
});

describe("nextRecoveryAfter", () => {
  it("取最后一事件的 seq", () => {
    const events = [chatSseEvent("chunk", 3), chatSseEvent("done", 9)];
    expect(nextRecoveryAfter(events, 0)).toBe(9);
  });

  it("空/无 seq 事件回退到原游标", () => {
    expect(nextRecoveryAfter([], 12)).toBe(12);
    expect(
      nextRecoveryAfter(
        [{ type: "runtime.step", timestamp: "t", payload: {} }],
        5,
      ),
    ).toBe(5);
  });
});

describe("trajectoryRecoveryPushes", () => {
  it("按 seq 升序输出并过滤非轨迹事件", () => {
    const pushes = trajectoryRecoveryPushes([
      chatSseEvent("done", 9),
      chatSseEvent("tool_start", 4, { name: "read_file" }),
      { type: "runtime.step", timestamp: "t", payload: { seq: 1 } },
      chatSseEvent("chunk", 6, { text: "hi" }),
    ]);
    expect(pushes.map((push) => push.kind)).toEqual([
      "tool_start",
      "chunk",
      "done",
    ]);
    expect(pushes[1]?.payload).toEqual({ text: "hi", _event: { sequence: 6 } });
  });

  it("seq=0 事件排末尾（降级事件）", () => {
    const pushes = trajectoryRecoveryPushes([
      chatSseEvent("done", 0),
      chatSseEvent("meta", 1),
    ]);
    expect(pushes.map((push) => push.kind)).toEqual(["meta", "done"]);
  });
});
