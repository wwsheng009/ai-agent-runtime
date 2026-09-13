// P1-1 验收：组件层折叠结果覆盖四类 Turn（工具后跟文本 / 无 final answer / 纯工具 / 重试行）。
// 断言写在语义层（可见文本、aria 状态），不依赖 DOM 基数或实现细节。

import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import type { Artifact, ChatMessage, MessageSegment } from "@/data/mock";

import { AssistantMessageCard } from "./assistant-message-card";

function assistantMessage(segments: MessageSegment[]): ChatMessage {
  return {
    id: "assistant-collapse",
    role: "assistant",
    author: "Runtime",
    label: "response",
    segments,
  };
}

function renderCard(message: ChatMessage, streamingMessageId: string | null = null) {
  const relatedEvidence: Artifact[] = [];
  return renderToStaticMarkup(
    <AssistantMessageCard
      labelId={`${message.id}-label`}
      message={message}
      metaId={`${message.id}-meta`}
      onSelectArtifact={() => {}}
      relatedEvidence={relatedEvidence}
      statusId={`${message.id}-status`}
      streamingMessageId={streamingMessageId}
    />,
  );
}

describe("AssistantMessageCard 折叠渲染", () => {
  it("工具调用后跟文本：折叠过程证据，只保留最终回答与摘要行", () => {
    const markup = renderCard(
      assistantMessage([
        { type: "reasoning", content: "先盘点入口文件" },
        {
          type: "tool",
          toolCallId: "call-1",
          name: "read_file",
          status: "finished",
          argsSummary: "src/index.ts",
          resultSummary: "42 行",
        },
        { type: "text", content: "结论：入口文件共 42 行。" },
      ]),
    );

    expect(markup).toContain("1 个工具调用 · 1 条带回复");
    expect(markup).toContain("展开过程证据");
    expect(markup).toContain('aria-expanded="false"');
    expect(markup).toContain("结论：入口文件共 42 行。");
    expect(markup).not.toContain("read_file");
    expect(markup).not.toContain("先盘点入口文件");
  });

  it("无 final answer 的已结束 Turn：保全过程可见且不显示摘要行", () => {
    const markup = renderCard(
      assistantMessage([
        { type: "reasoning", content: "先检查测试失败原因" },
        { type: "tool", name: "run_tests", status: "error", errorMessage: "1 failed" },
      ]),
    );

    expect(markup).toContain("推理过程");
    expect(markup).toContain("run_tests");
    expect(markup).not.toContain("个工具调用");
    expect(markup).not.toContain("展开过程证据");
  });

  it("纯工具 Turn：不折叠，工具行全部可见", () => {
    const markup = renderCard(
      assistantMessage([
        { type: "tool", name: "read_file", status: "finished", resultSummary: "ok" },
        { type: "tool", name: "edit_file", status: "finished", resultSummary: "ok" },
      ]),
    );

    expect(markup).toContain("read_file");
    expect(markup).toContain("edit_file");
    expect(markup).not.toContain("展开过程证据");
  });

  it("重试行：两次工具调用合并进摘要，工具行不再可见", () => {
    const markup = renderCard(
      assistantMessage([
        { type: "tool", name: "read_file", status: "error", errorMessage: "EACCES" },
        { type: "tool", name: "read_file", status: "finished", resultSummary: "ok" },
        { type: "text", content: "重试后已读取成功。" },
      ]),
    );

    expect(markup).toContain("2 个工具调用 · 2 条带回复");
    expect(markup).toContain("重试后已读取成功。");
    expect(markup).not.toContain("read_file");
  });

  it("摘要行省略零值段：无回复的工具调用只显示工具数", () => {
    const markup = renderCard(
      assistantMessage([
        { type: "tool", name: "write_file", status: "started" },
        { type: "text", content: "已开始写入。" },
      ]),
    );

    expect(markup).toContain("1 个工具调用");
    expect(markup).not.toContain("条带回复");
    expect(markup).not.toContain("个子代理");
  });

  it("同一 Turn 追加新文本后折叠结果稳定：摘要行仍为一处", () => {
    const segments: MessageSegment[] = [
      { type: "tool", name: "read_file", status: "finished", resultSummary: "ok" },
      { type: "text", content: "结论如下。" },
    ];

    const before = renderCard(assistantMessage(segments));
    const after = renderCard(
      assistantMessage([...segments, { type: "text", content: "补充一句。" }]),
    );

    expect(before).toContain("1 个工具调用 · 1 条带回复");
    expect(after).toContain("1 个工具调用 · 1 条带回复");
    expect(after.split("展开过程证据")).toHaveLength(2);
    expect(after).toContain("结论如下。");
    expect(after).toContain("补充一句。");
    expect(after).not.toContain("read_file");
  });

  it("流式中的 Turn 不折叠（过程可见）", () => {
    const message = assistantMessage([
      { type: "reasoning", content: "正在读取入口文件" },
      { type: "text", content: "正在生成回答" },
    ]);

    const markup = renderCard(message, message.id);

    expect(markup).toContain("推理过程");
    expect(markup).not.toContain("展开过程证据");
  });

  it("完整用量：渲染 Token 用量行", () => {
    const message = assistantMessage([{ type: "text", content: "结论如下。" }]);
    message.usage = {
      promptTokens: 1200,
      completionTokens: 300,
      totalTokens: 1500,
    };

    const markup = renderCard(message);

    expect(markup).toContain("1,200");
    expect(markup).toContain("1,500");
  });

  it("用量不完整：整行隐藏，不显示半截数字", () => {
    const message = assistantMessage([{ type: "text", content: "结论如下。" }]);
    Object.assign(message, { usage: { promptTokens: 1200 } });

    const markup = renderCard(message);

    expect(markup).toContain("结论如下。");
    expect(markup).not.toContain("1,200");
    expect(markup).not.toContain("Token");
  });
});
