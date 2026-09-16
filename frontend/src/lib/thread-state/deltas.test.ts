// P0-2 随源拆分：由 workspace-thread-state.test.ts 按关注点切分，断言未改动。
import { describe, expect, it } from "vitest";

import type { ChatMessage, Thread } from "@/data/mock";
import {
  applyRuntimeDeltaToThread,
  createStreamingAssistantMessage,
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

// 断流 / 重放后的事件重建（2026-09-16）：增量帧的目标回合在 thread 里没有 live
// 助手消息时不再丢弃，而是补建 streaming 占位消息——否则 /runtime/events 重放里
// 明明有完整的 assistant_delta 帧，对话面板也只能空着等回合结束。
describe("applyRuntimeDeltaToThread 在无 live 消息时补建在途回合", () => {
  function deltaEvent(payload: Record<string, unknown>): SessionRuntimeEvent {
    return { type: "assistant_delta", timestamp: "2026-09-16T00:00:00Z", payload };
  }

  /** 断流 / reload 后的会话：助手消息已定稿，没有可写的 live 目标。 */
  function createFinalizedThread(): Thread {
    const thread = createThread();
    thread.messages[0] = { ...thread.messages[0], label: "answer", streaming: false };
    return thread;
  }

  function textOf(message: ChatMessage): string {
    return message.segments
      .filter((segment) => segment.type === "text")
      .map((segment) => segment.content)
      .join("");
  }

  it("补建 streaming 占位消息，正文只追加一次", () => {
    const thread = applyRuntimeDeltaToThread(
      createFinalizedThread(),
      deltaEvent({ delta: "重放正文", turn_id: "turn-1", stream_id: "stream-1", sequence: 1 }),
      "turn-1",
    );

    const created = thread.messages[thread.messages.length - 1];
    expect(thread.messages.filter((message) => message.role === "assistant")).toHaveLength(2);
    // 字段与既有命名一致（isLiveAssistantMessage 后续仍能命中这条消息）。
    expect(created).toMatchObject({
      id: "turn-turn-1-assistant",
      streaming: true,
      label: "streaming",
      runtimeTurnId: "turn-1",
    });
    expect(created.segments).toEqual([{ type: "text", content: "重放正文" }]);
    expect(textOf(created)).toBe("重放正文");

    // 同回合的后续增量继续落在这条消息上：不新建第二条、也不重复写入。
    const next = applyRuntimeDeltaToThread(
      thread,
      deltaEvent({ delta: "，继续", turn_id: "turn-1", stream_id: "stream-1", sequence: 2 }),
      "turn-1",
    );
    expect(next.messages.filter((message) => message.role === "assistant")).toHaveLength(2);
    expect(textOf(next.messages[next.messages.length - 1])).toBe("重放正文，继续");
  });

  it("回合归属不明或属于旧回合时不补建消息（引用相等）", () => {
    const thread = createFinalizedThread();

    // 帧无 turn_id 且调用方也没有在途回合身份：归属不明 → 保持旧行为（丢弃）。
    expect(
      applyRuntimeDeltaToThread(thread, deltaEvent({ delta: "x", stream_id: "s1", sequence: 1 })),
    ).toBe(thread);
    // 帧的回合与会话当前在途回合明确不一致：旧回合 → 不建消息。
    expect(
      applyRuntimeDeltaToThread(
        thread,
        deltaEvent({ delta: "x", turn_id: "turn-old", stream_id: "s1", sequence: 1 }),
        "turn-1",
      ),
    ).toBe(thread);
  });

  it("同 id 的助手消息已存在（该回合已定稿）时不补建，避免同 id 重复消息", () => {
    const thread = createFinalizedThread();
    thread.messages.push({
      ...createStreamingAssistantMessage("turn-turn-1-assistant", [], "turn-1"),
      streaming: false,
    });

    expect(
      applyRuntimeDeltaToThread(
        thread,
        deltaEvent({ delta: "x", turn_id: "turn-1", stream_id: "s1", sequence: 1 }),
        "turn-1",
      ),
    ).toBe(thread);
  });
});
