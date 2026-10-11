// 对话区消息过滤面板：角色多选 + 正文搜索，吸附在消息区顶部居中。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。
//
// 过滤在服务端执行（/web/api/screen?roles=…&q=…）：前端只持有最新一页窗口
// （msg_limit），在客户端过滤会漏掉尚未加载的更早消息，也拿不到「匹配 N / 共 M 条」
// 的准确计数。服务端先按条件过滤，再在过滤后的序列上做 msg_limit/msg_before
// 分页 —— 语义等同于搜索结果分页（见 web_handlers.go 的 HandleChatWebAPIScreen）。
// 本模块只负责：面板 UI、过滤条件状态、把条件编码成查询串、展示服务端返回的
// 匹配计数，以及面板的拖动换位（最左 ⠿ 把手，见文件尾部拖动一节）；条件变化时通
// 过 onChange 回调通知 chat.js 整块重建对话区（索引语义随过滤改变，增量尾部替换会错位）。

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

// 拖动把手（面板最左 ⠿）与 composer 把手同一套参数与交互契约：
// 方向键 8px 微调 / Shift 1px 精调 / Home·双击复位；位置记忆落 localStorage。
var STORAGE_KEY = "aicli.web.msgfilter.v1";
var EDGE = 2;      // 拖动落点至少留在信息流内的边距（px）
var KEY_STEP = 8;  // 方向键微调步长（px）
var KEY_FINE = 1;  // Shift+方向键精调步长（px）

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
var gripEl = null;
var offset = { dx: 0, dy: 0 }; // 面板相对吸附点（sticky 顶部居中）的 translate 位移
var dragState = null;          // { startX, startY, baseDx, baseDy }；非拖动中为 null

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

// ---- 浮动工具栏拖动（把手 = 面板最左 ⠿，见 index.html #msg-filter-grip）----
// 与 composer 把手同一套交互契约：指针拖动（Pointer 优先、回退鼠标事件）、
// 方向键 8px 微调（Shift 1px 精调）、Home / 双击复位。位移是相对吸附点
// （sticky 顶部居中）的 translate：sticky 吸附语义不变——滚动时工具栏仍钉在
// 信息流可视区顶部，只是整体平移到用户拖到的位置。落点永远夹在 #conversation
// 内（EDGE 边距）：既不会拖丢，也不会因 transform 溢出产生横向滚动条。
// 位置落 localStorage（隐私模式等场景静默降级，本次会话内仍然生效）。

// 记忆读写：localStorage 异常（隐私模式等）静默忽略。
function storeOffset() {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ dx: offset.dx, dy: offset.dy }));
  } catch (e) { /* ignore */ }
}

function restoreOffset() {
  try {
    var raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) { return; }
    var v = JSON.parse(raw);
    if (v && typeof v === "object") {
      offset.dx = Number(v.dx) || 0;
      offset.dy = Number(v.dy) || 0;
    }
  } catch (e) { /* ignore */ }
}

function filterPanelEl() {
  return (rootEl && rootEl.querySelector) ? rootEl.querySelector(".msg-filter-panel") : null;
}

// clampOffset 把位移夹回 #conversation 可视范围：横向以面板布局位（flex 居中的
// 左缘）为基准换算可视边界；纵向 0 = 吸附顶边（不允许拖到消息区上方）。
function clampOffset(dx, dy) {
  var conv = document.getElementById("conversation");
  var panel = filterPanelEl();
  if (!conv || !panel) { return { dx: 0, dy: 0 }; }
  var cw = conv.clientWidth, ch = conv.clientHeight;
  var pw = panel.offsetWidth, ph = panel.offsetHeight;
  var layoutLeft = (cw - pw) / 2;
  var lo = EDGE - layoutLeft, hi = cw - EDGE - pw - layoutLeft;
  if (lo > hi) { dx = (lo + hi) / 2; } // 面板比容器还宽：横移无意义，居中兜底
  else { dx = Math.min(hi, Math.max(lo, dx)); }
  var dyHi = ch - EDGE - ph;
  dy = dyHi < 0 ? 0 : Math.min(dyHi, Math.max(0, dy));
  return { dx: Math.round(dx), dy: Math.round(dy) };
}

function applyOffset() {
  var panel = filterPanelEl();
  if (!panel) { return; }
  if (offset.dx || offset.dy) {
    panel.style.transform = "translate(" + offset.dx + "px, " + offset.dy + "px)";
  } else if (panel.style.transform) {
    panel.style.transform = ""; // 默认位置不留内联样式，DOM 保持干净
  }
}

function resetFilterPos() {
  offset = { dx: 0, dy: 0 };
  applyOffset();
  storeOffset();
}

// coord 取指针坐标：指针/鼠标事件直接用 clientX/Y，触摸事件回退 touches。
function coord(ev, axis) {
  if (!ev) { return 0; }
  var direct = axis === "x" ? ev.clientX : ev.clientY;
  if (typeof direct === "number") { return direct; }
  var list = ev.touches || ev.changedTouches;
  if (list && list.length) { return axis === "x" ? list[0].clientX : list[0].clientY; }
  return 0;
}

function onGripPointerDown(ev) {
  if (ev && typeof ev.button === "number" && ev.button !== 0) { return; } // 只响应主键
  if (ev && typeof ev.preventDefault === "function") { ev.preventDefault(); } // 拖动期间不要选中文字
  dragState = { startX: coord(ev, "x"), startY: coord(ev, "y"), baseDx: offset.dx, baseDy: offset.dy };
  if (document.body && document.body.classList) { document.body.classList.add("msgfilter-dragging"); }
  // 指针捕获：指针移出把手/窗口仍能继续拖动（失败也不影响基本拖动）。
  if (ev && typeof ev.pointerId === "number" && gripEl && typeof gripEl.setPointerCapture === "function") {
    try { gripEl.setPointerCapture(ev.pointerId); } catch (e) { /* ignore */ }
  }
}

function onDragPointerMove(ev) {
  if (!dragState) { return; }
  if (ev && typeof ev.preventDefault === "function") { ev.preventDefault(); }
  offset = clampOffset(
    dragState.baseDx + coord(ev, "x") - dragState.startX,
    dragState.baseDy + coord(ev, "y") - dragState.startY
  );
  applyOffset();
}

function onDragPointerUp() {
  if (!dragState) { return; }
  dragState = null;
  if (document.body && document.body.classList) { document.body.classList.remove("msgfilter-dragging"); }
  storeOffset(); // 只在落点确定时落盘，拖动过程中不反复写 localStorage
}

// 把手键盘微调：方向键 8px、Shift+方向键 1px、Home 复位（与 composer 把手一致）。
function onGripKey(ev) {
  var key = ev && ev.key;
  if (!key) { return; }
  if (key === "Home") { ev.preventDefault(); resetFilterPos(); return; }
  var step = ev.shiftKey ? KEY_FINE : KEY_STEP;
  var dx = 0, dy = 0;
  if (key === "ArrowLeft") { dx = -step; }
  else if (key === "ArrowRight") { dx = step; }
  else if (key === "ArrowUp") { dy = -step; }
  else if (key === "ArrowDown") { dy = step; }
  else { return; }
  ev.preventDefault();
  offset = clampOffset(offset.dx + dx, offset.dy + dy);
  applyOffset();
  storeOffset();
}

function bindFilterDrag() {
  if (!gripEl || typeof gripEl.addEventListener !== "function" || !document.addEventListener) { return; }
  var hasPointer = typeof window !== "undefined" && typeof window.PointerEvent === "function";
  if (hasPointer) {
    gripEl.addEventListener("pointerdown", onGripPointerDown);
    document.addEventListener("pointermove", onDragPointerMove);
    document.addEventListener("pointerup", onDragPointerUp);
    document.addEventListener("pointercancel", onDragPointerUp);
  } else {
    gripEl.addEventListener("mousedown", onGripPointerDown);
    document.addEventListener("mousemove", onDragPointerMove);
    document.addEventListener("mouseup", onDragPointerUp);
  }
  gripEl.addEventListener("dblclick", resetFilterPos);
  gripEl.addEventListener("keydown", onGripKey);
  // 窗口缩放后落点可能越界：重夹一次（clamp 对任意入参幂等，无位移时是空操作）。
  if (typeof window.addEventListener === "function") {
    window.addEventListener("resize", function () {
      offset = clampOffset(offset.dx, offset.dy);
      applyOffset();
    });
  }
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

  // 拖动把手：绑定事件 + 恢复上次位置（夹回当前容器尺寸内再应用）。
  gripEl = document.getElementById("msg-filter-grip");
  bindFilterDrag();
  restoreOffset();
  offset = clampOffset(offset.dx, offset.dy);
  applyOffset();
}
