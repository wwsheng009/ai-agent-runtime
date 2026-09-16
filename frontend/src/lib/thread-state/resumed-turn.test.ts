// P4-刷新续传：在途回合在线程上的认领 / 撤销（纯函数）。
import { describe, expect, it } from "vitest";

import type { ChatMessage, Thread } from "@/data/mock";
import {
  adoptResumedTurnInThread,
  releaseResumedTurnInThread,
  threadHoldsResumedTurn,
} from "./resumed-turn";
import { createThread } from "./test-fixtures";

function historyAssistant(id: string, content: string): ChatMessage {
  return {
    id,
    role: "assistant",
    author: "Runtime stream",
    label: "assistant",
    segments: [{ type: "text", content }],
  };
}

function historyUser(id: string, content: string): ChatMessage {
  return {
    id,
    role: "user",
    author: "You",
    label: "user",
    segments: [{ type: "text", content }],
  };
}

function partialThread(): Thread {
  return {
    ...createThread(),
    messages: [
      historyUser("msg-user-1", "继续写完"),
      historyAssistant("msg-assistant-1", "前半截答案"),
    ],
  };
}

describe("adoptResumedTurnInThread", () => {
  it("把末尾半截助手消息认领为在途回合", () => {
    const next = adoptResumedTurnInThread(partialThread(), "turn-9");

    const adopted = next.messages[next.messages.length - 1];
    expect(adopted).toMatchObject({
      id: "msg-assistant-1",
      streaming: true,
      runtimeTurnId: "turn-9",
    });
    // 正文交给消息自己承载：认领只补在途标记，不碰内容。
    expect(adopted.segments).toEqual([
      { type: "text", content: "前半截答案" },
    ]);
    expect(threadHoldsResumedTurn(next, "turn-9")).toBe(true);
  });

  it("同回合重复认领是幂等的（引用相等）", () => {
    const adopted = adoptResumedTurnInThread(partialThread(), "turn-9");

    expect(adoptResumedTurnInThread(adopted, "turn-9")).toBe(adopted);
    expect(adoptResumedTurnInThread(adopted, " turn-9 ")).toBe(adopted);
  });

  it("末尾是 user 消息时不认领（本回合还没有可续写的正文）", () => {
    const thread: Thread = {
      ...createThread(),
      messages: [historyUser("msg-user-1", "新问题")],
    };

    expect(adoptResumedTurnInThread(thread, "turn-9")).toBe(thread);
  });

  it("提示基础设施行不认领（它的正文不渲染，认领会吞掉整轮增量）", () => {
    // 刷新后的线程里只有历史同步投影出的 System prompt 行：形状与「未定稿的助手
    // 消息」一致，但渲染口径是折叠标题。认领它 → 增量写进不显示的正文。
    const infraRows: ChatMessage[] = [
      {
        id: "msg-system-1",
        role: "assistant",
        author: "System context",
        label: "system",
        segments: [{ type: "text", content: "系统提示词" }],
      },
      {
        id: "msg-system-2",
        role: "assistant",
        author: "Runtime history",
        label: "system",
        segments: [{ type: "text", content: "系统提示词" }],
      },
    ];

    for (const row of infraRows) {
      const thread: Thread = {
        ...createThread(),
        messages: [historyUser("msg-user-1", "问题"), row],
      };

      expect(adoptResumedTurnInThread(thread, "turn-9")).toBe(thread);
    }
  });

  it("已定稿 / 已中止 / 已被别的回合认领的助手消息都不抢", () => {
    const threads: Thread[] = [
      {
        ...createThread(),
        messages: [
          historyUser("msg-user-1", "问题"),
          { ...historyAssistant("msg-a", "上一轮答复"), streaming: false },
        ],
      },
      {
        ...createThread(),
        messages: [
          historyUser("msg-user-1", "问题"),
          { ...historyAssistant("msg-a", "被中止的答复"), interrupted: true },
        ],
      },
      {
        ...createThread(),
        messages: [
          historyUser("msg-user-1", "问题"),
          { ...historyAssistant("msg-a", "别的回合"), runtimeTurnId: "turn-old" },
        ],
      },
    ];

    for (const thread of threads) {
      expect(adoptResumedTurnInThread(thread, "turn-9")).toBe(thread);
    }
  });

  it("空回合身份 / 空线程不改动", () => {
    const thread = partialThread();
    const emptyThread: Thread = { ...thread, messages: [] };

    expect(adoptResumedTurnInThread(thread, "")).toBe(thread);
    expect(adoptResumedTurnInThread(thread, null)).toBe(thread);
    expect(adoptResumedTurnInThread(emptyThread, "turn-9")).toBe(emptyThread);
  });
});

describe("releaseResumedTurnInThread", () => {
  it("回合结束后清 streaming，保留正文与回合身份", () => {
    const adopted = adoptResumedTurnInThread(partialThread(), "turn-9");

    const released = releaseResumedTurnInThread(adopted, "turn-9");
    const message = released.messages[released.messages.length - 1];
    expect(message.streaming).toBe(false);
    expect(message.runtimeTurnId).toBe("turn-9");
    expect(message.segments).toEqual([
      { type: "text", content: "前半截答案" },
    ]);
  });

  it("非本回合 / 已收敛时不改动（引用相等）", () => {
    const adopted = adoptResumedTurnInThread(partialThread(), "turn-9");
    const released = releaseResumedTurnInThread(adopted, "turn-9");

    expect(releaseResumedTurnInThread(adopted, "turn-other")).toBe(adopted);
    expect(releaseResumedTurnInThread(adopted, "")).toBe(adopted);
    expect(releaseResumedTurnInThread(released, "turn-9")).toBe(released);
  });
});
