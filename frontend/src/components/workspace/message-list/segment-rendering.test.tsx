// §12.1.4 验收：空文本段不生成行节点。
// 空 flex item 自身高度为 0，但仍会吃掉父级 `gap-2`，在相邻行之间撑出空行；
// 因此渲染层必须直接不产出节点（而不是渲染成空 div）。

import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { type MessageSegment } from "@/data/mock";

import { renderMessageSegment } from "./segment-rendering";

function renderSegment(segment: MessageSegment) {
  return renderToStaticMarkup(<>{renderMessageSegment(segment, { flowKey: "k" })}</>);
}

describe("renderMessageSegment 空内容不占位", () => {
  it("空文本段 / 纯空白文本段：不产出任何节点", () => {
    expect(renderSegment({ type: "text", content: "" })).toBe("");
    expect(renderSegment({ type: "text", content: "   \n\t " })).toBe("");
  });

  it("有正文的文本段：正常产出（行锚点保留）", () => {
    const markup = renderSegment({ type: "text", content: "结论：42 行。" });

    expect(markup).toContain("结论：42 行。");
    expect(markup).toContain('data-chat-flow-key="k"');
  });
});
