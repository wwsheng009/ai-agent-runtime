import { renderToStaticMarkup } from "react-dom/server";
import { createRoot } from "react-dom/client";
import { act } from "react";
import { beforeAll, describe, expect, it, vi } from "vitest";

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

  it("calibrates inline code to the reference chip: 0 5px padding, no border", () => {
    const markup = renderToStaticMarkup(
      <MessageMarkdown content={"Run `pnpm test` before pushing."} />,
    );
    const codeClassName = /<code class="([^"]*)"/.exec(markup)?.[1] ?? "";
    const tokens = codeClassName.split(/\s+/).filter(Boolean);

    // 批次 D2：字号/行高由 `.app-md-inline-code` 走码字轴 token 派生（14/22）。
    expect(tokens).toContain("app-md-inline-code");
    // 参考站 MarkdownText.module.css:161-172：padding 0 5px、radius 6px、无边框。
    expect(tokens).toContain("px-[5px]");
    expect(tokens).toContain("rounded-[6px]");
    expect(
      tokens.filter((token) => token === "border" || token.startsWith("border-")),
    ).toEqual([]);
    expect(tokens.filter((token) => /^(py-|p-|pt-|pb-)/.test(token))).toEqual([]);
    // 字号/行高不得内联在组件里（必须由 base.css 的码字轴 token 派生）。
    expect(tokens.filter((token) => /^(text-\[|leading-)/.test(token))).toEqual([]);
  });

  it("keeps the code block body and banner on distinct surface tokens", () => {
    const markup = renderToStaticMarkup(
      <MessageMarkdown
        content={["```ts", "const answer = 42;", "```"].join("\n")}
      />,
    );
    const surfaceClassName =
      /class="([^"]*app-code-surface[^"]*)"/.exec(markup)?.[1] ?? "";
    const bannerClassName =
      /class="([^"]*bg-code-block-header-bg[^"]*)"/.exec(markup)?.[1] ?? "";

    // 批次 D2：代码块 13/22（行高走 `.app-chat-copy .app-code-surface pre` 的码字轴
    // 22px token），底/条分色沿用语义 token（参考站 #1b1b1c / #2c2c2e）。
    expect(surfaceClassName).toContain("bg-code-block-bg");
    expect(surfaceClassName).not.toContain("bg-code-block-header-bg");
    expect(bannerClassName).toContain("bg-code-block-header-bg");
    expect(bannerClassName).not.toContain("bg-code-block-bg");
  });

  it("keeps unfinished fenced blocks renderable while streaming", () => {
    const markup = renderToStaticMarkup(
      <MessageMarkdown
        content={["```ts", "const answer = 42;", "const pending = 1"].join("\n")}
        streaming
      />,
    );

    expect(markup).toContain("Streaming ts");
    expect(markup).toContain('aria-busy="true"');
    // 第二前沿：已换行的完整行走 Prism 高亮；仍在增长的 partial 行按纯文本渲染，
    // 避免半截 token（未闭合字符串 / 注释）把高亮染色带偏。
    expect(markup).toContain('class="token keyword"');
    expect(markup).toContain('class="token number"');
    expect(markup).toContain('data-line-kind="partial"');
    expect(markup).toContain("const pending = 1");
    // 切分产物（stableCode 末尾的空行）被去掉：两行内容 = 两个行号，partial 行号接续。
    expect(markup.split("app-code-line-number").length - 1).toBe(2);
    expect(
      markup.slice(markup.indexOf('data-line-kind="partial"')),
    ).toContain(">2<");
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
          "[web](https://example.com/docs)",
          "",
          "[anchor](#section-1)",
          "",
          "[mail](mailto:team@example.com)",
          "",
          "[site](/docs/guide)",
          "",
          "[rel](docs/guide.md)",
          "",
          "[proto](//evil.example.com/x)",
        ].join("\n")}
      />,
    );

    // 绝对 http(s) / mailto / 页内锚点放行。
    expect(markup).toContain('href="https://example.com/docs"');
    expect(markup).toContain('href="mailto:team@example.com"');
    expect(markup).toContain('href="#section-1"');
    // 相对链接与协议相对地址（方案 §2②「禁用相对链接」）不进入可导航链接。
    expect(markup).not.toContain("/docs/guide");
    expect(markup).not.toContain("docs/guide.md");
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

  it("renders one line break per source line in pre-wrap paragraphs", () => {
    // Tool receipt 回归：mdast-util-to-hast 的 hardBreak 会输出 `<br>` + 一个纯换行文本
    // 节点；`whitespace-pre-wrap` 段落会把该换行再渲染成一次强制换行，于是每个换行都
    // 多出一个空行。这里锁住「`<br>` 之后不再保留换行文本」的收敛形态。
    const content = [
      "{",
      '  "tool_call_id": "call_00_vnGp5jYxQ8mXg9g4IJ2i7846",',
      '  "ok": true',
      "}",
      "",
      "Full raw output artifact_id: art_325227a5aeb54277b9e39b834d216e76",
    ].join("\n");
    const markup = renderToStaticMarkup(<MessageMarkdown content={content} />);

    expect(markup).toContain("whitespace-pre-wrap");
    expect(markup).toContain("&quot;tool_call_id&quot;");
    expect(markup).not.toMatch(/<br\s*\/?>\n/);
    expect(markup.split("<br").length - 1).toBe(3);
  });

  it("renders a clickable artifact output link for the raw output pointer line", () => {
    const artifactId = "art_325227a5aeb54277b9e39b834d216e76";
    const markup = renderToStaticMarkup(
      <MessageMarkdown
        content={`Some tool output\n\nFull raw output artifact_id: ${artifactId}`}
      />,
    );

    expect(markup).toContain('data-artifact-output-link="true"');
    expect(markup).toContain("查看完整原始输出");
    expect(markup).toContain(
      `aria-label="查看完整原始输出（${artifactId}）"`,
    );
    // 指针行原文本不再以普通文本暴露。
    expect(markup).not.toContain(`Full raw output artifact_id: ${artifactId}`);
  });

  it("renders the artifact link for a pointer line inside a fenced code block", () => {
    const artifactId = "art_325227a5aeb54277b9e39b834d216e76";
    const markup = renderToStaticMarkup(
      <MessageMarkdown
        content={`\`\`\`json\n{"ok": true}\n\nFull raw output artifact_id: ${artifactId}\n\`\`\``}
      />,
    );

    expect(markup).toContain('data-artifact-output-link="true"');
    expect(markup).toContain(
      `aria-label="查看完整原始输出（${artifactId}）"`,
    );
  });

  it("opens the artifact detail dialog via onSelectArtifact when the pointer link is clicked", async () => {
    const artifactId = "art_325227a5aeb54277b9e39b834d216e76";
    const onSelectArtifact = vi.fn();
    const container = document.createElement("div");
    const root = createRoot(container);
    await act(async () => {
      root.render(
        <MessageMarkdown
          content={`Full raw output artifact_id: ${artifactId}`}
          onSelectArtifact={onSelectArtifact}
        />,
      );
    });

    const button = container.querySelector<HTMLButtonElement>(
      "[data-artifact-output-link]",
    );
    expect(button).not.toBeNull();
    button?.dispatchEvent(new MouseEvent("click", { bubbles: true }));

    expect(onSelectArtifact).toHaveBeenCalledTimes(1);
    expect(onSelectArtifact).toHaveBeenCalledWith(artifactId);

    await act(async () => {
      root.unmount();
    });
  });

  it("falls back to copying the artifact id with visible feedback when no onSelectArtifact is provided", async () => {
    const artifactId = "art_325227a5aeb54277b9e39b834d216e76";
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    const container = document.createElement("div");
    const root = createRoot(container);
    await act(async () => {
      root.render(
        <MessageMarkdown content={`Full raw output artifact_id: ${artifactId}`} />,
      );
    });

    const button = container.querySelector<HTMLButtonElement>(
      "[data-artifact-output-link]",
    );
    expect(button).not.toBeNull();
    await act(async () => {
      button?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(writeText).toHaveBeenCalledTimes(1);
    expect(writeText).toHaveBeenCalledWith(artifactId);
    // 可见反馈：按钮文本切换为「已复制完整原始输出 id」。
    expect(container.innerHTML).toContain("已复制完整原始输出 id");

    await act(async () => {
      root.unmount();
    });
  });
});
