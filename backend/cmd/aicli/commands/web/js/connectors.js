// 机器人页签:IM 连接器管理(飞书 / QQ / 微信 / Telegram)。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。
//
// 数据源与三端同契约(docs/bots/03-host-integration.md §5):
//   - GET  /api/connectors/schema   通道 Schema(字段/模式/动作),页面渲染的唯一输入;
//   - GET  /api/connectors          连接器列表(密钥只回 secret_keys);
//   - POST /api/connectors          新建;PATCH /api/connectors/{id} 更新
//     (settings/secrets 留空=保持,__clear__=清除);
//   - POST /api/connectors/{id}/test|takeover|reload|actions/{action};
//   - GET  /api/connectors/{id}/logs?limit=50(entries 倒序 + stats)。
//
// 通道差异全部由 Schema 驱动:字段类型(text/secret/bool/enum/url/number)、
// 模式(modes)、动作(actions: kind=qr 走扫码对话框,kind=button 直接执行)。
// 新增通道(或通道新增动作)本文件零改动。
//
// 微信扫码(start_login):展示 qr_code_content(data:image 渲染 <img>,否则文本 +
// 复制链接)→ 每 2s 轮询 login_status → confirmed 且 credential_ready 时把返回的
// settings/secrets 合并 PATCH 回连接器(与 React 页 action-dialog.tsx 同语义)。
import { apiFetch, esc, showToast } from "./util.js";

function connEl(id) { return document.getElementById(id); }

var schemaProviders = [];   // GET /api/connectors/schema → providers
var connectorItems = [];    // GET /api/connectors → items
var connectorsLoadedOnce = false;
var connectorsSeq = 0;      // 竞态守卫:列表请求序号(晚到旧响应不覆盖新渲染)
var editorState = null;     // 编辑对话框状态 {id, isEdit, provider}
var qrState = null;         // 扫码对话框状态 {connectorID, seq, timer, loginId, closed}
var logsSeq = 0;

// ---- 通用:请求与错误文案 ----

// apiJSON 在 apiFetch(超时/降级)之上解析 JSON 并统一抛错;错误对象带
// status 与 data,便于按 503(未装配)等状态给专门文案。
function apiJSON(path, init) {
  return apiFetch(path, init).then(function (res) {
    return res.json().catch(function () { return {}; }).then(function (data) {
      if (!res.ok) {
        var err = new Error(errorText(data, res.status));
        err.status = res.status;
        err.data = data;
        throw err;
      }
      return data;
    });
  });
}

function jsonInit(method, body) {
  return {
    method: method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body === undefined ? {} : body)
  };
}

// errorText 把服务端错误载荷压成一行可读文案:
// 校验失败 {error, fields:[{field,message}]} / 普通 {reason|error} / 503 专门文案。
function errorText(data, status) {
  var base = "";
  if (data && data.fields && data.fields.length) {
    var parts = [];
    for (var i = 0; i < data.fields.length; i++) {
      var f = data.fields[i] || {};
      var name = f.field || f.key || "";
      var msg = f.message || f.reason || "";
      parts.push((name ? name + ": " : "") + msg);
    }
    base = (data.error || "配置校验失败") + " — " + parts.join(";");
  } else if (data && data.reason) {
    base = String(data.reason);
  } else if (data && data.error) {
    base = String(data.error);
  }
  if (base) { return base; }
  if (status === 503) { return "连接器未在本会话装配(需要运行 aicli chat 且连接器初始化成功)"; }
  return "HTTP " + status;
}

function connectorPath(id) { return "/api/connectors/" + encodeURIComponent(id); }
function actionPath(id, action) { return connectorPath(id) + "/actions/" + encodeURIComponent(action); }

function providerMeta(name) {
  for (var i = 0; i < schemaProviders.length; i++) {
    if (schemaProviders[i] && schemaProviders[i].provider === name) { return schemaProviders[i]; }
  }
  return null;
}

function findItem(id) {
  for (var i = 0; i < connectorItems.length; i++) {
    if (connectorItems[i] && connectorItems[i].id === id) { return connectorItems[i]; }
  }
  return null;
}

// ---- 列表 ----

// loadConnectors 是页签懒加载入口(ui.js activateTab 调用):Schema 只拉一次,
// 列表每次进入都重拉(运行状态随时被宿主/其它端改变)。
export function loadConnectors() {
  var seq = ++connectorsSeq;
  var schemaPromise = schemaProviders.length
    ? Promise.resolve(null)
    : apiJSON("/api/connectors/schema", { cache: "no-store" });
  schemaPromise
    .then(function (schema) {
      if (schema && schema.providers) { schemaProviders = schema.providers; }
      return apiJSON("/api/connectors", { cache: "no-store" });
    })
    .then(function (data) {
      if (seq !== connectorsSeq) { return; }
      connectorItems = (data && data.items) ? data.items : [];
      connectorsLoadedOnce = true;
      renderConnectors();
    })
    .catch(function (err) {
      if (seq !== connectorsSeq) { return; }
      renderConnectorsError(err);
    });
}

function renderConnectorsError(err) {
  var listEl = connEl("connectors-list");
  var statusEl = connEl("connectors-status");
  if (statusEl) { statusEl.textContent = ""; }
  if (!listEl) { return; }
  listEl.innerHTML = '<div class="connectors-empty connectors-error">加载失败:' + esc(err && err.message ? err.message : String(err)) + "</div>";
}

function renderConnectors() {
  var listEl = connEl("connectors-list");
  var statusEl = connEl("connectors-status");
  if (!listEl) { return; }
  if (statusEl) {
    statusEl.textContent = connectorItems.length ? connectorItems.length + " 个连接器" : "";
  }
  if (!connectorItems.length) {
    listEl.innerHTML = '<div class="connectors-empty">还没有连接器。点「＋ 新增连接器」选择通道(飞书 / QQ / 微信 / Telegram)开始接入。</div>';
    return;
  }
  var html = "";
  for (var i = 0; i < connectorItems.length; i++) {
    html += rowHTML(connectorItems[i]);
  }
  listEl.innerHTML = html;
}

function rowHTML(item) {
  var meta = providerMeta(item.provider);
  var label = meta && meta.label ? meta.label : item.provider;
  var runtime = (item.runtime && item.runtime.stats) ? item.runtime.stats : null;
  var statsHTML = "";
  if (runtime) {
    var errCls = runtime.errors > 0 ? " connector-stat-err" : "";
    statsHTML = '<span class="connector-stat' + errCls + '" title="入站/出站/错误 累计计数">入 ' +
      num(runtime.inbound) + " · 出 " + num(runtime.outbound) + " · 错 " + num(runtime.errors) + "</span>";
  }
  var ownerText = item.active_owner
    ? esc(item.active_owner) + (item.owned_by_current_host ? "(本机)" : "")
    : "未认领";
  var actionsHTML = "";
  var actions = (meta && meta.actions) ? meta.actions : [];
  for (var i = 0; i < actions.length; i++) {
    var action = actions[i] || {};
    var kind = action.kind === "qr" ? "qr" : "button";
    actionsHTML += '<button type="button" data-act="action" data-action="' + esc(action.key) +
      '" data-kind="' + kind + '" title="' + esc(action.kind === "qr" ? "扫码登录(展示二维码并自动轮询)" : "执行动作") +
      '">' + esc(action.label || action.key) + "</button>";
  }
  var lastError = runtime && runtime.last_error
    ? '<div class="connector-last-error" title="最近一次错误">' + esc(runtime.last_error) + "</div>"
    : "";
  // 创建时间/更新时间（旧数据无戳时省略）；bot_identity 是扫码通道的平台
  // 稳定标识（如微信 ilink_bot_id）——同一机器人重扫码会得到相同值。
  var createdText = item.created_at ? formatTimestamp(item.created_at) : "";
  var updatedText = item.updated_at ? formatTimestamp(item.updated_at) : "";
  var stampText = createdText ? ("建于 " + createdText + (updatedText ? " · 更新 " + updatedText : "")) : "";
  var identityHTML = item.bot_identity
    ? '<span class="connector-owner" title="平台侧机器人标识（同一机器人重扫码值不变）">标识: ' + esc(item.bot_identity) + "</span>"
    : "";
  return '<div class="connector-row" data-id="' + esc(item.id) + '">' +
    '<div class="connector-row-main">' +
      '<span class="connector-name">' + esc(item.name || item.id) + "</span>" +
      '<code class="connector-id">' + esc(item.id) + "</code>" +
      '<span class="connector-badge">' + esc(label) + "</span>" +
      '<span class="connector-badge">' + esc(item.mode || "-") + "</span>" +
      '<span class="connector-badge ' + (item.enabled ? "connector-on" : "connector-off") + '">' +
        (item.enabled ? "已启用" : "已停用") + "</span>" +
      '<span class="connector-owner" title="运行归属(同一通道只允许一处运行)">归属: ' + ownerText + "</span>" +
      identityHTML +
      statsHTML +
    "</div>" +
    (stampText ? '<div class="connector-owner">' + esc(stampText) + "</div>" : "") +
    '<div class="connector-row-actions">' +
      '<button type="button" data-act="toggle">' + (item.enabled ? "停用" : "启用") + "</button>" +
      '<button type="button" data-act="edit">编辑</button>' +
      actionsHTML +
      '<button type="button" data-act="test">发送测试</button>' +
      '<button type="button" data-act="logs">日志</button>' +
      (item.active_owner && !item.owned_by_current_host
        ? '<button type="button" data-act="takeover" title="强制以本宿主身份接管该通道的运行">抢占</button>'
        : "") +
      '<button type="button" data-act="delete" class="danger-btn">删除</button>' +
    "</div>" +
    lastError +
  "</div>";
}

function num(v) { return (typeof v === "number" && isFinite(v)) ? v : 0; }

// formatTimestamp 把 RFC3339 UTC 时间戳转本地可读形式（非法/缺省返回空）。
function formatTimestamp(value) {
  if (!value) { return ""; }
  var d = new Date(value);
  if (isNaN(d.getTime())) { return ""; }
  var pad = function (n) { return (n < 10 ? "0" : "") + n; };
  return d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) +
    " " + pad(d.getHours()) + ":" + pad(d.getMinutes());
}

// ---- 列表行操作(事件委托) ----

function onListClick(event) {
  var btn = event.target && event.target.closest ? event.target.closest("[data-act]") : null;
  if (!btn) { return; }
  var row = btn.closest(".connector-row");
  var id = row ? row.getAttribute("data-id") : "";
  if (!id) { return; }
  var item = findItem(id);
  if (!item) { return; }
  var act = btn.getAttribute("data-act");
  if (act === "toggle") { toggleConnector(item); return; }
  if (act === "edit") { openEditor(id); return; }
  if (act === "action") { runAction(item, btn.getAttribute("data-action") || "", btn.getAttribute("data-kind") || "button"); return; }
  if (act === "test") { runTest(item); return; }
  if (act === "logs") { openLogsDialog(item); return; }
  if (act === "takeover") { runTakeover(item); return; }
  if (act === "delete") { runDelete(item); return; }
}

function toggleConnector(item) {
  apiJSON(connectorPath(item.id), jsonInit("PATCH", { enabled: !item.enabled }))
    .then(function () {
      showToast((item.enabled ? "已停用:" : "已启用:") + item.id, "ok");
      loadConnectors();
    })
    .catch(function (err) { showToast("操作失败:" + err.message, "err", 4000); });
}

function runTest(item) {
  showToast("正在发送测试消息…", "ok", 1200);
  apiJSON(connectorPath(item.id) + "/test", jsonInit("POST"))
    .then(function (data) {
      var msg = data && data.message ? String(data.message) : "测试消息已发送";
      showToast(msg, "ok", 3500);
      loadConnectors();
    })
    .catch(function (err) { showToast("发送失败:" + err.message, "err", 5000); });
}

function runTakeover(item) {
  if (!window.confirm("抢占运行归属:" + item.id + "？\n旧宿主将停止该通道的运行(需确认对方已停机,避免双活)。")) { return; }
  apiJSON(connectorPath(item.id) + "/takeover", jsonInit("POST"))
    .then(function () {
      showToast("已接管:" + item.id, "ok");
      loadConnectors();
    })
    .catch(function (err) { showToast("抢占失败:" + err.message, "err", 5000); });
}

function runDelete(item) {
  if (!window.confirm("删除连接器:" + item.id + "？\n将移除配置与凭据,不可恢复。")) { return; }
  apiJSON(connectorPath(item.id), { method: "DELETE" })
    .then(function () {
      showToast("已删除:" + item.id, "ok");
      loadConnectors();
    })
    .catch(function (err) { showToast("删除失败:" + err.message, "err", 4000); });
}

// ---- 新建 / 编辑对话框(完全由 Schema 渲染) ----

// openEditor 是「新增/编辑」入口。新增时若通道带 kind=qr 动作（微信等），
// **不展示表单**：直接自动创建连接器（自动 ID/默认值）→ 立即打开扫码对话框，
// 凭据由扫码成功后回填（与 TUI /bots、React「扫码接入」同一零输入体验）。
// 完整字段表单只服务于编辑（以及没有扫码动作的通道）。
function openEditor(id) {
  var item = id ? findItem(id) : null;
  var providerName = item ? item.provider : (schemaProviders[0] ? schemaProviders[0].provider : "");
  if (!item) {
    var meta = providerMeta(providerName);
    if (meta && hasQRAction(meta)) {
      quickCreateWithScan(meta);
      return;
    }
    // 多通道时先选通道：带扫码动作的通道点「开始」直接走零输入扫码流。
    if (schemaProviders.length > 1) {
      openProviderPicker();
      return;
    }
  }
  editorState = { id: id || "", isEdit: !!item, provider: providerName };
  renderEditor();
  openModal(item ? "编辑连接器:" + (item.name || item.id) : "新增连接器");
}

function hasQRAction(meta) {
  var actions = (meta && meta.actions) ? meta.actions : [];
  for (var i = 0; i < actions.length; i++) {
    if (actions[i] && actions[i].kind === "qr") { return true; }
  }
  return false;
}

function firstQRAction(meta) {
  var actions = (meta && meta.actions) ? meta.actions : [];
  for (var i = 0; i < actions.length; i++) {
    if (actions[i] && actions[i].kind === "qr") { return actions[i]; }
  }
  return null;
}

// quickCreateWithScan 零输入创建：自动生成 ID（provider-<base36 时间戳>），
// enabled/mode/policy 按默认（allowed_users 由扫码回填时自动写入 owner）。
function quickCreateWithScan(meta) {
  var action = firstQRAction(meta);
  if (!action) { return; }
  var id = meta.provider + "-" + Date.now().toString(36) + Math.floor(Math.random() * 0xffff).toString(36);
  apiJSON("/api/connectors", jsonInit("POST", {
    id: id,
    provider: meta.provider,
    name: meta.label || meta.provider,
    mode: (meta.modes && meta.modes[0]) || "polling"
  }))
    .then(function (created) {
      showToast("已创建:" + id + ",正在获取二维码…", "ok", 2500);
      loadConnectors();
      openQRDialog(created || { id: id, name: meta.label || meta.provider }, action.key);
    })
    .catch(function (err) {
      showToast("创建失败:" + err.message, "err", 5000);
      // 创建失败（如 id 撞车）退回表单兜底，不让用户卡死。
      editorState = { id: "", isEdit: false, provider: meta.provider };
      renderEditor();
      openModal("新增连接器");
    });
}

// openProviderPicker 是多通道下的「先选类型」轻对话框：带扫码动作的通道
// 确认后直接进入零输入扫码流；其余通道进入常规表单。
function openProviderPicker() {
  editorState = null;
  var options = "";
  for (var p = 0; p < schemaProviders.length; p++) {
    var prov = schemaProviders[p];
    var mark = hasQRAction(prov) ? "（扫码接入）" : "（手动配置）";
    options += '<option value="' + esc(prov.provider) + '">' +
      esc((prov.label || prov.provider) + mark) + "</option>";
  }
  setModalBody(
    '<div class="connector-form-section">选择通道类型</div>' +
    '<div class="connector-form-grid">' +
    fieldHTML("通道", '<select id="connector-pick-provider">' + options + "</select>",
      "微信等扫码通道：点「开始」自动创建并出二维码，无需填表。") +
    "</div>"
  );
  setModalFooter(
    '<button type="button" data-editor="cancel">取消</button>' +
    '<button type="button" class="primary-btn" data-editor="pick">开始</button>'
  );
  openModal("新增连接器");
}

function pickProviderAndStart() {
  var select = connEl("connector-pick-provider");
  if (!select) { return; }
  var provider = select.value;
  var meta = providerMeta(provider);
  closeModal();
  if (!meta) { return; }
  if (hasQRAction(meta)) {
    quickCreateWithScan(meta);
    return;
  }
  editorState = { id: "", isEdit: false, provider: provider };
  renderEditor();
  openModal("新增连接器");
}

// renderEditor 重建对话框正文。新建时 provider 可选(切换即换字段集),
// 编辑时 provider 锁定(后端禁止变更通道)。
function renderEditor() {
  if (!editorState) { return; }
  var item = editorState.isEdit ? findItem(editorState.id) : null;
  var meta = providerMeta(editorState.provider);
  if (!meta) {
    setModalBody('<div class="connectors-empty">通道 Schema 不可用(未注册?)。</div>');
    setModalFooter("");
    return;
  }
  var settings = (item && item.settings) ? item.settings : {};
  var secretKeys = (item && item.secret_keys) ? item.secret_keys : [];
  var policy = (item && item.policy) ? item.policy : {};

  var html = "";
  html += '<div class="connector-form-grid">';
  if (!editorState.isEdit) {
    var options = "";
    for (var p = 0; p < schemaProviders.length; p++) {
      var prov = schemaProviders[p];
      options += '<option value="' + esc(prov.provider) + '"' +
        (prov.provider === editorState.provider ? " selected" : "") + ">" +
        esc(prov.label || prov.provider) + "</option>";
    }
    html += fieldHTML("通道", '<select id="connector-form-provider">' + options + "</select>", "");
    html += fieldHTML("ID *", '<input id="connector-form-id" type="text" autocomplete="off" placeholder="如 wx-main(字母/数字/._-,最长 64)">', "创建后不可改名;用于配置键与状态归属。");
  }
  html += fieldHTML("显示名", '<input id="connector-form-name" type="text" autocomplete="off" placeholder="' +
    esc(item && item.name ? item.name : "如 工作群") + '" value="' + esc(item && item.name ? item.name : "") + '">', "");
  html += fieldHTML("启用", '<input id="connector-form-enabled" type="checkbox"' +
    ((!item || item.enabled) ? " checked" : "") + ">", "启用且归属本宿主时开始轮询/长连接。");
  html += "</div>";

  // 模式:provider.modes 驱动(单值也显示,让用户确认接入方式)。
  if (meta.modes && meta.modes.length) {
    var modeOptions = "";
    var currentMode = item ? item.mode : (meta.modes[0] || "");
    for (var m = 0; m < meta.modes.length; m++) {
      modeOptions += '<option value="' + esc(meta.modes[m]) + '"' +
        (meta.modes[m] === currentMode ? " selected" : "") + ">" + esc(modeLabel(meta.modes[m])) + "</option>";
    }
    html += '<div class="connector-form-section">接入模式</div><div class="connector-form-grid">' +
      fieldHTML("模式", '<select id="connector-form-mode">' + modeOptions + "</select>", "") + "</div>";
  }

  // 通道字段:Schema 逐字段渲染(mode 已单独处理,跳过)。
  var fieldsHTML = "";
  for (var f = 0; f < meta.fields.length; f++) {
    var field = meta.fields[f];
    if (!field || field.key === "mode") { continue; }
    fieldsHTML += schemaFieldHTML(field, settings, secretKeys, item);
  }
  if (fieldsHTML) {
    html += '<div class="connector-form-section">' + esc(meta.label || meta.provider) + " 参数</div>" +
      '<div class="connector-form-grid">' + fieldsHTML + "</div>";
  }

  // 策略(policy):aicli 侧 fail-closed——启用中的连接器必须有非空
  // allowed_users 才会启动轮询,因此这里必须可编辑。
  html += '<div class="connector-form-section">策略</div><div class="connector-form-grid">' +
    fieldHTML("允许用户(allowed_users)", '<textarea id="connector-form-allowed-users" rows="3" placeholder="每行一个平台用户 ID;留空=拒绝所有入站(aicli 侧不会启动)">' +
      esc((policy.allowed_users || []).join("\n")) + "</textarea>",
      "白名单:只有列表中的平台用户消息会进入 agent;留空时 aicli 宿主直接跳过启动(fail-closed)。") +
    fieldHTML("命令前缀", '<input id="connector-form-command-prefix" type="text" autocomplete="off" placeholder="/" value="' +
      esc(policy.command_prefix || "") + '">', "IM 里触发命令(如 /approve /new)的前缀。") +
    fieldHTML("会话模式", '<input id="connector-form-session-mode" type="text" autocomplete="off" placeholder="(默认)" value="' +
      esc(policy.session_mode || "") + '">', "session_mode 透传后端;留空用默认。") +
    fieldHTML("回复模式", '<input id="connector-form-reply-mode" type="text" autocomplete="off" placeholder="(默认) stream" value="' +
      esc(policy.reply_mode || "") + '">', "reply_mode:stream(流式占位+编辑)/final(仅终态)。") +
    fieldHTML("运行归属", runtimeOwnerSelectHTML(policy.runtime_owner || ""), "auto=先到先得;指定宿主后仅该宿主可认领。") +
    "</div>";

  html += '<p class="connector-form-note">留空=保持原值;输入 <code>__clear__</code> 可清除既有非密钥字段。密钥只写不读(GET 仅回字段名)。</p>';
  setModalBody(html);
  setModalFooter(
    '<button type="button" data-editor="cancel">取消</button>' +
    '<button type="button" class="primary-btn" data-editor="save">保存</button>'
  );
}

function modeLabel(mode) {
  if (mode === "polling") { return "长轮询"; }
  if (mode === "webhook") { return "Webhook"; }
  if (mode === "gateway") { return "长连接"; }
  return mode;
}

function runtimeOwnerSelectHTML(current) {
  var opts = [["", "(默认:auto)"], ["auto", "auto(先到先得)"], ["aicli", "aicli"], ["runtime-server", "runtime-server"], ["gateway", "gateway"]];
  var html = '<select id="connector-form-runtime-owner">';
  for (var i = 0; i < opts.length; i++) {
    html += '<option value="' + esc(opts[i][0]) + '"' + (opts[i][0] === current ? " selected" : "") + ">" + esc(opts[i][1]) + "</option>";
  }
  return html + "</select>";
}

function fieldHTML(label, control, help) {
  return '<label class="connector-field"><span class="connector-field-label">' + esc(label) + "</span>" +
    control + (help ? '<span class="connector-field-help">' + esc(help) + "</span>" : "") + "</label>";
}

// schemaFieldHTML 按字段类型渲染控件:
//   secret → password(编辑时占位「已配置(留空不改)」,不显示原值);
//   bool   → checkbox(值 true/false);enum → select;number → number;其余 → text。
function schemaFieldHTML(field, settings, secretKeys, item) {
  var key = field.key;
  var label = (field.label || key) + (field.required ? " *" : "");
  var help = field.help || "";
  var value = settings[key] !== undefined && settings[key] !== null ? String(settings[key]) : "";
  if (field.type === "secret") {
    var configured = secretKeys.indexOf(key) >= 0;
    var placeholder = configured ? "已配置(留空不改)" : (field.placeholder || "");
    return fieldHTML(label, '<input type="password" data-secret="' + esc(key) + '" autocomplete="new-password" placeholder="' +
      esc(placeholder) + '">', help || (configured ? "已配置;留空=保持,输入 __clear__ 清除。" : ""));
  }
  if (field.type === "bool") {
    var on = value === "true" || (value === "" && field.default === "true");
    return fieldHTML(label, '<input type="checkbox" data-setting="' + esc(key) + '"' + (on ? " checked" : "") + ">", help);
  }
  if (field.type === "enum") {
    var options = field.options || [];
    var sel = "";
    var current = value || field.default || "";
    for (var i = 0; i < options.length; i++) {
      sel += '<option value="' + esc(options[i].value) + '"' + (options[i].value === current ? " selected" : "") + ">" +
        esc(options[i].label || options[i].value) + "</option>";
    }
    return fieldHTML(label, '<select data-setting="' + esc(key) + '">' + sel + "</select>", help);
  }
  var type = field.type === "number" ? "number" : "text";
  var fallback = value || (item ? "" : (field.default || ""));
  return fieldHTML(label, '<input type="' + type + '" data-setting="' + esc(key) + '" autocomplete="off" placeholder="' +
    esc(field.placeholder || "") + '" value="' + esc(fallback) + '">', help);
}

// collectEditor 读取表单为 submitRequest(与 restapi 的字段一一对应)。
// settings/secrets 的留空=保持语义交给后端 MergeSubmit;新建时留空走默认值。
function collectEditor() {
  var meta = providerMeta(editorState.provider);
  var settings = {};
  var secrets = {};
  var body = connEl("connectors-modal-body");
  if (meta && body) {
    for (var i = 0; i < meta.fields.length; i++) {
      var field = meta.fields[i];
      if (!field || field.key === "mode") { continue; }
      var input = body.querySelector('[data-setting="' + field.key + '"]');
      var secretInput = body.querySelector('[data-secret="' + field.key + '"]');
      if (secretInput) {
        var sv = (secretInput.value || "").trim();
        if (sv) { secrets[field.key] = sv; }
        continue;
      }
      if (!input) { continue; }
      var value;
      if (field.type === "bool") { value = input.checked ? "true" : "false"; }
      else { value = (input.value || "").trim(); }
      if (value === "") { continue; } // 留空=保持(新建时后端按默认值合并)
      settings[field.key] = value;
    }
  }
  var users = [];
  var usersEl = connEl("connector-form-allowed-users");
  if (usersEl) {
    var lines = String(usersEl.value || "").split(/[\n,]/);
    for (var u = 0; u < lines.length; u++) {
      var name = lines[u].trim();
      if (name) { users.push(name); }
    }
  }
  var payload = {
    name: (connEl("connector-form-name") || {}).value || "",
    enabled: !!(connEl("connector-form-enabled") || {}).checked,
    mode: (connEl("connector-form-mode") || {}).value || "",
    settings: settings,
    secrets: secrets,
    policy: {
      allowed_users: users,
      command_prefix: ((connEl("connector-form-command-prefix") || {}).value || "").trim(),
      session_mode: ((connEl("connector-form-session-mode") || {}).value || "").trim(),
      reply_mode: ((connEl("connector-form-reply-mode") || {}).value || "").trim(),
      runtime_owner: (connEl("connector-form-runtime-owner") || {}).value || ""
    }
  };
  if (!editorState.isEdit) {
    payload.id = ((connEl("connector-form-id") || {}).value || "").trim();
    payload.provider = editorState.provider;
  }
  return payload;
}

function saveEditor() {
  if (!editorState) { return; }
  var payload = collectEditor();
  if (!editorState.isEdit && !payload.id) {
    showToast("请填写 ID", "err", 3000);
    return;
  }
  var promise = editorState.isEdit
    ? apiJSON(connectorPath(editorState.id), jsonInit("PATCH", payload))
    : apiJSON("/api/connectors", jsonInit("POST", payload));
  promise
    .then(function () {
      showToast(editorState.isEdit ? "已保存:" + editorState.id : "已创建:" + payload.id, "ok");
      closeModal();
      loadConnectors();
    })
    .catch(function (err) { showToast("保存失败:" + err.message, "err", 6000); });
}

// ---- 通道动作:kind=button 直接执行;kind=qr 走扫码对话框 ----

function runAction(item, actionKey, kind) {
  if (!actionKey) { return; }
  if (kind === "qr") { openQRDialog(item, actionKey); return; }
  apiJSON(actionPath(item.id, actionKey), jsonInit("POST"))
    .then(function (data) {
      var suffix = (data && data.status) ? "(" + data.status + ")" : "";
      showToast("动作完成:" + actionKey + suffix, "ok", 3000);
      loadConnectors();
    })
    .catch(function (err) { showToast("动作失败:" + err.message, "err", 5000); });
}

// ---- 扫码登录对话框(start_login → login_status 轮询 → 凭据回填) ----

function openQRDialog(item, actionKey) {
  stopQRPoll();
  qrState = { connectorID: item.id, actionKey: actionKey, seq: 0, timer: null, loginId: "", content: "", closed: false };
  openModal("扫码登录:" + (item.name || item.id));
  setModalBody('<div class="connector-qr-status">正在获取二维码…</div>');
  setModalFooter('<button type="button" data-qr="close">关闭</button>');
  startQRLogin();
}

function startQRLogin() {
  var st = qrState;
  if (!st || st.closed) { return; }
  var seq = ++st.seq;
  setQRStatus("正在获取二维码…");
  apiJSON(actionPath(st.connectorID, st.actionKey), jsonInit("POST"))
    .then(function (data) {
      if (!qrState || qrState !== st || st.closed || seq !== st.seq) { return; }
      st.loginId = data.login_id || "";
      renderQR(data);
      scheduleQRPoll(2000);
    })
    .catch(function (err) {
      if (!qrState || qrState !== st || st.closed || seq !== st.seq) { return; }
      setQRStatus("获取二维码失败:" + err.message);
    });
}

function renderQR(data) {
  var st = qrState;
  if (!st) { return; }
  var status = data.status || "wait";
  var statusText = {
    wait: "等待扫码…",
    scanned: "已扫码,请在手机上确认…",
    confirmed: "已确认",
    expired: "二维码已过期"
  }[status] || status;
  var content = data.qr_code_content || st.content || "";
  st.content = content;
  var html = '<div class="connector-qr-status" data-status="' + esc(status) + '">' + esc(statusText) + "</div>";
  if (content && content.indexOf("data:image/") === 0) {
    // data:image 前缀 = 直接可渲染的二维码图片(与 React 页同判定)。
    html += '<div class="connector-qr-image-wrap"><img class="connector-qr-img" alt="登录二维码" src="' + esc(content) + '"></div>';
  } else if (content) {
    // 链接/短码文本:交给本机 QR 端点编码为 PNG(与 TUI /bots 同一 go-qrcode),
    // 链接兜底展示在图片下方;端点失败时 <img> onerror 自动退回纯文本链接。
    var qrSrc = "/web/api/qr?text=" + encodeURIComponent(content);
    html += '<div class="connector-qr-image-wrap"><img class="connector-qr-img" alt="登录二维码" src="' + esc(qrSrc) + '"' +
      ' onerror="this.closest(\'.connector-qr-image-wrap\').classList.add(\'connector-qr-fallback\');this.remove();"></div>';
    html += '<code class="connector-qr-link">' + esc(content) + "</code>";
  }
  var meta = [];
  if (data.expires_at) { meta.push("有效期至 " + data.expires_at); }
  if (data.refresh_count) { meta.push("已自动刷新 " + data.refresh_count + " 次"); }
  if (meta.length) { html += '<div class="connector-qr-meta">' + esc(meta.join(" · ")) + "</div>"; }
  html += '<p class="connector-form-note">用手机扫码(或打开链接)完成登录;成功后凭据自动回填到连接器。</p>';
  setModalBody(html);
  var footer = '<button type="button" data-qr="restart">重新获取二维码</button>';
  if (content && content.indexOf("data:image/") !== 0) {
    footer += '<button type="button" data-qr="copy">复制链接</button>';
  }
  footer += '<button type="button" data-qr="close">关闭</button>';
  setModalFooter(footer);
}

function scheduleQRPoll(delay) {
  var st = qrState;
  if (!st || st.closed) { return; }
  st.timer = setTimeout(function () { pollQRLogin(st); }, delay);
}

function pollQRLogin(st) {
  if (!qrState || qrState !== st || st.closed) { return; }
  var seq = st.seq;
  apiJSON(actionPath(st.connectorID, "login_status"), jsonInit("POST", { login_id: st.loginId }))
    .then(function (data) {
      if (!qrState || qrState !== st || st.closed || seq !== st.seq) { return; }
      renderQR(data);
      if (data.status === "confirmed") {
        st.closed = true;
        stopQRPoll();
        finishQRLogin(st, data);
        return;
      }
      if (data.status === "expired") { stopQRPoll(); return; }
      scheduleQRPoll(2000);
    })
    .catch(function (err) {
      if (!qrState || qrState !== st || st.closed || seq !== st.seq) { return; }
      // 400(登录会话不存在/已清理)重试无意义,停下并如实展示;其余(网络抖动)继续轮询。
      if (err.status === 400) { stopQRPoll(); setQRStatus("轮询失败:" + err.message); return; }
      scheduleQRPoll(3000);
    });
}

// finishQRLogin:confirmed 后按 credential_ready 决定回填。
// 回填 = 把 login_status 返回的 settings/secrets 合并 PATCH(与三端同契约)。
function finishQRLogin(st, data) {
  if (data.credential_ready !== true) {
    showToast("已确认登录,等待凭据就绪(可稍后重新打开查看)", "ok", 3500);
    closeModal();
    loadConnectors();
    return;
  }
  apiJSON(connectorPath(st.connectorID), jsonInit("PATCH", {
    settings: data.settings || {},
    secrets: data.secrets || {}
  }))
    .then(function (result) {
      // 服务端重复扫码收敛：adopted=true 表示凭据合并进了既有机器人
      // （removed_id 是已删除的壳）。提示语区分「新建」与「更新既有」。
      if (result && result.adopted && result.removed_id) {
        showToast("扫码登录成功:识别为既有机器人 " + result.id + ",凭据已更新(临时 " + result.removed_id + " 已清理)", "ok", 5000);
      } else {
        showToast("扫码登录成功,凭据已回填:" + st.connectorID, "ok", 4000);
      }
      closeModal();
      loadConnectors();
    })
    .catch(function (err) {
      showToast("凭据回填失败:" + err.message + "(可稍后在编辑里手动填写)", "err", 6000);
      closeModal();
      loadConnectors();
    });
}

function stopQRPoll() {
  if (qrState && qrState.timer) { clearTimeout(qrState.timer); qrState.timer = null; }
}

function setQRStatus(text) {
  var el = connEl("connectors-modal-body");
  var statusEl = el ? el.querySelector(".connector-qr-status") : null;
  if (statusEl) { statusEl.textContent = text; }
}

// ---- 运行日志对话框 ----

var logsState = { id: "" };

function openLogsDialog(item) {
  logsState.id = item.id;
  var seq = ++logsSeq;
  openModal("运行日志:" + (item.name || item.id));
  setModalBody('<div class="connectors-empty">加载中…</div>');
  setModalFooter('<button type="button" data-logs="refresh">刷新</button><button type="button" data-logs="close">关闭</button>');
  loadLogs(item.id, seq);
}

function loadLogs(id, seq) {
  apiJSON(connectorPath(id) + "/logs?limit=50", { cache: "no-store" })
    .then(function (data) {
      if (seq !== logsSeq) { return; }
      renderLogs(data);
    })
    .catch(function (err) {
      if (seq !== logsSeq) { return; }
      setModalBody('<div class="connectors-empty connectors-error">加载失败:' + esc(err.message) + "</div>");
    });
}

function renderLogs(data) {
  var stats = (data && data.stats) ? data.stats : {};
  var entries = (data && data.entries) ? data.entries : [];
  var html = '<div class="connector-logs-stats">入 ' + num(stats.inbound) + " · 出 " + num(stats.outbound) +
    " · 错 " + num(stats.errors) + " · 去重跳过 " + num(stats.dedup_skipped) +
    " · 队列满 " + num(stats.queue_full) + "</div>";
  if (stats.last_error) {
    html += '<div class="connector-last-error">最近错误:' + esc(stats.last_error) + "</div>";
  }
  if (!entries.length) {
    html += '<div class="connectors-empty">暂无日志(进程内环形缓冲,重启清零)。</div>';
    setModalBody(html);
    return;
  }
  html += '<div class="connector-logs-list">';
  for (var i = 0; i < entries.length; i++) {
    var e = entries[i] || {};
    html += '<div class="connector-log-line connector-log-' + esc(e.level || "info") + '">' +
      '<span class="connector-log-time">' + esc(formatLogTime(e.time)) + "</span>" +
      '<span class="connector-log-level">' + esc(e.level || "") + "</span>" +
      '<span class="connector-log-dir">' + esc(e.direction || "") + "</span>" +
      '<span class="connector-log-summary">' + esc(e.summary || "") + "</span></div>";
  }
  html += "</div>";
  setModalBody(html);
}

function formatLogTime(t) {
  if (!t) { return ""; }
  var d = new Date(t);
  if (isNaN(d.getTime())) { return String(t); }
  return d.toLocaleTimeString();
}

function copyText(text, okMsg) {
  if (!text) { return; }
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(
      function () { showToast(okMsg, "ok"); },
      function () { showToast("复制失败", "err"); }
    );
    return;
  }
  showToast("复制失败:当前浏览器不支持剪贴板 API", "err");
}

// ---- 对话框骨架(overlay + modal,与其它页签弹窗同范式) ----

var modalOpen = false;

function openModal(title) {
  var overlay = connEl("connectors-overlay");
  var titleEl = connEl("connectors-modal-title");
  if (!overlay) { return; }
  if (titleEl) { titleEl.textContent = title || ""; }
  overlay.classList.add("active");
  modalOpen = true;
}

function closeModal() {
  var overlay = connEl("connectors-overlay");
  if (!overlay || !overlay.classList.contains("active")) { return false; }
  overlay.classList.remove("active");
  modalOpen = false;
  editorState = null;
  stopQRPoll();
  qrState = null;
  return true;
}

function setModalBody(html) {
  var el = connEl("connectors-modal-body");
  if (el) { el.innerHTML = html; }
}

function setModalFooter(html) {
  var el = connEl("connectors-modal-footer");
  if (el) { el.innerHTML = html; }
}

// ---- 事件接线 ----

function onQRButton(act) {
  if (act === "close") { closeModal(); return; }
  if (act === "copy") { copyText(qrState ? qrState.content : "", "登录链接已复制"); return; }
  if (act === "restart") {
    var st = qrState;
    if (!st) { return; }
    stopQRPoll();
    st.loginId = "";
    st.content = "";
    st.seq++; // 作废在途响应
    startQRLogin();
  }
}

function onModalBodyClick(event) {
  var btn = event.target && event.target.closest ? event.target.closest("[data-qr]") : null;
  if (btn) { onQRButton(btn.getAttribute("data-qr")); }
}

// 新建时切换通道 → 按新 Schema 重渲染字段集(编辑时 provider 锁定,选择器不存在)。
function onModalBodyChange(event) {
  var target = event.target;
  if (target && target.id === "connector-form-provider" && editorState) {
    editorState.provider = target.value;
    renderEditor();
  }
}

function onModalFooterClick(event) {
  var btn = event.target && event.target.closest ? event.target.closest("[data-editor],[data-qr],[data-logs]") : null;
  if (!btn) { return; }
  if (btn.hasAttribute("data-editor")) {
    var act = btn.getAttribute("data-editor");
    if (act === "cancel") { closeModal(); }
    if (act === "save") { saveEditor(); }
    if (act === "pick") { pickProviderAndStart(); }
    return;
  }
  if (btn.hasAttribute("data-qr")) { onQRButton(btn.getAttribute("data-qr")); return; }
  if (btn.hasAttribute("data-logs")) {
    var l = btn.getAttribute("data-logs");
    if (l === "close") { closeModal(); }
    if (l === "refresh" && logsState.id) { loadLogs(logsState.id, ++logsSeq); }
  }
}

export function initConnectors() {
  var overlay = connEl("connectors-overlay");
  if (overlay) {
    // 与其它弹窗同样的 reparent:fixed 定位不受页签层叠上下文影响。
    document.body.appendChild(overlay);
    overlay.addEventListener("click", function (event) {
      if (event.target === overlay) { closeModal(); }
    });
  }
  var closeBtn = connEl("connectors-modal-close");
  if (closeBtn) { closeBtn.addEventListener("click", function () { closeModal(); }); }
  var refreshBtn = connEl("connectors-refresh-btn");
  if (refreshBtn) { refreshBtn.addEventListener("click", function () { loadConnectors(); }); }
  var addBtn = connEl("connectors-add-btn");
  if (addBtn) { addBtn.addEventListener("click", function () { openEditor(""); }); }
  var listEl = connEl("connectors-list");
  if (listEl) { listEl.addEventListener("click", onListClick); }
  var bodyEl = connEl("connectors-modal-body");
  if (bodyEl) {
    bodyEl.addEventListener("click", onModalBodyClick);
    bodyEl.addEventListener("change", onModalBodyChange);
  }
  var footerEl = connEl("connectors-modal-footer");
  if (footerEl) { footerEl.addEventListener("click", onModalFooterClick); }
  document.addEventListener("keydown", function (event) {
    if (event.key !== "Escape") { return; }
    // 弹窗打开时 Esc 只关闭弹窗:捕获阶段短路,避免同时触发会话中断。
    if (closeModal()) {
      event.preventDefault();
      event.stopPropagation();
    }
  }, true);
}
