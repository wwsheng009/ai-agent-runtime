(function () {
  "use strict";

  var el = function (id) { return document.getElementById(id); };

  /* ─────────────────────────── state ─────────────────────────── */

  var lastNodes = [];
  var lastDevices = [];
  var seenAttention = {};
  var attentionSeeded = false;
  var readAttention = {};
  var presenceTargets = {};
  var sessionFilterText = "";
  // 默认只显示在线会话：离线快照收进「离线」筛选，避免主列表被旧数据占满。
  var sessionFilterKind = "online";
  var sessionSort = "activity";
  var authedState = false;

  var attentionPriority = { approval: 0, question: 1, error: 2, completed: 3, waiting: 4 };

  var attentionLabels = {
    completed: "已完成",
    approval: "待审批",
    question: "待回答",
    error: "出错",
    waiting: "等待中",
  };

  function attentionLabel(kind) {
    return attentionLabels[kind] || kind;
  }

  /* ─────────────────────────── api ───────────────────────────── */

  function api(path, options) {
    var opts = Object.assign({ credentials: "same-origin" }, options || {});
    return fetch(path, opts);
  }

  function apiJSON(path, options) {
    return api(path, options).then(function (res) {
      return res.json().catch(function () { return null; }).then(function (body) {
        return { res: res, body: body };
      });
    });
  }

  /* ─────────────────────────── toasts ────────────────────────── */

  function toastHost() {
    var host = el("toast-host");
    if (!host) {
      host = document.createElement("div");
      host.id = "toast-host";
      host.className = "toast-host";
      document.body.appendChild(host);
    }
    return host;
  }

  // Toasts are announced (role=status / role=alert), stack inside one host
  // instead of overlapping on the same fixed coordinates, and hold their
  // dismissal timer while hovered or focused so they can actually be read.
  function pushToast(text, kind, action) {
    var host = toastHost();
    var toast = document.createElement("div");
    toast.className = "toast" + (kind ? " toast-" + kind : "");
    toast.setAttribute("role", kind === "error" ? "alert" : "status");

    var msg = document.createElement("span");
    msg.className = "toast-text";
    msg.textContent = text;
    toast.appendChild(msg);

    var actions = document.createElement("div");
    actions.className = "toast-actions";
    if (action) {
      var actionBtn = document.createElement("button");
      actionBtn.type = "button";
      actionBtn.className = "btn ghost sm";
      actionBtn.textContent = action.label;
      actionBtn.addEventListener("click", function () { dismiss(); action.run(); });
      actions.appendChild(actionBtn);
    }
    var close = document.createElement("button");
    close.type = "button";
    close.className = "toast-close";
    close.setAttribute("aria-label", "关闭通知");
    close.textContent = "\u00d7";
    close.addEventListener("click", dismiss);
    actions.appendChild(close);
    toast.appendChild(actions);

    var timer = null;
    var done = false;
    function dismiss() {
      if (done) { return; }
      done = true;
      if (timer) { clearTimeout(timer); }
      toast.remove();
    }
    function schedule(ms) {
      if (timer) { clearTimeout(timer); }
      timer = setTimeout(dismiss, ms);
    }
    function hold() { if (timer) { clearTimeout(timer); timer = null; } }
    toast.addEventListener("mouseenter", hold);
    toast.addEventListener("focusin", hold);
    toast.addEventListener("mouseleave", function () { if (!done) { schedule(2500); } });
    toast.addEventListener("focusout", function () {
      if (!done && !toast.contains(document.activeElement)) { schedule(2500); }
    });

    host.appendChild(toast);
    // Cap the stack so a burst of events cannot fill the viewport.
    while (host.children.length > 4) { host.removeChild(host.firstChild); }
    schedule(kind === "error" ? 6000 : 4000);
    return toast;
  }

  function notify(text, kind) {
    pushToast(text, kind);
  }

  function setAuthed(authed) {
    authedState = !!authed;
    el("login").hidden = authed;
    el("app").hidden = !authed;
    el("logout").hidden = !authed;
    el("logout-top").hidden = !authed;
    // Two-screen layout: pre-auth = standalone login page (chrome hidden),
    // authed = full workspace. The class also gates the chrome via CSS.
    document.body.classList.toggle("login-mode", !authed);
    if (!authed) {
      closeSidebar();
      el("password").setAttribute("aria-invalid", "false");
      el("password").focus();
      pushConfig = null; // re-read gateway push config on the next login
    }
    refreshPushState();
    renderInstallHint();
  }

  /* ─────────────────────── hash router ───────────────────────── */

  var VIEW_TITLES = {
    dashboard: "仪表盘",
    sessions: "会话",
    nodes: "节点",
    tokens: "设备令牌",
    connectors: "连接器",
  };

  function currentView() {
    var h = (window.location.hash || "#dashboard").replace(/^#\/?/, "");
    var name = h.split("?")[0];
    return VIEW_TITLES[name] ? name : "dashboard";
  }

  function applyRoute() {
    var view = currentView();
    document.querySelectorAll(".view").forEach(function (sec) {
      if (sec.id === "login") { return; }
      sec.hidden = sec.getAttribute("data-view") !== view;
    });
    document.querySelectorAll(".nav-item").forEach(function (a) {
      a.classList.toggle("active", a.getAttribute("data-view") === view);
    });
    var titleEl = el("view-title");
    if (titleEl) { titleEl.textContent = VIEW_TITLES[view] || "aicli-gateway"; }
    // Connectors are a lazy view: only hit /api/connectors when the tab is open
    // (the management plane may legitimately be unwired → 403/503 page state).
    if (view === "connectors") { loadConnectors(false); }
    closeSidebar();
  }

  window.addEventListener("hashchange", function () {
    applyHashParams();
    applyRoute();
  });

  function navigate(view, params) {
    window.location.hash = view + (params ? "?" + params : "");
  }

  function closeSidebar() {
    el("sidebar").classList.remove("open");
    el("sidebar-scrim").hidden = true;
  }

  /* ─────────────────────── data loading ──────────────────────── */

  function loadNodes() {
    // Mark the panels busy so first paint shows "加载中…" instead of blanks.
    var views = document.querySelector(".views");
    if (views) { views.setAttribute("aria-busy", "true"); }
    return apiJSON("/api/nodes").then(function (r) {
      if (r.res.status === 401) { setAuthed(false); return; }
      setAuthed(true);
      lastNodes = (r.body && r.body.nodes) || [];
      renderAll(lastNodes);
      subscribeEvents();
    }).finally(function () {
      if (views) { views.removeAttribute("aria-busy"); }
    });
  }

  function loadDevices() {
    return apiJSON("/api/devices").then(function (r) {
      if (r.res.status === 401) { setAuthed(false); return; }
      lastDevices = (r.body && r.body.devices) || [];
      renderDevices(lastDevices);
    });
  }

  /* ─────────────────────── rendering: connectors ─────────────── */

  var connectorsLoaded = false;

  // loadConnectors 读取 GET /api/connectors（owner 会话鉴权）。force=true 时
  // 跳过缓存；未接线（启动时 connectors disabled）或响应不是 JSON 契约时给出
  // 明确的页面态，而不是空白列表。
  function loadConnectors(force) {
    if (connectorsLoaded && !force) { return Promise.resolve(); }
    return apiJSON("/api/connectors").then(function (r) {
      if (r.res.status === 401) { setAuthed(false); return; }
      if (r.res.status === 403 || r.res.status === 503) {
        renderConnectors(null, (r.body && r.body.error) || ("连接器管理面不可用（HTTP " + r.res.status + "）"));
        return;
      }
      if (!r.res.ok || !r.body || !Array.isArray(r.body.items)) {
        renderConnectors(null, "连接器管理面未接线（gateway 启动日志中查找 connectors disabled）");
        return;
      }
      connectorsLoaded = true;
      renderConnectors(r.body.items, "");
    }).catch(function (err) {
      renderConnectors(null, "读取连接器失败：" + (err && err.message ? err.message : err));
    });
  }

  function renderConnectors(items, errorText) {
    var list = el("connectors-list");
    if (!list) { return; }
    var countEl = el("connectors-count");
    list.innerHTML = "";
    if (errorText) {
      if (countEl) { countEl.hidden = true; }
      var errorEl = document.createElement("p");
      errorEl.className = "empty";
      errorEl.textContent = errorText;
      list.appendChild(errorEl);
      return;
    }
    if (countEl) {
      countEl.textContent = items.length + " 个";
      countEl.hidden = false;
    }
    if (!items.length) {
      var emptyEl = document.createElement("p");
      emptyEl.className = "empty";
      emptyEl.textContent = "还没有连接器 —— 在支持连接器编辑的客户端（aicli 微前端 / runtime-server / 前端切片）创建通道后，这里会显示运行状态。";
      list.appendChild(emptyEl);
      return;
    }
    items.forEach(function (item) {
      var card = document.createElement("div");
      card.className = "card" + (item.enabled ? "" : " offline");
      card.innerHTML =
        '<div class="card-head"><span class="card-title"></span><span class="status-chip ' + (item.enabled ? "chip-active" : "chip-revoked") + '"></span></div>' +
        '<div class="card-meta"></div>' +
        '<div class="card-actions"></div>';
      card.querySelector(".card-title").textContent = (item.name || item.id) + "（" + item.provider + "）";
      card.querySelector(".status-chip").textContent = item.enabled ? "已启用" : "已停用";
      var owner = item.active_owner
        ? "运行宿主 " + item.active_owner + (item.owned_by_current_host ? "（本网关）" : "")
        : "未认领运行（网关重启或宿主离线）";
      var meta = card.querySelector(".card-meta");
      meta.textContent = "id " + item.id + (item.mode ? " · " + item.mode : "") + " · " + owner +
        (item.secret_keys && item.secret_keys.length ? " · 密钥字段 " + item.secret_keys.join("/") : "");
      // 管理跳转：完整编辑器由前端切片提供，这里保留契约入口（单项 JSON +
      // 全量 Schema），不做编辑，避免与前端 repo 耦合。
      var detail = document.createElement("a");
      detail.className = "btn ghost sm";
      detail.href = "/api/connectors/" + encodeURIComponent(item.id);
      detail.target = "_blank";
      detail.rel = "noopener";
      detail.textContent = "配置 JSON";
      card.querySelector(".card-actions").appendChild(detail);
      list.appendChild(card);
    });
  }

  var eventsSubscribed = false;
  var lastEventAt = 0;
  var sseDropped = false;

  // Visible connection state: a banner + the topbar chip tint. EventSource
  // reconnects on its own; we only surface it and re-sync on reopen.
  function setConnBanner(lost) {
    var banner = el("conn-banner");
    if (banner) { banner.hidden = !lost; }
    var chip = el("conn-chip");
    if (chip) { chip.classList.toggle("conn-lost", lost); }
  }

  function subscribeEvents() {
    if (eventsSubscribed || typeof EventSource !== "function") { return; }
    eventsSubscribed = true;
    var source = new EventSource("/api/events");
    source.addEventListener("open", function () {
      lastEventAt = Date.now();
      setConnBanner(false);
      // A reopen after a drop can hide missed registry changes; re-align once.
      if (sseDropped) { sseDropped = false; loadNodes(); }
    });
    source.addEventListener("nodes", function (event) {
      lastEventAt = Date.now();
      try {
        var payload = JSON.parse(event.data);
        lastNodes = (payload && payload.nodes) || [];
        renderAll(lastNodes);
      } catch (err) { /* ignore malformed frame */ }
    });
    source.addEventListener("session", function (event) {
      lastEventAt = Date.now();
      try {
        var payload = JSON.parse(event.data);
        if (payload) { patchSession(payload.node_id, payload.session); }
      } catch (err) { /* ignore malformed frame */ }
    });
    source.addEventListener("session-event", function (event) {
      lastEventAt = Date.now();
      try { pushStripEvent(JSON.parse(event.data)); } catch (err) { /* ignore */ }
    });
    source.addEventListener("error", function () {
      sseDropped = true;
      setConnBanner(true);
    });
  }

  /* ── Migration event strip (state/attention changes) ─────────── */

  var STRIP_LIMIT = 10;
  var STRIP_TTL_MS = 10 * 60 * 1000;
  var stripEvents = [];

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
    if (!stripEvents.length) { host.hidden = true; host.innerHTML = ""; return; }
    host.hidden = false;
    host.innerHTML = "";
    var title = document.createElement("span");
    title.className = "event-strip-title";
    title.textContent = "刚刚";
    host.appendChild(title);
    stripEvents.forEach(function (ev) {
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
    var clear = document.createElement("button");
    clear.type = "button";
    clear.className = "btn ghost sm event-strip-clear";
    clear.textContent = "清空";
    clear.addEventListener("click", function () { stripEvents = []; renderEventStrip(); });
    host.appendChild(clear);
  }

  // Click-through: reveal the session row (switching to 全部 when the current
  // filter hides it) and pulse it so the operator lands on the right card.
  function locateSession(nodeID, sessionID) {
    var selector = '.session-card[data-draft-key="' + nodeID + "|" + sessionID + '"]';
    var card = document.querySelector(selector);
    if (!card) {
      sessionFilterKind = "all";
      syncFilterControls();
      saveSessionPrefs();
      renderSessionTable(lastNodes);
      card = document.querySelector(selector);
      if (!card) { notify("未在列表中找到该会话（可能已过期或已移除）", "error"); return; }
    }
    if (card.scrollIntoView) { card.scrollIntoView({ block: "center" }); }
    card.classList.remove("card-updated");
    void card.offsetWidth; // restart the pulse animation
    card.classList.add("card-updated");
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
    updateStats(lastNodes);
    renderInbox(lastNodes);
    renderSessionTable(lastNodes);
  }

  /* ─────────────────────── derived helpers ───────────────────── */

  function attentionKey(node, session) {
    return node.node_id + "|" + session.session_id + "|" + session.attention;
  }

  function isWatching(nodeID, sessionID) {
    return !!presenceTargets[nodeID + "|" + (sessionID || "")];
  }

  function sessionLabelOf(session) {
    return session.title || session.session_id;
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

  function inboxItems(nodes) {
    return allSessions(nodes).filter(function (item) {
      return !item.node.stale && item.session.attention && !readAttention[attentionKey(item.node, item.session)];
    }).sort(function (a, b) {
      var pa = attentionPriority[a.session.attention]; if (pa === undefined) { pa = 99; }
      var pb = attentionPriority[b.session.attention]; if (pb === undefined) { pb = 99; }
      if (pa !== pb) { return pa - pb; }
      return sessionLabelOf(a.session) < sessionLabelOf(b.session) ? -1 : 1;
    });
  }

  /* ─────────────────────── rendering: stats ──────────────────── */

  function updateStats(nodes) {
    var live = 0, offline = 0, active = 0;
    (nodes || []).forEach(function (node) {
      if (node.stale) { offline++; } else { live++; active += (node.sessions || []).length; }
    });
    var attention = inboxItems(nodes).length;
    el("stat-online").textContent = String(live);
    el("stat-offline").textContent = String(offline);
    el("stat-sessions").textContent = String(active);
    el("stat-attention").textContent = String(attention);
    var badge = el("nav-badge-sessions");
    if (badge) {
      badge.textContent = attention ? String(attention) : "";
      badge.hidden = !attention;
    }
    var chip = el("conn-chip");
    if (chip) {
      var chipText = live ? live + " 个节点在线" : "无节点在线";
      chip.innerHTML = '<span class="dot ' + (live ? "live" : "offline") + '"></span>' +
        '<span class="conn-text"></span>';
      chip.querySelector(".conn-text").textContent = chipText;
    }
  }

  /* ─────────────────────── rendering: sessions ───────────────── */

  function sessionMatches(item) {
    var s = item.session;
    if (sessionFilterKind === "online" && item.node.stale) { return false; }
    if (sessionFilterKind === "attention" && !s.attention) { return false; }
    if (sessionFilterKind === "running" && (item.node.stale || !(s.busy || isActiveSessionState(s.state)))) { return false; }
    if (sessionFilterKind === "offline" && !item.node.stale) { return false; }
    if (sessionFilterText) {
      var q = sessionFilterText.toLowerCase();
      var hay = ((s.title || "") + " " + (s.preview || "") + " " + (s.last_message || "") + " " +
        (s.status_text || "") + " " + s.session_id).toLowerCase();
      if (hay.indexOf(q) === -1) { return false; }
    }
    return true;
  }

  var ROLE_LABELS = { user: "用户", assistant: "助手", tool: "工具", system: "系统" };

  function roleLabelOf(role) {
    return ROLE_LABELS[role] || (role || "消息");
  }

  // Reply box rendered inside each session card. It posts through the gateway
  // reverse proxy (/n/{node}/web/api/input) to the agent's own input endpoint:
  // the agent's forwarder injects the local write token and rewrites Origin,
  // so no extra auth wiring is needed here. Rendered only when the node
  // advertises the "input" capability (older agents keep the old card).
  function buildReplyForm(node, session) {
    var form = document.createElement("form");
    form.className = "s-reply";
    form.innerHTML =
      '<div class="s-reply-main">' +
      '<textarea class="s-reply-input" rows="1" placeholder="回复该会话…（Enter 发送，Shift+Enter 换行）"></textarea>' +
      '<button class="btn primary sm s-reply-send" type="submit">发送</button>' +
      '</div>' +
      '<span class="s-reply-hint" hidden></span>';
    var input = form.querySelector(".s-reply-input");
    var sendBtn = form.querySelector(".s-reply-send");
    var hint = form.querySelector(".s-reply-hint");
    var busy = false;
    // The input and 发送 share one row — the button never takes a line of its
    // own — and the textarea grows with the draft up to the CSS max-height.
    // (jsdom has no layout engine: scrollHeight === 0, so the sync is skipped.)
    var autoGrow = function () {
      if (!input.scrollHeight) { return; }
      input.style.height = "auto";
      input.style.height = Math.min(input.scrollHeight, 160) + "px";
    };
    input.addEventListener("input", autoGrow);
    // Keep clicks inside the reply box from bubbling out to the card.
    form.addEventListener("click", function (e) { e.stopPropagation(); });
    form.addEventListener("submit", function (e) {
      e.preventDefault();
      var text = (input.value || "").trim();
      if (!text || busy) { return; }
      if (node.stale) {
        notify("节点「" + (node.name || node.node_id) + "」离线，无法回复。", "error");
        return;
      }
      busy = true;
      sendBtn.disabled = true;
      hint.textContent = "发送中…";
      hint.hidden = false;
      api("/n/" + encodeURIComponent(node.node_id) + "/web/api/input", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ prompt: text })
      }).then(function (res) {
        return res.json().catch(function () { return null; }).then(function (body) {
          if (!res.ok) {
            throw new Error((body && body.reason) || ("HTTP " + res.status));
          }
          if (body && body.status === "queued") {
            notify("已发送到会话：" + sessionLabelOf(session), "ok");
            input.value = "";
            autoGrow();
            hint.hidden = true;
            hint.textContent = "";
          } else {
            throw new Error((body && body.reason) || (body && body.status) || "发送被拒绝");
          }
        });
      }).catch(function (err) {
        notify("回复失败：" + err.message, "error");
        hint.textContent = "发送失败，可重试";
      }).finally(function () {
        busy = false;
        sendBtn.disabled = false;
      });
    });
    // Enter submits; Shift+Enter inserts a newline (textarea default).
    input.addEventListener("keydown", function (e) {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        form.dispatchEvent(new Event("submit", { cancelable: true }));
      }
    });
    return form;
  }

  function sessionSorter(a, b) {
    if (sessionSort === "title") {
      return sessionLabelOf(a.session) < sessionLabelOf(b.session) ? -1 : 1;
    }
    if (sessionSort === "node") {
      var na = a.node.name || a.node.node_id, nb = b.node.name || b.node.node_id;
      return na < nb ? -1 : na > nb ? 1 : 0;
    }
    // Default sort: sessions that need the operator first (approval/question/
    // error), then running ones, then everything else by recency.
    var ra = attentionSortRank(a.session), rb = attentionSortRank(b.session);
    if (ra !== rb) { return ra - rb; }
    var ba = a.session.busy ? 0 : 1, bb = b.session.busy ? 0 : 1;
    if (ba !== bb) { return ba - bb; }
    var ta = new Date(a.session.updated_at || 0).getTime();
    var tb = new Date(b.session.updated_at || 0).getTime();
    return tb - ta;
  }

  function attentionSortRank(session) {
    switch (session.attention) {
      case "approval": return 0;
      case "question": return 1;
      case "error": return 2;
    }
    return isActiveSessionState(session.state) || session.busy ? 3 : 4;
  }

  /* ── Session view preferences (filter kind + sort, persisted) ── */

  var SESSION_PREFS_KEY = "aicli-gw-session-prefs";
  var SESSIONS_SCROLL_KEY = "aicli-gw-sessions-scroll";

  function loadSessionPrefs() {
    try {
      var p = JSON.parse(localStorage.getItem(SESSION_PREFS_KEY) || "null");
      if (!p) { return; }
      if (typeof p.kind === "string" && p.kind) { sessionFilterKind = p.kind; }
      if (typeof p.sort === "string" && p.sort) { sessionSort = p.sort; }
      if (p.tail === false) { tailEnabled = false; }
    } catch (err) { /* storage unavailable */ }
  }

  function saveSessionPrefs() {
    try {
      localStorage.setItem(SESSION_PREFS_KEY, JSON.stringify({ kind: sessionFilterKind, sort: sessionSort, tail: tailEnabled }));
    } catch (err) { /* storage unavailable */ }
  }

  // Reflect the current filter/sort on the static controls (boot restore,
  // deep links and programmatic changes share one path).
  function syncFilterControls() {
    document.querySelectorAll(".seg-btn").forEach(function (b) {
      var on = b.getAttribute("data-sfilter") === sessionFilterKind;
      b.classList.toggle("active", on);
      // These are toggle buttons in a group, not tabs: aria-pressed (not
      // aria-selected) is what conveys the current filter.
      b.setAttribute("aria-pressed", on ? "true" : "false");
    });
    var sortSel = el("session-sort");
    if (sortSel) { sortSel.value = sessionSort; }
  }

  function findSessionByKey(key) {
    if (!key) { return null; }
    var items = allSessions(lastNodes);
    for (var i = 0; i < items.length; i++) {
      if (items[i].node.node_id + "|" + items[i].session.session_id === key) { return items[i]; }
    }
    return null;
  }

  // ── Session reply drafts: keep user input across list updates ──
  // renderSessionTable rebuilds the card DOM on every update; a reply the user
  // is typing must survive that (and identical SSE frames must not steal focus
  // or caret). Drafts are keyed by node+session and restored in place.
  var sessionDrafts = {};
  var lastSessionTableSignature = "";
  // Per-session UI state: which cards are expanded (kept across re-renders),
  // and the last rendered fingerprint (drives the "changed" pulse).
  var expandedSessions = {};
  var sessionLastSeen = {};
  // Device groups the operator folded; scroll restore runs once per page load.
  var collapsedGroups = {};
  var sessionsScrollRestored = false;
  // Live-output tail: global on/off (persisted) + per-card follow state.
  var tailEnabled = true;
  var tailFollow = {};

  // Fingerprint of the visible session fields; a change pulses the card so a
  // busy list still tells the operator where something moved.
  function sessionFingerprint(s) {
    return [
      s.state || "", s.attention || "", s.busy ? 1 : 0, s.status_text || "",
      s.updated_at || "", (s.last_message || "").length, s.last_message_role || "",
    ].join("|");
  }

  function sessionTableSignature(items) {
    var parts = items.map(function (item) {
      var s = item.session;
      return [
        item.node.node_id, item.node.stale ? 1 : 0, (item.node.capabilities || []).join(","),
        s.session_id, s.title || "", s.state || "", s.attention || "", s.busy ? 1 : 0,
        s.status_text || "", s.status_started_at || "", s.status_tail || "", s.updated_at || "",
        s.preview || "", s.last_message_role || "", s.last_message || "",
      ].join("\u0001");
    });
    // tailEnabled participates: toggling the live-output switch must rebuild
    // the cards even when the session data itself is unchanged.
    return sessionFilterKind + "\u0002" + sessionFilterText + "\u0002" + sessionSort +
      "\u0002" + (tailEnabled ? "1" : "0") + "\u0002" + parts.join("\u0003");
  }

  function captureSessionDrafts() {
    document.querySelectorAll(".session-card[data-draft-key]").forEach(function (card) {
      var input = card.querySelector(".s-reply-input");
      if (!input) { return; }
      var key = card.getAttribute("data-draft-key");
      var focused = document.activeElement === input;
      var text = input.value || "";
      if (!text && !focused) { delete sessionDrafts[key]; return; }
      sessionDrafts[key] = {
        text: text,
        focused: focused,
        start: focused ? input.selectionStart : null,
        end: focused ? input.selectionEnd : null,
      };
    });
  }

  function restoreSessionDrafts(host) {
    host.querySelectorAll(".session-card[data-draft-key]").forEach(function (card) {
      var d = sessionDrafts[card.getAttribute("data-draft-key")];
      var input = card.querySelector(".s-reply-input");
      if (!d || !input) { return; }
      if (d.text) { input.value = d.text; }
      if (d.focused) {
        d.focused = false; // focus is restored once; later re-renders keep it
        input.focus();
        if (d.start !== null && d.start !== undefined) {
          try { input.setSelectionRange(d.start, d.end); } catch (err) { /* detached */ }
        }
      }
    });
  }

  // ── Active session states (shared by cards / filters / detail dialog) ──
  var ACTIVE_SESSION_STATES = { running: true, thinking: true, answering: true, waiting: true, review: true };

  function isActiveSessionState(state) {
    return !!ACTIVE_SESSION_STATES[state];
  }

  function formatStatusElapsed(ms) {
    var seconds = Math.max(0, Math.floor(ms / 1000));
    if (seconds < 60) { return seconds + "s"; }
    var minutes = Math.floor(seconds / 60);
    var rem = seconds % 60;
    if (minutes < 60) { return minutes + "m " + (rem < 10 ? "0" : "") + rem + "s"; }
    return Math.floor(minutes / 60) + "h " + (minutes % 60) + "m";
  }

  // The agent sends the activity anchor once; the console advances the clock
  // locally so a running turn never streams near-identical updates.
  function refreshStatusElapsed() {
    var now = Date.now();
    document.querySelectorAll(".s-status-elapsed[data-started]").forEach(function (node) {
      var started = Date.parse(node.getAttribute("data-started"));
      if (isNaN(started)) { return; }
      node.textContent = "· " + formatStatusElapsed(now - started);
    });
  }
  // Local 5s tick: elapsed clocks + relative labels (内容/同步/心跳) keep
  // advancing even when no SSE frame arrives.
  function refreshRelativeTimes() {
    document.querySelectorAll("[data-rel-ts]").forEach(function (node) {
      var ms = parseInt(node.getAttribute("data-rel-ts"), 10);
      if (!ms) { return; }
      var prefix = node.getAttribute("data-rel-prefix") || "";
      node.textContent = prefix + timeAgo(ms);
      var staleMs = parseInt(node.getAttribute("data-rel-stale-ms") || "0", 10);
      node.classList.toggle("age-stale", !!(staleMs && Date.now() - ms > staleMs));
    });
  }
  setInterval(function () {
    refreshStatusElapsed();
    refreshRelativeTimes();
    pruneStripEvents();
  }, 5000);

  function renderSessionTable(nodes) {
    var host = el("session-table");
    if (!host) { return; }
    var items = allSessions(nodes).filter(sessionMatches).sort(sessionSorter);
    var empty = el("session-empty");
    if (empty) { empty.hidden = items.length > 0; }
    // Skip redundant rebuilds (identical SSE frames): typing, focus and
    // selection stay untouched unless something visible actually changed.
    var signature = sessionTableSignature(items);
    if (signature === lastSessionTableSignature) { return; }
    lastSessionTableSignature = signature;
    captureSessionDrafts();
    host.innerHTML = "";
    // Nodes with several visible sessions collapse into a group (session list
    // grouped by device); single-session nodes stay flat so the two-column
    // board keeps its density.
    var nodeCounts = {};
    items.forEach(function (item) {
      var k = item.node.node_id;
      nodeCounts[k] = (nodeCounts[k] || 0) + 1;
    });
    var renderedNodes = {};
    items.forEach(function (item) {
      var nodeID = item.node.node_id;
      if (renderedNodes[nodeID]) { return; }
      renderedNodes[nodeID] = true;
      if (nodeCounts[nodeID] > 1) {
        host.appendChild(buildSessionGroup(item.node, items.filter(function (it) {
          return it.node.node_id === nodeID;
        })));
        return;
      }
      host.appendChild(buildSessionCard(item));
    });
    restoreSessionDrafts(host);
    if (!sessionsScrollRestored) {
      sessionsScrollRestored = true;
      setTimeout(restoreSessionsScroll, 0);
    }
  }

  // buildSessionGroup wraps several sessions of one node in a collapsible
  // header (device grouping): live/offline dot + name + kind/count/last seen.
  function buildSessionGroup(node, groupItems) {
    var group = document.createElement("section");
    group.className = "session-group" + (node.stale ? " offline" : "");
    var collapsed = !!collapsedGroups[node.node_id];
    group.innerHTML =
      '<button type="button" class="group-head" aria-expanded="' + String(!collapsed) + '">' +
      '<span class="dot ' + (node.stale ? "offline" : "live") + '"></span>' +
      '<span class="group-name"></span><span class="group-meta"></span>' +
      '<span class="group-caret" aria-hidden="true">' + (collapsed ? "▸" : "▾") + '</span></button>' +
      '<div class="group-body"></div>';
    group.querySelector(".group-name").textContent = node.name || node.node_id;
    var latest = 0;
    groupItems.forEach(function (item) {
      var t = new Date(item.session.updated_at || 0).getTime();
      if (t > latest) { latest = t; }
    });
    group.querySelector(".group-meta").textContent =
      (node.kind || "agent") + " · 会话 " + groupItems.length +
      (latest ? " · 最近 " + new Date(latest).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : "");
    var body = group.querySelector(".group-body");
    groupItems.forEach(function (item) { body.appendChild(buildSessionCard(item)); });
    if (collapsed) { body.hidden = true; }
    group.querySelector(".group-head").addEventListener("click", function () {
      var next = !collapsedGroups[node.node_id];
      if (next) { collapsedGroups[node.node_id] = true; } else { delete collapsedGroups[node.node_id]; }
      body.hidden = next;
      group.querySelector(".group-caret").textContent = next ? "▸" : "▾";
      this.setAttribute("aria-expanded", String(!next));
    });
    return group;
  }

  function buildSessionCard(item) {
      var s = item.session, node = item.node;
      var cardKey = node.node_id + "|" + s.session_id;
      var expanded = !!expandedSessions[cardKey];
      var card = document.createElement("div");
      card.className = "session-card" + (node.stale ? " offline" : "") +
        (s.attention ? " att-" + s.attention : "") + (expanded ? " expanded" : "");
      card.setAttribute("data-draft-key", cardKey);
      // Put the card itself in the tab order (arrow-key nav already focuses it,
      // but Tab could not reach it) and give it a spoken name.
      card.setAttribute("tabindex", "0");
      // Pulse when this session visibly moved since the previous render.
      var fingerprint = sessionFingerprint(s);
      if (sessionLastSeen[cardKey] !== undefined && sessionLastSeen[cardKey] !== fingerprint) {
        card.classList.add("card-updated");
      }
      sessionLastSeen[cardKey] = fingerprint;
      var hasReply = nodeHasWebUI(node) && (node.capabilities || []).indexOf("input") !== -1;
      var hasExtras = !!(s.last_message || "") || hasReply || !!(s.status_tail || "");
      // Information is layered by business attribute, top → bottom: identity
      // (title/badge/time) → live activity band (state chip + host activity
      // text + elapsed clock) → content (preview / last message) → meta line
      // (node · ID · sync) → actions → reply → live tail (pinned footer).
      card.innerHTML =
        '<div class="s-title"><span class="dot ' + (node.stale ? "offline" : "live") + '"></span><span class="s-name"></span></div>' +
        '<span class="att-badge s-badge" hidden></span><span class="s-time"></span>' +
        '<div class="s-activity" hidden><span class="s-state" hidden></span>' +
        '<span class="s-status-text"></span><span class="s-status-elapsed" hidden></span></div>' +
        (s.preview ? '<div class="s-preview"></div>' : "") +
        '<div class="s-last" hidden></div>' +
        '<div class="s-meta"><span class="s-node-text"></span>' +
        '<span class="s-id" role="button" tabindex="0" title="点击复制会话 ID">' +
        '<span class="s-id-label">ID</span><code class="s-id-code"></code></span>' +
        '<span class="s-sync" hidden></span></div>' +
        '<div class="s-actions">' +
        (hasExtras ? '<button class="btn ghost sm" type="button" data-expand>' + (expanded ? "收起" : "展开") + '</button>' : '') +
        '<button class="btn ghost sm" type="button" data-detail>详情</button>' +
        (nodeHasWebUI(node)
          ? '<button class="btn ghost sm" type="button" data-open>打开</button>'
          : '<button class="btn ghost sm" type="button" disabled title="' +
            (node.stale ? "节点离线，无法打开" : "节点未提供本地 Web 服务（aicli 需以 --web 启动），无法打开") +
            '">打开</button>') +
        (node.stale ? '<button class="btn danger sm" type="button" data-remove>移除</button>' : '') +
        '</div>' +
        '<div class="s-live" hidden title="实时输出（最后一行），点击展开完整尾流"></div>' +
        '<div class="s-tail" hidden>' +
        '<div class="s-tail-head"><span class="s-tail-title">实时输出</span><span class="s-tail-meta"></span>' +
        '<button class="btn ghost sm" type="button" data-tail-follow>跟随中</button></div>' +
        '<pre class="s-tail-body"></pre></div>';
      card.querySelector(".s-name").textContent = sessionLabelOf(s);
      card.setAttribute("aria-label",
        sessionLabelOf(s) + "，" + (node.name || node.node_id) + "，" + stateLabelOf(s) +
        (s.attention ? "，" + attentionLabel(s.attention) : ""));
      card.querySelector(".s-node-text").textContent =
        (node.name || node.node_id) + " · " + (node.kind || "agent");
      var syncEl = card.querySelector(".s-sync");
      var syncedMs = s.synced_at ? new Date(s.synced_at).getTime() : NaN;
      if (!isNaN(syncedMs)) {
        setRelTime(syncEl, syncedMs, "同步 ", 60000,
          "网关最后收到该会话更新：" + absoluteTime(syncedMs) + "（服务器时间）");
        syncEl.hidden = false;
      }
      var idRow = card.querySelector(".s-id");
      var idEl = card.querySelector(".s-id-code");
      idEl.textContent = s.session_id;
      var copyID = function () {
        copyText(s.session_id, null);
      };
      idRow.addEventListener("click", copyID);
      idRow.addEventListener("keydown", function (e) {
        if (e.key === "Enter" || e.key === " ") { e.preventDefault(); copyID(); }
      });
      // Live activity band (动态状态): state chip (运行中/思考中/回答中/等待中/
      // 审查中/已中断) + the host's one-line activity text + a locally-advanced
      // clock, kept apart from the metadata line so "what is happening now"
      // reads as the card's most time-sensitive zone.
      var stateChip = card.querySelector(".s-state");
      var stateLabel = STATE_LABELS[s.state];
      var chipShown = false;
      if (stateLabel && (isActiveSessionState(s.state) || s.state === "interrupted")) {
        stateChip.textContent = stateLabel;
        stateChip.className = "s-state st-" + s.state;
        stateChip.hidden = false;
        chipShown = true;
      }
      var statusText = (s.status_text || "").trim();
      var startedMs = Date.parse(s.status_started_at || "");
      var showElapsed = isActiveSessionState(s.state) && !isNaN(startedMs);
      if (chipShown || statusText || showElapsed) {
        var activityLine = card.querySelector(".s-activity");
        if (statusText) {
          var statusTextEl = card.querySelector(".s-status-text");
          statusTextEl.textContent = statusText;
          statusTextEl.title = statusText;
        }
        if (showElapsed) {
          var elapsedEl = card.querySelector(".s-status-elapsed");
          elapsedEl.setAttribute("data-started", s.status_started_at);
          elapsedEl.textContent = "· " + formatStatusElapsed(Date.now() - startedMs);
          elapsedEl.hidden = false;
        }
        activityLine.hidden = false;
      }
      if (s.attention) {
        var b = card.querySelector(".s-badge");
        b.textContent = attentionLabel(s.attention);
        b.className = "att-badge s-badge att-" + s.attention;
        b.hidden = false;
      }
      var t = card.querySelector(".s-time");
      var updatedMs = s.updated_at ? new Date(s.updated_at).getTime() : NaN;
      if (!isNaN(updatedMs)) {
        setRelTime(t, updatedMs, "", 0, "内容时间：" + absoluteTime(updatedMs));
      } else { t.hidden = true; }
      if (s.preview) { card.querySelector(".s-preview").textContent = s.preview; }
      // Full last message (multi-line, unlike the one-line preview). Falls back
      // to the preview when the agent build predates the field.
      var lastBlock = card.querySelector(".s-last");
      var lastText = s.last_message || "";
      if (lastText) {
        var role = s.last_message_role || "";
        var label = document.createElement("span");
        label.className = "s-last-role" + (role ? " role-" + role : "");
        label.textContent = roleLabelOf(role);
        var body = document.createElement("span");
        body.className = "s-last-text";
        body.textContent = lastText;
        lastBlock.appendChild(label);
        lastBlock.appendChild(body);
        lastBlock.hidden = false;
      }
      // Live-output tail (bounded by the shared core): rendered only while
      // the global "实时输出" switch is on; the 跟随 button pins the viewport
      // to the newest line and pauses automatically when the user scrolls up.
      var tailText = tailEnabled ? (s.status_tail || "") : "";
      var tailBlock = card.querySelector(".s-tail");
      // Collapsed cards still surface the newest live line, so progress is
      // visible without expanding; clicking it expands the full tail.
      var liveLine = card.querySelector(".s-live");
      if (tailText) {
        var lastLine = (tailText.replace(/\s+$/, "").split("\n").pop() || "").replace(/\s+/g, " ").trim();
        if (lastLine.length > 140) { lastLine = "…" + lastLine.slice(lastLine.length - 140); }
        liveLine.textContent = lastLine || "…";
        liveLine.hidden = false;
        var previewEl = card.querySelector(".s-preview");
        if (previewEl) { previewEl.hidden = true; } // live text wins while active
        liveLine.addEventListener("click", function () {
          if (!expandedSessions[cardKey]) {
            expandedSessions[cardKey] = true;
            card.classList.add("expanded");
            var expandButton = card.querySelector("[data-expand]");
            if (expandButton) { expandButton.textContent = "收起"; }
            rememberPresence(node, s, { tail: true });
          }
          var body = card.querySelector(".s-tail-body");
          if (body && body.scrollIntoView) { body.scrollIntoView({ block: "nearest" }); }
        });
        tailBlock.hidden = false;
        var tailBody = card.querySelector(".s-tail-body");
        tailBody.textContent = tailText;
        card.querySelector(".s-tail-meta").textContent = tailText.length + " 字符";
        var followBtn = card.querySelector("[data-tail-follow]");
        var updateFollowBtn = function () {
          followBtn.textContent = tailFollow[cardKey] === false ? "跳到底部" : "跟随中";
        };
        updateFollowBtn();
        var stickToBottom = function () { tailBody.scrollTop = tailBody.scrollHeight; };
        if (tailFollow[cardKey] !== false) { stickToBottom(); }
        tailBody.addEventListener("scroll", function () {
          var atBottom = tailBody.scrollHeight - tailBody.scrollTop - tailBody.clientHeight < 24;
          tailFollow[cardKey] = atBottom;
          updateFollowBtn();
        });
        followBtn.addEventListener("click", function () {
          tailFollow[cardKey] = true;
          updateFollowBtn();
          stickToBottom();
        });
      }
      // The card body is informational only: opening a session always goes
      // through the explicit 打开 button, so text selection or a mis-tap on
      // the row never navigates away.
      card.querySelector("[data-detail]").addEventListener("click", function () {
        openSessionDetail(node, s);
      });
      // Offline snapshots render the 打开 button disabled (no tunnel); only a
      // live node gets the open handler.
      var openBtn = card.querySelector("[data-open]");
      if (openBtn) {
        openBtn.addEventListener("click", function () {
          openSession(node, s);
        });
      }
      // 展开/收起: the full last message and the reply form live in the
      // expanded area; the choice is remembered per session across re-renders.
      var expandBtn = card.querySelector("[data-expand]");
      if (expandBtn) {
        expandBtn.addEventListener("click", function () {
          var next = !expandedSessions[cardKey];
          if (next) { expandedSessions[cardKey] = true; } else { delete expandedSessions[cardKey]; }
          card.classList.toggle("expanded", next);
          expandBtn.textContent = next ? "收起" : "展开";
          // Expansion is the "I'm watching this session" signal: it gates the
          // live tail on the agent (P1-b) and suppresses pushes while open.
          rememberPresence(node, s, { tail: next });
        });
      }
      // Inline reply: POST through the gateway reverse proxy to the agent's
      // own /web/api/input (the agent injects its local write token and the
      // route allowlist already covers /web). Gated on the agent advertising
      // the "input" capability so stale agents just hide the form.
      if (!node.stale && (node.capabilities || []).indexOf("input") !== -1) {
        card.appendChild(buildReplyForm(node, s));
      }
      // Live output is pinned to the very bottom of the card: below the action
      // row and the reply form. Collapsed cards show the newest streamed line,
      // expanded cards swap it for the full rolling tail (never both).
      card.appendChild(card.querySelector(".s-live"));
      card.appendChild(card.querySelector(".s-tail"));
      var rmBtn = card.querySelector("[data-remove]");
      if (rmBtn) {
        rmBtn.addEventListener("click", function (e) {
          e.stopPropagation();
          if (!window.confirm("从注册表移除该会话条目？\n（仅清除网关侧快照，不影响 agent 上的会话数据。）")) { return; }
          api("/api/nodes/" + encodeURIComponent(node.node_id) +
              "/sessions/" + encodeURIComponent(s.session_id), { method: "DELETE" })
            .then(function () { notify("会话条目已移除", "ok"); loadNodes(); })
            .catch(function (err) { notify("移除失败：" + err, "error"); });
        });
      }
      return card;
  }

  /* ─────────────────────── rendering: inbox ──────────────────── */

  function renderInbox(nodes) {
    var list = el("inbox-list");
    if (!list) { return; }
    var count = el("inbox-count");
    var items = inboxItems(nodes);
    if (count) {
      count.textContent = items.length ? String(items.length) : "";
      count.hidden = !items.length;
    }
    var panel = el("inbox-panel");
    if (panel) { panel.hidden = !items.length; }
    list.innerHTML = "";
    items.forEach(function (item) {
      // A real <button>: the whole card is the action, so it must be reachable
      // by keyboard (a clickable <div> is not).
      var row = document.createElement("button");
      row.type = "button";
      row.className = "card card-action";
      // <button> only allows phrasing content, so the card body uses spans
      // (styled as blocks) rather than divs.
      row.innerHTML =
        '<span class="card-head"><span class="card-title"></span><span class="att-badge" hidden></span></span>' +
        '<span class="card-meta"></span>';
      row.querySelector(".card-title").textContent =
        (item.node.name || item.node.node_id) + " · " + sessionLabelOf(item.session);
      var b = row.querySelector(".att-badge");
      b.textContent = attentionLabel(item.session.attention);
      b.className = "att-badge att-" + item.session.attention;
      b.hidden = false;
      row.querySelector(".card-meta").textContent = item.node.node_id;
      row.addEventListener("click", function () { openSession(item.node, item.session); });
      list.appendChild(row);
    });
  }

  /* ─────────────────────── rendering: nodes ──────────────────── */

  function nodeCard(node) {
    var sessions = node.sessions || [];
    var card = document.createElement("div");
    card.className = "card" + (node.stale ? " offline" : "");
    card.innerHTML =
      '<div class="card-head"><span class="card-title"></span><span class="badge"></span></div>' +
      '<div class="card-meta"></div>' +
      '<div class="node-sessions"></div>' +
      '<div class="card-actions"></div>';
    card.querySelector(".card-title").innerHTML =
      '<span class="dot ' + (node.stale ? "offline" : "live") + '"></span>' +
      (node.name || node.device_name || node.node_id);
    card.querySelector(".badge").textContent =
      (node.kind || "agent") + " · 会话 " + sessions.length;
    var meta = card.querySelector(".card-meta");
    meta.textContent = node.node_id;
    var hbMs = node.last_heartbeat ? new Date(node.last_heartbeat).getTime() : NaN;
    if (!isNaN(hbMs)) {
      meta.appendChild(document.createTextNode(" · "));
      var hb = document.createElement("span");
      hb.className = "n-heartbeat";
      setRelTime(hb, hbMs, node.stale ? "最后心跳 " : "心跳 ",
        node.stale ? 0 : 60000,
        "网关最后收到该节点心跳：" + absoluteTime(hbMs) + "（服务器时间）");
      meta.appendChild(hb);
    }

    // Session rows live in their own divider list; .card-actions stays
    // button-only so the two never share a flex row.
    var sessionsHost = card.querySelector(".node-sessions");
    var actions = card.querySelector(".card-actions");
    if (sessions.length) {
      sessions.slice(0, 3).forEach(function (session) {
        var row = document.createElement("div");
        row.className = "session-row";
        row.innerHTML =
          '<div class="session-info"><span class="session-title"></span><div class="session-preview" hidden></div></div>' +
          '<span class="att-badge" hidden></span><button class="btn ghost sm" type="button">打开</button>';
        row.querySelector(".session-title").textContent = sessionLabelOf(session);
        var p = row.querySelector(".session-preview");
        if (session.preview) {
          p.textContent = session.preview;
          p.hidden = false;
        }
        var b = row.querySelector(".att-badge");
        if (session.attention) {
          b.textContent = attentionLabel(session.attention);
          b.className = "att-badge att-" + session.attention;
        } else {
          b.hidden = true;
        }
        row.querySelector("button").addEventListener("click", function (e) {
          e.stopPropagation();
          openSession(node, session);
        });
        sessionsHost.appendChild(row);
      });
      if (sessions.length > 3) {
        var more = document.createElement("button");
        more.className = "btn ghost sm";
        more.type = "button";
        more.textContent = "查看全部 " + sessions.length + " 个会话";
        more.addEventListener("click", function () { navigate("sessions"); });
        actions.appendChild(more);
      }
    } else if (!node.stale) {
      var open = document.createElement("button");
      open.className = "btn ghost sm";
      open.type = "button";
      open.textContent = "打开节点";
      if (nodeHasWebUI(node)) {
        open.addEventListener("click", function () {
          rememberPresence(node, null);
          window.open(nodeWebBase(node), "_blank");
        });
      } else {
        open.disabled = true;
        open.title = webUnavailableText(node);
      }
      actions.appendChild(open);
    }
    // Snapshot rows never resolve on their own; give the operator a cleanup
    // action. Live nodes are protected (removing them would just be undone by
    // the next heartbeat sync and would confuse more than help).
    if (node.stale) {
      var rm = document.createElement("button");
      rm.className = "btn danger sm";
      rm.type = "button";
      rm.textContent = "移除";
      rm.addEventListener("click", function (e) {
        e.stopPropagation();
        if (!window.confirm("移除离线快照节点 " + (node.name || node.node_id) + "？\n该节点的所有会话条目会一并删除（agent 重新上线会自动重新注册）。")) { return; }
        api("/api/nodes/" + encodeURIComponent(node.node_id), { method: "DELETE" })
          .then(function () { notify("节点已移除：" + (node.name || node.node_id), "ok"); loadNodes(); })
          .catch(function (err) { notify("移除失败：" + err, "error"); });
      });
      actions.appendChild(rm);
    }
    return card;
  }

  function renderNodeLists(nodes) {
    var liveHost = el("node-live-list");
    var offHost = el("node-offline-list");
    var dashHost = el("dash-node-list");
    var staleNodes = nodes.filter(function (n) { return n.stale; });
    var clearBtn = el("clear-stale");
    if (clearBtn) {
      // The one-click cleanup is offered only while offline snapshots exist.
      clearBtn.hidden = !staleNodes.length;
      clearBtn.textContent = staleNodes.length ? "清除全部（" + staleNodes.length + "）" : "清除全部";
    }
    if (liveHost) {
      liveHost.innerHTML = "";
      nodes.filter(function (n) { return !n.stale; }).forEach(function (n) {
        liveHost.appendChild(nodeCard(n));
      });
      if (!liveHost.children.length) {
        liveHost.innerHTML = '<p class="empty">没有在线节点 —— 在 aicli / runtime-server 配置 gateway.url 后自动接入。</p>';
      }
    }
    if (offHost) {
      offHost.innerHTML = "";
      staleNodes.forEach(function (n) {
        offHost.appendChild(nodeCard(n));
      });
      if (!offHost.children.length) {
        offHost.innerHTML = '<p class="empty">没有离线快照。</p>';
      }
    }
    if (dashHost) {
      dashHost.innerHTML = "";
      nodes.slice(0, 4).forEach(function (n) {
        dashHost.appendChild(nodeCard(n));
      });
    }
  }

  function renderAttentionBreakdown(nodes) {
    var panel = el("attention-breakdown");
    if (!panel) { return; }
    var counts = { approval: 0, question: 0, error: 0, completed: 0 };
    var any = false;
    (nodes || []).forEach(function (node) {
      if (node.stale) { return; }
      (node.sessions || []).forEach(function (s) {
        if (s.attention && counts[s.attention] !== undefined) {
          counts[s.attention]++;
          any = true;
        }
      });
    });
    panel.hidden = !any;
    ["approval", "question", "error", "completed"].forEach(function (k) {
      var n = el("attn-" + k);
      if (n) { n.textContent = String(counts[k]); }
    });
  }

  function renderActivityFeed(nodes) {
    var host = el("activity-feed");
    if (!host) { return; }
    // Aggregate: every session with an updated_at, newest first, cap 8.
    var items = allSessions(nodes).filter(function (item) {
      return item.session.updated_at;
    }).sort(function (a, b) {
      return new Date(b.session.updated_at) - new Date(a.session.updated_at);
    }).slice(0, 8);
    host.innerHTML = "";
    if (!items.length) {
      host.innerHTML = '<p class="empty">暂无会话活动。</p>';
      return;
    }
    items.forEach(function (item) {
      var s = item.session;
      // Real <button> (keyboard reachable); the offline state is spelled out
      // instead of being conveyed by the dot colour alone.
      var row = document.createElement("button");
      row.type = "button";
      row.className = "activity-row card-action";
      var when = new Date(s.updated_at);
      var fresh = (Date.now() - when.getTime()) < 5 * 60 * 1000;
      row.innerHTML =
        '<span class="dot ' + (item.node.stale ? "offline" : "live") + '"></span>' +
        '<span class="activity-main">' +
        '<span class="activity-title"></span>' +
        '<span class="activity-when"></span>' +
        '</span>';
      row.querySelector(".activity-title").textContent =
        (item.node.name || item.node.node_id) + " · " + sessionLabelOf(s) +
        (s.attention ? "（" + attentionLabel(s.attention) + "）" : "") +
        (item.node.stale ? "（离线快照）" : "");
      row.querySelector(".activity-when").textContent =
        when.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) +
        (fresh ? " · 刚刚" : "");
      if (fresh) { row.classList.add("fresh"); }
      row.addEventListener("click", function () { openSession(item.node, s); });
      host.appendChild(row);
    });
    var win = el("activity-window");
    if (win) { win.textContent = "最近 " + items.length + " 条"; }
  }

  function renderAll(nodes) {
    updateStats(nodes);
    renderInbox(nodes);
    renderAttentionBreakdown(nodes);
    renderActivityFeed(nodes);
    renderNodeLists(nodes);
    renderSessionTable(nodes);
  }

  /* ─────────────────────── rendering: devices ────────────────── */

  function renderDevices(devices) {
    var list = el("device-list");
    if (!list) { return; }
    list.innerHTML = "";
    if (!devices.length) {
      list.innerHTML = '<p class="empty">还没有设备令牌 —— 签发一个给 aicli / runtime-server 接入用。</p>';
      return;
    }
    devices.forEach(function (d) {
      var card = document.createElement("div");
      card.className = "card device-card" + (d.revoked ? " offline" : "");
      card.innerHTML =
        '<div class="card-head"><span class="card-title"></span><span class="status-chip ' + (d.revoked ? "chip-revoked" : "chip-active") + '"></span></div>' +
        '<div class="device-token-row"><code class="device-prefix"></code></div>' +
        '<div class="card-meta"></div>' +
        '<div class="card-actions"></div>';
      card.querySelector(".card-title").textContent = d.name || d.id;
      card.querySelector(".status-chip").textContent = d.revoked ? "已撤销" : "有效";
      var prefixEl = card.querySelector(".device-prefix");
      // The API only exposes the 8-char identifier (the full token is hashed at
      // rest and shown once at issue time), so render it as the complete id.
      prefixEl.textContent = d.prefix;
      prefixEl.title = "令牌标识（完整令牌仅在签发时显示一次）";
      if (d.revoked) { prefixEl.style.textDecoration = "line-through"; }
      var meta = card.querySelector(".card-meta");
      meta.textContent = "id " + d.id + " · 签发于 " +
        (d.created_at ? new Date(d.created_at).toLocaleString() : "—") +
        (d.last_used && d.last_used.indexOf("0001") === -1
          ? " · 最近使用 " + new Date(d.last_used).toLocaleString() : "");
      var actions = card.querySelector(".card-actions");
      if (!d.revoked) {
        var revoke = document.createElement("button");
        revoke.className = "btn danger sm";
        revoke.type = "button";
        revoke.textContent = "撤销令牌";
        revoke.addEventListener("click", function () {
          if (!window.confirm("撤销令牌「" + (d.name || d.id) + "」？\n使用它的 agent 将无法再注册；撤销后可选择彻底删除记录。")) { return; }
          api("/api/devices/" + encodeURIComponent(d.id), { method: "DELETE" })
            .then(function () { notify("令牌已撤销：" + (d.name || d.id), "ok"); loadDevices(); })
            .catch(function (err) { notify("撤销失败：" + err, "error"); });
        });
        actions.appendChild(revoke);
      } else {
        var del = document.createElement("button");
        del.className = "btn danger sm";
        del.type = "button";
        del.textContent = "删除记录";
        del.addEventListener("click", function () {
          if (!window.confirm("彻底删除已撤销令牌「" + (d.name || d.id) + "」的记录？\n此操作不可恢复（令牌本身已失效）。")) { return; }
          api("/api/devices/" + encodeURIComponent(d.id) + "?purge=1", { method: "DELETE" })
            .then(function () { notify("令牌记录已删除：" + (d.name || d.id), "ok"); loadDevices(); })
            .catch(function (err) { notify("删除失败：" + err, "error"); });
        });
        actions.appendChild(del);
      }
      list.appendChild(card);
    });
  }

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

  // webUnavailableText explains why a node cannot be opened, per host kind.
  function webUnavailableText(node) {
    var name = "节点「" + ((node && (node.name || node.node_id)) || "") + "」";
    if (node && node.kind === "runtime-server") {
      return name + "的本地服务尚未就绪，请稍后重试。";
    }
    return name + "未提供本地 Web 服务（启动 aicli 时需带 --web），无法打开。";
  }

  function sessionOpenTarget(node, sessionID) {
    var base = nodeWebBase(node);
    if (node.kind === "runtime-server") { return base; }
    return base + "?session=" + encodeURIComponent(sessionID || "");
  }

  function openSession(node, session) {
    if (!node) { return; }
    // Offline (stale snapshot) nodes have no tunnel: the proxy would answer
    // 502. Tell the operator instead of pretending to navigate.
    if (node.stale) {
      notify("节点「" + (node.name || node.node_id) + "」当前离线，无法打开 —— 等它重新上线后再试，或先在节点页移除该快照。", "error");
      return;
    }
    // A live node without a Web route (agent started without its web client)
    // would only fail inside the tunnel: refuse up front with the reason.
    if (!nodeHasWebUI(node)) {
      notify(webUnavailableText(node), "error");
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
    readAttention[attentionKey(node, session)] = true;
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
    var first = modal.querySelector(FOCUSABLE_SEL);
    (first || modal).focus();
  }

  function closeModal(id) {
    var modal = el(id);
    if (!modal) { return; }
    modal.hidden = true;
    if (el("token-modal").hidden && el("session-modal").hidden) {
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

  function openModalEl() {
    return !el("session-modal").hidden ? el("session-modal")
      : (!el("token-modal").hidden ? el("token-modal") : null);
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
      (node.name || node.node_id) + " · " + (node.kind || "agent") +
      (node.stale ? " · 离线快照" : "");
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
      el("sd-last").textContent = session.last_message;
      lastWrap.hidden = false;
    } else {
      lastWrap.hidden = true;
    }
    var tailText = (session.status_tail || "").trim();
    var tailWrap = el("sd-tail-wrap");
    if (tailText) {
      el("sd-tail").textContent = tailText;
      tailWrap.hidden = false;
    } else {
      tailWrap.hidden = true;
    }
    openModal("session-modal");
  }

  /* ─────────────────────── attention tracking ────────────────── */

  function trackAttention(nodes) {
    var current = {};
    var fresh = [];
    (nodes || []).forEach(function (node) {
      if (node.stale) { return; }
      (node.sessions || []).forEach(function (session) {
        if (!session.attention) { return; }
        var key = attentionKey(node, session);
        current[key] = true;
        if (isWatching(node.node_id, session.session_id)) {
          readAttention[key] = true;
          return;
        }
        if (!seenAttention[key]) { fresh.push({ node: node, session: session }); }
      });
    });
    var seeded = attentionSeeded;
    Object.keys(readAttention).forEach(function (k) {
      if (!current[k]) { delete readAttention[k]; }
    });
    seenAttention = current;
    attentionSeeded = true;
    if (!seeded) { return; }
    fresh.forEach(function (item) { showAttentionToast(item.node, item.session); });
  }

  function showAttentionToast(node, session) {
    // The "open" affordance is a real button, so the toast is usable without a
    // mouse; clicking anywhere on the toast is no longer the only entry point.
    pushToast(
      (node.name || node.node_id) + " · " + sessionLabelOf(session) + "：" +
        attentionLabel(session.attention),
      null,
      {
        label: "打开",
        run: function () {
          if (!nodeHasWebUI(node)) {
            notify(webUnavailableText(node), "error");
            return;
          }
          window.open(sessionOpenTarget(node, session.session_id), "_blank");
        },
      }
    );
  }

  /* ─────────────────────── presence ──────────────────────────── */

  function rememberPresence(node, session, opts) {
    if (!node || !node.node_id) { return; }
    var target = {
      node_id: node.node_id,
      session_id: session ? session.session_id : "",
      // tail=true asks the gateway to keep the live-output stream on for this
      // session (P1-b watch gating); false releases it.
      tail: !!(opts && opts.tail),
    };
    presenceTargets[target.node_id + "|" + target.session_id] = target;
    sendPresence();
  }

  function sendPresence() {
    if (document.hidden) { return; }
    Object.keys(presenceTargets).forEach(function (key) {
      api("/api/presence", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(presenceTargets[key]),
      }).catch(function () {});
    });
  }

  // releasePresence clears the gateway-side suppression as soon as the page is
  // hidden or unloaded. Without it a phone that backgrounded the console keeps
  // suppressing pushes for the session it last viewed until the presence TTL
  // (default 90s) expires — the classic "切后台后没有及时提醒" report.
  function releasePresence() {
    if (!Object.keys(presenceTargets).length) { return; }
    var body = JSON.stringify({ all: true });
    try {
      fetch("/api/presence/release", {
        method: "POST",
        credentials: "same-origin",
        keepalive: true,
        headers: { "Content-Type": "application/json" },
        body: body,
      }).catch(function () {});
    } catch (e) { /* keepalive unsupported: the TTL still bounds the window */ }
  }

  setInterval(sendPresence, 30000);
  document.addEventListener("visibilitychange", function () {
    if (document.hidden) {
      releasePresence();
      return;
    }
    sendPresence();
    // Returning to the tab: re-sync the notification toggle with the browser's
    // actual permission/subscription (a prompt may have been answered while
    // the page was hidden).
    if (authedState) { refreshPushState(); }
    // Coming back from a suspended tab: if the SSE feed has been quiet for
    // a long stretch, pull a fresh snapshot instead of trusting the cache.
    if (eventsSubscribed && lastEventAt && Date.now() - lastEventAt > 60000) { loadNodes(); }
  });
  window.addEventListener("pagehide", releasePresence);

  /* ─────────────────────── Web Push ──────────────────────────── */

  var pushRegistration = null;
  var pushConfig = null;       // cached GET /api/push/config
  var pushConfigPromise = null;
  var pushSynced = false;      // per-page guard for the idempotent re-register

  function urlBase64ToUint8Array(base64) {
    var padding = "=".repeat((4 - (base64.length % 4)) % 4);
    var normalized = (base64 + padding).replace(/-/g, "+").replace(/_/g, "/");
    var raw = atob(normalized);
    var out = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) { out[i] = raw.charCodeAt(i); }
    return out;
  }

  function setPushLabel(text) {
    var label = el("push-toggle-label");
    if (label) { label.textContent = text; }
  }

  function isIOSDevice() {
    return /iPad|iPhone|iPod/.test(navigator.userAgent) ||
      (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
  }

  function isStandalone() {
    return (window.matchMedia && window.matchMedia("(display-mode: standalone)").matches) ||
      navigator.standalone === true;
  }

  // pushEnvironment explains whether Web Push can work here and, when it
  // cannot, why. The reason is surfaced in the toggle tooltip and in error
  // toasts instead of silently hiding the button: on plain HTTP or in an iOS
  // Safari tab the user previously saw no notification affordance at all.
  function pushEnvironment() {
    if (window.isSecureContext === false) {
      return { ok: false, reason: "当前地址不是 HTTPS（安全上下文），浏览器禁用了 Service Worker 与推送。请改用 https:// 访问网关，或用 --tls-cert/--tls-key 让网关直接提供 TLS。" };
    }
    if (isIOSDevice() && !isStandalone()) {
      return { ok: false, needsInstall: true, reason: "iOS 的 Web Push 只在“添加到主屏幕”后的 PWA 中生效：请用 Safari 打开本页 → 分享 → 添加到主屏幕，再从桌面图标打开并开启通知。" };
    }
    if (!("serviceWorker" in navigator) || !("PushManager" in window)) {
      return { ok: false, reason: "当前浏览器不支持 Web Push。请使用 Chrome / Edge / Safari 16.4+，或改用控制台实时更新。" };
    }
    return { ok: true };
  }

  function loadPushConfig() {
    if (pushConfig) { return Promise.resolve(pushConfig); }
    if (pushConfigPromise) { return pushConfigPromise; }
    pushConfigPromise = apiJSON("/api/push/config").then(function (r) {
      pushConfig = (r && r.res && r.res.ok && r.body) ? r.body : { enabled: false };
    }).catch(function () {
      pushConfig = { enabled: false };
    }).then(function () {
      pushConfigPromise = null;
      return pushConfig;
    });
    return pushConfigPromise;
  }

  function pushLabel() {
    var label = navigator.platform || "";
    if (navigator.userAgent.indexOf("Mobile") !== -1) { label += " mobile"; }
    if (isIOSDevice()) { label += " ios"; }
    return label.trim();
  }

  // Re-POST an existing browser subscription: the gateway upserts by endpoint,
  // so this self-heals a lost/restored push store without user action.
  function syncPushSubscription(sub) {
    if (!sub || pushSynced || !sub.toJSON) { return; }
    pushSynced = true;
    var json = sub.toJSON();
    apiJSON("/api/push/subscriptions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        endpoint: json.endpoint,
        keys: json.keys || {},
        label: pushLabel(),
      }),
    }).catch(function () {});
  }

  function refreshPushState() {
    var btn = el("push-toggle");
    var test = el("push-test");
    if (!btn) { return; }
    btn.hidden = !authedState;
    if (!authedState) { return; }
    var env = pushEnvironment();
    if (!env.ok) {
      setPushLabel(env.needsInstall ? "先安装到主屏幕" : "通知不可用");
      btn.title = env.reason;
      if (test) { test.hidden = true; }
      return;
    }
    if (!("Notification" in window) || !Notification.requestPermission) {
      setPushLabel("通知不可用");
      btn.title = "当前浏览器不支持系统通知。";
      if (test) { test.hidden = true; }
      return;
    }
    if (Notification.permission === "denied") {
      setPushLabel("通知被禁用");
      btn.title = "浏览器/系统已拒绝通知权限：请在站点设置或系统通知设置中允许后刷新页面。";
      if (test) { test.hidden = true; }
      return;
    }
    loadPushConfig().then(function (cfg) {
      if (!cfg || !cfg.enabled) {
        setPushLabel("通知未启用");
        btn.title = "网关未启用推送（--push-enabled=false 或 VAPID 初始化失败）。";
        if (test) { test.hidden = true; }
        return;
      }
      btn.title = "";
      return navigator.serviceWorker.ready
      .then(function (reg) {
        pushRegistration = reg;
        return reg.pushManager.getSubscription();
      })
      .then(function (sub) {
        if (sub && Notification.permission === "granted") {
          setPushLabel("关闭通知");
          if (test) { test.hidden = false; }
          syncPushSubscription(sub);
        } else {
          setPushLabel("开启通知");
          if (test) { test.hidden = true; }
        }
      });
    }).catch(function () {});
  }

  function mapPushError(err) {
    if (err && err.name === "AbortError") {
      return new Error("浏览器推送服务拒绝了订阅 —— 常见原因：当前网络无法访问浏览器的推送服务器（国内 Android 的 Chrome/Edge 依赖 Google FCM，可能不可用），或系统/浏览器策略禁用了推送。iOS 请确认已“添加到主屏幕”；也可改用桌面端控制台或配置替代通知通道。");
    }
    if (err && err.name === "NotAllowedError") {
      return new Error("通知权限被拒绝 —— 请在浏览器地址栏/系统设置中允许通知后重试。");
    }
    return err instanceof Error ? err : new Error(String(err));
  }

  // requestPushPermission must be called inside the click gesture. Chromium can
  // keep the returned promise pending (quiet UI, a prompt that was missed, or a
  // previously dismissed dialog), so we also poll Notification.permission and
  // give the user a full minute before failing with actionable guidance.
  function requestPushPermission() {
    if (Notification.permission === "granted" || Notification.permission === "denied") {
      return Promise.resolve(Notification.permission);
    }
    var req = Notification.requestPermission();
    return new Promise(function (resolve, reject) {
      var settled = false;
      var poll = setInterval(function () {
        var p = Notification.permission;
        if (p === "granted" || p === "denied") { finish(p); }
      }, 500);
      var timer = setTimeout(function () {
        fail(new Error("通知权限仍未确认（已等待 60 秒）。请点击地址栏的站点设置（🔒/⚙ 图标 → 通知 → 允许），或检查系统通知设置，然后刷新页面重试。"));
      }, 60000);
      function finish(value) {
        if (settled) { return; }
        settled = true;
        clearInterval(poll);
        clearTimeout(timer);
        resolve(value);
      }
      function fail(err) {
        if (settled) { return; }
        settled = true;
        clearInterval(poll);
        clearTimeout(timer);
        reject(err);
      }
      req.then(finish, function (err) {
        var p = Notification.permission;
        if (p === "granted" || p === "denied") { finish(p); } else { fail(mapPushError(err)); }
      });
    });
  }

  // enablePush must run inside the user gesture: iOS/Safari only shows the
  // permission prompt while the activation is live, so requestPermission is
  // called synchronously, before any async work (config fetch,
  // serviceWorker.ready, ...).
  function enablePush() {
    var env = pushEnvironment();
    if (!env.ok) { return Promise.reject(new Error(env.reason)); }
    var permission;
    if ("Notification" in window && Notification.requestPermission) {
      permission = requestPushPermission();
    } else {
      permission = Promise.resolve("granted");
    }
    return permission.then(function (state) {
      if (state !== "granted") {
        throw new Error("通知权限未授予（当前：" + state + "）。请在浏览器/系统设置中允许通知后重试。");
      }
      return loadPushConfig();
    }).then(function (cfg) {
      if (!cfg || !cfg.enabled || !cfg.public_key) {
        throw new Error("网关未启用推送（--push-enabled 或 VAPID 初始化失败）");
      }
      return navigator.serviceWorker.ready.then(function (reg) {
        pushRegistration = reg;
        return reg.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: urlBase64ToUint8Array(cfg.public_key),
        });
      });
    }).then(function (sub) {
      if (!sub) { return null; }
      var json = sub.toJSON();
      return apiJSON("/api/push/subscriptions", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          endpoint: json.endpoint,
          keys: json.keys || {},
          label: pushLabel(),
        }),
      }).then(function (r) {
        if (!r.res.ok) { throw new Error("网关订阅失败（HTTP " + r.res.status + "）"); }
        return sub;
      });
    }).catch(function (err) {
      throw mapPushError(err);
    });
  }

  function disablePush() {
    if (!pushRegistration) { return Promise.resolve(); }
    return pushRegistration.pushManager.getSubscription().then(function (sub) {
      if (!sub) { return; }
      var endpoint = sub.endpoint;
      return sub.unsubscribe().then(function () {
        return apiJSON("/api/push/unsubscribe", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ endpoint: endpoint }),
        });
      });
    });
  }

  /* ─────────────────────── theme ─────────────────────────────── */

  // Three-state cycle: follow-system → light → dark → follow-system.
  // Persisted in localStorage; rendered pre-paint by the inline head script.
  function applyTheme(theme) {
    if (theme === "light" || theme === "dark") {
      document.documentElement.setAttribute("data-theme", theme);
    } else {
      document.documentElement.removeAttribute("data-theme");
    }
    var label = theme === "light" ? "亮色" : theme === "dark" ? "暗色" : "跟随系统";
    var labelEl = el("theme-label");
    if (labelEl) { labelEl.textContent = label; }
    var btn = el("theme-toggle");
    if (btn) { btn.title = "当前：" + label + "（点击切换）"; }
  }

  function cycleTheme() {
    var cur = document.documentElement.getAttribute("data-theme");
    var next;
    if (!cur) { next = "light"; }        // system → light
    else if (cur === "light") { next = "dark"; }
    else { next = null; }                // dark → system
    try {
      if (next) { localStorage.setItem("aicli-gw-theme", next); }
      else { localStorage.removeItem("aicli-gw-theme"); }
    } catch (e) { /* storage unavailable */ }
    applyTheme(next);
  }

  el("theme-toggle").addEventListener("click", cycleTheme);
  applyTheme(document.documentElement.getAttribute("data-theme"));

  /* ─────────────────────── wiring ────────────────────────────── */

  // Skip link: move focus into <main> without touching the hash, otherwise the
  // router would read "#main" as an unknown view and snap back to the dashboard.
  var skipLink = document.querySelector(".skip-link");
  if (skipLink) {
    skipLink.addEventListener("click", function (e) {
      e.preventDefault();
      var main = el("main");
      if (main) { main.focus(); }
    });
  }

  el("login-form").addEventListener("submit", function (e) {
    e.preventDefault();
    var passwordEl = el("password");
    var errorEl = el("login-error");
    errorEl.hidden = true;
    passwordEl.setAttribute("aria-invalid", "false");
    function fail(message) {
      errorEl.textContent = message;
      errorEl.hidden = false;
      passwordEl.setAttribute("aria-invalid", "true");
      passwordEl.focus();
    }
    apiJSON("/api/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password: passwordEl.value }),
    }).then(function (r) {
      if (!r.res.ok) {
        // Distinguish a bad password from a gateway/transport failure instead
        // of reporting every failure as "wrong password".
        fail(r.res.status === 401
          ? "登录失败：密码错误"
          : "登录失败：网关返回 " + r.res.status);
        return;
      }
      passwordEl.value = "";
      applyRoute();
      loadNodes();
      loadDevices();
    }).catch(function () {
      fail("登录失败：无法连接网关，请确认服务正在运行");
    });
  });

  function doLogout() {
    api("/api/auth/logout", { method: "POST" }).then(function () { setAuthed(false); });
  }
  el("logout").addEventListener("click", doLogout);
  el("logout-top").addEventListener("click", doLogout);

  el("refresh").addEventListener("click", function () { loadNodes(); loadDevices(); loadConnectors(true); });
  // Connectors tab has its own refresh so a stale list is one click away.
  var refreshConnectors = el("connectors-refresh");
  if (refreshConnectors) {
    refreshConnectors.addEventListener("click", function () { loadConnectors(true); });
  }
  // Keep the standalone nodes-panel refresh working too (same data source).
  var refreshNodes = el("refresh-nodes");
  if (refreshNodes) { refreshNodes.addEventListener("click", function () { loadNodes(); }); }

  // One-click cleanup of offline snapshots (stale entries restored from
  // registry.json). One bulk call removes every stale node at once; the
  // per-node "移除" buttons stay available for selective cleanup.
  var clearStale = el("clear-stale");
  if (clearStale) {
    clearStale.addEventListener("click", function () {
      var staleCount = lastNodes.filter(function (n) { return n.stale; }).length;
      if (!staleCount) { return; }
      if (!window.confirm("清除全部 " + staleCount + " 个离线快照节点？\n这些节点的所有会话条目会一并删除（agent 重新上线会自动重新注册）。")) { return; }
      clearStale.disabled = true;
      api("/api/nodes?stale=1", { method: "DELETE" })
        .then(function (res) {
          if (res.status === 401) { setAuthed(false); return; }
          if (!res.ok) { notify("清除失败：HTTP " + res.status, "error"); return; }
          notify("已清除 " + staleCount + " 个离线快照节点", "ok");
          loadNodes();
        })
        .catch(function (err) { notify("清除失败：" + err, "error"); })
        .finally(function () { clearStale.disabled = false; });
    });
  }

  var pushBusy = false;
  el("push-toggle").addEventListener("click", function () {
    if (pushBusy) { return; }
    // The click handler is the only entry point for enabling: it must stay
    // synchronous up to Notification.requestPermission() (see enablePush).
    var env = pushEnvironment();
    if (!env.ok) { notify(env.reason, "error"); return; }
    if (!("Notification" in window) || !Notification.requestPermission) {
      notify("当前浏览器不支持系统通知。", "error");
      return;
    }
    if (Notification.permission === "denied") {
      notify("通知权限被拒绝：请在浏览器/系统设置中允许通知后刷新页面。", "error");
      return;
    }
    if (pushConfig && pushConfig.enabled === false) {
      notify("网关未启用推送（--push-enabled=false 或 VAPID 初始化失败）。", "error");
      return;
    }
    var disabling = el("push-toggle-label").textContent.indexOf("关闭") !== -1;
    pushBusy = true;
    setPushLabel(disabling ? "关闭中…" : "开启中…");
    var btn = el("push-toggle");
    var test = el("push-test");
    btn.disabled = true;
    Promise.resolve(disabling ? disablePush() : enablePush())
      .then(function () {
        setPushLabel(disabling ? "开启通知" : "关闭通知");
        if (test) { test.hidden = disabling; }
        notify(disabling ? "已关闭通知" : "已开启通知", "ok");
      })
      .catch(function (err) {
        refreshPushState();
        notify("通知操作失败：" + (err && err.message ? err.message : "请检查浏览器通知权限"), "error");
      })
      .finally(function () {
        pushBusy = false;
        btn.disabled = false;
      });
  });

  var pushTest = el("push-test");
  if (pushTest) {
    pushTest.addEventListener("click", function () {
      var idleText = pushTest.textContent;
      pushTest.disabled = true;
      // The API waits for the push service round trip (usually 0.5–3s), so
      // give immediate feedback instead of looking dead.
      pushTest.textContent = "发送中…";
      apiJSON("/api/push/test", { method: "POST" }).then(function (r) {
        if (!r.res.ok) { throw new Error("HTTP " + r.res.status); }
        var ch = r.body && r.body.channels;
        if (ch) {
          var parts = [];
          var ok = true;
          var wp = ch.webpush || {};
          if (wp.enabled) {
            var wd = wp.delivered || 0, wm = wp.subscriptions || 0;
            parts.push("Web Push " + wd + "/" + wm);
            if (wd === 0) { ok = false; }
          } else {
            parts.push("Web Push 未启用");
          }
          var nf = ch.ntfy || {};
          if (nf.enabled) {
            if (nf.delivered) { parts.push("ntfy 已投递" + (nf.topic ? "（" + nf.topic + "）" : "")); }
            else { parts.push("ntfy 失败" + (nf.error ? "：" + nf.error : "")); ok = false; }
          }
          notify("测试通知：" + parts.join("；") + (ok ? "" : "。请检查对应通道配置。"), ok ? "ok" : "error");
          return;
        }
        var delivered = (r.body && r.body.delivered) || 0;
        var total = (r.body && r.body.subscriptions) || 0;
        if (total === 0) {
          notify("网关没有已保存的订阅：请先在本设备开启通知。", "error");
          return;
        }
        if (delivered === 0) {
          notify("推送服务拒收了全部 " + total + " 个订阅（0/" + total + "）：请关闭后重新开启通知，并确认 data/vapid.key 未被重建。", "error");
          return;
        }
        notify("已投递 " + delivered + "/" + total + " 个订阅。若本机未弹出：iOS 需从主屏幕图标打开；Android 请检查系统通知与电池优化。", delivered === total ? "ok" : "error");
      }).catch(function (err) {
        notify("测试通知失败：" + (err && err.message ? err.message : err), "error");
      }).finally(function () { pushTest.disabled = false; pushTest.textContent = idleText; });
    });
  }

  // Mobile sidebar
  el("nav-toggle").addEventListener("click", function () {
    el("sidebar").classList.add("open");
    el("sidebar-scrim").hidden = false;
  });
  el("sidebar-scrim").addEventListener("click", closeSidebar);

  // Desktop collapse: icon rail, persisted across sessions.
  try {
    if (localStorage.getItem("aicli-gw-sb-collapsed") === "1") {
      document.body.classList.add("sb-collapsed");
    }
  } catch (e) { /* storage unavailable */ }
  function setCollapsed(collapsed) {
    document.body.classList.toggle("sb-collapsed", collapsed);
    try { localStorage.setItem("aicli-gw-sb-collapsed", collapsed ? "1" : "0"); } catch (e) {}
  }
  el("sidebar-collapse").addEventListener("click", function () { setCollapsed(true); });
  el("sidebar-expand").addEventListener("click", function () { setCollapsed(false); });

  // Session filters
  el("session-filter").addEventListener("input", function (e) {
    sessionFilterText = e.target.value.trim();
    renderSessionTable(lastNodes);
  });
  document.querySelectorAll(".seg-btn").forEach(function (btn) {
    btn.addEventListener("click", function () {
      sessionFilterKind = btn.getAttribute("data-sfilter");
      syncFilterControls();
      saveSessionPrefs();
      renderSessionTable(lastNodes);
    });
  });
  el("session-sort").addEventListener("change", function (e) {
    sessionSort = e.target.value;
    saveSessionPrefs();
    renderSessionTable(lastNodes);
  });

  // Live-output tail on/off (persisted): expanded cards render the bounded
  // tail only while this switch is on.
  function syncTailToggle() {
    var btn = el("tail-toggle");
    if (btn) { btn.textContent = "实时输出：" + (tailEnabled ? "开" : "关"); }
  }
  el("tail-toggle").addEventListener("click", function () {
    tailEnabled = !tailEnabled;
    if (!tailEnabled) {
      // Turning live output off releases every tail watch (agents stop
      // streaming; presence TTL no longer needs to hold tail targets).
      Object.keys(presenceTargets).forEach(function (k) { presenceTargets[k].tail = false; });
      sendPresence();
    }
    saveSessionPrefs();
    syncTailToggle();
    renderSessionTable(lastNodes);
  });

  // Keyboard model: "/" focuses the search, ↑/↓ (or j/k) walks the session
  // cards, Enter opens the focused card's detail dialog, Space toggles 展开.
  document.addEventListener("keydown", function (e) {
    if (!el("session-modal").hidden || !el("token-modal").hidden) { return; }
    var target = e.target;
    var tag = (target && target.tagName) || "";
    var typing = tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" ||
      !!(target && target.isContentEditable);
    if (e.key === "/" && !typing) {
      e.preventDefault();
      var filterEl = el("session-filter");
      if (filterEl) { filterEl.focus(); }
      return;
    }
    if (typing) { return; }
    var cards = Array.prototype.slice.call(document.querySelectorAll(".session-card"));
    if (!cards.length) { return; }
    var current = target && target.closest ? target.closest(".session-card") : null;
    var idx = current ? cards.indexOf(current) : -1;
    var next = null;
    if (e.key === "ArrowDown" || e.key === "j") {
      next = idx === -1 ? cards[0] : cards[Math.min(idx + 1, cards.length - 1)];
    } else if (e.key === "ArrowUp" || e.key === "k") {
      next = idx === -1 ? cards[cards.length - 1] : cards[Math.max(idx - 1, 0)];
    }
    if (next) {
      e.preventDefault();
      if (next.getAttribute("tabindex") === null) { next.setAttribute("tabindex", "-1"); }
      next.focus();
      next.scrollIntoView({ block: "nearest" });
      return;
    }
    if (current && target === current && (e.key === "Enter" || e.key === " ")) {
      var found = findSessionByKey(current.getAttribute("data-draft-key"));
      if (!found) { return; }
      e.preventDefault();
      if (e.key === " ") {
        var expandButton = current.querySelector("[data-expand]");
        if (expandButton) { expandButton.click(); }
      } else {
        openSessionDetail(found.node, found.session);
      }
    }
  });

  // Token modal
  el("issue-token").addEventListener("click", function () {
    openModal("token-modal");
    el("device-name").focus();
  });
  el("token-modal-close").addEventListener("click", function () {
    closeModal("token-modal");
  });
  el("token-modal").addEventListener("click", function (e) {
    if (e.target === el("token-modal")) { closeModal("token-modal"); }
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !el("token-modal").hidden) { closeModal("token-modal"); }
  });

  // Session detail modal
  el("session-modal-close").addEventListener("click", closeSessionDetail);
  el("session-modal-done").addEventListener("click", closeSessionDetail);
  el("session-modal-open").addEventListener("click", function () {
    if (!detailTarget) { return; }
    openSession(detailTarget.node, detailTarget.session);
    closeSessionDetail();
  });
  el("sd-copy").addEventListener("click", function (e) {
    if (detailTarget) { copyText(detailTarget.session.session_id, e.currentTarget); }
  });
  el("session-modal").addEventListener("click", function (e) {
    if (e.target === el("session-modal")) { closeSessionDetail(); }
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !el("session-modal").hidden) { closeSessionDetail(); }
  });

  el("device-form").addEventListener("submit", function (e) {
    e.preventDefault();
    apiJSON("/api/devices", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: el("device-name").value }),
    }).then(function (r) {
      if (r.res.status === 401) { setAuthed(false); return; }
      el("device-name").value = "";
      var tokenEl = el("device-token");
      if (r.body && r.body.token) {
        tokenEl.innerHTML = "";
        var hint = document.createElement("div");
        hint.className = "token-hint";
        hint.textContent = "完整令牌仅显示这一次，关闭后将无法再次查看（只能撤销重建）——请立即复制保存：";
        var row = document.createElement("div");
        row.className = "token-row";
        var strong = document.createElement("strong");
        strong.textContent = r.body.token;
        var copy = document.createElement("button");
        copy.className = "btn ghost sm";
        copy.type = "button";
        copy.textContent = "复制";
        copy.addEventListener("click", function () {
          var done = function () { copy.textContent = "已复制 ✓"; };
          if (navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(r.body.token).then(done, done);
          } else {
            var ta = document.createElement("textarea");
            ta.value = r.body.token;
            document.body.appendChild(ta);
            ta.select();
            try { document.execCommand("copy"); } catch (err) { /* legacy */ }
            ta.remove();
            done();
          }
        });
        row.appendChild(strong);
        row.appendChild(copy);
        tokenEl.appendChild(hint);
        tokenEl.appendChild(row);
        var note = document.createElement("div");
        note.className = "token-note";
        note.textContent = "列表中只会显示 8 位标识（" + r.body.device.prefix + "），用于辨认；完整令牌不会再次出现。";
        tokenEl.appendChild(note);
        tokenEl.hidden = false;
      }
      loadDevices();
    });
  });

  // Deep-link: #sessions?attention=1 preselects the attention filter
  function applyHashParams() {
    var h = window.location.hash || "";
    var q = h.indexOf("?");
    if (q === -1) { return; }
    var params = new URLSearchParams(h.slice(q + 1));
    if (params.get("attention")) {
      sessionFilterKind = "attention";
      syncFilterControls();
    }
  }

  /* ─────────────────────── PWA install hint ─────────────────── */

  var deferredInstall = null;

  window.addEventListener("beforeinstallprompt", function (event) {
    // Android/Chrome: keep the prompt for an explicit "安装" click instead of
    // letting the mini-infobar disappear without teaching the user why.
    event.preventDefault();
    deferredInstall = event;
    renderInstallHint();
  });

  function installHintDismissed() {
    try { return localStorage.getItem("aicli-gw-install-hint-dismissed") === "1"; } catch (e) { return false; }
  }

  function renderInstallHint() {
    var hint = el("install-hint");
    if (!hint) { return; }
    if (!authedState || installHintDismissed()) { hint.hidden = true; return; }
    var action = el("install-hint-action");
    if (deferredInstall) {
      el("install-hint-text").textContent = "安装到主屏幕后可像 App 一样打开，并在后台收到通知提醒。";
      action.hidden = false;
      hint.hidden = false;
      return;
    }
    if (isIOSDevice() && !isStandalone()) {
      el("install-hint-text").textContent = "iPhone/iPad 需先安装：Safari 分享 → 添加到主屏幕，再从桌面图标打开，后台通知才会生效。";
      action.hidden = true;
      hint.hidden = false;
      return;
    }
    hint.hidden = true;
  }

  el("install-hint-dismiss").addEventListener("click", function () {
    el("install-hint").hidden = true;
    try { localStorage.setItem("aicli-gw-install-hint-dismissed", "1"); } catch (e) { /* storage unavailable */ }
  });
  el("install-hint-action").addEventListener("click", function () {
    var ev = deferredInstall;
    deferredInstall = null;
    el("install-hint").hidden = true;
    if (ev && ev.prompt) { ev.prompt(); }
  });

  /* ─────────────────────── boot ──────────────────────────────── */

  if ("serviceWorker" in navigator) {
    // The worker posts this after showing a system notification, which proves
    // the push reached this browser (vs. the OS suppressing the toast).
    navigator.serviceWorker.addEventListener("message", function (event) {
      var data = event.data || {};
      if (data.type === "push-received") {
        notify("已收到推送：" + (data.title || "通知") + (data.body ? " · " + data.body : ""), "ok");
      }
    });
    window.addEventListener("load", function () {
      // ?v= is bumped with CACHE in sw.js on every release so CDN edges do not
      // keep serving an old worker script.
      navigator.serviceWorker.register("./sw.js?v=35").catch(function () {});
    });
  }

  loadSessionPrefs();
  syncFilterControls();
  syncTailToggle();
  applyHashParams();
  applyRoute();
  // Probe once for an existing owner session; only fetch the device list when
  // the probe authenticated, so a fresh visitor emits a single 401 (the
  // probe itself) in the console instead of two.
  loadNodes().then(function () {
    if (authedState) { loadDevices(); }
  });
  renderInstallHint();
  refreshPushState();
})();
