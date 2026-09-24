// SSE 事件流:EventSource 连接、onSSEEvent 事件分发、状态行/日志/动态状态栏。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { hideApproval, showApproval, showQuestion } from "./approvals.js";
import { clearPendingPrompts, getUiState, refreshScreen, setUI, updateTitle } from "./chat.js";
import { handleCacheSSEEvent } from "./cache.js";
import { loadRuntimeMeta } from "./runtime.js";
import { loadStatusBar } from "./statusbar.js";
import { handleMeshStreamEvent, loadSessions, meshResumeSeq, notifySessionSwitchedCompleted, notifySharedStreamState } from "./sessions.js";
import { addStreamImage, appendStreamReasoning, appendStreamText, beginStream, endStream, isStreamActive, renderStream, setStreamText, setStreamTool, startTypeTimer } from "./stream.js";
import { isNetDegraded, onNetStateChange, webAuthToken } from "./util.js";

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

// 工具终态后的节流 transcript 刷新：长回合中完成/取消的工具单元格会写入
// 服务端权威 messages，但流式期间对话区默认不重绘（避免打断 stream 气泡），
// 用户会看到整轮停留在旧快照（线上表现为只显示 "• Running grep" +
// "Analyzing"）。这里在 tool_end 后去抖 + 最小间隔刷新对话区，并保留
// stream 气泡（keepStream），让工具过程在回合进行中可见。
var transcriptRefreshTimer = null;
var transcriptRefreshLast = 0;
var TRANSCRIPT_REFRESH_MIN_INTERVAL = 1200;
function scheduleTranscriptRefresh() {
  if (transcriptRefreshTimer) { return; }
  var wait = Math.max(0, TRANSCRIPT_REFRESH_MIN_INTERVAL - (Date.now() - transcriptRefreshLast));
  transcriptRefreshTimer = setTimeout(function () {
    transcriptRefreshTimer = null;
    transcriptRefreshLast = Date.now();
    // keepStream 仅在流式仍活跃时生效；回合结束后走常规刷新路径。
    refreshScreen(false, { keepStream: isStreamActive() });
  }, wait);
}

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
        // 运行态只由顶栏 #turn-status 呈现（#send-status 只承载发送/排队/
        // 停止等瞬态提示），避免同一执行状态在顶栏重复出现两次。
        setUI("busy", "");
      } else {
        refreshScreen();
        // 若正在等待自己刚发送的 prompt 的 turn_start，保持 posting
        if (getUiState() !== "posting") { setUI("idle", ""); }
      }
      loadSessions(); // 重连后刷新会话列表
      loadStatusBar(); // 重连后刷新底部状态栏
      break;
    case "turn_start":
      // 顶栏不再附带 provider/model（当前配置见底部配置栏 / 状态栏）。
      setTurn("处理中");
      setUI("busy", "");
      beginStream();
      break;
    case "reasoning_delta":
      if (isStreamActive() && (data.content || data.text)) {
        appendStreamReasoning(data.content || data.text);
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
        // 流式期间用节流快照刷新对话区，但保留 stream 气泡（keepStream）。
        scheduleTranscriptRefresh();
      } else {
        refreshScreen();
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
      loadStatusBar(); // 回合结束后刷新上下文/balance 状态
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
    case "session_switched":
      // 会话切换/结束：复位按钮、刷新会话列表与屏幕
      setUI("idle", "");
      dynamicStatus = null;
      renderDynamicStatus();
      clearPendingPrompts(); // 旧会话的本地回显不带到新会话
      loadSessions();
      loadStatusBar(); // 会话切换后刷新状态栏
      endStream();
      refreshScreen(true);
      // session_switched 由服务端按会话身份变化合成（P2 ④：/resume、/new、/load
      // 不产生 turn，运行时不会发布 session_start/end）：额外恢复切换期间禁用的
      // 入口，并清掉 sessions.js 的断连兜底定时器。
      if (eventName === "session_switched") { notifySessionSwitchedCompleted(); }
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
      loadStatusBar();
      break;
    case "cache_request_finished":
      // LLM 缓存请求记录终态（cache.analytics.v1）：缓存页签防抖增量刷新。
      handleCacheSSEEvent();
      break;
    case "error":
      setUI("idle", "发生错误");
      refreshScreen();
      break;
    default:
      if (refreshKeys[eventName] && !isStreamActive()) { refreshScreen(); }
  }
}

// eventsURL：本页唯一的 SSE 地址（单连接契约，Web 子方案 §5.6）。
//
// `?mesh=1` 让服务端把网格扇入帧并入这条流：过去每页要开两条常驻 SSE
// （/web/api/events + /web/api/mesh/events），HTTP/1.1 下浏览器单 host 只有约
// 6 条并发连接，两个标签页就能占满连接池，之后所有 /web/api/* 请求永久排队
// ——页面既不报错也不恢复。合并后每页只占一条连接。
//
// `since_seq` 是网格帧的续传游标（对主事件流无副作用：它不重放历史）；
// EventSource 无法设置请求头，非回环模式只能把写令牌追加到查询参数
// （回环模式 GET/SSE 无需令牌；取值顺序见 util.js::webAuthToken）。
function eventsURL() {
  var url = "/web/api/events?mesh=1";
  var since = meshResumeSeq();
  if (since > 0) { url += "&since_seq=" + since; }
  var token = webAuthToken();
  if (token) { url += "&token=" + encodeURIComponent(token); }
  return url;
}

function openEventSource() {
  setStatus("连接中…", false);
  var es = new EventSource(eventsURL());
  es.onopen = function () {
    setStatus("已连接", true);
    // 通知 sessions.js：合并流已建立，网格帧进入 mesh.ready 宽限期。
    notifySharedStreamState(true);
  };
  es.onerror = function () {
    setStatus("已断开，重连中…", false);
    // 主 SSE 断开 = 网格帧随之中断：sessions.js 据此退回 10s 轮询兜底（§5.6）。
    notifySharedStreamState(false);
    es.close();
    setTimeout(openEventSource, 2000);
  };
  es.onmessage = function (e) {
    var data = {};
    try { data = JSON.parse(e.data); } catch (err) { /* ignore */ }
    onSSEEvent("message", data);
  };
  ["connected", "heartbeat", "screen_refresh", "turn_start", "turn_delta", "turn_end",
   "session_start", "session_end", "session_switched", "session_interrupted", "reasoning_delta",
   "assistant_delta", "assistant_image_progress", "tool_start", "tool_end", "approval_requested",
   "approval_resolved", "question_asked", "question_answered", "dynamic_status", "model_changed",
   "cache_request_finished"].forEach(function (name) {
    es.addEventListener(name, function (e) {
      var data = {};
      try { data = JSON.parse(e.data); } catch (err) { /* ignore */ }
      onSSEEvent(name, data);
    });
  });
  // 网格帧（合并流）：帧类型与独立端点逐字一致，处理逻辑统一在 sessions.js
  // （续传游标 meshLastSeq、退避重连、200ms 合并刷新、10s 轮询兜底）。
  // mesh.unavailable 表示服务端未提供网格（--mesh=false / 扇入客户端超限），
  // 前端只降级不报错（§4.7）。
  ["mesh.ready", "mesh.unavailable", "mesh.peer.joined", "mesh.peer.left",
   "mesh.peer.updated", "mesh.session.changed", "mesh.peer.event", "mesh.lagged"].forEach(function (name) {
    es.addEventListener(name, function (e) {
      var data = {};
      try { data = JSON.parse(e.data); } catch (err) { /* ignore */ }
      handleMeshStreamEvent(name, data, e.lastEventId);
    });
  });
}

// ---- 降级模式（请求超时 + 轮询兜底，§4.7）----
//
// 触发：util.js 的降级状态机连续两次网络层失败（超时/断网）后置位。此时页面
// 可能连一条请求都发不出去（浏览器连接池被常驻流占满），表现为「什么都没反应」。
// 处理：显示常驻横幅如实告知 + 每 5s 用带超时的轻量 GET 重试（screen / sessions /
// statusbar），任一请求成功即自动退出降级并刷新界面——不需要用户手动刷新页面。
var DEGRADED_POLL_INTERVAL_MS = 5000;
var degradedBannerEl = null;
var degradedPollTimer = null;

function renderDegradedBanner(active, reason) {
  if (!active) {
    if (degradedBannerEl && degradedBannerEl.parentNode) {
      degradedBannerEl.parentNode.removeChild(degradedBannerEl);
    }
    degradedBannerEl = null;
    return;
  }
  if (!degradedBannerEl) {
    degradedBannerEl = document.createElement("div");
    degradedBannerEl.id = "net-degraded-banner";
    degradedBannerEl.style.cssText =
      "position:fixed;top:0;left:0;right:0;z-index:9999;padding:6px 12px;" +
      "background:#7a4b00;color:#ffe9c4;font-size:12px;text-align:center;";
    document.body.appendChild(degradedBannerEl);
  }
  degradedBannerEl.textContent = "⚠ 连接异常：" + (reason || "请求超时") +
    "。已切换为降级轮询，连接恢复后自动刷新。";
}

// startDegradedPolling：降级期间的轮询重试。只发 3 个轻量 GET（都带 12s 超时），
// 避免在连接池已满时进一步堆请求。
function startDegradedPolling() {
  if (degradedPollTimer) { return; }
  degradedPollTimer = setInterval(function () {
    if (!isNetDegraded()) { stopDegradedPolling(); return; }
    refreshScreen();
    loadSessions();
    loadStatusBar();
  }, DEGRADED_POLL_INTERVAL_MS);
}

function stopDegradedPolling() {
  if (degradedPollTimer) { clearInterval(degradedPollTimer); degradedPollTimer = null; }
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
  // 降级状态订阅（§4.7）：连续网络层失败 → 横幅 + 轮询重试；恢复 → 收起横幅。
  onNetStateChange(function (degraded, reason) {
    renderDegradedBanner(degraded, reason);
    if (degraded) {
      startDegradedPolling();
    } else {
      stopDegradedPolling();
    }
  });
  // 动态状态栏时钟:本地每秒推进 (N • esc to interrupt) 后缀;
  // 无活动状态时渲染函数直接置空,成本可忽略。
  setInterval(function () {
    if (dynamicStatus) { renderDynamicStatus(); }
  }, 1000);
}
