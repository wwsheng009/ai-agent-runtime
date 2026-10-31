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
      // 连接器视图可能先于节点快照打开（首屏 hash 直达 #connectors）：
      // 节点到齐后按签名补读一次；节点集合/路由不变时不会重复请求。
      if (currentView() === "connectors" && connectorGatewaySettled) {
        refreshConnectorNodes(false);
      }
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

