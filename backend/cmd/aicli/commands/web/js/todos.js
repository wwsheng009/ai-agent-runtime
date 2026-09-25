// 任务列表浮动面板（composer 上沿）：todo 全量快照的只读展示 + 折叠记忆。
//
// 结构见 index.html #todo-panel：它贴在 #composer-panel 顶部（CSS 用 bottom:100%
// 定位到 composer 上沿，见 style.css），随 composer 停靠 / 拖动 / 折叠一起移动，
// 不做任何 JS 几何同步，也不会给页面其它元素让位。
//
// 数据两条通道（与 frontend/ 的 lib/thread-state/todos.ts 同源同形）：
//   1. 实时：SSE tool_end 的 data.todo_snapshot（后端按工具作用域裁剪的小视图，
//      只含 content/status/active_form + session_id/goal_id），按 _event.sequence
//      单调推进——旧序号（重连乱序）不回退已显示的新列表；
//   2. 回放：GET /web/api/screen?format=json 的 todo_snapshot（会话 transcript
//      最近一次 todos 工具结果），用于刷新页面 / 会话切换后恢复面板。
// 回放只在没有实时快照时兜底（实时是权威）；会话切换/结束先清空，等回放到来。
//
// 可见性与状态：
//   - 无快照 / 空列表 → 面板隐藏（不占位、不渲染空壳）；
//   - 有任务（含全部完成）→ 面板显示；全部完成时标题显示「全部完成」并整体降噪，
//     让用户能看到最后一轮任务的勾除结果；
//   - 折叠态（单行：标题 + 计数 + 当前任务）落 localStorage（键 aicli.web.todos.v1，
//     隐私模式静默降级），默认展开。
//
// aicli micro web client 前端模块(无构建步骤,由 app.js 入口聚合)。

var STORAGE_KEY = "aicli.web.todos.v1";
var MAX_ROWS = 50; // 单次渲染的条数上限：长列表只渲染前 N 条 + 「还有 N 项」

var STATUSES = ["pending", "in_progress", "completed"];
var STATUS_LABEL = { pending: "待处理", in_progress: "进行中", completed: "已完成" };
var STATUS_ICON = { pending: "○", in_progress: "◐", completed: "✓" };

var els = null;
var snapshot = null;  // { items, sessionId, goalId, seq, source }
var collapsed = false;
var boundToggle = null; // 已绑定折叠监听的按钮（重复 init 不重复绑定）
var lastReplayKey = null; // 最近一次回放载荷指纹：refreshScreen 秒级刷新时避免重复重建列表

function readText(value) {
  return typeof value === "string" ? value.trim() : "";
}

function num(value, fallback) {
  var n = Number(value);
  return isFinite(n) ? n : fallback;
}

// ---- 记忆（折叠态） ----
function store() {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ collapsed: collapsed }));
  } catch (e) { /* 隐私模式等：静默降级，本次会话内仍然生效 */ }
}

function restore() {
  try {
    var raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) { return null; }
    var v = JSON.parse(raw);
    return v && typeof v === "object" ? v : null;
  } catch (e) { return null; }
}

// ---- 解析与派生（纯函数，供沙盒校验脚本直接断言） ----

export function normalizeTodoStatus(value) {
  var status = readText(value);
  return STATUSES.indexOf(status) >= 0 ? status : "";
}

// 逐项校验 content/status：坏项丢弃；缺 active_form 记空串。
// 返回 null 表示「载荷本身不可用」（不是数组，或整组没有一条合法项）——
// 调用方必须保留原快照，而不是用空列表覆盖面板。
export function parseTodoItems(raw) {
  if (!Array.isArray(raw)) { return null; }
  var items = [];
  for (var i = 0; i < raw.length; i++) {
    var row = raw[i];
    if (!row || typeof row !== "object") { continue; }
    var content = readText(row.content);
    var status = normalizeTodoStatus(row.status);
    if (!content || !status) { continue; }
    items.push({ content: content, status: status, activeForm: readText(row.active_form) });
  }
  if (items.length === 0 && raw.length > 0) { return null; }
  return items;
}

// 载荷 → 内部快照。source: "runtime"（实时）/ "history"（回放兜底）。
export function parseTodoSnapshot(raw, seq, source) {
  if (!raw || typeof raw !== "object") { return null; }
  var items = parseTodoItems(raw.items);
  if (!items) { return null; }
  return {
    items: items,
    sessionId: readText(raw.session_id),
    goalId: readText(raw.goal_id),
    seq: num(seq, 0),
    source: source === "history" ? "history" : "runtime"
  };
}

export function countTodoItems(items) {
  var counts = { total: 0, pending: 0, inProgress: 0, completed: 0 };
  (items || []).forEach(function (item) {
    counts.total += 1;
    if (item.status === "completed") { counts.completed += 1; }
    else if (item.status === "in_progress") { counts.inProgress += 1; }
    else { counts.pending += 1; }
  });
  return counts;
}

// 当前进行项（同项目契约：同一时刻最多一条 in_progress）。
export function currentTodoItem(items) {
  var list = items || [];
  for (var i = 0; i < list.length; i++) {
    if (list[i].status === "in_progress") { return list[i]; }
  }
  return null;
}

// 单项展示文案：优先执行态文案（active_form），缺省回退任务描述。
export function todoItemLabel(item) {
  return (item && (item.activeForm || item.content)) || "";
}

// 进度百分比（整数，按已完成 / 总数）。
export function todoProgressPercent(items) {
  var counts = countTodoItems(items);
  if (counts.total === 0) { return 0; }
  return Math.round(counts.completed * 100 / counts.total);
}

// 快照合并：runtime 按 seq 单调推进（过期序号不回退）；history 只在没有
// runtime 快照时兜底（回放是「最近一次」，不一定比实时新）。
export function mergeTodoSnapshot(current, incoming) {
  var existing = current || null;
  if (!incoming) { return existing; }
  if (!existing) { return incoming; }
  if (incoming.source === "runtime") {
    if (existing.source === "runtime" && incoming.seq < existing.seq) { return existing; }
    return incoming;
  }
  return existing.source === "runtime" ? existing : incoming;
}

// ---- 渲染 ----

function progressLabel(counts) {
  var parts = [];
  if (counts.completed > 0) { parts.push("已完成 " + counts.completed); }
  if (counts.inProgress > 0) { parts.push("进行中 " + counts.inProgress); }
  if (counts.pending > 0) { parts.push("待处理 " + counts.pending); }
  return parts.join(" · ");
}

function applyCollapsed() {
  if (!els) { return; }
  if (els.panel.classList && els.panel.classList.toggle) {
    els.panel.classList.toggle("todo-collapsed", collapsed);
  }
  if (els.toggleBtn) {
    els.toggleBtn.setAttribute("aria-expanded", collapsed ? "false" : "true");
    els.toggleBtn.textContent = collapsed ? "▸" : "▾";
    els.toggleBtn.title = (collapsed ? "展开" : "折叠") + "任务列表";
  }
}

function renderRow(item) {
  var li = document.createElement("li");
  li.className = "todo-item todo-status-" + item.status.replace(/_/g, "-");
  li.setAttribute("data-status", item.status);
  li.setAttribute("aria-label", STATUS_LABEL[item.status] + "：" + item.content);
  var icon = document.createElement("span");
  icon.className = "todo-icon";
  icon.setAttribute("aria-hidden", "true");
  icon.textContent = STATUS_ICON[item.status] || "○";
  var text = document.createElement("span");
  text.className = "todo-text";
  // 进行中显示执行态文案，其余显示任务描述；title 始终给任务描述。
  text.textContent = item.status === "in_progress" ? todoItemLabel(item) : item.content;
  text.title = item.content;
  li.appendChild(icon);
  li.appendChild(text);
  return li;
}

function renderList(items) {
  var list = els.list;
  if (!list) { return; }
  list.textContent = "";
  var rows = items.length > MAX_ROWS ? items.slice(0, MAX_ROWS) : items;
  rows.forEach(function (item) { list.appendChild(renderRow(item)); });
  if (items.length > MAX_ROWS) {
    var more = document.createElement("li");
    more.className = "todo-more";
    more.textContent = "还有 " + (items.length - MAX_ROWS) + " 项…";
    list.appendChild(more);
  }
}

function render() {
  if (!els || !els.panel) { return; }
  var items = snapshot ? snapshot.items : [];
  var visible = items.length > 0;
  if (els.panel.hidden !== undefined) { els.panel.hidden = !visible; }
  if (els.panel.setAttribute) {
    els.panel.setAttribute("aria-hidden", visible ? "false" : "true");
  }
  applyCollapsed();
  if (!visible) {
    if (els.list) { els.list.textContent = ""; }
    if (els.progress) { els.progress.textContent = ""; }
    if (els.current) { els.current.textContent = ""; }
    return;
  }
  var counts = countTodoItems(items);
  if (els.progress) { els.progress.textContent = progressLabel(counts); }
  var current = currentTodoItem(items);
  if (els.current) {
    els.current.textContent = current ? todoItemLabel(current)
      : (counts.completed === counts.total ? "全部完成 ✓" : "");
    els.current.title = current ? current.content : "";
    if (els.panel.classList && els.panel.classList.toggle) {
      els.panel.classList.toggle("todo-all-done", counts.completed === counts.total);
    }
  }
  var percent = todoProgressPercent(items);
  if (els.progressbar) {
    els.progressbar.setAttribute("aria-valuemax", String(counts.total));
    els.progressbar.setAttribute("aria-valuenow", String(counts.completed));
    els.progressbar.setAttribute("aria-valuetext", "已完成 " + counts.completed + " / " + counts.total);
  }
  if (els.fill && els.fill.style) { els.fill.style.width = percent + "%"; }
  renderList(items);
}

// ---- 状态入口 ----

function setSnapshot(next) {
  if (next === snapshot) { return false; }
  snapshot = next;
  render();
  return true;
}

// 实时通道：tool_end 带 todo_snapshot 时更新面板。
// 返回 true 表示本次确实替换了面板内容（坏载荷 / 过期序号返回 false）。
export function applyTodoRuntime(data) {
  if (!data || !data.todo_snapshot) { return false; }
  var seq = data._event ? num(data._event.sequence, 0) : 0;
  var incoming = parseTodoSnapshot(data.todo_snapshot, seq, "runtime");
  if (!incoming) { return false; }
  return setSnapshot(mergeTodoSnapshot(snapshot, incoming));
}

// 回放通道：/web/api/screen 响应里的 todo_snapshot（会话级，与消息窗口/过滤无关）。
// 载荷不可用时保持现值；实时快照存在时不覆盖；同一份回放（refreshScreen 秒级刷新）
// 不重复重建列表（返回 false）。
export function applyTodoReplay(raw) {
  var incoming = parseTodoSnapshot(raw, 0, "history");
  if (!incoming) { return false; }
  var key = replayKey(incoming);
  if (key === lastReplayKey && snapshot && snapshot.source === "history") { return false; }
  lastReplayKey = key;
  return setSnapshot(mergeTodoSnapshot(snapshot, incoming));
}

// replayKey：回放载荷指纹（会话 + 逐项内容/状态；回放无 seq 可比）。
function replayKey(incoming) {
  var parts = [incoming.sessionId];
  incoming.items.forEach(function (item) {
    parts.push(item.status + ":" + item.content + ":" + item.activeForm);
  });
  return parts.join("\n");
}

// 会话切换 / 结束：清空并隐藏，等新会话的实时事件或回放快照到来
// （不清折叠偏好——那是用户级设置，不随会话走；回放指纹一并清掉，
//   否则新会话若恰好是同一份列表会被去重逻辑跳过、面板停在隐藏态）。
export function resetTodoPanel() {
  lastReplayKey = null;
  setSnapshot(null);
}

// SSE 分流：sse.js 的 onSSEEvent 统一调用（tool_end 实时更新；会话边界清空）。
export function handleTodoSSEEvent(eventName, data) {
  if (eventName === "tool_end") { return applyTodoRuntime(data); }
  if (eventName === "session_start" || eventName === "session_end" || eventName === "session_switched") {
    resetTodoPanel();
  }
  return false;
}

export function toggleTodoPanel() {
  collapsed = !collapsed;
  applyCollapsed();
  store();
}

// 供调试 / 脚本断言读取当前快照（不暴露可变引用）。
export function getTodoSnapshot() {
  return snapshot;
}

export function initTodoPanel() {
  if (!document || typeof document.getElementById !== "function") { return; }
  var panel = document.getElementById("todo-panel");
  if (!panel) { return; } // 页面没有面板（旧页面 / 裁剪构建）：静默降级
  els = {
    panel: panel,
    toggleBtn: document.getElementById("todo-toggle"),
    progress: document.getElementById("todo-progress"),
    current: document.getElementById("todo-current"),
    progressbar: document.getElementById("todo-progressbar"),
    fill: document.getElementById("todo-progressbar-fill"),
    list: document.getElementById("todo-list")
  };
  var saved = restore();
  if (saved && typeof saved.collapsed === "boolean") { collapsed = saved.collapsed; }
  // 重复 init（页面脚本重入 / 测试重放）不重复绑定：同一按钮只挂一次监听。
  if (els.toggleBtn && els.toggleBtn.addEventListener && els.toggleBtn !== boundToggle) {
    els.toggleBtn.addEventListener("click", toggleTodoPanel);
    boundToggle = els.toggleBtn;
  }
  render();
}
