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

  // connectorSchemaProviders 是三端共用的 GET /api/connectors/schema 缓存
  // （providers 数组）；连接器卡片据此渲染通用动作按钮（kind=button，
  // 如 QQ「授权校验」）。拉取失败/未接线时静默降级（卡片仍可用）。
  var connectorSchemaProviders = null;

  function loadConnectorSchema() {
    if (connectorSchemaProviders) { return Promise.resolve(connectorSchemaProviders); }
    return apiJSON("/api/connectors/schema").then(function (r) {
      if (r.res.ok && r.body && Array.isArray(r.body.providers)) {
        connectorSchemaProviders = r.body.providers;
      }
      return connectorSchemaProviders;
    }).catch(function () { return null; });
  }

  function connectorSchemaFor(provider) {
    if (!connectorSchemaProviders) { return null; }
    for (var i = 0; i < connectorSchemaProviders.length; i++) {
      if (connectorSchemaProviders[i].provider === provider) { return connectorSchemaProviders[i]; }
    }
    return null;
  }

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
      // Schema 加载完成后补渲染动作按钮（首帧不被 schema 请求阻塞）。
      loadConnectorSchema().then(function (providers) {
        if (providers && window.location.hash.replace(/^#\/?/, "").split("?")[0] === "connectors") {
          renderConnectors(r.body.items, "");
        }
      });
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
      // 通用动作（Schema kind=button，如 QQ「授权校验」）：直接执行并把返回
      // 摘要（message 优先）以 toast 展示；二维码类 action 仍由 web 端承载。
      var schema = connectorSchemaFor(item.provider);
      var actions = (schema && schema.actions) || [];
      actions.forEach(function (action) {
        if (!action || action.kind !== "button" || !action.key) { return; }
        var actionBtn = document.createElement("button");
        actionBtn.type = "button";
        actionBtn.className = "btn ghost sm";
        actionBtn.textContent = action.label || action.key;
        actionBtn.addEventListener("click", function () {
          runConnectorActionButton(item, action, actionBtn);
        });
        card.querySelector(".card-actions").appendChild(actionBtn);
      });
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

  // runConnectorActionButton 执行一次通道动作（POST /api/connectors/{id}/actions/{action}，
  // 空体=无参数）；执行期间禁用按钮防连点，失败给出可读错误。
  function runConnectorActionButton(item, action, btn) {
    var label = action.label || action.key;
    var path = "/api/connectors/" + encodeURIComponent(item.id) + "/actions/" + encodeURIComponent(action.key);
    btn.disabled = true;
    apiJSON(path, { method: "POST" }).then(function (r) {
      btn.disabled = false;
      if (!r.res.ok) {
        notify(label + "失败：" + ((r.body && r.body.error) || ("HTTP " + r.res.status)), "error");
        return;
      }
      var summary = connectorActionSummary(r.body);
      notify("动作完成：" + (summary || label));
    }).catch(function (err) {
      btn.disabled = false;
      notify(label + "失败：" + (err && err.message ? err.message : err), "error");
    });
  }

  // connectorActionSummary 压一行摘要：message 优先（QQ 授权校验），
  // 回退 status（微信 login_status），再回退整个载荷 JSON。
  function connectorActionSummary(body) {
    if (!body) { return ""; }
    if (typeof body.message === "string" && body.message.trim()) { return body.message.trim(); }
    if (typeof body.status === "string" && body.status.trim()) { return body.status.trim(); }
    var keys = Object.keys(body);
    if (!keys.length) { return ""; }
    try { return JSON.stringify(body); } catch (err) { return ""; }
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

  // 子代理行（wire.SessionSummary.parent）是 display-only 的从属会话：没有自己的
  // 注意力轴，只跟随父会话展示与筛选（docs/gateway/07-console.md）。
  function isSubagentSession(s) {
    return !!(s && s.parent && s.parent.parent_session_id);
  }

  // orderSessionsWithChildren 把列表重排成「父会话紧跟其子代理行」，并丢弃父会话
  // 不可见的孤儿子行（父被过滤或子代理行被隐藏时，子行不应单独出现）。
  function orderSessionsWithChildren(items) {
    var visibleParents = {};
    var childrenByParent = {};
    items.forEach(function (item) {
      if (!isSubagentSession(item.session)) { visibleParents[item.session.session_id] = true; }
    });
    items.forEach(function (item) {
      if (!isSubagentSession(item.session)) { return; }
      var pid = item.session.parent.parent_session_id;
      if (!visibleParents[pid]) { return; }
      (childrenByParent[pid] = childrenByParent[pid] || []).push(item);
    });
    var out = [];
    items.forEach(function (item) {
      if (isSubagentSession(item.session)) { return; }
      out.push(item);
      var kids = childrenByParent[item.session.session_id];
      if (kids && kids.length) { out = out.concat(kids); }
    });
    return out;
  }

  // 建/更分离约定（本文件所有行组件共用）：create 只组一次骨架与事件，事件
  // 处理器读 row.__item 取最新数据；update 做原位补丁。数据刷新不重建 DOM，
  // 滚动位置、选中文本与焦点得以保全——这是繁忙看板保持可读的关键。
  function createSubagentRow() {
    var row = document.createElement("div");
    row.className = "session-card s-child";
    row.setAttribute("tabindex", "0");
    row.innerHTML =
      '<span class="s-child-arrow" aria-hidden="true">↳</span>' +
      '<span class="s-child-title"></span>' +
      '<span class="s-child-agent chip"></span>' +
      '<span class="s-child-state"></span>';
    row.addEventListener("click", function () {
      var it = row.__item;
      if (it) { openSession(it.node, it.session); }
    });
    return row;
  }

  // updateSubagentRow renders one live child task: indent + agent identity + the
  // coarse state. Clicking opens the same session detail dialog as any row.
  function updateSubagentRow(row, item) {
    var s = item.session, node = item.node;
    row.__item = item;
    row.classList.toggle("offline", !!node.stale);
    setText(row.querySelector(".s-child-title"), sessionLabelOf(s));
    var agent = s.parent.agent_id || "";
    if (s.parent.agent_type) { agent += (agent ? " · " : "") + s.parent.agent_type; }
    setText(row.querySelector(".s-child-agent"), agent);
    setText(row.querySelector(".s-child-state"), stateLabelOf(s));
  }

  function sessionMatches(item) {
    var s = item.session;
    if (!showSubagents && isSubagentSession(s)) { return false; }
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

  // 角色标签：前四个是网关快照里的 last_message_role；其余来自节点
  // /web/api/screen 的 transcript（会话面板按原样渲染这些行）。
  var ROLE_LABELS = {
    user: "用户",
    assistant: "助手",
    reasoning: "推理",
    tool: "工具",
    system: "系统",
    command: "命令",
    diagnostic: "诊断",
    runtime: "运行时",
  };

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
    // Touch keyboards: Enter should submit, not insert a newline. A soft
    // keyboard sends keydown with key === "Enter" and no shiftKey, so this is
    // the same branch desktop already uses; multi-line drafts still use the
    // on-screen hint (Shift+Enter) where a hardware keyboard exists.
    input.setAttribute("enterkeyhint", "send");
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
      if (p.subagents === false) { showSubagents = false; }
      if (p.md === false) { mdEnabled = false; }
    } catch (err) { /* storage unavailable */ }
  }

  function saveSessionPrefs() {
    try {
      localStorage.setItem(SESSION_PREFS_KEY, JSON.stringify({ kind: sessionFilterKind, sort: sessionSort, tail: tailEnabled, subagents: showSubagents, md: mdEnabled }));
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
  // renderSessionTable patches keyed card DOM in place (see below); a card is
  // only re-created when it is new, re-parented or switched row kind. Drafts
  // are keyed by node+session so even that rebuild keeps the text (and
  // identical SSE frames must not steal focus or caret).
  var sessionDrafts = {};
  var lastSessionTableSignature = "";
  // Keyed DOM registry for the incremental board: key → element. Rows and
  // groups are patched in place so scroll positions, text selection, focus and
  // the live tail survive a data update instead of being wiped by a rebuild.
  var sessionRowEls = {};
  // Per-session UI state: which cards are expanded (kept across re-renders),
  // and the last rendered fingerprint (drives the "changed" pulse).
  var expandedSessions = {};
  var sessionLastSeen = {};
  // Device groups the operator folded; scroll restore runs once per page load.
  var collapsedGroups = {};
  var sessionsScrollRestored = false;
  // Live-output tail: global on/off (persisted) + per-card follow state.
  var tailEnabled = true;
  // 子代理行开关（持久化）：宿主按 AICLI_GATEWAY_REPORT_SUBAGENTS 上报子会话行，
  // 控制台默认显示，可折叠成"只看顶层会话"（docs/gateway/07-console.md）。
  var showSubagents = true;
  var tailFollow = {};
  // 消息正文 Markdown 渲染（持久化，默认开）：会话卡片最近消息、实时输出与
  // 详情弹窗共用内置渲染器 renderMarkdown——无外部依赖，先转义再渲染，不支持
  // 原始 HTML，链接仅放行 http(s)/mailto。
  var mdEnabled = true;

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
        s.parent ? (s.parent.agent_id || "") : "",
      ].join("\u0001");
    });
    // tailEnabled participates: toggling the live-output switch must rebuild
    // the cards even when the session data itself is unchanged.
    return sessionFilterKind + "\u0002" + sessionFilterText + "\u0002" + sessionSort +
      "\u0002" + (tailEnabled ? "1" : "0") + "\u0002" + (showSubagents ? "1" : "0") +
      "\u0002" + (mdEnabled ? "1" : "0") +
      "\u0002" + parts.join("\u0003");
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

  /* ─────────────────────── markdown rendering ────────────────── */

  function escapeHTML(text) {
    return String(text).replace(/[&<>"']/g, function (ch) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[ch];
    });
  }

  // safeLinkHref 只放行 http(s)/mailto 链接（输入已转义）：其余协议整体丢弃，
  // 防止 agent 输出里的 javascript: 等伪协议被点开。
  function safeLinkHref(href) {
    return /^(https?:\/\/|mailto:)/i.test(href) ? href : "";
  }

  // mdInline 渲染一行内的标记（输入为未转义的原始文本，内部先转义）。代码
  // span 与链接先抽取成占位符，避免强调/删除线规则改写它们内部的文本。
  function mdInline(raw) {
    var tokens = [];
    var stash = function (html) {
      tokens.push(html);
      return "\u0000" + (tokens.length - 1) + "\u0000";
    };
    var out = escapeHTML(raw)
      .replace(/`([^`\n]+)`/g, function (m, code) { return stash("<code>" + code + "</code>"); })
      .replace(/\[([^\]\n]+)\]\(([^)\s]+)\)/g, function (m, label, href) {
        var safe = safeLinkHref(href);
        if (!safe) { return m; }
        return stash('<a href="' + safe + '" target="_blank" rel="noopener noreferrer">' + label + "</a>");
      });
    out = out
      .replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>")
      .replace(/__([^_\n]+)__/g, "<strong>$1</strong>")
      .replace(/~~([^~\n]+)~~/g, "<del>$1</del>")
      .replace(/\*([^*\n]+)\*/g, "<em>$1</em>")
      .replace(/(^|[^A-Za-z0-9_])_([^_\n]+)_(?![A-Za-z0-9_])/g, "$1<em>$2</em>");
    return out.replace(/\u0000(\d+)\u0000/g, function (m, i) { return tokens[+i]; });
  }

  // renderMarkdown 是有意收敛的子集渲染器（消息正文用）：标题、粗体/斜体/
  // 删除线、行内代码、围栏代码块、有序/无序列表、引用、分割线、链接与 GFM
  // 管道表格；段落内
  // 的单个换行保留为 <br>（聊天语义）。先逐行判块、再行内渲染；输出不含原始
  // HTML（输入全量转义），可安全写入 innerHTML。

  // GFM 管道表格：行必须以 | 起止（与节点 micro web client 的 markdown.js 同一
  // 识别规则，保证同一条消息在面板与原版 Web 里渲染一致）；第二行是分隔行
  // （:--- / :---: / ---:）时首行为表头，否则整块当无表头表格。
  var MD_TABLE_ROW = /^ {0,3}\|.*\|[ \t]*$/;

  function mdSplitCells(row) {
    var s = String(row).trim();
    if (s.charAt(0) === "|") { s = s.slice(1); }
    if (s.charAt(s.length - 1) === "|") { s = s.slice(0, -1); }
    return s.split("|").map(function (c) { return c.trim(); });
  }

  function mdIsSeparatorRow(cells) {
    return cells.length > 0 && cells.every(function (c) { return /^:?-{3,}:?$/.test(c); });
  }

  // mdAlignOf 只可能返回 left/center/right/""，因此下面的 style 属性拼接没有
  // 注入面（值来自固定集合，不来自消息内容）。
  function mdAlignOf(cell) {
    if (/^:.*:$/.test(cell)) { return "center"; }
    if (/^:/.test(cell)) { return "left"; }
    if (/:$/.test(cell)) { return "right"; }
    return "";
  }

  function mdTable(rows) {
    var hasHeader = rows.length > 1 && mdIsSeparatorRow(mdSplitCells(rows[1]));
    var header = hasHeader ? mdSplitCells(rows[0]) : null;
    var aligns = hasHeader ? mdSplitCells(rows[1]).map(mdAlignOf) : [];
    var body = (hasHeader ? rows.slice(2) : rows).map(mdSplitCells);
    var styleAt = function (i) {
      return aligns[i] ? ' style="text-align:' + aligns[i] + '"' : "";
    };
    var h = "";
    if (header) {
      h += "<thead><tr>";
      header.forEach(function (c, i) { h += "<th" + styleAt(i) + ">" + mdInline(c) + "</th>"; });
      h += "</tr></thead>";
    }
    if (body.length) {
      h += "<tbody>";
      body.forEach(function (r) {
        h += "<tr>" + r.map(function (c, i) {
          return "<td" + styleAt(i) + ">" + mdInline(c) + "</td>";
        }).join("") + "</tr>";
      });
      h += "</tbody>";
    }
    return "<table>" + h + "</table>";
  }

  function renderMarkdown(text) {
    var lines = String(text || "").replace(/\r\n?/g, "\n").split("\n");
    var html = [];
    var para = [];
    var flushPara = function () {
      if (!para.length) { return; }
      html.push("<p>" + para.map(mdInline).join("<br>") + "</p>");
      para = [];
    };
    var i = 0;
    while (i < lines.length) {
      var line = lines[i];
      var fence = line.match(/^\s*(```|~~~)\s*([\w.+#-]*)\s*$/);
      if (fence) {
        flushPara();
        var buf = [];
        i++;
        for (; i < lines.length; i++) {
          if (/^\s*(```|~~~)\s*$/.test(lines[i])) { i++; break; }
          buf.push(lines[i]);
        }
        html.push('<pre class="md-code"><code>' + escapeHTML(buf.join("\n")) + "</code></pre>");
        continue;
      }
      if (/^\s*(-{3,}|\*{3,}|_{3,})\s*$/.test(line)) { flushPara(); html.push("<hr>"); i++; continue; }
      var head = line.match(/^\s*(#{1,6})\s+(.*)$/);
      if (head) {
        flushPara();
        var lvl = Math.min(6, head[1].length + 2); // # → h3：#/## 对卡片字号太粗放
        html.push("<h" + lvl + ">" + mdInline(head[2]) + "</h" + lvl + ">");
        i++;
        continue;
      }
      if (/^\s*>\s?/.test(line)) {
        flushPara();
        var quote = [];
        while (i < lines.length && /^\s*>\s?/.test(lines[i])) {
          quote.push(lines[i].replace(/^\s*>\s?/, ""));
          i++;
        }
        html.push("<blockquote>" + quote.map(mdInline).join("<br>") + "</blockquote>");
        continue;
      }
      // 表格：连续 | 行（至少两行）——与 micro web client 的块级扫描同一位置
      // （列表之前），否则单元格里的 "-" 会被列表规则先吃掉。
      if (MD_TABLE_ROW.test(line)) {
        var trows = [];
        var tj = i;
        while (tj < lines.length && MD_TABLE_ROW.test(lines[tj])) { trows.push(lines[tj]); tj++; }
        if (trows.length >= 2) {
          flushPara();
          html.push(mdTable(trows));
          i = tj;
          continue;
        }
      }
      var ul = line.match(/^\s*[-*+]\s+(.*)$/);
      var ol = line.match(/^\s*\d+[.)]\s+(.*)$/);
      if (ul || ol) {
        flushPara();
        var ordered = !!ol;
        var items = [];
        while (i < lines.length) {
          var item = lines[i].match(ordered ? /^\s*\d+[.)]\s+(.*)$/ : /^\s*[-*+]\s+(.*)$/);
          if (!item) { break; }
          items.push("<li>" + mdInline(item[1]) + "</li>");
          i++;
        }
        html.push(ordered ? "<ol>" + items.join("") + "</ol>" : "<ul>" + items.join("") + "</ul>");
        continue;
      }
      if (!line.trim()) { flushPara(); i++; continue; }
      para.push(line);
      i++;
    }
    flushPara();
    return html.join("");
  }

  // setMessageHTML 渲染消息正文（Markdown 开关决定富文本或纯文本），并且只在
  // 源文本/渲染模式变化时写入——流式刷新因此不会触碰未变化的 DOM。
  function setMessageHTML(node, text) {
    if (!node) { return; }
    var src = text || "";
    if (node.__mdSrc === src && node.__mdOn === mdEnabled) { return; }
    node.__mdSrc = src;
    node.__mdOn = mdEnabled;
    if (mdEnabled) {
      node.classList.add("md");
      node.innerHTML = renderMarkdown(src);
    } else {
      node.classList.remove("md");
      setText(node, src);
    }
  }

  // ── Incremental board: keyed reconcile instead of a full rebuild ──
  // setText writes only when the value actually changed: untouched nodes keep
  // text selection and avoid needless layout invalidation on every data tick.
  function setText(node, text) {
    if (node && node.textContent !== text) { node.textContent = text; }
  }

  // reconcileChildren reorders host's direct children to exactly match
  // `desired` (an ordered element array): unwanted nodes are removed, desired
  // nodes are moved (never re-created) into place. Element identity — and with
  // it scroll position, text selection, focus and listeners — is preserved.
  function reconcileChildren(host, desired) {
    Array.prototype.slice.call(host.children).forEach(function (child) {
      if (desired.indexOf(child) === -1) { child.remove(); }
    });
    var refNode = host.firstChild;
    desired.forEach(function (child) {
      if (child === refNode) {
        refNode = refNode.nextSibling;
        return;
      }
      host.insertBefore(child, refNode);
    });
  }

  // pruneSessionRowEls forgets registry entries whose elements left the board
  // (filtered out, removed snapshot, …) so the next render starts clean.
  function pruneSessionRowEls() {
    Object.keys(sessionRowEls).forEach(function (key) {
      if (!sessionRowEls[key].isConnected) { delete sessionRowEls[key]; }
    });
  }

  // ensureSessionRow returns the keyed element for one item, patching it in
  // place; only new rows (or rows that switched kind) allocate DOM.
  function ensureSessionRow(item) {
    var key = "c:" + item.node.node_id + "|" + item.session.session_id;
    var kind = isSubagentSession(item.session) ? "child" : "card";
    var rowEl = sessionRowEls[key];
    if (rowEl && rowEl.__kind === kind) {
      if (kind === "card") { updateSessionCard(rowEl, item); }
      else { updateSubagentRow(rowEl, item); }
      return rowEl;
    }
    var created = kind === "child" ? createSubagentRow() : createSessionCard();
    created.setAttribute("data-rkey", key);
    created.__kind = kind;
    sessionRowEls[key] = created;
    if (kind === "card") { updateSessionCard(created, item); }
    else { updateSubagentRow(created, item); }
    return created;
  }

  function ensureSessionGroup(node) {
    var key = "g:" + node.node_id;
    var group = sessionRowEls[key];
    if (group && group.__kind === "group") { return group; }
    group = createSessionGroup(node);
    group.setAttribute("data-rkey", key);
    group.__kind = "group";
    sessionRowEls[key] = group;
    return group;
  }

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
    // Nodes with several visible sessions collapse into a group (session list
    // grouped by device); single-session nodes stay flat so the two-column
    // board keeps its density.
    // 子代理行跟随父会话：先按可见父行重排（孤儿子行直接丢弃），节点分组只按
    // 顶层会话计数，避免"1 个父 + N 个子"把节点误判成多会话分组。
    items = orderSessionsWithChildren(items);
    var nodeCounts = {};
    items.forEach(function (item) {
      if (isSubagentSession(item.session)) { return; }
      var k = item.node.node_id;
      nodeCounts[k] = (nodeCounts[k] || 0) + 1;
    });
    var renderedNodes = {};
    var desired = [];
    items.forEach(function (item) {
      var nodeID = item.node.node_id;
      if (renderedNodes[nodeID]) { return; }
      renderedNodes[nodeID] = true;
      var nodeItems = items.filter(function (it) { return it.node.node_id === nodeID; });
      if (nodeCounts[nodeID] > 1) {
        // Grouped nodes: reconcile the rows inside the group body first, then
        // the group itself at board level — a row that flips between flat and
        // grouped moves parent without losing DOM identity.
        var group = ensureSessionGroup(item.node);
        reconcileChildren(group.querySelector(".group-body"), nodeItems.map(ensureSessionRow));
        updateSessionGroup(group, item.node, nodeItems);
        desired.push(group);
        return;
      }
      nodeItems.forEach(function (it) { desired.push(ensureSessionRow(it)); });
    });
    reconcileChildren(host, desired);
    pruneSessionRowEls();
    restoreSessionDrafts(host);
    if (!sessionsScrollRestored) {
      sessionsScrollRestored = true;
      setTimeout(restoreSessionsScroll, 0);
    }
  }

  // createSessionGroup wraps several sessions of one node in a collapsible
  // header (device grouping): live/offline dot + name + kind/count/last seen.
  function createSessionGroup(node) {
    var group = document.createElement("section");
    group.className = "session-group";
    group.innerHTML =
      '<button type="button" class="group-head" aria-expanded="true">' +
      '<span class="dot live"></span>' +
      '<span class="group-name"></span><span class="group-meta"></span>' +
      '<span class="group-caret" aria-hidden="true">▾</span></button>' +
      '<div class="group-body"></div>';
    group.querySelector(".group-head").addEventListener("click", function () {
      var next = !collapsedGroups[node.node_id];
      if (next) { collapsedGroups[node.node_id] = true; } else { delete collapsedGroups[node.node_id]; }
      group.querySelector(".group-body").hidden = next;
      group.querySelector(".group-caret").textContent = next ? "▸" : "▾";
      this.setAttribute("aria-expanded", String(!next));
    });
    return group;
  }

  function updateSessionGroup(group, node, groupItems) {
    group.classList.toggle("offline", !!node.stale);
    group.querySelector(".group-head .dot").className = "dot " + (node.stale ? "offline" : "live");
    setText(group.querySelector(".group-name"), node.name || node.node_id);
    var latest = 0;
    groupItems.forEach(function (item) {
      var t = new Date(item.session.updated_at || 0).getTime();
      if (t > latest) { latest = t; }
    });
    setText(group.querySelector(".group-meta"),
      (node.kind || "agent") + " · 会话 " + groupItems.length +
      (latest ? " · 最近 " + new Date(latest).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : ""));
    var collapsed = !!collapsedGroups[node.node_id];
    group.querySelector(".group-body").hidden = collapsed;
    group.querySelector(".group-caret").textContent = collapsed ? "▸" : "▾";
    group.querySelector(".group-head").setAttribute("aria-expanded", String(!collapsed));
  }

  // createSessionCard builds the full card skeleton once; every conditional
  // block stays in the DOM (toggled via `hidden`) so updateSessionCard only
  // patches text/visibility and never re-creates nodes — scroll positions,
  // text selection, focus and the live tail therefore survive data updates.
  function createSessionCard() {
    var card = document.createElement("div");
    card.className = "session-card";
    // Put the card itself in the tab order (arrow-key nav already focuses it,
    // but Tab could not reach it); updateSessionCard sets its spoken name.
    card.setAttribute("tabindex", "0");
    // Information is layered by business attribute, top → bottom: identity
    // (title/badge/time) → live activity band (state chip + host activity
    // text + elapsed clock) → content (preview / last message) → meta line
    // (node · ID · sync) → actions → reply → live tail (pinned footer).
    card.innerHTML =
      '<div class="s-title"><span class="dot live"></span><span class="s-name"></span></div>' +
      '<span class="att-badge s-badge" hidden></span><span class="s-time"></span>' +
      '<div class="s-activity" hidden><span class="s-state" hidden></span>' +
      '<span class="s-status-text"></span><span class="s-status-elapsed" hidden></span></div>' +
      '<div class="s-preview" hidden></div>' +
      '<div class="s-last" hidden><span class="s-last-role"></span><span class="s-last-text"></span></div>' +
      '<div class="s-meta"><span class="s-node-text"></span>' +
      '<span class="s-id" role="button" tabindex="0" title="点击复制会话 ID">' +
      '<span class="s-id-label">ID</span><code class="s-id-code"></code></span>' +
      '<span class="s-sync" hidden></span></div>' +
      '<div class="s-actions">' +
      '<button class="btn ghost sm" type="button" data-expand hidden></button>' +
      '<button class="btn ghost sm" type="button" data-detail>详情</button>' +
      '<button class="btn ghost sm" type="button" data-chat hidden>会话</button>' +
      '<button class="btn ghost sm" type="button" data-open>打开</button>' +
      '<button class="btn danger sm" type="button" data-remove hidden>移除</button>' +
      '</div>' +
      '<div class="s-live" hidden title="实时输出（最后一行），点击展开完整尾流"></div>' +
      '<div class="s-tail" hidden>' +
      '<div class="s-tail-head"><span class="s-tail-title">实时输出</span><span class="s-tail-meta"></span>' +
      '<button class="btn ghost sm" type="button" data-tail-follow>跟随中</button></div>' +
      '<pre class="s-tail-body"></pre></div>';
    // One-time listeners; they read card.__item for the newest snapshot so a
    // data patch never needs to re-bind handlers.
    var idRow = card.querySelector(".s-id");
    var copyID = function () {
      if (card.__item) { copyText(card.__item.session.session_id, null); }
    };
    idRow.addEventListener("click", copyID);
    idRow.addEventListener("keydown", function (e) {
      if (e.key === "Enter" || e.key === " ") { e.preventDefault(); copyID(); }
    });
    // The card body is informational only: opening a session always goes
    // through the explicit 打开 button, so text selection or a mis-tap on
    // the row never navigates away.
    card.querySelector("[data-detail]").addEventListener("click", function () {
      var it = card.__item;
      if (it) { openSessionDetail(it.node, it.session); }
    });
    // 会话实时面板：直接在本页渲染历史 + SSE 实时消息（节点 micro web client
    // 的数据源），不跳走也不开新标签。仅在节点上报 screen+events 能力时可见。
    card.querySelector("[data-chat]").addEventListener("click", function () {
      var it = card.__item;
      if (it) { openSessionChat(it.node, it.session); }
    });
    // Offline snapshots render the 打开 button disabled (no tunnel); only a
    // live node with a Web route passes the handler's re-check.
    var openBtn = card.querySelector("[data-open]");
    openBtn.addEventListener("click", function () {
      var it = card.__item;
      if (!it || openBtn.disabled) { return; }
      openSession(it.node, it.session);
    });
    // 展开/收起: the full last message and the reply form live in the
    // expanded area; the choice is remembered per session across updates.
    var expandBtn = card.querySelector("[data-expand]");
    expandBtn.addEventListener("click", function () {
      var it = card.__item;
      if (!it || expandBtn.hidden) { return; }
      var key = card.getAttribute("data-draft-key");
      var next = !expandedSessions[key];
      if (next) { expandedSessions[key] = true; } else { delete expandedSessions[key]; }
      card.classList.toggle("expanded", next);
      expandBtn.textContent = next ? "收起" : "展开";
      // Expansion is the "I'm watching this session" signal: it gates the
      // live tail on the agent (P1-b) and suppresses pushes while open.
      rememberPresence(it.node, it.session, { tail: next });
    });
    // Collapsed cards still surface the newest live line, so progress is
    // visible without expanding; clicking it expands the full tail.
    var liveLine = card.querySelector(".s-live");
    liveLine.addEventListener("click", function () {
      var it = card.__item;
      if (!it) { return; }
      var key = card.getAttribute("data-draft-key");
      if (!expandedSessions[key]) {
        expandedSessions[key] = true;
        card.classList.add("expanded");
        var expandButton = card.querySelector("[data-expand]");
        if (expandButton) { expandButton.textContent = "收起"; }
        rememberPresence(it.node, it.session, { tail: true });
      }
      var body = card.querySelector(".s-tail-body");
      if (body && body.scrollIntoView) { body.scrollIntoView({ block: "nearest" }); }
    });
    // Live-output tail: the 跟随 button pins the viewport to the newest line
    // and pauses automatically when the operator scrolls up（原位补丁只更新
    // 文本、不回零 scrollTop——这正是滚动阅读不被刷新打断的原因）。
    var tailBody = card.querySelector(".s-tail-body");
    var followBtn = card.querySelector("[data-tail-follow]");
    card.__syncFollowBtn = function () {
      var key = card.getAttribute("data-draft-key");
      followBtn.textContent = tailFollow[key] === false ? "跳到底部" : "跟随中";
    };
    followBtn.addEventListener("click", function () {
      tailFollow[card.getAttribute("data-draft-key")] = true;
      card.__syncFollowBtn();
      tailBody.scrollTop = tailBody.scrollHeight;
    });
    tailBody.addEventListener("scroll", function () {
      var atBottom = tailBody.scrollHeight - tailBody.scrollTop - tailBody.clientHeight < 24;
      tailFollow[card.getAttribute("data-draft-key")] = atBottom;
      card.__syncFollowBtn();
    });
    var rmBtn = card.querySelector("[data-remove]");
    rmBtn.addEventListener("click", function (e) {
      e.stopPropagation();
      var it = card.__item;
      if (!it) { return; }
      if (!window.confirm("从注册表移除该会话条目？\n（仅清除网关侧快照，不影响 agent 上的会话数据。）")) { return; }
      api("/api/nodes/" + encodeURIComponent(it.node.node_id) +
          "/sessions/" + encodeURIComponent(it.session.session_id), { method: "DELETE" })
        .then(function () { notify("会话条目已移除", "ok"); loadNodes(); })
        .catch(function (err) { notify("移除失败：" + err, "error"); });
    });
    return card;
  }
  // updateSessionCard patches one card to match the latest snapshot: text is
  // written only when it changed, visibility flips via `hidden`, and the live
  // tail keeps its scrollTop unless 跟随中 pins it to the bottom — so an
  // operator reading a scrolled-up detail is never thrown back by an update.
  function updateSessionCard(card, item) {
    var s = item.session, node = item.node;
    var cardKey = node.node_id + "|" + s.session_id;
    card.__item = item;
    card.setAttribute("data-draft-key", cardKey);
    var expanded = !!expandedSessions[cardKey];
    card.classList.toggle("offline", !!node.stale);
    card.classList.toggle("expanded", expanded);
    var attClass = s.attention ? "att-" + s.attention : "";
    if (card.__attClass !== attClass) {
      if (card.__attClass) { card.classList.remove(card.__attClass); }
      if (attClass) { card.classList.add(attClass); }
      card.__attClass = attClass;
    }
    // Pulse when this session visibly moved since the previous render.
    var fingerprint = sessionFingerprint(s);
    if (sessionLastSeen[cardKey] !== undefined && sessionLastSeen[cardKey] !== fingerprint) {
      card.classList.remove("card-updated");
      void card.offsetWidth; // restart the pulse animation
      card.classList.add("card-updated");
    }
    sessionLastSeen[cardKey] = fingerprint;
    card.querySelector(".s-title .dot").className = "dot " + (node.stale ? "offline" : "live");
    setText(card.querySelector(".s-name"), sessionLabelOf(s));
    card.setAttribute("aria-label",
      sessionLabelOf(s) + "，" + (node.name || node.node_id) + "，" + stateLabelOf(s) +
      (s.attention ? "，" + attentionLabel(s.attention) : ""));
    setText(card.querySelector(".s-node-text"),
      (node.name || node.node_id) + " · " + (node.kind || "agent"));
    var syncEl = card.querySelector(".s-sync");
    var syncedMs = s.synced_at ? new Date(s.synced_at).getTime() : NaN;
    if (!isNaN(syncedMs)) {
      setRelTime(syncEl, syncedMs, "同步 ", 60000,
        "网关最后收到该会话更新：" + absoluteTime(syncedMs) + "（服务器时间）");
      syncEl.hidden = false;
    } else {
      syncEl.hidden = true;
    }
    setText(card.querySelector(".s-id-code"), s.session_id);
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
    } else {
      stateChip.hidden = true;
    }
    var statusText = (s.status_text || "").trim();
    var startedMs = Date.parse(s.status_started_at || "");
    var showElapsed = isActiveSessionState(s.state) && !isNaN(startedMs);
    var activityLine = card.querySelector(".s-activity");
    var statusTextEl = card.querySelector(".s-status-text");
    setText(statusTextEl, statusText);
    if (statusText) { statusTextEl.title = statusText; }
    var elapsedEl = card.querySelector(".s-status-elapsed");
    if (chipShown || statusText || showElapsed) {
      if (showElapsed) {
        elapsedEl.setAttribute("data-started", s.status_started_at);
        setText(elapsedEl, "· " + formatStatusElapsed(Date.now() - startedMs));
        elapsedEl.hidden = false;
      } else {
        elapsedEl.hidden = true;
        elapsedEl.removeAttribute("data-started");
      }
      activityLine.hidden = false;
    } else {
      activityLine.hidden = true;
      elapsedEl.hidden = true;
      elapsedEl.removeAttribute("data-started");
    }
    if (s.attention) {
      var b = card.querySelector(".s-badge");
      b.textContent = attentionLabel(s.attention);
      b.className = "att-badge s-badge att-" + s.attention;
      b.hidden = false;
    } else {
      card.querySelector(".s-badge").hidden = true;
    }
    var t = card.querySelector(".s-time");
    var updatedMs = s.updated_at ? new Date(s.updated_at).getTime() : NaN;
    if (!isNaN(updatedMs)) {
      setRelTime(t, updatedMs, "", 0, "内容时间：" + absoluteTime(updatedMs));
      t.hidden = false;
    } else { t.hidden = true; }
    var previewEl = card.querySelector(".s-preview");
    if (s.preview) { setText(previewEl, s.preview); }
    // Full last message (multi-line, unlike the one-line preview). Falls back
    // to the preview when the agent build predates the field.
    var lastBlock = card.querySelector(".s-last");
    var lastText = s.last_message || "";
    if (lastText) {
      var role = s.last_message_role || "";
      var roleEl = card.querySelector(".s-last-role");
      roleEl.className = "s-last-role" + (role ? " role-" + role : "");
      setText(roleEl, roleLabelOf(role));
      setMessageHTML(card.querySelector(".s-last-text"), lastText);
      lastBlock.hidden = false;
    } else {
      lastBlock.hidden = true;
    }
    // Live-output tail (bounded by the shared core): rendered only while
    // the global "实时输出" switch is on; the 跟随 button pins the viewport
    // to the newest line and pauses automatically when the user scrolls up.
    var tailText = tailEnabled ? (s.status_tail || "") : "";
    var tailBlock = card.querySelector(".s-tail");
    var liveLine = card.querySelector(".s-live");
    if (tailText) {
      var lastLine = (tailText.replace(/\s+$/, "").split("\n").pop() || "").replace(/\s+/g, " ").trim();
      if (lastLine.length > 140) { lastLine = "…" + lastLine.slice(lastLine.length - 140); }
      setText(liveLine, lastLine || "…");
      liveLine.hidden = false;
      tailBlock.hidden = false;
      var tailBody = card.querySelector(".s-tail-body");
      setMessageHTML(tailBody, tailText);
      setText(card.querySelector(".s-tail-meta"), tailText.length + " 字符");
      card.__syncFollowBtn();
      // Follow mode pins the viewport to the newest line. When the operator
      // scrolled up (tailFollow false) scrollTop is left untouched — that is
      // what keeps a scrolled-up detail readable while the session streams.
      if (tailFollow[cardKey] !== false) { tailBody.scrollTop = tailBody.scrollHeight; }
    } else {
      liveLine.hidden = true;
      tailBlock.hidden = true;
    }
    // preview 与实时行互斥：有流式输出时让尾部文本优先（与旧卡片一致）。
    previewEl.hidden = !s.preview || !!tailText;
    // 展开 button: only meaningful when there is more content than the
    // collapsed card shows; the full last message and the reply form live in
    // the expanded area.
    var hasReply = nodeHasWebUI(node) && (node.capabilities || []).indexOf("input") !== -1;
    var hasExtras = !!(s.last_message || "") || hasReply || !!(s.status_tail || "");
    var expandBtn = card.querySelector("[data-expand]");
    expandBtn.hidden = !hasExtras;
    expandBtn.textContent = expanded ? "收起" : "展开";
    // Offline snapshots render the 打开 button disabled (no tunnel); live nodes
    // without a Web route keep it disabled with the reason in the tooltip.
    var openBtn = card.querySelector("[data-open]");
    var canOpen = nodeHasWebUI(node);
    openBtn.disabled = !canOpen;
    if (canOpen) {
      openBtn.removeAttribute("title");
    } else {
      openBtn.title = node.stale ? "节点离线，无法打开" : "节点未提供本地 Web 服务（aicli 需以 --web 启动），无法打开";
    }
    card.querySelector("[data-remove]").hidden = !node.stale;
    // 会话面板入口：只有能给出 transcript + 实时流的节点才有意义（详见
    // chatPanelSupported），其余节点保持隐藏——用户不会点进一个必然失败的面板。
    card.querySelector("[data-chat]").hidden = !chatPanelSupported(node, s);
    // Inline reply: POST through the gateway reverse proxy to the agent's
    // own /web/api/input (the agent injects its local write token and the
    // route allowlist already covers /web). Gated on the agent advertising
    // the "input" capability so stale agents just hide the form.
    var wantReply = !node.stale && (node.capabilities || []).indexOf("input") !== -1;
    var replyForm = card.querySelector(".s-reply");
    if (wantReply && !replyForm) {
      card.insertBefore(buildReplyForm(node, s), card.querySelector(".s-live"));
    } else if (!wantReply && replyForm) {
      replyForm.remove();
    }
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

  function openModalEl() {
    if (modalIsOpen("chat-modal")) { return el("chat-modal"); }
    if (modalIsOpen("session-modal")) { return el("session-modal"); }
    if (modalIsOpen("token-modal")) { return el("token-modal"); }
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

  /* ──────────────── session chat panel (history + live) ───────── */
  //
  // 数据源是节点自身的 micro web client，经网关反代访问：
  //   · 历史  GET  /n/{node}/web/api/screen?format=json&msg_limit=N[&msg_before=..]
  //   · 实时  SSE  /n/{node}/web/api/events
  //   · 发送  POST /n/{node}/web/api/input      {"prompt":"…"}
  // 本面板只做「消息渲染 + 输入」：provider / model / reasoning effort 切换
  // 一律不搬过来（这也是它与节点原版 web 客户端的范围差异）。
  //
  // 三条必须记住的服务端语义：
  //   1) EventSource 只把**显式注册过**的命名事件派发给 listener，漏注册 = 整帧
  //      被浏览器静默丢弃（节点侧用 web_event_sequence_asset_test.go 守这条）。
  //      本面板只注册自己消费的子集，见 CHAT_SSE_EVENTS。
  //   2) 网关 forwarder 有 5 分钟请求超时（agent/forward.go），SSE 会被周期性断开
  //      并由浏览器自动重连；重连后必须重拉一次 screen 快照补齐断线期间丢掉的帧，
  //      否则面板会永久缺失那一段（服务端队列满时同样是静默丢帧）。
  //   3) /web/api/screen 返回的是**节点当前活跃会话**的 transcript。aicli 一个进程
  //      只有一个活跃会话，因此只对父级会话开放入口：子代理行共享父 transcript，
  //      给了按钮只会让人以为在看子会话。

  var CHAT_PAGE_LIMIT = 40;    // 首屏与实时刷新窗口（条）
  var CHAT_OLDER_LIMIT = 60;   // 「加载更早消息」每页条数
  var CHAT_REFRESH_MS = 1000;  // 快照刷新尾随节流：事件风暴下每秒最多一次
  var CHAT_MAX_LIVE = 60000;   // 实时气泡本地累积上限（字符），超限只等快照收敛
  var CHAT_MAX_WINDOW = 400;   // 本地保留的消息行上限：刷新是「保前缀」合并（见
                               // chatApplyWindow），长跑会话会一直往后长，到顶就从
                               // 头部丢弃并重新开放「加载更早消息」。
  var CHAT_BUSY_POLL_MS = 2000; // 「会话忙」期间的兜底快照轮询间隔（见 chatSetBusy）
  var CHAT_SSE_EVENTS = [
    "connected", "heartbeat", "screen_refresh", "session_switched", "error",
    "session_start", "session_end", "session_interrupted",
    "turn_start", "turn_delta", "turn_end",
    "assistant_delta", "assistant_message", "assistant_image_progress", "reasoning_delta",
    "tool_start", "tool_end",
    "approval_requested", "approval_resolved", "question_asked", "question_answered",
    // 状态栏：dynamic_status 驱动动态状态行，model_changed 触发静态段重取。
    "dynamic_status", "model_changed",
  ];

  // chatState 是单例面板状态；null = 面板关闭。字段只由本节读写。
  var chatState = null;

  // chatPanelSupported：只有能给出 transcript + 实时流的节点才给「会话」入口。
  // runtime-server 是 SPA（自带 /api/runtime/*），未带 --web 的 aicli 不注册
  // screen/events 路由；离线快照没有隧道，一律不给。
  function chatPanelSupported(node, session) {
    if (!node || node.stale) { return false; }
    if (!nodeHasWebUI(node)) { return false; }
    if (isSubagentSession(session)) { return false; }
    var caps = node.capabilities || [];
    return caps.indexOf("screen") !== -1 && caps.indexOf("events") !== -1;
  }

  function chatCanInput(node) {
    return !!(node && !node.stale && (node.capabilities || []).indexOf("input") !== -1);
  }

  function chatEmptyLive() {
    return { streaming: false, done: false, assistant: "", reasoning: "", tool: "", images: [] };
  }

  function chatSetStatus(text, kind) {
    var dot = el("chat-live-status");
    var label = el("chat-live-text");
    if (!dot || !label) { return; }
    var st = chatState;
    // 帧计数直接写进状态点：SSE 是否真的在投递，是「面板不更新」类问题的第一个
    // 分叉点（连上了但零帧 ≠ 连接失败），运维/排障时一眼可读。
    var suffix = st && st.frames ? " · " + st.frames + " 帧" : "";
    // 只替换文本节点：状态点本身带 ::before 圆点，textContent 会把它一起清掉。
    label.textContent = (text || "") + suffix;
    dot.className = "chat-live-dot" + (kind ? " is-" + kind : "");
    dot.title = st && st.lastEvent
      ? "最近事件：" + st.lastEvent + "（累计 " + st.frames + " 帧）"
      : "尚未收到任何实时事件";
  }

  function chatNotice(text) {
    var box = el("chat-notice");
    if (!box) { return; }
    box.textContent = text || "";
    box.hidden = !text;
  }

  // chatPlaceholder 在列表为空时给一句空状态文案（复用同一个节点，避免堆叠）。
  function chatPlaceholder(text) {
    var host = el("chat-list");
    var node = el("chat-placeholder");
    if (!host) { return; }
    if (!node) {
      node = document.createElement("p");
      node.className = "chat-placeholder";
      node.id = "chat-placeholder";
      host.appendChild(node);
    }
    setText(node, text);
  }

  function chatShowHint(text) {
    var hint = el("chat-hint");
    if (!hint) { return; }
    hint.textContent = text || "";
    hint.hidden = !text;
  }

  // chatSetSending 同步发送按钮的三态：空闲（纸飞机）/ 发送中（转圈）/ 失败
  // （保持可点重试）。输入为空时按钮禁用，避免「点了没反应」的假反馈。
  function chatSetSending(sending) {
    var st = chatState;
    var btn = el("chat-send");
    var input = el("chat-input");
    if (!btn) { return; }
    if (sending) {
      btn.classList.add("is-sending");
      btn.disabled = true;
      btn.setAttribute("aria-busy", "true");
      return;
    }
    btn.classList.remove("is-sending");
    btn.removeAttribute("aria-busy");
    var hasText = !!(input && String(input.value || "").trim());
    btn.disabled = !hasText || !!(st && st.node && st.node.stale);
  }

  // chatSyncSendState 在每次输入变化时刷新按钮可用性。
  function chatSyncSendState() {
    var st = chatState;
    if (!st || st.sending) { return; }
    chatSetSending(false);
  }

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
    var st = chatState;
    var box = el("chat-dyn-status");
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
    var st = chatState;
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
        if (chatState) { chatRenderDynamic(); }
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
    var st = chatState;
    var box = el("chat-segs");
    if (!st || !box) { return; }
    api("/n/" + encodeURIComponent(st.node.node_id) + "/web/api/statusbar", { cache: "no-store" })
      .then(function (res) {
        if (!res.ok) { throw new Error("HTTP " + res.status); }
        return res.json();
      })
      .then(function (snap) {
        if (chatState !== st) { return; }
        chatRenderSegments(box, snap);
      })
      .catch(function () {
        if (chatState !== st) { return; }
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
    var st = chatState;
    if (!st) { return; }
    st.busy = !!busy;
    if (st.busy && !st.busyTimer) {
      st.busyTimer = setInterval(function () {
        if (chatState && chatState.busy) { chatFetchScreen(); }
      }, CHAT_BUSY_POLL_MS);
    } else if (!st.busy && st.busyTimer) {
      clearInterval(st.busyTimer);
      st.busyTimer = null;
    }
  }

  function chatAutoGrow() {
    var input = el("chat-input");
    // jsdom 没有布局引擎（scrollHeight === 0），此时跳过同步。
    if (!input || !input.scrollHeight) { return; }
    input.style.height = "auto";
    input.style.height = Math.min(input.scrollHeight, 160) + "px";
  }

  function openSessionChat(node, session) {
    if (!node || !session) { return; }
    if (node.stale) {
      notify("节点「" + (node.name || node.node_id) + "」当前离线，无法读取消息。", "error");
      return;
    }
    if (!chatPanelSupported(node, session)) {
      notify(webUnavailableText(node), "error");
      return;
    }
    var canInput = chatCanInput(node);
    setText(el("chat-modal-title"), sessionLabelOf(session));
    setText(el("chat-modal-sub"),
      (node.name || node.node_id) + " · " + (node.kind || "agent") +
      " · " + stateLabelOf(session));
    var host = el("chat-list");
    if (host) { host.innerHTML = ""; }
    chatNotice("");
    chatSetStatus("连接中…", "");
    chatShowHint("");
    var form = el("chat-form");
    if (form) { form.hidden = !canInput; }
    var ro = el("chat-readonly");
    if (ro) { ro.hidden = canInput; }
    var older = el("chat-older");
    if (older) { older.hidden = true; }
    var jump = el("chat-jump-latest");
    if (jump) { jump.hidden = true; }
    var input = el("chat-input");
    if (input) { input.value = ""; input.style.height = ""; }
    chatSetSending(false);
    chatPlaceholder("正在加载消息…");
    chatState = {
      node: node,
      session: session,
      win: null,
      msgStart: undefined,
      messages: [],
      rows: {},
      pending: [],
      live: chatEmptyLive(),
      seq: 0,
      follow: true,
      olderBusy: false,
      sending: false,
      available: true,
      failed: false,
      scrollRestore: null,
      source: null,
      pollTimer: null,
      refreshTimer: null,
      busy: false,
      busyTimer: null,
      frames: 0,
      lastEvent: "",
      dynText: "",
      dynRole: "",
      dynStartedAt: 0,
      dynTimer: null,
    };
    markRead(node, session);
    // 面板打开 = 「正在看这个会话」：抑制该会话的推送；关面板时释放。
    rememberPresence(node, session);
    chatRenderDynamic();
    chatRenderSegments(el("chat-segs"), null);
    openModal("chat-modal");
    chatFetchScreen(CHAT_PAGE_LIMIT);
    chatLoadStatusBar();
    chatConnect();
  }

  // ── 数据面：快照 / 渲染 ──────────────────────────────────────

  // chatFetchScreen 拉取权威窗口。older=true 时按 win.start 向前翻页，结果前插。
  // 每次请求带代次（seq），过期响应直接丢弃——快速重连 + 节流刷新会并发多份。
  function chatFetchScreen(limit, opts) {
    var st = chatState;
    if (!st) { return; }
    var o = opts || {};
    if (o.older) {
      if (st.olderBusy || !st.win || !st.win.has_more) { return; }
      st.olderBusy = true;
      chatUpdateOlder();
    }
    st.seq++;
    var seq = st.seq;
    var url = "/n/" + encodeURIComponent(st.node.node_id) +
      "/web/api/screen?format=json&msg_limit=" + (o.older ? CHAT_OLDER_LIMIT : (limit || CHAT_PAGE_LIMIT));
    if (o.older) {
      // 游标必须是**本地最老一条**的绝对索引（msgStart），不能取上一份服务端窗口的
      // start：刷新会把 win 推回「最新 N 条」窗口，此时 win.start > msgStart，用
      // win.start 翻页会取回一段已经在本地的区间，chatApplyWindow 的「紧邻才前插」
      // 判据（end === msgStart）随即失配、走整块重建分支——表现为点「加载更早消息」
      // 反而把最新消息弄丢（流式期间刷新频繁，几乎必现）。
      var cursor = typeof st.msgStart === "number" ? st.msgStart : st.win.start;
      url += "&msg_before=" + cursor;
    }
    api(url, { cache: "no-store" }).then(function (res) {
      if (!res.ok) { throw new Error("HTTP " + res.status); }
      return res.json();
    }).then(function (data) {
      // 翻页锁先放开：请求被刷新取代（seq 失配）时同样要解锁，否则 olderBusy 永久
      // 卡在 true，本次打开里「加载更早消息」与滚到顶自动翻页一起失效。
      st.olderBusy = false;
      // 面板已关闭 / 已换会话 / 已有更新请求 → 这一份作废。
      if (chatState !== st || st.seq !== seq) { return; }
      chatApplyWindow(data, o.older);
    }).catch(function (err) {
      st.olderBusy = false;
      chatUpdateOlder();
      if (chatState !== st || st.seq !== seq) { return; }
      chatFail("消息加载失败：" + (err && err.message ? err.message : err));
    });
  }

  // chatFail 只在本次打开里提示一次失败：持续失败的刷新不该反复弹 toast。
  function chatFail(message) {
    var st = chatState;
    if (!st) { return; }
    chatSetStatus("读取失败", "down");
    if (st.failed) { return; }
    st.failed = true;
    if (!st.messages.length) { chatPlaceholder(message); }
    notify(message + "（可点「打开原版 Web」查看完整界面）", "error");
  }

  // chatApplyWindow 把一次 screen 响应落进面板状态。两条写入路径：
  //   · 刷新（older=false）：与已加载区间在**后缀**合并，前缀行的 DOM 身份、
  //     折叠与滚动位置都不动（这就是流式刷新不打断阅读的原因）；
  //   · 翻页（older=true）：窗口紧邻且连续时前插；不连续说明历史被改写，整体重建。
  function chatApplyWindow(data, older) {
    var st = chatState;
    if (!st) { return; }
    if (!data || data.available === false) {
      var empty = el("chat-list");
      if (empty) { empty.innerHTML = ""; }
      st.rows = {};
      st.available = false;
      chatPlaceholder("暂无可显示的消息。");
      chatNotice("该节点当前没有可读的会话消息（" + (data && data.reason ? data.reason : "transcript 未就绪，或会话刚被切换") +
        "）。可点「打开原版 Web」查看完整界面。");
      chatSetStatus("无消息", "down");
      chatUpdateOlder();
      return;
    }
    st.available = true;
    st.failed = false;
    chatNotice("");
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
    }
    chatConfirmPending();
    // 权威行已经接管这一回合：实时气泡退场，避免同一段内容渲染两次。
    if (st.live.done) { st.live = chatEmptyLive(); }
    chatTrimWindow();
    chatRender();
    chatUpdateOlder();
  }

  // chatTrimWindow 把本地窗口收在 CHAT_MAX_WINDOW 内。行 key 用的是绝对索引
  // （"srv:" + msgStart + i），所以整块从头部丢弃后，留存行的 key 不变——DOM 身份、
  // 选区与折叠都保住。丢弃后必须重新开放翻页：更早的内容还在服务端。
  function chatTrimWindow() {
    var st = chatState;
    var host = el("chat-list");
    if (!st || !host) { return; }
    var extra = st.messages.length - CHAT_MAX_WINDOW;
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
      st.rows[key] = row;
    }
    row.__key = key;
    var cls = "chat-msg chat-msg-" + (role || "assistant");
    if (o.pending) { cls += " is-pending"; }
    if (o.streaming) { cls += " is-streaming"; }
    if (row.className !== cls) { row.className = cls; }
    var bubble = row.querySelector(".chat-bubble");

    var roleEl = row.querySelector(".chat-msg-role");
    var roleText = roleLabelOf(role) + (o.pending ? " · 发送中" : "");
    if (o.collapse && role === "tool") {
      // 工具行的折叠控件长在抬头行里：先写角色文本再追加控件（setText 只在文本
      // 变化时写入，不会反复清掉控件）。
      setText(roleEl, roleText);
      if (!row.querySelector(".chat-tool-toggle")) {
        var tgl = document.createElement("button");
        tgl.type = "button";
        tgl.className = "chat-tool-toggle";
        tgl.setAttribute("aria-expanded", "false");
        tgl.setAttribute("aria-label", "展开工具输出");
        tgl.innerHTML = '<span class="chat-tool-action">展开</span>' +
          '<span class="chat-tool-icon" aria-hidden="true">▼</span>';
        roleEl.appendChild(document.createTextNode(" "));
        roleEl.appendChild(tgl);
      }
    } else {
      setText(roleEl, roleText);
    }

    if (o.collapse && role === "reasoning") {
      setText(row.querySelector(".chat-reasoning summary"),
        roleLabelOf(role) + "过程");
      setText(row.querySelector(".chat-reasoning-body"), content || "");
    } else if (o.collapse && role === "tool") {
      setText(row.querySelector(".chat-tool-output"), content || "");
    } else {
      setMessageHTML(bubble, content || "");
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

  // chatRender 组装列表：权威行 → 实时气泡（推理/回答/工具）→ 本地待确认回显。
  function chatRender() {
    var st = chatState;
    var host = el("chat-list");
    if (!st || !host) { return; }
    var desired = [];
    var base = typeof st.msgStart === "number" ? st.msgStart : 0;
    st.messages.forEach(function (m, i) {
      // 服务端历史行才带折叠结构（推理面板 / 工具折叠）。
      desired.push(chatRowElement(st, "srv:" + (base + i), m.role, m.content, { collapse: true }));
    });
    if (st.live.reasoning) {
      desired.push(chatRowElement(st, "live:reasoning", "reasoning", st.live.reasoning,
        { streaming: st.live.streaming }));
    }
    // 只有真的收到助手正文（或图片）时才插实时助手行。turn_start 就会把
    // streaming 置真，但一回合可能长时间停在推理/工具阶段；此时渲染一个空的
    // 「助手」气泡会一直占在列表底部（表现为一行只有标签、没有内容），而推理/
    // 工具行与状态栏已经表达了进度。streaming 只用于已有正文时的光标动画。
    var liveImages = st.live.images || [];
    if (st.live.assistant || liveImages.length) {
      desired.push(chatRowElement(st, "live:assistant", "assistant", st.live.assistant,
        { streaming: st.live.streaming, images: liveImages }));
    }
    if (st.live.tool) {
      desired.push(chatRowElement(st, "live:tool", "tool", st.live.tool, {}));
    }
    st.pending.forEach(function (text, i) {
      desired.push(chatRowElement(st, "pending:" + i, "user", text, { pending: true }));
    });
    reconcileChildren(host, desired);
    var keep = {};
    desired.forEach(function (row) { keep[row.__key] = true; });
    Object.keys(st.rows).forEach(function (key) {
      if (!keep[key]) { delete st.rows[key]; }
    });
    if (!desired.length) { chatPlaceholder("暂无消息。"); }
    // 折叠后处理：测量工具行溢出 → 启用/隐藏控件；按用户账本恢复手动展开；
    // 忙时活跃回合的最后一块自动展开、其余折回（与 micro web client 同一状态机）。
    chatRefreshToolToggles(host);
    Object.keys(st.rows).forEach(function (key) {
      if (!chatUserExpanded[key]) { return; }
      var row = st.rows[key];
      var det = row.querySelector(".chat-reasoning");
      if (det && !det.open) { det.open = true; }
      var out = row.querySelector(".chat-tool-output");
      if (out && out.classList.contains("chat-collapsed")) { chatSetToolExpanded(row, true); }
    });
    chatApplyLiveTail(host, st.busy);
    // 翻页前插：按新增高度补偿滚动位置，阅读不被顶走。
    if (st.scrollRestore != null) {
      host.scrollTop = host.scrollHeight - st.scrollRestore;
      st.scrollRestore = null;
    }
    if (st.follow) { host.scrollTop = host.scrollHeight; }
    chatSyncJump();
  }

  // chatScheduleRender 把高频增量合并到一帧：逐字流不该每帧重排整份列表。
  var chatRenderQueued = false;
  function chatScheduleRender() {
    if (chatRenderQueued) { return; }
    chatRenderQueued = true;
    var run = function () { chatRenderQueued = false; chatRender(); };
    if (typeof window.requestAnimationFrame === "function") { window.requestAnimationFrame(run); }
    else { setTimeout(run, 16); }
  }

  /* ── 折叠交互（与 micro web client chat.js 同一状态机） ────────────── */

  // 用户手动展开的行（按服务端绝对索引记账）：窗口刷新会重建 DOM，但行 key 不变，
  // 所以在 chatApplyWindow/chRender 后按 key 恢复即可——手动展开的块永不自动折回。
  var chatUserExpanded = {};

  // chatRefreshToolToggles 测量工具行是否溢出：溢出 → 启用抬头折叠控件；
  // 未溢出 → 直接展示全文并隐藏控件（与 micro web client 的
  // refreshToolOutputToggles 同一判定）。面板隐藏时 clientHeight 为 0，
  // 不打 ready 标记，下次渲染重测。
  function chatRefreshToolToggles(host) {
    var outputs = host.querySelectorAll(".chat-tool-output");
    for (var i = 0; i < outputs.length; i++) {
      var out = outputs[i];
      var row = out.closest(".chat-msg");
      if (!row || row.__toggleReady) { continue; }
      var tgl = row.querySelector(".chat-tool-toggle");
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
    }
  }

  function chatSetToolExpanded(row, expanded) {
    var tgl = row.querySelector(".chat-tool-toggle");
    var out = row.querySelector(".chat-tool-output");
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
      if (key && chatUserExpanded[key]) { continue; }
      var det = row.querySelector(".chat-reasoning");
      if (det && det.open) { det.open = false; }
      var out = row.querySelector(".chat-tool-output");
      if (out && out.classList.contains("chat-expanded")) { chatSetToolExpanded(row, false); }
    }
    if (!followTurn) { return; }
    // 最后一块总是展开（busy 时）。
    [lastReasoning, lastTool].forEach(function (row) {
      if (!row) { return; }
      var det = row.querySelector(".chat-reasoning");
      if (det && !det.open) { det.open = true; return; }
      var out = row.querySelector(".chat-tool-output");
      if (out && !out.classList.contains("chat-expanded")) { chatSetToolExpanded(row, true); }
    });
  }

  function chatScrollToBottom(force) {
    var st = chatState;
    var host = el("chat-list");
    if (!st || !host) { return; }
    if (force) { st.follow = true; }
    host.scrollTop = host.scrollHeight;
    chatSyncJump();
  }

  // chatSyncJump：只有「列表可滚 + 用户已上滚」时才提供回到最新。
  function chatSyncJump() {
    var st = chatState;
    var host = el("chat-list");
    var btn = el("chat-jump-latest");
    if (!st || !host || !btn) { return; }
    var overflow = host.scrollHeight - host.clientHeight > 40;
    btn.hidden = !(overflow && !st.follow);
  }

  function chatUpdateOlder() {
    var st = chatState;
    var box = el("chat-older");
    if (!st || !box) { return; }
    box.hidden = !(st.win && st.win.has_more);
    var btn = el("chat-older-btn");
    if (btn) { btn.disabled = !!st.olderBusy; }
  }

  // chatLoadOlder 向前翻一页（顶部按钮与滚到顶部附近都会触发）。
  function chatLoadOlder() {
    var st = chatState;
    var host = el("chat-list");
    if (!st || !host || st.olderBusy) { return; }
    if (!st.win || !st.win.has_more) { return; }
    st.scrollRestore = host.scrollHeight - host.scrollTop;
    chatFetchScreen(CHAT_OLDER_LIMIT, { older: true });
  }

  // chatConfirmPending：服务端窗口出现同文本 user 行时释放乐观回显。只做「按文本
  // 匹配移除」这一档，不引入节点原版客户端的完整双源对账。
  function chatNormalize(text) {
    return String(text || "").replace(/\s+/g, " ").trim();
  }

  function chatConfirmPending() {
    var st = chatState;
    if (!st || !st.pending.length) { return; }
    var seen = {};
    st.messages.forEach(function (m) {
      if (m.role === "user") { seen[chatNormalize(m.content)] = true; }
    });
    st.pending = st.pending.filter(function (text) { return !seen[chatNormalize(text)]; });
  }

  function closeSessionChat() {
    var st = chatState;
    if (!st) { return; }
    // 先断流再清状态：否则关闭过程中的帧会写进正在被清空的 DOM。
    chatDisconnect();
    chatState = null;
    delete presenceTargets[st.node.node_id + "|" + st.session.session_id];
    sendPresence();
    var host = el("chat-list");
    if (host) { host.innerHTML = ""; }
    closeModal("chat-modal");
  }

  // ── 实时面：SSE 增量 ────────────────────────────────────────

  // chatConnect 打开节点的 /web/api/events。注册的事件名见 CHAT_SSE_EVENTS：
  // EventSource 只派发显式注册过的命名事件，漏一个就等于整帧丢失。
  function chatConnect() {
    var st = chatState;
    if (!st) { return; }
    chatDisconnect();
    if (typeof EventSource !== "function") {
      // 没有 EventSource：退化为轮询，至少能看到近实时历史（否则面板完全静默）。
      chatSetStatus("轮询中（浏览器不支持实时流）", "down");
      st.pollTimer = setInterval(function () { chatFetchScreen(); }, 3000);
      return;
    }
    var source = new EventSource("/n/" + encodeURIComponent(st.node.node_id) + "/web/api/events");
    st.source = source;
    source.addEventListener("open", function () {
      if (chatState !== st) { return; }
      chatSetStatus("已连接", "live");
      // 重连后必须补一次快照：断线（含网关 5 分钟超时）期间的增量已永久丢失。
      chatFetchScreen();
    });
    source.addEventListener("error", function () {
      if (chatState !== st) { return; }
      // 浏览器会自动重连；这里只如实反映状态，不主动关闭（关掉反而放弃重连）。
      chatSetStatus("连接断开，重连中…", "down");
    });
    CHAT_SSE_EVENTS.forEach(function (name) {
      source.addEventListener(name, function (event) {
        if (chatState !== st) { return; }
        var data = {};
        try { data = JSON.parse(event.data); } catch (err) { data = {}; }
        st.frames++;
        st.lastEvent = name;
        chatOnEvent(name, data);
      });
    });
    // 无名帧（服务端未写 `event:` 字段）也要解析，否则整帧静默丢弃。
    source.onmessage = function (event) {
      if (chatState !== st) { return; }
      var data = {};
      try { data = JSON.parse(event.data); } catch (err) { data = {}; }
      st.frames++;
      st.lastEvent = "message";
      chatOnEvent("message", data);
    };
    // 重连后按既有忙闲重新武装兜底轮询（chatDisconnect 把它停了）。
    chatSetBusy(st.busy);
  }

  function chatDisconnect() {
    var st = chatState;
    if (!st) { return; }
    if (st.source) {
      try { st.source.close(); } catch (err) { /* 已关闭 */ }
      st.source = null;
    }
    if (st.pollTimer) { clearInterval(st.pollTimer); st.pollTimer = null; }
    if (st.refreshTimer) { clearTimeout(st.refreshTimer); st.refreshTimer = null; }
    // 兜底轮询与状态栏时钟随连接一起停：后台/关闭时不空转（chatConnect 会按
    // st.busy 重新武装轮询）。
    if (st.busyTimer) { clearInterval(st.busyTimer); st.busyTimer = null; }
    if (st.dynTimer) { clearInterval(st.dynTimer); st.dynTimer = null; }
  }

  // chatScheduleRefresh 是尾随节流：事件风暴下最多每秒一次 screen 快照。
  function chatScheduleRefresh() {
    var st = chatState;
    if (!st || st.refreshTimer) { return; }
    st.refreshTimer = setTimeout(function () {
      if (chatState !== st) { return; }
      st.refreshTimer = null;
      chatFetchScreen();
    }, CHAT_REFRESH_MS);
  }

  function chatBeginStream() {
    var st = chatState;
    if (!st) { return; }
    st.live = chatEmptyLive();
    st.live.streaming = true;
    chatScheduleRender();
  }

  // chatAppendLive 累积增量文本。超过上限就停手：那种量级只可能来自异常输出，
  // 交给下一次权威快照收敛，别把本地内存堆穿。
  function chatAppendLive(kind, text) {
    var st = chatState;
    if (!st || !text || typeof text !== "string") { return; }
    if (kind === "reasoning") {
      if (st.live.reasoning.length > CHAT_MAX_LIVE) { return; }
      st.live.reasoning += text;
    } else {
      if (st.live.assistant.length > CHAT_MAX_LIVE) { return; }
      st.live.assistant += text;
    }
    st.live.streaming = true;
    chatScheduleRender();
  }

  // chatEndStream 把实时气泡标记为「内容已完整但尚未被快照接管」。气泡要等下一份
  // 权威窗口落地才退场（chatApplyWindow 里按 live.done 清除），否则终稿与增量之间
  // 会出现一次空档闪烁。
  function chatEndStream() {
    var st = chatState;
    if (!st) { return; }
    if (!st.live.assistant && !st.live.reasoning && !(st.live.images && st.live.images.length)) {
      st.live = chatEmptyLive();
      return;
    }
    st.live.streaming = false;
    st.live.done = true;
  }

  // chatOnEvent 是唯一的 SSE 分派点。语义与节点原版客户端一致：
  // 增量只喂实时气泡，权威内容一律以 screen 快照为准。
  function chatOnEvent(name, data) {
    var st = chatState;
    if (!st) { return; }
    var d = data || {};
    switch (name) {
      case "connected":
        chatSetStatus("已连接", "live");
        // connected 载荷带权威 session_busy：面板是中途打开的，这个值决定了要不要
        // 立刻开始兜底轮询（否则会等最多 30s 的第一次 heartbeat 才开始）。
        chatSetBusy(!!d.session_busy);
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
        chatSetBusy(true);
        chatSetStatus("处理中…", "busy");
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
          chatScheduleRender();
        }
        chatFetchScreen();
        break;
      case "assistant_image_progress":
        var img = d.image && typeof d.image === "object" ? d.image : d;
        var src = (img && typeof img === "object") ? (img.url || img.b64_data || img.data || "") : "";
        if (src) {
          st.live.images.push(src);
          st.live.streaming = true;
          chatScheduleRender();
        }
        break;
      case "tool_start":
        st.live.tool = "[工具: " + (d.tool_name || "?") + " 执行中…]";
        if (st.live.streaming) { chatScheduleRender(); }
        break;
      case "tool_end":
        st.live.tool = "[工具: " + (d.tool_name || "?") + " 完成]";
        if (st.live.streaming) { chatScheduleRender(); }
        chatScheduleRefresh();
        break;
      case "turn_end":
        // 请求边界权威快照：增量在服务端队列满时是静默丢帧，只有整段快照能补齐
        // （与节点原版客户端、TUI 渲染器同语义）。
        if (d.reasoning_snapshot) { st.live.reasoning = d.reasoning_snapshot; }
        if (d.assistant_snapshot) { st.live.assistant = d.assistant_snapshot; }
        st.live.tool = "";
        chatEndStream();
        chatSetBusy(false);
        chatSetStatus("就绪", "live");
        chatScheduleRender();
        chatFetchScreen();
        break;
      case "heartbeat":
        // 权威忙闲（每次心跳都带）：这是「兜底轮询该开还是该关」的唯一可靠依据，
        // 不依赖 turn_start/turn_end 是否丢帧。
        if (typeof d.session_busy === "boolean") { chatSetBusy(d.session_busy); }
        // 尾部丢帧自愈：turn_end 被丢掉后可能不再有任何帧，气泡会永久停在「输出中」。
        if (d.session_busy === false && st.live.streaming) {
          chatEndStream();
          chatSetStatus("就绪", "live");
          chatScheduleRender();
          chatFetchScreen();
        }
        break;
      case "session_interrupted":
        st.live = chatEmptyLive();
        chatSetBusy(false);
        chatSetStatus("已停止", "down");
        chatScheduleRender();
        chatFetchScreen();
        break;
      case "dynamic_status":
        // 状态栏主体：Retrying / Analyzing / Running…（active=false 即收尾）。
        chatSetDynamic(d);
        break;
      case "model_changed":
        // provider/model/reasoning 切换落地 → 静态段（provider/model/context）变了。
        chatLoadStatusBar();
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
        chatNotice("该会话正在等待审批 —— 打开原版 Web 处理后继续。");
        break;
      case "question_asked":
        chatNotice("该会话提出了问题 —— 打开原版 Web 回答后继续。");
        break;
      case "approval_resolved":
      case "question_answered":
        chatNotice("");
        chatScheduleRefresh();
        break;
      case "error":
        chatSetStatus("连接异常", "down");
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
    var st = chatState;
    if (!st || !newID || newID === st.session.session_id) { return; }
    delete presenceTargets[st.node.node_id + "|" + st.session.session_id];
    var next = null;
    (st.node.sessions || []).forEach(function (s) {
      if (s && s.session_id === newID) { next = s; }
    });
    st.session = next || { session_id: newID, title: newID };
    setText(el("chat-modal-title"), sessionLabelOf(st.session));
    setText(el("chat-modal-sub"),
      (st.node.name || st.node.node_id) + " · " + (st.node.kind || "agent") +
      " · " + stateLabelOf(st.session));
    markRead(st.node, st.session);
    rememberPresence(st.node, st.session);
  }

  // chatResetForSession：/new、/resume、/load 会让 transcript 换人，旧行必须整块
  // 丢弃——否则上一条会话的内容会串进新会话的对话区。
  function chatResetForSession() {
    var st = chatState;
    if (!st) { return; }
    var host = el("chat-list");
    if (host) { host.innerHTML = ""; }
    st.messages = [];
    st.msgStart = undefined;
    st.win = null;
    st.rows = {};
    st.pending = [];
    st.live = chatEmptyLive();
    st.failed = false;
    chatPlaceholder("正在加载消息…");
    chatUserExpanded = {};
    // 换会话 = 上一段的动态状态与忙闲都不再适用，状态栏必须跟着复位重取，
    // 否则会把旧会话的 "Analyzing…（32m）" 挂在新会话上。
    chatSetDynamic({ active: false });
    chatSetBusy(false);
    chatLoadStatusBar();
    chatUpdateOlder();
    chatFetchScreen();
    // 会话列表里的摘要也变了（标题/状态/最近消息），顺手对齐。
    loadNodes();
  }

  // ── 输入面：发送 ────────────────────────────────────────────

  // chatSend 复用卡片回复表单已固化的契约：POST /n/{node}/web/api/input
  // {"prompt":…}，只有 {status:"queued"} 算成功；失败保留草稿。
  function chatSend() {
    var st = chatState;
    if (!st) { return; }
    var input = el("chat-input");
    var text = (input && input.value ? input.value : "").trim();
    if (!text || st.sending) { return; }
    if (st.node.stale) {
      notify("节点「" + (st.node.name || st.node.node_id) + "」离线，无法发送。", "error");
      return;
    }
    if (!chatCanInput(st.node)) {
      chatShowHint("该节点未开启输入通道。");
      return;
    }
    st.sending = true;
    chatSetSending(true);
    chatShowHint("发送中…");
    api("/n/" + encodeURIComponent(st.node.node_id) + "/web/api/input", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ prompt: text }),
    }).then(function (res) {
      return res.json().catch(function () { return null; }).then(function (body) {
        if (!res.ok) { throw new Error((body && body.reason) || ("HTTP " + res.status)); }
        if (!body || body.status !== "queued") {
          throw new Error((body && body.reason) || (body && body.status) || "发送被拒绝");
        }
        if (chatState !== st) { return; }
        // 乐观回显：服务端 screen 里出现同文本 user 行时由 chatConfirmPending 释放。
        st.pending.push(text);
        if (input) { input.value = ""; chatAutoGrow(); }
        chatShowHint("");
        chatSetSending(false);
        chatRender();
        chatScrollToBottom(true);
      });
    }).catch(function (err) {
      if (chatState !== st) { return; }
      notify("发送失败：" + (err && err.message ? err.message : err), "error");
      chatShowHint("发送失败，可重试（草稿已保留）。");
      chatSetSending(false);
    }).finally(function () {
      if (chatState !== st) { return; }
      st.sending = false;
      chatSetSending(false);
    });
  }

  // 面板打开时切后台：断开 SSE 省流量，也避免后台被网关的 5 分钟超时反复抖动；
  // 回到前台重连并补一次快照。presence 也在这里重发（隐藏期间 sendPresence 是
  // no-op，回前台必须补，否则推送抑制会随 TTL 提前失效）。
  document.addEventListener("visibilitychange", function () {
    if (!chatState) { return; }
    if (document.hidden) {
      chatDisconnect();
      chatSetStatus("后台暂停", "down");
      return;
    }
    chatConnect();
    chatFetchScreen();
    sendPresence();
  });

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

  // 子代理行显示/隐藏（持久化）：宿主未上报子会话行时按钮无副作用。元素缺失时
  // 静默跳过（PWA 可能缓存了旧版 index.html）。
  function syncSubagentToggle() {
    var btn = el("subagent-toggle");
    if (btn) { btn.textContent = "子代理：" + (showSubagents ? "显示" : "隐藏"); }
  }
  var subagentToggleBtn = el("subagent-toggle");
  if (subagentToggleBtn) {
    subagentToggleBtn.addEventListener("click", function () {
      showSubagents = !showSubagents;
      saveSessionPrefs();
      syncSubagentToggle();
      renderSessionTable(lastNodes);
    });
  }
  syncSubagentToggle();

  // 消息 Markdown 渲染开关（持久化，默认开，见 renderMarkdown）：关闭回落纯文本。
  function syncMdToggle() {
    var btn = el("md-toggle");
    if (btn) { btn.textContent = "Markdown：" + (mdEnabled ? "开" : "关"); }
  }
  var mdToggleBtn = el("md-toggle");
  if (mdToggleBtn) {
    mdToggleBtn.addEventListener("click", function () {
      mdEnabled = !mdEnabled;
      saveSessionPrefs();
      syncMdToggle();
      renderSessionTable(lastNodes);
      if (detailTarget) { openSessionDetail(detailTarget.node, detailTarget.session); }
      // 会话面板里已渲染的正文同样按新模式重排（否则要等下一帧增量）。
      if (chatState) { chatRender(); }
    });
  }
  syncMdToggle();

  // Keyboard model: "/" focuses the search, ↑/↓ (or j/k) walks the session
  // cards, Enter opens the focused card's detail dialog, Space toggles 展开.
  document.addEventListener("keydown", function (e) {
    // 任一模态打开时不接管快捷键：会话面板里有输入框与滚动区，全局的 "/" 聚焦、
    // ↑/↓ 走卡片在这里都不该生效（另一条 typing 守卫只挡输入类元素）。
    if (modalIsOpen("chat-modal") || modalIsOpen("session-modal") || modalIsOpen("token-modal")) { return; }
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

  // Session chat modal. 面板元素缺失时（PWA 缓存了旧 index.html）整块跳过，
  // 这样新版 app.js 不会因为少节点而抛错（入口按钮同样不会出现）。
  if (el("chat-modal")) {
    el("chat-modal-close").addEventListener("click", closeSessionChat);
    el("chat-modal").addEventListener("click", function (e) {
      if (e.target === el("chat-modal")) { closeSessionChat(); }
    });
    el("chat-modal-open").addEventListener("click", function () {
      if (!chatState) { return; }
      openSession(chatState.node, chatState.session);
    });
    el("chat-jump-latest").addEventListener("click", function () { chatScrollToBottom(true); });
    el("chat-older-btn").addEventListener("click", chatLoadOlder);
    el("chat-form").addEventListener("submit", function (e) {
      e.preventDefault();
      chatSend();
    });
    el("chat-input").addEventListener("input", function () {
      chatAutoGrow();
      chatSyncSendState();
    });
    // Enter 发送 / Shift+Enter 换行（与卡片回复表单一致）。
    el("chat-input").addEventListener("keydown", function (e) {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        chatSend();
      }
    });
    // 上滚暂停自动跟随；滚到顶部附近自动翻页（阅读历史时不必先找按钮）。
    el("chat-list").addEventListener("scroll", function () {
      var st = chatState;
      var host = el("chat-list");
      if (!st || !host) { return; }
      st.follow = host.scrollHeight - host.scrollTop - host.clientHeight < 24;
      chatSyncJump();
      if (host.scrollTop < 64) { chatLoadOlder(); }
    });
    // 工具行折叠控件：点击 + Enter/Space（与 micro web client 同一委托方式）。
    // 用户手动展开 → 记账（永不自动折回）；手动收起 → 连自动标记一起清。
    el("chat-list").addEventListener("click", function (e) {
      var tgl = e.target && e.target.closest ? e.target.closest(".chat-tool-toggle") : null;
      if (!tgl) { return; }
      var row = tgl.closest(".chat-msg");
      if (!row || tgl.classList.contains("is-off")) { return; }
      var out = row.querySelector(".chat-tool-output");
      if (!out) { return; }
      var expanded = out.classList.contains("chat-collapsed");
      chatSetToolExpanded(row, expanded);
      if (row.__key) {
        if (expanded) { chatUserExpanded[row.__key] = true; }
        else { delete chatUserExpanded[row.__key]; }
      }
    });
    el("chat-list").addEventListener("keydown", function (e) {
      if (e.key !== "Enter" && e.key !== " " && e.key !== "Spacebar") { return; }
      var tgl = e.target && e.target.closest ? e.target.closest(".chat-tool-toggle") : null;
      if (!tgl) { return; }
      e.preventDefault();
      tgl.click();
    });
    // 推理面板：原生 <details> 切换后记录用户意图（延迟一拍读切换后的状态，
    // 程序化 open/close 不经过 click，不会被误记）。
    el("chat-list").addEventListener("toggle", function (e) {
      var det = e.target;
      if (!det.classList || !det.classList.contains("chat-reasoning")) { return; }
      var row = det.closest(".chat-msg");
      if (!row || !row.__key) { return; }
      if (det.open) { chatUserExpanded[row.__key] = true; }
      else { delete chatUserExpanded[row.__key]; }
    }, true);
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && modalIsOpen("chat-modal")) { closeSessionChat(); }
    });
  }

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
      navigator.serviceWorker.register("./sw.js?v=44").catch(function () {});
    });
  }

  loadSessionPrefs();
  syncFilterControls();
  syncTailToggle();
  syncMdToggle();
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
