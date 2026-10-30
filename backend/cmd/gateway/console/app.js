(function () {
  "use strict";

  var el = function (id) { return document.getElementById(id); };

  /* ─────────────────────────── state ─────────────────────────── */

  var lastNodes = [];
  var lastDevices = [];
  var lastMounts = [];
  var mountTokenTarget = null;
  var logTab = "mounts";
  var logFollow = true;
  var logTimer = null;
  var logLoading = false;
  var logMountEntries = [];
  var logAuditEntries = [];
  var sessionMenuSeq = 0;
  var seenAttention = {};
  var attentionSeeded = false;
  var readAttention = {};
  // 已读注意力（点击打开过的待处理）跨刷新保留，短 TTL 自动过期：一小时内
  // 刷新页面不再把处理过的待办重新弹回 inbox；TTL 保证误标的旧条目会自然
  // 回流（服务端 attention 还在时会重新出现在 seenAttention 检测里）。
  var READ_ATTENTION_KEY = "aicli-gw-read-attention";
  var READ_ATTENTION_TTL_MS = 60 * 60 * 1000;
  var presenceTargets = {};
  var sessionFilterText = "";
  // 默认只显示在线会话：离线快照收进「离线」筛选，避免主列表被旧数据占满。
  var sessionFilterKind = "online";
  var sessionSort = "activity";
  var authedState = false;
  // 通知策略（GET/PUT /api/notify/policy）：notifyPolicy 是生效值（原生通知
  // 桥据此过滤），notifyPolicyDraft 是「通知设置」页未保存的编辑副本。
  var notifyPolicy = null;
  var notifyPolicyMeta = null;
  var notifyPolicyDraft = null;
  var notifyPolicyLoaded = false;
  var notifyPolicyLoading = null;
  var notifyPolicyBaseline = "";
  var notifyPolicySaving = false;
  var notifyQuietError = "";
  var notifyQuietTouched = false;
  var notifyQuietInvalidEl = null;
  var notifySessionQuery = "";
  var notifySessionOnlyCustom = false;
  var deviceQuery = "";
  var deviceStatusFilter = "";

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

  // "Pending" means the operator can act now. Completed/waiting remain valid
  // attention states for cards and notifications, but they are not work items.
  function isActionableAttention(kind) {
    return kind === "approval" || kind === "question" || kind === "error";
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

  /* ───────────────────────── UX-06: 通用二次确认 ──────────────── */
  // 用站内模态框替代 window.confirm：文案可包含对象、数量与后果，
  // 取消/确认都会把焦点还给触发按钮，且不会累积 document 监听器。
  function confirmDestructive(options) {
    var modal = el("confirm-modal");
    if (!modal) { return Promise.resolve({ confirmed: false }); }

    var titleEl = el("confirm-title");
    var msgEl = el("confirm-message");
    var cancelBtn = el("confirm-cancel");
    var confirmBtn = el("confirm-confirm");
    var closeBtn = modal.querySelector(".modal-close");
    var previousFocus = options.trigger || document.activeElement;
    var closed = false;
    var resolvePromise;
    var promise = new Promise(function (resolve) { resolvePromise = resolve; });

    titleEl.textContent = options.title || "确认操作";
    msgEl.textContent = options.message || "";
    cancelBtn.textContent = options.cancelText || "取消";
    confirmBtn.textContent = options.confirmText || "是的，执行";
    cancelBtn.hidden = options.showCancel === false;

    function focusables() {
      return [closeBtn, cancelBtn, confirmBtn].filter(function (node) {
        return node && !node.hidden && !node.disabled;
      });
    }

    function onKeydown(e) {
      if (closed) { return; }
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        close(false);
        return;
      }
      if (e.key !== "Tab") { return; }
      var nodes = focusables();
      if (!nodes.length) { return; }
      var idx = nodes.indexOf(document.activeElement);
      if (idx === -1) { idx = 0; }
      var next = e.shiftKey ? (idx - 1 + nodes.length) % nodes.length : (idx + 1) % nodes.length;
      e.preventDefault();
      nodes[next].focus();
    }

    function onOverlayClick(e) {
      if (e.target === modal) { close(false); }
    }

    function close(confirmed, result) {
      if (closed) { return; }
      closed = true;
      modal.hidden = true;
      document.removeEventListener("keydown", onKeydown, true);
      modal.removeEventListener("click", onOverlayClick);
      cancelBtn.onclick = null;
      confirmBtn.onclick = null;
      if (closeBtn) { closeBtn.onclick = null; }
      // 操作可能触发列表重绘；等一帧再还焦点：触发器还在就回去，
      // 已被移除则落到当前视图标题，避免焦点掉到 body。
      setTimeout(function () {
        if (previousFocus && previousFocus.isConnected && typeof previousFocus.focus === "function") {
          previousFocus.focus();
          return;
        }
        var view = document.querySelector(".view:not([hidden])");
        var fallback = view && view.querySelector("h2, .page-title");
        if (!fallback) { fallback = el("main"); }
        if (fallback) {
          if (!fallback.hasAttribute("tabindex")) { fallback.setAttribute("tabindex", "-1"); }
          fallback.focus();
        }
      }, 0);
      resolvePromise({ confirmed: confirmed, result: result });
    }

    cancelBtn.onclick = function () { close(false); };
    if (closeBtn) { closeBtn.onclick = function () { close(false); }; }
    confirmBtn.onclick = function () {
      confirmBtn.disabled = true;
      Promise.resolve(options.onConfirm ? options.onConfirm() : null)
        .then(function (result) { close(true, result); })
        .catch(function (err) {
          confirmBtn.disabled = false;
          pushToast("操作失败：" + (err && err.message ? err.message : err), "error");
        });
    };

    document.addEventListener("keydown", onKeydown, true);
    modal.addEventListener("click", onOverlayClick);
    modal.hidden = false;
    confirmBtn.disabled = false;
    // 破坏性操作默认聚焦「取消」，避免 Enter 直接执行。
    var initial = options.focusConfirm ? confirmBtn : (cancelBtn.hidden ? confirmBtn : cancelBtn);
    initial.focus();

    return promise;
  }
  /* ───────────────────────── UX-06 结束 ──────────────────────── */

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
      notifyPolicy = null;
      notifyPolicyMeta = null;
      notifyPolicyDraft = null;
      notifyPolicyLoaded = false;
      notifyPolicyBaseline = "";
      notifyPolicySaving = false;
      notifyQuietError = "";
      notifyQuietTouched = false;
      notifyQuietInvalidEl = null;
    }
    refreshPushState();
    renderInstallHint();
  }

  /* ─────────────────────── hash router ───────────────────────── */

  var VIEW_TITLES = {
    dashboard: "仪表盘",
    sessions: "会话",
    nodes: "节点",
    mcp: "MCP 服务",
    logs: "日志",
    tokens: "设备令牌",
    connectors: "连接器",
    notifications: "通知设置",
  };
  var notifyLastView = "";

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
    // MCP 挂载同为惰性视图：只有打开时才读 /api/mounts，避免登录后多一次请求。
    if (view === "mcp") { loadMounts(); }
    // 日志视图同样是惰性的：进入时拉一次，离开时停掉轮询。
    if (view === "logs") { initLogsView(); } else { stopLogTimer(); }
    // 通知设置同样是惰性视图：只在打开时拉取策略，避免每次登录多一个请求。
    if (view === "notifications") {
      loadNotifyPolicy();
      if (notifyPolicyLoaded) { renderNotifyPolicy(); }
    }
    // 微信式会话壳只在「会话」视图生效：body.im-mode 打开整页固定高度，
    // 让左右两栏各自滚动（而不是整页滚动）。
    document.body.classList.toggle("im-mode", view === "sessions");
    if (view !== "sessions" && imChatIsOpen()) {
      // 离开会话视图就收起会话流并断开 SSE；这里不能 history.back()，
      // 否则会把用户刚点的导航又弹回去。
      imSuppressHistoryBack = true;
      closeSessionChat();
      imSuppressHistoryBack = false;
    }
    if (view === "sessions") { setTimeout(autoSelectFirstSession, 0); }
    closeSidebar();
    notifyLastView = view;
  }

  window.addEventListener("hashchange", function () {
    if (notifyLastView === "notifications" && currentView() !== "notifications" &&
        notifyPolicyIsDirty()) {
      var leave = true;
      try {
        leave = window.confirm("通知设置有未保存的修改，离开后会丢失。确定离开吗？");
      } catch (e) { /* headless/jsdom: fail open, the draft remains recoverable */ }
      if (!leave) {
        try { history.replaceState(null, "", "#notifications"); } catch (e2) { window.location.hash = "notifications"; }
        return;
      }
      notifyDiscardPolicyDraft();
    }
    applyHashParams();
    applyRoute();
  });
  window.addEventListener("beforeunload", function (event) {
    if (!notifyPolicyIsDirty()) { return; }
    event.preventDefault();
    event.returnValue = "";
  });

  function navigate(view, params) {
    window.location.hash = view + (params ? "?" + params : "");
  }

  // UX-07: 顶栏刷新是唯一的手动刷新入口，只刷新当前视图。
  function refreshCurrentView() {
    var view = currentView();
    if (view === "tokens") { return loadDevices(); }
    if (view === "connectors") { return loadConnectors(true); }
    if (view === "mcp") { return loadMounts(); }
    if (view === "logs") { refreshLogs(); return Promise.resolve(); }
    if (view === "notifications") {
      if (notifyPolicyIsDirty()) {
        notify("通知设置还有未保存的修改，先保存或放弃再刷新", "error");
        return Promise.resolve();
      }
      return loadNotifyPolicy(true);
    }
    return loadNodes();
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
      // 登录/刷新完成后补做通知定位请求（事件可能早于快照到达）。
      flushPendingLocate();
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
        // 挂载登记随节点注册/断连变化；只在该视图打开时才多拉一次。
        if (currentView() === "mcp") { loadMounts(); }
        if (currentView() === "logs") { loadMounts().then(populateLogSlugOptions); }
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

  function locateSession(nodeID, sessionID) {
    if (!nodeID || !sessionID) { return; }
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

  /* ─────────────────────── rendering: stats ──────────────────── */

  var lastAnnouncedAttention = null;
  var attentionAnnounceTimer = null;

  // UX-08: 计数变化只通过一个 role=status 汇总播报，并做延迟合并；
  // 避免 SSE 连发时反复打断读屏用户。
  function announceAttentionChange(count) {
    if (lastAnnouncedAttention === null) {
      lastAnnouncedAttention = count;
      return;
    }
    if (count === lastAnnouncedAttention) { return; }
    lastAnnouncedAttention = count;
    if (attentionAnnounceTimer) { clearTimeout(attentionAnnounceTimer); }
    attentionAnnounceTimer = setTimeout(function () {
      attentionAnnounceTimer = null;
      var status = el("a11y-status");
      if (!status) { return; }
      status.textContent = count ? "待处理 " + count + " 项" : "当前没有待处理";
    }, 1500);
  }

  function updateStats(nodes) {
    var live = 0, offline = 0, active = 0;
    (nodes || []).forEach(function (node) {
      if (node.stale) { offline++; } else { live++; active += (node.sessions || []).length; }
    });
    var attention = inboxItems(nodes).length;
    announceAttentionChange(attention);
    el("stat-online").textContent = String(live);
    el("stat-offline").textContent = String(offline);
    el("stat-sessions").textContent = String(active);
    el("stat-attention").textContent = String(attention);
    var attentionCard = el("stat-attention-card");
    if (attentionCard) {
      attentionCard.classList.toggle("has-attention", attention > 0);
      attentionCard.classList.toggle("is-empty", attention === 0);
      attentionCard.setAttribute("aria-label", attention ? attention + " 项待处理" : "暂无待处理");
      if (attention) {
        attentionCard.setAttribute("href", "#sessions?attention=1");
      } else {
        attentionCard.removeAttribute("href");
      }
    }
    var badge = el("nav-badge-sessions");
    if (badge) {
      badge.textContent = attention ? String(attention) : "";
      badge.hidden = !attention;
      badge.title = attention ? attention + " 项待处理" : "";
    }
    var navSessions = document.querySelector('.nav-item[data-view="sessions"]');
    if (navSessions) {
      navSessions.setAttribute("aria-label", attention ? "会话，" + attention + " 项待处理" : "会话");
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
    if (sessionFilterKind === "attention" &&
        (item.node.stale || !isActionableAttention(s.attention) ||
         readAttention[attentionKey(item.node, s)])) { return false; }
    if (sessionFilterKind === "running" && (item.node.stale || !(s.busy || isActiveSessionState(s.state)))) { return false; }
    if (sessionFilterKind === "offline" && !item.node.stale) { return false; }
    if (sessionFilterText) {
      var q = sessionFilterText.toLowerCase();
      // 节点显示名一起参与匹配："找 laptop 上那个会话"是高频搜索意图；
      // 节点 id（裸 uuid）不参与，避免常见子串误命中。
      var hay = (nodeDisplayName(item.node) + " " + (s.title || "") + " " + (s.preview || "") + " " +
        (s.last_message || "") + " " + (s.status_text || "") + " " + s.session_id).toLowerCase();
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
        notify("节点「" + nodeDisplayName(node) + "」离线，无法回复。", "error");
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
      var na = nodeDisplayName(a.node), nb = nodeDisplayName(b.node);
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
  var sessionsAutoSelected = false;
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

  // chatSetStreamingText appends only the new suffix to the live text.
  // Long text is split at line boundaries into content-visibility chunks so a
  // 30KB answer does not force the browser to re-layout all previous lines on
  // every token. The concatenated textContent remains byte-for-byte the source.
  function chatSetStreamingText(node, text) {
    if (!node) { return; }
    var src = text || "";
    var chunks = node.__streamChunks;
    if (!chunks || !chunks.length || chunks[0].el.parentNode !== node) {
      chunks = chatResetStreamingText(node, src);
    } else {
      var current = "";
      chunks.forEach(function (chunk) { current += chunk.node.data; });
      if (src.indexOf(current) !== 0) {
        chunks = chatResetStreamingText(node, src);
      } else if (src.length > current.length) {
        chunks[chunks.length - 1].node.appendData(src.slice(current.length));
      }
    }
    chatSplitStreamingChunks(node, chunks);
  }

  function chatResetStreamingText(node, text) {
    node.textContent = "";
    var chunk = chatNewStreamChunk(text);
    node.__streamChunks = [chunk];
    node.appendChild(chunk.el);
    return node.__streamChunks;
  }

  function chatNewStreamChunk(text) {
    var el = document.createElement("div");
    el.className = "chat-stream-chunk";
    var textNode = document.createTextNode(text || "");
    el.appendChild(textNode);
    return { el: el, node: textNode };
  }

  function chatSplitStreamingChunks(node, chunks) {
    if (!chunks || !chunks.length) { return; }
    var last = chunks[chunks.length - 1];
    while (last.node.data.length > CHAT_LIVE_CHUNK_CHARS) {
      var data = last.node.data;
      var cut = data.lastIndexOf("\n", CHAT_LIVE_CHUNK_CHARS - 1);
      if (cut >= 0) { cut += 1; }
      else { cut = CHAT_LIVE_CHUNK_CHARS; }
      var rest = data.slice(cut);
      last.node.deleteData(cut, rest.length);
      var next = chatNewStreamChunk(rest);
      node.insertBefore(next.el, last.el.nextSibling);
      chunks.push(next);
      last = next;
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
    var listCount = el("im-list-count");
    if (listCount) {
      setText(listCount, String(items.length));
      listCount.hidden = items.length === 0;
    }
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
    syncImActiveCard();
    if (currentView() === "sessions") { setTimeout(autoSelectFirstSession, 0); }
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
    setText(group.querySelector(".group-name"), nodeDisplayName(node));
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

  // Session action menus live in the DOM. One pair of document listeners closes
  // any open menu (outside click / Escape) so cards never accumulate handlers.
  function closeSessionMenus(restoreFocus) {
    document.querySelectorAll(".s-menu").forEach(function (menu) {
      if (menu.hidden) { return; }
      var hadFocus = !!(document.activeElement && menu.contains(document.activeElement));
      menu.hidden = true;
      var btn = menu.parentElement && menu.parentElement.querySelector("[data-more]");
      if (btn) {
        btn.setAttribute("aria-expanded", "false");
        if (restoreFocus && hadFocus) { btn.focus(); }
      }
    });
  }

  // Menus are rendered fixed to the viewport, not inside the card's scroll
  // container: the list clips overflow, and a popup anchored above the action
  // row would otherwise be invisible. Flip above/below when the viewport runs
  // out of room and keep the right edge aligned with the trigger.
  function positionSessionMenu(trigger, menu) {
    var r = trigger.getBoundingClientRect();
    var m = menu.getBoundingClientRect();
    var pad = 8;
    var gap = 6;
    var left = Math.min(window.innerWidth - m.width - pad, Math.max(pad, r.right - m.width));
    var below = r.bottom + gap;
    var above = r.top - m.height - gap;
    var top = below + m.height <= window.innerHeight - pad ? below : Math.max(pad, above);
    menu.style.left = Math.round(left) + "px";
    menu.style.top = Math.round(top) + "px";
  }

  function visibleMenuItems(menu) {
    return Array.prototype.slice.call(menu.querySelectorAll('[role="menuitem"]')).filter(function (item) {
      return !item.hidden;
    });
  }

  // 任意卡片（设备 / MCP / 离线快照）共用同一套溢出菜单行为：
  // 固定定位、方向键/Home/End、Escape 关闭、焦点归还。
  function bindCardMenu(card, menu) {
    if (!card || !menu) { return; }
    var trigger = card.querySelector("[data-more]");
    if (!trigger) { return; }
    var menuID = menu.id || ("card-menu-" + (++sessionMenuSeq));
    menu.id = menuID;
    trigger.setAttribute("aria-controls", menuID);
    trigger.setAttribute("aria-expanded", "false");
    trigger.addEventListener("click", function (e) {
      e.stopPropagation();
      var willOpen = menu.hidden;
      closeSessionMenus(false);
      if (!willOpen) { return; }
      menu.hidden = false;
      trigger.setAttribute("aria-expanded", "true");
      positionSessionMenu(trigger, menu);
      var items = visibleMenuItems(menu);
      if (items.length) { items[0].focus(); }
    });
    menu.addEventListener("keydown", function (e) {
      var items = visibleMenuItems(menu);
      if (!items.length) { return; }
      var idx = items.indexOf(document.activeElement);
      if (e.key === "ArrowDown") {
        e.preventDefault();
        items[(idx + 1) % items.length].focus();
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        items[(idx - 1 + items.length) % items.length].focus();
      } else if (e.key === "Home") {
        e.preventDefault();
        items[0].focus();
      } else if (e.key === "End") {
        e.preventDefault();
        items[items.length - 1].focus();
      } else if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        closeSessionMenus(true);
      } else if (e.key === "Tab") {
        closeSessionMenus(true);
      }
    });
  }

  document.addEventListener("click", function (e) {
    if (e.target && e.target.closest && e.target.closest(".s-menu, [data-more]")) { return; }
    closeSessionMenus(false);
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") { closeSessionMenus(true); }
  });
  function repositionOpenSessionMenus() {
    document.querySelectorAll(".s-menu").forEach(function (menu) {
      if (menu.hidden) { return; }
      var btn = menu.parentElement && menu.parentElement.querySelector("[data-more]");
      if (btn) { positionSessionMenu(btn, menu); }
    });
  }
  window.addEventListener("resize", repositionOpenSessionMenus);
  window.addEventListener("scroll", function (e) {
    if (e.target && e.target.closest && e.target.closest(".s-menu")) { return; }
    repositionOpenSessionMenus();
  }, true);
  var sessionListBody = document.querySelector(".im-list-body");
  if (sessionListBody) {
    sessionListBody.addEventListener("scroll", repositionOpenSessionMenus);
  }

  function syncExpandButton(card, expanded) {
    var btn = card.querySelector("[data-expand]");
    if (!btn) { return; }
    btn.setAttribute("aria-expanded", String(!!expanded));
    btn.setAttribute("aria-label", expanded ? "收起详情" : "展开详情");
    btn.title = expanded ? "收起详情" : "展开详情";
  }

  // createSessionCard builds the full card skeleton once; every conditional
  // block stays in the DOM (toggled via `hidden`) so updateSessionCard only
  // patches text/visibility and never re-creates nodes — scroll positions,
  // text selection, focus and the live tail therefore survive data updates.
  function createSessionCard() {
    var card = document.createElement("div");
    card.className = "session-card";
    card.setAttribute("role", "group");
    card.setAttribute("aria-keyshortcuts", "Enter Space c o a");
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
      '<span class="s-access" hidden></span>' +
      '<span class="s-sync" hidden></span></div>' +
      '<div class="s-actions">' +
      '<button class="btn primary sm att-action" type="button" data-attend hidden></button>' +
      '<button class="btn primary sm" type="button" data-open hidden>打开会话</button>' +
      '<button class="btn ghost sm s-expand" type="button" data-expand aria-expanded="false" aria-label="展开详情" title="展开详情" hidden>' +
      '<svg aria-hidden="true" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 12 15 18 9"/></svg></button>' +
      '<button class="btn ghost sm s-more" type="button" data-more aria-haspopup="menu" aria-expanded="false" title="更多操作" aria-label="更多操作">⋯</button>' +
      '<div class="s-menu" role="menu" aria-label="会话操作" hidden>' +
      '<button class="menu-item" type="button" role="menuitem" data-detail>详情</button>' +
      '<button class="menu-item" type="button" role="menuitem" data-web hidden>原版 Web</button>' +
      '<button class="menu-item" type="button" role="menuitem" data-window-open hidden title="在该节点本机的新终端窗口里打开此会话（带它当初的启动参数）">新终端</button>' +
      '<button class="menu-item" type="button" role="menuitem" data-window-resume hidden title="让该会话在节点本机重新跑起来（无窗口则新开一个）">恢复</button>' +
      '<button class="menu-item danger" type="button" role="menuitem" data-window-close hidden title="关闭节点本机上的这个会话窗口（等同在窗口里敲 /exit）">关窗口</button>' +
      '<button class="menu-item danger" type="button" role="menuitem" data-remove hidden>移除</button>' +
      '</div>' +
      '</div>' +
      '<div class="s-live" hidden title="实时输出（最后一行），点击展开完整尾流"></div>' +
      '<div class="s-tail" hidden>' +
      '<div class="s-tail-head"><span class="s-tail-title">实时输出</span><span class="s-tail-meta"></span>' +
      '<button class="btn ghost sm" type="button" data-tail-follow>跟随中</button></div>' +
      '<pre class="s-tail-body"></pre></div>';
    // One-time listeners; they read card.__item for the newest snapshot so a
    // data patch never needs to re-bind handlers.
    // 审批 / 回答：卡片上的主操作，直接打开站内面板并聚焦交互区（不发外部跳转）。
    card.querySelector("[data-attend]").addEventListener("click", function (e) {
      e.stopPropagation();
      var it = card.__item;
      if (it) { openSessionChat(it.node, it.session, { focusInteractive: true }); }
    });
    // 微信式：整行就是「打开这条会话」。行内按钮 / 展开区 / 可选中文本
    // 仍然各自生效，只有真正的空白处落点才打开会话流。
    card.addEventListener("click", function (e) {
      var it = card.__item;
      if (!it) { return; }
      var t = e.target;
      if (t && t.closest && t.closest("button, a, input, textarea, select, label, .s-tail-body, .s-last, .s-menu")) { return; }
      var sel = window.getSelection && window.getSelection();
      if (sel && String(sel).length) { return; }
      openSessionFromCard(it.node, it.session);
    });
    // 动作收纳：小屏只保留「打开会话 / ⋯」，其余动作收进弹出菜单。
    var moreBtn = card.querySelector("[data-more]");
    var menu = card.querySelector(".s-menu");
    var menuID = "s-menu-" + (++sessionMenuSeq);
    menu.id = menuID;
    moreBtn.setAttribute("aria-controls", menuID);
    moreBtn.addEventListener("click", function (e) {
      e.stopPropagation();
      var willOpen = menu.hidden;
      closeSessionMenus(false);
      if (willOpen) {
        menu.hidden = false;
        moreBtn.setAttribute("aria-expanded", "true");
        positionSessionMenu(moreBtn, menu);
        var items = visibleMenuItems(menu);
        if (items.length) { items[0].focus(); }
      }
    });
    menu.addEventListener("keydown", function (e) {
      var items = visibleMenuItems(menu);
      if (!items.length) { return; }
      var idx = items.indexOf(document.activeElement);
      if (e.key === "ArrowDown") {
        e.preventDefault();
        e.stopPropagation();
        items[(idx + 1 + items.length) % items.length].focus();
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        e.stopPropagation();
        items[(idx - 1 + items.length) % items.length].focus();
      } else if (e.key === "Home") {
        e.preventDefault();
        e.stopPropagation();
        items[0].focus();
      } else if (e.key === "End") {
        e.preventDefault();
        e.stopPropagation();
        items[items.length - 1].focus();
      } else if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        closeSessionMenus(true);
      } else if (e.key === "Tab") {
        closeSessionMenus(true);
      }
    });
    menu.addEventListener("focusout", function () {
      setTimeout(function () {
        if (menu.hidden) { return; }
        if (menu.contains(document.activeElement) || document.activeElement === moreBtn) { return; }
        closeSessionMenus(false);
      }, 0);
    });
    menu.addEventListener("click", function (e) {
      // 菜单项自己的处理器先执行；随后关闭菜单，避免遮住下一次操作。
      e.stopPropagation();
      closeSessionMenus(true);
    });
    // The card body was informational only before; 详情 stays explicit.
    card.querySelector("[data-detail]").addEventListener("click", function () {
      var it = card.__item;
      if (it) { openSessionDetail(it.node, it.session); }
    });
    // 原版 Web 是明确的外部/旧版入口，和站内「打开会话」分开命名，避免
    // 两个都叫「打开」却走向不同页面。
    card.querySelector("[data-web]").addEventListener("click", function () {
      var it = card.__item;
      if (it) { openSession(it.node, it.session); }
    });
    // Primary action: open the same inline session as a card click. Legacy
    // agents without screen/events fall back to their original Web UI and use
    // the explicit 原版 Web label instead.
    var openBtn = card.querySelector("[data-open]");
    openBtn.addEventListener("click", function () {
      var it = card.__item;
      if (!it || openBtn.disabled) { return; }
      if (openBtn.getAttribute("data-open-mode") === "web") {
        openSession(it.node, it.session);
      } else {
        openSessionFromCard(it.node, it.session);
      }
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
      syncExpandButton(card, next);
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
        syncExpandButton(card, true);
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
    // 窗口动作（新终端 / 恢复 / 关窗口）：控制台 → 网关排队 → 节点下一次心跳
    // 取走 → 在节点那台机器上执行。202 只代表「已排进队列」，提示必须如实说明
    // 投递时机——谎称窗口已打开，操作者就会对着空桌面找原因。
    var windowActions = [
      { selector: "[data-window-open]", kind: "open-terminal", done: "已排队：该节点下一个心跳会在本机新终端里打开此会话", confirm: null },
      { selector: "[data-window-resume]", kind: "resume", done: "已排队：该节点下一个心跳会恢复此会话", confirm: null },
      { selector: "[data-window-close]", kind: "close", done: "已排队：该节点下一个心跳会关闭此窗口", confirm: "关闭该会话在本机的窗口进程？\n（等同在窗口里敲 /exit：会话会正常保存。）" }
    ];
    windowActions.forEach(function (action) {
      var btn = card.querySelector(action.selector);
      btn.addEventListener("click", function (e) {
        e.stopPropagation();
        var it = card.__item;
        if (!it || btn.disabled) { return; }
        function run() {
          return requestWindowAction(it.node, it.session, action.kind)
            .then(function () { notify(action.done, "ok"); });
        }
        if (action.confirm) {
          closeSessionMenus(false);
          confirmDestructive({
            title: "关闭窗口进程？",
            message: action.confirm,
            trigger: moreBtn,
            confirmText: "关闭窗口",
            onConfirm: run
          });
          return;
        }
        run().catch(function (err) { notify("窗口动作失败：" + err, "error"); });
      });
    });
    var rmBtn = card.querySelector("[data-remove]");
    rmBtn.addEventListener("click", function (e) {
      e.stopPropagation();
      var it = card.__item;
      if (!it) { return; }
      closeSessionMenus(false);
      confirmDestructive({
        title: "移除会话条目？",
        message: "从注册表移除该会话条目？\n（仅清除网关侧快照，不影响 agent 上的会话数据。）",
        trigger: moreBtn,
        confirmText: "移除",
        onConfirm: function () {
          return api("/api/nodes/" + encodeURIComponent(it.node.node_id) +
              "/sessions/" + encodeURIComponent(it.session.session_id), { method: "DELETE" })
            .then(function () { notify("会话条目已移除", "ok"); loadNodes(); });
        }
      });
    });
    return card;
  }

  // requestWindowAction 把一条窗口动作排给节点，并把「排队成功」与「执行成功」
  // 分开表达（网关只保证投递，执行结果要等节点自己的下一次上报才可见）。
  function requestWindowAction(node, session, kind) {
    var path = "/api/nodes/" + encodeURIComponent(node.node_id) +
      "/sessions/" + encodeURIComponent(session.session_id) +
      "/window?action=" + encodeURIComponent(kind);
    return api(path, { method: "POST" });
  }
  // updateSessionCard patches one card to match the latest snapshot: text is
  // written only when it changed, visibility flips via `hidden`, and the live
  // tail keeps its scrollTop unless 跟随中 pins it to the bottom — so an
  // operator reading a scrolled-up detail is never thrown back by an update.
  function updateSessionCard(card, item) {
    var s = item.session, node = item.node;
    var cardKey = node.node_id + "|" + s.session_id;
    var canOpen = nodeHasWebUI(node);
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
    card.classList.toggle("is-active",
      !!chatState && chatState.node && chatState.session &&
      chatState.node.node_id === node.node_id &&
      chatState.session.session_id === s.session_id);
    var label = sessionLabelOf(s);
    setText(card.querySelector(".s-name"), label);
    // 头像位：微信列表行的首字母/首字方块（CSS ::before 读取 data-avatar）。
    var avatar = Array.from((label || "").trim())[0] || "·";
    if (card.getAttribute("data-avatar") !== avatar) { card.setAttribute("data-avatar", avatar); }
    card.setAttribute("aria-label",
      sessionLabelOf(s) + "，" + nodeDisplayName(node) + "，" + stateLabelOf(s) +
      (s.attention ? "，" + attentionLabel(s.attention) : "") +
      (canOpen ? "，按 Enter 打开会话" : "，按 Enter 查看详情"));
    setText(card.querySelector(".s-node-text"),
      nodeDisplayName(node) + " · " + (node.kind || "agent"));
    var accessEl = card.querySelector(".s-access");
    var accessText = node.stale || canOpen ? "" :
      (node.kind === "runtime-server" ? "服务未就绪" : "网页未启用");
    accessEl.hidden = !accessText;
    accessEl.textContent = accessText;
    if (accessText) {
      accessEl.title = webUnavailableText(node);
    } else {
      accessEl.removeAttribute("title");
    }
    // 列表不再显示 node-id / session-id（id 收进「详情」）；注意力为审批/提问
    // 且节点支持站内面板时，把主操作提升为「审批 / 回答」。
    var attendBtn = card.querySelector("[data-attend]");
    var attendKind = (s.attention === "approval" || s.attention === "question") &&
      chatPanelSupported(node, s) && chatCanInput(node) ? s.attention : "";
    if (attendKind) {
      attendBtn.hidden = false;
      attendBtn.textContent = attendKind === "approval" ? "审批" : "回答";
      attendBtn.title = attendKind === "approval"
        ? "在本面板内批准或拒绝该工具调用"
        : "在本面板内回答该提问";
      attendBtn.setAttribute("data-attention", attendKind);
    } else {
      attendBtn.hidden = true;
      attendBtn.removeAttribute("data-attention");
    }
    var syncEl = card.querySelector(".s-sync");
    var syncedMs = s.synced_at ? new Date(s.synced_at).getTime() : NaN;
    if (!isNaN(syncedMs)) {
      setRelTime(syncEl, syncedMs, "同步 ", 60000,
        "网关最后收到该会话更新：" + absoluteTime(syncedMs) + "（服务器时间）");
      syncEl.hidden = false;
    } else {
      syncEl.hidden = true;
    }
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
    var wantReply = !node.stale && canOpen &&
      (node.capabilities || []).indexOf("input") !== -1;
    var hasExtras = !!(s.last_message || "") || wantReply || !!tailText;
    var expandBtn = card.querySelector("[data-expand]");
    expandBtn.hidden = !hasExtras;
    card.classList.toggle("expanded", expanded && hasExtras);
    syncExpandButton(card, expanded && hasExtras);
    // Primary action: inline chat when the node advertises the panel contract;
    // legacy Web-only nodes keep a clearly labelled 原版 Web fallback. Offline
    // or Web-less nodes get no dead disabled button: their card click opens
    // the detail dialog instead.
    var openBtn = card.querySelector("[data-open]");
    var webItem = card.querySelector("[data-web]");
    var canInline = chatPanelSupported(node, s);
    var openMode = "";
    if (attendKind) {
      openBtn.hidden = true;
      openBtn.disabled = false;
      openBtn.removeAttribute("data-open-mode");
    } else if (canOpen) {
      openMode = canInline ? "inline" : "web";
      openBtn.hidden = false;
      openBtn.disabled = false;
      openBtn.textContent = openMode === "inline" ? "打开会话" : "原版 Web";
      openBtn.title = openMode === "inline" ? "在站内打开该会话" : "在浏览器打开原版 Web UI";
      openBtn.setAttribute("data-open-mode", openMode);
    } else {
      openBtn.hidden = true;
      openBtn.disabled = true;
      openBtn.removeAttribute("data-open-mode");
      openBtn.title = "";
    }
    webItem.hidden = !canOpen || openMode === "web";
    card.querySelector("[data-remove]").hidden = !node.stale;
    // 窗口动作按钮：只在该节点自己通告了 window-actions（本机有可用终端）时出现。
    // 子代理行不独立拥有窗口——它是父进程里的一个任务，所以恒不显示。
    var canWindow = windowActionsSupported(node) && !isSubagentSession(s);
    card.querySelector("[data-window-open]").hidden = !canWindow;
    card.querySelector("[data-window-resume]").hidden = !canWindow;
    card.querySelector("[data-window-close]").hidden = !canWindow;
    // 「新终端」的落点与命令要能被看到：这两个字段是 agent 上报的本机事实
    // （工作目录错了或命令不对，操作者在按钮按下前就能发现）。
    var windowTitle = canWindow
      ? "窗口将在该节点本机执行" + (s.workspace ? "\n工作目录：" + s.workspace : "") +
          (s.launch_command ? "\n启动命令：" + s.launch_command : "") +
          (s.mesh_window ? "\n当前窗口：" + s.mesh_window : "")
      : "该节点未提供本机窗口能力";
    card.querySelector("[data-window-open]").title = windowTitle;
    // Inline reply: POST through the gateway reverse proxy to the agent's
    // own /web/api/input (the agent injects its local write token and the
    // route allowlist already covers /web). Gated on the agent advertising
    // the "input" capability so stale agents just hide the form.
    var replyForm = card.querySelector(".s-reply");
    if (wantReply && !replyForm) {
      card.insertBefore(buildReplyForm(node, s), card.querySelector(".s-live"));
    } else if (!wantReply && replyForm) {
      replyForm.remove();
    }
    var openMenu = card.querySelector(".s-menu");
    if (openMenu && !openMenu.hidden) {
      var menuItems = visibleMenuItems(openMenu);
      if (openMenu.contains(document.activeElement) && menuItems.indexOf(document.activeElement) === -1 && menuItems.length) {
        menuItems[0].focus();
      }
      var menuTrigger = card.querySelector("[data-more]");
      if (menuTrigger) { positionSessionMenu(menuTrigger, openMenu); }
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
        nodeDisplayName(item.node) + " · " + sessionLabelOf(item.session);
      var b = row.querySelector(".att-badge");
      b.textContent = attentionLabel(item.session.attention);
      b.className = "att-badge att-" + item.session.attention;
      b.hidden = false;
      // 副标题给出「节点 · 状态」，node-id 只收进 hover 提示，避免与标题重复。
      row.querySelector(".card-meta").textContent =
        nodeDisplayName(item.node) + " · " + stateLabelOf(item.session);
      if (item.node.node_id) { row.title = "节点 ID：" + item.node.node_id; }
      row.addEventListener("click", function () {
        // 待办行点击与卡片「审批 / 回答」同语义：能面板内打开就聚焦交互区。
        openSessionInline(item.node, item.session, chatFocusOptsFor(item.session));
      });
      list.appendChild(row);
    });
    // 面板头部的「下一条待处理」按钮随同一份 inbox 数据刷新（面板关闭时自隐藏）。
    chatUpdateNextPending();
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
      nodeDisplayName(node);
    card.querySelector(".badge").textContent =
      (node.kind || "agent") + " · 会话 " + sessions.length;
    var meta = card.querySelector(".card-meta");
    // node-id 属技术标识：不再作为副标题与标题重复展示。
    // 完整 id 保留在 hover 提示与「详情」弹窗，排障时仍可查看/复制。
    if (node.node_id) {
      card.setAttribute("data-node-id", node.node_id);
      card.title = "节点 ID：" + node.node_id;
    }
    meta.textContent = "";
    var hbMs = node.last_heartbeat ? new Date(node.last_heartbeat).getTime() : NaN;
    if (!isNaN(hbMs)) {
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
          openSessionInline(node, session);
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
    if (node.stale) {
      // UX-06: 破坏性动作默认收进 ⋯，不再作为实心红按钮常驻。
      actions.insertAdjacentHTML("beforeend",
        '<button class="btn ghost sm s-more" type="button" data-more aria-haspopup="menu" aria-expanded="false" title="更多操作" aria-label="更多操作">⋯</button>' +
        '<div class="s-menu" role="menu" aria-label="离线快照节点操作" hidden>' +
        '<button class="menu-item danger" type="button" role="menuitem" data-remove>移除该节点</button>' +
        '</div>');
      var staleMenu = actions.querySelector(".s-menu");
      var staleMore = actions.querySelector("[data-more]");
      bindCardMenu(card, staleMenu);
      staleMenu.querySelector("[data-remove]").addEventListener("click", function () {
        closeSessionMenus(false);
        confirmDestructive({
          title: "移除离线快照节点？",
          message: "移除离线快照节点 " + nodeDisplayName(node) +
            "？\n该节点的所有会话条目会一并删除（agent 重新上线会自动重新注册）。",
          trigger: staleMore,
          onConfirm: function () {
            return api("/api/nodes/" + encodeURIComponent(node.node_id), { method: "DELETE" })
              .then(function () {
                notify("节点已移除：" + nodeDisplayName(node), "ok");
                loadNodes();
              });
          }
        });
      });
    }

    if (node.node_id) {
      var copyNode = document.createElement("button");
      copyNode.className = "btn ghost sm";
      copyNode.type = "button";
      copyNode.textContent = "复制节点 ID";
      copyNode.title = "复制完整节点 ID：" + node.node_id;
      copyNode.setAttribute("aria-label", "复制节点 ID " + shortNodeID(node.node_id));
      copyNode.addEventListener("click", function () { copyText(node.node_id, copyNode); });
      actions.appendChild(copyNode);
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
      clearBtn.setAttribute("aria-label", "清除全部离线快照节点，共 " + staleNodes.length + " 个");
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
    var counts = { approval: 0, question: 0, error: 0 };
    var items = inboxItems(nodes);
    items.forEach(function (item) {
      var kind = item.session.attention;
      if (counts[kind] !== undefined) { counts[kind]++; }
    });
    panel.hidden = !items.length;
    ["approval", "question", "error"].forEach(function (k) {
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
        nodeDisplayName(item.node) + " · " + sessionLabelOf(s) +
        (s.attention ? "（" + attentionLabel(s.attention) + "）" : "") +
        (item.node.stale ? "（离线快照）" : "");
      row.querySelector(".activity-when").textContent =
        when.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) +
        (fresh ? " · 刚刚" : "");
      if (fresh) { row.classList.add("fresh"); }
      row.addEventListener("click", function () { openSessionInline(item.node, s); });
      host.appendChild(row);
    });
    var win = el("activity-window");
    if (win) { win.textContent = "最近 " + items.length + " 条"; }
  }

  function renderAll(nodes) {
    // v40 重构丢了这里的调用，导致 trackAttention（fresh attention →
    // toast/原生通知）整条链路从不执行；恢复并加控制台资产测试防回归。
    trackAttention(nodes);
    updateStats(nodes);
    renderInbox(nodes);
    renderAttentionBreakdown(nodes);
    renderActivityFeed(nodes);
    renderNodeLists(nodes);
    renderSessionTable(nodes);
    if (currentView() === "notifications") { renderNotifyPolicySessions(); }
  }

  /* ─────────────────────── rendering: devices ────────────────── */

  function renderDevices(devices) {
    var list = el("device-list");
    if (!list) { return; }
    var search = el("device-search");
    if (search && search.value !== deviceQuery) { search.value = deviceQuery; }
    var status = el("device-status-filter");
    if (status && status.value !== deviceStatusFilter) { status.value = deviceStatusFilter; }
    var query = String(deviceQuery || "").trim().toLowerCase();
    var filtered = devices.filter(function (d) {
      if (deviceStatusFilter === "active" && d.revoked) { return false; }
      if (deviceStatusFilter === "revoked" && !d.revoked) { return false; }
      if (!query) { return true; }
      return [d.name, d.id, d.prefix].join(" ").toLowerCase().indexOf(query) !== -1;
    });
    var count = el("device-count");
    if (count) {
      count.textContent = filtered.length === devices.length
        ? devices.length + " 个令牌"
        : filtered.length + " / " + devices.length + " 个令牌";
      count.hidden = devices.length === 0;
    }
    list.innerHTML = "";
    if (!devices.length) {
      list.innerHTML = '<p class="empty">还没有设备令牌 —— 签发一个给 aicli / runtime-server 接入用。</p>';
      return;
    }
    if (!filtered.length) {
      list.innerHTML = '<p class="empty">没有匹配的令牌。清除搜索或切换状态过滤后再试。</p>';
      return;
    }
    filtered.forEach(function (d) {
      var card = document.createElement("div");
      card.className = "card device-card" + (d.revoked ? " offline" : "");
      card.innerHTML =
        '<div class="card-head"><span class="card-title"></span><span class="status-chip ' + (d.revoked ? "chip-revoked" : "chip-active") + '"></span></div>' +
        '<div class="device-token-row"><span class="device-token-label">令牌标识</span><code class="device-prefix"></code><button class="btn ghost sm device-prefix-copy" type="button">复制</button></div>' +
        '<div class="card-meta"></div>' +
        '<div class="card-actions"></div>';
      card.querySelector(".card-title").textContent = d.name || d.id;
      card.querySelector(".status-chip").textContent = d.revoked ? "已撤销" : "有效";
      var prefixEl = card.querySelector(".device-prefix");
      // The API only exposes the 8-char identifier (the full token is hashed at
      // rest and shown once at issue time), so render it as the complete id.
      prefixEl.textContent = d.prefix || "—";
      prefixEl.title = "令牌标识（完整令牌仅在签发时显示一次）";
      if (d.revoked) { prefixEl.style.textDecoration = "line-through"; }
      var prefixCopy = card.querySelector(".device-prefix-copy");
      prefixCopy.setAttribute("aria-label", "复制令牌标识 " + (d.prefix || ""));
      prefixCopy.title = "复制令牌标识（不是完整令牌）";
      prefixCopy.addEventListener("click", function () { copyText(d.prefix || "", prefixCopy); });
      var meta = card.querySelector(".card-meta");
      appendDeviceMeta(meta, "记录 ID " + (d.id || "—"), "meta-id");
      appendDeviceMeta(meta, "签发于 " + (d.created_at ? new Date(d.created_at).toLocaleString() : "—"), "meta-time");
      if (d.last_used && d.last_used.indexOf("0001") === -1) {
        appendDeviceMeta(meta, "最近使用 " + new Date(d.last_used).toLocaleString(), "meta-time");
      }
      // UX-06: 撤销/删除收进 ⋯，列表行不再常驻实心红按钮。
      var deviceActions = card.querySelector(".card-actions");
      deviceActions.insertAdjacentHTML("beforeend",
        '<button class="btn ghost sm s-more" type="button" data-more aria-haspopup="menu" aria-expanded="false" title="更多操作" aria-label="更多操作">⋯</button>' +
        '<div class="s-menu" role="menu" aria-label="设备令牌操作" hidden>' +
        '<button class="menu-item" type="button" role="menuitem" data-copy-id>复制记录 ID</button>' +
        '<button class="menu-item danger" type="button" role="menuitem" data-action>' +
        (d.revoked ? "删除记录" : "撤销令牌") +
        '</button>' +
        '</div>');
      var deviceMenu = deviceActions.querySelector(".s-menu");
      var deviceMore = deviceActions.querySelector("[data-more]");
      bindCardMenu(card, deviceMenu);
      deviceMenu.querySelector("[data-copy-id]").addEventListener("click", function () {
        copyText(d.id || "", this);
        closeSessionMenus(true);
      });
      deviceMenu.querySelector("[data-action]").addEventListener("click", function () {
        closeSessionMenus(false);
        var purge = !!d.revoked;
        confirmDestructive({
          title: purge ? "删除记录？" : "撤销令牌？",
          message: purge
            ? "彻底删除已撤销令牌「" + (d.name || d.id) + "」的记录？\n此操作不可恢复（令牌本身已失效）。"
            : "撤销令牌「" + (d.name || d.id) + "」？\n使用它的 agent 将无法再注册；撤销后可选择彻底删除记录。",
          trigger: deviceMore,
          onConfirm: function () {
            return api("/api/devices/" + encodeURIComponent(d.id) + (purge ? "?purge=1" : ""), { method: "DELETE" })
              .then(function () {
                notify((purge ? "令牌记录已删除：" : "令牌已撤销：") + (d.name || d.id), "ok");
                loadDevices();
              });
          }
        });
      });

      list.appendChild(card);
    });
  }

  function appendDeviceMeta(host, text, className) {
    if (!host) { return; }
    if (host.childNodes.length) {
      var sep = document.createElement("span");
      sep.className = "meta-sep";
      sep.textContent = " · ";
      host.appendChild(sep);
    }
    var part = document.createElement("span");
    part.className = className || "";
    part.textContent = text;
    host.appendChild(part);
  }

  /* ─────────────────────── MCP mounts ────────────────────────── */

  function loadMounts() {
    return apiJSON("/api/mounts").then(function (r) {
      if (r.res.status === 401) { setAuthed(false); return; }
      lastMounts = (r.body && r.body.mounts) || [];
      renderMounts(lastMounts);
    });
  }

  function mountLabels(m) {
    return (m && m.labels) || {};
  }

  function mountLabelList(value) {
    if (!Array.isArray(value)) { return []; }
    return value.filter(function (item) {
      return typeof item === "string" && item.trim() !== "";
    });
  }

  function mountToolCount(m) {
    var labels = mountLabels(m);
    var count = Number(labels.tool_count);
    if (!isFinite(count) || count < 0) { count = 0; }
    return Math.max(count, mountLabelList(labels.tools).length);
  }

  function mountHostText(labels) {
    var host = labels.hostname || "—";
    var platform = [labels.os, labels.arch].filter(Boolean).join("/");
    return platform ? host + " · " + platform : host;
  }

  function mountProfileText(labels) {
    var profile = labels.profile || "—";
    if (labels.read_only && profile !== "readonly") {
      profile += " · 只读";
    }
    if (labels.tool_prefix) {
      profile += " · " + labels.tool_prefix;
    }
    return profile;
  }

  function mountVersionText(labels) {
    var bits = [];
    if (labels.version) { bits.push(labels.version); }
    if (labels.instance) { bits.push("实例 " + labels.instance); }
    if (labels.backend) { bits.push(labels.backend); }
    return bits.length ? bits.join(" · ") : "—";
  }

  function appendMountFact(facts, label, value, asCode) {
    var row = document.createElement("div");
    row.className = "mcp-fact";
    var name = document.createElement("span");
    name.className = "mcp-fact-label";
    name.textContent = label;
    var text = document.createElement(asCode ? "code" : "span");
    text.className = "mcp-fact-value";
    text.textContent = value;
    row.appendChild(name);
    row.appendChild(text);
    facts.appendChild(row);
  }

  function appendMountChips(container, values) {
    var list = document.createElement("div");
    list.className = "mcp-chip-list";
    values.forEach(function (value) {
      var chip = document.createElement("code");
      chip.className = "mcp-chip";
      chip.textContent = value;
      list.appendChild(chip);
    });
    container.appendChild(list);
  }

  function appendMountDetails(host, title, values, emptyText) {
    var details = document.createElement("details");
    details.className = "mcp-details";
    var summary = document.createElement("summary");
    summary.textContent = title;
    summary.title = "展开查看";
    details.appendChild(summary);
    if (values.length) {
      appendMountChips(details, values);
    } else if (emptyText) {
      var empty = document.createElement("p");
      empty.className = "mcp-detail-empty";
      empty.textContent = emptyText;
      details.appendChild(empty);
    }
    host.appendChild(details);
  }

  function appendMountFacts(factsHost, detailsHost, m) {
    var labels = mountLabels(m);
    var facts = factsHost;
    if (!facts) { return; }
    appendMountFact(facts, "主机", mountHostText(labels));
    appendMountFact(facts, "Profile", mountProfileText(labels));
    appendMountFact(facts, "工作区", labels.workspace || "—", true);
    appendMountFact(facts, "版本", mountVersionText(labels));

    var tools = mountLabelList(labels.tools);
    var toolCount = mountToolCount(m);
    if (toolCount > 0 || tools.length > 0) {
      appendMountDetails(
        detailsHost,
        "工具（" + (toolCount || tools.length) + "）",
        tools,
        "节点只上报了数量，没有工具名（旧版节点或列表被裁剪）。"
      );
    }
    var roots = mountLabelList(labels.roots);
    if (roots.length > 0) {
      appendMountDetails(detailsHost, "目录（" + roots.length + "）", roots, "");
    }
  }

  function renderMounts(mounts) {
    var list = el("mcp-list");
    if (!list) { return; }
    var summary = el("mcp-summary");
    var live = 0, disabled = 0, offline = 0;
    mounts.forEach(function (m) {
      if (m.disabled) { disabled++; } else if (m.stale) { offline++; } else { live++; }
    });
    if (summary) {
      summary.textContent = mounts.length
        ? (mounts.length + " 个 · 在线 " + live +
           (disabled ? " · 禁用 " + disabled : "") +
           (offline ? " · 离线 " + offline : ""))
        : "";
    }
    list.innerHTML = "";
    if (!mounts.length) {
      list.innerHTML = '<p class="empty">还没有 MCP 挂载 —— 在节点上运行 aicli-remote-tools serve 并连到本网关后会自动出现。</p>';
      return;
    }
    mounts.forEach(function (m) {
      var statusText = m.disabled ? "已禁用" : (m.stale ? "离线" : "在线");
      var chip = m.disabled ? "chip-revoked" : (m.stale ? "chip-offline" : "chip-active");
      var card = document.createElement("div");
      card.className = "card mcp-card";
      card.innerHTML =
        '<div class="card-head"><span class="card-title"></span>' +
        '<span class="status-chip ' + chip + '"></span></div>' +
        '<div class="mcp-slug"><code></code></div>' +
        '<div class="mcp-endpoint"><code class="mcp-url"></code></div>' +
        '<div class="mcp-facts"></div>' +
        '<div class="mcp-details-host"></div>' +
        '<div class="card-meta"></div>' +
        '<div class="card-actions"></div>';
      card.querySelector(".card-title").textContent = m.name || m.slug;
      card.querySelector(".status-chip").textContent = statusText;
      card.querySelector(".mcp-slug code").textContent = "/m/" + m.slug + "/mcp";
      var urlEl = card.querySelector(".mcp-url");
      urlEl.textContent = m.public_url || "（网关未配置 public-base-url）";
      var bits = ["节点 " + (shortNodeID(m.node_id) || "—")];
      if (m.local_path) { bits.push("本地 " + m.local_path); }
      if (m.tokens != null) { bits.push(m.tokens + " 个令牌"); }
      if (m.limits && m.limits.max_body_bytes) {
        bits.push(Math.round(m.limits.max_body_bytes / 1024 / 1024) + " MiB");
      }
      if (m.last_seen) { bits.push("最近 " + new Date(m.last_seen).toLocaleString()); }
      card.querySelector(".card-meta").textContent = bits.join(" · ");
      appendMountFacts(
        card.querySelector(".mcp-facts"),
        card.querySelector(".mcp-details-host"),
        m
      );

      var actions = card.querySelector(".card-actions");
      if (m.public_url) {
        var copy = document.createElement("button");
        copy.className = "btn ghost sm";
        copy.type = "button";
        copy.textContent = "复制地址";
        copy.addEventListener("click", function () { copyText(m.public_url, copy); });
        actions.appendChild(copy);
      }
      var tokensBtn = document.createElement("button");
      tokensBtn.className = "btn ghost sm";
      tokensBtn.type = "button";
      tokensBtn.textContent = m.tokens ? "令牌 · " + m.tokens : "令牌";
      tokensBtn.addEventListener("click", function () { openMountTokens(m); });
      actions.appendChild(tokensBtn);
      var logsBtn = document.createElement("button");
      logsBtn.className = "btn ghost sm";
      logsBtn.type = "button";
      logsBtn.textContent = "日志";
      logsBtn.addEventListener("click", function () {
        navigate("logs", "slug=" + encodeURIComponent(m.slug));
      });
      actions.appendChild(logsBtn);
      var toggle = document.createElement("button");
      toggle.className = "btn sm " + (m.disabled ? "primary" : "ghost");
      toggle.type = "button";
      toggle.textContent = m.disabled ? "启用" : "禁用";
      toggle.addEventListener("click", function () {
        var verb = m.disabled ? "启用" : "禁用";
        var path = "/api/mounts/" + encodeURIComponent(m.slug) + (m.disabled ? "/enable" : "/disable");
        var opts = { method: "POST", headers: { "Content-Type": "application/json" } };
        if (!m.disabled) { opts.body = JSON.stringify({ reason: "console" }); }
        function apply() {
          return api(path, opts).then(function (res) {
            if (!res.ok) { throw new Error("HTTP " + res.status); }
            notify("已" + verb + "：" + (m.name || m.slug), "ok");
            loadMounts();
          });
        }
        if (m.disabled) {
          apply().catch(function (err) { notify(verb + "失败：" + err, "error"); });
          return;
        }
        confirmDestructive({
          title: "禁用挂载？",
          message: "禁用挂载「" + (m.name || m.slug) + "」？\n公网入口会立即返回 403，节点与地址保留。",
          trigger: toggle,
          onConfirm: apply
        });
      });
      actions.appendChild(toggle);

      // UX-06: 仅离线挂载存在删除动作，因此只有它需要 ⋯；
      // 菜单项使用 danger 文本色，而不是实心红按钮。
      if (m.stale) {
        actions.insertAdjacentHTML("beforeend",
          '<button class="btn ghost sm s-more" type="button" data-more aria-haspopup="menu" aria-expanded="false" title="更多操作" aria-label="更多操作">⋯</button>' +
          '<div class="s-menu" role="menu" aria-label="MCP 挂载操作" hidden>' +
          '<button class="menu-item danger" type="button" role="menuitem" data-delete>删除离线挂载</button>' +
          '</div>');
        var mountMenu = actions.querySelector(".s-menu");
        var mountMore = actions.querySelector("[data-more]");
        bindCardMenu(card, mountMenu);
        mountMenu.querySelector("[data-delete]").addEventListener("click", function () {
          closeSessionMenus(false);
          confirmDestructive({
            title: "删除离线挂载？",
            message: "删除离线挂载「" + (m.name || m.slug) + "」？\n地址将不再保留，持有该 URL 的客户端会得到 404。",
            trigger: mountMore,
            onConfirm: function () {
              return api("/api/mounts/" + encodeURIComponent(m.slug), { method: "DELETE" })
                .then(function (res) {
                  if (!res.ok) { throw new Error("HTTP " + res.status); }
                  notify("已删除挂载：" + (m.name || m.slug), "ok");
                  loadMounts();
                });
            }
          });
        });
      }

      list.appendChild(card);
    });
  }

  /* ─────────────────── MCP mount tokens ──────────────────────── */

  function openMountTokens(m) {
    if (!m) { return; }
    mountTokenTarget = { slug: m.slug, name: m.name || m.slug };
    el("mount-token-modal-title").textContent = "MCP 令牌 · " + mountTokenTarget.name;
    el("mount-token-modal-sub").textContent =
      "外部客户端调用 " + (m.public_url || ("/m/" + m.slug + "/mcp")) +
      " 的凭据。明文只在签发时显示一次；网关只保存 SHA-256 与指纹。";
    el("mount-token-name").value = "";
    el("mount-token-ttl").value = "7776000";
    el("mount-token-days").value = "";
    syncMountTokenTTL();
    hideMountTokenReveal();
    openModal("mount-token-modal");
    el("mount-token-name").focus();
    loadMountTokens();
  }

  function hideMountTokenReveal() {
    var box = el("mount-token-reveal");
    if (!box) { return; }
    box.hidden = true;
    box.innerHTML = "";
  }

  function syncMountTokenTTL() {
    var select = el("mount-token-ttl");
    var days = el("mount-token-days");
    var hint = el("mount-token-ttl-hint");
    if (!select) { return; }
    var custom = select.value === "custom";
    var never = select.value === "never";
    if (days) {
      days.hidden = !custom;
      if (custom) { days.focus(); }
    }
    if (hint) { hint.hidden = !never; }
  }

  function closeMountTokenModal() {
    // Never leave the one-time plaintext in a hidden DOM node after close.
    hideMountTokenReveal();
    mountTokenTarget = null;
    closeModal("mount-token-modal");
  }

  function showMountToken(token) {
    var box = el("mount-token-reveal");
    if (!box) { return; }
    box.innerHTML = "";
    var hint = document.createElement("div");
    hint.className = "token-hint";
    hint.textContent = "完整令牌仅显示这一次，关闭后无法再次查看（只能撤销重建）——请立即复制保存：";
    var row = document.createElement("div");
    row.className = "token-row";
    var strong = document.createElement("strong");
    strong.textContent = token;
    var copy = document.createElement("button");
    copy.className = "btn ghost sm";
    copy.type = "button";
    copy.textContent = "复制";
    copy.addEventListener("click", function () { copyText(token, copy); });
    row.appendChild(strong);
    row.appendChild(copy);
    var note = document.createElement("div");
    note.className = "token-note";
    note.textContent = "客户端请求头：Authorization: Bearer <上方令牌>";
    box.appendChild(hint);
    box.appendChild(row);
    box.appendChild(note);
    box.hidden = false;
  }

  function loadMountTokens() {
    if (!mountTokenTarget) { return Promise.resolve(); }
    var slug = mountTokenTarget.slug;
    return apiJSON("/api/mounts/" + encodeURIComponent(slug) + "/tokens").then(function (r) {
      if (r.res.status === 401) { setAuthed(false); return; }
      if (!r.res.ok) {
        notify("读取 MCP 令牌失败：HTTP " + r.res.status, "error");
        return;
      }
      renderMountTokens((r.body && r.body.tokens) || []);
    }).catch(function (err) {
      notify("读取 MCP 令牌失败：" + err, "error");
    });
  }

  function validTokenTime(raw) {
    if (!raw) { return null; }
    var d = new Date(raw);
    if (isNaN(d.getTime()) || d.getUTCFullYear() <= 1) { return null; }
    return d;
  }

  function mountTokenState(t) {
    if (t.revoked) { return { text: "已撤销", chip: "chip-revoked" }; }
    var exp = validTokenTime(t.expires_at);
    if (exp && exp.getTime() <= Date.now()) {
      return { text: "已过期", chip: "chip-revoked" };
    }
    return { text: "有效", chip: "chip-active" };
  }

  function renderMountTokens(tokens) {
    var list = el("mount-token-list");
    if (!list) { return; }
    list.innerHTML = "";
    if (!tokens.length) {
      list.innerHTML = '<p class="empty">还没有令牌 —— 在上方签发一个给外部 MCP 客户端。</p>';
      return;
    }
    var targetSlug = mountTokenTarget ? mountTokenTarget.slug : "";
    tokens.forEach(function (t) {
      var state = mountTokenState(t);
      var card = document.createElement("div");
      card.className = "card mcp-token-card";
      card.innerHTML =
        '<div class="card-head"><span class="card-title"></span>' +
        '<span class="status-chip ' + state.chip + '"></span></div>' +
        '<div class="mcp-token-prefix"><code></code></div>' +
        '<div class="card-meta"></div>' +
        '<div class="card-actions"></div>';
      card.querySelector(".card-title").textContent = t.name || "未命名调用方";
      card.querySelector(".status-chip").textContent = state.text;
      var prefix = card.querySelector(".mcp-token-prefix code");
      prefix.textContent = t.prefix;
      prefix.title = "令牌指纹（完整令牌只在签发时显示一次）";
      if (t.revoked) { prefix.style.textDecoration = "line-through"; }
      var bits = ["id " + t.id];
      var created = validTokenTime(t.created_at);
      if (created) { bits.push("签发 " + created.toLocaleString()); }
      var used = validTokenTime(t.last_used);
      if (used) { bits.push("最近使用 " + used.toLocaleString()); }
      var expires = validTokenTime(t.expires_at);
      if (expires) {
        bits.push("到期 " + expires.toLocaleString());
      } else {
        bits.push("不过期");
      }
      if (t.mounts && t.mounts.length > 1) {
        bits.push("作用域 " + t.mounts.join(", "));
      }
      card.querySelector(".card-meta").textContent = bits.join(" · ");

      var actions = card.querySelector(".card-actions");
      if (!t.revoked) {
        var revoke = document.createElement("button");
        revoke.className = "btn ghost sm";
        revoke.type = "button";
        revoke.textContent = "撤销";
        revoke.addEventListener("click", function () {
          confirmDestructive({
            title: "撤销 MCP 令牌？",
            message: "撤销令牌「" + (t.name || t.id) + "」？\n使用它的客户端下一次请求即 401。",
            trigger: revoke,
            confirmText: "撤销",
            onConfirm: function () {
              return api("/api/mounts/" + encodeURIComponent(targetSlug) + "/tokens/" + encodeURIComponent(t.id), {
                method: "DELETE",
              }).then(function (res) {
                if (!res.ok) { throw new Error("HTTP " + res.status); }
                notify("令牌已撤销：" + (t.name || t.id), "ok");
                loadMountTokens();
                loadMounts();
              });
            }
          });
        });
        actions.appendChild(revoke);
      } else {
        var del = document.createElement("button");
        del.className = "btn ghost sm";
        del.type = "button";
        del.textContent = "删除记录";
        del.addEventListener("click", function () {
          confirmDestructive({
            title: "删除令牌记录？",
            message: "彻底删除已撤销令牌「" + (t.name || t.id) + "」的记录？\n此操作不可恢复。",
            trigger: del,
            confirmText: "删除记录",
            onConfirm: function () {
              return api("/api/mounts/" + encodeURIComponent(targetSlug) + "/tokens/" +
                  encodeURIComponent(t.id) + "?purge=1", { method: "DELETE" })
                .then(function (res) {
                  if (!res.ok) { throw new Error("HTTP " + res.status); }
                  notify("令牌记录已删除", "ok");
                  loadMountTokens();
                });
            }
          });
        });
        actions.appendChild(del);
      }
      list.appendChild(card);
    });
  }

  /* ───────────────────────── logs view ───────────────────────── */

  function stopLogTimer() {
    if (logTimer) {
      clearInterval(logTimer);
      logTimer = null;
    }
  }

  function startLogTimer() {
    stopLogTimer();
    if (!logFollow) { return; }
    logTimer = setInterval(function () {
      if (currentView() === "logs") { refreshLogs(); }
    }, 5000);
  }

  function initLogsView() {
    // A per-mount "日志" link always means "this mount's calls": force the
    // mounts tab even if the operator last looked at audit events.
    if (logHashSlug()) {
      logTab = "mounts";
      applyLogTabUI();
    }
    var ready = lastMounts.length ? Promise.resolve() : loadMounts();
    Promise.resolve(ready).then(function () {
      populateLogSlugOptions();
      refreshLogs();
    });
    startLogTimer();
  }

  function logHashSlug() {
    var h = window.location.hash || "";
    var q = h.indexOf("?");
    if (q < 0) { return ""; }
    return new URLSearchParams(h.slice(q + 1)).get("slug") || "";
  }

  function populateLogSlugOptions() {
    var sel = el("log-slug");
    if (!sel) { return; }
    var wanted = logHashSlug() || sel.value;
    sel.innerHTML = '<option value="">全部挂载</option>';
    lastMounts.forEach(function (m) {
      var opt = document.createElement("option");
      opt.value = m.slug;
      opt.textContent = (m.name || m.slug) + " · " + m.slug;
      sel.appendChild(opt);
    });
    if (wanted) { sel.value = wanted; }
  }

  function setLogSummary(text) {
    var summary = el("logs-summary");
    if (summary) { summary.textContent = text || ""; }
  }

  function refreshLogs() {
    if (currentView() !== "logs" || logLoading) { return; }
    logLoading = true;
    var job = logTab === "audit" ? loadAuditLogs() : loadMountLogs();
    Promise.resolve(job).catch(function () {
      setLogSummary("读取失败，稍后重试");
    }).then(function () {
      logLoading = false;
    });
  }

  function loadMountLogs() {
    var sel = el("log-slug");
    var slug = sel ? sel.value : "";
    var url = "/api/mounts/logs?limit=500";
    if (slug) { url += "&slug=" + encodeURIComponent(slug); }
    return apiJSON(url).then(function (r) {
      if (r.res.status === 401) { setAuthed(false); return; }
      if (!r.res.ok) {
        setLogSummary("MCP 日志读取失败：HTTP " + r.res.status);
        return;
      }
      logMountEntries = (r.body && r.body.entries) || [];
      renderMountLogs(logMountEntries);
      setLogSummary("MCP 调用 " + logMountEntries.length + " 条 · 更新于 " +
        new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }));
    });
  }

  function mountStatusFilter() {
    var sel = el("log-status");
    return sel ? sel.value : "";
  }

  function renderMountLogs(entries) {
    var host = el("log-mount-list");
    if (!host) { return; }
    var filter = mountStatusFilter();
    var visible = entries.filter(function (e) {
      if (!filter) { return true; }
      if (filter === "errors") { return e.status >= 400 && e.reason !== "sse_unavailable"; }
      return String(e.status) === filter;
    });
    host.innerHTML = "";
    if (!visible.length) {
      host.innerHTML = '<p class="empty">没有匹配的调用记录。</p>';
      return;
    }
    visible.forEach(function (e) { host.appendChild(mountLogRow(e)); });
  }

  function mountLogRow(e) {
    var row = document.createElement("div");
    row.className = "log-row";
    var status = document.createElement("span");
    var expectedSse = e.reason === "sse_unavailable";
    status.className = "log-status " +
      (expectedSse ? "st-event" : (e.status >= 500 ? "st-bad" : (e.status >= 400 ? "st-warn" : "st-ok")));
    status.textContent = e.status ? String(e.status) : "—";
    var main = document.createElement("span");
    main.className = "log-main";
    var title = document.createElement("span");
    title.className = "log-title";
    var slug = document.createElement("code");
    slug.textContent = e.slug || "—";
    title.appendChild(slug);
    var titleText = " " + (e.method || "") + " " + (e.path || "");
    if (e.command) { titleText += " · " + e.command; }
    title.appendChild(document.createTextNode(titleText));
    var meta = document.createElement("span");
    meta.className = "log-meta";
    var bits = [];
    if (e.token_id) { bits.push("token " + e.token_id); }
    if (e.remote_addr) { bits.push(e.remote_addr); }
    if (e.request_bytes != null || e.response_bytes != null) {
      bits.push(formatBytes(e.request_bytes) + " → " + formatBytes(e.response_bytes));
    }
    if (e.duration_ms != null) { bits.push(e.duration_ms + "ms"); }
    if (e.user_agent) { bits.push("UA " + truncate(e.user_agent, 48)); }
    if (e.reason) { bits.push(e.reason === "sse_unavailable" ? "SSE 推送未启用（正常）" : e.reason); }
    meta.textContent = bits.join(" · ");
    main.appendChild(title);
    main.appendChild(meta);
    var time = document.createElement("span");
    time.className = "log-time";
    time.textContent = formatLogTime(e.time);
    var when = validTokenTime(e.time);
    if (when) { time.title = when.toLocaleString(); }
    row.appendChild(status);
    row.appendChild(main);
    row.appendChild(time);
    return row;
  }

  function loadAuditLogs() {
    var type = el("log-type") ? el("log-type").value : "";
    var query = el("log-query") ? el("log-query").value.trim() : "";
    var url = "/api/audit?limit=500";
    if (type) { url += "&type=" + encodeURIComponent(type); }
    if (query) { url += "&q=" + encodeURIComponent(query); }
    return apiJSON(url).then(function (r) {
      if (r.res.status === 401) { setAuthed(false); return; }
      if (!r.res.ok) {
        setLogSummary("审计日志读取失败：HTTP " + r.res.status);
        return;
      }
      logAuditEntries = (r.body && r.body.events) || [];
      renderAuditLogs(logAuditEntries);
      setLogSummary("审计事件 " + logAuditEntries.length + " 条 · 更新于 " +
        new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }));
    });
  }

  function renderAuditLogs(events) {
    var host = el("log-audit-list");
    if (!host) { return; }
    host.innerHTML = "";
    if (!events.length) {
      host.innerHTML = '<p class="empty">没有匹配的审计事件。</p>';
      return;
    }
    events.forEach(function (event) { host.appendChild(auditLogRow(event)); });
  }

  function auditLogRow(event) {
    var row = document.createElement("div");
    row.className = "log-row";
    var type = document.createElement("span");
    type.className = "log-status st-event" + (event.result ? " st-warn" : "");
    type.textContent = (event.type || "event").replace(/^mount_/, "mount/");
    var main = document.createElement("span");
    main.className = "log-main";
    var title = document.createElement("span");
    title.className = "log-title";
    title.textContent = (event.type || "event") +
      (event.result ? " · " + event.result : "") +
      (event.actor ? " · " + event.actor : "");
    var meta = document.createElement("span");
    meta.className = "log-meta";
    var bits = [];
    if (event.detail) { bits.push(event.detail); }
    if (event.ip) { bits.push("ip " + event.ip); }
    meta.textContent = bits.join(" · ");
    main.appendChild(title);
    main.appendChild(meta);
    var time = document.createElement("span");
    time.className = "log-time";
    time.textContent = formatLogTime(event.time);
    var when = validTokenTime(event.time);
    if (when) { time.title = when.toLocaleString(); }
    row.appendChild(type);
    row.appendChild(main);
    row.appendChild(time);
    return row;
  }

  function formatLogTime(raw) {
    var d = validTokenTime(raw);
    if (!d) { return "—"; }
    return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  }

  // formatBytes renders a byte count compactly; null/undefined yields "—".
  function formatBytes(n) {
    if (n == null || isNaN(n)) { return "—"; }
    if (n < 1024) { return n + "B"; }
    if (n < 1024 * 1024) { return (n / 1024).toFixed(1) + "KB"; }
    return (n / (1024 * 1024)).toFixed(1) + "MB";
  }

  // truncate caps a string to max characters, appending an ellipsis when cut.
  function truncate(s, max) {
    s = String(s == null ? "" : s);
    if (s.length <= max) { return s; }
    return s.slice(0, max) + "…";
  }

  function applyLogTabUI() {
    document.querySelectorAll("[data-logtab]").forEach(function (btn) {
      var active = btn.getAttribute("data-logtab") === logTab;
      btn.classList.toggle("active", active);
      btn.setAttribute("aria-selected", active ? "true" : "false");
      btn.setAttribute("aria-pressed", active ? "true" : "false");
    });
    var mountsPanel = el("log-mounts-panel");
    var auditPanel = el("log-audit-panel");
    if (mountsPanel) { mountsPanel.hidden = logTab !== "mounts"; }
    if (auditPanel) { auditPanel.hidden = logTab !== "audit"; }
  }

  function setLogTab(tab) {
    logTab = tab === "audit" ? "audit" : "mounts";
    applyLogTabUI();
    refreshLogs();
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
  var CHAT_STREAM_FRAME_MS = 32; // 实时正文最多约 30fps：长正文一次布局约 16ms，
                                 // 60fps 会把主线程吃满；仍按 rAF 对齐绘制帧。
  var CHAT_LIVE_PLAIN_AFTER_MS = 30; // 一次实时补丁超过该耗时就改为纯文本流式，
                                     // 终稿仍由服务端行按 Markdown 渲染。
  var CHAT_LIVE_MARKDOWN_MAX = 6000; // 超过该长度的实时正文直接走纯文本快路径，
                                     // 避免等到 Markdown 单帧已经超预算才降级。
  var CHAT_LIVE_CHUNK_CHARS = 1200; // 纯文本流式按行边界切块；旧块不被重写，
                                    // 屏外块由 content-visibility 跳过布局。
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

  // UX: 桌面端把会话级管理动作集中到右栏会话头，列表卡片只保留摘要。
  function syncChatHeaderActions() {
    var st = chatState;
    var menu = el("chat-more-menu");
    if (!st || !menu) { return; }
    var node = st.node, session = st.session;
    var canWeb = nodeHasWebUI(node);
    var canWindow = windowActionsSupported(node) && !isSubagentSession(session);
    menu.querySelector("[data-chat-detail]").hidden = false;
    menu.querySelector("[data-chat-web]").hidden = !canWeb;
    menu.querySelector("[data-chat-window-open]").hidden = !canWindow;
    menu.querySelector("[data-chat-window-resume]").hidden = !canWindow;
    menu.querySelector("[data-chat-window-close]").hidden = !canWindow;
    menu.querySelector("[data-chat-remove]").hidden = !node.stale;
  }

  /* ── 「下一条待处理」流转（孤立的审批清理循环）────────────────────
     面板一次只显示一条会话；逐个清掉待审批/待回答时，用户此前必须关闭面板、
     回列表、再找下一条。这两个函数把这条循环收进面板：头部常驻按钮 + 处理完
     一条后的流转 toast，目标都取自同一份 inboxItems（未读、actionable 的
     attention，按注意力
     优先级排序），并显式跳过当前面板会话。 */

  // pendingQueue 返回除当前面板会话外的待处理队列（未读、未离线）。
  // 排序在 inboxItems 的注意力优先级之上做一次稳定分组：能面板内直接完成的
  // 条目（有 screen+events；审批/提问还须有 input）排前，「下一条」因此总是
  // 优先落回面板；只能外部打开的条目仍保留在队列里（计数不撒谎）。
  function chatQueueRank(item) {
    if (!chatPanelSupported(item.node, item.session)) { return 1; }
    var att = item.session.attention;
    if ((att === "approval" || att === "question") && !chatCanInput(item.node)) { return 1; }
    return 0;
  }

  function pendingQueue() {
    var st = chatState;
    var items = inboxItems(lastNodes);
    if (st) {
      items = items.filter(function (item) {
        return !(item.node.node_id === st.node.node_id &&
          item.session.session_id === st.session.session_id);
      });
    }
    // Array.prototype.sort 在现代引擎是稳定的：同组内保持注意力优先级顺序。
    return items.sort(function (a, b) { return chatQueueRank(a) - chatQueueRank(b); });
  }

  // chatUpdateNextPending 刷新头部「下一条待处理」按钮：队列为空时隐藏，
  // 否则显示剩余条数。任何数据刷新（renderInbox）与面板开合都会走到这里。
  function chatUpdateNextPending() {
    var btn = el("chat-next-pending");
    if (!btn) { return; }
    var st = chatState;
    if (!st) { btn.hidden = true; return; }
    var queue = pendingQueue();
    if (!queue.length) { btn.hidden = true; return; }
    btn.hidden = false;
    var label = el("chat-next-pending-label");
    if (label) {
      label.textContent = queue.length > 1
        ? "下一条待处理（" + queue.length + "）"
        : "下一条待处理";
    }
    var tip = "下一条待处理：" + sessionLabelOf(queue[0].session) +
      " · " + nodeDisplayName(queue[0].node) +
      "（" + attentionLabel(queue[0].session.attention) + "）";
    btn.title = tip;
    btn.setAttribute("aria-label", tip);
  }

  // chatSwitchTo 在面板内切换到另一条会话：复用既有一对生命周期函数
  // （close 断流/释放 presence/归还焦点，open 全新连接），不产生第二套状态。
  // 目标节点无法站内打开时落回外部流程且不关闭当前面板（用户可自行决定）。
  function chatSwitchTo(node, session, opts) {
    if (!node || !session) { return; }
    if (!chatPanelSupported(node, session)) {
      openSession(node, session);
      return;
    }
    closeSessionChat();
    openSessionChat(node, session, opts);
  }

  // chatFocusOptsFor 目标带待审批/待回答时，切换后聚焦交互区（与卡片
  // 「审批 / 回答」按钮同一语义）；其余注意力只打开面板不抢焦点。
  function chatFocusOptsFor(session) {
    if (session && (session.attention === "approval" || session.attention === "question")) {
      return { focusInteractive: true };
    }
    return null;
  }

  // chatOfferNextPending 在审批/提问被处理掉的瞬间，把下一条待处理作为
  // 可点击流转呈现——「处理完这条，接着处理下一条」不必回列表找。
  function chatOfferNextPending() {
    var queue = pendingQueue();
    if (!queue.length) { chatUpdateNextPending(); return; }
    var next = queue[0];
    chatUpdateNextPending();
    pushToast("已处理 · 下一条待处理：" +
      nodeDisplayName(next.node) + " · " + sessionLabelOf(next.session) +
      "（" + attentionLabel(next.session.attention) + "）", null, {
      label: "打开",
      run: function () {
        chatSwitchTo(next.node, next.session, chatFocusOptsFor(next.session));
      },
    });
  }

  function chatEmptyLive() {
    return { streaming: false, done: false, plain: false, assistant: "", reasoning: "", tool: "", images: [] };
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

  function chatNotice(text, actions) {
    var box = el("chat-notice");
    if (!box) { return; }
    box.innerHTML = "";
    if (!text) { box.hidden = true; return; }
    box.hidden = false;
    var copy = document.createElement("span");
    copy.className = "chat-notice-text";
    copy.textContent = text;
    box.appendChild(copy);
    (actions || []).forEach(function (action) {
      if (!action || !action.label || typeof action.run !== "function") { return; }
      var btn = document.createElement("button");
      btn.type = "button";
      btn.className = "btn ghost sm";
      btn.textContent = action.label;
      btn.addEventListener("click", action.run);
      box.appendChild(btn);
    });
  }

  /* ── 审批 / 提问交互区（与节点原版 Web 客户端同契约）─────────────
     数据来自节点 SSE：connected.pending_approval / pending_question、
     approval_requested、question_asked；决议走 POST /web/api/input：
       approval        {type:"approval", request_id, allow}
       question_answer {type:"question_answer", question_id, answer}
     这样卡片上的「审批 / 回答」不必跳转到原版 Web。 */

  function chatInteractiveHint(text, kind) {
    var hint = el("ci-answer-hint");
    if (!hint) { return; }
    hint.textContent = text || "";
    hint.hidden = !text;
    hint.className = "chat-hint" + (kind ? " is-" + kind : "");
  }

  // chatRenderApprovalArgs 把审批参数渲染成「键: 值」行，而不是一整坨缩进 JSON：
  // 手机上的审批决策要一眼看清每个参数是什么；嵌套值保持 pretty-print，超长值
  // 截到 400 字符（悬停看全文）。非对象/解析失败的参数保持原文，绝不吞信息。
  function chatRenderApprovalArgs(target, rawArgs) {
    if (!target) { return; }
    target.textContent = "";
    var value = rawArgs;
    if (typeof value === "string") {
      try { value = JSON.parse(value); } catch (err) { value = rawArgs; }
    }
    if (!value || typeof value !== "object" || Array.isArray(value)) {
      setText(target, typeof value === "string" ? value : (JSON.stringify(value, null, 2) || ""));
      return;
    }
    Object.keys(value).forEach(function (key) {
      var row = document.createElement("div");
      row.className = "ci-arg-row";
      var k = document.createElement("span");
      k.className = "ci-arg-key";
      k.textContent = key;
      var v = document.createElement("span");
      v.className = "ci-arg-val";
      var item = value[key];
      var text = typeof item === "string" ? item : JSON.stringify(item, null, 2);
      if (text == null) { text = String(item); }
      if (text.length > 400) {
        v.textContent = text.slice(0, 400) + "…";
        v.title = text;
      } else {
        v.textContent = text;
      }
      row.appendChild(k);
      row.appendChild(v);
      target.appendChild(row);
    });
  }

  function chatRenderInteractive() {
    var box = el("chat-interactive");
    if (!box) { return; }
    var st = chatState;
    if (!st || (!st.pendingApproval && !st.pendingQuestion)) {
      box.hidden = true;
      return;
    }
    var approval = st.pendingApproval;
    var question = st.pendingQuestion;
    var kindEl = el("ci-kind");
    var titleEl = el("ci-title");
    var promptEl = el("ci-prompt");
    var detail = el("ci-detail");
    var toggle = el("ci-detail-toggle");
    var actions = el("ci-actions");
    var suggestions = el("ci-suggestions");
    var form = el("ci-answer-form");
    var answerInput = el("ci-answer-input");
    if (approval) {
      setText(kindEl, "待审批工具");
      setText(titleEl, approval.tool_name || "?");
      setText(promptEl, approval.prompt || "");
      chatRenderApprovalArgs(detail, approval.arguments);
      var args = approval.arguments;
      if (typeof args === "string") {
        try { args = JSON.parse(args); } catch (err) { /* keep raw string */ }
      }
      var hasArgs = !!(args && (typeof args !== "object" || Object.keys(args).length));
      if (detail) { detail.hidden = !hasArgs; }
      // 参数默认可见（审批决策的直接依据），标签跟随当前可见性——旧实现固定
      // 写「显示详情」而内容已展开，第一次点击标签与动作相反。
      if (toggle) { toggle.hidden = !hasArgs; toggle.textContent = (detail && detail.hidden) ? "显示详情" : "隐藏详情"; }
      if (actions) { actions.hidden = false; }
      if (el("ci-approve")) { el("ci-approve").disabled = false; }
      if (el("ci-deny")) { el("ci-deny").disabled = false; }
      if (suggestions) { suggestions.hidden = true; suggestions.innerHTML = ""; }
      if (form) { form.hidden = true; }
      chatInteractiveHint("", "");
    } else {
      setText(kindEl, "提问");
      // 交互条不暴露 question_id：id 只走数据面（question_answer 用它回执），
      // 界面保留「提问」+ 题面 + 建议/自由输入即可。
      setText(titleEl, "");
      setText(promptEl, question.prompt || "");
      if (detail) { detail.hidden = true; detail.textContent = ""; }
      if (toggle) { toggle.hidden = true; }
      if (actions) { actions.hidden = true; }
      var list = (question.suggestions || []);
      if (typeof list === "string") { list = [list]; }
      if (suggestions) {
        suggestions.innerHTML = "";
        suggestions.hidden = list.length === 0;
        list.forEach(function (text) {
          var btn = document.createElement("button");
          btn.type = "button";
          btn.className = "btn ghost sm ci-suggestion";
          btn.textContent = text;
          btn.addEventListener("click", function () { chatSendQuestionAnswer(text); });
          suggestions.appendChild(btn);
        });
      }
      if (form) { form.hidden = false; }
      if (answerInput && st.focusInteractive) {
        try { answerInput.focus(); } catch (err) { /* focus unsupported */ }
      }
      chatInteractiveHint("", "");
    }
    st.focusInteractive = false;
    box.hidden = false;
  }

  function chatPostInput(payload) {
    var st = chatState;
    if (!st) { return Promise.reject(new Error("面板已关闭")); }
    return api("/n/" + encodeURIComponent(st.node.node_id) + "/web/api/input", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (body) {
        return { ok: res.ok, status: res.status, body: body || {} };
      });
    });
  }

  function chatSendApprovalDecision(allow) {
    var st = chatState;
    if (!st || !st.pendingApproval) { return; }
    var requestID = st.pendingApproval.request_id;
    if (!requestID) { notify("审批请求缺少 request_id，无法提交", "error"); return; }
    var approveBtn = el("ci-approve");
    var denyBtn = el("ci-deny");
    if (approveBtn) { approveBtn.disabled = true; }
    if (denyBtn) { denyBtn.disabled = true; }
    chatInteractiveHint("已提交，等待节点确认…", "");
    chatPostInput({ type: "approval", request_id: requestID, allow: !!allow }).then(function (r) {
      var body = r.body || {};
      if (body.status === "resolved") {
        chatNotice("");
        chatInteractiveHint("", "");
      } else {
        chatInteractiveHint(body.reason || ("提交失败（" + (body.status || r.status) + "）"), "error");
        if (approveBtn) { approveBtn.disabled = false; }
        if (denyBtn) { denyBtn.disabled = false; }
      }
    }).catch(function (err) {
      chatInteractiveHint("提交失败：" + err, "error");
      if (approveBtn) { approveBtn.disabled = false; }
      if (denyBtn) { denyBtn.disabled = false; }
    });
  }

  function chatSendQuestionAnswer(answer) {
    var st = chatState;
    if (!st || !st.pendingQuestion) { return; }
    var text = String(answer == null ? "" : answer).trim();
    if (!text) { chatInteractiveHint("请输入回答", "error"); return; }
    var questionID = st.pendingQuestion.question_id;
    if (!questionID) { notify("提问缺少 question_id，无法提交", "error"); return; }
    var submit = el("ci-answer-submit");
    if (submit) { submit.disabled = true; }
    chatInteractiveHint("已提交，等待节点确认…", "");
    chatPostInput({ type: "question_answer", question_id: questionID, answer: text }).then(function (r) {
      var body = r.body || {};
      if (body.status === "resolved") {
        chatInteractiveHint("", "");
        var input = el("ci-answer-input");
        if (input) { input.value = ""; }
      } else {
        chatInteractiveHint(body.reason || ("提交失败（" + (body.status || r.status) + "）"), "error");
      }
    }).catch(function (err) {
      chatInteractiveHint("提交失败：" + err, "error");
    }).finally(function () {
      if (submit) { submit.disabled = false; }
    });
  }

  function chatToggleInteractiveDetail() {
    var detail = el("ci-detail");
    var toggle = el("ci-detail-toggle");
    if (!detail || !toggle) { return; }
    detail.hidden = !detail.hidden;
    toggle.textContent = detail.hidden ? "显示详情" : "隐藏详情";
  }

  (function bindChatInteractive() {
    var approve = el("ci-approve");
    if (approve) { approve.addEventListener("click", function () { chatSendApprovalDecision(true); }); }
    var deny = el("ci-deny");
    if (deny) { deny.addEventListener("click", function () { chatSendApprovalDecision(false); }); }
    var toggle = el("ci-detail-toggle");
    if (toggle) { toggle.addEventListener("click", chatToggleInteractiveDetail); }
    var form = el("ci-answer-form");
    if (form) {
      form.addEventListener("submit", function (e) {
        e.preventDefault();
        var input = el("ci-answer-input");
        chatSendQuestionAnswer(input ? input.value : "");
      });
    }
    var answer = el("ci-answer-input");
    if (answer) {
      answer.addEventListener("keydown", function (e) {
        if (e.key !== "Enter" || e.shiftKey) { return; }
        if (e.isComposing || e.keyCode === 229) { return; }
        e.preventDefault();
        chatSendQuestionAnswer(answer.value);
      });
    }
  })();

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
    var wasBusy = st.busy;
    st.busy = !!busy;
    if (st.busy && !st.busyTimer) {
      st.busyTimer = setInterval(function () {
        if (chatState && chatState.busy) { chatFetchScreen(); }
      }, CHAT_BUSY_POLL_MS);
    } else if (!st.busy && st.busyTimer) {
      clearInterval(st.busyTimer);
      st.busyTimer = null;
    }
    // 活跃回合的「最后一块推理/工具自动展开」是忙闲状态的呈现，不应依赖下一份
    // 快照恰好变化才生效（相同快照会被 chatApplyWindow 跳过）。只在 false→true
    // 的边沿扫描一次，避免每轮轮询重复走整份 DOM。
    if (!wasBusy && st.busy) {
      var host = el("chat-list");
      if (host) { chatApplyLiveTail(host, true); }
    }
  }

  function chatAutoGrow() {
    var input = el("chat-input");
    // jsdom 没有布局引擎（scrollHeight === 0），此时跳过同步。
    if (!input || !input.scrollHeight) { return; }
    input.style.height = "auto";
    input.style.height = Math.min(input.scrollHeight, 160) + "px";
  }

  // ── 微信式会话壳：列表 ↔ 会话流 ──────────────────────────────
  // 桌面端列表与会话流同屏双栏（微信电脑版），手机端列表整页、点开滑入会话
  // 流整页并用返回键回列表（微信手机版）。#chat-modal 仍复用原来的数据面，
  // 只是不再走 openModal：不加 scrim、不设 inert、不加焦点陷阱。
  var imChatHistoryPushed = false;
  var imSuppressHistoryBack = false;

  function imViewport() {
    return (window.matchMedia && window.matchMedia("(max-width: 860px)").matches) ? "mobile" : "desktop";
  }

  function imChatIsOpen() {
    var panel = el("chat-modal");
    return !!(panel && !panel.hidden);
  }

  // First visit to the sessions view on desktop should not leave a dead empty
  // pane. Select the first visible, chat-capable session without marking it
  // read and without stealing focus. The operator can still close it and browse.
  function autoSelectFirstSession() {
    if (sessionsAutoSelected) { return; }
    if (currentView() !== "sessions" || imViewport() !== "desktop" || imChatIsOpen()) { return; }
    var items = allSessions(lastNodes).filter(sessionMatches).sort(sessionSorter);
    for (var i = 0; i < items.length; i++) {
      var item = items[i];
      if (isSubagentSession(item.session) || !chatPanelSupported(item.node, item.session)) { continue; }
      sessionsAutoSelected = true;
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
    var st = chatState;
    var key = (st && st.node && st.session)
      ? st.node.node_id + "|" + st.session.session_id
      : "";
    var cards = document.querySelectorAll(".session-card");
    for (var i = 0; i < cards.length; i++) {
      cards[i].classList.toggle("is-active", !!key && cards[i].getAttribute("data-draft-key") === key);
    }
  }

  function imOpenChatPanel(noFocus) {
    var panel = el("chat-modal");
    if (!panel) { return; }
    panel.hidden = false;
    var emptyPane = el("im-chat-empty");
    if (emptyPane) { emptyPane.hidden = true; }
    syncImActiveCard();
    if (imViewport() === "mobile") {
      document.body.classList.add("im-chat-open");
      // 会话流在手机上表现为一个"页面"：压一条历史记录，系统/手势返回即回列表。
      if (!imChatHistoryPushed) {
        try { window.history.pushState({ imChat: true }, ""); imChatHistoryPushed = true; } catch (e) {}
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
    var panel = el("chat-modal");
    if (!panel) { return; }
    panel.hidden = true;
    document.body.classList.remove("im-chat-open");
    var emptyPane = el("im-chat-empty");
    if (emptyPane) { emptyPane.hidden = false; }
    if (imChatHistoryPushed) {
      imChatHistoryPushed = false;
      if (!imSuppressHistoryBack) {
        // 回退掉打开时压入的那条记录，让浏览历史保持干净。
        try { window.history.back(); } catch (e) {}
      }
    }
    syncImActiveCard();
  }

  function openSessionChat(node, session, opts) {
    if (!node || !session) { return; }
    sessionsAutoSelected = true;
    var wantInteractive = !!(opts && opts.focusInteractive);
    var skipMarkRead = !!(opts && opts.skipMarkRead);
    var skipFocus = !!(opts && opts.skipFocus);
    if (node.stale) {
      pushToast("该节点当前离线，暂时无法读取会话消息。", "error", {
        label: "查看详情",
        run: function () { openSessionDetail(node, session); },
      });
      return;
    }
    if (!chatPanelSupported(node, session)) {
      if (!nodeHasWebUI(node)) {
        pushToast(webUnavailableText(node), "error", {
          label: "查看详情",
          run: function () { openSessionDetail(node, session); },
        });
      } else {
        pushToast("该会话暂时不能在站内打开，可尝试原版 Web。", "error", {
          label: "打开原版 Web",
          run: function () { openSession(node, session); },
        });
      }
      return;
    }
    var canInput = chatCanInput(node);
    setText(el("chat-modal-title"), sessionLabelOf(session));
    setText(el("chat-modal-sub"),
      nodeDisplayName(node) + " · " + (node.kind || "agent") +
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
      screenBusy: false,
      lastScreenAt: 0,
      screenETag: "",
      renderFrame: 0,
      lastLiveRenderAt: 0,
      bottomPin: 0,
      scrollGuardUntil: 0,
      programmaticScrollTop: 0,
      followHoldUntil: Date.now() + 800,
      bottomPinTimer: 0,
      toolMeasurePending: false,
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
      // 审批 / 提问交互态：由 SSE connected / approval_requested /
      // question_asked 填充；卡片「审批 / 回答」进入时聚焦交互区。
      pendingApproval: null,
      pendingQuestion: null,
      focusInteractive: wantInteractive,
    };
    syncChatHeaderActions();
    // 面板打开时先收起上一次的交互条（状态刚被替换，避免残留上一条审批）。
    chatRenderInteractive();
    if (!skipMarkRead) { markRead(node, session); }
    // 面板打开 = 「正在看这个会话」：抑制该会话的推送；关面板时释放。
    rememberPresence(node, session);
    chatRenderDynamic();
    chatRenderSegments(el("chat-segs"), null);
    imOpenChatPanel(skipFocus);
    chatFetchScreen(CHAT_PAGE_LIMIT);
    chatLoadStatusBar();
    chatConnect();
    chatUpdateNextPending();
  }

  // ── 数据面：快照 / 渲染 ──────────────────────────────────────

  // chatFetchScreen 拉取权威窗口。older=true 时按 win.start 向前翻页，结果前插。
  // 每次请求带代次（seq），过期响应直接丢弃——快速重连 + 节流刷新会并发多份。
  function chatFetchScreen(limit, opts) {
    var st = chatState;
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
      chatUpdateOlder();
    }
    st.seq++;
    var seq = st.seq;
    var url = "/n/" + encodeURIComponent(st.node.node_id) +
      "/web/api/screen?format=json&compact=1&msg_limit=" +
      (o.older ? CHAT_OLDER_LIMIT : (limit || CHAT_PAGE_LIMIT));
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
    api(url, requestOpts).then(function (res) {
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
      if (chatState !== st || st.seq !== seq) { return; }
      st.lastScreenAt = Date.now();
      if (result.etag) { st.screenETag = result.etag; }
      if (result.notModified) {
        // 304 说明权威内容没变；但首次打开时列表可能尚未完成排版，工具行测量
        // 会留待下一轮。这里只补这一项局部工作，不再遍历/重写整份消息列表。
        if (st.toolMeasurePending) {
          var host = el("chat-list");
          if (host) { chatRefreshToolToggles(host); }
        }
        return;
      }
      chatApplyWindow(result.data, o.older);
    }).catch(function (err) {
      st.olderBusy = false;
      chatUpdateOlder();
      if (chatState !== st || st.seq !== seq) { return; }
      st.lastScreenAt = Date.now();
      chatFail("暂时无法加载这段会话。",
        "GET " + url + "\n" + (err && err.message ? err.message : String(err)));
    }).finally(function () {
      if (!o.older) { st.screenBusy = false; }
    });
  }

  function chatRetryLoad() {
    var st = chatState;
    if (!st) { return; }
    st.failed = false;
    st.available = true;
    st.lastScreenAt = 0;
    st.screenETag = "";
    chatNotice("");
    chatPlaceholder("正在重新加载消息…");
    chatSetStatus("重新加载…", "");
    chatFetchScreen(CHAT_PAGE_LIMIT);
  }

  // chatFail 只在本次打开里提示一次失败：持续失败的刷新不该反复弹 toast。
  function chatFail(userMessage, reason) {
    var st = chatState;
    if (!st) { return; }
    chatSetStatus("读取失败", "down");
    if (st.failed) { return; }
    st.failed = true;
    var message = userMessage || "暂时无法加载这段会话。";
    if (!st.messages.length) { chatPlaceholder(message); }
    var diagnostics = sessionDiagnostics(st.node, st.session, reason);
    chatNotice(message + " 你可以重试，或打开原版 Web；诊断信息可复制给管理员。", [
      { label: "重试", run: chatRetryLoad },
      { label: "复制诊断", run: function () { copyText(diagnostics); } },
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
    var st = chatState;
    if (!st) { return; }
    if (!data || data.available === false) {
      var empty = el("chat-list");
      if (empty) { empty.innerHTML = ""; }
      st.rows = {};
      st.available = false;
      chatPlaceholder("暂无可显示的消息。");
      var unavailableReason = data && data.reason ? data.reason : "transcript 未就绪，或会话刚被切换";
      var unavailableDiagnostics = sessionDiagnostics(st.node, st.session, "screen unavailable: " + unavailableReason);
      chatNotice("这个会话暂时没有可读取的消息。可以重试，或打开原版 Web 查看完整界面。", [
        { label: "重试", run: chatRetryLoad },
        { label: "复制诊断", run: function () { copyText(unavailableDiagnostics); } },
      ]);
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
    chatConfirmPending();
    // 权威行已经接管这一回合：实时气泡退场，避免同一段内容渲染两次。
    if (st.live.done) { st.live = chatEmptyLive(); }
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
      var host = el("chat-list");
      if (host) { chatRefreshToolToggles(host); }
    }
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
    var roleText = roleLabelOf(role) + (o.pending ? " · 发送中" : "");
    if (o.collapse && role === "tool") {
      // 工具行的折叠控件长在抬头行里：先写角色文本再追加控件（setText 只在文本
      // 变化时写入，不会反复清掉控件）。
      setText(roleEl, roleText);
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
      setText(roleEl, roleText);
    }

    if (o.collapse && role === "reasoning") {
      setText(row.__reasonSummary, roleLabelOf(role) + "过程");
      setText(row.__reasonBody, content || "");
    } else if (o.collapse && role === "tool") {
      setText(row.__toolOutput, content || "");
    } else if (o.plain) {
      // Streaming text is transient and can grow to tens of KB. Re-parsing
      // Markdown + replacing innerHTML every frame makes each update a layout
      // stall; keep the live bubble plain and render Markdown once on the final
      // (non-streaming) frame / authoritative snapshot.
      bubble.classList.remove("md");
      bubble.__mdSrc = null;
      bubble.__mdOn = false;
      chatSetStreamingText(bubble, content || "");
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
      var livePlain = st.live.plain || st.live.done ||
        st.live.assistant.length > CHAT_LIVE_MARKDOWN_MAX;
      rows.push(chatRowElement(st, "live:assistant", "assistant", st.live.assistant,
        { streaming: st.live.streaming, plain: livePlain, images: liveImages }));
    }
    if (st.live.tool) {
      rows.push(chatRowElement(st, "live:tool", "tool", st.live.tool, { plain: true }));
    }
    return rows;
  }

  // chatRender 是全量收敛路径：只在窗口/消息集合变化后调用。它负责结构行、
  // 折叠测量和用户展开账本的恢复。
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
    desired = desired.concat(chatLiveRows(st));
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
    if (st.toolMeasurePending) { chatRefreshToolToggles(host); }
    if (Object.keys(chatUserExpanded).length) {
      Object.keys(st.rows).forEach(function (key) {
        if (!chatUserExpanded[key]) { return; }
        var row = st.rows[key];
        if (row.__reasonEl && !row.__reasonEl.open) { row.__reasonEl.open = true; }
        var out = row.__toolOutput;
        if (out && out.classList.contains("chat-collapsed")) { chatSetToolExpanded(row, true); }
      });
    }
    chatApplyLiveTail(host, st.busy);
    // 翻页前插：按新增高度补偿滚动位置，阅读不被顶走。
    if (st.scrollRestore != null) {
      chatSetScrollTop(st, host, host.scrollHeight - st.scrollRestore);
      st.scrollRestore = null;
    }
    if (st.follow) { chatSetScrollTop(st, host, host.scrollHeight); }
    chatSyncJump();
    chatPinBottomAfterLayout();
  }

  // chatPatchLive 是流式热路径：历史行集合未变时只补实时气泡与待确认回显，
  // 不重新遍历权威消息内容、不扫描折叠状态，也不重写未变化的 DOM。
  function chatPatchLive() {
    var st = chatState;
    var host = el("chat-list");
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
    reconcileChildren(host, desired);
    var keep = {};
    desired.forEach(function (row) { keep[row.__key] = true; });
    Object.keys(st.rows).forEach(function (key) {
      if (!keep[key]) { delete st.rows[key]; }
    });
    if (!desired.length) { chatPlaceholder("暂无消息。"); }
    if (st.follow) { chatSetScrollTop(st, host, host.scrollHeight); }
    chatSyncJump();
  }

  // chatScheduleRender 按浏览器绘制帧合并高频增量：同一帧内多个 token 只补一次
  // 实时气泡。这里不用固定 50ms 定时器——那会让文字按 20fps 成段跳变，且回调
  // 可能落在布局帧之外，主观上更卡。
  function chatScheduleRender() {
    var st = chatState;
    if (!st || st.renderFrame) { return; }
    var nowFn = (window.performance && window.performance.now)
      ? function () { return window.performance.now(); }
      : function () { return Date.now(); };
    var run = function () {
      st.renderFrame = 0;
      if (chatState !== st) { return; }
      var now = nowFn();
      if (now - st.lastLiveRenderAt < CHAT_STREAM_FRAME_MS) {
        chatScheduleRender();
        return;
      }
      st.lastLiveRenderAt = now;
      var renderStart = nowFn();
      chatPatchLive();
      // Adaptive downgrade: short responses keep live Markdown. If one patch
      // (parse + layout) crosses the frame budget, switch this turn to the
      // cheap committed-prefix text path; the final server row is still Markdown.
      if (chatState === st && st.live.streaming && !st.live.plain &&
          nowFn() - renderStart > CHAT_LIVE_PLAIN_AFTER_MS) {
        st.live.plain = true;
        chatScheduleRender();
      }
    };
    if (typeof window.requestAnimationFrame === "function") {
      st.renderFrame = window.requestAnimationFrame(run);
    } else {
      st.renderFrame = setTimeout(run, 16);
    }
  }

  function chatCancelRender() {
    var st = chatState;
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
    var st = chatState;
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
      if (key && chatUserExpanded[key]) { continue; }
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
    var st = chatState;
    var host = el("chat-list");
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
    var st = chatState;
    var host = el("chat-list");
    if (!st || !host || !st.follow || st.bottomPin) { return; }
    var tries = 0;
    var pin = function () {
      st.bottomPin = 0;
      if (chatState !== st || !st.follow) { return; }
      chatSetScrollTop(st, host, host.scrollHeight);
      chatSyncJump();
      tries++;
      // Intrinsic sizes settle over a few frames on a long transcript. Re-pin
      // briefly so the initial open (and a backwards paging settle) lands on the
      // true bottom without adding work to the per-token path.
      if (tries < 8) { st.bottomPin = window.requestAnimationFrame(pin); }
    };
    if (typeof window.requestAnimationFrame === "function") {
      st.bottomPin = window.requestAnimationFrame(pin);
    } else {
      st.bottomPin = setTimeout(pin, 16);
    }
    if (!st.bottomPinTimer) {
      st.bottomPinTimer = setTimeout(function () {
        st.bottomPinTimer = 0;
        if (chatState !== st || !st.follow) { return; }
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
    closeSessionMenus(false);
    // 先断流再清状态：否则关闭过程中的帧会写进正在被清空的 DOM。
    chatDisconnect();
    chatState = null;
    chatRenderInteractive();
    chatUpdateNextPending();
    delete presenceTargets[st.node.node_id + "|" + st.session.session_id];
    sendPresence();
    var host = el("chat-list");
    if (host) { host.innerHTML = ""; }
    imCloseChatPanel();
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
    chatCancelRender();
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
        // connected 同时带当前挂起的审批/提问（中途打开面板不会丢交互入口）。
        st.pendingApproval = d.pending_approval || null;
        st.pendingQuestion = d.pending_question || null;
        chatRenderInteractive();
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
        chatScheduleRefresh();
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
        chatScheduleRefresh();
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
          chatScheduleRefresh();
        }
        break;
      case "session_interrupted":
        st.live = chatEmptyLive();
        chatSetBusy(false);
        chatSetStatus("已停止", "down");
        chatScheduleRender();
        chatScheduleRefresh();
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
        st.pendingApproval = d;
        st.pendingQuestion = null;
        chatNotice("");
        chatRenderInteractive();
        break;
      case "question_asked":
        st.pendingQuestion = d;
        st.pendingApproval = null;
        chatNotice("");
        chatRenderInteractive();
        break;
      case "approval_resolved":
        // 只有本面板确实展示过这条待办时才提示「下一条」；重复/迟到的帧只刷新按钮。
        var hadApproval = !!st.pendingApproval;
        st.pendingApproval = null;
        chatRenderInteractive();
        chatScheduleRefresh();
        if (hadApproval) { chatOfferNextPending(); } else { chatUpdateNextPending(); }
        break;
      case "question_answered":
        var hadQuestion = !!st.pendingQuestion;
        st.pendingQuestion = null;
        chatRenderInteractive();
        chatScheduleRefresh();
        if (hadQuestion) { chatOfferNextPending(); } else { chatUpdateNextPending(); }
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
      nodeDisplayName(st.node) + " · " + (st.node.kind || "agent") +
      " · " + stateLabelOf(st.session));
    syncChatHeaderActions();
    markRead(st.node, st.session);
    rememberPresence(st.node, st.session);
  }

  // chatResetForSession：/new、/resume、/load 会让 transcript 换人，旧行必须整块
  // 丢弃——否则上一条会话的内容会串进新会话的对话区。
  function chatResetForSession() {
    var st = chatState;
    if (!st) { return; }
    chatCancelRender();
    var host = el("chat-list");
    if (host) { host.innerHTML = ""; }
    st.messages = [];
    st.msgStart = undefined;
    st.win = null;
    st.rows = {};
    st.pending = [];
    st.live = chatEmptyLive();
    st.failed = false;
    st.screenETag = "";
    st.lastScreenAt = 0;
    st.toolMeasurePending = false;
    st.followHoldUntil = Date.now() + 800;
    // 换会话后旧的审批/提问一律失效（节点的 pending 由新一轮 connected 重报）。
    st.pendingApproval = null;
    st.pendingQuestion = null;
    chatRenderInteractive();
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
      notify("节点「" + nodeDisplayName(st.node) + "」离线，无法发送。", "error");
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
          readAttention[key] = Date.now();
          return;
        }
        if (!seenAttention[key]) { fresh.push({ node: node, session: session }); }
      });
    });
    var seeded = attentionSeeded;
    Object.keys(readAttention).forEach(function (k) {
      if (!current[k]) { delete readAttention[k]; }
    });
    // attentionKey 清理后落盘：服务端已消解的条目不留在存储里无限增长。
    saveReadAttention();
    seenAttention = current;
    attentionSeeded = true;
    if (!seeded) { return; }
    fresh.forEach(function (item) { showAttentionToast(item.node, item.session); });
  }

  function showAttentionToast(node, session) {
    // The Tauri shell cannot use Web Push (WebView2 rejects subscribe()).
    // Deliver the OS notification from the same fresh-attention edge that
    // drives the in-page toast; gateway presence handles server-side dedup.
    nativeAttentionNotification(node, session);
    // The "open" affordance is a real button, so the toast is usable without a
    // mouse; clicking anywhere on the toast is no longer the only entry point.
    pushToast(
      nodeDisplayName(node) + " · " + sessionLabelOf(session) + "：" +
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

  /* ── Tauri native notifications ─────────────────────────────── */

  // The desktop/mobile shell loads this page in a WebView. WebView2 exposes
  // PushManager but rejects subscribe(), so system notifications must go
  // through Tauri's native notification bridge.
  function nativeNotificationApi() {
    return (window.__TAURI__ && window.__TAURI__.notification) || null;
  }

  function readNativeNotifyPref() {
    try { return localStorage.getItem("aicli-gw-native-notify"); } catch (e) { return null; }
  }

  function writeNativeNotifyPref(enabled) {
    try { localStorage.setItem("aicli-gw-native-notify", enabled ? "1" : "0"); } catch (e) {}
  }

  // Default: follow the OS permission. An explicit local "0" disables app-side
  // sends without trying to revoke a system permission from JS.
  function nativeNotificationsEnabled(granted) {
    var pref = readNativeNotifyPref();
    if (pref === "0") { return false; }
    if (pref === "1") { return true; }
    return !!granted;
  }

  function enableNativeNotifications() {
    var api = nativeNotificationApi();
    if (!api) { return Promise.resolve(); }
    return Promise.resolve(api.isPermissionGranted()).then(function (granted) {
      if (granted) { return "granted"; }
      return api.requestPermission();
    }).then(function (state) {
      if (state !== "granted") {
        throw new Error("应用通知权限未授予（当前：" + state + "）。请在系统设置中允许通知后重试。");
      }
      writeNativeNotifyPref(true);
    });
  }

  function disableNativeNotifications() {
    // The OS permission cannot be revoked from JS; this only disables sends.
    writeNativeNotifyPref(false);
    return Promise.resolve();
  }

  function sendNativeNotification(title, body, extra) {
    var api = nativeNotificationApi();
    if (!api) { return Promise.resolve(false); }
    return Promise.resolve(api.isPermissionGranted()).then(function (granted) {
      if (!granted || !nativeNotificationsEnabled(granted)) { return false; }
      return Promise.resolve(api.sendNotification({
        title: title,
        body: body,
        extra: extra || {},
      })).then(function () { return true; });
    }).catch(function () { return false; });
  }

  function nativeAttentionNotification(node, session) {
    if (!nativeNotificationApi()) { return; }
    // 策略门（GET /api/notify/policy）：服务端 notify-policy.json 里被关闭的
    // kind / 免打扰 / 会话级静音在这里拦截，避免只改网关配置却仍被 App 弹窗
    // 打扰。策略未加载时保持原行为（fail-open），防止网络失败吞掉全部通知。
    if (!notifyPolicyAllowsAttention(session.attention, session.session_id)) { return; }
    var extra = {
      node_id: node.node_id,
      session_id: session.session_id,
      attention: session.attention,
    };
    // 点击深链：aicli 节点必须是 /n/{node}/web/?session=…（route allowlist 只
    // 放行 /web），runtime-server 才是 /n/{node}/。缺 url 时桌面壳会自行拼
    // /n/{node}/?session=… 并被反代 403（"route not allowed"），所以这里显式
    // 带上控制台同源的绝对 URL。
    try {
      extra.url = new URL(sessionOpenTarget(node, session.session_id), location.origin).toString();
    } catch (err) { /* URL 构造失败时退回桌面壳的 node/session 兜底拼装 */ }
    sendNativeNotification(
      nodeDisplayName(node) + " · " + sessionLabelOf(session),
      attentionLabel(session.attention),
      extra
    );
  }

  /* ────────────── 通知设置页 / 通知策略（GET-PUT API） ────────── */

  // 词表兜底：正常取 /api/notify/policy 的 meta.kinds（与网关登记表同源）。
  var NOTIFY_KIND_FALLBACK = [
    { kind: "approval", label: "待审批", priority: "high" },
    { kind: "question", label: "待回答", priority: "high" },
    { kind: "error", label: "出错", priority: "high" },
    { kind: "completed", label: "已完成", priority: "normal" },
    { kind: "waiting", label: "等待中", priority: "normal" },
  ];
  var NOTIFY_PRIORITY_OPTIONS = [
    { value: "low", label: "全部通知", hint: "所有类型都通知，包括未来新增的低优先级消息。" },
    { value: "normal", label: "重要通知（默认）", hint: "待处理、已完成和等待中都会通知。" },
    { value: "high", label: "仅待处理", hint: "仅待审批、待回答和出错；完成/等待状态不打扰。" },
  ];
  var NOTIFY_KIND_DESCRIPTIONS = {
    approval: "工具请求审批，需要你允许或拒绝。",
    question: "会话向你提问，等待你的答复。",
    error: "执行失败，需要排查或重试。",
    completed: "一轮执行完成，可以回来查看结果。",
    waiting: "等待子任务或外部条件继续。",
  };

  function notifyNormalizeKind(kind) {
    return kind === "completion" ? "completed" : String(kind || "");
  }

  function notifyKindPriority(kind) {
    kind = notifyNormalizeKind(kind);
    if (kind === "approval" || kind === "question" || kind === "error") { return "high"; }
    return "normal";
  }

  function notifyKindDescription(kind) {
    kind = notifyNormalizeKind(kind);
    return NOTIFY_KIND_DESCRIPTIONS[kind] || "收到该类事件时通知。";
  }

  function notifyPriorityOption(value) {
    for (var i = 0; i < NOTIFY_PRIORITY_OPTIONS.length; i++) {
      if (NOTIFY_PRIORITY_OPTIONS[i].value === value) { return NOTIFY_PRIORITY_OPTIONS[i]; }
    }
    return NOTIFY_PRIORITY_OPTIONS[1];
  }

  function notifyPriorityLabel(value) {
    return notifyPriorityOption(value).label;
  }

  function notifyPriorityRank(priority) {
    if (priority === "high") { return 2; }
    if (priority === "normal") { return 1; }
    return 0;
  }

  function notifyParseQuietHours(list) {
    var out = [];
    (Array.isArray(list) ? list : []).forEach(function (item) {
      var m = /^\s*(\d{1,2}):(\d{2})\s*-\s*(\d{1,2}):(\d{2})\s*$/.exec(String(item || ""));
      if (!m) { return; }
      var start = parseInt(m[1], 10) * 60 + parseInt(m[2], 10);
      var end = parseInt(m[3], 10) * 60 + parseInt(m[4], 10);
      if (start === end) { return; }
      out.push([start, end]);
    });
    return out;
  }

  function notifyInQuietHours(ranges, now) {
    var minutes = now.getHours() * 60 + now.getMinutes();
    for (var i = 0; i < ranges.length; i++) {
      var start = ranges[i][0], end = ranges[i][1];
      if (start < end) {
        if (minutes >= start && minutes < end) { return true; }
      } else if (minutes >= start || minutes < end) {
        return true;
      }
    }
    return false;
  }

  // notifyPolicyAllowsAttention 是控制台侧的策略判定（与服务端
  // notify.Preference 的字段级覆盖同序：global → session → channel）。
  // 策略未加载时返回 true，保持既有行为，避免一次网络失败把原生通知
  // 全部静默。
  function notifyPolicyAllowsAttention(kind, sessionID) {
    if (!kind) { return false; }
    if (!notifyPolicy) { return true; }
    var global = notifyPolicy.global || {};
    if (global.muted === true) { return false; }
    var session = (notifyPolicy.sessions || {})[sessionID] || {};
    if (session.muted === true) { return false; }
    var priority = notifyKindPriority(kind);
    var minPriority = session.min_priority || global.min_priority || "low";
    if (notifyPriorityRank(priority) < notifyPriorityRank(minPriority)) { return false; }
    var kinds = session.allow_kinds != null ? session.allow_kinds : global.allow_kinds;
    if (kinds != null) {
      var allowed = false;
      for (var i = 0; i < kinds.length; i++) {
        if (notifyNormalizeKind(kinds[i]) === notifyNormalizeKind(kind)) { allowed = true; break; }
      }
      if (!allowed) { return false; }
    }
    var quiet = session.quiet_hours != null ? session.quiet_hours : global.quiet_hours;
    if (priority !== "high" && notifyInQuietHours(notifyParseQuietHours(quiet), new Date())) {
      return false;
    }
    return true;
  }

  function cloneJSON(value) {
    try { return JSON.parse(JSON.stringify(value || {})); } catch (e) { return {}; }
  }

  function notifyPolicyKindsMeta() {
    if (notifyPolicyMeta && Array.isArray(notifyPolicyMeta.kinds) && notifyPolicyMeta.kinds.length) {
      return notifyPolicyMeta.kinds;
    }
    return NOTIFY_KIND_FALLBACK;
  }

  function notifyPolicyChannelRows() {
    return (notifyPolicyMeta && Array.isArray(notifyPolicyMeta.channels)) ? notifyPolicyMeta.channels : [];
  }

  function notifyCanonicalJSON(value) {
    if (value === null || typeof value !== "object") { return JSON.stringify(value); }
    if (Array.isArray(value)) {
      return "[" + value.map(notifyCanonicalJSON).join(",") + "]";
    }
    var keys = Object.keys(value).sort();
    return "{" + keys.map(function (key) {
      return JSON.stringify(key) + ":" + notifyCanonicalJSON(value[key]);
    }).join(",") + "}";
  }

  function notifyPolicyIsDirty() {
    if (notifyQuietError) { return true; }
    if (!notifyPolicyDraft || !notifyPolicy) { return false; }
    return notifyCanonicalJSON(notifyPolicyDraft) !== notifyPolicyBaseline;
  }

  function notifySetQuietError(message) {
    notifyQuietError = String(message || "");
    var node = el("notify-quiet-error");
    if (node) {
      node.textContent = notifyQuietError;
      node.hidden = !notifyQuietError;
    }
  }

  function notifyRefreshDirty() {
    var bar = el("notify-savebar");
    var text = el("notify-dirty");
    var dot = el("notify-dirty-dot");
    var save = el("notify-save");
    var dirty = notifyPolicyIsDirty();
    var blocked = !!notifyQuietError;
    if (bar) {
      bar.classList.toggle("is-dirty", dirty || blocked);
      bar.classList.toggle("is-invalid", blocked);
    }
    if (dot) { dot.hidden = !(dirty || blocked); }
    if (text) {
      if (!notifyPolicyLoaded && !notifyPolicyDraft) {
        text.textContent = "策略尚未加载";
      } else if (blocked) {
        text.textContent = notifyQuietError;
      } else if (notifyPolicySaving) {
        text.textContent = "正在保存…";
      } else if (dirty) {
        text.textContent = "有未保存的修改";
      } else {
        text.textContent = "所有修改已保存";
      }
    }
    if (save) { save.disabled = !dirty || blocked || notifyPolicySaving; }
  }

  function notifyDiscardPolicyDraft() {
    if (!notifyPolicy) { return; }
    notifyPolicyDraft = cloneJSON(notifyPolicy);
    notifySetQuietError("");
    notifyRefreshDirty();
  }

  function notifyParseClock(value) {
    var match = /^(\d{1,2}):(\d{2})$/.exec(String(value || "").trim());
    if (!match) { return ""; }
    var hour = parseInt(match[1], 10);
    var minute = parseInt(match[2], 10);
    if (hour > 23 || minute > 59) { return ""; }
    return (hour < 10 ? "0" : "") + hour + ":" + (minute < 10 ? "0" : "") + minute;
  }

  function notifyQuietRowsFromPolicy() {
    var raw = notifyPolicyDraft && notifyPolicyDraft.global && notifyPolicyDraft.global.quiet_hours;
    if (!Array.isArray(raw)) { return []; }
    var rows = [];
    raw.forEach(function (item) {
      var match = /^\s*(\d{1,2}:\d{2})\s*-\s*(\d{1,2}:\d{2})\s*$/.exec(String(item || ""));
      if (!match) { return; }
      var start = notifyParseClock(match[1]);
      var end = notifyParseClock(match[2]);
      if (start && end) { rows.push({ start: start, end: end }); }
    });
    return rows;
  }

  function notifyQuietRangeKey(start, end) {
    return start + "-" + end;
  }

  function notifyCollectQuietRows(showError) {
    var host = el("notify-quiet-rows");
    var invalid = null;
    if (!host) { return true; }
    var rows = Array.prototype.slice.call(host.querySelectorAll(".notify-quiet-row"));
    var ranges = [];
    var error = "";
    rows.forEach(function (row, index) {
      var startInput = row.querySelector("[data-quiet-start]");
      var endInput = row.querySelector("[data-quiet-end]");
      var start = notifyParseClock(startInput && startInput.value);
      var end = notifyParseClock(endInput && endInput.value);
      if (!start || !end) {
        if (!error) {
          error = "请同时填写第 " + (index + 1) + " 个免打扰时段的开始和结束时间。";
          invalid = !start ? startInput : endInput;
        }
        return;
      }
      if (start === end) {
        if (!error) {
          error = "免打扰时段的开始和结束时间不能相同。";
          invalid = startInput;
        }
        return;
      }
      var key = notifyQuietRangeKey(start, end);
      if (ranges.indexOf(key) !== -1) {
        if (!error) {
          error = "存在重复的免打扰时段，请合并相同区间。";
          invalid = startInput;
        }
        return;
      }
      ranges.push(key);
    });
    if (showError) { notifySetQuietError(error); }
    if (!error) {
      notifyPolicyDraft.global = notifyPolicyDraft.global || {};
      if (ranges.length || notifyQuietTouched) {
        notifyPolicyDraft.global.quiet_hours = ranges;
      }
    }
    notifyQuietInvalidEl = invalid;
    notifyRefreshDirty();
    return !error;
  }

  function loadNotifyPolicy(force) {
    if (notifyPolicyLoading) { return notifyPolicyLoading; }
    if (notifyPolicyLoaded && !force) { return Promise.resolve(notifyPolicy); }
    notifyPolicyLoading = apiJSON("/api/notify/policy").then(function (r) {
      if (r.res.status === 401) { setAuthed(false); return notifyPolicy; }
      if (!r.res.ok || !r.body || !r.body.policy) {
        throw new Error("HTTP " + r.res.status + (r.body && r.body.error ? " " + r.body.error : ""));
      }
      notifyPolicy = r.body.policy;
      notifyPolicyMeta = r.body.meta || {};
      notifyPolicyDraft = cloneJSON(notifyPolicy);
      notifyPolicyBaseline = notifyCanonicalJSON(notifyPolicy);
      notifyPolicyLoaded = true;
      notifyPolicySaving = false;
      notifyQuietTouched = false;
      notifyQuietInvalidEl = null;
      notifySetQuietError("");
      renderNotifyPolicy();
      return notifyPolicy;
    }).catch(function (err) {
      if (currentView() === "notifications") {
        notify("通知策略读取失败：" + (err && err.message ? err.message : err), "error");
      }
      notifyRefreshDirty();
      return notifyPolicy;
    });
    var pending = notifyPolicyLoading;
    pending.finally(function () { if (notifyPolicyLoading === pending) { notifyPolicyLoading = null; } });
    return pending;
  }

  function setNotifyStatus(text) {
    var status = el("notify-status");
    if (!status) { return; }
    status.textContent = text || "";
    status.hidden = !text;
  }

  function renderNotifyPolicy() {
    if (!notifyPolicyDraft) { notifyRefreshDirty(); return; }
    var global = notifyPolicyDraft.global || {};
    var enabled = el("notify-global-enabled");
    if (enabled) { enabled.checked = global.muted !== true; }
    var min = el("notify-min-priority");
    if (min) { min.value = global.min_priority || "normal"; }
    renderNotifyPriorityHint();
    renderNotifyQuietRows();
    var source = el("notify-policy-source");
    if (source) {
      var meta = notifyPolicyMeta || {};
      var labels = { file: "策略文件", legacy: "旧偏好回退", "default": "默认值" };
      source.textContent = labels[meta.source] || "策略";
      source.title = meta.path || "";
      source.hidden = false;
    }
    renderNotifyPolicyKinds();
    renderNotifyPolicyChannels();
    renderNotifyPolicySessions();
    notifyRefreshDirty();
  }

  function renderNotifyPriorityHint() {
    var hint = el("notify-priority-hint");
    var select = el("notify-min-priority");
    if (!hint || !select) { return; }
    hint.textContent = notifyPriorityOption(select.value).hint;
  }

  function renderNotifyQuietRows() {
    var host = el("notify-quiet-rows");
    if (!host) { return; }
    host.textContent = "";
    notifyQuietRowsFromPolicy().forEach(function (range) {
      notifyAppendQuietRow(range);
    });
  }

  function notifyAppendQuietRow(range) {
    var host = el("notify-quiet-rows");
    if (!host) { return; }
    range = range || { start: "", end: "" };
    var row = document.createElement("div");
    row.className = "notify-quiet-row";
    var start = document.createElement("input");
    start.type = "time";
    start.className = "filter-input notify-quiet-time";
    start.value = range.start || "";
    start.setAttribute("data-quiet-start", "");
    start.setAttribute("aria-label", "免打扰开始时间");
    var separator = document.createElement("span");
    separator.className = "notify-quiet-sep";
    separator.textContent = "至";
    separator.setAttribute("aria-hidden", "true");
    var end = document.createElement("input");
    end.type = "time";
    end.className = "filter-input notify-quiet-time";
    end.value = range.end || "";
    end.setAttribute("data-quiet-end", "");
    end.setAttribute("aria-label", "免打扰结束时间");
    var remove = document.createElement("button");
    remove.type = "button";
    remove.className = "btn ghost sm notify-quiet-remove";
    remove.textContent = "移除";
    remove.setAttribute("aria-label", "移除此免打扰时段");
    var changed = function () {
      notifyQuietTouched = true;
      notifyCollectQuietRows(true);
    };
    start.addEventListener("input", changed);
    start.addEventListener("change", changed);
    end.addEventListener("input", changed);
    end.addEventListener("change", changed);
    remove.addEventListener("click", function () {
      notifyQuietTouched = true;
      row.remove();
      notifyCollectQuietRows(true);
    });
    row.appendChild(start);
    row.appendChild(separator);
    row.appendChild(end);
    row.appendChild(remove);
    host.appendChild(row);
    return start;
  }

  function renderNotifyPolicyKinds() {
    var host = el("notify-kinds");
    if (!host || !notifyPolicyDraft) { return; }
    host.textContent = "";
    var global = notifyPolicyDraft.global || {};
    var allow = global.allow_kinds;
    notifyPolicyKindsMeta().forEach(function (spec) {
      var label = document.createElement("label");
      label.className = "notify-kind";
      var input = document.createElement("input");
      input.type = "checkbox";
      input.setAttribute("data-notify-kind", spec.kind);
      if (allow == null) {
        input.checked = true;
      } else {
        input.checked = allow.some(function (kind) {
          return notifyNormalizeKind(kind) === notifyNormalizeKind(spec.kind);
        });
      }
      input.addEventListener("change", function () {
        notifyPolicyDraft.global = notifyPolicyDraft.global || {};
        notifyPolicyDraft.global.allow_kinds = collectCheckedNotifyKinds();
        notifyRefreshDirty();
      });
      var copy = document.createElement("span");
      copy.className = "notify-kind-copy";
      var title = document.createElement("strong");
      title.textContent = spec.label;
      var description = document.createElement("span");
      description.className = "notify-kind-desc";
      description.textContent = notifyKindDescription(spec.kind);
      copy.appendChild(title);
      copy.appendChild(description);
      label.appendChild(input);
      label.appendChild(copy);
      host.appendChild(label);
    });
  }

  function collectCheckedNotifyKinds() {
    var out = [];
    document.querySelectorAll("#notify-kinds input[data-notify-kind]").forEach(function (input) {
      if (input.checked) { out.push(input.getAttribute("data-notify-kind")); }
    });
    return out;
  }

  function renderNotifyPolicyChannels() {
    var host = el("notify-channels");
    if (!host || !notifyPolicyDraft) { return; }
    host.textContent = "";
    var rows = notifyPolicyChannelRows();
    if (!rows.length) {
      var empty = document.createElement("p");
      empty.className = "empty";
      empty.textContent = "后端未报告可用通道。";
      host.appendChild(empty);
      return;
    }
    rows.forEach(function (row) {
      var label = document.createElement("label");
      label.className = "notify-channel" + (row.available ? "" : " is-unavailable");
      var input = document.createElement("input");
      input.type = "checkbox";
      input.disabled = !row.available;
      var entry = (notifyPolicyDraft.channels || {})[row.name] || {};
      input.checked = row.available && entry.muted !== true;
      input.addEventListener("change", function () {
        notifyPolicyDraft.channels = notifyPolicyDraft.channels || {};
        if (input.checked) {
          delete notifyPolicyDraft.channels[row.name];
        } else {
          notifyPolicyDraft.channels[row.name] = { muted: true };
        }
        notifyRefreshDirty();
      });
      var copy = document.createElement("span");
      copy.className = "notify-channel-copy";
      var title = document.createElement("strong");
      title.textContent = row.label;
      var status = document.createElement("span");
      status.className = "notify-channel-desc";
      if (!row.available) {
        var badge = document.createElement("span");
        badge.className = "badge notify-channel-badge";
        badge.textContent = "未配置";
        title.appendChild(badge);
        status.textContent = "需在网关启动参数中配置后可用。";
        label.title = "该通道尚未在网关配置";
      } else {
        status.textContent = entry.muted === true ? "已关闭：不会向该通道发送通知。" : "接收网关外发通知。";
      }
      copy.appendChild(title);
      copy.appendChild(status);
      label.appendChild(input);
      label.appendChild(copy);
      host.appendChild(label);
    });
  }

  function notifyPolicySessionRows() {
    var rows = [];
    (lastNodes || []).forEach(function (node) {
      (node.sessions || []).forEach(function (session) {
        if (session && session.session_id) { rows.push({ node: node, session: session }); }
      });
    });
    rows.sort(function (a, b) {
      var na = nodeDisplayName(a.node).localeCompare(nodeDisplayName(b.node));
      if (na !== 0) { return na; }
      var ia = String(a.node.node_id || "").localeCompare(String(b.node.node_id || ""));
      if (ia !== 0) { return ia; }
      var pa = attentionPriority[a.session.attention] != null ? attentionPriority[a.session.attention] : 9;
      var pb = attentionPriority[b.session.attention] != null ? attentionPriority[b.session.attention] : 9;
      if (pa !== pb) { return pa - pb; }
      return String(a.session.title || "").localeCompare(String(b.session.title || ""));
    });
    return rows;
  }

  function notifySessionOverrideActive(entry) {
    return !!(entry && (entry.muted != null || entry.min_priority || entry.allow_kinds != null));
  }

  function notifySessionSearchText(item) {
    return [
      nodeDisplayName(item.node),
      item.node && item.node.node_id,
      item.session && item.session.session_id,
      item.session && item.session.title,
      item.session && item.session.preview,
    ].join(" ").toLowerCase();
  }

  function notifyFilteredSessionRows(rows) {
    var query = String(notifySessionQuery || "").trim().toLowerCase();
    var sessions = notifyPolicyDraft.sessions || {};
    return rows.filter(function (item) {
      if (notifySessionOnlyCustom && !notifySessionOverrideActive(sessions[item.session.session_id])) {
        return false;
      }
      return !query || notifySessionSearchText(item).indexOf(query) !== -1;
    });
  }

  function renderNotifyPolicySessions() {
    var host = el("notify-session-list");
    if (!host || !notifyPolicyDraft) { return; }
    // 正在编辑时不要被 SSE 刷新打断焦点/滚动。
    if (host.contains(document.activeElement)) { return; }
    host.textContent = "";
    var allRows = notifyPolicySessionRows();
    var filtered = notifyFilteredSessionRows(allRows);
    var rows = filtered.slice(0, 200);
    var search = el("notify-session-search");
    if (search && search.value !== notifySessionQuery) { search.value = notifySessionQuery; }
    var onlyCustom = el("notify-session-only-custom");
    if (onlyCustom) { onlyCustom.checked = notifySessionOnlyCustom; }
    var empty = el("notify-session-empty");
    if (empty) {
      empty.hidden = rows.length > 0;
      empty.textContent = allRows.length ? "没有匹配的会话。" : "暂无已知会话；会话上报后这里会出现可单独静音或提高门槛的条目。";
    }
    var count = el("notify-session-count");
    if (count) {
      count.textContent = filtered.length === allRows.length
        ? allRows.length + " 个会话"
        : rows.length + " / " + allRows.length + " 个会话";
      count.hidden = allRows.length === 0;
    }
    var sessions = notifyPolicyDraft.sessions || {};
    var group = null;
    var groupHost = null;
    rows.forEach(function (item) {
      var sessionID = item.session.session_id;
      var override = sessions[sessionID] || {};
      var nodeID = item.node.node_id || nodeDisplayName(item.node);
      if (!group || group.getAttribute("data-node-id") !== nodeID) {
        group = document.createElement("section");
        group.className = "notify-session-group";
        group.setAttribute("data-node-id", nodeID);
        var head = document.createElement("div");
        head.className = "notify-session-group-head";
        head.textContent = nodeDisplayName(item.node);
        groupHost = document.createElement("div");
        groupHost.className = "list notify-session-group-list";
        group.appendChild(head);
        group.appendChild(groupHost);
        host.appendChild(group);
      }
      var row = document.createElement("div");
      row.className = "notify-session-row";
      row.setAttribute("data-session-id", sessionID);
      row.classList.toggle("is-overridden", notifySessionOverrideActive(override));
      var name = document.createElement("span");
      name.className = "notify-session-name";
      name.textContent = sessionLabelOf(item.session);
      name.title = sessionID;
      row.appendChild(name);
      var custom = document.createElement("span");
      custom.className = "badge notify-session-custom";
      custom.textContent = "已调整";
      custom.hidden = !notifySessionOverrideActive(override);
      row.appendChild(custom);

      var mute = document.createElement("label");
      mute.className = "check-line";
      var muteInput = document.createElement("input");
      muteInput.type = "checkbox";
      muteInput.checked = override.muted === true;
      muteInput.addEventListener("change", function () {
        updateNotifySessionOverride(sessionID, "muted", muteInput.checked ? true : null);
        notifyMarkSessionRow(row, custom);
      });
      var muteText = document.createElement("span");
      muteText.textContent = "静音";
      mute.appendChild(muteInput);
      mute.appendChild(muteText);
      row.appendChild(mute);

      var select = document.createElement("select");
      select.className = "select sm";
      select.setAttribute("aria-label", "该会话的通知范围：" + sessionLabelOf(item.session));
      [["", "跟随全局"]].concat(NOTIFY_PRIORITY_OPTIONS.map(function (opt) {
        return [opt.value, opt.label];
      })).forEach(function (opt) {
        var option = document.createElement("option");
        option.value = opt[0];
        option.textContent = opt[1];
        select.appendChild(option);
      });
      select.value = override.min_priority || "";
      select.addEventListener("change", function () {
        updateNotifySessionOverride(sessionID, "min_priority", select.value || null);
        notifyMarkSessionRow(row, custom);
      });
      row.appendChild(select);
      groupHost.appendChild(row);
    });
  }

  function notifyMarkSessionRow(row, badge) {
    if (!row || !notifyPolicyDraft) { return; }
    var sessionID = row.getAttribute("data-session-id");
    var entry = (notifyPolicyDraft.sessions || {})[sessionID];
    var active = notifySessionOverrideActive(entry);
    row.classList.toggle("is-overridden", active);
    if (badge) { badge.hidden = !active; }
  }

  function updateNotifySessionOverride(sessionID, field, value) {
    notifyPolicyDraft.sessions = notifyPolicyDraft.sessions || {};
    var entry = notifyPolicyDraft.sessions[sessionID] || {};
    if (value == null) {
      delete entry[field];
    } else {
      entry[field] = value;
    }
    if (entry.muted == null && !entry.min_priority && entry.allow_kinds == null) {
      delete notifyPolicyDraft.sessions[sessionID];
    } else {
      notifyPolicyDraft.sessions[sessionID] = entry;
    }
    notifyRefreshDirty();
  }

  function saveNotifyPolicy() {
    if (!notifyPolicyDraft || !notifyPolicyLoaded || notifyPolicySaving) { return; }
    if (!notifyCollectQuietRows(true)) {
      if (notifyQuietInvalidEl) { notifyQuietInvalidEl.focus(); }
      return;
    }
    if (!notifyPolicyIsDirty()) {
      notifyRefreshDirty();
      return;
    }
    var payload = cloneJSON(notifyPolicyDraft);
    payload.version = 1;
    notifyPolicySaving = true;
    notifyRefreshDirty();
    setNotifyStatus("保存中…");
    apiJSON("/api/notify/policy", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    }).then(function (r) {
      if (r.res.status === 401) {
        notifyPolicySaving = false;
        setNotifyStatus("");
        setAuthed(false);
        return;
      }
      if (!r.res.ok) {
        throw new Error((r.body && r.body.error) || ("HTTP " + r.res.status));
      }
      notifyPolicy = r.body.policy;
      notifyPolicyMeta = r.body.meta || notifyPolicyMeta;
      notifyPolicyDraft = cloneJSON(notifyPolicy);
      notifyPolicyBaseline = notifyCanonicalJSON(notifyPolicy);
      notifyPolicyLoaded = true;
      notifyPolicySaving = false;
      notifyQuietTouched = false;
      notifyQuietInvalidEl = null;
      notifySetQuietError("");
      setNotifyStatus("");
      renderNotifyPolicy();
      notify("通知策略已保存并立即生效", "ok");
    }).catch(function (err) {
      notifyPolicySaving = false;
      setNotifyStatus("");
      notifyRefreshDirty();
      notify("保存策略失败：" + (err && err.message ? err.message : err), "error");
    });
  }

  function resetNotifyPolicyDraft() {
    if (notifyPolicyIsDirty()) {
      var reset = true;
      try {
        reset = window.confirm("恢复默认值会覆盖当前未保存的修改，确定继续吗？");
      } catch (e) { /* headless/jsdom: proceed */ }
      if (!reset) { return; }
    }
    var defaults = (notifyPolicyMeta && notifyPolicyMeta.defaults) || {
      muted: false,
      min_priority: "normal",
      allow_kinds: ["approval", "question", "error", "completed", "waiting"],
    };
    notifyPolicyDraft = { version: 1, global: cloneJSON(defaults), sessions: {}, channels: {} };
    notifyPolicyDraft.global = notifyPolicyDraft.global || {};
    if (!Array.isArray(notifyPolicyDraft.global.quiet_hours)) {
      notifyPolicyDraft.global.quiet_hours = [];
    }
    notifyQuietTouched = true;
    notifyQuietInvalidEl = null;
    notifySetQuietError("");
    renderNotifyPolicy();
    notify("已恢复默认值，点击「保存策略」后生效", "ok");
  }

  // pushEnvironment explains whether Web Push can work here and, when it
  // cannot, why. The reason is surfaced in the toggle tooltip and in error
  // toasts instead of silently hiding the button: on plain HTTP or in an iOS
  // Safari tab the user previously saw no notification affordance at all.
  function pushEnvironment() {
    if (nativeNotificationApi()) { return { ok: true, native: true }; }
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
    var nativeApi = nativeNotificationApi();
    if (nativeApi) {
      btn.title = "使用 Tauri 系统原生通知";
      Promise.resolve(nativeApi.isPermissionGranted()).then(function (granted) {
        var enabled = nativeNotificationsEnabled(granted);
        setPushLabel(enabled ? "关闭应用通知" : "开启应用通知");
        if (test) { test.hidden = !enabled; }
        if (!granted) {
          btn.title = "尚未授予应用通知权限：点击开启并在系统设置中允许通知。";
        }
      }).catch(function () {
        setPushLabel("开启应用通知");
        if (test) { test.hidden = true; }
      });
      return;
    }
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
    if (nativeNotificationApi()) { return enableNativeNotifications(); }
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
    if (nativeNotificationApi()) { return disableNativeNotifications(); }
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
    syncTopbarMenuLabel();
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

  // Narrow screens keep one topbar action: this menu absorbs the theme and
  // refresh buttons. Logout deliberately stays only in the sidebar.
  function syncTopbarMenuLabel() {
    var item = el("topbar-theme");
    if (!item) { return; }
    var label = el("theme-label");
    item.textContent = "主题：" + (label && label.textContent ? label.textContent : "跟随系统");
  }

  function topbarMenuItems() {
    var menu = el("topbar-menu");
    if (!menu) { return []; }
    return Array.prototype.slice.call(menu.querySelectorAll('[role="menuitem"]'));
  }

  function closeTopbarMenu(restoreFocus) {
    var menu = el("topbar-menu");
    var trigger = el("topbar-more");
    if (!menu || menu.hidden) { return; }
    var hadFocus = !!(document.activeElement && menu.contains(document.activeElement));
    menu.hidden = true;
    if (trigger) {
      trigger.setAttribute("aria-expanded", "false");
      if (restoreFocus && hadFocus) { trigger.focus(); }
    }
  }

  var topbarMore = el("topbar-more");
  var topbarMenu = el("topbar-menu");
  if (topbarMore && topbarMenu) {
    topbarMore.addEventListener("click", function (e) {
      e.stopPropagation();
      var willOpen = topbarMenu.hidden;
      closeSessionMenus(false);
      if (!willOpen) {
        closeTopbarMenu(false);
        return;
      }
      syncTopbarMenuLabel();
      topbarMenu.hidden = false;
      topbarMore.setAttribute("aria-expanded", "true");
      var items = topbarMenuItems();
      if (items.length) { items[0].focus(); }
    });
    topbarMenu.addEventListener("keydown", function (e) {
      var items = topbarMenuItems();
      if (!items.length) { return; }
      var idx = items.indexOf(document.activeElement);
      if (e.key === "ArrowDown") {
        e.preventDefault();
        e.stopPropagation();
        items[(idx + 1 + items.length) % items.length].focus();
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        e.stopPropagation();
        items[(idx - 1 + items.length) % items.length].focus();
      } else if (e.key === "Home") {
        e.preventDefault();
        e.stopPropagation();
        items[0].focus();
      } else if (e.key === "End") {
        e.preventDefault();
        e.stopPropagation();
        items[items.length - 1].focus();
      } else if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        closeTopbarMenu(true);
      }
    });
    document.addEventListener("click", function (e) {
      if (e.target && e.target.closest && e.target.closest("#topbar-more, #topbar-menu")) { return; }
      closeTopbarMenu(false);
    });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { closeTopbarMenu(true); }
    });
    window.addEventListener("resize", function () {
      if (window.innerWidth > 860) { closeTopbarMenu(false); }
    });
    var topbarTheme = el("topbar-theme");
    if (topbarTheme) {
      topbarTheme.addEventListener("click", function () {
        cycleTheme();
        syncTopbarMenuLabel();
        closeTopbarMenu(true);
      });
    }
    var topbarRefresh = el("topbar-refresh");
    if (topbarRefresh) {
      topbarRefresh.addEventListener("click", function () {
        var refresh = el("refresh");
        if (refresh) { refresh.click(); }
        closeTopbarMenu(true);
      });
    }
  }

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
    if (notifyPolicyIsDirty()) {
      var leave = true;
      try {
        leave = window.confirm("通知设置有未保存的修改，退出登录后会丢失。确定退出吗？");
      } catch (e) { /* headless/jsdom: proceed */ }
      if (!leave) { return; }
    }
    api("/api/auth/logout", { method: "POST" }).then(function () { setAuthed(false); });
  }
  el("logout").addEventListener("click", doLogout);
  el("logout-top").addEventListener("click", doLogout);

  el("refresh").addEventListener("click", function () { refreshCurrentView(); });
  // Logs view: tabs, manual refresh, follow toggle and server-side filters.
  document.querySelectorAll("[data-logtab]").forEach(function (btn) {
    btn.addEventListener("click", function () { setLogTab(btn.getAttribute("data-logtab")); });
  });
  var logsFollow = el("logs-follow");
  if (logsFollow) {
    logsFollow.addEventListener("click", function () {
      logFollow = !logFollow;
      logsFollow.textContent = "自动刷新：" + (logFollow ? "开" : "关");
      logsFollow.setAttribute("aria-pressed", logFollow ? "true" : "false");
      if (logFollow) {
        startLogTimer();
        refreshLogs();
      } else {
        stopLogTimer();
      }
    });
  }
  var logSlugEl = el("log-slug");
  if (logSlugEl) {
    logSlugEl.addEventListener("change", function () {
      if (logTab === "mounts") { loadMountLogs(); }
    });
  }
  var logStatusEl = el("log-status");
  if (logStatusEl) {
    logStatusEl.addEventListener("change", function () {
      if (logTab === "mounts") { renderMountLogs(logMountEntries); }
    });
  }
  var logTypeEl = el("log-type");
  if (logTypeEl) {
    logTypeEl.addEventListener("change", function () {
      if (logTab === "audit") { loadAuditLogs(); }
    });
  }
  var logQueryEl = el("log-query");
  if (logQueryEl) {
    var logQueryTimer = null;
    logQueryEl.addEventListener("input", function () {
      clearTimeout(logQueryTimer);
      logQueryTimer = setTimeout(function () {
        if (logTab === "audit") { loadAuditLogs(); }
      }, 300);
    });
  }
  // UX-06: 离线快照清理改为「描边按钮 + 站内确认」，并在确认文案里
  // 列出受影响节点；不再是实心红按钮 + 原生 confirm。
  var clearStale = el("clear-stale");
  if (clearStale) {
    clearStale.addEventListener("click", function () {
      var staleNodes = lastNodes.filter(function (n) { return n.stale; });
      if (!staleNodes.length) { return; }
      var preview = staleNodes.slice(0, 5).map(function (n) {
        var hbMs = n.last_heartbeat ? new Date(n.last_heartbeat).getTime() : NaN;
        return "• " + nodeDisplayName(n) +
          (isNaN(hbMs) ? "" : "（最后活跃 " + new Date(hbMs).toLocaleString() + "）");
      }).join("\n");
      if (staleNodes.length > 5) {
        preview += "\n• 另有 " + (staleNodes.length - 5) + " 个节点";
      }
      closeSessionMenus(false);
      confirmDestructive({
        title: "清除全部离线快照？",
        message: "将删除 " + staleNodes.length + " 个离线节点及其会话条目。\n\n" +
          preview + "\n\nagent 重新上线会自动重新注册。",
        trigger: clearStale,
        confirmText: "清除全部",
        onConfirm: function () {
          return api("/api/nodes?stale=1", { method: "DELETE" })
            .then(function (res) {
              if (res.status === 401) { setAuthed(false); return; }
              if (!res.ok) { throw new Error("HTTP " + res.status); }
              notify("已清除 " + staleNodes.length + " 个离线快照节点", "ok");
              loadNodes();
            });
        }
      });
    });
  }

  var pushBusy = false;
  el("push-toggle").addEventListener("click", function () {
    if (pushBusy) { return; }
    if (nativeNotificationApi()) {
      var nativeDisabling = el("push-toggle-label").textContent.indexOf("关闭") !== -1;
      pushBusy = true;
      setPushLabel(nativeDisabling ? "关闭中…" : "开启中…");
      var nativeBtn = el("push-toggle");
      nativeBtn.disabled = true;
      Promise.resolve(nativeDisabling ? disableNativeNotifications() : enableNativeNotifications())
        .then(function () {
          refreshPushState();
          notify(nativeDisabling ? "已关闭应用通知" : "已开启应用通知", "ok");
        })
        .catch(function (err) {
          refreshPushState();
          notify("应用通知操作失败：" + (err && err.message ? err.message : "请检查系统通知权限"), "error");
        })
        .finally(function () {
          pushBusy = false;
          nativeBtn.disabled = false;
        });
      return;
    }
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
      if (nativeNotificationApi()) {
        var nativeIdle = pushTest.textContent;
        pushTest.disabled = true;
        pushTest.textContent = "发送中…";
        sendNativeNotification("aicli-gateway", "应用通知测试成功").then(function (ok) {
          notify(ok ? "已发送应用通知" : "应用通知未开启或权限未授予", ok ? "ok" : "error");
        }).finally(function () {
          pushTest.disabled = false;
          pushTest.textContent = nativeIdle;
        });
        return;
      }
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
  // cards, Enter/Space opens the same inline session as a card click, and
  // c/o/a act on the focused card (站内会话 / 原版 Web / 审批·回答).
  // Session-level management actions live in the right-hand chat header.
  document.addEventListener("keydown", function (e) {
    // 任一模态打开时不接管快捷键：会话面板里有输入框与滚动区，全局的 "/" 聚焦、
    // ↑/↓ 走卡片在这里都不该生效（另一条 typing 守卫只挡输入类元素）。
    if (imChatPageOpen() || modalIsOpen("session-modal") ||
        modalIsOpen("token-modal") || modalIsOpen("mount-token-modal")) { return; }
    var target = e.target;
    if (target && target.closest && target.closest(".s-menu")) { return; }
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
    // 卡片动作键：必须无修饰键（Ctrl+C 复制 / Ctrl+O 打开文件不能被劫持），
    // 且只作用于获得焦点的卡片——先 j/k 选中、再 c/o/a 动作。
    if (current && target === current && !e.ctrlKey && !e.metaKey && !e.altKey) {
      var action = e.key === "c" || e.key === "o" || e.key === "a";
      var cardItem = action ? findSessionByKey(current.getAttribute("data-draft-key")) : null;
      if (cardItem) {
        e.preventDefault();
        if (e.key === "c") {
          // 与行点击/主按钮同一条主路径（无 Web 路由时落到详情）。
          openSessionFromCard(cardItem.node, cardItem.session);
          return;
        }
        if (e.key === "o") {
          openSession(cardItem.node, cardItem.session);
          return;
        }
        // a = 审批 / 回答：与卡片 data-attend 按钮同一可用性门槛；不可用时
        // 不动作（按钮此时同样是隐藏的，不给"为什么没反应"的歧义）。
        var att = cardItem.session.attention;
        if ((att === "approval" || att === "question") &&
            chatPanelSupported(cardItem.node, cardItem.session) &&
            chatCanInput(cardItem.node)) {
          openSessionChat(cardItem.node, cardItem.session, { focusInteractive: true });
        }
        return;
      }
    }
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
      openSessionFromCard(found.node, found.session);
    }
  });

  // Token modal
  el("issue-token").addEventListener("click", function () {
    openModal("token-modal");
    el("device-name").focus();
  });
  var deviceSearch = el("device-search");
  if (deviceSearch) {
    deviceSearch.addEventListener("input", function () {
      deviceQuery = deviceSearch.value || "";
      renderDevices(lastDevices);
    });
  }
  var deviceStatus = el("device-status-filter");
  if (deviceStatus) {
    deviceStatus.addEventListener("change", function () {
      deviceStatusFilter = deviceStatus.value || "";
      renderDevices(lastDevices);
    });
  }
  el("token-modal-close").addEventListener("click", function () {
    closeModal("token-modal");
  });
  el("token-modal").addEventListener("click", function (e) {
    if (e.target === el("token-modal")) { closeModal("token-modal"); }
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !el("token-modal").hidden) { closeModal("token-modal"); }
  });

  // MCP mount token modal
  el("mount-token-modal-close").addEventListener("click", function () {
    closeMountTokenModal();
  });
  el("mount-token-modal").addEventListener("click", function (e) {
    if (e.target === el("mount-token-modal")) { closeMountTokenModal(); }
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !el("mount-token-modal").hidden) {
      closeMountTokenModal();
    }
  });
  el("mount-token-refresh").addEventListener("click", loadMountTokens);
  var mountTokenTTL = el("mount-token-ttl");
  if (mountTokenTTL) { mountTokenTTL.addEventListener("change", syncMountTokenTTL); }
  el("mount-token-form").addEventListener("submit", function (e) {
    e.preventDefault();
    if (!mountTokenTarget) { return; }
    var name = el("mount-token-name").value.trim();
    var ttlChoice = el("mount-token-ttl").value;
    var body = { name: name };
    if (ttlChoice === "never") {
      body.never_expires = true;
    } else if (ttlChoice === "custom") {
      var days = parseInt(el("mount-token-days").value, 10);
      if (!days || days < 1 || days > 3650) {
        notify("有效期请输入 1–3650 天", "error");
        el("mount-token-days").focus();
        return;
      }
      body.ttl_seconds = days * 86400;
    } else {
      body.ttl_seconds = parseInt(ttlChoice, 10) || 0;
    }
    var submit = el("mount-token-form").querySelector('button[type="submit"]');
    if (submit) { submit.disabled = true; }
    apiJSON("/api/mounts/" + encodeURIComponent(mountTokenTarget.slug) + "/tokens", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }).then(function (r) {
      if (r.res.status === 401) { setAuthed(false); return; }
      if (!r.res.ok || !r.body || !r.body.token) {
        throw new Error("HTTP " + r.res.status +
          (r.body && r.body.message ? " " + r.body.message : ""));
      }
      showMountToken(r.body.token);
      el("mount-token-name").value = "";
      loadMountTokens();
      loadMounts();
      notify("已签发 MCP 令牌（仅显示一次）", "ok");
    }).catch(function (err) {
      notify("签发失败：" + err, "error");
    }).then(function () {
      if (submit) { submit.disabled = false; }
    });
  });

  // Session detail modal
  el("session-modal-close").addEventListener("click", closeSessionDetail);
  el("session-modal-done").addEventListener("click", closeSessionDetail);
  el("session-modal-open").addEventListener("click", function () {
    if (!detailTarget) { return; }
    var target = detailTarget;
    closeSessionDetail();
    openSessionFromCard(target.node, target.session);
  });
  el("sd-copy").addEventListener("click", function (e) {
    if (detailTarget) { copyText(detailTarget.session.session_id, e.currentTarget); }
  });
  el("sd-copy-diagnostics").addEventListener("click", function (e) {
    if (detailTarget) {
      copyText(sessionDiagnostics(detailTarget.node, detailTarget.session, "detail"), e.currentTarget);
    }
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
    // 手机端返回键：会话流整页 → 会话列表。
    var backBtn = el("chat-back");
    if (backBtn) { backBtn.addEventListener("click", closeSessionChat); }
    // 会话级管理动作统一放在右栏会话头；列表卡片只负责选择会话。
    var chatMore = el("chat-more");
    var chatMenu = el("chat-more-menu");
    if (chatMore && chatMenu) {
      bindCardMenu(chatMore.parentElement, chatMenu);
      chatMenu.querySelector("[data-chat-detail]").addEventListener("click", function () {
        if (!chatState) { return; }
        closeSessionMenus(false);
        openSessionDetail(chatState.node, chatState.session);
      });
      chatMenu.querySelector("[data-chat-web]").addEventListener("click", function () {
        if (!chatState) { return; }
        closeSessionMenus(false);
        openSession(chatState.node, chatState.session);
      });
      [
        { selector: "[data-chat-window-open]", kind: "open-terminal", done: "已排队：该节点下一个心跳会在本机新终端里打开此会话" },
        { selector: "[data-chat-window-resume]", kind: "resume", done: "已排队：该节点下一个心跳会恢复此会话" },
      ].forEach(function (action) {
        chatMenu.querySelector(action.selector).addEventListener("click", function () {
          if (!chatState) { return; }
          closeSessionMenus(false);
          requestWindowAction(chatState.node, chatState.session, action.kind)
            .then(function () { notify(action.done, "ok"); })
            .catch(function (err) { notify("窗口动作失败：" + err, "error"); });
        });
      });
      chatMenu.querySelector("[data-chat-window-close]").addEventListener("click", function () {
        if (!chatState) { return; }
        var st = chatState;
        closeSessionMenus(false);
        confirmDestructive({
          title: "关闭窗口进程？",
          message: "关闭该会话在本机的窗口进程？\n（等同在窗口里敲 /exit：会话会正常保存。）",
          trigger: chatMore,
          confirmText: "关闭窗口",
          onConfirm: function () {
            return requestWindowAction(st.node, st.session, "close")
              .then(function () { notify("已排队：该节点下一个心跳会关闭此窗口", "ok"); });
          }
        });
      });
      chatMenu.querySelector("[data-chat-remove]").addEventListener("click", function () {
        if (!chatState) { return; }
        var st = chatState;
        closeSessionMenus(false);
        confirmDestructive({
          title: "移除会话条目？",
          message: "从注册表移除该会话条目？\n（仅清除网关侧快照，不影响 agent 上的会话数据。）",
          trigger: chatMore,
          confirmText: "移除",
          onConfirm: function () {
            return api("/api/nodes/" + encodeURIComponent(st.node.node_id) +
                "/sessions/" + encodeURIComponent(st.session.session_id), { method: "DELETE" })
              .then(function () { notify("会话条目已移除", "ok"); closeSessionChat(); loadNodes(); });
          }
        });
      });
    }
    el("chat-jump-latest").addEventListener("click", function () { chatScrollToBottom(true); });
    // 头部「下一条待处理」：面板内直接流转到下一条待审批/待回答，不必回列表。
    // 元素缺失时静默跳过（PWA 可能缓存了没有该按钮的旧 index.html）。
    var nextPendingBtn = el("chat-next-pending");
    if (nextPendingBtn) {
      nextPendingBtn.addEventListener("click", function () {
        var queue = pendingQueue();
        if (!queue.length) { chatUpdateNextPending(); return; }
        chatSwitchTo(queue[0].node, queue[0].session, chatFocusOptsFor(queue[0].session));
      });
    }
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
      if (Date.now() < st.followHoldUntil) {
        chatSyncJump();
        return;
      }
      if (st.scrollGuardUntil && Date.now() < st.scrollGuardUntil &&
          Math.abs(host.scrollTop - st.programmaticScrollTop) <= 1) {
        chatSyncJump();
        return;
      }
      st.follow = host.scrollHeight - host.scrollTop - host.clientHeight < 24;
      chatSyncJump();
      if (host.scrollTop < 64) { chatLoadOlder(); }
    });
    // A real wheel/touch gesture overrides the short initial follow hold. A
    // programmatic scroll (and content-visibility settling) must not be mistaken
    // for the operator trying to leave the live edge.
    ["wheel", "touchmove", "pointerdown"].forEach(function (name) {
      el("chat-list").addEventListener(name, function () {
        var st = chatState;
        if (!st) { return; }
        st.followHoldUntil = 0;
        st.scrollGuardUntil = 0;
      }, { passive: true });
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
      // 手机端会话页按 Esc 等同返回列表；桌面端双栏下 Esc 不关面板，
      // 以免在会话流里打字时误关（关闭走头部 ✕ 或离开会话视图）。
      if (e.key === "Escape" && imChatPageOpen()) { closeSessionChat(); }
    });
    // 系统/手势返回：会话流页回退时收起面板（浏览器已经回退了那条记录）。
    window.addEventListener("popstate", function () {
      if (!imChatIsOpen()) { return; }
      imSuppressHistoryBack = true;
      closeSessionChat();
      imSuppressHistoryBack = false;
    });
    // 断点切换（桌面 ↔ 手机）：跨过 860px 时把「整页会话流」状态归位，
    // 否则缩到桌面宽度后 body.im-chat-open 会残留并锁住滚动。
    if (window.matchMedia) {
      var imMq = window.matchMedia("(max-width: 860px)");
      var onImMq = function () {
        if (imMq.matches) { return; }
        document.body.classList.remove("im-chat-open");
      };
      if (imMq.addEventListener) { imMq.addEventListener("change", onImMq); }
    }
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

  // Deep-link: #sessions?attention=1 preselects the attention filter,
  // #sessions?running=1 the running filter (dashboard stat cards link here).
  function applyHashParams() {
    var h = window.location.hash || "";
    var q = h.indexOf("?");
    if (q === -1) { return; }
    var params = new URLSearchParams(h.slice(q + 1));
    if (params.get("attention")) {
      sessionFilterKind = "attention";
      syncFilterControls();
    }
    if (params.get("running")) {
      sessionFilterKind = "running";
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

  /* ─────────────────── 通知设置页交互 ───────────────────────── */

  function ensureNotifyDraft() {
    notifyPolicyDraft = notifyPolicyDraft || { version: 1, global: {}, sessions: {}, channels: {} };
    notifyPolicyDraft.global = notifyPolicyDraft.global || {};
    return notifyPolicyDraft;
  }

  var notifyEnabledInput = el("notify-global-enabled");
  if (notifyEnabledInput) {
    notifyEnabledInput.addEventListener("change", function () {
      ensureNotifyDraft().global.muted = !notifyEnabledInput.checked;
      notifyRefreshDirty();
    });
  }
  var notifyMinInput = el("notify-min-priority");
  if (notifyMinInput) {
    notifyMinInput.addEventListener("change", function () {
      ensureNotifyDraft().global.min_priority = notifyMinInput.value;
      renderNotifyPriorityHint();
      notifyRefreshDirty();
    });
  }
  var notifyQuietAdd = el("notify-quiet-add");
  if (notifyQuietAdd) {
    notifyQuietAdd.addEventListener("click", function () {
      notifyQuietTouched = true;
      var first = notifyAppendQuietRow({ start: "", end: "" });
      if (first) { first.focus(); }
      notifyCollectQuietRows(true);
    });
  }
  var notifySessionSearch = el("notify-session-search");
  if (notifySessionSearch) {
    notifySessionSearch.addEventListener("input", function () {
      notifySessionQuery = notifySessionSearch.value || "";
      renderNotifyPolicySessions();
    });
  }
  var notifySessionOnly = el("notify-session-only-custom");
  if (notifySessionOnly) {
    notifySessionOnly.addEventListener("change", function () {
      notifySessionOnlyCustom = notifySessionOnly.checked;
      renderNotifyPolicySessions();
    });
  }
  var notifySaveBtn = el("notify-save");
  if (notifySaveBtn) { notifySaveBtn.addEventListener("click", saveNotifyPolicy); }
  var notifyResetBtn = el("notify-reset");
  if (notifyResetBtn) { notifyResetBtn.addEventListener("click", resetNotifyPolicyDraft); }
  var notifyTestBtn = el("notify-test");
  if (notifyTestBtn) {
    notifyTestBtn.addEventListener("click", function () {
      var sidebarTest = el("push-test");
      if (!sidebarTest) { return; }
      notifyTestBtn.disabled = true;
      sidebarTest.click();
      setTimeout(function () { notifyTestBtn.disabled = false; }, 1500);
    });
  }
  notifyRefreshDirty();

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
      navigator.serviceWorker.register("./sw.js?v=75").catch(function () {});
    });
  }

  loadSessionPrefs();
  loadReadAttention();
  syncFilterControls();
  syncTailToggle();
  syncMdToggle();
  applyHashParams();
  applyRoute();
  // Probe once for an existing owner session; only fetch the device list when
  // the probe authenticated, so a fresh visitor emits a single 401 (the
  // probe itself) in the console instead of two.
  loadNodes().then(function () {
    if (authedState) {
      loadDevices();
      loadNotifyPolicy();
    }
  });
  renderInstallHint();
  refreshPushState();
})();
