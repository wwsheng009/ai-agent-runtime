// zz-boot.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=101";

  /* ─────────────────────── boot ──────────────────────────────── */

  if ("serviceWorker" in navigator) {
    // The worker posts this after showing a system notification, which proves
    // the push reached this browser (vs. the OS suppressing the toast).
    navigator.serviceWorker.addEventListener("message", function (event) {
      var data = event.data || {};
      if (data.type === "push-received") {
        G.notify("已收到推送：" + (data.title || "通知") + (data.body ? " · " + data.body : ""), "ok");
      }
      // 通知点击：service worker 复用了已打开的标签页，把会话深链交给页面
      // 就地导航（而不是新开窗口或整页重载）。
      if (data.type === "aicli-gateway-open" && data.url) {
        G.openGatewayDeepLink(data.url);
      }
    });
    window.addEventListener("load", function () {
      // ?v= is bumped with CACHE in sw.js on every release so CDN edges do not
      // keep serving an old worker script.
      navigator.serviceWorker.register("./sw.js?v=101").catch(function () {});
    });
  }

  G.loadSessionPrefs();
  G.loadReadAttention();
  G.syncFilterControls();
  G.syncTailToggle();
  G.syncMdToggle();
  G.applyHashParams();
  G.applyRoute();
  // Probe once for an existing owner session; only fetch the device list when
  // the probe authenticated, so a fresh visitor emits a single 401 (the
  // probe itself) in the console instead of two.
  G.loadNodes().then(function () {
    if (S.authedState) {
      G.loadDevices();
      G.loadNotifyPolicy();
    }
  });
  G.renderInstallHint();
  G.refreshPushState();
