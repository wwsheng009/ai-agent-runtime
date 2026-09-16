#!/usr/bin/env node
/**
 * DOM 级字号轴差分审计（frontend/scripts/audit-font-sizes-dom.mjs）
 *
 * 目标：验证「页面上所有文字是否都受外观设置的三根字号轴控制」。
 *   轴 = root（界面基准，--app-root-font-size）
 *        chat（聊天正文，--app-chat-font-size）
 *        code（代码/终端，--app-code-font-size）
 *
 * 思路（实测优先，不靠猜）：
 *   跑 4 次快照 —— 基准 + 分别只放大其中一根轴，其余不变 —— 然后逐元素 diff：
 *     · 三根轴都不响应   → 硬编码字号（PX 现场），缺陷
 *     · 只响应 root，却身处聊天/代码语境 → 轴错位（能缩放但不跟所在轴同步）
 *     · 响应所在轴       → 正常
 *
 * 元素身份用「DOM 结构链 + 标签 + class」定位，避免依赖索引顺序。
 *
 * 用法：
 *   node scripts/audit-font-sizes-dom.mjs --url http://localhost:5193/workspace/sessions/<id>
 *   node scripts/audit-font-sizes-dom.mjs --url ... --json > .tmp/dom-font-audit.json
 *   node scripts/audit-font-sizes-dom.mjs --url ... --base 16/15/13 --root 22/15/13 --chat 16/21/13 --code 16/15/19
 */
import fs from "node:fs";
import path from "node:path";
import process from "node:process";

const args = process.argv.slice(2);
const readArg = (name, fallback) => {
  const i = args.indexOf(`--${name}`);
  return i >= 0 && args[i + 1] ? args[i + 1] : fallback;
};
const hasFlag = (name) => args.includes(`--${name}`);

const url = readArg("url", "http://localhost:5193/");
const asJson = hasFlag("json");
const waitMs = Number(readArg("wait", "4000"));

const parseTriple = (raw, fallback) => {
  const parts = String(raw ?? "")
    .split("/")
    .map((v) => Number(v.trim()));
  return parts.length === 3 && parts.every((n) => Number.isFinite(n) && n > 0) ? parts : fallback;
};

const variants = {
  base: parseTriple(readArg("base", "16/15/13"), [16, 15, 13]),
  root: parseTriple(readArg("root", "22/15/13"), [22, 15, 13]),
  chat: parseTriple(readArg("chat", "16/21/13"), [16, 21, 13]),
  code: parseTriple(readArg("code", "16/15/19"), [16, 15, 19]),
};

let chromium;
try {
  ({ chromium } = await import("@playwright/test"));
} catch {
  ({ chromium } = await import("playwright"));
}

const SETTINGS_KEY = "ai-agent-runtime.workspace.settings";

/**
 * 「字号源自码字轴」的判定选择器：必须与 base.css 中真正把 font-size 绑到
 * `--app-code-font-size*` 的规则一一对应，否则会把仅使用等宽**字体族**的元素
 * 误判为码字轴，进而把本来正确跟随 root 轴的文字报成 AXIS 轴错位。
 *   · `.app-inline-mono` / `.app-terminal-copy` / `.app-code-surface`：显式作用域；
 *   · `input.font-mono` / `textarea.font-mono` / `.app-code-editor`：base.css 里带
 *     `!important` 的强制码字轴规则；
 *   · `.font-mono[class*="app-text-"]`：`.font-mono.app-text-N` 组合规则（后代继承同轴）。
 * 注意：**裸 `.font-mono`** 只是 Tailwind 的字体族工具类，不携带字号语义，
 * 例如 /runtime/config 里的 `span.font-mono` 码片按设计跟随 root 轴。
 * 该函数会被序列化进浏览器执行，故选择器用参数传入（不能用闭包变量）。
 */
const CODE_AXIS_SIZE_SELECTOR = [
  ".app-code-surface",
  ".app-terminal-copy",
  ".app-inline-mono",
  ".app-md-inline-code",
  ".app-code-editor",
  "input.font-mono",
  "textarea.font-mono",
  '.font-mono[class*="app-text-"]',
].join(", ");

const SNAPSHOT_FN = (codeAxisSelector) => {
  const out = {};
  const root = document.documentElement;
  const seen = new Map();
  for (const el of document.querySelectorAll("body *")) {
    if (el.closest(".sr-only") || el.classList.contains("sr-only")) continue;
    const hasOwnText = [...el.childNodes].some(
      (n) => n.nodeType === 3 && (n.textContent || "").trim().length > 0,
    );
    if (!hasOwnText) continue;
    const rect = el.getBoundingClientRect();
    if (rect.width < 1 || rect.height < 1) continue;
    const style = getComputedStyle(el);
    if (style.visibility === "hidden" || style.display === "none") continue;
    const size = Number.parseFloat(style.fontSize);
    if (!Number.isFinite(size)) continue;

    const chain = [];
    let node = el;
    while (node && node !== document.body) {
      const parent = node.parentElement;
      if (!parent) break;
      const sameTag = [...parent.children].filter((c) => c.tagName === node.tagName);
      chain.unshift(`${node.tagName}${sameTag.indexOf(node)}`);
      node = parent;
    }
    const baseKey = `${chain.join(">")}|${(el.getAttribute("class") || "").trim()}`;
    const dup = seen.get(baseKey) ?? 0;
    seen.set(baseKey, dup + 1);

    out[`${baseKey}#${dup}`] = {
      tag: el.tagName.toLowerCase(),
      classes: (el.getAttribute("class") || "").trim(),
      size,
      text: (el.textContent || "").trim().replace(/\s+/g, " ").slice(0, 48),
      inChat: !!el.closest(".app-chat-copy, .app-chat-input, .app-chat-process-row, .app-chat-bubble"),
      inCode: !!el.closest(codeAxisSelector),
    };
  }
  return {
    axis: {
      root: Number.parseFloat(getComputedStyle(root).fontSize),
      chat: Number.parseFloat(getComputedStyle(root).getPropertyValue("--app-chat-font-size")),
      code: Number.parseFloat(getComputedStyle(root).getPropertyValue("--app-code-font-size")),
    },
    elements: out,
  };
};

const browser = await chromium.launch({ channel: "chrome" }).catch(() => chromium.launch());

async function capture([textSize, chatTextSize, codeTextSize], label) {
  const context = await browser.newContext({ viewport: { width: 1600, height: 1000 } });
  await context.addInitScript(
    ([key, payload]) => window.localStorage.setItem(key, payload),
    [
      SETTINGS_KEY,
      JSON.stringify({ appearance: { textSize, chatTextSize, codeTextSize, themeMode: "dark" } }),
    ],
  );
  const page = await context.newPage();
  await page.goto(url, { waitUntil: "domcontentloaded" });
  await page.waitForTimeout(waitMs);
  // 统一滚动行为，让懒加载区域尽量一致地出现。
  await page.evaluate(async () => {
    const scrollables = [...document.querySelectorAll("*")].filter(
      (el) => el.scrollHeight > el.clientHeight + 40 && el.clientHeight > 120,
    );
    for (const el of scrollables.slice(0, 6)) {
      el.scrollTop = el.scrollHeight;
      await new Promise((r) => setTimeout(r, 100));
    }
    await new Promise((r) => setTimeout(r, 200));
  });
  await page.waitForTimeout(500);
  const snapshot = await page.evaluate(SNAPSHOT_FN, CODE_AXIS_SIZE_SELECTOR);
  const shot = path.resolve(process.cwd(), ".tmp", `font-audit-${label}.png`);
  fs.mkdirSync(path.dirname(shot), { recursive: true });
  await page.screenshot({ path: shot });
  await context.close();
  return snapshot;
}

const snapshots = {
  base: await capture(variants.base, "base"),
  root: await capture(variants.root, "root"),
  chat: await capture(variants.chat, "chat"),
  code: await capture(variants.code, "code"),
};
await browser.close();

const baseEls = snapshots.base.elements;
const rows = [];
for (const [key, el] of Object.entries(baseEls)) {
  const r = snapshots.root.elements[key];
  const c = snapshots.chat.elements[key];
  const d = snapshots.code.elements[key];
  if (!r || !c || !d) continue;
  const dRoot = Number((r.size - el.size).toFixed(2));
  const dChat = Number((c.size - el.size).toFixed(2));
  const dCode = Number((d.size - el.size).toFixed(2));
  const respondsRoot = Math.abs(dRoot) > 0.01;
  const respondsChat = Math.abs(dChat) > 0.01;
  const respondsCode = Math.abs(dCode) > 0.01;
  let verdict;
  if (respondsChat || respondsCode) verdict = "OK"; // 跟了所在轴
  else if (respondsRoot) {
    verdict = el.inChat || el.inCode ? "AXIS" : "OK"; // 能缩放，但没跟所在轴
  } else verdict = "FROZEN"; // 三根轴都不动 = 硬编码
  rows.push({
    key,
    tag: el.tag,
    classes: el.classes,
    text: el.text,
    base: el.size,
    dRoot,
    dChat,
    dCode,
    axis: el.inChat ? "chat" : el.inCode ? "code" : "root",
    verdict,
  });
}

const summary = {};
for (const r of rows) summary[r.verdict] = (summary[r.verdict] ?? 0) + 1;

const frozenBySize = {};
for (const r of rows.filter((x) => x.verdict === "FROZEN")) {
  frozenBySize[r.base] = (frozenBySize[r.base] ?? 0) + 1;
}

const report = {
  url,
  variants,
  axisObserved: {
    base: snapshots.base.axis,
    root: snapshots.root.axis,
    chat: snapshots.chat.axis,
    code: snapshots.code.axis,
  },
  counts: { compared: rows.length, ...summary },
  frozenBySize,
  frozen: rows.filter((r) => r.verdict === "FROZEN").sort((a, b) => b.base - a.base),
  axisMismatch: rows.filter((r) => r.verdict === "AXIS").sort((a, b) => b.base - a.base),
};

if (asJson) {
  console.log(JSON.stringify(report, null, 2));
} else {
  console.log(`URL: ${url}`);
  console.log(
    `变体：base ${variants.base.join("/")} | root ${variants.root.join("/")} | ` +
      `chat ${variants.chat.join("/")} | code ${variants.code.join("/")}`,
  );
  console.log(
    `轴实测：base root=${snapshots.base.axis.root} chat=${snapshots.base.axis.chat} code=${snapshots.base.axis.code}`,
  );
  console.log(
    `对比 ${rows.length} 个文本元素：OK=${summary.OK ?? 0}  ` +
      `FROZEN(硬编码，不受任何轴控制)=${summary.FROZEN ?? 0}  ` +
      `AXIS(轴错位)=${summary.AXIS ?? 0}`,
  );
  if (Object.keys(frozenBySize).length) {
    console.log(`\nFROZEN 字号分布：`);
    for (const [size, count] of Object.entries(frozenBySize).sort(
      (a, b) => Number(b[0]) - Number(a[0]),
    )) {
      console.log(`  ${`${size}px`.padStart(8)}  ${String(count).padStart(4)} 个元素`);
    }
  }
  console.log(`\nFROZEN 明细（最多 120 条）：`);
  for (const r of report.frozen.slice(0, 120)) {
    console.log(
      `  ${String(r.base).padStart(5)}px [${r.axis.padEnd(4)}] ${r.tag.padEnd(6)} ` +
        `${JSON.stringify(r.text).slice(0, 30).padEnd(32)} ${r.classes.slice(0, 80)}`,
    );
  }
  if (report.axisMismatch.length) {
    console.log(`\nAXIS 轴错位明细（最多 80 条）：`);
    for (const r of report.axisMismatch.slice(0, 80)) {
      console.log(
        `  ${String(r.base).padStart(5)}px [${r.axis.padEnd(4)}] ${r.tag.padEnd(6)} ` +
          `${JSON.stringify(r.text).slice(0, 30).padEnd(32)} ${r.classes.slice(0, 80)}`,
      );
    }
  }
}
