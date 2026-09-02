package commands

// chatWebPageHTML 是微型 Web 客户端页面（§4.2.1）。
// 全部内联，无外部依赖；同源提供，无需 CORS。
const chatWebPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>aicli micro web client</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body {
    margin: 0; padding: 12px;
    background: #101418; color: #d7dce0;
    font-family: "Cascadia Code", Consolas, Menlo, monospace;
    font-size: 13px; line-height: 1.5;
    display: flex; flex-direction: column; height: 100vh;
  }
  header { display: flex; align-items: center; gap: 12px; padding-bottom: 8px; border-bottom: 1px solid #2a3138; }
  h1 { font-size: 14px; margin: 0; font-weight: 600; }
  #connection-status { font-size: 12px; padding: 2px 8px; border-radius: 10px; background: #333; }
  #connection-status.connected { background: #1d4d2b; color: #8ff0a4; }
  #connection-status.disconnected { background: #54211f; color: #ffb4a8; }
  #turn-status { margin-left: auto; font-size: 12px; color: #9aa7b0; }
  #conversation {
    flex: 1; min-height: 0; overflow: auto;
    margin: 8px 0;
  }
  #screen {
    margin: 0 0 8px 0;
    padding: 10px; white-space: pre-wrap; word-break: break-word;
    background: #0b0e11; border: 1px solid #2a3138; border-radius: 6px;
  }
  .tabs { display: flex; gap: 6px; margin-top: 8px; border-bottom: 1px solid #2a3138; }
  .tab-btn {
    padding: 5px 16px; cursor: pointer;
    background: #16222d; color: #9aa7b0;
    border: 1px solid #2a3138; border-bottom: none;
    border-radius: 6px 6px 0 0;
    font-family: inherit; font-size: 12px;
  }
  .tab-btn:hover { background: #20354a; color: #c9d6e4; }
  .tab-btn.active { background: #22303c; color: #d7dce0; border-color: #3d5570; }
  .tab-panel { display: none; flex: 1; min-height: 0; flex-direction: column; }
  .tab-panel.active { display: flex; }
  #approval-panel {
    display: none; padding: 8px 10px; margin-bottom: 8px;
    background: #1e2630; border: 1px solid #3d5570; border-radius: 6px;
  }
  #approval-panel .prompt { margin: 4px 0 8px; color: #c9d6e4; }
  #approval-panel button { margin-right: 8px; }
  #question-suggestions { margin: 4px 0 8px; }
  #question-suggestions .suggestion-btn {
    display: inline-block; margin: 2px 6px 2px 0;
    padding: 4px 10px; cursor: pointer;
    background: #16222d; color: #a8c8e0;
    border: 1px solid #3d5570; border-radius: 12px;
    font-family: inherit; font-size: 12px;
  }
  #question-suggestions .suggestion-btn:hover { background: #20354a; }
  #input-row { display: flex; gap: 8px; }
  #prompt {
    flex: 1; padding: 8px 10px;
    background: #0b0e11; color: #d7dce0;
    border: 1px solid #2a3138; border-radius: 6px;
    font-family: inherit; font-size: 13px;
  }
  button {
    padding: 8px 16px; cursor: pointer;
    background: #22303c; color: #d7dce0;
    border: 1px solid #3d5570; border-radius: 6px;
    font-family: inherit; font-size: 13px;
  }
  button:hover { background: #2c3d4d; }
  #send-status { font-size: 12px; color: #9aa7b0; min-width: 60px; align-self: center; }
  #event-log { flex: 1; overflow: auto; margin-top: 8px; font-size: 11px; color: #7c8890; }
  #event-log .ev-line { padding: 1px 0; border-bottom: 1px solid #1c232b; }
  #event-log .ev-name { color: #6aa0c0; }
  #log-toolbar { display: flex; align-items: center; gap: 10px; margin-top: 8px; }
  #log-toolbar button { padding: 3px 12px; font-size: 11px; }
  #log-count { font-size: 11px; color: #7c8890; }
  .tw-cursor { display: inline-block; width: 8px; height: 1em;
    background: #8ff0a4; vertical-align: text-bottom;
    animation: tw-blink 1s step-start infinite; }
  #stream-msg {
    flex: 0 0 auto; margin: 0 0 8px 0;
    padding: 10px; white-space: pre-wrap; word-break: break-word;
    background: #0b0e11; border: 1px solid #2a3138; border-radius: 6px;
    font-family: inherit; font-size: 13px; line-height: 1.5;
    display: none;
  }
  @keyframes tw-blink { 50% { opacity: 0; } }
  .stream-reasoning { color: #a89a6a; font-style: italic; }
  .stream-tool { color: #6aa0c0; }
  #footer {
    margin-top: 8px; padding-top: 6px;
    border-top: 1px solid #2a3138;
    display: flex; gap: 16px; font-size: 11px; color: #6a7a85;
  }
  #footer a { color: #6aa0c0; text-decoration: none; }
  #footer a:hover { text-decoration: underline; color: #8ac0e0; }
  #footer .sep { color: #3a4a55; }
</style>
</head>
<body>
<header>
  <h1>aicli micro web client</h1>
  <span id="connection-status">connecting</span>
  <span id="turn-status">-</span>
</header>

<div id="approval-panel">
  <div id="approval-title">待处理请求</div>
  <div id="approval-prompt" class="prompt"></div>
  <div id="question-suggestions"></div>
  <button id="approve-btn">允许</button>
  <button id="deny-btn">拒绝</button>
</div>

<div class="tabs">
  <button class="tab-btn active" id="tab-main-btn" type="button">对话</button>
  <button class="tab-btn" id="tab-log-btn" type="button">日志</button>
</div>

<div id="tab-main" class="tab-panel active">
  <div id="conversation">
    <pre id="screen">(empty)</pre>
    <div id="stream-msg"></div>
  </div>
  <div id="input-row">
    <input id="prompt" type="text" placeholder="输入消息后回车发送…" autocomplete="off">
    <button id="send-btn">发送</button>
    <span id="send-status"></span>
  </div>
</div>

<div id="tab-log" class="tab-panel">
  <div id="log-toolbar">
    <button id="log-clear-btn" type="button">清空</button>
    <span id="log-count">0 条事件</span>
  </div>
  <div id="event-log"></div>
</div>

<div id="footer">
  <span id="footer-endpoints"><a href="/debug/endpoints" target="_blank" rel="noopener">/debug/endpoints</a></span>
  <span class="sep">|</span>
  <span id="footer-web"><a href="/web/" target="_blank" rel="noopener">/web/</a></span>
</div>

<script>
(function () {
  "use strict";

  var screenEl = document.getElementById("screen");
  var statusEl = document.getElementById("connection-status");
  var turnEl = document.getElementById("turn-status");
  var promptEl = document.getElementById("prompt");
  var sendBtn = document.getElementById("send-btn");
  var sendStatusEl = document.getElementById("send-status");
  var eventLogEl = document.getElementById("event-log");
  var tabMainBtn = document.getElementById("tab-main-btn");
  var tabLogBtn = document.getElementById("tab-log-btn");
  var tabMainEl = document.getElementById("tab-main");
  var tabLogEl = document.getElementById("tab-log");
  var approvalPanel = document.getElementById("approval-panel");
  var approvalPrompt = document.getElementById("approval-prompt");
  var approvalTitle = document.getElementById("approval-title");
  var approveBtn = document.getElementById("approve-btn");
  var denyBtn = document.getElementById("deny-btn");
  var questionSuggestionsEl = document.getElementById("question-suggestions");

  var pendingApprovalRequestID = null;
  var pendingQuestionID = null;
  var lastSequence = 0;

  // ---- 打字机状态（实时逐字揭示） ----
  var streamActive = false;          // turn_start → turn_end
  var streamEnded = false;           // turn_end 已到达，等待打字机揭示完成
  var streamReasoning = "";          // 推理文本（完整累积）
  var streamText = "";               // 助手文本（完整累积）
  var streamTool = "";               // 当前工具指示
  var streamRevealed = 0;            // streamText 已显示的字符数
  var streamReasoningRevealed = 0;   // streamReasoning 已显示行数
  var typeTimer = null;              // 打字机定时器句柄
  var streamMsgEl = null;            // 流式消息容器（screen 下方追加，信息流模式）
  var TYPE_SPEED = 20;               // 每字符间隔（毫秒），越小越快
  var TYPE_CHARS_PER_TICK = 1;       // 每 tick 揭示字符数

  function startTypeTimer() {
    if (typeTimer) return;
    typeTimer = setInterval(function () {
      var changed = false;
      // 推理文本：逐段揭示
      if (streamReasoningRevealed < streamReasoning.length) {
        var rRemaining = streamReasoning.length - streamReasoningRevealed;
        // 大段推理提速：保证最多约 5 秒内揭示完
        streamReasoningRevealed = Math.min(streamReasoningRevealed + Math.max(10, Math.ceil(rRemaining / 250)), streamReasoning.length);
        changed = true;
      }
      // 助手文本：逐字揭示
      if (streamRevealed < streamText.length) {
        var tRemaining = streamText.length - streamRevealed;
        // 大文本提速：保证最多约 5 秒内揭示完
        var perTick = Math.max(TYPE_CHARS_PER_TICK, Math.ceil(tRemaining / 250));
        streamRevealed = Math.min(streamRevealed + perTick, streamText.length);
        changed = true;
      }
      if (changed) renderStream();
      // 队列排空后停止定时器（节省资源）；若 turn 已结束则收尾
      if (streamRevealed >= streamText.length && streamReasoningRevealed >= streamReasoning.length) {
        stopTypeTimer();
        if (streamEnded) { finishStream(); }
      }
    }, TYPE_SPEED);
  }

  function stopTypeTimer() {
    if (typeTimer) {
      clearInterval(typeTimer);
      typeTimer = null;
    }
  }

  // 转义 HTML 实体，防止流式文本中的 < > & 破坏 innerHTML 渲染。
  function esc(s) {
    return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  function renderStream() {
    var parts = [];
    if (streamReasoningRevealed > 0) {
      parts.push("[思考]");
      parts.push(esc(streamReasoning.substring(0, streamReasoningRevealed)));
      parts.push("[/思考]");
    }
    if (streamTool) { parts.push(esc(streamTool)); }
    var displayed = esc(streamText.substring(0, streamRevealed));
    if (displayed) {
      parts.push(displayed);
      // 光标闪烁（仅在未揭示完时）
      if (streamRevealed < streamText.length) {
        parts[parts.length - 1] = displayed + "<span class=\"tw-cursor\"></span>";
      }
    }
    if (!streamMsgEl) return;
    streamMsgEl.innerHTML = parts.length ? parts.join("\n") : "思考中…<span class=\"tw-cursor\"></span>";
    streamMsgEl.scrollIntoView(false);
  }

  function beginStream() {
    streamActive = true;
    streamEnded = false;
    streamReasoning = "";
    streamReasoningRevealed = 0;
    streamText = "";
    streamRevealed = 0;
    streamTool = "";
    // 确保 streamMsgEl 引用有效，并显示在 screen 下方（信息流模式：不覆盖历史）
    if (!streamMsgEl) {
      streamMsgEl = document.getElementById("stream-msg");
    }
    streamMsgEl.style.display = "block";
    streamMsgEl.innerHTML = "思考中…<span class=\"tw-cursor\"></span>";
    streamMsgEl.scrollIntoView(false);
    // 并行加载对话历史（异步，不影响流式消息渲染）
    refreshScreen();
    startTypeTimer();
  }

  // 打字机揭示完成后收尾：隐藏流式消息，刷新权威屏幕内容（含新回合）。
  function finishStream() {
    streamActive = false;
    streamEnded = false;
    if (streamMsgEl) { streamMsgEl.style.display = "none"; }
    refreshScreen();
  }

  function endStream() {
    // turn_end：不再有增量。不强制揭示——保留已缓冲文本，
    // 由打字机定时器继续逐字揭示完剩余部分后调用 finishStream()。
    streamEnded = true;
    if (!typeTimer) {
      if (streamRevealed >= streamText.length && streamReasoningRevealed >= streamReasoning.length) {
        // 无待揭示内容，直接收尾
        finishStream();
      } else {
        // 异常兜底：有未揭示内容但定时器未运行，重新启动它
        startTypeTimer();
      }
    }
    // 定时器运行中：由定时器 tick 检测揭示完成后调用 finishStream()
  }

  function activateTab(tabName) {
    var isMain = tabName === "main";
    tabMainBtn.classList.toggle("active", isMain);
    tabLogBtn.classList.toggle("active", !isMain);
    tabMainEl.classList.toggle("active", isMain);
    tabLogEl.classList.toggle("active", !isMain);
    if (isMain) { refreshScreen(); }
  }

  function setStatus(text, connected) {
    statusEl.textContent = text;
    statusEl.className = connected ? "connected" : "disconnected";
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

  function refreshScreen() {
    fetch("/web/api/screen?format=text", { cache: "no-store" })
      .then(function (res) { return res.ok ? res.text() : "ERROR " + res.status; })
      .then(function (text) { screenEl.textContent = text; scrollToBottom(); })
      .catch(function (err) { screenEl.textContent = "screen fetch failed: " + err; });
  }

  function scrollToBottom() {
    var conv = document.getElementById("conversation");
    if (conv) { conv.scrollTop = conv.scrollHeight; }
  }

  function hideApproval() {
    approvalPanel.style.display = "none";
    pendingApprovalRequestID = null;
    pendingQuestionID = null;
    questionSuggestionsEl.innerHTML = "";
    approveBtn.style.display = "";
    denyBtn.style.display = "";
  }

  function showApproval(data) {
    pendingApprovalRequestID = (data && data.request_id) || null;
    pendingQuestionID = null;
    approvalTitle.textContent = "待审批工具: " + (data && data.tool_name || "?");
    approvalPrompt.textContent = (data && data.prompt) || "";
    questionSuggestionsEl.innerHTML = "";
    approveBtn.style.display = "";
    denyBtn.style.display = "";
    approvalPanel.style.display = "block";
  }

  function showQuestion(data) {
    pendingQuestionID = (data && data.question_id) || null;
    pendingApprovalRequestID = null;
    approvalTitle.textContent = "问题: " + (pendingQuestionID || "?");
    approvalPrompt.textContent = (data && data.prompt) || "";
    approveBtn.style.display = "none";
    denyBtn.style.display = "none";
    // 建议项渲染为可点击按钮，点击即作为回答提交。
    questionSuggestionsEl.innerHTML = "";
    var suggestions = (data && data.suggestions) || [];
    if (typeof suggestions === "string") { suggestions = [suggestions]; }
    suggestions.forEach(function (s) {
      var btn = document.createElement("button");
      btn.className = "suggestion-btn";
      btn.textContent = s;
      btn.addEventListener("click", function () {
        sendQuestionAnswer(s);
      });
      questionSuggestionsEl.appendChild(btn);
    });
    approvalPanel.style.display = "block";
  }

  function sendQuestionAnswer(answer) {
    if (!pendingQuestionID) { return; }
    var qid = pendingQuestionID;
    sendInput({ type: "question_answer", question_id: qid, answer: answer });
    hideApproval();
  }

  function sendInput(payload) {
    sendStatusEl.textContent = "发送中…";
    fetch("/web/api/input", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    })
      .then(function (res) { return res.json().catch(function () { return { status: "error", reason: "bad response" }; }); })
      .then(function (json) {
        if (json.status === "queued" || json.status === "resolved") {
          sendStatusEl.textContent = "已发送";
          promptEl.value = "";
        } else {
          sendStatusEl.textContent = "失败: " + (json.reason || json.status);
        }
      })
      .catch(function (err) { sendStatusEl.textContent = "失败: " + err; });
  }

  // 屏幕刷新策略（§8.6 方法二）：关键事件后主动拉取屏幕内容。
  var refreshKeys = { "turn_end": 1, "tool_end": 1, "session_end": 1, "session_interrupted": 1, "error": 1, "screen_refresh": 1, "compact_end": 1 };

  function onSSEEvent(eventName, data) {
    logEvent(eventName, data);
    lastSequence = (data && data._event && data._event.sequence) || lastSequence;

    switch (eventName) {
      case "connected":
        setStatus(data.session_active ? "已连接" : "已连接(无会话)", true);
        setTurn(data.session_busy ? "忙碌 turn=" + (data.turn_id || "?") : "就绪");
        if (data.pending_approval) { showApproval(data.pending_approval); }
        if (data.pending_question) { showQuestion(data.pending_question); }
        if (data.session_busy) { beginStream(); } else { refreshScreen(); }
        break;
      case "turn_start":
        setTurn("处理中 " + (data.model ? "(" + data.model + ")" : ""));
        beginStream();
        break;
      case "reasoning_delta":
        if (streamActive && data.content) {
          streamReasoning += data.content;
          startTypeTimer(); // 打字机定时器逐段揭示
        }
        break;
      case "assistant_delta":
        if (streamActive && data.text) {
          streamText += data.text;
          startTypeTimer(); // 打字机定时器逐字揭示
        }
        break;
      case "assistant_message":
        if (streamActive) {
          streamText = data.content || streamText;
          startTypeTimer();
        }
        break;
      case "tool_start":
        if (streamActive) {
          streamTool = "[工具: " + (data.tool_name || "?") + " 执行中…]";
          renderStream();
        }
        break;
      case "tool_end":
        if (streamActive) {
          streamTool = "[工具: " + (data.tool_name || "?") + " 完成]";
          renderStream();
        }
        break;
      case "turn_end":
        setTurn("就绪");
        endStream();
        break;
      case "approval_requested":
        showApproval(data);
        break;
      case "approval_resolved":
        hideApproval();
        if (!streamActive) { refreshScreen(); }
        break;
      case "question_asked":
        showQuestion(data);
        break;
      case "question_answered":
        hideApproval();
        break;
      default:
        if (refreshKeys[eventName] && !streamActive) { refreshScreen(); }
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
     "assistant_delta", "tool_start", "tool_end", "approval_requested",
     "approval_resolved", "question_asked", "question_answered"].forEach(function (name) {
      es.addEventListener(name, function (e) {
        var data = {};
        try { data = JSON.parse(e.data); } catch (err) { /* ignore */ }
        onSSEEvent(name, data);
      });
    });
  }

  tabMainBtn.addEventListener("click", function () { activateTab("main"); });
  tabLogBtn.addEventListener("click", function () { activateTab("log"); });

  var logClearBtn = document.getElementById("log-clear-btn");
  if (logClearBtn) {
    logClearBtn.addEventListener("click", function () {
      eventLogEl.innerHTML = "";
      var countEl = document.getElementById("log-count");
      if (countEl) { countEl.textContent = "0 条事件"; }
    });
  }

  sendBtn.addEventListener("click", function () {
    var text = promptEl.value.trim();
    if (!text) { return; }
    if (pendingQuestionID) {
      // 有未回答的问题：输入框内容作为回答提交。
      if (!text) { sendStatusEl.textContent = "请输入回答"; return; }
      sendQuestionAnswer(text);
      promptEl.value = "";
      return;
    }
    if (approvalPanel.style.display === "block" && pendingApprovalRequestID) {
      // 审批模式下普通发送被拒绝；必须用允许/拒绝按钮。
      sendStatusEl.textContent = "请使用允许/拒绝按钮";
      return;
    }
    sendInput({ prompt: text });
  });

  promptEl.addEventListener("keydown", function (e) {
    if (e.key === "Enter") { sendBtn.click(); }
  });

  approveBtn.addEventListener("click", function () {
    if (pendingApprovalRequestID) {
      sendInput({ type: "approval", request_id: pendingApprovalRequestID, allow: true });
      hideApproval();
    }
  });

  denyBtn.addEventListener("click", function () {
    if (pendingApprovalRequestID) {
      sendInput({ type: "approval", request_id: pendingApprovalRequestID, allow: false });
      hideApproval();
    }
  });

  // 底部栏显示完整 URL
  var origin = window.location.origin;
  document.getElementById("footer-endpoints").innerHTML = '<a href="' + origin + '/debug/endpoints" target="_blank" rel="noopener">' + origin + '/debug/endpoints</a>';
  document.getElementById("footer-web").innerHTML = '<a href="' + origin + '/web/" target="_blank" rel="noopener">' + origin + '/web/</a>';

  openEventSource();
  refreshScreen();
})();
</script>
</body>
</html>
`