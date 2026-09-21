// 行为验证：aicli micro web client 工具输出折叠/展开功能（Node 沙盒，stub document/fetch/keydown）。
// 运行：node scripts/verify-micro-web-tool-output.mjs（无需浏览器；见 docs/aicli/web-testing.md）
// 覆盖：
//   1. chatMsgRowHtml("tool") 生成正确的折叠结构（.tool-output .tool-collapsed + 抬头控件）
//   2. HTML 转义（content 中的 < > & " 被正确转义）
//   3. refreshToolOutputToggles：内容溢出时启用抬头控件，内容过短时隐藏控件并取消折叠
//   4. 点击「工具」抬头：折叠 → 展开（文字「展开」→「收起」，图标 ▼ → ▲），反之亦然
//   5. 键盘（Enter/Space）切换；控件隐藏（tool-toggle-off）时点击不生效
//   6. 会话复制：提取 .tool-output 文本，排除抬头控件文字
import assert from "node:assert";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_DIR = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web", "js");

// ---- DOM element stub ----
function makeClassList(initial) {
  var set = new Set(initial || []);
  return {
    contains: function (c) { return set.has(c); },
    add: function () {
      for (var i = 0; i < arguments.length; i++) {
        arguments[i].split(/\s+/).filter(Boolean).forEach(function (c) { set.add(c); });
      }
    },
    remove: function () {
      for (var i = 0; i < arguments.length; i++) {
        arguments[i].split(/\s+/).filter(Boolean).forEach(function (c) { set.delete(c); });
      }
    },
    toggle: function (c, on) {
      if (on === undefined) { if (set.has(c)) { set.delete(c); } else { set.add(c); } }
      else { on ? set.add(c) : set.delete(c); }
    },
  };
}

// 简单选择器匹配：支持 .class, .class.class, [data-x="y"], tag
function matches(el, sel) {
  if (!el || !el.classList) { return false; }
  // .class.class selector
  if (sel.startsWith(".") && !sel.includes("[")) {
    var classes = sel.slice(1).split(".");
    return classes.every(function (c) { return el.classList.contains(c); });
  }
  // .class[data-x="y"] selector
  var mixedMatch = sel.match(/^\.(\w+)\[data-(\w+)="([^"]*)"\]$/);
  if (mixedMatch) {
    return el.classList.contains(mixedMatch[1]) &&
      el.getAttribute("data-" + mixedMatch[2]) === mixedMatch[3];
  }
  // [data-x="y"] selector
  var attrMatch = sel.match(/^\[data-([\w-]+)="([^"]*)"\]$/);
  if (attrMatch) {
    return el.getAttribute("data-" + attrMatch[1]) === attrMatch[2];
  }
  return false;
}

// 创建一个支持 DOM API 的 mock 元素
function makeEl(id) {
  var el = {
    id: id,
    tagName: "DIV",
    className: "",
    textContent: "",
    value: "",
    disabled: false,
    name: "",
    selectedIndex: 0,
    options: [],
    selectionStart: 0,
    selectionEnd: 0,
    offsetHeight: 0,
    scrollHeight: 0,
    clientHeight: 0,
    scrollIntoView: function () {},
    parentNode: null,
    _parent: null,
    classList: makeClassList(),
    style: {},
    listeners: {},
    _html: "",
    _children: [],
    _attrs: {},

    set innerHTML(v) { this._html = v || ""; },
    get innerHTML() { return this._html; },

    set textContent(v) { this._text = v || ""; },
    get textContent() { return this._text || ""; },

    addEventListener: function (type, fn) {
      if (!this.listeners[type]) { this.listeners[type] = []; }
      this.listeners[type].push(fn);
    },
    removeEventListener: function (type, fn) {
      if (!this.listeners[type]) { return; }
      this.listeners[type] = this.listeners[type].filter(function (f) { return f !== fn; });
    },
    dispatch: function (type, event) {
      var self = this;
      (this.listeners[type] || []).forEach(function (fn) { fn(event || {}); });
    },
    click: function () {
      var self = this;
      (this.listeners.click || []).forEach(function (fn) {
        fn({ target: self, currentTarget: self, preventDefault: function () {}, stopPropagation: function () {} });
      });
    },
    focus: function () {},
    remove: function () {},
    removeChild: function () {},
    appendChild: function (child) {
      child.parentNode = this;
      child._parent = this;
      if (!this._children) { this._children = []; }
      this._children.push(child);
      return child;
    },
    insertAdjacentHTML: function () {},
    getAttribute: function (name) { return this._attrs[name] != null ? this._attrs[name] : null; },
    setAttribute: function (name, value) { this._attrs[name] = String(value); },
    querySelector: function (sel) {
      if (!this._children) { return null; }
      for (var i = 0; i < this._children.length; i++) {
        var c = this._children[i];
        if (matches(c, sel)) { return c; }
        if (c._children && c._children.length) {
          var found = c.querySelector(sel);
          if (found) { return found; }
        }
      }
      return null;
    },
    querySelectorAll: function (sel) {
      var results = [];
      var self = this;
      function walk(el) {
        if (!el._children) { return; }
        for (var i = 0; i < el._children.length; i++) {
          var c = el._children[i];
          if (matches(c, sel)) { results.push(c); }
          walk(c);
        }
      }
      walk(this);
      return results;
    },
    closest: function (sel) {
      var node = this;
      while (node) {
        if (matches(node, sel)) { return node; }
        node = node._parent || node.parentNode;
      }
      return null;
    },
  };
  return el;
}

// ---- Global stubs ----
var elements = {};
function getElement(id) {
  if (elements[id] === undefined) {
    elements[id] = makeEl(id);
  }
  return elements[id];
}

globalThis.document = {
  getElementById: getElement,
  querySelector: function (sel) { return getElement("sel:" + sel); },
  querySelectorAll: function () { return []; },
  createElement: function (tag) { return makeEl("created:" + tag); },
  documentElement: makeEl("html"),
  body: {
    appendChild: function (el) { el.parentNode = this; },
    classList: makeClassList(),
    style: {},
  },
  addEventListener: function (type, fn) {
    if (!this._listeners) { this._listeners = {}; }
    if (!this._listeners[type]) { this._listeners[type] = []; }
    this._listeners[type].push(fn);
  },
};

globalThis.localStorage = {
  _data: {},
  getItem: function (k) { return this._data[k] != null ? this._data[k] : null; },
  setItem: function (k, v) { this._data[k] = String(v); },
  removeItem: function (k) { delete this._data[k]; },
};

globalThis.window = {
  matchMedia: function () { return { matches: false, addEventListener: function () {}, removeEventListener: function () {} }; },
  addEventListener: function () {},
  removeEventListener: function () {},
  localStorage: globalThis.localStorage,
};

globalThis.EventSource = function () {
  return { addEventListener: function () {}, close: function () {} };
};

Object.defineProperty(globalThis, "navigator", {
  value: {
    clipboard: {
      writeText: function () { return Promise.resolve(); },
    },
  },
  writable: true,
  configurable: true,
});

globalThis.fetch = function () {
  return Promise.resolve({ status: 200, json: function () { return Promise.resolve({ available: true, messages: [] }); } });
};

// 预注册 chat.js 顶层需要的元素
var chatElements = ["screen", "prompt", "send-btn", "send-status", "welcome",
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
];
chatElements.forEach(function (id) { elements[id] = makeEl(id); });

// 辅助：创建 mock tool 输出元素树（抬头控件在 .msg-label 内，与 chat.js 渲染结构一致）
function makeToolOutputMock(content, overflow) {
  var output = makeEl("tool-output");
  output.classList.add("tool-output", "tool-collapsed");
  output.setAttribute("data-tool-output", "1");
  output.textContent = content;
  output._html = content;
  output.scrollHeight = overflow ? 200 : 50;
  output.clientHeight = 50;

  var label = makeEl("tool-label");
  label.classList.add("msg-label", "tool-toggle");
  label.setAttribute("data-tool-toggle", "1");
  label.setAttribute("aria-expanded", "false");
  var labelText = makeEl("tool-toggle-text");
  labelText.classList.add("tool-toggle-text");
  labelText.textContent = "工具";
  var action = makeEl("tool-toggle-action");
  action.classList.add("tool-toggle-action");
  action.textContent = "展开";
  var icon = makeEl("tool-toggle-icon");
  icon.classList.add("tool-toggle-icon");
  icon.textContent = "▼";
  label.appendChild(labelText);
  label.appendChild(action);
  label.appendChild(icon);

  var bodyEl = makeEl("msg-body");
  bodyEl.classList.add("msg-body");
  bodyEl.appendChild(output);

  var row = makeEl("msg-row");
  row.classList.add("msg-row", "msg-tool");
  row.appendChild(label);
  row.appendChild(bodyEl);

  return { row: row, label: label, action: action, icon: icon, output: output, bodyEl: bodyEl };
}

(async () => {
  var chat = await import(pathToFileURL(path.join(WEB_DIR, "chat.js")).href);
  var chatMsgRowHtml = chat.chatMsgRowHtml;
  var refreshToolOutputToggles = chat.refreshToolOutputToggles;
  var screenEl = chat.screenEl;
  var promptEl = elements["prompt"];
  var sendBtn = elements["send-btn"];
  var conversationEl = elements["conversation"];
  var screenCopyBtn = elements["screen-copy-btn"];

  // ---- 1. chatMsgRowHtml("tool") 生成正确的折叠结构（控件在「工具」抬头行内）----
  var shortContent = "line1\nline2\nline3\nline4\nline5";
  var toolHtml = chatMsgRowHtml("tool", shortContent, false);
  assert.ok(toolHtml.indexOf('class="msg-row msg-tool"') >= 0, "tool 行应带 msg-row msg-tool 类");
  assert.ok(toolHtml.indexOf('class="msg-label tool-toggle"') >= 0, "「工具」抬头应带 tool-toggle 控件类");
  assert.ok(toolHtml.indexOf('data-tool-toggle="1"') >= 0, "抬头控件应带 data-tool-toggle 属性");
  assert.ok(toolHtml.indexOf('role="button"') >= 0, "抬头控件应带 role=button 语义");
  assert.ok(toolHtml.indexOf('class="tool-output tool-collapsed"') >= 0, "tool 输出应默认折叠 (.tool-collapsed)");
  assert.ok(toolHtml.indexOf('data-tool-output="1"') >= 0, "tool 输出应带 data-tool-output 属性");
  assert.ok(toolHtml.indexOf('class="tool-toggle-action"') >= 0, "抬头应有展开/收起文字控件");
  assert.ok(toolHtml.indexOf(">展开<") >= 0, "抬头控件默认文字应为「展开」");
  assert.ok(toolHtml.indexOf('class="tool-toggle-icon"') >= 0, "抬头应有 ▼/▲ 图标");
  assert.ok(toolHtml.indexOf("▼") >= 0, "默认折叠应显示向下图标 ▼");
  assert.ok(toolHtml.indexOf("▲") < 0, "默认折叠不应显示向上图标 ▲");

  // 控件必须位于「工具」抬头（msg-label）内、msg-body 之前；body 内不应再有旧按钮
  var labelIdx = toolHtml.indexOf('class="msg-label tool-toggle"');
  var bodyIdx = toolHtml.indexOf('class="msg-body"');
  var actionIdx = toolHtml.indexOf('class="tool-toggle-action"');
  assert.ok(actionIdx > labelIdx && actionIdx < bodyIdx, "展开/收起控件应在「工具」抬头行内（msg-body 之前）");
  assert.ok(toolHtml.indexOf('class="tool-toggle-btn"') < 0, "旧的整体按钮（.tool-toggle-btn）不应再出现");

  // 非 tool 角色不应带 tool-output 结构
  var userHtml = chatMsgRowHtml("user", "hello", false);
  assert.ok(userHtml.indexOf("tool-output") < 0, "user 消息不应包含 tool-output 结构");
  assert.ok(userHtml.indexOf("tool-toggle") < 0, "user 消息不应包含抬头控件");

  // ---- 2. HTML 转义 ----
  var unsafeContent = "<script>alert('xss')</script> & \"quotes\"";
  var unsafeHtml = chatMsgRowHtml("tool", unsafeContent, false);
  assert.ok(unsafeHtml.indexOf("&lt;script&gt;") >= 0, "tool 输出应转义 <script>");
  assert.ok(unsafeHtml.indexOf("&amp;") >= 0, "tool 输出应转义 &");
  assert.ok(unsafeHtml.indexOf("&quot;") >= 0, "tool 输出应转义 \"");
  assert.ok(unsafeHtml.indexOf("<script>") < 0, "tool 输出不应包含原始 <script> 标签");

  // ---- 3. refreshToolOutputToggles: 长内容启用抬头控件，短内容隐藏控件 ----
  // 构建 mock DOM: screenEl 包含两个 tool 行
  var longMock = makeToolOutputMock("line1\nline2\n...long content", true);
  var shortMock = makeToolOutputMock("short", false);

  // Override screenEl.querySelectorAll to return our mock tool outputs
  var origScreenQuery = screenEl.querySelectorAll.bind(screenEl);
  screenEl._children = [longMock.row, shortMock.row];
  // 也需要 msg-row 查询用于复制测试
  screenEl.querySelectorAll = function (sel) {
    if (sel === ".tool-output") { return [longMock.output, shortMock.output]; }
    if (sel === ".msg-row") { return [longMock.row, shortMock.row]; }
    // 回退到原始实现
    return origScreenQuery(sel);
  };

  refreshToolOutputToggles();

  // 长内容（溢出）：抬头控件可用，保持折叠状态
  assert.ok(!longMock.label.classList.contains("tool-toggle-off"), "溢出内容抬头控件应可用");
  assert.ok(longMock.output.classList.contains("tool-collapsed"), "溢出内容应保持折叠状态");

  // 短内容（不溢出）：抬头控件隐藏（tool-toggle-off），取消折叠（展开显示）
  assert.ok(shortMock.label.classList.contains("tool-toggle-off"), "短内容抬头控件应隐藏（tool-toggle-off）");
  assert.ok(shortMock.output.classList.contains("tool-expanded"), "短内容应取消折叠（展开显示）");

  // ---- 4. 点击「工具」抬头：折叠 → 展开 ----
  // initChat 绑定点击/键盘事件委托到 conversationEl（先调用以注册监听器）。
  chat.initChat();

  // 模拟点击抬头内的文字控件；处理器用 e.target.closest(".tool-toggle") 定位抬头
  conversationEl.dispatch("click", {
    target: longMock.action,
    currentTarget: conversationEl,
    preventDefault: function () {},
    stopPropagation: function () {},
  });

  assert.ok(!longMock.output.classList.contains("tool-collapsed"), "点击后应取消折叠");
  assert.ok(longMock.output.classList.contains("tool-expanded"), "点击后应处于展开状态");
  assert.strictEqual(longMock.action.textContent, "收起", "点击后文字应为「收起」");
  assert.strictEqual(longMock.icon.textContent, "▲", "点击后图标应为向上 ▲");
  assert.strictEqual(longMock.label.getAttribute("aria-expanded"), "true", "点击后 aria-expanded 应为 true");

  // ---- 5. 再次点击（图标处）：展开 → 折叠 ----
  conversationEl.dispatch("click", {
    target: longMock.icon,
    currentTarget: conversationEl,
    preventDefault: function () {},
    stopPropagation: function () {},
  });

  assert.ok(longMock.output.classList.contains("tool-collapsed"), "再次点击后应恢复折叠");
  assert.ok(!longMock.output.classList.contains("tool-expanded"), "再次点击后应取消展开");
  assert.strictEqual(longMock.action.textContent, "展开", "再次点击后文字应为「展开」");
  assert.strictEqual(longMock.icon.textContent, "▼", "再次点击后图标应为向下 ▼");
  assert.strictEqual(longMock.label.getAttribute("aria-expanded"), "false", "再次点击后 aria-expanded 应为 false");

  // ---- 6. 键盘可达：Enter / Space 切换，其他键忽略 ----
  conversationEl.dispatch("keydown", {
    target: longMock.action,
    key: "Enter",
    currentTarget: conversationEl,
    preventDefault: function () {},
  });
  assert.ok(longMock.output.classList.contains("tool-expanded"), "Enter 应展开");
  assert.strictEqual(longMock.icon.textContent, "▲", "Enter 展开后图标应为 ▲");

  conversationEl.dispatch("keydown", {
    target: longMock.icon,
    key: " ",
    currentTarget: conversationEl,
    preventDefault: function () {},
  });
  assert.ok(longMock.output.classList.contains("tool-collapsed"), "Space 应收起");
  assert.strictEqual(longMock.icon.textContent, "▼", "Space 收起后图标应为 ▼");

  conversationEl.dispatch("keydown", {
    target: longMock.icon,
    key: "a",
    currentTarget: conversationEl,
    preventDefault: function () {},
  });
  assert.ok(longMock.output.classList.contains("tool-collapsed"), "非 Enter/Space 键不应切换");

  // ---- 7. 控件隐藏（tool-toggle-off）时点击/键盘不生效 ----
  conversationEl.dispatch("click", {
    target: shortMock.action,
    currentTarget: conversationEl,
    preventDefault: function () {},
    stopPropagation: function () {},
  });
  assert.ok(shortMock.output.classList.contains("tool-expanded"), "控件隐藏时点击不应折叠短内容");
  assert.strictEqual(shortMock.action.textContent, "展开", "控件隐藏时文字不应变化");

  conversationEl.dispatch("keydown", {
    target: shortMock.icon,
    key: "Enter",
    currentTarget: conversationEl,
    preventDefault: function () {},
  });
  assert.ok(shortMock.output.classList.contains("tool-expanded"), "控件隐藏时键盘不应折叠短内容");

  // ---- 8. 会话复制：提取 tool 输出文本（排除抬头控件文字）----
  var copyTriggered = false;
  var copiedText = "";
  var origWriteText = globalThis.navigator.clipboard.writeText;
  globalThis.navigator.clipboard.writeText = function (text) {
    copyTriggered = true;
    copiedText = text;
    return Promise.resolve();
  };

  screenCopyBtn.dispatch("click", { target: screenCopyBtn });

  assert.ok(copyTriggered, "点击复制按钮应触发 clipboard.writeText");
  assert.ok(copiedText.indexOf("line1") >= 0, "复制内容应包含工具输出文本");
  assert.ok(copiedText.indexOf("...long content") >= 0, "复制内容应包含完整工具输出");
  assert.ok(copiedText.indexOf("short") >= 0, "复制内容应包含短工具输出");
  assert.ok(copiedText.indexOf("展开") < 0, "复制内容不应包含控件文字「展开」");
  assert.ok(copiedText.indexOf("收起") < 0, "复制内容不应包含控件文字「收起」");
  assert.ok(copiedText.indexOf("▼") < 0 && copiedText.indexOf("▲") < 0, "复制内容不应包含图标 ▼/▲");
  assert.ok(copiedText.indexOf("工具") < 0, "复制内容不应包含抬头标签文字");

  globalThis.navigator.clipboard.writeText = origWriteText;

  // ---- 9. 点击非控件元素不应触发折叠逻辑 ----
  var nonToggleButton = makeEl("other-btn");
  conversationEl.dispatch("click", {
    target: nonToggleButton,
    currentTarget: conversationEl,
    preventDefault: function () {},
    stopPropagation: function () {},
  });
  // 长内容应保持步骤 6 结束时的折叠状态
  assert.ok(longMock.output.classList.contains("tool-collapsed"), "点击非控件元素不应改变折叠状态");
  assert.strictEqual(longMock.action.textContent, "展开", "点击非控件元素不应改变控件文字");
  assert.strictEqual(longMock.icon.textContent, "▼", "点击非控件元素不应改变图标");

  console.log("ALL TOOL OUTPUT TESTS PASSED");
  process.exit(0);
})().catch((err) => { console.error("FAILED:", (err && err.stack) || err); process.exit(1); });
