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
      ensureGatewayConnectorOptions();
      // IM 目标的事件类型是多选：进页面就按 kinds 元数据补齐复选框。
      renderNotifyIMKinds();
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

  // 通知点击落地：service worker 聚焦已有控制台页面后把目标 URL postMessage
  // 过来。同源 hash 深链就地导航，避免整页重载（并保留登录态与草稿）。
  function openGatewayDeepLink(raw) {
    var u;
    try { u = new URL(raw, location.origin); } catch (e) { return; }
    if (u.origin !== location.origin) { return; }
    var hash = u.hash || "";
    if (!hash) { return; }
    try { history.replaceState(null, "", hash); } catch (e) { window.location.hash = hash.replace(/^#/, ""); }
    applyHashParams();
    applyRoute();
  }

  // UX-07: 顶栏刷新是唯一的手动刷新入口，只刷新当前视图。
  function refreshCurrentView() {
    var view = currentView();
    if (view === "tokens") { return loadDevices(); }
    if (view === "connectors") {
      connectorNotice("");
      return loadConnectors(true).then(function () { return loadConnectorBindings(); });
    }
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

