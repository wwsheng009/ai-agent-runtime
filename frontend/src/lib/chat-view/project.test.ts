import { describe, expect, it } from "vitest";

import type { ChatMessage, MessageSegment } from "@/data/mock";

import { buildNodes } from "./nodes";
import { isSystemPromptMessage, projectChatView } from "./project";

function assistantMessage(
  segments: MessageSegment[],
  overrides: Partial<ChatMessage> = {},
): ChatMessage {
  return {
    id: "assistant-1",
    role: "assistant",
    author: "Runtime",
    label: "response",
    segments,
    ...overrides,
  };
}

describe("projectChatView", () => {
  it("已结束的 Turn：折叠过程证据，仅保留最终回答", () => {
    const view = projectChatView(
      assistantMessage([
        { type: "reasoning", content: "think" },
        {
          type: "tool",
          name: "read",
          status: "finished",
          resultSummary: "ok",
        },
        { type: "text", content: "Final answer" },
      ]),
    );

    expect(view.collapsed).toBe(true);
    expect(view.nodes.map((node) => node.segment.type)).toEqual(["text"]);
    expect(view.hiddenNodes.map((node) => node.segment.type)).toEqual([
      "reasoning",
      "tool",
    ]);
    expect(view.summary?.tools).toBe(1);
    expect(view.summary?.replies).toBe(1);
    expect(view.finalAnswerStart).toBe(2);
  });

  it("流式中的 Turn 不折叠（过程可见）", () => {
    const view = projectChatView(
      assistantMessage([
        { type: "reasoning", content: "think" },
        { type: "text", content: "partial" },
      ]),
      { streaming: true },
    );

    expect(view.collapsed).toBe(false);
    expect(view.nodes).toHaveLength(2);
    expect(view.hiddenNodes).toEqual([]);
    expect(view.summary).toBeNull();
  });

  it("无最终回答的已结束 Turn：保全过程可见", () => {
    const view = projectChatView(
      assistantMessage([
        { type: "reasoning", content: "think" },
        { type: "tool", name: "grep", status: "running" },
      ]),
    );

    expect(view.collapsed).toBe(false);
    expect(view.nodes).toHaveLength(2);
    expect(view.finalAnswerStart).toBe(-1);
  });

  it("纯工具 Turn 不折叠", () => {
    const view = projectChatView(
      assistantMessage([
        { type: "tool", name: "read", status: "finished", resultSummary: "ok" },
        { type: "tool", name: "edit", status: "finished", resultSummary: "ok" },
      ]),
    );

    expect(view.collapsed).toBe(false);
    expect(view.nodes).toHaveLength(2);
  });

  it("重试行：两次工具调用合并进摘要，可见行稳定为一", () => {
    const view = projectChatView(
      assistantMessage([
        { type: "tool", name: "read", status: "error", errorMessage: "EACCES" },
        { type: "tool", name: "read", status: "finished", resultSummary: "ok" },
        { type: "text", content: "Recovered" },
      ]),
    );

    expect(view.collapsed).toBe(true);
    expect(view.nodes).toHaveLength(1);
    expect(view.summary?.tools).toBe(2);
    expect(view.summary?.replies).toBe(2);
    expect(view.hiddenNodes.map((node) => node.key)).toEqual([
      "assistant-1:tool:0",
      "assistant-1:tool:1",
    ]);
  });

  it("追加后续段不改变既有 node key，且最终回答边界保持", () => {
    const base: MessageSegment[] = [
      { type: "reasoning", content: "think" },
      { type: "tool", name: "read", status: "finished", resultSummary: "ok" },
      { type: "text", content: "Answer" },
    ];
    const extra: MessageSegment = { type: "text", content: "more" };

    const before = buildNodes("assistant-1", base);
    const after = buildNodes("assistant-1", [...base, extra]);

    expect(after.slice(0, before.length).map((node) => node.key)).toEqual(
      before.map((node) => node.key),
    );
    expect(projectChatView(assistantMessage([...base, extra])).finalAnswerStart).toBe(2);
  });

  it("无过程证据的纯回答不折叠", () => {
    const view = projectChatView(
      assistantMessage([{ type: "text", content: "Just an answer" }]),
    );

    expect(view.collapsed).toBe(false);
    expect(view.nodes).toHaveLength(1);
    expect(view.finalAnswerStart).toBe(0);
  });

  it("手动展开的已结束 Turn 不折叠", () => {
    const view = projectChatView(
      assistantMessage([
        { type: "tool", name: "read", status: "finished", resultSummary: "ok" },
        { type: "text", content: "Answer" },
      ]),
      { expanded: true },
    );

    expect(view.collapsed).toBe(false);
    expect(view.nodes).toHaveLength(2);
    expect(view.hiddenNodes).toEqual([]);
    expect(view.summary).toBeNull();
  });

  it("system/message 默认折叠为 System prompt 行，可展开", () => {
    const message = assistantMessage(
      [{ type: "text", content: "You are a helpful agent." }],
      { label: "system", author: "System context" },
    );

    expect(isSystemPromptMessage(message)).toBe(true);

    const collapsed = projectChatView(message);
    expect(collapsed.systemPrompt).toBe(true);
    expect(collapsed.collapsed).toBe(true);
    expect(collapsed.nodes).toEqual([]);
    expect(collapsed.hiddenNodes).toHaveLength(1);
    expect(collapsed.summary).toBeNull();

    const expanded = projectChatView(message, { expanded: true });
    expect(expanded.collapsed).toBe(false);
    expect(expanded.nodes).toHaveLength(1);
  });
});
