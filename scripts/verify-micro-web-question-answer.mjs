// 行为验证：aicli micro web client 提问回答写入（「答案无法写入」回归）。
// 运行：node scripts/verify-micro-web-question-answer.mjs（无需浏览器；见 docs/aicli/web-testing.md）
// 覆盖：
//   1. showQuestion：建议项渲染 + 自由回答输入区（#question-answer-row）显示、
//      输入框清空并聚焦；approval 场景输入区隐藏、允许/拒绝恢复可见
//   2. 建议项点击 → POST /web/api/input {type:"question_answer",question_id,answer}
//   3. 自由回答写入：输入 + Enter / 提交按钮 → 同一 payload；Enter 阻止换行；
//      Shift+Enter 与 IME 组合输入（isComposing / keyCode 229）不提交；
//      空答案（含全空白）只提示不发送；提交后模态框收起、输入区隐藏并清空
//   4. 主动收起对话框（✕ / 遮罩空白 / 输入区 Esc）：提问只隐藏对话框、保留
//      pendingQuestionID（底部 composer 仍可写入答案，即 chat.js 分支），
//      审批保持「收起即放弃本次决议」的旧语义
//   5. index.html / style.css 静态不变量：输入区在 #approval-modal-body 内、
//      位于建议项之后，默认 display:none；样式选择器与主题变量齐备
import assert from "node:assert";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_ROOT = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web");
const WEB_DIR = path.join(WEB_ROOT, "js");

// ===========================================================================
// 迷你 DOM（只覆盖 approvals.js 及其依赖模块在导入/本用例路径上用到的最小面）
// ===========================================================================

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

function detach(node) {
  if (!node || !node.parentNode) { return; }
  var siblings = node.parentNode.children;
  var idx = siblings.indexOf(node);
  if (idx >= 0) { siblings.splice(idx, 1); }
  node.parentNode = null;
}

// 元素 textContent = 自身文本 + 子元素文本（toast 这类「容器 + 追加子节点」
// 的写法要求 getter 能聚合子节点，否则断言读不到提示文案）。
function textOf(node) {
  if (!node) { return ""; }
  if (node.nodeType !== 1) { return String(node.text || ""); }
  return String(node._text || "") + (node.children || []).map(textOf).join("");
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
    _text: "",
    _html: "",
    scrollTop: 0,
    scrollHeight: 0,
    clientHeight: 0,
    offsetHeight: 0,
    focus: function () { el.focused = true; },
    focused: false,
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
    insertAdjacentHTML: function () {},
    insertBefore: function (child, ref) {
      detach(child);
      var idx = ref ? this.children.indexOf(ref) : -1;
      if (idx < 0) { this.children.push(child); } else { this.children.splice(idx, 0, child); }
      child.parentNode = this;
      return child;
    },
    querySelectorAll: function () { return []; },
    querySelector: function () { return null; },
    closest: function () { return null; },
  };
  el.classList = makeClassList(el);
  Object.defineProperty(el, "className", {
    get: function () { return this.attrs["class"] || ""; },
    set: function (v) { this.attrs["class"] = String(v); },
  });
  Object.defineProperty(el, "innerHTML", {
    get: function () { return this._html; },
    set: function (v) { this._html = String(v == null ? "" : v); this.children = []; },
  });
  Object.defineProperty(el, "textContent", {
    get: function () { return textOf(this); },
    set: function (v) { this._text = String(v == null ? "" : v); this.children = []; },
  });
  return el;
}

// ---- Global stubs（approvals.js 的模块图在导入期依赖）----
var elements = new Map();
function getElement(id) {
  if (!elements.has(id)) { elements.set(id, makeEl("div")); }
  return elements.get(id);
}

globalThis.document = {
  getElementById: getElement,
  createElement: function (tag) { return makeEl(tag); },
  querySelector: function () { return null; },
  querySelectorAll: function () { return []; },
  documentElement: makeEl("html"),
  body: makeEl("body"),
  addEventListener: function () {},
};

function makeStorage() {
  return {
    _data: {},
    getItem: function (k) { return this._data[k] != null ? this._data[k] : null; },
    setItem: function (k, v) { this._data[k] = String(v); },
    removeItem: function (k) { delete this._data[k]; },
  };
}

globalThis.localStorage = makeStorage();
globalThis.sessionStorage = makeStorage();

globalThis.window = {
  matchMedia: function () {
    return { matches: false, addEventListener: function () {}, removeEventListener: function () {} };
  },
  addEventListener: function () {},
  removeEventListener: function () {},
  innerWidth: 1280,
  innerHeight: 800,
  confirm: function () { return false; },
  open: function () { return null; },
  localStorage: globalThis.localStorage,
  sessionStorage: globalThis.sessionStorage,
};

globalThis.EventSource = function () {
  return { addEventListener: function () {}, close: function () {} };
};

Object.defineProperty(globalThis, "navigator", {
  value: {
    userAgent: "node",
    clipboard: { writeText: function () { return Promise.resolve(); } },
  },
  writable: true,
  configurable: true,
});

// POST /web/api/input 捕获：sendInput 同步发起 fetch，断言可在同一次调用后立刻做。
// nextInputResponse 控制服务端回执，用于验证 resolved / stale 两条分支。
var nextInputResponse = { status: "resolved" };
var postedInputs = [];
globalThis.fetch = function (url, opts) {
  var body = null;
  try { body = opts && opts.body ? JSON.parse(opts.body) : null; } catch (e) { body = null; }
  postedInputs.push({ url: String(url), method: opts && opts.method, body: body });
  return Promise.resolve({
    status: 200,
    json: function () { return Promise.resolve(nextInputResponse); },
  });
};

// ===========================================================================
// 断言
// ===========================================================================

const approvals = await import(pathToFileURL(path.join(WEB_DIR, "approvals.js")).href);
const overlay = getElement("approval-overlay");
const answerRow = getElement("question-answer-row");
const answerInput = getElement("question-answer-input");
const answerSubmit = getElement("question-answer-submit");
const answerHint = getElement("question-answer-hint");
const suggestionsEl = getElement("question-suggestions");
const approveBtn = getElement("approve-btn");
const denyBtn = getElement("deny-btn");
const closeBtn = getElement("approval-modal-close");
const toastEl = getElement("toast-container");

function resetCaptured() { postedInputs.length = 0; }
function lastInput() { return postedInputs[postedInputs.length - 1]; }
function keydown(ev) { answerInput.dispatch("keydown", ev); }
function enterEvent(extra) {
  var ev = {
    key: "Enter", shiftKey: false,
    preventDefault: function () { ev.defaultPrevented = true; },
    defaultPrevented: false,
  };
  Object.keys(extra || {}).forEach(function (k) { ev[k] = extra[k]; });
  return ev;
}

approvals.initApprovals();

// ---- 1. 提问：建议项 + 自由回答输入区 ----
resetCaptured();
approvals.showQuestion({ question_id: "q_1", prompt: "选哪个？", suggestions: ["A", "B"] });
assert.strictEqual(overlay.classList.contains("active"), true, "提问应打开模态框");
assert.notStrictEqual(answerRow.style.display, "none", "提问必须显示自由回答输入区");
assert.strictEqual(answerInput.value, "", "打开提问时输入框应为空");
assert.strictEqual(answerInput.focused, true, "打开提问时输入框应获得焦点（可直接键入答案）");
assert.strictEqual(suggestionsEl.children.length, 2, "两个建议项应渲染为按钮");
assert.strictEqual(approvals.hasPendingQuestion(), true, "有待答问题");
assert.strictEqual(approvals.hasPendingApproval(), false, "提问不是审批");
assert.strictEqual(approveBtn.style.display, "none", "提问时隐藏「允许」");
assert.strictEqual(denyBtn.style.display, "none", "提问时隐藏「拒绝」");

// ---- 2. 建议项点击 = 快捷回答 ----
resetCaptured();
suggestionsEl.children[0].click();
assert.deepStrictEqual(lastInput().body, { type: "question_answer", question_id: "q_1", answer: "A" },
  "建议项点击应提交 question_answer");
assert.strictEqual(overlay.classList.contains("active"), false, "回答后模态框收起");
assert.strictEqual(approvals.hasPendingQuestion(), false, "回答后清空待答状态");
assert.strictEqual(answerRow.style.display, "none", "回答后隐藏输入区");

// ---- 3. 自由回答写入（本缺陷核心路径）----
resetCaptured();
approvals.showQuestion({ question_id: "q_2", prompt: "你的名字？" });
answerInput.value = "  阿德  ";
var enter = enterEvent();
keydown(enter);
assert.deepStrictEqual(lastInput().body, { type: "question_answer", question_id: "q_2", answer: "阿德" },
  "输入框 Enter 应把文本作为答案写入");
assert.strictEqual(enter.defaultPrevented, true, "Enter 应阻止在 textarea 内插入换行");
assert.strictEqual(overlay.classList.contains("active"), false, "自由回答后模态框收起");
assert.strictEqual(answerInput.value, "", "自由回答后输入框清空");

// 3b. 提交按钮路径
resetCaptured();
approvals.showQuestion({ question_id: "q_3", prompt: "继续？" });
answerInput.value = "继续";
answerSubmit.click();
assert.deepStrictEqual(lastInput().body, { type: "question_answer", question_id: "q_3", answer: "继续" },
  "提交按钮应写入答案");

// 3c. 空答案：只提示不发送
resetCaptured();
approvals.showQuestion({ question_id: "q_4", prompt: "补充说明？" });
answerInput.value = "   ";
keydown(enterEvent());
assert.strictEqual(postedInputs.length, 0, "空答案不得发起请求");
assert.strictEqual(answerHint.textContent, "请输入回答", "空答案应给出提示");
assert.strictEqual(approvals.hasPendingQuestion(), true, "空答案不消耗待答状态");
assert.strictEqual(overlay.classList.contains("active"), true, "空答案后模态框保持打开");

// 3d. Shift+Enter 换行 / IME 组合输入不提交
resetCaptured();
answerInput.value = "多行";
keydown(enterEvent({ shiftKey: true }));
keydown(enterEvent({ isComposing: true }));
keydown(enterEvent({ keyCode: 229 }));
assert.strictEqual(postedInputs.length, 0, "Shift+Enter / IME 组合输入不得提交");
// 组合确认后再按 Enter 正常提交
keydown(enterEvent());
assert.deepStrictEqual(lastInput().body, { type: "question_answer", question_id: "q_4", answer: "多行" },
  "IME 组合结束后的 Enter 应提交");

// ---- 4. 主动收起：提问保留答案路由，审批保持旧语义 ----
resetCaptured();
approvals.showQuestion({ question_id: "q_5", prompt: "还继续吗？" });
closeBtn.click();
assert.strictEqual(overlay.classList.contains("active"), false, "✕ 应收起对话框");
assert.strictEqual(approvals.hasPendingQuestion(), true, "✕ 收起提问后必须保留 pendingQuestionID（答案路由不能丢）");
assert.ok(toastEl.textContent.indexOf("底部输入区") >= 0, "收起提问应提示底部输入区可继续回答");
// 底部 composer 路径（chat.js 的 hasPendingQuestion 分支）仍可写入答案
approvals.sendQuestionAnswer("继续");
assert.deepStrictEqual(lastInput().body, { type: "question_answer", question_id: "q_5", answer: "继续" },
  "收起对话框后 composer 仍应能写入答案");
assert.strictEqual(approvals.hasPendingQuestion(), false, "composer 回答后清空待答状态");

// 遮罩空白点击同样保留答案路由
resetCaptured();
approvals.showQuestion({ question_id: "q_6", prompt: "再看一次？" });
overlay.dispatch("click", { target: overlay });
assert.strictEqual(overlay.classList.contains("active"), false, "点遮罩空白应收起对话框");
assert.strictEqual(approvals.hasPendingQuestion(), true, "点遮罩收起提问后仍保留答案路由");

// 输入区 Esc 收起
resetCaptured();
overlay.classList.add("active");
keydown({ key: "Escape", preventDefault: function () {} });
assert.strictEqual(overlay.classList.contains("active"), false, "输入区 Esc 应收起对话框");
assert.strictEqual(approvals.hasPendingQuestion(), true, "Esc 收起提问后仍保留答案路由");
approvals.sendQuestionAnswer("最后一答");
assert.deepStrictEqual(lastInput().body, { type: "question_answer", question_id: "q_6", answer: "最后一答" },
  "Esc 收起后 composer 仍应能写入答案");

// 审批：收起即放弃本次决议（旧语义），且不残留提问态
resetCaptured();
approvals.showApproval({ request_id: "r_1", tool_name: "bash", prompt: "执行？", arguments: { cmd: "ls" } });
assert.strictEqual(answerRow.style.display, "none", "审批场景不得显示回答输入区");
assert.strictEqual(approveBtn.style.display, "", "审批场景恢复「允许」");
assert.strictEqual(denyBtn.style.display, "", "审批场景恢复「拒绝」");
assert.strictEqual(approvals.hasPendingApproval(), true, "有待审批请求");
closeBtn.click();
assert.strictEqual(approvals.hasPendingApproval(), false, "审批收起即放弃本次决议（旧语义保持）");
assert.strictEqual(overlay.classList.contains("active"), false, "审批收起后模态框关闭");

// 审批决议按钮仍走 approval payload
resetCaptured();
approvals.showApproval({ request_id: "r_2", tool_name: "bash", prompt: "执行？" });
approveBtn.click();
assert.deepStrictEqual(lastInput().body, { type: "approval", request_id: "r_2", allow: true }, "允许按钮 payload");
approvals.showApproval({ request_id: "r_3", tool_name: "bash", prompt: "执行？" });
denyBtn.click();
assert.deepStrictEqual(lastInput().body, { type: "approval", request_id: "r_3", allow: false }, "拒绝按钮 payload");

// 提问 → 审批切换：输入区收起并清空，不把上一题的半截答案带进审批框
approvals.showQuestion({ question_id: "q_7", prompt: "答案？" });
answerInput.value = "半截答案";
approvals.showApproval({ request_id: "r_4", tool_name: "bash", prompt: "执行？" });
assert.strictEqual(answerRow.style.display, "none", "切换到审批后输入区隐藏");
assert.strictEqual(answerInput.value, "", "切换到审批后输入区清空");

// ---- 5. index.html / style.css 静态不变量 ----
const html = fs.readFileSync(path.join(WEB_ROOT, "index.html"), "utf8");
const css = fs.readFileSync(path.join(WEB_ROOT, "style.css"), "utf8");

const iBody = html.indexOf('id="approval-modal-body"');
const iSuggest = html.indexOf('id="question-suggestions"');
const iRow = html.indexOf('id="question-answer-row"');
const iInput = html.indexOf('id="question-answer-input"');
const iSubmit = html.indexOf('id="question-answer-submit"');
const iFooter = html.indexOf('id="approval-modal-footer"');
assert.ok(iBody >= 0 && iSuggest > iBody, "建议项容器应在 #approval-modal-body 内");
assert.ok(iRow > iSuggest && iRow < iFooter, "回答输入区应在建议项之后、模态框 footer 之前");
assert.ok(iInput > iRow && iInput < iFooter, "回答输入框应在输入区内");
assert.ok(iSubmit > iRow && iSubmit < iFooter, "提交按钮应在输入区内");
assert.ok(/id="question-answer-row"[^>]*style="display:none"/.test(html),
  "输入区默认 display:none（approval 场景不得闪出）");

["#question-answer-row {", "#question-answer-row textarea", "#question-answer-actions {", ".question-answer-hint {",
  "button.answer-submit-btn"].forEach(function (sel) {
  assert.ok(css.indexOf(sel) >= 0, "style.css 缺少选择器: " + sel);
});
const redDefs = (css.match(/--red:/g) || []).length;
assert.ok(redDefs >= 2, "空答案提示色 --red 应在深色/浅色两套主题中都有定义");

// ---- 5. 服务端回执分支：stale（提问已结束）必须如实提示，不得伪装成功 ----
// 场景：提问在服务端已无挂起项（已被其它入口回答 / 本轮已终止）时提交回答，
// actor 侧是幂等 no-op，服务端现在回 status=stale；前端必须提示「未送达模型」，
// 而不是沿用「已提交」的成功文案（这正是「提交了回答但没有回传 LLM」的静默路径）。
function flushAsync() { return new Promise(function (resolve) { setTimeout(resolve, 0); }); }

resetCaptured();
nextInputResponse = { status: "stale", reason: "该提问已结束（已被回答或本轮已终止），回答未送达模型" };
approvals.showQuestion({ question_id: "q_stale", prompt: "迟到的回答？" });
approvals.sendQuestionAnswer("迟到的回答");
assert.deepStrictEqual(lastInput().body, { type: "question_answer", question_id: "q_stale", answer: "迟到的回答" },
  "stale 场景的提交 payload 形状不变");
assert.strictEqual(overlay.classList.contains("active"), false, "提交后应收起对话框");
await flushAsync();
assert.ok(toastEl.textContent.indexOf("未送达模型") >= 0,
  "服务端回 stale 时必须提示回答未送达模型（toast=" + toastEl.textContent + "）");

// resolved 回归：真正送达时不得出现未送达告警。
resetCaptured();
toastEl.innerHTML = "";
nextInputResponse = { status: "resolved" };
approvals.showQuestion({ question_id: "q_ok", prompt: "正常回答？" });
approvals.sendQuestionAnswer("正常回答");
await flushAsync();
assert.strictEqual(toastEl.textContent.indexOf("未送达模型"), -1,
  "resolved 回执不得出现未送达告警（toast=" + toastEl.textContent + "）");
assert.deepStrictEqual(lastInput().body, { type: "question_answer", question_id: "q_ok", answer: "正常回答" },
  "resolved 场景 payload 形状不变");

// chat.js 的 composer 分支必须仍在（收起对话框后的写入路径）
const chatJs = fs.readFileSync(path.join(WEB_DIR, "chat.js"), "utf8");
assert.ok(chatJs.indexOf("hasPendingQuestion()") >= 0 && chatJs.indexOf("sendQuestionAnswer(text)") >= 0,
  "chat.js 底部输入区的提问回答分支不得被移除");

console.log("verify-micro-web-question-answer: 全部通过");
