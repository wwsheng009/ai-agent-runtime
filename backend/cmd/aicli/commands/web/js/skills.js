// 技能页签：当前会话的 skill 目录 + 点击条目打开的详情面板。
// 数据源 /web/api/skills（与 TUI /skills 同源：session.FunctionCatalog 的
// skill 描述符）；详情每次点击现取 /web/api/skills/{name}，不用列表快照替代，
// 拉取失败如实显示错误码/消息。
// aicli micro web client 前端模块(无构建步骤,由 app.js 入口聚合)。

import { esc } from "./util.js";

// 列表/详情请求序号：丢弃会话切换、连续点击或手动刷新竞态下迟到的过期响应。
var skillsSeq = 0;
var detailSeq = 0;
var skillsLoaded = false;
// 会话感知（与 cache.js 同约定）：目录来自「当前会话」的 Function Catalog，
// 切换会话后旧目录不再代表当前会话，需按会话 id 判定过期。
var currentSessionID = "";
var skillsLoadedSessionID = "";

function skillsEl(id) { return document.getElementById(id); }

function skillsAPI(path) {
  return fetch(path, { cache: "no-store" }).then(function (res) {
    return res.json().then(function (body) {
      return { status: res.status, body: body };
    }, function () {
      return { status: res.status, body: null };
    });
  });
}

// ---- 列表 ----

export function loadSkills() {
  // 首次进入页签，或已渲染目录属于其它会话（切换会话发生在页签后台时由
  // syncSkillsSession 记录）才拉取；同一会话内重复切页签不重复发请求。
  if (skillsLoaded && skillsLoadedSessionID === currentSessionID) { return; }
  skillsLoaded = true;
  refreshSkills();
}

export function refreshSkills() {
  var listEl = skillsEl("skills-list");
  if (!listEl) { return; }
  var seq = ++skillsSeq;
  skillsLoadedSessionID = currentSessionID;
  listEl.innerHTML = '<div class="skills-empty">加载中…</div>';
  skillsAPI("/web/api/skills").then(function (result) {
    if (seq !== skillsSeq) { return; }
    if (result.status !== 200) {
      renderSkillsError(result.body && result.body.error);
      return;
    }
    renderSkillsList(result.body);
  }).catch(function () {
    if (seq !== skillsSeq) { return; }
    renderSkillsError(null);
  });
}

// 仅当技能页签当前可见时重拉（后台页签不浪费请求，由下次进入时按会话不一致刷新）。
export function refreshSkillsIfActive() {
  var panel = skillsEl("tab-skills");
  if (!panel || !panel.classList.contains("active")) { return; }
  refreshSkills();
}

// 会话同步（sessions.js 拿到 /web/api/sessions 的 current_session_id 时调用）。
export function syncSkillsSession(sessionID) {
  var next = sessionID || "";
  if (next === currentSessionID) { return; }
  currentSessionID = next;
  if (!skillsLoaded) { return; }                  // 尚未加载过：进入页签时自然拉取
  if (skillsLoadedSessionID === next) { return; } // 目录已属该会话
  refreshSkillsIfActive();
}

function renderSkillsError(err) {
  var code = err && err.code ? err.code : "network_error";
  var msg = err && err.message ? err.message : "";
  var countEl = skillsEl("skills-count");
  var listEl = skillsEl("skills-list");
  if (countEl) { countEl.textContent = "加载失败"; }
  if (listEl) {
    listEl.innerHTML = '<div class="skills-empty skills-error">加载失败: ' + esc(code)
      + (msg ? " — " + esc(msg) : "") + "</div>";
  }
}

function renderSkillsList(body) {
  var listEl = skillsEl("skills-list");
  var countEl = skillsEl("skills-count");
  var skills = (body && body.skills) || [];
  var count = body && typeof body.count === "number" ? body.count : skills.length;
  if (countEl) { countEl.textContent = "共 " + count + " 个"; }
  if (!listEl) { return; }
  if (!skills.length) {
    listEl.innerHTML = '<div class="skills-empty">当前会话没有可用的 skill</div>';
    return;
  }
  var rows = [];
  for (var i = 0; i < skills.length; i++) {
    rows.push(renderSkillRow(skills[i]));
  }
  listEl.innerHTML = rows.join("");
  bindSkillRows(listEl);
}

// renderSkillRow 只渲染后端实际返回的字段：没有 category/version/description
// 就不出现对应节点，不补默认值。
function renderSkillRow(item) {
  var name = item && item.name ? String(item.name) : "";
  var badges = [];
  if (item && item.category) { badges.push('<span class="skill-badge">' + esc(item.category) + "</span>"); }
  if (item && item.version) { badges.push('<span class="skill-badge">v' + esc(item.version) + "</span>"); }
  if (item && item.kind) { badges.push('<span class="skill-badge skill-badge-kind">' + esc(item.kind) + "</span>"); }
  var sub = [];
  if (item && item.function_name && item.function_name !== name) {
    sub.push('<span class="skill-fn">' + esc(item.function_name) + "</span>");
  }
  if (item && item.description) { sub.push('<span class="skill-desc">' + esc(item.description) + "</span>"); }
  return '<button class="skill-row" type="button" data-skill-name="' + esc(name) + '">'
    + '<span class="skill-row-main"><span class="skill-name">' + esc(name) + "</span>" + badges.join("") + "</span>"
    + (sub.length ? '<span class="skill-row-sub">' + sub.join("") + "</span>" : "")
    + "</button>";
}

function bindSkillRows(listEl) {
  var rows = listEl.querySelectorAll(".skill-row");
  for (var i = 0; i < rows.length; i++) {
    rows[i].addEventListener("click", function (event) {
      skillDetailTrigger = event.currentTarget;
      openSkillDetail(event.currentTarget.getAttribute("data-skill-name"));
    });
  }
}

// ---- 详情面板 ----
//
// 面板内按「页签」分组展示后端字段：每个页签对应一组字段，只有该组字段在响应里
// 确有值时才生成页签（空组不出现，也不补默认值）。页签按钮渲染到 #skill-detail-tabs，
// 内容区只渲染当前页签的内容（切换即重渲染 #skill-detail-body）。

// SKILL_DETAIL_GROUPS 定义页签顺序与字段归属；fields.length === 1 的分组不在内容区
// 重复标题（页签名已表达该语义），多字段分组里的标量字段仍带字段标签。
var SKILL_DETAIL_GROUPS = [
  { key: "overview", label: "概览", fields: ["name", "function_name", "kind", "category", "version", "description", "labels", "capabilities"] },
  { key: "triggers", label: "触发", fields: ["triggers"] },
  { key: "dependencies", label: "依赖", fields: ["dependencies"] },
  { key: "source", label: "来源", fields: ["source"] },
  { key: "metadata", label: "元数据", fields: ["metadata"] },
];
var SKILL_FIELD_LABELS = {
  name: "目录名",
  function_name: "可调用名",
  kind: "类型",
  category: "分类",
  version: "版本",
  labels: "标签",
  capabilities: "能力",
};
var skillDetailGroups = []; // 当前 skill 的非空分组 [{key,label,html}]
var activeSkillTab = "";
var skillDetailTrigger = null; // 打开详情的那一行，关闭时把焦点还回去

export function openSkillDetail(name) {
  var overlay = skillsEl("skill-detail-overlay");
  var titleEl = skillsEl("skill-detail-title");
  var bodyEl = skillsEl("skill-detail-body");
  if (!overlay || !bodyEl || !name) { return; }
  var seq = ++detailSeq;
  if (titleEl) { titleEl.textContent = name; }
  resetSkillDetailChrome();
  bodyEl.innerHTML = '<div class="skills-empty">加载中…</div>';
  overlay.classList.add("active");
  skillsAPI("/web/api/skills/" + encodeURIComponent(name)).then(function (result) {
    if (seq !== detailSeq) { return; }
    if (result.status !== 200) {
      renderSkillDetailError(result.body && result.body.error);
      return;
    }
    renderSkillDetail(result.body);
  }).catch(function () {
    if (seq !== detailSeq) { return; }
    renderSkillDetailError(null);
  });
}

function renderSkillDetailError(err) {
  var code = err && err.code ? err.code : "network_error";
  var msg = err && err.message ? err.message : "";
  resetSkillDetailChrome();
  var bodyEl = skillsEl("skill-detail-body");
  if (bodyEl) {
    bodyEl.innerHTML = '<div class="skills-empty skills-error">加载失败: ' + esc(code)
      + (msg ? " — " + esc(msg) : "") + "</div>";
  }
}

// resetSkillDetailChrome 清空上一个 skill 的页签/头部摘要与分组状态（加载中、失败、
// 关闭后重新打开都不残留旧页签）。
function resetSkillDetailChrome() {
  skillDetailGroups = [];
  activeSkillTab = "";
  var tabsEl = skillsEl("skill-detail-tabs");
  if (tabsEl) { tabsEl.innerHTML = ""; }
  var metaEl = skillsEl("skill-detail-meta");
  if (metaEl) { metaEl.innerHTML = ""; }
}

// renderSkillDetail 按「后端有值才显示」渲染：先按分组收集内容，只有非空分组才生成
// 页签；默认选中第一个非空页签。所有内容都来自 /web/api/skills/{name} 的响应字段。
function renderSkillDetail(body) {
  var data = body || {};
  skillDetailGroups = buildSkillGroups(data);
  renderSkillDetailMeta(data);
  var tabsEl = skillsEl("skill-detail-tabs");
  var bodyEl = skillsEl("skill-detail-body");
  if (!skillDetailGroups.length) {
    activeSkillTab = "";
    if (bodyEl) { bodyEl.innerHTML = '<div class="skills-empty">后端未返回该 skill 的可展示字段</div>'; }
    return;
  }
  activeSkillTab = skillDetailGroups[0].key;
  if (tabsEl) { tabsEl.innerHTML = renderSkillTabs(skillDetailGroups, activeSkillTab); }
  renderSkillTabBody();
  focusSkillTab(activeSkillTab); // 焦点落在选中的页签上，←/→ 立即可用
}

// 头部摘要：只用后端给出的 kind/category/version 生成徽标，无值则不出现。
function renderSkillDetailMeta(body) {
  var metaEl = skillsEl("skill-detail-meta");
  if (!metaEl) { return; }
  var badges = [];
  if (body && body.kind) { badges.push('<span class="skill-badge skill-badge-kind">' + esc(body.kind) + "</span>"); }
  if (body && body.category) { badges.push('<span class="skill-badge">' + esc(body.category) + "</span>"); }
  if (body && body.version) { badges.push('<span class="skill-badge">v' + esc(body.version) + "</span>"); }
  metaEl.innerHTML = badges.join("");
}

function renderSkillTabs(groups, activeKey) {
  var out = [];
  for (var i = 0; i < groups.length; i++) {
    var g = groups[i];
    var on = g.key === activeKey;
    out.push('<button class="skill-tab' + (on ? " active" : "") + '" type="button" role="tab"'
      + ' id="skill-tab-' + esc(g.key) + '" data-skill-tab="' + esc(g.key) + '"'
      + ' aria-controls="skill-tabpanel-' + esc(g.key) + '"'
      + ' aria-selected="' + (on ? "true" : "false") + '"'
      + ' tabindex="' + (on ? "0" : "-1") + '">' + esc(g.label) + "</button>");
  }
  return out.join("");
}

function buildSkillGroups(data) {
  var groups = [];
  for (var i = 0; i < SKILL_DETAIL_GROUPS.length; i++) {
    var def = SKILL_DETAIL_GROUPS[i];
    var single = def.fields.length === 1; // 单字段分组：页签名已表达语义，不再重复字段标签
    var rows = []; // 同组的标量字段共用一个网格，标签列才能对齐
    var blocks = []; // 段落 / 徽标 / 卡片 / JSON 等块级内容
    for (var j = 0; j < def.fields.length; j++) {
      var key = def.fields[j];
      var value = data[key];
      if (isEmptySkillValue(value)) { continue; }
      if (key === "description") { blocks.push('<p class="skill-text">' + esc(String(value)) + "</p>"); continue; }
      if (key === "labels" || key === "capabilities") {
        var chips = renderSkillChips(value);
        if (!chips) { continue; }
        if (!single) { blocks.push('<div class="skill-section-title">' + esc(SKILL_FIELD_LABELS[key] || key) + "</div>"); }
        blocks.push(chips);
        continue;
      }
      if (isSkillScalar(value)) {
        rows.push(renderSkillKVRow(single ? "" : SKILL_FIELD_LABELS[key] || key, esc(String(value))));
        continue;
      }
      blocks.push(renderSkillEntries(value));
    }
    var html = "";
    if (rows.length) { html += '<div class="skill-kv-grid">' + rows.join("") + "</div>"; }
    html += blocks.join("");
    if (html) { groups.push({ key: def.key, label: def.label, html: html }); }
  }
  return groups;
}

function isEmptySkillValue(value) {
  if (value === undefined || value === null || value === "") { return true; }
  if (Array.isArray(value)) { return value.length === 0; }
  if (typeof value === "object") { return Object.keys(value).length === 0; }
  return false;
}

function isSkillObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isSkillScalar(value) {
  return !Array.isArray(value) && !isSkillObject(value);
}

// renderSkillKVRow 生成一行键值；label 为空即单字段分组（页签名已说明字段语义，不重复字段标签）。
function renderSkillKVRow(label, valueHtml) {
  var head = label ? '<span class="skill-kv-label">' + esc(label) + "</span>" : "";
  // 值用 div（而非 span）：徽标组 / JSON 块是块级内容，span 里嵌 div 属非法嵌套。
  return '<div class="skill-kv">' + head + '<div class="skill-kv-value">' + valueHtml + "</div></div>";
}

function renderSkillKVs(entries) {
  var rows = [];
  for (var i = 0; i < entries.length; i++) {
    rows.push(renderSkillKVRow(entries[i][0], entries[i][1]));
  }
  return rows.length ? '<div class="skill-kv-grid">' + rows.join("") + "</div>" : "";
}

function renderSkillChips(value) {
  var items = Array.isArray(value) ? value : [value];
  var out = [];
  for (var i = 0; i < items.length; i++) {
    if (isEmptySkillValue(items[i]) || !isSkillScalar(items[i])) { continue; }
    out.push('<span class="skill-chip">' + esc(String(items[i])) + "</span>");
  }
  return out.length ? '<div class="skill-chips">' + out.join("") + "</div>" : "";
}

// renderSkillJSON 只对无法逐字段落地的嵌套结构兜底：原样展示后端 JSON，不做语义解释。
function renderSkillJSON(value) {
  var text = "";
  try {
    text = JSON.stringify(value, null, 2);
  } catch (e) {
    text = "";
  }
  if (text === "" || text === "null" || text === "[]" || text === "{}") { return ""; }
  return '<pre class="skill-json">' + esc(text) + "</pre>";
}

// renderSkillObject 把对象的顶层字段落成 KV 行；嵌套值退化为 JSON 块。
// skipKey 用于卡片：标题已展示的字段不再重复成一行。
function renderSkillObject(obj, skipKey) {
  var keys = Object.keys(obj);
  var entries = [];
  for (var i = 0; i < keys.length; i++) {
    if (keys[i] === skipKey) { continue; }
    var value = obj[keys[i]];
    if (isEmptySkillValue(value)) { continue; }
    var html = "";
    if (isSkillScalar(value)) { html = esc(String(value)); }
    else if (Array.isArray(value) && value.every(isSkillScalar)) { html = renderSkillChips(value); }
    else { html = renderSkillJSON(value); }
    if (html) { entries.push([keys[i], html]); }
  }
  return renderSkillKVs(entries);
}

function renderSkillEntry(item) {
  if (!isSkillObject(item)) {
    return '<div class="skill-card">' + esc(String(item)) + "</div>";
  }
  // 卡片标题只用后端字段（name/type/kind/id 中第一个有值的标量），不造序号或占位标题；
  // 该字段已作为标题出现，不再在卡片内重复成一行。
  var titleKeys = ["name", "type", "kind", "id"];
  var titleKey = "";
  for (var i = 0; i < titleKeys.length; i++) {
    if (!isEmptySkillValue(item[titleKeys[i]]) && isSkillScalar(item[titleKeys[i]])) { titleKey = titleKeys[i]; break; }
  }
  var head = titleKey ? '<div class="skill-card-title">' + esc(String(item[titleKey])) + "</div>" : "";
  var body = renderSkillObject(item, titleKey);
  return '<div class="skill-card">' + head + (body || renderSkillJSON(item)) + "</div>";
}

// renderSkillEntries 渲染结构化字段：标量数组 → 徽标；对象数组 → 卡片；对象 → KV 行。
function renderSkillEntries(value) {
  if (isEmptySkillValue(value)) { return ""; }
  if (isSkillObject(value)) { return renderSkillObject(value) || renderSkillJSON(value); }
  if (!Array.isArray(value)) { return renderSkillChips(value); }
  var scalarOnly = true;
  for (var i = 0; i < value.length; i++) {
    if (!isSkillScalar(value[i])) { scalarOnly = false; break; }
  }
  if (scalarOnly) { return renderSkillChips(value); }
  var cards = [];
  for (var j = 0; j < value.length; j++) { cards.push(renderSkillEntry(value[j])); }
  return '<div class="skill-cards">' + cards.join("") + "</div>";
}

function renderSkillTabBody() {
  var bodyEl = skillsEl("skill-detail-body");
  if (!bodyEl) { return; }
  var html = "";
  for (var i = 0; i < skillDetailGroups.length; i++) {
    if (skillDetailGroups[i].key === activeSkillTab) { html = skillDetailGroups[i].html; break; }
  }
  bodyEl.innerHTML = '<div class="skill-tabpanel" id="skill-tabpanel-' + esc(activeSkillTab) + '"'
    + ' role="tabpanel" aria-labelledby="skill-tab-' + esc(activeSkillTab) + '" tabindex="0">'
    + html + "</div>";
}

// selectSkillTab 切换页签：只有已渲染的分组可切换，返回是否发生了切换。
export function selectSkillTab(key) {
  var found = false;
  for (var i = 0; i < skillDetailGroups.length; i++) {
    if (skillDetailGroups[i].key === key) { found = true; break; }
  }
  if (!found) { return false; }
  activeSkillTab = key;
  syncSkillTabBar();
  renderSkillTabBody();
  return true;
}

// syncSkillTabBar 就地更新选中态（不重建按钮，避免键盘/点击后焦点丢失）。
function syncSkillTabBar() {
  var tabsEl = skillsEl("skill-detail-tabs");
  if (!tabsEl || !tabsEl.querySelectorAll) { return; }
  var buttons = tabsEl.querySelectorAll("button[data-skill-tab]");
  for (var i = 0; i < buttons.length; i++) {
    var on = buttons[i].getAttribute("data-skill-tab") === activeSkillTab;
    if (buttons[i].classList) { buttons[i].classList.toggle("active", on); }
    buttons[i].setAttribute("aria-selected", on ? "true" : "false");
    buttons[i].setAttribute("tabindex", on ? "0" : "-1");
  }
}

function focusSkillTab(key) {
  var tabsEl = skillsEl("skill-detail-tabs");
  if (!tabsEl || !tabsEl.querySelector) { return; }
  var button = tabsEl.querySelector('button[data-skill-tab="' + key + '"]');
  if (button && button.focus) { button.focus(); }
}

// moveSkillTab 处理页签键盘导航（←/→ 环绕，Home/End 首尾）。
function moveSkillTab(mode) {
  if (!skillDetailGroups.length) { return; }
  var index = 0;
  for (var i = 0; i < skillDetailGroups.length; i++) {
    if (skillDetailGroups[i].key === activeSkillTab) { index = i; break; }
  }
  var last = skillDetailGroups.length - 1;
  if (mode === "ArrowRight") { index = index === last ? 0 : index + 1; }
  else if (mode === "ArrowLeft") { index = index === 0 ? last : index - 1; }
  else if (mode === "Home") { index = 0; }
  else if (mode === "End") { index = last; }
  else { return; }
  selectSkillTab(skillDetailGroups[index].key);
  focusSkillTab(skillDetailGroups[index].key);
}

export function closeSkillDetail() {
  var overlay = skillsEl("skill-detail-overlay");
  if (!overlay || !overlay.classList.contains("active")) { return false; }
  overlay.classList.remove("active");
  detailSeq++; // 关闭后迟到的详情响应不再写入面板
  if (skillDetailTrigger && skillDetailTrigger.focus) { skillDetailTrigger.focus(); }
  skillDetailTrigger = null;
  return true;
}

// ---- 初始化 ----

export function initSkills() {
  var overlay = skillsEl("skill-detail-overlay");
  if (overlay) {
    // 与 provider 编辑弹窗同样的 reparent：fixed 定位不受页签层叠上下文影响。
    document.body.appendChild(overlay);
    overlay.addEventListener("click", function (event) {
      if (event.target === overlay) { closeSkillDetail(); }
    });
  }
  var closeBtn = skillsEl("skill-detail-close");
  if (closeBtn) { closeBtn.addEventListener("click", function () { closeSkillDetail(); }); }
  var refreshBtn = skillsEl("skills-refresh-btn");
  if (refreshBtn) { refreshBtn.addEventListener("click", function () { refreshSkills(); }); }
  // 页签在每次打开详情时重建，因此用容器级委托（含键盘导航），不在按钮上绑监听器。
  var tabsEl = skillsEl("skill-detail-tabs");
  if (tabsEl) {
    tabsEl.addEventListener("click", function (event) {
      var button = skillTabButtonFrom(event.target);
      if (button) { selectSkillTab(button.getAttribute("data-skill-tab")); }
    });
    tabsEl.addEventListener("keydown", function (event) {
      if (event.key !== "ArrowRight" && event.key !== "ArrowLeft" && event.key !== "Home" && event.key !== "End") { return; }
      if (!skillDetailGroups.length) { return; }
      event.preventDefault();
      moveSkillTab(event.key);
    });
  }
  document.addEventListener("keydown", function (event) {
    if (event.key !== "Escape") { return; }
    // 详情面板打开时 Esc 只关闭面板：在捕获阶段短路，避免同时触发会话中断。
    if (closeSkillDetail()) {
      event.preventDefault();
      event.stopPropagation();
    }
  }, true);
}

// skillTabButtonFrom 从事件目标回溯到页签按钮（点击按钮内部的文本节点也能命中）。
function skillTabButtonFrom(target) {
  if (!target) { return null; }
  if (typeof target.closest === "function") { return target.closest("button[data-skill-tab]"); }
  return null;
}
