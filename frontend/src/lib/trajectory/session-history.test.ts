import { describe, expect, it } from "vitest";

import type { SessionHistoryMessage } from "@/types/runtime";

import {
  HISTORY_ITEM_PREFIX,
  HISTORY_TEXT_LIMIT,
  groupSessionHistoryTrajectoryPushes,
  historyMessageItemId,
  historyMessageTurnId,
  sessionHistoryMessageToTrajectoryPushes,
  sessionHistoryToTrajectoryPushes,
} from "./session-history";
import { TRAJECTORY_ITEM_ID_KEY } from "./types";

function message(
  role: string,
  content: string,
  extra: Partial<SessionHistoryMessage> = {},
): SessionHistoryMessage {
  return { role, content, ...extra };
}

function itemIdOf(payload: Record<string, unknown>): string {
  return String(payload[TRAJECTORY_ITEM_ID_KEY] ?? "");
}

describe("sessionHistoryMessageToTrajectoryPushes：历史消息 → 降级轨迹帧", () => {
  it("user 消息 → 单条 user 行（带正文与稳定身份）", () => {
    const pushes = sessionHistoryMessageToTrajectoryPushes(
      message("user", "你好", { metadata: { message_id: "m-1" } }),
      0,
    );

    expect(pushes).toHaveLength(1);
    expect(pushes[0].kind).toBe("user");
    expect(pushes[0].payload.content).toBe("你好");
    expect(itemIdOf(pushes[0].payload)).toBe(`${HISTORY_ITEM_PREFIX}m-1:user`);
  });

  it("user 空正文不产生行（避免空白块）", () => {
    expect(sessionHistoryMessageToTrajectoryPushes(message("user", "  "))).toEqual(
      [],
    );
  });

  it("content 为空时回退 content_parts 文本分片", () => {
    const pushes = sessionHistoryMessageToTrajectoryPushes(
      message("user", "", {
        content_parts: [
          { type: "text", text: "分片 A" },
          { type: "image", text: "忽略" },
          { type: "text", text: "分片 B" },
        ],
      }),
    );

    expect(pushes[0].payload.content).toBe("分片 A分片 B");
  });

  it("assistant 消息按 推理 → 正文 → 工具调用 顺序投影，且身份互不覆盖", () => {
    const pushes = sessionHistoryMessageToTrajectoryPushes(
      message("assistant", "答案", {
        metadata: { message_id: "a-1", reasoning_content: "先想一下" },
        tool_calls: [
          { id: "call-1", name: "web_search", arguments: { query: "x" } },
        ],
      }),
      3,
    );

    expect(pushes.map((push) => push.kind)).toEqual([
      "reasoning",
      "chunk",
      "tool_start",
    ]);
    expect(pushes[1].payload.type).toBe("text");
    expect(pushes[1].payload.content).toBe("答案");
    const ids = pushes.map((push) => itemIdOf(push.payload));
    expect(new Set(ids).size).toBe(3);
    expect(pushes[2].payload.tool).toMatchObject({
      id: "call-1",
      name: "web_search",
    });
    expect(
      (pushes[2].payload.tool as { args_summary: string }).args_summary,
    ).toContain("x");
  });

  it("reasoning_details 数组兼容（metadata.reasoning_content 缺失时）", () => {
    const pushes = sessionHistoryMessageToTrajectoryPushes(
      message("assistant", "答案", {
        metadata: {
          reasoning_details: [{ text: "片段1" }, { summary: "片段2" }],
        },
      }),
    );

    expect(pushes[0].kind).toBe("reasoning");
    expect(pushes[0].payload.content).toBe("片段1片段2");
  });

  it("仅工具调用的 assistant 消息不产生空正文行", () => {
    const pushes = sessionHistoryMessageToTrajectoryPushes(
      message("assistant", "", {
        metadata: { message_id: "a-2" },
        tool_calls: [{ id: "call-2", name: "read_file" }],
      }),
    );

    expect(pushes).toHaveLength(1);
    expect(pushes[0].kind).toBe("tool_start");
  });

  it("tool 消息 → tool_end（tool_call_id 作为工具身份，与 live 帧一致）", () => {
    const pushes = sessionHistoryMessageToTrajectoryPushes(
      message("tool", "工具输出", {
        tool_call_id: "call-1",
        metadata: { tool_name: "web_search" },
      }),
    );

    expect(pushes).toHaveLength(1);
    expect(pushes[0].kind).toBe("tool_end");
    expect(pushes[0].payload.tool).toMatchObject({
      id: "call-1",
      name: "web_search",
      output: "工具输出",
    });
  });

  it("metadata.ok=false 的 tool 消息走 error 分支", () => {
    const pushes = sessionHistoryMessageToTrajectoryPushes(
      message("tool", "命令失败", {
        tool_call_id: "call-9",
        metadata: { ok: false, tool_name: "shell" },
      }),
    );

    const tool = pushes[0].payload.tool as Record<string, unknown>;
    expect(tool.error).toBe("命令失败");
    expect(tool.output).toBeUndefined();
  });

  it("缺 tool_call_id 的 tool 消息跳过（无身份会覆盖其他工具行）", () => {
    expect(
      sessionHistoryMessageToTrajectoryPushes(message("tool", "输出")),
    ).toEqual([]);
  });

  it("system/developer 消息是提示词脚手架，不进入轨迹", () => {
    expect(sessionHistoryMessageToTrajectoryPushes(message("system", "提示"))).toEqual(
      [],
    );
    expect(
      sessionHistoryMessageToTrajectoryPushes(message("developer", "提示")),
    ).toEqual([]);
  });

  it("超长正文按上限截断（避免撑爆轨迹）", () => {
    const pushes = sessionHistoryMessageToTrajectoryPushes(
      message("user", "x".repeat(HISTORY_TEXT_LIMIT + 100)),
    );

    const content = String(pushes[0].payload.content);
    expect(content.length).toBe(HISTORY_TEXT_LIMIT + 1);
    expect(content.endsWith("…")).toBe(true);
  });

  it("缺 message_id 时用下标兜底，同一份历史投影结果稳定", () => {
    const first = historyMessageItemId(message("user", "a"), 2, "user");
    const second = historyMessageItemId(message("user", "a"), 2, "user");
    expect(first).toBe(second);
    expect(first).toBe(`${HISTORY_ITEM_PREFIX}msg-2:user`);
  });
});

describe("sessionHistoryToTrajectoryPushes：整段历史按序投影", () => {
  it("多轮消息各自成行，不互相覆盖", () => {
    const pushes = sessionHistoryToTrajectoryPushes([
      message("user", "问题一", { metadata: { message_id: "u1" } }),
      message("assistant", "答案一", { metadata: { message_id: "a1" } }),
      message("tool", "结果", {
        tool_call_id: "call-1",
        metadata: { tool_name: "t" },
      }),
      message("user", "问题二", { metadata: { message_id: "u2" } }),
    ]);

    expect(pushes.map((push) => push.kind)).toEqual([
      "user",
      "chunk",
      "tool_end",
      "user",
    ]);
    const userIds = pushes
      .filter((push) => push.kind === "user")
      .map((push) => itemIdOf(push.payload));
    expect(userIds).toEqual([
      `${HISTORY_ITEM_PREFIX}u1:user`,
      `${HISTORY_ITEM_PREFIX}u2:user`,
    ]);
  });
});

describe("groupSessionHistoryTrajectoryPushes：按 turn 分组", () => {
  const history: SessionHistoryMessage[] = [
    message("user", "问题", { metadata: { turn_id: "turn-1" } }),
    message("assistant", "答案", { metadata: { turn_id: "turn-1" } }),
    message("user", "再问", { metadata: { turn_id: "turn-2" } }),
    message("system", "脚手架"),
    message("user", "游离", {}),
  ];

  it("连续同 turn 合并，turn 变化另起一组，无 turn 消息单独成组", () => {
    const groups = groupSessionHistoryTrajectoryPushes(history);

    expect(groups.map((group) => group.turnId)).toEqual(["turn-1", "turn-2", ""]);
    expect(groups[0].pushes).toHaveLength(2);
    expect(groups[2].pushes).toHaveLength(1);
  });

  it("historyMessageTurnId 读 metadata.turn_id（缺失为空串）", () => {
    expect(historyMessageTurnId(history[0])).toBe("turn-1");
    expect(historyMessageTurnId(history[4])).toBe("");
  });
});
