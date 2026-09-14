import { describe, expect, it } from "vitest";

import { type ToolMessageSegment } from "@/lib/thread-state/messages";

import { resolveToolCardKind } from "./kind";
import {
  isToolRowExpandable,
  orderSummaryParts,
  resolveToolRowPresentation,
  SUMMARY_MAX_PARTS,
  SUMMARY_TEXT_LIMIT,
} from "./state";

function toolSegment(
  partial: Partial<Omit<ToolMessageSegment, "type">> = {},
): ToolMessageSegment {
  return {
    type: "tool",
    name: "read_file",
    status: "finished",
    ...partial,
  };
}

describe("resolveToolCardKind", () => {
  it("按名字命中专属卡，未注册走 generic", () => {
    expect(resolveToolCardKind("read_file")).toBe("read");
    expect(resolveToolCardKind("apply_patch")).toBe("diff");
    expect(resolveToolCardKind("run_terminal_cmd")).toBe("terminal");
    expect(resolveToolCardKind("Grep")).toBe("search");
    expect(resolveToolCardKind("web_search")).toBe("web");
    expect(resolveToolCardKind("generate_image")).toBe("image");
    expect(resolveToolCardKind("jq")).toBe("json");
    expect(resolveToolCardKind("mcp__filesystem__read_file")).toBe("read");
    expect(resolveToolCardKind("my_custom_tool")).toBe("generic");
  });
});

describe("isToolRowExpandable", () => {
  it("存在输入或输出/错误详情时可展开（折叠态 24px 单行）", () => {
    expect(isToolRowExpandable(toolSegment({ argsSummary: "src/a.ts" }))).toBe(true);
    expect(isToolRowExpandable(toolSegment({ argsSummary: "   " }))).toBe(false);
    expect(isToolRowExpandable(toolSegment({ resultSummary: "ok" }))).toBe(true);
    expect(
      isToolRowExpandable(toolSegment({ status: "error", errorMessage: "boom" })),
    ).toBe(true);
    expect(isToolRowExpandable(toolSegment())).toBe(false);
  });
});

describe("orderSummaryParts（B5 单行摘要）", () => {
  it("按 路径 > 命令/查询 > URL > diff > 退出码 排序并最多保留两项", () => {
    expect(
      orderSummaryParts([
        { type: "exitCode", code: 2 },
        { type: "diff", additions: 1, removals: 1 },
        { type: "url", url: "https://example.com" },
        { type: "text", text: "npm test" },
        { type: "path", path: "src/a.ts" },
      ]),
    ).toEqual([
      { type: "path", path: "src/a.ts" },
      { type: "text", text: "npm test" },
    ]);
    expect(SUMMARY_MAX_PARTS).toBe(2);
  });

  it("命令/查询等自由文本按阈值截断（单行长度 ≤ 阈值且带省略号）", () => {
    const command = `npm run ${"very-long-argument ".repeat(10)}`;
    const [part] = orderSummaryParts([{ type: "text", text: command }]);
    expect(part.type).toBe("text");
    if (part.type === "text") {
      expect(part.text.length).toBeLessThanOrEqual(SUMMARY_TEXT_LIMIT);
      expect(part.text.endsWith("…")).toBe(true);
    }
  });

  it("给定 details 时首选路径/命令，不把 diff/退出码顶到首位", () => {
    expect(
      orderSummaryParts([
        { type: "diff", additions: 4, removals: 2 },
        { type: "path", path: "src/a.ts" },
      ])[0],
    ).toEqual({ type: "path", path: "src/a.ts" });

    expect(
      orderSummaryParts([
        { type: "exitCode", code: 2 },
        { type: "text", text: "npm test" },
      ])[0],
    ).toEqual({ type: "text", text: "npm test" });
  });
});

describe("resolveToolRowPresentation", () => {
  it("读类成功态：路径摘要 + 行根属性；失败态禁用文件链接并替换摘要", () => {
    const ok = resolveToolRowPresentation(
      toolSegment({ name: "read_file", details: { filePath: "src/a.ts" } }),
    );
    expect(ok.kind).toBe("read");
    expect(ok.summary).toEqual({
      tone: "default",
      parts: [{ type: "path", path: "src/a.ts" }],
    });
    expect(ok.filePath).toBe("src/a.ts");
    expect(ok.fileLinkDisabled).toBe(false);
    expect(ok.attributes).toMatchObject({
      "data-tool-row": "true",
      "data-tool-row-kind": "read",
      "data-tool-row-status": "finished",
      "data-tool-row-name": "read_file",
      "data-tool-row-expandable": "false",
    });

    const failed = resolveToolRowPresentation(
      toolSegment({
        name: "read_file",
        status: "error",
        errorMessage: "ENOENT: no such file",
        resultSummary: "旧输出不应出现",
        details: { filePath: "src/a.ts" },
      }),
    );
    expect(failed.summary).toEqual({
      tone: "danger",
      parts: [{ type: "path", path: "src/a.ts" }],
    });
    expect(failed.fileLinkDisabled).toBe(true);
    expect(failed.attributes["data-tool-row-status"]).toBe("error");
    expect(JSON.stringify(failed.summary)).not.toContain("旧输出");
  });

  it("diff 卡携带 +A -R 与目标文件", () => {
    const presentation = resolveToolRowPresentation(
      toolSegment({
        name: "apply_patch",
        argsSummary: "*** Begin Patch...",
        details: { filePath: "src/a.ts", diff: { additions: 4, removals: 2 } },
      }),
    );
    expect(presentation.kind).toBe("diff");
    expect(presentation.summary.parts).toEqual([
      { type: "path", path: "src/a.ts" },
      { type: "diff", additions: 4, removals: 2 },
    ]);
    expect(presentation.attributes["data-tool-row-expandable"]).toBe("true");
  });

  it("终端/搜索/网页卡摘要来自命令、查询与 URL", () => {
    expect(
      resolveToolRowPresentation(
        toolSegment({ name: "shell", details: { command: "npm test", exitCode: 2 } }),
      ).summary.parts,
    ).toEqual([
      { type: "text", text: "npm test" },
      { type: "exitCode", code: 2 },
    ]);

    expect(
      resolveToolRowPresentation(
        toolSegment({ name: "grep", status: "running", details: { query: "useEffect" } }),
      ).summary.parts,
    ).toEqual([{ type: "text", text: "useEffect" }]);

    expect(
      resolveToolRowPresentation(
        toolSegment({ name: "web_fetch", details: { url: "https://example.com" } }),
      ).summary.parts,
    ).toEqual([{ type: "url", url: "https://example.com" }]);

    expect(
      resolveToolRowPresentation(
        toolSegment({ name: "web_fetch", details: { url: "example.com" } }),
      ).summary.parts,
    ).toEqual([{ type: "url", url: "example.com" }]);
  });

  it("退出码为 0 不占摘要位；generic 工具无摘要", () => {
    expect(
      resolveToolRowPresentation(
        toolSegment({ name: "shell", details: { command: "ls", exitCode: 0 } }),
      ).summary.parts,
    ).toEqual([{ type: "text", text: "ls" }]);

    const generic = resolveToolRowPresentation(
      toolSegment({ name: "my_custom_tool", details: { filePath: "src/a.ts" } }),
    );
    expect(generic.kind).toBe("generic");
    expect(generic.summary.parts).toEqual([]);
    expect(generic.attributes["data-tool-row-has-summary"]).toBe("false");
  });

  it("历史数据兜底：无明细时从 argsSummary 解析路径", () => {
    const presentation = resolveToolRowPresentation(
      toolSegment({ name: "read_file", argsSummary: '{"file_path":"src/history.ts"}' }),
    );
    expect(presentation.summary.parts).toEqual([
      { type: "path", path: "src/history.ts" },
    ]);
  });
});
