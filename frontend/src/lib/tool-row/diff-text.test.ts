import { describe, expect, it } from "vitest";

import {
  parseToolPatch,
  patchTextFromArgs,
  patchTextFromOutput,
  stripRenderedPatch,
  TOOL_PATCH_TEXT_LIMIT,
} from "./diff-text";

/** 真实工具输出形态（后端 tool.completed 的 render_output：说明行 + ```diff 围栏）。 */
const PATCH_OUTPUT = [
  "补丁已应用：修改 1；影响 1 个路径",
  "",
  "文件差异:",
  "```diff",
  "--- a/hello.txt",
  "+++ b/hello.txt",
  "@@ -1 +1 @@",
  "-old line",
  "+new line",
  "```",
].join("\n");

describe("patchTextFromOutput", () => {
  it("从工具输出的 ```diff 围栏里取出补丁正文", () => {
    const patch = patchTextFromOutput(PATCH_OUTPUT);
    expect(patch?.truncated).toBe(false);
    expect(patch?.text.startsWith("--- a/hello.txt")).toBe(true);
    expect(patch?.text.endsWith("+new line")).toBe(true);
  });

  it("无围栏时从首个带行号 @@ 行回退到文件头", () => {
    const patch = patchTextFromOutput(
      ["应用完成", "--- a/x.ts", "+++ b/x.ts", "@@ -3,1 +3,2 @@", " ctx", "+add"].join("\n"),
    );
    expect(patch?.text.split("\n")[0]).toBe("--- a/x.ts");
    expect(parseToolPatch(patch?.text ?? "").ok).toBe(true);
  });

  it("没有带行号的 hunk 头就不给候选（Codex 裸 @@ 补丁交给原始文本面板）", () => {
    const codex = ["```diff", "*** Begin Patch", "*** Update File: a.ts", "@@", "-old", "+new", "```"].join(
      "\n",
    );
    expect(patchTextFromOutput(codex)).toBeUndefined();
    expect(patchTextFromOutput("补丁已应用：修改 0")).toBeUndefined();
    expect(patchTextFromOutput("")).toBeUndefined();
  });
});

describe("patchTextFromArgs", () => {
  it("取 patch / diff 字段，非补丁文本一律忽略", () => {
    const codex = "*** Begin Patch\n*** Update File: a.ts\n@@\n-old\n+new\n*** End Patch";
    expect(patchTextFromArgs({ patch: codex })?.text).toBe(codex);
    expect(patchTextFromArgs({ diff: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b" })?.text).toContain("@@ -1 +1 @@");
    expect(patchTextFromArgs({ patch: "just text" })).toBeUndefined();
    expect(patchTextFromArgs({ patch: 42 })).toBeUndefined();
    expect(patchTextFromArgs({})).toBeUndefined();
    expect(patchTextFromArgs(undefined)).toBeUndefined();
  });

  it("按行边界截断到上限：只掉整行，不产生半行内容", () => {
    const lines = Array.from({ length: 40000 }, (_, index) => `+line ${index} ${"x".repeat(16)}`);
    const patch = patchTextFromArgs({ patch: ["--- a/big.ts", "+++ b/big.ts", "@@ -1,1 +1,40000 @@", ...lines].join("\n") });
    expect(patch?.truncated).toBe(true);
    expect((patch?.text.length ?? 0) <= TOOL_PATCH_TEXT_LIMIT).toBe(true);
    const kept = patch?.text.split("\n") ?? [];
    expect(kept[kept.length - 1]).toMatch(/^\+line \d+ x{16}$/);
  });
});

describe("parseToolPatch", () => {
  it("解析单 hunk：行号来自 @@ 头，增删行各归其侧", () => {
    const parsed = parseToolPatch(["--- a/hello.txt", "+++ b/hello.txt", "@@ -1 +1 @@", "-old line", "+new line"].join("\n"));
    expect(parsed.ok).toBe(true);
    if (!parsed.ok) {
      return;
    }
    expect(parsed.files.map((file) => file.path)).toEqual(["hello.txt"]);
    expect(parsed.files[0].hunks[0].header).toBe("@@ -1 +1 @@");
    expect(parsed.files[0].hunks[0].lines).toEqual([
      { type: "del", oldNo: 1, newNo: null, text: "old line" },
      { type: "add", oldNo: null, newNo: 1, text: "new line" },
    ]);
    expect(parsed.insertions).toBe(1);
    expect(parsed.deletions).toBe(1);
    expect(parsed.partial).toBe(false);
  });

  it("解析多文件多 hunk：行号逐行推进，nonewline 不占行号，/dev/null 用旧路径", () => {
    const parsed = parseToolPatch(
      [
        "diff --git a/a.ts b/a.ts",
        "index 111..222 100644",
        "--- a/a.ts",
        "+++ b/a.ts",
        "@@ -1,2 +1,3 @@",
        " ctx",
        "-del",
        "+add",
        "+more",
        "@@ -10,2 +11,2 @@ tail",
        " x",
        "-y",
        "+z",
        "\\ No newline at end of file",
        "diff --git a/dir/b.ts b/dir/b.ts",
        "new file mode 100644",
        "--- /dev/null",
        "+++ b/dir/b.ts",
        "@@ -0,0 +1,1 @@",
        "+fresh",
      ].join("\n"),
    );
    expect(parsed.ok).toBe(true);
    if (!parsed.ok) {
      return;
    }
    expect(parsed.files.map((file) => file.path)).toEqual(["a.ts", "dir/b.ts"]);
    expect(parsed.files[0].hunks).toHaveLength(2);
    expect(parsed.files[0].hunks[0].lines).toEqual([
      { type: "context", oldNo: 1, newNo: 1, text: "ctx" },
      { type: "del", oldNo: 2, newNo: null, text: "del" },
      { type: "add", oldNo: null, newNo: 2, text: "add" },
      { type: "add", oldNo: null, newNo: 3, text: "more" },
    ]);
    expect(parsed.files[0].hunks[1].header).toBe("@@ -10,2 +11,2 @@ tail");
    expect(parsed.files[0].hunks[1].lines).toEqual([
      { type: "context", oldNo: 10, newNo: 11, text: "x" },
      { type: "del", oldNo: 11, newNo: null, text: "y" },
      { type: "add", oldNo: null, newNo: 12, text: "z" },
      { type: "nonewline", oldNo: null, newNo: null, text: "" },
    ]);
    expect(parsed.files[1].hunks[0].lines).toEqual([
      { type: "add", oldNo: null, newNo: 1, text: "fresh" },
    ]);
    expect(parsed.insertions).toBe(4);
    expect(parsed.deletions).toBe(2);
    expect(parsed.hunks).toHaveLength(3);
    expect(parsed.partial).toBe(false);
  });

  it("hunk 正文里以 +++ 开头的增行是内容，不是文件头", () => {
    const parsed = parseToolPatch(
      ["--- a/a.txt", "+++ b/a.txt", "@@ -1,2 +1,3 @@", " head", "-old", "+++ kept", "+tail"].join("\n"),
    );
    expect(parsed.ok).toBe(true);
    if (!parsed.ok) {
      return;
    }
    expect(parsed.files[0].hunks[0].lines[2]).toEqual({
      type: "add",
      oldNo: null,
      newNo: 2,
      text: "++ kept",
    });
  });

  it("末尾 hunk 行数不足 = 截断，标记 partial 但不补行", () => {
    const parsed = parseToolPatch(["--- a/a.ts", "+++ b/a.ts", "@@ -1,3 +1,3 @@", " one", " two"].join("\n"));
    expect(parsed.ok).toBe(true);
    if (!parsed.ok) {
      return;
    }
    expect(parsed.partial).toBe(true);
    expect(parsed.files[0].hunks[0].lines).toEqual([
      { type: "context", oldNo: 1, newNo: 1, text: "one" },
      { type: "context", oldNo: 2, newNo: 2, text: "two" },
    ]);
  });

  it("非末尾 hunk 行数不符 = 文本损坏，判失败", () => {
    const parsed = parseToolPatch(
      ["--- a/a.ts", "+++ b/a.ts", "@@ -1,5 +1,5 @@", " one", "@@ -20,1 +20,1 @@", "-a", "+b"].join("\n"),
    );
    expect(parsed).toEqual({ ok: false, reason: "hunk-line-count-mismatch" });
  });

  it("Codex 补丁 / 空文本 / 无 hunk 一律失败（不编造行号）", () => {
    const codex = "*** Begin Patch\n*** Update File: a.ts\n@@\n-old\n+new\n*** End Patch";
    expect(parseToolPatch(codex)).toEqual({ ok: false, reason: "no-hunks" });
    expect(parseToolPatch("")).toEqual({ ok: false, reason: "no-hunks" });
    expect(parseToolPatch("Binary files a/x and b/x differ")).toEqual({ ok: false, reason: "no-hunks" });
  });
});

describe("stripRenderedPatch", () => {
  it("去掉已由行级视图承载的 ```diff 围栏，保留工具说明行", () => {
    expect(stripRenderedPatch(PATCH_OUTPUT)).toBe("补丁已应用：修改 1；影响 1 个路径\n\n文件差异:");
  });

  it("未闭合的围栏（结果被截断）同样去掉；没有带行号 hunk 的围栏保持原样", () => {
    expect(stripRenderedPatch("文件差异:\n```diff\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old")).toBe(
      "文件差异:",
    );
    const codex = "说明\n```diff\n*** Begin Patch\n@@\n-a\n+b\n```";
    expect(stripRenderedPatch(codex)).toBe(codex);
  });

  it("没有围栏的文本原样返回（错误信息 / 普通输出不被动）", () => {
    expect(stripRenderedPatch("补丁应用失败：STALE_CONTEXT")).toBe("补丁应用失败：STALE_CONTEXT");
    expect(stripRenderedPatch("")).toBe("");
  });
});
