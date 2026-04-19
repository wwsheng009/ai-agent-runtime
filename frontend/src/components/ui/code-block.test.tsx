// @vitest-environment jsdom

import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { CodeBlock } from "./code-block";
import { highlightCode } from "./code-highlighting";

describe("CodeBlock", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
  });

  afterEach(() => {
    if (root) {
      act(() => {
        root?.unmount();
      });
    }
    container.remove();
    document.body.innerHTML = "";
  });

  function renderCodeBlock(props: Partial<ComponentProps<typeof CodeBlock>> = {}) {
    root = createRoot(container);

    act(() => {
      root?.render(
        <CodeBlock
          code="const answer = 42;"
          language="ts"
          title="example.ts"
          {...props}
        />,
      );
    });
  }

  function dispatchClick(target: EventTarget) {
    act(() => {
      target.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
  }

  it("renders highlighted token spans for supported languages", () => {
    const markup = renderToStaticMarkup(
      <CodeBlock code={'const answer = "ok";'} language="ts" title="example.ts" />,
    );

    expect(markup).toContain('class="token keyword"');
    expect(markup).toContain('class="token string"');
    expect(markup).toContain(">const<");
    expect(markup).toContain("&quot;ok&quot;");
  });

  it("marks inserted and deleted lines for diff code blocks", () => {
    const markup = renderToStaticMarkup(
      <CodeBlock
        code={["diff --git a/app.ts b/app.ts", "+const added = true;", "-const removed = false;"].join("\n")}
        language="diff"
      />,
    );

    expect(markup).toContain('data-line-kind="inserted"');
    expect(markup).toContain('data-line-kind="deleted"');
  });

  it("collapses long code blocks and expands on demand", () => {
    renderCodeBlock({
      code: Array.from({ length: 18 }, (_, index) => `line ${index + 1}`).join("\n"),
      collapsible: true,
      language: "text",
      title: "long.txt",
    });

    expect(container.textContent).toContain("Show 2 more lines");
    expect(container.textContent).toContain("line 16");
    expect(container.textContent).not.toContain("line 18");

    const toggle = container.querySelector('button[aria-expanded="false"]');
    expect(toggle).toBeInstanceOf(HTMLButtonElement);

    dispatchClick(toggle as HTMLButtonElement);

    expect(container.textContent).toContain("Collapse code");
    expect(container.textContent).toContain("line 18");
  });

  it("preserves trailing empty lines when splitting highlighted output", () => {
    const lines = highlightCode("first line\n", "text");

    expect(lines).toHaveLength(2);
    expect(lines[0]).toEqual({
      kind: "normal",
      segments: [
        {
          content: "first line",
          types: [],
        },
      ],
    });
    expect(lines[1]).toEqual({
      kind: "normal",
      segments: [],
    });
  });

  it("maps patch aliases to diff line kinds", () => {
    const lines = highlightCode(
      ["@@ -1,2 +1,2 @@", "+const next = true;", "--- a/app.ts", "-const prev = false;"].join(
        "\n",
      ),
      "patch",
    );

    expect(lines[1].kind).toBe("inserted");
    expect(lines[2].kind).toBe("normal");
    expect(lines[3].kind).toBe("deleted");
  });
});
