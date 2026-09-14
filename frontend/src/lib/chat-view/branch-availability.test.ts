/**
 * 批次 2（§5.4）验收：`resolveBranchAnchor` 的锚点结论。
 *
 * 覆盖五类输入（方案 §5.4 测试计划）：
 * - 空流 / 流式中 / live-only（中断）消息在尾部 / 合成退化 id / 正常末尾；
 * - 另补：等待审批、末尾是未回答的用户消息、末尾是工具回执。
 */
import { describe, expect, it } from "vitest";

import type { ChatMessage, MessageSegment } from "@/data/mock";

import {
  BRANCH_UNAVAILABLE_REASON_KEY,
  isSyntheticHistoryMessageId,
  resolveBranchAnchor,
} from "./branch-availability";
import { projectChatFlow } from "./flow";

const UNAVAILABLE = {
  kind: "unavailable",
  reasonKey: BRANCH_UNAVAILABLE_REASON_KEY,
} as const;

function assistantMessage(
  segments: MessageSegment[],
  overrides: Partial<ChatMessage> = {},
): ChatMessage {
  return {
    id: "assistant-1",
    role: "assistant",
    author: "Runtime stream",
    label: "response",
    segments,
    ...overrides,
  };
}

function userMessage(
  segments: MessageSegment[],
  overrides: Partial<ChatMessage> = {},
): ChatMessage {
  return {
    id: "user-1",
    role: "user",
    author: "You",
    label: "prompt",
    segments,
    ...overrides,
  };
}

const ANSWER: MessageSegment[] = [{ type: "text", content: "轮次回答" }];

describe("resolveBranchAnchor", () => {
  it("空流：不产生锚点", () => {
    expect(resolveBranchAnchor(projectChatFlow([]), { isResponding: false })).toEqual(
      UNAVAILABLE,
    );
  });

  it("流式中：会话仍在输出（含等待审批）一律不可用", () => {
    const messages: ChatMessage[] = [
      userMessage(ANSWER),
      assistantMessage(ANSWER, { id: "assistant-live", streaming: true }),
    ];
    const items = projectChatFlow(messages, {
      streamingMessageId: "assistant-live",
    });

    expect(resolveBranchAnchor(items, { isResponding: true })).toEqual(UNAVAILABLE);
    // 等待审批：本轮未定型，同样是不可用（与流式同口径）。
    expect(
      resolveBranchAnchor(items, { isResponding: false, hasPendingApproval: true }),
    ).toEqual(UNAVAILABLE);
    // 实时边界：宿主漏报 isResponding 时，尾部 streaming 标记仍然兜住。
    expect(resolveBranchAnchor(items, { isResponding: false })).toEqual(UNAVAILABLE);
  });

  it("live-only / 中断消息在尾部：不锚定，也不回落到上一条已完成轮次", () => {
    // `history-artifacts.ts:33-41`：历史未覆盖的 live-only 消息追加在权威历史之后。
    const messages: ChatMessage[] = [
      userMessage(ANSWER),
      assistantMessage(ANSWER, { id: "assistant-done" }),
      assistantMessage([{ type: "text", content: "被我中止的半截回答" }], {
        id: "assistant-interrupted",
        interrupted: true,
      }),
    ];

    expect(
      resolveBranchAnchor(projectChatFlow(messages), { isResponding: false }),
    ).toEqual(UNAVAILABLE);
  });

  it("合成退化 id：`${sessionId}-history-${index}` 不产生锚点", () => {
    const messages: ChatMessage[] = [
      userMessage(ANSWER),
      assistantMessage(ANSWER, { id: "session-42-history-3" }),
    ];

    expect(
      resolveBranchAnchor(projectChatFlow(messages), { isResponding: false }),
    ).toEqual(UNAVAILABLE);
    expect(isSyntheticHistoryMessageId("msg_abc")).toBe(false);
    expect(isSyntheticHistoryMessageId("session-42-history-3")).toBe(true);
  });

  it("正常末尾：锚定末尾轮次的真实消息 id", () => {
    const messages: ChatMessage[] = [
      userMessage(ANSWER),
      assistantMessage(ANSWER, { id: "assistant-1" }),
      userMessage(ANSWER, { id: "user-2" }),
      assistantMessage(ANSWER, { id: "msg_runtime_9" }),
    ];

    expect(
      resolveBranchAnchor(projectChatFlow(messages), { isResponding: false }),
    ).toEqual({ kind: "available", messageId: "msg_runtime_9" });
  });

  it("末尾是尚未回答的用户消息：该轮未完成，不产生锚点", () => {
    const messages: ChatMessage[] = [
      userMessage(ANSWER),
      assistantMessage(ANSWER, { id: "assistant-1" }),
      userMessage(ANSWER, { id: "user-2" }),
    ];

    expect(
      resolveBranchAnchor(projectChatFlow(messages), { isResponding: false }),
    ).toEqual(UNAVAILABLE);
  });

  it("末尾是工具回执行：不是轮末尾，不产生锚点", () => {
    const messages: ChatMessage[] = [
      userMessage(ANSWER),
      assistantMessage(
        [
          {
            type: "tool",
            toolCallId: "call-1",
            name: "read_file",
            status: "finished",
            resultSummary: "42 行",
          },
        ],
        { id: "tool-1", label: "tool", author: "Tool receipt" },
      ),
    ];

    expect(
      resolveBranchAnchor(projectChatFlow(messages), { isResponding: false }),
    ).toEqual(UNAVAILABLE);
  });
});
