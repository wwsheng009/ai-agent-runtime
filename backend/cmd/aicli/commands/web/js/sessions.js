// 侧边栏会话列表:CRUD/重命名/排序/搜索、输入历史、输入注入(sendInput)。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { autoGrow, clearPendingPrompts, dropPendingUserPrompt, getUiState, promptEl, refreshScreen, sendStatusEl, setUI } from "./chat.js";
import { showToast } from "./util.js";

var sidebarEl = document.getElementById("sidebar");
var sidebarToggleBtn = document.getElementById("sidebar-toggle");
var sidebarCollapseBtn = document.getElementById("sidebar-collapse-btn");
var sessionsNewBtn = document.getElementById("sessions-new-btn");
var sessionsRefreshBtn = document.getElementById("sessions-refresh-btn");
var sessionsSortEl = document.getElementById("sessions-sort");
var sessionListEl = document.getElementById("session-list");
var sessionSearchEl = document.getElementById("session-search");
var sessions = [];               // 会话列表缓存（GET /web/api/sessions）
var sessionsQuery = "";          // 会话列表搜索词（纯前端过滤）
var sidebarCollapsed = false;    // 侧边栏折叠状态（localStorage 记忆）
var sessionsReqSeq = 0;          // loadSessions 请求序号（丢弃过期响应）
var sessionsSort = "created_at"; // 会话排序：created_at（默认）| updated_at

// 输入历史（localStorage 记忆，最近 50 条）
var inputHistory = [];
var inputHistoryIdx = -1;        // 当前浏览位置（-1 表示不在历史浏览中）

// 恢复排序偏好（localStorage 记忆）
try {
  var savedSort = localStorage.getItem("webSessionSort");
  if (savedSort === "created_at" || savedSort === "updated_at") { sessionsSort = savedSort; }
} catch (e) { /* ignore */ }
if (sessionsSortEl) { sessionsSortEl.value = sessionsSort; }

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
export function sendInput(payload) {
  var isInterrupt = payload && payload.type === "interrupt";
  fetch("/web/api/input", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload)
  })
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
  items.forEach(function (s) {
    var item = document.createElement("div");
    item.className = "session-item" + (s.current ? " active" : "");
    item.title = s.id;

    var main = document.createElement("button");
    main.type = "button";
    main.className = "session-main";
    main.addEventListener("click", function () { resumeSession(s.id); });

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
    sessionListEl.appendChild(item);
  });
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

// 拉取会话列表（GET /web/api/sessions）
export function loadSessions() {
  var seq = ++sessionsReqSeq;
  fetch("/web/api/sessions?sort=" + encodeURIComponent(sessionsSort), { cache: "no-store" })
    .then(function (res) { return res.ok ? res.json() : null; })
    .then(function (data) {
      if (!data) { return; }
      if (seq !== sessionsReqSeq) { return; } // 丢弃过期响应
      sessions = data.sessions || [];
      renderSessionList();
    })
    .catch(function (err) { console.error("sessions fetch failed:", err); });
}

// 切换会话（POST /web/api/sessions/resume → 注入 /resume <id>）
function resumeSession(id) {
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
  fetch("/web/api/sessions/resume", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ session_id: id })
  })
    .then(function (res) {
      return res.json().catch(function () { return { status: "error", reason: "bad response" }; });
    })
    .then(function (json) {
      for (var k = 0; k < all.length; k++) { all[k].classList.remove("resuming"); }
      if (json.status === "queued" || json.status === "already_current") {
        sendStatusEl.textContent = json.status === "already_current" ? "已是当前会话" : "已切换，刷新中…";
        if (json.status === "already_current") {
          loadSessions();
          refreshScreen();
        } else {
          // /resume 是异步注入输入队列的（主循环稍后才执行），立即刷新拿到的是旧列表。
          // 且 CLI 侧 resume 不发布 session_end/session_start SSE 事件，无法靠 SSE 感知完成时机。
          // 因此轮询 /web/api/sessions，直到 current_session_id 变成目标会话（带次数上限）。
          var attempts = 0;
          (function pollResumed() {
            fetch("/web/api/sessions?sort=" + encodeURIComponent(sessionsSort), { cache: "no-store" })
              .then(function (r) { return r.ok ? r.json() : null; })
              .then(function (data) {
                if (!data) { return; }
                var cur = data.current_session_id || "";
                if (cur === id || ++attempts >= 8) {
                  sessions = data.sessions || [];
                  clearPendingPrompts(); // 旧会话的本地回显不带到被恢复会话
                  renderSessionList();
                  refreshScreen(true);
                  sendStatusEl.textContent = cur === id ? "已切换" : "已切换(状态未同步)";
                } else {
                  setTimeout(pollResumed, 300);
                }
              })
              .catch(function (err) { console.error("resume poll failed:", err); });
          })();
        }
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
  var oldID = "";
  fetch("/web/api/sessions?sort=" + encodeURIComponent(sessionsSort), { cache: "no-store" })
    .then(function (r) { return r.ok ? r.json() : null; })
    .then(function (data) {
      if (data) { oldID = data.current_session_id || ""; }
      return fetch("/web/api/sessions/new", { method: "POST" });
    })
    .then(function (res) {
      return res.json().catch(function () { return { status: "error", reason: "bad response" }; });
    })
    .then(function (json) {
      if (json.status === "queued") {
        var attempts = 0;
        (function pollNew() {
          fetch("/web/api/sessions?sort=" + encodeURIComponent(sessionsSort), { cache: "no-store" })
            .then(function (r) { return r.ok ? r.json() : null; })
            .then(function (data) {
              if (!data) { return; }
              var cur = data.current_session_id || "";
              if ((cur !== "" && cur !== oldID) || ++attempts >= 8) {
                sessions = data.sessions || [];
                clearPendingPrompts(); // 旧会话的本地回显不带到新会话
                renderSessionList();
                refreshScreen(true);
                if (sessionsNewBtn) { sessionsNewBtn.disabled = false; }
                sendStatusEl.textContent = (cur !== "" && cur !== oldID) ? "已新建会话" : "已新建(状态未同步)";
              } else {
                setTimeout(pollNew, 300);
              }
            })
            .catch(function () { if (sessionsNewBtn) { sessionsNewBtn.disabled = false; } });
        })();
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

}
