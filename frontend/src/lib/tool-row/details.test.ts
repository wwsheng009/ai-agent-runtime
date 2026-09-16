import { describe, expect, it } from "vitest";

import { type AgentChatStreamChunkPayload } from "@/types/runtime";

import {
  countContentLines,
  countDiffLines,
  extractToolDetails,
  matchToolFilePath,
  parseToolDetailsFromArgsText,
  resolveToolSegmentDetails,
} from "./details";

function payload(partial: Partial<AgentChatStreamChunkPayload>): AgentChatStreamChunkPayload {
  return partial as AgentChatStreamChunkPayload;
}

describe("countDiffLines / countContentLines", () => {
  it("忽略 +++/--- 文件头，只统计正负行", () => {
    const patch = [
      "*** Begin Patch",
      "*** Update File: src/a.ts",
      "@@",
      "-old",
      "+new",
      "+extra",
      "*** End Patch",
    ].join("\n");
    expect(countDiffLines(patch)).toEqual({ additions: 2, removals: 1 });
  });

  it("内容行数不计尾部换行造成的空行", () => {
    expect(countContentLines("a\nb\n")).toBe(2);
    expect(countContentLines("")).toBe(0);
    expect(countContentLines("\n")).toBe(0);
  });
});

describe("extractToolDetails", () => {
  it("从工具入参提取文件路径 / 命令 / 查询 / URL / 退出码", () => {
    expect(
      extractToolDetails(payload({ tool: { args: { file_path: "src/a.ts" } } }), "read_file"),
    ).toEqual({ filePath: "src/a.ts" });

    expect(
      extractToolDetails(payload({ tool: { args: { command: "npm test" } } }), "shell"),
    ).toEqual({ command: "npm test" });

    expect(
      extractToolDetails(payload({ tool: { args: { pattern: "useEffect" } } }), "grep"),
    ).toEqual({ query: "useEffect" });

    expect(
      extractToolDetails(payload({ tool: { args: { url: "https://example.com/a" } } }), "web_fetch"),
    ).toEqual({ url: "https://example.com/a" });

    expect(
      extractToolDetails(payload({ tool: { exit_code: 0 } }), "shell"),
    ).toEqual({ exitCode: 0 });
  });

  it("apply_patch 从完整 patch 统计行数并取目标文件", () => {
    const patch = [
      "*** Begin Patch",
      "*** Update File: src/feature.ts",
      "@@",
      "-const a = 1;",
      "+const a = 2;",
      "+const b = 3;",
      "*** End Patch",
    ].join("\n");

    expect(
      extractToolDetails(payload({ tool: { args: { patch } } }), "apply_patch"),
    ).toEqual({
      filePath: "src/feature.ts",
      diff: { additions: 2, removals: 1 },
    });
  });

  it("优先使用事件白名单里的行级字段", () => {
    expect(
      extractToolDetails(
        payload({ tool: { args: { file_path: "a.ts" }, additions: 7, removals: 3 } }),
        "edit",
      ),
    ).toEqual({ filePath: "a.ts", diff: { additions: 7, removals: 3 } });
  });

  it("无结构化字段时返回 undefined，不补默认值", () => {
    expect(extractToolDetails(payload({ tool: { args: {} } }), "unknown_tool")).toBeUndefined();
  });
});

describe("parseToolDetailsFromArgsText（历史 / 演示数据兜底）", () => {
  it("JSON 入参解析出文件路径", () => {
    expect(
      parseToolDetailsFromArgsText('{"file_path":"src/b.ts","offset":10}', "read_file"),
    ).toEqual({ filePath: "src/b.ts" });
  });

  it("截断的 JSON 不产生错误的 diff 统计", () => {
    const truncated =
      '{"patch":"*** Begin Patch\\n*** Update File: a.ts\\n+1\\n+2\\n+3\\n+4\\n+5\\n+6\\n+7\\n+8\\n+9\\n+10\\n+11\\n+12...';
    const details = parseToolDetailsFromArgsText(truncated, "apply_patch");
    expect(details?.diff).toBeUndefined();
    expect(details?.filePath).toBe("a.ts");
  });

  it("裸路径字符串按读类工具识别，普通字符串不误判", () => {
    expect(parseToolDetailsFromArgsText("src/index.ts", "read_file")).toEqual({
      filePath: "src/index.ts",
    });
    expect(parseToolDetailsFromArgsText("42 行", "read_file")).toBeUndefined();
    expect(parseToolDetailsFromArgsText("npm run build", "shell")).toBeUndefined();
  });
});

describe("resolveToolSegmentDetails", () => {
  it("已有结构化明细时优先使用，不回退解析", () => {
    expect(
      resolveToolSegmentDetails({
        name: "read_file",
        argsSummary: "other.ts",
        details: { filePath: "src/real.ts" },
      }),
    ).toEqual({ filePath: "src/real.ts" });
  });

  it("无明细时解析 argsSummary", () => {
    expect(resolveToolSegmentDetails({ name: "read_file", argsSummary: "src/x.ts" })).toEqual({
      filePath: "src/x.ts",
    });
    expect(resolveToolSegmentDetails({ name: "read_file" })).toBeUndefined();
  });
});

describe("matchToolFilePath", () => {
  it("分隔符归一后支持全等与相对/绝对后缀匹配", () => {
    expect(matchToolFilePath("src/a.ts", "src/a.ts")).toBe(true);
    expect(matchToolFilePath("E:\\repo\\src\\a.ts", "src/a.ts")).toBe(true);
    expect(matchToolFilePath("src/a.ts", "E:/repo/src/a.ts")).toBe(true);
    expect(matchToolFilePath("src/a.ts", "src/b.ts")).toBe(false);
    expect(matchToolFilePath("", "src/a.ts")).toBe(false);
  });
});

describe("extractToolDetails：行级 diff 文本", () => {
  /** 后端 tool.completed 的真实形态：说明行 + ```diff 围栏（带行号 hunk）。 */
  const DIFF_FENCE = [
    "补丁已应用：修改 1；影响 1 个路径",
    "",
    "文件差异:",
    "```diff",
    "--- a/src/a.ts",
    "+++ b/src/a.ts",
    "@@ -1,2 +1,2 @@",
    "-old",
    "+new",
    "```",
  ].join("\n");
  const CODEX_PATCH = "*** Begin Patch\n*** Update File: src/a.ts\n@@\n-old\n+new\n*** End Patch";

  it("apply_patch：从 render_output 围栏里保留补丁文本，并按补丁行统计兜底", () => {
    const details = extractToolDetails(
      payload({ tool: { render_output: DIFF_FENCE, args: { patch: CODEX_PATCH } } }),
      "apply_patch",
    );

    expect(details?.filePath).toBe("src/a.ts");
    expect(details?.diffText).toBe("--- a/src/a.ts\n+++ b/src/a.ts\n@@ -1,2 +1,2 @@\n-old\n+new");
    expect(details?.diff).toEqual({ additions: 1, removals: 1 });
    expect(details?.diffTextTruncated).toBeUndefined();
  });

  it("事件里已有的真实统计优先于解析结果", () => {
    const details = extractToolDetails(
      payload({ tool: { render_output: DIFF_FENCE, additions: 7, removals: 9 } }),
      "apply_patch",
    );

    expect(details?.diff).toEqual({ additions: 7, removals: 9 });
    expect(details?.diffText).toBeTruthy();
  });

  it("Codex 裸 @@ 补丁没有行号 → 不保留 diffText（回落原始文本）", () => {
    const details = extractToolDetails(
      payload({ tool: { args: { patch: CODEX_PATCH } } }),
      "apply_patch",
    );

    expect(details?.diffText).toBeUndefined();
    expect(details?.diff).toEqual({ additions: 1, removals: 1 });
  });

  it("非 diff 类工具即使输出里有补丁围栏也不保留行级文本", () => {
    const details = extractToolDetails(
      payload({ tool: { render_output: DIFF_FENCE } }),
      "read_file",
    );

    expect(details?.diffText).toBeUndefined();
  });

  it("resolveToolSegmentDetails：没有入参文本时从结果围栏恢复行级文本", () => {
    const details = resolveToolSegmentDetails({
      name: "apply_patch",
      resultSummary: DIFF_FENCE,
    });

    expect(details?.diffText).toContain("@@ -1,2 +1,2 @@");
  });
});
