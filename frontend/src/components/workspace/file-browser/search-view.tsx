// 面板「全库搜索」的两块展示接线（P1-9/P1-10，规划 §4.7.2-A/§4.7.3）：
//   * `BrowserSearchResults`：结果就绪时替换目录树的扁平结果视图（受控，动作全部上抛）；
//   * `BrowserSearchNotices`：仍在树上操作时的「搜索中…」行与降级提示（§4.7.5）。
//
// 落位：与 `lib`/`search-results.tsx` 的关系是「视图 → 原子列表件」：这里只做
//   `useFsSearchQuery` 结果 → props 的绑定与文案替换，不持有任何请求状态。
import { cn } from "@/lib/utils";

import { FileSearchResults, type FileSearchResultsLabels } from "@/components/workspace/file-browser/search-results";
import type { UseFsSearchQueryResult } from "@/hooks/workspace/use-fs-search";

import type { FsSearchItem } from "@/types/runtime/fs-browser";

export type BrowserSearchResultsProps = {
  search: UseFsSearchQueryResult;
  labels: FileSearchResultsLabels;
  /** 当前选中的结果路径（来源收口在 owner：树选中不会点亮结果行）。 */
  selectedPath: string | null;
  /** 已归一化的失败原因（`unavailable` 时只用于提示文案）。 */
  errorText: string;
  onActivate: (item: FsSearchItem) => void;
  onExit: () => void;
};

/** 结果视图接线：把 hook 状态与本地化文案绑定到原子列表件（自身不发请求、不改选中）。 */
export function BrowserSearchResults(props: BrowserSearchResultsProps) {
  const { errorText, labels, onActivate, onExit, search, selectedPath } = props;
  return (
    <FileSearchResults
      className="min-h-0"
      errorMessage={search.status === "error" && !search.unavailable ? errorText : ""}
      hasMore={search.hasMore}
      items={search.items}
      labels={labels}
      loadingMore={search.loadingMore}
      onEnterDir={onActivate}
      onExit={onExit}
      onLoadMore={search.loadMore}
      onRetry={search.retry}
      onSelect={onActivate}
      scanned={search.scanned}
      selectedPath={selectedPath}
      status={search.status}
      truncated={search.truncated}
      unavailable={search.unavailable}
    />
  );
}

export type BrowserSearchNoticesProps = {
  /** 首次搜索在途（仍在树上操作）。 */
  pending: boolean;
  /** 失败 / 不可用：退回本地过滤后的树 + 提示。 */
  degraded: boolean;
  unavailable: boolean;
  errorText: string;
  labels: FileSearchResultsLabels;
  onRetry: () => void;
};

/**
 * 降级提示（§4.7.5）：不可用 → 一次性提示且**不提供重试**（端点没实现，重试只会继续失败）；
 * 其它失败 → 可重试提示。提示只描述事实，不吹成成功。
 */
export function BrowserSearchNotices(props: BrowserSearchNoticesProps) {
  const { degraded, errorText, labels, onRetry, pending, unavailable } = props;
  return (
    <>
      {pending ? (
        <p className="px-1 app-text-11 text-muted-foreground" data-testid="file-search-pending">
          {labels.loading}
        </p>
      ) : null}
      {degraded ? (
        <div className="flex items-center gap-2 px-1" data-testid="file-search-degraded">
          <span className={cn("min-w-0 flex-1 app-text-11", unavailable ? "text-muted-foreground" : "text-accent-gold")}>
            {unavailable ? labels.unavailable : labels.error.replace("{{message}}", errorText)}
          </span>
          {unavailable ? null : (
            <button
              className="shrink-0 rounded border border-border/70 px-1.5 py-0.5 app-text-11 text-foreground hover:bg-white/5"
              onClick={onRetry}
              type="button"
            >
              {labels.retry}
            </button>
          )}
        </div>
      ) : null}
    </>
  );
}
