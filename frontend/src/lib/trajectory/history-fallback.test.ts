import { beforeEach, describe, expect, it, vi } from "vitest";

import { getSessionHistory } from "@/api/runtime/sessions";
import type { SessionHistoryMessage, SessionRuntimeEvent } from "@/types/runtime";

import {
  HISTORY_MAX_PAGES,
  HISTORY_PAGE_SIZE,
  fetchSessionHistoryMessages,
  hasTrajectoryContentFrames,
  mergeTrajectoryHistoryFallback,
  trajectoryReplaySteps,
  type TrajectoryReplayStep,
} from "./history-fallback";
import { HISTORY_ITEM_PREFIX } from "./session-history";
import { TRAJECTORY_ITEM_ID_KEY } from "./types";

vi.mock("@/api/runtime/sessions", () => ({
  getSessionHistory: vi.fn(),
}));

const mockGetSessionHistory = vi.mocked(getSessionHistory);

function runtimeEvent(
  type: string,
  seq: number,
  extra: Record<string, unknown> = {},
): SessionRuntimeEvent {
  return {
    type,
    timestamp: "2026-09-14T00:00:00Z",
    payload: { seq, ...extra },
  };
}

function message(
  role: string,
  content: string,
  extra: Partial<SessionHistoryMessage> = {},
): SessionHistoryMessage {
  return { role, content, ...extra };
}

/** 步骤 → 可读描述（push 行看正文/工具名，skip 行看 seq）。 */
function describeStep(step: TrajectoryReplayStep): string {
  if (step.type === "skip") {
    return `skip:${step.seq}`;
  }
  const payload = step.push.payload;
  const id = String(payload[TRAJECTORY_ITEM_ID_KEY] ?? "");
  if (step.push.kind === "chunk") {
    return `chunk:${String(payload.content)}`;
  }
  if (step.push.kind === "user") {
    return `user:${String(payload.content)}:${id}`;
  }
  if (step.push.kind === "tool_end") {
    const tool = payload.tool as { id: string };
    return `tool_end:${tool.id}`;
  }
  return step.push.kind;
}

describe("hasTrajectoryContentFrames：判定是否需要历史兜底", () => {
  it("只有生命周期与诊断事件 → false（轨迹会只剩 system 行）", () => {
    expect(
      hasTrajectoryContentFrames([
        runtimeEvent("session_start", 1),
        runtimeEvent("aicli.chat.user_submitted", 2),
        runtimeEvent("session_end", 3),
      ]),
    ).toBe(false);
  });

  it("存在 chat.sse.* 帧 → true", () => {
    expect(
      hasTrajectoryContentFrames([
        runtimeEvent("session_start", 1),
        runtimeEvent("chat.sse.chunk", 2, { type: "text", content: "x" }),
      ]),
    ).toBe(true);
  });

  it("存在 assistant reporter 事件 → true", () => {
    expect(
      hasTrajectoryContentFrames([runtimeEvent("assistant_delta", 5, { content: "x" })]),
    ).toBe(true);
  });
});

describe("trajectoryReplaySteps：事件序列 → 恢复计划", () => {
  it("被过滤事件的 seq 空洞保留为 skip 步（游标不能永久 pending）", () => {
    const steps = trajectoryReplaySteps([
      runtimeEvent("session_start", 1),
      runtimeEvent("tool.progress", 3),
      runtimeEvent("session_end", 5),
    ]);

    expect(steps.map((step) => step.type)).toEqual(["push", "skip", "push"]);
    expect(steps[1]).toMatchObject({ type: "skip", seq: 3 });
  });
});

describe("mergeTrajectoryHistoryFallback：历史消息插回事件序列", () => {
  const events = [
    runtimeEvent("session_start", 1, { turn_id: "turn-1" }),
    runtimeEvent("tool.progress", 2, { turn_id: "turn-1" }),
    runtimeEvent("session_end", 3, { turn_id: "turn-1" }),
    runtimeEvent("session_start", 4, { turn_id: "turn-2" }),
    runtimeEvent("session_compact_completed", 5, { turn_id: "turn-2" }),
  ];

  const history: SessionHistoryMessage[] = [
    message("user", "问题一", { metadata: { turn_id: "turn-1", message_id: "u1" } }),
    message("assistant", "答案一", {
      metadata: { turn_id: "turn-1", message_id: "a1" },
    }),
    message("user", "问题二", { metadata: { turn_id: "turn-2", message_id: "u2" } }),
    message("user", "没有 turn 的消息", { metadata: { message_id: "u3" } }),
  ];

  it("该 turn 的消息插在收尾行之前，顺序完整", () => {
    const merged = mergeTrajectoryHistoryFallback(
      trajectoryReplaySteps(events),
      history,
    );

    expect(merged.map(describeStep)).toEqual([
      "runtime", // session_start
      "skip:2", // tool.progress 空洞仍在原位置
      "user:问题一:history:u1:user",
      "chunk:答案一",
      "runtime", // session_end（该 turn 的消息插在收尾行之前）
      "runtime", // turn-2 session_start
      "runtime", // session_compact_completed
      "user:问题二:history:u2:user", // turn-2 无收尾行 → 追加
      "user:没有 turn 的消息:history:u3:user",
    ]);
  });

  it("多条用户消息不会互相覆盖（每条都有稳定身份）", () => {
    const merged = mergeTrajectoryHistoryFallback(trajectoryReplaySteps(events), history);
    const userIds = merged
      .filter(
        (step) =>
          step.type === "push" &&
          step.push.kind === "user" &&
          String(step.push.payload[TRAJECTORY_ITEM_ID_KEY]).includes("u"),
      )
      .map((step) =>
        step.type === "push"
          ? String(step.push.payload[TRAJECTORY_ITEM_ID_KEY])
          : "",
      );

    expect(new Set(userIds).size).toBe(userIds.length);
    expect(userIds.every((id) => id.startsWith(HISTORY_ITEM_PREFIX))).toBe(true);
  });

  it("没有历史时不改变计划（健康会话零影响）", () => {
    const steps = trajectoryReplaySteps(events);
    expect(mergeTrajectoryHistoryFallback(steps, [])).toEqual(steps);
  });
});

describe("mergeTrajectoryHistoryFallback：aicli 会话（两侧 turn_id 不同 id 空间）", () => {
  // 实测：生命周期事件的 turn_id 是 UUID（turn_6d38f6c0-…），消息元数据里是
  // 另一套 hash id（turn_101d0c3a…），永远不相等 → 按时间顺序位置对齐。
  const eventTurns = [
    "turn_6d38f6c0-875f-43f0-a93c-1c661355981a",
    "turn_011e15ca-3584-4fb7-ab1d-0ff77bd19ee7",
  ];
  const events = [
    runtimeEvent("aicli.chat.user_submitted", 1),
    runtimeEvent("session_start", 2, { turn_id: eventTurns[0] }),
    runtimeEvent("session_compact_skipped", 3, { turn_id: eventTurns[0] }),
    runtimeEvent("session_end", 5, { turn_id: eventTurns[0] }),
    runtimeEvent("aicli.chat.user_submitted", 6),
    runtimeEvent("session_start", 7, { turn_id: eventTurns[1] }),
    runtimeEvent("session_end", 9, { turn_id: eventTurns[1] }),
  ];
  const history: SessionHistoryMessage[] = [
    message("user", "第一个问题", { metadata: { turn_id: "turn_aaaa", message_id: "u1" } }),
    message("assistant", "第一个回答", { metadata: { turn_id: "turn_aaaa", message_id: "a1" } }),
    message("user", "第二个问题", { metadata: { turn_id: "turn_bbbb", message_id: "u2" } }),
    message("assistant", "第二个回答", { metadata: { turn_id: "turn_bbbb", message_id: "a2" } }),
  ];

  it("组数与收尾行数一致 → 每轮消息插在该轮 session_end 之前", () => {
    const merged = mergeTrajectoryHistoryFallback(
      trajectoryReplaySteps(events),
      history,
    );

    expect(merged.map(describeStep)).toEqual([
      "skip:1", // aicli.chat.user_submitted（非白名单）
      "runtime", // session_start(t1)
      "runtime", // session_compact_skipped(t1)
      "user:第一个问题:history:u1:user",
      "chunk:第一个回答",
      "runtime", // session_end(t1)
      "skip:6",
      "runtime", // session_start(t2)
      "user:第二个问题:history:u2:user",
      "chunk:第二个回答",
      "runtime", // session_end(t2)
    ]);
  });

  it("turn_id 能相等匹配时优先 id 通道（不受历史顺序影响）", () => {
    const idEvents = [
      runtimeEvent("session_start", 1, { turn_id: "turn_x" }),
      runtimeEvent("session_end", 2, { turn_id: "turn_x" }),
      runtimeEvent("session_start", 3, { turn_id: "turn_y" }),
      runtimeEvent("session_end", 4, { turn_id: "turn_y" }),
    ];
    // 历史顺序刻意打乱（y 在前）：id 匹配按事件顺序插入，而不是按历史顺序对齐。
    const idHistory: SessionHistoryMessage[] = [
      message("user", "y 轮消息", { metadata: { turn_id: "turn_y" } }),
      message("user", "x 轮消息", { metadata: { turn_id: "turn_x" } }),
    ];

    const merged = mergeTrajectoryHistoryFallback(
      trajectoryReplaySteps(idEvents),
      idHistory,
    );

    expect(merged.map(describeStep)).toEqual([
      "runtime",
      "user:x 轮消息:history:msg-1:user",
      "runtime",
      "runtime",
      "user:y 轮消息:history:msg-0:user",
      "runtime",
    ]);
  });

  it("收尾行多于组数时退化为追加（不会把消息插进别的轮次）", () => {
    const merged = mergeTrajectoryHistoryFallback(
      trajectoryReplaySteps(events),
      [message("user", "只有一条", { metadata: { turn_id: "turn_only" } })],
    );

    expect(merged.map(describeStep)).toEqual([
      "skip:1",
      "runtime",
      "runtime",
      "runtime",
      "skip:6",
      "runtime",
      "runtime",
      "user:只有一条:history:msg-0:user", // 追加在末尾（1 组 != 2 收尾行）
    ]);
  });

  it("历史组多于收尾行（最后一轮还没结束）→ 多出的组追加在末尾", () => {
    const merged = mergeTrajectoryHistoryFallback(
      trajectoryReplaySteps(events.slice(0, 3)), // 只有 t1：start/compact，无 session_end
      history,
    );

    // 收尾行数为 0 → 不启用位置对齐，全部按历史顺序追加（不丢消息）。
    expect(merged.map(describeStep)).toEqual([
      "skip:1",
      "runtime",
      "runtime",
      "user:第一个问题:history:u1:user",
      "chunk:第一个回答",
      "user:第二个问题:history:u2:user",
      "chunk:第二个回答",
    ]);
  });
});

describe("fetchSessionHistoryMessages：分页拉全量历史（旧 → 新）", () => {
  beforeEach(() => {
    mockGetSessionHistory.mockReset();
  });

  it("按 before_seq 翻页并反转拼成正序", async () => {
    mockGetSessionHistory
      .mockResolvedValueOnce({
        session_id: "s1",
        count: 1,
        history: [message("user", "第三条")],
        has_more: true,
        next_before_seq: 3,
      })
      .mockResolvedValueOnce({
        session_id: "s1",
        count: 2,
        history: [message("user", "第一条"), message("user", "第二条")],
        has_more: false,
      });

    const history = await fetchSessionHistoryMessages("s1");

    expect(mockGetSessionHistory).toHaveBeenCalledTimes(2);
    expect(mockGetSessionHistory).toHaveBeenNthCalledWith(1, "s1", {
      limit: HISTORY_PAGE_SIZE,
      beforeSeq: undefined,
    });
    expect(mockGetSessionHistory).toHaveBeenNthCalledWith(2, "s1", {
      limit: HISTORY_PAGE_SIZE,
      beforeSeq: 3,
    });
    expect(history.map((entry) => entry.content)).toEqual([
      "第一条",
      "第二条",
      "第三条",
    ]);
  });

  it("has_more 但游标异常时停止翻页（不会死循环）", async () => {
    mockGetSessionHistory.mockResolvedValue({
      session_id: "s1",
      count: 1,
      history: [message("user", "x")],
      has_more: true,
      next_before_seq: 0,
    });

    await fetchSessionHistoryMessages("s1");

    expect(mockGetSessionHistory).toHaveBeenCalledTimes(1);
  });

  it("取消后返回空数组并停止翻页", async () => {
    mockGetSessionHistory.mockResolvedValue({
      session_id: "s1",
      count: 1,
      history: [message("user", "x")],
      has_more: true,
      next_before_seq: 5,
    });

    const history = await fetchSessionHistoryMessages("s1", () => true);

    expect(history).toEqual([]);
    expect(mockGetSessionHistory).toHaveBeenCalledTimes(1);
  });

  it("页数上限生效（异常会话不会无限翻页）", async () => {
    mockGetSessionHistory.mockResolvedValue({
      session_id: "s1",
      count: 1,
      history: [message("user", "x")],
      has_more: true,
      next_before_seq: 7,
    });

    await fetchSessionHistoryMessages("s1");

    expect(mockGetSessionHistory).toHaveBeenCalledTimes(HISTORY_MAX_PAGES);
  });
});
