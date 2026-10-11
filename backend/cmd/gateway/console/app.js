(function () {
  "use strict";

  var el = function (id) { return document.getElementById(id); };

  function setAuthed(authed) {
    el("login").hidden = authed;
    el("nodes").hidden = !authed;
    el("devices").hidden = !authed;
    el("push-toggle").hidden = !authed;
    el("logout").hidden = !authed;
  }

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

  function loadNodes() {
    return apiJSON("/api/nodes").then(function (r) {
      if (r.res.status === 401) { setAuthed(false); return; }
      setAuthed(true);
      renderNodes((r.body && r.body.nodes) || []);
      subscribeEvents();
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
        renderNodes((payload && payload.nodes) || []);
      } catch (err) {
        /* ignore malformed frame */
      }
    });
    // The browser reconnects automatically; nothing to do on error.
    source.addEventListener("error", function () {});
  }

  function nodeWebBase(node) {
    var webPath = node.kind === "runtime-server" ? "/" : "/web/";
    return "/n/" + encodeURIComponent(node.node_id) + webPath;
  }

  function sessionOpenTarget(node, sessionID) {
    var base = nodeWebBase(node);
    // runtime-server's SPA manages its own session list; aicli takes ?session=.
    if (node.kind === "runtime-server") { return base; }
    return base + "?session=" + encodeURIComponent(sessionID);
  }

  // --- Attention inbox / toast (docs/gateway/08-notifications.md §5.2) ------
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

  var seenAttention = {};
  var attentionSeeded = false;

  // trackAttention diffs the current attention set against the last render and
  // raises a toast for newly attentive sessions (the first load only seeds).
  function trackAttention(nodes) {
    var current = {};
    var fresh = [];
    (nodes || []).forEach(function (node) {
      (node.sessions || []).forEach(function (session) {
        if (!session.attention) { return; }
        var key = node.node_id + "|" + session.session_id + "|" + session.attention;
        current[key] = true;
        if (!seenAttention[key]) { fresh.push({ node: node, session: session }); }
      });
    });
    var seeded = attentionSeeded;
    seenAttention = current;
    attentionSeeded = true;
    if (!seeded) { return; }
    fresh.forEach(function (item) { showAttentionToast(item.node, item.session); });
  }

  function showAttentionToast(node, session) {
    var toast = document.createElement("div");
    toast.className = "toast";
    toast.textContent =
      (node.name || node.node_id) + " · " +
      (session.title || session.session_id) + "：" +
      attentionLabel(session.attention);
    toast.addEventListener("click", function () {
      window.open(sessionOpenTarget(node, session.session_id), "_blank");
      toast.remove();
    });
    document.body.appendChild(toast);
    setTimeout(function () { toast.remove(); }, 8000);
  }

  function renderNodes(nodes) {
    trackAttention(nodes);
    var list = el("node-list");
    list.innerHTML = "";
    if (!nodes.length) {
      list.innerHTML = '<p class="badge">暂无已注册设备。</p>';
      return;
    }
    nodes.forEach(function (node) {
      var sessions = node.sessions || [];
      var card = document.createElement("div");
      card.className = "card";
      card.innerHTML =
        '<div class="card-head"><span class="card-title"></span><span class="badge"></span></div>' +
        '<div class="card-meta"></div>' +
        '<div class="card-actions"></div>';
      card.querySelector(".card-title").textContent = node.name || node.device_name || node.node_id;
      card.querySelector(".badge").textContent =
        (node.kind || "agent") + " · 会话 " + sessions.length;
      card.querySelector(".card-meta").textContent = node.node_id;
      var actions = card.querySelector(".card-actions");

      if (sessions.length) {
        sessions.forEach(function (session) {
          var row = document.createElement("div");
          row.className = "session-row";
          row.innerHTML = '<span class="session-title"></span><span class="attention" hidden></span><button class="secondary" type="button">打开</button>';
          row.querySelector(".session-title").textContent = session.title || session.session_id;
          if (session.attention) {
            var badge = row.querySelector(".attention");
            badge.textContent = attentionLabel(session.attention);
            badge.hidden = false;
          }
          row.querySelector("button").addEventListener("click", function () {
            rememberPresence(node, session);
            window.open(sessionOpenTarget(node, session.session_id), "_blank");
          });
          actions.appendChild(row);
        });
      } else {
        var open = document.createElement("button");
        open.className = "secondary";
        open.type = "button";
        open.textContent = "打开";
        open.addEventListener("click", function () {
          rememberPresence(node, null);
          window.open(nodeWebBase(node), "_blank");
        });
        actions.appendChild(open);
      }
      list.appendChild(card);
    });
  }

  function loadDevices() {
    return apiJSON("/api/devices").then(function (r) {
      if (r.res.status === 401) { setAuthed(false); return; }
      renderDevices((r.body && r.body.devices) || []);
    });
  }

  function renderDevices(devices) {
    var list = el("device-list");
    list.innerHTML = "";
    if (!devices.length) {
      list.innerHTML = '<p class="badge">暂无设备令牌。</p>';
      return;
    }
    devices.forEach(function (d) {
      var card = document.createElement("div");
      card.className = "card";
      card.innerHTML =
        '<div class="card-head"><span class="card-title"></span><span class="badge"></span></div>' +
        '<div class="card-meta"></div>' +
        '<div class="card-actions"><button class="danger" data-revoke>撤销</button></div>';
      card.querySelector(".card-title").textContent = d.name || d.id;
      card.querySelector(".badge").textContent = d.revoked ? "已撤销" : d.prefix + "…";
      card.querySelector(".card-meta").textContent = "id " + d.id;
      card.querySelector("[data-revoke]").addEventListener("click", function () {
        api("/api/devices/" + encodeURIComponent(d.id), { method: "DELETE" }).then(loadDevices);
      });
      list.appendChild(card);
    });
  }

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
      loadNodes();
      loadDevices();
    });
  });

  el("logout").addEventListener("click", function () {
    api("/api/auth/logout", { method: "POST" }).then(function () { setAuthed(false); });
  });

  el("refresh-nodes").addEventListener("click", loadNodes);
  el("refresh-devices").addEventListener("click", loadDevices);

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
        tokenEl.textContent = "令牌（仅显示一次）：" + r.body.token;
        tokenEl.hidden = false;
      }
      loadDevices();
    });
  });

  // --- Web Push notifications (docs/gateway/08-notifications.md) ------------
  var pushRegistration = null;

  function urlBase64ToUint8Array(base64) {
    var padding = "=".repeat((4 - (base64.length % 4)) % 4);
    var normalized = (base64 + padding).replace(/-/g, "+").replace(/_/g, "/");
    var raw = atob(normalized);
    var out = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) { out[i] = raw.charCodeAt(i); }
    return out;
  }

  function setPushLabel(text) { el("push-toggle").textContent = text; }

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
      .catch(function () { /* notifications are optional */ });
  }

  function enablePush() {
    return apiJSON("/api/push/config").then(function (r) {
      if (!r.body || !r.body.enabled || !r.body.public_key) { return null; }
      return navigator.serviceWorker.ready.then(function (reg) {
        pushRegistration = reg;
        return reg.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: urlBase64ToUint8Array(r.body.public_key),
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
      }).then(function () { setPushLabel("关闭通知"); });
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

  el("push-toggle").addEventListener("click", function () {
    var enabling = el("push-toggle").textContent !== "关闭通知";
    Promise.resolve(enabling ? enablePush() : disablePush()).catch(function () {
      el("login-error").textContent = "通知操作失败（请检查浏览器权限）";
      el("login-error").hidden = false;
    });
  });

  // --- Presence: suppress notifications for what the user is watching --------
  // (docs/gateway/08-notifications.md §5.4). We re-assert presence on an
  // interval while the tab is visible; a hidden/closed tab simply stops, and the
  // gateway's presence TTL expires.
  var presenceTargets = {};

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
      }).catch(function () { /* presence is best-effort */ });
    });
  }

  setInterval(sendPresence, 30000);
  document.addEventListener("visibilitychange", function () {
    if (!document.hidden) { sendPresence(); }
  });

  loadNodes();
  loadDevices();
  initPush();

  if ("serviceWorker" in navigator) {
    window.addEventListener("load", function () {
      navigator.serviceWorker.register("./sw.js").catch(function () {
        /* offline shell is an enhancement; ignore failures */
      });
    });
  }
})();
