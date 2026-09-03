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
  var approvalOverlay = document.getElementById("approval-overlay");
  var approvalModalTitle = document.getElementById("approval-modal-title");
  var approvalPrompt = document.getElementById("approval-prompt");
  var approvalDetail = document.getElementById("approval-detail");
  var detailToggleBtn = document.getElementById("detail-toggle");
  var approvalModalCloseBtn = document.getElementById("approval-modal-close");
  var approveBtn = document.getElementById("approve-btn");
  var denyBtn = document.getElementById("deny-btn");
  var questionSuggestionsEl = document.getElementById("question-suggestions");
  var sidebarEl = document.getElementById("sidebar");
  var sidebarToggleBtn = document.getElementById("sidebar-toggle");
  var sidebarCollapseBtn = document.getElementById("sidebar-collapse-btn");
  var sessionsNewBtn = document.getElementById("sessions-new-btn");
  var sessionsRefreshBtn = document.getElementById("sessions-refresh-btn");
  var sessionsSortEl = document.getElementById("sessions-sort");
  var sessionListEl = document.getElementById("session-list");
  var themeToggleBtn = document.getElementById("theme-toggle");
  var sessionSearchEl = document.getElementById("session-search");
  var welcomeEl = document.getElementById("welcome");
  var toastContainer = document.getElementById("toast-container");
  var shortcutHelpEl = document.getElementById("shortcut-help");
  var scrollBottomBtn = document.getElementById("scroll-bottom-btn");
  var screenCopyBtn = document.getElementById("screen-copy-btn");
  var conversationEl = document.getElementById("conversation");

  var pendingApprovalRequestID = null;
  var pendingQuestionID = null;
  var lastSequence = 0;
  var userScrolledAway = false;    // 用户上滚阅读历史：暂停自动跟随
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

  // ---- 主题：深色/浅色（localStorage 记忆，默认跟随系统） ----
  var themeMode = "auto"; // auto | light | dark
  try {
    var savedTheme = localStorage.getItem("webTheme");
    if (savedTheme === "light" || savedTheme === "dark" || savedTheme === "auto") { themeMode = savedTheme; }
  } catch (e) { /* ignore */ }

  function applyTheme() {
    var root = document.documentElement;
    // 显式深/浅：设置 class；auto：跟随系统（由 CSS prefers-color-scheme 决定）
    root.classList.toggle("light", themeMode === "light");
    root.classList.toggle("dark", themeMode === "dark");
    if (themeToggleBtn) {
      themeToggleBtn.textContent = themeMode === "light" ? "☀" : (themeMode === "dark" ? "☾" : "◐");
      themeToggleBtn.title = "主题: " + themeMode + "（点击切换，Ctrl+L）";
    }
  }

  function toggleTheme() {
    // auto → light → dark → auto 循环
    themeMode = themeMode === "auto" ? "light" : (themeMode === "light" ? "dark" : "auto");
    try { localStorage.setItem("webTheme", themeMode); } catch (e) { /* ignore */ }
    applyTheme();
  }

  applyTheme();

  // ---- 打字机状态（实时逐字揭示） ----
  var streamActive = false;          // turn_start → turn_end
  var streamEnded = false;           // turn_end 已到达，等待打字机揭示完成
  var streamReasoning = "";          // 推理文本（完整累积）
  var streamText = "";               // 助手文本（完整累积）
  var streamTool = "";               // 当前工具指示
  var streamImages = [];             // 已渲染图像（assistant_image_progress，按 src 去重）
  var streamRevealed = 0;            // streamText 已显示的字符数
  var streamReasoningRevealed = 0;   // streamReasoning 已显示行数
  var typeTimer = null;              // 打字机定时器句柄
  var streamMsgEl = null;            // 流式消息容器（screen 下方追加，信息流模式）
  var TYPE_SPEED = 20;               // 每字符间隔（毫秒），越小越快
  var TYPE_CHARS_PER_TICK = 1;       // 每 tick 揭示字符数

  // 本地乐观回显：已发送但尚未被服务端 screen 快照确认的 user prompt。
  // 发送时立即以 pending 气泡追加到对话区；refreshScreen 收到服务端
  // messages 后按内容去重确认（服务端已包含则移除，未包含则保留 pending）。
  var localPendingPrompts = [];

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
    updateTitle();
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
  // 支持：**粗体**、~~删除线~~、`行内代码`、```代码块```、# 标题、
  //       - 无序列表、1. 有序列表、- [x] 任务列表、> 引用块、
  //       | 表格 |、[链接](url)、裸 URL 自动链接
  // 注意：所有输入先经 esc() 转义，因此这里匹配的是转义后的实体
  // （如 > 为 &gt;），保证输出安全。

  // 解析表格行（| a | b |），返回 <table> HTML；无表头分隔行时按普通行渲染。
  function renderTableBlock(blockLines) {
    function cells(row) {
      var s = row.trim();
      if (s.charAt(0) === "|") { s = s.slice(1); }
      if (s.charAt(s.length - 1) === "|") { s = s.slice(0, -1); }
      return s.split("|").map(function (c) { return c.trim(); });
    }
    function isSepRow(cs) {
      return cs.length > 0 && cs.every(function (c) {
        return /^:?-{3,}:?$/.test(c);
      });
    }
    var rows = blockLines.map(cells);
    var header = null;
    var body = rows;
    if (rows.length >= 2 && isSepRow(rows[1])) {
      header = rows[0];
      body = rows.slice(2);
    }
    function cellHtml(c, align) {
      var style = align && align !== "" ? ' style="text-align:' + align + '"' : "";
      return "<td" + style + ">" + c + "</td>";
    }
    function alignOf(c) {
      if (/^:.*:$/.test(c)) { return "center"; }
      if (/^:/.test(c)) { return "left"; }
      if (/:$/.test(c)) { return "right"; }
      return "";
    }
    var h = "";
    if (header) {
      var aligns = rows[1].map(alignOf);
      h += "<thead><tr>";
      header.forEach(function (c, i) {
        var style = aligns[i] ? ' style="text-align:' + aligns[i] + '"' : "";
        h += "<th" + style + ">" + c + "</th>";
      });
      h += "</tr></thead>";
    }
    if (body.length) {
      h += "<tbody>";
      body.forEach(function (r) {
        h += "<tr>" + r.map(function (c) { return cellHtml(c); }).join("") + "</tr>";
      });
      h += "</tbody>";
    }
    return "<table>" + h + "</table>";
  }

  function renderMarkdown(text) {
    if (!text) return "";
    var html = esc(text);
    // 代码块（```...```）→ <pre> + 复制按钮 + 语言标签 + <code>，
    // 先用占位符暂存，避免后续行级规则（表格/引用/列表/URL）误处理代码内容，
    // 全部处理完成后恢复。复制行为由 #stream-msg 上的事件委托处理。
    var codeBlocks = [];
    html = html.replace(/```(\w*)\n?([\s\S]*?)```/g, function (_, lang, code) {
      var label = lang ? '<span class="lang-label">' + lang + '</span>' : '';
      var block = '<pre><button class="copy-code-btn" type="button" title="复制代码">复制</button>'
        + label + '<code class="lang-' + (lang || 'text') + '">'
        + esc(code.trim()) + '</code></pre>';
      codeBlocks.push(block);
      return "\u0001MDC" + (codeBlocks.length - 1) + "\u0001";
    });
    // 行内代码（`...`）
    html = html.replace(/`([^`]+)`/g, '<code>$1</code>');
    // 删除线（~~text~~）
    html = html.replace(/~~([^~]+)~~/g, '<del>$1</del>');
    // 标题（# ～ ######）
    html = html.replace(/^###### (.+)$/gm, '<h6>$1</h6>');
    html = html.replace(/^##### (.+)$/gm, '<h5>$1</h5>');
    html = html.replace(/^#### (.+)$/gm, '<h4>$1</h4>');
    html = html.replace(/^### (.+)$/gm, '<h3>$1</h3>');
    html = html.replace(/^## (.+)$/gm, '<h2>$1</h2>');
    html = html.replace(/^# (.+)$/gm, '<h1>$1</h1>');
    // 粗体（**...**）
    html = html.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
    // 任务列表（- [x] / - [ ]），需在无序列表之前
    html = html.replace(/^[-*] \[([ xX])\] (.+)$/gm, function (_, checked, item) {
      return '<li class="task' + (checked !== " " ? " done" : "") + '">'
        + '<input type="checkbox" disabled' + (checked !== " " ? " checked" : "") + '> '
        + item + '</li>';
    });
    // 无序列表（- 或 * 开头）
    html = html.replace(/^[*-] (.+)$/gm, '<li>$1</li>');
    // 有序列表（1. text）
    html = html.replace(/^(\d+)\. (.+)$/gm, '<li class="li-num">$2</li>');
    // 将连续 <li> 包裹为 <ul> / <ol>（非贪婪，空行中断）
    html = html.replace(/((?:<li[^>]*>.*?<\/li>\n?)+)/g, function (m) {
      if (m.indexOf("<ul>") !== -1 || m.indexOf("<ol>") !== -1) { return m; }
      var isOrdered = m.indexOf('class="li-num"') !== -1;
      return isOrdered ? "<ol>" + m + "</ol>" : "<ul>" + m + "</ul>";
    });
    // 引用块（> 开头，连续行合并）
    html = html.replace(/(^|\n)&gt;(?:[^\n]*)(?:\n&gt;[^\n]*)*/g, function (m) {
      var lines = m.split("\n").map(function (l) {
        return l.replace(/^&gt;/, "").replace(/^ /, "");
      });
      return "\n<blockquote>" + lines.join("<br>") + "</blockquote>";
    });
    // 表格（连续 | 行块；含 |---| 分隔行则渲染表头）
    html = html.replace(/(?:^|\n)(?:\|[^\n]+\|\n?){2,}/g, function (m) {
      var lines = m.split("\n").filter(function (l) { return l.trim() !== ""; });
      // 至少两行且每行都是表格行
      var allRows = lines.every(function (l) {
        var t = l.trim();
        return t.charAt(0) === "|" && t.charAt(t.length - 1) === "|";
      });
      if (!allRows) { return m; }
      return "\n" + renderTableBlock(lines) + "\n";
    });
    // 链接 [text](url)
    html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
    // 裸 URL 自动链接（排除已生成的 <a href="..."> 属性；剥离尾部标点）
    html = html.replace(/(^|[^"'>])(https?:\/\/[^\s<]+)/g, function (m, pre, url) {
      var clean = url.replace(/[),.;:!?'"，。；：！？」』】》]+$/, "");
      var rest = url.slice(clean.length);
      return pre + '<a href="' + clean + '" target="_blank" rel="noopener">' + clean + '</a>' + rest;
    });
    // 换行转 <br>
    html = html.replace(/\n/g, '<br>');
    // 修复 <li>/<blockquote> 内尾随 <br>
    html = html.replace(/<li><br>/g, '<li>');
    html = html.replace(/<\/li><br>/g, '</li>');
    html = html.replace(/<\/blockquote><br>/g, '</blockquote>');
    // 恢复代码块
    html = html.replace(/\u0001MDC(\d+)\u0001/g, function (_, i) {
      return codeBlocks[+i] || "";
    });
    return html;
  }

  function renderStream() {
    var parts = [];
    // 图像预览（assistant_image_progress）
    streamImages.forEach(function (src) {
      parts.push('<div class="image-preview"><img src="' + esc(src) + '" alt="生成图像" loading="lazy"></div>');
    });
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
    if (!userScrolledAway) {
      streamMsgEl.scrollIntoView(false);
    }
  }

  function beginStream() {
    streamActive = true;
    streamEnded = false;
    streamReasoning = "";
    streamReasoningRevealed = 0;
    streamText = "";
    streamRevealed = 0;
    streamTool = "";
    streamImages = [];
    // 确保 streamMsgEl 引用有效，并显示在 screen 下方（信息流模式：不覆盖历史）
    if (!streamMsgEl) {
      streamMsgEl = document.getElementById("stream-msg");
    }
    streamMsgEl.style.display = "block";
    streamMsgEl.innerHTML = '思考中…<span class="tw-cursor"></span>';
    if (!userScrolledAway) {
      streamMsgEl.scrollIntoView(false);
    }
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
      // 结构化模式（服务端 messages 气泡）：以角色行追加，保留现场；
      // 纯文本回退模式（无 surface 快照）：沿用旧拼接逻辑。
      if (screenEl.querySelector(".msg-row")) {
        if (streamReasoning) {
          screenEl.insertAdjacentHTML("beforeend", chatMsgRowHtml("reasoning", streamReasoning, false));
        }
        if (streamText) {
          screenEl.insertAdjacentHTML("beforeend", chatMsgRowHtml("assistant", streamText, false));
        }
      } else {
        var existing = screenEl.textContent;
        var prefix = (existing && existing !== "(empty)") ? existing + "\n\n" : "";
        screenEl.textContent = prefix + persisted;
      }
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

  // ---- 结构化对话渲染（role-based 气泡） ----
  var MSG_LABELS = {
    user: "你",
    assistant: "aicli",
    reasoning: "推理",
    tool: "工具",
    system: "系统",
    command: "命令",
    diagnostic: "诊断",
    runtime: "事件"
  };

  // 单条消息 HTML：label（角色）+ body（内容），支持 pending 态。
  function chatMsgRowHtml(role, content, pending) {
    var label = MSG_LABELS[role] || "消息";
    var cls = "msg-row msg-" + role + (pending ? " msg-pending" : "");
    if (role === "reasoning") {
      // 推理过程：折叠面板（与流式渲染 #stream-msg .reasoning-block 视觉一致）
      return '<div class="' + cls + '">' +
        '<details class="reasoning-block">' +
        '<summary>' + esc(label) + '</summary>' +
        '<div class="reasoning-content">' + esc(content) + '</div>' +
        '</details>' +
        '</div>';
    }
    return '<div class="' + cls + '">' +
      '<div class="msg-label">' + esc(label) + '</div>' +
      '<div class="msg-body">' + esc(content) + '</div>' +
      '</div>';
  }

  // 用服务端 messages 重建对话区；未确认的本地 prompt 保留为 pending 气泡。
  function renderConversationMessages(messages) {
    if (!screenEl) { return; }
    var html = "";
    var seenUser = {};
    (messages || []).forEach(function (m) {
      var role = m.role || "assistant";
      var content = (m.content || "").replace(/\s+$/, "");
      if (role === "user") { seenUser[content] = true; }
      html += chatMsgRowHtml(role, content, false);
    });
    // 服务端尚未包含的本地 prompt → pending 气泡（发送中态），保留待确认。
    localPendingPrompts = localPendingPrompts.filter(function (text) {
      if (seenUser[text]) { return false; } // 已被服务端确认
      html += chatMsgRowHtml("user", text, true);
      return true;
    });
    screenEl.innerHTML = html || "(empty)";
  }

  // 立即追加一条本地 user pending 气泡（乐观回显，不等服务端回合）。
  function appendPendingUserPrompt(text) {
    if (!text) { return; }
    localPendingPrompts.push(text);
    if (screenEl) {
      // 首个消息时先清除占位符 "(empty)"，避免气泡混在占位文本后。
      if (screenEl.textContent === "(empty)" && !screenEl.querySelector(".msg-row")) {
        screenEl.innerHTML = "";
      }
      screenEl.insertAdjacentHTML("beforeend", chatMsgRowHtml("user", text, true));
    }
    scrollToBottom(true);
  }

  // 发送失败时移除本地 pending 气泡（释放乐观回显）。
  function dropPendingUserPrompt(text) {
    if (!text) { return; }
    localPendingPrompts = localPendingPrompts.filter(function (p) { return p !== text; });
    if (screenEl) {
      var pendingRows = screenEl.querySelectorAll(".msg-row.msg-pending");
      pendingRows.forEach(function (el) {
        var bodyEl = el.querySelector(".msg-body");
        if (bodyEl && bodyEl.textContent.trim() === text) {
          el.remove();
        }
      });
    }
  }

  // ---- provider / model / reasoning_effort 配置选择器 ----
  // 权威值来自 GET /web/api/runtime；切换动作构造 /model 命令注入
  // /web/api/input，由主循环统一执行（与 TTY 行为一致）。
  var cfg = { provider: "", model: "", reasoning: "" };
  var cfgUiDirty = false; // 切换提交后保持用户选择，等待权威确认前不重设 UI

  function cfgEls() {
    return {
      provider: document.getElementById("cfg-provider"),
      model: document.getElementById("cfg-model"),
      modelOpts: document.getElementById("cfg-model-options"),
      reasoning: document.getElementById("cfg-reasoning"),
      current: document.getElementById("cfg-current"),
      status: document.getElementById("cfg-status")
    };
  }

  // 拉取并同步权威配置到选择器。cfgUiDirty 时不重设用户正在操作的控件。
  function loadRuntimeMeta() {
    fetch("/web/api/runtime", { cache: "no-store" })
      .then(function (res) { return res.ok ? res.json() : null; })
      .then(function (meta) {
        if (!meta) { return; }
        var els = cfgEls();
        if (!els.provider || !els.model || !els.reasoning) { return; }
        var cur = meta.current || {};
        cfg.provider = cur.provider || "";
        cfg.model = cur.model || "";
        cfg.reasoning = cur.reasoning_effort || "";
        var providers = meta.providers || [];
        if (!cfgUiDirty) {
          // provider 下拉：优先已启用 provider，当前值不在列表时补一项
          var html = "";
          var hasCurrent = false;
          providers.forEach(function (p) {
            if (p.name === cfg.provider) { hasCurrent = true; }
            html += '<option value="' + esc(p.name) + '"' + (p.name === cfg.provider ? " selected" : "") + '>' + esc(p.name) + "</option>";
          });
          if (cfg.provider && !hasCurrent) {
            html = '<option value="' + esc(cfg.provider) + '" selected>' + esc(cfg.provider) + "</option>" + html;
          }
          els.provider.innerHTML = html;
          // 当前 provider 的模型 datalist
          var curProvider = null;
          providers.forEach(function (p) { if (p.name === cfg.provider) { curProvider = p; } });
          var models = curProvider ? (curProvider.models || []) : [];
          if (els.modelOpts) {
            els.modelOpts.innerHTML = models.map(function (m) {
              return '<option value="' + esc(m) + '"></option>';
            }).join("");
          }
          els.model.value = cfg.model;
          els.model.placeholder = cfg.model || (curProvider && curProvider.default_model) || "输入模型名";
          els.reasoning.value = cfg.reasoning;
        }
        els.current.textContent = (cfg.provider || "?") + " · " + (cfg.model || "?") + (cfg.reasoning ? " · " + cfg.reasoning : "");
      })
      .catch(function (err) { console.error("runtime meta fetch failed:", err); });
  }

  // 选择器变更 → 构造差异化的 /model 命令并注入。
  function applyRuntimeConfig() {
    var els = cfgEls();
    if (!els.provider || !els.model || !els.reasoning) { return; }
    var provider = els.provider.value || "";
    var model = (els.model.value || "").trim();
    var reasoning = els.reasoning.value || "";
    if (provider === cfg.provider && (model === cfg.model || model === "") && reasoning === cfg.reasoning) {
      return; // 无实际变化
    }
    var parts = ["/model"];
    if (provider && provider !== cfg.provider) { parts.push("--provider=" + provider); }
    if (model && model !== cfg.model) { parts.push("--model=" + model); }
    if (reasoning === "" && cfg.reasoning !== "") {
      parts.push("--clear-reasoning"); // 恢复默认 effort
    } else if (reasoning !== "" && reasoning !== cfg.reasoning) {
      parts.push("-r=" + reasoning);
    }
    var cmd = parts.join(" ");
    cfgUiDirty = true;
    if (els.status) {
      els.status.textContent = "切换中…";
      els.status.className = "cfg-status busy";
    }
    fetch("/web/api/input", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ prompt: cmd })
    })
      .then(function (res) { return res.json().catch(function () { return { status: "error", reason: "bad response" }; }); })
      .then(function (json) {
        if (json.status !== "queued") {
          if (els.status) {
            els.status.textContent = "提交失败: " + (json.reason || json.status);
            els.status.className = "cfg-status err";
          }
          cfgUiDirty = false;
          loadRuntimeMeta();
          return;
        }
        // 轮询权威值直到生效（命令在输入队列中稍后执行）
        pollRuntimeMeta(provider, model, reasoning, 8);
      })
      .catch(function (err) {
        if (els.status) {
          els.status.textContent = "提交失败: " + err;
          els.status.className = "cfg-status err";
        }
        cfgUiDirty = false;
        loadRuntimeMeta();
      });
  }

  function pollRuntimeMeta(targetProvider, targetModel, targetReasoning, attempts) {
    var els = cfgEls();
    if (attempts <= 0) {
      cfgUiDirty = false;
      loadRuntimeMeta();
      if (els.status) {
        els.status.textContent = "已提交（配置可能未同步）";
        els.status.className = "cfg-status";
      }
      return;
    }
    fetch("/web/api/runtime", { cache: "no-store" })
      .then(function (res) { return res.ok ? res.json() : null; })
      .then(function (meta) {
        var cur = (meta && meta.current) || {};
        var ok = cur.provider === targetProvider &&
          (targetModel === "" || cur.model === targetModel) &&
          cur.reasoning_effort === targetReasoning;
        if (ok) {
          cfgUiDirty = false;
          loadRuntimeMeta();
          if (els.status) {
            els.status.textContent = "已生效";
            els.status.className = "cfg-status ok";
            setTimeout(function () { els.status.textContent = ""; els.status.className = "cfg-status"; }, 2500);
          }
        } else {
          setTimeout(function () { pollRuntimeMeta(targetProvider, targetModel, targetReasoning, attempts - 1); }, 400);
        }
      })
      .catch(function () {
        setTimeout(function () { pollRuntimeMeta(targetProvider, targetModel, targetReasoning, attempts - 1); }, 400);
      });
  }

  function refreshScreen(forceClear) {
    fetch("/web/api/screen?format=json", { cache: "no-store" })
      .then(function (res) { return res.ok ? res.json() : null; })
      .then(function (data) {
        loadRuntimeMeta(); // 会话切换/命令执行后同步 provider/model/reasoning 权威值
        if (!data || !data.available) {
          // 无可用屏幕快照（无 surface / 空帧）：保留 screenEl 已有内容
          // （finishStream 已写入流式累积文本），不覆盖为 "(empty)"。
          // 会话切换（新建/恢复）后旧会话内容不应残留：forceClear 时清空。
          if (forceClear) {
            screenEl.textContent = "";
            if (streamMsgEl) { streamMsgEl.style.display = "none"; }
          }
          scrollToBottom();
          updateWelcome();
          return;
        }
        // 结构化消息可用（推荐路径）：角色气泡渲染；否则回退纯文本快照。
        if (Array.isArray(data.messages) && data.messages.length > 0) {
          renderConversationMessages(data.messages);
        } else {
          screenEl.textContent = data.text || "";
        }
        if (streamMsgEl) { streamMsgEl.style.display = "none"; }
        // 尊重用户滚动位置：若用户上滚阅读历史，不强制拉底（G3）。
        scrollToBottom();
        updateWelcome();
      })
      .catch(function (err) {
        // 网络错误：保留现有内容，不覆盖。
        console.error("screen fetch failed:", err);
      });
  }

  function scrollToBottom(force) {
    // force=true 强制滚底；否则尊重用户滚动位置（上滚阅读历史时不打扰）。
    if (!force && userScrolledAway) { return; }
    var conv = document.getElementById("conversation");
    if (conv) { conv.scrollTop = conv.scrollHeight; }
  }

  // ---- textarea 自动增高 ----
  function autoGrow() {
    if (!promptEl) { return; }
    promptEl.style.height = "auto";
    promptEl.style.height = Math.min(promptEl.scrollHeight, 160) + "px";
    updateScrollBtn();
  }

  // ---- Toast 轻提示 ----
  var toastTimer = null;
  function showToast(msg, kind, duration) {
    kind = kind || "ok";
    duration = duration || 2500;
    if (!toastContainer) { return; }
    clearTimeout(toastTimer);
    toastContainer.innerHTML = "";
    var t = document.createElement("div");
    t.className = "toast toast-" + kind;
    t.textContent = msg;
    toastContainer.appendChild(t);
    toastTimer = setTimeout(function () {
      if (t.parentNode) { t.parentNode.removeChild(t); }
    }, duration);
  }

  // ---- 页面标题反映运行状态 ----
  function updateTitle() {
    var prefix = "";
    if (uiState === "busy") { prefix = "● "; }
    else if (uiState === "posting") { prefix = "… "; }
    else if (uiState === "interrupting") { prefix = "… "; }
    else if (statusEl && statusEl.classList.contains("disconnected")) { prefix = "✗ "; }
    document.title = prefix + "aicli micro web client";
  }

  // ---- 欢迎页显示/隐藏 ----
  function updateWelcome() {
    if (!welcomeEl) { return; }
    var screenText = (screenEl.textContent || "").trim();
    var show = !streamActive && !streamEnded &&
      (screenText === "" || screenText === "(empty)");
    welcomeEl.style.display = show ? "block" : "none";
    if (screenCopyBtn) {
      screenCopyBtn.style.display = (screenText && screenText !== "(empty)") ? "inline-block" : "none";
    }
  }

  // ---- 浮动回底按钮显示/隐藏 ----
  function updateScrollBtn() {
    if (!scrollBottomBtn || !conversationEl) { return; }
    scrollBottomBtn.style.display = userScrolledAway ? "inline-block" : "none";
    // 定位在输入区上方，输入区多行增高时自动跟随
    var ir = document.getElementById("input-row");
    if (ir) { scrollBottomBtn.style.bottom = (ir.offsetHeight + 12) + "px"; }
  }

  // ---- 快捷键帮助面板切换 ----
  function toggleShortcutHelp() {
    if (!shortcutHelpEl) { return; }
    var show = shortcutHelpEl.style.display !== "block";
    shortcutHelpEl.style.display = show ? "block" : "none";
  }

  function hideApproval() {
    if (approvalOverlay) { approvalOverlay.classList.remove("active"); }
    pendingApprovalRequestID = null;
    pendingQuestionID = null;
    questionSuggestionsEl.innerHTML = "";
    approveBtn.style.display = "";
    denyBtn.style.display = "";
    if (approvalDetail) { approvalDetail.classList.remove("open"); approvalDetail.textContent = ""; }
    if (detailToggleBtn) { detailToggleBtn.textContent = "显示详情"; detailToggleBtn.style.display = ""; }
  }

  function showApproval(data) {
    pendingApprovalRequestID = (data && data.request_id) || null;
    pendingQuestionID = null;
    approvalModalTitle.textContent = "待审批工具: " + (data && data.tool_name || "?");
    approvalPrompt.innerHTML = renderMarkdown((data && data.prompt) || "");
    questionSuggestionsEl.innerHTML = "";
    approveBtn.style.display = "";
    denyBtn.style.display = "";
    if (detailToggleBtn) { detailToggleBtn.style.display = ""; }
    if (approvalDetail) {
      var args = (data && data.arguments) || "";
      if (typeof args === "object") { args = JSON.stringify(args, null, 2); }
      approvalDetail.textContent = args || "";
      approvalDetail.classList.remove("open");
    }
    if (detailToggleBtn) { detailToggleBtn.textContent = "显示详情"; }
    if (approvalOverlay) { approvalOverlay.classList.add("active"); }
  }

  function showQuestion(data) {
    pendingQuestionID = (data && data.question_id) || null;
    pendingApprovalRequestID = null;
    approvalModalTitle.textContent = "问题: " + (pendingQuestionID || "?");
    approvalPrompt.innerHTML = renderMarkdown((data && data.prompt) || "");
    approveBtn.style.display = "none";
    denyBtn.style.display = "none";
    if (detailToggleBtn) { detailToggleBtn.style.display = "none"; }
    if (approvalDetail) { approvalDetail.classList.remove("open"); approvalDetail.textContent = ""; }
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
    if (approvalOverlay) { approvalOverlay.classList.add("active"); }
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
          autoGrow();
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
          if (payload && payload.prompt) { dropPendingUserPrompt(payload.prompt); }
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
                    localPendingPrompts = []; // 旧会话的本地回显不带到被恢复会话
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
                  localPendingPrompts = []; // 旧会话的本地回显不带到新会话
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
      case "assistant_image_progress":
        // 图像生成进度：提取可预览的 URL/base64 并渲染到流式消息中。
        if (streamActive && data) {
          var img = data.image || data;
          var src = (img && typeof img === "object") ? (img.url || img.b64_data || img.data || "") : "";
          if (src && streamImages.indexOf(src) === -1) {
            streamImages.push(src);
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
        dynamicStatus = null;
        renderDynamicStatus();
        localPendingPrompts = []; // 旧会话的本地回显不带到新会话
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
     "assistant_delta", "assistant_image_progress", "tool_start", "tool_end", "approval_requested",
     "approval_resolved", "question_asked", "question_answered", "dynamic_status"].forEach(function (name) {
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
      autoGrow();
      return;
    }
    if (approvalOverlay && approvalOverlay.classList.contains("active") && pendingApprovalRequestID) {
      sendStatusEl.textContent = "请使用允许/拒绝按钮";
      return;
    }
    setUI("posting", "发送中…");
    appendPendingUserPrompt(text); // 乐观回显：立即显示用户气泡（发送中态）
    sendInput({ prompt: text });
  });

  promptEl.addEventListener("input", function () {
    autoGrow();
    if (uiState === "idle") { renderButton(); }
  });

  promptEl.addEventListener("keydown", function (e) {
    if (e.key === "ArrowUp" && !e.altKey) {
      // 输入历史：仅当光标位于首行（向上）时才浏览历史，
      // 否则让默认行为移动光标到上一行（多行输入场景）。
      if (uiState === "posting" || uiState === "interrupting") { return; }
      if (promptEl.selectionStart > 0) { return; }
      if (!inputHistory.length) { return; }
      e.preventDefault();
      if (inputHistoryIdx === -1) { inputHistoryIdx = inputHistory.length - 1; }
      else if (inputHistoryIdx > 0) { inputHistoryIdx--; }
      promptEl.value = inputHistory[inputHistoryIdx];
      autoGrow();
      return;
    }
    if (e.key === "ArrowDown" && !e.altKey) {
      // 仅当光标位于末行（向下）时才浏览历史
      if (uiState === "posting" || uiState === "interrupting") { return; }
      if (promptEl.selectionStart < promptEl.value.length) { return; }
      if (inputHistoryIdx === -1) { return; }
      e.preventDefault();
      if (inputHistoryIdx < inputHistory.length - 1) { inputHistoryIdx++; promptEl.value = inputHistory[inputHistoryIdx]; }
      else { inputHistoryIdx = -1; promptEl.value = ""; }
      autoGrow();
      return;
    }
    if (e.key === "Escape") {
      // Esc：若快捷键帮助面板打开则先关闭它
      if (shortcutHelpEl && shortcutHelpEl.style.display === "block") {
        toggleShortcutHelp();
        return;
      }
      // Esc：执行中触发中断（与「停止」按钮同路径）
      if (uiState === "busy") {
        e.preventDefault();
        sendInput({ type: "interrupt" });
      }
      return;
    }
    if ((e.ctrlKey || e.metaKey) && e.key === "/") {
      // Ctrl+/：显示/隐藏快捷键帮助面板
      e.preventDefault();
      toggleShortcutHelp();
      return;
    }
    if ((e.ctrlKey || e.metaKey) && (e.key === "l" || e.key === "L")) {
      // Ctrl+L：切换深色/浅色主题
      e.preventDefault();
      toggleTheme();
      return;
    }
    if ((e.ctrlKey || e.metaKey) && (e.key === "k" || e.key === "K")) {
      // Ctrl+K：清空当前对话视图（仅清除本地屏幕显示，不影响会话历史）
      e.preventDefault();
      screenEl.textContent = "";
      localPendingPrompts = [];
      if (streamMsgEl) { streamMsgEl.innerHTML = ""; }
      updateWelcome();
      scrollToBottom(true);
      return;
    }
    if (e.key !== "Enter" || e.shiftKey) { return; } // Shift+Enter: 换行（textarea 默认）
    if (e.isComposing || e.keyCode === 229) { return; } // IME 组合输入确认：不触发发送
    if (uiState === "posting" || uiState === "interrupting") { return; }
    if (uiState === "busy") {
      // 执行中：Enter 排队下一条消息（不中断当前任务）；按钮点击才触发停止。
      var text = promptEl.value.trim();
      if (!text) { return; }
      if (pendingQuestionID) {
        sendQuestionAnswer(text);
        promptEl.value = "";
        autoGrow();
        return;
      }
      if (approvalOverlay && approvalOverlay.classList.contains("active") && pendingApprovalRequestID) {
        sendStatusEl.textContent = "请使用允许/拒绝按钮";
        return;
      }
      appendPendingUserPrompt(text); // 排队期间同样先回显用户气泡
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

  // ---- 主题切换按钮 ----
  if (themeToggleBtn) {
    themeToggleBtn.addEventListener("click", function () { toggleTheme(); });
  }

  // ---- 审批模态框：关闭 / 详情展开 ----
  if (approvalModalCloseBtn) {
    approvalModalCloseBtn.addEventListener("click", function () { hideApproval(); });
  }
  // 点击遮罩空白处关闭（但需有活动审批时；question/approval 均适用）
  if (approvalOverlay) {
    approvalOverlay.addEventListener("click", function (e) {
      if (e.target === approvalOverlay) { hideApproval(); }
    });
  }
  if (detailToggleBtn) {
    detailToggleBtn.addEventListener("click", function () {
      if (!approvalDetail) { return; }
      var open = approvalDetail.classList.toggle("open");
      detailToggleBtn.textContent = open ? "隐藏详情" : "显示详情";
    });
  }

  // ---- 代码块复制按钮（事件委托，复制 <code> 文本） ----
  if (streamMsgEl) {
    streamMsgEl.addEventListener("click", function (e) {
      var t = e.target;
      var btn = (t && t.closest) ? t.closest(".copy-code-btn") : null;
      if (!btn) { return; }
      var codeEl = btn.parentNode ? btn.parentNode.querySelector("code") : null;
      if (!codeEl) { return; }
      var codeText = codeEl.textContent || "";
      if (!navigator.clipboard) {
        showToast("复制失败（浏览器不支持剪贴板）", "error");
        return;
      }
      navigator.clipboard.writeText(codeText).then(function () {
        var old = btn.textContent;
        btn.textContent = "✓ 已复制";
        setTimeout(function () { btn.textContent = old; }, 1500);
      }).catch(function () {
        showToast("复制失败", "error");
      });
    });
  }

  // ---- 智能滚动：用户手动上滚时暂停自动跟随 ----
  if (conversationEl) {
    conversationEl.addEventListener("scroll", function () {
      var atBottom = conversationEl.scrollHeight - conversationEl.scrollTop - conversationEl.clientHeight < 40;
      userScrolledAway = !atBottom;
      updateScrollBtn();
    });
  }
  // 浮动回底按钮
  if (scrollBottomBtn) {
    scrollBottomBtn.addEventListener("click", function () {
      userScrolledAway = false;
      scrollToBottom(true);
      updateScrollBtn();
    });
  }
  // 会话复制按钮
  if (screenCopyBtn) {
    screenCopyBtn.addEventListener("click", function () {
      var text = "";
      var rows = screenEl.querySelectorAll(".msg-row");
      if (rows.length) {
        var parts = [];
        rows.forEach(function (row) {
          // 推理内容在折叠面板内（.reasoning-content），加前缀保留语义；
          // 其余角色取 .msg-body 正文。按 DOM 顺序（= 对话时序）收集。
          var reasoningEl = row.querySelector(".reasoning-content");
          if (reasoningEl) {
            parts.push("[推理] " + reasoningEl.textContent);
            return;
          }
          var bodyEl = row.querySelector(".msg-body");
          if (bodyEl) { parts.push(bodyEl.textContent); }
        });
        text = parts.join("\n\n");
      } else {
        text = screenEl.textContent || "";
      }
      if (!text || text === "(empty)") { return; }
      if (!navigator.clipboard) { showToast("复制失败", "error"); return; }
      navigator.clipboard.writeText(text).then(function () {
        showToast("会话内容已复制", "ok");
      }).catch(function () {
        showToast("复制失败", "error");
      });
    });
  }
  // 欢迎页示例按钮
  if (welcomeEl) {
    welcomeEl.addEventListener("click", function (e) {
      var chip = e.target.closest(".welcome-chip");
      if (!chip) { return; }
      var text = chip.getAttribute("data-prompt") || "";
      if (!text) { return; }
      promptEl.value = text;
      autoGrow();
      promptEl.focus();
      // 程序赋值不触发 input 事件，需手动刷新按钮可用态
      if (uiState === "idle") { renderButton(); }
      // 自动发送（若当前 idle）
      if (uiState === "idle") {
        sendBtn.click();
      }
    });
  }
  // 快捷键帮助面板
  if (shortcutHelpEl) {
    shortcutHelpEl.querySelector(".shortcut-close").addEventListener("click", function () { toggleShortcutHelp(); });
    shortcutHelpEl.querySelector(".shortcut-overlay").addEventListener("click", function () { toggleShortcutHelp(); });
  }
  // provider / model / reasoning 切换选择器
  var cfgProviderEl = document.getElementById("cfg-provider");
  var cfgModelEl = document.getElementById("cfg-model");
  var cfgReasoningEl = document.getElementById("cfg-reasoning");
  var cfgBarEl = document.getElementById("cfg-bar");
  [cfgProviderEl, cfgReasoningEl].forEach(function (el) {
    if (el) { el.addEventListener("change", applyRuntimeConfig); }
  });
  if (cfgModelEl) {
    cfgModelEl.addEventListener("change", applyRuntimeConfig); // 失焦/选择 datalist 项时提交
    cfgModelEl.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        e.preventDefault();
        applyRuntimeConfig();
        cfgModelEl.blur();
      }
    });
  }

  // 底部栏显示完整 URL
  var origin = window.location.origin;
  document.getElementById("footer-endpoints").innerHTML = '<a href="' + origin + '/debug/endpoints" target="_blank" rel="noopener">' + origin + '/debug/endpoints</a>';
  document.getElementById("footer-web").innerHTML = '<a href="' + origin + '/web/" target="_blank" rel="noopener">' + origin + '/web/</a>';

  loadRuntimeMeta();
  openEventSource();
  refreshScreen();
  loadSessions();
  renderButton(); // 初始按钮状态（idle：输入为空时禁用）
  // 动态状态栏时钟：本地每秒推进 (N • esc to interrupt) 后缀；
  // 无活动状态时渲染函数直接置空，成本可忽略。
  setInterval(function () {
    if (dynamicStatus) { renderDynamicStatus(); }
  }, 1000);
})();
