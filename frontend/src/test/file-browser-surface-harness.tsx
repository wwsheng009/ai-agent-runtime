// 面级测试共享脚手架（P0-5/P1-9）：把「建 DOM / 渲染面 / 受控输入 / 结果视图查询」从
// `components/workspace/file-browser-surface.test.tsx` 抽出，用例文件只保留断言
// （同时守住 `verify-max-lines` 的 500 非空行门禁）。
//
// 纪律：
//   * 这里**不做** `vi.mock`：请求替换仍由用例文件声明，脚手架只消费被测组件与 DOM；
//   * `settle()` / `advanceDebounce()` 是为 fake timers 准备的（setState 经 React 调度器排队，
//     不推进 0ms 定时器就不会提交），与 `hooks/workspace/use-fs-search.test.tsx` 同款口径。
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { vi } from "vitest";

import { FS_SEARCH_DEBOUNCE_PANEL_MS, FS_SEARCH_LIMIT_PANEL } from "@/hooks/workspace/use-fs-search";

import { FileBrowserSurface } from "@/components/workspace/file-browser-surface";

import type {
  FsEntry,
  FsListingResult,
  FsPreview,
  FsRoot,
  FsSearchItem,
  FsSearchResult,
} from "@/types/runtime/fs-browser";

type ReactActEnvironmentGlobal = typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean };

let container: HTMLDivElement | null = null;
let root: Root | null = null;

export function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

export function rootFixture(overrides: Partial<FsRoot> = {}): FsRoot {
  return {
    scope: "session:s1",
    kind: "session",
    name: "ws",
    path: "E:/ws",
    exists: true,
    isGitRepo: false,
    ...overrides,
  };
}

export function entry(partial: Partial<FsEntry> & { name: string; path: string }): FsEntry {
  return { type: "file", size: 10, mtime: 0, ...partial };
}

export function page(overrides: Partial<FsListingResult> = {}): FsListingResult {
  return {
    dir: { path: "", absPath: "E:/ws", parent: "", isRoot: true },
    entries: [],
    nextCursor: null,
    hasMore: false,
    truncated: false,
    sort: "type_then_name",
    ...overrides,
  };
}

export function rowByPath(path: string) {
  const rows = [...document.body.querySelectorAll('[role="treeitem"]')];
  return rows.find((row) => row.querySelector(`button[title="${path}"]`)) ?? null;
}

export function noticeText() {
  return document.body.querySelector('[data-testid="file-browser-degraded"]')?.textContent ?? "";
}

export function clickRow(path: string, init: MouseEventInit = {}) {
  act(() => {
    rowByPath(path)
      ?.querySelector(`button[title="${path}"]`)
      ?.dispatchEvent(new MouseEvent("click", { bubbles: true, ...init }));
  });
}

/** 建容器 + act 环境标记；渲染/清理都走脚手架，用例文件不再直接碰 ReactDOM。 */
export function setupSurfaceDom() {
  (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
}

export function teardownSurfaceDom() {
  act(() => {
    root?.unmount();
  });
  root = null;
  container?.remove();
  container = null;
  delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
}

export async function mountSurface() {
  await act(async () => {
    root?.render(<FileBrowserSurface sessionId="s1" workspacePath="E:/ws" />);
  });
  await act(async () => {
    await flush();
  });
}

/** React 受控 input：走原型原生 setter + input 事件，绕过 value tracker。 */
export function setFilterValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  act(() => {
    setter?.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

/**
 * fake timers 下的落定：act 之外的 setState 经 React 调度器（setImmediate）排队，
 * 不推进 0ms 定时器就永远不会提交（与 `use-fs-search.test.tsx` 的 settle 同款）。
 */
export async function settle() {
  await act(async () => {
    for (let i = 0; i < 4; i += 1) {
      await flush();
      await vi.advanceTimersByTimeAsync(0);
    }
  });
}

/** 推进防抖并落定请求 Promise 链（默认面板防抖 300ms）。 */
export async function advanceDebounce(ms = FS_SEARCH_DEBOUNCE_PANEL_MS) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
    await flush();
  });
  await settle();
}

export function filterInput(): HTMLInputElement {
  const input = document.body.querySelector<HTMLInputElement>('[data-testid="file-browser-filter"]');
  if (!input) {
    throw new Error("file-browser-filter 未渲染");
  }
  return input;
}

export function searchRows(): Element[] {
  return [...document.body.querySelectorAll('[data-testid="file-search-results"] [role="option"]')];
}

export function searchRowButton(path: string): HTMLButtonElement | null {
  return document.body.querySelector<HTMLButtonElement>(
    `[data-testid="file-search-results"] button[title="${path}"]`,
  );
}

export function resultsVisible(): boolean {
  return document.body.querySelector('[data-testid="file-search-results"]') !== null;
}

export function degradedText(): string {
  return document.body.querySelector('[data-testid="file-search-degraded"]')?.textContent ?? "";
}

export function searchItem(path: string, overrides: Partial<FsSearchItem> = {}): FsSearchItem {
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

export function searchPage(overrides: Partial<FsSearchResult> = {}): FsSearchResult {
  return {
    scope: "session:s1",
    query: "q",
    base: "",
    items: [],
    nextCursor: null,
    hasMore: false,
    scanned: 5,
    truncated: false,
    truncatedReasons: [],
    elapsedMs: 3,
    limit: FS_SEARCH_LIMIT_PANEL,
    ...overrides,
  };
}

export function previewFixture(): FsPreview {
  return { kind: "text", path: "deep/nested/found.ts", size: 17, truncated: false, text: "const found = 1;\n" };
}

// —— P5：多页签文件管理器的查询助手（页签条 / 激活态 / 关闭入口） ——

/** 文件管理器页签条容器（role=tablist）。 */
export function tabStrip(): Element | null {
  return document.body.querySelector('[data-testid="file-manager-tab-strip"]');
}

/** 页签条内全部页签按钮（role=tab），按渲染顺序。 */
export function tabButtons(): HTMLButtonElement[] {
  return [
    ...document.body.querySelectorAll<HTMLButtonElement>(
      '[data-testid="file-manager-tab-strip"] [role="tab"]',
    ),
  ];
}

export function tabLabels(): string[] {
  return tabButtons().map((button) => button.textContent?.trim() ?? "");
}

export function activeTabLabel(): string {
  return (
    tabButtons()
      .find((button) => button.getAttribute("aria-selected") === "true")
      ?.textContent?.trim() ?? ""
  );
}

export function tabButton(label: string): HTMLButtonElement | null {
  return tabButtons().find((button) => button.textContent?.trim() === label) ?? null;
}

/** 页签的关闭按钮；非可关页签（根页签）返回 null。 */
export function tabCloseButton(label: string): HTMLButtonElement | null {
  const button = tabButton(label);
  const id = button?.getAttribute("data-testid")?.slice("file-manager-tab-".length) ?? "";
  return (
    [...document.body.querySelectorAll<HTMLButtonElement>(
      '[data-testid="file-manager-tab-strip"] button',
    )].find((item) => item.getAttribute("data-testid") === `file-manager-tab-close-${id}`) ?? null
  );
}

export function clickTab(label: string) {
  const button = tabButton(label);
  act(() => {
    button?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

export function closeTab(label: string) {
  const button = tabCloseButton(label);
  act(() => {
    button?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

/** 文件页签正文是否展示指定路径（正文 testid 与旧预览面板同源，见 `file-tab.tsx`）。 */
export function filePaneShowsPath(path: string): boolean {
  const pane = document.body.querySelector('[data-testid="file-manager-file-tab"]');
  return pane !== null && (pane.textContent ?? "").includes(path);
}

/** 目录树是否在场（根页签激活时为 true；文件页签接管正文时为 false）。 */
export function browserTreeVisible(): boolean {
  return document.body.querySelector('[role="tree"]') !== null;
}
