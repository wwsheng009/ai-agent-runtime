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
    const parts = splitStreamingMarkdown(
      ["# Title", "", "Stable paragraph.", "", "Tail is still growing"].join("\n"),
    );

    expect(parts).toEqual({
      stableContent: ["# Title", "", "Stable paragraph."].join("\n"),
      tailContent: "Tail is still growing",
      tailMode: "plain",
    });
  });

  it("keeps an unfinished fenced block in the active markdown tail", () => {
    const parts = splitStreamingMarkdown(
      ["Intro", "", "```ts", "const answer = 42;"].join("\n"),
    );

    expect(parts).toEqual({
      stableContent: "Intro",
      tailContent: ["```ts", "const answer = 42;"].join("\n"),
      tailMode: "markdown",
    });
  });

  it("treats structured trailing blocks as markdown after a stable boundary", () => {
    const parts = splitStreamingMarkdown(
      ["Intro", "", "- first item", "- second item"].join("\n"),
    );

    expect(parts).toEqual({
      stableContent: "Intro",
      tailContent: ["- first item", "- second item"].join("\n"),
      tailMode: "markdown",
    });
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
