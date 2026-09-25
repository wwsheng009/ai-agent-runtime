// 可折叠 / 可拖拽的左右分栏助手：「文件」页签与「GIT」页签共用同一套交互
// （折叠成窄轨、拖拽把手、键盘微调、双击复位、宽度与折叠态记在 localStorage），
// 两个页签的手感与无障碍行为因此天然一致——不必各自实现一遍，也不会各自漂移。
//
// 分工与 style.css 的约定（两侧同名媒体查询里写几何）：
//   1. 布局容器加 class 表达折叠态：<layout>.side-collapsed（窄屏一律上下结构，折叠不生效）；
//   2. 左栏宽度写进布局容器的 CSS 变量（opts.widthVar，如 --files-side-width），
//      CSS 侧用 flex-basis 消费它；
//   3. 把手是 role=separator + tabindex=0 的元素，可聚焦 → ←/→ 微调、Home/End 到两端、
//      双击复位；拖拽期间 <body> 加 .pane-resizing（锁光标、禁选中）。
// 本模块只做 CSS 表达不了的事（折叠 class、CSS 变量、按钮文字/aria、把手 aria 数值、
// localStorage 记忆），每次对账后回调 onApply(split) 让调用方补齐各自的面板语义
// （浏览面板 hidden / 右栏空态等）。
// aicli micro web client 前端模块(无构建步骤,由 app.js 入口聚合)。

// createSplitPane 返回该分栏的控制器。opts：
//   layoutId/sideId/toggleId/splitterId  四个必需元素（缺失时对应能力静默降级）
//   widthVar          CSS 变量名（默认 --side-width）
//   widthKey/collapsedKey  localStorage 键（空字符串 = 不记忆）
//   splitQuery        分栏断点（默认 900px，与 style.css 对齐）
//   defaultWidth/minWidth/maxWidth/mainMin/step  几何常量（px）
//   collapseTitle/expandTitle  折叠按钮的 title 文案
//   onApply(split)    每次对账后的回调（split=当前是否左右分栏）
//   onBreakpoint      跨过断点时的入口函数（默认 apply）；需要先做「进入分栏」这类
//                     一次性动作的调用方传自己的对账入口，避免绕过它
export function createSplitPane(opts) {
  var o = opts || {};
  var splitQuery = o.splitQuery || "(min-width: 900px)";
  var widthVar = o.widthVar || "--side-width";
  var defaultWidth = o.defaultWidth || 300;
  var minWidth = o.minWidth || 200;
  var maxWidth = o.maxWidth || 640;
  var mainMin = o.mainMin || 320;
  var step = o.step || 24;

  var mql = null;            // matchMedia(splitQuery)；取不到就一律按窄屏处理
  var split = false;         // 当前是否处于左右分栏
  var collapsed = false;     // 左栏是否折叠（只在大屏生效，窄屏忽略该状态）
  var width = defaultWidth;  // 记忆宽度（CSS 变量的来源）
  var dragX = null;          // 拖拽起点 clientX；非拖拽中为 null
  var dragWidth = 0;         // 拖拽起点时的左栏实际宽度

  function el(id) { return id ? document.getElementById(id) : null; }
  function layoutEl() { return el(o.layoutId); }
  function sideEl() { return el(o.sideId); }
  function handleEl() { return el(o.splitterId); }

  // 记忆读写：隐私模式等场景 localStorage 会抛异常，静默忽略（本次会话内仍然生效）。
  function store(key, value) {
    if (!key) { return; }
    try { localStorage.setItem(key, value); } catch (e) { /* ignore */ }
  }
  function restore(key) {
    if (!key) { return ""; }
    try { return localStorage.getItem(key) || ""; } catch (e) { return ""; }
  }

  // splitNow 当前是否处于大屏分栏。matchMedia 不可用时按窄屏处理（降级后功能照旧，
  // 只是没有左右分栏）。
  function splitNow() { return !!(mql && mql.matches); }

  // limits 当前容器下允许的左栏宽度范围：右侧至少留 mainMin，所以窗口窄时上限会自己
  // 降下来（容器拿不到时退回常量上限）。
  function limits() {
    var layout = layoutEl();
    var max = maxWidth;
    if (layout) {
      var room = Math.round(layout.getBoundingClientRect().width) - mainMin;
      if (room > 0 && room < max) { max = room; }
    }
    if (max < minWidth) { max = minWidth; }
    return { min: minWidth, max: max };
  }

  // currentWidth 左栏**当前实际**宽度。拖拽起点与键盘步进都以它为准（不是记忆值：
  // 窗口变窄被 CSS 挤过之后，记忆值往往还没变，拿记忆值当基准会「按下就跳一下」）。
  function currentWidth() {
    var side = sideEl();
    var value = side ? Math.round(side.getBoundingClientRect().width) : 0;
    return value > 0 ? value : width;
  }

  // applyWidth 把记忆宽度写进 CSS 变量，并同步把手的 aria 数值。
  function applyWidth() {
    var layout = layoutEl();
    if (!layout || !layout.style || !layout.style.setProperty) { return; }
    layout.style.setProperty(widthVar, width + "px");
    var handle = handleEl();
    if (handle && split) {
      var lim = limits();
      // 展开时报「当前实际宽度」（被窗口挤过就是挤过的值），折叠时左栏只有窄轨，
      // 报了反而与记忆宽度对不上（此时把手 display:none，本来也不进无障碍树）。
      var shown = collapsed ? width : currentWidth();
      handle.setAttribute("aria-valuenow", String(Math.min(Math.max(shown, lim.min), lim.max)));
      handle.setAttribute("aria-valuemin", String(lim.min));
      handle.setAttribute("aria-valuemax", String(lim.max));
    }
  }

  // setWidth 设定左栏宽度（夹到当前可用范围）。拖拽时每帧都调用，所以只改内存 + CSS 变量，
  // 落盘交给收尾的那一次（persist=true）。
  function setWidth(value, persist) {
    var lim = limits();
    var next = Math.round(value);
    if (!(next >= lim.min)) { next = lim.min; }
    if (next > lim.max) { next = lim.max; }
    // 夹完还是当前渲染宽度 → 什么都没变（拖到端点、在端点继续按方向键）：直接返回，
    // 不覆盖记忆值也不落盘。否则窗口被挤窄时按一下 →，就把用户原本记着的更宽偏好抹掉了。
    if (next === currentWidth()) { return; }
    width = next;
    applyWidth();
    if (persist) { store(o.widthKey, String(width)); }
  }

  // updateToggle 折叠按钮的文字/提示/aria 跟着状态走。按钮在大屏常驻：折叠后左栏
  // 只剩一条窄轨，按钮留在轨里——不需要第二个「展开」入口，焦点也不会因为折叠而丢。
  function updateToggle() {
    var btn = el(o.toggleId);
    if (!btn) { return; }
    var folded = split && collapsed;
    btn.textContent = folded ? "»" : "«";
    btn.setAttribute("aria-expanded", folded ? "false" : "true");
    btn.title = folded ? (o.expandTitle || "展开面板") : (o.collapseTitle || "折叠面板");
  }

  // onDragMove 拖拽中：按指针位移直接算宽度（基准是按下时的实际宽度 + 起点坐标，
  // 不读 DOM，避免与 CSS 挤压互相追）。
  function onDragMove(event) {
    if (dragX === null) { return; }
    event.preventDefault();
    setWidth(dragWidth + (event.clientX - dragX), false);
  }

  // endDrag 收尾：解绑 document 上的 move/up、还原光标，并把宽度落盘一次。
  function endDrag() {
    if (dragX === null) { return; }
    dragX = null;
    document.removeEventListener("pointermove", onDragMove);
    document.removeEventListener("pointerup", endDrag);
    document.removeEventListener("pointercancel", endDrag);
    window.removeEventListener("blur", endDrag);
    if (document.body.classList) { document.body.classList.remove("pane-resizing"); }
    store(o.widthKey, String(width));
  }

  // beginDrag 按下把手开始拖拽：只在分栏、展开、主指针时生效。move/up 挂在 document 上
  // （指针捕获成功时事件照样冒泡到 document，两条路都能走通；捕获失败也不会漏事件）。
  function beginDrag(event) {
    if (!split || collapsed) { return; }
    if (event.button !== undefined && event.button !== 0) { return; } // 只认主指针/左键
    if (dragX !== null) { return; }
    var handle = handleEl();
    if (!handle) { return; }
    event.preventDefault(); // 别让拖拽顺手起一段选区
    dragX = event.clientX;
    dragWidth = currentWidth();
    if (handle.setPointerCapture && event.pointerId !== undefined) {
      try { handle.setPointerCapture(event.pointerId); } catch (e) { /* ignore */ }
    }
    document.body.classList.add("pane-resizing");
    document.addEventListener("pointermove", onDragMove);
    document.addEventListener("pointerup", endDrag);
    document.addEventListener("pointercancel", endDrag);
    // 兜底：拖到窗口外松手（没拿到指针捕获时 document 收不到 pointerup）或中途切走窗口，
    // 都会让拖拽状态卡住——窗口失焦一律按「结束拖拽」处理。
    window.addEventListener("blur", endDrag);
  }

  // onKey 键盘调整（把手 role=separator 可聚焦）：←/→ 微调、Home/End 到两端。
  function onKey(event) {
    var value = currentWidth();
    var lim = limits();
    if (event.key === "ArrowLeft") { value -= step; }
    else if (event.key === "ArrowRight") { value += step; }
    else if (event.key === "Home") { value = lim.min; }
    else if (event.key === "End") { value = lim.max; }
    else { return; }
    event.preventDefault();
    setWidth(value, true);
  }

  // apply 对账折叠态、宽度与按钮文案，最后回调 onApply(split) 让调用方补齐自己的语义。
  function apply() {
    split = splitNow();
    var layout = layoutEl();
    if (layout && layout.classList) {
      layout.classList.toggle("side-collapsed", split && collapsed);
    }
    applyWidth();
    updateToggle();
    if (typeof o.onApply === "function") { o.onApply(split); }
  }

  // toggle 折叠/展开并记住选择（下次打开页面沿用；窄屏不适用，忽略该状态）。
  function toggle() {
    collapsed = !collapsed;
    store(o.collapsedKey, collapsed ? "1" : "0");
    apply();
  }

  // init 绑定断点监听、恢复记忆值、挂上按钮与把手事件，并做首次对账。
  function init() {
    mql = window.matchMedia ? window.matchMedia(splitQuery) : null;
    if (mql) {
      var onChange = typeof o.onBreakpoint === "function" ? o.onBreakpoint : apply;
      if (mql.addEventListener) { mql.addEventListener("change", onChange); }
      else if (mql.addListener) { mql.addListener(onChange); } // 旧 Safari
    }
    collapsed = restore(o.collapsedKey) === "1";
    var saved = parseInt(restore(o.widthKey), 10);
    if (saved > 0) { width = saved; }
    var btn = el(o.toggleId);
    if (btn) { btn.addEventListener("click", toggle); }
    var handle = handleEl();
    if (handle) {
      handle.addEventListener("pointerdown", beginDrag);
      handle.addEventListener("keydown", onKey);
      handle.addEventListener("dblclick", function () { setWidth(defaultWidth, true); });
      // 聚焦时刷新一次 aria 数值：可用范围按容器宽度算，用户缩放窗口后这些值会停在旧值上，
      // 而读屏正是聚焦这一刻读它们（比挂 ResizeObserver 更省事，也没有回调环的风险）。
      handle.addEventListener("focus", applyWidth);
    }
    apply();
  }

  return {
    init: init,
    apply: apply,
    toggle: toggle,
    isSplit: function () { return split; },
    isCollapsed: function () { return collapsed; },
    wantsSplit: splitNow,   // 只看断点，不改状态：调用方用它做「进入分栏」的一次性动作
    setWidth: setWidth,
    currentWidth: currentWidth,
    limits: limits
  };
}
