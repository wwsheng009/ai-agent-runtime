// P1：跨目录模糊搜索客户端单测（查询参数 / 载荷归一化 / 游标与降级判据）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  FS_SEARCH_PATH,
  fetchFsSearch,
  isFsSearchCursorError,
  isFsSearchQueryTooLongError,
  isFsSearchUnavailable,
  normalizeFsSearchPayload,
} from "@/api/runtime/fs-search";
import { RuntimeApiError } from "@/api/runtime/shared";

describe("normalizeFsSearchPayload", () => {
  it("归一化条目、命中位置与分页/截断字段", () => {
    const result = normalizeFsSearchPayload({
      scope: "session:s1",
      query: "compmenu",
      base: "frontend",
      items: [
        {
          name: "composer-menu.ts",
          path: "frontend/src/lib/composer-menu.ts",
          type: "file",
          size: 12345,
          mtime: 1758000000,
          ext: ".ts",
          score: 932,
          match: { field: "name", start: 0, end: 6 },
        },
      ],
      next_cursor: "eyJ2Ijox",
      has_more: true,
      scanned: 4213,
      truncated: true,
      truncated_reason: ["budget", "scan"],
      elapsed_ms: 37,
      limit: 20,
    });

    expect(result.scope).toBe("session:s1");
    expect(result.query).toBe("compmenu");
    expect(result.base).toBe("frontend");
    expect(result.items[0]).toEqual({
      name: "composer-menu.ts",
      path: "frontend/src/lib/composer-menu.ts",
      type: "file",
      size: 12345,
      mtime: 1758000000,
      ext: ".ts",
      score: 932,
      match: { field: "name", start: 0, end: 6 },
    });
    expect(result.nextCursor).toBe("eyJ2Ijox");
    expect(result.hasMore).toBe(true);
    expect(result.scanned).toBe(4213);
    expect(result.truncated).toBe(true);
    expect(result.truncatedReasons).toEqual(["budget", "scan"]);
    expect(result.elapsedMs).toBe(37);
    expect(result.limit).toBe(20);
  });

  it("未知 type 收口 unknown、非法 score/size/mtime 收口 0/-1", () => {
    const result = normalizeFsSearchPayload({
      items: [
        { name: "weird", path: "weird", type: "portal", size: "big", mtime: null, score: -3 },
        { name: "dir", path: "dir", type: "dir", size: -1, mtime: 0, score: 0 },
      ],
    });

    expect(result.items[0]).toEqual({
      name: "weird",
      path: "weird",
      type: "unknown",
      size: -1,
      mtime: -1,
      score: 0,
    });
    expect(result.items[1]).toMatchObject({ type: "dir", size: -1, mtime: 0, score: 0 });
  });

  it("缺 name 时用 path 兜底；name/path 全缺或非对象条目跳过", () => {
    const result = normalizeFsSearchPayload({
      items: [{ path: "a/b.txt" }, { name: "  " }, "not-an-object", null],
    });

    expect(result.items).toEqual([
      { name: "a/b.txt", path: "a/b.txt", type: "unknown", size: -1, mtime: -1, score: 0 },
    ]);
  });

  it("match 非法（字段未知/偏移非数/越界反序）时整体丢弃，不猜高亮位置", () => {
    const result = normalizeFsSearchPayload({
      items: [
        { name: "a", path: "a", match: { field: "content", start: 0, end: 1 } },
        { name: "b", path: "b", match: { field: "name", start: "0", end: 1 } },
        { name: "c", path: "c", match: { field: "name", start: 5, end: 2 } },
        { name: "d", path: "d", match: { field: "path", start: -1, end: 2 } },
        { name: "e", path: "e", match: { field: "path", start: 1, end: 3 } },
      ],
    });

    expect(result.items.slice(0, 4).every((item) => item.match === undefined)).toBe(true);
    expect(result.items[4]?.match).toEqual({ field: "path", start: 1, end: 3 });
  });

  it("next_cursor 空串/null 收口 null；未知 truncated_reason 丢弃并去重", () => {
    const result = normalizeFsSearchPayload({
      items: [],
      next_cursor: "",
      truncated_reason: ["budget", "wat", "budget", 7],
    });

    expect(result.nextCursor).toBeNull();
    expect(result.hasMore).toBe(false);
    expect(result.truncated).toBe(false);
    expect(result.truncatedReasons).toEqual(["budget"]);
  });

  it("缺 items 数组直接抛错（不伪装成空结果）", () => {
    expect(() => normalizeFsSearchPayload({ scope: "cwd" })).toThrow(/missing `items`/);
    expect(() => normalizeFsSearchPayload(null)).toThrow(/missing `items`/);
  });

  it("truncated 与 has_more 正交：truncated=true 且 has_more=false 是合法组合", () => {
    const result = normalizeFsSearchPayload({
      items: [],
      truncated: true,
      truncated_reason: ["depth"],
      has_more: false,
    });

    expect(result.truncated).toBe(true);
    expect(result.truncatedReasons).toEqual(["depth"]);
    expect(result.hasMore).toBe(false);
    expect(result.nextCursor).toBeNull();
  });
});

describe("fetchFsSearch", () => {
  const originalFetch = globalThis.fetch;
  let calls: Array<{ url: string; init?: RequestInit }> = [];

  function respondWith(body: unknown, status = 200) {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }) as typeof fetch;
  }

  beforeEach(() => {
    calls = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("GET /fs/search 并映射查询参数（scope 去空白、q 始终下发、空 cursor 不下发）", async () => {
    respondWith({ items: [] });

    await fetchFsSearch({
      scope: "  session:s1  ",
      query: "compmenu",
      path: "frontend",
      cursor: null,
      limit: 20,
      showHidden: false,
      kinds: "file",
      maxDepth: 8,
      maxScan: 20000,
      budgetMs: 250,
    });

    const url = new URL(calls[0].url, "http://runtime.test");
    expect(url.pathname).toBe(FS_SEARCH_PATH);
    expect(url.searchParams.get("scope")).toBe("session:s1");
    expect(url.searchParams.get("q")).toBe("compmenu");
    expect(url.searchParams.get("path")).toBe("frontend");
    expect(url.searchParams.get("limit")).toBe("20");
    expect(url.searchParams.get("show_hidden")).toBe("false");
    expect(url.searchParams.get("kinds")).toBe("file");
    expect(url.searchParams.get("max_depth")).toBe("8");
    expect(url.searchParams.get("max_scan")).toBe("20000");
    expect(url.searchParams.get("budget_ms")).toBe("250");
    expect(url.searchParams.has("cursor")).toBe(false);
  });

  it("缺省 query 不下发 q（后端缺省与空串同义 = 浅层首屏小批量）", async () => {
    respondWith({ items: [] });

    await fetchFsSearch({ scope: "cwd" });

    const url = new URL(calls[0].url, "http://runtime.test");
    expect(url.searchParams.has("q")).toBe(false);
  });

  it("空 scope 不发请求，直接抛错", async () => {
    respondWith({ items: [] });

    await expect(fetchFsSearch({ scope: "   " })).rejects.toThrow(/requires a scope/);
    expect(calls).toHaveLength(0);
  });

  it("cursor_invalid / query_too_long 错误码被识别", async () => {
    respondWith({ error: "cursor is no longer valid", code: "cursor_invalid" }, 400);
    const cursorError = await fetchFsSearch({ scope: "cwd", cursor: "stale" }).catch(
      (caught: unknown) => caught,
    );
    expect(cursorError).toBeInstanceOf(RuntimeApiError);
    expect(isFsSearchCursorError(cursorError)).toBe(true);

    respondWith({ error: "query too long", code: "query_too_long" }, 400);
    const longError = await fetchFsSearch({ scope: "cwd", query: "x".repeat(300) }).catch(
      (caught: unknown) => caught,
    );
    expect(isFsSearchQueryTooLongError(longError)).toBe(true);

    expect(isFsSearchCursorError(new RuntimeApiError(500, null))).toBe(false);
    expect(isFsSearchQueryTooLongError(cursorError)).toBe(false);
  });

  it("unavailable 判据只认 404/405/501/503（503 表示端点未就绪，调用方退回本地过滤）", () => {
    expect(isFsSearchUnavailable(new RuntimeApiError(503, null))).toBe(true);
    expect(isFsSearchUnavailable(new RuntimeApiError(404, null))).toBe(true);
    expect(isFsSearchUnavailable(new RuntimeApiError(405, null))).toBe(true);
    expect(isFsSearchUnavailable(new RuntimeApiError(501, null))).toBe(true);
    expect(isFsSearchUnavailable(new RuntimeApiError(500, null))).toBe(false);
    expect(isFsSearchUnavailable(new RuntimeApiError(400, null))).toBe(false);
    expect(isFsSearchUnavailable(new Error("boom"))).toBe(false);
  });
});
