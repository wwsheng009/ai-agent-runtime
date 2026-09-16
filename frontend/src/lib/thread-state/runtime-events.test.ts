// P0-2 随源拆分：由 workspace-thread-state.test.ts 按关注点切分，断言未改动。
import { describe, expect, it } from "vitest";

import type { Thread } from "@/data/mock";
import {
  applyRuntimeDeltaToThread,
  applyRuntimeEventToThread,
  buildStreamingMessageSegments,
  createStreamingAssistantMessage,
  mergeRuntimeSessionsIntoThreads,
  mergeRuntimeEvent,
} from "@/lib/workspace-thread-state";
import type { RuntimeSessionRecord, SessionRuntimeEvent } from "@/types/runtime";
import { applyChatSseBridgeFrame } from "./events-live";
import { createThread } from "./test-fixtures";

describe("runtime events and session merge", () => {
  it("deduplicates runtime events and keeps the newest 100 entries", () => {
    const seed: SessionRuntimeEvent[] = Array.from({ length: 100 }, (_, index) => ({
      type: "runtime.step",
      timestamp: `2026-03-31T00:00:${String(index).padStart(2, "0")}Z`,
      payload: {
        seq: index + 1,
      },
    }));

    const duplicate = seed[99];
    const deduped = mergeRuntimeEvent(seed, duplicate);
    expect(deduped).toHaveLength(100);
    expect(deduped).toBe(seed);

    const next = mergeRuntimeEvent(seed, {
      type: "runtime.step",
      timestamp: "2026-03-31T00:01:40Z",
      payload: {
        seq: 101,
      },
    });

    expect(next).toHaveLength(100);
    expect(next[0].payload?.seq).toBe(2);
    expect(next[99].payload?.seq).toBe(101);
  });

  it("preserves existing thread identity while attaching a restored runtime session", () => {
    const thread = createThread();
    const sessions: RuntimeSessionRecord[] = [
      {
        id: "session-1",
        state: "active",
        metadata: {
          title: "Recovered thread",
          summary: "Loaded from runtime session list.",
          lastSkill: "workspace",
        },
        updatedAt: "2026-03-31T10:10:00Z",
      },
    ];

    const nextThreads = mergeRuntimeSessionsIntoThreads(
      [{ ...thread, id: "thread-local", sessionId: "session-1" }],
      sessions,
    );

    expect(nextThreads).toHaveLength(1);
    expect(nextThreads[0]).toMatchObject({
      id: "thread-local",
      sessionId: "session-1",
      title: "Recovered thread",
      summary: "Loaded from runtime session list.",
      runtimeSource: "workspace",
    });
  });

  it("adds a stopped callout while preserving partial output and reasoning", () => {
    const segments = buildStreamingMessageSegments(
      "Partial answer",
      "runtime",
      "Need a follow-up step",
      {
        status: "stopped",
      },
    );

    expect(segments).toEqual([
      {
        type: "text",
        content: "Partial answer",
      },
      {
        type: "reasoning",
        content: "Need a follow-up step",
        running: false,
      },
      {
        type: "callout",
        title: "Response stopped",
        tone: "warning",
        content:
          "Generation was stopped locally. Partial output is preserved so the next turn can continue from this point.",
      },
    ]);
  });

  it("§12.1.4：没有正文时不产占位文本段（首块到达前正文区不出现「...」行）", () => {
    expect(buildStreamingMessageSegments("", "runtime", "")).toEqual([]);

    // 只有推理先行时，段落序列里也不带占位文本段。
    expect(buildStreamingMessageSegments("", "runtime", "先看入口文件")).toEqual([
      {
        type: "reasoning",
        content: "先看入口文件",
        running: false,
      },
    ]);
  });

  it("§12.1.4：流式助手消息初始无 segments（占位不再落成文本行）", () => {
    const message = createStreamingAssistantMessage(
      "assistant-1",
      ["artifact-1"],
      "turn-1",
    );

    expect(message.segments).toEqual([]);
  });

  // 回归：合并结果必须「内容没变 ⇒ 身份不变」。缺 updatedAt/createdAt 的会话此前
  // 每次合并都会生成新的 ISO 时间戳，导致 changed 恒为 true、线程数组每事件换新、
  // 下游 memo 全部失效（流式期间每事件一次全页重渲染）。
  it("会话列表无变化时合并返回同一数组（不产生虚假变更）", () => {
    const sessions: RuntimeSessionRecord[] = [
      {
        id: "session-stable",
        state: "active",
        metadata: { title: "Stable thread" },
      },
    ];

    const first = mergeRuntimeSessionsIntoThreads([], sessions);
    expect(first).toHaveLength(1);

    const second = mergeRuntimeSessionsIntoThreads(first, sessions);
    expect(second).toBe(first);

    // 同一份快照重复合并同样不该换身份（每个 SSE 事件都会重跑一次）。
    const third = mergeRuntimeSessionsIntoThreads(first, [...sessions]);
    expect(third).toBe(first);
  });

  it("会话更新后仍复用同一条线程（索引替换路径不丢匹配）", () => {
    const seeded = mergeRuntimeSessionsIntoThreads([], [
      { id: "session-a", state: "active", metadata: { title: "First" } },
      { id: "session-b", state: "active", metadata: { title: "Second" } },
    ]);
    expect(seeded).toHaveLength(2);

    const next = mergeRuntimeSessionsIntoThreads(seeded, [
      {
        id: "session-a",
        state: "active",
        metadata: { title: "First" },
        updatedAt: "2026-03-31T12:00:00Z",
      },
      { id: "session-b", state: "active", metadata: { title: "Second" } },
    ]);

    expect(next).toHaveLength(2);
    expect(next.find((thread) => thread.sessionId === "session-a")?.updatedAt).toBe(
      "2026-03-31T12:00:00Z",
    );
  });

  // 回归：运行时事件产物承担「最近 100 条事件的完整 payload」，每来一个事件都会
  // 重建一次。惰性化后未读取 content 就不该付出 JSON.stringify 成本（CDP 实测
  // 单回合 729ms），但读到的字符串必须与 eager 版本完全一致。
  it("运行时事件产物惰性序列化，且 content 与 eager 结果一致", () => {
    let serializations = 0;
    const event: SessionRuntimeEvent = {
      type: "runtime.step",
      timestamp: "2026-03-31T00:02:00Z",
      payload: {
        seq: 7,
        probe: {
          toJSON: () => {
            serializations += 1;
            return "probe-value";
          },
        },
      },
    };

    const nextThread = applyRuntimeEventToThread(
      createThread(),
      "session-1",
      [event],
      event,
    );
    const artifact = nextThread.artifacts.find(
      (item) => item.id === "session-runtime-events-session-1",
    );

    expect(artifact).toBeDefined();
    expect(serializations).toBe(0);

    // 期望串自身会触发一次 toJSON，因此按「增量」断言：读取 content 只多一次。
    const expected = JSON.stringify(
      { session_id: "session-1", count: 1, events: [event] },
      null,
      2,
    );
    const afterExpected = serializations;

    expect(artifact?.content).toBe(expected);
    expect(serializations).toBe(afterExpected + 1);
    // 记忆化：重复读取不再重新序列化。
    expect(artifact?.content).toContain("probe-value");
    expect(serializations).toBe(afterExpected + 1);
  });
});

// 断流 / 重放后的事件重建（2026-09-16）：桥接帧的目标回合在 thread 里没有 live
// 助手消息时，按帧的 turn id 与调用方持有的在途回合（同源于 chat 请求的 turn_id）
// 补建 streaming 占位消息，而不是整帧丢弃——/runtime/events?after= 重放里明明有
// 完整的 chat.sse.* 帧，对话面板却只能空着等回合结束。
describe("applyRuntimeEventToThread 在无 live 消息时按桥接帧补建在途回合", () => {
  const TOOL_CALL = {
    id: "observation_step_1_tool_0",
    name: "shell",
    arguments: { command: "go test ./..." },
  };

  /** 断流 / reload 后的会话：助手消息已定稿，没有可写的 live 目标。 */
  function createRecoveredThread(): Thread {
    const thread = createThread();
    thread.messages[0] = { ...thread.messages[0], label: "answer", streaming: false };
    return thread;
  }

  function toolFrame(
    type: string,
    options: { content?: string; withDelta?: boolean; turnId?: string } = {},
  ): SessionRuntimeEvent {
    return {
      type,
      timestamp: "2026-09-16T00:00:01Z",
      payload: {
        type,
        index: 1,
        content: options.content ?? "",
        ...(options.withDelta ? { delta: TOOL_CALL } : {}),
        tool: { ...TOOL_CALL, args: TOOL_CALL.arguments, status: type, content: options.content ?? "" },
        tool_call: TOOL_CALL,
        metadata: {},
        ...(options.turnId ? { turn_id: options.turnId } : {}),
      },
    };
  }

  function applyTurn(thread: Thread, event: SessionRuntimeEvent): Thread {
    return applyRuntimeEventToThread(thread, "session-1", [event], event, "turn-1");
  }

  function lastMessage(thread: Thread) {
    return thread.messages[thread.messages.length - 1];
  }

  function toolRows(thread: Thread) {
    return lastMessage(thread).segments.filter((segment) => segment.type === "tool");
  }

  it("tool_end 帧补建 streaming 占位消息并落成工具行", () => {
    const next = applyTurn(
      createRecoveredThread(),
      toolFrame("chat.sse.tool_end", { content: "===== command 1/1 [ok] =====\nExit code: 0", turnId: "turn-1" }),
    );

    // 字段与既有命名一致（isLiveAssistantMessage 后续仍能命中这条消息）。
    expect(lastMessage(next)).toMatchObject({
      id: "turn-turn-1-assistant",
      role: "assistant",
      streaming: true,
      label: "streaming",
      runtimeTurnId: "turn-1",
    });
    // 工具行按 tool_call.id 合键，status 走既有映射（tool_end → finished）。
    expect(toolRows(next)).toEqual([
      expect.objectContaining({ toolCallId: TOOL_CALL.id, name: "shell", status: "finished" }),
    ]);
    expect(next.lastRuntimeEventType).toBe("tool_end:shell");
  });

  it("同回合的增量 + 连续工具帧收敛到同一条补建消息", () => {
    let thread = applyRuntimeDeltaToThread(
      createRecoveredThread(),
      {
        type: "assistant_delta",
        timestamp: "2026-09-16T00:00:02Z",
        payload: { delta: "开始测试", turn_id: "turn-1", stream_id: "stream-1", sequence: 1 },
      },
      "turn-1",
    );
    thread = applyTurn(thread, toolFrame("chat.sse.tool_call", { withDelta: true, turnId: "turn-1" }));
    thread = applyTurn(thread, toolFrame("chat.sse.tool_end", { content: "Exit code: 0", turnId: "turn-1" }));

    expect(thread.messages.filter((message) => message.streaming === true)).toHaveLength(1);
    expect(lastMessage(thread).id).toBe("turn-turn-1-assistant");
    // 正文只出现一次；两条工具帧（tool_call.id 合键）收敛成一行。
    expect(lastMessage(thread).segments.filter((segment) => segment.type === "text")).toEqual([
      { type: "text", content: "开始测试" },
    ]);
    expect(toolRows(thread)).toHaveLength(1);
    expect(toolRows(thread)[0]).toMatchObject({ toolCallId: TOOL_CALL.id, status: "finished" });
  });

  it("回合归属不明或属于旧回合的桥接帧不补建消息（引用相等）", () => {
    const thread = createRecoveredThread();
    const bridgeKind = { kind: "tool", status: "finished" } as const;

    // 帧无 turn_id 且调用方也没有在途回合身份：归属不明 → 保持旧行为（丢弃）。
    expect(
      applyChatSseBridgeFrame(thread, toolFrame("chat.sse.tool_end", { content: "out" }), bridgeKind),
    ).toBe(thread);
    // 帧的回合与会话当前在途回合明确不一致：旧回合 → 不建消息。
    expect(
      applyChatSseBridgeFrame(
        thread,
        toolFrame("chat.sse.tool_end", { content: "out", turnId: "turn-old" }),
        bridgeKind,
        "turn-1",
      ),
    ).toBe(thread);
  });

  it("纯阶段帧（chunk）不补建空占位消息", () => {
    const thread = createRecoveredThread();
    const chunk: SessionRuntimeEvent = {
      type: "chat.sse.chunk",
      timestamp: "2026-09-16T00:00:03Z",
      payload: { content: "增量", turn_id: "turn-1" },
    };

    expect(applyChatSseBridgeFrame(thread, chunk, { kind: "phase" }, "turn-1")).toBe(thread);
  });
});
