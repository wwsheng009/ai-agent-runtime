// 自动化任务（Schedule）页签:跨会话任务目录 + 行内删除确认(P4) + 人工创建(P7)。
// aicli micro web client 前端模块(无构建步骤,由 app.js/ui.js 聚合)。
// 数据来自 /web/api/schedule/*(与 E1 frontend、TUI /schedule 同一后端);
// 展示口径:后端 UTC,本地展示;回执不代表模型执行完成。
// 人工创建调 POST /web/api/schedule/create(origin=manual):选择器校验在后端服务层,
// 与模型工具同一条管线;本端只做轻量的必填/形状预检。

import { apiFetch, esc, showToast } from "./util.js";
import { getCurrentSessionID, getSessionSummaries } from "./sessions.js";

var currentSessionId = "";
var loaded = false;
var loading = false;
// Full catalog snapshot + currently displayed (filtered) rows.
var allTasks = [];
var taskCache = [];
var activeFilter = "all";
var activeTargetKind = "all";
var activeSession = "all";
var activeKind = "all";
var activeDate = "";
var searchQuery = "";
var expandedPromptIds = {};
var lastFormFocus = null;
// null = create mode; non-null = editing this task.
var editingTask = null;
var taskActionPending = "";
var expandedHistoryId = "";
var confirmDeleteId = "";
var confirmDeleteTimer = null;
var historyByTask = {};
var historyLoading = {};
var historyErrors = {};

var SCHEDULE_API_BASE = "/web/api/schedule";

// 从各类错误体提取可行动明细。兼容：
//   - runtime API 扁平体 {error:"...", code:"..."}
//   - 本地 Web 通用体 {status:"rejected", reason:"..."}
//   - 嵌套体 {error:{code,message}}
// 响应体不是 JSON 时（例如 nginx/404 纯文本）给出 HTTP 状态，绝不静默成空串。
function scheduleErrorMessage(payload, status) {
  if (payload && typeof payload === "object") {
    if (typeof payload.error === "string" && payload.error.trim()) {
      return payload.error.trim();
    }
    if (payload.error && typeof payload.error === "object") {
      var nestedMessage = payload.error.message || payload.error.error;
      var nestedCode = payload.error.code || payload.code;
      if (nestedMessage) {
        return (nestedCode ? String(nestedCode) + ": " : "") + String(nestedMessage);
      }
    }
    if (typeof payload.reason === "string" && payload.reason.trim()) {
      return payload.reason.trim();
    }
    if (typeof payload.message === "string" && payload.message.trim()) {
      return (payload.code ? String(payload.code) + ": " : "") + payload.message.trim();
    }
    if (typeof payload.code === "string" && payload.code.trim()) {
      return payload.code.trim();
    }
  }
  return status ? "HTTP " + status : "未知错误";
}

function scheduleJSONResult(res) {
  return res.json()
    .catch(function () { return null; })
    .then(function (payload) {
      return { ok: res.ok, status: res.status, payload: payload };
    });
}

function scheduleStatusLabel(status) {
  if (status === "paused") { return "已暂停"; }
  if (status === "active") { return "已开启"; }
  return "未运行";
}

// A headless/workspace task has no bound SessionID, but the creator session is
// still allowed to manage (edit/pause/resume/delete) it.
function taskSessionId(task) {
  if (!task) { return ""; }
  return String(task.sessionId || task.createdBySessionId || effectiveScheduleSessionID() || "").trim();
}

// sessions.js 是当前会话身份的唯一来源；setScheduleSession 是兼容旧调用的
// 显式注入（测试/嵌入）。两者都为空时返回 ""，不猜测。
function effectiveScheduleSessionID() {
  var live = "";
  try { live = String(getCurrentSessionID() || "").trim(); } catch (_) { /* keep fallback */ }
  return live || String(currentSessionId || "").trim();
}

function taskRecordExpectation(task) {
  return {
    id: task.id,
    kind: task.kind,
    title: task.title,
    prompt: task.prompt,
    goal: task.goal,
    plan: task.plan,
    scheduledAt: task.scheduledAt,
    afterSeconds: task.afterSeconds,
    everySeconds: task.everySeconds,
    time: task.time,
    timeZone: task.timeZone,
    weekdays: task.weekdays,
    expression: task.expression,
  };
}

function findCachedTask(id) {
  for (var a = 0; a < allTasks.length; a++) {
    if (allTasks[a].id === id) { return allTasks[a]; }
  }
  for (var i = 0; i < taskCache.length; i++) {
    if (taskCache[i].id === id) { return taskCache[i]; }
  }
  return null;
}

// 当前会话 id 由 sessions.js 写入(与 chat/skills 同一约定)。
export function setScheduleSession(sessionId) {
  currentSessionId = sessionId || "";
}

// 当前筛选 + 搜索后的可见任务。展示与操作始终分离：taskCache 是视图，
// allTasks 是操作的事实源，切换筛选不会让行内按钮失效。
function filterTasks(tasks) {
  var query = searchQuery.trim().toLowerCase();
  var result = [];
  for (var i = 0; i < (tasks || []).length; i++) {
    var task = tasks[i];
    if (activeFilter !== "all" && task.status !== activeFilter) { continue; }
    var targetKind = task.targetKind || "session";
    if (activeTargetKind !== "all" && targetKind !== activeTargetKind) { continue; }
    if (activeKind !== "all" && task.kind !== activeKind) { continue; }
    if (!matchesSessionFilter(task)) { continue; }
    if (activeDate && !taskMatchesDate(task, activeDate)) { continue; }
    if (query) {
      var haystack = [
        task.title, task.prompt, task.goal, (task.plan || []).join(" "), task.id,
        task.sessionId, task.createdBySessionId, task.targetWorkspace, task.targetProfile,
        task.time, formatClock(task.time), weekdayLabel(task.weekdays),
      ].concat(
        dateSearchFragments(task.scheduledAt),
        dateSearchFragments(task.lastDelivery && task.lastDelivery.deliveredAt),
      ).join(" ").toLowerCase();
      if (haystack.indexOf(query) === -1) { continue; }
    }
    result.push(task);
  }
  return result;
}

function matchesSessionFilter(task) {
  if (activeSession === "all") { return true; }
  if (activeSession === "__unbound__") {
    return !String(task.sessionId || "").trim();
  }
  if (activeSession === "__current__") {
    var current = effectiveScheduleSessionID();
    if (!current) { return false; }
    return task.sessionId === current || task.createdBySessionId === current;
  }
  return task.sessionId === activeSession || task.createdBySessionId === activeSession;
}

function localDateKey(value) {
  if (!value) { return ""; }
  var parsed = new Date(value);
  if (isNaN(parsed.getTime())) { return ""; }
  var pad = function (n) { return String(n).padStart(2, "0"); };
  return parsed.getFullYear() + "-" + pad(parsed.getMonth() + 1) + "-" + pad(parsed.getDate());
}

function dateSearchFragments(value) {
  if (!value) { return []; }
  var parsed = new Date(value);
  if (isNaN(parsed.getTime())) { return [String(value)]; }
  var pad = function (n) { return String(n).padStart(2, "0"); };
  var dateKey = localDateKey(value);
  var time = pad(parsed.getHours()) + ":" + pad(parsed.getMinutes()) +
    ":" + pad(parsed.getSeconds());
  var fragments = [
    String(value), dateKey, dateKey.replace(/-/g, "/"),
    dateKey + " " + time, dateKey + " " + time.slice(0, 5),
    parsed.toLocaleString(), parsed.toLocaleDateString(), parsed.toLocaleTimeString(),
  ];
  return fragments;
}

function taskMatchesDate(task, dateKey) {
  if (!dateKey) { return true; }
  if (localDateKey(task.scheduledAt) === dateKey) { return true; }
  return !!(task.lastDelivery && localDateKey(task.lastDelivery.deliveredAt) === dateKey);
}

// 会话筛选的选项来自任务本身：目标会话 + 创建者会话。创建者会话可能就是
// 子代理/子会话（headless 任务没有目标会话，只保留创建者用于归属与过滤）。
function rebuildSessionFilterOptions() {
  var select = document.getElementById("schedule-filter-session");
  if (!select) { return; }
  var roles = {};
  var order = [];
  var add = function (id, role) {
    id = String(id || "").trim();
    if (!id) { return; }
    if (!roles[id]) {
      roles[id] = { target: false, creator: false };
      order.push(id);
    }
    roles[id][role] = true;
  };
  var hasUnbound = false;
  for (var i = 0; i < allTasks.length; i++) {
    var task = allTasks[i];
    var targetKind = task.targetKind || "session";
    add(task.sessionId, "target");
    add(task.createdBySessionId, "creator");
    if (!String(task.sessionId || "").trim()) {
      hasUnbound = true;
    }
  }
  order.sort();
  var present = {};
  var currentSession = effectiveScheduleSessionID();
  var options = '<option value="all">全部会话（目标/创建者）</option>';
  if (currentSession) {
    options += '<option value="__current__">当前会话</option>';
  }
  if (hasUnbound) {
    options += '<option value="__unbound__">未绑定目标会话</option>';
  }
  for (var s = 0; s < order.length; s++) {
    var id = order[s];
    var role = roles[id];
    var roleLabel = role.target && role.creator
      ? "目标/创建者"
      : role.target ? "目标" : "创建者会话";
    var shortId = id.length > 30 ? id.slice(0, 30) + "…" : id;
    options += '<option value="' + esc(id) + '">' + esc(shortId) + " · " +
      esc(roleLabel) + "</option>";
    present[id] = true;
  }
  select.innerHTML = options;
  var allowed = activeSession === "all" ||
    (activeSession === "__current__" && !!effectiveScheduleSessionID()) ||
    (activeSession === "__unbound__" && hasUnbound) ||
    !!present[activeSession];
  if (!allowed) { activeSession = "all"; }
  select.value = activeSession;
}

function hasActiveScheduleFilters() {
  return activeFilter !== "all" || activeTargetKind !== "all" ||
    activeSession !== "all" || activeKind !== "all" || !!activeDate || !!searchQuery.trim();
}

function clearScheduleFilters() {
  activeFilter = "all";
  activeTargetKind = "all";
  activeSession = "all";
  activeKind = "all";
  activeDate = "";
  searchQuery = "";
  var search = document.getElementById("schedule-search");
  if (search) { search.value = ""; }
  var date = document.getElementById("schedule-filter-date");
  if (date) { date.value = ""; }
  var selects = ["schedule-filter-target-kind", "schedule-filter-session", "schedule-filter-kind"];
  for (var i = 0; i < selects.length; i++) {
    var select = document.getElementById(selects[i]);
    if (select) { select.value = "all"; }
  }
  applyScheduleView();
}

function updateFilterCounts() {
  var counts = { all: allTasks.length, active: 0, paused: 0, inactive: 0 };
  for (var i = 0; i < allTasks.length; i++) {
    var status = allTasks[i].status;
    if (status === "active") { counts.active++; }
    else if (status === "paused") { counts.paused++; }
    else { counts.inactive++; }
  }
  var keys = ["all", "active", "paused", "inactive"];
  for (var k = 0; k < keys.length; k++) {
    var key = keys[k];
    var countEl = document.getElementById("schedule-filter-" + key + "-count");
    if (countEl) { countEl.textContent = String(counts[key]); }
    var button = document.getElementById("schedule-filter-" + key);
    if (button) {
      button.classList.toggle("is-active", key === activeFilter);
      button.setAttribute("aria-pressed", key === activeFilter ? "true" : "false");
    }
  }
}

function renderSummary() {
  var el = document.getElementById("schedule-summary");
  if (!el) { return; }
  if (allTasks.length === 0) {
    el.textContent = "";
    return;
  }
  var counts = { active: 0, paused: 0, inactive: 0 };
  for (var i = 0; i < allTasks.length; i++) {
    var status = allTasks[i].status;
    if (status === "active") { counts.active++; }
    else if (status === "paused") { counts.paused++; }
    else { counts.inactive++; }
  }
  var text = "共 " + allTasks.length + " 个任务 · 已开启 " + counts.active +
    " · 已暂停 " + counts.paused + " · 未运行 " + counts.inactive;
  if (hasActiveScheduleFilters()) {
    text += " · 当前显示 " + taskCache.length + " 个";
  }
  el.textContent = text;
}

function renderEmptyState(hasQuery) {
  var listEl = document.getElementById("schedule-list");
  if (!listEl) { return; }
  taskCache = [];
  var message = hasQuery
    ? "没有匹配的任务。可以换一个关键词，或清除筛选后查看全部任务。"
    : "还没有自动化任务。创建一条定时提醒，或让模型在会话中调用 schedule_create。";
  var action = hasQuery
    ? '<button id="schedule-empty-clear" type="button" class="schedule-empty-action">清除筛选</button>'
    : '<button id="schedule-empty-new" type="button" class="schedule-empty-action primary-btn">＋ 新建任务</button>';
  listEl.innerHTML = '<div class="schedule-empty"><p>' + esc(message) + '</p>' + action + '</div>';
  bindListExtraActions();
  renderSummary();
}

function renderLoadError(message) {
  var listEl = document.getElementById("schedule-list");
  if (!listEl) { return; }
  allTasks = [];
  taskCache = [];
  listEl.innerHTML = '<div class="schedule-empty schedule-empty-error">' +
    '<p>加载失败: ' + esc(message) + '</p>' +
    '<button id="schedule-retry" type="button" class="schedule-empty-action">重试</button></div>';
  bindListExtraActions();
  renderSummary();
  updateFilterCounts();
}

function bindListExtraActions() {
  var retry = document.getElementById("schedule-retry");
  if (retry) { retry.addEventListener("click", function () { loadSchedule(true); }); }
  var clear = document.getElementById("schedule-empty-clear");
  if (clear) {
    clear.addEventListener("click", clearScheduleFilters);
  }
  var create = document.getElementById("schedule-empty-new");
  if (create) { create.addEventListener("click", function () { openCreateForm(); }); }
}

// 静态 HTML 端点(查询串未进入 innerHTML,列表体经 esc 转义)。
function renderTasks(tasks) {
  var listEl = document.getElementById("schedule-list");
  if (!listEl) { return; }
  var ordered = (tasks || []).slice().sort(function (left, right) {
    var rank = function (status) {
      if (status === "active") { return 0; }
      if (status === "paused") { return 1; }
      return 2;
    };
    var byRank = rank(left.status) - rank(right.status);
    if (byRank !== 0) { return byRank; }
    var leftAt = Date.parse(left.scheduledAt || "") || 0;
    var rightAt = Date.parse(right.scheduledAt || "") || 0;
    if (leftAt !== rightAt) { return leftAt - rightAt; }
    return String(left.id).localeCompare(String(right.id));
  });
  taskCache = ordered;
  if (ordered.length === 0) {
    renderEmptyState(allTasks.length > 0);
    return;
  }
  var html = "";
  for (var i = 0; i < ordered.length; i++) {
    var task = ordered[i];
    var live = task.status === "active";
    var paused = task.status === "paused";
    var itemClass = "schedule-item" + (live ? " is-active" : paused ? " is-paused" : " is-inactive");
    var badgeClass = live
      ? "schedule-badge schedule-badge-live"
      : paused
        ? "schedule-badge schedule-badge-paused"
        : "schedule-badge";
    var badge = '<span class="' + badgeClass + '">' + esc(scheduleStatusLabel(task.status)) + '</span>';
    var actions = '<div class="schedule-actions">';
    if (live) {
      actions += taskActionButton(task, "pause", "暂停");
    } else if (paused) {
      actions += taskActionButton(task, "resume", "恢复");
    } else {
      actions += taskActionButton(task, "reschedule", "再次排程");
    }
    if (live || paused) {
      actions += taskActionButton(task, "edit", "编辑");
    }
    actions += taskActionButton(task, "history", expandedHistoryId === task.id ? "收起历史" : "运行历史");
    if (confirmDeleteId === task.id) {
      actions += taskActionButton(task, "delete-confirm", "确认删除");
      actions += taskActionButton(task, "delete-cancel", "取消");
    } else {
      actions += taskActionButton(task, "delete", "删除");
    }
    actions += "</div>";
    var target = targetLabel(task);
    var next = "";
    var nextLabel = "下次运行";
    if ((live || paused) && task.scheduledAt) {
      if (paused) { nextLabel = "原定运行"; }
      next = esc(formatLocal(task.scheduledAt)) +
        ' <span class="schedule-dim">(' + esc(relative(task.scheduledAt)) + ')</span>';
    } else if (task.lastDelivery && task.lastDelivery.deliveredAt) {
      nextLabel = "最近投递";
      next = esc(formatLocal(task.lastDelivery.deliveredAt));
    } else {
      nextLabel = "状态";
      next = '<span class="schedule-dim">已结束</span>';
    }
    var goal = task.goal
      ? '<div class="schedule-detail"><span class="schedule-detail-label">作业目标</span><div>' + esc(task.goal) + '</div></div>'
      : "";
    var plan = "";
    if (task.plan && task.plan.length > 0) {
      var steps = "";
      for (var s = 0; s < task.plan.length; s++) {
        steps += "<li>" + esc(task.plan[s]) + "</li>";
      }
      plan = '<div class="schedule-detail"><span class="schedule-detail-label">执行步骤</span>' +
        '<ol class="schedule-plan">' + steps + "</ol></div>";
    }
    var promptText = String(task.prompt || "");
    var canExpand = promptText.length > 90 || promptText.indexOf("\n") !== -1;
    var expanded = !!expandedPromptIds[task.id];
    var promptId = "schedule-prompt-" + i;
    var prompt = '<div class="schedule-prompt' + (expanded ? " is-expanded" : "") +
      '" id="' + promptId + '">' + esc(promptText) + "</div>";
    var expand = canExpand
      ? '<button type="button" class="schedule-expand" data-id="' + esc(task.id) +
        '" data-action="toggle-prompt" aria-expanded="' + (expanded ? "true" : "false") +
        '" aria-controls="' + promptId + '">' + (expanded ? "收起内容" : "展开内容") + "</button>"
      : "";
    html += '<li class="' + itemClass + '">' +
      '<div class="schedule-head">' + badge +
      '<span class="schedule-title">' + esc(task.title) + "</span>" + actions + "</div>" +
      '<div class="schedule-facts">' +
      scheduleFact("规则", esc(scheduleRuleLabel(task))) +
      scheduleFact(nextLabel, next) +
      scheduleFact("执行方式", esc(target)) +
      "</div>" +
      goal + plan + prompt + expand +
      renderTaskHistory(task) +
      "</li>";
  }
  listEl.innerHTML = '<ul class="schedule-list-body">' + html + "</ul>";
  bindTaskActionButtons(listEl);
  renderSummary();
}

function scheduleFact(label, value) {
  return '<div class="schedule-fact"><span class="schedule-fact-label">' + esc(label) +
    '</span><span class="schedule-fact-value">' + value + "</span></div>";
}

function scheduleRuleLabel(task) {
  switch (task.kind) {
    case "after":
      return "一次性 · " + scheduleFrequency(task);
    case "at":
      return "一次性 · 指定时间";
    case "every":
      return "固定间隔 · " + scheduleFrequency(task);
    default:
      return scheduleFrequency(task) || kindLabel(task.kind);
  }
}

function targetLabel(task) {
  var kind = task.targetKind || "session";
  if (kind === "session") {
    var owner = taskSessionId(task);
    var shortOwner = owner.length > 22 ? owner.slice(0, 22) + "…" : owner;
    return "原会话" + (owner ? "（" + shortOwner + "）" : "");
  }
  var text = kind === "workspace" ? "工作区" : "无人值守";
  if (task.targetWorkspace) { text += " · " + task.targetWorkspace; }
  if (task.targetProfile) { text += " · Profile " + task.targetProfile; }
  return text;
}

function applyScheduleView() {
  rebuildSessionFilterOptions();
  updateFilterCounts();
  var reset = document.getElementById("schedule-filter-reset");
  if (reset) { reset.hidden = !hasActiveScheduleFilters(); }
  renderTasks(filterTasks(allTasks));
}

function taskActionButton(task, action, label) {
  var pending = taskActionPending.indexOf(task.id + ":") === 0;
  var busyLabel = action === "pause" ? "暂停中…"
    : action === "resume" ? "恢复中…"
      : action === "delete" ? "删除中…" : label;
  var classes = "schedule-action";
  if (action === "pause" || action === "resume" || action === "reschedule") {
    classes += " schedule-action-primary";
  }
  if (action === "delete" || action === "delete-confirm") {
    classes += " schedule-action-danger";
  }
  var ariaLabel = label + "：" + (task.title || task.id);
  var ariaExpanded = action === "history"
    ? ' aria-expanded="' + (expandedHistoryId === task.id ? "true" : "false") + '"'
    : "";
  return '<button type="button" class="' + classes + '" data-id="' + esc(task.id) +
    '" data-action="' + esc(action) + '" aria-label="' + esc(ariaLabel) + '"' + ariaExpanded +
    (pending ? " disabled" : "") + ">" + esc(pending ? busyLabel : label) + "</button>";
}

function renderTaskHistory(task) {
  if (expandedHistoryId !== task.id) { return ""; }
  var html = '<div class="schedule-history">';
  if (historyLoading[task.id]) {
    html += '<div class="schedule-history-empty">正在加载运行历史…</div>';
  } else if (historyErrors[task.id]) {
    html += '<div class="schedule-history-error">加载失败: ' +
      esc(historyErrors[task.id]) + '</div>';
  } else {
    var runs = historyByTask[task.id] || [];
    if (runs.length === 0) {
      html += '<div class="schedule-history-empty">还没有运行记录（原会话提醒只产生投递回执，不产生执行运行记录）</div>';
    } else {
      for (var i = 0; i < runs.length; i++) {
        var run = runs[i];
        var failed = run.status === "failed";
        html += '<div class="schedule-history-run">' +
          '<div class="schedule-history-head">' +
          '<span class="' + (failed ? "schedule-history-badge failed" : "schedule-history-badge") + '">' +
          (failed ? "失败" : "成功") + '</span>' +
          '<span class="schedule-dim">完成于 ' +
          esc(formatLocal(run.finishedAt || run.startedAt, true)) + '（本地）</span>' +
          (run.sessionId ? '<span class="schedule-dim">会话: ' + esc(run.sessionId) + '</span>' : '') +
          '</div>';
        if (run.output) {
          html += '<pre class="schedule-run-output">' + esc(run.output) + '</pre>';
        }
        if (run.error) {
          html += '<div class="schedule-history-error">错误: ' + esc(run.error) + '</div>';
        }
        html += '</div>';
      }
    }
  }
  html += '</div>';
  return html;
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
  var zone = task.timeZone ? "（" + task.timeZone + "）" : "";
  switch (task.kind) {
    case "after": return formatDuration(task.afterSeconds) + "后触发";
    case "every": return "每 " + formatDuration(task.everySeconds);
    case "at": return "指定时间";
    case "daily": return "每天 " + formatClock(task.time) + zone;
    case "weekly": return "每周" + weekdayLabel(task.weekdays) + " " + formatClock(task.time) + zone;
    case "cron": return "Cron " + (task.expression || "") + zone;
    default: return "";
  }
}

function formatDuration(seconds) {
  var value = Number(seconds);
  if (!isFinite(value) || value <= 0) { return "—"; }
  if (value < 60) { return value + " 秒"; }
  var minutes = Math.round(value / 60);
  if (minutes < 60) { return minutes + " 分钟"; }
  var hours = Math.round(minutes / 60);
  if (hours < 24) { return hours + " 小时"; }
  return Math.round(hours / 24) + " 天";
}

// 后端 time 形如 "09:00:00.000"；展示时去掉尾随的零秒，保留 HH:MM。
function formatClock(value) {
  var raw = String(value || "").trim();
  if (!raw) { return "—"; }
  var parts = raw.split(":");
  if (parts.length < 2) { return raw; }
  var hour = parts[0];
  var minute = parts[1];
  var second = (parts[2] || "0").replace(/\..*$/, "");
  if (Number(second) > 0) { return hour + ":" + minute + ":" + second; }
  return hour + ":" + minute;
}

function normalizeClockInput(value) {
  var raw = String(value || "").trim();
  if (!raw) { return ""; }
  var parts = raw.split(":");
  if (parts.length < 2) { return raw; }
  var second = (parts[2] || "0").replace(/\..*$/, "");
  return parts[0] + ":" + parts[1] + ":" + second;
}

function weekdayLabel(weekdays) {
  var names = ["一", "二", "三", "四", "五", "六", "日"];
  var list = (weekdays || []).map(function (day) {
    var index = Number(day) - 1;
    return index >= 0 && index < names.length ? names[index] : String(day);
  });
  return list.length > 0 ? list.join("、") : "（未选择）";
}

function padLocalPart(value) {
  return String(value).padStart(2, "0");
}

// 统一使用浏览器本地时区的 24 小时制：YYYY-MM-DD HH:mm（history 传 true 加秒）。
// 任务目录/回执中的时间都是 UTC 时间戳，这里只做展示层本地化，不改变存储。
function formatLocal(value, withSeconds) {
  var parsed = new Date(value);
  if (isNaN(parsed.getTime())) { return value == null ? "" : String(value); }
  var text = parsed.getFullYear() + "-" + padLocalPart(parsed.getMonth() + 1) + "-" +
    padLocalPart(parsed.getDate()) + " " + padLocalPart(parsed.getHours()) + ":" +
    padLocalPart(parsed.getMinutes());
  if (withSeconds) { text += ":" + padLocalPart(parsed.getSeconds()); }
  return text;
}

function localTimeZoneName() {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "本地";
  } catch (_) {
    return "本地";
  }
}

function updateLocalTimeZoneLabel() {
  var el = document.getElementById("schedule-local-zone-label");
  if (!el) { return; }
  el.textContent = "时间按本地时区（" + localTimeZoneName() +
    "）显示；规则时间保留配置时区。";
}

function relative(value) {
  var parsed = new Date(value).getTime();
  if (isNaN(parsed)) { return ""; }
  var delta = Math.round((parsed - Date.now()) / 1000);
  var future = delta >= 0;
  var abs = Math.abs(delta);
  if (abs < 10) { return "刚刚"; }
  var text;
  if (abs < 60) {
    text = abs + " 秒";
  } else if (abs < 3600) {
    text = Math.floor(abs / 60) + " 分钟";
  } else if (abs < 86400) {
    text = Math.floor(abs / 3600) + " 小时";
  } else {
    text = Math.floor(abs / 86400) + " 天";
  }
  return text + (future ? "后" : "前");
}

function bindTaskActionButtons(listEl) {
  var buttons = listEl.querySelectorAll(".schedule-action");
  for (var i = 0; i < buttons.length; i++) {
    buttons[i].addEventListener("click", function (event) {
      var button = event.currentTarget;
      var id = button.getAttribute("data-id");
      var action = button.getAttribute("data-action");
      var task = findCachedTask(id);
      if (!task) { return; }
      if (action === "toggle-prompt") {
        expandedPromptIds[task.id] = !expandedPromptIds[task.id];
        applyScheduleView();
      } else if (action === "delete") {
        startDeleteConfirm(task);
      } else if (action === "delete-confirm") {
        cancelDeleteConfirm();
        deleteTask(taskSessionId(task), task.id);
      } else if (action === "delete-cancel") {
        cancelDeleteConfirm();
        applyScheduleView();
      } else if (action === "edit") {
        openEditTask(task);
      } else if (action === "reschedule") {
        openCreateForm(task, true);
      } else if (action === "history") {
        toggleTaskHistory(task);
      } else if (action === "pause" || action === "resume") {
        setTaskStatus(task, action);
      }
    });
  }
}

function startDeleteConfirm(task) {
  cancelDeleteConfirm();
  confirmDeleteId = task.id;
  confirmDeleteTimer = setTimeout(function () {
    confirmDeleteId = "";
    confirmDeleteTimer = null;
    applyScheduleView();
  }, 6000);
  applyScheduleView();
}

function cancelDeleteConfirm() {
  if (confirmDeleteTimer) {
    clearTimeout(confirmDeleteTimer);
    confirmDeleteTimer = null;
  }
  confirmDeleteId = "";
}

function toggleTaskHistory(task) {
  if (expandedHistoryId === task.id) {
    expandedHistoryId = "";
    applyScheduleView();
    return;
  }
  expandedHistoryId = task.id;
  if (historyByTask[task.id] || historyLoading[task.id]) {
    applyScheduleView();
    return;
  }
  historyLoading[task.id] = true;
  delete historyErrors[task.id];
  applyScheduleView();
  var query = "?id=" + encodeURIComponent(task.id) + "&limit=20";
  var owner = taskSessionId(task);
  if (owner) { query += "&session_id=" + encodeURIComponent(owner); }
  apiFetch(SCHEDULE_API_BASE + "/runs" + query)
    .then(scheduleJSONResult)
    .then(function (result) {
      delete historyLoading[task.id];
      if (!result.ok) {
        historyErrors[task.id] = scheduleErrorMessage(result.payload, result.status);
      } else {
        historyByTask[task.id] = result.payload && result.payload.runs ? result.payload.runs : [];
      }
      applyScheduleView();
    })
    .catch(function (error) {
      delete historyLoading[task.id];
      historyErrors[task.id] = error && error.message ? error.message : String(error);
      applyScheduleView();
    });
}

function setTaskStatus(task, action) {
  var owner = taskSessionId(task);
  if (!owner) {
    showToast("操作失败: 无法确定任务所属会话", "err", 5000);
    return;
  }
  taskActionPending = task.id + ":" + action;
  applyScheduleView();
  apiFetch(SCHEDULE_API_BASE + "/" + action, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ session_id: owner, id: task.id }),
  })
    .then(scheduleJSONResult)
    .then(function (result) {
      taskActionPending = "";
      if (!result.ok || (result.payload && result.payload.updated === false && result.payload.code)) {
        showToast("操作失败: " + scheduleErrorMessage(result.payload, result.status), "err", 6000);
      } else {
        showToast(action === "pause" ? "任务已暂停" : "任务已恢复");
      }
      loadSchedule(true);
    })
    .catch(function (error) {
      taskActionPending = "";
      showToast("操作失败: " + (error && error.message ? error.message : String(error)), "err", 6000);
      loadSchedule(true);
    });
}

function deleteTask(sessionId, id) {
  cancelDeleteConfirm();
  taskActionPending = id + ":delete";
  applyScheduleView();
  apiFetch(SCHEDULE_API_BASE + "/delete", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ session_id: sessionId, id: id }),
  })
    .then(scheduleJSONResult)
    .then(function (result) {
      taskActionPending = "";
      if (!result.ok || (result.payload && result.payload.deleted === false)) {
        showToast("删除失败: " + scheduleErrorMessage(result.payload, result.status), "err", 5000);
      } else {
        showToast("任务已删除");
      }
      loadSchedule(true);
    })
    .catch(function (error) {
      taskActionPending = "";
      showToast("删除失败: " + (error && error.message ? error.message : String(error)), "err", 5000);
      loadSchedule(true);
    });
}

// 拉取目录;force=true 忽略已加载标记(删除/刷新按钮用)。
export function loadSchedule(force) {
  var listEl = document.getElementById("schedule-list");
  if (!listEl || loading) { return; }
  if (loaded && !force) { return; }
  loading = true;
  setScheduleRefreshBusy(true);
  listEl.setAttribute("aria-busy", "true");
  listEl.innerHTML = '<div class="schedule-empty schedule-loading">正在加载任务…</div>';
  apiFetch(SCHEDULE_API_BASE + "/catalog")
    .then(scheduleJSONResult)
    .then(function (result) {
      loading = false;
      loaded = true;
      setScheduleRefreshBusy(false);
      listEl.setAttribute("aria-busy", "false");
      if (!result.ok) {
        renderLoadError(scheduleErrorMessage(result.payload, result.status));
        return;
      }
      allTasks = result.payload && Array.isArray(result.payload.tasks) ? result.payload.tasks : [];
      applyScheduleView();
    })
    .catch(function (error) {
      loading = false;
      loaded = true;
      setScheduleRefreshBusy(false);
      listEl.setAttribute("aria-busy", "false");
      renderLoadError(error && error.message ? error.message : String(error));
    });
}

function setScheduleRefreshBusy(busy) {
  var button = document.getElementById("schedule-refresh-btn");
  if (!button) { return; }
  if (busy) {
    if (!button.getAttribute("data-idle-label")) {
      button.setAttribute("data-idle-label", button.textContent || "刷新");
    }
    button.disabled = true;
    button.textContent = "刷新中…";
    return;
  }
  button.disabled = false;
  var label = button.getAttribute("data-idle-label");
  if (label) { button.textContent = label; }
}

export function initSchedule() {
  var listEl = document.getElementById("schedule-list");
  if (!listEl) { return; }
  updateLocalTimeZoneLabel();
  var refreshBtn = document.getElementById("schedule-refresh-btn");
  if (refreshBtn) {
    refreshBtn.addEventListener("click", function () { loadSchedule(true); });
  }
  var filters = document.getElementById("schedule-filters");
  if (filters) {
    var filterButtons = filters.querySelectorAll(".schedule-filter");
    for (var i = 0; i < filterButtons.length; i++) {
      filterButtons[i].addEventListener("click", function (event) {
        activeFilter = event.currentTarget.getAttribute("data-filter") || "all";
        applyScheduleView();
      });
    }
  }
  var search = document.getElementById("schedule-search");
  if (search) {
    search.addEventListener("input", function () {
      searchQuery = search.value || "";
      applyScheduleView();
    });
  }
  var targetKindFilter = document.getElementById("schedule-filter-target-kind");
  if (targetKindFilter) {
    targetKindFilter.addEventListener("change", function () {
      activeTargetKind = targetKindFilter.value || "all";
      applyScheduleView();
    });
  }
  var sessionFilter = document.getElementById("schedule-filter-session");
  if (sessionFilter) {
    sessionFilter.addEventListener("change", function () {
      activeSession = sessionFilter.value || "all";
      applyScheduleView();
    });
  }
  var kindFilter = document.getElementById("schedule-filter-kind");
  if (kindFilter) {
    kindFilter.addEventListener("change", function () {
      activeKind = kindFilter.value || "all";
      applyScheduleView();
    });
  }
  var dateFilter = document.getElementById("schedule-filter-date");
  if (dateFilter) {
    var applyDateFilter = function () {
      activeDate = dateFilter.value || "";
      applyScheduleView();
    };
    dateFilter.addEventListener("change", applyDateFilter);
    dateFilter.addEventListener("input", applyDateFilter);
  }
  var resetFilter = document.getElementById("schedule-filter-reset");
  if (resetFilter) {
    resetFilter.addEventListener("click", clearScheduleFilters);
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
  var closeBtn = document.getElementById("schedule-form-close");
  if (closeBtn) {
    closeBtn.addEventListener("click", function () { closeCreateForm(); });
  }
  // Esc 关闭（与技能详情弹层同一约定）；弹层内部按键不冒泡。
  overlay.addEventListener("keydown", function (event) {
    if (event.key === "Escape") {
      event.stopPropagation();
      closeCreateForm();
      return;
    }
    trapScheduleFormFocus(event);
  });
  // 点击遮罩空白处关闭（与技能详情弹层行为一致）。
  overlay.addEventListener("click", function (event) {
    if (event.target === overlay) { closeCreateForm(); }
  });
  var kindSelect = document.getElementById("schedule-form-kind");
  if (kindSelect) {
    kindSelect.addEventListener("change", function () {
      clearScheduleFormErrors();
      syncCreateFormFields(kindSelect.value);
      updateSchedulePreview();
    });
  }
  var targetKindSelect = document.getElementById("schedule-form-target-kind");
  if (targetKindSelect) {
    targetKindSelect.addEventListener("change", function () {
      clearScheduleFormErrors();
      syncScheduleTargetFields(targetKindSelect.value);
      updateSchedulePreview();
    });
  }
  form.addEventListener("input", function () {
    clearScheduleFieldError(document.activeElement);
    clearScheduleErrorSummary();
    updateSchedulePreview();
  });
  form.addEventListener("change", function () {
    clearScheduleFieldError(document.activeElement);
    clearScheduleErrorSummary();
    updateSchedulePreview();
  });
  form.addEventListener("submit", function (event) {
    event.preventDefault();
    submitCreateForm();
  });
}

function openCreateForm(seedTask, again) {
  var overlay = document.getElementById("schedule-form-overlay");
  var form = document.getElementById("schedule-form");
  var status = document.getElementById("schedule-form-status");
  if (!overlay || !form) { return; }
  if (document.activeElement) { lastFormFocus = document.activeElement; }
  form.reset();
  editingTask = null;
  if (status) { status.textContent = ""; }
  clearScheduleFormErrors();
  fillSessionSelect();
  fillZoneSelect();
  var targetKind = document.getElementById("schedule-form-target-kind");
  if (targetKind) {
    targetKind.disabled = false;
    targetKind.value = seedTask && seedTask.targetKind ? seedTask.targetKind : "session";
  }
  var kind = document.getElementById("schedule-form-kind");
  if (kind) {
    kind.disabled = false;
    if (seedTask) { kind.value = seedTask.kind; }
  }
  var session = document.getElementById("schedule-form-session");
  if (session) { session.disabled = false; }
  var amount = document.getElementById("schedule-form-amount");
  if (amount) { amount.disabled = false; }
  var modalTitle = document.getElementById("schedule-form-title-label");
  if (modalTitle) { modalTitle.textContent = again ? "再次排程（创建新任务）" : "新建自动化任务"; }
  var saveBtn = document.getElementById("schedule-form-save");
  if (saveBtn) { saveBtn.textContent = "创建任务"; }
  if (seedTask) {
    fillFormFromTask(seedTask, false);
    if (again) {
      var againTitle = document.getElementById("schedule-form-title");
      if (againTitle) { againTitle.value = seedTask.title; }
    }
  }
  syncScheduleTargetFields(targetKind ? targetKind.value : "session");
  syncCreateFormFields(kind ? kind.value : "after");
  updateSchedulePreview();
  overlay.classList.add("active");
  var first = document.getElementById("schedule-form-title");
  if (first) { first.focus(); }
}

function openEditTask(task) {
  var overlay = document.getElementById("schedule-form-overlay");
  var form = document.getElementById("schedule-form");
  var status = document.getElementById("schedule-form-status");
  if (!overlay || !form || !task) { return; }
  if (document.activeElement) { lastFormFocus = document.activeElement; }
  form.reset();
  editingTask = task;
  if (status) { status.textContent = ""; }
  clearScheduleFormErrors();
  fillSessionSelect();
  fillZoneSelect();
  fillFormFromTask(task, true);
  var targetKind = document.getElementById("schedule-form-target-kind");
  if (targetKind) {
    targetKind.value = task.targetKind || "session";
    targetKind.disabled = true;
  }
  syncScheduleTargetFields(task.targetKind || "session");
  var targetDescription = document.getElementById("schedule-target-description");
  if (targetDescription) {
    targetDescription.textContent = (targetDescription.textContent || "") +
      " 目标与执行步骤创建后不可修改。";
  }
  var lockedTargetFields = ["schedule-form-goal", "schedule-form-plan",
    "schedule-form-workspace", "schedule-form-profile"];
  for (var lockedIndex = 0; lockedIndex < lockedTargetFields.length; lockedIndex++) {
    var locked = document.getElementById(lockedTargetFields[lockedIndex]);
    if (locked) { locked.disabled = true; }
  }
  var modalTitle = document.getElementById("schedule-form-title-label");
  if (modalTitle) { modalTitle.textContent = "编辑自动化任务"; }
  var saveBtn = document.getElementById("schedule-form-save");
  if (saveBtn) { saveBtn.textContent = "保存"; }
  var session = document.getElementById("schedule-form-session");
  if (session) { session.disabled = true; }
  var kind = document.getElementById("schedule-form-kind");
  if (kind) {
    kind.value = task.kind;
    kind.disabled = true;
  }
  syncCreateFormFields(task.kind);
  if (task.kind === "after") {
    var amount = document.getElementById("schedule-form-amount");
    if (amount) { amount.disabled = true; }
    if (status) { status.textContent = "一次性延迟任务仅支持修改名称和内容；如需改时间请使用“再次排程”。"; }
  }
  updateSchedulePreview();
  overlay.classList.add("active");
  var first = document.getElementById("schedule-form-title");
  if (first) { first.focus(); }
}

function fillFormFromTask(task, editing) {
  var setValue = function (id, value) {
    var el = document.getElementById(id);
    if (el) { el.value = value == null ? "" : String(value); }
  };
  setValue("schedule-form-title", task.title);
  setValue("schedule-form-prompt", task.prompt);
  setValue("schedule-form-goal", task.goal || "");
  setValue("schedule-form-workspace", task.targetWorkspace || "");
  setValue("schedule-form-profile", task.targetProfile || "");
  setValue("schedule-form-plan", (task.plan || []).join("\n"));
  setValue("schedule-form-target-kind", task.targetKind || "session");
  setSessionValue(taskSessionId(task));
  setValue("schedule-form-kind", task.kind);
  if (task.kind === "after" || task.kind === "every") {
    setValue("schedule-form-amount", Math.max(1, Math.round(Number(task.kind === "after" ? task.afterSeconds : task.everySeconds || 60) / 60)));
  }
  if (task.kind === "at") {
    setValue("schedule-form-time", toDatetimeLocal(task.scheduledAt));
  }
  if (task.kind === "daily" || task.kind === "weekly" || task.kind === "cron") {
    setZoneValue(task.timeZone);
  }
  if (task.kind === "daily" || task.kind === "weekly") {
    setValue("schedule-form-clock", normalizeClockInput(task.time));
  }
  if (task.kind === "weekly") {
    var selected = task.weekdays || [];
    var boxes = document.querySelectorAll("#schedule-form-weekdays input[type=checkbox]");
    for (var i = 0; i < boxes.length; i++) {
      boxes[i].checked = selected.indexOf(Number(boxes[i].value)) !== -1;
    }
  }
  if (task.kind === "cron") {
    setValue("schedule-form-cron", task.expression || "");
  }
  syncScheduleTargetFields(task.targetKind || "session");
}

function setSessionValue(sessionId) {
  var select = document.getElementById("schedule-form-session");
  if (!select || !sessionId) { return; }
  var found = false;
  for (var i = 0; i < select.options.length; i++) {
    if (select.options[i].value === sessionId) { found = true; break; }
  }
  if (!found) {
    var option = document.createElement("option");
    option.value = sessionId;
    option.textContent = sessionId;
    select.appendChild(option);
  }
  select.value = sessionId;
}

function setZoneValue(zone) {
  var select = document.getElementById("schedule-form-zone");
  if (!select || !zone) { return; }
  var found = false;
  for (var i = 0; i < select.options.length; i++) {
    if (select.options[i].value === zone) { found = true; break; }
  }
  if (!found) {
    var option = document.createElement("option");
    option.value = zone;
    option.textContent = zone;
    select.appendChild(option);
  }
  select.value = zone;
}

function toDatetimeLocal(value) {
  var parsed = new Date(value);
  if (isNaN(parsed.getTime())) { return ""; }
  var pad = function (n) { return String(n).padStart(2, "0"); };
  return parsed.getFullYear() + "-" + pad(parsed.getMonth() + 1) + "-" + pad(parsed.getDate()) +
    "T" + pad(parsed.getHours()) + ":" + pad(parsed.getMinutes()) + ":" + pad(parsed.getSeconds());
}

// 目标会话下拉：会话摘要来自 sessions.js 的 loadSessions 缓存（无第二份请求），
// 默认选中当前会话；缓存未就绪（空列表）时注入当前会话占位项，保证「默认当前」成立。
function fillSessionSelect() {
  var select = document.getElementById("schedule-form-session");
  if (!select) { return; }
  var current = getCurrentSessionID();
  var summaries = getSessionSummaries();
  if (!summaries.length && current) {
    summaries = [{ id: current, title: "(当前会话)" }];
  }
  var options = "";
  var currentSeen = false;
  for (var i = 0; i < summaries.length; i++) {
    var s = summaries[i];
    if (s.id === current) { currentSeen = true; }
    var shortId = s.id.length > 22 ? s.id.slice(0, 22) + "…" : s.id;
    options += '<option value="' + esc(s.id) + '"' + (s.id === current ? " selected" : "") + ">" +
      esc(s.title) + "（" + esc(shortId) + "）</option>";
  }
  // 缓存里没有当前会话（异常态）：追加占位项而不是静默选第一个。
  if (!currentSeen && current) {
    options = '<option value="' + esc(current) + '" selected>(当前会话)（' + esc(current.slice(0, 22)) + "…）</option>" + options;
  }
  select.innerHTML = options;
}

// 时区下拉：Intl.supportedValuesOf('timeZone')（现代浏览器全量 IANA 清单），
// 老浏览器回退到常用清单；默认选中浏览器时区。
function fillZoneSelect() {
  var select = document.getElementById("schedule-form-zone");
  if (!select) { return; }
  var zones = [];
  try {
    if (typeof Intl.supportedValuesOf === "function") {
      zones = Intl.supportedValuesOf("timeZone") || [];
    }
  } catch (_) { /* 走回退清单 */ }
  if (!zones.length) {
    zones = ["UTC", "Asia/Shanghai", "Asia/Tokyo", "Asia/Singapore", "Asia/Hong_Kong",
      "Europe/London", "Europe/Berlin", "Europe/Paris", "America/New_York",
      "America/Chicago", "America/Los_Angeles", "Australia/Sydney"];
  }
  var local = "UTC";
  try { local = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"; } catch (_) { /* keep UTC */ }
  var options = "";
  var localSeen = false;
  for (var i = 0; i < zones.length; i++) {
    var z = zones[i];
    if (z === local) { localSeen = true; }
    options += '<option value="' + esc(z) + '"' + (z === local ? " selected" : "") + ">" + esc(z) + "</option>";
  }
  // 浏览器时区不在清单（极端）：补在顶部，保证默认值可见。
  if (!localSeen) {
    options = '<option value="' + esc(local) + '" selected>' + esc(local) + "</option>" + options;
  }
  select.innerHTML = options;
}

function closeCreateForm() {
  var overlay = document.getElementById("schedule-form-overlay");
  if (overlay) { overlay.classList.remove("active"); }
  editingTask = null;
  clearScheduleFormErrors();
  var modalTitle = document.getElementById("schedule-form-title-label");
  if (modalTitle) { modalTitle.textContent = "新建自动化任务"; }
  var saveBtn = document.getElementById("schedule-form-save");
  if (saveBtn) {
    saveBtn.textContent = "创建任务";
    saveBtn.disabled = false;
    saveBtn.removeAttribute("aria-busy");
  }
  var kind = document.getElementById("schedule-form-kind");
  if (kind) { kind.disabled = false; }
  var targetKind = document.getElementById("schedule-form-target-kind");
  if (targetKind) { targetKind.disabled = false; }
  var session = document.getElementById("schedule-form-session");
  if (session) { session.disabled = false; }
  var amount = document.getElementById("schedule-form-amount");
  if (amount) { amount.disabled = false; }
  var unlockedFields = ["schedule-form-goal", "schedule-form-plan",
    "schedule-form-workspace", "schedule-form-profile"];
  for (var unlockedIndex = 0; unlockedIndex < unlockedFields.length; unlockedIndex++) {
    var unlocked = document.getElementById(unlockedFields[unlockedIndex]);
    if (unlocked) { unlocked.disabled = false; }
  }
  var target = lastFormFocus;
  lastFormFocus = null;
  if (target && typeof target.focus === "function") { target.focus(); }
}

// 规则类型 → 可见字段（after/every 用 amount，at 用 time，daily/weekly/cron
// 用 clock+zone，weekly 另有 weekdays，cron 另有 expression）。
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
  show("schedule-form-clock-item", kind === "daily" || kind === "weekly");
  show("schedule-form-zone-item", kind === "daily" || kind === "weekly" || kind === "cron");
  show("schedule-form-weekdays-item", kind === "weekly");
  show("schedule-form-cron-item", kind === "cron");
  var timeLabel = document.getElementById("schedule-form-time-label");
  if (timeLabel) { timeLabel.textContent = "具体时间（本地）"; }
  var clockLabel = document.getElementById("schedule-form-clock-label");
  if (clockLabel) { clockLabel.textContent = "规则时间（HH:MM[:SS]，按所选时区）"; }
}

// 执行上下文 → 字段可见性与必填性。session 只需要目标会话；headless 需要
// 作业目标与可选 profile；workspace 额外需要目标工作区绝对路径。
function syncScheduleTargetFields(targetKind) {
  var show = function (id, visible) {
    var el = document.getElementById(id);
    if (el) { el.hidden = !visible; }
  };
  var isSession = targetKind === "session";
  var isWorkspace = targetKind === "workspace";
  show("schedule-form-session-item", isSession);
  show("schedule-form-workspace-item", isWorkspace);
  show("schedule-form-profile-item", !isSession);
  show("schedule-form-goal-item", !isSession);
  show("schedule-form-plan-item", !isSession);
  show("schedule-form-target-hint", !isSession);
  setScheduleRequired("schedule-form-session", isSession);
  setScheduleRequired("schedule-form-workspace", isWorkspace);
  setScheduleRequired("schedule-form-goal", !isSession);
  var description = document.getElementById("schedule-target-description");
  if (description) {
    description.textContent = isSession
      ? "到期把任务内容投递到目标会话，不创建新会话；适合提醒与轻量通知。"
      : isWorkspace
        ? "到期在指定工作区创建临时会话，按作业目标与执行步骤运行，完成后归档。"
        : "到期在默认工作区创建临时会话执行；执行 Profile 可指定运行配置（可选）。";
  }
}

function setScheduleRequired(id, required) {
  var el = document.getElementById(id);
  if (!el) { return; }
  if (required) { el.setAttribute("aria-required", "true"); }
  else { el.removeAttribute("aria-required"); }
}

function showScheduleFormError(message, fieldId) {
  var summary = document.getElementById("schedule-form-error-summary");
  if (summary) {
    summary.hidden = false;
    summary.textContent = message;
  }
  var status = document.getElementById("schedule-form-status");
  if (status) { status.textContent = ""; }
  if (fieldId) {
    showScheduleFieldError(fieldId, message);
    var field = document.getElementById(fieldId);
    if (field && typeof field.focus === "function") { field.focus(); }
    return;
  }
  if (summary && typeof summary.focus === "function") { summary.focus(); }
}

function showScheduleFieldError(fieldId, message) {
  var field = document.getElementById(fieldId);
  if (!field) { return; }
  var errorId = fieldId + "-error";
  var errorEl = document.getElementById(errorId);
  if (!errorEl) {
    errorEl = document.createElement("p");
    errorEl.id = errorId;
    errorEl.className = "schedule-field-error";
    errorEl.setAttribute("role", "alert");
    var container = field.closest ? field.closest("label") : null;
    if (container) { container.appendChild(errorEl); }
    else if (field.parentNode) { field.parentNode.insertBefore(errorEl, field.nextSibling); }
  }
  errorEl.textContent = message;
  field.setAttribute("aria-invalid", "true");
  field.setAttribute("aria-describedby", errorId);
  field.classList.add("is-invalid");
}

function clearScheduleFieldError(element) {
  if (!element || !element.id) { return; }
  var fieldId = element.id;
  var errorEl = document.getElementById(fieldId + "-error");
  if (errorEl && errorEl.parentNode) { errorEl.parentNode.removeChild(errorEl); }
  element.removeAttribute("aria-invalid");
  element.removeAttribute("aria-describedby");
  element.classList.remove("is-invalid");
}

function clearScheduleFormErrors() {
  var form = document.getElementById("schedule-form");
  if (form) {
    var invalid = form.querySelectorAll("[aria-invalid='true']");
    for (var i = 0; i < invalid.length; i++) {
      invalid[i].removeAttribute("aria-invalid");
      invalid[i].removeAttribute("aria-describedby");
      invalid[i].classList.remove("is-invalid");
    }
    var errors = form.querySelectorAll(".schedule-field-error");
    for (var e = 0; e < errors.length; e++) {
      if (errors[e].parentNode) { errors[e].parentNode.removeChild(errors[e]); }
    }
  }
  clearScheduleErrorSummary();
}

function clearScheduleErrorSummary() {
  var summary = document.getElementById("schedule-form-error-summary");
  if (summary) {
    summary.hidden = true;
    summary.textContent = "";
  }
}

// 键盘可达性：Tab 在弹层内循环，Escape 关闭（见 initCreateForm 的 keydown）。
function trapScheduleFormFocus(event) {
  if (!event || event.key !== "Tab") { return; }
  var modal = document.getElementById("schedule-form-modal");
  if (!modal || typeof modal.querySelectorAll !== "function") { return; }
  var nodes = modal.querySelectorAll("button, input, select, textarea, [tabindex]:not([tabindex='-1'])");
  var focusable = [];
  for (var i = 0; i < nodes.length; i++) {
    if (nodes[i].disabled || nodes[i].hidden) { continue; }
    if (typeof nodes[i].getClientRects === "function" && nodes[i].getClientRects().length === 0) {
      continue;
    }
    focusable.push(nodes[i]);
  }
  if (focusable.length === 0) { return; }
  var first = focusable[0];
  var last = focusable[focusable.length - 1];
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}

function updateSchedulePreview() {
  var preview = document.getElementById("schedule-form-preview");
  if (!preview) { return; }
  var val = function (id) {
    var el = document.getElementById(id);
    return el ? String(el.value || "").trim() : "";
  };
  var kindEl = document.getElementById("schedule-form-kind");
  var kind = kindEl ? kindEl.value : "after";
  var text = "";
  if (kind === "after" || kind === "every") {
    var minutes = Number(val("schedule-form-amount"));
    if (editingTask && editingTask.kind === "after") {
      text = "一次性延迟任务保留原定时间；如需调整请使用“再次排程”。";
    } else if (!isFinite(minutes) || minutes <= 0) {
      text = "请输入大于 0 的分钟数。";
    } else {
      var seconds = Math.round(minutes * 60);
      var next = new Date(Date.now() + seconds * 1000);
      text = kind === "after"
        ? "预计在 " + formatLocal(next.toISOString()) + "（本地）触发（" +
          formatDuration(seconds) + "后）。"
        : "每 " + formatDuration(seconds) + "触发一次；首次约在 " +
          formatLocal(next.toISOString()) + "（本地）。";
    }
  } else if (kind === "at") {
    var raw = val("schedule-form-time");
    if (!raw) { text = "请选择具体时间。"; }
    else {
      var parsed = new Date(raw);
      text = isNaN(parsed.getTime())
        ? "时间格式无效。"
        : "将在 " + formatLocal(parsed.toISOString()) + "（本地）触发。";
    }
  } else if (kind === "daily") {
    var time = val("schedule-form-clock");
    var zone = val("schedule-form-zone");
    text = time
      ? "每天 " + formatClock(time) + (zone ? "（" + zone + "）" : "") + " 触发。"
      : "请填写本地时间（HH:MM[:SS]）。";
  } else if (kind === "weekly") {
    var weeklyTime = val("schedule-form-clock");
    var weeklyZone = val("schedule-form-zone");
    var boxes = document.querySelectorAll("#schedule-form-weekdays input[type=checkbox]:checked");
    var days = [];
    for (var i = 0; i < boxes.length; i++) {
      var day = Number(boxes[i].value);
      if (day >= 1 && day <= 7) { days.push(day); }
    }
    text = weeklyTime && days.length > 0
      ? "每周" + weekdayLabel(days) + " " + formatClock(weeklyTime) +
        (weeklyZone ? "（" + weeklyZone + "）" : "") + " 触发。"
      : "请至少勾选一个星期并填写本地时间。";
  } else if (kind === "cron") {
    var expression = val("schedule-form-cron");
    var cronZone = val("schedule-form-zone");
    text = expression
      ? "按 Cron " + expression + (cronZone ? "（" + cronZone + "）" : "") + " 触发。"
      : "请填写 Cron 表达式。";
  }
  preview.textContent = text || "填写时间规则后显示执行预览。";
}

// 由表单派生创建负载；返回 {error} 表示本地预检未过（状态栏提示）。
function buildCreateRequest() {
  var val = function (id) {
    var el = document.getElementById(id);
    return el ? String(el.value || "").trim() : "";
  };
  var sessionSelect = document.getElementById("schedule-form-session");
  var sessionId = sessionSelect ? String(sessionSelect.value || "").trim() : "";
  var targetKindEl = document.getElementById("schedule-form-target-kind");
  var targetKind = targetKindEl ? targetKindEl.value : "session";
  var title = val("schedule-form-title");
  var prompt = val("schedule-form-prompt");
  var kindEl = document.getElementById("schedule-form-kind");
  var kind = kindEl ? kindEl.value : "after";
  if (targetKind === "session" && !sessionId) {
    return { error: "请选择目标会话", fieldId: "schedule-form-session" };
  }
  if (!title) { return { error: "请填写任务名称", fieldId: "schedule-form-title" }; }
  if (!prompt) { return { error: "请填写任务内容", fieldId: "schedule-form-prompt" }; }
  var goal = val("schedule-form-goal");
  var targetWorkspace = val("schedule-form-workspace");
  var targetProfile = val("schedule-form-profile");
  var plan = val("schedule-form-plan")
    .split("\n")
    .map(function (line) { return line.trim(); })
    .filter(function (line) { return line.length > 0; });
  if (targetKind !== "session" && !goal) {
    return { error: "无人值守/工作区任务必须填写作业目标", fieldId: "schedule-form-goal" };
  }
  if (targetKind === "workspace" && !targetWorkspace) {
    return { error: "工作区任务必须填写目标工作区绝对路径", fieldId: "schedule-form-workspace" };
  }
  var base = { title: title, prompt: prompt };
  if (targetKind === "session") {
    base.session_id = sessionId;
  } else {
    // session_id is not a delivery binding for non-session targets; the
    // backend uses it as the creator/owner so this management surface can
    // still edit/pause/delete the task later.
    var creatorSessionId = getCurrentSessionID();
    if (creatorSessionId) { base.session_id = creatorSessionId; }
    base.target_kind = targetKind;
    base.goal = goal;
    if (plan.length > 0) { base.plan = plan; }
    if (targetProfile) { base.target_profile = targetProfile; }
    if (targetKind === "workspace") { base.target_workspace = targetWorkspace; }
  }
  if (kind === "after" || kind === "every") {
    var minutes = Number(val("schedule-form-amount"));
    if (!isFinite(minutes) || minutes <= 0 || (kind === "every" && minutes < 1)) {
      return {
        error: kind === "after" ? "延迟分钟数必须为正整数" : "间隔必须至少 1 分钟",
        fieldId: "schedule-form-amount",
      };
    }
    if (kind === "after") { base.after_seconds = Math.round(minutes * 60); }
    else { base.every_seconds = Math.round(minutes * 60); }
    return base;
  }
  if (kind === "at") {
    var raw = val("schedule-form-time");
    if (!raw) { return { error: "请选择具体时间", fieldId: "schedule-form-time" }; }
    var parsed = new Date(raw);
    if (isNaN(parsed.getTime())) {
      return { error: "时间格式无效", fieldId: "schedule-form-time" };
    }
    base.at = parsed.toISOString();
    return base;
  }
  var zone = val("schedule-form-zone");
  if (!zone) { return { error: "请选择时区", fieldId: "schedule-form-zone" }; }
  if (kind === "daily") {
    var dailyTime = val("schedule-form-clock");
    if (!dailyTime) {
      return { error: "请填写本地时间（HH:MM[:SS]）", fieldId: "schedule-form-clock" };
    }
    base.daily = { time: dailyTime, time_zone: zone };
    return base;
  }
  if (kind === "weekly") {
    var weeklyTime = val("schedule-form-clock");
    if (!weeklyTime) {
      return { error: "请填写本地时间（HH:MM[:SS]）", fieldId: "schedule-form-clock" };
    }
    var boxes = document.querySelectorAll("#schedule-form-weekdays input[type=checkbox]:checked");
    var weekdays = [];
    for (var i = 0; i < boxes.length; i++) {
      var day = Number(boxes[i].value);
      if (day >= 1 && day <= 7) { weekdays.push(day); }
    }
    if (weekdays.length === 0) {
      return { error: "请至少勾选一个星期", fieldId: "schedule-form-weekdays" };
    }
    weekdays.sort(function (a, b) { return a - b; });
    base.weekly = { time: weeklyTime, time_zone: zone, weekdays: weekdays };
    return base;
  }
  if (kind === "cron") {
    var expression = val("schedule-form-cron");
    if (!expression) { return { error: "请填写 Cron 表达式", fieldId: "schedule-form-cron" }; }
    base.cron = { expression: expression, time_zone: zone };
    return base;
  }
  return { error: "未知规则类型: " + kind, fieldId: "schedule-form-kind" };
}

// 编辑只修改当前规则的名称/内容/时间；kind 在编辑态锁定，避免 after 等
// 不支持时间替换的规则产生“表单看起来能改、后端实际忽略”的假入口。
function buildUpdateRequest(task) {
  var val = function (id) {
    var el = document.getElementById(id);
    return el ? String(el.value || "").trim() : "";
  };
  var sessionId = taskSessionId(task);
  var title = val("schedule-form-title");
  var prompt = val("schedule-form-prompt");
  if (!sessionId) { return { error: "无法确定任务所属会话" }; }
  if (!title) { return { error: "请填写任务名称", fieldId: "schedule-form-title" }; }
  if (!prompt) { return { error: "请填写任务内容", fieldId: "schedule-form-prompt" }; }
  var request = {
    session_id: sessionId,
    id: task.id,
    expected: taskRecordExpectation(task),
    title: title,
    prompt: prompt,
  };
  if (task.kind === "after") {
    return request;
  }
  if (task.kind === "at") {
    var raw = val("schedule-form-time");
    if (!raw) { return { error: "请选择具体时间", fieldId: "schedule-form-time" }; }
    var parsed = new Date(raw);
    if (isNaN(parsed.getTime())) {
      return { error: "时间格式无效", fieldId: "schedule-form-time" };
    }
    request.change = { kind: "at", at: parsed.toISOString() };
    return request;
  }
  var zone = val("schedule-form-zone");
  if (!zone) { return { error: "请选择时区", fieldId: "schedule-form-zone" }; }
  if (task.kind === "every") {
    var minutes = Number(val("schedule-form-amount"));
    if (!isFinite(minutes) || minutes < 1) {
      return { error: "间隔必须至少 1 分钟", fieldId: "schedule-form-amount" };
    }
    request.change = { kind: "every", every_seconds: Math.round(minutes * 60) };
    return request;
  }
  if (task.kind === "daily") {
    var dailyTime = val("schedule-form-clock");
    if (!dailyTime) {
      return { error: "请填写本地时间（HH:MM[:SS]）", fieldId: "schedule-form-clock" };
    }
    request.change = { kind: "daily", daily: { time: dailyTime, time_zone: zone } };
    return request;
  }
  if (task.kind === "weekly") {
    var weeklyTime = val("schedule-form-clock");
    if (!weeklyTime) {
      return { error: "请填写本地时间（HH:MM[:SS]）", fieldId: "schedule-form-clock" };
    }
    var boxes = document.querySelectorAll("#schedule-form-weekdays input[type=checkbox]:checked");
    var weekdays = [];
    for (var i = 0; i < boxes.length; i++) {
      var day = Number(boxes[i].value);
      if (day >= 1 && day <= 7) { weekdays.push(day); }
    }
    if (weekdays.length === 0) {
      return { error: "请至少勾选一个星期", fieldId: "schedule-form-weekdays" };
    }
    weekdays.sort(function (a, b) { return a - b; });
    request.change = { kind: "weekly", weekly: { time: weeklyTime, time_zone: zone, weekdays: weekdays } };
    return request;
  }
  if (task.kind === "cron") {
    var expression = val("schedule-form-cron");
    if (!expression) { return { error: "请填写 Cron 表达式", fieldId: "schedule-form-cron" }; }
    request.change = { kind: "cron", cron: { expression: expression, time_zone: zone } };
    return request;
  }
  return { error: "未知规则类型: " + task.kind, fieldId: "schedule-form-kind" };
}

function submitCreateForm() {
  var status = document.getElementById("schedule-form-status");
  var saveBtn = document.getElementById("schedule-form-save");
  var updating = editingTask;
  clearScheduleFormErrors();
  var request = updating ? buildUpdateRequest(updating) : buildCreateRequest();
  if (request.error) {
    showScheduleFormError(request.error, request.fieldId);
    return;
  }
  if (saveBtn) {
    saveBtn.disabled = true;
    saveBtn.setAttribute("aria-busy", "true");
  }
  if (status) { status.textContent = updating ? "保存中…" : "创建中…"; }
  var idleLabel = updating ? "保存" : "创建任务";
  apiFetch(SCHEDULE_API_BASE + (updating ? "/update" : "/create"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(request),
  })
    .then(scheduleJSONResult)
    .then(function (result) {
      if (saveBtn) {
        saveBtn.disabled = false;
        saveBtn.removeAttribute("aria-busy");
        saveBtn.textContent = idleLabel;
      }
      if (result.ok) {
        var rejected = updating && result.payload &&
          result.payload.updated === false && result.payload.code;
        if (rejected) {
          showScheduleFormError(
            "保存失败: " + scheduleErrorMessage(result.payload, result.status), "");
          return;
        }
        showToast(updating ? "任务已更新" : "任务已创建（来源：人工）");
        closeCreateForm();
        loadSchedule(true);
        return;
      }
      // 后端 400 携带 invalid_selector/not_future 等服务层错误码。
      showScheduleFormError(
        (updating ? "保存失败: " : "创建失败: ") +
        scheduleErrorMessage(result.payload, result.status), "");
    })
    .catch(function (error) {
      if (saveBtn) {
        saveBtn.disabled = false;
        saveBtn.removeAttribute("aria-busy");
        saveBtn.textContent = idleLabel;
      }
      showScheduleFormError(
        (updating ? "保存失败: " : "创建失败: ") +
        (error && error.message ? error.message : String(error)), "");
    });
}
