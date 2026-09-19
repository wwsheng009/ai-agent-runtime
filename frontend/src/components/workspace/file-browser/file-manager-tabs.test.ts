// 文件管理器页签模型（纯函数）的单测：身份判重、快照隔离、关闭回落、不可关根页签。

import { describe, expect, it } from "vitest";

import type { FsEntry } from "@/types/runtime/fs-browser";

import {
  FILE_MANAGER_BROWSER_TAB_ID,
  activeFileTab,
  buildFileTabId,
  canOpenFileTab,
  closeFileTab,
  emptyFileManagerTabs,
  isBrowserActive,
  openFileTab,
  selectFileTab,
  type FileManagerFileTab,
} from "./file-manager-tabs";

function entry(partial: Partial<FsEntry> & { name: string; path: string }): FsEntry {
  return { type: "file", size: 12, mtime: 0, ...partial };
}

function fileTab(scope: string, name: string, path = name): FileManagerFileTab {
  return { id: buildFileTabId(scope, path), scope, path, name, entry: entry({ name, path }) };
}

describe("file-manager-tabs", () => {
  it("初始状态只有根页签（文件浏览器），不可关闭", () => {
    expect(emptyFileManagerTabs).toEqual({
      tabs: [],
      activeId: FILE_MANAGER_BROWSER_TAB_ID,
    });
    expect(isBrowserActive(emptyFileManagerTabs)).toBe(true);
    expect(activeFileTab(emptyFileManagerTabs)).toBeNull();
    expect(closeFileTab(emptyFileManagerTabs, FILE_MANAGER_BROWSER_TAB_ID)).toBe(
      emptyFileManagerTabs,
    );
  });

  it("打开文件追加页签并激活，不改写原状态（不可变）", () => {
    const first = fileTab("workspace:w1", "a.ts", "src/a.ts");
    const next = openFileTab(emptyFileManagerTabs, first);

    expect(emptyFileManagerTabs.tabs).toHaveLength(0);
    expect(next.tabs).toEqual([first]);
    expect(next.activeId).toBe(first.id);
    expect(isBrowserActive(next)).toBe(false);
    expect(activeFileTab(next)).toBe(first);
  });

  it("同一 scope+path 重复打开只聚焦既有页签，不产生重复项", () => {
    const first = fileTab("workspace:w1", "a.ts", "src/a.ts");
    const second = openFileTab(openFileTab(emptyFileManagerTabs, first), {
      ...first,
      entry: entry({ name: "a.ts", path: "src/a.ts", size: 999 }),
    });

    expect(second.tabs).toHaveLength(1);
    expect(second.activeId).toBe(first.id);
    // 聚焦语义：不刷新既有快照（预览正文按 scope+path 重新取数）。
    expect(second.tabs[0]?.entry.size).toBe(12);
  });

  it("不同 scope 的同名文件是不同页签（身份为 scope+path 快照）", () => {
    const a = fileTab("workspace:w1", "a.ts", "src/a.ts");
    const b = fileTab("workspace:w2", "a.ts", "src/a.ts");
    const next = openFileTab(openFileTab(emptyFileManagerTabs, a), b);

    expect(next.tabs).toHaveLength(2);
    expect(next.activeId).toBe(b.id);
    expect(a.id).not.toBe(b.id);
  });

  it("关闭非活动页签保持当前激活项", () => {
    const a = fileTab("s", "a.ts");
    const b = fileTab("s", "b.ts");
    const state = openFileTab(openFileTab(emptyFileManagerTabs, a), b);
    const next = closeFileTab(state, a.id);

    expect(next.tabs.map((tab) => tab.name)).toEqual(["b.ts"]);
    expect(next.activeId).toBe(b.id);
  });

  it("关闭活动页签依次回落到右邻 → 左邻 → 根页签", () => {
    const a = fileTab("s", "a.ts");
    const b = fileTab("s", "b.ts");
    const c = fileTab("s", "c.ts");
    let state = openFileTab(openFileTab(openFileTab(emptyFileManagerTabs, a), b), c);

    // 关 b（活动）→ 右邻 c。
    state = selectFileTab(state, b.id);
    state = closeFileTab(state, b.id);
    expect(state.activeId).toBe(c.id);

    // 关 c（活动）→ 无右邻则左邻 a。
    state = closeFileTab(state, c.id);
    expect(state.activeId).toBe(a.id);

    // 关最后一个 → 回根页签。
    state = closeFileTab(state, a.id);
    expect(state).toEqual({ tabs: [], activeId: FILE_MANAGER_BROWSER_TAB_ID });
  });

  it("select 未知 id 保持原状；可切回根页签", () => {
    const a = fileTab("s", "a.ts");
    const state = openFileTab(emptyFileManagerTabs, a);

    expect(selectFileTab(state, "missing")).toBe(state);
    expect(selectFileTab(state, FILE_MANAGER_BROWSER_TAB_ID).activeId).toBe(
      FILE_MANAGER_BROWSER_TAB_ID,
    );
  });

  it("canOpenFileTab：目录/无权限不可开，文件/软链/未知类型可开", () => {
    expect(canOpenFileTab(entry({ name: "src", path: "src", type: "dir" }))).toBe(false);
    expect(
      canOpenFileTab(entry({ name: "secret", path: "secret", type: "inaccessible" })),
    ).toBe(false);
    expect(canOpenFileTab(entry({ name: "a.ts", path: "a.ts" }))).toBe(true);
    expect(canOpenFileTab(entry({ name: "link", path: "link", type: "symlink" }))).toBe(true);
    expect(canOpenFileTab(entry({ name: "weird", path: "weird", type: "unknown" }))).toBe(true);
  });

  it("buildFileTabId 以 scope 与 path 的二元组为身份（分隔符防歧义）", () => {
    expect(buildFileTabId("s", "src/a.ts")).toBe(buildFileTabId("s", "src/a.ts"));
    expect(buildFileTabId("s", "src/a.ts")).not.toBe(buildFileTabId("s2", "src/a.ts"));
    // 朴素拼接会撞车的两组输入必须可区分。
    expect(buildFileTabId("a", "b/c")).not.toBe(buildFileTabId("a/b", "c"));
  });
});
