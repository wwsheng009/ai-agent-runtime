// P0：单层目录列表客户端（GET /api/runtime/fs/list）。
//
// 后端契约（backend/internal/filebrowse/list.go）：
//   query: scope, path(相对根), cursor, limit(≤1000), sort, show_hidden, dirs_first
//   响应体：{ dir: {path, abs_path, parent, is_root}, entries: [...],
//            next_cursor, has_more, truncated, sort }
//
// 归一化纪律：
//   * `entries` 非数组 → 抛错（不伪装成空目录）；
//   * `next_cursor` 非字符串（含 null）→ null；`has_more` 缺省 false；
//   * 缺 `type` 或未知 type → "unknown"（不猜成 file/dir，避免误进目录）；
//   * size/mtime 非有限数 → -1（后端探测失败的既有约定）。

import { buildRuntimeUrlWithQuery, fetchRuntimeJson, isRuntimeApiErrorCode } from "./shared";

import type {
  FsEntry,
  FsEntryType,
  FsListingRequest,
  FsListingResult,
  FsSortKey,
} from "@/types/runtime/fs-browser";

export const FS_LIST_PATH = "/api/runtime/fs/list";

type RawRecord = Record<string, unknown>;

const ENTRY_TYPES: readonly FsEntryType[] = [
  "dir",
  "file",
  "symlink",
  "inaccessible",
  "unknown",
];

const SORT_KEYS: readonly FsSortKey[] = [
  "name_asc",
  "name_desc",
  "mtime_desc",
  "size_desc",
  "type_then_name",
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

function normalizeEntry(raw: unknown): FsEntry | null {
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
  return {
    name: name || path,
    path,
    type: ENTRY_TYPES.includes(typeValue) ? typeValue : "unknown",
    size: asNumber(record.size),
    mtime: asNumber(record.mtime),
    ...(ext ? { ext } : {}),
    ...(typeof record.is_text === "boolean" ? { isText: record.is_text } : {}),
    ...(typeof record.is_symlink === "boolean" ? { isSymlink: record.is_symlink } : {}),
    ...(record.internal === true ? { internal: true } : {}),
  };
}

export function normalizeFsListingPayload(payload: unknown): FsListingResult {
  const root = asRecord(payload);
  const entriesRaw = root?.entries;
  if (!Array.isArray(entriesRaw)) {
    throw new Error("runtime fs listing payload is missing `entries`");
  }

  const dir = asRecord(root?.dir);
  const sortValue = asString(root?.sort) as FsSortKey;

  const entries: FsEntry[] = [];
  for (const item of entriesRaw) {
    const entry = normalizeEntry(item);
    if (entry) {
      entries.push(entry);
    }
  }

  return {
    dir: {
      path: asString(dir?.path),
      absPath: asString(dir?.abs_path),
      parent: asString(dir?.parent),
      isRoot: dir?.is_root === true,
    },
    entries,
    nextCursor: typeof root?.next_cursor === "string" && root.next_cursor ? root.next_cursor : null,
    hasMore: root?.has_more === true,
    truncated: root?.truncated === true,
    sort: SORT_KEYS.includes(sortValue) ? sortValue : "type_then_name",
  };
}

export type FsListRequestOptions = { signal?: AbortSignal };

export async function fetchFsListing(
  request: FsListingRequest,
  options: FsListRequestOptions = {},
): Promise<FsListingResult> {
  const scope = request.scope.trim();
  if (!scope) {
    throw new Error("fs listing requires a scope");
  }
  const url = buildRuntimeUrlWithQuery(FS_LIST_PATH, {
    scope,
    path: request.path ?? "",
    cursor: request.cursor ?? undefined,
    limit: request.limit,
    sort: request.sort,
    show_hidden: request.showHidden === undefined ? undefined : request.showHidden,
    dirs_first: request.dirsFirst === undefined ? undefined : request.dirsFirst,
  });
  const payload = await fetchRuntimeJson<unknown>(url, {
    method: "GET",
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return normalizeFsListingPayload(payload);
}

/** 游标失效的判定：UI 需要重置到第一页而不是重试同一游标（避免死循环）。 */
export function isFsListingCursorError(error: unknown): boolean {
  return isRuntimeApiErrorCode(error, "cursor_invalid");
}
