// change-model 单测：P3-4 扁平化顺序 + P4-1 乐观搬移（含负例）。
//
// 纪律（与 lib/git/change-model.ts 注释一致，测试用来钉住）：
//   * 冲突组永不参与乐观搬移；
//   * 目标组按 path 去重（同一文件不允许在两组里各留一份）；
//   * 只改分组归属与 `clean`，repo/warnings/generatedAt 原样保留；
//   * 没有匹配文件时不改任何字段（不做「看起来像做了事」的空操作）。

import { describe, expect, it } from "vitest";

import type { GitFileStatus, GitStatusResult } from "@/types/runtime/git-browse";

import { applyOptimisticStage, flattenGitChanges } from "./change-model";

function file(path: string, status: string, overrides: Partial<GitFileStatus> = {}): GitFileStatus {
  return { path, status, insertions: 1, deletions: 0, binary: false, ...overrides };
}

function statusResult(overrides: Partial<GitStatusResult> = {}): GitStatusResult {
  return {
    repo: {
      root: "E:/repo",
      branch: "main",
      detached: false,
      head: "abc1234",
      ahead: 0,
      behind: 0,
      isBare: false,
    },
    clean: false,
    staged: [],
    unstaged: [],
    untracked: [],
    conflicts: [],
    warnings: [],
    generatedAt: 1234,
    ...overrides,
  };
}

const paths = (files: readonly GitFileStatus[]) => files.map((item) => item.path);

describe("flattenGitChanges", () => {
  it("按固定分组顺序扁平化，组内保持后端顺序", () => {
    const entries = flattenGitChanges(
      statusResult({
        conflicts: [file("c.ts", "UU")],
        staged: [file("s1.ts", "M."), file("s2.ts", "A.")],
        unstaged: [file("u1.ts", ".M")],
        untracked: [file("n1.ts", "??")],
      }),
    );
    expect(entries.map((entry) => [entry.group, entry.path])).toEqual([
      ["conflicts", "c.ts"],
      ["staged", "s1.ts"],
      ["staged", "s2.ts"],
      ["unstaged", "u1.ts"],
      ["untracked", "n1.ts"],
    ]);
  });

  it("null 状态不伪造条目", () => {
    expect(flattenGitChanges(null)).toEqual([]);
  });
});

describe("applyOptimisticStage", () => {
  it("stage：未暂存侧读 Y 字母搬到已暂存（`.M` → `M.`）", () => {
    const next = applyOptimisticStage(
      statusResult({ unstaged: [file("a.ts", ".M"), file("b.ts", ".M")] }),
      ["a.ts"],
      "stage",
    );
    expect(paths(next.staged)).toEqual(["a.ts"]);
    expect(next.staged[0].status).toBe("M.");
    expect(paths(next.unstaged)).toEqual(["b.ts"]);
  });

  it("stage：未跟踪文件搬成 `A.`（index 里是新增），不是 `??`", () => {
    const next = applyOptimisticStage(
      statusResult({ untracked: [file("new.ts", "??")] }),
      ["new.ts"],
      "stage",
    );
    expect(paths(next.staged)).toEqual(["new.ts"]);
    expect(next.staged[0].status).toBe("A.");
    expect(next.untracked).toEqual([]);
  });

  it("stage：保留后端给定的重命名来源与二进制结论（不重算内容）", () => {
    const next = applyOptimisticStage(
      statusResult({
        unstaged: [file("renamed.ts", ".R", { from: "old.ts", binary: true })],
      }),
      ["renamed.ts"],
      "stage",
    );
    expect(next.staged[0]).toMatchObject({ path: "renamed.ts", from: "old.ts", binary: true });
  });

  it("unstage：已暂存读 X 字母回到未暂存（`M.` → `.M`），`A.` 回未跟踪（`??`）", () => {
    const next = applyOptimisticStage(
      statusResult({ staged: [file("mod.ts", "M."), file("added.ts", "A.")] }),
      ["mod.ts", "added.ts"],
      "unstage",
    );
    expect(next.staged).toEqual([]);
    expect(paths(next.unstaged)).toEqual(["mod.ts"]);
    expect(next.unstaged[0].status).toBe(".M");
    expect(paths(next.untracked)).toEqual(["added.ts"]);
    expect(next.untracked[0].status).toBe("??");
  });

  it("冲突组不参与乐观搬移（`git add` 冲突文件等于宣称已解决）", () => {
    const conflict = file("c.ts", "UU");
    const next = applyOptimisticStage(statusResult({ conflicts: [conflict] }), ["c.ts"], "stage");
    expect(next.conflicts).toEqual([conflict]);
    expect(next.staged).toEqual([]);
  });

  it("目标组按 path 去重：同一文件不会在两组各留一份", () => {
    const next = applyOptimisticStage(
      statusResult({
        staged: [file("both.ts", "M.")],
        unstaged: [file("both.ts", ".M")],
      }),
      ["both.ts"],
      "stage",
    );
    expect(paths(next.staged)).toEqual(["both.ts"]);
    expect(next.staged[0].status).toBe("M.");
    expect(next.unstaged).toEqual([]);
  });

  it("空文件列表是真正的空操作（不改 clean / 分组）", () => {
    const before = statusResult({ unstaged: [file("a.ts", ".M")] });
    expect(applyOptimisticStage(before, [], "stage")).toBe(before);
  });

  it("只改分组归属与 clean，服务端字段（repo/warnings/generatedAt）原样保留", () => {
    const before = statusResult({
      unstaged: [file("a.ts", ".M")],
      warnings: ["unparsed line"],
      clean: false,
    });
    const next = applyOptimisticStage(before, ["a.ts"], "stage");
    expect(next.repo).toEqual(before.repo);
    expect(next.warnings).toEqual(["unparsed line"]);
    expect(next.generatedAt).toBe(1234);
    expect(next.clean).toBe(false);
  });

  it("clean 与后端同口径（四组全空才为 true）：搬移不把「有暂存」伪装成干净", () => {
    // 后端 status.go：Clean = staged && unstaged && untracked && conflicts 全空。
    // 把文件搬到 staged 仍是「有变更」，不能算干净。
    const afterStage = applyOptimisticStage(
      statusResult({ unstaged: [file("a.ts", ".M")] }),
      ["a.ts"],
      "stage",
    );
    expect(afterStage.staged.map((item) => item.path)).toEqual(["a.ts"]);
    expect(afterStage.clean).toBe(false);

    const afterUnstage = applyOptimisticStage(
      statusResult({ staged: [file("a.ts", "M.")] }),
      ["a.ts"],
      "unstage",
    );
    expect(afterUnstage.unstaged.map((item) => item.path)).toEqual(["a.ts"]);
    expect(afterUnstage.clean).toBe(false);

    // 四组全空（无可搬 / 目标本就不存在）时复算结果才是 true。
    const clean = applyOptimisticStage(statusResult({ clean: true }), ["nope.ts"], "stage");
    expect(clean.clean).toBe(true);
  });
});
