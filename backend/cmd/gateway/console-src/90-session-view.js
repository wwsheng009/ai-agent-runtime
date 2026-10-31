  /* ─────────────────────── session open / read ───────────────── */

  function nodeWebBase(node) {
    var webPath = node.kind === "runtime-server" ? "/" : "/web/";
    return "/n/" + encodeURIComponent(node.node_id) + webPath;
  }

  // nodeWebRoute returns the leading route that serves a node's Web UI: an
  // aicli micro client lives under /web, a runtime-server SPA at /.
  function nodeWebRoute(node) {
    return node && node.kind === "runtime-server" ? "/" : "/web";
  }

  // nodeHasWebUI reports whether the node advertised a reverse-proxy route for
  // its Web UI. Agents without a local Web service register no such route
  // (fail-closed), so the console must not offer an open action that could only
  // end in a proxy error.
  function nodeHasWebUI(node) {
    if (!node || node.stale) { return false; }
    var want = nodeWebRoute(node);
    var routes = node.routes || [];
    for (var i = 0; i < routes.length; i++) {
      var route = routes[i];
      if (route === "/" || route === want) { return true; }
      if (route.indexOf(want + "/") === 0) { return true; }
    }
    return false;
  }

  // windowActionsSupported 报告该节点能否在**它自己那台机器上**开关会话窗口。
  // 判据是节点通告的 window-actions 能力位：agent 只有在本机真的探测到可用终端
  // 后端（Windows Terminal / 新控制台 / gnome-terminal…）时才通告它。这跟
  // nodeHasWebUI 同一个道理——能力位是承诺，控制台不该提供一个必然落空的按钮。
  function windowActionsSupported(node) {
    if (!node || node.stale) { return false; }
    var caps = node.capabilities || [];
    for (var i = 0; i < caps.length; i++) {
      if (caps[i] === "window-actions") { return true; }
    }
    return false;
  }

  // webUnavailableText explains why a node cannot be opened, per host kind.
  function webUnavailableText(node) {
    var name = "节点「" + ((node && nodeDisplayName(node)) || "") + "」";
    if (node && node.kind === "runtime-server") {
      return name + "的本地服务暂时未就绪，请稍后重试。";
    }
    return name + "暂时没有提供网页会话。请在节点上启用网页服务后重试。";
  }

  function sessionOpenTarget(node, sessionID) {
    var base = nodeWebBase(node);
    if (node.kind === "runtime-server") { return base; }
    return base + "?session=" + encodeURIComponent(sessionID || "");
  }

  // 网关会话的规范 URL 标识：不区分节点类型，统一映射到控制台会话视图
  // （#sessions?node=…&session=…）。通知点击、卡片「复制链接」与事件条共用同一
  // 个深链，落地后由 applyHashParams() → locateSession() 聚焦对应卡片。
  function gatewaySessionLink(nodeID, sessionID) {
    var q = [];
    if (nodeID) { q.push("node=" + encodeURIComponent(nodeID)); }
    if (sessionID) { q.push("session=" + encodeURIComponent(sessionID)); }
    return location.origin + location.pathname + "#sessions" + (q.length ? "?" + q.join("&") : "");
  }

  function copyText(text) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      return navigator.clipboard.writeText(text);
    }
    return new Promise(function (resolve, reject) {
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.position = "fixed";
      ta.style.top = "-1000px";
      document.body.appendChild(ta);
      ta.select();
      var ok = false;
      try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
      document.body.removeChild(ta);
      if (ok) { resolve(); } else { reject(new Error("clipboard unavailable")); }
    });
  }

  function copyGatewaySessionLink(node, session) {
    var link = gatewaySessionLink(node && node.node_id, session && session.session_id);
    copyText(link).then(function () {
      notify("已复制会话链接", "ok");
    }).catch(function () {
      notify("复制失败：" + link, "error");
    });
  }

  function sessionAccessText(node, session) {
    if (!node || node.stale) { return "离线快照，仅可查看详情"; }
    if (!nodeHasWebUI(node)) {
      return node.kind === "runtime-server" ? "本地服务未就绪" : "网页会话未启用";
    }
    if (chatPanelSupported(node, session)) { return "站内会话可用"; }
    return "原版 Web 可用";
  }

  // Diagnostics stay out of the primary UI, but are copied as one support-ready
  // block. Never include message bodies or credentials here.
  function sessionDiagnostics(node, session, reason) {
    var lines = [
      "aicli-gateway 会话诊断",
      "time: " + new Date().toISOString(),
      "node: " + ((node && (nodeDisplayName(node) + " (" + (node.node_id || "—") + ")")) || "—"),
      "node_kind: " + ((node && node.kind) || "—"),
      "node_stale: " + !!(node && node.stale),
      "node_routes: " + ((node && (node.routes || []).join(", ")) || "—"),
      "node_capabilities: " + ((node && (node.capabilities || []).join(", ")) || "—"),
      "session: " + ((session && (sessionLabelOf(session) + " (" + (session.session_id || "—") + ")")) || "—"),
      "session_state: " + ((session && stateLabelOf(session)) || "—"),
      "session_attention: " + ((session && session.attention) || "none"),
      "session_updated_at: " + ((session && session.updated_at) || "—"),
      "target: " + ((node && nodeHasWebUI(node)) ? sessionOpenTarget(node, session && session.session_id) : "—"),
      "reason: " + (reason || "—"),
    ];
    return lines.join("\n");
  }

  function openSession(node, session) {
    if (!node) { return; }
    // Offline (stale snapshot) nodes have no tunnel: the proxy would answer
    // 502. Tell the operator instead of pretending to navigate.
    if (node.stale) {
      pushToast("该节点当前离线，网页会话不可用。", "error", {
        label: "查看详情",
        run: function () { openSessionDetail(node, session); },
      });
      return;
    }
    // A live node without a Web route (agent started without its web client)
    // would only fail inside the tunnel: refuse up front with the reason.
    if (!nodeHasWebUI(node)) {
      pushToast(webUnavailableText(node), "error", {
        label: "查看详情",
        run: function () { openSessionDetail(node, session); },
      });
      return;
    }
    if (session) { markRead(node, session); }
    rememberPresence(node, session);
    var target = sessionOpenTarget(node, session ? session.session_id : "");
    if (isMobileViewport()) {
      // Phones get the same-tab flow so the browser back button returns to
      // the list; prefs + scroll are persisted so it comes back as it was.
      saveSessionPrefs();
      saveSessionsScroll();
      window.location.assign(target);
      return;
    }
    window.open(target, "_blank");
  }

  // openSessionInline 是仪表盘行（需要处理 / 最近活动 / 节点会话行）的统一
  // 打开入口：节点支持站内面板时留在本页（手机上不开新标签、不打断列表），
  // 否则落回 openSession 的既有流程（新标签 / 移动同标签跳转）。判定与卡片
  // 「会话」按钮完全一致（chatPanelSupported）——无 input 能力的节点也能开
  // 只读面板；需要能作答的历史入口（审批/回答）另有 chatCanInput 门槛。
  function openSessionInline(node, session, opts) {
    if (!node) { return; }
    if (chatPanelSupported(node, session)) {
      openSessionChat(node, session, opts);
      return;
    }
    openSession(node, session);
  }

  // Card shortcut: pointer click, Enter and the explicit "打开会话" button all
  // land here so the primary path cannot diverge. A node without a Web route
  // has no usable destination, so show the detail dialog instead of a dead
  // proxy error or toast.
  function openSessionFromCard(node, session) {
    if (!node || !session) { return; }
    if (!nodeHasWebUI(node)) {
      openSessionDetail(node, session);
      return;
    }
    openSessionInline(node, session, chatFocusOptsFor(session));
  }

  function isMobileViewport() {
    return typeof window.matchMedia === "function" && window.matchMedia("(max-width: 860px)").matches;
  }

  function saveSessionsScroll() {
    try { localStorage.setItem(SESSIONS_SCROLL_KEY, String(Math.round(window.scrollY || 0))); } catch (err) { /* storage */ }
  }

  function restoreSessionsScroll() {
    if (currentView() !== "sessions") { return; }
    try {
      var y = parseInt(localStorage.getItem(SESSIONS_SCROLL_KEY) || "0", 10);
      if (y > 0) { window.scrollTo(0, y); }
    } catch (err) { /* storage */ }
  }

  function markRead(node, session) {
    if (!node || !session || !session.attention) { return; }
    readAttention[attentionKey(node, session)] = Date.now();
    saveReadAttention();
    renderInbox(lastNodes);
    updateStats(lastNodes);
    renderSessionTable(lastNodes);
  }

  /* ─────────────────────── session detail dialog ─────────────── */

  var detailTarget = null;

  var STATE_LABELS = {
    idle: "闲置",
    running: "运行中",
    thinking: "思考中",
    answering: "回答中",
    waiting: "等待中",
    review: "审查中",
    completed: "已完成",
    interrupted: "已中断",
    error: "出错",
  };

  function stateLabelOf(session) {
    var label = STATE_LABELS[session.state] || session.state || "未知";
    return session.busy && !isActiveSessionState(session.state) ? label + " · 处理中" : label;
  }

  // Either the registry's `busy` flag or an active state marks a live turn;
  // older snapshots may omit `busy` (omitempty false), so evaluate both.
  function sessionBusyFlag(session) {
    if (!session) { return false; }
    return session.busy === true || isActiveSessionState(session.state);
  }

  // Clipboard write with a legacy fallback. The button briefly confirms, but a
  // label swap alone is invisible to screen readers (and absent when the caller
  // passes no button), so the outcome is also announced as a toast.
  function copyText(text, btn) {
    var done = function () {
      if (btn) {
        var prev = btn.getAttribute("data-label") || btn.textContent;
        btn.setAttribute("data-label", prev);
        btn.textContent = "已复制 ✓";
        setTimeout(function () { btn.textContent = prev; }, 1500);
      }
      notify("已复制到剪贴板", "ok");
    };
    var failed = function () { notify("复制失败，请手动选择文本", "error"); };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, failed);
    } else {
      var ta = document.createElement("textarea");
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      var ok = false;
      try { ok = document.execCommand("copy"); } catch (err) { ok = false; }
      ta.remove();
      if (ok) { done(); } else { failed(); }
    }
  }

  /* ─────────────────── modal focus management ────────────────── */

  var FOCUSABLE_SEL =
    'a[href],button:not([disabled]),input:not([disabled]),' +
    'select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';
  var modalReturnFocus = {};

  // modalIsOpen 把「元素缺失」也当作未打开：PWA 可能缓存了旧版 index.html，
  // 新版 app.js 不能因为少一个模态节点就抛错（与 subagent-toggle 同一约定）。
  function modalIsOpen(id) {
    var modal = el(id);
    return !!(modal && !modal.hidden);
  }

  function openModal(id) {
    var modal = el(id);
    if (!modal) { return; }
    modalReturnFocus[id] = document.activeElement;
    modal.hidden = false;
    // aria-modal alone does not stop the browser from tabbing into the page
    // behind the dialog; inert does (the focus trap below is the fallback).
    var workspace = document.querySelector(".workspace");
    if (workspace) { workspace.inert = true; }
    document.addEventListener("keydown", trapModalTab, true);
    // 跳过 hidden 元素：querySelector 会命中带 hidden 的首个可聚焦控件，而 .focus()
    // 对不可见元素是 no-op —— 焦点会留在刚被置为 inert 的页面上，对话框标题不被读屏
    // 播报（会话面板的「跳到最新」正是一个默认隐藏的首个可聚焦元素）。这里只按
    // hidden 属性判断，不依赖布局尺寸，jsdom 下行为一致。
    var first = Array.prototype.filter.call(modal.querySelectorAll(FOCUSABLE_SEL), function (n) {
      return !n.hidden && !n.closest("[hidden]");
    })[0];
    (first || modal).focus();
  }

  function closeModal(id) {
    var modal = el(id);
    if (!modal) { return; }
    modal.hidden = true;
    if (!openModalEl()) {
      var workspace = document.querySelector(".workspace");
      if (workspace) { workspace.inert = false; }
      document.removeEventListener("keydown", trapModalTab, true);
    }
    // Hand focus back to whatever opened the dialog instead of dropping it on
    // <body> (which resets the keyboard user's position in the page).
    var back = modalReturnFocus[id];
    modalReturnFocus[id] = null;
    if (back && typeof back.focus === "function" && document.contains(back)) {
      back.focus();
    }
  }

  // 会话流面板是「视图的一部分」，不是模态框：它不铺 scrim、不给 .workspace
  // 设 inert、不参与 Tab 焦点陷阱。这里必须把它排除，否则会话流常驻（桌面
  // 双栏）时任何真实弹层关闭后都会把 .workspace 永久留在 inert 状态。
  function openModalEl() {
    if (modalIsOpen("session-modal")) { return el("session-modal"); }
    if (modalIsOpen("token-modal")) { return el("token-modal"); }
    if (modalIsOpen("mount-token-modal")) { return el("mount-token-modal"); }
    if (modalIsOpen("connector-editor-modal")) { return el("connector-editor-modal"); }
    if (modalIsOpen("connector-test-modal")) { return el("connector-test-modal"); }
    if (modalIsOpen("connector-logs-modal")) { return el("connector-logs-modal"); }
    if (modalIsOpen("connector-action-modal")) { return el("connector-action-modal"); }
    if (modalIsOpen("connector-qr-modal")) { return el("connector-qr-modal"); }
    return null;
  }

  function trapModalTab(e) {
    if (e.key !== "Tab") { return; }
    var modal = openModalEl();
    if (!modal) { return; }
    var items = Array.prototype.filter.call(
      modal.querySelectorAll(FOCUSABLE_SEL),
      function (n) { return n.offsetWidth > 0 || n.offsetHeight > 0 || n === document.activeElement; }
    );
    if (!items.length) { e.preventDefault(); modal.focus(); return; }
    var first = items[0];
    var last = items[items.length - 1];
    if (e.shiftKey && (document.activeElement === first || document.activeElement === modal)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }

  function closeSessionDetail() {
    detailTarget = null;
    closeModal("session-modal");
  }

  function openSessionDetail(node, session) {
    var modal = el("session-modal");
    if (!modal || !node || !session) { return; }
    detailTarget = { node: node, session: session };
    el("sd-title").textContent = session.title || "（无标题）";
    el("sd-id").textContent = session.session_id;
    el("sd-node").textContent =
      nodeDisplayName(node) + " · " + (node.kind || "agent") +
      (node.stale ? " · 离线快照" : "");
    var canOpenDetail = nodeHasWebUI(node);
    el("sd-access").textContent = sessionAccessText(node, session);
    el("sd-diagnostics").textContent = sessionDiagnostics(node, session, "detail");
    var detailOpen = el("session-modal-open");
    detailOpen.hidden = !canOpenDetail;
    detailOpen.textContent = chatPanelSupported(node, session) ? "打开会话" : "打开原版 Web";
    el("sd-node-id").textContent = node.node_id || "—";
    el("sd-state").textContent = stateLabelOf(session);
    el("sd-attention").textContent = session.attention ? attentionLabel(session.attention) : "无";
    var statusBits = [];
    var detailStatus = (session.status_text || "").trim();
    if (detailStatus) { statusBits.push(detailStatus); }
    var detailStarted = Date.parse(session.status_started_at || "");
    if (isActiveSessionState(session.state) && !isNaN(detailStarted)) {
      statusBits.push("已持续 " + formatStatusElapsed(Date.now() - detailStarted));
    }
    el("sd-status").textContent = statusBits.length ? statusBits.join(" · ") : "—";
    var updated = session.updated_at ? new Date(session.updated_at) : null;
    el("sd-updated").textContent =
      updated && !isNaN(updated.getTime()) ? updated.toLocaleString() : "—";
    var synced = session.synced_at ? new Date(session.synced_at) : null;
    el("sd-sync").textContent =
      synced && !isNaN(synced.getTime())
        ? timeAgo(synced) + "（" + synced.toLocaleString() + "，网关时间）"
        : "—";
    var stateAt = session.state_changed_at ? new Date(session.state_changed_at) : null;
    el("sd-state-at").textContent =
      stateAt && !isNaN(stateAt.getTime())
        ? timeAgo(stateAt) + "（" + stateAt.toLocaleString() + "，网关时间）"
        : "—";
    var attAt = session.attention_changed_at ? new Date(session.attention_changed_at) : null;
    el("sd-attention-at").textContent =
      attAt && !isNaN(attAt.getTime())
        ? timeAgo(attAt) + "（" + attAt.toLocaleString() + "，网关时间）"
        : "—";
    var hbMs2 = node.last_heartbeat ? new Date(node.last_heartbeat).getTime() : NaN;
    el("sd-heartbeat").textContent =
      isNaN(hbMs2) ? "—" : timeAgo(hbMs2) + "（" + absoluteTime(hbMs2) + "，网关时间）";
    el("sd-preview").textContent = session.preview || "—";
    var lastWrap = el("sd-last-wrap");
    if (session.last_message) {
      var role = el("sd-last-role");
      role.textContent = roleLabelOf(session.last_message_role || "");
      role.className = "s-last-role" + (session.last_message_role ? " role-" + session.last_message_role : "");
      setMessageHTML(el("sd-last"), session.last_message);
      lastWrap.hidden = false;
    } else {
      lastWrap.hidden = true;
    }
    var tailText = (session.status_tail || "").trim();
    var tailWrap = el("sd-tail-wrap");
    if (tailText) {
      setMessageHTML(el("sd-tail"), tailText);
      tailWrap.hidden = false;
    } else {
      tailWrap.hidden = true;
    }
    openModal("session-modal");
  }

