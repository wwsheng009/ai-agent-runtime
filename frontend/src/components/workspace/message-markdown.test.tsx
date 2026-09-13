import { renderToStaticMarkup } from "react-dom/server";
import { createRoot } from "react-dom/client";
import { act } from "react";
import { beforeAll, describe, expect, it } from "vitest";

import { codeHighlightingReady } from "../ui/code-highlighting";
import { MessageMarkdown } from "./message-markdown";

describe("MessageMarkdown", () => {
  beforeAll(async () => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
    await codeHighlightingReady;
  });

  it("renders markdown links, lists, and tables", () => {
    const markup = renderToStaticMarkup(
      <MessageMarkdown
        content={[
          "# Summary",
          "",
          "- first item",
          "- second item",
          "",
          "| Name | Value |",
          "| --- | --- |",
          "| mode | live |",
          "",
          "[Docs](https://example.com/docs)",
        ].join("\n")}
      />,
    );

    expect(markup).toContain("<ul");
    expect(markup).toContain("<table");
    expect(markup).toContain('href="https://example.com/docs"');
    expect(markup).toContain('target="_blank"');
  });

  it("keeps unfinished fenced blocks renderable while streaming", () => {
    const markup = renderToStaticMarkup(
      <MessageMarkdown
        content={["```ts", "const answer = 42;"].join("\n")}
        streaming
      />,
    );

    expect(markup).toContain("Streaming ts");
    expect(markup).toContain('aria-busy="true"');
    expect(markup).toContain('class="token keyword"');
    expect(markup).toContain('class="token number"');
    expect(markup).toContain(">ts<");
  });

  it("renders unfinished tilde fences through the direct streaming code path", () => {
    const markup = renderToStaticMarkup(
      <MessageMarkdown
        content={["~~~python", "print('hi')"].join("\n")}
        streaming
      />,
    );

    expect(markup).toContain("Streaming python");
    expect(markup).toContain(">python<");
    expect(markup).toContain("print");
  });

  it("renders streaming ordered lists through the structured tail path", () => {
    const markup = renderToStaticMarkup(
      <MessageMarkdown
        content={["Intro", "", "3. first item", "4. second item with [link](https://example.com)"].join(
          "\n",
        )}
        streaming
      />,
    );

    expect(markup).toContain("<ol");
    expect(markup).toContain('start="3"');
    // 流式期尾块走 createInlineMarkdownComponents(true)：只渲染占位 <a>，
    // href 待 settled 后由主路径补齐。
    expect(markup).not.toContain('href="https://example.com"');
    expect(markup).toContain('data-streaming-link="true"');
    expect(markup).toContain('data-streaming-active="true"');
    expect(markup).toContain('aria-live="polite"');
  });

  it("defers link href and image src until the message settles", () => {
    const content = [
      "Intro paragraph",
      "",
      "![shot](https://cdn.example.com/shot.png)",
      "",
      "3. first item",
      "4. second item with [link](https://example.com)",
      "",
      "![local](./shot.png)",
    ].join("\n");

    const streamingMarkup = renderToStaticMarkup(
      <MessageMarkdown content={content} streaming />,
    );

    // 流式期链接不烘焙 href、图片不发请求：引用/图片 handler 只出占位。
    expect(streamingMarkup).not.toContain('href="https://example.com"');
    expect(streamingMarkup).not.toContain(
      'src="https://cdn.example.com/shot.png"',
    );
    expect(streamingMarkup).toContain('data-streaming-link="true"');
    expect(streamingMarkup).toContain('data-streaming-image="true"');

    const settledMarkup = renderToStaticMarkup(
      <MessageMarkdown content={content} />,
    );

    // settled 自愈：同一份文本重解析后链接/绝对图片落地，相对图片保持拦截占位。
    expect(settledMarkup).toContain('href="https://example.com"');
    expect(settledMarkup).toContain('target="_blank"');
    expect(settledMarkup).toContain('src="https://cdn.example.com/shot.png"');
    expect(settledMarkup).toContain('data-image-blocked="true"');
    expect(settledMarkup).not.toContain("data-streaming-link");
    expect(settledMarkup).not.toContain("data-streaming-image");
  });

  it("keeps settled link targets scheme-restricted", () => {
    const markup = renderToStaticMarkup(
      <MessageMarkdown
        content={[
          "[site](/docs/guide)",
          "",
          "[rel](docs/guide.md)",
          "",
          "[proto](//evil.example.com/x)",
          "",
          "[mail](mailto:team@example.com)",
        ].join("\n")}
      />,
    );

    expect(markup).toContain('href="/docs/guide"');
    expect(markup).toContain('href="docs/guide.md"');
    expect(markup).toContain('href="mailto:team@example.com"');
    // 协议相对地址的 scheme 由宿主页面决定，不在渲染层放行。
    expect(markup).not.toContain("evil.example.com");
  });

  it("renders streaming tables through the structured tail path", () => {
    const markup = renderToStaticMarkup(
      <MessageMarkdown
        content={[
          "Intro",
          "",
          "| Name | Value |",
          "| :--- | ---: |",
          "| mode | live |",
        ].join("\n")}
        streaming
      />,
    );

    expect(markup).toContain("<table");
    expect(markup).toContain("Name");
    expect(markup).toContain("text-right");
    expect(markup).toContain('data-streaming-active="true"');
    expect(markup).toContain('aria-live="polite"');
  });

  it("renders streaming blockquotes through the structured tail path", () => {
    const markup = renderToStaticMarkup(
      <MessageMarkdown
        content={["Intro", "", "> quoted line", "> second line"].join("\n")}
        streaming
      />,
    );

    expect(markup).toContain("<blockquote");
    expect(markup).toContain("quoted line");
    expect(markup).toContain("second line");
    expect(markup).toContain('data-streaming-active="true"');
    expect(markup).toContain('aria-live="polite"');
  });

  it("renders streaming plain tails with only the final sentence active", () => {
    const markup = renderToStaticMarkup(
      <MessageMarkdown
        content="Stable sentence. Active sentence is still growing"
        streaming
      />,
    );

    expect(markup).toContain('data-streaming-mode="sentence"');
    expect(markup).toContain('data-streaming-active="true"');
    expect(markup).toContain('aria-live="polite"');
    expect(markup).toContain("Stable sentence.");
    expect(markup).toContain("Active sentence is still growing");
  });

  it("renders multiline plain tails with only the final line active", () => {
    const markup = renderToStaticMarkup(
      <MessageMarkdown
        content={"alpha line\nbeta line"}
        streaming
      />,
    );

    expect(markup).toContain('data-streaming-mode="line"');
    expect(markup).toContain('data-streaming-active="true"');
    expect(markup).toContain('aria-live="polite"');
    expect(markup).toContain("alpha line");
    expect(markup).toContain("beta line");
  });

  it("renders patch fences with diff line semantics and chat collapse controls", () => {
    const markup = renderToStaticMarkup(
      <MessageMarkdown
        content={[
          "```patch",
          ...Array.from({ length: 17 }, (_, index) =>
            index % 2 === 0 ? `+added line ${index + 1}` : `-removed line ${index + 1}`,
          ),
          "```",
        ].join("\n")}
      />,
    );

    expect(markup).toContain('data-line-kind="inserted"');
    expect(markup).toContain('data-line-kind="deleted"');
    expect(markup).toContain("展开剩余 1 行");
  });

  it("renders a stopped tail when interrupted and not streaming", () => {
    const markup = renderToStaticMarkup(
      <MessageMarkdown content="Partial answer" interrupted />,
    );

    expect(markup).toContain("已停止");
    expect(markup).toContain('role="status"');
  });

  it("does not render the stopped tail while streaming", () => {
    const markup = renderToStaticMarkup(
      <MessageMarkdown content="Partial answer" interrupted streaming />,
    );

    expect(markup).not.toContain("已停止");
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
});
