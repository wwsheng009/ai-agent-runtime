  /* ── Migration event strip (state/attention changes) ─────────── */

  var STRIP_LIMIT = 10;
  var STRIP_TTL_MS = 10 * 60 * 1000;
  var stripEvents = [];
  var stripExpanded = false;

  function pushStripEvent(ev) {
    if (!ev || !ev.session_id) { return; }
    // One chip per session: a session that keeps transitioning (运行中→回答中→
    // 已完成) refreshes its existing chip in place instead of stacking a new
    // duplicate on every transition, and moves to the front with the latest
    // label. Distinct sessions stay apart.
    var key = (ev.node_id || "") + "|" + ev.session_id;
    stripEvents = stripEvents.filter(function (item) {
      return item.nodeID + "|" + item.sessionID !== key;
    });
    // UX-07: 已完成不进入全局事件条；同时清掉该会话之前的活动 chip，
    // 避免停在旧状态上。
    if (ev.state === "completed" || ev.attention === "completed") {
      renderEventStrip();
      return;
    }
    stripEvents.unshift({
      nodeID: ev.node_id || "",
      sessionID: ev.session_id,
      state: ev.state || "",
      attention: ev.attention || "",
      ts: Date.parse(ev.ts || "") || Date.now(),
    });
    stripEvents = stripEvents.slice(0, STRIP_LIMIT);
    renderEventStrip();
  }

  function stripTitleOf(ev) {
    var items = allSessions(lastNodes);
    for (var i = 0; i < items.length; i++) {
      if (items[i].session.session_id === ev.sessionID) { return sessionLabelOf(items[i].session); }
    }
    return ev.sessionID;
  }

  function renderEventStrip() {
    var host = el("event-strip");
    if (!host) { return; }
    var now = Date.now();
    stripEvents = stripEvents.filter(function (ev) { return now - ev.ts < STRIP_TTL_MS; });
    if (stripEvents.length <= 3) { stripExpanded = false; }
    if (!stripEvents.length) {
      host.hidden = true;
      host.innerHTML = "";
      host.classList.remove("expanded");
      return;
    }
    host.hidden = false;
    host.classList.toggle("expanded", stripExpanded);
    host.innerHTML = "";
    var title = document.createElement("span");
    title.className = "event-strip-title";
    title.textContent = "刚刚";
    host.appendChild(title);
    var visible = stripExpanded ? stripEvents : stripEvents.slice(0, 3);
    visible.forEach(function (ev) {
      var chip = document.createElement("button");
      chip.type = "button";
      chip.className = "event-chip" + (ev.attention ? " ev-att-" + ev.attention : "");
      var label = ev.attention ? attentionLabel(ev.attention) : (STATE_LABELS[ev.state] || ev.state || "状态变化");
      chip.textContent = stripTitleOf(ev) + " · " + label + " ";
      var when = document.createElement("span");
      when.className = "event-chip-when";
      setRelTime(when, ev.ts, "", 0, absoluteTime(ev.ts));
      chip.appendChild(when);
      chip.addEventListener("click", function () { locateSession(ev.nodeID, ev.sessionID); });
      host.appendChild(chip);
    });
    if (stripEvents.length > 3) {
      var more = document.createElement("button");
      more.type = "button";
      more.className = "btn ghost sm event-strip-more";
      more.textContent = stripExpanded ? "收起" : "还有 " + (stripEvents.length - 3) + " 条";
      more.setAttribute("aria-expanded", stripExpanded ? "true" : "false");
      more.addEventListener("click", function () {
        stripExpanded = !stripExpanded;
        renderEventStrip();
      });
      host.appendChild(more);
    }
    var clear = document.createElement("button");
    clear.type = "button";
    clear.className = "btn ghost sm event-strip-clear";
    clear.textContent = "隐藏动态";
    clear.addEventListener("click", function () {
      stripEvents = [];
      stripExpanded = false;
      renderEventStrip();
    });
    host.appendChild(clear);
  }

  // Click-through: reveal the session row (switching to 全部 when the current
  // filter hides it) and pulse it so the operator lands on the right card.
  // 通知点击（Tauri 事件）与事件条点击共用这一入口：只做站内定位，不导航到
  // 会话 Web UI。
  function sessionCardSelector(nodeID, sessionID) {
    return '.session-card[data-draft-key="' + nodeID + "|" + sessionID + '"]';
  }

  function pulseSessionCard(card) {
    if (card.scrollIntoView) { card.scrollIntoView({ block: "center" }); }
    card.classList.remove("card-updated");
    void card.offsetWidth; // restart the pulse animation
    card.classList.add("card-updated");
    if (card.focus) {
      try { card.focus({ preventScroll: true }); } catch (err) { /* focus options unsupported */ }
    }
  }

  // 网关会话 URL 标识（#sessions?node=…&session=…）允许只带 session：从当前快照
  // 反查它挂在哪个节点，这样任意来源的深链都能定位到同一张卡片。
  function nodeIDForSession(sessionID) {
    var items = allSessions(lastNodes);
    for (var i = 0; i < items.length; i++) {
      if (items[i].session.session_id === sessionID) { return items[i].node.node_id; }
    }
    return "";
  }

  function locateSession(nodeID, sessionID) {
    if (!sessionID) { return; }
    if (!nodeID) { nodeID = nodeIDForSession(sessionID); }
    if (!nodeID) {
      // 快照里还没有这个会话（通知先于快照到达）：补拉一次后按 session 反查。
      loadNodes().then(function () {
        var resolved = nodeIDForSession(sessionID);
        if (resolved) { locateSession(resolved, sessionID); return; }
        notify("未在列表中找到该会话（可能已过期或已移除）", "error");
      });
      return;
    }
    if (currentView() !== "sessions") {
      navigate("sessions");
      // hashchange 是异步的；立即 applyRoute，保证随后的滚动/聚焦发生在
      // 已可见的会话视图上（对 hidden 容器调用 scrollIntoView 是无效的）。
      applyRoute();
    }
    var selector = sessionCardSelector(nodeID, sessionID);
    var card = document.querySelector(selector);
    if (card) { pulseSessionCard(card); return; }
    // 当前筛选/搜索把它藏起来了：放宽为“全部 + 空搜索”后重排一次。
    sessionFilterKind = "all";
    sessionFilterText = "";
    if (el("session-filter")) { el("session-filter").value = ""; }
    syncFilterControls();
    saveSessionPrefs();
    renderSessionTable(lastNodes);
    card = document.querySelector(selector);
    if (card) { pulseSessionCard(card); return; }
    // 未知/新会话（例如通知先于快照到达）：补拉一次快照再试。
    loadNodes().then(function () {
      var retry = document.querySelector(selector);
      if (retry) { pulseSessionCard(retry); return; }
      notify("未在列表中找到该会话（可能已过期或已移除）", "error");
    });
  }

  // 桌面壳点击通知时只派发定位事件（不 navigate、不开新窗口）。页面不在会话
  // 视图、未登录或快照未就绪时先记下目标，登录/加载完成后补做。
  var pendingLocate = null;
  window.addEventListener("aicli-gateway-locate", function (event) {
    var detail = (event && event.detail) || {};
    if (!detail.node_id || !detail.session_id) { return; }
    pendingLocate = { nodeID: String(detail.node_id), sessionID: String(detail.session_id) };
    if (authedState) { flushPendingLocate(); }
  });

  function flushPendingLocate() {
    if (!pendingLocate || !authedState) { return; }
    var target = pendingLocate;
    pendingLocate = null;
    locateSession(target.nodeID, target.sessionID);
  }

  // Expired chips are pruned by the 5s tick (labels advance via setRelTime).
  function pruneStripEvents() {
    if (!stripEvents.length) { return; }
    var now = Date.now();
    var next = stripEvents.filter(function (ev) { return now - ev.ts < STRIP_TTL_MS; });
    if (next.length !== stripEvents.length) {
      stripEvents = next;
      renderEventStrip();
    }
  }

  // Patch one session from an `event: session` delta (the live-tail path):
  // without this the 1Hz tail would force a full-snapshot re-render per frame.
  // Unknown nodes fall back to a full reload; structural removals never come
  // through this channel (the server broadcasts a full snapshot for those).
  function patchSession(nodeID, session) {
    if (!session || !session.session_id) { return; }
    var found = false;
    (lastNodes || []).forEach(function (node) {
      if (node.node_id !== nodeID) { return; }
      found = true;
      var list = node.sessions || (node.sessions = []);
      for (var i = 0; i < list.length; i++) {
        if (list[i].session_id === session.session_id) { list[i] = session; return; }
      }
      list.push(session);
    });
    if (!found) { loadNodes(); return; }
    if (chatState && chatState.node && chatState.session &&
        chatState.node.node_id === nodeID &&
        chatState.session.session_id === session.session_id) {
      chatState.session = session;
      setText(el("chat-modal-sub"),
        nodeDisplayName(chatState.node) + " · " + (chatState.node.kind || "agent") +
        " · " + stateLabelOf(session));
      chatSetBusy(sessionBusyFlag(session));
    }
    updateStats(lastNodes);
    renderInbox(lastNodes);
    renderSessionTable(lastNodes);
    // 增量 session 事件也要走 fresh-attention 检测：网关对注意力变化先推
    // 单会话增量；漏掉这一调用会让 toast/原生通知延迟到下一次全量快照。
    trackAttention(lastNodes);
  }

  /* ─────────────────────── derived helpers ───────────────────── */

  function loadReadAttention() {
    try {
      var raw = JSON.parse(localStorage.getItem(READ_ATTENTION_KEY) || "null");
      if (!raw || typeof raw !== "object") { return; }
      var now = Date.now();
      Object.keys(raw).forEach(function (key) {
        var ts = typeof raw[key] === "number" ? raw[key] : 0;
        if (ts && now - ts < READ_ATTENTION_TTL_MS) { readAttention[key] = ts; }
      });
    } catch (err) { /* storage unavailable */ }
  }

  function saveReadAttention() {
    try {
      localStorage.setItem(READ_ATTENTION_KEY, JSON.stringify(readAttention));
    } catch (err) { /* storage unavailable */ }
  }

  function attentionKey(node, session) {
    return node.node_id + "|" + session.session_id + "|" + session.attention;
  }

  function isWatching(nodeID, sessionID) {
    return !!presenceTargets[nodeID + "|" + (sessionID || "")];
  }

  // sessionLabelOf 给列表 / 面板一句人类可读的标题：会话没有标题时回退「未命名会话」，
  // 绝不回退裸 session_id（两个 id 只出现在「详情」弹窗里）。
  function sessionLabelOf(session) {
    var title = session && session.title ? String(session.title).trim() : "";
    return title || "未命名会话";
  }

  // shortNodeID：把 node_id 收敛成稳定可区分的短标识（取末尾 8 位）。
  // 完整 node-id 只在「详情」弹窗与 hover 提示中出现，供排障使用。
  function shortNodeID(id) {
    var s = String(id || "").trim();
    if (!s) { return ""; }
    if (s.indexOf("node-") === 0) { s = s.slice(5); }
    return s.length <= 8 ? s : s.slice(-8);
  }

  // nodeDisplayName 只给人类可读名：界面不暴露完整 node-id。
  // 有节点名用节点名；无名时用「类型 + 短标识」保证可区分，而不是笼统的「本机 agent」。
  function nodeDisplayName(node) {
    var name = node && node.name ? String(node.name).trim() : "";
    if (name) { return name; }
    var kind = node && node.kind ? String(node.kind) : "agent";
    var kindLabel = kind === "runtime-server" ? "runtime-server" : "本机 agent";
    var short = shortNodeID(node && node.node_id);
    return short ? kindLabel + " " + short : kindLabel;
  }

  // Relative time labels: "刚刚 / 12s 前 / 3 分钟前 / 2 小时前 / 09-12 14:33".
  function timeAgo(value) {
    var ms = typeof value === "number" ? value : new Date(value || 0).getTime();
    if (!ms || isNaN(ms)) { return ""; }
    var diff = Math.max(0, Date.now() - ms);
    var seconds = Math.floor(diff / 1000);
    if (seconds < 10) { return "刚刚"; }
    if (seconds < 60) { return seconds + "s 前"; }
    var minutes = Math.floor(seconds / 60);
    if (minutes < 60) { return minutes + " 分钟前"; }
    var hours = Math.floor(minutes / 60);
    if (hours < 24) { return hours + " 小时前"; }
    var d = new Date(ms);
    return d.toLocaleDateString([], { month: "2-digit", day: "2-digit" }) + " " +
      d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  }

  function absoluteTime(value) {
    var d = new Date(typeof value === "number" ? value : value || 0);
    return isNaN(d.getTime()) ? "" : d.toLocaleString();
  }

  // setRelTime wires an element for local relative-time refresh: the 5s tick
  // re-renders the label and toggles .age-stale past the given threshold.
  function setRelTime(el2, ms, prefix, staleMs, title) {
    el2.textContent = prefix + timeAgo(ms);
    if (title) { el2.title = title; }
    el2.setAttribute("data-rel-ts", String(ms));
    el2.setAttribute("data-rel-prefix", prefix || "");
    if (staleMs) { el2.setAttribute("data-rel-stale-ms", String(staleMs)); }
    el2.classList.toggle("age-stale", !!(staleMs && Date.now() - ms > staleMs));
  }

  function allSessions(nodes) {
    var out = [];
    (nodes || []).forEach(function (node) {
      (node.sessions || []).forEach(function (session) {
        out.push({ node: node, session: session });
      });
    });
    return out;
  }

  // Inbox/pending is deliberately a subset of attention: completed/waiting
  // remain useful status/notification signals, but they are not operator work.
  function inboxItems(nodes) {
    return allSessions(nodes).filter(function (item) {
      return !item.node.stale && isActionableAttention(item.session.attention) &&
        !readAttention[attentionKey(item.node, item.session)];
    }).sort(function (a, b) {
      var pa = attentionPriority[a.session.attention]; if (pa === undefined) { pa = 99; }
      var pb = attentionPriority[b.session.attention]; if (pb === undefined) { pb = 99; }
      if (pa !== pb) { return pa - pb; }
      return sessionLabelOf(a.session) < sessionLabelOf(b.session) ? -1 : 1;
    });
  }

