// 行为验证：aicli micro web client 对话页签「消息过滤面板」（角色多选 + 正文搜索 + 计数）。
// 运行：node scripts/verify-micro-web-msg-filter.mjs（无需浏览器；见 docs/aicli/web-testing.md）
// 覆盖：
//   1. 静态契约：index.html 面板结构（#msg-filter 位于 #conversation 内、#screen 之前，
//      搜索输入默认收起，清除按钮默认禁用）、style.css 的 sticky 居中吸附、app.js 入口接线
//   2. msg-filter.js：角色多选（aria-pressed / 查询串 / 顺序稳定）、搜索图标展开收起、
//      输入去抖 300ms 合并、回车立即提交、Esc 先清词再收起、清除按钮、面板点击不冒泡
//   3. 服务端过滤接线（chat.js）：refreshScreen / loadOlderMessages / 复制 都带 roles/q；
//      条件变化整块重建并作废在途的更早消息分页请求（代次守卫）
//   4. 计数与空态：message_window.total 是匹配数、unfiltered_total 是过滤前总数；
//      0 命中显示「没有匹配的消息（已过滤）」而不是空白
import assert from "node:assert";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_DIR = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web");
const JS_DIR = path.join(WEB_DIR, "js");

// ===========================================================================
// 迷你 DOM（与 verify-micro-web-msg-window.mjs 同一套极简实现）
// 过滤面板要断言真实节点（chip 的 aria-pressed、搜索框 display、计数文本），
// 字符串级 stub 不够用。
// ===========================================================================

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
    focusCount: 0,
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
  Object.defineProperty(el, "className", {
    get: function () { return el.attrs["class"] || ""; },
    set: function (v) { el.attrs["class"] = String(v); },
  });
  Object.defineProperty(el, "textContent", {
    get: function () { return textOf(el); },
    set: function (v) {
      el.children = [];
      if (v !== "" && v != null) { el.children.push(makeTextNode(v)); el.children[0].parentNode = el; }
    },
  });
  Object.defineProperty(el, "innerHTML", {
    get: function () { return serialize(el); },
    set: function (v) {
      el.children = parseHTML(String(v == null ? "" : v));
      el.children.forEach(function (c) { c.parentNode = el; });
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
    var ev = event || { target: el, currentTarget: el };
    (el.listeners[type] || []).forEach(function (fn) { fn(ev); });
    return ev;
  };
  el.click = function () {
    return el.dispatch("click", {
      target: el,
      currentTarget: el,
      preventDefault: function () {},
      stopPropagation: function () {},
    });
  };
  el.focus = function () { el.focusCount++; };
  el.scrollIntoView = function () {};
  return el;
}

// ===========================================================================
// 全局 stub（document / fetch / 事件源）与元素注册
// ===========================================================================

const INDEX_HTML = fs.readFileSync(path.join(WEB_DIR, "index.html"), "utf8");
const STYLE_CSS = fs.readFileSync(path.join(WEB_DIR, "style.css"), "utf8");
const APP_JS = fs.readFileSync(path.join(WEB_DIR, "app.js"), "utf8");
const CHAT_JS = fs.readFileSync(path.join(JS_DIR, "chat.js"), "utf8");
const STREAM_JS = fs.readFileSync(path.join(JS_DIR, "stream.js"), "utf8");

var elements = {};
function getElement(id) {
  if (elements[id] === undefined) { elements[id] = createEl("div", { id: id }); }
  return elements[id];
}

// index.html 里出现的 id 全部注册（面板与各模块顶层查询的元素同源），
// 未出现的按需惰性创建（与浏览器里「模块自建节点」等价）。
(INDEX_HTML.match(/\bid="([\w-]+)"/g) || []).forEach(function (raw) {
  var id = raw.slice(4, -1);
  elements[id] = createEl("div", { id: id });
});
[
  "screen", "prompt", "send-btn", "send-status", "welcome",
  "scroll-bottom-btn", "conversation", "stream-msg", "screen-copy-btn",
  "connection-status", "turn-status", "event-log", "log-clear-btn", "log-count",
  "theme-toggle", "toast-container", "dynamic-status",
].forEach(function (id) { if (!elements[id]) { elements[id] = createEl("div", { id: id }); } });

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

var clipboardWrites = [];
Object.defineProperty(globalThis, "navigator", {
  value: {
    clipboard: {
      writeText: function (text) { clipboardWrites.push(String(text)); return Promise.resolve(); },
    },
  },
  writable: true,
  configurable: true,
});

// ---- fetch 路由：/web/api/screen 依次消费 screenQueue ----
// 队列元素：{ payload } 立即返回；{ deferred: true } 由测试显式 resolve（用于验证
// 过滤条件变化后在途分页请求被作废）。
var screenQueue = [];
var fetchCalls = [];

function queuePayload(payload) { screenQueue.push({ payload: payload }); }
function queueDeferred() {
  var entry = { deferred: true, resolved: false };
  entry.promise = new Promise(function (resolve) {
    entry.resolve = function (payload) { entry.resolved = true; resolve(payload); };
  });
  screenQueue.push(entry);
  return entry;
}

globalThis.fetch = function (url, opts) {
  fetchCalls.push({ url: url, opts: opts || {} });
  if (url.indexOf("/web/api/screen") === 0) {
    if (!screenQueue.length) {
      throw new Error("未预期的 screen 请求（screenQueue 为空）: " + url);
    }
    var entry = screenQueue.shift();
    var payloadPromise = entry.deferred ? entry.promise : Promise.resolve(entry.payload);
    return payloadPromise.then(function (payload) {
      return { ok: true, status: 200, json: function () { return Promise.resolve(payload); } };
    });
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
  var win = { total: total, start: start, end: end, limit: end - start, has_more: start > 0 };
  if (typeof opts.unfilteredTotal === "number") { win.unfiltered_total = opts.unfilteredTotal; }
  return {
    available: true,
    text: messages.map(function (m) { return m.content; }).join("\n"),
    messages: messages,
    message_window: win,
  };
}

// 过滤后的响应：只有匹配消息（绝对索引保留），total = 匹配数，unfiltered_total = 过滤前总数。
function filteredPayload(matched, unfilteredTotal) {
  return {
    available: true,
    text: matched.map(function (m) { return m.content; }).join("\n"),
    messages: matched,
    message_window: {
      total: matched.length,
      start: 0,
      end: matched.length,
      limit: matched.length,
      has_more: false,
      unfiltered_total: unfilteredTotal,
    },
  };
}

function flush() { return new Promise(function (resolve) { setTimeout(resolve, 0); }); }
function sleep(ms) { return new Promise(function (resolve) { setTimeout(resolve, ms); }); }

function screenFetchUrls() {
  return fetchCalls.filter(function (c) { return c.url.indexOf("/web/api/screen") === 0; })
    .map(function (c) { return c.url; });
}
function rowIndices() {
  return screenEl.querySelectorAll("[data-msg-index]").map(function (el) {
    return parseInt(el.getAttribute("data-msg-index"), 10);
  });
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

var screenEl = getElement("screen");
var conversationEl = getElement("conversation");
var filterRootEl = getElement("msg-filter");
var filterRolesEl = getElement("msg-filter-roles");
var filterSearchBtn = getElement("msg-filter-search-btn");
var filterSearchEl = getElement("msg-filter-search");
var filterCountEl = getElement("msg-filter-count");
var filterClearEl = getElement("msg-filter-clear");

// ===========================================================================
// 1. 静态契约：面板结构 / 样式吸附 / 入口接线
// ===========================================================================

// ---- 1.1 index.html：面板在 #conversation 内、#screen 之前，控件齐全 ----
var convIdx = INDEX_HTML.indexOf('id="conversation"');
var filterIdx = INDEX_HTML.indexOf('id="msg-filter"');
var screenIdx = INDEX_HTML.indexOf('id="screen"');
assert.ok(convIdx >= 0 && filterIdx > convIdx, "面板应位于 #conversation 内（sticky 相对滚动容器吸附）");
assert.ok(screenIdx > filterIdx, "面板应在 #screen 之前（作为消息区第一个子节点吸附在顶部）");
["msg-filter-roles", "msg-filter-search-btn", "msg-filter-search", "msg-filter-count", "msg-filter-clear"]
  .forEach(function (id) {
    assert.ok(INDEX_HTML.indexOf('id="' + id + '"') > filterIdx, "面板应包含 #" + id);
  });
assert.ok(/id="msg-filter-search"[^>]*style="display:none"/.test(INDEX_HTML),
  "搜索输入框默认收起（点击搜索图标才展开）");
assert.ok(/id="msg-filter-search-btn"[^>]*aria-expanded="false"/.test(INDEX_HTML),
  "搜索图标应带 aria-expanded 初值 false");
assert.ok(/id="msg-filter-clear"[^>]*disabled/.test(INDEX_HTML), "清除按钮默认禁用（无过滤条件时不可点）");
assert.ok(/id="msg-filter-roles"[^>]*role="group"/.test(INDEX_HTML), "角色多选区应带 role=group（可访问性）");

// ---- 1.2 style.css：sticky 吸附 + 居中 + 空白不拦截点击 ----
var cssBlock = STYLE_CSS.slice(STYLE_CSS.indexOf("#conversation .msg-filter {"),
  STYLE_CSS.indexOf("/* ---- 长会话窗口化"));
assert.ok(cssBlock.length > 0, "应存在 .msg-filter 样式块（位于长会话提示行样式之前）");
assert.ok(/position:\s*sticky/.test(cssBlock) && /top:\s*0/.test(cssBlock), "面板应 sticky 吸附在滚动容器顶部");
assert.ok(/justify-content:\s*center/.test(cssBlock), "面板应水平居中");
assert.ok(/\.msg-filter\s*\{[^}]*pointer-events:\s*none/.test(cssBlock.replace(/#conversation /g, "")),
  "外层 wrapper 应 pointer-events:none（两侧空白不拦截消息点击）");
assert.ok(/\.msg-filter-panel\s*\{[^}]*pointer-events:\s*auto/.test(cssBlock.replace(/#conversation /g, "")),
  "面板本体应恢复 pointer-events:auto");
assert.ok(/\.msg-filter-chip\[aria-pressed="true"\]/.test(cssBlock), "选中态应通过 aria-pressed 着色（不重建节点）");
assert.ok(/@media \(max-width: 767px\)[\s\S]*\.msg-filter\s*\{[^}]*padding-top/.test(cssBlock),
  "窄屏应下移面板避让会话复制按钮");

// ---- 1.3 app.js / chat.js / stream.js 接线 ----
assert.ok(/import\s*\{[^}]*initMsgFilter[^}]*\}\s*from\s*"\.\/js\/msg-filter\.js"/.test(APP_JS),
  "app.js 应 import initMsgFilter");
assert.ok(/initChat\(\)[\s\S]{0,200}initMsgFilter\(\)/.test(APP_JS), "app.js 应在 initChat() 之后初始化过滤面板");
assert.ok(/setFilterChangeHandler\(onFilterChange\)/.test(CHAT_JS), "chat.js 应注册过滤条件变化回调");
var filterQueryUses = (CHAT_JS.match(/filterQueryString\(\)/g) || []).length;
assert.ok(filterQueryUses >= 3,
  "refreshScreen / loadOlderMessages / 复制 三处请求都应带过滤条件，实际 " + filterQueryUses + " 处");
assert.ok(/gen !== filterGen/.test(CHAT_JS), "更早消息分页应有代次守卫（过滤变化后作废在途请求）");
assert.ok(/filterAllowsRole\("user"\)/.test(CHAT_JS), "过滤排除用户消息时不应渲染本地乐观回显");
assert.ok(/isFilterActive\(\)/.test(STREAM_JS), "stream.js 应感知过滤状态（排除的流式角色不显示气泡）");

// ===========================================================================
// 模块加载（与 app.js 启动顺序一致：先 chat 后过滤面板）
// ===========================================================================

const msgFilter = await import(pathToFileURL(path.join(JS_DIR, "msg-filter.js")).href);
const chat = await import(pathToFileURL(path.join(JS_DIR, "chat.js")).href);

chat.initChat();
msgFilter.initMsgFilter();

// 角色多选按钮点击（事件委托在 #msg-filter-roles 上，事件对象需带 target）
function clickChip(role) {
  var chip = filterRolesEl.querySelector('[data-filter-role="' + role + '"]');
  assert.ok(chip, "应存在角色按钮 " + role);
  filterRolesEl.dispatch("click", {
    target: chip,
    currentTarget: filterRolesEl,
    preventDefault: function () {},
    stopPropagation: function () {},
  });
  return chip;
}

function chipState(role) {
  var chip = filterRolesEl.querySelector('[data-filter-role="' + role + '"]');
  return chip ? chip.getAttribute("aria-pressed") : null;
}

function pressSearchKey(key) {
  filterSearchEl.dispatch("keydown", {
    key: key,
    target: filterSearchEl,
    currentTarget: filterSearchEl,
    preventDefault: function () {},
    stopPropagation: function () {},
  });
}

function typeSearch(text) {
  filterSearchEl.value = text;
  filterSearchEl.dispatch("input", { target: filterSearchEl, currentTarget: filterSearchEl });
}

// ===========================================================================
// 2. 过滤面板行为（msg-filter.js）
// ===========================================================================

// ---- 2.1 初始状态：8 个角色按钮、未选中、搜索收起、清除禁用、无计数 ----
var chips = filterRolesEl.querySelectorAll(".msg-filter-chip");
assert.strictEqual(chips.length, 8, "角色多选按钮应有 8 个（user/assistant/reasoning/tool/system/command/diagnostic/runtime）");
assert.deepStrictEqual(chips.map(function (c) { return c.getAttribute("data-filter-role"); }),
  ["user", "assistant", "reasoning", "tool", "system", "command", "diagnostic", "runtime"],
  "角色顺序应与后端已知角色集合一致（查询串稳定，便于日志比对）");
chips.forEach(function (c) {
  assert.strictEqual(c.getAttribute("aria-pressed"), "false", "初始应全部未选中：" + c.getAttribute("data-filter-role"));
});
assert.deepStrictEqual(msgFilter.getFilterState(), { roles: [], query: "", active: false, searchOpen: false },
  "初始过滤状态应为空");
assert.strictEqual(msgFilter.filterQueryString(), "", "未过滤时查询串为空（URL 不变，历史行为兼容）");
assert.strictEqual(msgFilter.isFilterActive(), false, "初始不应处于过滤态");
assert.strictEqual(msgFilter.filterAllowsRole("tool"), true, "未过滤时所有角色都可见");
assert.strictEqual(filterSearchEl.style.display, "none", "搜索输入框初始收起");
assert.strictEqual(filterSearchBtn.getAttribute("aria-expanded"), "false", "搜索图标 aria-expanded 初始 false");
assert.strictEqual(filterClearEl.disabled, true, "无过滤条件时清除按钮禁用");
assert.strictEqual(filterCountEl.textContent, "", "尚无服务端计数时不显示文案");

// ---- 2.2 角色多选：aria-pressed / 查询串 / 顺序稳定 / 触发服务端刷新 ----
queuePayload(screenPayload(0, 6, 6));
clickChip("tool");
assert.strictEqual(chipState("tool"), "true", "点击后按钮应进入选中态");
assert.deepStrictEqual(msgFilter.getFilterState().roles, ["tool"], "选中角色应进入状态");
assert.strictEqual(msgFilter.filterQueryString(), "&roles=tool", "选中角色应编码进查询串");
assert.strictEqual(msgFilter.isFilterActive(), true, "选中角色即处于过滤态");
assert.strictEqual(msgFilter.filterAllowsRole("user"), false, "未选中的角色不应显示");
assert.strictEqual(msgFilter.filterAllowsRole("tool"), true, "选中的角色应显示");
assert.strictEqual(filterClearEl.disabled, false, "有过滤条件时清除按钮可用");
assert.strictEqual(filterCountEl.textContent, "查询中…", "条件变化到响应返回前应显示查询中（不展示旧计数）");
await flush();
var toolUrl = screenFetchUrls().pop();
assert.ok(/msg_limit=40/.test(toolUrl) && /&roles=tool$/.test(toolUrl),
  "过滤刷新应带 roles 且保留 msg_limit（服务端过滤 + 分页），实际 " + toolUrl);

queuePayload(screenPayload(0, 6, 6));
clickChip("user");
assert.deepStrictEqual(msgFilter.getFilterState().roles, ["user", "tool"],
  "多选顺序应固定按角色词表（不随点击顺序漂移）");
assert.strictEqual(msgFilter.filterQueryString(), "&roles=user%2Ctool", "多角色以逗号连接并做 URL 编码");
await flush();

queuePayload(screenPayload(0, 6, 6));
clickChip("tool");
assert.deepStrictEqual(msgFilter.getFilterState().roles, ["user"], "再次点击应取消选中");
assert.strictEqual(msgFilter.filterQueryString(), "&roles=user", "取消选中后查询串应同步收敛");
await flush();

// 取消全部角色 → 回到未过滤（URL 不带任何过滤参数）
queuePayload(screenPayload(0, 6, 6));
clickChip("user");
assert.strictEqual(msgFilter.isFilterActive(), false, "取消全部角色后不应处于过滤态");
assert.strictEqual(msgFilter.filterQueryString(), "", "无过滤条件时不应带查询串");
assert.strictEqual(msgFilter.filterAllowsRole("user"), true, "取消过滤后所有角色恢复可见");
await flush();

// ---- 2.3 搜索图标：点击展开输入框（默认收起），再点收起 ----
filterSearchBtn.click();
assert.strictEqual(msgFilter.getFilterState().searchOpen, true, "点击搜索图标应展开输入框");
assert.strictEqual(filterSearchEl.style.display, "inline-block", "展开后输入框应可见");
assert.strictEqual(filterSearchBtn.getAttribute("aria-expanded"), "true", "展开后 aria-expanded 应为 true");
assert.ok(filterSearchBtn.classList.contains("active"), "展开时图标应有 active 高亮");
assert.ok(filterSearchEl.focusCount > 0, "展开后应聚焦输入框（直接可打字）");

// ---- 2.4 输入去抖：连续打字只提交一次（300ms 停顿后）----
var beforeDebounce = screenFetchUrls().length;
typeSearch("Ans");
await sleep(100);
typeSearch("Answer");
assert.strictEqual(msgFilter.getFilterState().query, "", "去抖窗口内不应提交（避免每个按键一次请求）");
queuePayload(screenPayload(0, 6, 6)); // 停顿后去抖到期才发请求，先排好响应
await sleep(400);
assert.strictEqual(msgFilter.getFilterState().query, "Answer", "停顿后应提交搜索词");
assert.strictEqual(screenFetchUrls().length - beforeDebounce, 1, "连续输入合并为一次请求");
assert.strictEqual(msgFilter.filterQueryString(), "&q=Answer", "搜索词应编码进查询串");
assert.strictEqual(msgFilter.filterAllowsRole("tool"), true, "只搜索正文（未选角色）时所有角色仍可见");
await flush();

// ---- 2.5 回车立即提交（不等去抖）；Esc 先清词、再收起 ----
queuePayload(screenPayload(0, 6, 6));
typeSearch("xyz");
pressSearchKey("Enter");
assert.strictEqual(msgFilter.getFilterState().query, "xyz", "回车应立即提交（无需等去抖）");
assert.strictEqual(msgFilter.filterQueryString(), "&q=xyz", "立即提交后查询串同步更新");
await flush();

queuePayload(screenPayload(0, 6, 6));
pressSearchKey("Escape");
assert.strictEqual(msgFilter.getFilterState().query, "", "有搜索词时 Esc 先清空搜索词");
assert.strictEqual(filterSearchEl.value, "", "Esc 清词应同时清空输入框");
assert.strictEqual(msgFilter.getFilterState().searchOpen, true, "Esc 清词后输入框保持展开（还需再按一次才收起）");
await flush();

pressSearchKey("Escape");
assert.strictEqual(msgFilter.getFilterState().searchOpen, false, "无搜索词时 Esc 应收起输入框");
assert.strictEqual(filterSearchEl.style.display, "none", "收起后输入框不可见");
assert.strictEqual(filterSearchBtn.getAttribute("aria-expanded"), "false", "收起后 aria-expanded 应为 false");

// ---- 2.6 清除按钮：一次清掉角色 + 搜索词并收起输入框 ----
queuePayload(screenPayload(0, 6, 6));
clickChip("tool");
await flush();
queuePayload(screenPayload(0, 6, 6));
filterSearchBtn.click();
typeSearch("abc");
pressSearchKey("Enter");
await flush();
assert.strictEqual(msgFilter.isFilterActive(), true, "清除前应处于过滤态");

queuePayload(screenPayload(0, 6, 6));
var beforeClear = screenFetchUrls().length;
filterClearEl.click();
assert.deepStrictEqual(msgFilter.getFilterState(), { roles: [], query: "", active: false, searchOpen: false },
  "清除应重置角色与搜索词并收起输入框");
assert.strictEqual(filterSearchEl.value, "", "清除应清空输入框");
assert.strictEqual(filterClearEl.disabled, true, "清除后按钮应回到禁用态");
assert.strictEqual(msgFilter.filterQueryString(), "", "清除后请求不再带过滤条件");
assert.strictEqual(screenFetchUrls().length - beforeClear, 1, "清除只触发一次刷新（不重复请求）");
await flush();

// ---- 2.7 面板内的点击不冒泡到对话区（避免命中消息行的复制/折叠委托）----
var stopped = false;
filterRootEl.dispatch("click", {
  target: filterRootEl,
  currentTarget: filterRootEl,
  preventDefault: function () {},
  stopPropagation: function () { stopped = true; },
});
assert.ok(stopped, "面板内点击应阻止冒泡（对话区委托不会误判为消息行操作）");

// ===========================================================================
// 3. 服务端过滤接线（chat.js）：请求参数 / 计数 / 空态 / 分页 / 复制
// ===========================================================================

// 视口高度固定为「内容高于一屏」：避免 maybeFillViewport 自动补页吃掉用例里
// 预排的响应（真实浏览器里 40 条消息同样会撑出滚动条）。
conversationEl._height = 4000;
conversationEl._clientHeight = 400;

// ---- 3.1 过滤刷新：带 roles、按匹配窗口重建、计数为「匹配 N / 共 M 条」----
queuePayload(filteredPayload([
  { role: "tool", content: "shell output" },
  { role: "tool", content: "read file" },
], 6));
clickChip("tool");
await flush();
assert.strictEqual(filterCountEl.textContent, "匹配 2 / 共 6 条",
  "message_window.total 是匹配数、unfiltered_total 是过滤前总数");
assert.deepStrictEqual(rowIndices(), [0, 1], "过滤后只渲染匹配的消息（索引在过滤后的序列上）");
assert.strictEqual(rowBodyText(0), "shell output", "匹配消息内容应来自服务端过滤结果");
assert.ok(screenEl.textContent.indexOf("q1") < 0, "未匹配的旧行不应残留（过滤改变索引语义，必须整块重建）");
var filteredUrl = screenFetchUrls().pop();
assert.ok(/msg_limit=40/.test(filteredUrl) && /&roles=tool$/.test(filteredUrl),
  "过滤刷新仍走分页参数（服务端过滤 + 搜索结果分页），实际 " + filteredUrl);

// ---- 3.2 0 命中：显示「没有匹配的消息（已过滤）」并同步计数，而不是空白 ----
queuePayload(filteredPayload([], 6));
filterSearchBtn.click();
typeSearch("no-such-text");
pressSearchKey("Enter");
await flush();
assert.strictEqual(screenEl.textContent, "没有匹配的消息（已过滤）", "0 命中应给出明确空态文案");
assert.strictEqual(filterCountEl.textContent, "匹配 0 / 共 6 条", "0 命中也要显示匹配计数（用户知道是过滤而非无消息）");

// ---- 3.3 上滚分页：带 msg_before 游标 + 过滤条件；条件变化后作废在途请求 ----
queuePayload(screenPayload(60, 100, 100, { unfilteredTotal: 300 }));
chat.refreshScreen();
await flush();
assert.strictEqual(rowIndices().length, 40, "过滤态首屏仍只取最新一页（msg_limit=40）");
assert.strictEqual(filterCountEl.textContent, "匹配 100 / 共 300 条", "分页窗口的计数口径与过滤一致");

var deferredOlder = queueDeferred();
chat.loadOlderMessages();
await flush();
var olderUrl = screenFetchUrls().pop();
assert.ok(/msg_before=60/.test(olderUrl) && /&roles=tool/.test(olderUrl),
  "上滚分页应同时带游标与过滤条件，实际 " + olderUrl);

// 在途分页请求返回前改变过滤条件：该页属于旧条件（游标语义已变），必须丢弃。
queuePayload(filteredPayload([{ role: "tool", content: "only-match" }], 300));
clickChip("tool"); // 取消选中 → 过滤条件变化（仍由搜索词保持激活）
await flush();
assert.deepStrictEqual(rowIndices(), [0], "条件变化后按新条件整块重建");
deferredOlder.resolve(screenPayload(20, 60, 100, { unfilteredTotal: 300 }));
await flush();
assert.deepStrictEqual(rowIndices(), [0], "过期分页响应不应插入（否则新结果集里会混入旧条件的消息）");

// ---- 3.4 复制：过滤态下复制的是「当前看到的消息集合」----
// 先把窗口推到「只覆盖一部分」（start>0），复制才会走服务端全量取回路径。
queuePayload(screenPayload(60, 100, 100, { unfilteredTotal: 300 }));
chat.refreshScreen();
await flush();

clipboardWrites.length = 0;
queuePayload({
  available: true,
  text: "tool> shell output",
  messages: [{ role: "tool", content: "shell output" }],
  message_window: { total: 1, start: 0, end: 1, limit: 1, has_more: false, unfiltered_total: 300 },
});
getElement("screen-copy-btn").click();
await flush();
await flush();
var copyUrl = screenFetchUrls().pop();
// 同 msg-window：复制显式带 msg_limit=all 取完整文本，过滤条件一并带上。
assert.strictEqual(copyUrl, "/web/api/screen?format=json&msg_limit=all&q=no-such-text",
  "窗口只覆盖一部分时复制应显式取服务端完整文本（msg_limit=all）并带上过滤条件");
assert.ok(clipboardWrites.length > 0 && clipboardWrites[clipboardWrites.length - 1].indexOf("shell output") >= 0,
  "复制内容应来自过滤后的 transcript，实际 " + JSON.stringify(clipboardWrites));

// ---- 3.5 回归：清除过滤后请求不再带参数、计数显示「共 M 条」----
queuePayload(screenPayload(0, 6, 6));
filterClearEl.click();
await flush();
var clearUrl = screenFetchUrls().pop();
assert.ok(clearUrl.indexOf("roles=") < 0 && clearUrl.indexOf("&q=") < 0,
  "清除过滤后请求不应带 roles/q（回到历史行为），实际 " + clearUrl);
assert.strictEqual(filterCountEl.textContent, "共 6 条", "未过滤时计数显示总条数");
assert.strictEqual(msgFilter.filterQueryString(), "", "清除后查询串为空");
assert.deepStrictEqual(rowIndices(), [0, 1, 2, 3, 4, 5], "清除过滤后回到完整窗口渲染");

console.log("verify-micro-web-msg-filter: 全部断言通过（面板结构 / 角色多选 / 搜索展开与去抖 / 服务端过滤 + 分页 / 空态与计数）");
