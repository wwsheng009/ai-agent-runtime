// SSE 事件流:EventSource 连接、onSSEEvent 事件分发、状态行/日志/动态状态栏。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { hideApproval, showApproval, showQuestion } from "./approvals.js";
import { clearPendingPrompts, getUiState, refreshScreen, setUI, updateTitle } from "./chat.js";
import { loadRuntimeMeta } from "./runtime.js";
import { loadSessions } from "./sessions.js";
import { addStreamImage, appendStreamReasoning, appendStreamText, beginStream, endStream, isStreamActive, renderStream, setStreamText, setStreamTool, startTypeTimer } from "./stream.js";

export var statusEl = document.getElementById("connection-status");
var turnEl = document.getElementById("turn-status");
var eventLogEl = document.getElementById("event-log");
var lastSequence = 0;
function setStatus(text, connected) {
  statusEl.textContent = text;
  statusEl.className = connected ? "connected" : "disconnected";
  updateTitle();
}

function setTurn(text) { turnEl.textContent = text; }

function logEvent(name, data) {
  var line = document.createElement("div");
  var ts = data && data._event ? data._event.timestamp : "";
  line.className = "ev-line";
  var nameSpan = document.createElement("span");
  nameSpan.className = "ev-name";
  nameSpan.textContent = (ts ? "[" + ts + "] " : "") + name;
  var payload = document.createElement("span");
  payload.textContent = " " + JSON.stringify(data || {});
  line.appendChild(nameSpan);
  line.appendChild(payload);
  eventLogEl.appendChild(line);
  while (eventLogEl.childNodes.length > 200) { eventLogEl.removeChild(eventLogEl.firstChild); }
  eventLogEl.scrollTop = eventLogEl.scrollHeight;
  // 更新日志计数
  var countEl = document.getElementById("log-count");
  if (countEl) { countEl.textContent = eventLogEl.childNodes.length + " 条事件"; }
}

var refreshKeys = { "turn_end": 1, "tool_end": 1, "session_end": 1, "session_interrupted": 1, "error": 1, "screen_refresh": 1, "compact_end": 1 };

// ---- 动态状态栏（同步 aicli chat 底部活动状态行） ----
// dynamicStatus: { text, role, interruptible, startedAt }
// 由 SSE dynamic_status 事件驱动；时钟（"(1m 52s • esc to interrupt)"）由
// 前端基于 started_at 本地每秒推进，格式与 Go formatChatDynamicStatusElapsed
// 完全一致（Ns / Nm Ns / Nh Nm Ns）。
var dynamicStatus = null;

function fmtElapsed(ms) {
  var seconds = Math.max(0, Math.floor(ms / 1000));
  if (seconds < 60) { return seconds + "s"; }
  var minutes = Math.floor(seconds / 60);
  var rem = seconds % 60;
  if (minutes < 60) { return minutes + "m " + rem + "s"; }
  var hours = Math.floor(minutes / 60);
  return hours + "h " + (minutes % 60) + "m " + rem + "s";
}

function renderDynamicStatus() {
  var el = document.getElementById("dynamic-status");
  if (!el) { return; }
  if (!dynamicStatus || !dynamicStatus.text) {
    el.style.display = "none";
    el.textContent = "";
    return;
  }
  var text = dynamicStatus.text;
  if (dynamicStatus.startedAt) {
    var elapsed = fmtElapsed(Date.now() - dynamicStatus.startedAt);
    text = text + (dynamicStatus.interruptible ? " (" + elapsed + " • esc to interrupt)" : " (" + elapsed + ")");
  }
  el.textContent = text;
  el.className = "dynamic-status ds-" + (dynamicStatus.role || "info").toLowerCase();
  el.style.display = "flex";
}

function onSSEEvent(eventName, data) {
  logEvent(eventName, data);
  lastSequence = (data && data._event && data._event.sequence) || lastSequence;

  switch (eventName) {
    case "connected":
      setStatus(data.session_active ? "已连接" : "已连接(无会话)", true);
      setTurn(data.session_busy ? "忙碌 turn=" + (data.turn_id || "?") : "就绪");
      dynamicStatus = null; // 重连后等待下一次状态事件，避免显示陈旧状态
      renderDynamicStatus();
      if (data.pending_approval) { showApproval(data.pending_approval); }
      if (data.pending_question) { showQuestion(data.pending_question); }
      if (data.session_busy) {
        beginStream();
        setUI("busy", "执行中…");
      } else {
        refreshScreen();
        // 若正在等待自己刚发送的 prompt 的 turn_start，保持 posting
        if (getUiState() !== "posting") { setUI("idle", ""); }
      }
      loadSessions(); // 重连后刷新会话列表
      break;
    case "turn_start":
      setTurn("处理中 " + (data.model ? "(" + data.model + ")" : ""));
      setUI("busy", "执行中…");
      beginStream();
      break;
    case "reasoning_delta":
      if (isStreamActive() && data.content) {
        appendStreamReasoning(data.content);
        startTypeTimer(); // 打字机定时器逐段揭示
      }
      break;
    case "assistant_delta":
      if (isStreamActive() && data.text) {
        appendStreamText(data.text);
        startTypeTimer(); // 打字机定时器逐字揭示
      }
      break;
    case "assistant_message":
      if (isStreamActive()) {
        setStreamText(data.content);
        startTypeTimer();
      }
      break;
    case "tool_start":
      if (isStreamActive()) {
        setStreamTool("[工具: " + (data.tool_name || "?") + " 执行中…]");
        renderStream();
      }
      break;
    case "tool_end":
      if (isStreamActive()) {
        setStreamTool("[工具: " + (data.tool_name || "?") + " 完成]");
        renderStream();
      }
      break;
    case "assistant_image_progress":
      // 图像生成进度：提取可预览的 URL/base64 并渲染到流式消息中。
      if (isStreamActive() && data) {
        var img = data.image || data;
        var src = (img && typeof img === "object") ? (img.url || img.b64_data || img.data || "") : "";
        if (src && addStreamImage(src)) {
          renderStream();
        }
      }
      break;
    case "turn_end":
      setTurn("就绪");
      setUI("idle", "");
      endStream();
      break;
    case "approval_requested":
      showApproval(data);
      break;
    case "approval_resolved":
      hideApproval();
      if (!isStreamActive()) { refreshScreen(); }
      break;
    case "question_asked":
      showQuestion(data);
      break;
    case "question_answered":
      hideApproval();
      break;
    case "session_interrupted":
      setUI("idle", "已停止");
      setTurn("就绪");
      loadSessions();
      endStream();
      refreshScreen();
      break;
    case "session_start":
    case "session_end":
      // 会话切换/结束：复位按钮、刷新会话列表与屏幕
      setUI("idle", "");
      dynamicStatus = null;
      renderDynamicStatus();
      clearPendingPrompts(); // 旧会话的本地回显不带到新会话
      loadSessions();
      endStream();
      refreshScreen(true);
      break;
    case "dynamic_status":
      // 动态状态栏：active=false 或空文本时清除；active=true 时显示并
      // 以 started_at 为基准本地推进时钟（服务端只在状态变化时推送一次）。
      if (data && data.active && data.text) {
        dynamicStatus = {
          text: data.text,
          role: data.role || "info",
          interruptible: !!data.interruptible,
          startedAt: data.started_at ? new Date(data.started_at).getTime() : 0
        };
      } else {
        dynamicStatus = null;
      }
      renderDynamicStatus();
      break;
    case "model_changed":
      // TUI 侧 /model、/reasoning_effort 或 /login 切换落地（aicli.chat.
      // model_selection_changed）：重新拉取权威 runtime 状态刷新底部栏。
      // web 自己切换期间 cfgUiDirty 会防止本处刷新覆盖用户正在确认的值。
      loadRuntimeMeta();
      break;
    case "error":
      setUI("idle", "发生错误");
      refreshScreen();
      break;
    default:
      if (refreshKeys[eventName] && !isStreamActive()) { refreshScreen(); }
  }
}

function openEventSource() {
  setStatus("连接中…", false);
  var es = new EventSource("/web/api/events");
  es.onopen = function () { setStatus("已连接", true); };
  es.onerror = function () {
    setStatus("已断开，重连中…", false);
    es.close();
    setTimeout(openEventSource, 2000);
  };
  es.onmessage = function (e) {
    var data = {};
    try { data = JSON.parse(e.data); } catch (err) { /* ignore */ }
    onSSEEvent("message", data);
  };
  ["connected", "heartbeat", "screen_refresh", "turn_start", "turn_delta", "turn_end",
   "session_start", "session_end", "session_interrupted", "reasoning_delta",
   "assistant_delta", "assistant_image_progress", "tool_start", "tool_end", "approval_requested",
   "approval_resolved", "question_asked", "question_answered", "dynamic_status", "model_changed"].forEach(function (name) {
    es.addEventListener(name, function (e) {
      var data = {};
      try { data = JSON.parse(e.data); } catch (err) { /* ignore */ }
      onSSEEvent(name, data);
    });
  });
}

export function initSSE() {
  var logClearBtn = document.getElementById("log-clear-btn");
  if (logClearBtn) {
    logClearBtn.addEventListener("click", function () {
      eventLogEl.innerHTML = "";
      var countEl = document.getElementById("log-count");
      if (countEl) { countEl.textContent = "0 条事件"; }
    });
  }

  openEventSource();
  // 动态状态栏时钟:本地每秒推进 (N • esc to interrupt) 后缀;
  // 无活动状态时渲染函数直接置空,成本可忽略。
  setInterval(function () {
    if (dynamicStatus) { renderDynamicStatus(); }
  }, 1000);
}
