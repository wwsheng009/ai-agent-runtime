// 行为验证：aicli micro web client 单条消息复制（所有角色抬头行的 ⧉ 图标）。
// 运行：node scripts/verify-micro-web-copy-msg.mjs（无需浏览器；见 docs/aicli/web-testing.md）
// 覆盖：
//   1. chatMsgRowHtml 所有角色（user / assistant / reasoning / tool / system /
//      command / diagnostic / runtime）都带 .msg-copy-btn：位置在抬头行内、
//      正文容器之前；reasoning 行的图标不在 <summary> 内（避免点击复制连带折叠）
//   2. domMessageText：只取本条消息自己的正文容器（不含角色标签 / 控件文字 /
//      相邻消息）；assistant 取 .msg-text 原文（不含 md 渲染产物）；reasoning
//      不加 [推理] 前缀；无正文容器时返回 ""
//   3. initChat 点击委托：点某行 ⧉ 只复制该行正文（行间隔离），成功后图标短暂变 ✓
//   4. 整会话复制（#screen-copy-btn）不受影响：仍按 DOM 顺序拼接、保留 [推理]
//      前缀、不含复制图标与角色标签文字
//   5. 工具行展开/收起在抬头行被 .msg-head 包裹后仍可用（closest(".msg-row")）
//   6. 流式气泡（#stream-msg）复制图标：复制已累积的完整助手文本，且与对话区
//      委托不重复响应同一次点击
//   7. style.css / stream.js 不变量（图标样式、推理行绝对定位、流式不参与 md|txt）
import assert from "node:assert";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_ROOT = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web");
const WEB_DIR = path.join(WEB_ROOT, "js");

// ===========================================================================
// 迷你 DOM（极简 HTML 解析，与 verify-micro-web-render-mode.mjs 同款）
// 单条复制必须断言真实节点归属：图标在抬头行、正文在正文容器，且复制结果只
// 来自正文容器——字符串级 stub 覆盖不了「点了哪一行的哪个容器」。
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
    insertAdjacentHTML: function (pos, html) {
      if (pos !== "beforeend") { return; }
      parseInto(this, String(html == null ? "" : html));
    },
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

// ---- Global stubs（chat.js / stream.js 顶层依赖）----
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

const chat = await import(pathToFileURL(path.join(WEB_DIR, "chat.js")).href);
const stream = await import(pathToFileURL(path.join(WEB_DIR, "stream.js")).href);
const conversationEl = getElement("conversation");
const screenEl = getElement("screen");
const screenCopyBtn = getElement("screen-copy-btn");
const streamMsgEl = getElement("stream-msg");

// 所有消息角色（含未在 MSG_LABELS 里注册的 error：应回退到「消息」标签）
const ROLES = ["user", "assistant", "reasoning", "tool", "system", "command", "diagnostic", "runtime", "error"];

// ---- 1. 结构：所有角色都有复制图标，且在抬头行内、正文之前 ----
ROLES.forEach(function (role) {
  var html = chat.chatMsgRowHtml(role, "正文内容", false, 5);
  assert.ok(html.indexOf('class="msg-copy-btn"') >= 0, role + " 行应有单条复制图标");
  assert.ok(html.indexOf('data-copy-msg="1"') >= 0, role + " 行复制图标应带 data-copy-msg 标记");
  assert.ok(html.indexOf('title="复制本条消息"') >= 0, role + " 行复制图标应有 title 提示");
  assert.ok(html.indexOf('aria-label="复制本条消息"') >= 0, role + " 行复制图标应有 aria-label");
  assert.ok(html.indexOf('type="button"') >= 0, role + " 行复制图标应是真实 <button>（键盘可达）");

  var row = parseRow(html);
  var btn = row.querySelector(".msg-copy-btn");
  assert.ok(btn !== null, role + " 行解析后应能找到 .msg-copy-btn");
  assert.strictEqual(btn.textContent, "⧉", role + " 行复制图标文字应为 ⧉");
  assert.strictEqual(btn.closest(".msg-row"), row, role + " 行复制图标应归属于本行");

  // 图标必须在本行正文容器之外（否则控件文字会混进复制结果）
  var body = row.querySelector(".msg-body");
  if (body) {
    assert.ok(body.querySelector(".msg-copy-btn") === null, role + " 行复制图标不应在正文容器内");
  }
  var reasContent = row.querySelector(".reasoning-content");
  if (reasContent) {
    assert.ok(reasContent.querySelector(".msg-copy-btn") === null, role + " 行复制图标不应在推理正文容器内");
  }
  // 图标文字不得出现在任何正文容器里
  [".msg-body", ".tool-output", ".reasoning-content"].forEach(function (sel) {
    var el = row.querySelector(sel);
    if (el) { assert.ok(el.textContent.indexOf("⧉") < 0, role + " 行 " + sel + " 内不应含图标文字"); }
  });
});

// 抬头行顺序：.msg-head（含图标）在 .msg-body 之前 = 右上角
ROLES.forEach(function (role) {
  if (role === "reasoning") { return; } // 推理行无 .msg-body，单独断言
  var html = chat.chatMsgRowHtml(role, "x", false);
  var headIdx = html.indexOf('class="msg-head"');
  var btnIdx = html.indexOf('class="msg-copy-btn"');
  var bodyIdx = html.indexOf('class="msg-body"');
  assert.ok(headIdx >= 0, role + " 行应有 .msg-head 抬头行");
  assert.ok(btnIdx > headIdx, role + " 行复制图标应在抬头行内");
  assert.ok(btnIdx < bodyIdx, role + " 行复制图标应在正文之前（右上角）");
});

// assistant 行：复制图标在 md|txt 控件之后（最右），且不破坏切换控件
var assistantHtml = chat.chatMsgRowHtml("assistant", "正文", false);
assert.ok(assistantHtml.indexOf('class="msg-render-toggle"') >= 0, "assistant 行应保留 md|txt 切换控件");
assert.ok(assistantHtml.indexOf('class="msg-render-toggle"') < assistantHtml.indexOf('class="msg-copy-btn"'),
  "assistant 行复制图标应在 md|txt 控件右侧（抬头行最右）");
var assistantRow = parseRow(assistantHtml);
assert.strictEqual(assistantRow.querySelectorAll(".render-mode-btn").length, 2, "assistant 行切换控件仍应恰好两个按钮");

// tool 行：抬头控件（展开/收起）仍在抬头行内，复制图标与其并列
var toolHtml = chat.chatMsgRowHtml("tool", "输出", false);
assert.ok(toolHtml.indexOf('class="msg-label tool-toggle"') >= 0, "tool 行应保留抬头展开/收起控件");
assert.ok(toolHtml.indexOf('class="msg-label tool-toggle"') < toolHtml.indexOf('class="msg-copy-btn"'),
  "tool 行复制图标应在展开/收起控件之后");
var toolRowShape = parseRow(toolHtml);
assert.ok(toolRowShape.querySelector(".msg-head").querySelector(".tool-toggle") !== null,
  "tool 行展开/收起控件应在抬头行内");

// reasoning 行：图标是本行直接子节点、不在 <summary> / details 内
// （点击复制不应连带展开/收起面板）
var reasoningRow = parseRow(chat.chatMsgRowHtml("reasoning", "推理正文", false));
var reasoningBtn = reasoningRow.querySelector(".msg-copy-btn");
assert.strictEqual(reasoningBtn.closest(".msg-row"), reasoningRow, "推理行复制图标应归属于本行");
assert.ok(reasoningRow.querySelector("summary").querySelector(".msg-copy-btn") === null,
  "推理行复制图标不应在 <summary> 内（避免点击复制连带展开/收起）");
assert.ok(reasoningRow.querySelector("details").querySelector(".msg-copy-btn") === null,
  "推理行复制图标不应在折叠面板内");

// ---- 2. domMessageText：只取本条消息自己的正文 ----
var userRow = parseRow(chat.chatMsgRowHtml("user", "用户正文 <b>", false));
assert.strictEqual(chat.domMessageText(userRow), "用户正文 <b>", "user 行应只复制正文（不含「你」标签与图标）");

var asstRow = parseRow(chat.chatMsgRowHtml("assistant", "```\ncode line\n```", false));
chat.applyMessageRenderMode(asstRow, "md");
assert.strictEqual(chat.domMessageText(asstRow), "```\ncode line\n```",
  "assistant 行应复制 .msg-text 原文（不含 md 渲染产物与代码块「复制」按钮文字）");

var toolRowText = parseRow(chat.chatMsgRowHtml("tool", "工具输出全文", false));
assert.strictEqual(chat.domMessageText(toolRowText), "工具输出全文",
  "tool 行应只复制工具输出（不含「工具」标签 / 展开 / ▼ / ⧉）");

var reasRowText = parseRow(chat.chatMsgRowHtml("reasoning", "推理正文", false));
assert.strictEqual(chat.domMessageText(reasRowText), "推理正文",
  "reasoning 行应只复制推理内容（不加 [推理] 前缀——那是整会话复制的语义标注）");

assert.strictEqual(chat.domMessageText(parseRow('<div class="msg-row msg-system"></div>')), "",
  "无正文容器时应返回空串（不抛错）");
assert.strictEqual(chat.domMessageText(null), "", "null 行应返回空串（不抛错）");

// ---- 3. 点击委托：点某行 ⧉ 只复制该行正文 ----
chat.initChat(); // 与 app.js 启动序列一致：绑定 conversation 上的点击委托
screenEl.children = [];
var rowA = parseRow(chat.chatMsgRowHtml("user", "第一条正文", false));
var rowB = parseRow(chat.chatMsgRowHtml("assistant", "第二条正文", false));
screenEl.appendChild(rowA);
screenEl.appendChild(rowB);

function clickIn(host, el) {
  host.dispatch("click", {
    target: el, currentTarget: host,
    preventDefault: function () {}, stopPropagation: function () {},
  });
}

clipboardWrites.length = 0;
var btnA = rowA.querySelector(".msg-copy-btn");
clickIn(conversationEl, btnA);
await new Promise(function (r) { setTimeout(r, 0); });
assert.strictEqual(clipboardWrites.length, 1, "点单条复制应只写入剪贴板一次");
assert.strictEqual(clipboardWrites[0], "第一条正文", "点 user 行复制应只复制该行正文（不含相邻消息）");
assert.strictEqual(btnA.textContent, "✓", "复制成功后图标应短暂变 ✓（行内反馈）");

clipboardWrites.length = 0;
clickIn(conversationEl, rowB.querySelector(".msg-copy-btn"));
await new Promise(function (r) { setTimeout(r, 0); });
assert.strictEqual(clipboardWrites.length, 1, "点第二条消息的复制应只写入剪贴板一次");
assert.strictEqual(clipboardWrites[0], "第二条正文", "点 assistant 行复制应只复制该行正文");
assert.ok(clipboardWrites[0].indexOf("⧉") < 0 && clipboardWrites[0].indexOf("aicli") < 0,
  "复制结果不应混入图标或角色标签文字");

// ---- 4. 整会话复制不受影响：顺序拼接 + [推理] 前缀 + 无控件文字 ----
screenEl.children = [];
screenEl.appendChild(parseRow(chat.chatMsgRowHtml("user", "问一句", false)));
screenEl.appendChild(parseRow(chat.chatMsgRowHtml("reasoning", "想一想", false)));
screenEl.appendChild(parseRow(chat.chatMsgRowHtml("tool", "查一查", false)));
screenEl.appendChild(parseRow(chat.chatMsgRowHtml("assistant", "答一句", false)));
clipboardWrites.length = 0;
screenCopyBtn.dispatch("click", {});
await new Promise(function (r) { setTimeout(r, 0); });
assert.strictEqual(clipboardWrites.length, 1, "点会话复制应写入剪贴板一次");
var convText = clipboardWrites[0];
assert.strictEqual(convText, "问一句\n\n[推理] 想一想\n\n查一查\n\n答一句",
  "整会话复制应保持 DOM 顺序拼接，推理保留 [推理] 前缀，且不含复制图标文字");

// ---- 5. 工具行展开/收起：抬头行被 .msg-head 包裹后仍可用 ----
screenEl.children = [];
var toolRow = parseRow(chat.chatMsgRowHtml("tool", "工具输出", false));
screenEl.appendChild(toolRow);
var toolOutput = toolRow.querySelector(".tool-output");
toolOutput.scrollHeight = 120;
toolOutput.clientHeight = 40;
chat.refreshToolOutputToggles(screenEl);
var toolLabel = toolRow.querySelector(".tool-toggle");
assert.ok(!toolLabel.classList.contains("tool-toggle-off"), "溢出内容的抬头控件应可用");
clickIn(conversationEl, toolLabel.querySelector(".tool-toggle-text"));
assert.ok(toolOutput.classList.contains("tool-expanded"),
  '点抬头应展开（closest(".msg-row") 定位不受 .msg-head 包裹影响）');
clickIn(conversationEl, toolLabel.querySelector(".tool-toggle-text"));
assert.ok(toolOutput.classList.contains("tool-collapsed"), "再点抬头应收起");
assert.strictEqual(chat.domMessageText(toolRow), "工具输出", "展开/收起后单条复制仍只取工具输出");

// ---- 6. 流式气泡复制：复制已累积的完整助手文本，且不重复响应 ----
stream.initStream(); // 与 app.js 启动序列一致：绑定 conversation 上的代码块 / 流式复制委托
stream.beginStream();
stream.setStreamText("流式正文内容");
await new Promise(function (r) { setTimeout(r, 150); }); // 等打字机揭示（20ms/tick）
assert.ok(streamMsgEl.querySelector(".stream-head") !== null, "流式气泡应有抬头行");
var streamBtn = streamMsgEl.querySelector(".stream-copy-btn");
assert.ok(streamBtn !== null, "流式气泡应有单条复制图标");
assert.ok(streamMsgEl.querySelector(".msg-copy-btn") === null,
  "流式气泡不应复用 .msg-copy-btn（否则对话区委托会重复响应同一次点击）");

clipboardWrites.length = 0;
clickIn(conversationEl, streamBtn);
await new Promise(function (r) { setTimeout(r, 0); });
assert.strictEqual(clipboardWrites.length, 1, "点流式气泡复制应只写入剪贴板一次（两处委托不重复响应）");
assert.strictEqual(clipboardWrites[0], "流式正文内容", "流式气泡应复制已累积的完整助手文本");

// 只有推理、尚无助手文本时：退回复制推理文本
stream.beginStream();
stream.appendStreamReasoning("只有推理");
await new Promise(function (r) { setTimeout(r, 150); });
var streamBtn2 = streamMsgEl.querySelector(".stream-copy-btn");
assert.ok(streamBtn2 !== null, "推理阶段的流式气泡也应有复制图标");
clipboardWrites.length = 0;
clickIn(conversationEl, streamBtn2);
await new Promise(function (r) { setTimeout(r, 0); });
assert.strictEqual(clipboardWrites.length, 1, "推理阶段点复制也应写入剪贴板一次");
assert.strictEqual(clipboardWrites[0], "只有推理", "尚无助手文本时应退回复制推理文本");

// 收尾：流式结束后状态复位（回归：复制图标不影响流式生命周期）
stream.endStream();
await new Promise(function (r) { setTimeout(r, 200); });
assert.strictEqual(stream.isStreamActive(), false, "流式结束后应复位为非活跃");

// ---- 7. style.css / 源码不变量 ----
var css = fs.readFileSync(path.join(WEB_ROOT, "style.css"), "utf8");
function hasRule(sel, decl) {
  var esc = function (s) { return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"); };
  return new RegExp(esc(sel) + "\\s*\\{[^}]*" + esc(decl), "m").test(css);
}
assert.ok(hasRule("#screen .msg-row .msg-head", "display: flex"), "style.css 应有抬头行样式（所有角色共用）");
assert.ok(hasRule("#stream-msg .stream-copy-btn", "cursor: pointer"), "style.css 应有复制图标样式");
assert.ok(hasRule("#screen .msg-row.msg-reasoning > .msg-copy-btn", "position: absolute"),
  "推理行复制图标应绝对定位在行右上角（不进 summary）");
assert.ok(hasRule("#stream-msg .stream-head", "justify-content: flex-end"),
  "流式气泡抬头行应右对齐（图标在右上角）");

var chatSrc = fs.readFileSync(path.join(WEB_DIR, "chat.js"), "utf8");
assert.ok(chatSrc.indexOf('closest(".msg-copy-btn")') >= 0, "chat.js 点击委托应识别 .msg-copy-btn");
assert.ok(chatSrc.indexOf("export function domMessageText") >= 0, "chat.js 应导出 domMessageText");
var streamSrc = fs.readFileSync(path.join(WEB_DIR, "stream.js"), "utf8");
assert.ok(streamSrc.indexOf("copyTextToClipboard") >= 0, "stream.js 应复用 chat.js 的剪贴板写入");
assert.ok(streamSrc.indexOf('closest(".stream-copy-btn")') >= 0, "stream.js 委托应识别 .stream-copy-btn");
assert.ok(streamSrc.indexOf("renderMessageBody") < 0, "流式气泡不参与 md|txt 切换（避免默认值漂移）");

console.log("verify-micro-web-copy-msg: 全部断言通过");
console.log("  - 所有角色（含 error 回退标签）都有抬头行 ⧉ 复制图标，位于正文之前");
console.log("  - 单条复制只取该行正文容器（无标签 / 控件 / 相邻消息 / md 渲染产物）");
console.log("  - 整会话复制保持 DOM 顺序拼接与 [推理] 前缀，不受单条复制影响");
console.log("  - 工具行展开/收起在 .msg-head 包裹后仍可用；流式气泡可复制且不重复响应");
