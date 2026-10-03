// 会话载荷 LRU 卸载的纯函数测试：有界、无损（仅冷会话）、引用稳定。
import { describe, expect, it } from "vitest";

import { type Thread } from "@/data/mock";

import {
  isRetainedThread,
  threadRetentionKeys,
  trimIdleThreadPayloads,
} from "./retention";

function makeThread(overrides: Partial<Thread> & { id: string }): Thread {
  const { id, ...rest } = overrides;
  return {
    id,
    title: `title-${id}`,
    summary: "",
    updatedAt: "2026-01-01T00:00:00.000Z",
    status: "active",
    tags: [],
    prompts: [],
    messages: [],
    artifacts: [],
    ...rest,
  };
}

function makeThreadWithPayload(
  id: string,
  overrides: Partial<Thread> = {},
): Thread {
  return makeThread({
    id,
    messages: [
      {
        id: `${id}-m1`,
        role: "assistant",
        author: "runtime",
        label: "",
        segments: [{ type: "text", content: `payload-${id}` }],
      },
    ],
    artifacts: [
      {
        id: `${id}-a1`,
        name: "x.json",
        path: "runtime/x.json",
        summary: "",
        kind: "json",
        content: "{}",
      },
    ],
    ...overrides,
  });
}

describe("threadRetentionKeys", () => {
  it("归一化 id 与 sessionId 两种形态并剔除空值", () => {
    expect(
      threadRetentionKeys(makeThread({ id: "thread-1", sessionId: "dir/session-1" })),
    ).toEqual(["thread-1", "session-1"]);
    expect(threadRetentionKeys(makeThread({ id: "solo" }))).toEqual(["solo"]);
  });
});

describe("isRetainedThread", () => {
  const base = { protectedKeys: new Set<string>(), keepKeys: new Set<string>() };

  it("命中保留/保护集合即不卸载（id 与 sessionId 均可命中）", () => {
    const thread = makeThread({ id: "thread-1", sessionId: "session-1" });
    expect(
      isRetainedThread(thread, { ...base, keepKeys: new Set(["session-1"]) }),
    ).toBe(true);
    expect(
      isRetainedThread(thread, {
        ...base,
        protectedKeys: new Set(["thread-1"]),
      }),
    ).toBe(true);
  });

  it("错误现场 / 草稿 / 流式消息一律保留", () => {
    expect(
      isRetainedThread(makeThread({ id: "e", transport: "error" }), base),
    ).toBe(true);
    expect(
      isRetainedThread(makeThread({ id: "d", status: "draft" }), base),
    ).toBe(true);
    const streaming = makeThread({
      id: "s",
      messages: [
        {
          id: "m",
          role: "assistant",
          author: "runtime",
          label: "",
          streaming: true,
          segments: [],
        },
      ],
    });
    expect(isRetainedThread(streaming, base)).toBe(true);
  });
});

describe("trimIdleThreadPayloads", () => {
  it("保留最近 K 个会话与选中会话，更冷的会话清空 messages/artifacts 并保留元数据", () => {
    const threads = ["a", "b", "c", "d"].map((id) => makeThreadWithPayload(id));
    const next = trimIdleThreadPayloads({
      threads,
      selectedThreadId: "a",
      recentKeys: ["a", "b"],
      lruSize: 1,
    });
    const byId = new Map(next.map((thread) => [thread.id, thread]));
    // selected + recent 保留完整载荷。
    expect(byId.get("a")?.messages).toHaveLength(1);
    expect(byId.get("b")?.messages).toHaveLength(1);
    // 窗口外的冷会话：载荷卸载，元数据原样。
    expect(byId.get("c")?.messages).toEqual([]);
    expect(byId.get("c")?.artifacts).toEqual([]);
    expect(byId.get("c")?.title).toBe("title-c");
    expect(byId.get("d")?.messages).toEqual([]);
  });

  it("protectedKeys（在途回合）即使不在 LRU 窗口内也不卸载", () => {
    const threads = ["a", "b"].map((id) => makeThreadWithPayload(id));
    const next = trimIdleThreadPayloads({
      threads,
      selectedThreadId: "a",
      recentKeys: ["a"],
      protectedKeys: new Set(["b"]),
      lruSize: 0,
    });
    expect(next).toBe(threads);
  });

  it("无命中时返回原数组、原线程引用（不触发无意义重渲染）", () => {
    const threads = [makeThreadWithPayload("a"), makeThread({ id: "b" })];
    expect(
      trimIdleThreadPayloads({
        threads,
        selectedThreadId: "a",
        recentKeys: ["a"],
        lruSize: 0,
      }),
    ).toBe(threads);
  });

  it("仅替换被卸载的线程引用，保留线程保持原对象", () => {
    const threads = ["a", "b", "c"].map((id) => makeThreadWithPayload(id));
    const next = trimIdleThreadPayloads({
      threads,
      selectedThreadId: "a",
      recentKeys: ["a"],
      lruSize: 0,
    });
    expect(next[0]).toBe(threads[0]);
    expect(next[1]).not.toBe(threads[1]);
    expect(next[1]?.messages).toEqual([]);
    expect(next[2]?.messages).toEqual([]);
  });
});
