// P0-2 随源拆分：由 workspace-thread-state.test.ts 按关注点切分，断言未改动。
import { describe, expect, it } from "vitest";

import {
  applyRuntimeDeltaToThread,
} from "@/lib/workspace-thread-state";
import type { SessionRuntimeEvent } from "@/types/runtime";
import { createThread } from "./test-fixtures";
import { matchesActiveTurn } from "./deltas";

describe("applyRuntimeDeltaToThread", () => {
  function deltaEvent(type: string, payload: Record<string, unknown>): SessionRuntimeEvent {
    return { type, timestamp: "2026-08-30T00:00:00Z", payload };
  }

  it("appends assistant_delta text to the latest assistant message", () => {
    const nextThread = applyRuntimeDeltaToThread(
      createThread(),
      deltaEvent("assistant_delta", { delta: " world", stream_id: "stream-1", sequence: 2 }),
    );

    const textSegment = nextThread.messages[0].segments.find((s) => s.type === "text");
    expect(textSegment?.type === "text" ? textSegment.content : "").toBe("Merged answer world");
  });

  it("replaces the streaming placeholder on first delta", () => {
    const thread = createThread();
    thread.messages[0].segments = [{ type: "text", content: "..." }];

    const nextThread = applyRuntimeDeltaToThread(
      thread,
      deltaEvent("assistant_delta", { content: "Hello", sequence: 1 }),
    );

    const textSegment = nextThread.messages[0].segments.find((s) => s.type === "text");
    expect(textSegment?.type === "text" ? textSegment.content : "").toBe("Hello");
  });

  it("appends assistant_reasoning delta into the reasoning segment", () => {
    const thread = createThread();
    thread.messages[0].segments.push({ type: "reasoning", content: "think", running: false });

    const nextThread = applyRuntimeDeltaToThread(
      thread,
      deltaEvent("assistant_reasoning", { reasoning: { summary: " harder" } }),
    );

    const reasoningSegment = nextThread.messages[0].segments.find((s) => s.type === "reasoning");
    expect(reasoningSegment?.type === "reasoning" ? reasoningSegment.content : "").toBe("think\n harder");
    expect(reasoningSegment?.type === "reasoning" ? reasoningSegment.running : false).toBe(true);
  });

  it("creates a reasoning segment when absent", () => {
    const nextThread = applyRuntimeDeltaToThread(
      createThread(),
      deltaEvent("assistant_reasoning", { reasoning: { summary: "cold start" } }),
    );

    const reasoningSegment = nextThread.messages[0].segments.find((s) => s.type === "reasoning");
    expect(reasoningSegment?.type === "reasoning" ? reasoningSegment.content : "").toBe("cold start");
  });

  it("ignores deltas when no assistant message exists", () => {
    const thread = createThread();
    thread.messages = [{ id: "user-1", role: "user", author: "me", label: "", segments: [{ type: "text", content: "hi" }] }];

    const nextThread = applyRuntimeDeltaToThread(
      thread,
      deltaEvent("assistant_delta", { delta: "ignored" }),
    );
    const textSegments = nextThread.messages[0].segments.filter((s) => s.type === "text");
    expect(textSegments).toHaveLength(1);
    expect(textSegments[0].type === "text" ? textSegments[0].content : "").toBe("hi");
  });

  it("keeps non-delta events untouched", () => {
    const thread = createThread();
    const nextThread = applyRuntimeDeltaToThread(
      thread,
      deltaEvent("session_start", { status: "running" }),
    );
    expect(nextThread).toBe(thread);
    const textSegment = nextThread.messages[0].segments[0];
    expect(textSegment.type === "text" ? textSegment.content : "").toBe("Merged answer");
  });

  it("applies a delta without turn identity while a turn is active", () => {
    // 真后端 `loop.go` 仅在 turnID != "" 时注入 payload.turn_id；缺身份
    // 的增量必须照常渲染，否则打字机会退化成「流结束后一次性定型」。
    const nextThread = applyRuntimeDeltaToThread(
      createThread(),
      deltaEvent("assistant_delta", { delta: " typed", stream_id: "s1", sequence: 3 }),
      "turn-active",
    );

    const textSegment = nextThread.messages[0].segments.find((s) => s.type === "text");
    expect(textSegment?.type === "text" ? textSegment.content : "").toBe("Merged answer typed");
  });

  it("ignores a delta explicitly bound to another turn", () => {
    const nextThread = applyRuntimeDeltaToThread(
      createThread(),
      deltaEvent("assistant_delta", {
        delta: "foreign",
        turn_id: "turn-other",
        stream_id: "s1",
        sequence: 4,
      }),
      "turn-active",
    );

    const textSegment = nextThread.messages[0].segments.find((s) => s.type === "text");
    expect(textSegment?.type === "text" ? textSegment.content : "").toBe("Merged answer");
  });

  it("applies a delta whose turn identity is known on only one side", () => {
    // 两条通道（/api/agent/chat 与 /runtime/stream）的 turn 身份空间不同：
    // 消息带 chat 身份而增量缺 turn_id，或增量带身份而消息缺身份，都是真后端
    // 常态。旧实现要求「两边都为空」才放行，于是增量帧全部到齐却一帧也写不进
    // 消息，打字机退化成「流结束后一次性定型」（见 isLiveAssistantMessage）。
    const identifiedMessage = createThread();
    identifiedMessage.messages[0] = {
      ...identifiedMessage.messages[0],
      runtimeTurnId: "chat-turn-9",
    };
    const appliedToIdentified = applyRuntimeDeltaToThread(
      identifiedMessage,
      deltaEvent("assistant_delta", { delta: " typed", stream_id: "s1", sequence: 6 }),
    );
    const identifiedText = appliedToIdentified.messages[0].segments.find(
      (s) => s.type === "text",
    );
    expect(identifiedText?.type === "text" ? identifiedText.content : "").toBe(
      "Merged answer typed",
    );

    const appliedToAnonymous = applyRuntimeDeltaToThread(
      createThread(),
      deltaEvent("assistant_delta", {
        delta: " typed",
        turn_id: "runtime-turn-9",
        stream_id: "s1",
        sequence: 7,
      }),
    );
    const anonymousText = appliedToAnonymous.messages[0].segments.find(
      (s) => s.type === "text",
    );
    expect(anonymousText?.type === "text" ? anonymousText.content : "").toBe(
      "Merged answer typed",
    );
  });

  it("ignores a delta when message and event name different turns", () => {
    const thread = createThread();
    thread.messages[0] = {
      ...thread.messages[0],
      runtimeTurnId: "chat-turn-9",
    };
    const nextThread = applyRuntimeDeltaToThread(
      thread,
      deltaEvent("assistant_delta", {
        delta: "foreign",
        turn_id: "runtime-turn-1",
        stream_id: "s1",
        sequence: 8,
      }),
    );

    const textSegment = nextThread.messages[0].segments.find((s) => s.type === "text");
    expect(textSegment?.type === "text" ? textSegment.content : "").toBe("Merged answer");
  });

  it("treats the assistant.delta bus alias as a text delta", () => {
    const nextThread = applyRuntimeDeltaToThread(
      createThread(),
      deltaEvent("assistant.delta", { delta: " dot", stream_id: "s1", sequence: 5 }),
      "turn-active",
    );

    const textSegment = nextThread.messages[0].segments.find((s) => s.type === "text");
    expect(textSegment?.type === "text" ? textSegment.content : "").toBe("Merged answer dot");
  });
});

describe("matchesActiveTurn（两条通道共用的 turn 归属判定）", () => {
  it("treats unknown identity as unknown, never as another turn", () => {
    expect(matchesActiveTurn("turn-a", "")).toBe(true);
    expect(matchesActiveTurn("turn-a", undefined)).toBe(true);
    expect(matchesActiveTurn("", "turn-b")).toBe(true);
    expect(matchesActiveTurn(undefined, undefined)).toBe(true);
  });

  it("accepts a matching identity and rejects an explicitly different one", () => {
    expect(matchesActiveTurn("turn-a", "turn-a")).toBe(true);
    expect(matchesActiveTurn(" turn-a ", "turn-a")).toBe(true);
    expect(matchesActiveTurn("turn-a", "turn-b")).toBe(false);
  });
});
