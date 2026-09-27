import { afterEach, describe, expect, it, vi } from "vitest";

import type { Thread } from "@/data/mock";
import {
  getLiveStreamEntry,
  resetLiveStreamTextStore,
} from "@/lib/live-stream-text";
import { isReplaceStreamChunk } from "@/lib/thread-state/shared";

import { createAgentChatStreamHandlers } from "./stream-handlers";
import { createTurnRuntimeState } from "./turn-state";

function createHarness() {
  const turnState = createTurnRuntimeState({} as Thread);
  const frameScheduler = { flush: vi.fn(), schedule: vi.fn() };
  const handlers = createAgentChatStreamHandlers({
    assistantMessageId: "msg-1",
    attachTurnArtifact: vi.fn(),
    controller: new AbortController(),
    finalizeTurn: vi.fn(),
    frameScheduler,
    handleToolEnd: vi.fn(),
    notifyFailure: vi.fn(),
    pushTrajectory: vi.fn(),
    sessionId: "session-1",
    setPhaseAndRef: vi.fn(),
    turnId: "turn-1",
    turnState,
    updateCurrentThread: vi.fn(),
    updateStreamingError: vi.fn(),
    upsertLiveToolSegment: vi.fn(),
  });
  return { frameScheduler, handlers, turnState };
}

afterEach(() => {
  resetLiveStreamTextStore();
});

describe("chat 流文本帧的写入语义", () => {
  it("缺省 mode 逐段追加，live 通道同步追加", () => {
    const { handlers, turnState } = createHarness();
    handlers.onChunk({ type: "text", content: "Hel" });
    handlers.onChunk({ type: "text", content: "lo" });
    expect(turnState.streamedText).toBe("Hello");
    expect(getLiveStreamEntry("msg-1")?.text).toBe("Hello");
  });

  it("mode=replace 用权威全文覆盖而不是追加（静态分支收口帧）", () => {
    const { handlers, turnState } = createHarness();
    handlers.onChunk({ type: "text", content: "partial" });
    handlers.onChunk({
      mode: "replace",
      type: "text",
      content: "partial and authoritative",
    });
    expect(turnState.streamedText).toBe("partial and authoritative");
    expect(getLiveStreamEntry("msg-1")?.text).toBe(
      "partial and authoritative",
    );
  });

  it("replace 帧不需要流式去重键也能被应用", () => {
    const { handlers, turnState } = createHarness();
    handlers.onChunk({ mode: "replace", type: "text", content: "final" });
    expect(turnState.streamedText).toBe("final");
  });
});

describe("isReplaceStreamChunk", () => {
  it("只认 replace/snapshot；未知或缺失 mode 保持追加兼容", () => {
    expect(
      isReplaceStreamChunk({ content: "x", mode: "replace", type: "text" }),
    ).toBe(true);
    expect(
      isReplaceStreamChunk({ content: "x", mode: "SNAPSHOT", type: "text" }),
    ).toBe(true);
    expect(
      isReplaceStreamChunk({ content: "x", mode: "append", type: "text" }),
    ).toBe(false);
    expect(isReplaceStreamChunk({ content: "x", type: "text" })).toBe(false);
  });
});
