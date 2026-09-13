import { describe, expect, it } from "vitest";

import {
  normalizeMarkdown,
  parseStreamingCodeFence,
  parseStreamingPlainTail,
  parseStreamingStructuredTail,
  splitStreamingMarkdown,
} from "./message-markdown-streaming";

describe("message-markdown-streaming", () => {
  it("splits stable prose from a growing trailing paragraph", () => {
    const content = [
      "# Title",
      "",
      "Stable paragraph.",
      "",
      "Tail is still growing",
    ].join("\n");
    const parts = splitStreamingMarkdown(content);

    expect(parts).toMatchObject({
      stableContent: "# Title\n\n",
      tailContent: "Stable paragraph.\n\nTail is still growing",
      tailMode: "plain",
    });
    // 切割点逐字一致：两段切片拼回原文（无空洞、无重叠）。
    expect(parts.stableContent + parts.tailContent).toBe(content);
    expect(parts.stableEndOffset).toBe(parts.stableContent.length);
    expect(parts.totalOffset).toBe(content.length);
    expect(parts.stableBlocks.map((block) => [block.start, block.end])).toEqual([
      [0, 9],
    ]);
    expect(parts.tailBlocks.map((block) => [block.start, block.end])).toEqual([
      [9, 28],
      [28, 49],
    ]);
  });

  it("freezes everything before the unstable tail window and merges loose lists", () => {
    const content = [
      "Frozen paragraph.",
      "",
      "Loose list head.",
      "",
      "- first item",
      "",
      "- second item",
    ].join("\n");
    const parts = splitStreamingMarkdown(content);

    // 松散列表跨空行仍是同一个块（容器合并），因此「尾 2 块」只覆盖到列表头段落。
    expect(parts.stableContent).toBe("Frozen paragraph.\n\n");
    expect(parts.stableContent + parts.tailContent).toBe(content);
    expect(parts.tailBlocks.map((block) => block.container)).toEqual([
      "plain",
      "list",
    ]);
    expect(parts.tailContent.startsWith("Loose list head.")).toBe(true);
    expect(parts.tailMode).toBe("markdown");
  });

  it("keeps an unfinished fenced block in the active markdown tail", () => {
    const content = ["Intro", "", "```ts", "const answer = 42;"].join("\n");
    const parts = splitStreamingMarkdown(content);

    expect(parts).toMatchObject({
      stableContent: "Intro\n\n",
      tailContent: ["```ts", "const answer = 42;"].join("\n"),
      tailMode: "markdown",
    });
    expect(parts.stableContent + parts.tailContent).toBe(content);
    expect(parts.tailBlocks).toEqual([
      {
        container: "plain",
        end: content.length,
        start: content.indexOf("```ts"),
      },
    ]);
  });

  it("truncates a block that straddles an unfinished fence start", () => {
    const content = ["Intro line", "```ts", "const answer = 42;"].join("\n");
    const parts = splitStreamingMarkdown(content);

    // fence 可以紧跟段落行（CommonMark 允许 fence 中断段落）：段落被按 fence 起点
    // 截断后仍留在冻结区，既不丢文本也不影响第二前沿。
    expect(parts.stableContent).toBe("Intro line\n");
    expect(parts.tailContent).toBe(["```ts", "const answer = 42;"].join("\n"));
    expect(parts.stableContent + parts.tailContent).toBe(content);
    expect(parts.stableBlocks).toEqual([
      { container: "plain", end: "Intro line\n".length, start: 0 },
    ]);
  });

  it("treats structured trailing blocks as markdown after a stable boundary", () => {
    const content = ["Intro", "", "- first item", "- second item"].join("\n");
    const parts = splitStreamingMarkdown(content);

    // 只有 2 块时整段都还在不稳定窗口内，但首块已按绝对 offset 单独成片，
    // 渲染层不会随尾块每个 chunk 重解析它。
    expect(parts).toMatchObject({
      stableContent: "",
      tailContent: content,
      tailMode: "markdown",
    });
    expect(parts.tailBlocks.map((block) => [block.start, block.end])).toEqual([
      [0, 7],
      [7, content.length],
    ]);
  });

  it("closes unfinished tilde fences while streaming", () => {
    expect(
      normalizeMarkdown(["~~~json", '{"mode":"live"}'].join("\n"), true),
    ).toBe(["~~~json", '{"mode":"live"}', "~~~"].join("\n"));
  });

  it("parses unfinished streaming code fences into direct CodeBlock input", () => {
    expect(
      parseStreamingCodeFence(
        ["~~~typescript title=\"demo.ts\"", "const answer = 42;", ""].join("\n"),
      ),
    ).toEqual({
      code: "const answer = 42;",
      info: 'typescript title="demo.ts"',
      language: "typescript",
      marker: "~~~",
      // body 以换行结尾 → 无 partial 行，全部代码都是可缓存的高亮输入。
      partialLine: "",
      stableCode: "const answer = 42;\n",
    });
  });

  it("splits unfinished code fences into a stable prefix and a partial line", () => {
    expect(
      parseStreamingCodeFence(
        ["```ts", "const answer = 42;", "const pending = 1"].join("\n"),
      ),
    ).toMatchObject({
      code: ["const answer = 42;", "const pending = 1"].join("\n"),
      language: "ts",
      partialLine: "const pending = 1",
      stableCode: "const answer = 42;\n",
    });
  });

  it("parses streaming list tails into structured items", () => {
    expect(
      parseStreamingStructuredTail(
        ["3. first item", "4. second item with `code`"].join("\n"),
      ),
    ).toEqual({
      kind: "list",
      items: ["first item", "second item with `code`"],
      ordered: true,
      start: 3,
    });
  });

  it("parses streaming blockquote tails into paragraphs", () => {
    expect(
      parseStreamingStructuredTail(
        ["> quoted line", ">", "> second paragraph"].join("\n"),
      ),
    ).toEqual({
      kind: "blockquote",
      paragraphs: ["quoted line", "second paragraph"],
    });
  });

  it("parses streaming table tails into headers, alignment and rows", () => {
    expect(
      parseStreamingStructuredTail(
        [
          "| Name | Value |",
          "| :--- | ---: |",
          "| mode | live |",
          "| count | 2 |",
        ].join("\n"),
      ),
    ).toEqual({
      kind: "table",
      headers: ["Name", "Value"],
      alignments: ["left", "right"],
      rows: [
        ["mode", "live"],
        ["count", "2"],
      ],
    });
  });

  it("splits single-line plain tails at the last completed sentence", () => {
    expect(
      parseStreamingPlainTail(
        "Stable sentence. Active sentence is still growing",
      ),
    ).toEqual({
      activeText: "Active sentence is still growing",
      mode: "sentence",
      stableText: "Stable sentence. ",
    });
  });

  it("splits multiline plain tails at the final line", () => {
    expect(parseStreamingPlainTail("alpha line\nbeta line")).toEqual({
      activeText: "beta line",
      mode: "line",
      stableText: "alpha line\n",
    });
  });

  it("falls back to the final word group when no sentence boundary exists", () => {
    expect(
      parseStreamingPlainTail(
        "streaming text without punctuation but with enough words to split safely",
      ),
    ).toEqual({
      activeText: "safely",
      mode: "sentence",
      stableText: "streaming text without punctuation but with enough words to split ",
    });
  });
});
