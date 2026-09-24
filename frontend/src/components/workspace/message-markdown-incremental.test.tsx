// P0-2 拆分：MessageMarkdown 组件级流式增量渲染回归。
// 由 message-markdown.test.tsx（原 L303-L467）下沉：冻结前缀 memo、改写重建、
// 块重解析次数上界、settled 后与一次性全量解析逐字节一致。

import { act } from "react";
import { createRoot } from "react-dom/client";
import { beforeAll, describe, expect, it, vi } from "vitest";

import { codeHighlightingReady } from "../ui/code-highlighting";
import { MessageMarkdown } from "./message-markdown";
import * as streamingModule from "./message-markdown-streaming";

describe("MessageMarkdown incremental streaming", () => {
  beforeAll(async () => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
    await codeHighlightingReady;
  });

  it("keeps frozen stable content rendered across streaming appends", async () => {
    const container = document.createElement("div");
    const root = createRoot(container);

    await act(async () => {
      root.render(
        <MessageMarkdown
          content={"First paragraph.\n\nSecond paragraph."}
          streaming
        />,
      );
    });
    await act(async () => {
      root.render(
        <MessageMarkdown
          content={"First paragraph.\n\nSecond paragraph.\n\nThird paragraph."}
          streaming
        />,
      );
    });

    const markup = container.innerHTML;
    expect(markup).toContain("First paragraph.");
    expect(markup).toContain("Second paragraph.");
    expect(markup).toContain("Third paragraph.");
    await act(async () => {
      root.unmount();
    });
  });

  it("rebuilds stable content when the streamed text is rewritten", async () => {
    const container = document.createElement("div");
    const root = createRoot(container);

    await act(async () => {
      root.render(
        <MessageMarkdown
          content={"Old opening.\n\nOld second block."}
          streaming
        />,
      );
    });
    await act(async () => {
      root.render(
        <MessageMarkdown
          content={"New opening.\n\nNew second block."}
          streaming
        />,
      );
    });

    const markup = container.innerHTML;
    expect(markup).toContain("New opening.");
    expect(markup).toContain("New second block.");
    expect(markup).not.toContain("Old opening.");
    await act(async () => {
      root.unmount();
    });
  });

  it("keeps block re-parses at a constant bound while streaming many chunks", async () => {
    const paragraphs = Array.from(
      { length: 24 },
      (_, index) => `Paragraph ${index} carries a body sentence.`,
    );
    const content = paragraphs.join("\n\n");

    const streamInChunks = async (chunkCount: number) => {
      const container = document.createElement("div");
      const root = createRoot(container);
      const step = Math.ceil(content.length / chunkCount);
      // `normalizeMarkdown` 是每次块解析的必经入口（冻结片段 + 尾块 markdown 路径），
      // 因此它被调用的次数就是「块重解析次数」的计数器。
      const parseSpy = vi.spyOn(streamingModule, "normalizeMarkdown");

      await act(async () => {
        root.render(<MessageMarkdown content="" streaming />);
      });
      for (let end = step; end < content.length; end += step) {
        await act(async () => {
          root.render(
            <MessageMarkdown content={content.slice(0, end)} streaming />,
          );
        });
      }
      await act(async () => {
        root.render(<MessageMarkdown content={content} streaming />);
      });

      const parses = parseSpy.mock.calls.length;
      const markup = container.innerHTML;
      parseSpy.mockRestore();
      await act(async () => {
        root.unmount();
      });
      return { markup, parses };
    };

    const coarse = await streamInChunks(4);
    const fine = await streamInChunks(48);

    // 冻结前缀按块 memo：每块恰好解析一次，且与 chunk 数无关（实测 4 chunk 与
    // 48 chunk 同为 23 次，24 块中尾块走 plain 路径不计）。若每 chunk 重解析整段
    // 前缀，这一数字会随 chunk 数线性增长。
    expect(fine.parses).toBe(coarse.parses);
    expect(fine.parses).toBeLessThanOrEqual(paragraphs.length);
    expect(fine.markup).toContain("Paragraph 0 carries a body sentence.");
    expect(fine.markup).toContain("Paragraph 23 carries a body sentence.");
  });

  it("settles into the same DOM as a one-shot full parse", async () => {
    const content = [
      "# Streaming title",
      "",
      "Intro paragraph with [docs](https://example.com/docs).",
      "",
      "- first item",
      "",
      "- second item",
      "",
      "```ts",
      "const answer = 42;",
      "```",
      "",
      "![shot](https://cdn.example.com/shot.png)",
    ].join("\n");

    const container = document.createElement("div");
    const root = createRoot(container);
    const step = Math.ceil(content.length / 12);
    await act(async () => {
      root.render(<MessageMarkdown content="" streaming />);
    });
    for (let end = step; end < content.length; end += step) {
      await act(async () => {
        root.render(
          <MessageMarkdown content={content.slice(0, end)} streaming />,
        );
      });
    }
    await act(async () => {
      root.render(<MessageMarkdown content={content} streaming />);
    });
    // 结算：同一棵树从流式态切到 settled，块切分与占位（链接/图片/代码）都必须自愈。
    await act(async () => {
      root.render(<MessageMarkdown content={content} />);
    });
    const incrementalMarkup = container.innerHTML;
    await act(async () => {
      root.unmount();
    });

    const oneShotContainer = document.createElement("div");
    const oneShotRoot = createRoot(oneShotContainer);
    await act(async () => {
      oneShotRoot.render(<MessageMarkdown content={content} />);
    });
    const oneShotMarkup = oneShotContainer.innerHTML;
    await act(async () => {
      oneShotRoot.unmount();
    });

    // 增量渲染是纯优化：settled 后与一次性全量解析逐字节一致（无残留占位 / 无丢块）。
    expect(incrementalMarkup).toBe(oneShotMarkup);
  });
});
