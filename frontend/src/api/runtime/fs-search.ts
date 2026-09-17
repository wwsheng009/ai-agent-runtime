// P1：跨目录模糊搜索客户端（GET /api/runtime/fs/search）。
//
// 后端契约（backend/internal/filebrowse/search.go，规划 §4.4）：
//   query: scope, q(字面匹配，≤256 rune), path(相对根，可空), cursor, limit(≤50),
//          show_hidden, kinds(file|dir|both), max_depth(≤16), max_scan(≤50000), budget_ms(≤1000)
//   `q` 按字面匹配（不解释正则/通配符）；空 `q` = 浅层首屏小批量。
//   响应体见 `FsSearchResult`；`truncated`（可能不完备）与 `has_more`（还有下一页）正交。
//
// 归一化纪律（与 fs-list.ts 同款；证据：`fs-list.ts:8-12`）：
//   * `items` 非数组 → 抛错（不伪装成空结果，否则「后端坏了」会被渲染成「没搜到」）；
//   * `next_cursor` 非字符串/空串 → null；`has_more`/`truncated` 缺省 false；
//   * 缺 `type` 或未知 type → "unknown"（不猜成 file/dir，避免把目录当文件预览）；
//   * size/mtime 非有限数 → -1（后端探测失败的既有约定）；
//   * 未知 `truncated_reason` 枚举值丢弃（归因不猜）；`match` 越界/非数时不输出（不高亮错位置）。

import { buildRuntimeUrlWithQuery, fetchRuntimeJson, RuntimeApiError, isRuntimeApiErrorCode } from "./shared";

import type {
  FsEntryType,
  FsSearchItem,
  FsSearchMatch,
  FsSearchRequest,
  FsSearchResult,
  FsSearchTruncatedReason,
} from "@/types/runtime/fs-browser";

export const FS_SEARCH_PATH = "/api/runtime/fs/search";

type RawRecord = Record<string, unknown>;

const ENTRY_TYPES: readonly FsEntryType[] = [
  "dir",
  "file",
  "symlink",
  "inaccessible",
  "unknown",
];

const TRUNCATED_REASONS: readonly FsSearchTruncatedReason[] = [
  "depth",
  "scan",
  "budget",
  "dir_entries",
];

function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

function asString(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function asNumber(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : -1;
}

/** 非负整数；缺失/负数/非有限 → 0（`scanned`/`elapsed_ms`/`limit` 用）。 */
function asCount(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : 0;
}

/** 命中位置：`field` 只认 name|path，偏移必须是有限非负整数且 end ≥ start，否则整体丢弃。 */
function normalizeMatch(raw: unknown): FsSearchMatch | undefined {
  const record = asRecord(raw);
  if (!record) {
    return undefined;
  }
  const field = asString(record.field);
  if (field !== "name" && field !== "path") {
    return undefined;
  }
  const start = record.start;
  const end = record.end;
  if (
    typeof start !== "number" ||
    typeof end !== "number" ||
    !Number.isFinite(start) ||
    !Number.isFinite(end) ||
    start < 0 ||
    end < start
  ) {
    return undefined;
  }
  return { field, start: Math.floor(start), end: Math.floor(end) };
}

function normalizeItem(raw: unknown): FsSearchItem | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const name = asString(record.name);
  const path = asString(record.path);
  if (!name && !path) {
    return null;
  }
  const typeValue = asString(record.type) as FsEntryType;
  const ext = asString(record.ext);
  const match = normalizeMatch(record.match);
  const score = asNumber(record.score);
  return {
    name: name || path,
    path,
    type: ENTRY_TYPES.includes(typeValue) ? typeValue : "unknown",
    size: asNumber(record.size),
    mtime: asNumber(record.mtime),
    ...(ext ? { ext } : {}),
    score: score >= 0 ? score : 0,
    ...(match ? { match } : {}),
  };
}

export function normalizeFsSearchPayload(payload: unknown): FsSearchResult {
  const root = asRecord(payload);
  const itemsRaw = root?.items;
  if (!Array.isArray(itemsRaw)) {
    throw new Error("runtime fs search payload is missing `items`");
  }

  const items: FsSearchItem[] = [];
  for (const item of itemsRaw) {
    const normalized = normalizeItem(item);
    if (normalized) {
      items.push(normalized);
    }
  }

  const reasonsRaw = root?.truncated_reason;
  const truncatedReasons: FsSearchTruncatedReason[] = [];
  if (Array.isArray(reasonsRaw)) {
    for (const reason of reasonsRaw) {
      const value = asString(reason) as FsSearchTruncatedReason;
      if (TRUNCATED_REASONS.includes(value) && !truncatedReasons.includes(value)) {
        truncatedReasons.push(value);
      }
    }
  }

  return {
    scope: asString(root?.scope),
    query: asString(root?.query),
    base: asString(root?.base),
    items,
    nextCursor: typeof root?.next_cursor === "string" && root.next_cursor ? root.next_cursor : null,
    hasMore: root?.has_more === true,
    scanned: asCount(root?.scanned),
    truncated: root?.truncated === true,
    truncatedReasons,
    elapsedMs: asCount(root?.elapsed_ms),
    limit: asCount(root?.limit),
  };
}

export type FsSearchRequestOptions = { signal?: AbortSignal };

export async function fetchFsSearch(
  request: FsSearchRequest,
  options: FsSearchRequestOptions = {},
): Promise<FsSearchResult> {
  const scope = request.scope.trim();
  if (!scope) {
    throw new Error("fs search requires a scope");
  }
  // 空串参数被 `buildRuntimeUrlWithQuery` 省略（`q=`/`path=`/`cursor=` 同款）：
  // 后端缺省值与空串同义（`q` 缺省 ""，空 path = 作用域根），语义不变。
  const url = buildRuntimeUrlWithQuery(FS_SEARCH_PATH, {
    scope,
    q: request.query,
    path: request.path,
    cursor: request.cursor ?? undefined,
    limit: request.limit,
    show_hidden: request.showHidden === undefined ? undefined : request.showHidden,
    kinds: request.kinds,
    max_depth: request.maxDepth,
    max_scan: request.maxScan,
    budget_ms: request.budgetMs,
  });
  const payload = await fetchRuntimeJson<unknown>(url, {
    method: "GET",
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return normalizeFsSearchPayload(payload);
}

/** 游标失效：UI 需要重置到第一页而不是重试同一游标（避免死循环，同 `isFsListingCursorError`）。 */
export function isFsSearchCursorError(error: unknown): boolean {
  return isRuntimeApiErrorCode(error, "cursor_invalid");
}

/** 查询串超长（400 `query_too_long`）：UI 应截断/提示而不是原样重试。 */
export function isFsSearchQueryTooLongError(error: unknown): boolean {
  return isRuntimeApiErrorCode(error, "query_too_long");
}

/** 404/405/501/503 = 端点未就绪：调用方退回本地过滤/首屏批量，不再重试（规划 §4.7.5）。 */
export function isFsSearchUnavailable(error: unknown): boolean {
  return (
    error instanceof RuntimeApiError &&
    (error.status === 404 || error.status === 405 || error.status === 501 || error.status === 503)
  );
}
