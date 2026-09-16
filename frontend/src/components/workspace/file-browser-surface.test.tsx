// @vitest-environment jsdom

// P0-5/P0-6 面级单测：作用域降级判据（`isFsRootsUnavailable`）与截断/未知类型的诚实展示。
//
// 断言纪律：面级测试只断言「用户可见的诚实性」——
//   * 503 → 降级文案 + 只用会话工作目录作用域，不假装拿到了工作区列表；
//   * 其它失败不冒充 503（如实给出原因）；
//   * `truncated=true` 必须显式提示；`type==="unknown"` 不得给出「进入目录」入口。
// 竞态/取消/游标失效由 `hooks/workspace/use-file-browser.test.tsx` 的 hook 级测试覆盖，此处不重复。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RuntimeApiError } from "@/api/runtime/shared";
import type { FsEntry, FsListingResult, FsRoot } from "@/types/runtime/fs-browser";

import { FileBrowserSurface } from "./file-browser-surface";

const { fetchFsListingMock, fetchFsRootsMock } = vi.hoisted(() => ({
  fetchFsListingMock: vi.fn(),
  fetchFsRootsMock: vi.fn(),
}));

// fs-roots 只替换请求函数，保留真实的 `isFsRootsUnavailable`（降级判据必须走真实实现）。
vi.mock("@/api/runtime/fs-roots", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/fs-roots")>();
  return { ...actual, fetchFsRoots: fetchFsRootsMock };
});

vi.mock("@/api/runtime/fs-list", () => ({
  fetchFsListing: fetchFsListingMock,
  isFsListingCursorError: () => false,
}));

type ReactActEnvironmentGlobal = typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean };

let container: HTMLDivElement | null = null;
let root: Root | null = null;

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

function rootFixture(overrides: Partial<FsRoot> = {}): FsRoot {
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

function entry(partial: Partial<FsEntry> & { name: string; path: string }): FsEntry {
  return { type: "file", size: 10, mtime: 0, ...partial };
}

function page(overrides: Partial<FsListingResult> = {}): FsListingResult {
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

function rowByPath(path: string) {
  const rows = [...document.body.querySelectorAll('[role="treeitem"]')];
  return rows.find((row) => row.querySelector(`button[title="${path}"]`)) ?? null;
}

function clickRow(path: string, init: MouseEventInit = {}) {
  act(() => {
    rowByPath(path)?.querySelector(`button[title="${path}"]`)?.dispatchEvent(new MouseEvent("click", { bubbles: true, ...init }));
  });
}

function noticeText() {
  return document.body.querySelector('[data-testid="file-browser-degraded"]')?.textContent ?? "";
}

async function mountSurface() {
  await act(async () => {
    root?.render(<FileBrowserSurface sessionId="s1" workspacePath="E:/ws" />);
  });
  await act(async () => {
    await flush();
  });
}

describe("FileBrowserSurface 作用域与降级", () => {
  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    fetchFsListingMock.mockReset();
    fetchFsRootsMock.mockReset();
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
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("roots 503 → 显式降级提示，且只用当前会话工作目录作用域", async () => {
    fetchFsRootsMock.mockRejectedValue(new RuntimeApiError(503, null));
    fetchFsListingMock.mockResolvedValue(page());

    await mountSurface();

    expect(noticeText()).toMatch(/文件服务不可用/);
    expect(fetchFsListingMock).toHaveBeenCalled();
    const scopes = fetchFsListingMock.mock.calls.map((call) => (call[0] as { scope: string }).scope);
    expect(scopes.every((scope) => scope === "session:s1")).toBe(true);
    // 作用域下拉与排序下拉共存在头部：排序下拉有固定 id，取另一个。
    const scopeSelect = [...document.body.querySelectorAll("select")].find((select) => select.id !== "file-browser-sort");
    const optionValues = [...(scopeSelect?.querySelectorAll("option") ?? [])].map((option) => option.getAttribute("value"));
    expect(optionValues).toEqual(["session:s1"]);
  });

  it("非降级判据的失败不冒充 503：如实给出原因", async () => {
    fetchFsRootsMock.mockRejectedValue(new Error("boom"));
    fetchFsListingMock.mockResolvedValue(page());

    await mountSurface();

    expect(noticeText()).toMatch(/无法读取文件浏览作用域/);
    expect(noticeText()).toContain("boom");
    expect(noticeText()).not.toMatch(/文件服务不可用/);
  });

  it("roots 就绪：截断显式提示，unknown 不给进入入口，行 a11y 计数与真实兄弟数一致", async () => {
    fetchFsRootsMock.mockResolvedValue({ roots: [rootFixture()] });
    fetchFsListingMock.mockResolvedValue(
      page({
        entries: [
          entry({ name: "src", path: "src", type: "dir" }),
          entry({ name: "weird", path: "weird", type: "unknown" }),
          entry({ name: "a.ts", path: "a.ts" }),
        ],
        truncated: true,
      }),
    );

    await mountSurface();

    expect(noticeText()).toBe("");
    expect(document.body.querySelector('[role="tree"]')).not.toBeNull();
    expect(document.body.querySelector('[data-testid="listing-truncated"]')?.textContent ?? "").toMatch(/本层仅显示前/);

    const rows = [...document.body.querySelectorAll('[role="treeitem"]')];
    expect(rows.map((row) => row.getAttribute("aria-setsize"))).toEqual(["3", "3", "3"]);
    expect(rows.map((row) => row.getAttribute("aria-posinset")).sort()).toEqual(["1", "2", "3"]);

    // 目录可进入（aria-expanded 存在且为 false），unknown 不得被当成目录。
    expect(rowByPath("src")?.getAttribute("aria-expanded")).toBe("false");
    expect(rowByPath("weird")?.hasAttribute("aria-expanded")).toBe(false);
    expect(rowByPath("a.ts")?.hasAttribute("aria-expanded")).toBe(false);
  });
});

// P4：拖拽落点、Ctrl 多选计数、右键菜单按「选中集合」给出批量动作（冒烟级，交互细节由 tree-list 单测覆盖）。
describe("FileBrowserSurface P4：拖拽落点、多选与右键菜单", () => {
  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    fetchFsListingMock.mockReset();
    fetchFsRootsMock.mockReset();
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
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("落点存在；Ctrl 选中 2 项后计数可见，右键菜单按多选给出批量下载与复制路径", async () => {
    fetchFsRootsMock.mockResolvedValue({ roots: [rootFixture()] });
    fetchFsListingMock.mockResolvedValue(
      page({ entries: [entry({ name: "a.ts", path: "a.ts" }), entry({ name: "b.ts", path: "b.ts" })] }),
    );
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });

    await mountSurface();

    expect(document.body.querySelector('[data-testid="file-drop-target"]')).not.toBeNull();
    expect(document.body.querySelector('[data-testid="file-selection-count"]')).toBeNull();

    clickRow("a.ts", { ctrlKey: true });
    clickRow("b.ts", { ctrlKey: true });
    expect(document.body.querySelector('[data-testid="file-selection-count"]')?.textContent ?? "").toMatch(/2/);

    act(() => {
      rowByPath("a.ts")?.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 20 }));
    });
    const menu = document.body.querySelector('[data-testid="file-row-menu"]');
    expect(menu).not.toBeNull();
    const items = [...(menu?.querySelectorAll("button") ?? [])];
    expect(items).toHaveLength(2);
    // 两条都是文件 → 批量下载可用，且标签带选中数量（不逐项展开）。
    expect(items[0].disabled).toBe(false);
    expect(items[0].textContent ?? "").toMatch(/2/);

    await act(async () => {
      items[1].dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await flush();
    });
    expect(writeText).toHaveBeenCalledWith("E:/ws/a.ts\nE:/ws/b.ts");
    expect(document.body.querySelector('[data-testid="file-copy-notice"]')?.textContent ?? "").not.toBe("");
  });
});

// P4-5/Q3：过滤只作用于已加载层；这里验证 owner 的受控接线（ScopeHeader → 树）与清除行为。
describe("FileBrowserSurface P4-5：已加载层过滤", () => {
  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    fetchFsListingMock.mockReset();
    fetchFsRootsMock.mockReset();
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
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  /** React 受控 input：走原型原生 setter + input 事件，绕过 value tracker。 */
  function setFilterValue(input: HTMLInputElement, value: string) {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
    act(() => {
      setter?.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }

  it("过滤词收窄已加载层且不发新请求；清除按钮回到完整列表", async () => {
    fetchFsRootsMock.mockResolvedValue({ roots: [rootFixture()] });
    fetchFsListingMock.mockResolvedValue(
      page({ entries: [entry({ name: "a.ts", path: "a.ts" }), entry({ name: "b.ts", path: "b.ts" })] }),
    );

    await mountSurface();
    expect(rowByPath("a.ts")).not.toBeNull();
    expect(rowByPath("b.ts")).not.toBeNull();

    const input = document.body.querySelector<HTMLInputElement>('[data-testid="file-browser-filter"]');
    expect(input).not.toBeNull();
    const callsBeforeFilter = fetchFsListingMock.mock.calls.length;
    setFilterValue(input as HTMLInputElement, "b.ts");

    expect(rowByPath("a.ts")).toBeNull();
    expect(rowByPath("b.ts")).not.toBeNull();
    // 已加载层过滤 ≠ 远端搜索：不新增列表请求。
    expect(fetchFsListingMock.mock.calls.length).toBe(callsBeforeFilter);

    act(() => {
      document.body
        .querySelector<HTMLButtonElement>('button[aria-label="清除过滤"]')
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(rowByPath("a.ts")).not.toBeNull();
    expect(fetchFsListingMock.mock.calls.length).toBe(callsBeforeFilter);
  });
});
