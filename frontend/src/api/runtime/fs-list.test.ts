// P0：单层目录列表客户端单测（查询参数 / 载荷归一化 / 游标失效判据）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  FS_LIST_PATH,
  fetchFsListing,
  isFsListingCursorError,
  normalizeFsListingPayload,
} from "@/api/runtime/fs-list";
import { RuntimeApiError } from "@/api/runtime/shared";

describe("normalizeFsListingPayload", () => {
  it("归一化目录头、条目与分页字段", () => {
    const result = normalizeFsListingPayload({
      dir: { path: "src", abs_path: "E:/repo/src", parent: "", is_root: false },
      entries: [
        {
          name: "components",
          path: "src/components",
          type: "dir",
          size: -1,
          mtime: 1720000000,
        },
        {
          name: "main.ts",
          path: "src/main.ts",
          type: "file",
          size: 1200,
          mtime: 1720000100,
          ext: "ts",
          is_text: true,
        },
      ],
      next_cursor: "c-2",
      has_more: true,
      truncated: false,
      sort: "name_asc",
    });

    expect(result.dir).toEqual({
      path: "src",
      absPath: "E:/repo/src",
      parent: "",
      isRoot: false,
    });
    expect(result.entries[1]).toMatchObject({ ext: "ts", isText: true, size: 1200 });
    expect(result.nextCursor).toBe("c-2");
    expect(result.hasMore).toBe(true);
    expect(result.sort).toBe("name_asc");
  });

  it("未知 type 收口 unknown，非法 size/mtime 收口 -1", () => {
    const result = normalizeFsListingPayload({
      dir: {},
      entries: [
        { name: "weird", path: "weird", type: "portal", size: "big", mtime: null },
        { name: "link", path: "link", type: "symlink", size: 0, mtime: 0, is_symlink: true },
      ],
    });

    expect(result.entries[0]).toMatchObject({ type: "unknown", size: -1, mtime: -1 });
    expect(result.entries[1]).toMatchObject({ type: "symlink", isSymlink: true, size: 0 });
  });

  it("缺 name 时用 path 兜底；name/path 全缺的条目跳过", () => {
    const result = normalizeFsListingPayload({
      entries: [{ path: "a/b.txt" }, { name: "  " }, "not-an-object"],
    });

    expect(result.entries).toEqual([
      {
        name: "a/b.txt",
        path: "a/b.txt",
        type: "unknown",
        size: -1,
        mtime: -1,
      },
    ]);
  });

  it("next_cursor 空串/null 收口 null；sort 非白名单回落 type_then_name", () => {
    const result = normalizeFsListingPayload({
      entries: [],
      next_cursor: "",
      sort: "random",
    });

    expect(result.nextCursor).toBeNull();
    expect(result.sort).toBe("type_then_name");
    expect(result.hasMore).toBe(false);
    expect(result.truncated).toBe(false);
  });

  it("内部项标记（上传分片目录）透传为 internal", () => {
    const result = normalizeFsListingPayload({
      entries: [
        {
          name: ".uploads",
          path: ".uploads",
          type: "dir",
          size: -1,
          mtime: -1,
          internal: true,
        },
      ],
    });

    expect(result.entries[0]?.internal).toBe(true);
  });

  it("缺 entries 数组直接抛错（不伪装成空目录）", () => {
    expect(() => normalizeFsListingPayload({ dir: {} })).toThrow(/missing `entries`/);
    expect(() => normalizeFsListingPayload(null)).toThrow(/missing `entries`/);
  });
});

describe("fetchFsListing", () => {
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

  it("GET /fs/list 并映射查询参数（scope 去空白、空 cursor 不下发）", async () => {
    respondWith({ entries: [] });

    await fetchFsListing({
      scope: "  cwd  ",
      path: "src",
      cursor: null,
      limit: 200,
      sort: "mtime_desc",
      showHidden: true,
      dirsFirst: false,
    });

    const url = new URL(calls[0].url, "http://runtime.test");
    expect(url.pathname).toBe(FS_LIST_PATH);
    expect(url.searchParams.get("scope")).toBe("cwd");
    expect(url.searchParams.get("path")).toBe("src");
    expect(url.searchParams.get("limit")).toBe("200");
    expect(url.searchParams.get("sort")).toBe("mtime_desc");
    expect(url.searchParams.get("show_hidden")).toBe("true");
    expect(url.searchParams.get("dirs_first")).toBe("false");
    expect(url.searchParams.has("cursor")).toBe(false);
  });

  it("空 scope 不发请求，直接抛错", async () => {
    respondWith({ entries: [] });

    await expect(fetchFsListing({ scope: "   " })).rejects.toThrow(/requires a scope/);
    expect(calls).toHaveLength(0);
  });

  it("cursor_invalid 错误码被识别（UI 需要重置到第一页而不是重试）", async () => {
    respondWith({ error: "cursor is no longer valid", code: "cursor_invalid" }, 400);

    const error = await fetchFsListing({ scope: "cwd", cursor: "stale" }).catch(
      (caught: unknown) => caught,
    );

    expect(error).toBeInstanceOf(RuntimeApiError);
    expect(isFsListingCursorError(error)).toBe(true);
    expect(isFsListingCursorError(new RuntimeApiError(500, null))).toBe(false);
  });
});
