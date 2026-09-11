// P0-2 随源拆分：由 workspace-thread-state.test.ts 按关注点切分，断言未改动。
import { describe, expect, it } from "vitest";

import {
  applyRuntimeDeltaToThread,
} from "@/lib/workspace-thread-state";
import type { SessionRuntimeEvent } from "@/types/runtime";
import { createThread } from "./test-fixtures";

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
});
