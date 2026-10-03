// runtime-stream-retention 纯函数测试：终态判定 / live 消息收集 / 队列上限淘汰。
import { describe, expect, it } from "vitest";

import { type Thread } from "@/data/mock";
import type { SessionRuntimeEvent } from "@/lib/runtime-api";

import {
  collectFinalizedLiveMessageIds,
  isFinalizedTurnFrame,
  MAX_PENDING_RUNTIME_COMMITS,
  trimPendingRuntimeCommits,
} from "./runtime-stream-retention";

function event(type: string, payload: Record<string, unknown> = {}): SessionRuntimeEvent {
  return { type, timestamp: "2026-08-30T00:00:00Z", payload };
}

function threadWithMessages(): Thread {
  return {
    id: "t",
    title: "t",
    summary: "",
    updatedAt: "2026-08-30T00:00:00Z",
    status: "active",
    tags: [],
    prompts: [],
    messages: [
      {
        id: "m-match",
        role: "assistant",
        author: "runtime",
        label: "streaming",
        runtimeTurnId: "turn-1",
        streaming: true,
        segments: [],
      },
      {
        id: "m-other",
        role: "assistant",
        author: "runtime",
        label: "streaming",
        runtimeTurnId: "turn-2",
        streaming: true,
        segments: [],
      },
      {
        id: "m-done",
        role: "assistant",
        author: "runtime",
        label: "runtime",
        streaming: false,
        segments: [],
      },
    ],
    artifacts: [],
  };
}

describe("isFinalizedTurnFrame", () => {
  it("覆盖 runtime 原生终态与 chat.sse.* 终态，普通增量不算终态", () => {
    expect(isFinalizedTurnFrame(event("turn.finished"))).toBe(true);
    expect(isFinalizedTurnFrame(event("turn_finished"))).toBe(true);
    expect(isFinalizedTurnFrame(event("chat.sse.done"))).toBe(true);
    expect(isFinalizedTurnFrame(event("assistant_delta"))).toBe(false);
  });
});

describe("collectFinalizedLiveMessageIds", () => {
  it("只收同回合仍在 streaming 的助手消息（缺 turn_id 视为匹配）", () => {
    const ids = collectFinalizedLiveMessageIds(
      threadWithMessages(),
      event("turn.finished", { turn_id: "turn-1" }),
    );
    expect(ids).toEqual(["m-match"]);
  });

  it("终态缺 turn_id 时收敛全部在途助手消息", () => {
    const ids = collectFinalizedLiveMessageIds(
      threadWithMessages(),
      event("turn.finished"),
    );
    expect(ids).toEqual(["m-match", "m-other"]);
  });
});

describe("trimPendingRuntimeCommits", () => {
  it("超限时优先丢弃最旧的纯增量条目", () => {
    const queue = Array.from({ length: MAX_PENDING_RUNTIME_COMMITS + 1 }, (_, index) => ({
      id: index,
      shouldApplyLiveDelta: index < 3,
    }));
    trimPendingRuntimeCommits(queue);
    expect(queue).toHaveLength(MAX_PENDING_RUNTIME_COMMITS);
    expect(queue[0]?.id).toBe(1);
    expect(queue.some((item) => item.id === 0)).toBe(false);
  });

  it("整队皆持久事件时丢弃最旧条目保底", () => {
    const queue = Array.from({ length: MAX_PENDING_RUNTIME_COMMITS + 1 }, (_, index) => ({
      id: index,
      shouldApplyLiveDelta: false,
    }));
    trimPendingRuntimeCommits(queue);
    expect(queue).toHaveLength(MAX_PENDING_RUNTIME_COMMITS);
    expect(queue[0]?.id).toBe(1);
  });

  it("未超限不动队列", () => {
    const queue = [{ shouldApplyLiveDelta: true }];
    trimPendingRuntimeCommits(queue);
    expect(queue).toHaveLength(1);
  });
});
