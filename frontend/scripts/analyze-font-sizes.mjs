#!/usr/bin/env node
/**
 * 字号审计脚本（font-size audit）
 *
 * 目的：找出前端代码里**绕过字号 token** 的字号声明，让所有界面的字号都能被
 * 「外观设置」里的三根字号轴（--app-root-font-size / --app-chat-font-size /
 * --app-code-font-size）统一控制。
 *
 * 判定口径：
 *   OK       值引用 var(--app-*font-size*) / var(--app-font-*) / 继承关键字 /
 *            相对单位（em 随局部上下文、rem 随 root 轴）/ 已存在的 `app-text-N` 字阶类
 *   PX       裸 px 值 —— 完全不受配置控制，必须改（硬性违规）
 *   TOKEN_UNKNOWN
 *            写了一个不存在的 `app-text-N`（如 app-text-14）：该类别名没有任何
 *            声明命中，元素会退回继承字号，等于手工放飞的硬编码（硬性违规）
 *   AXIS     聊天/代码语境里使用 root 轴字阶（text-sm 等）或 rem —— 能随配置缩放，
 *            但不随所在轴的 delta 同步；属观感一致性问题，需人工判断。
 *            注意：聊天作用域（.app-chat-scope/.app-chat-copy/…）在 base.css 里
 *            重绑了 `--app-root-font-size-N` 与 Tailwind 的 `--text-*`，静态分析
 *            看不到这层，因此本项偏保守（可能多报）；轴归属的权威结论请跑
 *            `node scripts/audit-font-sizes-dom.mjs --url <页面>` 做 DOM 实测。
 *   COLOR    text-[#hex] / text-[rgb(…)] 等颜色类，误报，忽略
 *   OTHER    无法静态判定（变量、模板串、非字面量），需人工判断
 *
 * 用法：
 *   node scripts/analyze-font-sizes.mjs                # 全量报表
 *   node scripts/analyze-font-sizes.mjs --json         # 机器可读
 *   node scripts/analyze-font-sizes.mjs --scope workspace   # 只看 workspace
 *   node scripts/analyze-font-sizes.mjs --only-bad     # 只列非 OK（PX/TOKEN_UNKNOWN/AXIS/OTHER）
 *   node scripts/analyze-font-sizes.mjs --check        # PX/TOKEN_UNKNOWN 非零则退出码 1
 */
import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const SRC_ROOT = path.resolve(HERE, "..", "src");
const SKIP_DIRS = new Set(["node_modules", "dist", ".tmp", ".artifacts", "__snapshots__"]);
const CODE_EXT = new Set([".ts", ".tsx", ".css"]);

const args = process.argv.slice(2);
const asJson = args.includes("--json");
const onlyBad = args.includes("--only-bad");
const checkMode = args.includes("--check");
const scopeIdx = args.indexOf("--scope");
const scopeArg = scopeIdx >= 0 ? args[scopeIdx + 1] : null;

/** Tailwind 默认字阶（v4 默认主题）：单位 rem，只跟 root 轴走。 */
const TAILWIND_TEXT_SCALE = new Set([
  "xs", "sm", "base", "lg", "xl", "2xl", "3xl", "4xl", "5xl", "6xl", "7xl", "8xl", "9xl",
]);

/** text-[…] 里的颜色写法：不是字号，直接忽略。 */
const COLOR_LIKE = /^(#|rgb\(|rgba\(|hsl\(|hsla\(|oklch\(|lab\(|lch\(|color\()/i;
/** 非字面量（模板串 / 变量 / 三元表达式）：静态判定不了。 */
const NON_LITERAL = /[$`]|\bprops\b|\bstyle\b/;

/**
 * 统一字号字阶类 `.app-text-N`（含 `.app-text-10-5` 这类半档）。
 * 这些类别名在 styles/globals/base.css 里声明，实际取值来自三根字号轴的 token，
 * 因此是「完全受配置控制」的正解；这里同时校验类别名真实存在 —— 写错档位
 * （如 app-text-14）不会命中任何规则，元素会退回继承字号，必须当成硬性违规。
 */
const APP_TEXT_TOKEN = /(?<![\w-])app-text-(\d+(?:-\d+)?)(?![\w-])/g;

function collectDeclaredAppTextTokens() {
  const cssPath = path.join(SRC_ROOT, "styles", "globals", "base.css");
  const css = fs.readFileSync(cssPath, "utf8");
  const declared = new Set();
  for (const m of css.matchAll(/\.app-text-(\d+(?:-\d+)?)\s*\{/g)) declared.add(m[1]);
  return declared;
}

const DECLARED_APP_TEXT_TOKENS = collectDeclaredAppTextTokens();

/** 元素级作用域证据：同一个 class 串里出现这些类，说明字号归属对应轴。 */
const CODE_SCOPE_EVIDENCE = /app-code-surface|app-terminal-copy|app-inline-mono|font-mono/;
const CHAT_SCOPE_EVIDENCE =
  /app-chat-scope|app-chat-copy|app-chat-input|app-chat-process-row|app-chat-turn-process|app-chat-bubble/;

function walk(dir, out = []) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (SKIP_DIRS.has(entry.name)) continue;
      walk(full, out);
      continue;
    }
    if (CODE_EXT.has(path.extname(entry.name))) out.push(full);
  }
  return out;
}

function classify(rawValue, { axisHint, kind }) {
  const value = rawValue.trim();
  if (!value) return "OTHER";
  if (COLOR_LIKE.test(value)) return "COLOR";
  if (/var\(\s*--app-/.test(value)) return "OK";
  if (/^(inherit|unset|initial|revert)$/.test(value)) return "OK";
  if (kind === "app-token") {
    // 已声明的字阶类：取值永远来自三根轴的 token。
    return DECLARED_APP_TEXT_TOKENS.has(value) ? "OK" : "TOKEN_UNKNOWN";
  }
  if (/^(-?[\d.]+)?em$/.test(value)) {
    // em 相对**局部**字号：宿主在聊天/码字作用域内时，它随所在轴同步，无需改造。
    return "OK";
  }
  if (/^(-?[\d.]+)?rem$/.test(value)) {
    return axisHint && axisHint !== "root" ? "AXIS" : "OK";
  }
  if (kind === "class-scale") {
    // Tailwind 默认字阶单位是 rem，随 root 轴缩放。
    return axisHint && axisHint !== "root" ? "AXIS" : "OK";
  }
  if (/^calc\(|^clamp\(|^min\(|^max\(/.test(value)) {
    if (/var\(/.test(value)) return "OK";
    if (/\d(px)\b/.test(value)) return "PX";
    return "OTHER";
  }
  if (/^[\d.]+px$/.test(value)) return "PX";
  if (/\bpx\b/.test(value)) return "PX";
  if (NON_LITERAL.test(value)) return "OTHER";
  if (/%$/.test(value)) return "OK";
  return "OTHER";
}

/** 从文件路径推断该文件主要落在哪根字号轴上。 */
function axisHintFor(relPath) {
  if (/message-list|message-markdown|composer|trajectory/.test(relPath)) return "chat";
  if (/diff|file-preview|artifact-preview|code|tool-row/.test(relPath)) return "code";
  return "root";
}

const CLASS_ARBITRARY = /text-\[([^\]]+)\]/g;
const CLASS_SCALE = /(?<![\w-])text-(xs|sm|base|lg|xl|2xl|3xl|4xl|5xl|6xl|7xl|8xl|9xl)(?![\w-])/g;
const CSS_FONT_SIZE = /font-size\s*:\s*([^;{}]+)/g;
const INLINE_FONT_SIZE = /fontSize\s*:\s*([^,}]+)/g;

const findings = [];

function addFinding(file, line, kind, raw, source, axisHint) {
  const value = raw.trim();
  const verdict = classify(value, { axisHint, kind });
  if (verdict === "COLOR") return;
  findings.push({
    file: path.relative(SRC_ROOT, file).replace(/\\/g, "/"),
    line,
    kind,
    value,
    verdict,
    axis: axisHint,
    source: source.trim().slice(0, 160),
  });
}

function lineOf(text, index) {
  let line = 1;
  for (let i = 0; i < index; i += 1) if (text[i] === "\n") line += 1;
  return line;
}

/** 取匹配点所在的 class 字面量（就近的一对引号/反引号之间），用于元素级作用域判定。 */
function classNameAt(text, index) {
  const start = Math.max(
    text.lastIndexOf('"', index),
    text.lastIndexOf("'", index),
    text.lastIndexOf("`", index),
  );
  if (start < 0) return null;
  const end = text.indexOf(text[start], index);
  if (end < 0) return null;
  return text.slice(start + 1, end);
}

/**
 * 轴归属判定：文件名只给「候选轴」，元素自身的类名证据优先。
 * 典型误报：`file-preview-dialog.tsx` 的弹窗标题/说明用 text-lg/text-sm —— 文件名命中
 * code 桶，但元素只是普通弹窗外壳，本来就该跟 root 轴；只有 class 里带
 * font-mono / app-code-* / app-terminal-copy / app-inline-mono 的元素才属于码字轴。
 */
function resolveAxisHint(text, index, fileHint) {
  const cls = classNameAt(text, index);
  if (!cls) return fileHint;
  if (CODE_SCOPE_EVIDENCE.test(cls)) return "code";
  if (CHAT_SCOPE_EVIDENCE.test(cls)) return "chat";
  return fileHint === "code" ? "root" : fileHint;
}

/**
 * 注释剥离：注释里的 `text-sm`、`app-text-12` 等示例字面量并非真实声明，
 * 若参与扫描会虚增 OK 计数，甚至因 classNameAt 归轴而误报 AXIS。
 * 实现要点：**保持字节长度与换行位置**（注释内容等长替换为空格），行号零漂移。
 *   · JSX 注释 `{/* … *\/}`：TS/TSX 生效；
 *   · 整行 TS 块注释（如文件头注释）：仅在注释独占整行时剥离；
 *   · CSS 块注释：CSS 文件生效。
 * 行内 `//` 不剥离：字符串里的 URL（http://…）会被误伤，得不偿失。
 */
function stripComments(source, isCss) {
  const blank = (m) => m.replace(/[^\n]/g, " ");
  if (isCss) return source.replace(/\/\*[\s\S]*?\*\//g, blank);
  return source
    .replace(/\{\/\*[\s\S]*?\*\/\}/g, blank)
    .replace(/^[ \t]*\/\*[\s\S]*?\*\/[ \t]*$/gm, blank);
}

function scan(file) {
  const rel = path.relative(SRC_ROOT, file).replace(/\\/g, "/");
  if (scopeArg && !rel.includes(scopeArg)) return;
  const source = fs.readFileSync(file, "utf8");
  const isCss = path.extname(file) === ".css";
  const text = stripComments(source, isCss);
  const axisHint = axisHintFor(rel);

  if (isCss) {
    for (const m of text.matchAll(CSS_FONT_SIZE)) {
      // 跳过 token 定义文件本身（那里就是唯一允许写基准 px 的地方）。
      if (rel === "styles/globals/tokens.css") continue;
      const value = m[1];
      if (/^var\(/.test(value.trim())) continue;
      addFinding(file, lineOf(text, m.index), "css", value, m[0], axisHint);
    }
    return;
  }

  for (const m of text.matchAll(CLASS_ARBITRARY)) {
    const raw = m[1].trim();
    // text-[length:...] 形式
    const normalized = raw.startsWith("length:") ? raw.slice("length:".length) : raw;
    // 只关心长度类写法：px/rem/em、length: 前缀、var(--app-*)、calc/clamp。
    const looksLikeLength =
      /\d(px|rem|em)\b/.test(normalized) ||
      raw.startsWith("length:") ||
      /var\(\s*--app-/.test(normalized) ||
      /^(calc|clamp|min|max)\(/.test(normalized);
    if (!looksLikeLength) continue;
    addFinding(
      file,
      lineOf(text, m.index),
      "class-arbitrary",
      normalized,
      m[0],
      resolveAxisHint(text, m.index, axisHint),
    );
  }

  for (const m of text.matchAll(CLASS_SCALE)) {
    const size = m[1];
    if (!TAILWIND_TEXT_SCALE.has(size)) continue;
    // 记录为 REM 语义（受 root 轴控制，但 axisHint != root 时是轴错位）。
    addFinding(
      file,
      lineOf(text, m.index),
      "class-scale",
      `${size} (tailwind)`,
      m[0],
      resolveAxisHint(text, m.index, axisHint),
    );
  }

  for (const m of text.matchAll(APP_TEXT_TOKEN)) {
    addFinding(file, lineOf(text, m.index), "app-token", m[1], m[0], axisHint);
  }

  for (const m of text.matchAll(INLINE_FONT_SIZE)) {
    const value = m[1];
    if (/^var\(/.test(value.trim())) continue;
    addFinding(file, lineOf(text, m.index), "inline-style", value, m[0], axisHint);
  }
}

const files = walk(SRC_ROOT);
for (const f of files) scan(f);

/** 硬性违规：完全不受配置控制。 */
const badVerdicts = new Set(["PX", "TOKEN_UNKNOWN"]);
/** 软性问题：能随配置缩放，但不随所在轴同步。 */
const softVerdicts = new Set(["AXIS", "OTHER"]);
const byVerdict = {};
const byFile = new Map();
for (const f of findings) {
  byVerdict[f.verdict] = (byVerdict[f.verdict] ?? 0) + 1;
  const key = `${f.verdict}\u0000${f.file}`;
  byFile.set(key, (byFile.get(key) ?? 0) + 1);
}

if (asJson) {
  console.log(JSON.stringify({ summary: byVerdict, total: findings.length, findings }, null, 2));
} else {
  // `--only-bad` 列全部「非 OK」条目（硬违规 + 软性问题），方便人工把 AXIS/OTHER
  // 一次过完；门禁仍只看 badVerdicts，见文件末尾。
  const shown = onlyBad
    ? findings.filter((f) => badVerdicts.has(f.verdict) || softVerdicts.has(f.verdict))
    : findings;
  const header = `字号审计：${findings.length} 处声明  ` +
    `OK=${byVerdict.OK ?? 0}  PX(不受配置控制)=${byVerdict.PX ?? 0}  ` +
    `TOKEN_UNKNOWN(字阶类不存在)=${byVerdict.TOKEN_UNKNOWN ?? 0}  ` +
    `AXIS(轴错位)=${byVerdict.AXIS ?? 0}  OTHER(需人工)=${byVerdict.OTHER ?? 0}`;
  console.log(header);
  console.log("".padEnd(header.length, "="));

  const grouped = new Map();
  for (const f of shown) {
    if (!grouped.has(f.file)) grouped.set(f.file, []);
    grouped.get(f.file).push(f);
  }
  for (const [file, items] of [...grouped.entries()].sort((a, b) => b[1].length - a[1].length)) {
    const bad = items.filter((i) => badVerdicts.has(i.verdict)).length;
    console.log(`\n${file}  (${items.length} 处${bad ? `, 需修 ${bad}` : ""})`);
    for (const i of items.sort((a, b) => a.line - b.line)) {
      console.log(`  L${String(i.line).padStart(4)}  [${i.verdict.padEnd(6)}] ${i.kind}`);
      console.log(`        值: ${i.value}`);
      console.log(`        源: ${i.source}`);
    }
  }
}

// 门禁只拦「完全不受字号配置控制」的硬违规：裸 px 与不存在的字阶类名。
// AXIS 是文件级启发式（聊天/码字作用域的 CSS 变量重绑它看不到），OTHER 是
// 模板串/测量读取之类的静态不可判定项，二者都需人工过一遍，不适合做门禁。
// 轴归属的权威结论以 scripts/audit-font-sizes-dom.mjs 的 DOM 实测为准。
const violations = findings.filter(
  (f) => f.verdict === "PX" || f.verdict === "TOKEN_UNKNOWN",
).length;
if (checkMode && violations > 0) {
  console.error(`\n[analyze-font-sizes] 存在 ${violations} 处不受字号配置控制的声明。`);
  process.exit(1);
}
