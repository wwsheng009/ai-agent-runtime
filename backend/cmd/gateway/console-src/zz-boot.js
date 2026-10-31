  /* ─────────────────────── boot ──────────────────────────────── */

  if ("serviceWorker" in navigator) {
    // The worker posts this after showing a system notification, which proves
    // the push reached this browser (vs. the OS suppressing the toast).
    navigator.serviceWorker.addEventListener("message", function (event) {
      var data = event.data || {};
      if (data.type === "push-received") {
        notify("已收到推送：" + (data.title || "通知") + (data.body ? " · " + data.body : ""), "ok");
      }
      // 通知点击：service worker 复用了已打开的标签页，把会话深链交给页面
      // 就地导航（而不是新开窗口或整页重载）。
      if (data.type === "aicli-gateway-open" && data.url) {
        openGatewayDeepLink(data.url);
      }
    });
    window.addEventListener("load", function () {
      // ?v= is bumped with CACHE in sw.js on every release so CDN edges do not
      // keep serving an old worker script.
      navigator.serviceWorker.register("./sw.js?v=94").catch(function () {});
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
