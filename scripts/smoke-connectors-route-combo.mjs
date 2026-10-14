// 冒烟验证:mock 最小 DOM,真实执行 connectors.js 生产函数体
// (编译源码后逐函数调用),杜绝"语法过但运行时炸"再次交付。
// 用法: node scripts/smoke-connectors-route-combo.mjs  (exit 0 = 全部通过)
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

// ---- 回归守卫:document.dataset 属性访问禁止再出现(dataset 只在 HTMLElement 上;
// 注释里的文字提及不算,只拦真实代码访问 document.dataset.xxx / [xxx]) ----
ok(!/document\.dataset\s*[.\[]/.test(src), "源码不得访问 document.dataset.* 属性(浏览器上 document.dataset === undefined,会 TypeError)");

// ---- 最小 DOM mock(仅覆盖 connectors.js 路由 combo 用到的 API 面) ----
function makeNode(tag, attrs = {}, parent = null) {
  const attrMap = Object.assign({}, attrs.attr || {});
  const node = {
    tagName: tag.toUpperCase(),
    id: attrs.id || "",
    value: attrs.value || "",
    children: [],
    parentNode: parent,
    style: {},
    dataset: {},
    listeners: {},
    attributes: attrMap,
    setAttribute(k, v) { this.attributes[k] = String(v); },
    getAttribute(k) { return k in this.attributes ? this.attributes[k] : null; },
    hasAttribute(k) { return k in this.attributes; },
    // innerHTML setter:轻量解析生产代码会写入的两种片段(弹层 button / datalist
    // option),转成子节点供 querySelectorAll 走真实 DOM 语义。
    set innerHTML(html) {
      this._innerHTML = String(html);
      this.children = [];
      if (!this._innerHTML) { return; }
      const btnRe = /<button\b[^>]*>/g;
      let m;
      while ((m = btnRe.exec(this._innerHTML)) !== null) {
        const tagHtml = m[0];
        const cls = (tagHtml.match(/class="([^"]*)"/) || [])[1] || "";
        const val = (tagHtml.match(/data-route-combo-value="([^"]*)"/) || [])[1];
        if (val === undefined) { continue; }
        const btn = makeNode("button", { class: cls, attr: { "data-route-combo-value": val } });
        btn.parentNode = this;
        this.children.push(btn);
      }
      const optRe = /<option\b[^>]*value="([^"]*)"[^>]*>/g;
      while ((m = optRe.exec(this._innerHTML)) !== null) {
        const opt = makeNode("option", { attr: { value: m[1] } });
        opt.parentNode = this;
        this.children.push(opt);
      }
      // 弹层检索行:filter input + count span + no-match div
      if (/<div class="cfg-popup-filter">/.test(this._innerHTML)) {
        const filterNode = makeNode("input", { attr: { "data-popup-filter": "" } });
        filterNode.tagName = "INPUT";
        filterNode.parentNode = this;
        this.children.push(filterNode);
        const countNode = makeNode("span", { attr: { "data-popup-count": "" } });
        countNode.tagName = "SPAN";
        countNode.parentNode = this;
        this.children.push(countNode);
        const noMatchNode = makeNode("div", { class: "cfg-popup-no-match" });
        noMatchNode.parentNode = this;
        this.children.push(noMatchNode);
      }
    },
    get innerHTML() { return this._innerHTML || ""; },
    addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); },
    appendChild(c) { c.parentNode = this; this.children.push(c); return c; },
    focus() {},
    blur() {},
    replaceWith(n) {
      if (!this.parentNode) { return; }
      const idx = this.parentNode.children.indexOf(this);
      this.parentNode.children[idx] = n;
      n.parentNode = this.parentNode;
    },
    closest(sel) {
      const match = el => {
        if (sel.startsWith(".")) { return (el.classList || { contains: () => false }).contains(sel.slice(1).split("[")[0]); }
        if (sel.startsWith("[")) { return sel.slice(1, -1) in el.attributes; }
        const m = sel.replace("]", "").split("[");
        return (el.classList || { contains: () => false }).contains(m[0].slice(1)) && (m[1] in el.attributes);
      };
      let cur = this;
      while (cur) { if (cur.tagName && cur.tagName !== "#document" && match(cur)) { return cur; } cur = cur.parentNode; }
      return null;
    },
    querySelector(sel) { return this.querySelectorAll(sel)[0] || null; },
    querySelectorAll(sel) {
      const out = [];
      const walk = el => {
        for (const c of el.children) {
          const cls = c.classList;
          if (sel === ".connector-combo-popup" && cls && cls.contains("connector-combo-popup")) { out.push(c); }
          if (sel === "option[value]" && c.tagName === "OPTION" && "value" in c.attributes) { out.push(c); }
          if (sel === "[data-route-combo-toggle]" && "data-route-combo-toggle" in c.attributes) { out.push(c); }
          if (sel === "[data-route-combo-value]" && "data-route-combo-value" in c.attributes) { out.push(c); }
          if (sel === "[data-popup-filter]" && "data-popup-filter" in c.attributes) { out.push(c); }
          if (sel === "[data-popup-count]" && "data-popup-count" in c.attributes) { out.push(c); }
          if (sel === "input" && c.tagName === "INPUT") { out.push(c); }
          if (sel.startsWith(".") && cls && cls.contains(sel.slice(1))) { out.push(c); }
          walk(c);
        }
      };
      walk(this);
      return out;
    },
    dispatch(type, target) {
      const ev = { target: target || this, preventDefault() {}, stopPropagation() {} };
      let cur = target || this;
      while (cur) {
        for (const fn of (cur.listeners && cur.listeners[type]) || []) { fn(ev); }
        cur = cur.parentNode;
      }
    },
    click() { this.dispatch("click"); }
  };
  if (attrs.class) { node.classList = makeClassList(attrs.class); }
  return node;
}
function makeClassList(str) {
  const set = new Set(str.split(" ").filter(Boolean));
  return { contains: c => set.has(c), add: c => set.add(c), remove: c => set.delete(c) };
}

const documentMock = {
  children: [], listeners: {}, dataset: undefined, // 与浏览器一致:document.dataset 为 undefined
  getElementById(id) {
    const find = el => {
      if (el.id === id) { return el; }
      for (const c of el.children) { const r = find(c); if (r) { return r; } }
      return null;
    };
    for (const top of this.children) { const r = find(top); if (r) { return r; } }
    return null;
  },
  querySelectorAll(sel) {
    const out = [];
    const walk = el => {
      for (const c of el.children) {
        if (sel === ".connector-combo-popup" && c.classList && c.classList.contains("connector-combo-popup")) { out.push(c); }
        walk(c);
      }
    };
    for (const top of this.children) { walk(top); }
    return out;
  },
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); },
  appendChild(el) { this.children.push(el); }
};
globalThis.document = documentMock;

// ---- 构建模态框 DOM(与 index.html + renderEditor 产出同构) ----
const modalBody = makeNode("div", { id: "connectors-modal-body" });
documentMock.appendChild(modalBody);
const grid = makeNode("div", { class: "connector-form-grid" });
modalBody.appendChild(grid);
const combo = (id, val) => {
  const field = makeNode("div", { class: "connector-field" });
  const labelSpan = makeNode("span", { class: "connector-field-label" });
  const wrap = makeNode("span", { class: "cfg-combo-wrap connector-combo" });
  const input = makeNode("input", { id, value: val });
  const toggle = makeNode("button", { id: id + "-toggle", attr: { "data-route-combo-toggle": id } });
  const popup = makeNode("div", { id: id + "-popup", class: "cfg-combo-popup connector-combo-popup", attr: { "data-route-combo-for": id } });
  popup.style.display = "none";
  wrap.appendChild(input); wrap.appendChild(toggle); wrap.appendChild(popup);
  field.appendChild(labelSpan); field.appendChild(wrap);
  grid.appendChild(field);
  return { input, toggle, popup, field };
};
const provider = combo("connector-form-route-provider", "workbuddy");
const model = combo("connector-form-route-model", "cn:glm-5.3-flash");
const reasoning = combo("connector-form-route-reasoning", "medium");
const dl = (id, vals) => {
  const dlNode = makeNode("datalist", { id });
  dlNode.setAttribute("hidden", "");
  for (const v of vals) {
    const o = makeNode("option", { attr: { value: v } });
    dlNode.appendChild(o);
  }
  grid.appendChild(dlNode);
  return dlNode;
};
dl("connector-route-provider-list", ["workbuddy", "CODEX_04", "workbuddy_anthropic"]);
dl("connector-route-model-list", ["cn:auto", "cn:glm-5.3-flash", "cn:glm-5.1"]);
dl("connector-route-reasoning-list", ["minimal", "low", "medium", "high", "max", "xhigh"]);

// ---- 编译源码(去 import/export)并真跑内部函数 ----
const stripped = src
  .replace(/^import[\s\S]*?from\s+"\.\/[^"]+";/gm, "")
  .replace(/^export\s+function/gm, "function");
const sandbox = `
var connEl = function(id){ return document.getElementById(id); };
var esc = function(s){ return String(s == null ? "" : s).replace(/[&<>"']/g, function(c){ return c === "&" ? "&amp;" : c === "<" ? "&lt;" : c === ">" ? "&gt;" : c === '"' ? "&quot;" : "&#39;"; }); };
var showToast = function(){};
var catalogModelsFor = function(c, p){ return p === "workbuddy" ? ["cn:auto","cn:glm-5.3-flash","cn:glm-5.1"] : (p === "CODEX_04" ? ["gpt-5.2-codex","gpt-5.4","gpt-5.4-mini","gpt-5.4-nano"] : []); };
var catalogProviderNames = function(){ return ["workbuddy","CODEX_04","workbuddy_anthropic"]; };
var catalogReasoningFor = function(c, p, m){ return { options: ["minimal","low","medium","high","max","xhigh"], def: "medium" }; };
var schemaProviders = [], connectorItems = [], connectorsLoadedOnce = false, connectorsSeq = 0, editorState = null, qrState = null, logsSeq = 0, routeCatalog = null, logsState = null, modalOpen = false;
` + stripped + `
return { bindRouteCascade, connectorRouteComboHTML, renderRouteComboPopup,
  closeRouteComboPopups, bindRouteComboOutsideDismiss, toggleRouteComboPopup,
  selectRouteComboValue, refreshRouteDatalists, routeProviderOptions, routeModelOptions, routeReasoningOptions,
  applyRoutePopupFilter, refreshRoutePopupCount, moveRoutePopupFocus,
  setRouteCatalog: function(v){ routeCatalog = v; } };
`;
const api = new Function("document", sandbox)(documentMock);

// 模拟 loadRouteCatalog() 已拉到目录(生产中 GET /web/api/runtime 后填入 routeCatalog)
api.setRouteCatalog({
  providers: [
    { name: "workbuddy", models: ["cn:auto", "cn:glm-5.3-flash", "cn:glm-5.1"] },
    { name: "CODEX_04", models: ["gpt-5.2-codex", "gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano"] }
  ]
});

// ===== 断言 0:combo 结构不得包在 label 里(否则弹层检索框无法聚焦) =====
ok(provider.field.tagName === "DIV", "combo 字段容器是 div.connector-field(非 label)");

// ===== 断言 1:bindRouteCascade 不抛(用户报的崩溃点) =====
let t1err = null;
try { api.bindRouteCascade(); } catch (e) { t1err = e; }
ok(!t1err, "bindRouteCascade() 首次调用不抛异常", t1err && String(t1err));
ok(provider.input.listeners.input.length === 1, "provider input 绑定 1 次 input 监听");
ok(documentMock.listeners.click.length === 1, "外点关闭只注册 1 次 document click 监听");
try { api.bindRouteCascade(); } catch (e) { t1err = e; }
ok(!t1err && documentMock.listeners.click.length === 1, "重复调用 bindRouteCascade 不叠加监听", documentMock.listeners.click.length);

// ===== 断言 2:toggleRouteComboPopup 开/关 =====
api.toggleRouteComboPopup("connector-form-route-model");
ok(model.popup.style.display === "block" && model.popup.querySelectorAll("[data-route-combo-value]").length === 3,
  "model 弹层打开显示 3 项候选(不被当前值过滤)", { display: model.popup.style.display });
ok(!!model.popup.querySelector(".cfg-combo-item") && model.popup.innerHTML.includes("current"),
  "当前值 cn:glm-5.3-flash 高亮 current");
const filterInput = model.popup.querySelector("[data-popup-filter]");
ok(!!filterInput, "弹层内置过滤检索框");
ok(model.popup.querySelector("[data-popup-count]").textContent === "3 项", "计数显示总数 3 项", model.popup.querySelector("[data-popup-count]").textContent);
// 过滤:输入 glm → 只剩 cn:glm-5.3-flash 与 cn:glm-5.1 两项,计数 2 / 3
filterInput.value = "glm";
api.applyRoutePopupFilter ? null : null;
{
  const ev = { target: filterInput, preventDefault() {}, stopPropagation() {} };
  for (const fn of grid.listeners.input || []) { fn(ev); }
}
ok(model.popup.querySelectorAll("[data-route-combo-value]").filter(b => b.style.display !== "none").length === 2,
  "检索 glm 过滤后剩 2 项");
ok(model.popup.querySelector("[data-popup-count]").textContent === "2 / 3", "计数变 2 / 3", model.popup.querySelector("[data-popup-count]").textContent);
// 无命中提示
filterInput.value = "zzz";
{
  const ev = { target: filterInput, preventDefault() {}, stopPropagation() {} };
  for (const fn of grid.listeners.input || []) { fn(ev); }
}
ok(model.popup.querySelector(".cfg-popup-no-match").style.display === "block", "无命中时显示 no-match 提示");
// Enter 选中首个可见项(先恢复过滤为空)
filterInput.value = "";
{
  const ev = { target: filterInput, preventDefault() {}, stopPropagation() {} };
  for (const fn of grid.listeners.input || []) { fn(ev); }
}
{
  const ev = { key: "Enter", target: filterInput, preventDefault() {}, stopPropagation() {} };
  for (const fn of grid.listeners.keydown || []) { fn(ev); }
}
ok(model.input.value === "cn:auto" && model.popup.style.display === "none",
  "检索框 Enter 选中首个可见项并关闭弹层", { value: model.input.value });

// ===== 断言 3:grid 事件委托——toggle click / item click(模拟真实点击路径) =====
model.toggle.dispatch("click", model.toggle);
ok(model.popup.style.display === "block", "委托:点 ▼ 打开 model 弹层");
const item = model.popup.querySelectorAll("[data-route-combo-value]")[0];
const pickedVal = item.getAttribute("data-route-combo-value");
item.dispatch("click", item);
ok(model.input.value === pickedVal, "委托:点选项写回 input 值", { picked: pickedVal, got: model.input.value });
ok(model.popup.style.display === "none", "点选后弹层关闭");

// ===== 断言 4:点选 provider 后下游 datalist 联动刷新 =====
provider.toggle.dispatch("click", provider.toggle);
const codexItem = provider.popup.querySelectorAll("[data-route-combo-value]").filter(b => b.getAttribute("data-route-combo-value") === "CODEX_04")[0];
codexItem.dispatch("click", codexItem);
ok(provider.input.value === "CODEX_04", "provider 点选写回 CODEX_04");
const modelOpts = documentMock.getElementById("connector-route-model-list").querySelectorAll("option[value]").map(o => o.getAttribute("value"));
ok(modelOpts.length === 4 && modelOpts[0] === "gpt-5.2-codex", "model datalist 已按新 provider 刷新(4 项 gpt-*)", modelOpts);

// ===== 断言 5:外点关闭 =====
api.toggleRouteComboPopup("connector-form-route-model");
ok(model.popup.style.display === "block", "重新打开 model 弹层");
documentMock.dispatch ? null : null;
{
  const ev = { target: modalBody, preventDefault() {}, stopPropagation() {} };
  for (const fn of documentMock.listeners.click) { fn(ev); }
}
ok(model.popup.style.display === "none", "点击 combo 外区域关闭所有弹层");

// ===== 断言 6:routeComboListId 契约与组合 HTML =====
ok(api.connectorRouteComboHTML("connector-form-route-x", "(p)", "v", "help", "标签").includes('id="connector-form-route-x"'),
  "connectorRouteComboHTML 产出含 input id");
ok(api.connectorRouteComboHTML("connector-form-route-x", "(p)", "v", "help", "标签").includes("connector-combo-popup"),
  "connectorRouteComboHTML 产出含弹层容器");

// ===== 断言 7:routeCatalog 为 null 时各 options 函数与 refresh 均安全 =====
let t7err = null;
try {
  api.routeProviderOptions(); api.routeModelOptions(); api.routeReasoningOptions(); api.refreshRouteDatalists();
} catch (e) { t7err = e; }
ok(!t7err, "routeCatalog 为 null 时 options/refresh 不抛", t7err && String(t7err));

console.log(failures === 0 ? "\n全部通过" : "\n存在失败项: " + failures);
process.exit(failures === 0 ? 0 : 1);
