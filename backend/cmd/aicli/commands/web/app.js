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
  var sidebarEl = document.getElementById("sidebar");
  var sidebarToggleBtn = document.getElementById("sidebar-toggle");
  var sidebarCollapseBtn = document.getElementById("sidebar-collapse-btn");
  var sessionsRefreshBtn = document.getElementById("sessions-refresh-btn");
  var sessionsSortEl = document.getElementById("sessions-sort");
  var sessionListEl = document.getElementById("session-list");

  var pendingApprovalRequestID = null;
  var pendingQuestionID = null;
  var lastSequence = 0;
  var sessions = [];               // 会话列表缓存（GET /web/api/sessions）
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

  // ---- 发送/停止按钮状态机 ----
  // idle        就绪：按钮为「发送」，输入为空时禁用
  // posting     POST 已发出，等待队列确认：按钮为「发送中…」禁用（脉冲）
  // busy        turn 正在执行：按钮变为「停止」（可点击中断）
  // interrupting 停止信号已发出，等待会话复位：按钮为「正在停止…」禁用
  var uiState = "idle";
  var uiResetTimer = null; // interrupting 超时保护

  // setUI 切换状态机并刷新按钮外观与禁用态。
  function setUI(state, statusText) {
    if (state !== "interrupting") { clearTimeout(uiResetTimer); }
    uiState = state;
    if (statusText !== undefined) { sendStatusEl.textContent = statusText; }
    renderButton();
    if (state === "interrupting") {
      // 兜底：若 10s 内未收到 session_interrupted / turn_end 复位事件
      //（SSE 断线 / 事件丢失 / 与 turn_end 竞态），强制回到 idle，
      // 避免按钮永久卡在「正在停止…」。
      uiResetTimer = setTimeout(function () {
        if (uiState === "interrupting") { setUI("idle", "已停止"); }
      }, 10000);
    }
  }

  function renderButton() {
    switch (uiState) {
      case "posting":
        sendBtn.textContent = "发送中…";
        sendBtn.className = "sending-btn";
        sendBtn.disabled = true;
        break;
      case "busy":
        sendBtn.textContent = "停止";
        sendBtn.className = "stop-btn";
        sendBtn.disabled = false;
        break;
      case "interrupting":
        sendBtn.textContent = "正在停止…";
        sendBtn.className = "stop-btn sending-btn";
        sendBtn.disabled = true;
        break;
      default: // idle
        sendBtn.textContent = "发送";
        sendBtn.className = "";
        sendBtn.disabled = !promptEl.value.trim();
        break;
    }
  }
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

  // ---- 精简 Markdown 解析器 ----
  // 将 Markdown 文本转换为安全的 HTML。
  // 支持：**粗体**、`行内代码`、```代码块```、# 标题、- 无序列表、[链接](url)
  function renderMarkdown(text) {
    if (!text) return "";
    var html = esc(text);
    // 代码块（```...```）
    html = html.replace(/```(\w*)\n?([\s\S]*?)```/g, function (_, lang, code) {
      var langClass = lang ? ' class="lang-' + lang + '"' : '';
      return '<pre><code' + langClass + '>' + esc(code.trim()) + '</code></pre>';
    });
    // 行内代码（`...`）
    html = html.replace(/`([^`]+)`/g, '<code>$1</code>');
    // 标题（# ～ ######）
    html = html.replace(/^###### (.+)$/gm, '<h6>$1</h6>');
    html = html.replace(/^##### (.+)$/gm, '<h5>$1</h5>');
    html = html.replace(/^#### (.+)$/gm, '<h4>$1</h4>');
    html = html.replace(/^### (.+)$/gm, '<h3>$1</h3>');
    html = html.replace(/^## (.+)$/gm, '<h2>$1</h2>');
    html = html.replace(/^# (.+)$/gm, '<h1>$1</h1>');
    // 粗体（**...**）
    html = html.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
    // 链接 [text](url)
    html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
    // 无序列表（- 或 * 开头）
    html = html.replace(/^[*-] (.+)$/gm, '<li>$1</li>');
    // 将连续 <li> 包裹为 <ul>
    html = html.replace(/(<li>.*<\/li>)\n<li>/g, '$1\n<li>');
    html = html.replace(/(<li>.*<\/li>)/g, function (m) {
      if (m.indexOf('<ul>') === -1) return '<ul>' + m + '</ul>';
      return m;
    });
    // 换行转 <br>
    html = html.replace(/\n/g, '<br>');
    // 修复 <li><br> 为 <li>
    html = html.replace(/<li><br>/g, '<li>');
    return html;
  }

  function renderStream() {
    var parts = [];
    if (streamReasoningRevealed > 0) {
      var reasoningHtml = renderMarkdown(streamReasoning.substring(0, streamReasoningRevealed));
      parts.push('<details class="reasoning-block" open>');
      parts.push('<summary>推理过程</summary>');
      parts.push('<div class="reasoning-content">' + reasoningHtml + '</div>');
      parts.push('</details>');
    }
    if (streamTool) {
      parts.push('<div class="stream-tool">' + esc(streamTool) + '</div>');
    }
    var displayed = streamText.substring(0, streamRevealed);
    if (displayed) {
      var mdHtml = renderMarkdown(displayed);
      parts.push('<div class="stream-text">' + mdHtml + '</div>');
      // 光标闪烁（仅在未揭示完时）
      if (streamRevealed < streamText.length) {
        parts.push('<span class="tw-cursor"></span>');
      }
    }
    if (!streamMsgEl) return;
    streamMsgEl.innerHTML = parts.length ? parts.join("\n") : '思考中…<span class="tw-cursor"></span>';
    streamMsgEl.scrollIntoView(false);
  }  function beginStream() {
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
    streamMsgEl.innerHTML = '思考中…<span class="tw-cursor"></span>';
    streamMsgEl.scrollIntoView(false);
    // 并行加载对话历史（异步，不影响流式消息渲染）
    refreshScreen();
    startTypeTimer();
  }

  // 打字机揭示完成后收尾：隐藏流式消息，刷新权威屏幕内容（含新回合）。
  function finishStream() {
    streamActive = false;
    streamEnded = false;
    // 把流式累积文本持久化到屏幕区，作为无 surface 时的会话历史。
    // 有 surface 时 refreshScreen() 会覆盖为权威屏幕快照（含全部历史）。
    // 无 surface 时追加累积，避免多轮对话互相覆盖。
    var persisted = "";
    if (streamReasoning) {
      persisted += "[reasoning]\n" + streamReasoning + "\n";
    }
    if (streamText) {
      persisted += streamText;
    }
    if (persisted) {
      var existing = screenEl.textContent;
      var prefix = (existing && existing !== "(empty)") ? existing + "\n\n" : "";
      screenEl.textContent = prefix + persisted;
    }
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
  }function refreshScreen() {
    fetch("/web/api/screen?format=json", { cache: "no-store" })
      .then(function (res) { return res.ok ? res.json() : null; })
      .then(function (data) {
        if (!data || !data.available) {
          // 无可用屏幕快照（无 surface / 空帧）：保留 screenEl 已有内容
          // （finishStream 已写入流式累积文本），不覆盖为 "(empty)"。
          scrollToBottom();
          return;
        }
        // 权威屏幕内容可用：覆盖显示，隐藏 #stream-msg（由终端渲染驱动）。
        screenEl.textContent = data.text || "";
        if (streamMsgEl) { streamMsgEl.style.display = "none"; }
        scrollToBottom();
      })
      .catch(function (err) {
        // 网络错误：保留现有内容，不覆盖。
        console.error("screen fetch failed:", err);
      });
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
    approvalPrompt.innerHTML = renderMarkdown((data && data.prompt) || "");
    questionSuggestionsEl.innerHTML = "";
    approveBtn.style.display = "";
    denyBtn.style.display = "";
    approvalPanel.style.display = "block";
  }

  function showQuestion(data) {
    pendingQuestionID = (data && data.question_id) || null;
    pendingApprovalRequestID = null;
    approvalTitle.textContent = "问题: " + (pendingQuestionID || "?");
    approvalPrompt.innerHTML = renderMarkdown((data && data.prompt) || "");
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
  function sendInput(payload) {
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
          if (uiState === "busy") {
            // 执行中排队（Enter 键入下一条）：保持 busy，不改变按钮角色
            sendStatusEl.textContent = "已排队，将在当前任务后执行…";
          } else {
            setUI("posting", "已排队，等待执行…");
          }
        } else if (json.status === "interrupted") {
          // 竞态防御：turn_end 可能已先行到达（此时已是 idle）。
          // 保持 idle 并给出提示，避免按钮退回「正在停止…」卡住。
          if (uiState === "idle") {
            sendStatusEl.textContent = "已停止";
          } else {
            setUI("interrupting", "正在停止…");
          }
        } else if (json.status === "resolved") {
          sendStatusEl.textContent = "已提交";
        } else {
          if (uiState === "busy") {
            sendStatusEl.textContent = "排队失败: " + (json.reason || json.status);
          } else {
            setUI("idle", "失败: " + (json.reason || json.status));
          }
        }
      })
      .catch(function (err) {
        if (isInterrupt) {
          setUI("busy", "停止失败: " + err);
        } else if (uiState === "busy") {
          sendStatusEl.textContent = "发送失败: " + err;
        } else {
          setUI("idle", "发送失败: " + err);
        }
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
  function renderSessionList() {
    if (!sessionListEl) { return; }
    var items = sessions || [];
    sessionListEl.innerHTML = "";
    if (!items.length) {
      var empty = document.createElement("div");
      empty.className = "session-empty";
      empty.textContent = "暂无历史会话";
      sessionListEl.appendChild(empty);
      return;
    }
    items.forEach(function (s) {
      var item = document.createElement("button");
      item.type = "button";
      item.className = "session-item" + (s.current ? " active" : "");
      item.title = s.id;
      var title = document.createElement("span");
      title.className = "session-title";
      title.textContent = s.title || "(未命名会话)";
      var meta = document.createElement("span");
      meta.className = "session-meta";
      var bits = [];
      if (s.current) { bits.push('<span class="session-current">● 当前</span>'); }
      if (typeof s.message_count === "number") { bits.push(s.message_count + " 条消息"); }
      // 时间戳跟随当前排序键：按创建时间排时显示创建时间，按更新时间排时显示更新时间。
      var ts = sessionsSort === "updated_at"
        ? fmtSessionTime(s.updated_at || s.created_at)
        : fmtSessionTime(s.created_at || s.updated_at);
      if (ts) { bits.push(ts); }
      meta.innerHTML = bits.join(" · ");
      item.appendChild(title);
      item.appendChild(meta);
      item.addEventListener("click", function () { resumeSession(s.id); });
      sessionListEl.appendChild(item);
    });
  }

  // 拉取会话列表（GET /web/api/sessions）
  function loadSessions() {
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
                    renderSessionList();
                    refreshScreen();
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
        if (data.session_busy) {
          beginStream();
          setUI("busy", "执行中…");
        } else {
          refreshScreen();
          // 若正在等待自己刚发送的 prompt 的 turn_start，保持 posting
          if (uiState !== "posting") { setUI("idle", ""); }
        }
        loadSessions(); // 重连后刷新会话列表
        break;
      case "turn_start":
        setTurn("处理中 " + (data.model ? "(" + data.model + ")" : ""));
        setUI("busy", "执行中…");
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
        setUI("idle", "");
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
        loadSessions();
        endStream();
        refreshScreen();
        break;
      case "error":
        setUI("idle", "发生错误");
        refreshScreen();
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
    // 状态机 dispatch
    if (uiState === "busy") {
      // 执行中：按钮变为「停止」→ 发中断信号
      sendInput({ type: "interrupt" });
      return;
    }
    if (uiState === "posting" || uiState === "interrupting") {
      return; // 等待中，不重复触发
    }
    // idle 状态
    var text = promptEl.value.trim();
    if (!text) { return; }
    if (pendingQuestionID) {
      if (!text) { sendStatusEl.textContent = "请输入回答"; return; }
      sendQuestionAnswer(text);
      promptEl.value = "";
      return;
    }
    if (approvalPanel.style.display === "block" && pendingApprovalRequestID) {
      sendStatusEl.textContent = "请使用允许/拒绝按钮";
      return;
    }
    setUI("posting", "发送中…");
    sendInput({ prompt: text });
  });

  promptEl.addEventListener("input", function () {
    if (uiState === "idle") { renderButton(); }
  });

  promptEl.addEventListener("keydown", function (e) {
    if (e.key === "ArrowUp" && !e.altKey) {
      // 输入历史：向上浏览（仅在 idle 或 busy 排队场景）
      if (uiState === "posting" || uiState === "interrupting") { return; }
      if (!inputHistory.length) { return; }
      e.preventDefault();
      if (inputHistoryIdx === -1) { inputHistoryIdx = inputHistory.length - 1; }
      else if (inputHistoryIdx > 0) { inputHistoryIdx--; }
      promptEl.value = inputHistory[inputHistoryIdx];
      return;
    }
    if (e.key === "ArrowDown" && !e.altKey) {
      if (uiState === "posting" || uiState === "interrupting") { return; }
      if (inputHistoryIdx === -1) { return; }
      e.preventDefault();
      if (inputHistoryIdx < inputHistory.length - 1) { inputHistoryIdx++; promptEl.value = inputHistory[inputHistoryIdx]; }
      else { inputHistoryIdx = -1; promptEl.value = ""; }
      return;
    }
    if (e.key === "Escape") {
      // Esc：执行中触发中断（与「停止」按钮同路径）
      if (uiState === "busy") {
        e.preventDefault();
        sendInput({ type: "interrupt" });
      }
      return;
    }
    if (e.key !== "Enter") { return; }
    if (uiState === "posting" || uiState === "interrupting") { return; }
    if (uiState === "busy") {
      // 执行中：Enter 排队下一条消息（不中断当前任务）；按钮点击才触发停止。
      var text = promptEl.value.trim();
      if (!text) { return; }
      if (pendingQuestionID) {
        sendQuestionAnswer(text);
        promptEl.value = "";
        return;
      }
      if (approvalPanel.style.display === "block" && pendingApprovalRequestID) {
        sendStatusEl.textContent = "请使用允许/拒绝按钮";
        return;
      }
      sendInput({ prompt: text });
      return;
    }
    sendBtn.click();
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

  // ---- 侧边栏按钮 ----
  if (sidebarToggleBtn) {
    sidebarToggleBtn.addEventListener("click", function () { setSidebarCollapsed(!sidebarCollapsed); });
  }
  if (sidebarCollapseBtn) {
    sidebarCollapseBtn.addEventListener("click", function () { setSidebarCollapsed(true); });
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

  // 底部栏显示完整 URL
  var origin = window.location.origin;
  document.getElementById("footer-endpoints").innerHTML = '<a href="' + origin + '/debug/endpoints" target="_blank" rel="noopener">' + origin + '/debug/endpoints</a>';
  document.getElementById("footer-web").innerHTML = '<a href="' + origin + '/web/" target="_blank" rel="noopener">' + origin + '/web/</a>';

  openEventSource();
  refreshScreen();
  loadSessions();
  renderButton(); // 初始按钮状态（idle：输入为空时禁用）
})();