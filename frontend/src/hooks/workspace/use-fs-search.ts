// P1：跨目录模糊搜索的共享状态机（composer `@` 引用与右侧文件浏览器搜索共用一套纪律）。
//
// 后端契约：GET /fs/search（backend/internal/filebrowse/search.go，规划 §4.4）。
//   `q` 按字面匹配；空 `q` 是浅层首屏小批量（composer 首屏仍走 P0 的 /fs/list）。
//
// 竞态与降级纪律（与 use-file-browser.ts 同口径）：
//   * 请求序号 + AbortController：只有最新序号的响应写回；主动取消（AbortError）不落 error；
//   * `scope`/`query`/`showHidden`/`kinds`/`limit` 变化：作废在途并重置（scope 变化还会清空结果，
//     避免把上一个作用域的命中显示给新作用域）；输入变化保留旧结果 + status="loading"（不闪空）；
//   * 失败保留上次成功结果并置 error 供重试；`unavailable`（404/405/501/503）单独标记，调用方不再重试；
//   * `cursor_invalid`：翻页失败时重置到第一页，不拿同一游标死循环；
//   * `has_more` 缺失/无游标一律按「无更多」，不臆造下一页；
//   * 翻页追加按 `path` 去重（游标抖动允许重复返回）。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  fetchFsSearch,
  isFsSearchCursorError,
  isFsSearchUnavailable,
} from "@/api/runtime/fs-search";
import { isAbortError } from "@/hooks/workspace/use-file-browser";
import type {
  FsSearchItem,
  FsSearchKind,
  FsSearchResult,
  FsSearchTruncatedReason,
} from "@/types/runtime/fs-browser";

/** 防抖：composer 输入快（200ms），面板结果重（300ms）。 */
export const FS_SEARCH_DEBOUNCE_COMPOSER_MS = 200;
export const FS_SEARCH_DEBOUNCE_PANEL_MS = 300;

/** 页大小：composer 只渲染 ≤8 条（多取一页供「结果较多」提示）；面板需要更多。 */
export const FS_SEARCH_LIMIT_COMPOSER = 20;
export const FS_SEARCH_LIMIT_PANEL = 50;

export type FsSearchStatus = "idle" | "loading" | "ready" | "error";

export type UseFsSearchQueryOptions = {
  scope: string;
  query: string;
  enabled: boolean;
  debounceMs?: number;
  limit?: number;
  kinds?: FsSearchKind;
  showHidden?: boolean;
  /**
   * 停用（`enabled=false`）时保留上次结果：
   * composer 关闭菜单再打开时列表不闪空；默认 false（面板退回树视图不残留搜索态）。
   * 作用域变化仍然强制清空（旧作用域的结果不得跨作用域复用）。
   */
  keepResultsWhenDisabled?: boolean;
};

export type UseFsSearchQueryResult = {
  items: FsSearchItem[];
  status: FsSearchStatus;
  hasMore: boolean;
  loadingMore: boolean;
  truncated: boolean;
  truncatedReasons: FsSearchTruncatedReason[];
  /** 本次实际扫描条目数（面板页脚「已扫描 N 项」用）。 */
  scanned: number;
  nextCursor: string | null;
  error: unknown;
  /** 端点未就绪（404/405/501/503）：调用方退回本地过滤/首屏路径，且不再重试（规划 §4.7.5）。 */
  unavailable: boolean;
  loadMore: () => void;
  retry: () => void;
};

type SearchState = Omit<UseFsSearchQueryResult, "loadMore" | "retry">;

export function createIdleSearchState(): SearchState {
  return {
    items: [],
    status: "idle",
    hasMore: false,
    loadingMore: false,
    truncated: false,
    truncatedReasons: [],
    scanned: 0,
    nextCursor: null,
    error: null,
    unavailable: false,
  };
}

function mergeReasons(
  previous: readonly FsSearchTruncatedReason[],
  next: readonly FsSearchTruncatedReason[],
): FsSearchTruncatedReason[] {
  const merged: FsSearchTruncatedReason[] = [...previous];
  for (const reason of next) {
    if (!merged.includes(reason)) {
      merged.push(reason);
    }
  }
  return merged;
}

/** 分页合成：首页替换、翻页按 path 去重追加（对齐 `applyListingPage` 的去重口径）。 */
export function applySearchPage(
  previous: SearchState,
  page: FsSearchResult,
  append: boolean,
): SearchState {
  const seen = append ? new Set(previous.items.map((item) => item.path)) : null;
  const items = append
    ? [...previous.items, ...page.items.filter((item) => !seen?.has(item.path))]
    : page.items;
  return {
    items,
    status: "ready",
    loadingMore: false,
    // 无游标就不提供「加载更多」，避免同一失效游标被反复重试。
    hasMore: page.hasMore && page.nextCursor !== null,
    truncated: (append ? previous.truncated : false) || page.truncated,
    truncatedReasons: mergeReasons(append ? previous.truncatedReasons : [], page.truncatedReasons),
    // scanned 跨页累计：每页都是全量重扫，这里累加各页扫描量，仅供页脚「已扫描 N 条」展示。
    scanned: (append ? previous.scanned : 0) + page.scanned,
    nextCursor: page.nextCursor,
    error: null,
    unavailable: false,
  };
}

export function useFsSearchQuery(options: UseFsSearchQueryOptions): UseFsSearchQueryResult {
  const {
    scope,
    query,
    enabled,
    debounceMs = FS_SEARCH_DEBOUNCE_COMPOSER_MS,
    limit = FS_SEARCH_LIMIT_COMPOSER,
    kinds = "file",
    showHidden = false,
    keepResultsWhenDisabled = false,
  } = options;

  const [state, setState] = useState<SearchState>(createIdleSearchState);

  const stateRef = useRef(state);
  const sequenceRef = useRef(0);
  const controllerRef = useRef<AbortController | null>(null);
  const scopeAppliedRef = useRef<string>("");
  const paramsRef = useRef({ scope, query, kinds, limit, showHidden });

  stateRef.current = state;
  paramsRef.current = { scope, query, kinds, limit, showHidden };

  const abortInFlight = useCallback(() => {
    controllerRef.current?.abort();
    controllerRef.current = null;
    // 递增序号：被中止的响应即使后到也不会写回。
    sequenceRef.current += 1;
  }, []);

  const start = useCallback((mode: "first" | "more") => {
    const activeScope = paramsRef.current.scope.trim();
    const activeQuery = paramsRef.current.query.trim();
    if (!activeScope || !activeQuery) {
      return;
    }
    const previous = stateRef.current;
    if (mode === "more" && (previous.loadingMore || !previous.hasMore || !previous.nextCursor)) {
      return;
    }

    controllerRef.current?.abort();
    const sequence = sequenceRef.current + 1;
    sequenceRef.current = sequence;
    const controller = new AbortController();
    controllerRef.current = controller;

    // 函数式写入：catch 分支（如 cursor 失效重置）可能刚排入队列，不能被这里用旧快照覆盖。
    setState((current) => ({
      ...current,
      status: mode === "first" ? "loading" : current.status,
      loadingMore: mode === "more",
      ...(mode === "first" ? { error: null, unavailable: false } : {}),
    }));

    void (async () => {
      try {
        const page = await fetchFsSearch(
          {
            scope: activeScope,
            query: activeQuery,
            cursor: mode === "more" ? previous.nextCursor : null,
            limit: paramsRef.current.limit,
            kinds: paramsRef.current.kinds,
            showHidden: paramsRef.current.showHidden,
          },
          { signal: controller.signal },
        );
        if (sequenceRef.current !== sequence) {
          return;
        }
        setState((current) => applySearchPage(current, page, mode === "more"));
      } catch (caught) {
        if (sequenceRef.current !== sequence || isAbortError(caught)) {
          return;
        }
        const cursorExpired = isFsSearchCursorError(caught);
        if (cursorExpired && mode === "more") {
          // 游标失效：作废游标并回到第一页（不重试同一游标），失败本身不算错误态。
          setState((current) => ({
            ...current,
            loadingMore: false,
            hasMore: false,
            nextCursor: null,
          }));
          start("first");
          return;
        }
        setState((current) => ({
          ...current,
          status: "error",
          loadingMore: false,
          error: caught,
          unavailable: isFsSearchUnavailable(caught),
          ...(cursorExpired ? { hasMore: false, nextCursor: null } : {}),
        }));
      } finally {
        if (controllerRef.current === controller) {
          controllerRef.current = null;
        }
      }
    })();
  }, []);

  const activeScope = scope.trim();
  const activeQuery = query.trim();
  const active = enabled && activeScope !== "" && activeQuery !== "";

  // 参数键：任一变化都作废在途（含 scope 变化时清空结果），并重新起一次防抖首屏请求。
  const requestKey = `${active ? 1 : 0}|${activeScope}|${activeQuery}|${kinds}|${limit}|${
    showHidden ? 1 : 0
  }`;

  useEffect(() => {
    const scopeChanged = scopeAppliedRef.current !== activeScope;
    scopeAppliedRef.current = activeScope;
    abortInFlight();

    if (!active) {
      if (!keepResultsWhenDisabled || scopeChanged) {
        setState(createIdleSearchState());
      }
      return;
    }

    // 保留旧结果（输入变化不闪空）；首次进入某作用域时没有可保留的结果。
    setState((current) =>
      scopeChanged
        ? { ...createIdleSearchState(), status: "loading" }
        : { ...current, status: "loading", loadingMore: false, error: null, unavailable: false },
    );

    const timer = window.setTimeout(() => start("first"), debounceMs);
    return () => window.clearTimeout(timer);
    // requestKey 已覆盖 scope/query/kinds/limit/showHidden/active；start/abortInFlight 恒稳定。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [requestKey, debounceMs, start, abortInFlight]);

  // 卸载：中止在途请求。
  useEffect(() => {
    return () => abortInFlight();
  }, [abortInFlight]);

  const loadMore = useCallback(() => start("more"), [start]);
  const retry = useCallback(() => start("first"), [start]);

  return useMemo(
    () => ({ ...state, loadMore, retry }),
    [state, loadMore, retry],
  );
}
