  /* ──────────── connectors: QR login (start_login → poll → refill) ─────────── */
  // 与 aicli micro web（commands/web/js/connectors.js）同口径：
  //   start_login → 渲染二维码（data:image 直出；链接走本机 /api/qr 编码）
  //   → 每 2s 轮询 login_status（后端长轮询）→ confirmed/expired 停止
  //   → confirmed 且 credential_ready 时把 settings/secrets 合并 PATCH 一次。
  var connectorQRState = {
    connectorID: "", name: "", actionKey: "start_login",
    seq: 0, timer: null, loginID: "", content: "",
    closed: false, payload: null, draft: false, refilled: false, busy: false,
  };

  function connectorHasQRAction(ps) {
    var actions = (ps && ps.actions) || [];
    return actions.some(function (a) { return a && a.kind === "qr"; });
  }

  function connectorFirstQRAction(ps) {
    var actions = (ps && ps.actions) || [];
    for (var i = 0; i < actions.length; i++) {
      if (actions[i] && actions[i].kind === "qr") { return actions[i]; }
    }
    return null;
  }

  function connectorQRDirectImage(content) {
    var text = (content || "").trim();
    if (!text) { return false; }
    if (/^data:image\//i.test(text)) { return true; }
    return /^https?:\/\//i.test(text) && /\.(png|jpe?g|gif|webp|bmp|svg)([?#]|$)/i.test(text);
  }

  function connectorQRStatusText(status) {
    switch (status) {
      case "wait": return "等待扫码…";
      case "scanned": return "已扫码，请在手机上确认…";
      case "confirmed": return "已确认";
      case "expired": return "二维码已过期，请点「重新获取二维码」";
      default: return status || "正在获取二维码…";
    }
  }

  function connectorQRShowError(text) {
    var box = el("connector-qr-error");
    if (!box) { return; }
    if (!text) { box.hidden = true; box.textContent = ""; return; }
    box.hidden = false;
    box.textContent = text;
  }

  function stopConnectorQRPoll() {
    if (connectorQRState.timer) {
      clearTimeout(connectorQRState.timer);
      connectorQRState.timer = null;
    }
  }

  function scheduleConnectorQRPoll(delay) {
    var st = connectorQRState;
    if (!st || st.closed || !st.connectorID) { return; }
    stopConnectorQRPoll();
    st.timer = setTimeout(function () { pollConnectorQRLogin(st); }, delay);
  }

  function renderConnectorQR() {
    var st = connectorQRState;
    if (!st) { return; }
    var status = st.payload && st.payload.status ? st.payload.status : "";
    var statusEl = el("connector-qr-status");
    if (statusEl) {
      statusEl.textContent = connectorQRStatusText(status);
      statusEl.setAttribute("data-status", status || "starting");
    }
    var content = (st.payload && st.payload.qr_code_content) || st.content || "";
    st.content = content;
    var wrap = el("connector-qr-image-wrap");
    var img = el("connector-qr-image");
    var linkWrap = el("connector-qr-link-wrap");
    var link = el("connector-qr-link");
    var copyBtn = el("connector-qr-copy");
    if (content) {
      if (wrap) { wrap.hidden = false; }
      if (img) {
        img.onerror = function () {
          img.hidden = true;
          if (wrap) { wrap.hidden = true; }
          if (linkWrap) { linkWrap.hidden = false; }
        };
        img.onload = function () { img.hidden = false; if (wrap) { wrap.hidden = false; } };
        img.src = connectorQRDirectImage(content) ? content : ("/api/qr?text=" + encodeURIComponent(content));
        img.hidden = false;
      }
      if (link) { link.textContent = content; }
      if (linkWrap) { linkWrap.hidden = connectorQRDirectImage(content); }
      if (copyBtn) { copyBtn.hidden = connectorQRDirectImage(content); }
    } else {
      if (wrap) { wrap.hidden = true; }
      if (linkWrap) { linkWrap.hidden = true; }
      if (copyBtn) { copyBtn.hidden = true; }
    }
    var meta = el("connector-qr-meta");
    if (meta) {
      var bits = [];
      if (st.payload && st.payload.expires_at) { bits.push("有效期至 " + st.payload.expires_at); }
      if (st.payload && st.payload.refresh_count) { bits.push("已自动刷新 " + st.payload.refresh_count + " 次"); }
      if (status === "confirmed" && st.payload && st.payload.credential_ready === false) { bits.push("等待凭据就绪"); }
      meta.textContent = bits.join(" · ");
      meta.hidden = !bits.length;
    }
  }

  function startConnectorQRLogin() {
    var st = connectorQRState;
    if (!st || st.closed || !st.connectorID) { return; }
    stopConnectorQRPoll();
    var seq = ++st.seq;
    st.payload = null;
    st.loginID = "";
    st.refilled = false;
    connectorQRShowError("");
    var statusEl = el("connector-qr-status");
    if (statusEl) { statusEl.textContent = "正在获取二维码…"; statusEl.setAttribute("data-status", "starting"); }
    var restart = el("connector-qr-restart");
    if (restart) { restart.disabled = true; }
    apiJSON("/api/connectors/" + encodeURIComponent(st.connectorID) + "/actions/" + encodeURIComponent(st.actionKey), { method: "POST" }).then(function (r) {
      if (!connectorQRState || connectorQRState !== st || st.closed || seq !== st.seq) { return; }
      if (restart) { restart.disabled = false; }
      if (!r.res.ok) {
        connectorQRShowError("获取二维码失败：" + ((r.body && r.body.error) || ("HTTP " + r.res.status)));
        return;
      }
      st.payload = r.body || {};
      st.loginID = st.payload.login_id || "";
      renderConnectorQR();
      scheduleConnectorQRPoll(2000);
    }).catch(function (err) {
      if (!connectorQRState || connectorQRState !== st || st.closed || seq !== st.seq) { return; }
      if (restart) { restart.disabled = false; }
      connectorQRShowError("获取二维码失败：" + (err && err.message ? err.message : err));
    });
  }

  function pollConnectorQRLogin(st) {
    if (!connectorQRState || connectorQRState !== st || st.closed || !st.loginID) { return; }
    var expiresAt = st.payload && st.payload.expires_at ? Date.parse(st.payload.expires_at) : NaN;
    if (!isNaN(expiresAt) && expiresAt <= Date.now()) {
      st.payload = Object.assign({}, st.payload, { status: "expired" });
      renderConnectorQR();
      return;
    }
    var seq = st.seq;
    apiJSON("/api/connectors/" + encodeURIComponent(st.connectorID) + "/actions/login_status", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ login_id: st.loginID }),
    }).then(function (r) {
      if (!connectorQRState || connectorQRState !== st || st.closed || seq !== st.seq) { return; }
      if (!r.res.ok) {
        // 400（登录会话不存在/已清理）重试无意义；其余（网络抖动）继续轮询。
        if (r.res.status === 400) {
          connectorQRShowError("轮询失败：" + ((r.body && r.body.error) || "登录会话已失效"));
          return;
        }
        scheduleConnectorQRPoll(3000);
        return;
      }
      st.payload = r.body || {};
      renderConnectorQR();
      var status = st.payload.status || "";
      if (status === "confirmed") {
        finishConnectorQRLogin(st);
        return;
      }
      if (status === "expired") { return; }
      scheduleConnectorQRPoll(2000);
    }).catch(function () {
      if (!connectorQRState || connectorQRState !== st || st.closed || seq !== st.seq) { return; }
      scheduleConnectorQRPoll(3000);
    });
  }

  function finishConnectorQRLogin(st) {
    stopConnectorQRPoll();
    var data = st.payload || {};
    if (data.credential_ready !== true) {
      notify("已确认登录，等待凭据就绪（可稍后重新打开查看）", "ok");
      closeConnectorQRDialog();
      loadConnectors(true).then(function () { return loadConnectorBindings(); });
      return;
    }
    st.busy = true;
    var done = el("connector-qr-done");
    if (done) { done.disabled = true; }
    apiJSON("/api/connectors/" + encodeURIComponent(st.connectorID), {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ settings: data.settings || {}, secrets: data.secrets || {} }),
    }).then(function (r) {
      st.busy = false;
      if (done) { done.disabled = false; }
      if (!r.res.ok) {
        connectorQRShowError("凭据回填失败：" + ((r.body && r.body.error) || ("HTTP " + r.res.status)) + "（可稍后在编辑里手动填写）");
        return;
      }
      st.refilled = true;
      var result = r.body || {};
      if (result.adopted && result.removed_id) {
        st.connectorID = result.id || st.connectorID;
        var superseded = result.superseded_account ? ("，原连接 " + result.superseded_account + " 已由微信解除") : "";
        notify("扫码登录成功：识别为既有机器人 " + (result.id || "") + "（同一 owner），已更新连接" + superseded + "；临时 " + result.removed_id + " 已清理", "ok");
      } else {
        notify("扫码登录成功，凭据已回填", "ok");
      }
      closeConnectorQRDialog();
      loadConnectors(true).then(function () { return loadConnectorBindings(); });
    }).catch(function (err) {
      st.busy = false;
      if (done) { done.disabled = false; }
      connectorQRShowError("凭据回填失败：" + (err && err.message ? err.message : err));
    });
  }

  function openConnectorQRDialog(item, action, opts) {
    opts = opts || {};
    stopConnectorQRPoll();
    connectorQRState = {
      connectorID: item.id,
      name: item.name || item.id,
      actionKey: (action && action.key) || "start_login",
      seq: 0, timer: null, loginID: "", content: "",
      closed: false, payload: null,
      draft: !!opts.draft, refilled: false, busy: false,
    };
    var title = el("connector-qr-title");
    if (title) { title.textContent = "扫码登录 · " + connectorQRState.name; }
    var sub = el("connector-qr-sub");
    if (sub) { sub.textContent = (action && action.label) || "扫码登录"; }
    connectorQRShowError("");
    var statusEl = el("connector-qr-status");
    if (statusEl) { statusEl.textContent = "正在获取二维码…"; statusEl.setAttribute("data-status", "starting"); }
    var wrap = el("connector-qr-image-wrap"); if (wrap) { wrap.hidden = true; }
    var img = el("connector-qr-image"); if (img) { img.removeAttribute("src"); img.hidden = true; }
    var linkWrap = el("connector-qr-link-wrap"); if (linkWrap) { linkWrap.hidden = true; }
    var meta = el("connector-qr-meta"); if (meta) { meta.hidden = true; meta.textContent = ""; }
    var copyBtn = el("connector-qr-copy"); if (copyBtn) { copyBtn.hidden = true; }
    var done = el("connector-qr-done"); if (done) { done.disabled = false; }
    var restart = el("connector-qr-restart"); if (restart) { restart.disabled = false; }
    openModal("connector-qr-modal");
    startConnectorQRLogin();
  }

  function closeConnectorQRDialog() {
    var st = connectorQRState;
    if (!st) { closeModal("connector-qr-modal"); return; }
    if (st.busy) { return; } // 凭据回填保存中拦截关闭
    st.closed = true;
    stopConnectorQRPoll();
    // 新建扫码流在未确认且未回填时清理草稿连接器（与 micro web 同语义）；
    // 已 confirmed 的实体即使 credential_ready=false 也保留，便于稍后重试。
    var draftID = st.draft && !st.refilled &&
      !(st.payload && st.payload.status === "confirmed") ? st.connectorID : "";
    closeModal("connector-qr-modal");
    if (draftID) {
      apiJSON("/api/connectors/" + encodeURIComponent(draftID), { method: "DELETE" }).then(function () {
        notify("已清理未完成的扫码连接器", "ok");
        loadConnectors(true);
      }).catch(function () { loadConnectors(true); });
    }
  }

  // connectorGenerateID 与 micro web/React 同口径：<provider>-<base36 时间><4 随机>。
  function connectorGenerateID(provider) {
    var prefix = String(provider || "conn").toLowerCase().replace(/[^a-z0-9]/g, "").slice(0, 16) || "conn";
    var time = Date.now().toString(36);
    var rand = Math.random().toString(36).slice(2, 6);
    return prefix + "-" + time + rand;
  }

  function startConnectorQRFromEditor() {
    var form = connectorForm;
    if (!form) { return; }
    var ps = connectorProviderSchemaOf(form);
    var action = connectorFirstQRAction(ps);
    if (!ps || !action) { notify("该通道不支持扫码登录", "error"); return; }
    var errBox = el("connector-editor-error");

    if (connectorEditing) {
      var item = connectorEditing;
      closeConnectorEditor();
      openConnectorQRDialog(item, action, { draft: false });
      return;
    }

    if (!(form.id || "").trim()) { form.id = connectorGenerateID(ps.provider); }
    var errors = {};
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test((form.id || "").trim())) { errors.id = "连接器 ID 不合法"; }
    (ps.fields || []).forEach(function (field) {
      if (!field.required || field.type === "secret") { return; } // 扫码路径不要求先填密钥
      if (!(form.values[field.key] || "").trim()) { errors[field.key] = (field.label || field.key) + " 为必填项"; }
    });
    if ((form.policyText || "").trim()) {
      try { JSON.parse(form.policyText); } catch (e) { errors.policy = "策略不是合法 JSON：" + e.message; }
    }
    connectorFormErrors = errors;
    renderConnectorEditorForm();
    if (Object.keys(errors).length) {
      if (errBox) { errBox.hidden = false; errBox.textContent = "请修正标红字段后再扫码。"; }
      return;
    }

    var payload;
    try {
      payload = connectorBuildPayload(form, ps, true);
      delete payload.secrets; // 凭据由扫码回填
    } catch (e) {
      connectorFormErrors.policy = e.message;
      renderConnectorEditorForm();
      return;
    }
    var saveBtn = el("connector-editor-save");
    if (saveBtn) { saveBtn.disabled = true; saveBtn.textContent = "创建中…"; }
    apiJSON("/api/connectors", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    }).then(function (r) {
      if (saveBtn) { saveBtn.disabled = false; saveBtn.textContent = "创建连接器"; }
      if (!r.res.ok) {
        connectorFormErrors = connectorFieldErrorsFrom(r.body);
        renderConnectorEditorForm();
        if (errBox) {
          errBox.hidden = false;
          errBox.textContent = (r.body && (r.body.error || r.body.message)) || ("创建失败：HTTP " + r.res.status);
        }
        return;
      }
      var created = r.body || {};
      if (!created.id) { created = { id: payload.id, name: payload.name, provider: ps.provider }; }
      closeConnectorEditor();
      notify("已创建扫码连接器 " + (created.id || payload.id), "ok");
      openConnectorQRDialog(created, action, { draft: true });
    }).catch(function (err) {
      if (saveBtn) { saveBtn.disabled = false; saveBtn.textContent = "创建连接器"; }
      if (errBox) { errBox.hidden = false; errBox.textContent = "创建失败：" + (err && err.message ? err.message : err); }
    });
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
        if (currentView() === "connectors" && connectorGatewaySettled) {
          refreshConnectorNodes(false);
        }
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
      // SSE 断开也可能是 owner 会话过期（/api/events 受 RequireSession 保护）。
      // 心跳会用一个轻量管理面请求复检，命中 401 就把用户送回登录页。
      authHeartbeat();
    });
  }

