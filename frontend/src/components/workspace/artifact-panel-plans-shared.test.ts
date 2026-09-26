// 归档计划阅读面的纯函数（§4.4 差异行分类）：边界用例直接在单测里钉住。
//
// 断言纪律：只钉「分类结果」——分类错了颜色就错了，但组件不重复测颜色类名。

import { describe, expect, it } from "vitest";

import { classifyPlanDiffLines, planDiffLineClass } from "./artifact-panel-plans-shared";

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
