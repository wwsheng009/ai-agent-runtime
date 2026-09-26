// 归档计划阅读面的纯函数（§4.4 差异行分类）：边界用例直接在单测里钉住。
//
// 断言纪律：只钉「分类结果」——分类错了颜色就错了，但组件不重复测颜色类名。

import { describe, expect, it } from "vitest";

import {
  classifyPlanDiffLines,
  formatPlanCommentRange,
  parsePlanDiffHunkHeader,
  planCommentStatusClass,
  planCommentStatusLabel,
  planDiffLineClass,
} from "./artifact-panel-plans-shared";

describe("classifyPlanDiffLines", () => {
  it("框架行 / hunk 头 / 新增 / 删除 / 上下文各归各类", () => {
    const lines = classifyPlanDiffLines(
      "--- v1 request_changes (user)\n+++ v2 approve (user)\n@@ -1,2 +1,3 @@\n # Plan\n-1. ship it\n+1. ship it fast\n+2. document\n",
    );

    expect(lines.map((line) => line.kind)).toEqual([
      "meta",
      "meta",
      "hunk",
      "context",
      "del",
      "add",
      "add",
    ]);
    expect(lines[4].text).toBe("-1. ship it");
  });

  it("以 '--' 开头的删除行不被误判成框架行", () => {
    // 删除行的渲染形式是标记符 + 原文；原文本身以 '-' 开头时会出现 '---…'。
    const lines = classifyPlanDiffLines("--- v1 a\n+++ v2 b\n@@ -1 +1 @@\n---- divider\n--- divider\n");

    expect(lines[3].kind).toBe("del");
    expect(lines[4].kind).toBe("del");
    expect(lines[0].kind).toBe("meta");
  });

  it("无末尾换行标记归入 meta，空文本返回空数组", () => {
    const lines = classifyPlanDiffLines("--- v1 a\n+++ v2 b\n@@ -1 +1 @@\n-a\n+b\n\\ No newline at end of file\n");

    expect(lines[lines.length - 1].kind).toBe("meta");
    expect(lines[lines.length - 1].text).toBe("\\ No newline at end of file");
    expect(classifyPlanDiffLines("")).toEqual([]);
  });
});

describe("planDiffLineClass", () => {
  it("新增/删除走不同强调色，其余走弱化文本", () => {
    expect(planDiffLineClass("add")).toBe("text-accent-teal");
    expect(planDiffLineClass("del")).toBe("text-accent-orange");
    expect(planDiffLineClass("hunk")).toBe("text-muted-foreground");
    expect(planDiffLineClass("meta")).toBe("text-foreground/70");
    expect(planDiffLineClass("context")).toBe("text-muted-foreground/80");
  });
});

// 行号口径（§4.4 行级评论的锚点来源）：必须是**正文本行号**，而不是 diff 文本行序。
describe("classifyPlanDiffLines 行号", () => {
  it("按 hunk 头起算：上下文两版都有，删除只给旧版，新增只给新版", () => {
    const lines = classifyPlanDiffLines(
      "--- v1 request_changes (user)\n+++ v2 approve (user)\n@@ -10,3 +20,4 @@\n 上下文\n-旧行\n+新行\n+再来一行\n",
    );

    expect(lines[3]).toMatchObject({ kind: "context", oldLine: 10, newLine: 20 });
    expect(lines[4]).toMatchObject({ kind: "del", oldLine: 11 });
    expect(lines[4].newLine).toBeUndefined();
    expect(lines[5]).toMatchObject({ kind: "add", newLine: 21 });
    expect(lines[6]).toMatchObject({ kind: "add", newLine: 22 });
    // 框架行与 hunk 头不参与行号。
    expect(lines[0].newLine).toBeUndefined();
    expect(lines[2].newLine).toBeUndefined();
  });

  it("多个 hunk 各自从自己的头起算", () => {
    const lines = classifyPlanDiffLines("@@ -1 +1 @@\n-a\n+b\n@@ -50,1 +60,1 @@\n X\n");

    expect(lines[1]).toMatchObject({ kind: "del", oldLine: 1 });
    expect(lines[2]).toMatchObject({ kind: "add", newLine: 1 });
    expect(lines[4]).toMatchObject({ kind: "context", oldLine: 50, newLine: 60 });
  });
});

describe("评论辅助", () => {
  it("区间文案与后端 / CLI 同口径（end <= start 视为单行）", () => {
    expect(formatPlanCommentRange(12, 12)).toBe("L12");
    expect(formatPlanCommentRange(12, 14)).toBe("L12-14");
    expect(formatPlanCommentRange(12, 3)).toBe("L12");
  });

  it("hunk 头解析：兼容省略 count，非法输入返回 null", () => {
    expect(parsePlanDiffHunkHeader("@@ -1 +1 @@")).toEqual({ oldStart: 1, newStart: 1 });
    expect(parsePlanDiffHunkHeader("@@ -10,3 +20,4 @@ fn main()")).toEqual({
      oldStart: 10,
      newStart: 20,
    });
    expect(parsePlanDiffHunkHeader("@@ nope @@")).toBeNull();
  });

  it("状态文案走 i18n 键，未知状态原文透传；三态各有配色", () => {
    expect(planCommentStatusLabel("anchored")).toBe(
      "panels.artifacts.plans.comments.status.anchored",
    );
    expect(planCommentStatusLabel("weird")).toBe("weird");
    expect(planCommentStatusLabel("")).toBe("—");
    expect(planCommentStatusClass("anchored")).toContain("accent-teal");
    expect(planCommentStatusClass("moved")).toContain("accent-orange");
    expect(planCommentStatusClass("orphaned")).toContain("muted-foreground");
    expect(planCommentStatusClass("weird")).toBeUndefined();
  });
});
