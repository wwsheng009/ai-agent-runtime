// @vitest-environment jsdom

// P1-2：目录树虚拟列表单测（行投影 / 游离态与「加载更多」合成行 / a11y 计数 / unknown 不当目录）。
//
// 断言纪律：虚拟滚动本身由 `computeVirtualWindow` 的固定行高契约保证（另有纯函数单测），这里只断言
// 「投影后的行序 + 每行的 aria-setsize/posinset 与真实兄弟数一致」，以及截断提示必须出现在展开层上。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { FileBrowserListing } from "@/hooks/workspace/use-file-browser";
import { buildTreeRows } from "@/lib/file-browser/entry-sort";
import type { GitBadgeIndex } from "@/lib/file-browser/git-badge";
import { buildRenderRows } from "@/lib/file-browser/tree-rows";
import type { FsEntry, FsEntryType } from "@/types/runtime/fs-browser";

import { FileTreeList, type FileTreeListProps } from "./tree-list";

type ReactActEnvironmentGlobal = typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean };

function entry(partial: Partial<FsEntry> & { name: string; path: string }): FsEntry {
  return { type: "file", size: 10, mtime: 0, ...partial };
}

function listing(entries: FsEntry[], overrides: Partial<FileBrowserListing> = {}): FileBrowserListing {
  return {
    status: "ready",
    entries,
    nextCursor: null,
    hasMore: false,
    truncated: false,
    error: null,
    cursorExpired: false,
    loadedAt: Date.now(),
    ...overrides,
  };
}

function project(source: Record<string, FsEntry[]>, expanded: string[], listings: Record<string, FileBrowserListing>) {
  const rows = buildTreeRows({
    source,
    expanded: new Set(expanded),
    sortKey: "type_then_name",
    filter: { showHidden: false },
  });
  return buildRenderRows({ rows, expanded: new Set(expanded), listings });
}

describe("buildRenderRows 行投影", () => {
  it("有下一页的目录行后追加「加载更多」合成行，并把同层 aria-setsize 一并 +1", () => {
    const source = { "": [entry({ name: "src", path: "src", type: "dir" })], src: [entry({ name: "a.ts", path: "src/a.ts" })] };
    const listings = {
      "": listing(source[""], { hasMore: true, nextCursor: "c1" }),
      src: listing(source.src, { hasMore: true, nextCursor: "c2" }),
    };
    const rows = project(source, ["src"], listings);
    const shapes = rows.map((item) => (item.kind === "status" ? `status:${item.status}` : item.row.entry.path));
    expect(shapes).toEqual(["src", "src/a.ts", "status:more", "status:more"]);
    const dirRow = rows[0];
    const childRow = rows[1];
    const srcStatus = rows[2];
    expect(dirRow.kind === "entry" && dirRow.row.setSize).toBe(2);
    expect(childRow.kind === "entry" && childRow.row.setSize).toBe(2);
    expect(srcStatus.kind === "status" && srcStatus.setSize).toBe(2);
    expect(srcStatus.kind === "status" && srcStatus.posInSet).toBe(2);
    expect(srcStatus.kind === "status" && srcStatus.path).toBe("src");
  });

  it("展开但列表失效（刷新丢缓存）→ 游离态合成行；未展开目录不生成任何合成行", () => {
    const source = { "": [entry({ name: "src", path: "src", type: "dir" })] };
    const rows = project(source, ["src"], { "": listing(source[""]) });
    expect(rows.map((item) => (item.kind === "status" ? `status:${item.status}` : item.row.entry.path))).toEqual(["src", "status:orphan"]);
    // 合成行是「该目录这一层」的最后一个兄弟，因此 +1 只影响该层的子行；目录行自身的 setSize 不变。
    expect(rows[0].kind === "entry" && rows[0].row.setSize).toBe(1);
    expect(rows[1].kind === "status" && rows[1].level).toBe(2);
    expect(rows[1].kind === "status" && rows[1].setSize).toBe(1);

    const collapsed = project(source, [], { "": listing(source[""]) });
    expect(collapsed.map((item) => (item.kind === "status" ? `status:${item.status}` : item.row.entry.path))).toEqual(["src"]);
  });

  it("loading_more 状态渲染为「loading」合成行（不重复挂载按钮）", () => {
    const source = { "": [entry({ name: "src", path: "src", type: "dir" })], src: [entry({ name: "a.ts", path: "src/a.ts" })] };
    const rows = project(source, ["src"], {
      "": listing(source[""]),
      src: listing(source.src, { hasMore: true, nextCursor: "c2", status: "loading_more" }),
    });
    expect(rows.filter((item) => item.kind === "status").map((item) => item.kind === "status" && item.status)).toEqual(["loading"]);
  });
});

describe("FileTreeList 渲染与 a11y", () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    root = null;
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderList(overrides: Partial<FileTreeListProps> = {}) {
    const props: FileTreeListProps = {
      listings: {},
      expanded: new Set<string>(),
      selectedPath: null,
      sortKey: "type_then_name",
      showHidden: false,
      onToggleDir: vi.fn(),
      onSelectEntry: vi.fn(),
      onEnterDir: vi.fn(),
      onLoadMore: vi.fn(),
      onReload: vi.fn(),
      onDownload: vi.fn(),
      ...overrides,
    };
    act(() => {
      root?.render(<FileTreeList {...props} />);
    });
    return props;
  }

  function rowByPath(path: string) {
    const rows = [...document.body.querySelectorAll('[role="treeitem"]')];
    return rows.find((row) => row.querySelector(`button[title="${path}"]`)) ?? null;
  }

  it("目录行带 aria-expanded/level/posinset/setsize，未知类型不当目录进入", () => {
    const entries: FsEntry[] = [
      entry({ name: "src", path: "src", type: "dir" }),
      entry({ name: "weird", path: "weird", type: "unknown" as FsEntryType }),
    ];
    renderList({
      listings: { "": listing(entries) },
      expanded: new Set<string>(),
    });
    const tree = document.body.querySelector('[data-testid="file-browser-tree"]');
    expect(tree?.getAttribute("role")).toBe("tree");
    expect(tree?.getAttribute("aria-activedescendant")).toBe("file-tree-row-0");

    const dirRow = rowByPath("src");
    expect(dirRow?.getAttribute("aria-expanded")).toBe("false");
    expect(dirRow?.getAttribute("aria-level")).toBe("1");
    expect(dirRow?.getAttribute("aria-posinset")).toBe("1");
    expect(dirRow?.getAttribute("aria-setsize")).toBe("2");
    expect(dirRow?.querySelectorAll("button")).toHaveLength(2);

    const unknownRow = rowByPath("weird");
    expect(unknownRow?.getAttribute("aria-expanded")).toBeNull();
    expect(unknownRow?.querySelectorAll("button")).toHaveLength(1);
  });

  it("游离态行给出「重新加载该层」入口，截断层在展开目录行上显式提示", () => {
    const onReload = vi.fn();
    const entries = [entry({ name: "src", path: "src", type: "dir" })];
    // 1) 展开但没有缓存 → 游离态合成行，点击即重新加载该层。
    renderList({ listings: { "": listing(entries) }, expanded: new Set(["src"]), onReload });
    const orphanRow = document.body.querySelector('[role="treeitem"][aria-level="2"]');
    expect(orphanRow?.textContent ?? "").toMatch(/orphan|游离|重新加载/);
    const action = orphanRow?.querySelector("button");
    expect(action).not.toBeNull();
    act(() => {
      action?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onReload).toHaveBeenCalledWith("src");

    // 2) 该层被服务端截断 → 展开目录行上显式提示「仅显示前 N 项」。
    renderList({
      listings: { "": listing(entries), src: listing([entry({ name: "a.ts", path: "src/a.ts" })], { truncated: true }) },
      expanded: new Set(["src"]),
    });
    const dirRow = rowByPath("src");
    expect(dirRow?.textContent ?? "").toMatch(/truncated|仅显示前/);
  });

  it("展开层有下一页时点击「加载更多」回调指向该层，且不在 loading 时重复挂载按钮", () => {
    const onLoadMore = vi.fn();
    const entries = [entry({ name: "src", path: "src", type: "dir" })];
    renderList({
      listings: { "": listing(entries), src: listing([entry({ name: "a.ts", path: "src/a.ts" })], { hasMore: true, nextCursor: "c2" }) },
      expanded: new Set(["src"]),
      onLoadMore,
    });
    const statusRow = [...document.body.querySelectorAll('[role="treeitem"][aria-level="2"]')].at(-1);
    const button = statusRow?.querySelector("button");
    expect(button).not.toBeNull();
    act(() => {
      button?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onLoadMore).toHaveBeenCalledWith("src");

    renderList({
      listings: { "": listing(entries), src: listing([entry({ name: "a.ts", path: "src/a.ts" })], { hasMore: true, nextCursor: "c2", status: "loading_more" }) },
      expanded: new Set(["src"]),
    });
    const loadingRow = [...document.body.querySelectorAll('[role="treeitem"][aria-level="2"]')].at(-1);
    expect(loadingRow?.querySelector("button")).toBeNull();
  });
});

// P4：Git 角标 / 多选（Ctrl 切换、Shift 范围）/ 右键菜单。角标只断言 DOM 契约（data-*），
// 文案与色调由 `change-list.tsx` 的单测保证；范围选择断言「按可见行顺序切片」。
describe("FileTreeList P4：Git 角标、多选与右键菜单", () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    root = null;
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  /** 展开 src 后可见行序：src → src/a.ts → b.ts → c.ts（范围选择按这个顺序切片）。 */
  const expandedListings = {
    "": listing([entry({ name: "src", path: "src", type: "dir" }), entry({ name: "b.ts", path: "b.ts" }), entry({ name: "c.ts", path: "c.ts" })]),
    src: listing([entry({ name: "a.ts", path: "src/a.ts" })]),
  };

  function renderList(overrides: Partial<FileTreeListProps> = {}) {
    const props: FileTreeListProps = {
      listings: expandedListings,
      expanded: new Set<string>(["src"]),
      selectedPath: null,
      sortKey: "type_then_name",
      showHidden: false,
      onToggleDir: vi.fn(),
      onSelectEntry: vi.fn(),
      onEnterDir: vi.fn(),
      onLoadMore: vi.fn(),
      onReload: vi.fn(),
      onDownload: vi.fn(),
      ...overrides,
    };
    act(() => {
      root?.render(<FileTreeList {...props} />);
    });
    return props;
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

  function badgeOf(path: string) {
    return rowByPath(path)?.querySelector('[data-testid="file-tree-git-badge"]') ?? null;
  }

  it("角标按索引渲染：未跟踪显示 ?，M 显示 M，未命中索引的行不渲染角标", () => {
    const gitBadges: GitBadgeIndex = new Map([
      ["src/a.ts", { group: "unstaged", status: ".M" }],
      ["c.ts", { group: "untracked", status: "??" }],
    ]);
    renderList({ gitBadges });
    expect(badgeOf("src/a.ts")?.getAttribute("data-git-badge")).toBe("M");
    expect(badgeOf("src/a.ts")?.textContent).toBe("M");
    expect(badgeOf("c.ts")?.getAttribute("data-git-badge")).toBe("untracked");
    expect(badgeOf("c.ts")?.textContent).toBe("?");
    expect(badgeOf("b.ts")).toBeNull();
  });

  it("gitBadges 为 null（非仓库 / 未映射）时整表不渲染角标，也不报错", () => {
    renderList({ gitBadges: null });
    expect(document.body.querySelectorAll('[data-testid="file-tree-git-badge"]')).toHaveLength(0);
    expect(rowByPath("b.ts")).not.toBeNull();
  });

  it("Ctrl 点击 = toggle、Shift 点击按可见行顺序给 range、Escape 清空选择", () => {
    const onSelectEntry = vi.fn();
    const onClearSelection = vi.fn();
    renderList({
      onSelectEntry,
      onClearSelection,
      selectedPaths: new Set(["src/a.ts"]),
      selectionAnchor: "src/a.ts",
    });
    const tree = document.body.querySelector('[data-testid="file-browser-tree"]');
    expect(tree?.getAttribute("aria-multiselectable")).toBe("true");
    expect(rowByPath("src/a.ts")?.getAttribute("aria-selected")).toBe("true");
    expect(rowByPath("b.ts")?.getAttribute("aria-selected")).toBe("false");

    clickRow("b.ts", { ctrlKey: true });
    expect(onSelectEntry).toHaveBeenLastCalledWith(expect.objectContaining({ path: "b.ts" }), "toggle");

    clickRow("c.ts", { shiftKey: true });
    expect(onSelectEntry).toHaveBeenLastCalledWith(expect.objectContaining({ path: "c.ts" }), "range", ["src/a.ts", "b.ts", "c.ts"]);

    act(() => {
      tree?.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Escape" }));
    });
    expect(onClearSelection).toHaveBeenCalledTimes(1);
  });

  it("没有锚点时 Shift 点击退化为单选（不猜范围）", () => {
    const onSelectEntry = vi.fn();
    renderList({ onSelectEntry });
    clickRow("b.ts", { shiftKey: true });
    expect(onSelectEntry).toHaveBeenLastCalledWith(expect.objectContaining({ path: "b.ts" }), "replace");
  });

  it("右键行回调带坐标并阻断默认菜单；未传回调时不拦截", () => {
    const onRowContextMenu = vi.fn();
    renderList({ onRowContextMenu });
    const event = new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 120, clientY: 40 });
    act(() => {
      rowByPath("b.ts")?.dispatchEvent(event);
    });
    expect(onRowContextMenu).toHaveBeenCalledWith(expect.objectContaining({ path: "b.ts" }), { x: 120, y: 40 });
    expect(event.defaultPrevented).toBe(true);

    renderList({});
    const bare = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
    act(() => {
      rowByPath("b.ts")?.dispatchEvent(bare);
    });
    expect(bare.defaultPrevented).toBe(false);
  });
});

describe("FileTreeList 已加载层过滤（P4-5/Q3）", () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  const source: Record<string, FsEntry[]> = {
    "": [
      entry({ name: "src", path: "src", type: "dir" }),
      entry({ name: "b.ts", path: "b.ts" }),
      entry({ name: "c.ts", path: "c.ts" }),
    ],
    src: [entry({ name: "a.ts", path: "src/a.ts" })],
  };
  const listings = { "": listing(source[""]), src: listing(source.src) };

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    root = null;
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function render(filterText?: string) {
    act(() => {
      root?.render(
        <FileTreeList
          expanded={new Set<string>()}
          filterText={filterText}
          listings={listings}
          onDownload={vi.fn()}
          onEnterDir={vi.fn()}
          onLoadMore={vi.fn()}
          onReload={vi.fn()}
          onSelectEntry={vi.fn()}
          onToggleDir={vi.fn()}
          selectedPath={null}
          sortKey="type_then_name"
          showHidden={false}
        />,
      );
    });
  }

  // 目录行同时有「展开/折叠」按钮（title=展开 src），因此按候选路径反查，不取第一个 title。
  const CANDIDATE_PATHS = ["src", "src/a.ts", "b.ts", "c.ts"];
  function visiblePaths() {
    const rows = [...document.body.querySelectorAll('[role="treeitem"]')];
    return CANDIDATE_PATHS.filter((path) =>
      rows.some((row) => row.querySelector(`button[title="${path}"]`)),
    );
  }

  it("过滤只作用于已加载层：仅保留命中项与「子树命中」的目录，并临时展开已加载子层", () => {
    render("a.ts");
    expect(visiblePaths()).toEqual(["src", "src/a.ts"]);
    const dir = document.body.querySelector('[role="treeitem"] button[title="src"]')?.closest('[role="treeitem"]');
    expect(dir?.getAttribute("aria-expanded")).toBe("true");

    // 未过滤时目录默认折叠，且同级文件都在（确认上面的收窄来自过滤，而不是投影本身）。
    render();
    expect(visiblePaths()).toEqual(["src", "b.ts", "c.ts"]);
  });

  it("无命中时给出「没有匹配…」而不是冒充空目录；空白过滤词等于不过滤", () => {
    render("zzz");
    const empty = document.body.querySelector('[data-testid="file-tree-empty"]');
    expect(empty?.textContent).toContain("zzz");
    expect(empty?.textContent ?? "").toMatch(/没有匹配|No loaded entries/);
    expect(visiblePaths()).toEqual([]);

    render("   ");
    expect(document.body.querySelector('[data-testid="file-tree-empty"]')).toBeNull();
    expect(visiblePaths()).toEqual(["src", "b.ts", "c.ts"]);
  });
});
