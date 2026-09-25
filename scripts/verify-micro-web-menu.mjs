// 行为验证：aicli micro web client 顶部菜单栏 + 会话导出下载（无需浏览器）。
// 运行：node scripts/verify-micro-web-menu.mjs（仓库根目录）
// 覆盖：
//   1. app.js 入口静态检查（DOM stub + 动态 import）：模块图完整、菜单模块已接线
//   2. index.html 结构：顶部菜单栏（文件/视图/帮助）+ 右侧状态簇（连接/轮次/发送/主题）
//   3. 每个 data-menu-action 都能落到真实存在的目标控件（转发点击，无第二份实现）
//   4. 菜单开合：点按钮展开、切换菜单、点外部关闭、Esc 关闭并回焦
//   5. 导出：请求 /web/api/export?format=…、按 Content-Disposition 命名下载、
//      失败（HTTP 非 2xx）时提示且不产生下载
//   6. style.css：不带 fallback 的 var(--token) 必须已定义（否则声明失效 → 背景透明）；
//      快捷键面板背景取自 --modalBg 且三种主题下均已定义
import assert from "node:assert";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_DIR = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web");
const INDEX_HTML = fs.readFileSync(path.join(WEB_DIR, "index.html"), "utf8");

let failures = 0;
function check(name, fn) {
  try {
    fn();
    console.log("  ok  - " + name);
  } catch (err) {
    failures++;
    console.log("  FAIL- " + name + "\n        " + (err && err.message ? err.message : err));
  }
}

// ===========================================================================
// 迷你 DOM（解析 index.html，够跑菜单与转发点击）
// ===========================================================================
const VOID_TAGS = new Set(["meta", "link", "br", "img", "input", "hr", "source"]);

function classListOf(el) { return String(el.attrs.class || "").split(/\s+/).filter(Boolean); }
function setClassList(el, list) { el.attrs.class = list.join(" "); }

function makeElement(tagName) {
  const el = {
    nodeType: 1,
    tagName: String(tagName).toUpperCase(),
    attrs: {},
    children: [],
    parentNode: null,
    style: {},
    listeners: {},
    value: "",
    disabled: false,
    _text: "",
    get textContent() {
      if (this.children.length) { return this.children.map((c) => c.textContent).join(""); }
      return this._text;
    },
    set textContent(v) { this._text = String(v); this.children = []; },
    set innerHTML(v) { this._html = String(v); this.children = []; this._text = ""; },
    get innerHTML() { return this._html || ""; },
    classList: {
      contains: (c) => classListOf(el).indexOf(c) >= 0,
      add: function () { const l = classListOf(el); Array.prototype.slice.call(arguments).join(" ").split(/\s+/).filter(Boolean).forEach((c) => { if (l.indexOf(c) < 0) l.push(c); }); setClassList(el, l); },
      remove: function () { const drop = Array.prototype.slice.call(arguments).join(" ").split(/\s+/).filter(Boolean); setClassList(el, classListOf(el).filter((c) => drop.indexOf(c) < 0)); },
      toggle: (c, on) => { const has = classListOf(el).indexOf(c) >= 0; const want = on === undefined ? !has : !!on; if (want && !has) el.classList.add(c); if (!want && has) el.classList.remove(c); },
    },
    getAttribute(name) { return name in this.attrs ? this.attrs[name] : null; },
    setAttribute(name, value) { this.attrs[name] = String(value); },
    removeAttribute(name) { delete this.attrs[name]; },
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    removeEventListener(type, fn) { this.listeners[type] = (this.listeners[type] || []).filter((f) => f !== fn); },
    dispatch(type, ev) {
      const event = ev || { type: type, stopPropagation() { event._stopped = true; }, preventDefault() {} };
      (this.listeners[type] || []).forEach((fn) => fn(event));
      if (!event._stopped && this.parentNode && this.parentNode.dispatch) { this.parentNode.dispatch(type, event); }
      return event;
    },
    click() { return this.dispatch("click"); },
    focus() { stubDocument.activeElement = this; },
    appendChild(child) { child.parentNode = this; this.children.push(child); return child; },
    removeChild(child) { const i = this.children.indexOf(child); if (i >= 0) this.children.splice(i, 1); child.parentNode = null; return child; },
    querySelector(sel) { const all = this.querySelectorAll(sel); return all.length ? all[0] : null; },
    querySelectorAll(sel) { return queryAllIn(this, sel); },
    closest(sel) { let node = this; while (node && node.nodeType === 1) { if (matches(node, sel)) return node; node = node.parentNode; } return null; },
  };
  return el;
}

function matchCompound(el, sel) {
  if (!el || el.nodeType !== 1) { return false; }
  const parts = sel.match(/(#[A-Za-z][\w-]*|\.[\w-]+|\[[^\]]+\]|[a-zA-Z][\w-]*)/g) || [];
  if (!parts.length) { return false; }
  for (const p of parts) {
    if (p.charAt(0) === "#") {
      if (el.attrs.id !== p.slice(1)) { return false; }
    } else if (p.charAt(0) === ".") {
      if (classListOf(el).indexOf(p.slice(1)) < 0) { return false; }
    } else if (p.charAt(0) === "[") {
      const m = p.match(/^\[([\w-]+)(?:="([^"]*)")?\]$/);
      if (!m) { return false; }
      const v = el.attrs[m[1]];
      if (m[2] === undefined) { if (v == null) { return false; } } else if (v !== m[2]) { return false; }
    } else if (el.tagName !== p.toUpperCase()) {
      return false;
    }
  }
  return true;
}

function matches(el, sel) {
  // 只支持后代组合（"#id .class"）与单段复合选择器
  const segs = sel.trim().split(/\s+/);
  if (!matchCompound(el, segs[segs.length - 1])) { return false; }
  let node = el.parentNode;
  for (let i = segs.length - 2; i >= 0; i--) {
    let found = false;
    while (node && node.nodeType === 1) {
      if (matchCompound(node, segs[i])) { found = true; node = node.parentNode; break; }
      node = node.parentNode;
    }
    if (!found) { return false; }
  }
  return true;
}

function queryAllIn(root, sel) {
  const out = [];
  const walk = (node) => {
    (node.children || []).forEach((child) => {
      if (matches(child, sel)) { out.push(child); }
      walk(child);
    });
  };
  walk(root);
  return out;
}

function parseHTML(html) {
  const root = makeElement("body");
  const stack = [root];
  const tokens = String(html).replace(/<!--[\s\S]*?-->/g, "").split(/(<[^>]+>)/).filter((s) => s !== "");
  for (const token of tokens) {
    if (token.charAt(0) !== "<") {
      if (token.trim()) { stack[stack.length - 1]._text += token; }
      continue;
    }
    const close = token.match(/^<\s*\/\s*([\w-]+)/);
    if (close) {
      for (let i = stack.length - 1; i > 0; i--) {
        if (stack[i].tagName === close[1].toUpperCase()) { stack.length = i; break; }
      }
      continue;
    }
    const open = token.match(/^<\s*([\w-]+)([\s\S]*?)\/?>$/);
    if (!open) { continue; }
    const el = makeElement(open[1]);
    const attrRe = /([\w:-]+)(?:\s*=\s*"([^"]*)")?/g;
    let m;
    while ((m = attrRe.exec(open[2])) !== null) { el.attrs[m[1]] = m[2] === undefined ? "" : m[2]; }
    stack[stack.length - 1].appendChild(el);
    if (!VOID_TAGS.has(open[1].toLowerCase())) { stack.push(el); }
  }
  return root;
}

// ===========================================================================
// 场景 1：index.html 结构 + 菜单接线
// ===========================================================================
console.log("[1] index.html 顶部菜单栏结构");
const dom = parseHTML(INDEX_HTML);
const header = dom.querySelector("header");
check("header 存在且包含菜单栏 nav#menu-bar", () => {
  assert.ok(header, "缺少 header");
  assert.ok(header.querySelector("#menu-bar"), "缺少 #menu-bar");
  assert.equal(header.querySelector("#menu-bar").tagName, "NAV");
});
check("菜单栏含 文件/视图/帮助 三个下拉", () => {
  const roots = header.querySelectorAll("#menu-bar .menu-root");
  assert.equal(roots.length, 3, "菜单数量应为 3，实际 " + roots.length);
  const labels = roots.map((r) => r.querySelector(".menu-btn").textContent.trim());
  assert.deepEqual(labels, ["文件", "视图", "帮助"]);
});
check("状态簇在右侧且含连接/轮次/发送状态与主题图标", () => {
  const status = header.querySelector("#header-status");
  assert.ok(status, "缺少 #header-status");
  assert.ok(status.querySelector("#connection-status"), "缺少 #connection-status");
  assert.ok(status.querySelector("#turn-status"), "缺少 #turn-status");
  assert.ok(status.querySelector("#send-status"), "缺少 #send-status");
  assert.ok(status.querySelector("#theme-toggle"), "缺少 #theme-toggle");
  // 状态簇不再出现在左侧菜单栏内（已从左侧移出）
  assert.equal(header.querySelector("#menu-bar").querySelector("#connection-status"), null, "连接状态仍在菜单栏内");
  assert.equal(header.querySelector("#menu-bar").querySelector("#theme-toggle"), null, "主题图标仍在菜单栏内");
});
check("每个菜单动作都指向真实存在的目标控件", () => {
  const targets = {
    "session-new": "#sessions-new-btn",
    "session-refresh": "#sessions-refresh-btn",
    "sidebar-toggle": "#sidebar-toggle",
    "theme-toggle": "#theme-toggle",
    "tab-main": "#tab-main-btn",
    "tab-log": "#tab-log-btn",
    "tab-debug": "#tab-debug-btn",
    "tab-about": "#tab-about-btn",
    "tab-cache": "#tab-cache-btn",
    "tab-analysis": "#tab-analysis-btn",
    "shortcut-help": "#shortcut-help",
    // 浮动 composer 面板的折叠开关：菜单动作转发到 js/composer.js 的
    // toggleComposerPanel()（而不是点击某个控件），面板本身即目标。
    "composer-toggle": "#composer-panel",
  };
  const items = header.querySelectorAll("#menu-bar .menu-item");
  assert.ok(items.length >= 12, "菜单项过少: " + items.length);
  items.forEach((item) => {
    const action = item.getAttribute("data-menu-action");
    assert.ok(action, "菜单项缺少 data-menu-action");
    if (action.indexOf("export-") === 0) { return; } // 导出项由场景 3 验证
    assert.ok(targets[action], "未登记的菜单动作: " + action);
    assert.ok(dom.querySelector(targets[action]), "动作 " + action + " 的目标 " + targets[action] + " 不存在");
  });
  ["export-full", "export-body", "export-tools", "export-trace"].forEach((action) => {
    assert.ok(items.some((i) => i.getAttribute("data-menu-action") === action), "缺少导出项 " + action);
  });
});

// ===========================================================================
// 场景 2：菜单交互（真实 DOM + 动态 import js/menu.js）
// ===========================================================================
const stubDocument = {
  documentElement: makeElement("html"),
  body: makeElement("body"),
  activeElement: null,
  listeners: {},
  getElementById(id) { return queryAllIn(this.body, "#" + id)[0] || null; },
  querySelector(sel) { return this.body.querySelector(sel); },
  querySelectorAll(sel) { return this.body.querySelectorAll(sel); },
  createElement(tag) { const el = makeElement(tag); if (String(tag).toLowerCase() === "a") { downloads.push(el); } return el; },
  addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
  removeEventListener(type, fn) { this.listeners[type] = (this.listeners[type] || []).filter((f) => f !== fn); },
  dispatch(type, ev) { (this.listeners[type] || []).forEach((fn) => fn(ev || { type: type, stopPropagation() {} })); },
};

function installDOM() {
  stubDocument.body = dom;
  globalThis.document = stubDocument;
  globalThis.window = globalThis;
  globalThis.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };
  globalThis.sessionStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };
}

const downloads = [];
const fetchCalls = [];
let fetchMode = "ok";
installDOM();
globalThis.URL = {
  createObjectURL: () => "blob:stub",
  revokeObjectURL: () => {},
};
globalThis.setTimeout = globalThis.setTimeout;
globalThis.fetch = (url, opts) => {
  fetchCalls.push({ url: url, opts: opts });
  if (fetchMode === "fail") { return Promise.resolve({ ok: false, status: 500, headers: { get: () => null } }); }
  return Promise.resolve({
    ok: true,
    status: 200,
    headers: { get: (name) => (name.toLowerCase() === "content-disposition" ? 'attachment; filename="sess_20260101_000000_full.json"; filename*=UTF-8\'\'sess_20260101_000000_full.json' : (name.toLowerCase() === "x-aicli-export-messages" ? "42" : null)) },
    blob: () => Promise.resolve({ size: 3 }),
  });
};

const menu = await import(pathToFileURL(path.join(WEB_DIR, "js", "menu.js")).href);
menu.initMenu();

const fileRoot = header.querySelectorAll("#menu-bar .menu-root")[0];
const viewRoot = header.querySelectorAll("#menu-bar .menu-root")[1];
const fileBtn = fileRoot.querySelector(".menu-btn");
const viewBtn = viewRoot.querySelector(".menu-btn");

console.log("[2] 菜单开合");
check("点击菜单按钮展开，aria-expanded 同步", () => {
  fileBtn.click();
  assert.ok(fileRoot.classList.contains("open"), "未展开");
  assert.equal(fileBtn.getAttribute("aria-expanded"), "true");
});
check("展开另一个菜单时前一个自动收起", () => {
  viewBtn.click();
  assert.ok(viewRoot.classList.contains("open"), "视图菜单未展开");
  assert.ok(!fileRoot.classList.contains("open"), "文件菜单未收起");
  assert.equal(fileBtn.getAttribute("aria-expanded"), "false");
});
check("再次点击同一按钮收起", () => {
  viewBtn.click();
  assert.ok(!viewRoot.classList.contains("open"), "未收起");
});
check("点击页面其它位置关闭菜单", () => {
  fileBtn.click();
  assert.ok(fileRoot.classList.contains("open"));
  stubDocument.dispatch("click", { type: "click", stopPropagation() {} });
  assert.ok(!fileRoot.classList.contains("open"), "外部点击未关闭");
});
check("Esc 关闭菜单并把焦点还给菜单按钮", () => {
  fileBtn.click();
  stubDocument.activeElement = null;
  stubDocument.dispatch("keydown", { key: "Escape" });
  assert.ok(!fileRoot.classList.contains("open"), "Esc 未关闭");
  assert.equal(stubDocument.activeElement, fileBtn, "焦点未回到菜单按钮");
});

console.log("[3] 菜单项转发既有控件");
check("新建会话项点击 #sessions-new-btn", () => {
  const btn = dom.querySelector("#sessions-new-btn");
  let hit = 0;
  btn.addEventListener("click", () => { hit++; });
  fileRoot.querySelectorAll(".menu-item").find((i) => i.getAttribute("data-menu-action") === "session-new").click();
  assert.equal(hit, 1);
  assert.ok(!fileRoot.classList.contains("open"), "动作后菜单未收起");
});
check("主题切换项点击 #theme-toggle", () => {
  const btn = dom.querySelector("#theme-toggle");
  let hit = 0;
  btn.addEventListener("click", () => { hit++; });
  viewRoot.querySelectorAll(".menu-item").find((i) => i.getAttribute("data-menu-action") === "theme-toggle").click();
  assert.equal(hit, 1);
});
check("页签项点击对应页签按钮", () => {
  const btn = dom.querySelector("#tab-debug-btn");
  let hit = 0;
  btn.addEventListener("click", () => { hit++; });
  viewRoot.querySelectorAll(".menu-item").find((i) => i.getAttribute("data-menu-action") === "tab-debug").click();
  assert.equal(hit, 1);
});

console.log("[4] 导出下载");
await checkAsync("导出项请求 /web/api/export?format=… 并按 Content-Disposition 命名下载", async () => {
  const item = fileRoot.querySelectorAll(".menu-item").find((i) => i.getAttribute("data-menu-action") === "export-trace");
  item.click();
  await new Promise((r) => setTimeout(r, 20));
  assert.equal(fetchCalls.length, 1, "应发出一次导出请求");
  assert.ok(fetchCalls[0].url.indexOf("/web/api/export?format=trace") === 0, "请求地址错误: " + fetchCalls[0].url);
  assert.equal(downloads.length, 1, "应触发一次下载");
  assert.equal(downloads[0].download, "sess_20260101_000000_full.json", "下载文件名未取服务端 Content-Disposition");
  assert.equal(downloads[0].href, "blob:stub");
});
await checkAsync("导出失败（HTTP 500）不产生下载且不抛出", async () => {
  fetchMode = "fail";
  fetchCalls.length = 0;
  downloads.length = 0;
  const item = fileRoot.querySelectorAll(".menu-item").find((i) => i.getAttribute("data-menu-action") === "export-full");
  item.click();
  await new Promise((r) => setTimeout(r, 20));
  assert.equal(fetchCalls.length, 1);
  assert.equal(downloads.length, 0, "失败时不应产生下载");
  fetchMode = "ok";
});

// ===========================================================================
// 场景 5：app.js 入口静态检查（DOM stub + 动态 import）
// ===========================================================================
console.log("[5] app.js 入口静态检查");
const proxyElement = new Proxy(function () {}, {
  get(target, prop) {
    if (prop === "then") { return undefined; }
    if (prop === "nodeType") { return 1; }
    if (prop === "length") { return 0; }
    if (prop === Symbol.iterator) { return function* () {}; }
    if (prop === Symbol.toPrimitive) { return () => ""; }
    if (prop === "toString") { return () => "[stub]"; }
    if (prop in target) { return target[prop]; }
    return proxyElement;
  },
  set() { return true; },
  apply() { return proxyElement; },
});
const proxyDocument = new Proxy(function () {}, {
  get(target, prop) {
    if (prop === "then") { return undefined; }
    if (prop === "getElementById" || prop === "createElement") { return () => proxyElement; }
    if (prop === "querySelector" || prop === "querySelectorAll") { return () => proxyElement; }
    if (prop === "addEventListener" || prop === "removeEventListener") { return () => {}; }
    if (prop in target) { return target[prop]; }
    return proxyElement;
  },
  apply() { return proxyElement; },
});
globalThis.document = proxyDocument;
globalThis.addEventListener = () => {};
globalThis.removeEventListener = () => {};
globalThis.EventSource = function () { return proxyElement; };
globalThis.fetch = () => new Promise(() => {}); // 挂起：只验证模块图与初始化不抛错
let importError = null;
try {
  await import(pathToFileURL(path.join(WEB_DIR, "app.js")).href);
} catch (err) {
  importError = err;
}
check("app.js 动态 import 无模块图/语法错误", () => {
  assert.equal(importError, null, importError && importError.stack);
});
check("app.js 已注册菜单模块（initMenu 调用）", () => {
  const src = fs.readFileSync(path.join(WEB_DIR, "app.js"), "utf8");
  assert.ok(/import\s*\{[^}]*initMenu[^}]*\}\s*from\s*"\.\/js\/menu\.js"/.test(src), "缺少 initMenu import");
  assert.ok(/initMenu\(\);/.test(src), "缺少 initMenu() 调用");
});

// ===========================================================================
// 场景 6：style.css 主题变量完整性
//   未定义的自定义属性会让声明在 computed-value 阶段失效（背景退化为 transparent）
// ===========================================================================
console.log("[6] style.css 主题变量与快捷键面板");
const CSS = fs.readFileSync(path.join(WEB_DIR, "style.css"), "utf8");
const themeScopes = (() => {
  const darkStart = CSS.indexOf(":root {");
  const lightStart = CSS.indexOf(":root.light {");
  const autoStart = CSS.indexOf("@media (prefers-color-scheme: light)");
  return {
    dark: CSS.slice(darkStart, lightStart),
    light: CSS.slice(lightStart, autoStart),
    auto: CSS.slice(autoStart, CSS.indexOf("}\n", autoStart)),
  };
})();
const definedTokens = new Set(Array.from(CSS.matchAll(/(--[a-zA-Z0-9-]+)\s*:/g)).map((m) => m[1]));

check("所有不带 fallback 的 var(--token) 都有定义", () => {
  const missing = [];
  for (const m of CSS.matchAll(/var\(\s*(--[a-zA-Z0-9-]+)\s*([,)])/g)) {
    if (m[2] === ",") { continue; } // 自带 fallback，允许缺省
    if (!definedTokens.has(m[1])) {
      missing.push(m[1] + " (L" + CSS.slice(0, m.index).split("\n").length + ")");
    }
  }
  assert.equal(missing.length, 0, "未定义的 CSS 变量: " + missing.join(", "));
});

check("快捷键面板背景为不透明语义变量，且三种主题下均已定义", () => {
  const panelStart = CSS.indexOf("#shortcut-help .shortcut-panel");
  const overlay = CSS.slice(CSS.indexOf("#shortcut-help .shortcut-overlay"), panelStart);
  const panel = CSS.slice(panelStart, CSS.indexOf("#shortcut-help h3"));
  const close = CSS.slice(CSS.indexOf("#shortcut-help .shortcut-close"), CSS.indexOf("#shortcut-help .shortcut-close:hover"));
  assert.ok(/background:\s*rgba\(/.test(overlay), "遮罩应保留半透明底色");
  const bg = panel.match(/background(?:-color)?:\s*([^;]+);/);
  assert.ok(bg, "面板缺少 background 声明");
  assert.ok(!/transparent/i.test(bg[1]), "面板背景不应为 transparent");
  const tokens = new Set();
  [panel, close].forEach((block) => {
    for (const m of block.matchAll(/var\(\s*(--[a-zA-Z0-9-]+)/g)) { tokens.add(m[1]); }
  });
  assert.ok(tokens.has("--modalBg"), "面板背景应使用 --modalBg 语义变量");
  const problems = [];
  tokens.forEach((t) => {
    ["dark", "light", "auto"].forEach((scope) => {
      if (!new RegExp(t + "\\s*:").test(themeScopes[scope])) { problems.push(t + " 未在 " + scope + " 主题定义"); }
    });
  });
  ["dark", "light", "auto"].forEach((scope) => {
    const v = (themeScopes[scope].match(/--modalBg\s*:\s*([^;]+);/) || [])[1];
    if (!v || /transparent/i.test(v)) { problems.push("--modalBg 在 " + scope + " 主题不是不透明色"); }
  });
  assert.equal(problems.length, 0, problems.join("; "));
});

async function checkAsync(name, fn) {
  try {
    await fn();
    console.log("  ok  - " + name);
  } catch (err) {
    failures++;
    console.log("  FAIL- " + name + "\n        " + (err && err.message ? err.message : err));
  }
}

console.log("");
console.log(failures === 0 ? "全部通过" : failures + " 项失败");
process.exit(failures === 0 ? 0 : 1);
