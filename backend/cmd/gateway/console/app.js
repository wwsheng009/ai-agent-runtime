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
  var sessionFilterKind = "all";
  var sessionSort = "activity";

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

  function setAuthed(authed) {
    el("login").hidden = authed;
    el("app").hidden = !authed;
    el("push-toggle").hidden = !authed;
    el("logout").hidden = !authed;
    el("logout-top").hidden = !authed;
  }

  /* ─────────────────────── hash router ───────────────────────── */

  var VIEW_TITLES = {
    dashboard: "仪表盘",
    sessions: "会话",
    nodes: "节点",
    tokens: "设备令牌",
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
    return apiJSON("/api/nodes").then(function (r) {
      if (r.res.status === 401) { setAuthed(false); return; }
      setAuthed(true);
      lastNodes = (r.body && r.body.nodes) || [];
      renderAll(lastNodes);
      subscribeEvents();
    });
  }

  function loadDevices() {
    return apiJSON("/api/devices").then(function (r) {
      if (r.res.status === 401) { setAuthed(false); return; }
      lastDevices = (r.body && r.body.devices) || [];
      renderDevices(lastDevices);
    });
  }

  var eventsSubscribed = false;
  function subscribeEvents() {
    if (eventsSubscribed || typeof EventSource !== "function") { return; }
    eventsSubscribed = true;
    var source = new EventSource("/api/events");
    source.addEventListener("nodes", function (event) {
      try {
        var payload = JSON.parse(event.data);
        lastNodes = (payload && payload.nodes) || [];
        renderAll(lastNodes);
      } catch (err) { /* ignore malformed frame */ }
    });
    source.addEventListener("error", function () {});
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

  function timeOf(session) {
    if (!session.updated_at) { return ""; }
    var d = new Date(session.updated_at);
    return isNaN(d.getTime()) ? "" : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
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
      chip.innerHTML = '<span class="dot ' + (live ? "live" : "offline") + '"></span>' +
        (live ? live + " 个节点在线" : "无节点在线");
    }
  }

  /* ─────────────────────── rendering: sessions ───────────────── */

  function sessionMatches(item) {
    var s = item.session;
    if (sessionFilterKind === "attention" && !s.attention) { return false; }
    if (sessionFilterKind === "running" && !(s.busy || s.state === "running")) { return false; }
    if (sessionFilterKind === "offline" && !item.node.stale) { return false; }
    if (sessionFilterText) {
      var q = sessionFilterText.toLowerCase();
      var hay = ((s.title || "") + " " + (s.preview || "") + " " + s.session_id).toLowerCase();
      if (hay.indexOf(q) === -1) { return false; }
    }
    return true;
  }

  function sessionSorter(a, b) {
    if (sessionSort === "title") {
      return sessionLabelOf(a.session) < sessionLabelOf(b.session) ? -1 : 1;
    }
    if (sessionSort === "node") {
      var na = a.node.name || a.node.node_id, nb = b.node.name || b.node.node_id;
      return na < nb ? -1 : na > nb ? 1 : 0;
    }
    var ba = a.session.busy ? 0 : 1, bb = b.session.busy ? 0 : 1;
    if (ba !== bb) { return ba - bb; }
    var ta = new Date(a.session.updated_at || 0).getTime();
    var tb = new Date(b.session.updated_at || 0).getTime();
    return tb - ta;
  }

  function renderSessionTable(nodes) {
    var host = el("session-table");
    if (!host) { return; }
    var items = allSessions(nodes).filter(sessionMatches).sort(sessionSorter);
    var empty = el("session-empty");
    if (empty) { empty.hidden = items.length > 0; }
    host.innerHTML = "";
    items.forEach(function (item) {
      var s = item.session, node = item.node;
      var card = document.createElement("div");
      card.className = "session-card" + (node.stale ? " offline" : "") + (s.attention ? " att-" + s.attention : "");
      card.innerHTML =
        '<div class="s-title"><span class="dot ' + (node.stale ? "offline" : "live") + '"></span><span class="s-name"></span></div>' +
        '<span class="attention" hidden></span><span class="s-time"></span>' +
        '<div class="s-node"></div>' +
        (s.preview ? '<div class="s-preview"></div>' : "");
      card.querySelector(".s-name").textContent = sessionLabelOf(s);
      card.querySelector(".s-node").textContent =
        (node.name || node.node_id) + " · " + (node.kind || "agent");
      if (s.attention) {
        var b = card.querySelector(".attention");
        b.textContent = attentionLabel(s.attention);
        b.className = "attention att-" + s.attention;
        b.hidden = false;
      }
      var t = card.querySelector(".s-time");
      var time = timeOf(s);
      if (time) { t.textContent = time; } else { t.hidden = true; }
      if (s.preview) { card.querySelector(".s-preview").textContent = s.preview; }
      card.addEventListener("click", function () { openSession(node, s); });
      host.appendChild(card);
    });
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
      var row = document.createElement("div");
      row.className = "card";
      row.innerHTML =
        '<div class="card-head"><span class="card-title"></span><span class="attention" hidden></span></div>' +
        '<div class="card-meta"></div>';
      row.querySelector(".card-title").textContent =
        (item.node.name || item.node.node_id) + " · " + sessionLabelOf(item.session);
      var b = row.querySelector(".attention");
      b.textContent = attentionLabel(item.session.attention);
      b.className = "attention att-" + item.session.attention;
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
      '<div class="card-actions"></div>';
    card.querySelector(".card-title").innerHTML =
      '<span class="dot ' + (node.stale ? "offline" : "live") + '"></span>' +
      (node.name || node.device_name || node.node_id);
    card.querySelector(".badge").textContent =
      (node.kind || "agent") + " · 会话 " + sessions.length;
    card.querySelector(".card-meta").textContent = node.node_id;

    var actions = card.querySelector(".card-actions");
    if (sessions.length) {
      sessions.slice(0, 3).forEach(function (session) {
        var row = document.createElement("div");
        row.className = "session-row";
        row.innerHTML =
          '<div class="session-info"><span class="session-title"></span><div class="session-preview" hidden></div></div>' +
          '<span class="attention" hidden></span><button class="btn ghost sm" type="button">打开</button>';
        row.querySelector(".session-title").textContent = sessionLabelOf(session);
        var p = row.querySelector(".session-preview");
        if (session.preview) {
          p.textContent = session.preview;
          p.hidden = false;
        }
        var b = row.querySelector(".attention");
        if (session.attention) {
          b.textContent = attentionLabel(session.attention);
          b.className = "attention att-" + session.attention;
        } else {
          b.hidden = true;
        }
        row.querySelector("button").addEventListener("click", function (e) {
          e.stopPropagation();
          openSession(node, session);
        });
        actions.appendChild(row);
      });
      if (sessions.length > 3) {
        var more = document.createElement("button");
        more.className = "btn ghost sm";
        more.type = "button";
        more.textContent = "查看全部 " + sessions.length + " 个会话";
        more.addEventListener("click", function () { navigate("sessions"); });
        actions.appendChild(more);
      }
    } else {
      var open = document.createElement("button");
      open.className = "btn ghost sm";
      open.type = "button";
      open.textContent = "打开节点";
      open.addEventListener("click", function () {
        rememberPresence(node, null);
        window.open(nodeWebBase(node), "_blank");
      });
      actions.appendChild(open);
    }
    return card;
  }

  function renderNodeLists(nodes) {
    var liveHost = el("node-live-list");
    var offHost = el("node-offline-list");
    var dashHost = el("dash-node-list");
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
      nodes.filter(function (n) { return n.stale; }).forEach(function (n) {
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

  function renderAll(nodes) {
    updateStats(nodes);
    renderInbox(nodes);
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
      card.className = "card";
      card.innerHTML =
        '<div class="card-head"><span class="card-title"></span><span class="badge"></span></div>' +
        '<div class="card-meta"></div>' +
        '<div class="card-actions"><button class="btn danger sm" data-revoke>撤销</button></div>';
      card.querySelector(".card-title").textContent = d.name || d.id;
      card.querySelector(".badge").textContent = d.revoked ? "已撤销" : d.prefix + "…";
      card.querySelector(".card-meta").textContent = "id " + d.id;
      card.querySelector("[data-revoke]").addEventListener("click", function () {
        api("/api/devices/" + encodeURIComponent(d.id), { method: "DELETE" }).then(loadDevices);
      });
      list.appendChild(card);
    });
  }

  /* ─────────────────────── session open / read ───────────────── */

  function nodeWebBase(node) {
    var webPath = node.kind === "runtime-server" ? "/" : "/web/";
    return "/n/" + encodeURIComponent(node.node_id) + webPath;
  }

  function sessionOpenTarget(node, sessionID) {
    var base = nodeWebBase(node);
    if (node.kind === "runtime-server") { return base; }
    return base + "?session=" + encodeURIComponent(sessionID || "");
  }

  function openSession(node, session) {
    if (!node) { return; }
    if (session) { markRead(node, session); }
    rememberPresence(node, session);
    window.open(sessionOpenTarget(node, session ? session.session_id : ""), "_blank");
  }

  function markRead(node, session) {
    if (!node || !session || !session.attention) { return; }
    readAttention[attentionKey(node, session)] = true;
    renderInbox(lastNodes);
    updateStats(lastNodes);
    renderSessionTable(lastNodes);
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
    var toast = document.createElement("div");
    toast.className = "toast";
    toast.textContent =
      (node.name || node.node_id) + " · " + sessionLabelOf(session) + "：" +
      attentionLabel(session.attention);
    toast.addEventListener("click", function () {
      window.open(sessionOpenTarget(node, session.session_id), "_blank");
      toast.remove();
    });
    document.body.appendChild(toast);
    setTimeout(function () { toast.remove(); }, 8000);
  }

  /* ─────────────────────── presence ──────────────────────────── */

  function rememberPresence(node, session) {
    if (!node || !node.node_id) { return; }
    var target = { node_id: node.node_id, session_id: session ? session.session_id : "" };
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

  setInterval(sendPresence, 30000);
  document.addEventListener("visibilitychange", function () {
    if (!document.hidden) { sendPresence(); }
  });

  /* ─────────────────────── Web Push ──────────────────────────── */

  var pushRegistration = null;

  function urlBase64ToUint8Array(base64) {
    var padding = "=".repeat((4 - (base64.length % 4)) % 4);
    var normalized = (base64 + padding).replace(/-/g, "+").replace(/_/g, "/");
    var raw = atob(normalized);
    var out = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) { out[i] = raw.charCodeAt(i); }
    return out;
  }

  function setPushLabel(text) { el("push-toggle").textContent = text; }

  function pushSupported() {
    return "serviceWorker" in navigator && "PushManager" in window;
  }

  function initPush() {
    if (!pushSupported()) { el("push-toggle").hidden = true; return; }
    navigator.serviceWorker.ready
      .then(function (reg) {
        pushRegistration = reg;
        return reg.pushManager.getSubscription();
      })
      .then(function (sub) { setPushLabel(sub ? "🔔 关闭通知" : "🔔 开启通知"); })
      .catch(function () {});
  }

  function enablePush() {
    return apiJSON("/api/push/config").then(function (r) {
      if (!r.body || !r.body.enabled || !r.body.public_key) { return null; }
      return navigator.serviceWorker.ready.then(function (reg) {
        pushRegistration = reg;
        return reg.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: urlBase64ToUint8Array(r.body.public_key),
        });
      });
    }).then(function (sub) {
      if (!sub) { return; }
      var json = sub.toJSON();
      return apiJSON("/api/push/subscriptions", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          endpoint: json.endpoint,
          keys: json.keys || {},
          label: (navigator.platform || "") + (navigator.userAgent.indexOf("Mobile") !== -1 ? " mobile" : ""),
        }),
      }).then(function () { setPushLabel("🔔 关闭通知"); });
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
      }).then(function () { setPushLabel("🔔 开启通知"); });
    });
  }

  /* ─────────────────────── wiring ────────────────────────────── */

  el("login-form").addEventListener("submit", function (e) {
    e.preventDefault();
    el("login-error").hidden = true;
    apiJSON("/api/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password: el("password").value }),
    }).then(function (r) {
      if (!r.res.ok) {
        el("login-error").textContent = "登录失败：密码错误";
        el("login-error").hidden = false;
        return;
      }
      el("password").value = "";
      applyRoute();
      loadNodes();
      loadDevices();
    });
  });

  function doLogout() {
    api("/api/auth/logout", { method: "POST" }).then(function () { setAuthed(false); });
  }
  el("logout").addEventListener("click", doLogout);
  el("logout-top").addEventListener("click", doLogout);

  el("refresh").addEventListener("click", function () { loadNodes(); loadDevices(); });

  el("push-toggle").addEventListener("click", function () {
    var enabling = el("push-toggle").textContent.indexOf("开启") !== -1;
    Promise.resolve(enabling ? enablePush() : disablePush()).catch(function () {
      el("login-error").textContent = "通知操作失败（请检查浏览器权限）";
      el("login-error").hidden = false;
    });
  });

  // Mobile sidebar
  el("nav-toggle").addEventListener("click", function () {
    el("sidebar").classList.add("open");
    el("sidebar-scrim").hidden = false;
  });
  el("sidebar-scrim").addEventListener("click", closeSidebar);

  // Session filters
  el("session-filter").addEventListener("input", function (e) {
    sessionFilterText = e.target.value.trim();
    renderSessionTable(lastNodes);
  });
  document.querySelectorAll(".seg-btn").forEach(function (btn) {
    btn.addEventListener("click", function () {
      document.querySelectorAll(".seg-btn").forEach(function (b) { b.classList.remove("active"); });
      btn.classList.add("active");
      sessionFilterKind = btn.getAttribute("data-sfilter");
      renderSessionTable(lastNodes);
    });
  });
  el("session-sort").addEventListener("change", function (e) {
    sessionSort = e.target.value;
    renderSessionTable(lastNodes);
  });

  // Token modal
  el("issue-token").addEventListener("click", function () {
    el("token-modal").hidden = false;
    el("device-name").focus();
  });
  el("token-modal-close").addEventListener("click", function () {
    el("token-modal").hidden = true;
  });
  el("token-modal").addEventListener("click", function (e) {
    if (e.target === el("token-modal")) { el("token-modal").hidden = true; }
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !el("token-modal").hidden) { el("token-modal").hidden = true; }
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
        tokenEl.textContent = "令牌（仅显示一次，立即复制保存）：" + r.body.token;
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
      document.querySelectorAll(".seg-btn").forEach(function (b) {
        b.classList.toggle("active", b.getAttribute("data-sfilter") === "attention");
      });
    }
  }

  /* ─────────────────────── boot ──────────────────────────────── */

  if ("serviceWorker" in navigator) {
    window.addEventListener("load", function () {
      navigator.serviceWorker.register("./sw.js").catch(function () {});
    });
  }

  applyHashParams();
  applyRoute();
  loadNodes();
  loadDevices();
  initPush();
})();
