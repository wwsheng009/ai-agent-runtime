// 面板「全库搜索」的视图状态（P1-9/P1-10，规划 §4.7.3/§4.7.4）——从 `file-browser-surface.tsx` 抽出，
// 让面组件只做「树 ↔ 结果」的合成与布局，搜索自身的接线/降级判据只有一份。
//
// 纪律：
//   * 查询 = `filterText.trim()`；空查询只做本地过滤、**不发请求**（既有即时语义不变）；
//   * 面板固定 `kinds="both"`（需要目录结果）/ `limit=50` / 防抖 300ms；`showHidden` 变化即重发；
//   * 合成规则（§4.7.3）：远端就绪（含空结果）或「pending 但已有上次结果」→ 结果视图；
//     其余（空查询 / 首次 pending / 失败 / 不可用）→ 本地过滤后的树 + 提示；
//   * 降级（§4.7.5）：`unavailable` 由 hook 判定（404/405/501/503），不重试；其它失败给可重试入口；
//   * 选中来源收口：搜索选中自带 `FsEntry`（不依赖树加载），与树选中互斥。
import { useCallback, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  type FileSearchResultsLabels,
} from "@/components/workspace/file-browser/search-results";
import {
  FS_SEARCH_DEBOUNCE_PANEL_MS,
  FS_SEARCH_LIMIT_PANEL,
  useFsSearchQuery,
  type UseFsSearchQueryResult,
} from "@/hooks/workspace/use-fs-search";

import type { FsEntry, FsSearchItem } from "@/types/runtime/fs-browser";

/** 选中来源（§4.7.4）：预览只吃 `FsEntry`，`source` 决定该选中是否点亮搜索结果视图。 */
export type FileBrowserSelection = { entry: FsEntry; source: "tree" | "search" };

/** 搜索结果 → `FsEntry`（`path/type/size/mtime/ext` 均来自响应，不依赖树是否加载过该目录）。 */
export function searchItemToEntry(item: FsSearchItem): FsEntry {
  return {
    name: item.name,
    path: item.path,
    type: item.type,
    size: item.size,
    mtime: item.mtime,
    ...(item.ext ? { ext: item.ext } : {}),
  };
}

/**
 * 预览来源收口（§4.7.4）：搜索选中优先（它自带 entry，取不到树也能预览）；
 * 否则退回树主选中项，且多选时不猜要看哪个文件（>1 个选中 → 不显示预览）。
 */
export function pickBrowserSelection(params: {
  entriesByPath: ReadonlyMap<string, FsEntry>;
  primaryPath: string | null;
  searchSelection: FsEntry | null;
  selectedPaths: readonly string[];
}): FileBrowserSelection | null {
  const { entriesByPath, primaryPath, searchSelection, selectedPaths } = params;
  if (searchSelection) {
    return { entry: searchSelection, source: "search" };
  }
  if (selectedPaths.length === 1 && primaryPath) {
    const hit = entriesByPath.get(primaryPath);
    if (hit) {
      return { entry: hit, source: "tree" };
    }
  }
  return null;
}

export type UseBrowserSearchOptions = {
  scope: string;
  /** 过滤词（受控在 owner）：trim 后作为查询；空查询只做本地过滤。 */
  filterText: string;
  showHidden: boolean;
  /** 目录结果的落点（owner 注入 `useFileBrowser.enterDir`）：进入该目录并退出搜索。 */
  enterDir: (path: string) => void;
};

export type UseBrowserSearchResult = {
  search: UseFsSearchQueryResult;
  query: string;
  /** 搜索来源的选中项（由响应直接构造）：与树选中互斥。 */
  searchSelection: FsEntry | null;
  /** 清除搜索选中（退出搜索 / 树内选中 / 切换作用域时调用）。 */
  clearSelection: () => void;
  /**
   * 结果项动作（§4.7.4 第 2 条 / Q8 决策）：
   *   * `file` → 选中并预览（不等树加载）；
   *   * `dir` → `enterDir` 并退出搜索；
   *   * `symlink/inaccessible/unknown` → 只选中并显示类型标注（结果行自带标注）。
   */
  activate: (item: FsSearchItem) => void;
  /** 结果视图替换目录树（§4.7.3 合成规则）。 */
  viewVisible: boolean;
  /** 首次搜索在途：树上方保留一行「搜索中…」。 */
  pending: boolean;
  /** 失败 / 不可用：退回本地过滤后的树 + 提示。 */
  degraded: boolean;
  /** 失败原因（已归一化，空原因用「未知原因」兜底）。 */
  errorText: string;
  /** 结果视图文案（已本地化）：结果组件不读 i18n。 */
  labels: FileSearchResultsLabels;
};

export function useBrowserSearch(options: UseBrowserSearchOptions): UseBrowserSearchResult {
  const { t } = useTranslation("workspace");
  const { enterDir, filterText, scope, showHidden } = options;
  const query = filterText.trim();
  const search = useFsSearchQuery({
    scope,
    query,
    enabled: query.length > 0,
    debounceMs: FS_SEARCH_DEBOUNCE_PANEL_MS,
    limit: FS_SEARCH_LIMIT_PANEL,
    kinds: "both",
    showHidden,
  });
  const [searchSelection, setSearchSelection] = useState<FsEntry | null>(null);

  const clearSelection = useCallback(() => setSearchSelection(null), []);
  const activate = useCallback(
    (item: FsSearchItem) => {
      if (item.type === "dir") {
        enterDir(item.path);
        setSearchSelection(null);
        return;
      }
      setSearchSelection(searchItemToEntry(item));
    },
    [enterDir],
  );

  const viewVisible =
    query.length > 0 &&
    (search.status === "ready" || (search.status === "loading" && search.items.length > 0));
  const pending = query.length > 0 && search.status === "loading" && !viewVisible;
  const degraded = query.length > 0 && search.status === "error";
  const rawError =
    search.error instanceof Error ? search.error.message : search.error === null ? "" : String(search.error);
  const errorText = rawError || t("panels.fileBrowser.search.errorUnknown");

  const labels: FileSearchResultsLabels = {
    aria: t("panels.fileBrowser.search.aria"),
    empty: t("panels.fileBrowser.search.empty"),
    // `{{message}}` 原样透传（i18next 对缺失插值保留占位符），由消费方用归一化后的原因替换。
    error: t("panels.fileBrowser.search.error"),
    exit: t("panels.fileBrowser.search.exit"),
    loadMore: t("panels.fileBrowser.search.loadMore"),
    loading: t("panels.fileBrowser.search.loading"),
    loadingMore: t("panels.fileBrowser.search.loadingMore"),
    resultDir: t("panels.fileBrowser.search.resultDir"),
    resultInaccessible: t("panels.fileBrowser.search.resultInaccessible"),
    resultSymlink: t("panels.fileBrowser.search.resultSymlink"),
    resultUnknown: t("panels.fileBrowser.search.resultUnknown"),
    retry: t("panels.fileBrowser.search.retry"),
    truncated: t("panels.fileBrowser.search.truncated"),
    unavailable: t("panels.fileBrowser.search.unavailable"),
  };

  return {
    activate,
    clearSelection,
    degraded,
    errorText,
    labels,
    pending,
    query,
    search,
    searchSelection,
    viewVisible,
  };
}
