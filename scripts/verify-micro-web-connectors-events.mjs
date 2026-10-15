// 行为验证:aicli micro web client 连接器对话框——事件订阅编辑(docs/bots/13 §6.1)
// 与「保存策略不再清空未展示键」修复。
// 运行: node scripts/verify-micro-web-connectors-events.mjs  (exit 0 = 全部通过)
// 覆盖:
//   1. eventSubRowHTML:字段/选中态/勾选态/删除按钮结构;
//   2. renderEventSubRows → collectEventSubs 回填-收集往返(ids 解析、enabled:false 保留);
//   3. 总开关关闭 = 仅 {enabled:false}(与 React mergeEventsIntoPayload 同语义);
//   4. onModalBodyClick 委托:添加行/删除行;
//   5. collectEditor:编辑态保留既有 policy 未展示键(notify/content_mirror),
//      覆盖 route/events;新建态不继承。
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const root = path.dirname(fileURLToPath(import.meta.url)) + "/..";
const jsPath = path.join(root, "backend/cmd/aicli/commands/web/js/connectors.js");
const src = readFileSync(jsPath, "utf8");

let failures = 0;
const ok = (cond, name, detail) => {
  if (cond) { console.log("  ok  - " + name); }
  else { failures++; console.error("  FAIL - " + name + (detail !== undefined ? " :: " + JSON.stringify(detail) : "")); }
};
const eq = (a, b) => JSON.stringify(a) === JSON.stringify(b);

// ---- 静态守卫 ----
ok(/function collectEventSubs\(/.test(src), "源码包含 collectEventSubs");
ok(/function onModalBodyClick[\s\S]*add-event-sub/.test(src), "onModalBodyClick 委托包含 add-event-sub");
ok(/Object\.assign\(\{\}, current\.policy\)/.test(src), "collectEditor 编辑态保留既有 policy(浅拷贝)");
ok(/connector-field-check/.test(src), "fieldHTML 勾选控件走行内布局(不单独占行)");
ok(/eventTypeCheckboxesHTML/.test(src) && /connector-types-picker/.test(src), "事件类型为折叠多选面板组件(非平铺按钮)");
ok(!/eventTypeChipsHTML/.test(src), "旧的平铺 chips 实现已移除");
ok(/EVENT_TYPE_FORMS/.test(src) && /data-types-warning/.test(src), "类型×投递形态相容性告警已接入");
ok(/refreshDeliverSelect/.test(src) && /splitEventSubRow/.test(src), "细项→粗项推导与一键拆分已接入");
ok(/eventTypeSections/.test(src) && /EVENT_TYPE_SECTION_ORDER/.test(src), "类型面板按投递形态分组(types↔deliver 关联显示)");

// ---- 最小 DOM mock ----
function matchesSelector(node, sel) {
  const m = String(sel).match(/^\[([\w-]+)(?:="([^"]*)")?\]$/);
  if (!m) { return false; }
  const attr = m[1], val = m[2];
  if (!(attr in (node.attributes || {}))) { return false; }
  return val === undefined || String(node.attributes[attr]) === val;
}
function makeNode(tag, attrs = {}) {
  const node = {
    tagName: tag.toUpperCase(),
    attrs: Object.assign({}, attrs.attr || {}),
    attributes: Object.assign({}, attrs.attr || {}),
    id: attrs.id || "",
    value: attrs.value || "",
    checked: !!attrs.checked,
    children: [],
    parentNode: null,
    listeners: {},
    _innerHTML: "",
    addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); },
    appendChild(c) { c.parentNode = this; this.children.push(c); return c; },
    removeChild(c) {
      const i = this.children.indexOf(c);
      if (i >= 0) { this.children.splice(i, 1); c.parentNode = null; }
      return c;
    },
    getAttribute(k) { return k in this.attributes ? this.attributes[k] : null; },
    hasAttribute(k) { return k in this.attributes; },
    setAttribute(k, v) { this.attributes[k] = String(v); },
    closest(sel) {
      let cur = this;
      while (cur) { if (matchesSelector(cur, sel)) { return cur; } cur = cur.parentNode; }
      return null;
    },
    querySelector(sel) { return this.querySelectorAll(sel)[0] || null; },
    querySelectorAll(sel) {
      const out = [];
      const walk = el => {
        for (const c of el.children) {
          if (matchesSelector(c, sel)) { out.push(c); }
          walk(c);
        }
      };
      walk(this);
      return out;
    },
    dispatch(type) {
      const ev = { target: this, preventDefault() {}, stopPropagation() {} };
      let cur = this;
      while (cur) {
        for (const fn of (cur.listeners && cur.listeners[type]) || []) { fn(ev); }
        cur = cur.parentNode;
      }
    },
    get innerHTML() { return this._innerHTML; },
    set innerHTML(html) {
      this._innerHTML = String(html || "");
      this.children = parseEventSubRows(this._innerHTML).map(r => { r.parentNode = this; return r; });
    },
    insertAdjacentHTML(pos, html) {
      const frag = String(html || "");
      this._innerHTML = this._innerHTML + frag;
      for (const r of parseEventSubRows(frag)) { r.parentNode = this; this.children.push(r); }
    },
  };
  return node;
}

// parseEventSubRows 只解析本编辑器产出的行标记(字段/选中态/勾选态/删除按钮)。
function parseEventSubRows(html) {
  const rows = [];
  const chunks = String(html).split("data-event-sub>");
  for (let i = 1; i < chunks.length; i++) {
    rows.push(parseEventSubRow(chunks[i]));
  }
  return rows;
}

function attrsOfTag(tagHtml) {
  const out = {};
  const re = /([\w-]+)(?:="([^"]*)")?/g;
  let m;
  const body = tagHtml.replace(/^<\w+/, "").replace(/>$/, "");
  while ((m = re.exec(body)) !== null) { out[m[1]] = m[2] === undefined ? "" : decodeEntities(m[2]); }
  return out;
}

function decodeEntities(s) {
  return String(s).replace(/&amp;/g, "&").replace(/&lt;/g, "<").replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"').replace(/&#39;/g, "'");
}

function parseEventSubRow(chunk) {
  const row = makeNode("div", { class: "connector-event-sub", attr: { "data-event-sub": "" } });
  const addField = (name, tag, attrs, isSelect, optionsHtml) => {
    const node = makeNode(isSelect ? "select" : tag, { attr: Object.assign({ "data-sub-field": name }, attrs.attr || {}) });
    node.attributes = node.attrs;
    node.value = attrs.value || "";
    node.checked = !!attrs.checked;
    node.disabled = !!attrs.disabled;
    if (isSelect) {
      const m = String(optionsHtml || "").match(/<option value="([^"]*)" selected>/);
      node.value = m ? decodeEntities(m[1]) : "";
    }
    row.appendChild(node);
  };
  const inputTag = name => {
    const re = new RegExp('<input[^>]*data-sub-field="' + name + '"[^>]*>');
    const m = chunk.match(re);
    if (!m) { return null; }
    return attrsOfTag(m[0]);
  };
  const selectHtml = name => {
    const re = new RegExp('<select[^>]*data-sub-field="' + name + '"[^>]*>([\\s\\S]*?)<\\/select>');
    const m = chunk.match(re);
    return m ? { tag: m[0], options: m[1] } : null;
  };
  addField("id", "input", { value: (inputTag("id") || {}).value || "" });
  const mode = selectHtml("sessions-mode");
  addField("sessions-mode", "select", {}, true, mode ? mode.options : "");
  const idsAttrs = inputTag("sessions-ids") || {};
  addField("sessions-ids", "input", { value: idsAttrs.value || "", disabled: "disabled" in idsAttrs });
  const deliver = selectHtml("deliver");
  addField("deliver", "select", {}, true, deliver ? deliver.options : "");
  if (/data-deliver-hint/.test(chunk)) {
    const hint = makeNode("span", { attr: { "data-deliver-hint": "" } });
    hint.textContent = "";
    row.appendChild(hint);
  }
  const enabledAttrs = inputTag("enabled") || {};
  addField("enabled", "input", { checked: "checked" in enabledAttrs });
  if (/data-editor="split-event-sub"/.test(chunk)) {
    const splitBtn = makeNode("button", { attr: { "data-editor": "split-event-sub" } });
    splitBtn.hidden = true;
    row.appendChild(splitBtn);
  }
  // 事件类型多选:折叠面板(汇总行 + 原生 checkbox;词表外值追加在末尾) + 自定义兜底输入。
  const summaryMatch = chunk.match(/<summary><span data-types-summary>([\s\S]*?)<\/span><\/summary>/);
  const summary = makeNode("span", { attr: { "data-types-summary": "" } });
  summary.textContent = summaryMatch ? decodeEntities(summaryMatch[1]) : "";
  row.appendChild(summary);
  if (/data-types-warning/.test(chunk)) {
    const warning = makeNode("span", { attr: { "data-types-warning": "" } });
    warning.textContent = "";
    warning.hidden = true;
    row.appendChild(warning);
  }
  const boxRe = /<input[^>]*data-sub-type="([^"]*)"[^>]*>/g;
  let boxMatch;
  while ((boxMatch = boxRe.exec(chunk)) !== null) {
    const node = makeNode("input", { attr: { "data-sub-type": decodeEntities(boxMatch[1]) } });
    node.checked = /\bchecked\b/.test(boxMatch[0]);
    row.appendChild(node);
  }
  addField("types-extra", "input", { value: (inputTag("types-extra") || {}).value || "" });
  if (/data-editor="remove-event-sub"/.test(chunk)) {
    row.appendChild(makeNode("button", { attr: { "data-editor": "remove-event-sub" } }));
  }
  return row;
}

// ---- document mock ----
const documentMock = {
  children: [],
  listeners: {},
  getElementById(id) {
    const find = el => {
      if (el.id === id) { return el; }
      for (const c of el.children) { const r = find(c); if (r) { return r; } }
      return null;
    };
    for (const top of this.children) { const r = find(top); if (r) { return r; } }
    return null;
  },
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); },
  appendChild(el) { this.children.push(el); },
};
globalThis.document = documentMock;

// ---- 编译源码(去 import/export)并真跑内部函数 ----
const stripped = src
  .replace(/^import[\s\S]*?from\s+"\.\/[^"]+";/gm, "")
  .replace(/^export\s+function/gm, "function");
const sandbox = `
var esc = function(s){ return String(s == null ? "" : s).replace(/[&<>"']/g, function(c){ return c === "&" ? "&amp;" : c === "<" ? "&lt;" : c === ">" ? "&gt;" : c === '"' ? "&quot;" : "&#39;"; }); };
var showToast = function(){};
var apiJSON = function(){ return Promise.resolve({}); };
var jsonInit = function(){ return {}; };
var copyText = function(){};
var loadRouteCatalog = function(){ return Promise.resolve(); };
var schemaProviders = [], connectorItems = [], editorState = null;
` + stripped + `
return {
  collectEditor: collectEditor,
  collectEventSubs: collectEventSubs,
  eventSubRowHTML: eventSubRowHTML,
  renderEventSubRows: renderEventSubRows,
  onModalBodyClick: onModalBodyClick,
  onModalBodyChange: onModalBodyChange,
  onModalBodyInput: onModalBodyInput,
  splitSubList: splitSubList,
  setSchemaProviders: function(v){ schemaProviders = v; },
  setItems: function(v){ connectorItems = v; },
  setEditorState: function(v){ editorState = v; }
};
`;
const api = new Function("document", sandbox)(documentMock);

// ---- 构建表单 DOM(与 index.html + renderEditor 产出同构) ----
const modalBody = makeNode("div", { id: "connectors-modal-body" });
documentMock.appendChild(modalBody);
const input = (id, value, checked) => {
  const n = makeNode("input", { id, value: value || "" });
  n.value = value || "";
  n.checked = !!checked;
  modalBody.appendChild(n);
  return n;
};
const name_ = input("connector-form-name", "tg-main");
input("connector-form-enabled", "", true);
input("connector-form-mode", "polling");
input("connector-form-allowed-users", "u1\nu2");
input("connector-form-command-prefix", "/");
input("connector-form-session-mode", "chat");
input("connector-form-reply-mode", "stream");
input("connector-form-runtime-owner", "");
input("connector-form-route-provider", "p2");
input("connector-form-route-model", "m2");
input("connector-form-route-reasoning", "high");
const eventsEnabled = input("connector-form-events-enabled", "", false);
const subsHost = makeNode("div", { id: "connector-event-subs" });
modalBody.appendChild(subsHost);

// ===== 1. eventSubRowHTML 结构与组件化 =====
{
  const html = api.eventSubRowHTML({
    id: "main-chat",
    sessions: { mode: "ids", ids: ["session_a", "session_b"] },
    types: ["assistant_message", "turn.*"],
    deliver: "content",
  });
  ok(html.includes('data-sub-field="id"') && html.includes('value="main-chat"'), "行 HTML 含 id 字段与值");
  ok(/data-sub-field="sessions-mode">[\s\S]*<option value="ids" selected>/.test(html), "sessions-mode 选中 ids");
  ok(html.includes('data-sub-field="sessions-ids"') && html.includes('value="session_a, session_b"'), "ids 列表回填为逗号分隔");
  ok(!/data-sub-field="sessions-ids"[^>]*disabled/.test(html), "mode=ids 时会话列表可用");
  ok(/data-sub-field="deliver">[\s\S]*<option value="content" selected>/.test(html), "deliver 选中 content");
  ok(html.includes('class="connector-field connector-field-check"><input type="checkbox" data-sub-field="enabled" checked><span class="connector-field-label">启用</span>'),
    "启用勾选为行内布局(勾选框在文字前,不单独占行)");
  ok(/<details class="connector-types-picker">/.test(html), "事件类型为折叠多选面板(details),不平铺按钮");
  ok(!/connector-type-chip/.test(html), "不再渲染平铺 chips 按钮");
  const boxCount = (html.match(/data-sub-type="/g) || []).length;
  ok(boxCount === 20, "面板含 20 个多选项(词表全量)", boxCount);
  ok(/content\(正文副本\)[\s\S]*data-sub-type="assistant_message"/.test(html), "分组:content 组含 assistant_message");
  ok(/stream\(流式编辑\)[\s\S]*data-sub-type="assistant_delta"/.test(html), "分组:stream 组含 assistant_delta");
  ok(/digest\(聚合\)[\s\S]*data-sub-type="tool\.completed"/.test(html), "分组:digest 组含 tool.completed");
  ok(/未接线\(勾选不会投递\)[\s\S]*data-sub-type="user_message"/.test(html), "分组:未接线组含 user_message");
  ok(html.includes('connector-type-hint">conversation'), "条目提示为族名(conversation)");
  ok(/data-sub-type="assistant_message" checked/.test(html)
    && /data-sub-type="turn\.\*" checked/.test(html)
    && !/data-sub-type="error" checked/.test(html), "选中项勾选,未选项不勾选");
  ok(html.includes("已选 2 项:assistant_message、turn.*"), "汇总行显示已选数量与名称");
  ok(html.includes('data-sub-field="types-extra"'), "保留自定义类型输入(兜底)");
  ok(!/data-sub-field="types"/.test(html), "事件类型不再使用文本输入框");
  ok(html.includes("data-deliver-hint"), "投递形态含推导提示位");
  ok(html.includes('data-editor="split-event-sub"'), "行内含一键拆分按钮(默认隐藏)");
  ok(html.includes('data-editor="remove-event-sub"'), "行内含删除按钮");
  const htmlOff = api.eventSubRowHTML({ id: "x", enabled: false });
  ok(!/data-sub-field="enabled" checked>/.test(htmlOff), "enabled:false 不勾选");
  ok(/data-sub-field="sessions-mode">[\s\S]*<option value="bound" selected>/.test(htmlOff), "空订阅默认 bound");
  ok(/data-sub-field="deliver">[\s\S]*<option value="notice" selected>/.test(htmlOff), "空订阅默认 notice");
  ok(/data-sub-field="sessions-ids"[^>]*disabled/.test(htmlOff), "bound 模式会话列表置灰");
}

// ===== 2. renderEventSubRows → collectEventSubs 往返 =====
{
  const subs = [
    { id: "main-chat", sessions: { mode: "bound" }, types: ["assistant_message"], deliver: "content" },
    { id: "live", sessions: { mode: "ids", ids: ["session_a", "session_b"] }, types: ["assistant_delta"], deliver: "stream", enabled: false },
  ];
  api.renderEventSubRows(subs);
  ok(subsHost.children.length === 2, "回填后容器含 2 行", subsHost.children.length);
  eventsEnabled.checked = true;
  const collected = api.collectEventSubs();
  ok(collected.enabled === true && collected.subscriptions.length === 2, "收集 2 条订阅", collected);
  ok(collected.subscriptions[0].id === "main-chat"
    && collected.subscriptions[0].deliver === "content"
    && eq(collected.subscriptions[0].types, ["assistant_message"])
    && collected.subscriptions[0].sessions.mode === "bound", "第 1 条字段往返一致", collected.subscriptions[0]);
  ok(collected.subscriptions[1].enabled === false, "enabled:false 单条保留", collected.subscriptions[1]);
  ok(collected.subscriptions[1].sessions.mode === "ids"
    && eq(collected.subscriptions[1].sessions.ids, ["session_a", "session_b"]), "ids 列表往返一致", collected.subscriptions[1].sessions);
}

// ===== 3. 总开关关闭 = 仅 enabled:false =====
{
  eventsEnabled.checked = false;
  ok(eq(api.collectEventSubs(), { enabled: false }), "关闭时只写 enabled:false(不保留 subscriptions)");
  eventsEnabled.checked = true;
}

// ===== 4. onModalBodyClick 委托:添加/删除行 =====
{
  const before = subsHost.children.length;
  const addBtn = makeNode("button", { attr: { "data-editor": "add-event-sub" } });
  modalBody.appendChild(addBtn);
  api.onModalBodyClick({ target: addBtn });
  ok(subsHost.children.length === before + 1, "点添加按钮 → 追加一行", subsHost.children.length);
  const newRow = subsHost.children[subsHost.children.length - 1];
  const removeBtn = newRow.querySelector('[data-editor="remove-event-sub"]');
  ok(!!removeBtn, "新行含删除按钮");
  api.onModalBodyClick({ target: removeBtn });
  ok(subsHost.children.length === before, "点删除按钮 → 移除该行", subsHost.children.length);
}

// ===== 5. collectEditor:编辑态保留未展示键 + 覆盖 route/events =====
{
  api.setSchemaProviders([{ provider: "telegram", label: "Telegram", fields: [] }]);
  api.setItems([{
    id: "c1", provider: "telegram", name: "tg-main",
    policy: {
      allowed_users: ["u9"], command_prefix: "!", content_mirror: true,
      notify: { enabled: true, kinds: ["approval"], scope: "all" },
      route: { provider: "old", model: "old-m", reasoning_effort: "low" },
      events: { enabled: false },
    },
  }]);
  api.setEditorState({ id: "c1", isEdit: true, provider: "telegram" });
  eventsEnabled.checked = true;
  api.renderEventSubRows([{ id: "ops", sessions: { mode: "bound" }, types: ["turn.*"], deliver: "digest" }]);
  const payload = api.collectEditor();
  ok(eq(payload.policy.notify, { enabled: true, kinds: ["approval"], scope: "all" }), "既有 notify 保留(不再被清空)", payload.policy.notify);
  ok(payload.policy.content_mirror === true, "既有 content_mirror 保留", payload.policy.content_mirror);
  ok(payload.policy.command_prefix === "/", "表单字段覆盖 command_prefix", payload.policy.command_prefix);
  ok(payload.policy.route.provider === "p2" && payload.policy.route.model === "m2", "route 由表单覆盖", payload.policy.route);
  ok(payload.policy.events.enabled === true
    && payload.policy.events.subscriptions[0].id === "ops"
    && payload.policy.events.subscriptions[0].deliver === "digest", "events 由表单覆盖", payload.policy.events);
  ok(eq(payload.policy.allowed_users, ["u1", "u2"]), "allowed_users 来自表单", payload.policy.allowed_users);
  ok(payload.policy.id === undefined && payload.provider === undefined, "编辑态不发送 id/provider 字段");
}

// ===== 6. collectEditor:新建态不继承既有 policy =====
{
  api.setEditorState({ id: "", isEdit: false, provider: "telegram" });
  eventsEnabled.checked = false;
  const payload = api.collectEditor();
  ok(!("notify" in payload.policy) && !("content_mirror" in payload.policy), "新建态不继承既有 policy 键", payload.policy);
  ok(eq(payload.policy.events, { enabled: false }), "新建态 events 只写开关", payload.policy.events);
  ok(payload.provider === "telegram", "新建态包含 provider");
}

// ===== 7. splitSubList 边界 =====
{
  ok(eq(api.splitSubList("a, b，c  d"), ["a", "b", "c", "d"]), "逗号/中文逗号/空白混排解析");
  ok(eq(api.splitSubList(""), []), "空串 → 空数组");
}

// ===== 8. 词表外类型保留(追加勾选项) + checkbox/汇总行联动 =====
{
  eventsEnabled.checked = true;
  api.renderEventSubRows([{ id: "legacy", types: ["completion", "custom.type", "turn.*"] }]);
  const row = subsHost.children[0];
  const customBox = row.querySelector('[data-sub-type="custom.type"]');
  ok(!!customBox && customBox.checked === true, "词表外类型追加为已勾选项(往返不丢)");
  const turnBox = row.querySelector('[data-sub-type="turn.*"]');
  ok(turnBox.checked === true, "词表内选中项已勾选");
  const first = api.collectEventSubs().subscriptions[0];
  ok(eq(first.types, ["turn.*", "completion", "custom.type"]), "收集保留词表内/外全部类型", first.types);
  const summary = row.querySelector("[data-types-summary]");
  ok(/^已选 3 项:/.test(summary.textContent), "汇总行计数=3", summary.textContent);
  const box = row.querySelector('[data-sub-type="assistant_message"]');
  box.checked = true;
  api.onModalBodyChange({ target: box });
  ok(/^已选 4 项:/.test(summary.textContent), "勾选后汇总行计数更新", summary.textContent);
  const after = api.collectEventSubs().subscriptions[0];
  ok(after.types.indexOf("assistant_message") >= 0, "勾选后的类型进入收集结果");
  box.checked = false;
  api.onModalBodyChange({ target: box });
  ok(/^已选 3 项:/.test(summary.textContent), "取消勾选后汇总行回退", summary.textContent);
  const extra = row.querySelector('[data-sub-field="types-extra"]');
  extra.value = "approval.alias";
  api.onModalBodyInput({ target: extra });
  ok(/^已选 4 项:/.test(summary.textContent), "自定义输入计入汇总");
  const withExtra = api.collectEventSubs().subscriptions[0];
  ok(withExtra.types.indexOf("approval.alias") >= 0, "自定义输入进入收集结果");
}

// ===== 9. 会话列表随模式联动(非 ids 置灰) =====
{
  api.renderEventSubRows([{ id: "m1", sessions: { mode: "bound" } }]);
  const row = subsHost.children[0];
  const ids = row.querySelector('[data-sub-field="sessions-ids"]');
  ok(ids.disabled === true, "bound 模式:会话列表置灰");
  const modeSel = row.querySelector('[data-sub-field="sessions-mode"]');
  modeSel.value = "ids";
  api.onModalBodyChange({ target: modeSel });
  ok(ids.disabled === false, "切到 ids:会话列表可用");
  modeSel.value = "all";
  api.onModalBodyChange({ target: modeSel });
  ok(ids.disabled === true, "切到 all:会话列表再次置灰");
}

// ===== 10. 类型 × 形态相容性告警 + 总开关自动打开(2026-10-15 现场问题回归) =====
{
  eventsEnabled.checked = true;
  api.renderEventSubRows([{ id: "misconfig", deliver: "notice", types: ["assistant_message", "user_message", "agent.turn.finished"] }]);
  const row = subsHost.children[0];
  const warning = row.querySelector("[data-types-warning]");
  ok(warning.hidden === false, "类型×形态不匹配时告警可见");
  ok(/assistant_message 需要 deliver=content/.test(warning.textContent), "提示 assistant_message 需要 content", warning.textContent);
  ok(/user_message 暂未接出站映射/.test(warning.textContent), "提示 user_message 未接线", warning.textContent);
  ok(!/agent\.turn\.finished/.test(warning.textContent), "notice 可用的 completion 不告警", warning.textContent);
  const deliverSel = row.querySelector('[data-sub-field="deliver"]');
  deliverSel.value = "content";
  api.onModalBodyChange({ target: deliverSel });
  ok(!/assistant_message/.test(warning.textContent), "切到 content 后 assistant_message 不再告警", warning.textContent);
  ok(/agent\.turn\.finished 需要 deliver=notice\/digest/.test(warning.textContent), "切到 content 后 completion 转为告警", warning.textContent);
  eventsEnabled.checked = false;
  const box = row.querySelector('[data-sub-type="assistant_message"]');
  box.checked = false;
  api.onModalBodyChange({ target: box });
  ok(eventsEnabled.checked === false, "取消勾选不会打开总开关");
  box.checked = true;
  api.onModalBodyChange({ target: box });
  ok(eventsEnabled.checked === true, "勾选类型自动打开总开关(避免保存成 enabled:false 的无效订阅)");
}

// ===== 11. 细项→粗项推导:唯一形态锁定 / 多形态收敛 / 跨形态一键拆分 =====
{
  eventsEnabled.checked = true;
  // a) 唯一形态:assistant_message → content 锁定
  api.renderEventSubRows([{ id: "d1", types: ["assistant_message"] }]);
  let row = subsHost.children[0];
  let deliver = row.querySelector('[data-sub-field="deliver"]');
  ok(deliver.value === "content", "assistant_message 推导 deliver=content", deliver.value);
  ok(deliver.disabled === true, "唯一形态锁定(无需用户配置)");
  ok(/由所选类型推导/.test(row.querySelector("[data-deliver-hint]").textContent), "提示由类型推导");
  // b) 多形态类型:completion 仅可选 notice/digest
  api.renderEventSubRows([{ id: "d2", types: ["agent.turn.finished"] }]);
  row = subsHost.children[0];
  deliver = row.querySelector('[data-sub-field="deliver"]');
  ok(deliver.disabled === false, "completion 保持可选(notice/digest)");
  ok(deliver.innerHTML.includes('value="notice"') && deliver.innerHTML.includes('value="digest"')
    && !deliver.innerHTML.includes('value="content"'), "选项收敛到 notice/digest");
  // c) 跨形态混合 → 冲突告警 + 拆分按钮
  api.renderEventSubRows([{ id: "mix", deliver: "notice", types: ["assistant_message", "agent.turn.finished"] }]);
  row = subsHost.children[0];
  ok(/assistant_message 需要 deliver=content/.test(row.querySelector("[data-types-warning]").textContent), "混合时提示冲突");
  const splitBtn = row.querySelector('[data-editor="split-event-sub"]');
  ok(splitBtn.hidden === false, "跨形态冲突时显示拆分按钮");
  // d) 点击拆分 → 两条单一形态订阅
  api.onModalBodyClick({ target: splitBtn });
  const models = api.collectEventSubs().subscriptions;
  ok(models.length === 2, "拆分为 2 条订阅", models.length);
  ok(models[0].deliver === "content" && eq(models[0].types, ["assistant_message"]), "第 1 条 content", models[0]);
  ok(models[1].deliver === "notice" && eq(models[1].types, ["agent.turn.finished"]), "第 2 条 notice", models[1]);
  ok(models[1].id === "mix-2", "续行 id 追加序号", models[1].id);
  ok(subsHost.children[1].querySelector("[data-types-warning]").hidden === true, "拆分后无残留告警");
}

console.log(failures === 0 ? "\n全部通过" : "\n存在失败项: " + failures);
process.exit(failures === 0 ? 0 : 1);
