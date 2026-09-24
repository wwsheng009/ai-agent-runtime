// 对话区消息过滤面板：角色多选 + 正文搜索，吸附在消息区顶部居中。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。
//
// 过滤在服务端执行（/web/api/screen?roles=…&q=…）：前端只持有最新一页窗口
// （msg_limit），在客户端过滤会漏掉尚未加载的更早消息，也拿不到「匹配 N / 共 M 条」
// 的准确计数。服务端先按条件过滤，再在过滤后的序列上做 msg_limit/msg_before
// 分页 —— 语义等同于搜索结果分页（见 web_handlers.go 的 HandleChatWebAPIScreen）。
// 本模块只负责：面板 UI、过滤条件状态、把条件编码成查询串、展示服务端返回的
// 匹配计数；条件变化时通过 onChange 回调通知 chat.js 整块重建对话区（索引语义
// 随过滤改变，增量尾部替换会错位）。

// 角色词表：与 chat.js 的 MSG_LABELS、后端 chatWebRoleForCellKind 保持同一集合。
// 后端只认这些值（未知角色按「忽略」处理，不会把结果集变成空），因此多选按钮
// 与之逐项对应；新增角色时三处需同步。
var FILTER_ROLES = [
  { role: "user", label: "用户" },
  { role: "assistant", label: "AI 助手" },
  { role: "reasoning", label: "推理" },
  { role: "tool", label: "工具" },
  { role: "system", label: "系统" },
  { role: "command", label: "命令" },
  { role: "diagnostic", label: "诊断" },
  { role: "runtime", label: "事件" }
];

var SEARCH_DEBOUNCE_MS = 300; // 搜索输入去抖：连续打字只在停顿后发一次请求

var selected = {};            // 角色选中表（role -> true）；空 = 不按角色过滤
var query = "";               // 搜索词（已 trim、保持原大小写；服务端按小写子串匹配）
var searchOpen = false;       // 搜索输入框是否展开
var onChange = null;          // 条件变化回调（chat.js 注册：整块重建对话区）
var searchTimer = null;       // 搜索去抖定时器句柄
var countPending = false;     // 已发起请求、等待服务端计数返回
var lastMatched = -1;         // 服务端最近一次返回的匹配条数（-1 = 尚未取到）
var lastUnfiltered = -1;      // 过滤前的总条数（服务端 unfiltered_total）

var rootEl = null;
var rolesEl = null;
var searchBtn = null;
var searchEl = null;
var countEl = null;
var clearEl = null;

// 已选角色列表：按 FILTER_ROLES 顺序输出（查询串稳定，便于日志/缓存比对）。
function selectedRoleList() {
  return FILTER_ROLES.filter(function (item) { return selected[item.role] === true; })
    .map(function (item) { return item.role; });
}

// 过滤是否生效：选中了任意角色，或搜索词非空。
export function isFilterActive() {
  return selectedRoleList().length > 0 || query !== "";
}

// 该角色是否应显示：过滤未生效 / 只搜索正文（未选角色）时全部可见。
// 供 chat.js 判断本地乐观回显（pending 用户气泡）与流式气泡是否展示。
export function filterAllowsRole(role) {
  if (!isFilterActive()) { return true; }
  var list = selectedRoleList();
  if (!list.length) { return true; }
  return list.indexOf(String(role)) >= 0;
}

// 过滤条件 → 查询串（以 & 开头，可直接拼在既有 URL 之后；未过滤时为空串）。
// 语义与后端 chatWebMessageFilterParam 对齐：roles 逗号分隔，q 为正文子串。
export function filterQueryString() {
  if (!isFilterActive()) { return ""; }
  var parts = [];
  var roles = selectedRoleList();
  if (roles.length) { parts.push("roles=" + encodeURIComponent(roles.join(","))); }
  if (query) { parts.push("q=" + encodeURIComponent(query)); }
  return parts.length ? "&" + parts.join("&") : "";
}

// 过滤条件快照（跨模块只读访问 / 测试断言用）。
export function getFilterState() {
  return { roles: selectedRoleList(), query: query, active: isFilterActive(), searchOpen: searchOpen };
}

// chat.js 注册条件变化回调（本模块不反向依赖 chat.js，避免循环 import）。
export function setFilterChangeHandler(fn) {
  onChange = (typeof fn === "function") ? fn : null;
}

// 展示服务端返回的匹配计数：过滤生效时「匹配 N / 共 M 条」，未过滤时「共 M 条」。
// matched / unfiltered 省略或为负时沿用上一次的值（增量刷新只带 message_window）。
export function updateFilterMatchInfo(matched, unfiltered) {
  if (typeof matched === "number" && matched >= 0) { lastMatched = matched; }
  if (typeof unfiltered === "number" && unfiltered >= 0) { lastUnfiltered = unfiltered; }
  countPending = false;
  renderCount();
}

function renderCount() {
  if (!countEl) { return; }
  if (countPending) { countEl.textContent = "查询中…"; return; }
  if (lastMatched < 0) { countEl.textContent = ""; return; }
  if (!isFilterActive()) { countEl.textContent = "共 " + lastMatched + " 条"; return; }
  countEl.textContent = (lastUnfiltered > lastMatched)
    ? "匹配 " + lastMatched + " / 共 " + lastUnfiltered + " 条"
    : "匹配 " + lastMatched + " 条";
}

function renderChips() {
  if (!rolesEl) { return; }
  var html = "";
  FILTER_ROLES.forEach(function (item) {
    html += '<button class="msg-filter-chip" type="button" data-filter-role="' + item.role + '"' +
      ' aria-pressed="' + (selected[item.role] === true ? "true" : "false") + '"' +
      ' title="只看/隐藏「' + item.label + '」消息（可多选）">' + item.label + "</button>";
  });
  rolesEl.innerHTML = html;
}

// 选中态回写：只改 aria-pressed（CSS 按属性着色），不重建节点（避免丢焦点）。
function syncChips() {
  if (!rolesEl || !rolesEl.querySelectorAll) { return; }
  rolesEl.querySelectorAll(".msg-filter-chip").forEach(function (btn) {
    btn.setAttribute("aria-pressed", selected[btn.getAttribute("data-filter-role")] === true ? "true" : "false");
  });
}

function syncClearBtn() {
  if (!clearEl) { return; }
  clearEl.disabled = !isFilterActive();
}

// 条件变化统一出口：回写面板外观 → 标记「查询中」→ 通知 chat.js 重建对话区。
// 计数在响应回来前先显示查询中，避免异步加载期间展示上一次的旧数字。
function apply() {
  syncChips();
  syncClearBtn();
  countPending = true;
  renderCount();
  if (onChange) { onChange(); }
}

function toggleRole(role) {
  if (selected[role] === true) { delete selected[role]; } else { selected[role] = true; }
  apply();
}

// 展开/收起搜索输入框：收起时清空搜索词（不留看不见的过滤条件）。
function setSearchOpen(open) {
  searchOpen = !!open;
  if (searchEl) { searchEl.style.display = searchOpen ? "inline-block" : "none"; }
  if (searchBtn) {
    searchBtn.setAttribute("aria-expanded", searchOpen ? "true" : "false");
    if (searchBtn.classList) { searchBtn.classList.toggle("active", searchOpen); }
  }
  if (searchOpen) {
    if (searchEl && searchEl.focus) { searchEl.focus(); }
    return;
  }
  if (searchEl) { searchEl.value = ""; }
  if (query) { query = ""; apply(); }
}

// 提交搜索词（去抖定时器到期 / 回车立即触发）。
function commitQuery(text) {
  var next = String(text == null ? "" : text).trim();
  if (next === query) { return; }
  query = next;
  apply();
}

// 清除全部过滤条件（角色 + 搜索词），并把搜索框收回。
export function clearFilter() {
  if (searchTimer) { clearTimeout(searchTimer); searchTimer = null; }
  selected = {};
  query = "";
  if (searchEl) { searchEl.value = ""; }
  // 先清条件再收起搜索框：setSearchOpen(false) 内「有词才重新应用」的分支
  // 因此不会触发第二次刷新（下面统一 apply 一次）。
  setSearchOpen(false);
  apply();
}

export function initMsgFilter() {
  rootEl = document.getElementById("msg-filter");
  rolesEl = document.getElementById("msg-filter-roles");
  searchBtn = document.getElementById("msg-filter-search-btn");
  searchEl = document.getElementById("msg-filter-search");
  countEl = document.getElementById("msg-filter-count");
  clearEl = document.getElementById("msg-filter-clear");
  if (!rolesEl || !searchBtn || !searchEl) { return; } // 页面结构缺失（旧版页面）：静默跳过

  renderChips();
  setSearchOpen(false);
  syncClearBtn();
  renderCount();

  // 角色多选：事件委托（按钮由 renderChips 生成）
  if (rolesEl.addEventListener) {
    rolesEl.addEventListener("click", function (e) {
      var btn = (e.target && e.target.closest) ? e.target.closest(".msg-filter-chip") : null;
      if (!btn) { return; }
      toggleRole(btn.getAttribute("data-filter-role"));
    });
  }

  // 搜索图标：点击展开/收起输入框
  searchBtn.addEventListener("click", function () { setSearchOpen(!searchOpen); });

  // 搜索输入：去抖提交；回车立即提交；Esc 先清词、再收起
  searchEl.addEventListener("input", function () {
    if (searchTimer) { clearTimeout(searchTimer); }
    searchTimer = setTimeout(function () {
      searchTimer = null;
      commitQuery(searchEl.value);
    }, SEARCH_DEBOUNCE_MS);
  });
  searchEl.addEventListener("keydown", function (e) {
    if (e.key === "Enter") {
      e.preventDefault();
      if (searchTimer) { clearTimeout(searchTimer); searchTimer = null; }
      commitQuery(searchEl.value);
      return;
    }
    if (e.key === "Escape") {
      e.preventDefault();
      if (searchEl.value) {
        if (searchTimer) { clearTimeout(searchTimer); searchTimer = null; }
        searchEl.value = "";
        commitQuery("");
        return;
      }
      setSearchOpen(false);
    }
  });

  if (clearEl) { clearEl.addEventListener("click", function () { clearFilter(); }); }
  // 面板内的点击不冒泡到对话区（否则会命中消息行的复制/折叠委托）。
  if (rootEl && rootEl.addEventListener) {
    rootEl.addEventListener("click", function (e) { if (e.stopPropagation) { e.stopPropagation(); } });
  }
}
