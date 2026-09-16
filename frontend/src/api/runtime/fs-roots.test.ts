// P0：作用域根客户端单测（请求形态 / 载荷归一化 / 降级判据）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  FS_ROOTS_PATH,
  fetchFsRoots,
  isFsRootsUnavailable,
  normalizeFsRootsPayload,
} from "@/api/runtime/fs-roots";
import { RuntimeApiError } from "@/api/runtime/shared";

describe("normalizeFsRootsPayload", () => {
  it("保留 scope/kind/name/path 与仓库探测结果", () => {
    const result = normalizeFsRootsPayload({
      roots: [
        {
          scope: "workspace:wd-1",
          kind: "workspace",
          name: "runtime",
          path: "E:/projects/ai/ai-agent-runtime",
          exists: true,
          is_git_repo: true,
          git_root: "E:/projects/ai/ai-agent-runtime",
        },
        {
          scope: "cwd",
          kind: "cwd",
          path: "E:/tmp",
          exists: true,
          is_git_repo: false,
        },
      ],
      count: 2,
    });

    expect(result.skipped).toBe(0);
    expect(result.roots).toEqual([
      {
        scope: "workspace:wd-1",
        kind: "workspace",
        name: "runtime",
        path: "E:/projects/ai/ai-agent-runtime",
        exists: true,
        isGitRepo: true,
        gitRoot: "E:/projects/ai/ai-agent-runtime",
      },
      {
        scope: "cwd",
        kind: "cwd",
        name: "cwd",
        path: "E:/tmp",
        exists: true,
        isGitRepo: false,
      },
    ]);
  });

  it("未知 kind 收口为 cwd；exists/is_git_repo 缺省 false", () => {
    const result = normalizeFsRootsPayload({
      roots: [{ scope: "session:s-1", kind: "galaxy", path: "/srv/app" }],
    });

    expect(result.roots[0]).toMatchObject({
      kind: "cwd",
      exists: false,
      isGitRepo: false,
    });
  });

  it("探测失败原因透传（probeError 非空才带出）", () => {
    const result = normalizeFsRootsPayload({
      roots: [
        {
          scope: "workspace:wd-2",
          kind: "workspace",
          path: "E:/gone",
          exists: false,
          is_git_repo: false,
          probe_error: "permission denied",
        },
      ],
    });

    expect(result.roots[0]?.probeError).toBe("permission denied");
  });

  it("缺 scope/path 的条目跳过并计数，不伪装成可用根", () => {
    const result = normalizeFsRootsPayload({
      roots: [
        { scope: "  ", path: "/a" },
        { scope: "cwd" },
        { scope: "cwd", path: "  " },
        { scope: "cwd", path: "/ok" },
        null,
      ],
    });

    expect(result.skipped).toBe(4);
    expect(result.roots).toHaveLength(1);
    expect(result.roots[0]?.path).toBe("/ok");
  });

  it("缺 roots 数组直接抛错（不伪装成空态）", () => {
    expect(() => normalizeFsRootsPayload({})).toThrow(/missing `roots`/);
    expect(() => normalizeFsRootsPayload(null)).toThrow(/missing `roots`/);
    expect(() => normalizeFsRootsPayload({ roots: {} })).toThrow(/missing `roots`/);
  });
});

describe("fetchFsRoots", () => {
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

  it("GET /api/runtime/fs/roots 并归一化结果", async () => {
    respondWith({ roots: [{ scope: "cwd", kind: "cwd", path: "/srv/app" }] });

    const result = await fetchFsRoots();

    expect(calls).toHaveLength(1);
    expect(new URL(calls[0].url, "http://runtime.test").pathname).toBe(FS_ROOTS_PATH);
    expect(calls[0].init?.method).toBe("GET");
    expect(result.roots).toHaveLength(1);
  });

  it("503 未注入服务时抛 RuntimeApiError 且判为不可用", async () => {
    respondWith({ error: "file browse service not configured" }, 503);

    const error = await fetchFsRoots().catch((caught: unknown) => caught);

    expect(error).toBeInstanceOf(RuntimeApiError);
    expect(isFsRootsUnavailable(error)).toBe(true);
    expect(isFsRootsUnavailable(new Error("boom"))).toBe(false);
  });
});
