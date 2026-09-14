// 行为验证：aicli micro web client 技能页签（Node 沙盒，stub document/fetch/keydown）。
// 运行：node scripts/verify-micro-web-skills-tab.mjs（无需浏览器；见 docs/aicli/web-testing.md 第 4 节）
// 覆盖：首次进入页签拉列表 / 同会话重复切页签不重复请求 / 后台切换会话后进页签强制刷新 /
//       页签可见时切换立即重拉 / 点击条目打开详情（分组页签只出现非空分组、只渲染当前页签）/
//       页签点击与 ← → Home End 键盘导航 / 详情按后端字段渲染不补默认值 /
//       过期详情响应被丢弃 / 列表与详情失败如实显示错误码 / Esc 关闭面板且不冒泡 /
//       ui.js 页签按钮接线。
import assert from "node:assert";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_DIR = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web", "js");

// ---- DOM stub ----
const elements = {};
function makeClassList() {
  return {
    _set: new Set(),
    contains(c) { return this._set.has(c); },
    add(c) { this._set.add(c); },
    remove(c) { this._set.delete(c); },
    toggle(c, on) { on ? this._set.add(c) : this._set.delete(c); },
  };
}
function makeEl(id) {
  const el = {
    id,
    classList: makeClassList(),
    parentNode: null,
    textContent: "",
    _html: "",
    _rows: {},
    _tabButtons: {},
    listeners: {},
    set innerHTML(v) { this._html = v; this._rows = {}; this._tabButtons = {}; },
    get innerHTML() { return this._html; },
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    dispatch(type, event) { (this.listeners[type] || []).forEach((fn) => fn(event || {})); },
    querySelector(sel) {
      const m = /^button\[data-skill-tab="(.+)"\]$/.exec(sel);
      if (!m) { return null; }
      return this._tabButtons[m[1]] || (this._tabButtons[m[1]] = makeTabButton(m[1]));
    },
    querySelectorAll(sel) {
      if (sel === ".skill-row") {
        const names = [...this._html.matchAll(/data-skill-name="([^"]*)"/g)].map((x) => x[1]);
        // 同名条目复用同一个 stub 对象：bindSkillRows 绑定的监听器与用例里的 rows 必须是同一批。
        return names.map((name) => this._rows[name] || (this._rows[name] = {
          _name: name,
          focused: false,
          listeners: {},
          getAttribute(n) { return n === "data-skill-name" ? name : null; },
          addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
          click() { (this.listeners.click || []).forEach((fn) => fn({ currentTarget: this })); },
          focus() { this.focused = true; },
        }));
      }
      if (sel === "button[data-skill-tab]") {
        const keys = [...this._html.matchAll(/data-skill-tab="([^"]*)"/g)].map((x) => x[1]);
        return keys.map((key) => this._tabButtons[key] || (this._tabButtons[key] = makeTabButton(key)));
      }
      return [];
    },
  };
  return el;
}
// 页签按钮 stub：syncSkillTabBar 只用 classList/aria 属性，焦点用 focus() 记录。
function makeTabButton(key) {
  return {
    _key: key,
    _attrs: { "data-skill-tab": key },
    classList: makeClassList(),
    focused: false,
    getAttribute(n) { return this._attrs[n] === undefined ? null : this._attrs[n]; },
    setAttribute(n, v) { this._attrs[n] = String(v); },
    focus() { this.focused = true; },
    closest(sel) { return sel === "button[data-skill-tab]" ? this : null; },
  };
}
const documentListeners = {};
for (const id of ["tab-skills", "skills-list", "skills-count", "skills-refresh-btn", "skill-detail-overlay", "skill-detail-title", "skill-detail-meta", "skill-detail-tabs", "skill-detail-close", "skill-detail-body"]) {
  elements[id] = makeEl(id);
}
globalThis.document = {
  getElementById: (id) => elements[id] || (elements[id] = makeEl(id)),
  querySelector: (sel) => (elements["sel:" + sel] || (elements["sel:" + sel] = makeEl(sel))),
  querySelectorAll: () => [],
  createElement: (tag) => makeEl("created:" + tag),
  documentElement: makeEl("html"),
  body: { appendChild(el) { el.parentNode = this; }, classList: makeEl("body").classList },
  addEventListener(type, fn) { (documentListeners[type] = documentListeners[type] || []).push(fn); },
};

// ---- fetch stub ----
let callLog = [];
let listBody = {
  count: 2,
  skills: [
    { name: "imagegen", function_name: "skill__imagegen", kind: "skill", description: "Generate images", category: "media", version: "1.2.0" },
    { name: "summarize", function_name: "skill__summarize", kind: "skill", description: "Summarize text" },
  ],
};
let listStatus = 200;
let detailBody = {
  name: "imagegen",
  function_name: "skill__imagegen",
  kind: "skill",
  description: "Generate images",
  labels: ["media", "image"],
  triggers: [{ type: "keyword", values: ["画图"], weight: 0.8 }],
  dependencies: [{ name: "http_request", kind: "tool" }],
  source: { path: "/skills/imagegen/SKILL.md", layer: "project" },
};
let detailStatus = 200;
let deferDetail = false;
let pendingDetail = null;

function stubResponse(status, body) {
  return Promise.resolve({ status, json: () => Promise.resolve(body) });
}
globalThis.fetch = function (input) {
  const url = String(input);
  callLog.push(url);
  if (url.indexOf("/web/api/skills/") >= 0) {
    if (deferDetail) { return new Promise((resolve) => { pendingDetail = resolve; }); }
    if (detailStatus !== 200) {
      return stubResponse(detailStatus, { error: { code: "skill_not_found", message: "skill not found: x" } });
    }
    return stubResponse(detailStatus, detailBody);
  }
  if (listStatus !== 200) {
    return stubResponse(listStatus, { error: { code: "skills_unavailable", message: "chat session not ready" } });
  }
  return stubResponse(200, listBody);
};

const count = (part) => callLog.filter((u) => u.indexOf(part) >= 0).length;
const tick = () => new Promise((r) => setTimeout(r, 0));
const tabKeys = (tabsEl) => [...tabsEl.innerHTML.matchAll(/data-skill-tab="([^"]*)"/g)].map((m) => m[1]);

(async () => {
  const skills = await import(pathToFileURL(path.join(WEB_DIR, "skills.js")).href);
  const panel = elements["tab-skills"];
  const listEl = elements["skills-list"];
  const overlay = elements["skill-detail-overlay"];
  const bodyEl = elements["skill-detail-body"];
  const tabsEl = elements["skill-detail-tabs"];
  const metaEl = elements["skill-detail-meta"];

  // 0) initSkills：详情面板 reparent 到 body（fixed 层级不受页签影响）
  skills.initSkills();
  assert.strictEqual(overlay.parentNode, globalThis.document.body, "overlay 应 reparent 到 body");

  // 1) 首次进入页签：拉取列表并渲染条目
  panel.classList.add("active");
  skills.loadSkills();
  await tick(); await tick();
  assert.strictEqual(count("/web/api/skills") - count("/web/api/skills/"), 1, "首次加载应拉取一次列表");
  assert.ok(listEl.innerHTML.indexOf("imagegen") >= 0, "列表应渲染 skill 名, got: " + listEl.innerHTML);
  assert.ok(listEl.innerHTML.indexOf("skill__imagegen") >= 0, "列表应渲染可调用名");
  assert.ok(listEl.innerHTML.indexOf("summarize") >= 0, "列表应渲染第二个 skill");
  assert.strictEqual(elements["skills-count"].textContent, "共 2 个", "计数应为后端 count");

  // 2) 同会话重复切页签：不重复请求
  skills.loadSkills();
  await tick();
  assert.strictEqual(count("/web/api/skills") - count("/web/api/skills/"), 1, "同会话切页签不应重复拉取");

  // 3) 后台切换会话：不立即请求；再进页签强制刷新
  panel.classList.remove("active");
  skills.syncSkillsSession("sess-b");
  await tick();
  assert.strictEqual(count("/web/api/skills") - count("/web/api/skills/"), 1, "后台切换不应立即拉取");
  panel.classList.add("active");
  skills.loadSkills();
  await tick(); await tick();
  assert.strictEqual(count("/web/api/skills") - count("/web/api/skills/"), 2, "会话变化后进页签应强制刷新");

  // 4) 页签可见时切换会话：立即重拉
  skills.syncSkillsSession("sess-c");
  await tick(); await tick();
  assert.strictEqual(count("/web/api/skills") - count("/web/api/skills/"), 3, "可见时切换会话应立即重拉");

  // 5) 点击条目：现取详情，按非空分组生成页签，默认渲染第一个页签
  const rows = listEl.querySelectorAll(".skill-row");
  assert.strictEqual(rows.length, 2, "应绑定 2 个条目点击");
  rows[0].click();
  await tick(); await tick();
  assert.strictEqual(count("/web/api/skills/imagegen"), 1, "点击应拉取该 skill 详情");
  assert.ok(overlay.classList.contains("active"), "详情面板应打开");
  assert.strictEqual(elements["skill-detail-title"].textContent, "imagegen", "面板标题应为 skill 名");
  assert.deepStrictEqual(tabKeys(tabsEl), ["overview", "triggers", "dependencies", "source"], "只应生成非空分组页签（fixture 无 metadata）");
  assert.ok(/class="skill-tab active"[^>]*id="skill-tab-overview"/.test(tabsEl.innerHTML), "默认选中第一个页签, got: " + tabsEl.innerHTML);
  assert.strictEqual((tabsEl.innerHTML.match(/aria-selected="true"/g) || []).length, 1, "只应有一个选中页签");
  assert.ok(bodyEl.innerHTML.indexOf("skill__imagegen") >= 0, "概览页签应渲染可调用名");
  assert.ok(bodyEl.innerHTML.indexOf("Generate images") >= 0, "概览页签应渲染描述");
  assert.ok(bodyEl.innerHTML.indexOf("media") >= 0, "概览页签应将 labels 渲染为徽标");
  assert.ok(bodyEl.innerHTML.indexOf('id="skill-tabpanel-overview"') >= 0, "内容区应带 tabpanel 语义");
  assert.ok(bodyEl.innerHTML.indexOf("画图") < 0, "未选中的页签内容不应渲染");
  assert.ok(tabsEl.querySelector('button[data-skill-tab="overview"]').focused, "打开详情应把焦点放到选中页签上");
  // 详情响应缺 category/version 时不得补默认值（列表快照里有，也不能拿来用）。
  assert.ok(bodyEl.innerHTML.indexOf("版本") < 0, "后端未返回 version 时不得出现版本行");
  assert.ok(metaEl.innerHTML.indexOf("skill-badge-kind") >= 0, "头部摘要应渲染 kind 徽标");
  assert.ok(metaEl.innerHTML.indexOf("1.2.0") < 0, "头部摘要不得使用列表快照里的 version");

  // 6) 页签切换：点击 + 键盘导航（只渲染当前页签内容）
  tabsEl.dispatch("click", { target: tabsEl.querySelector('button[data-skill-tab="triggers"]') });
  const triggersBtn = tabsEl.querySelector('button[data-skill-tab="triggers"]');
  assert.ok(bodyEl.innerHTML.indexOf("画图") >= 0 && bodyEl.innerHTML.indexOf("keyword") >= 0, "触发页签应渲染事件卡片");
  assert.ok(bodyEl.innerHTML.indexOf("Generate images") < 0, "切换后不应残留上一个页签内容");
  assert.strictEqual(triggersBtn.getAttribute("aria-selected"), "true", "点击的页签应变为选中态");
  assert.strictEqual((tabsEl.innerHTML.match(/aria-selected="true"/g) || []).length, 1, "选中态仍应唯一（按钮不重建）");

  let prevented = 0;
  const key = (k) => ({ key: k, preventDefault() { prevented++; } });
  tabsEl.dispatch("keydown", key("ArrowRight"));
  assert.ok(bodyEl.innerHTML.indexOf("http_request") >= 0, "→ 应切到依赖页签");
  assert.ok(bodyEl.innerHTML.indexOf('<div class="skill-card-title">http_request</div>') >= 0, "对象数组应渲染为带后端字段标题的卡片");
  assert.ok(bodyEl.innerHTML.indexOf('<span class="skill-kv-label">name</span>') < 0, "标题已展示的字段不应在卡片内重复成一行");
  assert.ok(bodyEl.innerHTML.indexOf('class="skill-kv-value"><span class="skill-kv-value"') < 0, "键值不应出现嵌套的 value 容器");
  tabsEl.dispatch("keydown", key("ArrowRight"));
  assert.ok(bodyEl.innerHTML.indexOf("project") >= 0, "→ 应切到来源页签");
  tabsEl.dispatch("keydown", key("ArrowRight"));
  assert.ok(bodyEl.innerHTML.indexOf("Generate images") >= 0, "→ 从末页签应环绕回首页签");
  assert.strictEqual(triggersBtn.getAttribute("aria-selected"), "false", "环绕后原页签应取消选中");
  assert.strictEqual((tabsEl.innerHTML.match(/aria-selected="true"/g) || []).length, 1, "环绕后选中态仍应唯一");
  tabsEl.dispatch("keydown", key("Home"));
  assert.ok(bodyEl.innerHTML.indexOf("Generate images") >= 0, "Home 应回到首页签");
  tabsEl.dispatch("keydown", key("End"));
  assert.ok(bodyEl.innerHTML.indexOf("project") >= 0, "End 应跳到末页签");
  tabsEl.dispatch("keydown", key("ArrowLeft"));
  assert.ok(bodyEl.innerHTML.indexOf("http_request") >= 0, "← 应回到依赖页签");
  assert.strictEqual(prevented, 6, "键盘导航应拦截默认行为");
  tabsEl.dispatch("keydown", { key: "a", preventDefault() { prevented++; } });
  assert.strictEqual(prevented, 6, "非导航键不应被拦截");

  // 7) 过期详情响应丢弃：先挂起一次点击，再点另一个条目，旧响应后到不覆盖
  deferDetail = true;
  rows[0].click();
  await tick();
  const staleResolve = pendingDetail;
  deferDetail = false;
  detailBody = { name: "summarize", function_name: "skill__summarize", description: "Summarize text" };
  rows[1].click();
  await tick(); await tick();
  assert.ok(bodyEl.innerHTML.indexOf("Summarize text") >= 0, "新详情应已渲染");
  assert.deepStrictEqual(tabKeys(tabsEl), ["overview"], "页签应按新 skill 的分组重建");
  staleResolve({ status: 200, json: () => Promise.resolve({ name: "imagegen", description: "STALE-PAYLOAD" }) });
  await tick(); await tick();
  assert.ok(bodyEl.innerHTML.indexOf("STALE-PAYLOAD") < 0, "过期详情响应不得覆盖面板");
  assert.deepStrictEqual(tabKeys(tabsEl), ["overview"], "过期响应不得改回旧页签");

  // 8) 详情失败：如实显示错误码，页签栏清空，面板保持打开
  detailStatus = 404;
  rows[1].click();
  await tick(); await tick();
  assert.ok(bodyEl.innerHTML.indexOf("skill_not_found") >= 0, "详情失败应显示错误码, got: " + bodyEl.innerHTML);
  assert.strictEqual(tabsEl.innerHTML, "", "失败时应清空页签栏（不残留上个 skill 的页签）");
  assert.strictEqual(metaEl.innerHTML, "", "失败时应清空头部摘要");
  assert.ok(overlay.classList.contains("active"), "失败时面板不应静默关闭");

  // 9) Esc：捕获阶段关闭面板并阻断（不触发会话中断）
  let escPrevented = 0;
  let stopped = 0;
  const escEvent = { key: "Escape", preventDefault() { escPrevented++; }, stopPropagation() { stopped++; } };
  (documentListeners.keydown || []).forEach((fn) => fn(escEvent));
  assert.ok(!overlay.classList.contains("active"), "Esc 应关闭详情面板");
  assert.strictEqual(escPrevented, 1, "面板打开时 Esc 应 preventDefault");
  assert.strictEqual(stopped, 1, "面板打开时 Esc 应 stopPropagation");
  assert.ok(rows[1].focused, "关闭详情应把焦点还给触发它的列表条目");
  (documentListeners.keydown || []).forEach((fn) => fn(escEvent));
  assert.strictEqual(escPrevented, 1, "面板已关闭时 Esc 不应拦截（交回会话中断逻辑）");

  // 10) 关闭后迟到的详情响应不再写入，重新打开也不残留旧页签
  detailStatus = 200;
  detailBody = { name: "imagegen", function_name: "skill__imagegen", description: "Generate images", metadata: { owner: "core" } };
  deferDetail = true;
  rows[0].click();
  await tick();
  const lateResolve = pendingDetail;
  deferDetail = false;
  skills.closeSkillDetail();
  assert.strictEqual(tabsEl.innerHTML, "", "打开详情时应先清空上一次的页签");
  lateResolve({ status: 200, json: () => Promise.resolve({ name: "imagegen", description: "LATE-PAYLOAD" }) });
  await tick(); await tick();
  assert.ok(bodyEl.innerHTML.indexOf("LATE-PAYLOAD") < 0, "关闭后迟到响应不得写入面板");

  // 11) 列表失败：显示错误码，不残留旧条目
  listStatus = 503;
  skills.refreshSkills();
  await tick(); await tick();
  assert.ok(listEl.innerHTML.indexOf("skills_unavailable") >= 0, "列表失败应显示错误码, got: " + listEl.innerHTML);
  assert.ok(listEl.innerHTML.indexOf("data-skill-name") < 0, "失败时不得残留旧条目");
  assert.strictEqual(elements["skills-count"].textContent, "加载失败", "失败时计数区应显示加载失败");

  // 12) 页签接线（ui.js → skills.js）：点击「技能」按钮即激活面板并按会话上下文拉取列表
  globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
  globalThis.window = {
    matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
    addEventListener() {},
    removeEventListener() {},
    localStorage: globalThis.localStorage,
  };
  globalThis.EventSource = function () { return { addEventListener() {}, close() {} }; };
  const ui = await import(pathToFileURL(path.join(WEB_DIR, "ui.js")).href);
  ui.initTabs();
  listStatus = 200;
  panel.classList.remove("active");
  skills.syncSkillsSession("sess-ui"); // 后台会话变化：进页签时才刷新
  await tick();
  const beforeClick = count("/web/api/skills") - count("/web/api/skills/");
  elements["tab-skills-btn"].dispatch("click");
  await tick(); await tick();
  assert.ok(panel.classList.contains("active"), "点击技能按钮应激活技能面板");
  assert.ok(!elements["tab-main"].classList.contains("active"), "点击技能按钮应停用对话面板");
  assert.strictEqual(count("/web/api/skills") - count("/web/api/skills/"), beforeClick + 1, "点击技能按钮应触发列表拉取");

  console.log("ALL SKILLS TAB TESTS PASSED");
  process.exit(0);
})().catch((err) => { console.error("FAILED:", (err && err.stack) || err); process.exit(1); });
