// 自动化任务（Schedule）页签:跨会话任务目录 + 行内删除确认(P4)。
// aicli micro web client 前端模块(无构建步骤,由 app.js/ui.js 聚合)。
// 数据来自 /api/runtime/schedule/*(与 E1 frontend、TUI /schedule 同一后端);
// 展示口径:后端 UTC,本地展示;回执不代表模型执行完成。

import { apiFetch, esc, showToast } from "./util.js";

var currentSessionId = "";
var loaded = false;
var loading = false;

// 当前会话 id 由 sessions.js 写入(与 chat/skills 同一约定)。
export function setScheduleSession(sessionId) {
  currentSessionId = sessionId || "";
}

// 静态 HTML 端点(查询串未进入 innerHTML,列表体经 esc 转义)。
function renderTasks(tasks) {
  var listEl = document.getElementById("schedule-list");
  if (!listEl) { return; }
  if (!tasks || tasks.length === 0) {
    listEl.innerHTML = '<p class="schedule-empty">还没有自动化任务，在会话中创建的任务会显示在这里</p>';
    return;
  }
  var html = "";
  for (var i = 0; i < tasks.length; i++) {
    var task = tasks[i];
    var live = task.status === "active";
    var badge = live
      ? '<span class="schedule-badge schedule-badge-live">已开启</span>'
      : '<span class="schedule-badge">未运行</span>';
    var freq = scheduleFrequency(task);
    var next = "";
    if (live && task.scheduledAt) {
      next = '<div class="schedule-meta">下次运行: ' + esc(formatLocal(task.scheduledAt)) +
        ' <span class="schedule-dim">(' + esc(relative(task.scheduledAt)) + ')</span></div>';
    } else if (task.lastDelivery && task.lastDelivery.deliveredAt) {
      next = '<div class="schedule-meta">最近投递: ' + esc(formatLocal(task.lastDelivery.deliveredAt)) + '</div>';
    }
    var deleting = listEl.getAttribute("data-deleting") === task.id;
    var action = deleting
      ? '<button class="schedule-del schedule-del-confirm" data-sid="' + esc(task.sessionId) +
        '" data-id="' + esc(task.id) + '" disabled>确认删除</button>'
      : '<button class="schedule-del" data-sid="' + esc(task.sessionId) +
        '" data-id="' + esc(task.id) + '">删除</button>';
    html += '<li class="schedule-item">' +
      '<div class="schedule-head">' + badge +
      '<span class="schedule-title">' + esc(task.title) + '</span>' + action + '</div>' +
      '<div class="schedule-meta">' + esc(kindLabel(task.kind)) +
      (freq ? " · " + esc(freq) : "") +
      (task.timeZone ? " · " + esc(task.timeZone) : "") + '</div>' +
      next +
      '<div class="schedule-prompt">' + esc(task.prompt) + '</div>' +
      '</li>';
  }
  listEl.innerHTML = '<ul class="schedule-list-body">' + html + '</ul>';
  bindDeleteButtons(listEl);
}

function kindLabel(kind) {
  switch (kind) {
    case "after": return "一次性（延迟）";
    case "at": return "一次性（绝对时间）";
    case "every": return "固定间隔";
    case "daily": return "每天";
    case "weekly": return "每周";
    case "cron": return "Cron";
    default: return kind || "";
  }
}

function scheduleFrequency(task) {
  switch (task.kind) {
    case "after": return (task.afterSeconds || 0) + "s";
    case "every": return (task.everySeconds || 0) + "s";
    case "daily": return task.time || "";
    case "weekly": return (task.time || "") + " (" + (task.weekdays || []).join(",") + ")";
    case "cron": return task.expression || "";
    default: return "";
  }
}

function formatLocal(value) {
  var parsed = new Date(value);
  if (isNaN(parsed.getTime())) { return value; }
  return parsed.toLocaleString();
}

function relative(value) {
  var parsed = new Date(value).getTime();
  if (isNaN(parsed)) { return ""; }
  var delta = Math.round((parsed - Date.now()) / 1000);
  var sign = delta >= 0 ? "" : "-";
  var abs = Math.abs(delta);
  if (abs < 60) { return sign + abs + "s"; }
  var minutes = Math.floor(abs / 60);
  if (minutes < 60) { return sign + minutes + "m"; }
  var hours = Math.floor(minutes / 60);
  if (hours < 24) { return sign + hours + "h"; }
  return sign + Math.floor(hours / 24) + "d";
}

function bindDeleteButtons(listEl) {
  var buttons = listEl.querySelectorAll(".schedule-del");
  for (var i = 0; i < buttons.length; i++) {
    buttons[i].addEventListener("click", function (event) {
      var button = event.currentTarget;
      if (!button.classList.contains("schedule-del-confirm")) {
        // 第一次点击:进入确认态(再次点击才删除)。
        button.classList.add("schedule-del-confirm");
        button.textContent = "确认删除";
        return;
      }
      deleteTask(button.getAttribute("data-sid"), button.getAttribute("data-id"));
    });
  }
}

function deleteTask(sessionId, id) {
  var listEl = document.getElementById("schedule-list");
  apiFetch("/api/runtime/schedule/delete", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ session_id: sessionId, id: id }),
  })
    .then(function (res) { return res.json().catch(function () { return null; }); })
    .then(function () {
    showToast("任务已删除");
    loadSchedule(true);
    }).catch(function (error) {
    showToast("删除失败: " + (error && error.message ? error.message : error));
    loadSchedule(true);
  });
}

// 拉取目录;force=true 忽略已加载标记(删除/刷新按钮用)。
export function loadSchedule(force) {
  var listEl = document.getElementById("schedule-list");
  if (!listEl || loading) { return; }
  if (loaded && !force) { return; }
  loading = true;
  listEl.innerHTML = '<p class="schedule-empty">正在加载…</p>';
  apiFetch("/api/runtime/schedule/catalog")
    .then(function (res) { return res.json().catch(function () { return null; }); })
    .then(function (payload) {
    loading = false;
    loaded = true;
    renderTasks(payload && payload.tasks ? payload.tasks : []);
    }).catch(function (error) {
    loading = false;
    loaded = true;
    listEl.innerHTML = '<p class="schedule-empty">加载失败: ' +
      esc(error && error.message ? error.message : String(error)) + '</p>';
    });
}

export function initSchedule() {
  var listEl = document.getElementById("schedule-list");
  if (!listEl) { return; }
  var refreshBtn = document.getElementById("schedule-refresh-btn");
  if (refreshBtn) {
    refreshBtn.addEventListener("click", function () { loadSchedule(true); });
  }
}
