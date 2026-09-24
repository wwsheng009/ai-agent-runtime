// 侧边栏会话列表:CRUD/重命名/排序/搜索、输入历史、输入注入(sendInput)。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { autoGrow, clearPendingPrompts, dropPendingUserPrompt, getUiState, promptEl, refreshScreen, sendStatusEl, setUI, updateTitle } from "./chat.js";
import { refreshCacheAnalyticsIfActive, syncCacheSession } from "./cache.js";
import { syncSkillsSession } from "./skills.js";
import { apiFetch, esc, showToast, webAuthToken } from "./util.js";

var sidebarEl = document.getElementById("sidebar");
var sidebarToggleBtn = document.getElementById("sidebar-toggle");
var sidebarCollapseBtn = document.getElementById("sidebar-collapse-btn");
var sessionsNewBtn = document.getElementById("sessions-new-btn");
var sessionsRefreshBtn = document.getElementById("sessions-refresh-btn");
var sessionsSortEl = document.getElementById("sessions-sort");
var sessionListEl = document.getElementById("session-list");
var sessionSearchEl = document.getElementById("session-search");
var sessionsOpenModeEl = document.getElementById("sessions-open-mode");
var headerSessionTitleEl = document.getElementById("header-session-title");
// 会话 ID 不在顶栏显示：写入「关于」页签的值元素（复制按钮交互在 ui.js）。
var aboutSessionIDEl = document.getElementById("about-session-id");
// 当前会话 id（/web/api/sessions 的 current_session_id）：会话身份的唯一来源，
// 分析页签等模块经 getCurrentSessionID() 读取，不再从 DOM 反查。
var currentSessionID = "";
var sessionSwitchOverlay = document.getElementById("session-switch-overlay");
var sessionSwitchTextEl = document.getElementById("session-switch-text");
var sessionSwitchHintEl = document.getElementById("session-switch-hint");
var sessionSwitchConfirmBtn = document.getElementById("session-switch-confirm-btn");
var sessionSwitchCancelBtn = document.getElementById("session-switch-cancel-btn");
var sessionSwitchCloseBtn = document.getElementById("session-switch-close");
var pendingSwitchSessionId = null; // 切换确认弹窗当前待确认的目标会话 id
// 会话归属冲突弹窗（Web 子方案 §5.7 / §6.3，S11）：
// running_elsewhere 三段式；ownership=conflict 只读（禁用 in-place）。
var sessionConflictOverlay = document.getElementById("session-conflict-overlay");
var sessionConflictTextEl = document.getElementById("session-conflict-text");
var sessionConflictHintEl = document.getElementById("session-conflict-hint");
var sessionConflictNodesEl = document.getElementById("session-conflict-nodes");
var sessionConflictOpenBtn = document.getElementById("session-conflict-open-btn");
var sessionConflictForceBtn = document.getElementById("session-conflict-force-btn");
var sessionConflictTakeoverBtn = document.getElementById("session-conflict-takeover-btn");
var sessionConflictCancelBtn = document.getElementById("session-conflict-cancel-btn");
var sessionConflictCloseBtn = document.getElementById("session-conflict-close");
// 「接管并切换」的二次确认态（§6.3）：第一次点击只把按钮切到确认态。
var conflictTakeoverArmed = false;
var pendingConflict = null;      // 冲突弹窗上下文 {sessionId, kind, nodeId, workspace, nodes}
var sessions = [];               // 会话列表缓存（GET /web/api/sessions）
var sessionsQuery = "";          // 会话列表搜索词（纯前端过滤）
var sidebarCollapsed = false;    // 侧边栏折叠状态（localStorage 记忆）
var sessionsReqSeq = 0;          // loadSessions 请求序号（丢弃过期响应）
var sessionsSort = "created_at"; // 会话排序：created_at（默认）| updated_at

// ---- 网格便捷视图（S11 / Web 子方案 §5.1、§5.3、§5.7）----
// 打开方式开关（§5.1 / Q13）：默认 new_window（P1 起）；只有用户显式设置过
// localStorage 才尊重用户值，未设置时不迁移。
var sessionOpenMode = "new_window";      // new_window | in_place
var otherWorkspacesCollapsed = false;    // 「其他工作区」分组折叠态（localStorage 记忆）
var meshViewAvailable = false;           // self 段非空 = 网格可用（降级时 false）
var meshSelf = null;                     // {node_id, mesh_root, workspace_path, workspace_name, counts}
var meshWorkspaces = [];                 // [{path,name,nodes,session_count,running_count}]

// ---- 网格实时订阅（S12 / Web 子方案 §5.6）----
// 单连接契约（§5.6）：网格帧默认由 sse.js 那条 `/web/api/events?mesh=1` 承载
// （服务端把扇入帧并入同一条流），本模块只负责帧处理与降级。只有在合并流没有
// 按约给出 mesh.ready 时（旧服务端 / 中间层改写查询参数），才回退到独立端点
// /web/api/mesh/events —— 那会多占一条 HTTP/1.1 连接，因此只作兜底。
//
// 帧只当「有变化」的信号，刷新统一回到 /web/api/sessions?scope=all 的同源数据——
// 前端不自行合并 peer 帧，避免与 mesh/peers 出现第二套聚合口径（§6.2 同源）。
var MESH_RECONNECT_MIN_MS = 1000;   // 断线退避 1s→2s→4s…（§5.6）
var MESH_RECONNECT_MAX_MS = 30000;  // 退避上限 30s
var MESH_POLL_INTERVAL_MS = 10000;  // SSE 不可用时的轮询兜底周期（§5.6 降级）
var MESH_REFRESH_THROTTLE_MS = 200; // 事件驱动 + 200ms 合并刷新（§5.6 节流 / Q11）
var MESH_READY_GRACE_MS = 3000;     // 合并流等 mesh.ready 的宽限期，超时才回退独立端点

var meshFallbackStream = null;          // EventSource（独立端点，仅回退路径）
var meshStreamStarted = false;          // 是否已进入订阅生命周期（网格可用才启动）
var meshStreamReady = false;            // 收到 mesh.ready = 订阅生效（据此停轮询）
var meshLastSeq = 0;                    // 续传游标（帧 id / data.seq，重连带 ?since_seq=）
var sharedStreamOpen = false;           // sse.js 的合并流是否已建立（notifySharedStreamState）
var meshReadyGraceTimer = null;         // 合并流 mesh.ready 宽限定时器
var meshReconnectDelay = MESH_RECONNECT_MIN_MS;
var meshReconnectTimer = null;
var meshRefreshTimer = null;            // 合并刷新定时器
var meshPollTimer = null;               // 轮询兜底定时器

// 输入历史（localStorage 记忆，最近 50 条）
var inputHistory = [];
var inputHistoryIdx = -1;        // 当前浏览位置（-1 表示不在历史浏览中）

// 恢复排序偏好（localStorage 记忆）
try {
  var savedSort = localStorage.getItem("webSessionSort");
  if (savedSort === "created_at" || savedSort === "updated_at") { sessionsSort = savedSort; }
} catch (e) { /* ignore */ }
if (sessionsSortEl) { sessionsSortEl.value = sessionsSort; }

// 恢复打开方式与分组折叠偏好（§5.1 / Q13；网格关闭时开关仍可见但不影响行为）
try {
  var savedOpenMode = localStorage.getItem("webSessionOpenMode");
  if (savedOpenMode === "in_place" || savedOpenMode === "new_window") { sessionOpenMode = savedOpenMode; }
  otherWorkspacesCollapsed = localStorage.getItem("webOtherWorkspacesCollapsed") === "1";
} catch (e) { /* ignore */ }
if (sessionsOpenModeEl) { sessionsOpenModeEl.value = sessionOpenMode; }

// 恢复输入历史（localStorage 记忆）
try {
  var savedHistory = JSON.parse(localStorage.getItem("webInputHistory") || "[]");
  if (Array.isArray(savedHistory)) { inputHistory = savedHistory.slice(0, 50); }
} catch (e) { /* ignore */ }

// 保存输入历史并更新 localStorage
function saveInputHistory(text) {
  if (!text.trim()) return;
  // 不重复保存最近一条
  if (inputHistory.length > 0 && inputHistory[inputHistory.length - 1] === text) return;
  inputHistory.push(text);
  if (inputHistory.length > 50) { inputHistory.shift(); }
  inputHistoryIdx = -1;
  try { localStorage.setItem("webInputHistory", JSON.stringify(inputHistory)); } catch (e) { /* ignore */ }
}

// sendInput POST /web/api/input；反馈按请求类型区分：
//   - prompt：queued 后停留在 posting，等待 SSE turn_start 进入 busy（按钮变「停止」）
//   - interrupt：收到 interrupted 即进入 interrupting，等待 session_interrupted 复位
//   - approval / question_answer：resolved 只做轻提示，按钮状态由 SSE 驱动
// 返回 fetch 链的 Promise（携带服务端判定结果），供需要按结果决策的调用方
// await；不关心结果的既有调用方可以照旧忽略返回值。
export function sendInput(payload) {
  var isInterrupt = payload && payload.type === "interrupt";
  // 超时放宽到 30s：POST 一旦被服务端接收就可能已经排队，过早中断会让界面显示
  // 「发送失败」而实际已入队（回合开始后 turn_start 会把状态纠回来）。30s 只用于
  // 兜住「请求根本没发出去（连接池占满）」这一类无限等待。
  return apiFetch("/web/api/input", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload)
  }, 30000)
    .then(function (res) { return res.json().catch(function () { return { status: "error", reason: "bad response" }; }); })
    .then(function (json) {
      if (json.status === "queued") {
        if (payload && payload.prompt) { saveInputHistory(payload.prompt); }
        promptEl.value = "";
        inputHistoryIdx = -1;
        autoGrow();
        if (getUiState() === "busy") {
          // 执行中排队（Enter 键入下一条）：保持 busy，不改变按钮角色
          sendStatusEl.textContent = "已排队，将在当前任务后执行…";
        } else {
          setUI("posting", "已排队，等待执行…");
        }
      } else if (json.status === "interrupted") {
        // 竞态防御：turn_end 可能已先行到达（此时已是 idle）。
        // 保持 idle 并给出提示，避免按钮退回「正在停止…」卡住。
        if (getUiState() === "idle") {
          sendStatusEl.textContent = "已停止";
        } else {
          setUI("interrupting", "正在停止…");
        }
      } else if (json.status === "stale") {
        // 反向交互已无对应挂起项（提问已被回答 / 本轮已终止）：答案没有进入
        // 模型上下文，必须如实提示，不能落进「已提交」的成功分支。
        sendStatusEl.textContent = "未送达: " + (json.reason || "提问已结束");
        if (payload && payload.type === "question_answer") {
          showToast("回答未送达模型: " + (json.reason || "提问已结束"), "error", 5000);
        }
      } else if (json.status === "resolved") {
        sendStatusEl.textContent = "已提交";
      } else {
        if (getUiState() === "busy") {
          sendStatusEl.textContent = "排队失败: " + (json.reason || json.status);
        } else {
          setUI("idle", "失败: " + (json.reason || json.status));
        }
        if (payload && payload.prompt) { dropPendingUserPrompt(payload.prompt); }
      }
      return json;
    })
    .catch(function (err) {
      if (isInterrupt) {
        setUI("busy", "停止失败: " + err);
      } else if (getUiState() === "busy") {
        sendStatusEl.textContent = "发送失败: " + err;
      } else {
        setUI("idle", "发送失败: " + err);
      }
      if (payload && payload.prompt) { dropPendingUserPrompt(payload.prompt); }
      return { status: "error", reason: String(err) };
    });
}// ---- 左侧会话列表：折叠/展开 ----
function setSidebarCollapsed(collapsed) {
  sidebarCollapsed = collapsed;
  document.body.classList.toggle("sidebar-collapsed", collapsed);
  try { localStorage.setItem("webSidebarCollapsed", collapsed ? "1" : "0"); } catch (e) { /* ignore */ }
  if (sidebarToggleBtn) {
    sidebarToggleBtn.textContent = collapsed ? "☰" : "«";
    sidebarToggleBtn.title = collapsed ? "展开会话列表" : "折叠会话列表";
  }
}

// 恢复折叠状态（localStorage 记忆）
try {
  setSidebarCollapsed(localStorage.getItem("webSidebarCollapsed") === "1");
} catch (e) { /* ignore */ }

function fmtSessionTime(iso) {
  if (!iso) { return ""; }
  var d = new Date(iso);
  if (isNaN(d.getTime())) { return ""; }
  var now = new Date();
  var sameDay = d.toDateString() === now.toDateString();
  function pad(n) { return n < 10 ? "0" + n : "" + n; }
  var hhmm = pad(d.getHours()) + ":" + pad(d.getMinutes());
  if (sameDay) { return hhmm; }
  return pad(d.getMonth() + 1) + "-" + pad(d.getDate()) + " " + hhmm;
}

// 渲染会话列表。顺序完全由后端决定（默认按创建时间降序，可切更新时间），
// 前端不排序，避免点击切换后列表位置跳动。
// 搜索过滤：按标题/摘要/ID 子串匹配（不区分大小写），纯前端过滤。
function renderSessionList() {
  if (!sessionListEl) { return; }
  var items = sessions || [];
  var query = sessionsQuery.toLowerCase();
  if (query) {
    items = items.filter(function (s) {
      return (s.title || "").toLowerCase().indexOf(query) !== -1 ||
             (s.summary || "").toLowerCase().indexOf(query) !== -1 ||
             (s.id || "").toLowerCase().indexOf(query) !== -1;
    });
  }
  sessionListEl.innerHTML = "";
  if (!items.length) {
    var empty = document.createElement("div");
    empty.className = "session-empty";
    if (query && sessions && sessions.length) {
      empty.textContent = "无匹配会话";
      var clearBtn = document.createElement("button");
      clearBtn.type = "button";
      clearBtn.className = "session-search-clear";
      clearBtn.textContent = "清除搜索";
      clearBtn.addEventListener("click", function () {
        if (sessionSearchEl) { sessionSearchEl.value = ""; }
        sessionsQuery = "";
        renderSessionList();
      });
      empty.appendChild(document.createElement("br"));
      empty.appendChild(clearBtn);
    } else {
      empty.textContent = sessions && sessions.length ? "" : "暂无历史会话";
    }
    sessionListEl.appendChild(empty);
    return;
  }
  // 跨工作区分组（§5.3）：网格可用时把非本进程工作区的条目归入「其他工作区」。
  // 分组只是呈现方式、不是可见性边界（网格 §4.2 规则 5）；网格关闭/降级时
  // meshViewAvailable=false，全部条目进主列表 = 现状视图。
  var mainItems = [];
  var otherItems = [];
  items.forEach(function (s) {
    if (meshViewAvailable && isOtherWorkspaceSession(s)) { otherItems.push(s); } else { mainItems.push(s); }
  });
  mainItems.forEach(function (s) { sessionListEl.appendChild(buildSessionItem(s)); });
  if (otherItems.length) { sessionListEl.appendChild(buildOtherWorkspacesGroup(otherItems)); }
}

// buildSessionItem 渲染单个会话条目（主列表与「其他工作区」分组共用）。
function buildSessionItem(s) {
  var item = document.createElement("div");
  item.className = "session-item" + (s.current ? " active" : "");
  item.title = s.id;

  var main = document.createElement("button");
  main.type = "button";
  main.className = "session-main";
  main.addEventListener("click", function () { handleSessionPrimaryClick(s, item); });

  var title = document.createElement("span");
  title.className = "session-title";
  title.textContent = s.title || "(未命名会话)";

  var summary = null;
  if (s.summary) {
    summary = document.createElement("span");
    summary.className = "session-summary";
    summary.textContent = s.summary;
  }

  var meta = document.createElement("span");
  meta.className = "session-meta";
  var bits = [];
  if (s.current) { bits.push('<span class="session-current">● 当前</span>'); }
  if (typeof s.message_count === "number") { bits.push(s.message_count + " 条消息"); }
  var ts = sessionsSort === "updated_at"
    ? fmtSessionTime(s.updated_at || s.created_at)
    : fmtSessionTime(s.created_at || s.updated_at);
  if (ts) { bits.push(ts); }
  meta.innerHTML = bits.join(" · ");

  main.appendChild(title);
  if (summary) { main.appendChild(summary); }
  main.appendChild(meta);
  var endpointLine = buildSessionEndpointLine(s);
  if (endpointLine) { main.appendChild(endpointLine); }

  var actions = document.createElement("span");
  actions.className = "session-actions";
  var renameBtn = document.createElement("button");
  renameBtn.type = "button";
  renameBtn.className = "session-action session-rename-btn";
  renameBtn.title = "重命名会话";
  renameBtn.textContent = "✎";
  renameBtn.addEventListener("click", function (ev) {
    ev.stopPropagation();
    beginRenameSession(s.id, item, title);
  });
  actions.appendChild(renameBtn);
  // 二级动作「在当前进程切换」（§5.1 表的二级动作列）：主点击被打开方式开关
  // 或 peer 归属占走时的显式入口；会话在别处运行时由 /resume 的归属检查兜底
  // （running_elsewhere → 三段式弹窗，§5.7）。
  if (meshViewAvailable && !s.current && sessionPrimaryAction(s) !== "in_place") {
    var switchBtn = document.createElement("button");
    switchBtn.type = "button";
    switchBtn.className = "session-action session-switch-btn";
    switchBtn.title = "在当前进程切换（会话在别处运行时会有冲突提示）";
    switchBtn.textContent = "⇄";
    switchBtn.addEventListener("click", function (ev) {
      ev.stopPropagation();
      resumeSession(s.id);
    });
    actions.appendChild(switchBtn);
  }
  // 在新窗口打开（§7.3 窗口 URL）：复用活节点或拉起独立进程，见
  // openSessionInNewWindow。当前会话同样可开（另一个进程 = 另一份上下文）。
  var openBtn = document.createElement("button");
  openBtn.type = "button";
  openBtn.className = "session-action session-open-btn";
  openBtn.title = "在新窗口打开（复用活节点，必要时拉起新进程）";
  openBtn.textContent = "⧉";
  openBtn.addEventListener("click", function (ev) {
    ev.stopPropagation();
    if (!confirmSpawnForSession(s)) { return; }
    openSessionInNewWindow(s.id, item);
  });
  actions.appendChild(openBtn);
  if (!s.current) {
    var deleteBtn = document.createElement("button");
    deleteBtn.type = "button";
    deleteBtn.className = "session-action session-delete-btn";
    deleteBtn.title = "删除会话";
    deleteBtn.textContent = "🗑";
    deleteBtn.addEventListener("click", function (ev) {
      ev.stopPropagation();
      confirmDeleteSession(s);
    });
    actions.appendChild(deleteBtn);
  }

  item.appendChild(main);
  item.appendChild(actions);
  return item;
}

// ---- 网格便捷视图的呈现（§5.1 徽标 / §5.3 分组）----

// 路径归一：Windows 大小写与分隔符不敏感，用于本工作区/其他工作区的比较。
function normalizePath(p) {
  return String(p || "").replace(/\\/g, "/").replace(/\/+$/, "").toLowerCase();
}

function lastPathSegment(p) {
  var parts = String(p || "").replace(/\\/g, "/").split("/");
  for (var i = parts.length - 1; i >= 0; i--) { if (parts[i]) { return parts[i]; } }
  return "";
}

// 条目工作区标签：优先 name，缺失时退化路径末段（§5.3：不做猜测）。
function sessionWorkspaceLabel(s) {
  if (s.workspace_name) { return s.workspace_name; }
  return lastPathSegment(s.workspace_path);
}

// 是否属于「其他工作区」：与 self.workspace_path 不同即算（§5.3）。
// 任一侧缺工作区信息时不猜（留在主列表）。
function isOtherWorkspaceSession(s) {
  var selfPath = meshSelf && meshSelf.workspace_path ? String(meshSelf.workspace_path) : "";
  var path = s && s.workspace_path ? String(s.workspace_path) : "";
  if (!selfPath || !path) { return false; }
  return normalizePath(path) !== normalizePath(selfPath);
}

// 节点端点里的 host:port（徽标文案用）。
function endpointAddr(ep) {
  var base = ep && ep.base_url ? String(ep.base_url) : "";
  if (!base) { return ""; }
  return base.replace(/^[a-zA-Z]+:\/\//, "").replace(/\/.*$/, "");
}

// 节点 ID 短后缀（排障用，§5.1）：node-8124-2026… → node-8124。
function shortNodeID(id) {
  var s = String(id || "").trim();
  if (!s) { return ""; }
  var parts = s.split("-");
  if (parts.length >= 2 && parts[0] && parts[1]) { return parts[0] + "-" + parts[1]; }
  return s.length > 12 ? s.slice(0, 12) + "…" : s;
}

// 状态徽标（§5.1 优先级表在 S11 载荷下的折算）。形状 + 文本双编码（Q12），
// 不只靠颜色；网格不可用时整条端点行不渲染（降级 = 无徽标现状视图）。
function sessionBadge(s) {
  var state = s.session_state || "unknown";
  var own = s.ownership || "none";
  var addr = endpointAddr(s.endpoint);
  if (own === "conflict") {
    return {
      cls: "sb-conflict",
      text: "⚠ 冲突（" + (s.conflict_count || 0) + " 个节点）",
      title: "多个活节点声称服务同一会话；先运行 aicli-mesh doctor 人工判断"
    };
  }
  if (state === "busy") {
    return own === "owner"
      ? { cls: "sb-busy", text: "◐ 忙碌（本窗口）", title: "本进程有任务进行中" }
      : { cls: "sb-busy", text: "◐ 忙碌" + (addr ? " @" + addr : ""), title: "该节点有任务进行中，打开新窗口不打扰它" };
  }
  if (state === "running") {
    return own === "owner"
      ? { cls: "sb-running", text: "● 运行中（本窗口）", title: "本进程正在服务该会话" }
      : { cls: "sb-running", text: "● 运行中" + (addr ? " @" + addr : ""), title: "别的活节点正在服务该会话（打开新窗口 = 复用对方节点）" };
  }
  if (state === "orphaned") {
    return {
      cls: "sb-idle",
      text: "⚠ 已让渡",
      title: "会话租约已被其它节点显式接管（网格 §4.4）：本节点继续运行但不再拥有该会话；打开新窗口会复用新节点"
    };
  }
  if (state === "idle") {
    var last = s.last_known;
    var lastAddr = last && last.host ? last.host + ":" + last.port : "";
    return lastAddr
      ? { cls: "sb-idle", text: "— 仅历史 · 上次 @" + lastAddr, title: "当前无活节点；地址来自 mesh/bindings 的「上次地址」" }
      : { cls: "sb-idle", text: "— 仅历史", title: "当前无活节点" };
  }
  return { cls: "sb-unknown", text: "? 状态未知", title: "网格不可读或档案不可解析：先运行 aicli-mesh show <session>" };
}

// 端点信息行（§5.1）：工作区名（跨工作区高亮）· 状态徽标 · 节点短后缀。
function buildSessionEndpointLine(s) {
  if (!meshViewAvailable) { return null; }
  var badge = sessionBadge(s);
  var line = document.createElement("span");
  line.className = "session-endpoint";
  var wsLabel = sessionWorkspaceLabel(s);
  if (wsLabel) {
    var ws = document.createElement("span");
    ws.className = "session-ws" + (isOtherWorkspaceSession(s) ? " session-ws-other" : "");
    ws.textContent = wsLabel;
    if (s.workspace_path) { ws.title = s.workspace_path; }
    line.appendChild(ws);
    line.appendChild(document.createTextNode(" · "));
  }
  var badgeEl = document.createElement("span");
  badgeEl.className = "session-badge " + badge.cls;
  badgeEl.textContent = badge.text;
  if (badge.title) { badgeEl.title = badge.title; }
  line.appendChild(badgeEl);
  var nodeID = s.endpoint && s.endpoint.node_id ? String(s.endpoint.node_id) : "";
  if (nodeID) {
    var nodeEl = document.createElement("span");
    nodeEl.className = "session-node";
    nodeEl.textContent = " · " + shortNodeID(nodeID);
    nodeEl.title = nodeID;
    line.appendChild(nodeEl);
  }
  return line;
}

// 主点击行为（§5.1 徽标优先级表 + 打开方式开关 §5.1/Q13）：
//   refresh  当前会话 → 聚焦/刷新本窗口（既有 already_current 路径）
//   conflict 归属冲突 → 展开冲突详情，禁用 in-place（§5.7）
//   disabled 状态未知 → 禁用主点击（提示 aicli-mesh show）
//   new_window / in_place 其余按开关；peer 占用恒走新窗口（复用对方节点，
//   in-place 会把它加载进第二个进程、可能状态分叉）
function sessionPrimaryAction(s) {
  if (s.current) { return "refresh"; }
  if (!meshViewAvailable) { return "in_place"; }
  if (s.ownership === "conflict") { return "conflict"; }
  if ((s.session_state || "unknown") === "unknown") { return "disabled"; }
  if (s.ownership === "peer") { return "new_window"; }
  return sessionOpenMode === "in_place" ? "in_place" : "new_window";
}

function handleSessionPrimaryClick(s, itemEl) {
  var action = sessionPrimaryAction(s);
  if (action === "disabled") {
    showToast("状态未知：先运行 aicli-mesh show " + s.id + " 查看节点档案", "error");
    return;
  }
  if (action === "conflict") {
    showSessionConflict({ kind: "conflict", sessionId: s.id, nodes: [] });
    return;
  }
  if (action === "new_window") {
    if (!confirmSpawnForSession(s)) { return; }
    openSessionInNewWindow(s.id, itemEl);
    return;
  }
  // refresh（当前会话）与 in_place 都走既有切换路径（/resume 归属检查兜底）。
  resumeSession(s.id);
}

// Q2：为「无活节点」的会话拉起新进程属高权限动作，先二次确认（文案含工作区与
// 等价 CLI 命令）；有活节点时 spawn 只做复用（不新起进程），不打扰用户。
function confirmSpawnForSession(s) {
  var state = s.session_state || "unknown";
  if (state === "running" || state === "busy") { return true; }
  var ws = sessionWorkspaceLabel(s) || "(未知工作区)";
  return window.confirm(
    "会话「" + (s.title || s.id) + "」当前没有运行中的节点。\n" +
    "将在工作区 " + ws + " 拉起新进程后打开窗口（等价命令：aicli-mesh open " + s.id + "）。\n\n继续？"
  );
}

// 「其他工作区（N）」分组（§5.3）：数据来自 ?scope=all 合并的 peer 会话条目；
// 分组内按工作区聚合，标题「name · N 个运行中」，有活节点的工作区优先。
function buildOtherWorkspacesGroup(otherItems) {
  var wrap = document.createElement("div");
  wrap.className = "session-group" + (otherWorkspacesCollapsed ? " collapsed" : "");
  var toggle = document.createElement("button");
  toggle.type = "button";
  toggle.className = "session-group-toggle";
  toggle.textContent = (otherWorkspacesCollapsed ? "▶" : "▼") + " 其他工作区（" + otherItems.length + "）";
  toggle.title = "非本进程工作区的会话（网格全量口径，默认可见；分组只是呈现方式）";
  toggle.addEventListener("click", function () {
    otherWorkspacesCollapsed = !otherWorkspacesCollapsed;
    try { localStorage.setItem("webOtherWorkspacesCollapsed", otherWorkspacesCollapsed ? "1" : "0"); } catch (e) { /* ignore */ }
    renderSessionList();
  });
  wrap.appendChild(toggle);

  var body = document.createElement("div");
  body.className = "session-group-body";
  var groups = [];
  var indexByKey = {};
  otherItems.forEach(function (s) {
    var key = s.workspace_path || s.workspace_name || "(无工作区)";
    if (indexByKey[key] === undefined) {
      indexByKey[key] = groups.length;
      groups.push({ key: key, name: sessionWorkspaceLabel(s) || "(无工作区)", items: [] });
    }
    groups[indexByKey[key]].items.push(s);
  });
  var runningCount = function (group) {
    var n = 0;
    group.items.forEach(function (s) {
      if (s.session_state === "running" || s.session_state === "busy") { n++; }
    });
    return n;
  };
  groups.sort(function (a, b) {
    var ar = runningCount(a);
    var br = runningCount(b);
    if (ar !== br) { return br - ar; }
    return String(a.name).localeCompare(String(b.name));
  });
  groups.forEach(function (group) {
    var head = document.createElement("div");
    head.className = "session-group-ws";
    head.textContent = group.name + " · " + runningCount(group) + " 个运行中";
    head.title = group.key;
    body.appendChild(head);
    group.items.forEach(function (s) { body.appendChild(buildSessionItem(s)); });
  });
  wrap.appendChild(body);
  return wrap;
}

// ---- 内联重命名：标题替换为输入框，Enter/失焦提交，Esc 取消 ----
function beginRenameSession(id, itemEl, titleEl) {
  if (itemEl.querySelector(".session-rename-input")) { return; }
  var input = document.createElement("input");
  input.type = "text";
  input.className = "session-rename-input";
  var raw = titleEl.textContent;
  input.value = (raw === "(未命名会话)" || raw === "(untitled)") ? "" : raw;
  input.maxLength = 100;
  var committed = false;
  var finish = function (save) {
    if (committed) { return; }
    committed = true;
    if (save) {
      var val = input.value.trim();
      if (!val) { renderSessionList(); return; }
      commitRenameSession(id, val, itemEl);
    } else {
      renderSessionList();
    }
  };
  input.addEventListener("keydown", function (ev) {
    if (ev.key === "Enter") { ev.preventDefault(); finish(true); }
    else if (ev.key === "Escape") { ev.preventDefault(); finish(false); }
  });
  input.addEventListener("blur", function () { finish(true); });
  itemEl.classList.add("renaming");
  titleEl.style.display = "none";
  titleEl.parentNode.insertBefore(input, titleEl.nextSibling);
  input.focus();
  input.select();
}

function commitRenameSession(id, val, itemEl) {
  if (itemEl) { itemEl.classList.add("resuming"); }
  fetch("/web/api/sessions/rename", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ session_id: id, title: val })
  })
    .then(function (res) {
      return res.json().catch(function () { return { status: "error", reason: "bad response" }; });
    })
    .then(function (json) {
      if (json.status === "ok") {
        for (var i = 0; i < sessions.length; i++) {
          if (sessions[i].id === id) { sessions[i].title = json.title || val; break; }
        }
        showToast("已重命名为「" + (json.title || val) + "」", "ok");
        loadSessions();
      } else {
        showToast("重命名失败: " + (json.reason || json.status), "error");
        renderSessionList();
      }
    })
    .catch(function (err) {
      showToast("重命名失败: " + err, "error");
      renderSessionList();
    });
}

// ---- 删除确认 + 请求 ----
function confirmDeleteSession(s) {
  if (!window.confirm("确定删除会话「" + (s.title || s.id) + "」？此操作不可恢复。")) { return; }
  fetch("/web/api/sessions/delete", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ session_id: s.id })
  })
    .then(function (res) {
      return res.json().catch(function () { return { status: "error", reason: "bad response" }; });
    })
    .then(function (json) {
      if (json.status === "ok") {
        sessions = sessions.filter(function (x) { return x.id !== s.id; });
        showToast("已删除会话", "ok");
        renderSessionList();
      } else {
        showToast("删除失败: " + (json.reason || json.status), "error");
      }
    })
    .catch(function (err) { showToast("删除失败: " + err, "error"); });
}

// ---- 在新窗口打开（POST /web/api/mesh/spawn，架构 §5.7） ----
//
// 服务端在会话工作区复用活节点、必要时拉起新进程，返回 §7.3 窗口 URL
// （含令牌）。浏览器侧两条约束：
//   - 必须先在点击手势里同步 window.open 出空窗口，await 之后再
//     win.location.replace(url)：fetch 之后才 window.open 会被弹窗拦截；
//   - URL 只交给那个窗口，不落 localStorage/sessionStorage/DOM（M7：
//     这是令牌唯一允许出现的传输位置）。
function openSessionInNewWindow(id, itemEl) {
  if (!id) { return; }
  var win = null;
  try { win = window.open("", "_blank"); } catch (e) { win = null; }
  if (itemEl) { itemEl.classList.add("resuming"); }
  fetch("/web/api/mesh/spawn", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ session_id: id, origin: "web" })
  })
    .then(function (res) {
      return res.json().catch(function () { return { status: "error", reason: "bad response" }; });
    })
    .then(function (json) {
      if (itemEl) { itemEl.classList.remove("resuming"); }
      var url = json && json.url;
      if (url && (json.status === "reused" || json.status === "started")) {
        if (win) { win.location.replace(url); } else { window.open(url, "_blank"); }
        showToast(json.status === "reused" ? "已在活节点打开新窗口" : "已拉起新进程并打开窗口", "ok");
        return;
      }
      closeBlankWindow(win);
      showToast("打开新窗口失败: " + spawnFailureText(json, id), "error");
    })
    .catch(function (err) {
      if (itemEl) { itemEl.classList.remove("resuming"); }
      closeBlankWindow(win);
      showToast("打开新窗口失败: " + err, "error");
    });
}

function closeBlankWindow(win) {
  if (!win) { return; }
  try { win.close(); } catch (e) { /* 跨源后 close 可能被拒，忽略 */ }
}

// SPAWN_CODE_TEXT 是 spawn 失败 code → 可执行文案的映射（P2 ③ / §5.2 回退路径）。
// 前端不重试：策略拒绝与拉起失败一律指回 CLI 面，并给出对应诊断命令。
var SPAWN_CODE_TEXT = {
  mesh_spawn_not_allowed: "本节点已关闭 spawn（--mesh-allow-spawn=false）",
  mesh_disabled: "本节点未启用网格（启动时加 --mesh）",
  mesh_nonloopback_denied: "spawn 仅接受回环请求（请在本机浏览器操作）",
  mesh_workspace_missing: "会话的工作区目录不存在或不可读",
  mesh_cross_workspace_denied: "目标开启了 --mesh-restrict-workspace：跨工作区写调用被拒",
  mesh_spawn_timeout: "等待节点就绪超时",
  mesh_spawn_failed: "拉起节点进程失败",
  mesh_spawn_bin_unavailable: "本机找不到可用的 aicli 可执行文件（AICLI_BIN 指错或未完整安装）"
};

// spawnFailureText 把 §5.9 信封（status/code/reason/message）压成一行提示；
// 已知 code 用固定文案，其余回退到服务端 reason（code 永远保留，便于检索）。
function spawnFailureText(json, sessionID) {
  if (!json) { return "无响应"; }
  var code = json.code || json.status || "error";
  var detail = json.reason || json.message || "";
  var known = SPAWN_CODE_TEXT[code];
  var text = known ? code + " — " + known : (detail ? code + " — " + detail : String(code));
  var target = sessionID || "<session>";
  if (json.status === "refused") {
    // §5.2 回退路径：策略拒绝不在前端重试，改用 CLI 面拿 URL。
    text += "；改用 CLI：aicli-mesh open " + target + " --print-url";
  } else if (code === "mesh_spawn_bin_unavailable") {
    // 二进制问题不是会话问题：诊断入口是 doctor（它打印解析到的路径与来源）。
    text += "；诊断：aicli-mesh doctor";
  } else if (code === "mesh_spawn_timeout" || code === "mesh_spawn_failed" || code === "mesh_workspace_missing") {
    // 失败态给出诊断命令（§5.2「复制诊断命令」）：节点档案与最近心跳。
    text += "；诊断：aicli-mesh show " + target;
  }
  return text;
}

// ---- 深链（§7.3 窗口 URL）：/web?session=<id>&token=<t> ----
//
// token 由页面头部的内联脚本（Go 注入，先于 ES 模块执行）转存 sessionStorage
// 并从地址栏抹掉；这里只处理 session。子进程本就以该会话启动（spawn 传
// `resume <sid>`），所以通常 current_session_id 已经相等——此时什么都不做。
// 只有对不上（例如深链指向别的会话）才复用既有切换路径补一次。
function applyDeepLinkSession() {
  var target = window.__aicli_deep_link_session || "";
  if (!target || target === currentSessionID) { return; }
  window.__aicli_deep_link_session = "";
  var known = false;
  for (var i = 0; i < sessions.length; i++) {
    if (sessions[i].id === target) { known = true; break; }
  }
  if (!known) {
    showToast("深链会话不存在: " + target, "error");
    return;
  }
  // 深链本身就是明确的切换意图，不弹确认框。
  proceedResumeSession(target);
}

// 当前会话身份：顶栏只显示标题，「关于」页签显示完整会话 ID。currentId 为
// /web/api/sessions 响应的 current_session_id；标题从 sessions 缓存按 id 匹配
// （调用点都保证缓存已随响应同步更新：loadSessions / 切换轮询 / 新建轮询），
// 与侧栏列表同一数据时机。"(untitled)"（后端占位标题）归一为中文占位；
// 长标题由 CSS 截断，悬停（title）看全值。
function updateSessionIdentity(currentId) {
  currentSessionID = currentId || "";
  if (headerSessionTitleEl) {
    if (!currentSessionID) {
      headerSessionTitleEl.textContent = "未选择会话";
      headerSessionTitleEl.removeAttribute("title");
    } else {
      var title = "";
      for (var i = 0; i < sessions.length; i++) {
        if (sessions[i].id === currentSessionID) { title = sessions[i].title || ""; break; }
      }
      var shown = title && title !== "(untitled)" ? title : "(未命名会话)";
      headerSessionTitleEl.textContent = shown;
      headerSessionTitleEl.title = shown;
    }
  }
  if (aboutSessionIDEl) {
    // 值始终完整显示（CSS 允许换行），无需再用 title 兜底。
    aboutSessionIDEl.textContent = currentSessionID || "（未选择会话）";
  }
}

// applyMeshView 同步网格便捷视图（§6.2）：self 段为空 = 网格关闭 / 根不可读，
// 前端整体回退到无徽标的现状视图（§4.7 降级契约：不报错、不提示）。
function applyMeshView(data) {
  meshSelf = data && data.self ? data.self : null;
  meshWorkspaces = data && Array.isArray(data.workspaces) ? data.workspaces : [];
  meshViewAvailable = !!meshSelf;
  maybeStartMeshStream();
  updateTitle(); // 标题节点后缀（P2 ⑤）：self 到达 / 消失都要重算一次
}

// meshNodeSuffix 是窗口标题的节点后缀（P2 ⑤ / §7.3）：多窗口并排时回答
// 「这个窗口是哪个节点、哪个工作区」。网格不可用（self 为空）时返回空串，
// 标题保持原样——降级视图不显示无意义占位（§4.7）。
export function meshNodeSuffix() {
  if (!meshSelf) { return ""; }
  var bits = [];
  var ws = meshSelf.workspace_name || lastPathSegment(meshSelf.workspace_path || "");
  if (ws) { bits.push(ws); }
  var node = String(meshSelf.node_id || "");
  if (node) { bits.push(node.length > 8 ? node.slice(0, 8) : node); }
  return bits.length ? " · " + bits.join(" · ") : "";
}

// ---- 网格实时订阅：连接 / 退避 / 降级 / 节流（S12 / §5.6）----

// meshEventsURL：独立端点（回退路径）地址 = 续传游标 + 非回环模式的令牌
// （EventSource 不能设请求头，取值顺序见 util.js::webAuthToken；回环模式
// GET/SSE 本就不需要令牌）。
function meshEventsURL() {
  var url = "/web/api/mesh/events";
  if (meshLastSeq > 0) { url += "?since_seq=" + meshLastSeq; }
  var token = webAuthToken();
  if (token) { url += (url.indexOf("?") >= 0 ? "&" : "?") + "token=" + encodeURIComponent(token); }
  return url;
}

// handleMeshStreamEvent：合并流（/web/api/events?mesh=1）分发的网格帧入口
// （sse.js 调用）。与独立端点共用 handleMeshFrame，只多一层 SSE `id:` 行带来的
// 续传游标（合并流上 id 行只出现在网格帧上）。
export function handleMeshStreamEvent(name, data, lastEventId) {
  if (lastEventId) {
    var id = Number(lastEventId);
    if (id > meshLastSeq) { meshLastSeq = id; }
  }
  handleMeshFrame(name, data);
}

// meshResumeSeq：合并流的续传游标（sse.js 重连时带 ?since_seq=<n>）。
export function meshResumeSeq() { return meshLastSeq; }

// notifySharedStreamState：sse.js 的共享流连接状态回调。
//   - 断开：网格帧随之中断 → 立即退回 10s 轮询兜底（§5.6 降级）；
//   - 建立：进入 mesh.ready 宽限期；等到就停轮询，等不到才启用独立端点回退。
export function notifySharedStreamState(open) {
  sharedStreamOpen = !!open;
  if (!open) {
    meshStreamReady = false;
    startMeshPolling();
    return;
  }
  scheduleMeshReadyGrace();
}

// maybeStartMeshStream：只在网格可用（sessions 响应 self 非空）时订阅。
// --mesh=false 时路由不注册（§9.7），订阅只会制造无意义的错误重试；降级视图
// 本来也没有徽标要刷新（§4.7：不报错、不提示）。
function maybeStartMeshStream() {
  if (meshStreamStarted || !meshViewAvailable) { return; }
  meshStreamStarted = true;
  scheduleMeshReadyGrace();
}

// scheduleMeshReadyGrace：合并流的 mesh.ready 宽限期。合并流正常时会在连接建立
// 后立刻回 mesh.ready（merged=true）；宽限期内没等到就回退独立端点，保证实时
// 视图在任何服务端/中间层组合下都不丢（回退会多占一条连接，只发生在这条路径）。
function scheduleMeshReadyGrace() {
  if (!meshStreamStarted || meshStreamReady || meshFallbackStream || meshReadyGraceTimer) { return; }
  if (!sharedStreamOpen) { return; }
  meshReadyGraceTimer = setTimeout(function () {
    meshReadyGraceTimer = null;
    if (meshStreamReady || meshFallbackStream || !sharedStreamOpen) { return; }
    openMeshStream();
  }, MESH_READY_GRACE_MS);
}

function clearMeshReadyGrace() {
  if (meshReadyGraceTimer) { clearTimeout(meshReadyGraceTimer); meshReadyGraceTimer = null; }
}

function closeMeshStream() {
  if (meshFallbackStream) {
    try { meshFallbackStream.close(); } catch (e) { /* ignore */ }
    meshFallbackStream = null;
  }
}

// openMeshStream：建立独立端点的回退订阅（旧服务端 / 中间层改写查询参数）。
// EventSource 自带重连既不能带 since_seq、也不能自定义退避，因此一律自行接管
// （onerror → 关连接 + 退避重连）。
function openMeshStream() {
  if (!meshStreamStarted) { return; }
  closeMeshStream();
  meshStreamReady = false;
  var es;
  try { es = new EventSource(meshEventsURL()); } catch (e) { scheduleMeshReconnect(); return; }
  meshFallbackStream = es;
  es.onerror = function () {
    closeMeshStream();
    meshStreamReady = false;
    startMeshPolling();      // 降级：SSE 不可用期间仍有 10s 全量兜底
    scheduleMeshReconnect(); // 退避重连（连上后由 mesh.ready 停掉轮询）
  };
  ["mesh.ready", "mesh.unavailable", "mesh.peer.joined", "mesh.peer.left", "mesh.peer.updated",
   "mesh.session.changed", "mesh.peer.event", "mesh.lagged"].forEach(function (name) {
    es.addEventListener(name, function (e) {
      var data = {};
      try { data = JSON.parse(e.data); } catch (err) { /* 非 JSON 帧忽略 */ }
      handleMeshStreamEvent(name, data, e.lastEventId);
    });
  });
}

function scheduleMeshReconnect() {
  if (!meshStreamStarted || meshReconnectTimer) { return; }
  var delay = meshReconnectDelay;
  meshReconnectDelay = Math.min(meshReconnectDelay * 2, MESH_RECONNECT_MAX_MS);
  meshReconnectTimer = setTimeout(function () {
    meshReconnectTimer = null;
    openMeshStream();
  }, delay);
}

function stopMeshPolling() {
  if (meshPollTimer) { clearInterval(meshPollTimer); meshPollTimer = null; }
}

// startMeshPolling：SSE 不可用（旧节点 / 非回环无令牌 / 代理阻断）时的降级
// 轮询：10s 拉一次同源视图，徽标仍可用，只是不实时（§5.6）。
function startMeshPolling() {
  if (meshPollTimer) { return; }
  meshPollTimer = setInterval(function () {
    if (meshStreamReady) { stopMeshPolling(); return; }
    scheduleMeshRefresh();
  }, MESH_POLL_INTERVAL_MS);
}

// scheduleMeshRefresh：事件驱动的合并刷新（§5.6 节流）：200ms 内的多帧只拉一次，
// 避免高频 turn 事件把侧栏重排打成幻灯片（Q11）。
function scheduleMeshRefresh() {
  if (meshRefreshTimer) { return; }
  meshRefreshTimer = setTimeout(function () {
    meshRefreshTimer = null;
    // 内联重命名进行中不重建列表（否则输入框被连根拔掉）；下一次事件或轮询补上。
    if (sessionListEl && sessionListEl.querySelector(".session-rename-input")) { return; }
    loadSessions();
  }, MESH_REFRESH_THROTTLE_MS);
}

// handleMeshFrame：§5.6 帧表的落点。joined / left / updated / session.changed 与
// peer 白名单事件（turn.* / session.*）都只标记「需要刷新」——增删、计数、忙碌
// 翻转、归属变化统一由同源全量视图重算（分组计数因此天然一致，不另存增量）。
function handleMeshFrame(name, data) {
  var seq = Number(data && data.seq) || 0;
  if (seq > meshLastSeq) { meshLastSeq = seq; }
  switch (name) {
    case "mesh.ready":
      // 订阅生效：重置退避、停掉降级轮询（首帧只回显订阅参数，无需刷新）。
      meshStreamReady = true;
      meshReconnectDelay = MESH_RECONNECT_MIN_MS;
      clearMeshReadyGrace();
      stopMeshPolling();
      return;
    case "mesh.unavailable":
      // 服务端明确表示本次不提供网格帧（--mesh=false / 扇入客户端超限 429）：
      // 不报错、不重连，保持 10s 轮询兜底（§4.7 / §5.6 降级）。
      startMeshPolling();
      return;
    case "mesh.lagged":
      // 缓冲溢出跳号：丢弃增量语义，立刻做一次全量兜底（§6.4）。
      scheduleMeshRefresh();
      return;
    case "mesh.peer.joined":
    case "mesh.peer.left":
    case "mesh.peer.updated":
    case "mesh.session.changed":
      // §4.4：本进程的会话被别的节点显式接管——提示不可静默（Q8）。
      if (name === "mesh.session.changed" && data && data.event === "orphaned") {
        var by = data.by_node_id ? "节点 " + data.by_node_id : "其它节点";
        showToast("会话 " + (data.session_id || "") + " 已被" + by + "接管：本窗口继续运行，但不再拥有该会话", "error");
      }
      scheduleMeshRefresh();
      return;
    case "mesh.peer.event":
      scheduleMeshRefresh();
      return;
    default:
      return;
  }
}

// 拉取会话列表（GET /web/api/sessions）。
// scope=all（§5.3 / Q5 硬契约）：跨工作区节点默认就在侧栏里，分组只是呈现方式；
// 网格关闭时该参数无副作用（后端不并 peer，口径与现状逐字一致）。
export function loadSessions() {
  var seq = ++sessionsReqSeq;
  // 带超时（util.js::apiFetch）：连接池被常驻流占满时，这里的请求会永久排队，
  // 侧栏就一直是空的且没有任何提示——超时后计入降级状态（sse.js 显示横幅并轮询重试）。
  apiFetch("/web/api/sessions?sort=" + encodeURIComponent(sessionsSort) + "&scope=all", { cache: "no-store" })
    .then(function (res) { return res.ok ? res.json() : null; })
    .then(function (data) {
      if (!data) { return; }
      if (seq !== sessionsReqSeq) { return; } // 丢弃过期响应
      sessions = data.sessions || [];
      applyMeshView(data);
      // 同步当前会话 id：会话变化时缓存页签按需重拉（可见立即刷，后台则记录，
      // 下次进入页签时由 loadCacheAnalytics 按会话不一致强制刷新）。
      syncCacheSession(data.current_session_id);
      // 技能页签同约定：目录属于当前会话的 Function Catalog，会话变化即过期。
      syncSkillsSession(data.current_session_id);
      // 顶栏标题 + 关于页签会话 ID 同步
      updateSessionIdentity(data.current_session_id);
      renderSessionList();
      // §7.3 深链：列表就绪后再对齐 ?session=（见 applyDeepLinkSession）
      applyDeepLinkSession();
    })
    .catch(function (err) { console.error("sessions fetch failed:", err); });
}

// 切换会话入口：点击非当前会话时先弹确认框（防误切丢上下文），
// 点击当前会话（already_current 刷新路径）不弹窗直接执行。
function resumeSession(id) {
  if (!id) { return; }
  var target = null;
  for (var i = 0; i < sessions.length; i++) {
    if (sessions[i].id === id) { target = sessions[i]; break; }
  }
  if (target && target.current) { proceedResumeSession(id); return; }
  showSessionSwitchConfirm(target, id);
}

// ---- 切换确认弹窗 ----
function hideSessionSwitchConfirm() {
  if (sessionSwitchOverlay) { sessionSwitchOverlay.classList.remove("active"); }
  pendingSwitchSessionId = null;
}

function showSessionSwitchConfirm(target, id) {
  if (!sessionSwitchOverlay || !sessionSwitchTextEl) { // DOM 缺失时退化为直接切换
    proceedResumeSession(id);
    return;
  }
  pendingSwitchSessionId = id;
  var shown = target && target.title && target.title !== "(untitled)" ? target.title : "(未命名会话)";
  sessionSwitchTextEl.textContent = "确定切换到会话「" + shown + "」？";
  sessionSwitchTextEl.title = "会话 ID：" + id;
  if (sessionSwitchHintEl) {
    // /resume 注入输入队列（FIFO）：忙碌时切换在当前任务与排队输入之后生效，
    // 不会打断进行中的 turn；排队输入仍在原会话执行。
    var busy = getUiState() !== "idle";
    sessionSwitchHintEl.textContent = busy ? "当前会话有任务进行中，切换将在当前任务与排队输入完成后生效。" : "";
    sessionSwitchHintEl.style.display = busy ? "" : "none";
  }
  sessionSwitchOverlay.classList.add("active");
  if (sessionSwitchConfirmBtn) { sessionSwitchConfirmBtn.focus(); }
}

// ---- 会话归属冲突弹窗（§5.7，S11）----
//
// 两种来源（§6.3）：
//   running_elsewhere → 三段式（打开那个窗口 / 仍在本进程切换 → force / 取消）；
//   conflict          → 只读：列出冲突节点，禁用 in-place，提示 aicli-mesh doctor。
// 「打开那个窗口」走本节点 mesh/spawn：服务端复用活节点并把令牌内联进 URL
// （§5.5 红线 2），前端不解析、不存储、不显示令牌（M7）。
function hideSessionConflict() {
  if (sessionConflictOverlay) { sessionConflictOverlay.classList.remove("active"); }
  pendingConflict = null;
}

function sessionTitleOf(id) {
  for (var i = 0; i < sessions.length; i++) {
    if (sessions[i].id === id) {
      var t = sessions[i].title || "";
      return t && t !== "(untitled)" ? t : "";
    }
  }
  return "";
}

function showSessionConflict(ctx) {
  if (!ctx) { return; }
  if (!sessionConflictOverlay || !sessionConflictTextEl) {
    // DOM 缺失：退化为 Toast——警示不可静默（§5.7）。
    showToast(ctx.kind === "conflict"
      ? "会话归属冲突：先运行 aicli-mesh doctor"
      : "该会话正在其它窗口运行（可用 aicli-mesh open " + ctx.sessionId + " 打开）", "error");
    return;
  }
  pendingConflict = ctx;
  conflictTakeoverArmed = false;
  var isConflict = ctx.kind === "conflict";
  var shown = sessionTitleOf(ctx.sessionId) || "(未命名会话)";
  if (isConflict) {
    sessionConflictTextEl.textContent = "会话「" + shown + "」被多个活节点声称服务（归属冲突）。";
  } else {
    var wsLabel = ctx.workspace ? lastPathSegment(ctx.workspace) : "";
    sessionConflictTextEl.textContent = "会话「" + shown + "」正在 " +
      (wsLabel ? wsLabel + " 工作区" : "其它工作区") + " 的窗口中运行" +
      (ctx.nodeId ? "（" + ctx.nodeId + "）" : "") + "。";
  }
  sessionConflictTextEl.title = "会话 ID：" + ctx.sessionId;
  if (sessionConflictHintEl) {
    sessionConflictHintEl.textContent = isConflict
      ? "冲突需人工判断：先运行 aicli-mesh doctor 查看节点档案；本进程切换已禁用。"
      : "在本进程切换会把它加载进第二个进程，可能造成状态分叉；「接管并切换」会显式回收租约（旧窗口继续运行，但会标记为已让渡）。";
    sessionConflictHintEl.style.display = "";
  }
  if (sessionConflictNodesEl) {
    sessionConflictNodesEl.innerHTML = "";
    var nodes = Array.isArray(ctx.nodes) ? ctx.nodes : [];
    if (nodes.length) {
      nodes.forEach(function (node) {
        var li = document.createElement("li");
        var bits = [String(node.node_id || "(未知节点)")];
        if (node.pid) { bits.push("pid " + node.pid); }
        if (node.workspace) { bits.push(lastPathSegment(node.workspace)); }
        if (node.heartbeat_at) { bits.push("心跳 " + node.heartbeat_at); }
        li.textContent = bits.join(" · ");
        sessionConflictNodesEl.appendChild(li);
      });
      sessionConflictNodesEl.style.display = "";
    } else {
      sessionConflictNodesEl.style.display = "none";
    }
  }
  if (sessionConflictOpenBtn) {
    sessionConflictOpenBtn.style.display = isConflict ? "none" : "";
    // 复用对方节点的前提是有可用的节点档案（node_id）；缺档案时只留 force / 取消。
    sessionConflictOpenBtn.disabled = !ctx.nodeId;
  }
  if (sessionConflictForceBtn) {
    // 冲突时禁用 in-place（§5.7）；force 只对 running_elsewhere 开放。
    sessionConflictForceBtn.disabled = isConflict;
    sessionConflictForceBtn.title = isConflict ? "归属冲突时禁用：先运行 aicli-mesh doctor" : "跳过归属检查，在本进程切换（可能状态分叉）";
  }
  if (sessionConflictTakeoverBtn) {
    // S15：接管入口（§6.3）只对 running_elsewhere 且服务端报告可用时开放；
    // conflict（≥2 节点）时禁用——冲突需人工判断（§5.7）。
    var canTakeover = !isConflict && ctx.takeoverAvailable === true;
    sessionConflictTakeoverBtn.style.display = canTakeover ? "" : "none";
    sessionConflictTakeoverBtn.disabled = !canTakeover;
    sessionConflictTakeoverBtn.textContent = "接管并切换";
    sessionConflictTakeoverBtn.title = canTakeover
      ? "显式回收会话租约（网格 §4.4）：旧窗口继续运行，但会标记为已让渡（orphaned）"
      : "";
  }
  sessionConflictOverlay.classList.add("active");
  if (sessionConflictOpenBtn && !sessionConflictOpenBtn.disabled) {
    sessionConflictOpenBtn.focus();
  } else if (sessionConflictForceBtn) {
    sessionConflictForceBtn.focus();
  }
}

// armSessionSwitchFallback(targetID, timeoutStatus)：会话切换的断连兜底（P2 ④）。
//
// 正常路径：主循环执行 /resume、/new、/load 后，服务端 SSE handler 合成
// session_switched，sse.js 的事件分支完成刷新（loadSessions 覆盖网格视图、
// 缓存/技能页签、会话身份与列表重绘）。只有 SSE 断连时本定时器才生效，语义
// 与旧的 8×300ms 轮询超时收尾一致：重拉列表、清本地回显、按目标 id 回退身份。
var sessionSwitchFallbackTimer = null;
function armSessionSwitchFallback(targetID, timeoutStatus) {
  if (sessionSwitchFallbackTimer) { clearTimeout(sessionSwitchFallbackTimer); }
  sessionSwitchFallbackTimer = setTimeout(function () {
    sessionSwitchFallbackTimer = null;
    loadSessions();
    clearPendingPrompts(); // 旧会话的本地回显不带到被切换会话
    refreshScreen(true);
    if (targetID) { updateSessionIdentity(targetID); } // 与旧轮询超时同口径：回退目标会话 id
    if (timeoutStatus) { sendStatusEl.textContent = timeoutStatus; }
  }, 4000);
}

// notifySessionSwitchedCompleted：由 sse.js 的 session_switched 分支调用（P2 ④）。
// 切换完成即恢复切换期间禁用的入口（新建按钮），清掉断连兜底定时器，并给出
// 终态提示（旧实现由 8×300ms 轮询的收尾分支承担同一职责）。
export function notifySessionSwitchedCompleted() {
  if (sessionsNewBtn) { sessionsNewBtn.disabled = false; }
  if (sessionSwitchFallbackTimer) {
    clearTimeout(sessionSwitchFallbackTimer);
    sessionSwitchFallbackTimer = null;
  }
  sendStatusEl.textContent = "会话已切换";
}

// 确认后的实际切换逻辑（POST /web/api/sessions/resume → 注入 /resume <id>）。
// force=true 跳过网格归属检查（§6.3）：只在用户于冲突弹窗里显式确认
// 「仍在本进程切换」时使用；takeover=true 走显式租约回收（§4.4，S15）。
function proceedResumeSession(id, force, takeover) {
  if (!id) { return; }
  // 点击项进入 resuming 状态，避免重复提交
  var all = sessionListEl.querySelectorAll(".session-item");
  for (var i = 0; i < all.length; i++) { all[i].classList.remove("resuming"); }
  var target = null;
  for (var j = 0; j < all.length; j++) {
    if (all[j].title === id) { target = all[j]; break; }
  }
  if (target) { target.classList.add("resuming"); }
  sendStatusEl.textContent = "切换会话中…";
  var payload = { session_id: id };
  if (force) { payload.force = true; }
  if (takeover) { payload.takeover = true; }
  fetch("/web/api/sessions/resume", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload)
  })
    .then(function (res) {
      return res.json().catch(function () { return { status: "error", reason: "bad response" }; });
    })
    .then(function (json) {
      for (var k = 0; k < all.length; k++) { all[k].classList.remove("resuming"); }
      if (json.status === "taken_over") {
        // §4.4：租约已回收，/resume 已入队；等 session_switched 完成刷新。
        sendStatusEl.textContent = "已接管，切换中…";
        armSessionSwitchFallback(id, "已接管(状态未同步)");
      } else if (json.status === "takeover_failed") {
        sendStatusEl.textContent = "接管失败: " + (json.reason || "unknown");
        showToast("接管失败: " + (json.reason || "unknown") + "（未切换，原窗口继续服务）", "error");
      } else if (json.status === "queued" || json.status === "already_current") {
        sendStatusEl.textContent = json.status === "already_current" ? "已是当前会话" : "已切换，刷新中…";
        if (json.status === "already_current") {
          loadSessions();
          refreshScreen();
          refreshCacheAnalyticsIfActive();
        } else {
          // /resume 是异步注入输入队列的（主循环稍后才执行），立即刷新拿到的是旧列表。
          // 切换不产生 turn，运行时不会发布 session_end/session_start；服务端 SSE
          // handler 以会话身份变化为准合成 session_switched（P2 ④），sse.js 的事件
          // 分支负责刷新。这里不再轮询 /web/api/sessions，只留断连兜底定时器。
          armSessionSwitchFallback(id, "已切换(状态未同步)");
        }
      } else if (json.status === "running_elsewhere") {
        // §5.7 / §6.3：目标会话正被另一个活节点服务，未注入队列 → 三段式弹窗。
        sendStatusEl.textContent = "该会话正在其它窗口运行";
        showSessionConflict({
          kind: "running_elsewhere",
          sessionId: id,
          nodeId: json.node_id,
          webUrl: json.web_url,
          workspace: json.workspace,
          endpoint: json.endpoint,
          takeoverAvailable: json.takeover_available === true
        });
      } else if (json.status === "conflict") {
        // ≥2 个活节点声称同一会话：禁用 in-place，提示人工判断（§6.5）。
        sendStatusEl.textContent = "会话归属冲突，未切换";
        showSessionConflict({ kind: "conflict", sessionId: id, nodes: json.nodes || [] });
      } else {
        sendStatusEl.textContent = "切换失败: " + (json.reason || json.status);
      }
    })
    .catch(function (err) {
      for (var m = 0; m < all.length; m++) { all[m].classList.remove("resuming"); }
      sendStatusEl.textContent = "切换失败: " + err;
    });
}// 屏幕刷新策略（§8.6 方法二）：关键事件后主动拉取屏幕内容。

// 新建会话（POST /web/api/sessions/new → 注入 /new）
function createNewSession() {
  if (sessionsNewBtn) { sessionsNewBtn.disabled = true; }
  sendStatusEl.textContent = "新建会话中…";
  fetch("/web/api/sessions/new", { method: "POST" })
    .then(function (res) {
      return res.json().catch(function () { return { status: "error", reason: "bad response" }; });
    })
    .then(function (json) {
      if (json.status === "queued") {
        // /new 同样由主循环异步执行：切换完成由 SSE session_switched 驱动（P2 ④），
        // 届时 notifySessionSwitchedCompleted 解禁按钮。这里不再轮询
        // /web/api/sessions，只留断连兜底定时器。
        armSessionSwitchFallback("", "已新建(状态未同步)");
      } else {
        if (sessionsNewBtn) { sessionsNewBtn.disabled = false; }
        sendStatusEl.textContent = "新建失败: " + (json.reason || json.status);
      }
    })
    .catch(function (err) {
      if (sessionsNewBtn) { sessionsNewBtn.disabled = false; }
      sendStatusEl.textContent = "新建失败: " + err;
    });
}

// ---- 跨模块状态访问接口(拆分引入:输入历史供对话区快捷键浏览) ----
export function getInputHistory() { return inputHistory; }
// 当前会话 id：分析页签等按会话判定快照是否过期（空串 = 尚未拿到会话列表响应）。
export function getCurrentSessionID() { return currentSessionID; }
export function getInputHistoryIdx() { return inputHistoryIdx; }
export function setInputHistoryIdx(v) { inputHistoryIdx = v; }

export function initSessions() {
  // ---- 侧边栏按钮 ----
  if (sidebarToggleBtn) {
    sidebarToggleBtn.addEventListener("click", function () { setSidebarCollapsed(!sidebarCollapsed); });
  }
  if (sidebarCollapseBtn) {
    sidebarCollapseBtn.addEventListener("click", function () { setSidebarCollapsed(true); });
  }
  if (sessionsNewBtn) {
    sessionsNewBtn.addEventListener("click", function () { createNewSession(); });
  }
  if (sessionsRefreshBtn) {
    sessionsRefreshBtn.addEventListener("click", function () { loadSessions(); });
  }
  if (sessionsSortEl) {
    sessionsSortEl.addEventListener("change", function () {
      sessionsSort = sessionsSortEl.value === "updated_at" ? "updated_at" : "created_at";
      try { localStorage.setItem("webSessionSort", sessionsSort); } catch (e) { /* ignore */ }
      loadSessions();
    });
  }
  if (sessionSearchEl) {
    sessionSearchEl.addEventListener("input", function () {
      sessionsQuery = sessionSearchEl.value;
      renderSessionList();
    });
  }
  // 打开方式开关（§5.1 / Q13）：主点击行为 = 新窗口打开（默认）/ 在本进程切换。
  // 只影响主点击；条目上的 ⧉ / ⇄ 二级动作始终可用。
  if (sessionsOpenModeEl) {
    sessionsOpenModeEl.addEventListener("change", function () {
      sessionOpenMode = sessionsOpenModeEl.value === "in_place" ? "in_place" : "new_window";
      try { localStorage.setItem("webSessionOpenMode", sessionOpenMode); } catch (e) { /* ignore */ }
      renderSessionList();
    });
  }

  // ---- 切换确认弹窗：确认 / 取消 / 关闭按钮 / 遮罩空白处 ----
  if (sessionSwitchConfirmBtn) {
    sessionSwitchConfirmBtn.addEventListener("click", function () {
      var id = pendingSwitchSessionId;
      hideSessionSwitchConfirm();
      if (id) { proceedResumeSession(id); }
    });
  }
  if (sessionSwitchCancelBtn) {
    sessionSwitchCancelBtn.addEventListener("click", function () { hideSessionSwitchConfirm(); });
  }
  if (sessionSwitchCloseBtn) {
    sessionSwitchCloseBtn.addEventListener("click", function () { hideSessionSwitchConfirm(); });
  }
  if (sessionSwitchOverlay) {
    sessionSwitchOverlay.addEventListener("click", function (e) {
      if (e.target === sessionSwitchOverlay) { hideSessionSwitchConfirm(); }
    });
  }
  // 弹窗打开期间 Esc 仅关闭弹窗：document capture 阶段短路后续监听，
  // 避免焦点仍落在输入框时同时触发全局 Esc=中断（promptEl keydown）。
  document.addEventListener("keydown", function (e) {
    if (!sessionSwitchOverlay || !sessionSwitchOverlay.classList.contains("active")) { return; }
    if (e.key !== "Escape") { return; }
    e.preventDefault();
    e.stopPropagation();
    hideSessionSwitchConfirm();
  }, true);

  // ---- 冲突弹窗（§5.7）：打开那个窗口 / 仍在本进程切换（force=true）/ 取消 ----
  if (sessionConflictOpenBtn) {
    sessionConflictOpenBtn.addEventListener("click", function () {
      var ctx = pendingConflict;
      hideSessionConflict();
      if (!ctx) { return; }
      // 走本节点 mesh/spawn：服务端复用活节点并内联令牌（§5.5 红线 2/3）。
      // 点击手势内 openSessionInNewWindow 会同步预开空白窗口，避免弹窗拦截（§5.2）。
      openSessionInNewWindow(ctx.sessionId, null);
    });
  }
  if (sessionConflictForceBtn) {
    sessionConflictForceBtn.addEventListener("click", function () {
      var ctx = pendingConflict;
      hideSessionConflict();
      if (ctx && ctx.kind !== "conflict") { proceedResumeSession(ctx.sessionId, true); }
    });
  }
  if (sessionConflictTakeoverBtn) {
    // 接管是显式所有权转移（§4.4）：第一次点击只把按钮切到确认态，第二次才
    // 发 {takeover:true}；旧窗口不会被杀，只会标记为已让渡。
    sessionConflictTakeoverBtn.addEventListener("click", function () {
      var ctx = pendingConflict;
      if (!ctx || ctx.kind === "conflict" || !ctx.sessionId) { return; }
      if (!conflictTakeoverArmed) {
        conflictTakeoverArmed = true;
        sessionConflictTakeoverBtn.textContent = "确认接管（旧窗口继续运行）";
        sessionConflictTakeoverBtn.title = "再次点击即回收租约并切换；旧窗口不会被杀，只会标记为已让渡";
        return;
      }
      conflictTakeoverArmed = false;
      hideSessionConflict();
      proceedResumeSession(ctx.sessionId, false, true);
    });
  }
  if (sessionConflictCancelBtn) {
    sessionConflictCancelBtn.addEventListener("click", function () { hideSessionConflict(); });
  }
  if (sessionConflictCloseBtn) {
    sessionConflictCloseBtn.addEventListener("click", function () { hideSessionConflict(); });
  }
  if (sessionConflictOverlay) {
    sessionConflictOverlay.addEventListener("click", function (e) {
      if (e.target === sessionConflictOverlay) { hideSessionConflict(); }
    });
  }
  // 与切换确认弹窗同策略：capture 阶段短路，避免 Esc 同时触发全局「中断」。
  document.addEventListener("keydown", function (e) {
    if (!sessionConflictOverlay || !sessionConflictOverlay.classList.contains("active")) { return; }
    if (e.key !== "Escape") { return; }
    e.preventDefault();
    e.stopPropagation();
    hideSessionConflict();
  }, true);

}
