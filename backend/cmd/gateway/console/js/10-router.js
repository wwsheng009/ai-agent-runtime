// 10-router.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=103";

Object.assign(G, {
  currentView: currentView,
  applyRoute: applyRoute,
  navigate: navigate,
  openGatewayDeepLink: openGatewayDeepLink,
  refreshCurrentView: refreshCurrentView,
  closeSidebar: closeSidebar,
});

  /* ─────────────────────── hash router ───────────────────────── */

  S.VIEW_TITLES = {
    dashboard: "仪表盘",
    sessions: "会话",
    nodes: "节点",
    mcp: "MCP 服务",
    logs: "日志",
    tokens: "设备令牌",
    connectors: "连接器",
    notifications: "通知设置",
  };
  S.notifyLastView = "";

  function currentView() {
    var h = (window.location.hash || "#dashboard").replace(/^#\/?/, "");
    var name = h.split("?")[0];
    return S.VIEW_TITLES[name] ? name : "dashboard";
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
    var titleEl = S.el("view-title");
    if (titleEl) { titleEl.textContent = S.VIEW_TITLES[view] || "aicli-gateway"; }
    // Connectors are a lazy view: only hit /api/connectors when the tab is open
    // (the management plane may legitimately be unwired → 403/503 page state).
    if (view === "connectors") { G.loadConnectors(false); }
    // MCP 挂载同为惰性视图：只有打开时才读 /api/mounts，避免登录后多一次请求。
    if (view === "mcp") { G.loadMounts(); }
    // 日志视图同样是惰性的：进入时拉一次，离开时停掉轮询。
    if (view === "logs") { G.initLogsView(); } else { G.stopLogTimer(); }
    // 通知设置同样是惰性视图：只在打开时拉取策略，避免每次登录多一个请求。
    if (view === "notifications") {
      G.loadNotifyPolicy();
      if (S.notifyPolicyLoaded) { G.renderNotifyPolicy(); }
      G.ensureGatewayConnectorOptions();
      // IM 目标的事件类型是多选：进页面就按 kinds 元数据补齐复选框。
      G.renderNotifyIMKinds();
      // Bot 目标列表也是惰性的：登录后进这一页才拉（登录前拉会 401，见 loadNotifyIM）。
      G.loadNotifyIM();
    }
    // 微信式会话壳只在「会话」视图生效：body.im-mode 打开整页固定高度，
    // 让左右两栏各自滚动（而不是整页滚动）。
    document.body.classList.toggle("im-mode", view === "sessions");
    if (view !== "sessions" && G.imChatIsOpen()) {
      // 离开会话视图就收起会话流并断开 SSE；这里不能 history.back()，
      // 否则会把用户刚点的导航又弹回去。
      S.imSuppressHistoryBack = true;
      G.closeSessionChat();
      S.imSuppressHistoryBack = false;
    }
    if (view === "sessions") { setTimeout(G.autoSelectFirstSession, 0); }
    closeSidebar();
    S.notifyLastView = view;
  }

  window.addEventListener("hashchange", function () {
    if (S.notifyLastView === "notifications" && currentView() !== "notifications" &&
        G.notifyPolicyIsDirty()) {
      var leave = true;
      try {
        leave = window.confirm("通知设置有未保存的修改，离开后会丢失。确定离开吗？");
      } catch (e) { /* headless/jsdom: fail open, the draft remains recoverable */ }
      if (!leave) {
        try { history.replaceState(null, "", "#notifications"); } catch (e2) { window.location.hash = "notifications"; }
        return;
      }
      G.notifyDiscardPolicyDraft();
    }
    G.applyHashParams();
    applyRoute();
  });
  window.addEventListener("beforeunload", function (event) {
    if (!G.notifyPolicyIsDirty()) { return; }
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
    G.applyHashParams();
    applyRoute();
  }

  // UX-07: 顶栏刷新是唯一的手动刷新入口，只刷新当前视图。
  function refreshCurrentView() {
    var view = currentView();
    if (view === "tokens") { return G.loadDevices(); }
    if (view === "connectors") {
      G.connectorNotice("");
      return G.loadConnectors(true).then(function () { return G.loadConnectorBindings(); });
    }
    if (view === "mcp") { return G.loadMounts(); }
    if (view === "logs") { G.refreshLogs(); return Promise.resolve(); }
    if (view === "notifications") {
      if (G.notifyPolicyIsDirty()) {
        G.notify("通知设置还有未保存的修改，先保存或放弃再刷新", "error");
        return Promise.resolve();
      }
      return G.loadNotifyPolicy(true).then(function () { return G.loadNotifyIM(); });
    }
    return G.loadNodes();
  }

  function closeSidebar() {
    S.el("sidebar").classList.remove("open");
    S.el("sidebar-scrim").hidden = true;
  }
