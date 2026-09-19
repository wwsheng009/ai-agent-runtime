// 文件管理器页签模型（纯函数，便于单测）：文件浏览器升级为「多页签文件管理器」——
// 根页签「文件浏览器」常驻，点击文件会追加「文件」页签并激活（/文件浏览器/文件1/文件2 …）。
//
// 归一化纪律：
//   * 页签身份 = 打开瞬间的 `scope + path` 快照（`buildFileTabId`）。切作用域不清已打开的页签，
//     预览请求始终按快照的 scope/path 走，与「传输托盘带 scope+dir 快照」同口径；
//   * 同一文件重复打开只聚焦已有页签，不产生重复项；
//   * 关闭活动页签时回落到右邻（无右邻则左邻；全部关闭回到根页签）；
//   * 根页签（`FILE_MANAGER_BROWSER_TAB_ID`）不可关闭，select 忽略未知 id（不猜激活目标）。

import type { FsEntry } from "@/types/runtime/fs-browser";

/** 根页签（文件浏览器）的身份；文件页签用 `buildFileTabId` 生成。 */
export const FILE_MANAGER_BROWSER_TAB_ID = "browser";

export type FileManagerFileTab = {
  /** 页签身份：`buildFileTabId(scope, path)`。 */
  id: string;
  /** 打开瞬间的作用域快照（预览请求 / 重复打开判重用）。 */
  scope: string;
  /** 相对作用域根的路径。 */
  path: string;
  /** 页签标题（文件名）。 */
  name: string;
  /** 打开瞬间的完整条目快照（元信息 / 下载用；预览正文始终按 scope+path 重新取数）。 */
  entry: FsEntry;
};

export type FileManagerTabsState = {
  /** 已打开的文件页签（按打开顺序追加；根页签不在数组里）。 */
  tabs: FileManagerFileTab[];
  /** 当前激活页签 id：`FILE_MANAGER_BROWSER_TAB_ID` 或某个文件页签 id。 */
  activeId: string;
};

export const emptyFileManagerTabs: FileManagerTabsState = {
  tabs: [],
  activeId: FILE_MANAGER_BROWSER_TAB_ID,
};

export function buildFileTabId(scope: string, path: string): string {
  return `${scope}\u0000${path}`;
}

/** 目录 / 无权限项不可作为文件页签打开（与预览语义一致：不点开空面板）。 */
export function canOpenFileTab(entry: FsEntry): boolean {
  return entry.type !== "dir" && entry.type !== "inaccessible";
}

export function isBrowserActive(state: FileManagerTabsState): boolean {
  return state.activeId === FILE_MANAGER_BROWSER_TAB_ID;
}

export function activeFileTab(state: FileManagerTabsState): FileManagerFileTab | null {
  return state.tabs.find((tab) => tab.id === state.activeId) ?? null;
}

/** 打开（或聚焦）一个文件页签；目录项应先用 `canOpenFileTab` 过滤。 */
export function openFileTab(
  state: FileManagerTabsState,
  file: FileManagerFileTab,
): FileManagerTabsState {
  if (state.tabs.some((tab) => tab.id === file.id)) {
    return { tabs: state.tabs, activeId: file.id };
  }
  return { tabs: [...state.tabs, file], activeId: file.id };
}

/** 切换激活页签；未知 id（既非根页签也非已打开文件）保持原状。 */
export function selectFileTab(state: FileManagerTabsState, id: string): FileManagerTabsState {
  if (id === FILE_MANAGER_BROWSER_TAB_ID || state.tabs.some((tab) => tab.id === id)) {
    return { ...state, activeId: id };
  }
  return state;
}

/** 关闭一个文件页签；根页签不可关。关闭活动页签时回落到右邻，其次左邻，最后根页签。 */
export function closeFileTab(state: FileManagerTabsState, id: string): FileManagerTabsState {
  if (id === FILE_MANAGER_BROWSER_TAB_ID) {
    return state;
  }
  const index = state.tabs.findIndex((tab) => tab.id === id);
  if (index < 0) {
    return state;
  }
  const next = state.tabs.filter((tab) => tab.id !== id);
  if (state.activeId !== id) {
    return { tabs: next, activeId: state.activeId };
  }
  const fallback = next[index] ?? next[index - 1];
  return { tabs: next, activeId: fallback?.id ?? FILE_MANAGER_BROWSER_TAB_ID };
}