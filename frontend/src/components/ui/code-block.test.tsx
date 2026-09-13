// @vitest-environment jsdom

import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import { CodeBlock } from "./code-block";
import { codeHighlightingReady, highlightCode } from "./code-highlighting";

(
  globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

// 视口懒激活测试替身：记录 observe/unobserve 并可手动派发相交回调。
class MockIntersectionObserver {
  readonly observed: Element[] = [];
  readonly unobserved: Element[] = [];
  disconnected = false;
  private readonly callback: IntersectionObserverCallback;

  constructor(callback: IntersectionObserverCallback) {
    this.callback = callback;
  }

  observe(target: Element) {
    this.observed.push(target);
  }

  unobserve(target: Element) {
    this.unobserved.push(target);
  }

  disconnect() {
    this.disconnected = true;
  }

  takeRecords(): IntersectionObserverEntry[] {
    return [];
  }

  trigger(isIntersecting: boolean) {
    this.callback(
      this.observed.map(
        (target) =>
          ({ isIntersecting, target }) as unknown as IntersectionObserverEntry,
      ),
      this as unknown as IntersectionObserver,
    );
  }
}

// 每次安装返回全新的构造函数：驱动 useViewportActivation 的单例重建分支，
// 并保证用例之间不会共享观察器状态。
function installMockObserver() {
  const instances: MockIntersectionObserver[] = [];

  class StubIntersectionObserver extends MockIntersectionObserver {
    constructor(callback: IntersectionObserverCallback) {
      super(callback);
      instances.push(this);
    }
  }

  vi.stubGlobal("IntersectionObserver", StubIntersectionObserver);

  return instances;
}

describe("CodeBlock", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeAll(async () => {
    await codeHighlightingReady;
  });

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

    expect(container.textContent).toContain("展开剩余 2 行");
    expect(container.textContent).toContain("line 16");
    expect(container.textContent).not.toContain("line 18");

    const toggle = container.querySelector('button[aria-expanded="false"]');
    expect(toggle).toBeInstanceOf(HTMLButtonElement);

    dispatchClick(toggle as HTMLButtonElement);

    expect(container.textContent).toContain("收起代码");
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

  it("defers highlighting until the block enters the viewport, then leaves the observation set", () => {
    const observers = installMockObserver();

    try {
      renderCodeBlock({ code: 'const answer = "ok";', language: "ts", title: "example.ts" });

      // 未进入视口：先按纯文本渲染，不产生 token span。
      expect(container.textContent).toContain('const answer = "ok";');
      expect(container.querySelector(".token")).toBeNull();

      expect(observers).toHaveLength(1);
      const target = observers[0].observed[0];
      expect(target).toBeInstanceOf(HTMLElement);

      // 仅在真正相交时激活。
      act(() => {
        observers[0].trigger(false);
      });
      expect(container.querySelector(".token")).toBeNull();

      act(() => {
        observers[0].trigger(true);
      });

      expect(container.querySelector(".token.keyword")?.textContent).toBe("const");
      // 一次性观察：激活后元素永久离开观察集，滚动不再重复触发高亮。
      expect(observers[0].unobserved).toHaveLength(1);
      expect(observers[0].unobserved[0]).toBe(target);
      // 文档级单例要保持存活，供后续代码块复用。
      expect(observers[0].disconnected).toBe(false);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("reuses one document-level observer across code blocks", () => {
    const observers = installMockObserver();

    try {
      root = createRoot(container);
      act(() => {
        root?.render(
          <>
            <CodeBlock code="const first = 1;" language="ts" title="first.ts" />
            <CodeBlock code="const second = 2;" language="ts" title="second.ts" />
          </>,
        );
      });

      expect(observers).toHaveLength(1);
      expect(observers[0].observed).toHaveLength(2);
      expect(container.querySelector(".token")).toBeNull();

      act(() => {
        observers[0].trigger(true);
      });

      // 一次相交回调激活所有已相交的代码块，并清空观察集。
      expect(container.querySelectorAll(".token.keyword")).toHaveLength(2);
      expect(observers[0].unobserved).toHaveLength(2);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("highlights eagerly when IntersectionObserver is unavailable", () => {
    vi.stubGlobal("IntersectionObserver", undefined);

    try {
      const markup = renderToStaticMarkup(
        <CodeBlock code={'const answer = "ok";'} language="ts" title="example.ts" />,
      );

      expect(markup).toContain('class="token keyword"');
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
