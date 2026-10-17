// @vitest-environment jsdom

// P3-1：web_search 来源卡片。断言口径：
//   * 数量徽标 / 域名列表 / 截断提示 / 含答案徽标都来自解析后的真实数据；
//   * 非 http(s) 的 URL 不渲染外链，只回退纯文本域名（不伪造可点击目标）；
//   * 无来源且无答案时组件不渲染（面板保持原始文本块）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { i18n } from "@/i18n";
import { zhWorkspacePanelsMessages } from "@/i18n/resources/zh-CN/workspace/panels-messages";
import { type WebSearchMetaView } from "@/lib/tool-row/web-search-meta";

import { ToolRowWebSources } from "./tool-row-web-sources";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

describe("ToolRowWebSources", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    i18n.addResourceBundle(
      "zh-CN",
      "workspace",
      { panels: { messages: zhWorkspacePanelsMessages } },
      true,
      false,
    );
    if (i18n.language !== "zh-CN") {
      void i18n.changeLanguage("zh-CN");
    }

    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function render(meta: WebSearchMetaView) {
    act(() => {
      root.render(<ToolRowWebSources meta={meta} />);
    });
  }

  const emptyView = {
    quality: null,
    qualitySignals: null,
    cancelledQueries: [] as string[],
  };

  it("渲染数量徽标 / 域名列表 / 截断提示 / 含答案徽标", () => {
    render({
      sources: [
        {
          url: "https://www.example.com/a",
          title: "示例标题",
          snippet: "",
          publishedAt: "2026-10-01",
        },
      ],
      hasAnswer: true,
      truncated: true,
      answerQueries: ["q"],
      ...emptyView,
    });

    expect(container.querySelector('[data-tool-row-web-sources="true"]')).not.toBeNull();
    expect(container.querySelector('[data-tool-row-web-count="1"]')).not.toBeNull();
    expect(container.querySelector('[data-tool-row-web-answer="true"]')).not.toBeNull();
    expect(container.querySelector('[data-tool-row-web-truncated="true"]')).not.toBeNull();

    const link = container.querySelector<HTMLAnchorElement>('[data-tool-row-web-source="true"] a');
    expect(link?.getAttribute("href")).toBe("https://www.example.com/a");
    expect(link?.getAttribute("rel")).toBe("noreferrer noopener");
    expect(link?.textContent).toContain("example.com");
    expect(container.textContent).toContain("示例标题");
    expect(container.textContent).toContain("2026-10-01");
  });

  it("非 http(s) 的 URL 不渲染外链（纯文本域名）", () => {
    render({
      sources: [
        { url: "javascript:alert(1)", title: "", snippet: "", publishedAt: "" },
      ],
      hasAnswer: false,
      truncated: false,
      answerQueries: [],
      ...emptyView,
    });

    expect(container.querySelector('[data-tool-row-web-source="true"] a')).toBeNull();
    expect(container.textContent).toContain("javascript:alert(1)");
  });

  it("无来源且无答案时不渲染（回退文本块）", () => {
    render({ sources: [], hasAnswer: false, truncated: false, answerQueries: [], ...emptyView });
    expect(container.querySelector('[data-tool-row-web-sources="true"]')).toBeNull();
  });

  it("低质徽标带信号 tooltip；ok 无徽标；取消查询渲染 chip（P3-2/P3-3）", () => {
    render({
      sources: [{ url: "https://a.example/1", title: "", snippet: "", publishedAt: "" }],
      hasAnswer: false,
      truncated: false,
      answerQueries: [],
      quality: "low",
      qualitySignals: { coverage: 0.4, domain_diversity: 0.6, snippet_presence: 1 },
      cancelledQueries: ["m1"],
    });

    const badge = container.querySelector('[data-tool-row-web-quality="low"]');
    expect(badge).not.toBeNull();
    expect(badge?.getAttribute("title")).toBe("coverage: 40% · domain_diversity: 60% · snippet_presence: 100%");

    const cancelled = container.querySelector('[data-tool-row-web-cancelled="m1"]');
    expect(cancelled).not.toBeNull();
    expect(cancelled?.textContent).toContain("m1");
  });

  it("quality=ok 不渲染低质徽标", () => {
    render({
      sources: [{ url: "https://a.example/1", title: "", snippet: "", publishedAt: "" }],
      hasAnswer: false,
      truncated: false,
      answerQueries: [],
      quality: "ok",
      qualitySignals: { coverage: 1 },
      cancelledQueries: [],
    });
    expect(container.querySelector('[data-tool-row-web-quality]')).toBeNull();
  });
});
