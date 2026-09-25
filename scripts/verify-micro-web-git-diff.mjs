// 行为验证：aicli micro web client 「GIT」页签 diff 行渲染（js/git.js，Node 沙盒，stub document）。
// 运行：node scripts/verify-micro-web-git-diff.mjs（无需浏览器；见 docs/aicli/web-testing.md 第 4 节）
// 覆盖：行号**只有一列**（add→新侧 / del→老侧 / context→新侧 / nonewline→空），
//       两侧行号不同时只出现该侧那一个数字（不再拼成「老 新」两列）、null/缺字段不补 0、
//       前缀与行 class、文本转义（不出现原始标签）、以及 renderDiff 必须走同一个行构造器。
import assert from "node:assert";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_DIR = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web");

// ---- DOM stub（git.js 顶层只经 util.js 取一个 #toast-container）----
globalThis.document = {
  getElementById: () => null,
  querySelector: () => null,
  addEventListener() {},
  removeEventListener() {},
};

const { diffLineHtml, diffLineNo } = await import(pathToFileURL(path.join(WEB_DIR, "js", "git.js")).href);

// 行号格的内容（没有就返回空串）——断言只关心这一格是不是「一个数字」。
function noCell(html) {
  const m = /<span class="git-diff-no">([\s\S]*?)<\/span>/.exec(html);
  return m ? m[1] : null;
}
function countMatches(text, re) {
  return (text.match(re) || []).length;
}

// ---- 1. 每个行块只有一个行号格、一个内容格 ----
const addLine = { type: "add", old_no: null, new_no: 8, text: "新增" };
const html = diffLineHtml(addLine);
assert.strictEqual(countMatches(html, /class="git-diff-no"/g), 1, "每行只应有一个行号格");
assert.strictEqual(countMatches(html, /class="git-diff-text"/g), 1, "每行只应有一个内容格");
assert.ok(html.includes('class="git-diff-line git-diff-add"'), "行 class 应带类型");

// ---- 2. add 行取新侧行号，不出现老侧 ----
assert.strictEqual(noCell(html), "8", "add 行行号应取新侧");
assert.ok(html.includes("+ 新增"), "add 行前缀应为 +");

// ---- 3. del 行取老侧行号，且不出现新侧 ----
const delHtml = diffLineHtml({ type: "del", old_no: 7, new_no: null, text: "删除" });
assert.strictEqual(noCell(delHtml), "7", "del 行行号应取老侧");
assert.ok(delHtml.includes("- 删除"), "del 行前缀应为 -");

// ---- 4. context 行两侧都有行号时只显示新侧一个数字（回归：曾拼成「10 20」两列）----
const ctxHtml = diffLineHtml({ type: "context", old_no: 10, new_no: 20, text: "上下文" });
assert.strictEqual(noCell(ctxHtml), "20", "context 行行号应取新侧（与磁盘文件对齐）");
assert.ok(!/10[\s\u00a0]+20/.test(ctxHtml), "行号格不得同时出现老/新两个数字（两列回归）");
assert.strictEqual(countMatches(noCell(ctxHtml), /\d+/g), 1, "行号格内只应有一个数字");

// ---- 5. nonewline / 缺字段：留空，不补 0、不借对侧 ----
const nonewlineHtml = diffLineHtml({ type: "nonewline", old_no: 3, new_no: 3, text: "\\ No newline at end of file" });
assert.strictEqual(noCell(nonewlineHtml), "", "nonewline 行不应有行号");
assert.ok(nonewlineHtml.includes("\\ No newline"), "nonewline 行前缀应为 \\");
assert.strictEqual(diffLineNo({ type: "add" }, "add"), "", "缺 new_no 的 add 行不应补 0");
assert.strictEqual(diffLineNo({ type: "del", old_no: null }, "del"), "", "null 老侧行号不应补 0");
assert.strictEqual(diffLineNo({ type: "context", new_no: 0 }, "context"), "0", "0 是合法行号，不能被当缺省丢掉");

// ---- 6. 文本转义：不把 diff 内容当 HTML 渲染 ----
const escHtml = diffLineHtml({ type: "add", new_no: 1, text: "<script>alert(1)</script>&\"'" });
assert.ok(!escHtml.includes("<script>"), "行文本必须转义（不得出现原始标签）");
assert.ok(escHtml.includes("&lt;script&gt;"), "行文本应转义为实体");

// ---- 7. 静态契约：renderDiff 必须用同一个行构造器（不许另起一份行 HTML）----
const src = fs.readFileSync(path.join(WEB_DIR, "js", "git.js"), "utf8");
assert.ok(
  src.includes("html.push(diffLineHtml(lines[j] || {}))"),
  "renderDiff 必须复用 diffLineHtml（另起一份行模板会让本脚本失守）",
);
assert.strictEqual(
  countMatches(src, /class="git-diff-no"/g),
  1,
  "git.js 里只应有一处生成行号格（diffLineHtml）",
);
assert.ok(!/old_no[\s\S]{0,40}\+ " " \+[\s\S]{0,40}new_no/.test(src), "不得再把老/新行号拼成两列");

// ---- 8. style.css：行号列宽度按单列给（不是两列并排时代的 88px）----
const css = fs.readFileSync(path.join(WEB_DIR, "style.css"), "utf8");
const noRule = /\.git-diff-no\s*\{([^}]*)\}/.exec(css);
assert.ok(noRule, "style.css 应有 .git-diff-no 规则");
const widthMatch = /width:\s*(\d+)px/.exec(noRule[1]);
assert.ok(widthMatch, ".git-diff-no 应显式给出宽度（单列）");
assert.ok(
  Number(widthMatch[1]) <= 64,
  `.git-diff-no 宽度 ${widthMatch[1]}px 超出单列需求（两列并排时代为 88px）`,
);

console.log("verify-micro-web-git-diff: 全部断言通过（单列行号 / 类型取值 / 转义 / 复用行构造器 / CSS 宽度）");
