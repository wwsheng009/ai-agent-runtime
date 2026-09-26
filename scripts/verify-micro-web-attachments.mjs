// 行为验证：aicli micro web client 图片附件输入面（无需浏览器）。
// 运行：node scripts/verify-micro-web-attachments.mjs（仓库根目录）
// 覆盖：
//   1. 三入口共用同一条上传：①「📎」按钮 + 隐藏 file input 选择文件；② #prompt 粘贴剪贴板图片；
//      ③ composer 面板内拖放（dragover 阻止默认 + 高亮、dragleave 收尾、drop 取 files）
//   2. multipart 契约：POST /web/api/attachments，字段名 file（多份），响应逐项落地
//   3. 附件轨：显示文件名 / 尺寸 / 字节数，可移除（×）；**不放缩略图**（无 <img> / 不读本地路径）
//   4. 发送：payload 带 image_paths（无附件时不带该字段，interrupt 等其它类型也不带）；
//      queued 后清空轨道；移除条目后发送不再带该路径
//   5. 失败如实报错：skipped=true、HTTP 409、网络错误都写进 #send-status 且不产生可用条目
import assert from "node:assert";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_DIR = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web");
const JS_DIR = path.join(WEB_DIR, "js");

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
// 迷你 DOM（沿用 verify-micro-web-question-answer.mjs 的沙盒写法：attachments.js 的
// 模块图经 sessions.js ← chat.js 拉入同一批模块，这里只补它们在本用例路径上用到的最小面）
// ===========================================================================

function classListOf(el) { return String(el.attrs["class"] || "").split(/\s+/).filter(Boolean); }
function setClassList(el, classes) { el.attrs["class"] = classes.join(" "); }

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

// textContent = 自身文本 + 子元素文本（showToast 这类「容器 + 追加子节点」的写法要求聚合）。
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
    files: null,
    hidden: false,
    _text: "",
    _html: "",
    scrollTop: 0,
    scrollHeight: 0,
    clientHeight: 0,
    offsetHeight: 0,
    offsetWidth: 0,
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
    contains: function (node) { return node === this || this.children.indexOf(node) >= 0; },
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
  title: "",
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

// ---- fetch 捕获：/web/api/attachments（multipart）与 /web/api/input（JSON）分档记录 ----
// 上传体是 FormData：这里按 [字段名, 文件名] 逐项展开，用来验证字段名与多份 file 的契约。
var requests = [];
var nextUpload = { status: 200, body: { status: "ok", accepted: 0, attachments: [] } };
var nextInput = { status: 200, body: { status: "queued" } };
var failUploadWithNetworkError = false;

globalThis.fetch = function (url, opts) {
  var target = String(url);
  var kind = target.indexOf("/web/api/attachments") >= 0 ? "attachments" : "input";
  var raw = opts ? opts.body : null;
  var record = { url: target, method: opts && opts.method, kind: kind, form: null, json: null };
  if (typeof FormData !== "undefined" && raw instanceof FormData) {
    record.form = [];
    raw.forEach(function (value, field) {
      record.form.push({ field: field, name: value && value.name ? String(value.name) : String(value) });
    });
  } else if (typeof raw === "string") {
    try { record.json = JSON.parse(raw); } catch (e) { record.json = null; }
  }
  requests.push(record);
  if (kind === "attachments" && failUploadWithNetworkError) {
    return Promise.reject(new Error("network down"));
  }
  var resp = kind === "attachments" ? nextUpload : nextInput;
  return Promise.resolve({
    status: resp.status,
    ok: resp.status >= 200 && resp.status < 300,
    json: function () { return Promise.resolve(resp.body); },
  });
};

function flushAsync() { return new Promise(function (resolve) { setTimeout(resolve, 0); }); }
function uploadRequests() { return requests.filter(function (r) { return r.kind === "attachments"; }); }
function inputRequests() { return requests.filter(function (r) { return r.kind === "input"; }); }
function lastUpload() { var list = uploadRequests(); return list[list.length - 1]; }
function lastInput() { var list = inputRequests(); return list[list.length - 1]; }
function resetRequests() { requests.length = 0; }
function file(name, type, size) {
  return new File([new Uint8Array(size || 8)], name, { type: type || "image/png" });
}
function dragEvent(files, extra) {
  var ev = {
    defaultPrevented: false,
    preventDefault: function () { ev.defaultPrevented = true; },
    dataTransfer: files ? { files: files, types: ["Files"] } : { files: [], types: ["Files"] },
  };
  Object.keys(extra || {}).forEach(function (k) { ev[k] = extra[k]; });
  return ev;
}

// ===========================================================================
// 行为断言：js/attachments.js + js/sessions.js（同一份模块图：attachments.js 由 app.js
// 聚合、sessions.js 的 sendInput 取走待发路径，因此两者一起导入才验证得到接线）
// ===========================================================================
const attachments = await import(pathToFileURL(path.join(JS_DIR, "attachments.js")).href);
const sessions = await import(pathToFileURL(path.join(JS_DIR, "sessions.js")).href);

const trackEl = getElement("attachment-track");
const attachFile = getElement("attach-file");
const attachBtn = getElement("attach-btn");
const panelEl = getElement("composer-panel");
const promptEl = getElement("prompt");
const sendStatus = getElement("send-status");

var pickClicks = 0;
attachFile.click = function () { pickClicks++; };

attachments.initComposerAttachments();

const PATH_A = "C:\\tmp\\aicli-images\\a.png";
const PATH_B = "C:\\tmp\\aicli-images\\b.jpg";
const PATH_C = "C:\\tmp\\aicli-images\\clip.png";
const PATH_I = "C:\\tmp\\aicli-images\\item.png";
const PATH_D = "C:\\tmp\\aicli-images\\drop.png";

function trackHtml() { return trackEl.innerHTML; }
function statusText() { return sendStatus.textContent; }

console.log("[1] 初始化与「📎」按钮");
check("初始化：轨道隐藏、无待发路径（不预置任何条目）", () => {
  assert.strictEqual(trackEl.hidden, true, "无附件时轨道应隐藏");
  assert.strictEqual(trackHtml(), "", "无附件时轨道应为空");
  assert.deepStrictEqual(attachments.composerImagePaths(), [], "初始不应有待发路径");
});
check("「📎」按钮点击转发到隐藏的 file input（不自己实现选择逻辑）", () => {
  attachBtn.click();
  assert.strictEqual(pickClicks, 1, "📎 按钮应触发 #attach-file 的 click()");
});

console.log("[2] 入口① 文件选择 → multipart 上传 → 轨道条目");
await checkAsync("选择的文件以 multipart 发到 /web/api/attachments（字段 file，多份）", async () => {
  nextUpload = {
    status: 200,
    body: {
      status: "ok", accepted: 2,
      attachments: [
        { name: "a.png", path: PATH_A, bytes: 1234, width: 4, height: 4 },
        { name: "b.jpg", path: PATH_B, bytes: 2048, width: 8, height: 8, note: "已压缩到上限内" },
      ],
    },
  };
  resetRequests();
  attachFile.files = [file("a.png", "image/png", 16), file("b.jpg", "image/jpeg", 32)];
  attachFile.dispatch("change", {});
  const req = lastUpload();
  assert.ok(req, "选择文件后必须发起上传请求（status=" + statusText() +
    " files=" + (attachFile.files ? attachFile.files.length : "null") +
    " listeners=" + Object.keys(attachFile.listeners).join(",") + "）");
  assert.strictEqual(req.url, "/web/api/attachments", "上传端点应为 /web/api/attachments");
  assert.strictEqual(req.method, "POST", "上传应为 POST");
  assert.strictEqual(req.form.length, 2, "两份文件应各自成为一个 multipart 部分");
  req.form.forEach((part) => assert.strictEqual(part.field, "file", "字段名必须是 file"));
  assert.deepStrictEqual(req.form.map((p) => p.name), ["a.png", "b.jpg"], "文件名应按选择顺序带上");
  await flushAsync();
});
check("上传成功后轨道显示文件名 / 尺寸 / 字节数，且不放缩略图（无 <img>）", () => {
  const html = trackHtml();
  assert.strictEqual(trackEl.hidden, false, "有条目后轨道应可见");
  assert.ok(html.indexOf("a.png") >= 0 && html.indexOf("b.jpg") >= 0, "轨道应显示文件名: " + html);
  assert.ok(html.indexOf("4×4") >= 0 && html.indexOf("8×8") >= 0, "轨道应显示尺寸: " + html);
  assert.ok(html.indexOf("1.2 KB") >= 0 && html.indexOf("2.0 KB") >= 0, "轨道应显示字节数: " + html);
  assert.ok(html.indexOf("已压缩到上限内") >= 0, "服务端说明应如实带上: " + html);
  assert.ok(html.indexOf("<img") < 0, "轨道不得放缩略图（浏览器读不到本地路径）: " + html);
  assert.ok(html.indexOf("C:\\tmp") < 0, "轨道不得内联本地绝对路径（显示与标记只留文件名 / 元信息）: " + html);
  assert.deepStrictEqual(attachments.composerImagePaths(), [PATH_A, PATH_B], "待发路径 = 服务端回执的 path");
});

console.log("[3] 发送：image_paths 携带 + queued 后清空轨道");
await checkAsync("发送 prompt 时 payload 带 image_paths（顺序 = 轨道顺序）", async () => {
  nextInput = { status: 200, body: { status: "queued", attached_images: 2 } };
  resetRequests();
  sessions.sendInput({ prompt: "看这两张图" });
  const body = lastInput().json;
  assert.strictEqual(body.prompt, "看这两张图", "prompt 原样发送");
  assert.deepStrictEqual(body.image_paths, [PATH_A, PATH_B], "image_paths 应为待发路径: " + JSON.stringify(body));
  await flushAsync();
});
check("queued 后清空轨道（上传了但没发出去的图片不粘到下一轮）", () => {
  assert.deepStrictEqual(attachments.composerImagePaths(), [], "queued 后不应再有待发路径");
  assert.strictEqual(trackEl.hidden, true, "queued 后轨道应隐藏");
  assert.strictEqual(trackHtml(), "", "queued 后轨道应清空");
});
await checkAsync("发送瞬间的图片说明（image_notes）如实透出到 #send-status", async () => {
  // 场景：上传后、发送前文件被清理 → 服务端 attach 时跳过并给出 note，界面不能装作已带上。
  nextUpload = {
    status: 200,
    body: { status: "ok", accepted: 1, attachments: [{ name: "gone.png", path: PATH_C, bytes: 16, width: 2, height: 2 }] },
  };
  resetRequests();
  attachFile.files = [file("gone.png", "image/png", 16)];
  attachFile.dispatch("change", {});
  await flushAsync();
  nextInput = { status: 200, body: { status: "queued", attached_images: 0, image_notes: ["图片已不存在: gone.png"] } };
  resetRequests();
  sessions.sendInput({ prompt: "发送已失效的图片" });
  await flushAsync();
  assert.ok(statusText().indexOf("图片已不存在") >= 0, "image_notes 应如实并入状态行: " + statusText());
  assert.deepStrictEqual(attachments.composerImagePaths(), [], "queued 后仍应清空轨道");
});
checkAsync; // 后续断言全部同步；保留 checkAsync 供扩展

console.log("[4] 无附件 / 非 prompt 请求：不得出现 image_paths 字段");
await checkAsync("无附件发送 prompt：payload 不含 image_paths 键（旧契约不变）", async () => {
  nextInput = { status: 200, body: { status: "queued" } };
  resetRequests();
  sessions.sendInput({ prompt: "纯文本" });
  const body = lastInput().json;
  assert.strictEqual("image_paths" in body, false, "无附件时不得出现 image_paths: " + JSON.stringify(body));
  await flushAsync();
});

console.log("[5] 入口② 粘贴剪贴板图片 → 同一条上传");
await checkAsync("粘贴 clipboardData.files 走 /web/api/attachments（与文件选择同一条路径）", async () => {
  nextUpload = {
    status: 200,
    body: { status: "ok", accepted: 1, attachments: [{ name: "clip.png", path: PATH_C, bytes: 512, width: 3, height: 3 }] },
  };
  resetRequests();
  const ev = { clipboardData: { files: [file("clip.png", "image/png", 8)] }, defaultPrevented: false, preventDefault: function () { ev.defaultPrevented = true; } };
  promptEl.dispatch("paste", ev);
  assert.strictEqual(ev.defaultPrevented, true, "带图片的粘贴应阻止默认（不把文件名插进输入框）");
  assert.strictEqual(uploadRequests().length, 1, "粘贴应走同一条上传端点");
  assert.deepStrictEqual(lastUpload().form.map((p) => [p.field, p.name]), [["file", "clip.png"]], "粘贴同样以 file 字段上传");
  await flushAsync();
  assert.deepStrictEqual(attachments.composerImagePaths(), [PATH_C], "粘贴图片应进入待发轨道");
  assert.ok(trackHtml().indexOf("clip.png") >= 0, "轨道应出现粘贴的图片: " + trackHtml());
});
await checkAsync("剪贴板只出现在 items 时按 image/* 取回；纯文本粘贴不触发上传", async () => {
  nextUpload = {
    status: 200,
    body: { status: "ok", accepted: 1, attachments: [{ name: "item.png", path: PATH_I, bytes: 256, width: 2, height: 2 }] },
  };
  resetRequests();
  const imageItem = { kind: "file", type: "image/png", getAsFile: function () { return file("item.png", "image/png", 8); } };
  const textItem = { kind: "string", type: "text/plain" };
  promptEl.dispatch("paste", { clipboardData: { files: [], items: [textItem, imageItem] }, preventDefault: function () {} });
  assert.strictEqual(uploadRequests().length, 1, "items 里的图片应触发上传");
  assert.deepStrictEqual(lastUpload().form.map((p) => p.name), ["item.png"], "只取 image/* 的 item");
  await flushAsync();
  resetRequests();
  const textOnly = { clipboardData: { files: [], items: [textItem] }, defaultPrevented: false, preventDefault: function () { textOnly.defaultPrevented = true; } };
  promptEl.dispatch("paste", textOnly);
  assert.strictEqual(uploadRequests().length, 0, "纯文本粘贴不得发起上传");
  assert.strictEqual(textOnly.defaultPrevented, false, "纯文本粘贴应交给 textarea 默认行为");
});
await checkAsync("有附件待发时，interrupt 等非 prompt 请求不带 image_paths", async () => {
  nextInput = { status: 200, body: { status: "interrupted" } };
  resetRequests();
  sessions.sendInput({ type: "interrupt" });
  assert.strictEqual("image_paths" in lastInput().json, false, "interrupt 不得携带图片附件");
  await flushAsync();
  assert.strictEqual(attachments.composerImagePaths().length, 2, "interrupt 不应清空待发轨道");
});

console.log("[6] 入口③ 面板内拖放");
await checkAsync("dragover 阻止默认并高亮，dragleave 收尾，drop 取 dataTransfer.files 上传", async () => {
  nextUpload = {
    status: 200,
    body: { status: "ok", accepted: 1, attachments: [{ name: "drop.png", path: PATH_D, bytes: 64, width: 1, height: 1 }] },
  };
  resetRequests();
  const over = dragEvent([file("drop.png", "image/png", 8)]);
  panelEl.dispatch("dragover", over);
  assert.strictEqual(over.defaultPrevented, true, "dragover 必须阻止默认（否则浏览器直接打开图片）");
  assert.strictEqual(panelEl.classList.contains("attach-dragover"), true, "拖入时应高亮面板");
  const leave = { relatedTarget: null };
  panelEl.dispatch("dragleave", leave);
  assert.strictEqual(panelEl.classList.contains("attach-dragover"), false, "dragleave 应清掉高亮");
  const drop = dragEvent([file("drop.png", "image/png", 8)]);
  panelEl.dispatch("drop", drop);
  assert.strictEqual(drop.defaultPrevented, true, "drop 应阻止默认");
  assert.strictEqual(panelEl.classList.contains("attach-dragover"), false, "drop 后不得残留高亮");
  assert.deepStrictEqual(lastUpload().form.map((p) => p.name), ["drop.png"], "drop 的文件应走同一条上传");
  await flushAsync();
  assert.ok(trackHtml().indexOf("drop.png") >= 0, "轨道应出现拖入的图片: " + trackHtml());
});
check("dragleave 时 relatedTarget 仍在面板内（拖到子元素）保持高亮", () => {
  const child = makeEl("div");
  panelEl.appendChild(child);
  panelEl.dispatch("dragover", dragEvent([file("x.png")]));
  assert.strictEqual(panelEl.classList.contains("attach-dragover"), true, "拖入面板应高亮");
  panelEl.dispatch("dragleave", { relatedTarget: child });
  assert.strictEqual(panelEl.classList.contains("attach-dragover"), true, "仍在面板内不应清高亮");
  panelEl.dispatch("dragleave", { relatedTarget: null });
  assert.strictEqual(panelEl.classList.contains("attach-dragover"), false, "离开面板应清高亮");
});

console.log("[7] 移除条目（×）后发送不再带该路径");
// 迷你 DOM 不解析 HTML：移除按钮的索引从**实际渲染出的标记**里取回（这本身就是对标记的断言），
// 再以「点击该按钮」的事件形状派发（target = 带 data-attach-remove 的按钮节点）。
function removeIndexFor(fname) {
  const html = trackHtml();
  const at = html.indexOf(fname);
  assert.ok(at >= 0, "轨道里找不到 " + fname + ": " + html);
  const m = /data-attach-remove="(\d+)"/.exec(html.slice(at));
  assert.ok(m, "移除按钮缺少 data-attach-remove: " + html.slice(at));
  return Number(m[1]);
}
function fakeRemoveTarget(idx) {
  return {
    getAttribute: function (n) { return n === "data-attach-remove" ? String(idx) : null; },
    parentNode: null,
  };
}

await checkAsync("点 × 移除后待发路径与轨道同步收缩，并如实提示", async () => {
  assert.deepStrictEqual(attachments.composerImagePaths(), [PATH_C, PATH_I, PATH_D], "前置：三条待发路径");
  const idx = removeIndexFor("drop.png");
  trackEl.dispatch("click", { target: fakeRemoveTarget(idx) });
  assert.deepStrictEqual(attachments.composerImagePaths(), [PATH_C, PATH_I], "移除后不得再带该路径");
  assert.ok(trackHtml().indexOf("drop.png") < 0, "轨道不应再有被移除的条目: " + trackHtml());
  assert.ok(statusText().indexOf("已移除附件 drop.png") >= 0, "移除应如实提示: " + statusText());
});
await checkAsync("移除后发送 prompt：payload 只带剩下的路径，queued 后清空", async () => {
  nextInput = { status: 200, body: { status: "queued", attached_images: 2 } };
  resetRequests();
  sessions.sendInput({ prompt: "只发剩下两张" });
  assert.deepStrictEqual(lastInput().json.image_paths, [PATH_C, PATH_I], "被移除的路径不得出现在 payload");
  await flushAsync();
  assert.deepStrictEqual(attachments.composerImagePaths(), [], "queued 后应清空轨道");
  assert.strictEqual(trackEl.hidden, true, "queued 后轨道应隐藏");
});

console.log("[8] 失败路径如实报错（不产生可用条目）");
await checkAsync("skipped=true：轨道留失败行 + #send-status 给出中文原因", async () => {
  nextUpload = {
    status: 200,
    body: { status: "ok", accepted: 0, attachments: [{ name: "huge.png", skipped: true, note: "图片超过体积上限" }] },
  };
  resetRequests();
  attachFile.files = [file("huge.png", "image/png", 8)];
  attachFile.dispatch("change", {});
  await flushAsync();
  assert.deepStrictEqual(attachments.composerImagePaths(), [], "skipped 项不得成为可用附件");
  assert.ok(trackHtml().indexOf("huge.png") >= 0 && trackHtml().indexOf("图片超过体积上限") >= 0,
    "轨上应如实显示跳过原因: " + trackHtml());
  assert.ok(trackHtml().indexOf("attach-failed") >= 0, "失败条目应带 attach-failed 标记: " + trackHtml());
  assert.ok(statusText().indexOf("已跳过") >= 0 && statusText().indexOf("图片超过体积上限") >= 0,
    "#send-status 应如实说明: " + statusText());
});
await checkAsync("skipped 之后发送：payload 不含 image_paths", async () => {
  nextInput = { status: 200, body: { status: "queued" } };
  resetRequests();
  sessions.sendInput({ prompt: "没有可用图片" });
  assert.strictEqual("image_paths" in lastInput().json, false, "无可用附件不得带 image_paths");
  await flushAsync();
});
await checkAsync("HTTP 409（无活动会话）：如实报错且不产生条目", async () => {
  nextUpload = { status: 409, body: { status: "error", reason: "no active chat session" } };
  resetRequests();
  attachFile.files = [file("c.png", "image/png", 8)];
  attachFile.dispatch("change", {});
  await flushAsync();
  assert.deepStrictEqual(attachments.composerImagePaths(), [], "HTTP 错误不得产生可用条目");
  assert.ok(statusText().indexOf("图片上传失败") >= 0 && statusText().indexOf("no active chat session") >= 0,
    "#send-status 应给出服务端原因: " + statusText());
});
await checkAsync("网络错误（fetch 拒绝）：如实报错且不产生条目", async () => {
  failUploadWithNetworkError = true;
  resetRequests();
  attachFile.files = [file("d.png", "image/png", 8)];
  attachFile.dispatch("change", {});
  await flushAsync();
  failUploadWithNetworkError = false;
  assert.deepStrictEqual(attachments.composerImagePaths(), [], "网络错误不得产生可用条目");
  assert.ok(statusText().indexOf("图片上传失败") >= 0 && statusText().indexOf("network down") >= 0,
    "#send-status 应给出网络原因: " + statusText());
});
await checkAsync("一次超过 8 张：直接拒收，不发起半截上传", async () => {
  resetRequests();
  const many = [];
  for (let i = 0; i < 9; i++) { many.push(file("m" + i + ".png", "image/png", 8)); }
  attachFile.files = many;
  attachFile.dispatch("change", {});
  assert.strictEqual(uploadRequests().length, 0, "超过上限不应发起上传请求");
  assert.ok(statusText().indexOf("一次最多上传 8 张图片") >= 0, "应如实说明上限: " + statusText());
  await flushAsync();
});

// ===========================================================================
// 静态不变量：结构位置 / 断言接线不会被后续重构悄悄摘掉
// ===========================================================================
console.log("[9] index.html / style.css / app.js / sessions.js 静态不变量");
const html = fs.readFileSync(path.join(WEB_DIR, "index.html"), "utf8");
const css = fs.readFileSync(path.join(WEB_DIR, "style.css"), "utf8");
const attachJs = fs.readFileSync(path.join(JS_DIR, "attachments.js"), "utf8");
const appJs = fs.readFileSync(path.join(WEB_DIR, "app.js"), "utf8");
const sessionsJs = fs.readFileSync(path.join(JS_DIR, "sessions.js"), "utf8");

check("附件轨在 #input-row 上方，整体留在 #composer-body / #composer-panel 内", () => {
  const body = html.indexOf('id="composer-body"');
  const track = html.indexOf('id="attachment-track"');
  const row = html.indexOf('id="input-row"');
  const skillsTab = html.indexOf('id="tab-skills"');
  assert.ok(body > 0 && track > body, "#attachment-track 应在 #composer-body 内");
  assert.ok(track < row, "#attachment-track 必须在 #input-row 上方");
  assert.ok(row < skillsTab, "#input-row 应留在 #composer-panel 内");
});
check("📎 按钮 + 隐藏 file input（accept=image/* multiple）都在输入行里", () => {
  const row = html.indexOf('id="input-row"');
  const btn = html.indexOf('id="attach-btn"');
  const input = html.indexOf('id="attach-file"');
  const sendBtn = html.indexOf('id="send-btn"');
  assert.ok(btn > row && btn < sendBtn, "#attach-btn 应在 #input-row 内");
  assert.ok(input > row && input < sendBtn, "#attach-file 应在 #input-row 内");
  const inputTag = html.slice(input, html.indexOf(">", input));
  assert.ok(/accept="image\/\*"/.test(inputTag), "file input 应限定图片: " + inputTag);
  assert.ok(/\bmultiple\b/.test(inputTag), "file input 应允许多选: " + inputTag);
  assert.ok(/#composer-panel \.attach-file \{ display: none; \}/.test(css), "隐藏文件选择器的规则缺失");
});
check("app.js 已接线 initComposerAttachments()（模块聚合入口）", () => {
  assert.ok(/import\s*\{[^}]*initComposerAttachments[^}]*\}\s*from\s*"\.\/js\/attachments\.js"/.test(appJs),
    "app.js 未 import initComposerAttachments");
  assert.ok(/initComposerAttachments\(\);/.test(appJs), "app.js 未调用 initComposerAttachments()");
});
check("attachments.js 导出约定：initComposerAttachments() + composerImagePaths()", () => {
  assert.ok(/export function initComposerAttachments\(\)/.test(attachJs), "缺少 initComposerAttachments() 导出");
  assert.ok(/export function composerImagePaths\(\)/.test(attachJs), "缺少 composerImagePaths() 导出");
});
check("sessions.js：只在发送 prompt 时并入 image_paths，queued 后清空轨道", () => {
  assert.ok(/import\s*\{[^}]*composerImagePaths[^}]*\}\s*from\s*"\.\/attachments\.js"/.test(sessionsJs),
    "sessions.js 未引入 composerImagePaths");
  assert.ok(/if \(payload && payload\.prompt\) \{[\s\S]{0,260}composerImagePaths\(\)/.test(sessionsJs),
    "image_paths 未挂在 prompt 分支上");
  assert.ok(/payload\.image_paths = imagePaths;/.test(sessionsJs), "sessions.js 未写入 payload.image_paths");
  assert.ok(/if \(imagePaths\) \{ clearComposerAttachments\(\); \}/.test(sessionsJs), "queued 分支未清空轨道");
  assert.ok(/if \(json\.image_notes && json\.image_notes\.length\)/.test(sessionsJs),
    "发送瞬间的图片说明应如实透出");
});
check("attachments.js 不伪造缩略图 / 不读本地路径（无 <img> / createObjectURL / FileReader）", () => {
  // 只看代码本体：文件头注释里写明「为什么不放缩略图」是有意为之，不能因此误报。
  const code = attachJs.replace(/^\s*\/\/.*$/gm, "");
  assert.ok(!/<img/i.test(code), "attachments.js 出现了 <img>（本地绝对路径读不到，只会是坏图）");
  assert.ok(!/createObjectURL/.test(code) && !/FileReader/.test(code), "attachments.js 不应尝试本地预览");
});
check("style.css：附件样式齐全且只用既有主题变量（不写死颜色）", () => {
  [".attachment-track {", ".attach-item {", ".attach-remove {", ".attach-name {",
    "#composer-panel.attach-dragover", "#composer-panel .attach-btn"].forEach((sel) => {
    assert.ok(css.indexOf(sel) >= 0, "style.css 缺少选择器: " + sel);
  });
  const start = css.indexOf(".attachment-track {");
  const end = css.indexOf("#input-row { display:");
  assert.ok(start > 0 && end > start, "找不到附件样式块");
  const block = css.slice(start, end);
  assert.ok(/var\(--/.test(block), "附件样式应使用既有主题变量");
  assert.ok(!/:\s*#[0-9a-fA-F]{3,8}\b/.test(block), "附件样式写死了十六进制颜色:\n" + block);
  assert.ok(!/\brgba?\(/.test(block), "附件样式写死了 rgb 颜色:\n" + block);
});

console.log("");
console.log("verify-micro-web-attachments: " + (failures === 0 ? "全部通过" : failures + " 项失败"));
process.exit(failures === 0 ? 0 : 1);
