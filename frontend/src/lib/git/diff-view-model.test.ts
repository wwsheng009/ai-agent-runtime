// diff 视图模型单测：split 配对 / 折叠区间 / 渲染预算截断 / 上下文上限保护。
//
// 断言口径（对应文档 §4.6 的诚实纪律）：行号缺失保留 null、折叠行数不猜、截断只截渲染预算。

import { describe, expect, it } from "vitest";

import {
  DEFAULT_DIFF_ROW_LIMIT,
  DIFF_CONTEXT_DEFAULT,
  MAX_DIFF_CONTEXT,
  buildDiffRows,
  countDiffRawLines,
  countHunkChanges,
  diffLanguageForPath,
  limitDiffRows,
  nextDiffContext,
  patchFileName,
  type DiffViewRow,
} from "@/lib/git/diff-view-model";
import type {
  GitDiffHunk,
  GitDiffLine,
  GitDiffLineType,
} from "@/types/runtime/git-browse";

function line(
  type: GitDiffLineType,
  oldNo: number | null,
  newNo: number | null,
  text: string,
): GitDiffLine {
  return { type, oldNo, newNo, text };
}

function hunk(
  oldStart: number,
  newStart: number,
  lines: GitDiffLine[],
  overrides: Partial<GitDiffHunk> = {},
): GitDiffHunk {
  const oldLines = lines.filter((item) => item.type === "context" || item.type === "del").length;
  const newLines = lines.filter((item) => item.type === "context" || item.type === "add").length;
  return {
    header: `@@ -${oldStart},${oldLines} +${newStart},${newLines} @@`,
    oldStart,
    oldLines,
    newStart,
    newLines,
    lines,
    ...overrides,
  };
}

function kinds(rows: DiffViewRow[]): string[] {
  return rows.map((row) => row.kind);
}

describe("buildDiffRows · unified", () => {
  it("del 段先于 add 段输出，行号列合并为一列（context 取新侧）", () => {
    const rows = buildDiffRows(
      [
        hunk(10, 20, [
          line("context", 10, 20, "ctx"),
          line("del", 11, null, "old"),
          line("add", null, 21, "new"),
        ]),
      ],
      { mode: "unified", includeBottomGap: false },
    );

    // 首个 hunk 从 10/20 行开始 → 顶部折叠区间如实标出隐藏行数（不补 0）。
    expect(kinds(rows)).toEqual(["gap", "hunk-header", "context", "del", "add"]);
    expect(rows[0].gap).toEqual({ position: "top", hiddenOld: 9, hiddenNew: 19 });
    const [header, context, del, add] = rows.slice(1);
    expect(header.header).toContain("@@ -10,2 +20,2 @@");
    // 老/新两列合并为一列：context 行取**新侧**行号（与磁盘上的文件对齐，不并排显示 10/20）。
    expect(context.unified).toEqual({
      lineNo: 20,
      prefix: " ",
      text: "ctx",
      tone: "context",
    });
    // 行号由内容单格承载（aria 文案据此生成）：add 取新侧、del 取老侧，两侧格子一律为 null。
    expect([context.old, context.new]).toEqual([null, null]);
    expect(del.unified?.lineNo).toBe(11);
    expect(del.unified?.prefix).toBe("-");
    expect([del.old, del.new]).toEqual([null, null]);
    expect(add.unified?.lineNo).toBe(21);
    expect(add.unified?.prefix).toBe("+");
    expect([add.old, add.new]).toEqual([null, null]);
  });

  it("nonewline 行显式成行且带原文，不吃掉相邻增删配对", () => {
    const rows = buildDiffRows(
      [
        hunk(1, 1, [
          line("del", 1, null, "tail"),
          line("nonewline", null, null, "\\ No newline at end of file"),
          line("add", null, 1, "tail v2"),
        ]),
      ],
      { mode: "unified", includeBottomGap: false },
    );

    expect(kinds(rows)).toEqual(["hunk-header", "del", "nonewline", "add"]);
    const marker = rows[2];
    expect(marker.unified?.tone).toBe("nonewline");
    expect(marker.unified?.text).toBe("\\ No newline at end of file");
    expect(marker.unified?.lineNo).toBeNull();
    // unified 行不再携带行号专用格（行号只由 `unified` 单格承载，且标记行本就没有行号）。
    expect([marker.old, marker.new]).toEqual([null, null]);
  });
});

describe("buildDiffRows · split 配对", () => {
  it("相邻 del/add 按序配对成一行，缺侧用占位格", () => {
    const rows = buildDiffRows(
      [
        hunk(5, 5, [
          line("del", 5, null, "a-old"),
          line("del", 6, null, "b-old"),
          line("del", 7, null, "c-old"),
          line("add", null, 5, "a-new"),
          line("add", null, 6, "b-new"),
        ]),
      ],
      { mode: "split", includeBottomGap: false },
    );

    expect(kinds(rows)).toEqual(["gap", "hunk-header", "pair", "pair", "del"]);
    expect(rows[0].gap).toEqual({ position: "top", hiddenOld: 4, hiddenNew: 4 });
    const [first, second, third] = rows.slice(2);
    expect(first.old?.text).toBe("a-old");
    expect(first.new?.text).toBe("a-new");
    expect(first.old?.lineNo).toBe(5);
    expect(first.new?.lineNo).toBe(5);
    expect(second.new?.lineNo).toBe(6);
    // 第三个 del 无对应 add：新侧是占位格（不编造行号/文本）。
    expect(third.old?.text).toBe("c-old");
    expect(third.new).toEqual({ lineNo: null, prefix: "", text: "", tone: "empty" });
  });

  it("纯新增（未跟踪文件）不产生占位老侧配对行", () => {
    const rows = buildDiffRows(
      [
        hunk(0, 1, [
          line("add", null, 1, "hello"),
          line("add", null, 2, "world"),
        ]),
      ],
      { mode: "split", includeBottomGap: false },
    );

    expect(kinds(rows)).toEqual(["hunk-header", "add", "add"]);
    expect(rows[1].old?.tone).toBe("empty");
    expect(rows[2].new?.text).toBe("world");
  });

  it("context 行不参与配对，阻断跨段配对", () => {
    const rows = buildDiffRows(
      [
        hunk(1, 1, [
          line("del", 1, null, "old-1"),
          line("context", 2, 1, "same"),
          line("add", null, 2, "new-2"),
        ]),
      ],
      { mode: "split", includeBottomGap: false },
    );

    expect(kinds(rows)).toEqual(["hunk-header", "del", "context", "add"]);
    expect(rows[1].new?.tone).toBe("empty");
    expect(rows[3].old?.tone).toBe("empty");
    // 每侧各一列行号：老侧取 oldNo、新侧取 newNo（共用一格会让新栏错显老侧行号）。
    expect([rows[2].old?.lineNo, rows[2].new?.lineNo]).toEqual([2, 1]);
  });
});

describe("buildDiffRows · 折叠区间", () => {
  it("顶部折叠行数来自首个 hunk 起点，尾部行数未知时保留 null", () => {
    const rows = buildDiffRows(
      [hunk(30, 40, [line("context", 30, 40, "ctx")])],
      { mode: "unified" },
    );

    expect(kinds(rows)).toEqual(["gap", "hunk-header", "context", "gap"]);
    expect(rows[0].gap).toEqual({ position: "top", hiddenOld: 29, hiddenNew: 39 });
    expect(rows[3].gap).toEqual({ position: "bottom", hiddenOld: null, hiddenNew: null });
    // gap 行不携带行内容，避免组件把它渲染成代码行。
    expect(rows[0].unified).toBeNull();
  });

  it("hunk 之间的折叠行数按两侧行号区间推导，可为 0 时不输出", () => {
    const rows = buildDiffRows(
      [
        hunk(1, 1, [line("context", 1, 1, "a"), line("context", 2, 2, "b")], {
          oldLines: 2,
          newLines: 2,
        }),
        hunk(20, 21, [line("context", 20, 21, "z")]),
      ],
      { mode: "unified", includeBottomGap: false },
    );

    expect(kinds(rows)).toEqual(["hunk-header", "context", "context", "gap", "hunk-header", "context"]);
    expect(rows[3].gap).toEqual({ position: "between", hiddenOld: 17, hiddenNew: 18 });
  });

  it("折叠的 hunk 只保留头部行（正文不占渲染预算）", () => {
    const hunks = [
      hunk(1, 1, [line("add", null, 1, "a"), line("add", null, 2, "b")]),
      hunk(9, 9, [line("del", 9, null, "c")]),
    ];
    const expanded = buildDiffRows(hunks, { mode: "unified", includeBottomGap: false });
    const collapsed = buildDiffRows(hunks, {
      mode: "unified",
      collapsedHunks: [0],
      includeBottomGap: false,
    });

    // 两个 hunk 之间不连续 → 中间折叠区间仍在（折叠只影响正文行，不影响区间行）。
    expect(kinds(expanded)).toEqual(["hunk-header", "add", "add", "gap", "hunk-header", "del"]);
    expect(kinds(collapsed)).toEqual(["hunk-header", "gap", "hunk-header", "del"]);
    expect(collapsed[0].hunkIndex).toBe(0);
  });

  it("空 hunks 不产出任何行（调用方需结合 truncated/parseError 判断，不能当无改动）", () => {
    expect(buildDiffRows([], { mode: "unified" })).toEqual([]);
    expect(buildDiffRows([], { mode: "split" })).toEqual([]);
  });
});

describe("limitDiffRows", () => {
  it("未超预算时原样返回并标记 hidden=0", () => {
    const rows = buildDiffRows(
      [hunk(1, 1, [line("context", 1, 1, "a")])],
      { mode: "unified", includeBottomGap: false },
    );
    const slice = limitDiffRows(rows, DEFAULT_DIFF_ROW_LIMIT);

    expect(slice.hidden).toBe(0);
    expect(slice.total).toBe(2);
    expect(slice.rows).toHaveLength(2);
  });

  it("超预算时按 limit 截断且不修改原序列", () => {
    const lines = Array.from({ length: 50 }, (_, index) =>
      line("context", index + 1, index + 1, `l${index}`),
    );
    const rows = buildDiffRows([hunk(1, 1, lines)], { mode: "unified", includeBottomGap: false });
    const slice = limitDiffRows(rows, 10);

    expect(slice.rows).toHaveLength(10);
    expect(slice.total).toBe(51);
    expect(slice.hidden).toBe(41);
    expect(rows).toHaveLength(51);
    expect(slice.rows[0].kind).toBe("hunk-header");
  });

  it("非法 limit 回落到默认预算而不是渲染 0 行", () => {
    const rows = buildDiffRows(
      [hunk(1, 1, [line("context", 1, 1, "a")])],
      { mode: "unified", includeBottomGap: false },
    );
    expect(limitDiffRows(rows, Number.NaN).rows).toHaveLength(2);
  });
});

describe("nextDiffContext · 上限保护", () => {
  it("正常追加一步", () => {
    expect(nextDiffContext(DIFF_CONTEXT_DEFAULT, 50)).toBe(53);
    expect(nextDiffContext(DIFF_CONTEXT_DEFAULT)).toBe(53);
  });

  it("夹取到 MAX_DIFF_CONTEXT 且到达上限后返回 null（不再发无意义请求）", () => {
    expect(nextDiffContext(MAX_DIFF_CONTEXT - 10, 1000)).toBe(MAX_DIFF_CONTEXT);
    expect(nextDiffContext(MAX_DIFF_CONTEXT, 50)).toBeNull();
    expect(nextDiffContext(Number.NaN, 0)).toBe(DIFF_CONTEXT_DEFAULT + 1);
  });
});

describe("hunk 摘要", () => {
  it("按行类型统计增删（不含上下文与 nonewline）", () => {
    const target = hunk(1, 1, [
      line("context", 1, 1, "a"),
      line("del", 2, null, "b"),
      line("del", 3, null, "c"),
      line("add", null, 2, "d"),
      line("nonewline", null, null, "\\ No newline at end of file"),
    ]);
    expect(countHunkChanges(target)).toEqual({ additions: 1, deletions: 2 });
  });
});

describe("raw 降级辅助（原始 diff 展示 / 下载）", () => {
  it("行数按 CRLF 归一化后统计，末尾空行不计", () => {
    expect(countDiffRawLines("")).toBe(0);
    expect(countDiffRawLines("a")).toBe(1);
    expect(countDiffRawLines("a\nb")).toBe(2);
    // 行尾换行只是终止符，不多算一行（否则「仅显示前 N 行」会虚报）。
    expect(countDiffRawLines("a\nb\n")).toBe(2);
    expect(countDiffRawLines("a\r\nb\r\n")).toBe(2);
    expect(countDiffRawLines("a\rb")).toBe(2);
  });

  it("下载文件名只取 basename 并剔除路径/非法字符（不进目录）", () => {
    expect(patchFileName("src/app.ts")).toBe("app.ts.patch");
    expect(patchFileName("src\\nested\\app.ts")).toBe("app.ts.patch");
    expect(patchFileName("C:/repo/a*b?c.go")).toBe("a_b_c.go.patch");
    expect(patchFileName("")).toBe("diff.patch");
  });

  it("高亮语言按扩展名映射，未知/无扩展名回落 text（不猜语言）", () => {
    expect(diffLanguageForPath("a/b/app.tsx")).toBe("tsx");
    expect(diffLanguageForPath("MODULE.GO")).toBe("go");
    expect(diffLanguageForPath("Makefile")).toBe("text");
    expect(diffLanguageForPath("weird.zzz")).toBe("text");
  });
});
