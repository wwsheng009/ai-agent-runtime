(function () {
  "use strict";

  var el = function (id) { return document.getElementById(id); };

  function setAuthed(authed) {
    el("login").hidden = authed;
    el("nodes").hidden = !authed;
    el("devices").hidden = !authed;
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
    });
  }

  function renderNodes(nodes) {
    var list = el("node-list");
    list.innerHTML = "";
    if (!nodes.length) {
      list.innerHTML = '<p class="badge">暂无已注册设备。</p>';
      return;
    }
    nodes.forEach(function (node) {
      var webPath = node.kind === "runtime-server" ? "/" : "/web/";
      var target = "/n/" + encodeURIComponent(node.node_id) + webPath;
      var card = document.createElement("div");
      card.className = "card";
      card.innerHTML =
        '<div class="card-head"><span class="card-title"></span><span class="badge"></span></div>' +
        '<div class="card-meta"></div>' +
        '<div class="card-actions">' +
        '<button class="secondary" data-open>打开会话</button>' +
        '<button class="secondary" data-newtab>新标签打开</button>' +
        "</div>";
      card.querySelector(".card-title").textContent = node.name || node.device_name || node.node_id;
      card.querySelector(".badge").textContent =
        (node.kind || "agent") + " · 会话 " + ((node.sessions || []).length);
      card.querySelector(".card-meta").textContent = node.node_id;
      card.querySelector("[data-open]").addEventListener("click", function () { window.location.href = target; });
      card.querySelector("[data-newtab]").addEventListener("click", function () { window.open(target, "_blank"); });
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

  loadNodes();
  loadDevices();
})();
