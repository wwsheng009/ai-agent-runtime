// 行为验证：aicli micro web client composer 面板（无需浏览器）。
// 运行：node scripts/verify-micro-web-composer.mjs（仓库根目录）
// 覆盖：
//   1. index.html 结构：面板是 #tab-main（「对话」页签）内、排在 #conversation 之后的
//      常规流一行（= 对话页底部），输入区 / 配置栏 / 动态状态条都在面板内
//   2. 面板必需元素齐全（拖动把手 / 折叠 / 复位 + 既有输入与选择器 id 不变）；
//      首行合并（2026-09）：动态状态条在 #composer-header 内，「输入」标题与
//      #composer-summary 配置摘要已移除
//   3. 「视图」菜单与快捷键表都提供折叠入口，且转发到同一实现（无第二份折叠逻辑）
//   4. style.css：让位靠 flex 常规流（#conversation flex:1 + min-height:0 + overflow:auto
//      自己收缩）——没有 --composer-reserve / body padding 之类的占位机制；自由位置改
//      fixed 并清掉停靠态的 margin；z-index 低于模态框
//   5. js/composer.js 行为（DOM stub + 动态 import）：停靠 → 拖动 → 夹取 → 键盘微调 →
//      复位 → 折叠 / 展开 → Ctrl+J → localStorage 记忆；且全程不写页面级 CSS 变量、不搬 DOM
import assert from "node:assert";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_DIR = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web");
const INDEX_HTML = fs.readFileSync(path.join(WEB_DIR, "index.html"), "utf8");
const STYLE_CSS = fs.readFileSync(path.join(WEB_DIR, "style.css"), "utf8");
const APP_JS = fs.readFileSync(path.join(WEB_DIR, "app.js"), "utf8");
const MENU_JS = fs.readFileSync(path.join(WEB_DIR, "js", "menu.js"), "utf8");
const COMPOSER_JS = fs.readFileSync(path.join(WEB_DIR, "js", "composer.js"), "utf8");
const CHAT_JS = fs.readFileSync(path.join(WEB_DIR, "js", "chat.js"), "utf8");
const RUNTIME_JS = fs.readFileSync(path.join(WEB_DIR, "js", "runtime.js"), "utf8");

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
async function checkAsync(name, fn) {
  try {
    await fn();
    console.log("  ok  - " + name);
  } catch (err) {
    failures++;
    console.log("  FAIL- " + name + "\n        " + (err && err.message ? err.message : err));
  }
}

// ===========================================================================
// 场景 1：index.html 结构（面板嵌在 #tab-main 底部、参与常规流）
// ===========================================================================
const VOID_TAGS = new Set(["meta", "link", "br", "img", "input", "hr", "source"]);

// ancestorsOf 返回 id 元素在文档里的祖先链（按 class/id/标签名标注），找不到返回 null。
function ancestorsOf(html, id) {
  const tokens = html.replace(/<!--[\s\S]*?-->/g, "").match(/<[^>]+>/g) || [];
  const stack = [];
  for (const token of tokens) {
    const close = token.match(/^<\s*\/\s*([\w-]+)/);
    if (close) {
      for (let i = stack.length - 1; i >= 0; i--) {
        if (stack[i].tag === close[1].toUpperCase()) { stack.length = i; break; }
      }
      continue;
    }
    const open = token.match(/^<\s*([\w-]+)/);
    if (!open) { continue; }
    const idMatch = token.match(/\bid="([\w-]+)"/);
    const classMatch = token.match(/\bclass="([^"]*)"/);
    if (idMatch && idMatch[1] === id) { return stack.map((n) => n.label); }
    if (VOID_TAGS.has(open[1].toLowerCase()) || /\/>$/.test(token)) { continue; }
    stack.push({
      tag: open[1].toUpperCase(),
      label: idMatch ? "#" + idMatch[1]
        : (classMatch ? "." + classMatch[1].split(/\s+/)[0] : open[1].toLowerCase()),
    });
  }
  return null;
}

console.log("[1] index.html 浮动面板结构");
const panelChain = ancestorsOf(INDEX_HTML, "composer-panel");
check("面板存在且是 #tab-main 的常规流子节点（不再挂在 .layout 下做浮层）", () => {
  assert.ok(panelChain, "index.html 缺少 #composer-panel");
  assert.deepEqual(panelChain, ["html", "body", ".layout", "#main-col", "#tab-main"],
    "面板祖先链应为 [html, body, .layout, #main-col, #tab-main]（对话页底部内嵌），实际 " + JSON.stringify(panelChain));
});
check("面板排在 #conversation 之后（flex 列的最后一行 = 对话页底部）", () => {
  const conv = INDEX_HTML.indexOf('id="conversation"');
  const panel = INDEX_HTML.indexOf('id="composer-panel"');
  assert.ok(conv > 0, "index.html 缺少 #conversation");
  assert.ok(panel > conv, "#composer-panel 必须排在 #conversation 之后，否则不是底部一行（实际 " + conv + " / " + panel + "）");
  assert.ok(panel < INDEX_HTML.indexOf('id="tab-skills"'), "#composer-panel 必须留在 #tab-main 内");
});
check("输入区 / 配置栏 / 动态状态条都在面板内，且面板在 #tab-main 内（随「对话」页签显隐）", () => {
  ["input-row", "cfg-bar", "dynamic-status", "prompt", "send-btn"].forEach((id) => {
    const chain = ancestorsOf(INDEX_HTML, id);
    assert.ok(chain, "缺少 #" + id);
    assert.ok(chain.indexOf("#composer-panel") >= 0, "#" + id + " 不在 #composer-panel 内: " + JSON.stringify(chain));
    assert.ok(chain.indexOf("#tab-main") >= 0, "#" + id + " 不在 #tab-main 内（面板应嵌在对话页底部）");
  });
});
check("面板必需元素齐全（把手 / 折叠 / 复位 / 面板体）", () => {
  ["composer-header", "composer-grip", "composer-collapse-btn", "composer-reset-btn",
    "composer-body"].forEach((id) => {
    assert.ok(INDEX_HTML.indexOf('id="' + id + '"') >= 0, "缺少 #" + id);
  });
  assert.ok(/id="composer-panel"[^>]*role="region"/.test(INDEX_HTML), "面板缺少 role=region");
  assert.ok(/id="composer-collapse-btn"[^>]*aria-expanded="true"/.test(INDEX_HTML), "折叠按钮缺少 aria-expanded");
  assert.ok(/id="composer-collapse-btn"[^>]*aria-controls="composer-body"/.test(INDEX_HTML), "折叠按钮缺少 aria-controls");
  assert.ok(/id="composer-grip"[^>]*type="button"/.test(INDEX_HTML), "把手应是可聚焦按钮");
});
check("既有选择器 id 全部保留（runtime.js 按 id 取元素，改结构不改契约）", () => {
  ["cfg-toggle", "cfg-toggle-value", "cfg-provider", "cfg-model", "cfg-model-options",
    "cfg-model-toggle", "cfg-model-count", "cfg-model-popup", "cfg-reasoning",
    "cfg-status"].forEach((id) => {
    assert.ok(INDEX_HTML.indexOf('id="' + id + '"') >= 0, "缺少 #" + id);
  });
});
// 面板底部曾再显示一份「provider · model · reasoning」，与三个选择框完全重复（用户要求去掉）。
// 2026-09 进一步要求「两行合一」：动态状态条进入标题行，原首行「输入」标题与配置摘要
// #composer-summary 一并移除；这份文案只剩窄屏 ⚙ 触发按钮一处承载。
check("配置文案不重复：底部 #cfg-current 已移除", () => {
  assert.ok(INDEX_HTML.indexOf('id="cfg-current"') < 0, "index.html 仍存在 #cfg-current（底部重复文案）");
  assert.ok(!/cfg-current/.test(STYLE_CSS), "style.css 仍存在 .cfg-current 规则");
  assert.ok(!/getElementById\("cfg-current"\)/.test(RUNTIME_JS), "runtime.js 仍在取 #cfg-current");
});
check("首行合并：动态状态条进入 #composer-header，原「输入 + 配置摘要」已移除", () => {
  const chain = ancestorsOf(INDEX_HTML, "dynamic-status");
  assert.ok(chain, "index.html 缺少 #dynamic-status");
  assert.ok(chain.indexOf("#composer-header") >= 0,
    "#dynamic-status 必须在 #composer-header 内（两行合一），实际祖先链 " + JSON.stringify(chain));
  assert.ok(INDEX_HTML.indexOf('id="composer-summary"') < 0,
    "index.html 仍存在 #composer-summary（用户要求取消 provider/model/reasoning 文案）");
  assert.ok(INDEX_HTML.indexOf('class="composer-title"') < 0, "index.html 仍存在「输入」标题");
  assert.ok(!/composer-summary/.test(STYLE_CSS), "style.css 仍存在 .composer-summary 规则");
  assert.ok(!/composer-title/.test(STYLE_CSS), "style.css 仍存在 .composer-title 规则");
  assert.ok(!/getElementById\("composer-summary"\)/.test(RUNTIME_JS) && !/els\.summary/.test(RUNTIME_JS),
    "runtime.js 仍在取 #composer-summary");
  assert.ok(/els\.toggleValue/.test(RUNTIME_JS), "窄屏 ⚙ 按钮的配置文案不应一起被删掉");
});

console.log("[2] 折叠入口（菜单 / 快捷键表 / 模块接线）");
check("「视图」菜单提供 composer-toggle，且面板存在", () => {
  assert.ok(/data-menu-action="composer-toggle"/.test(INDEX_HTML), "菜单缺少 composer-toggle 项");
  assert.ok(INDEX_HTML.indexOf('id="composer-panel"') >= 0, "菜单动作缺少目标 #composer-panel");
});
check("快捷键表登记 Ctrl+J", () => {
  assert.ok(/<td>Ctrl\+J<\/td>/.test(INDEX_HTML), "快捷键表缺少 Ctrl+J");
});
check("菜单动作转发到 composer.js 的同一入口（无第二份折叠逻辑）", () => {
  assert.ok(/import\s*\{[^}]*toggleComposerPanel[^}]*\}\s*from\s*"\.\/composer\.js"/.test(MENU_JS), "menu.js 未引入 toggleComposerPanel");
  assert.ok(/case "composer-toggle":\s*toggleComposerPanel\(\);/.test(MENU_JS), "menu.js 未把 composer-toggle 接到 toggleComposerPanel()");
  assert.ok(/export function toggleComposerPanel\(\)/.test(COMPOSER_JS), "composer.js 未导出 toggleComposerPanel()");
  assert.ok(/if \(ev\.key !== "j" && ev\.key !== "J"\)/.test(COMPOSER_JS), "composer.js 缺少 Ctrl+J 处理");
});
check("app.js 已接线 initComposerPanel()", () => {
  assert.ok(/import\s*\{[^}]*initComposerPanel[^}]*\}\s*from\s*"\.\/js\/composer\.js"/.test(APP_JS), "app.js 未 import initComposerPanel");
  assert.ok(/initComposerPanel\(\);/.test(APP_JS), "app.js 未调用 initComposerPanel()");
});

// ===========================================================================
// 场景 3：style.css 几何（常规流内嵌 + 自由浮层）
// ===========================================================================
console.log("[3] style.css 几何（常规流内嵌 + 自由浮层）");
const panelBlock = STYLE_CSS.slice(STYLE_CSS.indexOf("#composer-panel {"), STYLE_CSS.indexOf("#composer-panel:focus-within"));
check("#composer-panel 是 #tab-main 常规流里的底部一行（不收缩、水平居中）", () => {
  assert.ok(/position:\s*relative/.test(panelBlock), "停靠态应是常规流定位（position:relative）");
  assert.ok(/flex:\s*0 0 auto/.test(panelBlock), "缺少 flex:0 0 auto（面板高度不应被压缩）");
  assert.ok(/margin:\s*0 auto;/.test(panelBlock), "缺少「水平居中、底部不留外边距」的 margin: 0 auto");
  assert.ok(!/margin:\s*0 auto 8px/.test(panelBlock), "面板底部重新出现 8px 外边距（会与状态栏自己的 margin-top 叠成 16px 空档）");
  assert.ok(/width:\s*min\(920px/.test(panelBlock), "面板宽度契约丢失（min(920px, …)）");
  assert.ok(!/position:\s*absolute/.test(panelBlock), "停靠态不应再用 absolute（浮层会盖住消息）");
});
check("自由位置改回 fixed 并清掉停靠态 margin（拖动跟手、落点不被顶偏）", () => {
  const free = STYLE_CSS.slice(STYLE_CSS.indexOf('#composer-panel[data-composer-mode="free"]'));
  const rule = free.slice(0, free.indexOf("}"));
  assert.ok(/position:\s*fixed/.test(rule), "自由位置未改回 position:fixed（拖动坐标是视口坐标）");
  assert.ok(/left:\s*0/.test(rule) && /top:\s*0/.test(rule), "自由模式未接管 left/top");
  assert.ok(/margin:\s*0/.test(rule), "自由模式未清 margin（停靠态的 auto/8px 会顶偏落点）");
  assert.ok(/bottom:\s*auto/.test(rule), "自由模式未清 bottom");
});
// 让位是**结构性的**：面板是 #tab-main 常规流里的一行，#conversation 是 flex:1 的滚动区，
// 自动收缩到面板上沿。旧实现用 --composer-reserve + body padding-bottom 做占位（已删除），
// 这里既断言新机制存在，也断言占位机制不会再回来。
check("让位靠 flex 常规流：#conversation 自己收缩，没有占位变量 / body 留白", () => {
  const convRule = (STYLE_CSS.match(/#conversation \{[^}]*\}/) || [""])[0];
  assert.ok(/flex:\s*1/.test(convRule) && /min-height:\s*0/.test(convRule) && /overflow:\s*auto/.test(convRule),
    "#conversation 必须是 flex:1 + min-height:0 + overflow:auto 的可收缩滚动区，实际: " + convRule);
  // 只看代码本体：注释里保留「旧机制已移除」的说明是有意为之，不能因此误报。
  const styleCode = STYLE_CSS.replace(/\/\*[\s\S]*?\*\//g, "");
  const composerCode = COMPOSER_JS.replace(/^\s*\/\/.*$/gm, "");
  assert.ok(!/--composer-reserve/.test(styleCode), "style.css 仍存在 --composer-reserve（占位机制已废弃）");
  assert.ok(!/--composer-reserve/.test(composerCode), "composer.js 仍在写 --composer-reserve");
  assert.ok(!/body\s*\{[^}]*padding-bottom[^}]*composer/i.test(styleCode), "body 仍为面板留出 padding-bottom");
  // #footer（状态栏）自身规则里不得出现 composer 相关耦合（只取这一条规则，别扫到邻居注释）
  const footerRule = (styleCode.match(/#footer \{[^}]*\}/) || [""])[0];
  assert.ok(footerRule.length > 0, "style.css 里找不到 #footer 规则");
  assert.ok(!/composer/i.test(footerRule), "#footer 规则里出现 composer 耦合: " + footerRule);
});
check("面板 z-index 低于模态框（审批 / 会话切换浮层优先）", () => {
  const panelZ = Number((panelBlock.match(/z-index:\s*(\d+)/) || [])[1]);
  assert.ok(panelZ > 0, "面板缺少 z-index");
  const modalZ = Array.from(STYLE_CSS.matchAll(/z-index:\s*(\d+)/g)).map((m) => Number(m[1]));
  const maxModal = Math.max.apply(null, modalZ.filter((z) => z >= 999));
  assert.ok(maxModal > panelZ, "面板 z-index(" + panelZ + ") 不应高于模态框(" + maxModal + ")");
});
check("折叠态：只收起正文（输入区 + 配置栏），动态状态条随标题行保留", () => {
  assert.ok(/#composer-panel\.composer-collapsed \.composer-body/.test(STYLE_CSS), "缺少折叠态规则");
  assert.ok(!/composer-collapsed #dynamic-status/.test(STYLE_CSS),
    "折叠态不应再隐藏动态状态条（它已是标题行的一部分，隐藏并不省高度）");
});
// 折叠只收正文，卡片自身的内边距沿用展开态（用户口径：卡片高度不缩，只收与聊天区的间距）。
check("折叠态不缩卡片内边距（只收正文，卡片高度沿用展开态口径）", () => {
  assert.ok(!/#composer-panel\.composer-collapsed \{[^}]*padding/.test(STYLE_CSS),
    "折叠态不应再覆盖面板内边距（会改动卡片高度）");
  const headerRule = (STYLE_CSS.match(/#composer-panel\.composer-collapsed \.composer-header \{[^}]*\}/) || [""])[0];
  assert.ok(/padding-bottom:\s*2px/.test(headerRule), "折叠态标题行应保留原有 padding-bottom: 2px: " + headerRule);
});
// 面板与聊天区的间距挂在 #conversation 的下边距上（基础 8px）。面板折叠成一条后这段
// 间距必须跟着收（8px → 2px），否则细条上方会留出一段与体积不相称的空档。
check("折叠态收紧面板与聊天区的间距（#conversation 下边距随折叠 class 收缩）", () => {
  const rule = (STYLE_CSS.match(/#tab-main:has\(#composer-panel\.composer-collapsed\) #conversation \{[^}]*\}/) || [""])[0];
  assert.ok(rule.length > 0, "缺少 :has() 规则：折叠态应把 #conversation 下边距收紧（面板缩小后不应保留 8px 空档）");
  assert.ok(/margin-bottom:\s*2px/.test(rule), "折叠态 #conversation 下边距未收紧到 2px: " + rule);
  const convRule = (STYLE_CSS.match(/#conversation \{[^}]*\}/) || [""])[0];
  assert.ok(/margin:\s*8px 0;/.test(convRule), "展开态 #conversation 的 8px 间距契约被改动: " + convRule);
});
// 「⇲ 复位」只在面板离开原位（自由拖动后）时显示：停靠态它无事可做。
// 显隐跟 data-composer-mode 走（纯 CSS），避免又多一条 JS 状态。
check("⇲ 复位按钮只在自由位置显示（停靠态隐藏）", () => {
  assert.ok(INDEX_HTML.indexOf('id="composer-reset-btn"') >= 0, "复位按钮被误删（应保留、按需显示）");
  assert.ok(/#composer-panel\[data-composer-mode="dock"\] #composer-reset-btn \{ display: none; \}/.test(STYLE_CSS),
    "缺少「停靠态隐藏复位按钮」规则");
  assert.ok(!/resetBtn\.hidden/.test(COMPOSER_JS), "复位按钮显隐应交由 CSS，不要在 JS 里再判一次");
});
// 面板参与常规流后，「最新」按钮只需贴信息流下沿（#conversation 天然止于面板上沿），
// 不再需要按浮层重叠躲让；面板折叠 / 拖动 / 复位由 ResizeObserver 观察 #conversation 触发重算。
check("「最新」按钮按信息流下沿锚定，且随 #conversation 尺寸变化重算", () => {
  assert.ok(/conversationEl\.offsetTop/.test(CHAT_JS) && /conversationEl\.offsetHeight/.test(CHAT_JS),
    "chat.js 未按信息流下沿（offsetTop + offsetHeight）锚定按钮");
  assert.ok(!/data-composer-mode/.test(CHAT_JS), "chat.js 不应再按 composer 停靠态做重叠躲让（面板已让位）");
  assert.ok(/bottomObserver\.observe\(conversationEl\)/.test(CHAT_JS), "ResizeObserver 未观察 #conversation");
});
check("模式切换不改 DOM：结构固定在 index.html，composer.js 只切属性 / 样式", () => {
  const code = COMPOSER_JS.replace(/^\s*\/\/.*$/gm, "");
  assert.ok(!/appendChild|insertBefore|removeChild|parentNode|replaceChild/.test(code),
    "composer.js 不应搬移 DOM 节点（停靠 / 自由只靠 data-composer-mode）");
});

// ===========================================================================
// 场景 4：js/composer.js 行为（DOM stub + 动态 import）
// ===========================================================================
console.log("[4] js/composer.js 行为");

function classListOf(el) { return String(el.attrs.class || "").split(/\s+/).filter(Boolean); }
function setClassList(el, list) { el.attrs.class = list.join(" "); }

function makeEl(id) {
  const el = {
    id: id,
    attrs: id ? { id: id } : {},
    style: {},
    listeners: {},
    classList: {
      contains: (c) => classListOf(el).indexOf(c) >= 0,
      add: function () {
        const l = classListOf(el);
        Array.prototype.slice.call(arguments).join(" ").split(/\s+/).filter(Boolean).forEach((c) => { if (l.indexOf(c) < 0) { l.push(c); } });
        setClassList(el, l);
      },
      remove: function () {
        const drop = Array.prototype.slice.call(arguments).join(" ").split(/\s+/).filter(Boolean);
        setClassList(el, classListOf(el).filter((c) => drop.indexOf(c) < 0));
      },
      toggle: (c, on) => {
        const has = classListOf(el).indexOf(c) >= 0;
        const want = on === undefined ? !has : !!on;
        if (want && !has) { el.classList.add(c); }
        if (!want && has) { el.classList.remove(c); }
      },
    },
    getAttribute(name) { return name in this.attrs ? this.attrs[name] : null; },
    setAttribute(name, value) { this.attrs[name] = String(value); },
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    dispatch(type, ev) {
      const event = ev || { type: type, preventDefault() {} };
      if (!event.type) { event.type = type; }
      if (typeof event.preventDefault !== "function") { event.preventDefault = function () {}; }
      (this.listeners[type] || []).forEach((fn) => fn(event));
      return event;
    },
    getBoundingClientRect() {
      if (id === "composer-panel") {
        const left = el.style.left === "" || el.style.left === undefined ? DOCK_LEFT : parseFloat(el.style.left);
        const top = el.style.top === "" || el.style.top === undefined ? DOCK_TOP : parseFloat(el.style.top);
        return { left: left, top: top, width: el.offsetWidth, height: el.offsetHeight };
      }
      return { left: 0, top: 0, width: 0, height: 0 };
    },
    get offsetWidth() { return id === "composer-panel" ? 600 : 0; },
    // 折叠态只留标题行：stub 也按 class 给出不同高度，验证占位随之收缩。
    get offsetHeight() { return id === "composer-panel" ? (classListOf(el).indexOf("composer-collapsed") >= 0 ? 34 : 120) : 0; },
  };
  return el;
}

const VIEWPORT = { w: 1000, h: 800 };
const PANEL_W = 600, PANEL_H = 120;
// 停靠几何：面板是 #tab-main 常规流里的最后一行，真实位置由 flex 布局给出。
// stub 里没有布局引擎，按 composer.js panelRect() 的兜底公式模拟：水平居中 + 贴视口底部
// 留 EDGE(8px) 边距（真实浏览器里停靠几何由 CSS 直接给出，不走这条兜底）。
const DOCK_LEFT = (VIEWPORT.w - PANEL_W) / 2;   // 200
const DOCK_TOP = VIEWPORT.h - 8 - PANEL_H;      // 672

const ids = ["composer-panel", "composer-header", "composer-grip", "composer-collapse-btn",
  "composer-reset-btn", "composer-body"];
const elements = {};
ids.forEach((id) => { elements[id] = makeEl(id); });

const store = {};
const cssVars = {};
const bodyEl = makeEl("body");
const rootEl = makeEl("html");
rootEl.style.setProperty = function (name, value) { cssVars[name] = value; };

const stubDocument = {
  body: bodyEl,
  documentElement: rootEl,
  listeners: {},
  getElementById: (id) => elements[id] || null,
  addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
  dispatch(type, ev) { (this.listeners[type] || []).forEach((fn) => fn(ev)); },
};

globalThis.document = stubDocument;
globalThis.localStorage = {
  getItem: (k) => (k in store ? store[k] : null),
  setItem: (k, v) => { store[k] = String(v); },
  removeItem: (k) => { delete store[k]; },
};
globalThis.window = globalThis;
globalThis.innerWidth = VIEWPORT.w;
globalThis.innerHeight = VIEWPORT.h;
globalThis.addEventListener = () => {};

const composer = await import(pathToFileURL(path.join(WEB_DIR, "js", "composer.js")).href);
composer.initComposerPanel();

const panel = elements["composer-panel"];
const grip = elements["composer-grip"];
const collapseBtn = elements["composer-collapse-btn"];

checkAsync; // 行为断言全部同步，保留 checkAsync 供后续扩展

check("初始化：停靠模式，且不写任何页面级 CSS 变量（两层解耦）", () => {
  assert.equal(panel.getAttribute("data-composer-mode"), "dock");
  assert.deepEqual(Object.keys(cssVars), [], "面板不应写页面级 CSS 变量（否则又会耦合布局）");
});

check("拖动：mousedown → mousemove 进入自由模式，落点跟随指针，拖动中锁光标", () => {
  grip.dispatch("mousedown", { button: 0, clientX: 250, clientY: 700 });
  assert.ok(bodyEl.classList.contains("composer-dragging"), "拖动中未加 body.composer-dragging");
  stubDocument.dispatch("mousemove", { clientX: 300, clientY: 640 });
  assert.equal(panel.getAttribute("data-composer-mode"), "free");
  assert.equal(panel.style.left, "250px", "left 未跟随指针（期望 300-50）");
  assert.equal(panel.style.top, "612px", "top 未跟随指针（期望 640-28）");
  assert.deepEqual(Object.keys(cssVars), [], "自由位置同样不应写页面级 CSS 变量");
  stubDocument.dispatch("mouseup", {});
  assert.ok(!bodyEl.classList.contains("composer-dragging"), "松手后未解除 body.composer-dragging");
  assert.ok(/"mode":"free"/.test(store["aicli.web.composer.v1"] || ""), "未落盘自由位置");
});

check("拖动落点夹在视口内（窗口缩小也不会跑到屏幕外）", () => {
  grip.dispatch("mousedown", { button: 0, clientX: 100, clientY: 100 });
  // 指针拖到视口左上角之外很远：两个轴都必须被 EDGE(8px) 夹住
  stubDocument.dispatch("mousemove", { clientX: -500, clientY: -1200 });
  stubDocument.dispatch("mouseup", {});
  assert.equal(panel.style.left, "8px");
  assert.equal(panel.style.top, "8px");
});

check("把手键盘微调：方向键 8px、Shift+方向键 1px、Home 复位停靠", () => {
  grip.dispatch("keydown", { key: "ArrowRight" });
  assert.equal(panel.style.left, "16px", "ArrowRight 应移动 8px");
  grip.dispatch("keydown", { key: "ArrowDown", shiftKey: true });
  assert.equal(panel.style.top, "9px", "Shift+ArrowDown 应移动 1px");
  grip.dispatch("keydown", { key: "Home" });
  assert.equal(panel.getAttribute("data-composer-mode"), "dock");
  assert.equal(panel.style.left, "");
  assert.deepEqual(Object.keys(cssVars), [], "复位不应写页面级 CSS 变量");
});

check("⇲ 复位按钮与把手双击同样回到停靠", () => {
  grip.dispatch("mousedown", { button: 0, clientX: 250, clientY: 700 });
  stubDocument.dispatch("mousemove", { clientX: 400, clientY: 400 });
  stubDocument.dispatch("mouseup", {});
  assert.equal(panel.getAttribute("data-composer-mode"), "free");
  elements["composer-reset-btn"].dispatch("click", {});
  assert.equal(panel.getAttribute("data-composer-mode"), "dock");
});

// 旧实现的回归测试（页面级滚动条会让 100vh 与布局视口高度不等，占位必须补差值）
// 随让位机制一起删除：现在面板不占位，页面有没有滚动条都与它无关。
check("面板状态变化不写页面级 CSS 变量（与状态栏 / 正文完全解耦）", () => {
  rootEl.clientHeight = VIEWPORT.h - 10;   // 即便页面出现根滚动条
  elements["composer-reset-btn"].dispatch("click", {});
  collapseBtn.dispatch("click", {});
  collapseBtn.dispatch("click", {});
  assert.deepEqual(Object.keys(cssVars), [], "面板不应因视口 / 自身状态写任何页面级变量");
  rootEl.clientHeight = VIEWPORT.h;
});

check("折叠：class + aria-expanded 一起收缩，再点恢复", () => {
  collapseBtn.dispatch("click", {});
  assert.ok(panel.classList.contains("composer-collapsed"), "未加折叠 class");
  assert.equal(collapseBtn.getAttribute("aria-expanded"), "false");
  assert.equal(collapseBtn.textContent, "▸");
  collapseBtn.dispatch("click", {});
  assert.ok(!panel.classList.contains("composer-collapsed"), "未展开");
  assert.equal(collapseBtn.getAttribute("aria-expanded"), "true");
  assert.equal(collapseBtn.textContent, "▾");
});

check("Ctrl+J 全局切换折叠（任意页签 / 任意焦点位置）", () => {
  stubDocument.dispatch("keydown", { ctrlKey: true, key: "j", preventDefault() {} });
  assert.ok(panel.classList.contains("composer-collapsed"), "Ctrl+J 未折叠");
  stubDocument.dispatch("keydown", { ctrlKey: true, key: "J", preventDefault() {} });
  assert.ok(!panel.classList.contains("composer-collapsed"), "Ctrl+J 未展开");
  stubDocument.dispatch("keydown", { ctrlKey: true, key: "k", preventDefault() {} });
  assert.ok(!panel.classList.contains("composer-collapsed"), "非 J 组合键不应触发");
});

check("localStorage 记忆：折叠态与自由位置可回放", () => {
  const saved = JSON.parse(store["aicli.web.composer.v1"] || "{}");
  assert.equal(saved.mode, "dock");
  assert.equal(saved.collapsed, false);
  assert.equal(typeof saved.left, "number");
  assert.equal(typeof saved.top, "number");
});

console.log("");
console.log(failures === 0 ? "全部通过" : failures + " 项失败");
process.exit(failures === 0 ? 0 : 1);
