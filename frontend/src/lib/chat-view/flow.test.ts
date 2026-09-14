/**
 * E1（§8.4）验收：`ChatMessage[] → ChatFlowItem[]` 扁平 flow 投影。
 *
 * 覆盖点：
 * - 10 类 kind 全部可达（`user` / `steering` / `system-prompt` / `context` /
 *   `assistant-step` / `tool-call` / `notice` / `turn-process` / `turn-tail` / `fallback`）；
 * - 收起态过程行不产出（§13 C2），展开后回归且 key 稳定；
 * - 流式回合恒不折叠；`system-prompt` 与 `context` 分行；
 * - 未知类型降级为 `fallback`（不抛错、不吞事件、保留原始载荷）。
 */
import { describe, expect, it } from "vitest";

import type { ChatMessage, MessageSegment } from "@/data/mock";

import { flowAnchor, projectChatFlow, projectMessageFlow } from "./flow";

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

describe("projectMessageFlow", () => {
  it("user：一条消息 → 一个 user item，anchorKey = message.id", () => {
    const items = projectMessageFlow(
      userMessage([{ type: "text", content: "帮我看看入口文件" }]),
    );

    expect(items).toHaveLength(1);
    expect(items[0].kind).toBe("user");
    expect(items[0].anchorKey).toBe("user-1");
    expect(items[0].key.startsWith("user:")).toBe(true);
  });

  it("steering：label 为 steering 时走独立 kind（与 user 同形制）", () => {
    const items = projectMessageFlow(
      userMessage([{ type: "text", content: "改用方案 B" }], {
        label: "steering",
      }),
    );

    expect(items.map((item) => item.kind)).toEqual(["steering"]);
    expect(items[0].key.startsWith("steering:")).toBe(true);
  });

  it("工具回执：含 tool 段时投影为 tool-call（不再并入 context）", () => {
    const items = projectMessageFlow(
      assistantMessage(
        [
          {
            type: "tool",
            toolCallId: "call-1",
            name: "read_file",
            status: "finished",
            argsSummary: '{"file_path":"src/a.ts"}',
            resultSummary: "42 行",
          },
        ],
        { id: "tool-1", label: "tool", author: "Tool receipt" },
      ),
    );

    expect(items.map((item) => item.kind)).toEqual(["tool-call"]);
    expect(items[0].key.startsWith("tool-call:")).toBe(true);
    expect(items[0].anchorKey).toBe("tool-1");
    expect(
      items[0].kind === "tool-call" ? items[0].node.kind : "",
    ).toBe("tool");
  });

  it("system-prompt 与降级 context 分行：一条消息只产出各自 kind，不合流", () => {
    const systemItems = projectMessageFlow(
      assistantMessage([{ type: "text", content: "You are a runtime agent." }], {
        id: "system-1",
        label: "system",
        author: "System context",
      }),
    );
    const contextItems = projectMessageFlow(
      assistantMessage([{ type: "text", content: "# AGENTS.md\n项目约定…" }], {
        id: "context-1",
        label: "tool",
        author: "Tool receipt",
      }),
    );

    expect(systemItems.map((item) => item.kind)).toEqual(["system-prompt"]);
    expect(contextItems.map((item) => item.kind)).toEqual(["context"]);
    expect(systemItems[0].key.startsWith("system-prompt:")).toBe(true);
    expect(contextItems[0].key.startsWith("context:")).toBe(true);
  });

  it("降级 context：无 tool 段的历史回执取首行非空文本并剥离标题标记", () => {
    const [item] = projectMessageFlow(
      assistantMessage(
        [
          { type: "text", content: "\n# AGENTS.md\n项目约定…" },
          { type: "code", language: "ts", code: "export {};" },
        ],
        { id: "context-2", label: "tool", author: "Tool receipt" },
      ),
    );

    expect(item.kind).toBe("context");
    expect(item.kind === "context" ? item.source : "").toBe("AGENTS.md");
    expect(item.kind === "context" ? item.title : "").toBe("tool");
    expect(item.kind === "context" ? item.nodes.length : 0).toBe(2);
  });

  it("assistant：正文 / 推理 / 工具逐段投影，顺序与消息一致", () => {
    const items = projectMessageFlow(
      assistantMessage([
        { type: "reasoning", content: "先盘点入口文件" },
        {
          type: "tool",
          toolCallId: "call-1",
          name: "read_file",
          status: "finished",
          resultSummary: "42 行",
        },
        { type: "text", content: "结论：入口文件共 42 行。" },
      ]),
      { expandedMessageIds: ["assistant-1"] },
    );

    // 收起态统计行在最前，展开后过程行回到序列，尾部固定 turn-tail。
    expect(items[0].kind).toBe("turn-process");
    expect(items[items.length - 1].kind).toBe("turn-tail");
    expect(items.map((item) => item.kind)).toEqual([
      "turn-process",
      "assistant-step",
      "tool-call",
      "assistant-step",
      "turn-tail",
    ]);
    expect(
      items
        .filter((item) => item.kind === "tool-call")
        .every((item) => item.kind === "tool-call" && item.node.kind === "tool"),
    ).toBe(true);
  });

  it("turn-process：统计口径来自折叠摘要，全 0 时 summary.empty", () => {
    const items = projectMessageFlow(
      assistantMessage([
        {
          type: "tool",
          name: "read_file",
          status: "finished",
          resultSummary: "ok",
        },
        { type: "text", content: "结论如下。" },
      ]),
      { subagentCountByMessage: { "assistant-1": 2 } },
    );

    const process = items.find((item) => item.kind === "turn-process");

    expect(process?.kind === "turn-process" ? process.stats : null).toEqual({
      toolCalls: 1,
      messages: 1,
      subagents: 2,
    });
  });

  it("收起态：过程行不产出（§13 C2），只保留统计行 + 最终回答 + 尾行", () => {
    const message = assistantMessage([
      { type: "reasoning", content: "think" },
      { type: "tool", name: "read", status: "finished", resultSummary: "ok" },
      { type: "text", content: "Final answer" },
    ]);

    const collapsed = projectMessageFlow(message);
    const expanded = projectMessageFlow(message, {
      expandedMessageIds: [message.id],
    });

    expect(collapsed.map((item) => item.kind)).toEqual([
      "turn-process",
      "assistant-step",
      "turn-tail",
    ]);
    expect(expanded.map((item) => item.kind)).toEqual([
      "turn-process",
      "assistant-step",
      "tool-call",
      "assistant-step",
      "turn-tail",
    ]);
  });

  it("展开不改变既有 item key（回合内追加新段亦稳定）", () => {
    const base = assistantMessage([
      { type: "reasoning", content: "think" },
      { type: "tool", name: "read", status: "finished", resultSummary: "ok" },
      { type: "text", content: "Final answer" },
    ]);
    const before = projectMessageFlow(base, {
      expandedMessageIds: [base.id],
    }).map((item) => item.key);
    const after = projectMessageFlow(
      assistantMessage([...base.segments, { type: "text", content: "补充一句。" }]),
      { expandedMessageIds: [base.id] },
    ).map((item) => item.key);

    expect(after.slice(0, before.length - 1)).toEqual(before.slice(0, -1));
    expect(new Set(after).size).toBe(after.length);
  });

  it("流式回合恒不折叠：过程行全部可见，且不产出 turn-process", () => {
    const message = assistantMessage([
      { type: "reasoning", content: "正在读取入口文件" },
      { type: "text", content: "正在生成回答" },
    ]);
    const items = projectMessageFlow(message, {
      streamingMessageId: message.id,
    });

    expect(items.map((item) => item.kind)).toEqual([
      "assistant-step",
      "assistant-step",
      "turn-tail",
    ]);
  });

  it("callout → notice：warning 归 warn，其余归 info（success 不单独占色）", () => {
    const [warnItem] = projectMessageFlow(
      assistantMessage([
        { type: "callout", title: "截断", content: "达到最大 token", tone: "warning" },
      ]),
    );
    const [infoItem] = projectMessageFlow(
      assistantMessage([
        { type: "callout", title: "提示", content: "已完成", tone: "success" },
      ]),
    );

    expect(warnItem.kind === "notice" ? warnItem.tone : null).toBe("warn");
    expect(infoItem.kind === "notice" ? infoItem.tone : null).toBe("info");
  });

  it("未知类型：降级为 fallback 并保留原始载荷（不抛错、不吞事件）", () => {
    const unknown = {
      type: "future-event-kind",
      payload: { nested: true },
    } as unknown as MessageSegment;

    const items = projectMessageFlow(assistantMessage([unknown]));

    expect(items.map((item) => item.kind)).toEqual(["fallback", "turn-tail"]);
    expect(items[0].kind === "fallback" ? items[0].raw : null).toBe(unknown);
  });

  it("projectChatFlow：多条消息按序铺平为同级 item 序列", () => {
    const items = projectChatFlow([
      userMessage([{ type: "text", content: "你好" }], { id: "u1" }),
      assistantMessage([{ type: "text", content: "你好，有什么可以帮你？" }], {
        id: "a1",
      }),
    ]);

    expect(items.map((item) => item.kind)).toEqual([
      "user",
      "assistant-step",
      "turn-tail",
    ]);
    expect(items.map((item) => item.anchorKey)).toEqual(["u1", "a1", "a1"]);
  });

  it("flowAnchor：DOM 锚点三属性与 item 一一对应", () => {
    const [item] = projectMessageFlow(
      userMessage([{ type: "text", content: "你好" }]),
    );

    expect(flowAnchor(item)).toEqual({
      "data-chat-anchor-key": "user-1",
      "data-chat-flow-key": item.key,
      "data-chat-flow-kind": "user",
    });
  });
});
