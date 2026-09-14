// §12.1.4：消息级可见性判定的单元测试（空行收敛的单一判定源）。
import { describe, expect, it } from "vitest";

import { type ChatMessage } from "@/data/mock";

import { hasVisibleMessageContent, segmentHasVisibleContent } from "./message-visibility";

function message(overrides: Partial<ChatMessage>): ChatMessage {
  return {
    id: "message-1",
    role: "assistant",
    author: "Runtime stream",
    label: "streaming",
    segments: [],
    ...overrides,
  };
}

describe("hasVisibleMessageContent", () => {
  it("空文本段 / 空推理段不算可见（流式首块到达前的空壳）", () => {
    expect(
      hasVisibleMessageContent(
        message({
          segments: [
            { type: "text", content: "" },
            { type: "reasoning", content: "   " },
          ],
        }),
      ),
    ).toBe(false);
  });

  it("有正文或推理时算可见", () => {
    expect(
      hasVisibleMessageContent(
        message({ segments: [{ type: "text", content: "答案" }] }),
      ),
    ).toBe(true);
    expect(
      hasVisibleMessageContent(
        message({ segments: [{ type: "reasoning", content: "先看入口" }] }),
      ),
    ).toBe(true);
  });

  it("工具行 / 图片占位行自带状态，永远算可见", () => {
    expect(
      hasVisibleMessageContent(
        message({
          segments: [
            { type: "tool", name: "read_file", status: "finished" },
          ],
        }),
      ),
    ).toBe(true);
    expect(
      hasVisibleMessageContent(
        message({
          segments: [
            { type: "image-placeholder", imageId: "image-1", phase: "started" },
          ],
        }),
      ),
    ).toBe(true);
  });

  it("关联产物或回合用量命中时保留该消息行", () => {
    expect(hasVisibleMessageContent(message({ segments: [] }), 1)).toBe(true);
    expect(
      hasVisibleMessageContent(
        message({
          segments: [],
          usage: {
            promptTokens: 10,
            completionTokens: 20,
            totalTokens: 30,
          },
        }),
      ),
    ).toBe(true);
  });
});

describe("segmentHasVisibleContent", () => {
  it("空 callout / 空代码块不算可见，有内容才算", () => {
    expect(
      segmentHasVisibleContent({ type: "callout", title: "", content: " " }),
    ).toBe(false);
    expect(
      segmentHasVisibleContent({ type: "code", language: "ts", code: "" }),
    ).toBe(false);
    expect(
      segmentHasVisibleContent({
        type: "callout",
        title: "已停止",
        content: "",
      }),
    ).toBe(true);
  });
});
