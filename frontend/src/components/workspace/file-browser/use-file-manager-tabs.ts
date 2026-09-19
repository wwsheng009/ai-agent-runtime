// 文件管理器页签的 React 侧接线（开/关/切换 + 页签条数据）。
//
// 与 `file-manager-tabs.ts` 的分工：那里是纯函数状态机（无 React 依赖，可单测）；
// 这里只做 state / useCallback / useMemo / i18n 文案，供「文件浏览器面」消费。
import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import type { WorkspaceTabStripItem } from "@/components/ui/tab-strip";
import {
  activeFileTab,
  buildFileTabId,
  canOpenFileTab,
  closeFileTab,
  emptyFileManagerTabs,
  FILE_MANAGER_BROWSER_TAB_ID,
  isBrowserActive,
  openFileTab,
  selectFileTab,
  type FileManagerFileTab,
  type FileManagerTabsState,
} from "@/components/workspace/file-browser/file-manager-tabs";

import type { FsEntry } from "@/types/runtime/fs-browser";

export function useFileManagerTabs(scope: string) {
  const { t } = useTranslation("workspace");
  /** 页签状态：根页签常驻 + 已打开文件页签；开/关/切换语义见 `file-manager-tabs.ts`。 */
  const [managerTabs, setManagerTabs] = useState<FileManagerTabsState>(emptyFileManagerTabs);

  /**
   * 打开（或聚焦）一个文件页签：同一 scope+path 只保留一个，重复点击 = 激活已有页签。
   * scope 取点击瞬间的值（快照），切作用域不清已打开的页签（与传输托盘同口径）。
   */
  const openFile = useCallback(
    (entry: FsEntry) => {
      if (!canOpenFileTab(entry)) {
        return;
      }
      const fileTab: FileManagerFileTab = {
        id: buildFileTabId(scope, entry.path),
        scope,
        path: entry.path,
        name: entry.name,
        entry,
      };
      setManagerTabs((current) => openFileTab(current, fileTab));
    },
    [scope],
  );

  const selectTab = useCallback((id: string) => {
    setManagerTabs((current) => selectFileTab(current, id));
  }, []);

  const closeTab = useCallback((id: string) => {
    setManagerTabs((current) => closeFileTab(current, id));
  }, []);

  // 页签条数据：根页签「文件浏览器」常驻，其后是按打开顺序追加的可关闭文件页签。
  const tabItems: WorkspaceTabStripItem[] = useMemo(
    () => [
      { id: FILE_MANAGER_BROWSER_TAB_ID, label: t("panels.fileBrowser.manager.browserTab") },
      ...managerTabs.tabs.map((tab) => ({
        id: tab.id,
        label: tab.name,
        title: tab.path,
        closable: true,
      })),
    ],
    [managerTabs.tabs, t],
  );

  return {
    activeId: managerTabs.activeId,
    activeFile: activeFileTab(managerTabs),
    browserActive: isBrowserActive(managerTabs),
    closeTab,
    openFile,
    selectTab,
    tabItems,
  };
}
