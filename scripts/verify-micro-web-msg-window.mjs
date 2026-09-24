// 行为验证：aicli micro web client 长会话窗口化渲染（首屏只取最新一页 + 上滚懒加载更早消息）。
// 运行：node scripts/verify-micro-web-msg-window.mjs（无需浏览器；见 docs/aicli/web-testing.md）
// 覆盖：
//   1. chatMsgRowHtml 绝对索引（data-msg-index）：带 index 写入、不带 index 保持旧行为
//   2. serverMessagesHtml：按绝对索引顺序生成行
//   3. parseMessageWindow / shouldRebuildForWindow：窗口归一化与「整体重建 or 增量尾部」判定
//   4. refreshScreen：请求带 msg_limit、首屏只渲染最新一页（不是全量）、尾部增量替换
//      （不重建已有节点）、窗口右移时保留已加载的更早内容（游标不回退）
//   5. 上滚触发 loadOlderMessages：msg_before 游标、前插顺序、scrollTop 锚定补偿、到顶停止
//   6. 顶部提示行：还有更早消息时提示「上滚加载更早消息」，请求中显示加载态，到最早一条后移除
import assert from "node:assert";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_DIR = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web", "js");

// ===========================================================================
// 迷你 DOM（带极简 HTML 解析）
// 窗口化渲染必须断言真实节点顺序/绝对索引/滚动锚定，字符串级 stub 不够用。
// 解析器只需覆盖 chatMsgRowHtml 产出的结构（div/details/summary/span + 文本）。
// ===========================================================================

var insertObserver = null; // (parentEl, addedCount) => void：模拟插入内容导致的滚动高度增长

function decodeEntities(s) {
  return String(s)
    .replace(/&lt;/g, "<").replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"').replace(/&#39;/g, "'")
    .replace(/&amp;/g, "&");
}

function escapeAttr(s) {
  return String(s).replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
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

// 简单选择器匹配：tag / .class / .class.class / [attr] / [attr="v"] / 组合
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

function serialize(node) {
  if (node.nodeType === 3) { return node.text || ""; }
  var attrs = Object.keys(node.attrs || {}).map(function (k) {
    return " " + k + '="' + escapeAttr(node.attrs[k]) + '"';
  }).join("");
  var tag = node.tagName.toLowerCase();
  return "<" + tag + attrs + ">" + (node.children || []).map(serialize).join("") + "</" + tag + ">";
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

function makeFragment() {
  var frag = { nodeType: 11, children: [], parentNode: null };
  frag.appendChild = function (child) {
    detach(child);
    frag.children.push(child);
    child.parentNode = frag;
    return child;
  };
  return frag;
}

// 极简 HTML 解析：平衡标签栈；文本节点保留（实体解码）。
function parseHTML(html) {
  var root = { nodeType: 1, tagName: "#ROOT", attrs: {}, children: [], parentNode: null };
  var stack = [root];
  var re = /<\/?([a-zA-Z][a-zA-Z0-9-]*)((?:\s+[^<>]*?)?)\/?>/g;
  var last = 0;
  var m;
  function pushText(text) {
    if (!text) { return; }
    var node = makeTextNode(decodeEntities(text));
    node.parentNode = stack[stack.length - 1];
    stack[stack.length - 1].children.push(node);
  }
  while ((m = re.exec(html)) !== null) {
    pushText(html.slice(last, m.index));
    last = re.lastIndex;
    var raw = m[0];
    if (raw.charAt(1) === "/") {
      for (var i = stack.length - 1; i > 0; i--) {
        if (stack[i].tagName === m[1].toUpperCase()) { stack.length = i; break; }
      }
      continue;
    }
    var attrs = {};
    var attrRe = /([a-zA-Z_:][-a-zA-Z0-9_:.]*)(?:\s*=\s*"([^"]*)")?/g;
    var am;
    while ((am = attrRe.exec(m[2] || "")) !== null) {
      attrs[am[1]] = am[2] === undefined ? "" : decodeEntities(am[2]);
    }
    var el = createEl(m[1], attrs);
    el.parentNode = stack[stack.length - 1];
    stack[stack.length - 1].children.push(el);
    if (!/\/>$/.test(raw)) { stack.push(el); }
  }
  pushText(html.slice(last));
  return root.children;
}

function createEl(tagName, attrs) {
  var el = {
    nodeType: 1,
    tagName: String(tagName).toUpperCase(),
    attrs: attrs || {},
    children: [],
    parentNode: null,
    style: {},
    value: "",
    disabled: false,
    selectionStart: 0,
    selectionEnd: 0,
    offsetTop: 0,
    offsetHeight: 0,
    scrollTop: 0,
    _height: 0,
    _clientHeight: 0,
    listeners: {},
  };
  el.classList = makeClassList(el);

  Object.defineProperty(el, "scrollHeight", {
    get: function () { return el._height; },
    set: function (v) { el._height = v; },
  });
  Object.defineProperty(el, "clientHeight", {
    get: function () { return el._clientHeight; },
    set: function (v) { el._clientHeight = v; },
  });
  Object.defineProperty(el, "firstChild", {
    get: function () { return el.children.length ? el.children[0] : null; },
  });
  // className ↔ class 属性（classList 也读写同一份 attrs.class）
  Object.defineProperty(el, "className", {
    get: function () { return el.attrs["class"] || ""; },
    set: function (v) { el.attrs["class"] = String(v); },
  });
  Object.defineProperty(el, "textContent", {
    get: function () { return textOf(el); },
    set: function (v) {
      el.children = [];
      if (v !== "" && v != null) { el.children.push(makeTextNode(v)); el.children[0].parentNode = el; }
      if (insertObserver) { insertObserver(el, 0); }
    },
  });
  Object.defineProperty(el, "innerHTML", {
    get: function () { return serialize(el); },
    set: function (v) {
      el.children = parseHTML(String(v == null ? "" : v));
      el.children.forEach(function (c) { c.parentNode = el; });
      if (insertObserver) { insertObserver(el, el.children.length); }
    },
  });

  el.getAttribute = function (name) {
    var v = el.attrs[name];
    return v == null ? null : v;
  };
  el.setAttribute = function (name, value) { el.attrs[name] = String(value); };
  el.removeAttribute = function (name) { delete el.attrs[name]; };

  el.insertBefore = function (node, ref) {
    if (node && node.nodeType === 11) {
      var kids = node.children.slice();
      node.children = [];
      kids.forEach(function (k) { el.insertBefore(k, ref); });
      return node;
    }
    detach(node);
    var idx = ref ? el.children.indexOf(ref) : -1;
    if (idx < 0) { el.children.push(node); } else { el.children.splice(idx, 0, node); }
    node.parentNode = el;
    if (insertObserver) { insertObserver(el, 1); }
    return node;
  };
  el.appendChild = function (node) { return el.insertBefore(node, null); };
  el.removeChild = function (node) { detach(node); return node; };
  el.remove = function () { detach(el); };
  el.insertAdjacentHTML = function (position, html) {
    if (position !== "beforeend") { throw new Error("unsupported insertAdjacentHTML: " + position); }
    parseHTML(String(html)).forEach(function (child) {
      child.parentNode = el;
      el.children.push(child);
    });
    if (insertObserver) { insertObserver(el, 1); }
  };
  el.querySelectorAll = function (sel) {
    var out = [];
    (function walk(node) {
      (node.children || []).forEach(function (c) {
        if (matchCompound(c, sel)) { out.push(c); }
        walk(c);
      });
    })(el);
    return out;
  };
  el.querySelector = function (sel) { return el.querySelectorAll(sel)[0] || null; };
  el.closest = function (sel) {
    var node = el;
    while (node) {
      if (matchCompound(node, sel)) { return node; }
      node = node.parentNode;
    }
    return null;
  };
  el.matches = function (sel) { return matchCompound(el, sel); };
  el.addEventListener = function (type, fn) {
    if (!el.listeners[type]) { el.listeners[type] = []; }
    el.listeners[type].push(fn);
  };
  el.removeEventListener = function (type, fn) {
    if (!el.listeners[type]) { return; }
    el.listeners[type] = el.listeners[type].filter(function (f) { return f !== fn; });
  };
  el.dispatch = function (type, event) {
    (el.listeners[type] || []).forEach(function (fn) { fn(event || { target: el, currentTarget: el }); });
  };
  el.click = function () {
    (el.listeners.click || []).forEach(function (fn) {
      fn({ target: el, currentTarget: el, preventDefault: function () {}, stopPropagation: function () {} });
    });
  };
  el.focus = function () {};
  el.scrollIntoView = function () {};
  return el;
}

// ===========================================================================
// 全局 stub（document / fetch / 事件源）与响应构造
// ===========================================================================

var elements = {};
function getElement(id) {
  if (elements[id] === undefined) { elements[id] = createEl("div", { id: id }); }
  return elements[id];
}

globalThis.document = {
  getElementById: getElement,
  querySelector: function (sel) { return getElement("sel:" + sel); },
  querySelectorAll: function () { return []; },
  createElement: function (tag) { return createEl(tag, {}); },
  createDocumentFragment: makeFragment,
  documentElement: createEl("html", {}),
  body: createEl("body", {}),
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

Object.defineProperty(globalThis, "navigator", {
  value: { clipboard: { writeText: function () { return Promise.resolve(); } } },
  writable: true,
  configurable: true,
});

// 预注册各模块顶层需要的元素（与 verify-micro-web-tool-output.mjs 同集合）
[
  "screen", "prompt", "send-btn", "send-status", "welcome",
  "scroll-bottom-btn", "conversation", "stream-msg", "screen-copy-btn",
  "connection-status", "turn-status", "event-log", "log-clear-btn", "log-count",
  "theme-toggle", "tab-main-btn", "tab-log-btn", "tab-main", "tab-log",
  "tab-skills-btn", "tab-skills", "tab-mcp-btn", "tab-mcp",
  "tab-cache-btn", "tab-cache", "tab-analysis-btn", "tab-analysis",
  "tab-debug-btn", "tab-debug", "tab-about-btn", "tab-about",
  "shortcut-help", "dynamic-status", "toast-container",
  "approval-overlay", "approval-modal-title", "approval-prompt", "approval-detail",
  "detail-toggle", "approval-modal-close", "approve-btn", "deny-btn",
  "question-suggestions", "session-switch-overlay", "session-switch-modal",
  "session-switch-title", "session-switch-text", "sidebar", "sidebar-toggle",
  "sidebar-collapse-btn", "sessions-new-btn", "sessions-refresh-btn",
  "sessions-sort", "session-list", "cfg-provider", "cfg-model",
  "cfg-model-options", "cfg-model-toggle", "cfg-model-popup",
  "cfg-model-count", "cfg-reasoning", "cfg-current", "cfg-status",
  "input-row", "cfg-bar",
].forEach(function (id) { elements[id] = createEl("div", { id: id }); });

var screenEl = elements["screen"];
var conversationEl = elements["conversation"];

// 插入到 #screen 的内容会撑高 #conversation（模拟浏览器布局），用于断言
// 上滚加载时的 scrollTop 锚定补偿。
var ROW_PX = 40;
insertObserver = function (parent, count) {
  if (parent === screenEl && count > 0) { conversationEl._height += count * ROW_PX; }
};

// ---- fetch 路由：/web/api/screen 依次消费 screenQueue；其余端点返回空响应 ----
var screenQueue = [];
var fetchCalls = [];

globalThis.fetch = function (url, opts) {
  fetchCalls.push({ url: url, opts: opts || {} });
  if (url.indexOf("/web/api/screen") === 0) {
    if (!screenQueue.length) {
      throw new Error("未预期的 screen 请求（screenQueue 为空）: " + url);
    }
    var payload = screenQueue.shift();
    return Promise.resolve({ ok: true, status: 200, json: function () { return Promise.resolve(payload); } });
  }
  if (url.indexOf("/web/api/input") === 0) {
    return Promise.resolve({ ok: true, status: 200, json: function () { return Promise.resolve({ status: "queued" }); } });
  }
  return Promise.resolve({ ok: true, status: 200, json: function () { return Promise.resolve(null); } });
};

// 构造 /web/api/screen?format=json 响应：messages 只含窗口内消息（内容 m<index>）。
function screenPayload(start, end, total, opts) {
  opts = opts || {};
  var contents = opts.contents || {};
  var roles = opts.roles || {};
  var messages = [];
  for (var i = start; i < end; i++) {
    messages.push({
      role: roles[i] || (i % 2 === 0 ? "user" : "assistant"),
      content: contents[i] !== undefined ? contents[i] : ("m" + i),
    });
  }
  return {
    available: true,
    text: messages.map(function (m) { return m.content; }).join("\n"),
    messages: messages,
    message_window: { total: total, start: start, end: end, limit: end - start, has_more: start > 0 },
  };
}

function flush() { return new Promise(function (resolve) { setTimeout(resolve, 0); }); }

function rowEls() { return screenEl.querySelectorAll("[data-msg-index]"); }
function rowIndices() {
  return rowEls().map(function (el) { return parseInt(el.getAttribute("data-msg-index"), 10); });
}
function rowBodyText(index) {
  var el = screenEl.querySelector('[data-msg-index="' + index + '"]');
  if (!el) { return null; }
  // assistant 行默认 md：.msg-body 同时含 .msg-text（原文）与 .msg-md（渲染），
  // 直接读 .msg-body.textContent 会双算原文；取 .msg-text（原文）作为规范内容；
  // 其它角色正文只在 .msg-body，查不到 .msg-text 时回退 .msg-body。
  // （与 chat.js domMessageText/messageBodyEl 同源）
  var body = el.querySelector(".msg-text") || el.querySelector(".msg-body");
  return body ? body.textContent : null;
}
function screenFetchUrls() {
  return fetchCalls.filter(function (c) { return c.url.indexOf("/web/api/screen") === 0; })
    .map(function (c) { return c.url; });
}
function assertAscendingUnique(indices, label) {
  for (var i = 1; i < indices.length; i++) {
    assert.ok(indices[i] > indices[i - 1],
      label + "：索引应严格递增且不重复，实际 " + indices[i - 1] + " → " + indices[i]);
  }
}

// ===========================================================================
// 断言
// ===========================================================================

const chat = await import(pathToFileURL(path.join(WEB_DIR, "chat.js")).href);
chat.initChat(); // 绑定滚动/点击监听器（与 app.js 启动序列一致）

// ---- 1. 行 HTML 绝对索引（data-msg-index）----
var indexedRow = chat.chatMsgRowHtml("user", "hi", false, 7);
assert.ok(indexedRow.indexOf('data-msg-index="7"') >= 0, "带 index 的行应写入 data-msg-index");
assert.ok(chat.chatMsgRowHtml("user", "hi", false).indexOf("data-msg-index") < 0,
  "不带 index 时不应写 data-msg-index（保持旧调用行为）");
assert.ok(chat.chatMsgRowHtml("tool", "out", false, 3).indexOf('data-msg-index="3"') >= 0,
  "工具行也要带索引（增量替换需要定位工具行）");
assert.ok(chat.chatMsgRowHtml("reasoning", "r", false, 4).indexOf('data-msg-index="4"') >= 0,
  "推理行也要带索引");
assert.ok(chat.chatMsgRowHtml("tool", "out", false, 3).indexOf('class="msg-row msg-tool"') >= 0,
  "加索引不应改变既有 tool 行结构（工具折叠脚本依赖）");

// ---- 2. serverMessagesHtml：按绝对索引顺序生成 ----
var html = chat.serverMessagesHtml([
  { role: "user", content: "a" },
  { role: "assistant", content: "b" },
], 40);
assert.ok(html.indexOf('data-msg-index="40"') >= 0 && html.indexOf('data-msg-index="41"') >= 0,
  "窗口内消息应带绝对索引 40/41");
assert.ok(html.indexOf('data-msg-index="40"') < html.indexOf('data-msg-index="41"'), "索引顺序应与消息顺序一致");

// ---- 3. parseMessageWindow / shouldRebuildForWindow ----
assert.deepStrictEqual(chat.parseMessageWindow(undefined, 5), { start: 0, end: 5, total: 5, hasMore: false },
  "未分页响应应视为「全量 = 一个窗口」");
assert.deepStrictEqual(chat.parseMessageWindow({ total: 10, start: 7, end: 10, limit: 3, has_more: true }, 3),
  { start: 7, end: 10, total: 10, hasMore: true }, "完整 message_window 应原样采用");
var partialWin = chat.parseMessageWindow({ total: 10, start: 4 }, 3);
assert.strictEqual(partialWin.end, 7, "end 缺失时按「起始 + 返回条数」推算，避免 end < start 的空窗口");
assert.strictEqual(partialWin.hasMore, true, "has_more 缺失时按 start>0 推断");

assert.strictEqual(chat.shouldRebuildForWindow(0, 0, 60, 100), true, "首次渲染应整体重建");
assert.strictEqual(chat.shouldRebuildForWindow(60, 100, 60, 104), false, "尾部追加应走增量");
assert.strictEqual(chat.shouldRebuildForWindow(60, 100, 64, 104), false, "窗口右移应走增量（保留已加载的更早内容）");
assert.strictEqual(chat.shouldRebuildForWindow(60, 100, 40, 100), true, "窗口左移（回退/恢复）应整体重建");
assert.strictEqual(chat.shouldRebuildForWindow(60, 100, 60, 95), true, "窗口收缩（压缩）应整体重建");
assert.strictEqual(chat.shouldRebuildForWindow(60, 100, 110, 150), true,
  "新窗口与已渲染区间断开（中间消息缺失）应整体重建，避免留下空洞");

// ---- 4. 首屏：只取最新一页（不是全量）----
screenQueue.push(screenPayload(60, 100, 100));
chat.refreshScreen();
await flush();

assert.strictEqual(screenFetchUrls()[0], "/web/api/screen?format=json&msg_limit=40",
  "首屏请求应带 msg_limit 且不带 msg_before（取最新一页）");
var indices = rowIndices();
assert.strictEqual(indices.length, 40, "首屏只渲染最新一页（40 条），而不是全部 100 条");
assert.deepStrictEqual(indices, Array.from({ length: 40 }, function (_, i) { return 60 + i; }),
  "首屏窗口应为绝对索引 60..99");
assert.strictEqual(rowBodyText(60), "m60", "首条内容应来自服务端窗口");
assert.strictEqual(rowBodyText(99), "m99", "末条内容应来自服务端窗口");
assert.strictEqual(screenEl.querySelector('[data-msg-index="0"]'), null, "更早的消息不应出现在首屏");

var hint = screenEl.querySelector(".conv-load-older");
assert.ok(hint, "还有更早消息时应显示顶部提示行");
assert.strictEqual(hint.textContent, "↑ 上滚加载更早消息", "空闲提示文案");
assert.strictEqual(screenEl.children[0], hint, "提示行应位于对话区最上方");

// ---- 5. 尾部增量：新 turn 追加不重建更早的行 ----
screenQueue.push(screenPayload(60, 104, 104));
chat.refreshScreen();
await flush();

indices = rowIndices();
assert.strictEqual(indices.length, 44, "尾部追加后行数 = 原 40 + 新 4");
assert.deepStrictEqual(indices, Array.from({ length: 44 }, function (_, i) { return 60 + i; }),
  "尾部追加后索引应为 60..103");
assertAscendingUnique(indices, "尾部增量");
assert.strictEqual(rowBodyText(103), "m103", "新消息应渲染在末尾");

// 消息内容变化（流式回合中最后一条被改写）应就地更新，且不重复
screenQueue.push(screenPayload(60, 104, 104, { contents: { 102: "m102-edited" } }));
chat.refreshScreen();
await flush();
assert.strictEqual(rowBodyText(102), "m102-edited", "窗口内内容变化应被替换");
assert.strictEqual(rowIndices().filter(function (i) { return i === 102; }).length, 1, "同一索引不应出现两行");

// ---- 6. 窗口右移：保留已渲染的更早部分（游标不回退）----
var row60 = screenEl.querySelector('[data-msg-index="60"]');
var row64 = screenEl.querySelector('[data-msg-index="64"]');
row60._probe = "keep60";
row64._probe = "drop64";

screenQueue.push(screenPayload(64, 108, 108));
chat.refreshScreen();
await flush();

indices = rowIndices();
assert.deepStrictEqual(indices, Array.from({ length: 48 }, function (_, i) { return 60 + i; }),
  "窗口右移后应保留 60..63 并追加 64..107");
assert.strictEqual(screenEl.querySelector('[data-msg-index="60"]')._probe, "keep60",
  "早于窗口的行（60 < 64）不应被重建");
assert.strictEqual(screenEl.querySelector('[data-msg-index="64"]')._probe, undefined,
  "窗口内的行（64 >= 64）应来自最新快照（节点被替换）");
assert.strictEqual(chat.getUiState(), "idle", "刷新不应改变按钮状态机");

// ---- 7. 上滚懒加载更早消息：游标 / 前插顺序 / 滚动锚定 ----
var heightBefore = conversationEl.scrollHeight;
screenQueue.push(screenPayload(20, 60, 108));
conversationEl.scrollTop = 0;
conversationEl.dispatch("scroll");

assert.strictEqual(screenEl.querySelector(".conv-load-older").textContent, "加载更早消息…",
  "请求进行中提示行应显示加载态");
var olderUrl = screenFetchUrls().pop();
assert.strictEqual(olderUrl, "/web/api/screen?format=json&msg_limit=40&msg_before=60",
  "上滚请求应以已加载起始索引为游标（msg_before=60）");

await flush();

indices = rowIndices();
assert.deepStrictEqual(indices, Array.from({ length: 88 }, function (_, i) { return 20 + i; }),
  "更早一页应前插到窗口之前：20..107");
assertAscendingUnique(indices, "上滚加载");
assert.strictEqual(screenEl.children[0].classList.contains("conv-load-older"), true,
  "前插后提示行仍应位于最上方（在更早消息之前）");
assert.strictEqual(rowBodyText(20), "m20", "前插页内容应渲染");
assert.strictEqual(rowBodyText(59), "m59", "前插页末尾应紧邻原窗口起点");
assert.strictEqual(conversationEl.scrollTop, conversationEl.scrollHeight - heightBefore,
  "前插后应补偿 scrollTop（插入高度差），保持阅读位置不跳动");
screenEl.querySelector('[data-msg-index="20"]')._probe = "keep20";

// ---- 8. 取到最早一条：提示行移除且不再请求 ----
screenQueue.push(screenPayload(0, 20, 108));
conversationEl.scrollTop = 0;
conversationEl.dispatch("scroll");
await flush();

indices = rowIndices();
assert.deepStrictEqual(indices, Array.from({ length: 108 }, function (_, i) { return i; }),
  "取到最早一条后应包含 0..107");
assert.strictEqual(screenEl.querySelector(".conv-load-older"), null,
  "已到最早一条时不应再有提示行");

var fetchCountAtOldest = screenFetchUrls().length;
conversationEl.scrollTop = 0;
conversationEl.dispatch("scroll");
await flush();
assert.strictEqual(screenFetchUrls().length, fetchCountAtOldest,
  "已到最早一条后继续上滚不应再发请求");

// ---- 9. 已加载历史 + 实时刷新：不丢历史、不重复 ----
screenQueue.push(screenPayload(68, 108, 108));
chat.refreshScreen();
await flush();

indices = rowIndices();
assert.strictEqual(indices.length, 108, "实时刷新不应重复插入已加载的更早消息");
assert.deepStrictEqual(indices, Array.from({ length: 108 }, function (_, i) { return i; }));
assert.strictEqual(screenEl.querySelector('[data-msg-index="20"]')._probe, "keep20",
  "早于刷新窗口的行（20 < 68）应保留原节点");

// ---- 10. pending 气泡：未确认时保留且始终排在服务端行之后；确认后移除 ----
elements["prompt"].value = "hello";
elements["send-btn"].click();
await flush();

var pendingRow = screenEl.querySelector(".msg-row.msg-pending");
assert.ok(pendingRow, "发送后应立即出现 pending 气泡");
assert.strictEqual(screenEl.children[screenEl.children.length - 1], pendingRow,
  "pending 气泡应排在所有服务端行之后");
assert.strictEqual(pendingRow.querySelector(".msg-body").textContent, "hello");

// 服务端窗口尚未包含该 prompt：pending 保留
screenQueue.push(screenPayload(68, 108, 108));
chat.refreshScreen();
await flush();
pendingRow = screenEl.querySelector(".msg-row.msg-pending");
assert.ok(pendingRow, "服务端未确认时 pending 气泡应保留");
assert.strictEqual(screenEl.children[screenEl.children.length - 1], pendingRow,
  "增量刷新后 pending 气泡仍应在最末（服务端行插在它之前）");
assert.strictEqual(rowIndices().length, 108, "增量刷新不应把服务端行插到 pending 之后或重复渲染");

// 服务端窗口包含该 prompt：pending 释放，由服务端行接管
screenQueue.push(screenPayload(68, 109, 109, { contents: { 108: "hello" }, roles: { 108: "user" } }));
chat.refreshScreen();
await flush();
assert.strictEqual(screenEl.querySelector(".msg-row.msg-pending"), null, "服务端确认后应释放 pending 气泡");
assert.strictEqual(rowBodyText(108), "hello", "确认后的消息应来自服务端行");
assert.strictEqual(screenEl.children[screenEl.children.length - 1],
  screenEl.querySelector('[data-msg-index="108"]'), "确认后的服务端行应位于最末");

// ---- 11. 游标守卫：服务端返回重叠/断开的一页时不重复插入，并停止继续上滚 ----
// Ctrl+K 清空本地视图并重置窗口状态（loadedStart/loadedEnd 归零）
elements["prompt"].dispatch("keydown", { key: "k", ctrlKey: true, preventDefault: function () {} });
assert.strictEqual(rowIndices().length, 0, "Ctrl+K 应清空对话区");
assert.strictEqual(screenEl.querySelector(".conv-load-older"), null, "Ctrl+K 后不应残留提示行");

screenQueue.push(screenPayload(68, 109, 109));
chat.refreshScreen();
await flush();
indices = rowIndices();
assert.deepStrictEqual(indices, Array.from({ length: 41 }, function (_, i) { return 68 + i; }),
  "Ctrl+K 后刷新应重建最新窗口（68..108）");

// 服务端返回与已渲染区间重叠的一页：start(60) < cursor(68) 但 end(80) != cursor(68)
screenQueue.push(screenPayload(60, 80, 109));
conversationEl.scrollTop = 0;
conversationEl.dispatch("scroll");
await flush();
assert.deepStrictEqual(rowIndices(), indices, "重叠页不应插入（不产生重复行）");
assert.strictEqual(screenEl.querySelector(".conv-load-older"), null,
  "游标异常后应停止继续上滚（移除提示行）");

var fetchCountGuarded = screenFetchUrls().length;
conversationEl.scrollTop = 0;
conversationEl.dispatch("scroll");
await flush();
assert.strictEqual(screenFetchUrls().length, fetchCountGuarded, "游标异常后不应继续请求更早消息");

// ---- 12. 会话复制：窗口只覆盖一部分时取服务端完整 transcript ----
var copied = "";
globalThis.navigator.clipboard.writeText = function (t) { copied = t; return Promise.resolve(); };

// 当前窗口为 68..109 / 总数 109（只覆盖一部分）→ 复制应请求全量 transcript
var copyBtn = elements["screen-copy-btn"];
var fetchCountBeforeCopy = screenFetchUrls().length;
screenQueue.push({ available: true, text: "full-0\nfull-1\nfull-2" });
copyBtn.dispatch("click", { target: copyBtn });
assert.strictEqual(screenFetchUrls()[screenFetchUrls().length - 1], "/web/api/screen?format=json",
  "窗口不完整时复制应取全量 transcript（不带 msg_limit）");
assert.strictEqual(screenFetchUrls().length, fetchCountBeforeCopy + 1, "复制应只多发一次请求");
assert.strictEqual(copied, "", "全量 transcript 取回前不应写入部分内容");
await flush();
assert.strictEqual(copied, "full-0\nfull-1\nfull-2", "复制内容应为服务端完整 transcript");

// 上滚加载到最早一条（窗口覆盖全部消息）→ 复制走 DOM 收集，不再请求服务端
screenQueue.push(screenPayload(0, 109, 109));
chat.refreshScreen();
await flush();
assert.strictEqual(rowIndices().length, 109, "窗口覆盖全部消息时应渲染全部行");
var fetchCountFullWindow = screenFetchUrls().length;
copied = "";
copyBtn.dispatch("click", { target: copyBtn });
assert.strictEqual(screenFetchUrls().length, fetchCountFullWindow,
  "窗口已覆盖全部消息时复制不应再请求服务端");
assert.strictEqual(copied, Array.from({ length: 109 }, function (_, i) { return "m" + i; }).join("\n\n"),
  "窗口完整时复制按 DOM 顺序收集（既有格式）");

console.log("verify-micro-web-msg-window: 全部断言通过");
console.log("  - 首屏只渲染最新一页（msg_limit=40），不随会话总 turn 数增长");
console.log("  - 尾部增量替换 + 窗口右移保留已加载历史（游标不回退、不重复）");
console.log("  - 上滚以 msg_before 游标前插更早消息，并补偿 scrollTop 锚定");
console.log("  - pending 气泡与服务端窗口的确认/保留关系正确");
console.log("  - 游标异常（重叠/断开页）时不重复插入并停止继续上滚");
console.log("  - 会话复制在窗口不完整时取服务端全量 transcript，完整时不发额外请求");
