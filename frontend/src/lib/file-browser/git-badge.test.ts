// P4 单测：文件树 Git 角标索引（`/git/status` 仓库相对路径 → 作用域相对路径）。
//
// 断言纪律：这里只测「映射关系」本身（前缀补/剥、越界丢弃、分组优先级、平台大小写）；
// 字母与色调由 `change-list.tsx` 的 `gitStatusBadge` 负责（已有其单测）。

import { describe, expect, it } from "vitest";

import { buildGitBadgeIndex } from "@/lib/file-browser/git-badge";
import type { GitFileStatus } from "@/types/runtime/git-browse";

function file(path: string, status = "M."): GitFileStatus {
  return { path, status, insertions: 1, deletions: 1, binary: false };
}

describe("buildGitBadgeIndex", () => {
  it("作用域即仓库根：key 就是仓库相对路径", () => {
    const index = buildGitBadgeIndex({
      scopeRootPath: "E:/repo",
      repoRoot: "E:/repo",
      groups: { unstaged: [file("src/a.ts", ".M")], untracked: [file("new.txt", "??")] },
    });
    expect(index?.size).toBe(2);
    expect(index?.get("src/a.ts")).toEqual({ group: "unstaged", status: ".M" });
    expect(index?.get("new.txt")).toEqual({ group: "untracked", status: "??" });
  });

  it("作用域在仓库内：剥掉作用域前缀，作用域之外的变更整体丢弃（不越界映射）", () => {
    const index = buildGitBadgeIndex({
      scopeRootPath: "E:/repo/apps/web",
      repoRoot: "E:/repo",
      groups: { unstaged: [file("apps/web/src/a.ts", ".M"), file("apps/api/main.go", ".M"), file("README.md", ".M")] },
    });
    expect(index?.size).toBe(1);
    expect(index?.get("src/a.ts")).toEqual({ group: "unstaged", status: ".M" });
    expect(index?.has("apps/api/main.go")).toBe(false);
    expect(index?.has("README.md")).toBe(false);
  });

  it("仓库在作用域内：给 git 路径补上相对前缀", () => {
    const index = buildGitBadgeIndex({
      scopeRootPath: "E:/work",
      repoRoot: "E:/work/project",
      groups: { staged: [file("src/a.ts", "M.")] },
    });
    expect(index?.get("project/src/a.ts")).toEqual({ group: "staged", status: "M." });
  });

  it("同一路径命中多组时按 冲突 > 未暂存 > 已暂存 > 未跟踪 取一条，不合并", () => {
    const index = buildGitBadgeIndex({
      scopeRootPath: "E:/repo",
      repoRoot: "E:/repo",
      groups: {
        untracked: [file("a.ts", "??")],
        staged: [file("a.ts", "M.")],
        unstaged: [file("a.ts", ".M")],
        conflicts: [file("a.ts", "UU")],
      },
    });
    expect(index?.get("a.ts")).toEqual({ group: "conflicts", status: "UU" });

    const withoutConflict = buildGitBadgeIndex({
      scopeRootPath: "E:/repo",
      repoRoot: "E:/repo",
      groups: { untracked: [file("a.ts", "??")], staged: [file("a.ts", "M.")], unstaged: [file("a.ts", ".M")] },
    });
    expect(withoutConflict?.get("a.ts")).toEqual({ group: "unstaged", status: ".M" });
    expect(withoutConflict?.size).toBe(1);
  });

  it("缺少绝对根（降级兜底没有绝对路径）→ 返回 null，不猜归属", () => {
    expect(buildGitBadgeIndex({ scopeRootPath: null, repoRoot: "E:/repo", groups: { staged: [file("a.ts")] } })).toBeNull();
    expect(buildGitBadgeIndex({ scopeRootPath: "E:/repo", repoRoot: undefined, groups: { staged: [file("a.ts")] } })).toBeNull();
    expect(buildGitBadgeIndex({ scopeRootPath: "session:abc", repoRoot: "E:/repo", groups: { staged: [file("a.ts")] } })).toBeNull();
    expect(buildGitBadgeIndex({ scopeRootPath: "E:/repo", repoRoot: "E:\\repo", groups: { staged: [file("a.ts")] } })).not.toBeNull();
  });

  it("两个根无祖先关系（不同盘/不同树）→ 返回 null", () => {
    expect(buildGitBadgeIndex({ scopeRootPath: "E:/work", repoRoot: "D:/repo", groups: { staged: [file("a.ts")] } })).toBeNull();
    expect(buildGitBadgeIndex({ scopeRootPath: "/srv/app", repoRoot: "/srv/other", groups: { staged: [file("a.ts")] } })).toBeNull();
  });

  it("Windows 盘符路径大小写不敏感，POSIX 路径大小写敏感", () => {
    const windows = buildGitBadgeIndex({
      scopeRootPath: "E:/Repo/Sub",
      repoRoot: "E:/repo",
      groups: { unstaged: [file("Sub/A.ts", ".M")] },
    });
    expect(windows?.get("A.ts")).toEqual({ group: "unstaged", status: ".M" });

    const posix = buildGitBadgeIndex({
      scopeRootPath: "/srv/app",
      repoRoot: "/srv/app",
      groups: { unstaged: [file("Src/a.ts", ".M")] },
    });
    expect(posix?.get("Src/a.ts")).toBeDefined();
    expect(posix?.has("src/a.ts")).toBe(false);
  });

  it("路径归一化：反斜杠/重复分隔符/`./` 段被折叠，空路径跳过", () => {
    const index = buildGitBadgeIndex({
      scopeRootPath: "E:/repo",
      repoRoot: "E:/repo",
      groups: { unstaged: [file("./src\\a.ts", ".M"), file("   ", ".M")] },
    });
    expect(index?.size).toBe(1);
    expect(index?.get("src/a.ts")).toEqual({ group: "unstaged", status: ".M" });
  });

  it("无变更 → 空索引（不是 null；调用方据此显示「无角标」而非「不支持」）", () => {
    const index = buildGitBadgeIndex({ scopeRootPath: "E:/repo", repoRoot: "E:/repo", groups: {} });
    expect(index).not.toBeNull();
    expect(index?.size).toBe(0);
  });
});
