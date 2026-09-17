// @vitest-environment jsdom

// P1-9/P1-10：搜索结果视图（受控扁平列表）单测。
//
// 断言口径：只看「注入 props → 可见交互与文案」，不假设组件内部实现：
//   * `match.start/end` 是 rune 偏移：emoji / 中文按码点切分，绝不按 UTF-16 下标切（否则高亮会切坏字符）；
//   * 键盘 ↑/↓ 改活动行、Enter 落到活动行；Esc 与 owner 的「清除搜索」同源；
//   * 空 / 加载 / 错 / 不可用 / 截断 + hasMore 的文案与入口（不可用**不提供重试**，其它失败提供重试）；
//   * 类型标注只如实展示：只有 `dir` 可进入，symlink / inaccessible / unknown 不伪装成目录或文件。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { FsSearchItem } from "@/types/runtime/fs-browser";

import { FileSearchResults, type FileSearchResultsLabels, type FileSearchResultsProps } from "./search-results";
import { splitRuneMatch } from "./search-match";

/** 与 zh-CN 词典同口径的注入文案（组件不读 i18n，文案由 owner 提供）。 */
const LABELS: FileSearchResultsLabels = {
  aria: "全库搜索结果",
  empty: "未找到匹配的文件或目录。",
  error: "搜索失败：{{message}}",
  exit: "清除搜索，返回目录树",
  loadMore: "加载更多",
  loading: "正在搜索整个作用域…",
  loadingMore: "正在加载更多…",
  resultDir: "目录",
  resultInaccessible: "无权限",
  resultSymlink: "符号链接",
  resultUnknown: "类型未知",
  retry: "重试",
  truncated: "结果可能不完整（已扫描 {{count}} 项）。",
  unavailable: "远端搜索不可用，仅过滤已加载层。",
};

function item(path: string, overrides: Partial<FsSearchItem> = {}): FsSearchItem {
  return {
    name: path.split("/").pop() ?? path,
    path,
    type: "file",
    size: 10,
    mtime: 0,
    score: 100,
    ...overrides,
  };
}

type Handlers = {
  onSelect?: (item: FsSearchItem) => void;
  onEnterDir?: (item: FsSearchItem) => void;
  onLoadMore?: () => void;
  onRetry?: () => void;
  onExit?: () => void;
};

let container: HTMLDivElement | null = null;
let root: Root | null = null;

/** 渲染结果视图：默认是「就绪 + 空结果」，用例只覆盖关心的字段。 */
function renderResults(overrides: Partial<FileSearchResultsProps> = {}, handlers: Handlers = {}) {
  const props: FileSearchResultsProps = {
    errorMessage: "",
    hasMore: false,
    items: [],
    labels: LABELS,
    loadingMore: false,
    onEnterDir: handlers.onEnterDir ?? vi.fn(),
    onExit: handlers.onExit ?? vi.fn(),
    onLoadMore: handlers.onLoadMore ?? vi.fn(),
    onRetry: handlers.onRetry ?? vi.fn(),
    onSelect: handlers.onSelect ?? vi.fn(),
    scanned: 0,
    selectedPath: null,
    status: "ready",
    truncated: false,
    unavailable: false,
    ...overrides,
  };
  act(() => {
    root?.render(<FileSearchResults {...props} />);
  });
  return props;
}

function pressKey(key: string) {
  act(() => {
    document.body
      .querySelector('[data-testid="file-search-results"]')
      ?.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
  });
}

function click(target: Element | null | undefined) {
  act(() => {
    target?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

function rows(): Element[] {
  return [...document.body.querySelectorAll('[role="option"]')];
}

function text(testId: string): string {
  return document.body.querySelector(`[data-testid="${testId}"]`)?.textContent ?? "";
}

function buttonByText(label: string): HTMLButtonElement | null {
  return (
    [...document.body.querySelectorAll("button")].find((button) => button.textContent?.includes(label)) ?? null
  );
}

describe("splitRuneMatch", () => {
  it("按 rune 偏移切分：emoji 不被切成半个代理对，越界夹紧", () => {
    expect(splitRuneMatch("图片😀a.ts", { field: "name", start: 2, end: 3 })).toEqual(["图片", "😀", "a.ts"]);
    expect(splitRuneMatch("ab", { field: "name", start: 5, end: 9 })).toEqual(["ab", "", ""]);
    // 空命中（end ≤ start）不产生高亮，不猜位置。
    expect(splitRuneMatch("ab", { field: "name", start: 1, end: 1 })).toEqual(["ab", "", ""]);
  });
});

describe("FileSearchResults 结果行", () => {
  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container?.remove();
    container = null;
    delete (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT;
  });

  it("命中高亮只作用于 match.field 指定的那一列（name 命中不动 path 列）", () => {
    renderResults({
      items: [
        item("docs/图片😀a.ts", {
          name: "图片😀a.ts",
          match: { field: "name", start: 2, end: 3 },
        }),
      ],
    });

    const hits = [...document.body.querySelectorAll('[data-testid="file-search-hit"]')];
    expect(hits.map((hit) => hit.textContent)).toEqual(["😀"]);
    expect(rows()[0].textContent).toContain("图片😀a.ts");
    expect(rows()[0].textContent).toContain("docs/图片😀a.ts");
  });

  it("path 命中时高亮相对路径列，名称列原样展示", () => {
    renderResults({
      items: [item("src/deep/found.ts", { match: { field: "path", start: 4, end: 8 } })],
    });

    const hits = [...document.body.querySelectorAll('[data-testid="file-search-hit"]')];
    expect(hits.map((hit) => hit.textContent)).toEqual(["deep"]);
  });

  it("↑/↓ 移动活动行并被 Enter 采用（越界夹紧到 0 与末行）", () => {
    const onSelect = vi.fn();
    renderResults({ items: [item("a.ts"), item("b.ts"), item("c.ts")] }, { onSelect });

    const listbox = () => document.body.querySelector('[data-testid="file-search-results"]');
    expect(listbox()?.getAttribute("aria-activedescendant")).toBe("file-search-row-0");

    pressKey("ArrowDown");
    pressKey("ArrowDown");
    expect(listbox()?.getAttribute("aria-activedescendant")).toBe("file-search-row-2");
    pressKey("ArrowDown");
    expect(listbox()?.getAttribute("aria-activedescendant")).toBe("file-search-row-2");

    pressKey("Enter");
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect.mock.calls[0][0].path).toBe("c.ts");

    pressKey("ArrowUp");
    pressKey("ArrowUp");
    pressKey("ArrowUp");
    expect(listbox()?.getAttribute("aria-activedescendant")).toBe("file-search-row-0");
  });

  it("点击目录行走 onEnterDir，不冒充文件选中", () => {
    const onSelect = vi.fn();
    const onEnterDir = vi.fn();
    renderResults({ items: [item("src", { type: "dir" })] }, { onEnterDir, onSelect });

    click(document.body.querySelector('button[aria-label="src"]'));
    expect(onEnterDir).toHaveBeenCalledTimes(1);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("Esc 上抛退出（与 owner 的清除搜索同源）", () => {
    const onExit = vi.fn();
    renderResults({ items: [item("a.ts")] }, { onExit });

    pressKey("Escape");
    expect(onExit).toHaveBeenCalledTimes(1);
  });

  it("加载中 / 空结果：各自显式文案，不互相冒充", () => {
    renderResults({ status: "loading" });
    expect(text("file-search-loading")).toContain(LABELS.loading);
    expect(document.body.querySelector('[data-testid="file-search-empty"]')).toBeNull();

    renderResults({ status: "ready" });
    expect(text("file-search-empty")).toContain(LABELS.empty);
    expect(document.body.querySelector('[data-testid="file-search-loading"]')).toBeNull();
  });

  it("失败可重试；端点不可用只提示且不给重试入口", () => {
    const onRetry = vi.fn();
    renderResults({ errorMessage: "boom", status: "error" }, { onRetry });
    expect(text("file-search-error")).toContain("搜索失败：boom");
    click(buttonByText(LABELS.retry));
    expect(onRetry).toHaveBeenCalledTimes(1);

    renderResults({ errorMessage: "HTTP 404", status: "error", unavailable: true }, { onRetry });
    expect(text("file-search-error")).toContain(LABELS.unavailable);
    expect(buttonByText(LABELS.retry)).toBeNull();
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("截断与 hasMore 正交：截断显示已扫描数，加载更多可点/进行中禁用", () => {
    const onLoadMore = vi.fn();
    renderResults({ hasMore: true, items: [item("a.ts")], scanned: 12, truncated: true }, { onLoadMore });
    expect(text("file-search-truncated")).toContain("12");
    click(buttonByText(LABELS.loadMore));
    expect(onLoadMore).toHaveBeenCalledTimes(1);

    renderResults({ hasMore: true, loadingMore: true, items: [item("a.ts")] });
    const busy = buttonByText(LABELS.loadingMore);
    expect(busy?.disabled).toBe(true);
    // 只截断、没有下一页时不出现加载更多入口。
    renderResults({ scanned: 3, truncated: true });
    expect(buttonByText(LABELS.loadMore)).toBeNull();
  });

  it("选中态只由 owner 的 selectedPath 决定；类型标注只如实展示类型", () => {
    renderResults({
      items: [
        item("src", { type: "dir" }),
        item("link.ts", { type: "symlink" }),
        item("secret", { type: "inaccessible" }),
        item("weird", { type: "unknown" }),
      ],
      selectedPath: "link.ts",
    });

    const selected = rows().filter((row) => row.getAttribute("aria-selected") === "true");
    expect(selected).toHaveLength(1);
    expect(selected[0].textContent).toContain("link.ts");

    const labels = rows().map((row) => row.textContent ?? "");
    expect(labels[0]).toContain(LABELS.resultDir);
    expect(labels[1]).toContain(LABELS.resultSymlink);
    expect(labels[2]).toContain(LABELS.resultInaccessible);
    expect(labels[3]).toContain(LABELS.resultUnknown);
  });
});
