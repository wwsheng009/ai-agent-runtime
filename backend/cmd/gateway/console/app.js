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

  function notify(text, kind) {
    var n = document.createElement("div");
    n.className = "toast" + (kind ? " toast-" + kind : "");
    n.textContent = text;
    n.addEventListener("click", function () { n.remove(); });
    document.body.appendChild(n);
    setTimeout(function () { n.remove(); }, 3500);
  }

  function setAuthed(authed) {
    el("login").hidden = authed;
    el("app").hidden = !authed;
    el("push-toggle").hidden = !authed;
    el("logout").hidden = !authed;
    el("logout-top").hidden = !authed;
    // Two-screen layout: pre-auth = standalone login page (chrome hidden),
    // authed = full workspace. The class also gates the chrome via CSS.
    document.body.classList.toggle("login-mode", !authed);
    if (!authed) {
      closeSidebar();
      el("password").focus();
    }
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
      var hay = ((s.title || "") + " " + (s.preview || "") + " " + (s.last_message || "") + " " + s.session_id).toLowerCase();
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
      '<textarea class="s-reply-input" rows="2" placeholder="回复该会话…（Enter 发送，Shift+Enter 换行）"></textarea>' +
      '<div class="s-reply-row"><span class="s-reply-hint" hidden></span>' +
      '<button class="btn sm" type="submit">发送</button></div>';
    var input = form.querySelector(".s-reply-input");
    var hint = form.querySelector(".s-reply-hint");
    var busy = false;
    // Keep clicks inside the reply box from triggering the card's "open
    // session" navigation handler.
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
        '<span class="att-badge s-badge" hidden></span><span class="s-time"></span>' +
        '<div class="s-node"></div>' +
        (s.preview ? '<div class="s-preview"></div>' : "") +
        '<div class="s-last" hidden></div>' +
        '<div class="s-actions"><button class="btn ghost sm" data-open>打开</button>' +
        (node.stale ? '<button class="btn danger sm" data-remove>移除</button>' : '') +
        '</div>';
      card.querySelector(".s-name").textContent = sessionLabelOf(s);
      card.querySelector(".s-node").textContent =
        (node.name || node.node_id) + " · " + (node.kind || "agent");
      if (s.attention) {
        var b = card.querySelector(".s-badge");
        b.textContent = attentionLabel(s.attention);
        b.className = "att-badge s-badge att-" + s.attention;
        b.hidden = false;
      }
      var t = card.querySelector(".s-time");
      var time = timeOf(s);
      if (time) { t.textContent = time; } else { t.hidden = true; }
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
      card.addEventListener("click", function () { openSession(node, s); });
      card.querySelector("[data-open]").addEventListener("click", function (e) {
        e.stopPropagation();
        openSession(node, s);
      });
      // Inline reply: POST through the gateway reverse proxy to the agent's
      // own /web/api/input (the agent injects its local write token and the
      // route allowlist already covers /web). Gated on the agent advertising
      // the "input" capability so stale agents just hide the form.
      if (!node.stale && (node.capabilities || []).indexOf("input") !== -1) {
        card.appendChild(buildReplyForm(node, s));
      }
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
        '<div class="card-head"><span class="card-title"></span><span class="att-badge" hidden></span></div>' +
        '<div class="card-meta"></div>';
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
    card.querySelector(".card-meta").textContent = node.node_id;

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
      open.addEventListener("click", function () {
        rememberPresence(node, null);
        window.open(nodeWebBase(node), "_blank");
      });
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
      var row = document.createElement("div");
      row.className = "activity-row";
      var when = new Date(s.updated_at);
      var fresh = (Date.now() - when.getTime()) < 5 * 60 * 1000;
      row.innerHTML =
        '<span class="dot ' + (item.node.stale ? "offline" : "live") + '"></span>' +
        '<div class="activity-main">' +
        '<span class="activity-title"></span>' +
        '<span class="activity-when"></span>' +
        '</div>';
      row.querySelector(".activity-title").textContent =
        (item.node.name || item.node.node_id) + " · " + sessionLabelOf(s) +
        (s.attention ? "（" + attentionLabel(s.attention) + "）" : "");
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

  function setPushLabel(text) {
    var label = el("push-toggle-label");
    if (label) { label.textContent = text; }
  }

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
      .then(function (sub) { setPushLabel(sub ? "关闭通知" : "开启通知"); })
      .catch(function () {});
  }

  function enablePush() {
    return apiJSON("/api/push/config").then(function (r) {
      if (!r.body || !r.body.enabled || !r.body.public_key) {
        throw new Error("网关未启用推送（--push-enabled 或 VAPID 初始化失败）");
      }
      return navigator.serviceWorker.ready.then(function (reg) {
        pushRegistration = reg;
        return reg.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: urlBase64ToUint8Array(r.body.public_key),
        }).catch(function (err) {
          // Map raw browser errors to actionable messages.
          if (err && err.name === "AbortError") {
            throw new Error("浏览器推送服务拒绝了订阅 —— 通常因为当前网络无法访问浏览器的推送服务器，或浏览器策略禁用了推送。可改用 Chrome/Edge 正常网络环境，或使用控制台实时更新替代。");
          }
          if (err && err.name === "NotAllowedError") {
            throw new Error("通知权限被拒绝 —— 请在浏览器地址栏的权限设置中允许通知后重试。");
          }
          throw err;
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
      }).then(function (r) {
        if (!r.res.ok) { throw new Error("网关订阅失败（HTTP " + r.res.status + "）"); }
        setPushLabel("关闭通知");
      });
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
      }).then(function () { setPushLabel("开启通知"); });
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
    var enabling = el("push-toggle-label").textContent.indexOf("开启") !== -1;
    pushBusy = true;
    setPushLabel(enabling ? "开启中…" : "关闭中…");
    var btn = el("push-toggle");
    btn.disabled = true;
    Promise.resolve(enabling ? enablePush() : disablePush())
      .then(function () {
        notify(enabling ? "已开启通知" : "已关闭通知", "ok");
      })
      .catch(function (err) {
        setPushLabel(enabling ? "开启通知" : "关闭通知");
        notify("通知操作失败：" + (err && err.message ? err.message : "请检查浏览器通知权限"), "error");
      })
      .finally(function () {
        pushBusy = false;
        btn.disabled = false;
      });
  });

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
