// P3：Git 只读客户端单测（status 分组 / diff hunks 契约 / commits / 降级判据）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  GIT_DIFF_PATH,
  GIT_STATUS_PATH,
  fetchGitCommits,
  fetchGitDiff,
  fetchGitStatus,
  isGitRepoMissing,
  normalizeGitCommitsPayload,
  normalizeGitDiffPayload,
  normalizeGitStatusPayload,
} from "@/api/runtime/git";
import { RuntimeApiError } from "@/api/runtime/shared";

describe("normalizeGitStatusPayload", () => {
  it("归一化仓库信息、四类分组与 numstat 计数", () => {
    const result = normalizeGitStatusPayload({
      repo: {
        root: "E:/repo",
        branch: "main",
        detached: false,
        head: "abc123",
        upstream: "origin/main",
        ahead: 2,
        behind: 1,
        is_bare: false,
      },
      clean: false,
      staged: [{ path: "src/a.ts", status: "M", insertions: 12, deletions: 3 }],
      unstaged: [{ path: "src/b.ts", status: "M", insertions: 1, deletions: 0 }],
      untracked: [{ path: "notes.md", status: "?" }],
      conflicts: [{ path: "src/c.ts", status: "U" }],
      warnings: ["submodule skipped", 42],
      generated_at: 1720000000,
    });

    expect(result.repo).toMatchObject({
      root: "E:/repo",
      branch: "main",
      upstream: "origin/main",
      ahead: 2,
      behind: 1,
      detached: false,
    });
    expect(result.staged[0]).toMatchObject({ path: "src/a.ts", insertions: 12, deletions: 3 });
    expect(result.unstaged).toHaveLength(1);
    expect(result.untracked[0]).toMatchObject({ path: "notes.md", binary: false });
    expect(result.conflicts[0]?.status).toBe("U");
    expect(result.warnings).toEqual(["submodule skipped"]);
    expect(result.generatedAt).toBe(1720000000);
  });

  it("二进制文件 insertions/deletions 为 -1（不用 0 伪装）", () => {
    const result = normalizeGitStatusPayload({
      staged: [{ path: "assets/logo.png", status: "M", insertions: "-", deletions: "-", binary: true }],
    });

    expect(result.staged[0]).toMatchObject({ binary: true, insertions: -1, deletions: -1 });
  });

  it("重命名带出 from；缺 path 的条目跳过", () => {
    const result = normalizeGitStatusPayload({
      staged: [
        { path: "src/new.ts", from: "src/old.ts", status: "R" },
        { from: "src/ghost.ts", status: "R" },
      ],
    });

    expect(result.staged).toHaveLength(1);
    expect(result.staged[0]?.from).toBe("src/old.ts");
  });

  it("分组缺失按空数组收口；repo 缺 root 时为 null（UI 显示非仓库空态）", () => {
    const result = normalizeGitStatusPayload({ repo: { branch: "main" } });

    expect(result.repo).toBeNull();
    expect(result.staged).toEqual([]);
    expect(result.unstaged).toEqual([]);
    expect(result.untracked).toEqual([]);
    expect(result.conflicts).toEqual([]);
    expect(result.clean).toBe(false);
  });

  it("坏载荷直接抛错", () => {
    expect(() => normalizeGitStatusPayload(null)).toThrow(/not an object/);
    expect(() => normalizeGitStatusPayload("nope")).toThrow(/not an object/);
  });
});

describe("normalizeGitDiffPayload", () => {
  const hunk = {
    header: "@@ -1,2 +1,3 @@",
    old_start: 1,
    old_lines: 2,
    new_start: 1,
    new_lines: 3,
    lines: [
      { type: "context", old_no: 1, new_no: 1, text: "keep" },
      { type: "del", old_no: 2, new_no: null, text: "gone" },
      { type: "add", old_no: null, new_no: 2, text: "new" },
      { type: "nonewline", old_no: null, new_no: 3, text: "" },
    ],
  };

  it("归一化文件信息、hunks 与行号（null 表示该侧不存在）", () => {
    const result = normalizeGitDiffPayload({
      file: { path: "src/a.ts", status: "M", is_binary: false, abs_path: "E:/repo/src/a.ts" },
      target: "working",
      context: 3,
      whitespace: "show",
      insertions: 1,
      deletions: 1,
      hunks: [hunk],
      raw: "diff --git ...",
      truncated: false,
      generated_at: 1720000000,
    });

    expect(result.file).toMatchObject({ path: "src/a.ts", absPath: "E:/repo/src/a.ts" });
    expect(result.hunks[0]?.lines.map((line) => line.type)).toEqual([
      "context",
      "del",
      "add",
      "nonewline",
    ]);
    expect(result.hunks[0]?.lines[1]).toMatchObject({ oldNo: 2, newNo: null });
    expect(result.parseError).toBe("");
    expect(result.generatedAt).toBe(1720000000);
  });

  it("未知 line type 直接丢弃（不猜语义）", () => {
    const result = normalizeGitDiffPayload({
      hunks: [
        {
          ...hunk,
          lines: [...hunk.lines, { type: "sneaky", old_no: 9, new_no: 9, text: "?" }],
        },
      ],
    });

    expect(result.hunks[0]?.lines).toHaveLength(4);
  });

  it("hunks 缺失或 hunk 非法直接抛错（不静默当空 diff）", () => {
    expect(() => normalizeGitDiffPayload({ file: { path: "a.ts" } })).toThrow(/missing `hunks`/);
    expect(() =>
      normalizeGitDiffPayload({ hunks: [{ header: "@@", lines: "nope" }] }),
    ).toThrow(/invalid hunk/);
  });

  it("parse_error 非空时透传（UI 必须降级为纯文本）", () => {
    const result = normalizeGitDiffPayload({
      hunks: [],
      raw: "malformed diff body",
      parse_error: "unexpected hunk header",
      whitespace: "ignore_all",
      context: 5,
      truncated: true,
      truncated_reason: "max_bytes",
    });

    expect(result.parseError).toBe("unexpected hunk header");
    expect(result.raw).toBe("malformed diff body");
    expect(result.whitespace).toBe("ignore_all");
    expect(result.context).toBe(5);
    expect(result.truncated).toBe(true);
    expect(result.truncatedReason).toBe("max_bytes");
  });
});

describe("normalizeGitCommitsPayload", () => {
  it("归一化提交并补 shortSha 兜底", () => {
    const result = normalizeGitCommitsPayload({
      commits: [
        {
          sha: "0123456789abcdef",
          author: "dev",
          authored_at: "2026-09-16T00:00:00Z",
          subject: "feat: add panel",
          refs: ["HEAD -> main", 7],
        },
      ],
      next_cursor: "c-2",
      has_more: true,
    });

    expect(result.commits[0]).toMatchObject({
      sha: "0123456789abcdef",
      shortSha: "0123456",
      author: "dev",
      subject: "feat: add panel",
      refs: ["HEAD -> main"],
    });
    expect(result.nextCursor).toBe("c-2");
    expect(result.hasMore).toBe(true);
  });

  it("缺 sha 的提交跳过；缺 commits 抛错", () => {
    expect(normalizeGitCommitsPayload({ commits: [{ subject: "no sha" }] }).commits).toEqual([]);
    expect(() => normalizeGitCommitsPayload({})).toThrow(/missing `commits`/);
  });
});

describe("git 请求层", () => {
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

  it("status/diff/commits 走各自路径并携带 scope", async () => {
    respondWith({ repo: { root: "E:/repo" }, staged: [] });
    await fetchGitStatus({ scope: " cwd ", path: "frontend" });
    respondWith({ hunks: [], file: { path: "a.ts" } });
    await fetchGitDiff({ scope: "cwd", file: "a.ts", target: "staged", whitespace: "ignore_all" });
    respondWith({ commits: [] });
    await fetchGitCommits({ scope: "cwd", limit: 20 });

    const statusUrl = new URL(calls[0].url, "http://runtime.test");
    expect(statusUrl.pathname).toBe(GIT_STATUS_PATH);
    expect(statusUrl.searchParams.get("scope")).toBe("cwd");
    expect(statusUrl.searchParams.get("path")).toBe("frontend");

    const diffUrl = new URL(calls[1].url, "http://runtime.test");
    expect(diffUrl.pathname).toBe(GIT_DIFF_PATH);
    expect(diffUrl.searchParams.get("file")).toBe("a.ts");
    expect(diffUrl.searchParams.get("target")).toBe("staged");
    expect(diffUrl.searchParams.get("whitespace")).toBe("ignore_all");

    const commitsUrl = new URL(calls[2].url, "http://runtime.test");
    expect(commitsUrl.searchParams.get("limit")).toBe("20");
    expect([GIT_STATUS_PATH, GIT_DIFF_PATH]).toHaveLength(2);
  });

  it("缺 scope（或 diff 缺 file）不发请求", async () => {
    respondWith({ repo: null, staged: [] });

    await expect(fetchGitStatus({ scope: "  " })).rejects.toThrow(/requires a scope/);
    await expect(fetchGitDiff({ scope: "cwd", file: " " })).rejects.toThrow(
      /requires scope and file/,
    );
    await expect(fetchGitCommits({ scope: "" })).rejects.toThrow(/requires a scope/);
    expect(calls).toHaveLength(0);
  });

  it("503 git_unavailable 判为仓库缺失/不可用空态", async () => {
    respondWith({ error: "git executable not found", code: "git_unavailable" }, 503);

    const error = await fetchGitStatus({ scope: "cwd" }).catch((caught: unknown) => caught);

    expect(error).toBeInstanceOf(RuntimeApiError);
    expect(isGitRepoMissing(error)).toBe(true);
    expect(isGitRepoMissing(new Error("boom"))).toBe(false);
  });
});
