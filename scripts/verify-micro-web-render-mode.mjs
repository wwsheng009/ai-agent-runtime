// 行为验证：aicli micro web client assistant 消息 md|txt 渲染方式切换（默认 md）。
// 运行：node scripts/verify-micro-web-render-mode.mjs（无需浏览器；见 docs/aicli/web-testing.md）
// 覆盖：
//   1. normalizeRenderMode / renderMessageBody：仅 "md" 走 Markdown，其余（含缺省/非法）一律 text
//   2. chatMsgRowHtml("assistant")：右上角 md|txt 控件、默认 data-render-mode="md"、
//      .msg-text 保存转义原文、.msg-md 默认即同步渲染 Markdown（默认即显，不留空）
//   3. 非 assistant 行不带切换控件（user / reasoning / tool 等回归）
//   4. getMessageRenderMode / applyMessageRenderMode：属性 + 按钮 active/aria-pressed 同步、
//      切到 md 同步渲染（re），切回 txt 保留原文与已渲染结果（可反复切换、重复切到 md 不叠加）
//   5. toggleMessageRenderMode + initChat 点击委托：点 md / txt 按钮切到对应渲染方式
//   6. 会话复制（domConversationText）：assistant 行取 .msg-text 原文，不含 md 渲染产物
//      （代码块「复制」按钮文字）
//   7. style.css：显隐由 data-render-mode 驱动（不靠内联样式）；流式气泡固定走 renderMarkdown
import assert from "node:assert";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_ROOT = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web");
const WEB_DIR = path.join(WEB_ROOT, "js");

// ===========================================================================
// 迷你 DOM（极简 HTML 解析）
// 渲染方式切换必须断言真实节点结构（.msg-text / .msg-md / 两个按钮），
// 字符串级 stub 不足以覆盖「惰性渲染 + 按钮状态同步 + 点击委托」。
// 解析器只需覆盖 chatMsgRowHtml / renderMarkdown 产出的标签。
// ===========================================================================

function decodeEntities(s) {
  return String(s)
    .replace(/&lt;/g, "<").replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"').replace(/&#39;/g, "'")
    .replace(/&amp;/g, "&");
}

function classListOf(el) {
  return String(el.attrs["class"] || "").split(/\s+/).filter(Boolean);
}

function setClassList(el, classes) {
  el.attrs["class"] = classes.join(" ");
}

function makeClassList(el) {
  return {
    contains: function (c) { return classListOf(el).indexOf(c) >= 0; },
    add: function () {
      var list = classListOf(el);
      for (var i = 0; i < arguments.length; i++) {
        String(arguments[i]).split(/\s+/).filter(Boolean).forEach(function (c) {
          if (list.indexOf(c) < 0) { list.push(c); }
        });
      }
      setClassList(el, list);
    },
    remove: function () {
      var drop = [];
      for (var i = 0; i < arguments.length; i++) {
        String(arguments[i]).split(/\s+/).filter(Boolean).forEach(function (c) { drop.push(c); });
      }
      setClassList(el, classListOf(el).filter(function (c) { return drop.indexOf(c) < 0; }));
    },
    toggle: function (c, on) {
      if (on === undefined) { this.contains(c) ? this.remove(c) : this.add(c); return; }
      on ? this.add(c) : this.remove(c);
    },
  };
}

// 简单选择器匹配：tag / .class / [attr] / [attr="v"] / 组合
function matchCompound(el, sel) {
  if (!el || el.nodeType !== 1) { return false; }
  var parts = sel.match(/(\.[\w-]+|\[[^\]]+\]|[a-zA-Z][\w-]*)/g) || [];
  if (!parts.length) { return false; }
  for (var i = 0; i < parts.length; i++) {
    var p = parts[i];
    if (p.charAt(0) === ".") {
      if (classListOf(el).indexOf(p.slice(1)) < 0) { return false; }
    } else if (p.charAt(0) === "[") {
      var mm = p.match(/^\[([\w-]+)(?:="([^"]*)")?\]$/);
      if (!mm) { return false; }
      var v = el.attrs[mm[1]];
      if (mm[2] === undefined) { if (v == null) { return false; } }
      else if (v !== mm[2]) { return false; }
    } else if (el.tagName !== p.toUpperCase()) {
      return false;
    }
  }
  return true;
}

function textOf(node) {
  if (!node) { return ""; }
  if (node.nodeType === 3) { return node.text || ""; }
  return (node.children || []).map(textOf).join("");
}

function detach(node) {
  if (!node || !node.parentNode) { return; }
  var siblings = node.parentNode.children;
  var idx = siblings.indexOf(node);
  if (idx >= 0) { siblings.splice(idx, 1); }
  node.parentNode = null;
}

function makeTextNode(text) {
  return { nodeType: 3, text: String(text), children: [], parentNode: null };
}

function makeEl(tag) {
  var el = {
    nodeType: 1,
    tagName: String(tag || "div").toUpperCase(),
    attrs: {},
    children: [],
    parentNode: null,
    style: {},
    listeners: {},
    value: "",
    scrollTop: 0,
    scrollHeight: 0,
    clientHeight: 0,
    offsetHeight: 0,
    focus: function () {},
    remove: function () { detach(this); },
    scrollIntoView: function () {},
    addEventListener: function (type, fn) {
      if (!this.listeners[type]) { this.listeners[type] = []; }
      this.listeners[type].push(fn);
    },
    removeEventListener: function (type, fn) {
      if (!this.listeners[type]) { return; }
      this.listeners[type] = this.listeners[type].filter(function (f) { return f !== fn; });
    },
    dispatch: function (type, ev) {
      (this.listeners[type] || []).forEach(function (fn) { fn(ev || {}); });
    },
    click: function () {
      this.dispatch("click", {
        target: this, currentTarget: this,
        preventDefault: function () {}, stopPropagation: function () {},
      });
    },
    getAttribute: function (n) { return this.attrs[n] != null ? this.attrs[n] : null; },
    setAttribute: function (n, v) { this.attrs[n] = String(v); },
    hasAttribute: function (n) { return this.attrs[n] != null; },
    appendChild: function (child) {
      detach(child);
      child.parentNode = this;
      this.children.push(child);
      return child;
    },
    removeChild: function (child) { detach(child); return child; },
    insertBefore: function (child, ref) {
      detach(child);
      var idx = ref ? this.children.indexOf(ref) : -1;
      if (idx < 0) { this.children.push(child); } else { this.children.splice(idx, 0, child); }
      child.parentNode = this;
      return child;
    },
    querySelectorAll: function (sel) {
      var out = [];
      (function walk(node) {
        (node.children || []).forEach(function (c) {
          if (c.nodeType === 1) { out.push(c); walk(c); }
        });
      })(this);
      return out.filter(function (n) { return matchCompound(n, sel); });
    },
    querySelector: function (sel) { return this.querySelectorAll(sel)[0] || null; },
    closest: function (sel) {
      var node = this;
      while (node) {
        if (matchCompound(node, sel)) { return node; }
        node = node.parentNode;
      }
      return null;
    },
  };
  el.classList = makeClassList(el);
  Object.defineProperty(el, "innerHTML", {
    get: function () { return this.children.map(textOf).join(""); },
    set: function (v) {
      this.children = [];
      parseInto(this, String(v == null ? "" : v));
    },
  });
  Object.defineProperty(el, "textContent", {
    get: function () { return this.children.map(textOf).join(""); },
    set: function (v) { this.children = []; this.appendChild(makeTextNode(v == null ? "" : v)); },
  });
  return el;
}

function parseAttrs(el, attrStr) {
  var re = /([\w-]+)(?:\s*=\s*"([^"]*)")?/g;
  var m;
  while ((m = re.exec(attrStr)) !== null) {
    if (!m[1]) { continue; }
    el.attrs[m[1]] = m[2] !== undefined ? decodeEntities(m[2]) : "";
  }
}

// 极简 HTML 解析：平衡标签栈 + 实体解码。
function parseInto(root, html) {
  var stack = [root];
  var re = /<\/?([a-zA-Z][a-zA-Z0-9-]*)((?:\s+[^<>]*?)?)\/?>/g;
  var last = 0;
  var m;
  function pushText(text) {
    if (!text) { return; }
    var node = makeTextNode(decodeEntities(text));
    var parent = stack[stack.length - 1];
    node.parentNode = parent;
    parent.children.push(node);
  }
  while ((m = re.exec(html)) !== null) {
    pushText(html.slice(last, m.index));
    last = re.lastIndex;
    var raw = m[0];
    if (raw.charAt(1) === "/") {
      for (var i = stack.length - 1; i > 0; i--) {
        if (stack[i].tagName === m[1].toUpperCase()) { stack.length = i; break; }
      }
    } else {
      var el = makeEl(m[1]);
      parseAttrs(el, m[2] || "");
      var parent = stack[stack.length - 1];
      el.parentNode = parent;
      parent.children.push(el);
      if (!/\/>$/.test(raw) && !/^(br|hr|img|input|meta|link)$/i.test(m[1])) { stack.push(el); }
    }
  }
  pushText(html.slice(last));
}

// 把 chatMsgRowHtml 产出的字符串解析成可查询的元素树。
function parseRow(html) {
  var host = makeEl("div");
  host.innerHTML = html;
  return host.children[0];
}

// ---- Global stubs（chat.js 顶层依赖，与 verify-micro-web-tool-output.mjs 同款）----
var elements = {};
function getElement(id) {
  if (elements[id] === undefined) { elements[id] = makeEl("div"); }
  return elements[id];
}

globalThis.document = {
  getElementById: getElement,
  querySelector: function (sel) { return getElement("sel:" + sel); },
  querySelectorAll: function () { return []; },
  createElement: function (tag) { return makeEl(tag); },
  documentElement: makeEl("html"),
  body: {
    appendChild: function (el) { el.parentNode = this; },
    classList: makeClassList({ attrs: {} }),
    style: {},
  },
  addEventListener: function () {},
};

globalThis.localStorage = {
  _data: {},
  getItem: function (k) { return this._data[k] != null ? this._data[k] : null; },
  setItem: function (k, v) { this._data[k] = String(v); },
  removeItem: function (k) { delete this._data[k]; },
};

globalThis.window = {
  matchMedia: function () {
    return { matches: false, addEventListener: function () {}, removeEventListener: function () {} };
  },
  addEventListener: function () {},
  removeEventListener: function () {},
  localStorage: globalThis.localStorage,
};

globalThis.EventSource = function () {
  return { addEventListener: function () {}, close: function () {} };
};

var clipboardWrites = [];
Object.defineProperty(globalThis, "navigator", {
  value: {
    clipboard: {
      writeText: function (t) { clipboardWrites.push(t); return Promise.resolve(); },
    },
  },
  writable: true,
  configurable: true,
});

globalThis.fetch = function () {
  return Promise.resolve({ status: 200, json: function () { return Promise.resolve({}); } });
};

// ===========================================================================
// 断言
// ===========================================================================

const md = await import(pathToFileURL(path.join(WEB_DIR, "markdown.js")).href);
const chat = await import(pathToFileURL(path.join(WEB_DIR, "chat.js")).href);
const conversationEl = getElement("conversation");
const screenEl = getElement("screen");
const screenCopyBtn = getElement("screen-copy-btn");

// ---- 1. 渲染方式归一化 + 正文渲染（normalizeRenderMode 不承担默认，仅 "md"→md）----
assert.strictEqual(md.normalizeRenderMode("md"), "md", '显式 "md" 应归一为 md');
assert.strictEqual(md.normalizeRenderMode("text"), "text", '显式 "text" 应归一为 text');
assert.strictEqual(md.normalizeRenderMode(undefined), "text", "缺省应归一为 text（归一器不视为默认，仅 'md'→md）");
assert.strictEqual(md.normalizeRenderMode(null), "text", "null 应归一为 text");
assert.strictEqual(md.normalizeRenderMode(""), "text", "空串应归一为 text");
assert.strictEqual(md.normalizeRenderMode("MD"), "text", "大小写变体不应被当作 md（只认小写 md）");
assert.strictEqual(md.normalizeRenderMode("html"), "text", "非法值应归一为 text");

var rawText = "# 标题\n\n**粗体** 与 <script>alert(1)</script>";
var txtHtml = md.renderMessageBody(rawText, "text");
assert.ok(txtHtml.indexOf("<h1") < 0, "text 渲染不应产生 Markdown 结构");
assert.ok(txtHtml.indexOf("**粗体**") >= 0, "text 渲染应保留 Markdown 原始标记");
assert.ok(txtHtml.indexOf("&lt;script&gt;") >= 0, "text 渲染应转义 HTML");
assert.ok(txtHtml.indexOf("<script>") < 0, "text 渲染不应输出原始 <script>");
var mdHtml = md.renderMessageBody(rawText, "md");
assert.ok(mdHtml.indexOf("<h1") >= 0, "md 渲染应生成标题结构");
assert.ok(mdHtml.indexOf("<strong>") >= 0, "md 渲染应生成粗体");
assert.ok(mdHtml.indexOf("<script>") < 0, "md 渲染同样不应输出未转义脚本");
assert.strictEqual(md.renderMessageBody(undefined, "text"), "", "undefined 正文按空串处理（不抛错）");
assert.strictEqual(typeof md.renderMessageBody("", "md"), "string", "空正文 md 渲染应返回字符串");

// ---- 1b. 块级排版契约：块间恰好一个空行、首尾无空行、列表/引用内紧凑 ----
// 渲染器改为块级解析（先切块、再套行内规则），不再用「整体把 \n 转 <br>，再用
// 正则回头删多余 <br>」的后处理——那种做法分不清「块边界上的冗余空行」与
// 「作者确实写下的空行」，会把后者一起删掉。这里把排版契约固定成断言。
var blockCases = [
  ["两段之间", "a\n\nb", "<p>a</p><br><p>b</p>"],
  ["作者多写的空行收敛为一个", "a\n\n\n\nb", "<p>a</p><br><p>b</p>"],
  ["标题与正文之间", "# H\n\ntext", "<h1>H</h1><br><p>text</p>"],
  ["段落/列表/段落之间", "para\n\n- a\n- b\n\npara2",
    "<p>para</p><br><ul><li>a</li><li>b</li></ul><br><p>para2</p>"],
  ["列表项之间紧凑", "- a\n- b", "<ul><li>a</li><li>b</li></ul>"],
  ["有序列表项之间紧凑", "1. a\n2. b",
    '<ol><li class="li-num">a</li><li class="li-num">b</li></ol>'],
  ["引用块内多行紧凑", "> a\n> b", "<blockquote><p>a<br>b</p></blockquote>"],
  ["段内软换行保留", "a\nb", "<p>a<br>b</p>"],
  ["文档首尾空行被忽略", "\n\na\n\n", "<p>a</p>"],
  ["列表续行并入条目", "- item\n  continued\n- next",
    "<ul><li>item<br>continued</li><li>next</li></ul>"],
];
blockCases.forEach(function (c) {
  assert.strictEqual(md.renderMarkdown(c[1]), c[2], c[0] + " 应渲染成固定的块级结构");
});

// 「不多渲染空行」的核心不变量：块级元素自身即换行，块间那一行空行只由单个
// <br> 承载，因此输出里不允许出现连续 <br>，也不允许以 <br> 开头/结尾。
var spacingCorpus = [
  "a\n\nb", "# H\n\ntext", "- a\n- b", "1. a\n2. b", "> a\n> b", "> 提示\n>\n> - a\n> - b",
  "para\n\n- a\n\npara2", "a\nb\nc", "\n\na\n\n", "| a | b |\n|---|---|\n| 1 | 2 |",
  "```go\ncode\n```", "text\n\n```\ncode\n```\n\nmore", "- [x] done\n- [ ] todo",
  "### 标题\n\n1. 一\n2. 二\n\n> 引用\n\n结尾", "a\n\n\n\n\nb",
];
spacingCorpus.forEach(function (src) {
  var html = md.renderMarkdown(src);
  assert.ok(html.indexOf("<br><br>") < 0, "输出不应出现连续 <br>（多渲染空行）: " + JSON.stringify(src));
  assert.ok(!/^<br>/.test(html), "输出不应以 <br> 开头（文档首行空行）: " + JSON.stringify(src));
  assert.ok(!/<br>$/.test(html), "输出不应以 <br> 结尾（文档末行空行）: " + JSON.stringify(src));
});

// 代码块内容只转义一次，且保留行首缩进（旧实现 trim() 会吃掉缩进）。
var codeHtml = md.renderMarkdown('```go\n"x" <y> &z\n```');
assert.ok(codeHtml.indexOf("&quot;x&quot; &lt;y&gt; &amp;z") >= 0,
  "代码块内容应只转义一次（&quot; 不应变成 &amp;quot;）");
assert.ok(codeHtml.indexOf("&amp;quot;") < 0, "代码块内容不应被二次转义");
assert.ok(md.renderMarkdown("```\n    indented\n```").indexOf("    indented") >= 0,
  "代码块应保留行首缩进");
// 行内代码先摘占位符：代码里的 ** 不应再被当成粗体。
assert.ok(md.renderMarkdown("**b** 与 `**x**`").indexOf("<code>**x**</code>") >= 0,
  "行内代码内的 ** 不应被当成粗体");

// ---- 2. assistant 行结构：右上角 md|txt 控件 + 默认 md + 同步渲染 md 容器 ----
var assistantHtml = chat.chatMsgRowHtml("assistant", rawText, false);
assert.ok(assistantHtml.indexOf('class="msg-row msg-assistant"') >= 0, "assistant 行应带 msg-assistant 类");
assert.ok(assistantHtml.indexOf('data-render-mode="md"') >= 0, 'assistant 行默认应为 data-render-mode="md"');
assert.ok(assistantHtml.indexOf('class="msg-render-toggle"') >= 0, "assistant 行应带 md|txt 切换控件");
assert.ok(assistantHtml.indexOf('data-render-mode-set="md"') >= 0, "切换控件应有 md 按钮");
assert.ok(assistantHtml.indexOf('data-render-mode-set="txt"') >= 0, "切换控件应有 txt 按钮");
assert.ok(assistantHtml.indexOf('class="msg-text"') >= 0, "assistant 行应有 .msg-text 原文容器");
assert.ok(assistantHtml.indexOf('class="msg-md"') >= 0, "assistant 行应有 .msg-md 渲染容器");
assert.ok(assistantHtml.indexOf("style=\"display") < 0, "显隐应由 data-render-mode + CSS 决定，不写内联样式");

// 控件位置：抬头行（.msg-head）内、正文（.msg-body）之前 = 右上角
var headIdx = assistantHtml.indexOf('class="msg-head"');
var toggleIdx = assistantHtml.indexOf('class="msg-render-toggle"');
var bodyIdx = assistantHtml.indexOf('class="msg-body"');
assert.ok(headIdx >= 0, "assistant 行应有 .msg-head 抬头行");
assert.ok(toggleIdx > headIdx, "切换控件应在抬头行内");
assert.ok(toggleIdx < bodyIdx, "切换控件应在正文之前（右上角位置）");
var mdBtnIdx = assistantHtml.indexOf('data-render-mode-set="md"');
var txtBtnIdx = assistantHtml.indexOf('data-render-mode-set="txt"');
assert.ok(mdBtnIdx < txtBtnIdx, "按钮顺序应为 md | txt");

var rowA = parseRow(assistantHtml);
var buttonsA = rowA.querySelectorAll(".render-mode-btn");
assert.strictEqual(buttonsA.length, 2, "切换控件应恰好两个按钮");
var mdBtnA = rowA.querySelector('[data-render-mode-set="md"]');
var txtBtnA = rowA.querySelector('[data-render-mode-set="txt"]');
assert.ok(mdBtnA.classList.contains("active"), "默认激活 md 按钮");
assert.ok(!txtBtnA.classList.contains("active"), "默认不激活 txt 按钮");
assert.strictEqual(mdBtnA.getAttribute("aria-pressed"), "true", "md 按钮 aria-pressed 应为 true");
assert.strictEqual(txtBtnA.getAttribute("aria-pressed"), "false", "txt 按钮 aria-pressed 应为 false");
assert.strictEqual(mdBtnA.textContent, "md", "按钮文字应为 md");
assert.strictEqual(txtBtnA.textContent, "txt", "按钮文字应为 txt");
assert.strictEqual(chat.getMessageRenderMode(rowA), "md", "新建 assistant 行渲染方式应为 md");
assert.strictEqual(rowA.querySelector(".msg-text").textContent, rawText, ".msg-text 应保存原文（实体解码后等值）");
assert.ok(rowA.querySelector(".msg-md").children.length > 0, "默认 md 应同步预渲染 Markdown（默认即显，不留空）");
assert.ok(rowA.querySelector(".msg-md").querySelector("h1") !== null, "默认 md 的 Markdown 应渲染标题结构");

// XSS 回归：原文与切换控件都不带未转义标签
var evilHtml = chat.chatMsgRowHtml("assistant", '<img src=x onerror="alert(1)">', false);
assert.ok(evilHtml.indexOf("<img") < 0, "assistant 正文不应输出原始 <img>");
assert.ok(evilHtml.indexOf("&lt;img") >= 0, "assistant 正文应转义 <img>");

// ---- 3. 非 assistant 行不带切换控件（回归）----
["user", "reasoning", "tool", "error", "command", "diagnostic", "runtime"].forEach(function (role) {
  var h = chat.chatMsgRowHtml(role, "x", false);
  assert.ok(h.indexOf("msg-render-toggle") < 0, role + " 行不应带 md|txt 切换控件");
  assert.ok(h.indexOf("data-render-mode") < 0, role + " 行不应带 data-render-mode");
  assert.ok(h.indexOf('class="msg-md"') < 0, role + " 行不应带 .msg-md 容器");
});

// ---- 4. applyMessageRenderMode：属性 / 按钮状态 / 惰性渲染 ----
var rowB = parseRow(chat.chatMsgRowHtml("assistant", "# H\n\n- a\n- b", false));
var mdElB = rowB.querySelector(".msg-md");
assert.strictEqual(chat.applyMessageRenderMode(rowB, "md"), "md", "applyMessageRenderMode 应返回归一化后的方式");
assert.strictEqual(chat.getMessageRenderMode(rowB), "md", "切到 md 后属性应为 md");
assert.ok(rowB.querySelector('[data-render-mode-set="md"]').classList.contains("active"), "md 按钮应激活");
assert.ok(!rowB.querySelector('[data-render-mode-set="txt"]').classList.contains("active"), "txt 按钮应取消激活");
assert.strictEqual(rowB.querySelector('[data-render-mode-set="md"]').getAttribute("aria-pressed"), "true",
  "md 按钮 aria-pressed 应同步为 true");
assert.strictEqual(rowB.querySelector('[data-render-mode-set="txt"]').getAttribute("aria-pressed"), "false",
  "txt 按钮 aria-pressed 应同步为 false");
assert.ok(mdElB.children.length > 0, "切到 md 应惰性渲染 Markdown 正文");
assert.ok(mdElB.querySelector("h1") !== null, "md 渲染应生成标题节点");
assert.strictEqual(rowB.querySelector(".msg-text").textContent, "# H\n\n- a\n- b", "原文容器不应被渲染结果覆盖");

var renderedCount = mdElB.children.length;
chat.applyMessageRenderMode(rowB, "md");
assert.strictEqual(mdElB.children.length, renderedCount, "重复切到 md 不应叠加内容");

chat.applyMessageRenderMode(rowB, "text");
assert.strictEqual(chat.getMessageRenderMode(rowB), "text", "切回 txt 后属性应为 text");
assert.ok(rowB.querySelector('[data-render-mode-set="txt"]').classList.contains("active"), "txt 按钮应重新激活");
assert.ok(!rowB.querySelector('[data-render-mode-set="md"]').classList.contains("active"), "md 按钮应取消激活");
assert.ok(mdElB.children.length > 0, "切回 txt 不清空已渲染内容（再次切回无需重解析）");
assert.strictEqual(chat.applyMessageRenderMode(rowB, "html"), "text", "非法方式应归一为 text");

// ---- 5. 点击委托：md / txt 按钮切换 ----
chat.initChat(); // 与 app.js 启动序列一致：绑定 conversation 上的点击委托
var rowC = parseRow(chat.chatMsgRowHtml("assistant", "```\ncode line\n```", false));
conversationEl.appendChild(rowC);
function clickIn(row, sel) {
  var btn = row.querySelector(sel);
  conversationEl.dispatch("click", {
    target: btn, currentTarget: conversationEl,
    preventDefault: function () {}, stopPropagation: function () {},
  });
  return btn;
}
clickIn(rowC, '[data-render-mode-set="md"]');
assert.strictEqual(chat.getMessageRenderMode(rowC), "md", "点击 md 按钮应切到 Markdown 渲染");
assert.ok(rowC.querySelector(".msg-md").querySelector("pre") !== null, "md 渲染应生成代码块");
assert.ok(rowC.querySelector(".msg-md").querySelector(".copy-code-btn") !== null,
  "md 渲染的代码块应带复制按钮（由 stream.js 的 #screen 委托处理）");
clickIn(rowC, '[data-render-mode-set="txt"]');
assert.strictEqual(chat.getMessageRenderMode(rowC), "text", "点击 txt 按钮应切回纯文本");
clickIn(rowC, '[data-render-mode-set="md"]');
assert.strictEqual(chat.getMessageRenderMode(rowC), "md", "可反复切换回 md");

// ---- 6. 会话复制取原文（不含 md 渲染产物）----
screenEl.children = [];
var copyRow = parseRow(chat.chatMsgRowHtml("assistant", "```\ncode line\n```", false));
screenEl.appendChild(copyRow);
chat.applyMessageRenderMode(copyRow, "md");
clipboardWrites.length = 0;
screenCopyBtn.dispatch("click", {});
await new Promise(function (r) { setTimeout(r, 0); });
assert.strictEqual(clipboardWrites.length, 1, "点击复制按钮应写入剪贴板一次");
var copied = clipboardWrites[0];
assert.ok(copied.indexOf("code line") >= 0, "复制结果应包含 assistant 正文");
assert.ok(copied.indexOf("复制") < 0, "复制结果不应包含 md 渲染出的代码块「复制」按钮文字");

// ---- 7. CSS / 流式气泡不变量 ----
var css = fs.readFileSync(path.join(WEB_ROOT, "style.css"), "utf8");
function hasRule(sel, decl) {
  var esc = function (s) { return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"); };
  return new RegExp(esc(sel) + "\\s*\\{[^}]*" + esc(decl), "m").test(css);
}
assert.ok(hasRule('#screen .msg-row[data-render-mode="md"] .msg-text', "display: none"),
  'style.css 应在 data-render-mode="md" 时隐藏 .msg-text');
assert.ok(hasRule('#screen .msg-row:not([data-render-mode="md"]) .msg-md', "display: none"),
  "style.css 应在非 md（默认 txt）时隐藏 .msg-md");
assert.ok(css.indexOf("#screen .msg-render-toggle") >= 0, "style.css 应有切换控件样式");
assert.ok(css.indexOf("#screen .msg-row.msg-assistant .msg-md") >= 0, "style.css 应有 .msg-md 排版样式");
// 块级元素纵向外边距清零：块间那一行空行由 renderMarkdown 输出的 <br> 承载，
// margin 不为 0 时会与空行叠加成两行空隙（= 多渲染空间）。
assert.ok(hasRule("#screen .msg-row.msg-assistant .msg-md p", "margin: 0"),
  ".msg-md 段落纵向外边距应清零");
assert.ok(hasRule("#screen .msg-row.msg-assistant .msg-md pre", "margin: 0"),
  ".msg-md 代码块纵向外边距应清零");
assert.ok(hasRule("#screen .msg-row.msg-assistant .msg-md ol", "margin: 0"),
  ".msg-md 列表纵向外边距应清零");
assert.ok(hasRule("#screen .msg-row.msg-assistant .msg-md blockquote", "margin: 0"),
  ".msg-md 引用块纵向外边距应清零");
assert.ok(/#screen \.msg-row\.msg-assistant \.msg-md h6 \{\s*margin: 0;/.test(css),
  ".msg-md 标题纵向外边距应清零");
// 审批/问答弹窗同样吃 renderMarkdown 的块级输出，需要同一套清零规则。
assert.ok(/#approval-prompt p,[\s\S]{0,400}?\{[^}]*margin-top: 0; margin-bottom: 0;/.test(css),
  "审批/问答弹窗内 renderMarkdown 块级元素同样应清零纵向外边距");

var streamSrc = fs.readFileSync(path.join(WEB_DIR, "stream.js"), "utf8");
assert.ok(streamSrc.indexOf("renderMarkdown") >= 0, "流式气泡应继续走 renderMarkdown");
assert.ok(streamSrc.indexOf("renderMessageBody") < 0, "流式气泡不参与 md|txt 切换（避免默认值漂移）");
assert.ok(streamSrc.indexOf('document.getElementById("conversation")') >= 0,
  "代码块复制委托应挂在 #conversation（覆盖 #stream-msg 与 #screen）");

// ---- 8. 代码块复制委托：流式气泡与 assistant md 气泡都能复制 ----
const stream = await import(pathToFileURL(path.join(WEB_DIR, "stream.js")).href);
stream.initStream();
function clickTarget(el) {
  conversationEl.dispatch("click", {
    target: el, currentTarget: conversationEl,
    preventDefault: function () {}, stopPropagation: function () {},
  });
}
// #stream-msg 是 #conversation 的子节点（不在 #screen 内）：旧实现把委托注册在
// init 时为 null 的 streamMsgEl 上，导致流式气泡的复制按钮从未生效（文档 §5 已知问题）。
var streamBubble = getElement("stream-msg");
var streamPre = makeEl("pre");
streamPre.innerHTML = '<button class="copy-code-btn" type="button">复制</button><code>stream code</code>';
streamBubble.appendChild(streamPre);
clipboardWrites.length = 0;
clickTarget(streamPre.querySelector(".copy-code-btn"));
await new Promise(function (r) { setTimeout(r, 0); });
assert.deepStrictEqual(clipboardWrites, ["stream code"], "流式气泡的代码块复制按钮应可复制");

// assistant 切到 md 后的代码块（在 #screen 内）走同一委托
var mdPre = rowC.querySelector(".msg-md").querySelector(".copy-code-btn");
clipboardWrites.length = 0;
clickTarget(mdPre);
await new Promise(function (r) { setTimeout(r, 0); });
assert.deepStrictEqual(clipboardWrites, ["code line"], "assistant md 代码块复制按钮应可复制");

console.log("verify-micro-web-render-mode: 全部断言通过");
