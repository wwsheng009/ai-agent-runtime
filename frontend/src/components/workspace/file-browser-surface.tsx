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
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { RuntimeApiError } from "@/api/runtime/shared";
import { fetchFsRoots, isFsRootsUnavailable, pickDefaultRoot } from "@/api/runtime/fs-roots";
import { fetchGitStatus } from "@/api/runtime/git";
import { FileDropTarget } from "@/components/workspace/file-browser/drop-target";
import { PreviewPane } from "@/components/workspace/file-browser/preview-pane";
import { FileRowMenu, type FileRowMenuItem } from "@/components/workspace/file-browser/row-menu";
import { ScopeHeader } from "@/components/workspace/file-browser/scope-header";
import { TransferTray } from "@/components/workspace/file-browser/transfer-tray";
import { FileTreeList, type FileTreeSelectMode } from "@/components/workspace/file-browser/tree-list";
import { useDownloadManager } from "@/components/workspace/file-browser/use-download-manager";
import { isAbortError, useFileBrowser } from "@/hooks/workspace/use-file-browser";
import { readRetainedUploads, useFileTransfer } from "@/hooks/workspace/use-file-transfer";
import { buildGitBadgeIndex, type GitBadgeIndex } from "@/lib/file-browser/git-badge";
import { buildFallbackRoot, toAbsoluteDisplayPath } from "@/lib/file-browser/path-utils";
import { cn } from "@/lib/utils";

import type { FsEntry, FsRoot } from "@/types/runtime/fs-browser";

export type FileBrowserSurfaceProps = { sessionId: string; workspacePath?: string };

export function FileBrowserSurface({ sessionId, workspacePath }: FileBrowserSurfaceProps) {
  const { t } = useTranslation("workspace");
  const [roots, setRoots] = useState<FsRoot[]>([]);
  const [rootsStatus, setRootsStatus] = useState<"loading" | "ready" | "degraded">("loading");
  const [degradedStatus, setDegradedStatus] = useState<number | undefined>(undefined);
  const [degradedReason, setDegradedReason] = useState<string | undefined>(undefined);
  const [activeScope, setActiveScope] = useState("");
  const [trayOpen, setTrayOpen] = useState(false);
  const [retained, setRetained] = useState(() => readRetainedUploads());
  const [selectedPaths, setSelectedPaths] = useState<readonly string[]>([]);
  const [primaryPath, setPrimaryPath] = useState<string | null>(null);
  const [selectionAnchor, setSelectionAnchor] = useState<string | null>(null);
  /** 过滤词：只作用于已加载层（P4-5/Q3），不发请求、不做全树搜索。 */
  const [filterText, setFilterText] = useState("");
  const [rowMenu, setRowMenu] = useState<{ entry: FsEntry; x: number; y: number } | null>(null);
  const [gitBadges, setGitBadges] = useState<GitBadgeIndex | null>(null);
  const [badgeToken, setBadgeToken] = useState(0);
  const [copyNotice, setCopyNotice] = useState<"" | "done" | "failed">("");

  const fallbackRoot = useMemo(() => buildFallbackRoot(sessionId, workspacePath), [sessionId, workspacePath]);
  const scope = activeScope || fallbackRoot.scope;

  useEffect(() => {
    const controller = new AbortController();
    let alive = true;
    setRootsStatus("loading");
    void fetchFsRoots({ signal: controller.signal, sessionId })
      .then((result) => {
        if (!alive) {
          return;
        }
        const available = result.roots.filter((root) => root.exists);
        setRoots(available);
        setRootsStatus("ready");
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
        setRootsStatus("degraded");
        setActiveScope((current) => current || fallbackRoot.scope);
      });
    return () => {
      alive = false;
      controller.abort();
    };
  }, [fallbackRoot.scope, sessionId]);

  const browser = useFileBrowser({ scope });
  const transfer = useFileTransfer({ scope });
  const downloadManager = useDownloadManager({ scope });
  const { currentDir, listings, loadListing, toggleDir } = browser;
  const currentListing = listings[currentDir];

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
    setGitBadges(null);
    void fetchGitStatus({ scope }, { signal: controller.signal })
      .then((status) => {
        if (!alive) {
          return;
        }
        setGitBadges(
          buildGitBadgeIndex({
            scopeRootPath: activeRootPath,
            repoRoot: status.repo?.root ?? null,
            groups: {
              conflicts: status.conflicts,
              staged: status.staged,
              unstaged: status.unstaged,
              untracked: status.untracked,
            },
          }),
        );
      })
      .catch(() => {
        // 失败不弹错（git 面已有自己的空态）；这里只保证「不显示角标」。
        if (alive) {
          setGitBadges(null);
        }
      });
    return () => {
      alive = false;
      controller.abort();
    };
  }, [activeRootPath, badgeToken, scope]);

  useEffect(() => {
    setRetained(readRetainedUploads());
  }, [transfer.uploads]);

  // 预览只跟随「主选中项」，且多选时不猜要看哪个文件（>1 个选中 → 不显示预览）。
  const selectedEntry = selectedPaths.length === 1 && primaryPath ? (entriesByPath.get(primaryPath) ?? null) : null;

  const clearSelection = useCallback(() => {
    setSelectedPaths([]);
    setPrimaryPath(null);
    setSelectionAnchor(null);
  }, []);

  // 切作用域 / 换目录都会让旧相对路径失去意义：只清选中，不动传输（传输自己带 scope+dir 快照）。
  useEffect(() => {
    clearSelection();
    // 过滤词是「作用域内」的临时视图状态：换作用域后残留会把新树显示成空，因此一并清掉。
    setFilterText("");
  }, [clearSelection, scope]);

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

  /** 多选下载：只对 `type === "file"` 的行发请求（目录没有 zip 端点，符号链接不猜可下载性）。 */
  const startDownloads = useCallback(
    (entries: readonly FsEntry[]) => {
      const files = entries.filter((entry) => entry.type === "file");
      if (files.length === 0) {
        return;
      }
      for (const entry of files) {
        downloadManager.start(entry);
      }
      setTrayOpen(true);
    },
    [downloadManager],
  );

  const copyAbsolutePaths = useCallback(
    async (entries: readonly FsEntry[]) => {
      const paths = entries
        .map((entry) => toAbsoluteDisplayPath(activeRootPath, entry.path))
        .filter((path) => path.length > 0);
      if (paths.length === 0) {
        setCopyNotice("failed");
        return;
      }
      try {
        if (!navigator.clipboard?.writeText) {
          throw new Error("clipboard unavailable");
        }
        await navigator.clipboard.writeText(paths.join("\n"));
        setCopyNotice("done");
      } catch {
        setCopyNotice("failed");
      }
    },
    [activeRootPath],
  );

  useEffect(() => {
    if (!copyNotice) {
      return;
    }
    const timer = window.setTimeout(() => setCopyNotice(""), 1600);
    return () => window.clearTimeout(timer);
  }, [copyNotice]);

  const handleSelectEntry = useCallback(
    (entry: FsEntry, mode: FileTreeSelectMode, rangePaths?: readonly string[]) => {
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
    [selectedPaths, selectedSet, toggleDir],
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

  // 右键菜单：命中多选时按「所选」批量，否则只作用于当前行；禁用项必须给出原因（见 row-menu.tsx 纪律）。
  const menuEntries =
    rowMenu && selectedSet.has(rowMenu.entry.path) && selectedEntries.length > 1
      ? selectedEntries
      : rowMenu
        ? [rowMenu.entry]
        : [];
  const menuFiles = menuEntries.filter((entry) => entry.type === "file");
  const rowMenuItems: FileRowMenuItem[] = rowMenu
    ? [
        {
          id: "download",
          label:
            menuFiles.length > 1
              ? t("panels.fileBrowser.menu.downloadSelected", { count: menuFiles.length })
              : t("panels.fileBrowser.menu.download"),
          onSelect: () => startDownloads(menuFiles),
          disabled: menuFiles.length === 0,
          hint:
            menuFiles.length === 0
              ? t("panels.fileBrowser.menu.downloadUnavailable")
              : t("panels.fileBrowser.menu.downloadHint"),
        },
        {
          id: "copy-path",
          label:
            menuEntries.length > 1
              ? t("panels.fileBrowser.menu.copySelected", { count: menuEntries.length })
              : t("panels.fileBrowser.menu.copyPath"),
          onSelect: () => void copyAbsolutePaths(menuEntries),
          disabled: activeRootPath.length === 0,
          hint: activeRootPath
            ? t("panels.fileBrowser.menu.copyPathHint")
            : t("panels.fileBrowser.menu.copyPathUnavailable"),
        },
      ]
    : [];

  return (
    <div className="grid min-h-0 flex-1 grid-rows-[auto_minmax(0,1fr)] overflow-hidden" data-testid="file-browser-surface">
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
      <div className="grid min-h-0 grid-rows-[minmax(0,3fr)_minmax(0,2fr)] gap-2 overflow-hidden p-2">
        <div className="grid min-h-0 grid-rows-[minmax(0,1fr)_auto] gap-1">
          <FileTreeList
            expanded={browser.expanded}
            filterText={filterText}
            gitBadges={gitBadges}
            listings={listings}
            onClearSelection={clearSelection}
            onDownload={handleDownload}
            onEnterDir={browser.enterDir}
            onLoadMore={(path) => loadListing(path, "more")}
            onReload={(path) => loadListing(path, "first", true)}
            onRowContextMenu={(entry, position) => setRowMenu({ entry, x: position.x, y: position.y })}
            onSelectEntry={handleSelectEntry}
            onToggleDir={browser.toggleDir}
            selectedPath={primaryPath}
            selectedPaths={selectedSet}
            selectionAnchor={selectionAnchor}
            showHidden={browser.showHidden}
            sortKey={browser.sortKey}
          />
          {treeState ? <p className="px-1 text-[11px] text-muted-foreground">{treeState}</p> : null}
          {currentListing?.truncated ? (
            <p className="px-1 text-[11px] text-accent-gold" data-testid="listing-truncated">
              {t("panels.fileBrowser.tree.truncated", { count: currentListing.entries.length })}
            </p>
          ) : null}
          {currentListing?.cursorExpired ? (
            <p className="px-1 text-[11px] text-accent-gold">{t("panels.fileBrowser.tree.cursorExpired")}</p>
          ) : null}
          {ready ? <p className="px-1 text-[11px] text-muted-foreground">{t("panels.fileBrowser.tree.emptyDir")}</p> : null}
          {selectedPaths.length > 1 ? (
            <p className="px-1 text-[11px] text-muted-foreground" data-testid="file-selection-count">
              {t("panels.fileBrowser.tree.selectionCount", { count: selectedPaths.length })}
            </p>
          ) : null}
          {copyNotice ? (
            <p
              className={cn("px-1 text-[11px]", copyNotice === "done" ? "text-muted-foreground" : "text-accent-gold")}
              data-testid="file-copy-notice"
            >
              {t(copyNotice === "done" ? "panels.fileBrowser.menu.copyDone" : "panels.fileBrowser.menu.copyFailed")}
            </p>
          ) : null}
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
            onDismissUpload={(id) => {
              transfer.dismissUpload(id);
              setRetained(readRetainedUploads());
            }}
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
