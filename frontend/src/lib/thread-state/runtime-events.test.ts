// P0-2 随源拆分：由 workspace-thread-state.test.ts 按关注点切分，断言未改动。
import { describe, expect, it } from "vitest";

import {
  applyRuntimeEventToThread,
  buildStreamingMessageSegments,
  createStreamingAssistantMessage,
  mergeRuntimeSessionsIntoThreads,
  mergeRuntimeEvent,
} from "@/lib/workspace-thread-state";
import type { RuntimeSessionRecord, SessionRuntimeEvent } from "@/types/runtime";
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
