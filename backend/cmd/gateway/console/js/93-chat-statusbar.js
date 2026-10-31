// 93-chat-statusbar.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=103";

Object.assign(G, {
  fmtDuration: fmtDuration,
  chatRenderDynamic: chatRenderDynamic,
  chatSetDynamic: chatSetDynamic,
  chatRenderSegments: chatRenderSegments,
  chatLoadStatusBar: chatLoadStatusBar,
  chatSetBusy: chatSetBusy,
  chatAutoGrow: chatAutoGrow,
  imViewport: imViewport,
  imChatIsOpen: imChatIsOpen,
  autoSelectFirstSession: autoSelectFirstSession,
  imChatPageOpen: imChatPageOpen,
  isMobileChatOpenFlag: isMobileChatOpenFlag,
  syncImActiveCard: syncImActiveCard,
  imOpenChatPanel: imOpenChatPanel,
  imCloseChatPanel: imCloseChatPanel,
  openSessionChat: openSessionChat,
  chatFetchScreen: chatFetchScreen,
  chatRetryLoad: chatRetryLoad,
  chatFail: chatFail,
  chatMessagesEqual: chatMessagesEqual,
  chatApplyWindow: chatApplyWindow,
  chatTrimWindow: chatTrimWindow,
  chatRowElement: chatRowElement,
  chatLiveRows: chatLiveRows,
  chatRender: chatRender,
  chatPatchLive: chatPatchLive,
  chatScheduleRender: chatScheduleRender,
  chatCancelRender: chatCancelRender,
});

  /* ── 状态栏（动态状态 + 静态段） ─────────────────────────────── */

  function fmtDuration(secs) {
    if (secs < 60) { return secs + "s"; }
    var m = Math.floor(secs / 60);
    if (m < 60) { return m + "m " + (secs % 60) + "s"; }
    return Math.floor(m / 60) + "h " + (m % 60) + "m";
  }

  // chatRenderDynamic 渲染动态状态行。文本来自 SSE dynamic_status（服务端只在状态
  // 变化时推一次），时钟由本地按 elapsed_ms 基准每秒推进——与节点 micro web client
  // 的 statusbar 同语义（服务端不重复推同一状态，时钟必须本地走）。
  function chatRenderDynamic() {
    var st = S.chatState;
    var box = S.el("chat-dyn-status");
    if (!st || !box) { return; }
    if (!st.dynText) {
      box.hidden = true;
      box.textContent = "";
      return;
    }
    var text = st.dynText;
    if (st.dynStartedAt) {
      text += "（" + fmtDuration(Math.max(0, Math.floor((Date.now() - st.dynStartedAt) / 1000))) + "）";
    }
    box.hidden = false;
    box.className = "chat-dyn is-" + (st.dynRole || "info");
    box.textContent = text;
  }

  // chatSetDynamic 接收一条 dynamic_status 载荷：
  //   {active, text, role, elapsed_ms, interruptible}
  // active=false 或空文本 = 状态行结束。用服务端给的 elapsed_ms 反推本地起点，时钟
  // 才不会从 0 重新开始——面板是中途打开的，这是常态而非例外。
  function chatSetDynamic(data) {
    var st = S.chatState;
    if (!st) { return; }
    var d = data || {};
    if (!d.active || !d.text) {
      st.dynText = "";
      st.dynStartedAt = 0;
      if (st.dynTimer) { clearInterval(st.dynTimer); st.dynTimer = null; }
      chatRenderDynamic();
      return;
    }
    st.dynText = String(d.text);
    st.dynRole = d.role || "info";
    var ms = typeof d.elapsed_ms === "number" ? d.elapsed_ms : 0;
    st.dynStartedAt = ms > 0 ? Date.now() - ms : 0;
    if (!st.dynTimer) {
      st.dynTimer = setInterval(function () {
        if (S.chatState) { chatRenderDynamic(); }
      }, 1000);
    }
    chatRenderDynamic();
  }

  function chatRenderSegments(box, snap) {
    box.textContent = "";
    box.title = "";
    if (!snap || !snap.available) { return; }
    var segments = snap.segments || [];
    if (!segments.length) { return; }
    if (snap.full_line) { box.title = snap.full_line; }
    segments.forEach(function (seg, i) {
      if (i > 0) {
        var sep = document.createElement("span");
        sep.className = "chat-seg-sep";
        sep.textContent = "·";
        box.appendChild(sep);
      }
      var span = document.createElement("span");
      span.className = "chat-seg";
      span.textContent = seg.text || "";
      box.appendChild(span);
    });
  }

  // chatLoadStatusBar 取静态段（context/dir/git/window/tokens…）。与节点 micro web
  // client 的 statusbar 同一端点、同一渲染口径；失败只清空，不打扰用户（动态状态行
  // 是主体，静态段缺失不影响面板可用性）。
  function chatLoadStatusBar() {
    var st = S.chatState;
    var box = S.el("chat-segs");
    if (!st || !box) { return; }
    G.api("/n/" + encodeURIComponent(st.node.node_id) + "/web/api/statusbar", { cache: "no-store" })
      .then(function (res) {
        if (!res.ok) { throw new Error("HTTP " + res.status); }
        return res.json();
      })
      .then(function (snap) {
        if (S.chatState !== st) { return; }
        chatRenderSegments(box, snap);
      })
      .catch(function () {
        if (S.chatState !== st) { return; }
        box.textContent = "";
      });
  }

  // chatSetBusy 跟踪会话忙闲，并据此开/关**兜底快照轮询**。
  //
  // 为什么必须有这条兜底：服务端 SSE 队列在压力下是**静默丢帧**（enqueue 满了直接丢，
  // 不报错），断线重连期间的帧也永久缺失。只靠增量帧驱动渲染时，一旦丢掉的正是
  // assistant_delta 那一段，面板就表现为「连接正常但内容不动」；而 screen 快照始终是
  // 权威的。忙时按固定间隔重取快照，把「实时性」从「帧必须到达」解耦为「快照必须
  // 到达」，与节点原版客户端的降级轮询同一思路（且刷新是保前缀合并，不打断阅读）。
  function chatSetBusy(busy) {
    var st = S.chatState;
    if (!st) { return; }
    var wasBusy = st.busy;
    st.busy = !!busy;
    if (st.busy && !st.busyTimer) {
      st.busyTimer = setInterval(function () {
        if (S.chatState && S.chatState.busy) { chatFetchScreen(); }
      }, S.CHAT_BUSY_POLL_MS);
    } else if (!st.busy && st.busyTimer) {
      clearInterval(st.busyTimer);
      st.busyTimer = null;
    }
    if (!st.busy) {
      if (st.stopTimer) {
        clearTimeout(st.stopTimer);
        st.stopTimer = null;
      }
      if (st.interrupting) {
        st.interrupting = false;
        G.chatShowHint("已停止");
      }
    }
    // 活跃回合的「最后一块推理/工具自动展开」是忙闲状态的呈现，不应依赖下一份
    // 快照恰好变化才生效（相同快照会被 chatApplyWindow 跳过）。只在 false→true
    // 的边沿扫描一次，避免每轮轮询重复走整份 DOM。
    if (!wasBusy && st.busy) {
      var host = S.el("chat-list");
      if (host) { G.chatApplyLiveTail(host, true); }
    }
    G.chatSyncComposer();
  }

  function chatAutoGrow() {
    var input = S.el("chat-input");
    // jsdom 没有布局引擎（scrollHeight === 0），此时跳过同步。
    if (!input || !input.scrollHeight) { return; }
    input.style.height = "auto";
    input.style.height = Math.min(input.scrollHeight, 160) + "px";
  }

  // ── 微信式会话壳：列表 ↔ 会话流 ──────────────────────────────
  // 桌面端列表与会话流同屏双栏（微信电脑版），手机端列表整页、点开滑入会话
  // 流整页并用返回键回列表（微信手机版）。#chat-modal 仍复用原来的数据面，
  // 只是不再走 openModal：不加 scrim、不设 inert、不加焦点陷阱。
  S.imChatHistoryPushed = false;
  S.imSuppressHistoryBack = false;

  function imViewport() {
    return (window.matchMedia && window.matchMedia("(max-width: 860px)").matches) ? "mobile" : "desktop";
  }

  function imChatIsOpen() {
    var panel = S.el("chat-modal");
    return !!(panel && !panel.hidden);
  }

  // First visit to the sessions view on desktop should not leave a dead empty
  // pane. Select the first visible, chat-capable session without marking it
  // read and without stealing focus. The operator can still close it and browse.
  function autoSelectFirstSession() {
    if (S.sessionsAutoSelected) { return; }
    if (G.currentView() !== "sessions" || imViewport() !== "desktop" || imChatIsOpen()) { return; }
    var items = G.allSessions(S.lastNodes).filter(G.sessionMatches).sort(G.sessionSorter);
    for (var i = 0; i < items.length; i++) {
      var item = items[i];
      if (G.isSubagentSession(item.session) || !G.chatPanelSupported(item.node, item.session)) { continue; }
      S.sessionsAutoSelected = true;
      openSessionChat(item.node, item.session, { skipMarkRead: true, skipFocus: true });
      return;
    }
  }

  // 手机端「会话流页」是否正铺满屏幕：决定快捷键与返回键归属。
  function imChatPageOpen() {
    return imViewport() === "mobile" && isMobileChatOpenFlag();
  }

  function isMobileChatOpenFlag() {
    return document.body.classList.contains("im-chat-open");
  }

  // 列表行高亮：当前打开的会话（微信里是浅灰底）。
  function syncImActiveCard() {
    var st = S.chatState;
    var key = (st && st.node && st.session)
      ? st.node.node_id + "|" + st.session.session_id
      : "";
    var cards = document.querySelectorAll(".session-card");
    for (var i = 0; i < cards.length; i++) {
      cards[i].classList.toggle("is-active", !!key && cards[i].getAttribute("data-draft-key") === key);
    }
  }

  function imOpenChatPanel(noFocus) {
    var panel = S.el("chat-modal");
    if (!panel) { return; }
    panel.hidden = false;
    var emptyPane = S.el("im-chat-empty");
    if (emptyPane) { emptyPane.hidden = true; }
    syncImActiveCard();
    if (imViewport() === "mobile") {
      document.body.classList.add("im-chat-open");
      // 会话流在手机上表现为一个"页面"：压一条历史记录，系统/手势返回即回列表。
      if (!S.imChatHistoryPushed) {
        try { window.history.pushState({ imChat: true }, ""); S.imChatHistoryPushed = true; } catch (e) {}
      }
    }
    // 把焦点交给面板（读屏能立刻读到会话标题）；手机端不主动聚焦输入框，
    // 避免弹出软键盘遮住整段对话。
    if (!noFocus) {
      if (panel.getAttribute("tabindex") === null) { panel.setAttribute("tabindex", "-1"); }
      panel.focus({ preventScroll: true });
    }
  }

  function imCloseChatPanel() {
    var panel = S.el("chat-modal");
    if (!panel) { return; }
    panel.hidden = true;
    document.body.classList.remove("im-chat-open");
    var emptyPane = S.el("im-chat-empty");
    if (emptyPane) { emptyPane.hidden = false; }
    if (S.imChatHistoryPushed) {
      S.imChatHistoryPushed = false;
      if (!S.imSuppressHistoryBack) {
        // 回退掉打开时压入的那条记录，让浏览历史保持干净。
        try { window.history.back(); } catch (e) {}
      }
    }
    syncImActiveCard();
  }

  function openSessionChat(node, session, opts) {
    if (!node || !session) { return; }
    S.sessionsAutoSelected = true;
    var wantInteractive = !!(opts && opts.focusInteractive);
    var skipMarkRead = !!(opts && opts.skipMarkRead);
    var skipFocus = !!(opts && opts.skipFocus);
    if (node.stale) {
      G.pushToast("该节点当前离线，暂时无法读取会话消息。", "error", {
        label: "查看详情",
        run: function () { G.openSessionDetail(node, session); },
      });
      return;
    }
    if (!G.chatPanelSupported(node, session)) {
      if (!G.nodeHasWebUI(node)) {
        G.pushToast(G.webUnavailableText(node), "error", {
          label: "查看详情",
          run: function () { G.openSessionDetail(node, session); },
        });
      } else {
        G.pushToast("该会话暂时不能在站内打开，可尝试原版 Web。", "error", {
          label: "打开原版 Web",
          run: function () { G.openSession(node, session); },
        });
      }
      return;
    }
    var canInput = G.chatCanInput(node);
    var initialBusy = G.sessionBusyFlag(session);
    G.setText(S.el("chat-modal-title"), G.sessionLabelOf(session));
    G.setText(S.el("chat-modal-sub"),
      G.nodeDisplayName(node) + " · " + (node.kind || "agent") +
      " · " + G.stateLabelOf(session));
    var host = S.el("chat-list");
    if (host) { host.innerHTML = ""; }
    G.chatNotice("");
    G.chatSetStatus("连接中…", "");
    G.chatShowHint("");
    var form = S.el("chat-form");
    if (form) { form.hidden = !canInput; }
    var ro = S.el("chat-readonly");
    if (ro) { ro.hidden = canInput; }
    var older = S.el("chat-older");
    if (older) { older.hidden = true; }
    var jump = S.el("chat-jump-latest");
    if (jump) { jump.hidden = true; }
    var input = S.el("chat-input");
    if (input) { input.value = ""; input.style.height = ""; }
    G.chatPlaceholder("正在加载消息…");
    S.chatState = {
      node: node,
      session: session,
      win: null,
      msgStart: undefined,
      messages: [],
      rows: {},
      pending: [],
      live: G.chatEmptyLive(),
      seq: 0,
      screenBusy: false,
      lastScreenAt: 0,
      screenETag: "",
      renderFrame: 0,
      bottomPin: 0,
      scrollGuardUntil: 0,
      programmaticScrollTop: 0,
      followHoldUntil: Date.now() + 800,
      bottomPinTimer: 0,
      toolMeasurePending: false,
      follow: true,
      olderBusy: false,
      sending: false,
      interrupting: false,
      stopTimer: null,
      available: true,
      failed: false,
      scrollRestore: null,
      source: null,
      pollTimer: null,
      refreshTimer: null,
      busy: initialBusy,
      busyTimer: null,
      frames: 0,
      lastEvent: "",
      dynText: "",
      dynRole: "",
      dynStartedAt: 0,
      dynTimer: null,
      // 审批 / 提问交互态：由 SSE connected / approval_requested /
      // question_asked 填充；卡片「审批 / 回答」进入时聚焦交互区。
      pendingApproval: null,
      pendingQuestion: null,
      focusInteractive: wantInteractive,
    };
    G.chatSyncComposer();
    G.syncChatHeaderActions();
    // 面板打开时先收起上一次的交互条（状态刚被替换，避免残留上一条审批）。
    G.chatRenderInteractive();
    if (!skipMarkRead) { G.markRead(node, session); }
    // 面板打开 = 「正在看这个会话」：抑制该会话的推送；关面板时释放。
    G.rememberPresence(node, session);
    chatRenderDynamic();
    chatRenderSegments(S.el("chat-segs"), null);
    imOpenChatPanel(skipFocus);
    chatFetchScreen(S.CHAT_PAGE_LIMIT);
    chatLoadStatusBar();
    G.chatConnect();
    G.chatUpdateNextPending();
  }

  // ── 数据面：快照 / 渲染 ──────────────────────────────────────

  // chatFetchScreen 拉取权威窗口。older=true 时按 win.start 向前翻页，结果前插。
  // 每次请求带代次（seq），过期响应直接丢弃——快速重连 + 节流刷新会并发多份。
  function chatFetchScreen(limit, opts) {
    var st = S.chatState;
    if (!st) { return; }
    var o = opts || {};
    if (!o.older) {
      // 首屏打开会同时触发初始 screen、SSE open screen 和状态栏请求。
      // 同一面板只允许一份非翻页 screen 在途，并给刚完成的刷新 800ms 去重，
      // 避免重复解析/渲染同一窗口。
      if (st.screenBusy) { return; }
      if (st.lastScreenAt && Date.now() - st.lastScreenAt < 800) { return; }
      st.screenBusy = true;
    }
    if (o.older) {
      if (st.olderBusy || !st.win || !st.win.has_more) { return; }
      st.olderBusy = true;
      G.chatUpdateOlder();
    }
    st.seq++;
    var seq = st.seq;
    var url = "/n/" + encodeURIComponent(st.node.node_id) +
      "/web/api/screen?format=json&compact=1&msg_limit=" +
      (o.older ? S.CHAT_OLDER_LIMIT : (limit || S.CHAT_PAGE_LIMIT));
    if (o.older) {
      // 游标必须是**本地最老一条**的绝对索引（msgStart），不能取上一份服务端窗口的
      // start：刷新会把 win 推回「最新 N 条」窗口，此时 win.start > msgStart，用
      // win.start 翻页会取回一段已经在本地的区间，chatApplyWindow 的「紧邻才前插」
      // 判据（end === msgStart）随即失配、走整块重建分支——表现为点「加载更早消息」
      // 反而把最新消息弄丢（流式期间刷新频繁，几乎必现）。
      var cursor = typeof st.msgStart === "number" ? st.msgStart : st.win.start;
      url += "&msg_before=" + cursor;
    }
    var requestOpts = { cache: "no-store" };
    // ETag 由节点对响应体内容寻址。显式回传 If-None-Match 且不让浏览器持久化
    // transcript：内容未变时节点回 304，本次跳过 JSON 解析和 DOM 收敛；内容变化
    // 时 ETag 必然变化，不存在旧内容误命中。
    if (!o.older && st.screenETag) {
      requestOpts.headers = { "If-None-Match": st.screenETag };
    }
    G.api(url, requestOpts).then(function (res) {
      var etag = "";
      if (res.headers && typeof res.headers.get === "function") {
        etag = res.headers.get("ETag") || "";
      }
      if (res.status === 304) {
        return { notModified: true, etag: etag || st.screenETag };
      }
      if (!res.ok) { throw new Error("HTTP " + res.status); }
      return res.json().then(function (data) {
        return { data: data, etag: etag };
      });
    }).then(function (result) {
      // 翻页锁先放开：请求被刷新取代（seq 失配）时同样要解锁，否则 olderBusy 永久
      // 卡在 true，本次打开里「加载更早消息」与滚到顶自动翻页一起失效。
      st.olderBusy = false;
      // 面板已关闭 / 已换会话 / 已有更新请求 → 这一份作废。
      if (S.chatState !== st || st.seq !== seq) { return; }
      st.lastScreenAt = Date.now();
      if (result.etag) { st.screenETag = result.etag; }
      if (result.notModified) {
        // 304 说明权威内容没变；但首次打开时列表可能尚未完成排版，工具行测量
        // 会留待下一轮。这里只补这一项局部工作，不再遍历/重写整份消息列表。
        if (st.toolMeasurePending) {
          var host = S.el("chat-list");
          if (host) { G.chatRefreshToolToggles(host); }
        }
        return;
      }
      chatApplyWindow(result.data, o.older);
    }).catch(function (err) {
      st.olderBusy = false;
      G.chatUpdateOlder();
      if (S.chatState !== st || st.seq !== seq) { return; }
      st.lastScreenAt = Date.now();
      chatFail("暂时无法加载这段会话。",
        "GET " + url + "\n" + (err && err.message ? err.message : String(err)));
    }).finally(function () {
      if (!o.older) { st.screenBusy = false; }
    });
  }

  function chatRetryLoad() {
    var st = S.chatState;
    if (!st) { return; }
    st.failed = false;
    st.available = true;
    st.lastScreenAt = 0;
    st.screenETag = "";
    G.chatNotice("");
    G.chatPlaceholder("正在重新加载消息…");
    G.chatSetStatus("重新加载…", "");
    chatFetchScreen(S.CHAT_PAGE_LIMIT);
  }

  // chatFail 只在本次打开里提示一次失败：持续失败的刷新不该反复弹 toast。
  function chatFail(userMessage, reason) {
    var st = S.chatState;
    if (!st) { return; }
    G.chatSetStatus("读取失败", "down");
    if (st.failed) { return; }
    st.failed = true;
    var message = userMessage || "暂时无法加载这段会话。";
    if (!st.messages.length) { G.chatPlaceholder(message); }
    var diagnostics = G.sessionDiagnostics(st.node, st.session, reason);
    G.chatNotice(message + " 你可以重试，或打开原版 Web；诊断信息可复制给管理员。", [
      { label: "重试", run: chatRetryLoad },
      { label: "复制诊断", run: function () { G.copyText(diagnostics); } },
    ]);
  }

  // chatMessagesEqual 只比较面板实际渲染的字段。刷新窗口内容完全相同时跳过
  // 整份行遍历；role/content 任一变化仍走完整 render，语义不缩水。
  function chatMessagesEqual(a, b) {
    if (a === b) { return true; }
    if (!a || !b || a.length !== b.length) { return false; }
    for (var i = 0; i < a.length; i++) {
      var x = a[i], y = b[i];
      if (x === y) { continue; }
      if (!x || !y || x.role !== y.role || x.content !== y.content) { return false; }
    }
    return true;
  }

  // chatApplyWindow 把一次 screen 响应落进面板状态。两条写入路径：
  //   · 刷新（older=false）：与已加载区间在**后缀**合并，前缀行的 DOM 身份、
  //     折叠与滚动位置都不动（这就是流式刷新不打断阅读的原因）；
  //   · 翻页（older=true）：窗口紧邻且连续时前插；不连续说明历史被改写，整体重建。
  function chatApplyWindow(data, older) {
    var st = S.chatState;
    if (!st) { return; }
    if (!data || data.available === false) {
      var empty = S.el("chat-list");
      if (empty) { empty.innerHTML = ""; }
      st.rows = {};
      st.available = false;
      G.chatPlaceholder("暂无可显示的消息。");
      var unavailableReason = data && data.reason ? data.reason : "transcript 未就绪，或会话刚被切换";
      var unavailableDiagnostics = G.sessionDiagnostics(st.node, st.session, "screen unavailable: " + unavailableReason);
      G.chatNotice("这个会话暂时没有可读取的消息。可以重试，或打开原版 Web 查看完整界面。", [
        { label: "重试", run: chatRetryLoad },
        { label: "复制诊断", run: function () { G.copyText(unavailableDiagnostics); } },
      ]);
      G.chatSetStatus("无消息", "down");
      G.chatUpdateOlder();
      return;
    }
    st.available = true;
    st.failed = false;
    G.chatNotice("");
    // 键名是 message_window，没有 window 别名（服务端 chatDebugScreenSnapshot.
    // MessageWindow，见 chat_debug_screen_http.go）。读错键的后果是静默的：win 恒为
    // {} → has_more 恒 false → 「加载更早消息」永不出现；start 恒 0 → 翻页游标恒 0，
    // 服务端把 msg_before=0 归一成「取最新一页」，于是翻页即使被触发也只会原地打转。
    var win = data.message_window || {};
    var messages = data.messages || [];
    var start = typeof win.start === "number" ? win.start : 0;
    var end = typeof win.end === "number" ? win.end : start + messages.length;
    st.win = {
      start: start,
      end: end,
      total: typeof win.total === "number" ? win.total : end,
      has_more: !!win.has_more,
    };
    var previousMessages = st.messages;
    var previousStart = st.msgStart;
    var previousPendingLength = st.pending.length;
    var liveWasDone = !!(st.live && st.live.done);
    var needsRebuild = false;
    if (older && typeof st.msgStart === "number" && end === st.msgStart && start < st.msgStart) {
      st.messages = messages.concat(st.messages);
      st.msgStart = start;
    } else if (!older && typeof st.msgStart === "number" && start >= st.msgStart &&
               start - st.msgStart <= st.messages.length) {
      st.messages = st.messages.slice(0, start - st.msgStart).concat(messages);
    } else {
      // 整体重定位：旧行的 key 全部失效，交给 reconcile 回收（st.rows 必须一起清，
      // 否则会复用已经被移出 DOM 的节点）。
      st.messages = messages;
      st.msgStart = start;
      st.rows = {};
      needsRebuild = true;
    }
    G.chatConfirmPending();
    // 权威行已经接管这一回合：实时气泡退场，避免同一段内容渲染两次。
    if (st.live.done) { st.live = G.chatEmptyLive(); }
    chatTrimWindow();
    var messagesChanged = needsRebuild || st.msgStart !== previousStart ||
      !chatMessagesEqual(previousMessages, st.messages);
    if (messagesChanged) {
      chatRender();
    } else if (st.pending.length !== previousPendingLength || liveWasDone) {
      // 只有待确认回显或实时气泡状态变了：局部补丁即可，历史行保持原样。
      chatPatchLive();
    } else if (st.toolMeasurePending) {
      // 旧节点可能不返回 ETag（每次都是 200 + 相同内容）；测量尚未完成时仍要
      // 在可见布局上补一次，不能因为快照相同而永久跳过。
      var host = S.el("chat-list");
      if (host) { G.chatRefreshToolToggles(host); }
    }
    G.chatUpdateOlder();
  }

  // chatTrimWindow 把本地窗口收在 CHAT_MAX_WINDOW 内。行 key 用的是绝对索引
  // （"srv:" + msgStart + i），所以整块从头部丢弃后，留存行的 key 不变——DOM 身份、
  // 选区与折叠都保住。丢弃后必须重新开放翻页：更早的内容还在服务端。
  function chatTrimWindow() {
    var st = S.chatState;
    var host = S.el("chat-list");
    if (!st || !host) { return; }
    var extra = st.messages.length - S.CHAT_MAX_WINDOW;
    if (extra <= 0) { return; }
    // 丢弃的是视口上方的内容，到列表底部的距离因此不变——沿用翻页那套滚动补偿
    // （chatRender 里按 scrollHeight - scrollTop 还原），阅读位置不会跳。
    if (st.scrollRestore == null && !st.follow) {
      st.scrollRestore = host.scrollHeight - host.scrollTop;
    }
    st.messages = st.messages.slice(extra);
    st.msgStart = (typeof st.msgStart === "number" ? st.msgStart : 0) + extra;
    if (st.win) { st.win.has_more = true; }
  }

  // chatRowElement 返回 key 对应的行元素（不存在则建骨架）并补丁内容。行的身份由
  // chatRender 的 desired 顺序决定：重排/新增不重建 DOM，选区与折叠状态得以保留。
  //
  // 折叠语义与节点 micro web client 的 chatMsgRowHtml 对齐（docs：web/js/chat.js）：
  //   · reasoning → 原生 <details> 折叠面板，抬头行即 <summary>（默认收起一行）；
  //   · tool      → 默认折叠约 5 行（CSS max-height + 渐变遮罩），展开/收起控件并入
  //                 「工具」抬头行（文字 + ▼/▲ 图标），仅内容溢出时可用；完整文本
  //                 始终在 DOM 里（CSS 截断）。
  // 只有服务端行（opts.collapse）带折叠结构；live/pending 行是瞬态内容，保持平铺。
  function chatRowElement(st, key, role, content, opts) {
    var row = st.rows[key];
    var o = opts || {};
    if (!row) {
      row = document.createElement("div");
      if (o.collapse && role === "reasoning") {
        row.innerHTML = '<span class="chat-msg-role"></span>' +
          '<div class="chat-bubble"><details class="chat-reasoning">' +
          "<summary></summary>" +
          '<div class="chat-reasoning-body"></div>' +
          "</details></div>";
      } else if (o.collapse && role === "tool") {
        row.innerHTML = '<span class="chat-msg-role"></span>' +
          '<div class="chat-bubble"><div class="chat-tool-output chat-collapsed"></div></div>';
      } else {
        row.innerHTML = '<span class="chat-msg-role"></span><div class="chat-bubble"></div>';
      }
      // Cache the stable child lookups once per row. During a live turn this
      // function runs up to 20×/s; repeated querySelector calls across the whole
      // transcript were pure overhead.
      row.__roleEl = row.querySelector(".chat-msg-role");
      row.__bubble = row.querySelector(".chat-bubble");
      if (o.collapse && role === "reasoning") {
        row.__reasonEl = row.querySelector(".chat-reasoning");
        row.__reasonSummary = row.querySelector(".chat-reasoning summary");
        row.__reasonBody = row.querySelector(".chat-reasoning-body");
      } else if (o.collapse && role === "tool") {
        row.__toolOutput = row.querySelector(".chat-tool-output");
        st.toolMeasurePending = true;
      }
      st.rows[key] = row;
    }
    row.__key = key;
    var cls = "chat-msg chat-msg-" + (role || "assistant");
    if (o.pending) { cls += " is-pending"; }
    if (o.streaming) { cls += " is-streaming"; }
    if (row.className !== cls) { row.className = cls; }
    var bubble = row.__bubble;

    var roleEl = row.__roleEl;
    var roleText = G.roleLabelOf(role) + (o.pending ? " · 发送中" : "");
    if (o.collapse && role === "tool") {
      // 工具行的折叠控件长在抬头行里：先写角色文本再追加控件（setText 只在文本
      // 变化时写入，不会反复清掉控件）。
      G.setText(roleEl, roleText);
      if (!row.__toolToggle) {
        var tgl = document.createElement("button");
        tgl.type = "button";
        tgl.className = "chat-tool-toggle";
        tgl.setAttribute("aria-expanded", "false");
        tgl.setAttribute("aria-label", "展开工具输出");
        tgl.innerHTML = '<span class="chat-tool-action">展开</span>' +
          '<span class="chat-tool-icon" aria-hidden="true">▼</span>';
        roleEl.appendChild(document.createTextNode(" "));
        roleEl.appendChild(tgl);
        row.__toolToggle = tgl;
      }
    } else {
      G.setText(roleEl, roleText);
    }

    if (o.collapse && role === "reasoning") {
      G.setText(row.__reasonSummary, G.roleLabelOf(role) + "过程");
      G.setText(row.__reasonBody, content || "");
    } else if (o.collapse && role === "tool") {
      G.setText(row.__toolOutput, content || "");
    } else if (o.streamMarkdown) {
      if (S.mdEnabled) {
        bubble.classList.add("md");
        bubble.__mdSrc = null;
        bubble.__mdOn = true;
        G.chatSetStreamingMarkdown(bubble, content || "");
      } else {
        bubble.classList.remove("md");
        bubble.__mdSrc = null;
        bubble.__mdOn = false;
        G.chatSetStreamingText(bubble, content || "");
      }
    } else if (o.plain) {
      // Streaming text is transient and can grow to tens of KB. Re-parsing
      // Markdown is not applicable to reasoning/tool live rows (their server
      // twins are plain too); append in chunks without rewriting old text.
      bubble.classList.remove("md");
      bubble.__mdSrc = null;
      bubble.__mdOn = false;
      G.chatSetStreamingText(bubble, content || "");
    } else {
      G.setMessageHTML(bubble, content || "");
    }
    if (o.images && o.images.length) {
      // 图片追加在正文之后：setMessageHTML 只在正文变化时重写 innerHTML，
      // 重写之后这里再把缺的图补回来（按索引幂等）。
      o.images.forEach(function (src, i) {
        if (bubble.querySelector('[data-img="' + i + '"]')) { return; }
        var img = document.createElement("img");
        img.setAttribute("data-img", String(i));
        img.loading = "lazy";
        img.alt = "生成的图片";
        img.src = src;
        bubble.appendChild(img);
      });
    }
    return row;
  }

  // chatLiveRows 只构建实时气泡。结构快照与逐字增量共用这一份顺序定义：
  // 推理 → 助手正文/图片 → 工具状态。
  function chatLiveRows(st) {
    var rows = [];
    if (st.live.reasoning) {
      rows.push(chatRowElement(st, "live:reasoning", "reasoning", st.live.reasoning,
        { streaming: st.live.streaming, plain: true }));
    }
    // 只有真的收到助手正文（或图片）时才插实时助手行。turn_start 就会把
    // streaming 置真，但一回合可能长时间停在推理/工具阶段；此时渲染一个空的
    // 「助手」气泡会一直占在列表底部（表现为一行只有标签、没有内容），而推理/
    // 工具行与状态栏已经表达了进度。streaming 只用于已有正文时的光标动画。
    var liveImages = st.live.images || [];
    if (st.live.assistant || liveImages.length) {
      rows.push(chatRowElement(st, "live:assistant", "assistant", st.live.assistant,
        { streaming: st.live.streaming, streamMarkdown: true, images: liveImages }));
    }
    if (st.live.tool) {
      rows.push(chatRowElement(st, "live:tool", "tool", st.live.tool, { plain: true }));
    }
    return rows;
  }

  // chatRender 是全量收敛路径：只在窗口/消息集合变化后调用。它负责结构行、
  // 折叠测量和用户展开账本的恢复。
  function chatRender() {
    var st = S.chatState;
    var host = S.el("chat-list");
    if (!st || !host) { return; }
    var desired = [];
    var base = typeof st.msgStart === "number" ? st.msgStart : 0;
    st.messages.forEach(function (m, i) {
      // 服务端历史行才带折叠结构（推理面板 / 工具折叠）。
      desired.push(chatRowElement(st, "srv:" + (base + i), m.role, m.content, { collapse: true }));
    });
    desired = desired.concat(chatLiveRows(st));
    st.pending.forEach(function (text, i) {
      desired.push(chatRowElement(st, "pending:" + i, "user", text, { pending: true }));
    });
    G.reconcileChildren(host, desired);
    var keep = {};
    desired.forEach(function (row) { keep[row.__key] = true; });
    Object.keys(st.rows).forEach(function (key) {
      if (!keep[key]) { delete st.rows[key]; }
    });
    if (!desired.length) { G.chatPlaceholder("暂无消息。"); }
    // 折叠后处理：测量工具行溢出 → 启用/隐藏控件；按用户账本恢复手动展开；
    // 忙时活跃回合的最后一块自动展开、其余折回（与 micro web client 同一状态机）。
    if (st.toolMeasurePending) { G.chatRefreshToolToggles(host); }
    if (Object.keys(S.chatUserExpanded).length) {
      Object.keys(st.rows).forEach(function (key) {
        if (!S.chatUserExpanded[key]) { return; }
        var row = st.rows[key];
        if (row.__reasonEl && !row.__reasonEl.open) { row.__reasonEl.open = true; }
        var out = row.__toolOutput;
        if (out && out.classList.contains("chat-collapsed")) { G.chatSetToolExpanded(row, true); }
      });
    }
    G.chatApplyLiveTail(host, st.busy);
    // 翻页前插：按新增高度补偿滚动位置，阅读不被顶走。
    if (st.scrollRestore != null) {
      G.chatSetScrollTop(st, host, host.scrollHeight - st.scrollRestore);
      st.scrollRestore = null;
    }
    if (st.follow) { G.chatSetScrollTop(st, host, host.scrollHeight); }
    G.chatSyncJump();
    G.chatPinBottomAfterLayout();
  }

  // chatPatchLive 是流式热路径：历史行集合未变时只补实时气泡与待确认回显，
  // 不重新遍历权威消息内容、不扫描折叠状态，也不重写未变化的 DOM。
  function chatPatchLive() {
    var st = S.chatState;
    var host = S.el("chat-list");
    if (!st || !host) { return; }
    var desired = [];
    var pendingRows = [];
    var base = typeof st.msgStart === "number" ? st.msgStart : 0;
    var missing = false;
    st.messages.forEach(function (m, i) {
      var row = st.rows["srv:" + (base + i)];
      if (!row) { missing = true; return; }
      desired.push(row);
    });
    st.pending.forEach(function (text, i) {
      var row = st.rows["pending:" + i];
      if (!row) { missing = true; return; }
      pendingRows.push(row);
    });
    if (missing) {
      // 首份 screen 尚未落地或结构刚重定位：交给全量路径建骨架。
      chatRender();
      return;
    }
    desired = desired.concat(chatLiveRows(st), pendingRows);
    G.reconcileChildren(host, desired);
    var keep = {};
    desired.forEach(function (row) { keep[row.__key] = true; });
    Object.keys(st.rows).forEach(function (key) {
      if (!keep[key]) { delete st.rows[key]; }
    });
    if (!desired.length) { G.chatPlaceholder("暂无消息。"); }
    if (st.follow) { G.chatSetScrollTop(st, host, host.scrollHeight); }
    G.chatSyncJump();
  }

  // chatScheduleRender 按浏览器绘制帧合并高频增量：同一帧内多个 token 只补一次
  // 实时气泡。这里不用固定 50ms 定时器——那会让文字按 20fps 成段跳变，且回调
  // 可能落在布局帧之外，主观上更卡。
  function chatScheduleRender() {
    var st = S.chatState;
    if (!st || st.renderFrame) { return; }
    var run = function () {
      st.renderFrame = 0;
      if (S.chatState !== st) { return; }
      chatPatchLive();
    };
    if (typeof window.requestAnimationFrame === "function") {
      st.renderFrame = window.requestAnimationFrame(run);
    } else {
      st.renderFrame = setTimeout(run, 16);
    }
  }

  function chatCancelRender() {
    var st = S.chatState;
    if (!st) { return; }
    if (st.renderFrame) {
      if (typeof window.cancelAnimationFrame === "function") {
        window.cancelAnimationFrame(st.renderFrame);
      } else {
        clearTimeout(st.renderFrame);
      }
      st.renderFrame = 0;
    }
    if (st.bottomPin) {
      if (typeof window.cancelAnimationFrame === "function") {
        window.cancelAnimationFrame(st.bottomPin);
      } else {
        clearTimeout(st.bottomPin);
      }
      st.bottomPin = 0;
    }
    if (st.bottomPinTimer) {
      clearTimeout(st.bottomPinTimer);
      st.bottomPinTimer = 0;
    }
  }
