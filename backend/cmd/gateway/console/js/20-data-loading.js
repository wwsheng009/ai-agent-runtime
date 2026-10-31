// 20-data-loading.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=100";

Object.assign(G, {
  loadNodes: loadNodes,
  loadDevices: loadDevices,
});

  /* ─────────────────────── data loading ──────────────────────── */

  function loadNodes() {
    // Mark the panels busy so first paint shows "加载中…" instead of blanks.
    var views = document.querySelector(".views");
    if (views) { views.setAttribute("aria-busy", "true"); }
    return G.apiJSON("/api/nodes").then(function (r) {
      if (r.res.status === 401) { G.setAuthed(false); return; }
      G.setAuthed(true);
      S.lastNodes = (r.body && r.body.nodes) || [];
      G.renderAll(S.lastNodes);
      // 登录/刷新完成后补做通知定位请求（事件可能早于快照到达）。
      G.flushPendingLocate();
      G.subscribeEvents();
      // 连接器视图可能先于节点快照打开（首屏 hash 直达 #connectors）：
      // 节点到齐后按签名补读一次；节点集合/路由不变时不会重复请求。
      if (G.currentView() === "connectors" && S.connectorGatewaySettled) {
        G.refreshConnectorNodes(false);
      }
    }).finally(function () {
      if (views) { views.removeAttribute("aria-busy"); }
    });
  }

  function loadDevices() {
    return G.apiJSON("/api/devices").then(function (r) {
      if (r.res.status === 401) { G.setAuthed(false); return; }
      S.lastDevices = (r.body && r.body.devices) || [];
      G.renderDevices(S.lastDevices);
    });
  }
