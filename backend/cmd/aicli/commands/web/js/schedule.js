// 自动化任务（Schedule）页签:跨会话任务目录 + 行内删除确认(P4) + 人工创建(P7)。
// aicli micro web client 前端模块(无构建步骤,由 app.js/ui.js 聚合)。
// 数据来自 /api/runtime/schedule/*(与 E1 frontend、TUI /schedule 同一后端);
// 展示口径:后端 UTC,本地展示;回执不代表模型执行完成。
// 人工创建调 POST /schedule/create(origin=manual):选择器校验在后端服务层,
// 与模型工具同一条管线;本端只做轻量的必填/形状预检。

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
  initCreateForm();
}

// ---- 人工创建（P7）----

function initCreateForm() {
  var newBtn = document.getElementById("schedule-new-btn");
  var overlay = document.getElementById("schedule-form-overlay");
  var form = document.getElementById("schedule-form");
  if (!newBtn || !overlay || !form) { return; }
  newBtn.addEventListener("click", function () {
    openCreateForm();
  });
  var cancelBtn = document.getElementById("schedule-form-cancel");
  if (cancelBtn) {
    cancelBtn.addEventListener("click", function () { closeCreateForm(); });
  }
  // Esc 关闭（与技能详情弹层同一约定）；弹层内部按键不冒泡。
  overlay.addEventListener("keydown", function (event) {
    if (event.key === "Escape") {
      event.stopPropagation();
      closeCreateForm();
    }
  });
  // 点击遮罩空白处关闭（与技能详情弹层行为一致）。
  overlay.addEventListener("click", function (event) {
    if (event.target === overlay) { closeCreateForm(); }
  });
  var kindSelect = document.getElementById("schedule-form-kind");
  if (kindSelect) {
    kindSelect.addEventListener("change", function () {
      syncCreateFormFields(kindSelect.value);
    });
  }
  form.addEventListener("submit", function (event) {
    event.preventDefault();
    submitCreateForm();
  });
}

function openCreateForm() {
  var overlay = document.getElementById("schedule-form-overlay");
  var form = document.getElementById("schedule-form");
  var status = document.getElementById("schedule-form-status");
  if (!overlay || !form) { return; }
  form.reset();
  if (status) { status.textContent = ""; }
  var zone = document.getElementById("schedule-form-zone");
  if (zone && !zone.value) {
    try { zone.value = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"; } catch (_) { zone.value = "UTC"; }
  }
  var kind = document.getElementById("schedule-form-kind");
  syncCreateFormFields(kind ? kind.value : "after");
  overlay.classList.add("active");
  var first = document.getElementById("schedule-form-session");
  if (first) { first.focus(); }
}

function closeCreateForm() {
  var overlay = document.getElementById("schedule-form-overlay");
  if (overlay) { overlay.classList.remove("active"); }
}

// 规则类型 → 可见字段（after/every 用 amount，at 用 time，daily/weekly/cron
// 用 time+zone，weekly 另有 weekdays，cron 另有 expression）。
function syncCreateFormFields(kind) {
  var show = function (id, visible) {
    var el = document.getElementById(id);
    if (el) { el.hidden = !visible; }
  };
  var amountLabel = document.getElementById("schedule-form-amount-label");
  show("schedule-form-amount-item", kind === "after" || kind === "every");
  if (amountLabel) {
    amountLabel.textContent = kind === "after" ? "多少分钟后" : "间隔（分钟，≥1）";
  }
  show("schedule-form-time-item", kind === "at");
  show("schedule-form-zone-item", kind === "daily" || kind === "weekly" || kind === "cron");
  show("schedule-form-weekdays-item", kind === "weekly");
  show("schedule-form-cron-item", kind === "cron");
  var timeLabel = document.getElementById("schedule-form-time-label");
  if (timeLabel) { timeLabel.textContent = "具体时间"; }
}

// 由表单派生创建负载；返回 null 表示本地预检未过（状态栏提示）。
function buildCreateRequest() {
  var val = function (id) {
    var el = document.getElementById(id);
    return el ? String(el.value || "").trim() : "";
  };
  var sessionId = val("schedule-form-session");
  var title = val("schedule-form-title");
  var prompt = val("schedule-form-prompt");
  var kindEl = document.getElementById("schedule-form-kind");
  var kind = kindEl ? kindEl.value : "after";
  if (!sessionId) { return { error: "请填写目标会话 ID" }; }
  if (!title) { return { error: "请填写任务名称" }; }
  if (!prompt) { return { error: "请填写任务内容" }; }
  var base = { session_id: sessionId, title: title, prompt: prompt };
  if (kind === "after" || kind === "every") {
    var minutes = Number(val("schedule-form-amount"));
    if (!isFinite(minutes) || minutes <= 0 || (kind === "every" && minutes < 1)) {
      return { error: kind === "after" ? "延迟分钟数必须为正整数" : "间隔必须至少 1 分钟" };
    }
    if (kind === "after") { base.after_seconds = Math.round(minutes * 60); }
    else { base.every_seconds = Math.round(minutes * 60); }
    return base;
  }
  if (kind === "at") {
    var raw = val("schedule-form-time");
    if (!raw) { return { error: "请选择具体时间" }; }
    var parsed = new Date(raw);
    if (isNaN(parsed.getTime())) { return { error: "时间格式无效" }; }
    base.at = parsed.toISOString();
    return base;
  }
  var zone = val("schedule-form-zone");
  if (!zone) { return { error: "请填写时区（IANA，如 Asia/Shanghai）" }; }
  if (kind === "daily") {
    var dailyTime = val("schedule-form-time");
    if (!dailyTime) { return { error: "请填写本地时间（HH:MM[:SS]）" }; }
    base.daily = { time: dailyTime, time_zone: zone };
    return base;
  }
  if (kind === "weekly") {
    var weeklyTime = val("schedule-form-time");
    if (!weeklyTime) { return { error: "请填写本地时间（HH:MM[:SS]）" }; }
    var boxes = document.querySelectorAll("#schedule-form-weekdays input[type=checkbox]:checked");
    var weekdays = [];
    for (var i = 0; i < boxes.length; i++) {
      var day = Number(boxes[i].value);
      if (day >= 1 && day <= 7) { weekdays.push(day); }
    }
    if (weekdays.length === 0) { return { error: "请至少勾选一个星期" }; }
    weekdays.sort(function (a, b) { return a - b; });
    base.weekly = { time: weeklyTime, time_zone: zone, weekdays: weekdays };
    return base;
  }
  if (kind === "cron") {
    var expression = val("schedule-form-cron");
    if (!expression) { return { error: "请填写 Cron 表达式" }; }
    base.cron = { expression: expression, time_zone: zone };
    return base;
  }
  return { error: "未知规则类型: " + kind };
}

function submitCreateForm() {
  var status = document.getElementById("schedule-form-status");
  var saveBtn = document.getElementById("schedule-form-save");
  var request = buildCreateRequest();
  if (request.error) {
    if (status) { status.textContent = request.error; }
    return;
  }
  if (saveBtn) { saveBtn.disabled = true; }
  if (status) { status.textContent = "创建中…"; }
  apiFetch("/api/runtime/schedule/create", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(request),
  })
    .then(function (res) {
      return res.json().catch(function () { return null; }).then(function (payload) {
        return { ok: res.ok, payload: payload };
      });
    })
    .then(function (result) {
      if (saveBtn) { saveBtn.disabled = false; }
      if (result.ok) {
        showToast("任务已创建（来源：人工）");
        closeCreateForm();
        loadSchedule(true);
        return;
      }
      // 后端 400 携带 invalid_selector/not_future 等服务层错误码。
      var message = result.payload && result.payload.error ? result.payload.error : "创建失败";
      if (status) { status.textContent = message; }
    })
    .catch(function (error) {
      if (saveBtn) { saveBtn.disabled = false; }
      if (status) {
        status.textContent = "创建失败: " + (error && error.message ? error.message : String(error));
      }
    });
}
