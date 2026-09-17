// @vitest-environment jsdom

// P0-5/P0-6 面级单测：作用域降级判据（`isFsRootsUnavailable`）与截断/未知类型的诚实展示。
//
// 断言纪律：面级测试只断言「用户可见的诚实性」——
//   * 503 → 降级文案 + 只用会话工作目录作用域，不假装拿到了工作区列表；
//   * 其它失败不冒充 503（如实给出原因）；
//   * `truncated=true` 必须显式提示；`type==="unknown"` 不得给出「进入目录」入口。
// 竞态/取消/游标失效由 `hooks/workspace/use-file-browser.test.tsx` 的 hook 级测试覆盖，此处不重复。

import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RuntimeApiError } from "@/api/runtime/shared";
import { FS_SEARCH_DEBOUNCE_PANEL_MS, FS_SEARCH_LIMIT_PANEL } from "@/hooks/workspace/use-fs-search";
import type { FsRoot } from "@/types/runtime/fs-browser";

import {
  advanceDebounce,
  clickRow,
  degradedText,
  entry,
  filterInput,
  flush,
  mountSurface,
  noticeText,
  page,
  previewFixture,
  resultsVisible,
  rootFixture,
  rowByPath,
  searchItem,
  searchPage,
  searchRowButton,
  searchRows,
  setFilterValue,
  setupSurfaceDom,
  settle,
  teardownSurfaceDom,
} from "@/test/file-browser-surface-harness";

const { fetchFsListingMock, fetchFsPreviewMock, fetchFsRootsMock, fetchFsSearchMock } = vi.hoisted(() => ({
  fetchFsListingMock: vi.fn(),
  fetchFsPreviewMock: vi.fn(),
  fetchFsRootsMock: vi.fn(),
  fetchFsSearchMock: vi.fn(),
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

// 搜索/预览同样只替换请求函数：`isFsSearchUnavailable`（404/405/501/503 判据）必须走真实实现。
vi.mock("@/api/runtime/fs-search", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/fs-search")>();
  return { ...actual, fetchFsSearch: fetchFsSearchMock };
});

vi.mock("@/api/runtime/fs-preview", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/fs-preview")>();
  return { ...actual, fetchFsPreview: fetchFsPreviewMock };
});

// 默认预览响应：多数用例不关心预览；缺省实现（undefined）会让预览面板在 `.then` 上崩掉。
fetchFsPreviewMock.mockResolvedValue({ kind: "text", path: "", size: 0, truncated: false, text: "" });

describe("FileBrowserSurface 作用域与降级", () => {
  beforeEach(() => {
    setupSurfaceDom();
    fetchFsListingMock.mockReset();
    fetchFsRootsMock.mockReset();
  });

  afterEach(() => {
    teardownSurfaceDom();
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
    setupSurfaceDom();
    fetchFsListingMock.mockReset();
    fetchFsRootsMock.mockReset();
  });

  afterEach(() => {
    teardownSurfaceDom();
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
    setupSurfaceDom();
    fetchFsListingMock.mockReset();
    fetchFsRootsMock.mockReset();
  });

  afterEach(() => {
    teardownSurfaceDom();
  });

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

// P1-9/P1-10（规划 §4.7.3 合成规则 / §4.7.4 第 2 条 / §4.7.5 降级矩阵）：过滤词停顿后的全库搜索。
//
// 断言口径（只看用户可见的合成结果与真实请求参数，不假设 hook 内部实现）：
//   * 未到防抖窗口不发请求；空查询只做本地过滤、永不发请求；
//   * 结果就绪 → 结果视图替换目录树；空结果也给显式空态（不伪装成空的目录树）；
//   * 不可用（404/405/501/503）→ 退回本地过滤树 + 一次性提示，不给重试入口、不自动再发请求；
//   * 其它失败 → 可重试提示，重试成功后恢复结果视图；
//   * 搜索选中：`file` 用响应**直接构造 FsEntry** 预览（不等待树加载该目录），`dir` 进目录并退出搜索。
describe("FileBrowserSurface P1-9：停顿后全库搜索、结果视图与降级", () => {
  beforeEach(() => {
    // 防抖窗口用 fake timers 精确推进（与 `use-fs-search.test.tsx` 同款口径）。
    vi.useFakeTimers();
    setupSurfaceDom();
    fetchFsListingMock.mockReset();
    fetchFsPreviewMock.mockReset();
    fetchFsRootsMock.mockReset();
    fetchFsSearchMock.mockReset();
  });

  afterEach(() => {
    teardownSurfaceDom();
    vi.useRealTimers();
  });

  /** 渲染 + fake timers 落定（roots 缺省为会话根）：渲染与落定口径见 `@/test/file-browser-surface-harness`。 */
  async function mountSearchableSurface(roots: FsRoot[] = [rootFixture()]) {
    fetchFsRootsMock.mockResolvedValue({ roots });
    await mountSurface();
    await settle();
  }

  it("停顿后按 §4.7.4 参数发一次 /fs/search，结果就绪时结果视图替换目录树", async () => {
    fetchFsListingMock.mockResolvedValue(page({ entries: [entry({ name: "a.ts", path: "a.ts" })] }));
    fetchFsSearchMock.mockResolvedValue(
      searchPage({
        items: [
          searchItem("src/found.ts", {
            match: { field: "name", start: 0, end: 5 },
          }),
        ],
      }),
    );

    await mountSearchableSurface();
    const input = filterInput();
    setFilterValue(input, "found");

    // 防抖窗口内不发请求（即时反馈仍由本地过滤承担）。
    await advanceDebounce(FS_SEARCH_DEBOUNCE_PANEL_MS - 1);
    expect(fetchFsSearchMock).not.toHaveBeenCalled();
    expect(resultsVisible()).toBe(false);

    await advanceDebounce(1);
    expect(fetchFsSearchMock).toHaveBeenCalledTimes(1);
    expect(fetchFsSearchMock.mock.calls[0][0]).toMatchObject({
      scope: "session:s1",
      query: "found",
      limit: FS_SEARCH_LIMIT_PANEL,
      kinds: "both",
      showHidden: false,
    });

    // 结果视图接管列表区：目录树让位，结果行带高亮。
    expect(resultsVisible()).toBe(true);
    expect(document.body.querySelector('[role="tree"]')).toBeNull();
    expect(searchRows()).toHaveLength(1);
    expect(searchRowButton("src/found.ts")).not.toBeNull();
    expect(document.body.querySelector('[data-testid="file-search-hit"]')?.textContent).toBe("found");
  });

  it("空查询只做本地过滤：清空后即使推进防抖也不发请求", async () => {
    fetchFsListingMock.mockResolvedValue(page({ entries: [entry({ name: "a.ts", path: "a.ts" })] }));
    await mountSearchableSurface();

    const input = filterInput();
    setFilterValue(input, "a");
    setFilterValue(input, "");
    await advanceDebounce(FS_SEARCH_DEBOUNCE_PANEL_MS * 2);

    expect(fetchFsSearchMock).not.toHaveBeenCalled();
    expect(resultsVisible()).toBe(false);
    expect(rowByPath("a.ts")).not.toBeNull();
  });

  it("空结果给显式空态，不伪装成空的目录树", async () => {
    fetchFsListingMock.mockResolvedValue(page({ entries: [entry({ name: "a.ts", path: "a.ts" })] }));
    fetchFsSearchMock.mockResolvedValue(searchPage({ items: [], scanned: 9 }));

    await mountSearchableSurface();
    setFilterValue(filterInput(), "nothing");
    await advanceDebounce();

    expect(resultsVisible()).toBe(true);
    expect(document.body.querySelector('[data-testid="file-search-empty"]')?.textContent ?? "").toMatch(
      /未找到匹配的文件或目录/,
    );
    expect(document.body.querySelector('[role="tree"]')).toBeNull();
  });

  it("截断（truncated）显式提示已扫描条目数", async () => {
    fetchFsListingMock.mockResolvedValue(page());
    fetchFsSearchMock.mockResolvedValue(
      searchPage({ items: [searchItem("src/a.ts")], scanned: 12, truncated: true }),
    );

    await mountSearchableSurface();
    setFilterValue(filterInput(), "a");
    await advanceDebounce();

    expect(document.body.querySelector('[data-testid="file-search-truncated"]')?.textContent ?? "").toContain("12");
  });

  it("has_more：加载更多按游标追加并按 path 去重", async () => {
    fetchFsListingMock.mockResolvedValue(page());
    fetchFsSearchMock
      .mockResolvedValueOnce(
        searchPage({ items: [searchItem("a.ts"), searchItem("b.ts")], hasMore: true, nextCursor: "cur-1" }),
      )
      .mockResolvedValueOnce(searchPage({ items: [searchItem("b.ts"), searchItem("c.ts")] }));

    await mountSearchableSurface();
    setFilterValue(filterInput(), "x");
    await advanceDebounce();
    expect(searchRows()).toHaveLength(2);

    const loadMore = [...document.body.querySelectorAll<HTMLButtonElement>('[data-testid="file-search-results"] button')].find(
      (button) => button.textContent?.includes("加载更多"),
    );
    expect(loadMore).not.toBeUndefined();
    act(() => {
      loadMore?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await settle();

    expect(fetchFsSearchMock).toHaveBeenCalledTimes(2);
    expect(fetchFsSearchMock.mock.calls[1][0]).toMatchObject({ cursor: "cur-1", query: "x" });
    // 追加而不是替换：重复的 b.ts 只保留一行。
    expect(searchRows()).toHaveLength(3);
    expect(searchRows().filter((row) => row.textContent?.includes("b.ts"))).toHaveLength(1);
  });

  it("不可用（404）→ 退回本地过滤树 + 一次性提示：不给重试入口，也不自动再发请求", async () => {
    fetchFsListingMock.mockResolvedValue(
      page({ entries: [entry({ name: "a.ts", path: "a.ts" }), entry({ name: "b.ts", path: "b.ts" })] }),
    );
    fetchFsSearchMock.mockRejectedValue(new RuntimeApiError(404, null));

    await mountSearchableSurface();
    setFilterValue(filterInput(), "a");
    await advanceDebounce();

    expect(fetchFsSearchMock).toHaveBeenCalledTimes(1);
    // 本地过滤仍在（a.ts 保留、b.ts 被过滤掉），目录树未被结果视图替换。
    expect(rowByPath("a.ts")).not.toBeNull();
    expect(rowByPath("b.ts")).toBeNull();
    expect(resultsVisible()).toBe(false);
    expect(degradedText()).toMatch(/远端搜索不可用，仅过滤已加载层/);

    const degraded = document.body.querySelector('[data-testid="file-search-degraded"]');
    expect([...(degraded?.querySelectorAll("button") ?? [])]).toHaveLength(0);

    // 不重试：继续推进定时器（未改查询）不得再发第二次请求。
    await advanceDebounce(FS_SEARCH_DEBOUNCE_PANEL_MS * 4);
    expect(fetchFsSearchMock).toHaveBeenCalledTimes(1);
  });

  it("其它失败（500）→ 提示可重试，重试成功后恢复结果视图", async () => {
    fetchFsListingMock.mockResolvedValue(page({ entries: [entry({ name: "a.ts", path: "a.ts" })] }));
    fetchFsSearchMock
      .mockRejectedValueOnce(new RuntimeApiError(500, null))
      .mockResolvedValueOnce(searchPage({ items: [searchItem("src/a.ts")] }));

    await mountSearchableSurface();
    setFilterValue(filterInput(), "a");
    await advanceDebounce();

    expect(resultsVisible()).toBe(false);
    expect(degradedText()).toContain("500");

    const retry = document.body.querySelector<HTMLButtonElement>('[data-testid="file-search-degraded"] button');
    expect(retry).not.toBeNull();
    act(() => {
      retry?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await settle();

    expect(fetchFsSearchMock).toHaveBeenCalledTimes(2);
    expect(resultsVisible()).toBe(true);
    expect(searchRowButton("src/a.ts")).not.toBeNull();
  });

  it("搜索结果选中 file：用响应直接构造 FsEntry 预览，不等待树加载该目录", async () => {
    fetchFsListingMock.mockResolvedValue(page({ entries: [entry({ name: "a.ts", path: "a.ts" })] }));
    fetchFsSearchMock.mockResolvedValue(searchPage({ items: [searchItem("deep/nested/found.ts")], scanned: 3 }));
    fetchFsPreviewMock.mockResolvedValue(previewFixture());

    await mountSearchableSurface();
    setFilterValue(filterInput(), "found");
    await advanceDebounce();

    const listingCallsBefore = fetchFsListingMock.mock.calls.length;
    act(() => {
      searchRowButton("deep/nested/found.ts")?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await settle();

    // 预览请求按 `scope + 响应里的 path` 直接发出：没有为 `deep/nested` 触发任何列表请求。
    expect(fetchFsPreviewMock).toHaveBeenCalledTimes(1);
    expect(fetchFsPreviewMock.mock.calls[0][0]).toEqual({ scope: "session:s1", path: "deep/nested/found.ts" });
    expect(fetchFsListingMock.mock.calls.length).toBe(listingCallsBefore);
    expect(document.body.querySelector('[data-testid="file-browser-preview"]')).not.toBeNull();
    expect(document.body.textContent ?? "").toContain("const found = 1;");
  });

  it("搜索结果选中 dir：进入目录并退出搜索（清空查询、回到树视图）", async () => {
    fetchFsListingMock.mockResolvedValue(page({ entries: [entry({ name: "sub.ts", path: "src/sub/sub.ts" })] }));
    fetchFsSearchMock.mockResolvedValue(searchPage({ items: [searchItem("src/sub", { type: "dir" })], scanned: 2 }));

    await mountSearchableSurface();
    setFilterValue(filterInput(), "sub");
    await advanceDebounce();
    expect(resultsVisible()).toBe(true);

    act(() => {
      searchRowButton("src/sub")?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await settle();

    expect(
      fetchFsListingMock.mock.calls.some((call) => (call[0] as { path?: string }).path === "src/sub"),
    ).toBe(true);
    expect(filterInput().value).toBe("");
    expect(resultsVisible()).toBe(false);
    expect(document.body.querySelector('[role="tree"]')).not.toBeNull();
  });

  it("scope 切换：清空查询与上次结果，回到树视图", async () => {
    fetchFsListingMock.mockResolvedValue(page({ entries: [entry({ name: "a.ts", path: "a.ts" })] }));
    fetchFsSearchMock.mockResolvedValue(searchPage({ items: [searchItem("src/a.ts")] }));

    await mountSearchableSurface([
      rootFixture(),
      rootFixture({ scope: "workspace:w1", kind: "workspace", name: "other", path: "E:/other" }),
    ]);
    setFilterValue(filterInput(), "a");
    await advanceDebounce();
    expect(resultsVisible()).toBe(true);

    const scopeSelect = document.body.querySelector<HTMLSelectElement>("#file-browser-scope");
    expect(scopeSelect).not.toBeNull();
    const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")?.set;
    act(() => {
      setter?.call(scopeSelect, "workspace:w1");
      scopeSelect?.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();

    expect(filterInput().value).toBe("");
    expect(resultsVisible()).toBe(false);
    expect(document.body.querySelector('[role="tree"]')).not.toBeNull();
    const lastListing = fetchFsListingMock.mock.calls.at(-1)?.[0] as { scope?: string } | undefined;
    expect(lastListing?.scope).toBe("workspace:w1");
  });
});
