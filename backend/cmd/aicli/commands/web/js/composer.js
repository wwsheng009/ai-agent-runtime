// 浮动 composer 面板：用户输入 + provider / model / reasoning_effort 选择器 + 动态状态条。
// 结构见 index.html #composer-panel（挂在 .layout 之外的 body 下，position:fixed）。
//
// 为什么是浮动面板：旧结构把 #input-row / #cfg-bar / #dynamic-status 放在 #tab-main 内，
// 只有「对话」页签可见——切到文件/GIT/配置等页签时输入区整块消失，必须先切回对话页签
// 才能说话。提到 body 级 + fixed 后，任意页签都能继续输入与切换配置。
//
// 分工（几何在 CSS，状态在 JS，见 style.css 同名注释）：
//   1. 停靠 = CSS 的 left:50% / bottom:8px 居中（面板是 .layout 内的浮层，天然停在
//      #footer 状态栏上方）；自由 = 拖动后的 left/top 内联样式 + position:fixed，
//      两者靠 data-composer-mode 区分。
//   2. 折叠态写 .composer-collapsed（只留标题行）。面板是独立浮层：不吃任何元素的高度，
//      状态栏等页面元素都不因它移动（旧实现的 --composer-reserve 让位机制已移除）。
//   3. 拖动：优先指针事件，回退鼠标事件；拖动期间 body 加 .composer-dragging
//      （锁光标、禁选中）；落点始终夹在视口内，窗口缩小后不会跑到屏幕外。
//   4. 键盘：把手可聚焦——方向键 8px 微调、Shift+方向键 1px 精调、Home 复位停靠，
//      双击把手同样复位（与 splitpane 的双击复位手感一致）。
//   5. 记忆：位置与折叠态落 localStorage（隐私模式静默降级），键 aicli.web.composer.v1。
//
// 对外入口：initComposerPanel()（app.js 聚合）与 toggleComposerPanel()（Ctrl+J 与
// 「视图」菜单共用同一入口，菜单与快捷键不会各自实现一份折叠逻辑）。
// aicli micro web client 前端模块(无构建步骤,由 app.js 入口聚合)。

var STORAGE_KEY = "aicli.web.composer.v1";
var DOCK_GAP = 8;  // 停靠态与状态栏的间隙（px）；真实几何由 CSS 的 bottom:8px 给出，
                   // 这里只用于「无布局引擎」时推算拖动起点（沙盒兜底）
var EDGE = 8;      // 自由位置至少留在视口内的边距（px）
var KEY_STEP = 8;  // 方向键微调步长（px）
var KEY_FINE = 1;  // Shift+方向键精调步长（px）
var FOOTER_FALLBACK = 28; // 无布局引擎时状态栏高度的近似值（仅沙盒兜底，不参与真实布局）

var els = null;
var mode = "dock";              // "dock" 底部居中 | "free" 自由位置
var pos = { left: 0, top: 0 };  // 自由位置的左上角（视口坐标）
var collapsed = false;
var dragging = null;            // { dx, dy }：拖动起点相对面板左上角的偏移；非拖动中为 null

// 记忆读写：隐私模式等场景 localStorage 会抛异常，静默忽略（本次会话内仍然生效）。
function store() {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({
      mode: mode, left: pos.left, top: pos.top, collapsed: collapsed
    }));
  } catch (e) { /* ignore */ }
}

function restore() {
  try {
    var raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) { return null; }
    var v = JSON.parse(raw);
    return v && typeof v === "object" ? v : null;
  } catch (e) { return null; }
}

function num(v, fallback) {
  var n = Number(v);
  return isFinite(n) ? n : fallback;
}

function viewport() {
  var w = 0, h = 0;
  try { w = window.innerWidth || 0; h = window.innerHeight || 0; } catch (e) { w = 0; h = 0; }
  if (!w) { w = (document.documentElement && document.documentElement.clientWidth) || 1024; }
  if (!h) { h = (document.documentElement && document.documentElement.clientHeight) || 768; }
  return { w: w, h: h };
}

function panelSize() {
  var el = els && els.panel;
  if (!el) { return { w: 0, h: 0 }; }
  var w = el.offsetWidth || 0, h = el.offsetHeight || 0;
  if ((!w || !h) && typeof el.getBoundingClientRect === "function") {
    var r = el.getBoundingClientRect();
    w = w || r.width || 0;
    h = h || r.height || 0;
  }
  return { w: w, h: h };
}

// 面板当前左上角：优先真实几何；无布局引擎（沙盒 / 隐藏）时按模式推算。
// 注意：停靠态的**真值在 CSS**（.layout 内 absolute + bottom:8px），这里的推算只用于
// 沙盒里给拖动 / 键盘微调一个合理起点，不参与任何页面布局计算。
function panelRect() {
  var el = els && els.panel;
  if (el && typeof el.getBoundingClientRect === "function") {
    var r = el.getBoundingClientRect();
    if (r && (r.width || r.height || r.left || r.top)) { return { left: r.left, top: r.top }; }
  }
  if (mode === "free") { return { left: pos.left, top: pos.top }; }
  var vp = viewport(), size = panelSize();
  return { left: (vp.w - size.w) / 2, top: vp.h - FOOTER_FALLBACK - DOCK_GAP - size.h };
}

// 自由位置夹取：整个面板留在视口内（拖动、键盘微调、窗口缩小共用）。
function clampPos(left, top) {
  var vp = viewport(), size = panelSize();
  var maxLeft = Math.max(EDGE, vp.w - size.w - EDGE);
  var maxTop = Math.max(EDGE, vp.h - size.h - EDGE);
  return {
    left: Math.min(Math.max(EDGE, left), maxLeft),
    top: Math.min(Math.max(EDGE, top), maxTop)
  };
}

function apply() {
  var panel = els && els.panel;
  if (!panel) { return; }
  if (panel.classList) { panel.classList.toggle("composer-collapsed", collapsed); }
  if (els.collapseBtn) {
    els.collapseBtn.setAttribute("aria-expanded", collapsed ? "false" : "true");
    els.collapseBtn.textContent = collapsed ? "▸" : "▾";
    els.collapseBtn.title = (collapsed ? "展开" : "折叠") + "浮动输入面板（Ctrl+J）";
  }
  if (mode === "free") {
    pos = clampPos(pos.left, pos.top);
    panel.setAttribute("data-composer-mode", "free");
    panel.style.left = pos.left + "px";
    panel.style.top = pos.top + "px";
  } else {
    panel.setAttribute("data-composer-mode", "dock");
    panel.style.left = "";
    panel.style.top = "";
  }
}

// resetDock 复位到底部居中停靠（⇲ 按钮、把手 Home / 双击共用）。
function resetDock() {
  if (!els || !els.panel) { return; }
  mode = "dock";
  pos = { left: 0, top: 0 };
  apply();
  store();
}

// 折叠 / 展开浮动面板：「视图」菜单、Ctrl+J 与面板自己的 ▾ 按钮共用本入口。
export function toggleComposerPanel() {
  if (!els || !els.panel) { return; }
  collapsed = !collapsed;
  apply();
  store();
}

// coord 取指针坐标：指针/鼠标事件直接用 clientX/Y，触摸事件回退 touches/changedTouches。
function coord(ev, axis) {
  if (!ev) { return 0; }
  var direct = axis === "x" ? ev.clientX : ev.clientY;
  if (typeof direct === "number") { return direct; }
  var list = ev.touches || ev.changedTouches;
  if (list && list.length) { return axis === "x" ? list[0].clientX : list[0].clientY; }
  return 0;
}

function onPointerDown(ev) {
  if (!els || !els.panel) { return; }
  if (ev && typeof ev.button === "number" && ev.button !== 0) { return; } // 只响应主键
  if (ev && typeof ev.preventDefault === "function") { ev.preventDefault(); } // 拖动期间不要选中文字
  var rect = panelRect();
  dragging = { dx: coord(ev, "x") - rect.left, dy: coord(ev, "y") - rect.top };
  if (document.body && document.body.classList) { document.body.classList.add("composer-dragging"); }
  // 指针捕获：指针移出把手/窗口仍能继续拖动（失败也不影响基本拖动）。
  if (ev && typeof ev.pointerId === "number" && els.grip && typeof els.grip.setPointerCapture === "function") {
    try { els.grip.setPointerCapture(ev.pointerId); } catch (e) { /* ignore */ }
  }
}

function onPointerMove(ev) {
  if (!dragging) { return; }
  if (ev && typeof ev.preventDefault === "function") { ev.preventDefault(); }
  mode = "free";
  pos = clampPos(coord(ev, "x") - dragging.dx, coord(ev, "y") - dragging.dy);
  apply();
}

function onPointerUp() {
  if (!dragging) { return; }
  dragging = null;
  if (document.body && document.body.classList) { document.body.classList.remove("composer-dragging"); }
  store(); // 只在落点确定时落盘，拖动过程中不反复写 localStorage
}

// 把手键盘微调：方向键 8px、Shift+方向键 1px、Home 复位停靠。
function onGripKey(ev) {
  var key = ev && ev.key;
  if (!key) { return; }
  if (key === "Home") { ev.preventDefault(); resetDock(); return; }
  var step = ev.shiftKey ? KEY_FINE : KEY_STEP;
  var dx = 0, dy = 0;
  if (key === "ArrowLeft") { dx = -step; }
  else if (key === "ArrowRight") { dx = step; }
  else if (key === "ArrowUp") { dy = -step; }
  else if (key === "ArrowDown") { dy = step; }
  else { return; }
  ev.preventDefault();
  var rect = panelRect();
  mode = "free";
  pos = clampPos(rect.left + dx, rect.top + dy);
  apply();
  store();
}

// Ctrl+J（macOS 上 Cmd+J）：与「视图」菜单同一入口，任意页签、任意焦点位置都可用。
// 浏览器若把该组合键留作自身快捷键（如 Chrome 的下载列表），菜单项是等价入口。
function onGlobalKey(ev) {
  if (!ev || !(ev.ctrlKey || ev.metaKey) || ev.altKey) { return; }
  if (ev.key !== "j" && ev.key !== "J") { return; }
  if (ev.preventDefault) { ev.preventDefault(); }
  toggleComposerPanel();
}

function bindDrag() {
  var grip = els.grip;
  if (!grip || typeof grip.addEventListener !== "function" || !document.addEventListener) { return; }
  var hasPointer = typeof window !== "undefined" && typeof window.PointerEvent === "function";
  if (hasPointer) {
    grip.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("pointermove", onPointerMove);
    document.addEventListener("pointerup", onPointerUp);
    document.addEventListener("pointercancel", onPointerUp);
  } else {
    grip.addEventListener("mousedown", onPointerDown);
    document.addEventListener("mousemove", onPointerMove);
    document.addEventListener("mouseup", onPointerUp);
  }
  grip.addEventListener("dblclick", resetDock);
  grip.addEventListener("keydown", onGripKey);
}

// 视口变化：自由位置重新夹回视口内（窗口缩小可能已越界）；停靠态无需任何重算——
// 面板由 CSS 定位在 .layout 底部，与面板自身高度、状态栏高度都无关。
function observeSize() {
  if (typeof window !== "undefined" && typeof window.addEventListener === "function") {
    window.addEventListener("resize", function () {
      if (mode === "free") { apply(); }
    });
  }
}

export function initComposerPanel() {
  if (!document || typeof document.getElementById !== "function") { return; }
  var panel = document.getElementById("composer-panel");
  if (!panel) { return; } // 页面没有浮动面板（旧页面 / 裁剪构建）：静默降级，不影响其它模块
  els = {
    panel: panel,
    grip: document.getElementById("composer-grip"),
    collapseBtn: document.getElementById("composer-collapse-btn"),
    resetBtn: document.getElementById("composer-reset-btn")
  };
  var saved = restore();
  if (saved) {
    if (saved.mode === "free") {
      mode = "free";
      pos = { left: num(saved.left, EDGE), top: num(saved.top, EDGE) };
    }
    collapsed = !!saved.collapsed;
  }
  if (els.collapseBtn) { els.collapseBtn.addEventListener("click", toggleComposerPanel); }
  if (els.resetBtn) { els.resetBtn.addEventListener("click", resetDock); }
  bindDrag();
  if (document.addEventListener) { document.addEventListener("keydown", onGlobalKey); }
  observeSize();
  apply();
}
