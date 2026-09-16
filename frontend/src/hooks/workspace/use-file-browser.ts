// 文件浏览器目录层状态机（规划文档 §4.3 / 阶段 P0-5）。
//
// 后端契约：GET /fs/list → 单层项 + next_cursor + has_more + truncated；排序与隐藏是**服务端**参数，
//   每次分页都要下发，否则「只排了前 200 项却显示为全局有序」。（作用域根 /fs/roots 由面组件
//   file-browser-surface.tsx 负责取回并做降级，本 hook 只消费 scope。）
//
// 归一化纪律（竞态口径对齐 hooks/workspace/use-file-preview.ts）：
//   * 每层（相对路径）独立「请求序号 + AbortController」，只有最新结果写回该层；
//   * 切换作用域 / 改排序参数 / 卸载时中止在途请求；主动取消（AbortError）不落 error 态；
//   * next_cursor 或 has_more 缺失一律按「无更多」，不臆造下一页；cursor_invalid → 标记「需重置
//     到第一页」（UI 提示后走 refresh），不拿同一游标死循环重试；
//   * truncated=true 原样透出（UI 提示「本层仅显示前 N 项」），不静默当完整列表。
//
// 降级判据：层加载失败保留已加载项并置 error 供重试；scope 为空（根未就绪 / 已降级为无工作目录）
//   时完全不发请求，UI 呈现空态而不是伪造目录内容。

import { useCallback, useEffect, useRef, useState } from "react";

import { fetchFsListing, isFsListingCursorError } from "@/api/runtime/fs-list";
import { DEFAULT_SORT_KEY, normalizeSortKey } from "@/lib/file-browser/entry-sort";
import { normalizeRelativePath } from "@/lib/file-browser/path-utils";
import type { FsEntry, FsListingResult, FsSortKey } from "@/types/runtime/fs-browser";

/** 单层页大小：首屏快，同时覆盖常见目录（后端上限 1000）。 */
export const FS_LISTING_PAGE_LIMIT = 200;
/** 进入目录的 TTL：窗口内重复点击复用缓存，超时重拉，避免长期脏数据。 */
export const FS_LISTING_TTL_MS = 5_000;

export type FileBrowserListingStatus = "idle" | "loading" | "ready" | "loading_more" | "error";

export type FileBrowserListing = {
  status: FileBrowserListingStatus;
  entries: FsEntry[];
  nextCursor: string | null;
  hasMore: boolean;
  truncated: boolean;
  error: unknown;
  /** cursor_invalid：必须重置到第一页（UI 提示后走 refresh，而不是重试同一游标）。 */
  cursorExpired: boolean;
  loadedAt: number;
};

export function createIdleListing(): FileBrowserListing {
  return {
    status: "idle",
    entries: [],
    nextCursor: null,
    hasMore: false,
    truncated: false,
    error: null,
    cursorExpired: false,
    loadedAt: 0,
  };
}

/** 分页结果 → 层状态：无上一页则替换，有则按 path 去重追加（游标抖动不重复渲染）。 */
export function applyListingPage(
  previous: FileBrowserListing | undefined,
  page: FsListingResult,
  receivedAt: number,
): FileBrowserListing {
  const seen = new Set((previous?.entries ?? []).map((entry) => entry.path));
  const entries = previous
    ? [...previous.entries, ...page.entries.filter((entry) => !seen.has(entry.path))]
    : page.entries;
  return {
    status: "ready",
    entries,
    nextCursor: page.nextCursor,
    hasMore: page.hasMore && page.nextCursor !== null,
    truncated: (previous?.truncated ?? false) || page.truncated,
    error: null,
    cursorExpired: false,
    loadedAt: receivedAt,
  };
}

/** TTL 内且已就绪 → 复用缓存，不重复请求。 */
export function isFreshListing(
  listing: FileBrowserListing | undefined,
  now: number,
  ttlMs = FS_LISTING_TTL_MS,
): boolean {
  return Boolean(listing?.status === "ready" && listing.loadedAt > 0 && now - listing.loadedAt < ttlMs);
}

/** 主动取消（AbortController.abort）不落错误态；网络/超时/HTTP 错误照常上抛。 */
export function isAbortError(error: unknown): boolean {
  if (typeof DOMException !== "undefined" && error instanceof DOMException) {
    return error.name === "AbortError";
  }
  return error instanceof Error && error.name === "AbortError";
}

export type UseFileBrowserOptions = { scope: string };

export type UseFileBrowserResult = {
  scope: string;
  /** 相对路径（"" 为根层）→ 已加载层；只含已加载目录，未加载目录不臆造。 */
  listings: Record<string, FileBrowserListing>;
  currentDir: string;
  expanded: ReadonlySet<string>;
  selectedPath: string | null;
  showHidden: boolean;
  sortKey: FsSortKey;
  enterDir: (dirPath: string) => void;
  toggleDir: (dirPath: string) => void;
  selectEntry: (entry: FsEntry) => void;
  loadListing: (dirPath: string, mode: "first" | "more", force?: boolean) => boolean;
  setShowHidden: (value: boolean) => void;
  setSortKey: (key: FsSortKey) => void;
};

export function useFileBrowser({ scope }: UseFileBrowserOptions): UseFileBrowserResult {
  const [listings, setListings] = useState<Record<string, FileBrowserListing>>({});
  const [currentDir, setCurrentDir] = useState("");
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set<string>());
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [showHidden, setShowHidden] = useState(false);
  const [sortKey, setSortKeyValue] = useState<FsSortKey>(DEFAULT_SORT_KEY);

  const scopeRef = useRef(scope);
  const expandedRef = useRef(expanded);
  const listingsRef = useRef<Record<string, FileBrowserListing>>({});
  const sequencesRef = useRef(new Map<string, number>());
  const controllersRef = useRef(new Map<string, AbortController>());
  const paramsRef = useRef({ sortKey, showHidden });

  scopeRef.current = scope;
  expandedRef.current = expanded;
  paramsRef.current = { sortKey, showHidden };

  const abortAll = useCallback(() => {
    for (const controller of controllersRef.current.values()) {
      controller.abort();
    }
    controllersRef.current.clear();
    sequencesRef.current.clear();
  }, []);

  const writeListing = useCallback((dir: string, listing: FileBrowserListing) => {
    listingsRef.current = { ...listingsRef.current, [dir]: listing };
    setListings((previous) => ({ ...previous, [dir]: listing }));
  }, []);

  const loadListing = useCallback(
    (dirPath: string, mode: "first" | "more", force = false) => {
      const activeScope = scopeRef.current;
      if (!activeScope) {
        return false;
      }
      const dir = normalizeRelativePath(dirPath);
      const previous = listingsRef.current[dir];
      if (mode === "first" && !force && isFreshListing(previous, Date.now())) {
        return false;
      }
      const canContinue = Boolean(previous?.hasMore) && previous?.nextCursor !== null;
      if (mode === "more" && (!canContinue || previous?.status === "loading_more")) {
        return false;
      }
      controllersRef.current.get(dir)?.abort();
      const seq = (sequencesRef.current.get(dir) ?? 0) + 1;
      sequencesRef.current.set(dir, seq);
      const controller = new AbortController();
      controllersRef.current.set(dir, controller);
      writeListing(dir, {
        ...(mode === "more" && previous ? previous : createIdleListing()),
        status: mode === "more" ? "loading_more" : "loading",
        error: null,
        cursorExpired: false,
      });

      void (async () => {
        try {
          const page = await fetchFsListing(
            {
              scope: activeScope,
              path: dir,
              cursor: mode === "more" ? previous?.nextCursor ?? undefined : undefined,
              limit: FS_LISTING_PAGE_LIMIT,
              sort: paramsRef.current.sortKey,
              showHidden: paramsRef.current.showHidden,
              dirsFirst: true,
            },
            { signal: controller.signal },
          );
          if (sequencesRef.current.get(dir) !== seq) {
            return;
          }
          writeListing(dir, applyListingPage(mode === "more" ? previous : undefined, page, Date.now()));
        } catch (caught) {
          if (sequencesRef.current.get(dir) !== seq || isAbortError(caught)) {
            return;
          }
          // 保留已加载项（可能只是翻页失败），置错误态供 UI 重试。
          const cursorExpired = isFsListingCursorError(caught);
          writeListing(dir, {
            ...(mode === "more" && previous ? previous : createIdleListing()),
            status: "error",
            error: caught,
            cursorExpired,
            // 游标失效：旧游标已作废，不能继续提供「加载更多」（否则同一失效游标会被反复重试）。
            ...(cursorExpired ? { hasMore: false, nextCursor: null } : {}),
          });
        } finally {
          if (controllersRef.current.get(dir) === controller) {
            controllersRef.current.delete(dir);
          }
        }
      })();
      return true;
    },
    [writeListing],
  );

  // 作用域切换 / 卸载：中止在途请求并作废缓存（旧作用域的层数据不得复用）。
  useEffect(() => {
    return () => abortAll();
  }, [abortAll]);
  useEffect(() => {
    abortAll();
    listingsRef.current = {};
    setListings({});
    setExpanded(new Set<string>());
    setSelectedPath(null);
    setCurrentDir("");
    if (scope) {
      loadListing("", "first", true);
    }
  }, [scope, abortAll, loadListing]);

  const paramsToken = `${sortKey}|${showHidden}`;
  const paramsAppliedRef = useRef(paramsToken);
  useEffect(() => {
    if (paramsAppliedRef.current === paramsToken) {
      return;
    }
    paramsAppliedRef.current = paramsToken;
    if (!scopeRef.current) {
      return;
    }
    // 排序/隐藏是服务端分页参数：旧游标与旧首屏都不可信 → 作废缓存后按当前展开重拉。
    const openDirs = [...expandedRef.current];
    abortAll();
    listingsRef.current = {};
    setListings({});
    loadListing("", "first", true);
    for (const dir of openDirs) {
      loadListing(dir, "first", true);
    }
  }, [paramsToken, abortAll, loadListing]);

  const enterDir = useCallback(
    (dirPath: string) => {
      const dir = normalizeRelativePath(dirPath);
      setCurrentDir(dir);
      setSelectedPath(null);
      loadListing(dir, "first");
    },
    [loadListing],
  );

  const toggleDir = useCallback(
    (dirPath: string) => {
      const dir = normalizeRelativePath(dirPath);
      const isOpen = expandedRef.current.has(dir);
      setExpanded((previous) => {
        const next = new Set(previous);
        if (isOpen) {
          next.delete(dir);
        } else {
          next.add(dir);
        }
        return next;
      });
      if (!isOpen) {
        loadListing(dir, "first");
      }
    },
    [loadListing],
  );

  const selectEntry = useCallback(
    (entry: FsEntry) => {
      // 只有 type==="dir" 才允许进入；unknown / symlink / inaccessible 只选中，绝不伪装成目录。
      if (entry.type === "dir") {
        toggleDir(entry.path);
        return;
      }
      setSelectedPath(entry.path);
    },
    [toggleDir],
  );

  return {
    scope,
    listings,
    currentDir,
    expanded,
    selectedPath,
    showHidden,
    sortKey,
    enterDir,
    toggleDir,
    selectEntry,
    loadListing,
    setShowHidden,
    setSortKey: useCallback((key: FsSortKey) => setSortKeyValue(normalizeSortKey(key)), []),
  };
}
