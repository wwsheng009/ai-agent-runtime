/**
 * 批次 2（§5.4）验收：`resolveBranchAnchors` 的逐轮锚点结论。
 *
 * 核心回归：早期实现只取整条 transcript 的末尾（单锚点），导致「中间已完成的轮次」不可分支；
 * 现在按后端 `ListUserTurns` 的轮边界，每个已完成轮次的末条回答消息各自成锚点。
 *
 * 覆盖：
 * - 多轮各自成锚点 / 轮内中间消息不算锚点 / 末尾是未回答的用户消息；
 * - 空历史 / 流式中 / 等待审批 / live-only（流式、中断）消息在尾部；
 * - 合成退化 id / 纯工具回执行 / 历史上下文行 / 只有推理的消息（无入口）。
 */
import { describe, expect, it } from "vitest";

import { type ChatMessage, type MessageSegment } from "@/data/mock";

import {
  isSyntheticHistoryMessageId,
  resolveBranchAnchors,
} from "./branch-availability";

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
const REASONING_ONLY: MessageSegment[] = [
  { type: "reasoning", content: "先盘点入口文件" },
];
const TOOL_ONLY: MessageSegment[] = [
  {
    type: "tool",
    toolCallId: "call-1",
    name: "read_file",
    status: "finished",
    resultSummary: "42 行",
  },
];

function anchorsOf(messages: ChatMessage[], isResponding = false): string[] {
  return [...resolveBranchAnchors(messages, { isResponding })].sort();
}

describe("resolveBranchAnchors", () => {
  it("空历史：不产生锚点", () => {
    expect(anchorsOf([])).toEqual([]);
  });

  it("流式中 / 等待审批：会话仍在演进，一律不可用", () => {
    const messages: ChatMessage[] = [
      userMessage(ANSWER),
      assistantMessage(ANSWER, { id: "assistant-done" }),
      userMessage(ANSWER, { id: "user-2" }),
      assistantMessage(ANSWER, { id: "assistant-live", streaming: true }),
    ];

    expect(anchorsOf(messages, true)).toEqual([]);
    expect(
      resolveBranchAnchors(messages, {
        isResponding: false,
        hasPendingApproval: true,
      }).size,
    ).toBe(0);
  });

  it("多轮已完成：每个轮末尾各自成锚点（本次修复的核心）", () => {
    const messages: ChatMessage[] = [
      userMessage(ANSWER, { id: "user-1" }),
      assistantMessage(ANSWER, { id: "assistant-1" }),
      userMessage(ANSWER, { id: "user-2" }),
      assistantMessage(ANSWER, { id: "assistant-2" }),
      userMessage(ANSWER, { id: "user-3" }),
      assistantMessage(ANSWER, { id: "assistant-3" }),
    ];

    expect(anchorsOf(messages)).toEqual([
      "assistant-1",
      "assistant-2",
      "assistant-3",
    ]);
  });

  it("轮内中间消息不是锚点：其后还有本轮的其它消息", () => {
    const messages: ChatMessage[] = [
      userMessage(ANSWER, { id: "user-1" }),
      assistantMessage(ANSWER, { id: "assistant-mid" }),
      assistantMessage(ANSWER, { id: "assistant-tail" }),
    ];

    expect(anchorsOf(messages)).toEqual(["assistant-tail"]);
  });

  it("工具调用后才有最终回答：只有最终回答是轮末尾", () => {
    // 真实历史顺序：user → assistant(tool_calls) → tool 回执 → assistant(回答)。
    const messages: ChatMessage[] = [
      userMessage(ANSWER, { id: "user-1" }),
      assistantMessage(TOOL_ONLY, { id: "assistant-tool-calls" }),
      assistantMessage(TOOL_ONLY, {
        id: "tool-1",
        author: "Tool receipt",
        label: "tool",
      }),
      assistantMessage(ANSWER, { id: "assistant-final" }),
    ];

    expect(anchorsOf(messages)).toEqual(["assistant-final"]);
  });

  it("末尾是尚未回答的用户消息：该轮无锚点，但不影响上一轮已完成的锚点", () => {
    const messages: ChatMessage[] = [
      userMessage(ANSWER, { id: "user-1" }),
      assistantMessage(ANSWER, { id: "assistant-1" }),
      userMessage(ANSWER, { id: "user-2" }),
    ];

    expect(anchorsOf(messages)).toEqual(["assistant-1"]);
  });

  it("末尾是工具回执行：该轮未收尾，整轮不产生锚点", () => {
    const messages: ChatMessage[] = [
      userMessage(ANSWER, { id: "user-1" }),
      assistantMessage(ANSWER, { id: "assistant-1" }),
      assistantMessage(TOOL_ONLY, {
        id: "tool-1",
        author: "Tool receipt",
        label: "tool",
      }),
    ];

    expect(anchorsOf(messages)).toEqual([]);
  });

  it("live-only 边界：流式 / 中断消息自身不作锚点，但已完成轮次仍可分支", () => {
    // `history-artifacts.ts:33-41`：历史未覆盖的 live-only 消息追加在权威历史之后。
    const messages: ChatMessage[] = [
      userMessage(ANSWER, { id: "user-1" }),
      assistantMessage(ANSWER, { id: "assistant-done" }),
      userMessage(ANSWER, { id: "user-2" }),
      assistantMessage([{ type: "text", content: "被我中止的半截回答" }], {
        id: "assistant-interrupted",
        interrupted: true,
      }),
    ];

    expect(anchorsOf(messages)).toEqual(["assistant-done"]);
  });

  it("只有推理的消息：即使位于轮末尾也不产生锚点（按钮不出现）", () => {
    const messages: ChatMessage[] = [
      userMessage(ANSWER, { id: "user-1" }),
      assistantMessage(ANSWER, { id: "assistant-1" }),
      userMessage(ANSWER, { id: "user-2" }),
      assistantMessage(REASONING_ONLY, { id: "assistant-reasoning" }),
    ];

    expect(anchorsOf(messages)).toEqual(["assistant-1"]);
  });

  it("只有工具段的回答：末尾也不产生锚点（无可复现正文）", () => {
    const messages: ChatMessage[] = [
      userMessage(ANSWER, { id: "user-1" }),
      assistantMessage(TOOL_ONLY, { id: "assistant-tool-only" }),
    ];

    expect(anchorsOf(messages)).toEqual([]);
  });

  it("历史上下文行位于末尾：该轮无锚点（不产出 turn-tail）", () => {
    const messages: ChatMessage[] = [
      userMessage(ANSWER, { id: "user-1" }),
      assistantMessage(ANSWER, { id: "assistant-1" }),
      assistantMessage(ANSWER, {
        id: "context-1",
        author: "Context",
        label: "context",
      }),
    ];

    expect(anchorsOf(messages)).toEqual([]);
  });

  it("合成退化 id：`${sessionId}-history-${index}` 不产生锚点", () => {
    const messages: ChatMessage[] = [
      userMessage(ANSWER, { id: "user-1" }),
      assistantMessage(ANSWER, { id: "session-42-history-3" }),
    ];

    expect(anchorsOf(messages)).toEqual([]);
    expect(isSyntheticHistoryMessageId("msg_abc")).toBe(false);
    expect(isSyntheticHistoryMessageId("session-42-history-3")).toBe(true);
  });
});
