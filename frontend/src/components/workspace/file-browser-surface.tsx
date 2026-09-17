// 工作区右侧栏「文件浏览器面」出口（P0-5/P0-6 挂载契约 + P1-2/P2-3/P2-4 组装）。
//
// 后端契约：roots 来自 `GET /api/runtime/fs/roots`（`FsRoot[]`，scope 直接回传）；目录/预览/传输分别见
//   `fs-list.ts` / `fs-preview.ts` / `fs-transfer.ts`；本文件是唯一持有「作用域」的地方（子组件全部受控）。
//
// 归一化纪律：roots 请求**带 sessionId**，后端才会把「会话当前工作目录」作为 `kind: "session"` 根返回；
//   默认作用域取该会话根（`pickDefaultRoot`），没有时退回首根。端点不可用时才用
//   `buildFallbackRoot(sessionId, workspacePath)` 生成「只用当前会话工作目录」的作用域。
//
// 降级判据（不臆造数据）：
//   * roots 返回 404/405/501/503（`isFsRootsUnavailable`）→ 退化为会话工作目录并显式提示；
//   * `truncated=true` 在树里显式提示「本层仅显示前 N 项」（hook 已收口，树按层展示）；
//   * 传输队列展示上传状态机与下载三层的能力说明；上传冲突必须由用户显式选择，不默默覆盖；
//   * git 状态角标（P0-5/Q6）：只做一次 `/git/status` 建映射，非仓库 / git 不可用 / 作用域与仓库无祖先关系
//     → 整体跳过角标（不显示空角标、不显示错误态，文件浏览与 git 可用性解耦）；
//   * 目录下载 / 复制绝对路径在缺少能力时不装作可用：菜单项禁用并给出原因（目录下载无 zip 端点、根路径缺失）。
//
// P4-3/P4-4：拖拽上传落到当前目录；多选（Ctrl/Cmd 切换、Shift 范围）+ 右键菜单（下载 / 复制路径）。
//   选中集合与锚点是**面内状态**，不写设置、不跨会话记忆（与 Q8 的 sessionStorage 口径不冲突：本期未做）。
//
// P1-9/P1-10（规划 §4.7）：过滤词停顿后叠加**全库搜索**，结果就绪时用扁平结果视图替换目录树
//   （§4.7.2-A/§4.7.3 合成规则）；视图状态与展示件在 `file-browser/use-browser-search.ts` 与
//   `file-browser/search-view.tsx`——本组件只做树 ↔ 结果的合成、选中来源收口与布局。
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { RuntimeApiError } from "@/api/runtime/shared";
import { fetchFsRoots, isFsRootsUnavailable, pickDefaultRoot } from "@/api/runtime/fs-roots";
import { fetchGitStatus } from "@/api/runtime/git";
import { FileDropTarget } from "@/components/workspace/file-browser/drop-target";
import { PreviewPane } from "@/components/workspace/file-browser/preview-pane";
import { FileRowMenu } from "@/components/workspace/file-browser/row-menu";
import { ScopeHeader } from "@/components/workspace/file-browser/scope-header";
import { TransferTray } from "@/components/workspace/file-browser/transfer-tray";
import { FileTreeList, type FileTreeSelectMode } from "@/components/workspace/file-browser/tree-list";
import { useBrowserFileActions } from "@/components/workspace/file-browser/use-browser-file-actions";
import { useDownloadManager } from "@/components/workspace/file-browser/use-download-manager";
import { useRowMenuItems } from "@/components/workspace/file-browser/use-row-menu-items";
import {
  BrowserSearchNotices,
  BrowserSearchResults,
} from "@/components/workspace/file-browser/search-view";
import {
  pickBrowserSelection,
  useBrowserSearch,
} from "@/components/workspace/file-browser/use-browser-search";
import { isAbortError, useFileBrowser } from "@/hooks/workspace/use-file-browser";
import { readRetainedUploads, useFileTransfer } from "@/hooks/workspace/use-file-transfer";
import { buildGitBadgeIndex, type GitBadgeIndex } from "@/lib/file-browser/git-badge";
import { buildFallbackRoot } from "@/lib/file-browser/path-utils";
import { cn } from "@/lib/utils";

import type { FsEntry, FsRoot, FsSearchItem } from "@/types/runtime/fs-browser";

export type FileBrowserSurfaceProps = { sessionId: string; workspacePath?: string };

export function FileBrowserSurface({ sessionId, workspacePath }: FileBrowserSurfaceProps) {
  const { t } = useTranslation("workspace");
  const [roots, setRoots] = useState<FsRoot[]>([]);
  /** roots 请求结果与「请求 key」绑定：key 不匹配 = 仍在加载（见下方 `rootsStatus`）。 */
  const [rootsResult, setRootsResult] = useState<{ key: string; status: "ready" | "degraded" } | null>(null);
  const [degradedStatus, setDegradedStatus] = useState<number | undefined>(undefined);
  const [degradedReason, setDegradedReason] = useState<string | undefined>(undefined);
  const [activeScope, setActiveScope] = useState("");
  const [trayOpen, setTrayOpen] = useState(false);
  const [selectedPaths, setSelectedPaths] = useState<readonly string[]>([]);
  const [primaryPath, setPrimaryPath] = useState<string | null>(null);
  const [selectionAnchor, setSelectionAnchor] = useState<string | null>(null);
  /**
   * 过滤词：输入时**即时过滤已加载层**（P4-5/Q3：零请求、离线可用）；
   * 停顿后由 `useFsSearchQuery` 触发全库搜索（P1-9，§4.7.3 合成规则）。
   */
  const [filterText, setFilterText] = useState("");
  const [rowMenu, setRowMenu] = useState<{ entry: FsEntry; x: number; y: number } | null>(null);
  /** git 角标索引与「请求 key」绑定：key 不匹配 = 尚未就绪（见下方 `gitBadges`）。 */
  const [gitBadgesResult, setGitBadgesResult] = useState<{ key: string; index: GitBadgeIndex | null } | null>(null);
  const [badgeToken, setBadgeToken] = useState(0);

  const fallbackRoot = useMemo(() => buildFallbackRoot(sessionId, workspacePath), [sessionId, workspacePath]);
  const scope = activeScope || fallbackRoot.scope;
  /**
   * roots 状态是**派生值**：结果只对本次 `sessionId + fallbackRoot.scope` 生效，key 不匹配即「加载中」，
   * 因此切换会话/工作目录不需要在 effect 体内同步 `setState`（react-hooks/set-state-in-effect）。
   */
  const rootsKey = `${sessionId}|${fallbackRoot.scope}`;
  const rootsStatus: "loading" | "ready" | "degraded" = rootsResult?.key === rootsKey ? rootsResult.status : "loading";

  useEffect(() => {
    const controller = new AbortController();
    let alive = true;
    void fetchFsRoots({ signal: controller.signal, sessionId })
      .then((result) => {
        if (!alive) {
          return;
        }
        const available = result.roots.filter((root) => root.exists);
        setRoots(available);
        setRootsResult({ key: rootsKey, status: "ready" });
        // 默认作用域 = 会话目录根（kind=session），其次服务端顺序首根。
        setActiveScope((current) => current || pickDefaultRoot(available)?.scope || "");
      })
      .catch((error: unknown) => {
        if (!alive || isAbortError(error)) {
          return;
        }
        setRoots([]);
        if (isFsRootsUnavailable(error)) {
          // 降级判据：端点未实现/不可用（404/405/501/503）→ 用「服务不可用」文案。
          setDegradedStatus(error instanceof RuntimeApiError ? error.status : undefined);
          setDegradedReason(undefined);
        } else {
          // 其它失败不冒充 503：如实给出原因（仍只保留会话工作目录作用域）。
          setDegradedStatus(undefined);
          setDegradedReason(error instanceof Error ? error.message : String(error));
        }
        setRootsResult({ key: rootsKey, status: "degraded" });
        setActiveScope((current) => current || fallbackRoot.scope);
      });
    return () => {
      alive = false;
      controller.abort();
    };
  }, [fallbackRoot.scope, rootsKey, sessionId]);

  const browser = useFileBrowser({ scope });
  const transfer = useFileTransfer({ scope });
  const downloadManager = useDownloadManager({ scope });
  const { currentDir, enterDir, listings, loadListing, toggleDir } = browser;
  const currentListing = listings[currentDir];

  // 全库搜索（P1-9，§4.7.4）：状态与降级判据在 `use-browser-search.ts`，这里只提供 scope/过滤词/开关。
  const searchView = useBrowserSearch({ enterDir, filterText, scope, showHidden: browser.showHidden });
  // 回调是 hook 内 `useCallback` 的稳定引用：解构后入依赖，避免每次渲染新建的 `searchView` 对象被要求进数组。
  const { activate: activateSearchItem, clearSelection: clearSearchSelection, searchSelection } = searchView;

  /** 作用域绝对根（用于复制绝对路径 / git 角标映射）；缺失时相关能力显式降级而不是猜路径。 */
  const activeRootPath = useMemo(() => {
    const hit = roots.find((root) => root.scope === scope);
    return (hit?.path ?? workspacePath ?? "").trim();
  }, [roots, scope, workspacePath]);
  /** 作用域展示名（拖拽提示用；不猜盘符，直接用后端给出的根名）。 */
  const activeRootName = useMemo(() => {
    const hit = roots.find((root) => root.scope === scope);
    return (hit?.name || fallbackRoot.name || scope).trim();
  }, [fallbackRoot.name, roots, scope]);
  /** git 角标是**派生值**：作用域 / 根路径 / 刷新 token 任一变化都重新请求，未就绪时为 null。 */
  const gitBadgeKey = `${scope}|${activeRootPath}|${badgeToken}`;
  const gitBadges = gitBadgesResult?.key === gitBadgeKey ? gitBadgesResult.index : null;

  // 已加载层的 path → entry 索引：多选下载 / 复制路径都从这里取真实 entry，取不到就跳过（不猜类型）。
  const entriesByPath = useMemo(() => {
    const map = new Map<string, FsEntry>();
    for (const listing of Object.values(listings)) {
      for (const entry of listing.entries) {
        map.set(entry.path, entry);
      }
    }
    return map;
  }, [listings]);
  const selectedSet = useMemo(() => new Set(selectedPaths), [selectedPaths]);
  const selectedEntries = useMemo(
    () => selectedPaths.map((path) => entriesByPath.get(path)).filter((entry): entry is FsEntry => Boolean(entry)),
    [entriesByPath, selectedPaths],
  );

  // git 状态角标：一次请求建索引；非仓库 / git 不可用 / 无祖先关系 → null（整体跳过，不显示空角标）。
  useEffect(() => {
    const controller = new AbortController();
    let alive = true;
    void fetchGitStatus({ scope }, { signal: controller.signal })
      .then((status) => {
        if (!alive) {
          return;
        }
        setGitBadgesResult({
          key: gitBadgeKey,
          index: buildGitBadgeIndex({
            scopeRootPath: activeRootPath,
            repoRoot: status.repo?.root ?? null,
            groups: {
              conflicts: status.conflicts,
              staged: status.staged,
              unstaged: status.unstaged,
              untracked: status.untracked,
            },
          }),
        });
      })
      .catch(() => {
        // 失败不弹错（git 面已有自己的空态）；这里只保证「不显示角标」。
        if (alive) {
          setGitBadgesResult({ key: gitBadgeKey, index: null });
        }
      });
    return () => {
      alive = false;
      controller.abort();
    };
  }, [activeRootPath, gitBadgeKey, scope]);

  // 保留上传（重试入口）来自外部存储而非 React 状态：每次渲染重读一次（队列变化 / dismiss 都会触发重渲染），
  // 不做状态副本，也不在 effect 内 setState。
  const retained = readRetainedUploads();

  // 选中来源收口（§4.7.4）：搜索选中自带 `FsEntry`（不依赖树加载），否则退回树主选中项。
  const selection = useMemo(
    () => pickBrowserSelection({ entriesByPath, primaryPath, searchSelection, selectedPaths }),
    [entriesByPath, primaryPath, searchSelection, selectedPaths],
  );
  // 预览接口不变：仍然只吃 `FsEntry`（来源只决定它是谁选中的）。
  const selectedEntry = selection?.entry ?? null;
  /** 树选中不得点亮搜索结果行：只有 search 来源才作为结果视图的选中路径。 */
  const searchSelectedPath = selection?.source === "search" ? selection.entry.path : null;

  const clearSelection = useCallback(() => {
    setSelectedPaths([]);
    setPrimaryPath(null);
    setSelectionAnchor(null);
  }, []);

  /** 清除搜索并返回目录树（ScopeHeader 退出按钮 / Esc / 目录结果同源）；结果由 hook 按 query 清空。 */
  const exitSearch = useCallback(() => {
    setFilterText("");
    clearSearchSelection();
  }, [clearSearchSelection]);

  /**
   * 结果项动作（§4.7.4 第 2 条）：`dir` 先 `enterDir` 再清查询（回到树视图，沿用既有加载纪律）；
   * 其余类型只切换预览选中——`symlink/inaccessible/unknown` 的判定在 hook 内，这里不重复写。
   */
  const handleActivateSearchItem = useCallback(
    (item: FsSearchItem) => {
      activateSearchItem(item);
      if (item.type === "dir") {
        setFilterText("");
      }
    },
    [activateSearchItem],
  );

  /**
   * 切作用域 / 换目录都会让旧相对路径失去意义：只清选中，不动传输（传输自己带 scope+dir 快照）。
   * 用「渲染期重置」而不是 effect 内 setState（react-hooks/set-state-in-effect），行为等价：
   * 过滤词是作用域内的临时视图状态、搜索选中不得跨作用域预览，上次结果由 hook 在 scope 变化时清空。
   */
  const [selectionScope, setSelectionScope] = useState(scope);
  if (selectionScope !== scope) {
    setSelectionScope(scope);
    clearSelection();
    setFilterText("");
    clearSearchSelection();
  }

  useEffect(() => {
    loadListing(currentDir, "first");
  }, [currentDir, loadListing]);

  const handleRefresh = useCallback(() => {
    loadListing(currentDir, "first", true);
    setBadgeToken((token) => token + 1);
  }, [currentDir, loadListing]);
  const handleRefreshAll = useCallback(() => {
    loadListing(currentDir, "first", true);
    for (const path of browser.expanded) {
      loadListing(path, "first", true);
    }
    setBadgeToken((token) => token + 1);
  }, [browser.expanded, currentDir, loadListing]);
  const handlePickUpload = useCallback(
    (files: FileList) => {
      transfer.enqueueUpload(files, currentDir);
      setTrayOpen(true);
    },
    [currentDir, transfer],
  );
  const handleDownload = useCallback((entry: Parameters<typeof downloadManager.start>[0]) => {
    downloadManager.start(entry);
    setTrayOpen(true);
  }, [downloadManager]);

  /** 批量下载 / 复制绝对路径 / 一次性提示：动作与提示状态都在 `use-browser-file-actions.ts` 内。 */
  const { copyAbsolutePaths, copyNotice, startDownloads } = useBrowserFileActions({
    activeRootPath,
    downloadManager,
    setTrayOpen,
  });

  const handleSelectEntry = useCallback(
    (entry: FsEntry, mode: FileTreeSelectMode, rangePaths?: readonly string[]) => {
      // 树选中与搜索选中互斥：任何树内选择都让预览回到树来源。
      clearSearchSelection();
      if (mode === "replace") {
        // 沿用既有语义：单击目录 = 原地展开/折叠（是否选中与展开互不影响）。
        if (entry.type === "dir") {
          toggleDir(entry.path);
        }
        setSelectedPaths([entry.path]);
        setPrimaryPath(entry.path);
        setSelectionAnchor(entry.path);
        return;
      }
      if (mode === "range" && rangePaths && rangePaths.length > 0) {
        setSelectedPaths([...rangePaths]);
        setPrimaryPath(entry.path);
        return;
      }
      const next = selectedSet.has(entry.path)
        ? selectedPaths.filter((path) => path !== entry.path)
        : [...selectedPaths, entry.path];
      setSelectedPaths(next);
      setPrimaryPath(next.length === 0 ? null : next[next.length - 1]);
      setSelectionAnchor(entry.path);
    },
    [clearSearchSelection, selectedPaths, selectedSet, toggleDir],
  );

  const treeState = (() => {
    if (rootsStatus === "loading") {
      return t("panels.fileBrowser.tree.loading");
    }
    if (!currentListing || currentListing.status === "loading") {
      return t("panels.fileBrowser.tree.loading");
    }
    if (currentListing.status === "error") {
      return t("panels.fileBrowser.tree.error", {
        message: currentListing.error instanceof Error ? currentListing.error.message : String(currentListing.error),
      });
    }
    return "";
  })();
  // 过滤激活时空态的归属交给树（树会区分「空目录」与「没有匹配项」），避免两句矛盾文案同时在位。
  const ready =
    rootsStatus !== "loading" &&
    currentListing?.status === "ready" &&
    currentListing.entries.length === 0 &&
    filterText.trim() === "";

  // 右键菜单项（多选批量 / 单行 + 禁用原因）在 `use-row-menu-items.ts` 内组装：这里只给状态与回调。
  const rowMenuItems = useRowMenuItems({
    activeRootPath,
    onCopyPaths: (entries) => void copyAbsolutePaths(entries),
    onDownload: startDownloads,
    rowMenuEntry: rowMenu?.entry ?? null,
    selectedEntries,
    selectedSet,
  });

  return (
    // 列必须是 `minmax(0,1fr)`：隐式 `auto` 列的轨道尺寸吃内容最小宽度（实测被撑到 535.7px，
    // 容器只有 460px），整块面板会右溢出被 `overflow-hidden` 裁掉 —— 表现就是行尾的
    // 修改时间/大小列被切、树看着「没有滚动条」。显式 0 下限允许列收缩到容器宽度。
    <div
      className="grid min-h-0 flex-1 grid-cols-[minmax(0,1fr)] grid-rows-[auto_minmax(0,1fr)] overflow-hidden"
      data-testid="file-browser-surface"
    >
      <ScopeHeader
        activeScope={scope}
        canGoUp={currentDir !== ""}
        currentDir={currentDir}
        degraded={rootsStatus === "degraded"}
        degradedReason={degradedReason}
        degradedStatus={degradedStatus}
        filterText={filterText}
        hasFallback={Boolean(fallbackRoot.scope)}
        onChangeSort={browser.setSortKey}
        onFilterChange={setFilterText}
        onGoUp={() => browser.enterDir(currentDir.includes("/") ? currentDir.slice(0, currentDir.lastIndexOf("/")) : "")}
        onExitSearch={exitSearch}
        onPickUpload={handlePickUpload}
        onRefresh={handleRefresh}
        onRefreshAll={handleRefreshAll}
        onSelectScope={(next) => {
          setActiveScope(next);
          browser.enterDir("");
        }}
        onToggleHidden={() => browser.setShowHidden(!browser.showHidden)}
        onToggleTransferTray={() => setTrayOpen((open) => !open)}
        roots={rootsStatus === "ready" && roots.length > 0 ? roots : [fallbackRoot]}
        resultsVisible={searchView.viewVisible}
        searchActive={searchView.query.length > 0}
        showHidden={browser.showHidden}
        sortKey={browser.sortKey}
        transferCount={transfer.uploads.length + downloadManager.downloads.length}
        transferOpen={trayOpen}
      />

      <FileDropTarget
        className="min-h-0"
        hint={t("panels.fileBrowser.drop.hint", {
          dir: currentDir ? `${activeRootName}/${currentDir}` : activeRootName,
        })}
        onDropFiles={handlePickUpload}
      >
      {/* `flex-1` 是必需的：本层是 FileDropTarget（flex 列）的子项，缺它就退回内容高度，
          `minmax(0,3fr/2fr)` 拿不到确定高度 → 树永不溢出（无滚动条）、预览区被裁到区外。 */}
      <div className="grid min-h-0 flex-1 grid-cols-[minmax(0,1fr)] grid-rows-[minmax(0,3fr)_minmax(0,2fr)] gap-2 overflow-hidden p-2">
        <div className="grid min-h-0 grid-cols-[minmax(0,1fr)] grid-rows-[minmax(0,1fr)_auto] gap-1">
          {searchView.viewVisible ? (
            // 结果就绪 → 扁平结果列表接管列表区（§4.7.2-A）；多选/右键/传输不接入（§4.7.4 第 2 条）。
            <BrowserSearchResults
              errorText={searchView.errorText}
              labels={searchView.labels}
              onActivate={handleActivateSearchItem}
              onExit={exitSearch}
              search={searchView.search}
              selectedPath={searchSelectedPath}
            />
          ) : (
            <FileTreeList
              expanded={browser.expanded}
              filterText={filterText}
              gitBadges={gitBadges}
              listings={listings}
              onClearSelection={clearSelection}
              onDownload={handleDownload}
              onEnterDir={enterDir}
              onLoadMore={(path) => loadListing(path, "more")}
              onReload={(path) => loadListing(path, "first", true)}
              onRowContextMenu={(entry, position) => setRowMenu({ entry, x: position.x, y: position.y })}
              onSelectEntry={handleSelectEntry}
              onToggleDir={toggleDir}
              selectedPath={primaryPath}
              selectedPaths={selectedSet}
              selectionAnchor={selectionAnchor}
              showHidden={browser.showHidden}
              sortKey={browser.sortKey}
            />
          )}

          {/* 结果视图接管列表区时，树专属状态行（加载/截断/空目录/多选计数/复制提示）一律隐藏，避免两种语义打架。 */}
          {!searchView.viewVisible && treeState ? (
            <p className="px-1 app-text-11 text-muted-foreground">{treeState}</p>
          ) : null}
          {!searchView.viewVisible && currentListing?.truncated ? (
            <p className="px-1 app-text-11 text-accent-gold" data-testid="listing-truncated">
              {t("panels.fileBrowser.tree.truncated", { count: currentListing.entries.length })}
            </p>
          ) : null}
          {!searchView.viewVisible && currentListing?.cursorExpired ? (
            <p className="px-1 app-text-11 text-accent-gold">{t("panels.fileBrowser.tree.cursorExpired")}</p>
          ) : null}
          {!searchView.viewVisible && ready ? (
            <p className="px-1 app-text-11 text-muted-foreground">{t("panels.fileBrowser.tree.emptyDir")}</p>
          ) : null}
          {!searchView.viewVisible && selectedPaths.length > 1 ? (
            <p className="px-1 app-text-11 text-muted-foreground" data-testid="file-selection-count">
              {t("panels.fileBrowser.tree.selectionCount", { count: selectedPaths.length })}
            </p>
          ) : null}
          {!searchView.viewVisible && copyNotice ? (
            <p
              className={cn("px-1 app-text-11", copyNotice === "done" ? "text-muted-foreground" : "text-accent-gold")}
              data-testid="file-copy-notice"
            >
              {t(copyNotice === "done" ? "panels.fileBrowser.menu.copyDone" : "panels.fileBrowser.menu.copyFailed")}
            </p>
          ) : null}

          {/* 首次搜索在途 / 降级（§4.7.3、§4.7.5）：合成规则与不重试纪律都在 hook 里，这里只摆位置。 */}
          <BrowserSearchNotices
            degraded={searchView.degraded}
            errorText={searchView.errorText}
            labels={searchView.labels}
            onRetry={searchView.search.retry}
            pending={searchView.pending}
            unavailable={searchView.search.unavailable}
          />
        </div>

        <PreviewPane className="min-h-0 rounded-card border border-border/60" entry={selectedEntry} onDownload={handleDownload} scope={scope} />

        {trayOpen ? (
          <TransferTray
            className="row-span-2"
            downloads={downloadManager.downloads}
            onCancelDownload={downloadManager.cancel}
            onCancelUpload={transfer.cancelUpload}
            onClose={() => setTrayOpen(false)}
            onDismissDownload={downloadManager.dismiss}
            onDismissUpload={transfer.dismissUpload}
            onPauseUpload={transfer.pauseUpload}
            onResolveConflict={transfer.resolveConflict}
            onResumeUpload={transfer.resumeUpload}
            retained={retained}
            uploads={transfer.uploads}
          />
        ) : null}
      </div>
      </FileDropTarget>

      {rowMenu ? (
        <FileRowMenu items={rowMenuItems} onClose={() => setRowMenu(null)} x={rowMenu.x} y={rowMenu.y} />
      ) : null}
    </div>
  );
}
