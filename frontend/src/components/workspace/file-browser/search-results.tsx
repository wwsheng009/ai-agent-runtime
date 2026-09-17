// 工作区右侧栏「文件浏览器面」→ 全库搜索结果视图（P1-9/P1-10，规划 §4.7.2-A / §4.7.3）。
//
// 定位：**受控**展示层——结果与状态由 owner（`useFsSearchQuery` + `fetchFsSearch`）持有，本组件不发请求、
//   不改选中集合、不接多选/右键/传输（§4.7.4：避免两套选中集合纠缠），只把交互上抛。
//
// 后端契约：条目为 `FsSearchItem`（`types/runtime/fs-browser.ts`），`score` 由服务端按相关度给出；
//   本视图**不提供排序**（排序控件由 owner 在搜索态禁用并标注原因）。
//
// 归一化纪律：
//   * `match.start/end` 是**显示字符串的 rune 偏移**（`FsSearchMatch` 注释）：切分在 `search-match.ts`
//     内按码点做（`Array.from`），绝不按 UTF-16 下标切片，否则 emoji / 组合字符会被切坏；
//   * `match.field` 决定高亮 `name` 还是 `path`，另一列原样展示；缺失/空命中不猜位置；
//   * 不做虚拟化：面板页大小 ≤50（`FS_SEARCH_LIMIT_PANEL`），行数有界。
//
// 降级判据（§4.7.5）：
//   * `unavailable`（404/405/501/503）→ 显式提示且**不提供重试**（owner 已退回「仅过滤已加载层」）；
//   * 其它失败 → 保留已有结果 + 可重试入口（是否退回本地过滤由 owner 决定）；
//   * `truncated`（结果可能不完备）与 `hasMore`（还有下一页）正交，两条提示可同时出现。
import {
  AlertTriangleIcon,
  FileIcon,
  FileImageIcon,
  FolderIcon,
  LinkIcon,
  LoaderCircleIcon,
  ShieldAlertIcon,
} from "lucide-react";
import { useCallback, useState } from "react";

import { splitRuneMatch } from "@/components/workspace/file-browser/search-match";
import { cn } from "@/lib/utils";

import type { FsSearchItem, FsSearchMatch } from "@/types/runtime/fs-browser";

const ROW_ID_PREFIX = "file-search-row-";
const IMAGE_FILE_RE = /\.(png|jpe?g|gif|webp|bmp|svg|avif)$/i;

/** 搜索状态与 `useFsSearchQuery` 的 `FsSearchStatus` 同口径（此处独立声明，避免组件反向依赖 hook）。 */
export type FileSearchResultsStatus = "idle" | "loading" | "ready" | "error";

/** 已本地化文案（由 owner 注入）：本组件不读 i18n，便于单测断言与复用。 */
export type FileSearchResultsLabels = {
  /** 结果列表 aria-label。 */
  aria: string;
  /** 首次搜索中（还没有任何结果可展示）。 */
  loading: string;
  /** 空结果。 */
  empty: string;
  /** 失败（`{{message}}` 已插值）。 */
  error: string;
  /** 端点不可用（一次性提示，不重试）。 */
  unavailable: string;
  /** 截断（`{{count}}` 已插值为已扫描条目数）。 */
  truncated: string;
  /** 加载更多。 */
  loadMore: string;
  /** 加载更多进行中。 */
  loadingMore: string;
  /** 重试。 */
  retry: string;
  /** 清除搜索并返回目录树（Esc 同源）。 */
  exit: string;
  /** 目录结果的类型标注。 */
  resultDir: string;
  /** 符号链接标注。 */
  resultSymlink: string;
  /** 无权限标注。 */
  resultInaccessible: string;
  /** 类型未知标注。 */
  resultUnknown: string;
};

export type FileSearchResultsProps = {
  items: readonly FsSearchItem[];
  status: FileSearchResultsStatus;
  hasMore: boolean;
  loadingMore: boolean;
  truncated: boolean;
  /** 本次（含翻页累计）已扫描条目数，仅用于截断提示。 */
  scanned: number;
  /** 端点不可用（404/405/501/503）：不提供重试入口。 */
  unavailable: boolean;
  /** 失败原因（owner 已归一化为展示字符串）；空串表示无错误。 */
  errorMessage: string;
  /** 当前选中的结果路径（来源收口在 owner：树选中不会点亮结果行）。 */
  selectedPath: string | null;
  labels: FileSearchResultsLabels;
  onSelect: (item: FsSearchItem) => void;
  onEnterDir: (item: FsSearchItem) => void;
  onLoadMore: () => void;
  onRetry: () => void;
  /** Esc：清除搜索并返回目录树。 */
  onExit: () => void;
  className?: string;
};

/** 命中高亮：只在 `field` 对应的那一列调用（另一列不传 match）。 */
function HighlightedText({ match, text }: { match?: FsSearchMatch | undefined; text: string }) {
  const [before, hit, after] = splitRuneMatch(text, match);
  if (!hit) {
    return <>{text}</>;
  }
  return (
    <>
      {before}
      <mark className="rounded-[3px] bg-accent/30 text-foreground" data-testid="file-search-hit">
        {hit}
      </mark>
      {after}
    </>
  );
}

/** 行图标：返回元素而不是组件身份（与 tree-list 同纪律，避免每次渲染都是新组件）。 */
function searchItemIcon(item: FsSearchItem, className: string) {
  const props = { "aria-hidden": true, className } as const;
  if (item.type === "dir") {
    return <FolderIcon {...props} />;
  }
  if (item.type === "file") {
    return IMAGE_FILE_RE.test(item.name) ? <FileImageIcon {...props} /> : <FileIcon {...props} />;
  }
  if (item.type === "symlink") {
    return <LinkIcon {...props} />;
  }
  if (item.type === "inaccessible") {
    return <ShieldAlertIcon {...props} />;
  }
  if (item.type === "unknown") {
    return <AlertTriangleIcon {...props} />;
  }
  return <FileIcon {...props} />;
}

/** 目录可进入：只有 `type === "dir"`（symlink/unknown/inaccessible 不伪装成目录）。 */
function isEnterableItem(item: FsSearchItem): boolean {
  return item.type === "dir";
}

/** 非文件结果的类型标注：不显示「可预览/可进入」的暗示，只如实标注类型。 */
function typeHint(item: FsSearchItem, labels: FileSearchResultsLabels): string {
  if (item.type === "dir") {
    return labels.resultDir;
  }
  if (item.type === "symlink") {
    return labels.resultSymlink;
  }
  if (item.type === "inaccessible") {
    return labels.resultInaccessible;
  }
  if (item.type === "unknown") {
    return labels.resultUnknown;
  }
  return "";
}

export function FileSearchResults(props: FileSearchResultsProps) {
  const {
    className,
    errorMessage,
    hasMore,
    items,
    labels,
    loadingMore,
    onEnterDir,
    onExit,
    onLoadMore,
    onRetry,
    onSelect,
    scanned,
    selectedPath,
    status,
    truncated,
    unavailable,
  } = props;
  const [rawActiveIndex, setRawActiveIndex] = useState(0);
  // 结果集合收窄（翻页/查询变化）时把活动行夹回范围内：渲染期派生，不用 effect 里 setState。
  const activeIndex = items.length === 0 ? 0 : Math.min(rawActiveIndex, items.length - 1);

  const activate = useCallback(
    (item: FsSearchItem) => {
      if (isEnterableItem(item)) {
        onEnterDir(item);
        return;
      }
      onSelect(item);
    },
    [onEnterDir, onSelect],
  );

  const handleKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLDivElement>) => {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        if (items.length === 0) {
          return;
        }
        event.preventDefault();
        setRawActiveIndex((current) =>
          Math.min(Math.max(0, current + (event.key === "ArrowDown" ? 1 : -1)), items.length - 1),
        );
        return;
      }
      if (event.key === "Escape") {
        // 与 ScopeHeader 的「清除搜索」同源：恢复目录树。
        onExit();
        return;
      }
      if (event.key !== "Enter") {
        return;
      }
      const item = items[activeIndex];
      if (!item) {
        return;
      }
      event.preventDefault();
      activate(item);
    },
    [activate, activeIndex, items, onExit],
  );

  const errorText = errorMessage ? labels.error.replace("{{message}}", errorMessage) : labels.error;
  // scanned 来自 applySearchPage 的跨页累计（每页全量重扫的总和），文案按「累计扫描量」解释。
  const truncatedText = labels.truncated.replace("{{count}}", String(scanned));
  const hasFooter = hasMore || truncated;

  return (
    <div
      aria-activedescendant={items.length > 0 ? `${ROW_ID_PREFIX}${activeIndex}` : undefined}
      aria-label={labels.aria}
      className={cn(
        "app-scrollbar grid min-h-0 grid-rows-[minmax(0,1fr)_auto] overflow-hidden rounded-card border border-border/60 bg-surface/40",
        className,
      )}
      data-testid="file-search-results"
      onKeyDown={handleKeyDown}
      role="listbox"
      tabIndex={0}
    >
      <div className="app-scrollbar min-h-0 overflow-auto">
        {items.length === 0 && status === "loading" ? (
          <p className="flex items-center gap-2 px-3 py-4 text-xs text-muted-foreground" data-testid="file-search-loading">
            <LoaderCircleIcon aria-hidden className="size-3.5 animate-spin" />
            <span>{labels.loading}</span>
          </p>
        ) : null}

        {items.length === 0 && status === "ready" ? (
          <p className="px-3 py-4 text-xs text-muted-foreground" data-testid="file-search-empty">
            {labels.empty}
          </p>
        ) : null}

        {status === "error" ? (
          <div className="flex items-center gap-2 px-3 py-2 app-text-11" data-testid="file-search-error">
            <AlertTriangleIcon aria-hidden className={cn("size-3.5 shrink-0", unavailable ? "text-muted-foreground" : "text-accent-gold")} />
            <span className={cn("min-w-0 flex-1", unavailable ? "text-muted-foreground" : "text-foreground")}>
              {unavailable ? labels.unavailable : errorText}
            </span>
            {unavailable ? null : (
              <button
                className="shrink-0 rounded border border-border/70 px-1.5 py-0.5 text-foreground hover:bg-white/5"
                onClick={onRetry}
                type="button"
              >
                {labels.retry}
              </button>
            )}
          </div>
        ) : null}

        {items.map((item, index) => {
          const selected = item.path === selectedPath;
          const hint = typeHint(item, labels);
          return (
            <div
              aria-selected={selected}
              className={cn(
                "group flex items-center gap-1.5 pr-1.5 text-xs",
                index === activeIndex && "bg-white/[0.04]",
                selected && "bg-accent/10 text-foreground",
              )}
              id={`${ROW_ID_PREFIX}${index}`}
              key={item.path}
              onMouseEnter={() => setRawActiveIndex(index)}
              role="option"
            >
              <button
                aria-label={item.path}
                className="flex min-w-0 flex-1 items-center gap-1.5 py-1 text-left"
                onClick={() => activate(item)}
                title={item.path}
                type="button"
              >
                {searchItemIcon(item, cn("size-3.5 shrink-0", item.type === "inaccessible" && "text-accent-gold"))}
                <span className="min-w-0 flex-1">
                  <span className="block truncate">
                    <HighlightedText match={item.match?.field === "name" ? item.match : undefined} text={item.name} />
                  </span>
                  {item.path && item.path !== item.name ? (
                    <span className="block truncate font-mono app-text-10 text-muted-foreground">
                      <HighlightedText match={item.match?.field === "path" ? item.match : undefined} text={item.path} />
                    </span>
                  ) : null}
                </span>
                {hint ? <span className="shrink-0 app-text-10 text-muted-foreground">{hint}</span> : null}
              </button>
            </div>
          );
        })}
      </div>

      {hasFooter ? (
        <div className="flex items-center gap-2 border-t border-border/60 px-2 py-1">
          {truncated ? (
            <span className="min-w-0 flex-1 truncate app-text-11 text-accent-gold" data-testid="file-search-truncated">
              {truncatedText}
            </span>
          ) : (
            <span className="min-w-0 flex-1" />
          )}
          {hasMore ? (
            <button
              className="shrink-0 rounded border border-border/70 px-1.5 py-0.5 app-text-11 text-foreground hover:bg-white/5 disabled:opacity-50"
              disabled={loadingMore}
              onClick={onLoadMore}
              title={loadingMore ? labels.loadingMore : labels.loadMore}
              type="button"
            >
              {loadingMore ? labels.loadingMore : labels.loadMore}
            </button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
