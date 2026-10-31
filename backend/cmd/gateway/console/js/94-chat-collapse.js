// 94-chat-collapse.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=96";

Object.assign(G, {
  chatRefreshToolToggles: chatRefreshToolToggles,
  chatSetToolExpanded: chatSetToolExpanded,
  chatApplyLiveTail: chatApplyLiveTail,
  chatScrollToBottom: chatScrollToBottom,
  chatPinBottomAfterLayout: chatPinBottomAfterLayout,
  chatSetScrollTop: chatSetScrollTop,
  chatSyncJump: chatSyncJump,
  chatUpdateOlder: chatUpdateOlder,
  chatLoadOlder: chatLoadOlder,
  chatNormalize: chatNormalize,
  chatConfirmPending: chatConfirmPending,
  closeSessionChat: closeSessionChat,
  chatConnect: chatConnect,
  chatDisconnect: chatDisconnect,
  chatScheduleRefresh: chatScheduleRefresh,
  chatBeginStream: chatBeginStream,
  chatAppendLive: chatAppendLive,
  chatEndStream: chatEndStream,
  chatOnEvent: chatOnEvent,
  chatAdoptSession: chatAdoptSession,
  chatResetForSession: chatResetForSession,
  chatStopTurn: chatStopTurn,
  chatSend: chatSend,
});

  /* ── 折叠交互（与 micro web client chat.js 同一状态机） ────────────── */

  // 用户手动展开的行（按服务端绝对索引记账）：窗口刷新会重建 DOM，但行 key 不变，
  // 所以在 chatApplyWindow/chRender 后按 key 恢复即可——手动展开的块永不自动折回。
  S.chatUserExpanded = {};

  // chatRefreshToolToggles 测量工具行是否溢出：溢出 → 启用抬头折叠控件；
  // 未溢出 → 直接展示全文并隐藏控件（与 micro web client 的
  // refreshToolOutputToggles 同一判定）。面板隐藏时 clientHeight 为 0，
  // 不打 ready 标记，下次渲染重测。
  function chatRefreshToolToggles(host) {
    var outputs = host.querySelectorAll(".chat-tool-output");
    var pending = false;
    for (var i = 0; i < outputs.length; i++) {
      var out = outputs[i];
      var row = out.closest(".chat-msg");
      if (!row || row.__toggleReady) { continue; }
      var tgl = row.__toolToggle;
      if (!tgl) { continue; }
      if (out.scrollHeight > out.clientHeight) {
        tgl.classList.remove("is-off");
      } else {
        tgl.classList.add("is-off");
        out.classList.remove("chat-collapsed");
        out.classList.add("chat-expanded");
        tgl.setAttribute("aria-expanded", "true");
      }
      if (out.clientHeight > 0) { row.__toggleReady = true; }
      else { pending = true; }
    }
    var st = S.chatState;
    if (st) { st.toolMeasurePending = pending; }
  }

  function chatSetToolExpanded(row, expanded) {
    var tgl = row.__toolToggle;
    var out = row.__toolOutput;
    if (!out || (tgl && tgl.classList.contains("is-off"))) { return; }
    out.classList.toggle("chat-collapsed", !expanded);
    out.classList.toggle("chat-expanded", expanded);
    if (tgl) {
      var act = tgl.querySelector(".chat-tool-action");
      var icon = tgl.querySelector(".chat-tool-icon");
      if (act) { act.textContent = expanded ? "收起" : "展开"; }
      if (icon) { icon.textContent = expanded ? "▲" : "▼"; }
      tgl.setAttribute("aria-expanded", expanded ? "true" : "false");
      tgl.setAttribute("aria-label", expanded ? "收起工具输出" : "展开工具输出");
    }
  }

  // 活跃回合（最后一条 user 行之后）的最后一块推理/工具总是自动展开；其余自动
  // 折叠。用户手动展开过的块永不回收（chatUserExpanded 记账）。
  function chatApplyLiveTail(host, followTurn) {
    var rows = host.querySelectorAll(".chat-msg");
    var boundary = 0;
    var lastReasoning = null;
    var lastTool = null;
    for (var i = 0; i < rows.length; i++) {
      if (/\bchat-msg-user\b/.test(rows[i].className)) { boundary = i + 1; }
      if (/\bchat-msg-reasoning\b/.test(rows[i].className)) { lastReasoning = rows[i]; }
      if (/\bchat-msg-tool\b/.test(rows[i].className)) { lastTool = rows[i]; }
    }
    var liveRows = Array.prototype.slice.call(rows, boundary);
    for (var j = 0; j < liveRows.length; j++) {
      var row = liveRows[j];
      var key = row.__key;
      if (row === lastReasoning || row === lastTool) { continue; }
      if (key && S.chatUserExpanded[key]) { continue; }
      var det = row.__reasonEl;
      if (det && det.open) { det.open = false; }
      var out = row.__toolOutput;
      if (out && out.classList.contains("chat-expanded")) { chatSetToolExpanded(row, false); }
    }
    if (!followTurn) { return; }
    // 最后一块总是展开（busy 时）。
    [lastReasoning, lastTool].forEach(function (row) {
      if (!row) { return; }
      var det = row.__reasonEl;
      if (det && !det.open) { det.open = true; return; }
      var out = row.__toolOutput;
      if (out && !out.classList.contains("chat-expanded")) { chatSetToolExpanded(row, true); }
    });
  }

  function chatScrollToBottom(force) {
    var st = S.chatState;
    var host = S.el("chat-list");
    if (!st || !host) { return; }
    if (force) { st.follow = true; }
    chatSetScrollTop(st, host, host.scrollHeight);
    chatSyncJump();
    chatPinBottomAfterLayout();
  }

  // content-visibility makes the browser settle intrinsic row heights after
  // the synchronous render. One rAF later, re-pin the viewport to the true
  // bottom so opening a long transcript never leaves the operator one row short.
  function chatPinBottomAfterLayout() {
    var st = S.chatState;
    var host = S.el("chat-list");
    if (!st || !host || !st.follow || st.bottomPin) { return; }
    var lastHeight = -1;
    var stableFrames = 0;
    var tries = 0;
    var pin = function () {
      st.bottomPin = 0;
      if (S.chatState !== st || !st.follow) { return; }
      var height = host.scrollHeight;
      chatSetScrollTop(st, host, height);
      chatSyncJump();
      if (height === lastHeight) { stableFrames++; }
      else { stableFrames = 0; lastHeight = height; }
      tries++;
      // Intrinsic sizes settle over a few frames on a long transcript. Re-pin
      // briefly so the initial open (and a backwards paging settle) lands on the
      // true bottom without adding work to the per-token path.
      if (tries < 60 && stableFrames < 3) {
        st.bottomPin = window.requestAnimationFrame(pin);
      }
    };
    if (typeof window.requestAnimationFrame === "function") {
      st.bottomPin = window.requestAnimationFrame(pin);
    } else {
      st.bottomPin = setTimeout(pin, 16);
    }
    if (!st.bottomPinTimer) {
      st.bottomPinTimer = setTimeout(function () {
        st.bottomPinTimer = 0;
        if (S.chatState !== st || !st.follow) { return; }
        chatSetScrollTop(st, host, host.scrollHeight);
        chatSyncJump();
      }, 350);
    }
  }

  function chatSetScrollTop(st, host, top) {
    if (!st || !host) { return; }
    // The scroll event is async and observed after content-visibility has
    // already changed scrollHeight. Treat our own scrollTop writes as
    // intentional follow, otherwise the first short layout flips follow off
    // and the bottom pin cancels itself.
    st.scrollGuardUntil = Date.now() + 120;
    host.scrollTop = top;
    st.programmaticScrollTop = host.scrollTop;
  }

  // chatSyncJump：只有「列表可滚 + 用户已上滚」时才提供回到最新。
  function chatSyncJump() {
    var st = S.chatState;
    var host = S.el("chat-list");
    var btn = S.el("chat-jump-latest");
    if (!st || !host || !btn) { return; }
    var overflow = host.scrollHeight - host.clientHeight > 40;
    btn.hidden = !(overflow && !st.follow);
  }

  function chatUpdateOlder() {
    var st = S.chatState;
    var box = S.el("chat-older");
    if (!st || !box) { return; }
    box.hidden = !(st.win && st.win.has_more);
    var btn = S.el("chat-older-btn");
    if (btn) { btn.disabled = !!st.olderBusy; }
  }

  // chatLoadOlder 向前翻一页（顶部按钮与滚到顶部附近都会触发）。
  function chatLoadOlder() {
    var st = S.chatState;
    var host = S.el("chat-list");
    if (!st || !host || st.olderBusy) { return; }
    if (!st.win || !st.win.has_more) { return; }
    st.scrollRestore = host.scrollHeight - host.scrollTop;
    G.chatFetchScreen(S.CHAT_OLDER_LIMIT, { older: true });
  }

  // chatConfirmPending：服务端窗口出现同文本 user 行时释放乐观回显。只做「按文本
  // 匹配移除」这一档，不引入节点原版客户端的完整双源对账。
  function chatNormalize(text) {
    return String(text || "").replace(/\s+/g, " ").trim();
  }

  function chatConfirmPending() {
    var st = S.chatState;
    if (!st || !st.pending.length) { return; }
    var seen = {};
    st.messages.forEach(function (m) {
      if (m.role === "user") { seen[chatNormalize(m.content)] = true; }
    });
    st.pending = st.pending.filter(function (text) { return !seen[chatNormalize(text)]; });
  }

  function closeSessionChat() {
    var st = S.chatState;
    if (!st) { return; }
    G.closeSessionMenus(false);
    // 先断流再清状态：否则关闭过程中的帧会写进正在被清空的 DOM。
    chatDisconnect();
    S.chatState = null;
    G.chatRenderInteractive();
    G.chatUpdateNextPending();
    delete S.presenceTargets[st.node.node_id + "|" + st.session.session_id];
    G.sendPresence();
    var host = S.el("chat-list");
    if (host) { host.innerHTML = ""; }
    G.imCloseChatPanel();
  }

  // ── 实时面：SSE 增量 ────────────────────────────────────────

  // chatConnect 打开节点的 /web/api/events。注册的事件名见 CHAT_SSE_EVENTS：
  // EventSource 只派发显式注册过的命名事件，漏一个就等于整帧丢失。
  function chatConnect() {
    var st = S.chatState;
    if (!st) { return; }
    chatDisconnect();
    if (typeof EventSource !== "function") {
      // 没有 EventSource：退化为轮询，至少能看到近实时历史（否则面板完全静默）。
      G.chatSetStatus("轮询中（浏览器不支持实时流）", "down");
      st.pollTimer = setInterval(function () { G.chatFetchScreen(); }, 3000);
      return;
    }
    var source = new EventSource("/n/" + encodeURIComponent(st.node.node_id) + "/web/api/events");
    st.source = source;
    source.addEventListener("open", function () {
      if (S.chatState !== st) { return; }
      G.chatSetStatus("已连接", "live");
      // 重连后必须补一次快照：断线（含网关 5 分钟超时）期间的增量已永久丢失。
      G.chatFetchScreen();
    });
    source.addEventListener("error", function () {
      if (S.chatState !== st) { return; }
      // 浏览器会自动重连；这里只如实反映状态，不主动关闭（关掉反而放弃重连）。
      G.chatSetStatus("连接断开，重连中…", "down");
    });
    S.CHAT_SSE_EVENTS.forEach(function (name) {
      source.addEventListener(name, function (event) {
        if (S.chatState !== st) { return; }
        var data = {};
        try { data = JSON.parse(event.data); } catch (err) { data = {}; }
        st.frames++;
        st.lastEvent = name;
        chatOnEvent(name, data);
      });
    });
    // 无名帧（服务端未写 `event:` 字段）也要解析，否则整帧静默丢弃。
    source.onmessage = function (event) {
      if (S.chatState !== st) { return; }
      var data = {};
      try { data = JSON.parse(event.data); } catch (err) { data = {}; }
      st.frames++;
      st.lastEvent = "message";
      chatOnEvent("message", data);
    };
    // 重连后按既有忙闲重新武装兜底轮询（chatDisconnect 把它停了）。
    G.chatSetBusy(st.busy);
  }

  function chatDisconnect() {
    var st = S.chatState;
    if (!st) { return; }
    if (st.source) {
      try { st.source.close(); } catch (err) { /* 已关闭 */ }
      st.source = null;
    }
    if (st.pollTimer) { clearInterval(st.pollTimer); st.pollTimer = null; }
    if (st.refreshTimer) { clearTimeout(st.refreshTimer); st.refreshTimer = null; }
    G.chatCancelRender();
    if (st.stopTimer) { clearTimeout(st.stopTimer); st.stopTimer = null; }
    if (st.interrupting) {
      st.interrupting = false;
      G.chatSyncComposer();
    }
    // 兜底轮询与状态栏时钟随连接一起停：后台/关闭时不空转（chatConnect 会按
    // st.busy 重新武装轮询）。
    if (st.busyTimer) { clearInterval(st.busyTimer); st.busyTimer = null; }
    if (st.dynTimer) { clearInterval(st.dynTimer); st.dynTimer = null; }
  }

  // chatScheduleRefresh 是尾随节流：事件风暴下最多每秒一次 screen 快照。
  function chatScheduleRefresh() {
    var st = S.chatState;
    if (!st || st.refreshTimer) { return; }
    st.refreshTimer = setTimeout(function () {
      if (S.chatState !== st) { return; }
      st.refreshTimer = null;
      G.chatFetchScreen();
    }, S.CHAT_REFRESH_MS);
  }

  function chatBeginStream() {
    var st = S.chatState;
    if (!st) { return; }
    st.live = G.chatEmptyLive();
    st.live.streaming = true;
    G.chatScheduleRender();
  }

  // chatAppendLive 累积增量文本。超过上限就停手：那种量级只可能来自异常输出，
  // 交给下一次权威快照收敛，别把本地内存堆穿。
  function chatAppendLive(kind, text) {
    var st = S.chatState;
    if (!st || !text || typeof text !== "string") { return; }
    if (kind === "reasoning") {
      if (st.live.reasoning.length > S.CHAT_MAX_LIVE) { return; }
      st.live.reasoning += text;
    } else {
      if (st.live.assistant.length > S.CHAT_MAX_LIVE) { return; }
      st.live.assistant += text;
    }
    st.live.streaming = true;
    G.chatScheduleRender();
  }

  // chatEndStream 把实时气泡标记为「内容已完整但尚未被快照接管」。气泡要等下一份
  // 权威窗口落地才退场（chatApplyWindow 里按 live.done 清除），否则终稿与增量之间
  // 会出现一次空档闪烁。
  function chatEndStream() {
    var st = S.chatState;
    if (!st) { return; }
    if (!st.live.assistant && !st.live.reasoning && !(st.live.images && st.live.images.length)) {
      st.live = G.chatEmptyLive();
      return;
    }
    st.live.streaming = false;
    st.live.done = true;
  }

  // chatOnEvent 是唯一的 SSE 分派点。语义与节点原版客户端一致：
  // 增量只喂实时气泡，权威内容一律以 screen 快照为准。
  function chatOnEvent(name, data) {
    var st = S.chatState;
    if (!st) { return; }
    var d = data || {};
    switch (name) {
      case "connected":
        G.chatSetStatus("已连接", "live");
        // connected 载荷带权威 session_busy：面板是中途打开的，这个值决定了要不要
        // 立刻开始兜底轮询（否则会等最多 30s 的第一次 heartbeat 才开始）。
        G.chatSetBusy(!!d.session_busy);
        // connected 同时带当前挂起的审批/提问（中途打开面板不会丢交互入口）。
        st.pendingApproval = d.pending_approval || null;
        st.pendingQuestion = d.pending_question || null;
        G.chatRenderInteractive();
        break;
      case "message":
        // 无名帧没有可依赖的语义，只当作「可能有更新」的提示。
        chatScheduleRefresh();
        break;
      case "screen_refresh":
        chatScheduleRefresh();
        break;
      case "turn_start":
        chatBeginStream();
        G.chatSetBusy(true);
        G.chatSetStatus("处理中…", "busy");
        break;
      case "turn_delta":
        // 历史别名（节点原版客户端也注册这个名字）：语义与 screen_refresh 同类，
        // 只当「可能有更新」的提示，不当作可渲染的增量文本。
        chatScheduleRefresh();
        break;
      case "reasoning_delta":
        chatAppendLive("reasoning", d.content || d.text || "");
        break;
      case "assistant_delta":
        chatAppendLive("assistant", d.text || d.content || "");
        break;
      case "assistant_message":
        if (d.content) {
          st.live.assistant = d.content;
          chatEndStream();
          G.chatScheduleRender();
        }
        chatScheduleRefresh();
        break;
      case "assistant_image_progress":
        var img = d.image && typeof d.image === "object" ? d.image : d;
        var src = (img && typeof img === "object") ? (img.url || img.b64_data || img.data || "") : "";
        if (src) {
          st.live.images.push(src);
          st.live.streaming = true;
          G.chatScheduleRender();
        }
        break;
      case "tool_start":
        st.live.tool = "[工具: " + (d.tool_name || "?") + " 执行中…]";
        if (st.live.streaming) { G.chatScheduleRender(); }
        break;
      case "tool_end":
        st.live.tool = "[工具: " + (d.tool_name || "?") + " 完成]";
        if (st.live.streaming) { G.chatScheduleRender(); }
        chatScheduleRefresh();
        break;
      case "turn_end":
        // 请求边界权威快照：增量在服务端队列满时是静默丢帧，只有整段快照能补齐
        // （与节点原版客户端、TUI 渲染器同语义）。
        if (d.reasoning_snapshot) { st.live.reasoning = d.reasoning_snapshot; }
        if (d.assistant_snapshot) { st.live.assistant = d.assistant_snapshot; }
        st.live.tool = "";
        chatEndStream();
        G.chatSetBusy(false);
        G.chatSetStatus("就绪", "live");
        G.chatScheduleRender();
        chatScheduleRefresh();
        break;
      case "heartbeat":
        // 权威忙闲（每次心跳都带）：这是「兜底轮询该开还是该关」的唯一可靠依据，
        // 不依赖 turn_start/turn_end 是否丢帧。
        if (typeof d.session_busy === "boolean") { G.chatSetBusy(d.session_busy); }
        // 尾部丢帧自愈：turn_end 被丢掉后可能不再有任何帧，气泡会永久停在「输出中」。
        if (d.session_busy === false && st.live.streaming) {
          chatEndStream();
          G.chatSetStatus("就绪", "live");
          G.chatScheduleRender();
          chatScheduleRefresh();
        }
        break;
      case "session_interrupted":
        st.live = G.chatEmptyLive();
        G.chatSetBusy(false);
        G.chatSetStatus("已停止", "down");
        G.chatScheduleRender();
        chatScheduleRefresh();
        break;
      case "dynamic_status":
        // 状态栏主体：Retrying / Analyzing / Running…（active=false 即收尾）。
        G.chatSetDynamic(d);
        break;
      case "model_changed":
        // provider/model/reasoning 切换落地 → 静态段（provider/model/context）变了。
        G.chatLoadStatusBar();
        break;
      case "session_switched":
        // 会话身份变化（/new、/resume、/load）：transcript 换人，旧行必须整块丢弃。
        // 载荷带新的 session_id（web_handlers.go 的 chatWebSessionSwitchNotice），
        // 标题与 presence 要一起搬过去，否则「标题写着 A、内容是 B」，且推送抑制
        // 挂在已经不看的 A 上。
        chatAdoptSession(d.session_id);
        chatResetForSession();
        break;
      case "session_start":
      case "session_end":
        // 这两个是**一次 run 的起止边界**，不是会话身份变化：actor.go 在每次 run 的
        // 开头（:3071）与 assistant_message 之后（:3350）各发一次，载荷只带 turn_id
        // （web_schema.go 的 pickField(data, payload, "turn_id")）。按「整块重建面板」
        // 处理会**在每个回合起止清空整个面板**——消息列表、滚动位置、阅读进度全丢，
        // 并闪一次「正在加载消息…」，正好摧毁「保前缀合并不打断阅读」的设计意图。
        // 回合内的收敛已由 turn_start/turn_end/screen_refresh 负责，这里只需一次
        // 轻量快照刷新。（console_test.go 按字面量守这条，注释里别写那个函数名。）
        chatScheduleRefresh();
        break;
      case "approval_requested":
        st.pendingApproval = d;
        st.pendingQuestion = null;
        G.chatNotice("");
        G.chatRenderInteractive();
        break;
      case "question_asked":
        st.pendingQuestion = d;
        st.pendingApproval = null;
        G.chatNotice("");
        G.chatRenderInteractive();
        break;
      case "approval_resolved":
        // 只有本面板确实展示过这条待办时才提示「下一条」；重复/迟到的帧只刷新按钮。
        var hadApproval = !!st.pendingApproval;
        st.pendingApproval = null;
        G.chatRenderInteractive();
        chatScheduleRefresh();
        if (hadApproval) { G.chatOfferNextPending(); } else { G.chatUpdateNextPending(); }
        break;
      case "question_answered":
        var hadQuestion = !!st.pendingQuestion;
        st.pendingQuestion = null;
        G.chatRenderInteractive();
        chatScheduleRefresh();
        if (hadQuestion) { G.chatOfferNextPending(); } else { G.chatUpdateNextPending(); }
        break;
      case "error":
        G.chatSetStatus("连接异常", "down");
        break;
      default:
        break;
    }
  }

  // chatAdoptSession：节点换了活跃会话（/new、/resume、/load → session_switched，
  // 载荷带新的 session_id）。面板渲染的一直是**节点当前活跃会话**的 transcript，
  // 所以标题与 presence 必须跟着搬：否则会出现「标题写着 A、内容却是 B」，并且推送
  // 抑制挂在已经不看的 A 上（正在看的 B 反而照常推送）。
  // 新会话能从最近一次节点快照里查到就用它（标题/状态更完整），查不到就退回裸 id。
  function chatAdoptSession(newID) {
    var st = S.chatState;
    if (!st || !newID || newID === st.session.session_id) { return; }
    delete S.presenceTargets[st.node.node_id + "|" + st.session.session_id];
    var next = null;
    (st.node.sessions || []).forEach(function (s) {
      if (s && s.session_id === newID) { next = s; }
    });
    st.session = next || { session_id: newID, title: newID };
    G.setText(S.el("chat-modal-title"), G.sessionLabelOf(st.session));
    G.setText(S.el("chat-modal-sub"),
      G.nodeDisplayName(st.node) + " · " + (st.node.kind || "agent") +
      " · " + G.stateLabelOf(st.session));
    G.syncChatHeaderActions();
    G.markRead(st.node, st.session);
    G.rememberPresence(st.node, st.session);
  }

  // chatResetForSession：/new、/resume、/load 会让 transcript 换人，旧行必须整块
  // 丢弃——否则上一条会话的内容会串进新会话的对话区。
  function chatResetForSession() {
    var st = S.chatState;
    if (!st) { return; }
    G.chatCancelRender();
    var host = S.el("chat-list");
    if (host) { host.innerHTML = ""; }
    st.messages = [];
    st.msgStart = undefined;
    st.win = null;
    st.rows = {};
    st.pending = [];
    st.live = G.chatEmptyLive();
    st.failed = false;
    st.screenETag = "";
    st.lastScreenAt = 0;
    st.toolMeasurePending = false;
    st.followHoldUntil = Date.now() + 800;
    // 换会话后旧的审批/提问一律失效（节点的 pending 由新一轮 connected 重报）。
    st.pendingApproval = null;
    st.pendingQuestion = null;
    G.chatRenderInteractive();
    G.chatPlaceholder("正在加载消息…");
    S.chatUserExpanded = {};
    // 换会话 = 上一段的动态状态与忙闲都不再适用，状态栏必须跟着复位重取，
    // 否则会把旧会话的 "Analyzing…（32m）" 挂在新会话上。
    G.chatSetDynamic({ active: false });
    G.chatSetBusy(false);
    G.chatLoadStatusBar();
    chatUpdateOlder();
    G.chatFetchScreen();
    // 会话列表里的摘要也变了（标题/状态/最近消息），顺手对齐。
    G.loadNodes();
  }

  // ── 输入面：发送 ────────────────────────────────────────────

  // chatStopTurn 中断当前回合。响应 interrupted 表示请求已受理，真正复位仍以
  // session_interrupted / turn_end / heartbeat(session_busy=false) 为准。
  function chatStopTurn() {
    var st = S.chatState;
    if (!st || !st.busy || st.sending || st.interrupting) { return; }
    if (st.node.stale) {
      G.chatShowHint("节点离线，无法停止当前任务。");
      return;
    }
    if (!G.chatCanInput(st.node)) {
      G.chatShowHint("该节点未开启输入通道，无法停止。");
      return;
    }
    st.interrupting = true;
    G.chatSyncComposer();
    G.chatShowHint("正在停止…");
    G.chatSetStatus("正在停止…", "busy");
    if (st.stopTimer) { clearTimeout(st.stopTimer); }
    st.stopTimer = setTimeout(function () {
      st.stopTimer = null;
      if (S.chatState !== st || !st.interrupting) { return; }
      st.interrupting = false;
      G.chatSyncComposer();
      G.chatSetStatus(st.busy ? "处理中…" : "就绪", st.busy ? "busy" : "live");
      G.chatShowHint("停止请求未收到确认，可再次点击停止。");
    }, 10000);
    G.api("/n/" + encodeURIComponent(st.node.node_id) + "/web/api/input", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ type: "interrupt" }),
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (body) {
        if (!res.ok) { throw new Error((body && body.reason) || ("HTTP " + res.status)); }
        return body || {};
      });
    }).then(function (body) {
      if (S.chatState !== st || !st.interrupting) { return; }
      if (body.status !== "interrupted") {
        throw new Error(body.reason || body.status || "停止请求被拒绝");
      }
      G.chatSetStatus("正在停止…", "busy");
      G.chatShowHint(body.result === "already_stopping"
        ? "已在停止中，等待会话收尾…"
        : "已请求停止，等待会话收尾…");
    }).catch(function (err) {
      if (S.chatState !== st || !st.interrupting) { return; }
      if (st.stopTimer) { clearTimeout(st.stopTimer); st.stopTimer = null; }
      st.interrupting = false;
      G.chatSyncComposer();
      G.chatSetStatus(st.busy ? "处理中…" : "就绪", st.busy ? "busy" : "live");
      var message = err && err.message ? err.message : String(err);
      G.chatShowHint("停止失败：" + message);
      G.notify("停止失败：" + message, "error");
    });
  }

  // chatSend 复用卡片回复表单已固化的契约：POST /n/{node}/web/api/input
  // {"prompt":…}，只有 {status:"queued"} 算成功；失败保留草稿。
  function chatSend() {
    var st = S.chatState;
    if (!st) { return; }
    var input = S.el("chat-input");
    var text = (input && input.value ? input.value : "").trim();
    if (!text || st.sending || st.interrupting) { return; }
    if (st.node.stale) {
      G.notify("节点「" + G.nodeDisplayName(st.node) + "」离线，无法发送。", "error");
      return;
    }
    if (!G.chatCanInput(st.node)) {
      G.chatShowHint("该节点未开启输入通道。");
      return;
    }
    st.sending = true;
    G.chatSetSending(true);
    G.chatShowHint("发送中…");
    G.api("/n/" + encodeURIComponent(st.node.node_id) + "/web/api/input", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ prompt: text }),
    }).then(function (res) {
      return res.json().catch(function () { return null; }).then(function (body) {
        if (!res.ok) { throw new Error((body && body.reason) || ("HTTP " + res.status)); }
        if (!body || body.status !== "queued") {
          throw new Error((body && body.reason) || (body && body.status) || "发送被拒绝");
        }
        if (S.chatState !== st) { return; }
        // 乐观回显：服务端 screen 里出现同文本 user 行时由 chatConfirmPending 释放。
        st.pending.push(text);
        if (input) { input.value = ""; G.chatAutoGrow(); }
        G.chatShowHint("");
        G.chatSetSending(false);
        G.chatRender();
        chatScrollToBottom(true);
      });
    }).catch(function (err) {
      if (S.chatState !== st) { return; }
      G.notify("发送失败：" + (err && err.message ? err.message : err), "error");
      G.chatShowHint("发送失败，可重试（草稿已保留）。");
      G.chatSetSending(false);
    }).finally(function () {
      if (S.chatState !== st) { return; }
      st.sending = false;
      G.chatSetSending(false);
    });
  }

  // 面板打开时切后台：断开 SSE 省流量，也避免后台被网关的 5 分钟超时反复抖动；
  // 回到前台重连并补一次快照。presence 也在这里重发（隐藏期间 sendPresence 是
  // no-op，回前台必须补，否则推送抑制会随 TTL 提前失效）。
  document.addEventListener("visibilitychange", function () {
    if (!S.chatState) { return; }
    if (document.hidden) {
      chatDisconnect();
      G.chatSetStatus("后台暂停", "down");
      return;
    }
    chatConnect();
    G.chatFetchScreen();
    G.sendPresence();
  });
