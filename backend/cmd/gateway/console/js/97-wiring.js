// 97-wiring.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=101";

Object.assign(G, {
  doLogout: doLogout,
  setCollapsed: setCollapsed,
  syncTailToggle: syncTailToggle,
  syncSubagentToggle: syncSubagentToggle,
  syncMdToggle: syncMdToggle,
  applyHashParams: applyHashParams,
});

  /* ─────────────────────── wiring ────────────────────────────── */

  // Skip link: move focus into <main> without touching the hash, otherwise the
  // router would read "#main" as an unknown view and snap back to the dashboard.
  S.skipLink = document.querySelector(".skip-link");
  if (S.skipLink) {
    S.skipLink.addEventListener("click", function (e) {
      e.preventDefault();
      var main = S.el("main");
      if (main) { main.focus(); }
    });
  }

  S.el("login-form").addEventListener("submit", function (e) {
    e.preventDefault();
    var passwordEl = S.el("password");
    var errorEl = S.el("login-error");
    errorEl.hidden = true;
    passwordEl.setAttribute("aria-invalid", "false");
    function fail(message) {
      errorEl.textContent = message;
      errorEl.hidden = false;
      passwordEl.setAttribute("aria-invalid", "true");
      passwordEl.focus();
    }
    G.apiJSON("/api/auth/login", {
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
      G.applyRoute();
      G.loadNodes();
      G.loadDevices();
    }).catch(function () {
      fail("登录失败：无法连接网关，请确认服务正在运行");
    });
  });

  function doLogout() {
    if (G.notifyPolicyIsDirty()) {
      var leave = true;
      try {
        leave = window.confirm("通知设置有未保存的修改，退出登录后会丢失。确定退出吗？");
      } catch (e) { /* headless/jsdom: proceed */ }
      if (!leave) { return; }
    }
    G.api("/api/auth/logout", { method: "POST" }).then(function () { G.setAuthed(false); });
  }
  S.el("logout").addEventListener("click", doLogout);
  S.el("logout-top").addEventListener("click", doLogout);

  S.el("refresh").addEventListener("click", function () { G.refreshCurrentView(); });
  // Logs view: tabs, manual refresh, follow toggle and server-side filters.
  document.querySelectorAll("[data-logtab]").forEach(function (btn) {
    btn.addEventListener("click", function () { G.setLogTab(btn.getAttribute("data-logtab")); });
  });
  S.logsFollow = S.el("logs-follow");
  if (S.logsFollow) {
    S.logsFollow.addEventListener("click", function () {
      S.logFollow = !S.logFollow;
      S.logsFollow.textContent = "自动刷新：" + (S.logFollow ? "开" : "关");
      S.logsFollow.setAttribute("aria-pressed", S.logFollow ? "true" : "false");
      if (S.logFollow) {
        G.startLogTimer();
        G.refreshLogs();
      } else {
        G.stopLogTimer();
      }
    });
  }
  S.logSlugEl = S.el("log-slug");
  if (S.logSlugEl) {
    S.logSlugEl.addEventListener("change", function () {
      if (S.logTab === "mounts") { G.loadMountLogs(); }
    });
  }
  S.logStatusEl = S.el("log-status");
  if (S.logStatusEl) {
    S.logStatusEl.addEventListener("change", function () {
      if (S.logTab === "mounts") { G.renderMountLogs(S.logMountEntries); }
    });
  }
  S.logTypeEl = S.el("log-type");
  if (S.logTypeEl) {
    S.logTypeEl.addEventListener("change", function () {
      if (S.logTab === "audit") { G.loadAuditLogs(); }
    });
  }
  S.logQueryEl = S.el("log-query");
  if (S.logQueryEl) {
    var logQueryTimer = null;
    S.logQueryEl.addEventListener("input", function () {
      clearTimeout(logQueryTimer);
      logQueryTimer = setTimeout(function () {
        if (S.logTab === "audit") { G.loadAuditLogs(); }
      }, 300);
    });
  }
  // UX-06: 离线快照清理改为「描边按钮 + 站内确认」，并在确认文案里
  // 列出受影响节点；不再是实心红按钮 + 原生 confirm。
  S.clearStale = S.el("clear-stale");
  if (S.clearStale) {
    S.clearStale.addEventListener("click", function () {
      var staleNodes = S.lastNodes.filter(function (n) { return n.stale; });
      if (!staleNodes.length) { return; }
      var preview = staleNodes.slice(0, 5).map(function (n) {
        var hbMs = n.last_heartbeat ? new Date(n.last_heartbeat).getTime() : NaN;
        return "• " + G.nodeDisplayName(n) +
          (isNaN(hbMs) ? "" : "（最后活跃 " + new Date(hbMs).toLocaleString() + "）");
      }).join("\n");
      if (staleNodes.length > 5) {
        preview += "\n• 另有 " + (staleNodes.length - 5) + " 个节点";
      }
      G.closeSessionMenus(false);
      G.confirmDestructive({
        title: "清除全部离线快照？",
        message: "将删除 " + staleNodes.length + " 个离线节点及其会话条目。\n\n" +
          preview + "\n\nagent 重新上线会自动重新注册。",
        trigger: S.clearStale,
        confirmText: "清除全部",
        onConfirm: function () {
          return G.api("/api/nodes?stale=1", { method: "DELETE" })
            .then(function (res) {
              if (res.status === 401) { G.setAuthed(false); return; }
              if (!res.ok) { throw new Error("HTTP " + res.status); }
              G.notify("已清除 " + staleNodes.length + " 个离线快照节点", "ok");
              G.loadNodes();
            });
        }
      });
    });
  }

  S.pushBusy = false;
  S.el("push-toggle").addEventListener("click", function () {
    if (S.pushBusy) { return; }
    if (G.nativeNotificationApi()) {
      var nativeDisabling = S.el("push-toggle-label").textContent.indexOf("关闭") !== -1;
      S.pushBusy = true;
      G.setPushLabel(nativeDisabling ? "关闭中…" : "开启中…");
      var nativeBtn = S.el("push-toggle");
      nativeBtn.disabled = true;
      Promise.resolve(nativeDisabling ? G.disableNativeNotifications() : G.enableNativeNotifications())
        .then(function () {
          G.refreshPushState();
          G.notify(nativeDisabling ? "已关闭应用通知" : "已开启应用通知", "ok");
        })
        .catch(function (err) {
          G.refreshPushState();
          G.notify("应用通知操作失败：" + (err && err.message ? err.message : "请检查系统通知权限"), "error");
        })
        .finally(function () {
          S.pushBusy = false;
          nativeBtn.disabled = false;
        });
      return;
    }
    // The click handler is the only entry point for enabling: it must stay
    // synchronous up to Notification.requestPermission() (see enablePush).
    var env = G.pushEnvironment();
    if (!env.ok) { G.notify(env.reason, "error"); return; }
    if (!("Notification" in window) || !Notification.requestPermission) {
      G.notify("当前浏览器不支持系统通知。", "error");
      return;
    }
    if (Notification.permission === "denied") {
      G.notify("通知权限被拒绝：请在浏览器/系统设置中允许通知后刷新页面。", "error");
      return;
    }
    if (S.pushConfig && S.pushConfig.enabled === false) {
      G.notify("网关未启用推送（--push-enabled=false 或 VAPID 初始化失败）。", "error");
      return;
    }
    var disabling = S.el("push-toggle-label").textContent.indexOf("关闭") !== -1;
    S.pushBusy = true;
    G.setPushLabel(disabling ? "关闭中…" : "开启中…");
    var btn = S.el("push-toggle");
    var test = S.el("push-test");
    btn.disabled = true;
    Promise.resolve(disabling ? G.disablePush() : G.enablePush())
      .then(function () {
        G.setPushLabel(disabling ? "开启通知" : "关闭通知");
        if (test) { test.hidden = disabling; }
        G.notify(disabling ? "已关闭通知" : "已开启通知", "ok");
      })
      .catch(function (err) {
        G.refreshPushState();
        G.notify("通知操作失败：" + (err && err.message ? err.message : "请检查浏览器通知权限"), "error");
      })
      .finally(function () {
        S.pushBusy = false;
        btn.disabled = false;
      });
  });

  S.pushTest = S.el("push-test");
  if (S.pushTest) {
    S.pushTest.addEventListener("click", function () {
      if (G.nativeNotificationApi()) {
        var nativeIdle = S.pushTest.textContent;
        S.pushTest.disabled = true;
        S.pushTest.textContent = "发送中…";
        G.sendNativeNotification("aicli-gateway", "应用通知测试成功").then(function (ok) {
          G.notify(ok ? "已发送应用通知" : "应用通知未开启或权限未授予", ok ? "ok" : "error");
        }).finally(function () {
          S.pushTest.disabled = false;
          S.pushTest.textContent = nativeIdle;
        });
        return;
      }
      var idleText = S.pushTest.textContent;
      S.pushTest.disabled = true;
      // The API waits for the push service round trip (usually 0.5–3s), so
      // give immediate feedback instead of looking dead.
      S.pushTest.textContent = "发送中…";
      G.apiJSON("/api/push/test", { method: "POST" }).then(function (r) {
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
          G.notify("测试通知：" + parts.join("；") + (ok ? "" : "。请检查对应通道配置。"), ok ? "ok" : "error");
          return;
        }
        var delivered = (r.body && r.body.delivered) || 0;
        var total = (r.body && r.body.subscriptions) || 0;
        if (total === 0) {
          G.notify("网关没有已保存的订阅：请先在本设备开启通知。", "error");
          return;
        }
        if (delivered === 0) {
          G.notify("推送服务拒收了全部 " + total + " 个订阅（0/" + total + "）：请关闭后重新开启通知，并确认 data/vapid.key 未被重建。", "error");
          return;
        }
        G.notify("已投递 " + delivered + "/" + total + " 个订阅。若本机未弹出：iOS 需从主屏幕图标打开；Android 请检查系统通知与电池优化。", delivered === total ? "ok" : "error");
      }).catch(function (err) {
        G.notify("测试通知失败：" + (err && err.message ? err.message : err), "error");
      }).finally(function () { S.pushTest.disabled = false; S.pushTest.textContent = idleText; });
    });
  }

  // Mobile sidebar
  S.el("nav-toggle").addEventListener("click", function () {
    S.el("sidebar").classList.add("open");
    S.el("sidebar-scrim").hidden = false;
  });
  S.el("sidebar-scrim").addEventListener("click", G.closeSidebar);

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
  S.el("sidebar-collapse").addEventListener("click", function () { setCollapsed(true); });
  S.el("sidebar-expand").addEventListener("click", function () { setCollapsed(false); });

  // Session filters
  S.el("session-filter").addEventListener("input", function (e) {
    S.sessionFilterText = e.target.value.trim();
    G.renderSessionTable(S.lastNodes);
  });
  document.querySelectorAll(".seg-btn").forEach(function (btn) {
    btn.addEventListener("click", function () {
      S.sessionFilterKind = btn.getAttribute("data-sfilter");
      G.syncFilterControls();
      G.saveSessionPrefs();
      G.renderSessionTable(S.lastNodes);
    });
  });
  S.el("session-sort").addEventListener("change", function (e) {
    S.sessionSort = e.target.value;
    G.saveSessionPrefs();
    G.renderSessionTable(S.lastNodes);
  });

  // Live-output tail on/off (persisted): expanded cards render the bounded
  // tail only while this switch is on.
  function syncTailToggle() {
    var btn = S.el("tail-toggle");
    if (btn) { btn.textContent = "实时输出：" + (S.tailEnabled ? "开" : "关"); }
  }
  S.el("tail-toggle").addEventListener("click", function () {
    S.tailEnabled = !S.tailEnabled;
    if (!S.tailEnabled) {
      // Turning live output off releases every tail watch (agents stop
      // streaming; presence TTL no longer needs to hold tail targets).
      Object.keys(S.presenceTargets).forEach(function (k) { S.presenceTargets[k].tail = false; });
      G.sendPresence();
    }
    G.saveSessionPrefs();
    syncTailToggle();
    G.renderSessionTable(S.lastNodes);
  });

  // 子代理行显示/隐藏（持久化）：宿主未上报子会话行时按钮无副作用。元素缺失时
  // 静默跳过（PWA 可能缓存了旧版 index.html）。
  function syncSubagentToggle() {
    var btn = S.el("subagent-toggle");
    if (btn) { btn.textContent = "子代理：" + (S.showSubagents ? "显示" : "隐藏"); }
  }
  S.subagentToggleBtn = S.el("subagent-toggle");
  if (S.subagentToggleBtn) {
    S.subagentToggleBtn.addEventListener("click", function () {
      S.showSubagents = !S.showSubagents;
      G.saveSessionPrefs();
      syncSubagentToggle();
      G.renderSessionTable(S.lastNodes);
    });
  }
  syncSubagentToggle();

  // 消息 Markdown 渲染开关（持久化，默认开，见 renderMarkdown）：关闭回落纯文本。
  function syncMdToggle() {
    var btn = S.el("md-toggle");
    if (btn) { btn.textContent = "Markdown：" + (S.mdEnabled ? "开" : "关"); }
  }
  S.mdToggleBtn = S.el("md-toggle");
  if (S.mdToggleBtn) {
    S.mdToggleBtn.addEventListener("click", function () {
      S.mdEnabled = !S.mdEnabled;
      G.saveSessionPrefs();
      syncMdToggle();
      G.renderSessionTable(S.lastNodes);
      if (S.detailTarget) { G.openSessionDetail(S.detailTarget.node, S.detailTarget.session); }
      // 会话面板里已渲染的正文同样按新模式重排（否则要等下一帧增量）。
      if (S.chatState) { G.chatRender(); }
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
    if (G.imChatPageOpen() || G.modalIsOpen("session-modal") ||
        G.modalIsOpen("token-modal") || G.modalIsOpen("mount-token-modal")) { return; }
    var target = e.target;
    if (target && target.closest && target.closest(".s-menu")) { return; }
    var tag = (target && target.tagName) || "";
    var typing = tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" ||
      !!(target && target.isContentEditable);
    if (e.key === "/" && !typing) {
      e.preventDefault();
      var filterEl = S.el("session-filter");
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
      var cardItem = action ? G.findSessionByKey(current.getAttribute("data-draft-key")) : null;
      if (cardItem) {
        e.preventDefault();
        if (e.key === "c") {
          // 与行点击/主按钮同一条主路径（无 Web 路由时落到详情）。
          G.openSessionFromCard(cardItem.node, cardItem.session);
          return;
        }
        if (e.key === "o") {
          G.openSession(cardItem.node, cardItem.session);
          return;
        }
        // a = 审批 / 回答：与卡片 data-attend 按钮同一可用性门槛；不可用时
        // 不动作（按钮此时同样是隐藏的，不给"为什么没反应"的歧义）。
        var att = cardItem.session.attention;
        if ((att === "approval" || att === "question") &&
            G.chatPanelSupported(cardItem.node, cardItem.session) &&
            G.chatCanInput(cardItem.node)) {
          G.openSessionChat(cardItem.node, cardItem.session, { focusInteractive: true });
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
      var found = G.findSessionByKey(current.getAttribute("data-draft-key"));
      if (!found) { return; }
      e.preventDefault();
      G.openSessionFromCard(found.node, found.session);
    }
  });

  // Token modal
  S.el("issue-token").addEventListener("click", function () {
    G.openModal("token-modal");
    S.el("device-name").focus();
  });
  S.deviceSearch = S.el("device-search");
  if (S.deviceSearch) {
    S.deviceSearch.addEventListener("input", function () {
      S.deviceQuery = S.deviceSearch.value || "";
      G.renderDevices(S.lastDevices);
    });
  }
  S.deviceStatus = S.el("device-status-filter");
  if (S.deviceStatus) {
    S.deviceStatus.addEventListener("change", function () {
      S.deviceStatusFilter = S.deviceStatus.value || "";
      G.renderDevices(S.lastDevices);
    });
  }
  S.el("token-modal-close").addEventListener("click", function () {
    G.closeModal("token-modal");
  });
  S.el("token-modal").addEventListener("click", function (e) {
    if (e.target === S.el("token-modal")) { G.closeModal("token-modal"); }
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !S.el("token-modal").hidden) { G.closeModal("token-modal"); }
  });

  // MCP mount token modal
  S.el("mount-token-modal-close").addEventListener("click", function () {
    G.closeMountTokenModal();
  });
  S.el("mount-token-modal").addEventListener("click", function (e) {
    if (e.target === S.el("mount-token-modal")) { G.closeMountTokenModal(); }
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !S.el("mount-token-modal").hidden) {
      G.closeMountTokenModal();
    }
  });
  S.el("mount-token-refresh").addEventListener("click", G.loadMountTokens);
  S.mountTokenTTL = S.el("mount-token-ttl");
  if (S.mountTokenTTL) { S.mountTokenTTL.addEventListener("change", G.syncMountTokenTTL); }
  S.el("mount-token-form").addEventListener("submit", function (e) {
    e.preventDefault();
    if (!S.mountTokenTarget) { return; }
    var name = S.el("mount-token-name").value.trim();
    var ttlChoice = S.el("mount-token-ttl").value;
    var body = { name: name };
    if (ttlChoice === "never") {
      body.never_expires = true;
    } else if (ttlChoice === "custom") {
      var days = parseInt(S.el("mount-token-days").value, 10);
      if (!days || days < 1 || days > 3650) {
        G.notify("有效期请输入 1–3650 天", "error");
        S.el("mount-token-days").focus();
        return;
      }
      body.ttl_seconds = days * 86400;
    } else {
      body.ttl_seconds = parseInt(ttlChoice, 10) || 0;
    }
    var submit = S.el("mount-token-form").querySelector('button[type="submit"]');
    if (submit) { submit.disabled = true; }
    G.apiJSON("/api/mounts/" + encodeURIComponent(S.mountTokenTarget.slug) + "/tokens", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }).then(function (r) {
      if (r.res.status === 401) { G.setAuthed(false); return; }
      if (!r.res.ok || !r.body || !r.body.token) {
        throw new Error("HTTP " + r.res.status +
          (r.body && r.body.message ? " " + r.body.message : ""));
      }
      G.showMountToken(r.body.token);
      S.el("mount-token-name").value = "";
      G.loadMountTokens();
      G.loadMounts();
      G.notify("已签发 MCP 令牌（仅显示一次）", "ok");
    }).catch(function (err) {
      G.notify("签发失败：" + err, "error");
    }).then(function () {
      if (submit) { submit.disabled = false; }
    });
  });

  // Session detail modal
  S.el("session-modal-close").addEventListener("click", G.closeSessionDetail);
  S.el("session-modal-done").addEventListener("click", G.closeSessionDetail);
  S.el("session-modal-open").addEventListener("click", function () {
    if (!S.detailTarget) { return; }
    var target = S.detailTarget;
    G.closeSessionDetail();
    G.openSessionFromCard(target.node, target.session);
  });
  S.el("sd-copy").addEventListener("click", function (e) {
    if (S.detailTarget) { G.copyText(S.detailTarget.session.session_id, e.currentTarget); }
  });
  S.el("sd-copy-diagnostics").addEventListener("click", function (e) {
    if (S.detailTarget) {
      G.copyText(G.sessionDiagnostics(S.detailTarget.node, S.detailTarget.session, "detail"), e.currentTarget);
    }
  });
  S.el("session-modal").addEventListener("click", function (e) {
    if (e.target === S.el("session-modal")) { G.closeSessionDetail(); }
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !S.el("session-modal").hidden) { G.closeSessionDetail(); }
  });

  // Session chat modal. 面板元素缺失时（PWA 缓存了旧 index.html）整块跳过，
  // 这样新版 app.js 不会因为少节点而抛错（入口按钮同样不会出现）。
  if (S.el("chat-modal")) {
    S.el("chat-modal-close").addEventListener("click", G.closeSessionChat);
    // 手机端返回键：会话流整页 → 会话列表。
    var backBtn = S.el("chat-back");
    if (backBtn) { backBtn.addEventListener("click", G.closeSessionChat); }
    // 会话级管理动作统一放在右栏会话头；列表卡片只负责选择会话。
    var chatMore = S.el("chat-more");
    var chatMenu = S.el("chat-more-menu");
    if (chatMore && chatMenu) {
      G.bindCardMenu(chatMore.parentElement, chatMenu);
      chatMenu.querySelector("[data-chat-detail]").addEventListener("click", function () {
        if (!S.chatState) { return; }
        G.closeSessionMenus(false);
        G.openSessionDetail(S.chatState.node, S.chatState.session);
      });
      chatMenu.querySelector("[data-chat-web]").addEventListener("click", function () {
        if (!S.chatState) { return; }
        G.closeSessionMenus(false);
        G.openSession(S.chatState.node, S.chatState.session);
      });
      [
        { selector: "[data-chat-window-open]", kind: "open-terminal", done: "已排队：该节点下一个心跳会在本机新终端里打开此会话" },
        { selector: "[data-chat-window-resume]", kind: "resume", done: "已排队：该节点下一个心跳会恢复此会话" },
      ].forEach(function (action) {
        chatMenu.querySelector(action.selector).addEventListener("click", function () {
          if (!S.chatState) { return; }
          G.closeSessionMenus(false);
          G.requestWindowAction(S.chatState.node, S.chatState.session, action.kind)
            .then(function () { G.notify(action.done, "ok"); })
            .catch(function (err) { G.notify("窗口动作失败：" + err, "error"); });
        });
      });
      chatMenu.querySelector("[data-chat-window-close]").addEventListener("click", function () {
        if (!S.chatState) { return; }
        var st = S.chatState;
        G.closeSessionMenus(false);
        G.confirmDestructive({
          title: "关闭窗口进程？",
          message: "关闭该会话在本机的窗口进程？\n（等同在窗口里敲 /exit：会话会正常保存。）",
          trigger: chatMore,
          confirmText: "关闭窗口",
          onConfirm: function () {
            return G.requestWindowAction(st.node, st.session, "close")
              .then(function () { G.notify("已排队：该节点下一个心跳会关闭此窗口", "ok"); });
          }
        });
      });
      chatMenu.querySelector("[data-chat-remove]").addEventListener("click", function () {
        if (!S.chatState) { return; }
        var st = S.chatState;
        G.closeSessionMenus(false);
        G.confirmDestructive({
          title: "移除会话条目？",
          message: "从注册表移除该会话条目？\n（仅清除网关侧快照，不影响 agent 上的会话数据。）",
          trigger: chatMore,
          confirmText: "移除",
          onConfirm: function () {
            return G.api("/api/nodes/" + encodeURIComponent(st.node.node_id) +
                "/sessions/" + encodeURIComponent(st.session.session_id), { method: "DELETE" })
              .then(function () { G.notify("会话条目已移除", "ok"); G.closeSessionChat(); G.loadNodes(); });
          }
        });
      });
    }
    S.el("chat-jump-latest").addEventListener("click", function () { G.chatScrollToBottom(true); });
    // 头部「下一条待处理」：面板内直接流转到下一条待审批/待回答，不必回列表。
    // 元素缺失时静默跳过（PWA 可能缓存了没有该按钮的旧 index.html）。
    var nextPendingBtn = S.el("chat-next-pending");
    if (nextPendingBtn) {
      nextPendingBtn.addEventListener("click", function () {
        var queue = G.pendingQueue();
        if (!queue.length) { G.chatUpdateNextPending(); return; }
        G.chatSwitchTo(queue[0].node, queue[0].session, G.chatFocusOptsFor(queue[0].session));
      });
    }
    S.el("chat-form").addEventListener("submit", function (e) {
      e.preventDefault();
      G.chatSend();
    });
    var chatSendBtn = S.el("chat-send");
    if (chatSendBtn) {
      chatSendBtn.addEventListener("click", function (e) {
        var st = S.chatState;
        if (!st || !st.busy) { return; }
        // The button is type=submit: while a turn is running, intercept the
        // submit and turn the primary action into Stop. Enter still queues the
        // next prompt through the form handler below.
        e.preventDefault();
        G.chatStopTurn();
      });
    }
    S.el("chat-input").addEventListener("input", function () {
      G.chatAutoGrow();
      G.chatSyncSendState();
    });
    // Enter 发送 / Shift+Enter 换行（与卡片回复表单一致）。
    S.el("chat-input").addEventListener("keydown", function (e) {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        G.chatSend();
      }
    });
    // 上滚暂停自动跟随；滚到顶部附近自动翻页（阅读历史时不必先找按钮）。
    S.el("chat-list").addEventListener("scroll", function () {
      var st = S.chatState;
      var host = S.el("chat-list");
      if (!st || !host) { return; }
      if (st.bottomPin) { G.chatSyncJump(); return; }
      if (Date.now() < st.followHoldUntil) {
        G.chatSyncJump();
        return;
      }
      if (st.scrollGuardUntil && Date.now() < st.scrollGuardUntil &&
          Math.abs(host.scrollTop - st.programmaticScrollTop) <= 1) {
        G.chatSyncJump();
        return;
      }
      st.follow = host.scrollHeight - host.scrollTop - host.clientHeight < 24;
      G.chatSyncJump();
      if (host.scrollTop < 64) { G.chatLoadOlder(); }
    });
    // A real wheel/touch gesture overrides the short initial follow hold. A
    // programmatic scroll (and content-visibility settling) must not be mistaken
    // for the operator trying to leave the live edge.
    ["wheel", "touchmove", "pointerdown"].forEach(function (name) {
      S.el("chat-list").addEventListener(name, function () {
        var st = S.chatState;
        if (!st) { return; }
        st.followHoldUntil = 0;
        st.scrollGuardUntil = 0;
      }, { passive: true });
    });
    // 工具行折叠控件：点击 + Enter/Space（与 micro web client 同一委托方式）。
    // 用户手动展开 → 记账（永不自动折回）；手动收起 → 连自动标记一起清。
    S.el("chat-list").addEventListener("click", function (e) {
      var tgl = e.target && e.target.closest ? e.target.closest(".chat-tool-toggle") : null;
      if (!tgl) { return; }
      var row = tgl.closest(".chat-msg");
      if (!row || tgl.classList.contains("is-off")) { return; }
      var out = row.querySelector(".chat-tool-output");
      if (!out) { return; }
      var expanded = out.classList.contains("chat-collapsed");
      G.chatSetToolExpanded(row, expanded);
      if (row.__key) {
        if (expanded) { S.chatUserExpanded[row.__key] = true; }
        else { delete S.chatUserExpanded[row.__key]; }
      }
    });
    S.el("chat-list").addEventListener("keydown", function (e) {
      if (e.key !== "Enter" && e.key !== " " && e.key !== "Spacebar") { return; }
      var tgl = e.target && e.target.closest ? e.target.closest(".chat-tool-toggle") : null;
      if (!tgl) { return; }
      e.preventDefault();
      tgl.click();
    });
    // 推理面板：原生 <details> 切换后记录用户意图（延迟一拍读切换后的状态，
    // 程序化 open/close 不经过 click，不会被误记）。
    S.el("chat-list").addEventListener("toggle", function (e) {
      var det = e.target;
      if (!det.classList || !det.classList.contains("chat-reasoning")) { return; }
      var row = det.closest(".chat-msg");
      if (!row || !row.__key) { return; }
      if (det.open) { S.chatUserExpanded[row.__key] = true; }
      else { delete S.chatUserExpanded[row.__key]; }
    }, true);
    document.addEventListener("keydown", function (e) {
      // 手机端会话页按 Esc 等同返回列表；桌面端双栏下 Esc 不关面板，
      // 以免在会话流里打字时误关（关闭走头部 ✕ 或离开会话视图）。
      if (e.key === "Escape" && G.imChatPageOpen()) { G.closeSessionChat(); }
    });
    // 系统/手势返回：会话流页回退时收起面板（浏览器已经回退了那条记录）。
    window.addEventListener("popstate", function () {
      if (!G.imChatIsOpen()) { return; }
      S.imSuppressHistoryBack = true;
      G.closeSessionChat();
      S.imSuppressHistoryBack = false;
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

  S.el("device-form").addEventListener("submit", function (e) {
    e.preventDefault();
    G.apiJSON("/api/devices", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: S.el("device-name").value }),
    }).then(function (r) {
      if (r.res.status === 401) { G.setAuthed(false); return; }
      S.el("device-name").value = "";
      var tokenEl = S.el("device-token");
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
      G.loadDevices();
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
      S.sessionFilterKind = "attention";
      G.syncFilterControls();
    }
    if (params.get("running")) {
      S.sessionFilterKind = "running";
      G.syncFilterControls();
    }
    // 网关会话 URL 标识：通知点击 / 复制链接落到 #sessions?node=…&session=…
    // （node 可省略，按 session 反查）。快照未就绪或尚未登录时先记下目标，
    // 由 flushPendingLocate() 在 loadNodes() 成功后补做定位。
    var nodeParam = params.get("node") || "";
    var sessionParam = params.get("session") || "";
    if (sessionParam) {
      S.pendingLocate = { nodeID: nodeParam, sessionID: sessionParam };
      if (S.authedState) { G.flushPendingLocate(); }
    }
  }
