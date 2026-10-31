// 40-helpers.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=99";

Object.assign(G, {
  pushStripEvent: pushStripEvent,
  stripTitleOf: stripTitleOf,
  renderEventStrip: renderEventStrip,
  sessionCardSelector: sessionCardSelector,
  pulseSessionCard: pulseSessionCard,
  nodeIDForSession: nodeIDForSession,
  locateSession: locateSession,
  flushPendingLocate: flushPendingLocate,
  pruneStripEvents: pruneStripEvents,
  patchSession: patchSession,
  loadReadAttention: loadReadAttention,
  saveReadAttention: saveReadAttention,
  attentionKey: attentionKey,
  isWatching: isWatching,
  sessionLabelOf: sessionLabelOf,
  shortNodeID: shortNodeID,
  nodeDisplayName: nodeDisplayName,
  timeAgo: timeAgo,
  absoluteTime: absoluteTime,
  setRelTime: setRelTime,
  allSessions: allSessions,
  inboxItems: inboxItems,
});

  /* ── Migration event strip (state/attention changes) ─────────── */

  S.STRIP_LIMIT = 10;
  S.STRIP_TTL_MS = 10 * 60 * 1000;
  S.stripEvents = [];
  S.stripExpanded = false;

  function pushStripEvent(ev) {
    if (!ev || !ev.session_id) { return; }
    // One chip per session: a session that keeps transitioning (运行中→回答中→
    // 已完成) refreshes its existing chip in place instead of stacking a new
    // duplicate on every transition, and moves to the front with the latest
    // label. Distinct sessions stay apart.
    var key = (ev.node_id || "") + "|" + ev.session_id;
    S.stripEvents = S.stripEvents.filter(function (item) {
      return item.nodeID + "|" + item.sessionID !== key;
    });
    // UX-07: 已完成不进入全局事件条；同时清掉该会话之前的活动 chip，
    // 避免停在旧状态上。
    if (ev.state === "completed" || ev.attention === "completed") {
      renderEventStrip();
      return;
    }
    S.stripEvents.unshift({
      nodeID: ev.node_id || "",
      sessionID: ev.session_id,
      state: ev.state || "",
      attention: ev.attention || "",
      ts: Date.parse(ev.ts || "") || Date.now(),
    });
    S.stripEvents = S.stripEvents.slice(0, S.STRIP_LIMIT);
    renderEventStrip();
  }

  function stripTitleOf(ev) {
    var items = allSessions(S.lastNodes);
    for (var i = 0; i < items.length; i++) {
      if (items[i].session.session_id === ev.sessionID) { return sessionLabelOf(items[i].session); }
    }
    return ev.sessionID;
  }

  function renderEventStrip() {
    var host = S.el("event-strip");
    if (!host) { return; }
    var now = Date.now();
    S.stripEvents = S.stripEvents.filter(function (ev) { return now - ev.ts < S.STRIP_TTL_MS; });
    if (S.stripEvents.length <= 3) { S.stripExpanded = false; }
    if (!S.stripEvents.length) {
      host.hidden = true;
      host.innerHTML = "";
      host.classList.remove("expanded");
      return;
    }
    host.hidden = false;
    host.classList.toggle("expanded", S.stripExpanded);
    host.innerHTML = "";
    var title = document.createElement("span");
    title.className = "event-strip-title";
    title.textContent = "刚刚";
    host.appendChild(title);
    var visible = S.stripExpanded ? S.stripEvents : S.stripEvents.slice(0, 3);
    visible.forEach(function (ev) {
      var chip = document.createElement("button");
      chip.type = "button";
      chip.className = "event-chip" + (ev.attention ? " ev-att-" + ev.attention : "");
      var label = ev.attention ? G.attentionLabel(ev.attention) : (S.STATE_LABELS[ev.state] || ev.state || "状态变化");
      chip.textContent = stripTitleOf(ev) + " · " + label + " ";
      var when = document.createElement("span");
      when.className = "event-chip-when";
      setRelTime(when, ev.ts, "", 0, absoluteTime(ev.ts));
      chip.appendChild(when);
      chip.addEventListener("click", function () { locateSession(ev.nodeID, ev.sessionID); });
      host.appendChild(chip);
    });
    if (S.stripEvents.length > 3) {
      var more = document.createElement("button");
      more.type = "button";
      more.className = "btn ghost sm event-strip-more";
      more.textContent = S.stripExpanded ? "收起" : "还有 " + (S.stripEvents.length - 3) + " 条";
      more.setAttribute("aria-expanded", S.stripExpanded ? "true" : "false");
      more.addEventListener("click", function () {
        S.stripExpanded = !S.stripExpanded;
        renderEventStrip();
      });
      host.appendChild(more);
    }
    var clear = document.createElement("button");
    clear.type = "button";
    clear.className = "btn ghost sm event-strip-clear";
    clear.textContent = "隐藏动态";
    clear.addEventListener("click", function () {
      S.stripEvents = [];
      S.stripExpanded = false;
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
    var items = allSessions(S.lastNodes);
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
      G.loadNodes().then(function () {
        var resolved = nodeIDForSession(sessionID);
        if (resolved) { locateSession(resolved, sessionID); return; }
        G.notify("未在列表中找到该会话（可能已过期或已移除）", "error");
      });
      return;
    }
    if (G.currentView() !== "sessions") {
      G.navigate("sessions");
      // hashchange 是异步的；立即 applyRoute，保证随后的滚动/聚焦发生在
      // 已可见的会话视图上（对 hidden 容器调用 scrollIntoView 是无效的）。
      G.applyRoute();
    }
    var selector = sessionCardSelector(nodeID, sessionID);
    var card = document.querySelector(selector);
    if (card) { pulseSessionCard(card); return; }
    // 当前筛选/搜索把它藏起来了：放宽为“全部 + 空搜索”后重排一次。
    S.sessionFilterKind = "all";
    S.sessionFilterText = "";
    if (S.el("session-filter")) { S.el("session-filter").value = ""; }
    G.syncFilterControls();
    G.saveSessionPrefs();
    G.renderSessionTable(S.lastNodes);
    card = document.querySelector(selector);
    if (card) { pulseSessionCard(card); return; }
    // 未知/新会话（例如通知先于快照到达）：补拉一次快照再试。
    G.loadNodes().then(function () {
      var retry = document.querySelector(selector);
      if (retry) { pulseSessionCard(retry); return; }
      G.notify("未在列表中找到该会话（可能已过期或已移除）", "error");
    });
  }

  // 桌面壳点击通知时只派发定位事件（不 navigate、不开新窗口）。页面不在会话
  // 视图、未登录或快照未就绪时先记下目标，登录/加载完成后补做。
  S.pendingLocate = null;
  window.addEventListener("aicli-gateway-locate", function (event) {
    var detail = (event && event.detail) || {};
    if (!detail.node_id || !detail.session_id) { return; }
    S.pendingLocate = { nodeID: String(detail.node_id), sessionID: String(detail.session_id) };
    if (S.authedState) { flushPendingLocate(); }
  });

  function flushPendingLocate() {
    if (!S.pendingLocate || !S.authedState) { return; }
    var target = S.pendingLocate;
    S.pendingLocate = null;
    locateSession(target.nodeID, target.sessionID);
  }

  // Expired chips are pruned by the 5s tick (labels advance via setRelTime).
  function pruneStripEvents() {
    if (!S.stripEvents.length) { return; }
    var now = Date.now();
    var next = S.stripEvents.filter(function (ev) { return now - ev.ts < S.STRIP_TTL_MS; });
    if (next.length !== S.stripEvents.length) {
      S.stripEvents = next;
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
    (S.lastNodes || []).forEach(function (node) {
      if (node.node_id !== nodeID) { return; }
      found = true;
      var list = node.sessions || (node.sessions = []);
      for (var i = 0; i < list.length; i++) {
        if (list[i].session_id === session.session_id) { list[i] = session; return; }
      }
      list.push(session);
    });
    if (!found) { G.loadNodes(); return; }
    if (S.chatState && S.chatState.node && S.chatState.session &&
        S.chatState.node.node_id === nodeID &&
        S.chatState.session.session_id === session.session_id) {
      S.chatState.session = session;
      G.setText(S.el("chat-modal-sub"),
        nodeDisplayName(S.chatState.node) + " · " + (S.chatState.node.kind || "agent") +
        " · " + G.stateLabelOf(session));
      G.chatSetBusy(G.sessionBusyFlag(session));
    }
    G.updateStats(S.lastNodes);
    G.renderInbox(S.lastNodes);
    G.renderSessionTable(S.lastNodes);
    // 增量 session 事件也要走 fresh-attention 检测：网关对注意力变化先推
    // 单会话增量；漏掉这一调用会让 toast/原生通知延迟到下一次全量快照。
    G.trackAttention(S.lastNodes);
  }

  /* ─────────────────────── derived helpers ───────────────────── */

  function loadReadAttention() {
    try {
      var raw = JSON.parse(localStorage.getItem(S.READ_ATTENTION_KEY) || "null");
      if (!raw || typeof raw !== "object") { return; }
      var now = Date.now();
      Object.keys(raw).forEach(function (key) {
        var ts = typeof raw[key] === "number" ? raw[key] : 0;
        if (ts && now - ts < S.READ_ATTENTION_TTL_MS) { S.readAttention[key] = ts; }
      });
    } catch (err) { /* storage unavailable */ }
  }

  function saveReadAttention() {
    try {
      localStorage.setItem(S.READ_ATTENTION_KEY, JSON.stringify(S.readAttention));
    } catch (err) { /* storage unavailable */ }
  }

  function attentionKey(node, session) {
    return node.node_id + "|" + session.session_id + "|" + session.attention;
  }

  function isWatching(nodeID, sessionID) {
    return !!S.presenceTargets[nodeID + "|" + (sessionID || "")];
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
      return !item.node.stale && G.isActionableAttention(item.session.attention) &&
        !S.readAttention[attentionKey(item.node, item.session)];
    }).sort(function (a, b) {
      var pa = S.attentionPriority[a.session.attention]; if (pa === undefined) { pa = 99; }
      var pb = S.attentionPriority[b.session.attention]; if (pb === undefined) { pb = 99; }
      if (pa !== pb) { return pa - pb; }
      return sessionLabelOf(a.session) < sessionLabelOf(b.session) ? -1 : 1;
    });
  }
