import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  RuntimeApiError,
  getSessionLeaseConflictTitle,
} from "@/api/runtime/shared";
import type { Thread } from "@/data/mock";
import { resetLiveStreamTextStore } from "@/lib/live-stream-text";
import {
  createRuntimeDeltaCoordinator,
  type RuntimeDeltaCoordinator,
} from "@/lib/workspace-thread-state";
import type { AgentChatStreamChunkPayload } from "@/types/runtime";

import { createAgentChatStreamHandlers } from "./agent-chat-turn/stream-handlers";
import { createTurnRuntimeState } from "./agent-chat-turn/turn-state";
import {
  admitTransportFrame,
  getFrameIntakeDroppedTotal,
  readChatFrameSeq,
  resetFrameIntake,
} from "./frame-intake";

function chatFrame(value: Record<string, unknown>) {
  return value as unknown as AgentChatStreamChunkPayload;
}

function createHarness(coordinator?: RuntimeDeltaCoordinator) {
  const turnState = createTurnRuntimeState({} as Thread);
  const pushTrajectory = vi.fn();
  const updateStreamingError = vi.fn();
  const claimSpy = coordinator ? vi.spyOn(coordinator, "claim") : null;
  const handlers = createAgentChatStreamHandlers({
    assistantMessageId: "msg-1",
    attachTurnArtifact: vi.fn(),
    controller: new AbortController(),
    deltaCoordinator: coordinator,
    finalizeTurn: vi.fn(),
    frameScheduler: { flush: vi.fn(), schedule: vi.fn() },
    handleToolEnd: vi.fn(),
    notifyFailure: vi.fn(),
    pushTrajectory,
    sessionId: "session-1",
    setPhaseAndRef: vi.fn(),
    turnId: "turn-1",
    turnState,
    updateCurrentThread: vi.fn(),
    updateStreamingError,
    upsertLiveToolSegment: vi.fn(),
  });
  return { claimSpy, handlers, pushTrajectory, turnState, updateStreamingError };
}

beforeEach(() => {
  resetFrameIntake();
});

afterEach(() => {
  resetFrameIntake();
  resetLiveStreamTextStore();
});

describe("frame-intake：per-session 帧闸门（单写者 + seq 单调）", () => {
  it("同类别 seq 单调放行：相等与回退被拒并计数", () => {
    expect(
      admitTransportFrame({ sessionId: "s1", kind: "text", seq: 7 }),
    ).toBe(true);
    expect(
      admitTransportFrame({ sessionId: "s1", kind: "text", seq: 7 }),
    ).toBe(false);
    expect(
      admitTransportFrame({ sessionId: "s1", kind: "text", seq: 6 }),
    ).toBe(false);
    expect(getFrameIntakeDroppedTotal()).toBe(2);
    expect(
      admitTransportFrame({ sessionId: "s1", kind: "text", seq: 8 }),
    ).toBe(true);
  });

  it("水位按帧类别隔离：text 的水位不吞 reasoning/image 帧", () => {
    expect(
      admitTransportFrame({ sessionId: "s1", kind: "text", seq: 10 }),
    ).toBe(true);
    expect(
      admitTransportFrame({ sessionId: "s1", kind: "reasoning", seq: 5 }),
    ).toBe(true);
    expect(
      admitTransportFrame({ sessionId: "s1", kind: "image", seq: 9 }),
    ).toBe(true);
  });

  it("水位按会话隔离", () => {
    expect(
      admitTransportFrame({ sessionId: "s1", kind: "text", seq: 10 }),
    ).toBe(true);
    expect(
      admitTransportFrame({ sessionId: "s2", kind: "text", seq: 3 }),
    ).toBe(true);
  });

  it("无持久化 seq / 无身份的帧一律放行（缺身份不等于重复）", () => {
    expect(
      admitTransportFrame({ sessionId: "s1", kind: "text", seq: 0 }),
    ).toBe(true);
    expect(
      admitTransportFrame({ sessionId: "s1", kind: "text", seq: -3 }),
    ).toBe(true);
    expect(
      admitTransportFrame({ sessionId: "s1", kind: "text", seq: Number.NaN }),
    ).toBe(true);
    expect(
      admitTransportFrame({ turnId: "", kind: "text", seq: 7 }),
    ).toBe(true);
    expect(getFrameIntakeDroppedTotal()).toBe(0);
  });

  it("缺 session 时退化为 turn 身份", () => {
    expect(admitTransportFrame({ turnId: "t1", kind: "text", seq: 7 })).toBe(
      true,
    );
    expect(admitTransportFrame({ turnId: "t1", kind: "text", seq: 7 })).toBe(
      false,
    );
    expect(admitTransportFrame({ turnId: "t2", kind: "text", seq: 7 })).toBe(
      true,
    );
  });

  it("水位按单写者作用域隔离：新页面/新协调器从零开始，旧页面水位不带入", () => {
    const pageA = {};
    const pageB = {};
    expect(
      admitTransportFrame({ scope: pageA, sessionId: "s1", kind: "text", seq: 7 }),
    ).toBe(true);
    // 新页面（新 coordinator 实例）不继承上一页面的水位：同 seq 仍放行一次。
    expect(
      admitTransportFrame({ scope: pageB, sessionId: "s1", kind: "text", seq: 7 }),
    ).toBe(true);
    // 各自作用域内仍严格单调。
    expect(
      admitTransportFrame({ scope: pageA, sessionId: "s1", kind: "text", seq: 7 }),
    ).toBe(false);
  });

  it("readChatFrameSeq 只认 _event.sequence（number / 字符串数字）", () => {
    expect(readChatFrameSeq({ _event: { sequence: 12 } })).toBe(12);
    expect(readChatFrameSeq({ _event: { sequence: "13" } })).toBe(13);
    expect(readChatFrameSeq({ _event: { sequence: "abc" } })).toBe(0);
    expect(readChatFrameSeq({ sequence: 99 })).toBe(0);
    expect(readChatFrameSeq(null)).toBe(0);
  });
});

describe("chat 流入口：闸门拒绝的帧不消费 delta key", () => {
  it("已被 runtime 通道应用过的同 seq 帧整帧丢弃（轨迹/文本/key 都不动）", () => {
    const coordinator = createRuntimeDeltaCoordinator();
    const { claimSpy, handlers, pushTrajectory, turnState } =
      createHarness(coordinator);
    // 另一条通道（runtime）已应用 seq=7 的内容帧（同一页面共享的协调器作用域）。
    expect(
      admitTransportFrame({
        scope: coordinator,
        sessionId: "session-1",
        turnId: "turn-1",
        kind: "text",
        seq: 7,
      }),
    ).toBe(true);

    handlers.onChunk(
      chatFrame({
        type: "text",
        content: "重复帧",
        stream_id: "stream-1",
        sequence: 1,
        _event: { sequence: 7 },
      }),
    );

    expect(turnState.streamedText).toBe("");
    expect(pushTrajectory).not.toHaveBeenCalled();
    expect(claimSpy).not.toHaveBeenCalled();
  });

  it("放行的帧仍走既有 claim 顺序（seq=0 的 wire-only 帧不受闸门影响）", () => {
    const coordinator = createRuntimeDeltaCoordinator();
    const { claimSpy, handlers, pushTrajectory, turnState } =
      createHarness(coordinator);

    handlers.onChunk(
      chatFrame({ type: "text", content: "Hello", stream_id: "s", sequence: 1 }),
    );

    expect(turnState.streamedText).toBe("Hello");
    expect(pushTrajectory).toHaveBeenCalledTimes(1);
    expect(claimSpy).toHaveBeenCalledTimes(1);
  });

  it("放行的持久化 seq 帧推进水位，同 seq 重投直接丢弃", () => {
    const { handlers, pushTrajectory, turnState } = createHarness();
    const frame = chatFrame({
      type: "text",
      content: "A",
      stream_id: "s",
      sequence: 1,
      _event: { sequence: 9 },
    });

    handlers.onChunk(frame);
    handlers.onChunk(frame);

    expect(turnState.streamedText).toBe("A");
    expect(pushTrajectory).toHaveBeenCalledTimes(1);
  });

  it("权威全文帧（mode=replace）不参与闸门，即使带 seq 也必须应用", () => {
    const { handlers, turnState } = createHarness();
    expect(
      admitTransportFrame({
        sessionId: "session-1",
        turnId: "turn-1",
        kind: "text",
        seq: 20,
      }),
    ).toBe(true);

    handlers.onChunk(
      chatFrame({
        mode: "replace",
        type: "text",
        content: "权威全文",
        _event: { sequence: 12 },
      }),
    );

    expect(turnState.streamedText).toBe("权威全文");
  });

  it("reasoning 帧同样过闸门（同 seq 重投只应用一次）", () => {
    const { handlers, turnState } = createHarness();
    const frame = chatFrame({
      type: "reasoning",
      content: "推理",
      _event: { sequence: 4 },
    });

    handlers.onReasoning(frame);
    handlers.onReasoning(frame);

    expect(turnState.reasoningBlocks).toEqual(["推理"]);
  });
});

describe("chat 流 error 帧：租约冲突标题与 CLI 路径同源", () => {
  it("session_lease_conflict 帧带上与 getSessionLeaseConflictTitle 一致的标题", () => {
    const { handlers, updateStreamingError } = createHarness();

    handlers.onErrorEvent({
      error_type: "session_lease_conflict",
      message: "session leased",
      lease: { owner_kind: "runtime-server-agent-chat" },
    });

    const expected = getSessionLeaseConflictTitle(
      new RuntimeApiError(409, {
        code: "SESSION_LEASE_CONFLICT",
        context: { lease: { owner_kind: "runtime-server-agent-chat" } },
      }),
    );
    expect(updateStreamingError).toHaveBeenCalledWith(
      "session leased",
      expected,
    );
  });

  it("其余 error 帧不带标题（行为不变）", () => {
    const { handlers, updateStreamingError } = createHarness();

    handlers.onErrorEvent({ message: "boom" });

    expect(updateStreamingError).toHaveBeenCalledWith("boom", undefined);
  });
});
