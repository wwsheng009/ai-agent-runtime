// 工作区右侧栏「文件浏览器面」→ 作用域/工具栏头部（P0-5）。
//
// 后端契约：作用域项来自 `FsRoot`（`scope` 直接回传给后续请求；`path` 仅展示）；
//   `isGitRepo` / `gitRoot` / `probeError` 是可选探测结果，缺失即视为「未探测成功」，不猜语义。
//
// 归一化纪律：本组件是**受控**展示层——不自己发请求、不自己改作用域缓存，
//   所有动作（切作用域/排序/隐藏项/刷新/上传）都上抛给 owner（file-browser-surface.tsx）。
//
// 降级判据：roots 探测失败（503/404，见 `isFsRootsUnavailable`）时 owner 会传 `degraded`
//   与「只用当前会话工作目录」提示；此处只如实展示，不隐藏降级状态。
import {
  EyeIcon,
  EyeOffIcon,
  FolderUpIcon,
  GitBranchIcon,
  ListTreeIcon,
  RefreshCwIcon,
  SearchIcon,
  UploadIcon,
  XIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";
import { FS_SORT_KEY_OPTIONS, normalizeSortKey } from "@/lib/file-browser/entry-sort";

import type { FsRoot, FsSortKey } from "@/types/runtime/fs-browser";

// `as const satisfies` 保留字面量类型（不拓宽为 string），这样 `t()` 的键类型检查才有意义。
const SORT_LABEL_KEYS = {
  name_asc: "panels.fileBrowser.scope.sortNameAsc",
  name_desc: "panels.fileBrowser.scope.sortNameDesc",
  mtime_desc: "panels.fileBrowser.scope.sortMtimeDesc",
  size_desc: "panels.fileBrowser.scope.sortSizeDesc",
  type_then_name: "panels.fileBrowser.scope.sortTypeThenName",
} as const satisfies Record<FsSortKey, string>;

export type ScopeHeaderProps = {
  roots: readonly FsRoot[];
  activeScope: string;
  /** 是否处于降级模式（roots 不可用，仅剩会话工作目录）。 */
  degraded: boolean;
  degradedStatus?: number;
  /** 非「端点不可用」的失败原因（未知错误如实展示，不冒充 503 文案）。 */
  degradedReason?: string;
  hasFallback: boolean;
  currentDir: string;
  canGoUp: boolean;
  showHidden: boolean;
  sortKey: FsSortKey;
  /** 过滤词：由 owner 持有；这里只展示与上抛（输入时即时过滤已加载层，停顿后由 owner 触发全库搜索）。 */
  filterText: string;
  transferCount: number;
  transferOpen: boolean;
  /** 查询词非空：显示「清除搜索/返回目录树」按钮，Esc 同源（结果视图可能仍在加载或已降级）。 */
  searchActive: boolean;
  /** 搜索**结果视图**替换了目录树：排序控件不适用（服务端按相关度排序），禁用并标注原因。 */
  resultsVisible: boolean;
  onSelectScope: (scope: string) => void;
  onGoUp: () => void;
  onRefresh: () => void;
  onRefreshAll: () => void;
  onToggleHidden: () => void;
  onChangeSort: (key: FsSortKey) => void;
  onFilterChange: (value: string) => void;
  onExitSearch: () => void;
  onPickUpload: (files: FileList) => void;
  onToggleTransferTray: () => void;
  className?: string;
};

export function ScopeHeader(props: ScopeHeaderProps) {
  const { t } = useTranslation("workspace");
  const {
    activeScope,
    canGoUp,
    className,
    currentDir,
    degraded,
    degradedReason,
    degradedStatus,
    filterText,
    hasFallback,
    onChangeSort,
    onExitSearch,
    onFilterChange,
    onGoUp,
    onPickUpload,
    onRefresh,
    onRefreshAll,
    onSelectScope,
    onToggleHidden,
    onToggleTransferTray,
    roots,
    resultsVisible,
    searchActive,
    showHidden,
    sortKey,
    transferCount,
    transferOpen,
  } = props;
  const activeRoot = roots.find((root) => root.scope === activeScope);

  return (
    // 列显式 `minmax(0,1fr)`：隐式 `auto` 列的轨道会吃内容最小宽度（实测把头部撑到 533px
    // 而容器只有 460px），尾部控件因此被 `overflow-hidden` 裁掉、点不到。0 下限允许收缩。
    <header
      className={cn(
        "grid grid-cols-[minmax(0,1fr)] gap-2 border-b border-border/60 px-3 py-2",
        className,
      )}
    >
      <div className="flex items-center gap-2">
        <label className="sr-only" htmlFor="file-browser-scope">
          {t("panels.fileBrowser.scope.label")}
        </label>
        <select
          aria-label={t("panels.fileBrowser.scope.selectAria")}
          className="min-w-0 flex-1 truncate rounded border border-border/60 bg-surface-solid px-1.5 py-1 text-xs text-foreground"
          id="file-browser-scope"
          onChange={(event) => onSelectScope(event.target.value)}
          value={activeScope}
        >
          {roots.map((root) => (
            <option key={root.scope} value={root.scope}>
              {root.name || root.path || root.scope}
            </option>
          ))}
          {hasFallback && !roots.some((root) => root.scope === activeScope) && activeScope ? (
            <option value={activeScope}>{t("panels.fileBrowser.scope.cwdFallback")}</option>
          ) : null}
        </select>
        <button
          aria-label={t("panels.fileBrowser.scope.up")}
          className="rounded border border-border/60 p-1 text-muted-foreground hover:bg-white/5 disabled:opacity-40"
          disabled={!canGoUp}
          onClick={onGoUp}
          title={canGoUp ? t("panels.fileBrowser.scope.up") : t("panels.fileBrowser.scope.upDisabled")}
          type="button"
        >
          <FolderUpIcon aria-hidden className="size-3.5" />
        </button>
        <button
          aria-label={t("panels.fileBrowser.scope.refresh")}
          className="rounded border border-border/60 p-1 text-muted-foreground hover:bg-white/5"
          onClick={onRefresh}
          title={t("panels.fileBrowser.scope.refresh")}
          type="button"
        >
          <RefreshCwIcon aria-hidden className="size-3.5" />
        </button>
        <button
          aria-label={t("panels.fileBrowser.scope.refreshAll")}
          className="rounded border border-border/60 p-1 text-muted-foreground hover:bg-white/5"
          onClick={onRefreshAll}
          title={t("panels.fileBrowser.scope.refreshAll")}
          type="button"
        >
          <RefreshCwIcon aria-hidden className="size-3.5" />
        </button>
      </div>

      <div className="flex items-center gap-1.5 rounded border border-border/60 bg-surface-solid px-1.5 py-1">
        <SearchIcon aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
        <input
          aria-label={t("panels.fileBrowser.scope.filterAria")}
          className="min-w-0 flex-1 bg-transparent text-xs text-foreground outline-none placeholder:text-muted-foreground/60"
          data-testid="file-browser-filter"
          onChange={(event) => onFilterChange(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Escape") {
              // Esc 与「清除搜索/返回目录树」同源：清空查询并恢复目录树（§4.7.4 第 2 条）。
              event.preventDefault();
              onExitSearch();
            }
          }}
          placeholder={
            searchActive
              ? t("panels.fileBrowser.search.placeholder")
              : t("panels.fileBrowser.scope.filterPlaceholder")
          }
          title={t("panels.fileBrowser.scope.filterHint")}
          type="search"
          value={filterText}
        />
        {searchActive ? (
          <button
            aria-label={t("panels.fileBrowser.search.exit")}
            className="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-white/5"
            onClick={onExitSearch}
            title={t("panels.fileBrowser.search.exit")}
            type="button"
          >
            <ListTreeIcon aria-hidden className="size-3.5" />
          </button>
        ) : null}
        {filterText ? (
          <button
            aria-label={t("panels.fileBrowser.scope.filterClear")}
            className="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-white/5"
            onClick={() => onFilterChange("")}
            title={t("panels.fileBrowser.scope.filterClear")}
            type="button"
          >
            <XIcon aria-hidden className="size-3.5" />
          </button>
        ) : null}
      </div>

      {/* `flex-wrap`：本行是路径 + 角标 + 4 个固定控件（隐藏开关 / 排序 / 上传 / 传输队列），
          窄右栏里合计宽度会超过容器（实测 513px > 436px）。不换行就只能右溢被 `overflow-hidden`
          裁掉——被裁的控件点不到。换行保证控件永远可达，代价只是头部多占一行。 */}
      <div className="flex flex-wrap items-center gap-2">
        <span className="truncate font-mono app-text-11 text-muted-foreground" title={currentDir || "/"}>
          /{currentDir}
        </span>
        {activeRoot?.isGitRepo ? (
          <span
            className="inline-flex shrink-0 items-center gap-1 rounded border border-accent/40 px-1.5 py-0.5 app-text-10 text-accent-secondary"
            title={activeRoot.gitRoot ? t("panels.fileBrowser.scope.gitRoot", { path: activeRoot.gitRoot }) : t("panels.fileBrowser.scope.gitBadge")}
          >
            <GitBranchIcon aria-hidden className="size-3" />
            {t("panels.fileBrowser.scope.gitBadge")}
          </span>
        ) : null}
        <button
          aria-label={showHidden ? t("panels.fileBrowser.scope.hideHidden") : t("panels.fileBrowser.scope.showHidden")}
          className="ml-auto rounded border border-border/60 p-1 text-muted-foreground hover:bg-white/5"
          onClick={onToggleHidden}
          title={showHidden ? t("panels.fileBrowser.scope.hideHidden") : t("panels.fileBrowser.scope.showHidden")}
          type="button"
        >
          {showHidden ? <EyeOffIcon aria-hidden className="size-3.5" /> : <EyeIcon aria-hidden className="size-3.5" />}
        </button>
        <label className="sr-only" htmlFor="file-browser-sort">
          {t("panels.fileBrowser.scope.sortAria")}
        </label>
        <select
          aria-label={t("panels.fileBrowser.scope.sortAria")}
          className="rounded border border-border/60 bg-surface-solid px-1.5 py-1 app-text-11 text-foreground disabled:opacity-40"
          disabled={resultsVisible}
          id="file-browser-sort"
          onChange={(event) => onChangeSort(normalizeSortKey(event.target.value))}
          title={resultsVisible ? t("panels.fileBrowser.search.sortHint") : undefined}
          value={sortKey}
        >
          {FS_SORT_KEY_OPTIONS.map((key) => (
            <option key={key} value={key}>
              {t(SORT_LABEL_KEYS[key])}
            </option>
          ))}
        </select>
        <label
          className="inline-flex cursor-pointer items-center gap-1 rounded border border-border/60 px-1.5 py-1 app-text-11 text-muted-foreground hover:bg-white/5"
          title={t("panels.fileBrowser.scope.upload")}
        >
          <UploadIcon aria-hidden className="size-3.5" />
          <span>{t("panels.fileBrowser.scope.upload")}</span>
          <input
            aria-label={t("panels.fileBrowser.scope.uploadPick")}
            className="sr-only"
            multiple
            onChange={(event) => {
              if (event.target.files && event.target.files.length > 0) {
                onPickUpload(event.target.files);
              }
              event.target.value = "";
            }}
            type="file"
          />
        </label>
        <button
          aria-label={transferCount > 0 ? t("panels.fileBrowser.scope.transferTray", { count: transferCount }) : t("panels.fileBrowser.scope.transferTrayEmpty")}
          aria-pressed={transferOpen}
          className="rounded border border-border/60 px-1.5 py-1 app-text-11 text-muted-foreground hover:bg-white/5"
          onClick={onToggleTransferTray}
          title={transferCount > 0 ? t("panels.fileBrowser.scope.transferTray", { count: transferCount }) : t("panels.fileBrowser.scope.transferTrayEmpty")}
          type="button"
        >
          {transferCount > 0 ? t("panels.fileBrowser.scope.transferTray", { count: transferCount }) : t("panels.fileBrowser.scope.transferTrayEmpty")}
        </button>
      </div>

      {resultsVisible ? (
        // 禁用外还要**可见地**标注原因（disabled 元素在多数浏览器不弹 title）。
        <p className="app-text-11 text-muted-foreground" data-testid="file-browser-search-sort-hint">
          {t("panels.fileBrowser.search.sortHint")}
        </p>
      ) : null}

      {degraded ? (
        <p className="rounded border border-accent-gold/30 bg-accent-gold/10 px-2 py-1 app-text-11 text-foreground" data-testid="file-browser-degraded">
          {hasFallback
            ? degradedReason
              ? t("panels.fileBrowser.scope.degradedOther", { reason: degradedReason })
              : t("panels.fileBrowser.scope.degraded", { status: String(degradedStatus ?? 503) })
            : t("panels.fileBrowser.scope.degradedNoRoot")}
        </p>
      ) : null}
      {activeRoot?.probeError ? (
        <p className="app-text-11 text-muted-foreground">
          {t("panels.fileBrowser.scope.probeError", { reason: activeRoot.probeError })}
        </p>
      ) : null}
      {degraded && hasFallback ? (
        <p className="app-text-11 text-muted-foreground">{t("panels.fileBrowser.scope.unavailableHint")}</p>
      ) : null}
    </header>
  );
}
