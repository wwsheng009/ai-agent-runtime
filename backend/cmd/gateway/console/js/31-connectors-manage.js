// 31-connectors-manage.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=103";

Object.assign(G, {
  connectorNotice: connectorNotice,
  connectorSetBusy: connectorSetBusy,
  connectorIsBusy: connectorIsBusy,
  connectorProviderOptions: connectorProviderOptions,
  connectorDefaultValues: connectorDefaultValues,
  connectorHasModeField: connectorHasModeField,
  connectorEmptyForm: connectorEmptyForm,
  connectorFormFromView: connectorFormFromView,
  connectorProviderSchemaOf: connectorProviderSchemaOf,
  connectorSecretSet: connectorSecretSet,
  connectorFieldControl: connectorFieldControl,
  connectorErrorLine: connectorErrorLine,
  connectorFieldRow: connectorFieldRow,
  connectorSimpleRow: connectorSimpleRow,
  renderConnectorEditorForm: renderConnectorEditorForm,
  connectorBindingRows: connectorBindingRows,
  connectorBindingLabel: connectorBindingLabel,
  loadConnectorBindings: loadConnectorBindings,
  syncConnectorIDOptions: syncConnectorIDOptions,
  ensureGatewayConnectorOptions: ensureGatewayConnectorOptions,
  renderConnectorBindings: renderConnectorBindings,
  unbindConnectorChat: unbindConnectorChat,
  openConnectorEditor: openConnectorEditor,
  closeConnectorEditor: closeConnectorEditor,
  connectorValidate: connectorValidate,
  connectorBuildPayload: connectorBuildPayload,
  connectorFieldErrorsFrom: connectorFieldErrorsFrom,
  saveConnectorEditor: saveConnectorEditor,
  connectorActionBtn: connectorActionBtn,
  connectorToggle: connectorToggle,
  connectorSetRuntimeMode: connectorSetRuntimeMode,
  connectorReload: connectorReload,
  connectorDelete: connectorDelete,
  connectorTakeover: connectorTakeover,
  connectorMigratedEntry: connectorMigratedEntry,
  connectorNodeEntryForGateway: connectorNodeEntryForGateway,
  shortStableHash: shortStableHash,
  friendlyConnectorError: friendlyConnectorError,
  manageNotifyTargets: manageNotifyTargets,
  rollbackConnectorMigration: rollbackConnectorMigration,
  connectorMigrate: connectorMigrate,
  openConnectorTest: openConnectorTest,
  sendConnectorTest: sendConnectorTest,
  openConnectorLogs: openConnectorLogs,
  loadConnectorLogs: loadConnectorLogs,
  renderConnectorLogs: renderConnectorLogs,
  connectorActionImage: connectorActionImage,
  renderConnectorActionResult: renderConnectorActionResult,
  openConnectorActionResult: openConnectorActionResult,
  connectorLoginStatus: connectorLoginStatus,
});

  /* ──────────── connectors: management (create/edit/test/…) ─────────── */
  // 数据面：GET/POST /api/connectors、PATCH/DELETE /api/connectors/{id}、
  // POST /{id}/reload|test、GET /{id}/logs、POST /{id}/actions/{action}、
  // GET /api/connectors/bindings + PUT/DELETE /{id}/bindings/{chat}。
  // 表单完全由 GET /api/connectors/schema 驱动（docs/bots/07 §7）。

  S.connectorBindings = [];
  S.connectorBindingsError = "";
  S.connectorBusy = {};
  S.connectorEditing = null;
  S.connectorForm = null;
  S.connectorFormErrors = {};
  S.connectorSaving = false;
  S.connectorLogsState = { id: "", name: "", entries: [], stats: null, loading: false };
  S.connectorTestState = { id: "", name: "", busy: false };
  S.connectorActionState = { id: "", name: "", action: "", payload: null, busy: false };

  function connectorNotice(text) {
    var box = S.el("connectors-notice");
    if (!box) { return; }
    if (!text) { box.hidden = true; box.textContent = ""; return; }
    box.hidden = false;
    box.textContent = text;
  }

  function connectorSetBusy(id, busy) {
    if (busy) { S.connectorBusy[id] = true; } else { delete S.connectorBusy[id]; }
  }

  function connectorIsBusy(id) { return !!S.connectorBusy[id]; }

  function connectorProviderOptions() {
    return S.connectorSchemaProviders || [];
  }

  function connectorDefaultValues(providerSchema) {
    var values = {};
    var fields = (providerSchema && providerSchema.fields) || [];
    fields.forEach(function (field) { values[field.key] = field.default || ""; });
    return values;
  }

  function connectorHasModeField(providerSchema) {
    var fields = (providerSchema && providerSchema.fields) || [];
    return fields.some(function (field) { return field.key === "mode"; });
  }

  function connectorEmptyForm() {
    var providers = connectorProviderOptions();
    var first = providers.length ? providers[0] : null;
    var modes = (first && first.modes) || [];
    return {
      id: "",
      provider: first ? first.provider : "",
      name: "",
      enabled: true,
      mode: modes.length ? modes[0] : "",
      values: connectorDefaultValues(first),
      clearedSecrets: {},
      policyText: "{}",
      skipCredentialCheck: false,
    };
  }

  function connectorFormFromView(view) {
    var ps = G.connectorSchemaFor(view.provider);
    var values = {};
    var settings = (view && view.settings) || {};
    Object.keys(settings).forEach(function (key) { values[key] = String(settings[key]); });
    if (connectorHasModeField(ps) && !values.mode) { values.mode = view.mode || ""; }
    var policyText = "{}";
    try { policyText = JSON.stringify(view.policy || {}, null, 2); } catch (err) { policyText = "{}"; }
    return {
      id: view.id,
      provider: view.provider,
      name: view.name || "",
      enabled: view.enabled !== false,
      mode: view.mode || "",
      values: values,
      clearedSecrets: {},
      policyText: policyText,
      skipCredentialCheck: false,
    };
  }

  function connectorProviderSchemaOf(form) {
    return G.connectorSchemaFor(form && form.provider);
  }

  function connectorSecretSet(key) {
    if (!S.connectorEditing) { return false; }
    var keys = S.connectorEditing.secret_keys || [];
    return keys.indexOf(key) >= 0;
  }

  function connectorFieldControl(field, form) {
    var value = form.values[field.key] == null ? "" : String(form.values[field.key]);
    if (field.type === "secret") {
      var wrap = document.createElement("div");
      wrap.className = "connector-secret";
      var input = document.createElement("input");
      input.type = "password";
      input.autocomplete = "new-password";
      input.placeholder = connectorSecretSet(field.key) ? "已设置（留空保持不变）" : (field.placeholder || "");
      input.value = value;
      input.addEventListener("input", function () {
        form.values[field.key] = input.value;
        delete S.connectorFormErrors[field.key];
      });
      wrap.appendChild(input);
      if (connectorSecretSet(field.key)) {
        var clearLine = document.createElement("label");
        clearLine.className = "check-line";
        var clearBox = document.createElement("input");
        clearBox.type = "checkbox";
        clearBox.checked = !!form.clearedSecrets[field.key];
        clearBox.addEventListener("change", function () {
          form.clearedSecrets[field.key] = clearBox.checked;
          if (clearBox.checked) { input.value = ""; form.values[field.key] = ""; }
        });
        clearLine.appendChild(clearBox);
        clearLine.appendChild(document.createTextNode(" 清除已保存的密钥"));
        wrap.appendChild(clearLine);
      }
      return wrap;
    }
    if (field.type === "bool") {
      var line = document.createElement("label");
      line.className = "check-line";
      var box = document.createElement("input");
      box.type = "checkbox";
      box.checked = value === "true";
      box.addEventListener("change", function () {
        form.values[field.key] = box.checked ? "true" : "false";
        delete S.connectorFormErrors[field.key];
      });
      line.appendChild(box);
      line.appendChild(document.createTextNode(" 开启"));
      return line;
    }
    if (field.type === "enum") {
      var sel = document.createElement("select");
      sel.className = "select";
      var options = field.options || [];
      options.forEach(function (opt) {
        var o = document.createElement("option");
        o.value = opt.value;
        o.textContent = opt.label || opt.value;
        sel.appendChild(o);
      });
      if (value !== "" && !options.some(function (opt) { return opt.value === value; })) {
        var extra = document.createElement("option");
        extra.value = value;
        extra.textContent = value;
        sel.appendChild(extra);
      }
      sel.value = value;
      sel.addEventListener("change", function () {
        form.values[field.key] = sel.value;
        delete S.connectorFormErrors[field.key];
      });
      return sel;
    }
    var input2 = document.createElement("input");
    input2.type = field.type === "number" ? "number" : (field.type === "url" ? "url" : "text");
    if (field.type === "number") { input2.step = "any"; }
    input2.placeholder = field.placeholder || "";
    input2.value = value;
    input2.autocomplete = "off";
    input2.addEventListener("input", function () {
      form.values[field.key] = input2.value;
      delete S.connectorFormErrors[field.key];
    });
    return input2;
  }

  function connectorErrorLine(key) {
    var err = document.createElement("p");
    err.className = "field-error";
    err.textContent = S.connectorFormErrors[key] || "";
    err.hidden = !S.connectorFormErrors[key];
    return err;
  }

  function connectorFieldRow(field, form) {
    var row = document.createElement("div");
    row.className = "field";
    var label = document.createElement("label");
    label.className = "field-label";
    label.textContent = (field.label || field.key) + (field.required ? " *" : "");
    row.appendChild(label);
    row.appendChild(connectorFieldControl(field, form));
    if (field.help) {
      var help = document.createElement("p");
      help.className = "field-hint";
      help.textContent = field.help;
      row.appendChild(help);
    }
    row.appendChild(connectorErrorLine(field.key));
    return row;
  }

  function connectorSimpleRow(labelText, control, key) {
    var row = document.createElement("div");
    row.className = "field";
    var label = document.createElement("label");
    label.className = "field-label";
    label.textContent = labelText;
    row.appendChild(label);
    row.appendChild(control);
    row.appendChild(connectorErrorLine(key || ""));
    return row;
  }

  function renderConnectorEditorForm() {
    var host = S.el("connector-editor-form");
    if (!host || !S.connectorForm) { return; }
    host.innerHTML = "";
    var form = S.connectorForm;
    var ps = connectorProviderSchemaOf(form);

    if (S.connectorEditing) {
      var prov = document.createElement("input");
      prov.type = "text";
      prov.value = form.provider;
      prov.disabled = true;
      host.appendChild(connectorSimpleRow("通道（不可修改）", prov, "provider"));
    } else {
      var sel = document.createElement("select");
      sel.className = "select";
      connectorProviderOptions().forEach(function (p) {
        var o = document.createElement("option");
        o.value = p.provider;
        o.textContent = (p.label || p.provider) + "（" + p.provider + "）";
        sel.appendChild(o);
      });
      sel.value = form.provider;
      sel.addEventListener("change", function () {
        form.provider = sel.value;
        var next = G.connectorSchemaFor(sel.value);
        form.mode = next && next.modes && next.modes.length ? next.modes[0] : "";
        form.values = connectorDefaultValues(next);
        form.clearedSecrets = {};
        S.connectorFormErrors = {};
        renderConnectorEditorForm();
      });
      host.appendChild(connectorSimpleRow("通道", sel, "provider"));
    }

    if (G.connectorHasQRAction(ps)) {
      var qrEntry = document.createElement("div");
      qrEntry.className = "connector-qr-entry";
      var qrHint = document.createElement("p");
      qrHint.className = "connectors-hint";
      qrHint.textContent = S.connectorEditing
        ? "重新扫码只替换登录凭据（bot_token / 账号），名称、策略与已有绑定保持不变。"
        : "扫码通道无需手动填写 Token：点下方按钮自动创建连接器并出二维码，确认后凭据自动回填。";
      qrEntry.appendChild(qrHint);
      var qrStartBtn = document.createElement("button");
      qrStartBtn.type = "button";
      qrStartBtn.className = "btn sm";
      qrStartBtn.textContent = S.connectorEditing ? "重新扫码（替换凭据）" : "扫码登录（自动创建）";
      qrStartBtn.addEventListener("click", G.startConnectorQRFromEditor);
      qrEntry.appendChild(qrStartBtn);
      host.appendChild(qrEntry);
    }

    var idInput = document.createElement("input");
    idInput.type = "text";
    idInput.value = form.id;
    idInput.disabled = !!S.connectorEditing;
    idInput.placeholder = "如 tg-ops / qq-ops";
    idInput.autocomplete = "off";
    idInput.addEventListener("input", function () {
      form.id = idInput.value;
      delete S.connectorFormErrors.id;
    });
    host.appendChild(connectorSimpleRow("连接器 ID" + (S.connectorEditing ? "（不可修改）" : " *"), idInput, "id"));

    var nameInput = document.createElement("input");
    nameInput.type = "text";
    nameInput.value = form.name;
    nameInput.placeholder = "显示名（留空 = 使用 ID）";
    nameInput.autocomplete = "off";
    nameInput.addEventListener("input", function () {
      form.name = nameInput.value;
      delete S.connectorFormErrors.name;
    });
    host.appendChild(connectorSimpleRow("名称", nameInput, "name"));

    if (!connectorHasModeField(ps)) {
      var modes = (ps && ps.modes) || [];
      var modeSel = document.createElement("select");
      modeSel.className = "select";
      modes.forEach(function (m) {
        var o = document.createElement("option");
        o.value = m;
        o.textContent = m;
        modeSel.appendChild(o);
      });
      if (form.mode && modes.indexOf(form.mode) < 0) {
        var extra = document.createElement("option");
        extra.value = form.mode;
        extra.textContent = form.mode;
        modeSel.appendChild(extra);
      }
      modeSel.value = form.mode;
      modeSel.addEventListener("change", function () { form.mode = modeSel.value; });
      host.appendChild(connectorSimpleRow("接入模式", modeSel, "mode"));
    }

    var enabledLine = document.createElement("label");
    enabledLine.className = "check-line";
    var enabledBox = document.createElement("input");
    enabledBox.type = "checkbox";
    enabledBox.checked = form.enabled;
    enabledBox.addEventListener("change", function () { form.enabled = enabledBox.checked; });
    enabledLine.appendChild(enabledBox);
    enabledLine.appendChild(document.createTextNode(" 启用（保存后立即应用）"));
    host.appendChild(enabledLine);

    ((ps && ps.fields) || []).forEach(function (field) {
      host.appendChild(connectorFieldRow(field, form));
    });

    var policyWrap = document.createElement("div");
    policyWrap.className = "field";
    var policyLabel = document.createElement("label");
    policyLabel.className = "field-label";
    policyLabel.textContent = "策略（JSON，可选）";
    policyWrap.appendChild(policyLabel);
    var ta = document.createElement("textarea");
    ta.className = "filter-input connector-policy-input";
    ta.rows = 6;
    ta.spellcheck = false;
    ta.value = form.policyText;
    ta.addEventListener("input", function () {
      form.policyText = ta.value;
      delete S.connectorFormErrors.policy;
    });
    policyWrap.appendChild(ta);
    var policyHelp = document.createElement("p");
    policyHelp.className = "field-hint";
    policyHelp.textContent = "对应 REST 的 policy：runtime_owner / command_prefix / allowed_users / session_mode / reply_mode / notify / route / events 等。留空=不修改。";
    policyWrap.appendChild(policyHelp);
    policyWrap.appendChild(connectorErrorLine("policy"));
    host.appendChild(policyWrap);

    var skipLine = document.createElement("label");
    skipLine.className = "check-line";
    var skipBox = document.createElement("input");
    skipBox.type = "checkbox";
    skipBox.checked = form.skipCredentialCheck;
    skipBox.addEventListener("change", function () { form.skipCredentialCheck = skipBox.checked; });
    skipLine.appendChild(skipBox);
    skipLine.appendChild(document.createTextNode(" 跳过保存前凭据校验（平台暂不可达时强制落盘）"));
    host.appendChild(skipLine);

    if (S.connectorEditing) { renderConnectorBindings(); }
  }

  function connectorBindingRows(connectorID) {
    return (S.connectorBindings || []).filter(function (b) { return b.connector_id === connectorID; });
  }

  function connectorBindingLabel(b) {
    var session = (b.session_id || "").trim();
    var node = (b.node_id || "").trim();
    return b.chat + " → " + (session || "（未绑定会话）") + (node ? " @ " + node : "");
  }

  function loadConnectorBindings() {
    return G.apiJSON("/api/connectors/bindings").then(function (r) {
      if (r.res.status === 401) { G.setAuthed(false); return; }
      if (!r.res.ok || !r.body || !Array.isArray(r.body.items)) {
        S.connectorBindings = [];
        S.connectorBindingsError = (r.body && r.body.error) || ("HTTP " + r.res.status);
        return;
      }
      S.connectorBindings = r.body.items;
      S.connectorBindingsError = "";
    }).catch(function (err) {
      S.connectorBindings = [];
      S.connectorBindingsError = err && err.message ? err.message : String(err);
    }).then(function () {
      if (G.currentView() === "connectors") { G.renderConnectors(S.connectorEntries, S.connectorMessage); }
      if (S.connectorEditing) { renderConnectorBindings(); }
    });
  }

  // syncConnectorIDOptions 把网关自身连接器填进 Bot 通道表单的下拉框，
  // 让 operator 不必手记 connector_id。节点条目不属于本宿主 store，不进入列表。
  function syncConnectorIDOptions() {
    var sel = S.el("notify-im-connector");
    if (!sel) { return; }
    var current = sel.value;
    sel.innerHTML = "";
    var placeholder = document.createElement("option");
    placeholder.value = "";
    placeholder.textContent = "选择网关连接器";
    sel.appendChild(placeholder);
    (S.connectorGatewayEntries || []).forEach(function (entry) {
      var item = entry.connector;
      if (!item || !item.id) { return; }
      var opt = document.createElement("option");
      opt.value = item.id;
      opt.textContent = (item.name || item.id) + "（" + item.provider + "）" +
        (item.runtime_mode === "outbound" ? " · 副本" : "");
      sel.appendChild(opt);
    });
    if (current) { sel.value = current; }
  }

  // ensureGatewayConnectorOptions 让直接打开「通知设置」的用户也能拿到连接器
  // 下拉选项（连接器列表是懒加载视图，不能假设用户先去过连接器页）。
  function ensureGatewayConnectorOptions() {
    if (S.connectorGatewayEntries && S.connectorGatewayEntries.length) {
      syncConnectorIDOptions();
      return Promise.resolve();
    }
    return G.loadConnectors(false).then(function () {
      syncConnectorIDOptions();
    }).catch(function () {});
  }

  function renderConnectorBindings() {
    var host = S.el("connector-editor-bindings");
    if (!host) { return; }
    if (!S.connectorEditing) { host.hidden = true; host.innerHTML = ""; return; }
    host.hidden = false;
    host.innerHTML = "";
    var title = document.createElement("div");
    title.className = "connector-bindings-head";
    title.textContent = "绑定会话（聊天 → 会话）";
    host.appendChild(title);
    var rows = connectorBindingRows(S.connectorEditing.id);
    if (!rows.length) {
      var empty = document.createElement("p");
      empty.className = "connectors-hint";
      empty.textContent = "尚未绑定会话。绑定后，该 chat 的入站消息会投递到指定会话。";
      host.appendChild(empty);
    } else {
      rows.forEach(function (b) {
        var row = document.createElement("div");
        row.className = "connector-binding-row";
        var text = document.createElement("span");
        text.className = "connector-binding-label";
        text.textContent = connectorBindingLabel(b);
        row.appendChild(text);
        var del = document.createElement("button");
        del.type = "button";
        del.className = "btn ghost sm";
        del.textContent = "解绑";
        del.addEventListener("click", function () { unbindConnectorChat(S.connectorEditing.id, b.chat, del); });
        row.appendChild(del);
        host.appendChild(row);
      });
    }
    var form = document.createElement("div");
    form.className = "connector-binding-form";
    var chat = document.createElement("input");
    chat.className = "filter-input";
    chat.placeholder = "chat / 群号";
    chat.autocomplete = "off";
    var session = document.createElement("input");
    session.className = "filter-input";
    session.placeholder = "session_id";
    session.autocomplete = "off";
    var node = document.createElement("input");
    node.className = "filter-input";
    node.placeholder = "node_id（可选）";
    node.autocomplete = "off";
    var add = document.createElement("button");
    add.type = "button";
    add.className = "btn ghost sm";
    add.textContent = "绑定";
    add.addEventListener("click", function () {
      var chatValue = (chat.value || "").trim();
      var sessionValue = (session.value || "").trim();
      if (!chatValue || !sessionValue) {
        G.notify("chat 与 session_id 必填", "error");
        return;
      }
      add.disabled = true;
      var body = { session_id: sessionValue };
      var nodeValue = (node.value || "").trim();
      if (nodeValue) { body.node_id = nodeValue; }
      G.apiJSON("/api/connectors/" + encodeURIComponent(S.connectorEditing.id) + "/bindings/" + encodeURIComponent(chatValue), {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      }).then(function (r) {
        add.disabled = false;
        if (!r.res.ok) {
          G.notify("绑定失败：" + ((r.body && r.body.error) || ("HTTP " + r.res.status)), "error");
          return;
        }
        chat.value = ""; session.value = ""; node.value = "";
        G.notify("已绑定 " + chatValue, "ok");
        loadConnectorBindings();
      }).catch(function (err) {
        add.disabled = false;
        G.notify("绑定失败：" + (err && err.message ? err.message : err), "error");
      });
    });
    form.appendChild(chat);
    form.appendChild(session);
    form.appendChild(node);
    form.appendChild(add);
    host.appendChild(form);
  }

  function unbindConnectorChat(connectorID, chat, btn) {
    G.confirmDestructive({
      title: "解除绑定",
      message: "将解绑 " + connectorID + " 的 chat " + chat + "。入站消息不再投递到该会话。",
      confirmText: "解绑",
      trigger: btn,
      onConfirm: function () {
        return G.apiJSON("/api/connectors/" + encodeURIComponent(connectorID) + "/bindings/" + encodeURIComponent(chat), { method: "DELETE" }).then(function (r) {
          if (!r.res.ok) { throw new Error((r.body && r.body.error) || ("HTTP " + r.res.status)); }
        });
      },
    }).then(function (res) {
      if (res.confirmed) { G.notify("已解绑 " + chat, "ok"); loadConnectorBindings(); }
    });
  }

  function openConnectorEditor(view) {
    S.connectorEditing = view || null;
    S.connectorForm = view ? connectorFormFromView(view) : connectorEmptyForm();
    S.connectorFormErrors = {};
    S.connectorSaving = false;
    var err = S.el("connector-editor-error");
    if (err) { err.hidden = true; err.textContent = ""; }
    S.el("connector-editor-title").textContent = view ? "编辑连接器" : "新建连接器";
    var saveBtn = S.el("connector-editor-save");
    if (saveBtn) { saveBtn.disabled = false; saveBtn.textContent = view ? "保存修改" : "创建连接器"; }
    if (!S.connectorSchemaProviders) {
      G.loadConnectorSchema().then(function () {
        if (!S.connectorSchemaProviders) {
          connectorNotice("无法读取连接器 Schema，管理表单不可用。请检查 /api/connectors/schema。");
          return;
        }
        if (!S.connectorForm.provider) { S.connectorForm = connectorEmptyForm(); }
        renderConnectorEditorForm();
      });
    }
    renderConnectorEditorForm();
    G.openModal("connector-editor-modal");
  }

  function closeConnectorEditor() {
    G.closeModal("connector-editor-modal");
    S.connectorEditing = null;
    S.connectorForm = null;
    S.connectorFormErrors = {};
  }

  function connectorValidate(form, ps, isCreate) {
    var errors = {};
    var id = (form.id || "").trim();
    if (isCreate) {
      if (!id) { errors.id = "连接器 ID 必填"; }
      else if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(id)) { errors.id = "只能包含字母、数字、._-（最长 64）"; }
    }
    if (!ps) { errors.provider = "请选择通道"; return errors; }
    var existingSecrets = S.connectorEditing ? (S.connectorEditing.secret_keys || []) : [];
    (ps.fields || []).forEach(function (field) {
      if (!field.required) { return; }
      var value = (form.values[field.key] || "").trim();
      if (field.type === "secret") {
        if (form.clearedSecrets[field.key]) { return; }
        if (value || existingSecrets.indexOf(field.key) >= 0) { return; }
        errors[field.key] = (field.label || field.key) + " 为必填项";
        return;
      }
      if (!value) { errors[field.key] = (field.label || field.key) + " 为必填项"; }
    });
    if ((form.policyText || "").trim()) {
      try { JSON.parse(form.policyText); } catch (e) { errors.policy = "策略不是合法 JSON：" + e.message; }
    }
    return errors;
  }

  function connectorBuildPayload(form, ps, isCreate) {
    var settings = {};
    var secrets = {};
    (ps.fields || []).forEach(function (field) {
      var raw = form.values[field.key] == null ? "" : String(form.values[field.key]);
      if (field.type === "secret") {
        if (form.clearedSecrets[field.key]) { secrets[field.key] = "__clear__"; return; }
        var value = raw.trim();
        if (value) { secrets[field.key] = value; }
        return;
      }
      settings[field.key] = raw;
    });
    var modeField = connectorHasModeField(ps);
    var payload = {
      settings: settings,
      secrets: secrets,
      mode: modeField ? (settings.mode || "") : form.mode,
      enabled: form.enabled !== false,
    };
    if (isCreate) {
      payload.id = (form.id || "").trim();
      payload.provider = ps.provider;
      payload.name = (form.name || "").trim() || payload.id;
    } else {
      payload.name = (form.name || "").trim();
    }
    if ((form.policyText || "").trim()) { payload.policy = JSON.parse(form.policyText); }
    if (form.skipCredentialCheck) { payload.skip_credential_check = true; }
    return payload;
  }

  function connectorFieldErrorsFrom(body) {
    var out = {};
    var fields = body && body.fields;
    if (Array.isArray(fields)) {
      fields.forEach(function (item) {
        if (item && item.key) { out[item.key] = item.message || "校验失败"; }
      });
    }
    return out;
  }

  function saveConnectorEditor() {
    if (S.connectorSaving || !S.connectorForm) { return; }
    var ps = connectorProviderSchemaOf(S.connectorForm);
    var isCreate = !S.connectorEditing;
    S.connectorFormErrors = connectorValidate(S.connectorForm, ps, isCreate);
    renderConnectorEditorForm();
    var errBox = S.el("connector-editor-error");
    if (Object.keys(S.connectorFormErrors).length) {
      if (errBox) { errBox.hidden = false; errBox.textContent = "请修正标红的字段后再保存。"; }
      return;
    }
    var payload;
    try {
      payload = connectorBuildPayload(S.connectorForm, ps, isCreate);
    } catch (e) {
      S.connectorFormErrors.policy = e.message;
      renderConnectorEditorForm();
      return;
    }
    S.connectorSaving = true;
    var saveBtn = S.el("connector-editor-save");
    if (saveBtn) { saveBtn.disabled = true; saveBtn.textContent = "保存中…"; }
    if (errBox) { errBox.hidden = true; errBox.textContent = ""; }
    var path = isCreate ? "/api/connectors" : "/api/connectors/" + encodeURIComponent(S.connectorEditing.id);
    G.apiJSON(path, {
      method: isCreate ? "POST" : "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    }).then(function (r) {
      S.connectorSaving = false;
      if (saveBtn) { saveBtn.disabled = false; saveBtn.textContent = isCreate ? "创建连接器" : "保存修改"; }
      if (!r.res.ok) {
        S.connectorFormErrors = connectorFieldErrorsFrom(r.body);
        renderConnectorEditorForm();
        if (errBox) {
          errBox.hidden = false;
          errBox.textContent = (r.body && (r.body.error || r.body.message)) || ("保存失败：HTTP " + r.res.status);
        }
        return;
      }
      closeConnectorEditor();
      G.notify(isCreate ? "已创建连接器 " + payload.id : "连接器已保存", "ok");
      G.loadConnectors(true).then(function () { return loadConnectorBindings(); });
    }).catch(function (err) {
      S.connectorSaving = false;
      if (saveBtn) { saveBtn.disabled = false; saveBtn.textContent = isCreate ? "创建连接器" : "保存修改"; }
      if (errBox) { errBox.hidden = false; errBox.textContent = "保存失败：" + (err && err.message ? err.message : err); }
    });
  }

  function connectorActionBtn(label, disabled, onClick, extraClass) {
    var btn = document.createElement("button");
    btn.type = "button";
    // .btn.ghost is declared after .btn.primary in style.css, so a button
    // carrying both renders transparent and loses its primary emphasis.
    // Drop ghost whenever the caller promotes this button to card primary.
    var isPrimary = !!(extraClass && extraClass.indexOf("primary") >= 0);
    btn.className = "btn " + (isPrimary ? "" : "ghost ") + "sm" + (extraClass ? " " + extraClass : "");
    btn.textContent = label;
    btn.disabled = !!disabled;
    btn.addEventListener("click", function () { onClick(btn); });
    return btn;
  }

  function connectorToggle(item) {
    connectorSetBusy(item.id, true);
    connectorNotice("");
    G.renderConnectors(S.connectorEntries, S.connectorMessage);
    G.apiJSON("/api/connectors/" + encodeURIComponent(item.id), {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled: !item.enabled }),
    }).then(function (r) {
      connectorSetBusy(item.id, false);
      if (!r.res.ok) {
        connectorNotice("切换失败：" + ((r.body && r.body.error) || ("HTTP " + r.res.status)));
        G.renderConnectors(S.connectorEntries, S.connectorMessage);
        return;
      }
      G.notify((item.enabled ? "已停用 " : "已启用 ") + (item.name || item.id), "ok");
      G.loadConnectors(true);
    }).catch(function (err) {
      connectorSetBusy(item.id, false);
      connectorNotice("切换失败：" + (err && err.message ? err.message : err));
      G.renderConnectors(S.connectorEntries, S.connectorMessage);
    });
  }

  function connectorReload(item) {
    connectorSetBusy(item.id, true);
    connectorNotice("");
    G.renderConnectors(S.connectorEntries, S.connectorMessage);
    G.apiJSON("/api/connectors/" + encodeURIComponent(item.id) + "/reload", { method: "POST" }).then(function (r) {
      connectorSetBusy(item.id, false);
      if (!r.res.ok) {
        connectorNotice("重载失败：" + ((r.body && r.body.error) || ("HTTP " + r.res.status)));
        G.renderConnectors(S.connectorEntries, S.connectorMessage);
        return;
      }
      G.notify("已重载 " + (item.name || item.id), "ok");
      G.loadConnectors(true);
    }).catch(function (err) {
      connectorSetBusy(item.id, false);
      connectorNotice("重载失败：" + (err && err.message ? err.message : err));
      G.renderConnectors(S.connectorEntries, S.connectorMessage);
    });
  }

  // connectorSetRuntimeMode 切换连接器的宿主运行形态（docs/gateway/18 §7.3 C2）：
  //   - "gateway"：网关自持——本机轮询入站 + 出站，会话上下文（微信
  //     context_token）由本机产出并持久化，通知/回复不再依赖节点；
  //   - "outbound"：只出站副本——回到「节点收、网关发」的复制形态。
  // extraSettings 只做增量提交（PATCH 按 Schema 合并；未知键透传为 settings），
  // 传空串表示清回默认。切换后由 reload 立即生效（无需重启网关）。
  function connectorSetRuntimeMode(item, runtimeMode, extraSettings, opts) {
    opts = opts || {};
    var body = { runtime_mode: runtimeMode };
    if (extraSettings) {
      var settings = {};
      Object.keys(extraSettings).forEach(function (key) { settings[key] = extraSettings[key]; });
      if (Object.keys(settings).length) { body.settings = settings; }
    }
    connectorSetBusy(item.id, true);
    connectorNotice("");
    G.renderConnectors(S.connectorEntries, S.connectorMessage);
    return G.apiJSON("/api/connectors/" + encodeURIComponent(item.id), {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }).then(function (r) {
      connectorSetBusy(item.id, false);
      if (!r.res.ok) {
        connectorNotice("切换运行形态失败：" + ((r.body && r.body.error) || ("HTTP " + r.res.status)));
        G.renderConnectors(S.connectorEntries, S.connectorMessage);
        return;
      }
      if (opts.notice) { connectorNotice(opts.notice); }
      G.notify(opts.okText || "已切换运行形态", "ok");
      G.loadConnectors(true);
    }).catch(function (err) {
      connectorSetBusy(item.id, false);
      connectorNotice("切换运行形态失败：" + (err && err.message ? err.message : err));
      G.renderConnectors(S.connectorEntries, S.connectorMessage);
    });
  }

  function connectorDelete(item, btn) {
    G.confirmDestructive({
      title: "删除连接器",
      message: "将删除连接器 " + (item.name || item.id) + "（" + item.id + "）及其配置密钥；绑定与运行实例会停止。此操作不可撤销。",
      confirmText: "删除",
      trigger: btn,
      onConfirm: function () {
        var restore = Promise.resolve();
        if (item.origin && item.origin.node_id && item.origin.connector_id) {
          // 导入副本删除前先恢复节点本地通知，避免节点永久静音。
          restore = G.apiJSON("/api/nodes/" + encodeURIComponent(item.origin.node_id) +
            "/connectors/" + encodeURIComponent(item.origin.connector_id) + "/handoff/rollback",
            { method: "POST" }).then(function (r) {
            if (!r.res.ok) {
              throw new Error("恢复节点本地通知失败：" + ((r.body && r.body.error) || ("HTTP " + r.res.status)));
            }
          });
        }
        return restore.then(function () {
          return G.apiJSON("/api/connectors/" + encodeURIComponent(item.id), { method: "DELETE" });
        }).then(function (r) {
          if (!r.res.ok) { throw new Error((r.body && r.body.error) || ("HTTP " + r.res.status)); }
        });
      },
    }).then(function (res) {
      if (!res.confirmed) { return; }
      G.notify("已删除连接器 " + (item.name || item.id), "ok");
      G.loadConnectors(true).then(function () { return loadConnectorBindings(); });
    });
  }

  function connectorTakeover(item, btn) {
    G.confirmDestructive({
      title: "接管运行归属",
      message: "将把连接器 " + (item.name || item.id) + " 的运行归属从 " + (item.active_owner || "其它宿主") + " 抢到本网关，并重启其轮询。",
      confirmText: "接管",
      trigger: btn,
      onConfirm: function () {
        return G.apiJSON("/api/connectors/" + encodeURIComponent(item.id) + "/takeover", { method: "POST" }).then(function (r) {
          if (!r.res.ok) { throw new Error((r.body && r.body.error) || ("HTTP " + r.res.status)); }
        });
      },
    }).then(function (res) {
      if (!res.confirmed) { return; }
      G.notify("已接管 " + (item.name || item.id), "ok");
      G.loadConnectors(true);
    });
  }

  // connectorMigratedEntry 在网关连接器里找出来自这个节点连接器的副本。
  function connectorMigratedEntry(entry) {
    if (!entry || !entry.node || !entry.connector) { return null; }
    var nodeID = entry.node.node_id;
    var connectorID = entry.connector.id;
    if (!nodeID || !connectorID) { return null; }
    var groups = S.connectorGatewayEntries || [];
    for (var i = 0; i < groups.length; i++) {
      var candidate = groups[i] && groups[i].connector;
      if (candidate && candidate.origin &&
          candidate.origin.node_id === nodeID && candidate.origin.connector_id === connectorID) {
        return groups[i];
      }
    }
    return null;
  }

  function connectorNodeEntryForGateway(item) {
    if (!item || !item.origin) { return null; }
    var entries = S.connectorEntries || [];
    for (var i = 0; i < entries.length; i++) {
      var entry = entries[i];
      if (!entry || entry.source === "gateway" || !entry.node || !entry.connector) { continue; }
      if (entry.node.node_id === item.origin.node_id && entry.connector.id === item.origin.connector_id) {
        return entry;
      }
    }
    return null;
  }

  function shortStableHash(text) {
    var h = 2166136261;
    for (var i = 0; i < text.length; i++) {
      h ^= text.charCodeAt(i);
      h = Math.imul(h, 16777619);
    }
    return (h >>> 0).toString(36);
  }

  function friendlyConnectorError(err) {
    var msg = err && err.message ? err.message : String(err);
    if (/HTTP 502|no response|EOF|panic/i.test(msg)) {
      return "节点连接中断或版本不兼容；请确认节点在线且已升级到支持迁移的版本。";
    }
    if (/HTTP 404/i.test(msg)) {
      return "节点上没有该连接器，或节点版本过旧（缺少 handoff 接口）。";
    }
    if (/node offline|节点离线/i.test(msg)) {
      return "节点离线，无法读取 bot 配置。";
    }
    if (/HTTP 401|HTTP 403/i.test(msg)) {
      return "节点管理面鉴权失败。";
    }
    return msg;
  }

  function manageNotifyTargets(connectorID) {
    var nav = document.querySelector('.nav-item[data-view="notifications"]');
    if (nav) { nav.click(); }
    setTimeout(function () {
      var rows = document.querySelectorAll("#notify-im-targets .notify-im-row");
      var match = null;
      for (var i = 0; i < rows.length; i++) {
        if (rows[i].getAttribute("data-connector-id") === connectorID) { match = rows[i]; break; }
      }
      if (!match) { return; }
      match.classList.add("notify-im-row-focus");
      match.scrollIntoView({ behavior: "smooth", block: "center" });
      setTimeout(function () { match.classList.remove("notify-im-row-focus"); }, 2200);
    }, 150);
  }

  function rollbackConnectorMigration(node, connectorID, gatewayID, beforeConfig) {
    var chain = Promise.resolve();
    if (node && node.node_id && connectorID) {
      chain = chain.then(function () {
        return G.apiJSON("/api/nodes/" + encodeURIComponent(node.node_id) +
          "/connectors/" + encodeURIComponent(connectorID) + "/handoff/rollback",
          { method: "POST" }).catch(function () {});
      });
    }
    if (gatewayID) {
      chain = chain.then(function () {
        return G.apiJSON("/api/connectors/" + encodeURIComponent(gatewayID), { method: "DELETE" }).catch(function () {});
      });
    }
    if (beforeConfig) {
      chain = chain.then(function () {
        return G.apiJSON("/api/notify/im", {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(beforeConfig),
        }).catch(function () {});
      });
    }
    return chain.then(function () {
      if (beforeConfig) {
        S.notifyIMState.config = beforeConfig;
        G.renderNotifyIMTargets();
      }
    });
  }

  // connectorMigrate 是一次完整迁移：读取节点绑定 → 复制凭据 → 创建网关
  // outbound 副本 → 自动为每个绑定 chat 建立 Bot target → 关闭节点本地通知。
  // 失败时回滚节点/副本/target，避免“节点静音但网关没有目标”。
  function connectorMigrate(entry, btn, resync) {
    var item = entry && entry.connector;
    var node = entry && entry.node;
    if (!item || !node || !node.node_id) {
      G.notify("节点信息缺失，无法迁移", "error");
      return;
    }
    var nodeBase = "/api/nodes/" + encodeURIComponent(node.node_id);
    var bindingsURL = nodeBase + "/connectors/bindings?connector_id=" + encodeURIComponent(item.id);
    G.apiJSON(bindingsURL).then(function (bindingsResp) {
      if (!bindingsResp.res.ok) {
        throw new Error((bindingsResp.body && bindingsResp.body.error) || ("HTTP " + bindingsResp.res.status));
      }
      var bindings = ((bindingsResp.body && bindingsResp.body.items) || []).filter(function (b) {
        return b && b.chat;
      });
      if (!bindings.length) {
        throw new Error("该连接器没有可迁移的绑定 chat；已取消，节点配置未改动。");
      }
      var kinds = ["approval", "question", "completed", "error", "waiting"];
      var chats = bindings.map(function (b) { return b.chat; });
      var message = (resync
        ? "将重新同步节点「" + (G.nodeDisplayName(node) || node.node_id) + "」的 " + (item.name || item.id) + " 配置，并重建通知目标。\n\n"
        : "将把节点「" + (G.nodeDisplayName(node) || node.node_id) + "」的 " + (item.name || item.id) +
          "（" + item.provider + "）迁移到网关：\n\n") +
        "• 复制 " + bindings.length + " 个绑定 chat：" + chats.join("、") + "\n" +
        "• 创建网关 outbound-only 副本（不轮询，节点继续入站/回复）\n" +
        "• 通知范围：全部会话（含当前 live 会话）\n" +
        "• 自动创建 Bot target：" + kinds.map(G.notifyKindLabel).join("、") + "\n" +
        "• 关闭节点该连接器的本地通知\n\n" +
        "凭据只经隧道传输，不写日志。";
      return { bindings: bindings, kinds: kinds, message: message };
    }).then(function (prepared) {
      return G.confirmDestructive({
        title: resync ? "重新同步网关副本" : "迁移通知到网关",
        message: prepared.message,
        confirmText: resync ? "同步" : "迁移",
        trigger: btn,
        onConfirm: function () {
          var gatewayID = "";
          var beforeConfig = null;
          var chain = G.apiJSON(nodeBase + "/connectors/" + encodeURIComponent(item.id) + "/handoff", { method: "POST" })
            .then(function (r) {
              if (!r.res.ok) {
                throw new Error((r.body && r.body.error) || ("HTTP " + r.res.status));
              }
              gatewayID = (r.body && r.body.connector_id) || "";
              if (!gatewayID) { throw new Error("节点未返回网关副本 ID"); }
              return G.loadNotifyIM();
            })
            .then(function () {
              beforeConfig = JSON.parse(JSON.stringify(S.notifyIMState.config || { version: 1, targets: [] }));
              var next = JSON.parse(JSON.stringify(beforeConfig));
              if (!next.targets) { next.targets = []; }
              next.targets = next.targets.filter(function (t) {
                return !t || t.connector_id !== gatewayID;
              });
              prepared.bindings.forEach(function (binding) {
                var chat = String(binding.chat || "").trim();
                if (!chat) { return; }
                next.targets.push({
                  id: "mig-" + shortStableHash(gatewayID + "|" + chat),
                  connector_id: gatewayID,
                  label: (item.name || item.id) + " · " + chat,
                  source: "gateway",
                  scope: "all",
                  kinds: prepared.kinds.slice(),
                  enabled: true,
                });
              });
              return G.apiJSON("/api/notify/im", {
                method: "PUT",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify(next),
              }).then(function (put) {
                if (!put.res.ok) {
                  throw new Error((put.body && put.body.error) || ("HTTP " + put.res.status));
                }
                S.notifyIMState.config = (put.body && put.body.config) || next;
                if (!S.notifyIMState.config.targets) { S.notifyIMState.config.targets = []; }
                G.renderNotifyIMTargets();
                syncConnectorIDOptions();
                return { gatewayID: gatewayID, count: prepared.bindings.length };
              });
            });
          return chain.catch(function (err) {
            return rollbackConnectorMigration(node, item.id, gatewayID, beforeConfig).then(function () {
              throw new Error(friendlyConnectorError(err));
            });
          });
        },
      });
    }).then(function (res) {
      if (!res.confirmed) { return; }
      var result = res.result || {};
      G.notify((resync ? "已重新同步" : "已迁移") + " " + (result.count || 0) + " 个 chat 到网关" +
        (result.gatewayID ? "（" + result.gatewayID + "）" : "") +
        "；节点本地通知已关闭。可在 Bot 通道点「测试」验证。", "ok");
      G.loadConnectors(true);
    }).catch(function (err) {
      G.notify("迁移失败：" + friendlyConnectorError(err), "error");
    });
  }

  function openConnectorTest(item) {
    S.connectorTestState.id = item.id;
    S.connectorTestState.name = item.name || item.id;
    S.connectorTestState.busy = false;
    var title = S.el("connector-test-title");
    if (title) { title.textContent = "发送测试消息 · " + S.connectorTestState.name; }
    var sub = S.el("connector-test-sub");
    if (sub) { sub.textContent = "通过连接器 " + item.id + " 向指定 chat 发送一条测试消息，用于验证凭据与出站链路。"; }
    var err = S.el("connector-test-error");
    if (err) { err.hidden = true; err.textContent = ""; }
    var chat = S.el("connector-test-chat");
    var bindings = connectorBindingRows(item.id);
    if (chat) { chat.value = bindings.length ? bindings[0].chat : ""; }
    var text = S.el("connector-test-text");
    if (text) { text.value = "这是一条来自控制台的测试消息。"; }
    var send = S.el("connector-test-send");
    if (send) { send.disabled = false; send.textContent = "发送"; }
    G.openModal("connector-test-modal");
  }

  function sendConnectorTest() {
    if (S.connectorTestState.busy) { return; }
    var chat = (S.el("connector-test-chat") && S.el("connector-test-chat").value || "").trim();
    var text = (S.el("connector-test-text") && S.el("connector-test-text").value || "").trim();
    var errBox = S.el("connector-test-error");
    if (!chat) {
      if (errBox) { errBox.hidden = false; errBox.textContent = "目标会话 / 群 ID 必填。"; }
      return;
    }
    S.connectorTestState.busy = true;
    var send = S.el("connector-test-send");
    if (send) { send.disabled = true; send.textContent = "发送中…"; }
    if (errBox) { errBox.hidden = true; errBox.textContent = ""; }
    G.apiJSON("/api/connectors/" + encodeURIComponent(S.connectorTestState.id) + "/test", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ chat: chat, text: text }),
    }).then(function (r) {
      S.connectorTestState.busy = false;
      if (send) { send.disabled = false; send.textContent = "发送"; }
      if (!r.res.ok) {
        if (errBox) { errBox.hidden = false; errBox.textContent = "发送失败：" + ((r.body && r.body.error) || ("HTTP " + r.res.status)); }
        return;
      }
      G.closeModal("connector-test-modal");
      G.notify("测试消息已发送到 " + chat, "ok");
    }).catch(function (err) {
      S.connectorTestState.busy = false;
      if (send) { send.disabled = false; send.textContent = "发送"; }
      if (errBox) { errBox.hidden = false; errBox.textContent = "发送失败：" + (err && err.message ? err.message : err); }
    });
  }

  function openConnectorLogs(item) {
    S.connectorLogsState.id = item.id;
    S.connectorLogsState.name = item.name || item.id;
    S.connectorLogsState.entries = [];
    S.connectorLogsState.stats = null;
    var title = S.el("connector-logs-title");
    if (title) { title.textContent = "连接器日志 · " + S.connectorLogsState.name; }
    var sub = S.el("connector-logs-sub");
    if (sub) { sub.textContent = "内存日志（重启即清空），最新在前，最多 100 条。"; }
    var err = S.el("connector-logs-error");
    if (err) { err.hidden = true; err.textContent = ""; }
    renderConnectorLogs();
    G.openModal("connector-logs-modal");
    loadConnectorLogs(item.id);
  }

  function loadConnectorLogs(id) {
    S.connectorLogsState.loading = true;
    renderConnectorLogs();
    G.apiJSON("/api/connectors/" + encodeURIComponent(id) + "/logs?limit=100").then(function (r) {
      S.connectorLogsState.loading = false;
      var errBox = S.el("connector-logs-error");
      if (!r.res.ok) {
        if (errBox) { errBox.hidden = false; errBox.textContent = "读取日志失败：" + ((r.body && r.body.error) || ("HTTP " + r.res.status)); }
        renderConnectorLogs();
        return;
      }
      if (errBox) { errBox.hidden = true; errBox.textContent = ""; }
      S.connectorLogsState.entries = (r.body && r.body.entries) || [];
      S.connectorLogsState.stats = (r.body && r.body.stats) || null;
      renderConnectorLogs();
    }).catch(function (err) {
      S.connectorLogsState.loading = false;
      var errBox = S.el("connector-logs-error");
      if (errBox) { errBox.hidden = false; errBox.textContent = "读取日志失败：" + (err && err.message ? err.message : err); }
      renderConnectorLogs();
    });
  }

  function renderConnectorLogs() {
    var statsHost = S.el("connector-logs-stats");
    if (!statsHost) { return; }
    statsHost.innerHTML = "";
    var s = S.connectorLogsState.stats;
    if (s) {
      var bits = [
        "入站 " + (s.inbound || 0),
        "出站 " + (s.outbound || 0),
        "错误 " + (s.errors || 0),
        "去重跳过 " + (s.dedup_skipped || 0),
        "队列满 " + (s.queue_full || 0),
      ];
      var line = document.createElement("p");
      line.className = "connectors-hint";
      line.textContent = bits.join(" · ") + (s.last_error ? " · 最近错误：" + s.last_error : "");
      statsHost.appendChild(line);
    }
    var list = S.el("connector-logs-list");
    if (!list) { return; }
    list.innerHTML = "";
    if (S.connectorLogsState.loading) {
      var loading = document.createElement("p");
      loading.className = "empty";
      loading.textContent = "正在读取日志…";
      list.appendChild(loading);
      return;
    }
    if (!S.connectorLogsState.entries.length) {
      var empty = document.createElement("p");
      empty.className = "empty";
      empty.textContent = "暂无日志。连接器收到或发送消息后会在这里出现。";
      list.appendChild(empty);
      return;
    }
    S.connectorLogsState.entries.forEach(function (entry) {
      var row = document.createElement("div");
      row.className = "connector-log-row level-" + (entry.level || "info");
      var time = document.createElement("span");
      time.className = "connector-log-time";
      time.textContent = entry.time ? new Date(entry.time).toLocaleString() : "";
      var level = document.createElement("span");
      level.className = "connector-log-level";
      level.textContent = (entry.level || "info") + " / " + (entry.direction || "system");
      var chat = document.createElement("span");
      chat.className = "connector-log-chat";
      chat.textContent = entry.chat || "";
      var summary = document.createElement("span");
      summary.className = "connector-log-summary";
      summary.textContent = entry.summary || "";
      row.appendChild(time); row.appendChild(level); row.appendChild(chat); row.appendChild(summary);
      list.appendChild(row);
    });
  }

  function connectorActionImage(payload) {
    if (!payload) { return ""; }
    var keys = ["qr_code_content", "qrcode_img_content", "qr_code_img", "qrcode_img_string", "qr_image", "image", "qr_url", "url"];
    for (var i = 0; i < keys.length; i++) {
      var value = payload[keys[i]];
      if (typeof value !== "string" || !value.trim()) { continue; }
      var s = value.trim();
      if (/^data:image\//i.test(s) || /^https?:\/\//i.test(s)) { return s; }
      if (keys[i] !== "url" && /^[A-Za-z0-9+/=\s]+$/.test(s) && s.length > 200) {
        return "data:image/png;base64," + s.replace(/\s+/g, "");
      }
    }
    return "";
  }

  function renderConnectorActionResult() {
    var host = S.el("connector-action-body");
    if (!host) { return; }
    host.innerHTML = "";
    var payload = S.connectorActionState.payload || {};
    var img = connectorActionImage(payload);
    if (img) {
      var image = document.createElement("img");
      image.className = "connector-action-qr";
      image.alt = "二维码";
      image.src = img;
      host.appendChild(image);
    }
    var pre = document.createElement("pre");
    pre.className = "connector-action-json";
    try { pre.textContent = JSON.stringify(payload, null, 2); } catch (e) { pre.textContent = String(payload); }
    host.appendChild(pre);
    var loginID = payload.login_id;
    if (loginID && S.connectorActionState.action === "start_login") {
      var tools = document.createElement("div");
      tools.className = "toolbar";
      var btn = document.createElement("button");
      btn.type = "button";
      btn.className = "btn ghost sm";
      btn.textContent = "查询登录状态";
      btn.addEventListener("click", function () { connectorLoginStatus(S.connectorActionState.id, loginID, btn); });
      tools.appendChild(btn);
      host.appendChild(tools);
    }
  }

  function openConnectorActionResult(item, action, payload) {
    S.connectorActionState.id = item.id;
    S.connectorActionState.name = item.name || item.id;
    S.connectorActionState.action = action.key;
    S.connectorActionState.payload = payload || {};
    var title = S.el("connector-action-title");
    if (title) { title.textContent = "通道动作 · " + (action.label || action.key); }
    var sub = S.el("connector-action-sub");
    if (sub) { sub.textContent = S.connectorActionState.name + " · " + action.key; }
    var err = S.el("connector-action-error");
    if (err) { err.hidden = true; err.textContent = ""; }
    renderConnectorActionResult();
    G.openModal("connector-action-modal");
  }

  function connectorLoginStatus(connectorID, loginID, btn) {
    btn.disabled = true;
    G.apiJSON("/api/connectors/" + encodeURIComponent(connectorID) + "/actions/login_status", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ login_id: loginID }),
    }).then(function (r) {
      btn.disabled = false;
      var errBox = S.el("connector-action-error");
      if (!r.res.ok) {
        if (errBox) { errBox.hidden = false; errBox.textContent = "查询失败：" + ((r.body && r.body.error) || ("HTTP " + r.res.status)); }
        return;
      }
      S.connectorActionState.payload = r.body || {};
      renderConnectorActionResult();
      if (r.body && (r.body.settings || r.body.secrets)) {
        // 微信扫码确认会返回 settings/secrets 补丁：按 D4 两层模型回填实体。
        G.apiJSON("/api/connectors/" + encodeURIComponent(connectorID), {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ settings: r.body.settings || {}, secrets: r.body.secrets || {} }),
        }).then(function (pr) {
          if (!pr.res.ok) {
            if (errBox) { errBox.hidden = false; errBox.textContent = "凭据回填失败：" + ((pr.body && pr.body.error) || ("HTTP " + pr.res.status)); }
            return;
          }
          G.notify("扫码已确认，凭据已回填", "ok");
          G.loadConnectors(true).then(function () { return loadConnectorBindings(); });
        });
      }
    }).catch(function (err) {
      btn.disabled = false;
      var errBox = S.el("connector-action-error");
      if (errBox) { errBox.hidden = false; errBox.textContent = "查询失败：" + (err && err.message ? err.message : err); }
    });
  }
