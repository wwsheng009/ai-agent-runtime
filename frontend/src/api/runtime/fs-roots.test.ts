// P0：作用域根客户端单测（请求形态 / 载荷归一化 / 降级判据）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  FS_ROOTS_PATH,
  buildFsRootsUrl,
  fetchFsRoots,
  isFsRootsUnavailable,
  normalizeFsRootsPayload,
  pickDefaultRoot,
  pickPreviewRoot,
} from "@/api/runtime/fs-roots";
import { RuntimeApiError } from "@/api/runtime/shared";
import type { FsRoot } from "@/types/runtime/fs-browser";

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

  it("带 sessionId 时编码进 session_id 查询串；空白 sessionId 不落进 URL", async () => {
    respondWith({ roots: [] });

    await fetchFsRoots({ sessionId: "session:abc 1" });
    await fetchFsRoots({ sessionId: "   " });
    await fetchFsRoots();

    const first = new URL(calls[0].url, "http://runtime.test");
    expect(first.pathname).toBe(FS_ROOTS_PATH);
    expect(first.searchParams.get("session_id")).toBe("session:abc 1");
    expect(new URL(calls[1].url, "http://runtime.test").search).toBe("");
    expect(new URL(calls[2].url, "http://runtime.test").search).toBe("");
    expect(buildFsRootsUrl(null)).toBe(buildFsRootsUrl());
  });
});

describe("pickDefaultRoot", () => {
  function root(kind: FsRoot["kind"], scope: string): FsRoot {
    return { scope, kind, name: scope, path: `/srv/${scope}`, exists: true, isGitRepo: false };
  }

  it("优先会话目录根（kind=session），无视它在列表中的位置", () => {
    const roots = [root("workspace", "workspace:wd-1"), root("session", "session:s1"), root("cwd", "cwd")];
    expect(pickDefaultRoot(roots)?.scope).toBe("session:s1");
  });

  it("没有会话根时退回首根；空列表给 null（由调用方兜底）", () => {
    expect(pickDefaultRoot([root("workspace", "workspace:wd-1"), root("cwd", "cwd")])?.scope).toBe(
      "workspace:wd-1",
    );
    expect(pickDefaultRoot([])).toBeNull();
  });
});

describe("pickPreviewRoot", () => {
  function root(kind: FsRoot["kind"], scope: string, path: string): FsRoot {
    return { scope, kind, name: scope, path, exists: true, isGitRepo: false };
  }

  it("优先会话目录根：agent 的工具行相对路径就是相对它的", () => {
    const roots = [
      root("workspace", "workspace:wd-1", "E:/ai/other"),
      root("session", "session:s1", "E:/projects/ai/ai-agent-runtime"),
      root("cwd", "cwd", "E:/projects/ai/ai-agent-runtime/backend"),
    ];
    expect(pickPreviewRoot(roots)?.path).toBe("E:/projects/ai/ai-agent-runtime");
  });

  it("没有会话根时退进程 cwd 根（agent 与运行时进程同目录），不取任意工作区根", () => {
    const roots = [
      root("workspace", "workspace:wd-1", "E:/ai/other"),
      root("cwd", "cwd", "E:/projects/ai/ai-agent-runtime/backend"),
    ];
    expect(pickPreviewRoot(roots)?.scope).toBe("cwd");
  });

  it("只有工作区根或空列表时给 null：宁可原样透传，也不把相对路径拼到猜来的根上", () => {
    expect(pickPreviewRoot([root("workspace", "workspace:wd-1", "E:/ai/other")])).toBeNull();
    expect(pickPreviewRoot([])).toBeNull();
  });
});
