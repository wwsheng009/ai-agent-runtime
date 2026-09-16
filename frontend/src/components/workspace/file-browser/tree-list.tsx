// 工作区右侧栏「文件浏览器面」→ 目录树虚拟列表（P0-5 / P1-2）。
//
// 后端契约：行数据来自 `FsListingResult`（`hooks/workspace/use-file-browser.ts` 按目录缓存）；本组件不发请求，
//   只做「已加载层 + 展开集合 → 可视行」的投影与交互（进入目录 / 选中 / 加载更多 / 下载）。
//
// 归一化纪律：`type === "unknown"` 不按目录处理（`isEnterableDirectory`），不给进入/展开入口；
//   `has_more` / `next_cursor` 缺失由 hook 收口为「无更多」，这里只按 `hasMore` 画「加载更多」状态行。
//
// 降级判据（虚拟滚动 + a11y）：固定行高 28px + 可视窗口切片 + 上下占位；虚拟化后同层兄弟多半不在 DOM 中，
//   所以每行都带 aria-setsize / aria-posinset（合成状态行也计入同层计数）；展开但列表失效（游离态：刷新丢缓存 /
//   上一层加载失败）时给出显式「重新加载该层」入口，不静默留白；键盘导航用 aria-activedescendant 指向行，
//   不搬动 DOM 焦点（虚拟窗口外的行可能尚未挂载）。
//
// P4-4：多选（Ctrl/Cmd 切换、Shift 按可见行顺序取范围）与右键菜单由调用方持有状态；本组件只负责
//   「把带修饰键的点击翻译成模式」与「把角标索引渲染成徽标」，不自己发请求 / 不改写选中集合。
import { AlertTriangleIcon, ChevronRightIcon, DownloadIcon, FileIcon, FileImageIcon, FolderIcon, FolderOpenIcon, LinkIcon, LoaderCircleIcon, ShieldAlertIcon } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { gitStatusBadge, GIT_STATUS_TONE_CLASS } from "@/lib/git/change-model";
import type { FileBrowserListing } from "@/hooks/workspace/use-file-browser";
import { buildTreeRows, computeVirtualWindow, FILE_TREE_OVERSCAN, FILE_TREE_ROW_HEIGHT } from "@/lib/file-browser/entry-sort";
import type { GitBadgeHit, GitBadgeIndex } from "@/lib/file-browser/git-badge";
import { formatEntryMtime, isEnterableDirectory } from "@/lib/file-browser/path-utils";
import { buildRenderRows, type FileTreeRenderRow } from "@/lib/file-browser/tree-rows";
import { formatByteSize } from "@/lib/file-preview/decode";
import { cn } from "@/lib/utils";
import type { FsEntry, FsSortKey } from "@/types/runtime/fs-browser";

const ROW_ID_PREFIX = "file-tree-row-";

/** 选择模式：replace = 单击 / 键盘；toggle = Ctrl/Cmd 单击；range = Shift 单击（按可见行顺序）。 */
export type FileTreeSelectMode = "replace" | "toggle" | "range";

export type FileTreeListProps = {
  listings: Readonly<Record<string, FileBrowserListing>>;
  expanded: ReadonlySet<string>;
  selectedPath: string | null;
  /** 多选集合（含 primary）；缺省表示只支持单选。 */
  selectedPaths?: ReadonlySet<string>;
  /** Shift 范围选择的锚点（上一次单击落点）。 */
  selectionAnchor?: string | null;
  /** git 状态角标索引（作用域相对路径 → 徽标来源）；null/缺省 = 不显示角标。 */
  gitBadges?: GitBadgeIndex | null;
  sortKey: FsSortKey;
  showHidden: boolean;
  /** 过滤词：只作用于**已加载层**（P4-5/Q3），不触发全树检索；缺省/空白 = 不过滤。 */
  filterText?: string;
  onToggleDir: (path: string) => void;
  /** `rangePaths` 仅在 `mode === "range"` 时给出（按可见行顺序，含两端）。 */
  onSelectEntry: (entry: FsEntry, mode: FileTreeSelectMode, rangePaths?: readonly string[]) => void;
  onEnterDir: (path: string) => void;
  onLoadMore: (path: string) => void;
  onReload: (path: string) => void;
  onDownload: (entry: FsEntry) => void;
  onRowContextMenu?: (entry: FsEntry, position: { x: number; y: number }) => void;
  /** Escape：清空多选（语义由调用方决定）。 */
  onClearSelection?: () => void;
  className?: string;
};

export function FileTreeList(props: FileTreeListProps) {
  const { className, expanded, filterText, gitBadges, listings, onClearSelection, onDownload, onEnterDir, onLoadMore, onReload, onRowContextMenu, onSelectEntry, onToggleDir, selectedPath, selectedPaths, selectionAnchor, showHidden, sortKey } = props;
  const { t } = useTranslation("workspace");
  const viewportRef = useRef<HTMLDivElement | null>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewportHeight, setViewportHeight] = useState(320);
  const [activeIndex, setActiveIndex] = useState(0);

  const entries = useMemo(
    () =>
      buildTreeRows({
        source: Object.fromEntries(
          Object.entries(listings)
            .filter(([, listing]) => listing.status === "ready" || listing.status === "loading_more")
            .map(([path, listing]) => [path, listing.entries]),
        ),
        expanded,
        sortKey,
        filter: { showHidden, text: filterText },
      }),
    [expanded, filterText, listings, showHidden, sortKey],
  );
  const rows = useMemo(() => buildRenderRows({ rows: entries, expanded, listings }), [entries, expanded, listings]);
  const virtual = computeVirtualWindow({
    itemCount: rows.length,
    scrollTop,
    viewportHeight,
    rowHeight: FILE_TREE_ROW_HEIGHT,
    overscan: FILE_TREE_OVERSCAN,
  });

  useEffect(() => {
    const node = viewportRef.current;
    if (!node || typeof ResizeObserver === "undefined") {
      return;
    }
    const observer = new ResizeObserver(() => setViewportHeight(node.clientHeight || 320));
    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  // 活动行收敛（行数变少：过滤 / 折叠 / 刷新）在渲染期派生 —— 不用 effect 里 setState
  // （那会多一轮渲染，且被 react-hooks/set-state-in-effect 拦下）。
  const activeRowIndex = rows.length === 0 ? 0 : Math.min(activeIndex, rows.length - 1);

  // Shift 范围选择按「可见行顺序」取（虚拟窗口只影响渲染，不影响这个顺序）。
  const visibleEntryPaths = useMemo(
    () => rows.flatMap((item) => (item.kind === "entry" ? [item.row.entry.path] : [])),
    [rows],
  );
  const handleRowSelect = useCallback(
    (entry: FsEntry, mode: FileTreeSelectMode) => {
      if (mode !== "range") {
        onSelectEntry(entry, mode);
        return;
      }
      const clicked = visibleEntryPaths.indexOf(entry.path);
      const anchor = selectionAnchor ? visibleEntryPaths.indexOf(selectionAnchor) : -1;
      if (clicked < 0 || anchor < 0) {
        // 锚点不在当前可见集合里（折叠 / 刷新导致）→ 退化为单选，不猜范围。
        onSelectEntry(entry, "replace");
        return;
      }
      const start = Math.min(anchor, clicked);
      onSelectEntry(entry, "range", visibleEntryPaths.slice(start, Math.max(anchor, clicked) + 1));
    },
    [onSelectEntry, selectionAnchor, visibleEntryPaths],
  );

  // 键盘导航：只移动「活动行」（aria-activedescendant），折叠/进入/选择分别显式绑定。
  const handleKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLDivElement>) => {
      const item = rows[activeRowIndex];
      if (!item) {
        return;
      }
      const entry = item.kind === "entry" ? item.row.entry : null;
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        setActiveIndex((current) =>
          Math.min(Math.max(0, current + (event.key === "ArrowDown" ? 1 : -1)), Math.max(0, rows.length - 1)),
        );
        return;
      }
      if (event.key === "Escape") {
        onClearSelection?.();
        return;
      }
      if (!entry) {
        return;
      }
      if (event.key === "ArrowRight" && isEnterableDirectory(entry) && !expanded.has(entry.path)) {
        event.preventDefault();
        onToggleDir(entry.path);
      } else if (event.key === "ArrowLeft" && expanded.has(entry.path)) {
        event.preventDefault();
        onToggleDir(entry.path);
      } else if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        if (isEnterableDirectory(entry) && event.key === "Enter") {
          onEnterDir(entry.path);
          return;
        }
        onSelectEntry(entry, "replace");
      }
    },
    [activeRowIndex, expanded, onClearSelection, onEnterDir, onSelectEntry, onToggleDir, rows],
  );

  return (
    <div
      aria-activedescendant={rows.length > 0 ? `${ROW_ID_PREFIX}${activeRowIndex}` : undefined}
      aria-label={t("panels.fileBrowser.tree.ariaLabel")}
      aria-multiselectable={selectedPaths ? true : undefined}
      // `app-scrollbar`：全局滚动条样式（窄条 + 稳定 gutter），否则 overlay 滚动条在
      // 深色主题下几乎不可见 —— 用户会以为「没有滚动条 / 不能滚」。
      className={cn(
        "app-scrollbar min-h-0 overflow-auto rounded-card border border-border/60 bg-surface/40",
        className,
      )}
      data-testid="file-browser-tree"
      onKeyDown={handleKeyDown}
      onScroll={(event) => setScrollTop(event.currentTarget.scrollTop)}
      ref={viewportRef}
      role="tree"
      tabIndex={0}
    >
      {rows.length === 0 ? (
        <p className="px-3 py-4 text-xs text-muted-foreground" data-testid="file-tree-empty">
          {filterText?.trim()
            ? t("panels.fileBrowser.tree.noMatch", { query: filterText.trim() })
            : t("panels.fileBrowser.tree.emptyDir")}
        </p>
      ) : (
        <div>
          <div aria-hidden style={{ height: virtual.topPadding }} />
          <div style={{ height: virtual.totalHeight - virtual.topPadding - virtual.bottomPadding }}>
            {rows.slice(virtual.start, virtual.end).map((item, offset) => (
              <FileTreeRowView
                active={virtual.start + offset === activeRowIndex}
                expanded={expanded}
                gitBadge={gitBadges?.get(item.kind === "entry" ? item.row.entry.path : "") ?? null}
                inSelection={item.kind === "entry" ? (selectedPaths?.has(item.row.entry.path) ?? false) : false}
                index={virtual.start + offset}
                item={item}
                key={item.kind === "entry" ? item.row.entry.path : `status:${item.status}:${item.path}`}
                listings={listings}
                onDownload={onDownload}
                onEnterDir={onEnterDir}
                onLoadMore={onLoadMore}
                onReload={onReload}
                onRowContextMenu={onRowContextMenu}
                onSelectEntry={handleRowSelect}
                onToggleDir={onToggleDir}
                selectedPath={selectedPath}
              />
            ))}
          </div>
          <div aria-hidden style={{ height: virtual.bottomPadding }} />
        </div>
      )}
    </div>
  );
}

type FileTreeRowViewProps = {
  active: boolean;
  index: number;
  item: FileTreeRenderRow;
  listings: Readonly<Record<string, FileBrowserListing>>;
  expanded: ReadonlySet<string>;
  selectedPath: string | null;
  /** 是否在多选集合内（primary 选中由 `selectedPath` 表达）。 */
  inSelection: boolean;
  /** git 状态角标来源；null = 该行不显示角标（非仓库 / 未映射）。 */
  gitBadge: GitBadgeHit | null;
  onToggleDir: (path: string) => void;
  onSelectEntry: (entry: FsEntry, mode: FileTreeSelectMode) => void;
  onRowContextMenu?: (entry: FsEntry, position: { x: number; y: number }) => void;
  onEnterDir: (path: string) => void;
  onLoadMore: (path: string) => void;
  onReload: (path: string) => void;
  onDownload: (entry: FsEntry) => void;
};

const ROW_CLASS = "group flex items-center gap-1.5 pr-1.5 text-xs";
const IMAGE_FILE_RE = /\.(png|jpe?g|gif|webp|bmp|svg|avif)$/i;

/**
 * 行图标：返回**元素**而不是「组件身份」。
 * 渲染期现取组件身份会触发 `react-hooks/static-components`（每次渲染都是新组件 → 状态被重置）。
 */
function entryIconElement(entry: FsEntry, expanded: boolean, className: string) {
  const props = { "aria-hidden": true, className } as const;
  if (entry.type === "dir") {
    return expanded ? <FolderOpenIcon {...props} /> : <FolderIcon {...props} />;
  }
  if (entry.type === "file") {
    return IMAGE_FILE_RE.test(entry.name) ? <FileImageIcon {...props} /> : <FileIcon {...props} />;
  }
  if (entry.type === "symlink") {
    return <LinkIcon {...props} />;
  }
  if (entry.type === "inaccessible") {
    return <ShieldAlertIcon {...props} />;
  }
  if (entry.type === "unknown") {
    return <AlertTriangleIcon {...props} />;
  }
  return <FileIcon {...props} />;
}

/** 单行视图：entry 行与合成状态行共用外壳，a11y 属性统一由 `row` 提供。 */
function FileTreeRowView(props: FileTreeRowViewProps) {
  const { t } = useTranslation("workspace");
  const { active, gitBadge, inSelection, index, item, listings, onDownload, onEnterDir, onLoadMore, onReload, onRowContextMenu, onSelectEntry, onToggleDir, selectedPath } = props;

  if (item.kind === "status") {
    const orphan = item.status === "orphan";
    const action = orphan ? t("panels.fileBrowser.tree.orphanAction") : t("panels.fileBrowser.tree.loadMore", { count: listings[item.path]?.entries.length ?? 0 });
    return (
      <div
        aria-level={item.level}
        aria-posinset={item.posInSet}
        aria-setsize={item.setSize}
        className="flex items-center gap-2 pr-2 text-xs text-muted-foreground"
        id={`${ROW_ID_PREFIX}${index}`}
        role="treeitem"
        style={{ height: FILE_TREE_ROW_HEIGHT, paddingLeft: 12 + item.depth * 14 }}
      >
        {item.status === "loading" ? <LoaderCircleIcon aria-hidden className="size-3.5 animate-spin" /> : <AlertTriangleIcon aria-hidden className="size-3.5" />}
        <span className="truncate">{orphan ? t("panels.fileBrowser.tree.orphan") : t("panels.fileBrowser.tree.loadingMore")}</span>
        {item.status !== "loading" ? (
          <button
            aria-label={action}
            className="ml-auto rounded border border-border/70 px-1.5 py-0.5 text-[11px] text-foreground hover:bg-white/5"
            onClick={(event) => {
              event.stopPropagation();
              if (orphan) {
                onReload(item.path);
                return;
              }
              onLoadMore(item.path);
            }}
            title={action}
            type="button"
          >
            {action}
          </button>
        ) : null}
      </div>
    );
  }

  const { row } = item;
  const { entry } = row;
  const enterable = isEnterableDirectory(entry);
  const selected = selectedPath === entry.path;
  const badge = gitBadge ? gitStatusBadge(gitBadge.status, gitBadge.group) : null;
  const badgeLabel =
    badge && gitBadge
      ? t("panels.git.list.rowAria", {
          path: entry.path,
          badge: t(`panels.git.list.badge.${badge.letter}`),
          group: t(`panels.git.list.groups.${gitBadge.group}`),
        })
      : "";
  // 本层被截断时，在展开目录行上显式提示「仅显示前 N 项」（与面板底部提示同源，覆盖非当前层）。
  const childListing = listings[entry.path];
  const truncationHint = row.expanded && childListing?.truncated === true ? t("panels.fileBrowser.tree.truncated", { count: childListing.entries.length }) : "";
  const label = t(entry.type === "dir" ? "panels.fileBrowser.tree.entryDir" : "panels.fileBrowser.tree.entryFile", { name: entry.name });
  const toggleLabel = t(row.expanded ? "panels.fileBrowser.tree.collapse" : "panels.fileBrowser.tree.expand", { name: entry.name });

  return (
    <div
      aria-expanded={enterable ? row.expanded : undefined}
      aria-level={row.level}
      aria-posinset={row.posInSet}
      aria-selected={selected || inSelection}
      aria-setsize={row.setSize}
      className={cn(
        ROW_CLASS,
        active && "bg-white/[0.04]",
        (selected || inSelection) && "bg-accent/10 text-foreground",
      )}
      id={`${ROW_ID_PREFIX}${index}`}
      onContextMenu={(event) => {
        if (!onRowContextMenu) {
          return;
        }
        event.preventDefault();
        onRowContextMenu(entry, { x: event.clientX, y: event.clientY });
      }}
      role="treeitem"
      style={{ height: FILE_TREE_ROW_HEIGHT, paddingLeft: 6 + row.depth * 14 }}
    >
      {enterable ? (
        <button
          aria-label={toggleLabel}
          className="grid size-4 shrink-0 place-items-center rounded text-muted-foreground hover:bg-white/5"
          onClick={(event) => {
            event.stopPropagation();
            onToggleDir(entry.path);
          }}
          title={toggleLabel}
          type="button"
        >
          <ChevronRightIcon aria-hidden className={cn("size-3 transition-transform", row.expanded && "rotate-90")} />
        </button>
      ) : (
        <span aria-hidden className="size-4 shrink-0" />
      )}
      <button
        aria-label={label}
        className="flex min-w-0 flex-1 items-center gap-1.5 py-0.5 text-left"
        onClick={(event) => {
          // 修饰键语义：Ctrl/Cmd = 切换，Shift = 范围，两者同时按下按「切换」处理（不做第三种语义）。
          const mode: FileTreeSelectMode =
            event.metaKey || event.ctrlKey ? "toggle" : event.shiftKey ? "range" : "replace";
          onSelectEntry(entry, mode);
        }}
        onDoubleClick={() => enterable && onEnterDir(entry.path)}
        title={entry.path}
        type="button"
      >
        {entryIconElement(
          entry,
          row.expanded,
          cn("size-3.5 shrink-0", entry.type === "inaccessible" && "text-accent-gold"),
        )}
        <span className="truncate">{entry.name}</span>
        {badge ? (
          <span
            aria-label={badgeLabel}
            className={cn(
              "shrink-0 rounded-[4px] border px-1 font-mono app-text-10",
              GIT_STATUS_TONE_CLASS[badge.tone],
            )}
            data-git-badge={badge.letter}
            data-testid="file-tree-git-badge"
            title={badgeLabel}
          >
            {badge.letter === "untracked" ? "?" : badge.letter}
          </span>
        ) : null}
        {entry.type === "symlink" ? <span className="shrink-0 text-[10px] text-muted-foreground">{t("panels.fileBrowser.tree.symlink")}</span> : null}
        {entry.type === "unknown" ? <span className="shrink-0 text-[10px] text-accent-gold">{t("panels.fileBrowser.tree.unknownType")}</span> : null}
        {truncationHint ? <span className="shrink-0 text-[10px] text-accent-gold">{truncationHint}</span> : null}
        {entry.internal ? <span className="shrink-0 text-[10px] text-muted-foreground">{t("panels.fileBrowser.tree.internal")}</span> : null}
        {entry.type === "file" || entry.type === "symlink" ? (
          // 末两列固定语义：修改时间（探测失败/缺失 → 不渲染，不显示 1970）与人类可读大小
          // （原始字节数放 title，避免「1.0 KB」被当成精确值）。
          <span className="ml-auto flex shrink-0 items-center gap-2 font-mono text-[10px] text-muted-foreground">
            {entry.mtime > 0 && formatEntryMtime(entry.mtime) ? (
              <span className="hidden sm:inline" data-testid="file-tree-mtime">
                {formatEntryMtime(entry.mtime)}
              </span>
            ) : null}
            <span
              className="min-w-8 text-right"
              data-testid="file-tree-size"
              title={entry.size >= 0 ? `${entry.size} B` : undefined}
            >
              {entry.size >= 0 ? formatByteSize(entry.size) : t("panels.fileBrowser.tree.sizeUnknown")}
            </span>
          </span>
        ) : null}
      </button>
      {entry.type === "file" ? (
        <button
          aria-label={t("panels.fileBrowser.preview.download", { name: entry.name })}
          className="shrink-0 rounded p-0.5 text-muted-foreground opacity-0 hover:bg-white/5 focus:opacity-100 group-hover:opacity-100"
          onClick={(event) => {
            event.stopPropagation();
            onDownload(entry);
          }}
          title={t("panels.fileBrowser.preview.download", { name: entry.name })}
          type="button"
        >
          <DownloadIcon aria-hidden className="size-3.5" />
        </button>
      ) : null}
    </div>
  );
}
