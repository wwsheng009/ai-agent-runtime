// Provider 编辑弹窗:表单回显/保存、协议下拉 popup、API Key 状态、reasoning 编辑器、拖拽缩放。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { configData, loadConfigAdmin, providerByName, setCfgStatus } from "./config-admin.js";
import { configEl, esc, showToast } from "./util.js";

var cfgReasoningDraft = {}; // model -> { reasoning_model, reasoning_efforts, default_reasoning_effort, compact_reasoning_effort }
var cfgEditorSize = null;   // 弹窗用户调整过的尺寸 {w, h}，会话内记忆
var cfgApiKeySaved = false;        // 当前编辑的 provider 是否已配置凭据
var cfgApiKeySource = "";         // 凭据来源：inline / pool / key_store / oauth
var cfgApiKeyClearPending = false; // 用户点了「清除」等待保存生效
var cfgApiKeyMasked = "";          // 已保存 key 的掩码回显（快照 api_key_masked 或本地计算）

// ---- 协议下拉（Provider 编辑弹窗）----
// 原生 <input list=datalist> 在 input 有值时会被浏览器按当前值过滤选项，
// 存在值与无值时下拉显示不一致，且无下拉箭头、跨浏览器行为不一。
// 改为与底部 model 字段一致的 ▼ + 自定义 popup：无论是否有值都展示
// 全量协议列表（选项取自 HTML datalist，单一数据源），当前值高亮；
// 自定义输入的协议不在预置列表时附加到列表尾部，保证可见。
function protocolOptionValues() {
  var dl = configEl("cfg-provider-protocol-options");
  if (!dl) { return []; }
  var values = [];
  var opts = dl.querySelectorAll("option[value]");
  for (var i = 0; i < opts.length; i++) {
    var v = (opts[i].getAttribute("value") || "").trim();
    if (v && values.indexOf(v) < 0) { values.push(v); }
  }
  return values;
}

function renderProtocolPopup() {
  var popup = configEl("cfg-provider-protocol-popup");
  var input = configEl("cfg-provider-protocol");
  if (!popup || !input) { return; }
  var curVal = (input.value || "").trim();
  var values = protocolOptionValues();
  if (curVal && values.indexOf(curVal) < 0) { values.push(curVal); }
  if (values.length === 0) {
    popup.innerHTML = '<div class="cfg-combo-empty">暂无预置协议，可直接输入</div>';
    return;
  }
  var html = "";
  values.forEach(function (v) {
    var cls = "cfg-combo-item" + (v === curVal ? " current" : "");
    var tag = v === curVal ? '<span class="tag">当前</span>' : "";
    html += '<button type="button" class="' + cls + '" data-protocol="' + esc(v) + '" title="' + esc(v) + '">' +
      esc(v) + tag + "</button>";
  });
  popup.innerHTML = html;
}

function closeProtocolPopup() {
  var popup = configEl("cfg-provider-protocol-popup");
  if (popup) { popup.style.display = "none"; }
}

function toggleProtocolPopup() {
  var popup = configEl("cfg-provider-protocol-popup");
  var input = configEl("cfg-provider-protocol");
  if (!popup || !input) { return; }
  if (popup.style.display !== "none" && popup.innerHTML) {
    closeProtocolPopup();
    return;
  }
  // 打开时按当前值重渲染，保证高亮与自定义值附加始终最新。
  renderProtocolPopup();
  popup.style.display = "block";
}

function selectProtocolFromPopup(value) {
  var input = configEl("cfg-provider-protocol");
  value = (value || "").trim();
  if (!input || !value) { return; }
  input.value = value;
  closeProtocolPopup();
  input.focus();
}

export function openProviderEditor(name) {
  var p = name ? providerByName(name) : null;
  cfgReasoningDraft = {};
  closeProtocolPopup();
  var title = configEl("config-editor-title");
  if (title) { title.textContent = p ? "编辑 Provider: " + p.name : "新增 Provider"; }
  var orig = configEl("cfg-provider-original-name");
  if (orig) { orig.value = p ? p.name : ""; }
  var nameEl = configEl("cfg-provider-name");
  if (nameEl) { nameEl.value = p ? p.name : ""; nameEl.readOnly = !!p; }
  configEl("cfg-provider-protocol").value = p ? (p.protocol || "") : "";
  configEl("cfg-provider-base-url").value = p ? (p.base_url || "") : "";
  configEl("cfg-provider-api-path").value = p ? (p.api_path || "") : "";
  configEl("cfg-provider-forward-url").value = p ? (p.forward_url || "") : "";
  configEl("cfg-provider-default-model").value = p ? (p.default_model || "") : "";
  var enabled = configEl("cfg-provider-enabled");
  if (enabled) { enabled.checked = p ? p.enabled : true; }
  var setDefault = configEl("cfg-provider-set-default");
  if (setDefault) { setDefault.checked = !!(p && p.name === configData.default_provider); }
  // API key：明文不回传，输入框始终为空；状态行按凭据来源显示
  // 已保存（Key Store / OAuth / 密钥池 / 内联）或未配置，
  // 已保存时可一键标记「清除」（cfgApiKeyClearPending，保存时移除全部来源）。
  cfgApiKeySaved = !!(p && p.api_key_set);
  cfgApiKeySource = (p && p.api_key_source) || "";
  cfgApiKeyMasked = (p && p.api_key_masked) || "";
  cfgApiKeyClearPending = false;
  var apiKey = configEl("cfg-provider-api-key");
  if (apiKey) { apiKey.value = ""; apiKey.disabled = false; }
  renderAPIKeyStatus();
  // Proxy（provider 级覆盖）
  var proxy = p ? (p.proxy || null) : null;
  var proxyEnabled = configEl("cfg-provider-proxy-enabled");
  if (proxyEnabled) { proxyEnabled.checked = !!(proxy && proxy.enabled); }
  configEl("cfg-provider-proxy-http").value = proxy ? (proxy.http || "") : "";
  configEl("cfg-provider-proxy-https").value = proxy ? (proxy.https || "") : "";
  configEl("cfg-provider-proxy-no-proxy").value = proxy ? (proxy.no_proxy || "") : "";
  var removeProxy = configEl("cfg-provider-remove-proxy");
  if (removeProxy) { removeProxy.checked = false; }
  var removeProxyWrap = configEl("cfg-provider-remove-proxy-wrap");
  if (removeProxyWrap) { removeProxyWrap.style.display = proxy ? "" : "none"; }
  // 模型列表：supported ∪ default_model（去重）
  var models = [];
  (p ? (p.supported_models || []) : []).forEach(function (m) {
    if (m && models.indexOf(m) < 0) { models.push(m); }
  });
  if (p && p.default_model && models.indexOf(p.default_model) < 0) { models.push(p.default_model); }
  configEl("cfg-provider-models").value = models.join("\n");
  rebuildModelReasoningEditors(models, p);
  showConfigEditor(true);
  setCfgStatus(configEl("cfg-provider-status"), "", "");
}

// 刷新 API key 状态行：已保存（按来源区分 chip）/ 未配置 / 将清除（待定态）。
// placeholder 与 hint 随状态动态变化；「将清除」时禁用并清空输入框。
var cfgApiKeySourceChip = {
  inline: "已保存",
  pool: "已保存（密钥池）",
  key_store: "已保存（Key Store）",
  oauth: "已保存（OAuth）"
};
var cfgApiKeySourceHint = {
  inline: "凭据以内联 api_key 保存",
  pool: "使用 api_keys 密钥池",
  key_store: "凭据存放在 Key Store（api_key_ref）",
  oauth: "使用 OAuth access token（auth_ref）"
};
// 与后端 maskAPIKeyForDisplay 一致：<=8 字符整段打码；否则保留密钥
// 标识前缀（sk- / sk-proj- 等，第一个 "-" 及之前）连同其后 4 字符与
// 尾部 4 字符；无分隔符时退化为前 4 + "..." + 后 4。仅界面回显。
function maskAPIKey(key) {
  var s = String(key || "").trim();
  if (!s) { return ""; }
  if (s.length <= 8) { return "****"; }
  var idx = s.indexOf("-");
  if (idx < 0 || idx + 1 >= s.length - 4) {
    return s.slice(0, 4) + "..." + s.slice(-4);
  }
  var midEnd = Math.min(idx + 5, s.length - 4);
  return s.slice(0, idx + 1) + s.slice(idx + 1, midEnd) + "..." + s.slice(-4);
}
function renderAPIKeyStatus() {
  var statusEl = configEl("cfg-provider-api-key-status");
  var hint = configEl("cfg-provider-api-key-hint");
  var input = configEl("cfg-provider-api-key");
  if (input) {
    input.disabled = cfgApiKeyClearPending;
    if (cfgApiKeyClearPending) { input.value = ""; }
    input.placeholder = cfgApiKeyClearPending
      ? "已标记清除"
      : (cfgApiKeySaved ? "留空则不修改，输入新值覆盖" : "输入新 API Key，保存后生效");
  }
  var saveBtn = configEl("cfg-provider-api-key-save");
  if (saveBtn) {
    // 「将清除」时不提供更新；否则输入非空才可点。
    saveBtn.disabled = cfgApiKeyClearPending || !(input && String(input.value || "").trim());
  }
  if (!statusEl) { return; }
  if (cfgApiKeyClearPending) {
    statusEl.innerHTML = '<span class="cfg-key-status pending">将清除</span>' +
      ' <button type="button" class="cfg-key-clear" data-action="clear-api-key">取消</button>';
  } else if (cfgApiKeySaved) {
    // 掩码值可能来自用户输入/快照，用 DOM 节点渲染避免 innerHTML 注入。
    statusEl.textContent = "";
    var chip = document.createElement("span");
    chip.className = "cfg-key-status saved";
    chip.textContent = cfgApiKeySourceChip[cfgApiKeySource] || "已保存";
    statusEl.appendChild(chip);
    if (cfgApiKeyMasked) {
      var maskEl = document.createElement("span");
      maskEl.className = "cfg-key-masked";
      maskEl.textContent = cfgApiKeyMasked;
      statusEl.appendChild(maskEl);
    }
    var clearBtn = document.createElement("button");
    clearBtn.type = "button";
    clearBtn.className = "cfg-key-clear";
    clearBtn.setAttribute("data-action", "clear-api-key");
    clearBtn.textContent = "清除";
    statusEl.appendChild(clearBtn);
  } else {
    statusEl.textContent = "未配置";
    statusEl.className = "cfg-key-status";
  }
  if (!hint) { return; }
  if (cfgApiKeyClearPending) {
    hint.textContent = "保存后将移除该 provider 的全部凭据（内联 / Key Store / OAuth / 密钥池）；「取消」可恢复";
  } else if (cfgApiKeySaved) {
    var srcHint = cfgApiKeySourceHint[cfgApiKeySource] || "";
    hint.textContent = (srcHint ? srcHint + "；" : "") + "留空不修改，「清除」可移除全部凭据";
  } else {
    hint.textContent = "填入并保存后写入本地配置；也可通过 api_key_ref（Key Store）或 auth_ref（OAuth）使用存量凭据";
  }
}

// 快速更新 API Key：只提交 name + api_key，其余字段不提交（后端
// nil=保留原值的合并语义），无需走整个表单的「保存」。成功后清空输入、
// 取消「将清除」待定态（新 key 已生效）并刷新配置。
function saveAPIKeyOnly() {
  var nameEl = configEl("cfg-provider-name");
  var name = (nameEl && nameEl.value ? nameEl.value : "").trim();
  var apiKeyEl = configEl("cfg-provider-api-key");
  var btn = configEl("cfg-provider-api-key-save");
  if (!name) { showToast("请先输入 provider 名称", "err"); return; }
  var key = (apiKeyEl ? apiKeyEl.value : "").trim();
  if (!key) { showToast("请输入要更新的 API Key", "err"); return; }
  if (btn) { btn.disabled = true; }
  fetch("/web/api/config/providers", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name: name, api_key: key })
  })
    .then(function (res) { return res.json().catch(function () { return { status: "error", reason: "bad response" }; }); })
    .then(function (json) {
      if (json.status !== "ok") {
        showToast("API Key 更新失败: " + (json.reason || json.status), "err");
        if (btn) { btn.disabled = false; }
        return;
      }
      cfgApiKeyClearPending = false;
      if (apiKeyEl) { apiKeyEl.value = ""; }
      cfgApiKeySaved = true;
      cfgApiKeySource = "inline";
      // 后端保存响应回传真实掩码（Key Store 模式也能立即回显），
      // 兼容旧后端无 masked 字段时本地兜底计算。
      cfgApiKeyMasked = json.masked || maskAPIKey(key);
      renderAPIKeyStatus();
      showToast("API Key 已更新: " + name);
      loadConfigAdmin();
    })
    .catch(function (err) {
      showToast("API Key 更新失败: " + err, "err");
      if (btn) { btn.disabled = false; }
    });
}

// 弹窗位置/尺寸：打开时按上次记忆的尺寸（或默认 720x70vh）居中；
// 拖动/缩放见 initConfigTab 中的 mousedown 绑定。
function positionConfigEditor() {
  var overlay = configEl("config-editor-overlay");
  var modal = configEl("config-editor-modal");
  if (!overlay || !modal) { return; }
  var vw = window.innerWidth || document.documentElement.clientWidth;
  var vh = window.innerHeight || document.documentElement.clientHeight;
  var w, h;
  if (cfgEditorSize) {
    w = cfgEditorSize.w;
    h = cfgEditorSize.h;
  } else {
    w = Math.min(720, vw - 32);
    h = Math.min(620, Math.max(360, Math.round(vh * 0.7)));
  }
  var minW = Math.min(420, vw - 24);
  var minH = Math.min(260, vh - 24);
  if (w < minW) { w = minW; }
  if (h < minH) { h = minH; }
  if (w > vw - 24) { w = vw - 24; }
  if (h > vh - 24) { h = vh - 24; }
  modal.style.width = w + "px";
  modal.style.height = h + "px";
  modal.style.left = Math.max(8, Math.round((vw - w) / 2)) + "px";
  modal.style.top = Math.max(8, Math.round((vh - h) / 2)) + "px";
}

function showConfigEditor(show) {
  var overlay = configEl("config-editor-overlay");
  if (!overlay) { return; }
  if (show) {
    positionConfigEditor();
    overlay.classList.add("active");
  } else {
    closeProtocolPopup();
    overlay.classList.remove("active");
  }
}

// 根据模型列表重建每模型的 reasoning 编辑行；已有草稿（cfgReasoningDraft）
// 优先展示，避免输入过程中重建丢失用户已填内容。
function rebuildModelReasoningEditors(models, provider) {
  var container = configEl("cfg-model-reasoning-editors");
  if (!container) { return; }
  var unique = [];
  models.forEach(function (m) {
    m = String(m).trim();
    if (m && unique.indexOf(m) < 0) { unique.push(m); }
  });
  if (unique.length === 0) {
    container.innerHTML = '<div class="config-empty">保存模型列表后在此逐模型配置 reasoning effort</div>';
    return;
  }
  var html = "";
  unique.forEach(function (model) {
    var spec = {
      reasoning_model: false,
      reasoning_efforts: "",
      default_reasoning_effort: "",
      compact_reasoning_effort: ""
    };
    if (provider) {
      (provider.models || []).forEach(function (m) {
        if (m.name === model) {
          spec.reasoning_model = m.reasoning_model;
          spec.reasoning_efforts = (m.reasoning_efforts || []).join(", ");
          spec.default_reasoning_effort = m.default_reasoning_effort || "";
          spec.compact_reasoning_effort = m.compact_reasoning_effort || "";
        }
      });
    }
    if (cfgReasoningDraft[model]) { spec = cfgReasoningDraft[model]; }
    html += '<div class="cfg-model-reasoning" data-model="' + esc(model) + '">' +
      '<div class="cfg-model-reasoning-head">' +
      '<label class="cfg-check"><input type="checkbox" data-field="reasoning_model"' + (spec.reasoning_model ? " checked" : "") + "> " + esc(model) + ' 是 reasoning 模型</label>' +
      "</div>" +
      '<div class="cfg-model-reasoning-fields">' +
      '<label class="cfg-item"><span class="cfg-label">reasoning_efforts</span>' +
      '<input data-field="reasoning_efforts" value="' + esc(spec.reasoning_efforts) + '" placeholder="如 low, medium, high" autocomplete="off"></label>' +
      '<label class="cfg-item"><span class="cfg-label">默认 effort</span>' +
      '<input data-field="default_reasoning_effort" value="' + esc(spec.default_reasoning_effort) + '" placeholder="medium" autocomplete="off"></label>' +
      '<label class="cfg-item"><span class="cfg-label">压缩 effort</span>' +
      '<input data-field="compact_reasoning_effort" value="' + esc(spec.compact_reasoning_effort) + '" placeholder="low" autocomplete="off"></label>' +
      "</div></div>";
  });
  container.innerHTML = html;
}

// 把 reasoning 编辑行内容收集进 draft 表（提交前与重建前调用）。
function collectReasoningDrafts() {
  var container = configEl("cfg-model-reasoning-editors");
  if (!container) { return; }
  var rows = container.querySelectorAll(".cfg-model-reasoning");
  for (var i = 0; i < rows.length; i++) {
    var row = rows[i];
    var model = row.getAttribute("data-model");
    var draft = cfgReasoningDraft[model] || {
      reasoning_model: false,
      reasoning_efforts: "",
      default_reasoning_effort: "",
      compact_reasoning_effort: ""
    };
    var inputs = row.querySelectorAll("input[data-field]");
    for (var j = 0; j < inputs.length; j++) {
      var input = inputs[j];
      var field = input.getAttribute("data-field");
      if (field === "reasoning_model") { draft.reasoning_model = input.checked; }
      else { draft[field] = input.value.trim(); }
    }
    cfgReasoningDraft[model] = draft;
  }
}

function saveProvider(ev) {
  ev.preventDefault();
  var statusEl = configEl("cfg-provider-status");
  var nameEl = configEl("cfg-provider-name");
  var name = (nameEl && nameEl.value ? nameEl.value : "").trim();
  if (!name) { setCfgStatus(statusEl, "名称不能为空", "err"); return; }
  collectReasoningDrafts();
  var models = (configEl("cfg-provider-models").value || "").split(/\r?\n/).map(function (s) { return s.trim(); }).filter(Boolean);
  var reasoning = {};
  Object.keys(cfgReasoningDraft).forEach(function (model) {
    if (models.indexOf(model) < 0) { return; } // 只提交当前模型列表内的模型
    var d = cfgReasoningDraft[model];
    reasoning[model] = {
      reasoning_model: !!d.reasoning_model,
      reasoning_efforts: d.reasoning_efforts ? d.reasoning_efforts.split(/[,，\s]+/).filter(Boolean) : [],
      default_reasoning_effort: d.default_reasoning_effort || "",
      compact_reasoning_effort: d.compact_reasoning_effort || ""
    };
  });
  var payload = {
    name: name,
    protocol: (configEl("cfg-provider-protocol").value || "").trim(),
    base_url: (configEl("cfg-provider-base-url").value || "").trim(),
    api_path: (configEl("cfg-provider-api-path").value || "").trim(),
    forward_url: (configEl("cfg-provider-forward-url").value || "").trim(),
    default_model: (configEl("cfg-provider-default-model").value || "").trim(),
    supported_models: models,
    enabled: configEl("cfg-provider-enabled").checked,
    set_default_provider: configEl("cfg-provider-set-default").checked,
    reasoning: reasoning
  };
  // API key：非空=写入；标记清除=显式空串（后端移除 api_key 节点）。
  var apiKeyVal = (configEl("cfg-provider-api-key").value || "").trim();
  if (apiKeyVal) {
    payload.api_key = apiKeyVal;
  } else if (cfgApiKeyClearPending) {
    // 清除 = 移除全部凭据来源，避免只清内联后仍显示已保存（Key Store / OAuth）。
    payload.api_key = "";
    payload.api_key_ref = "";
    payload.auth_ref = "";
    payload.api_keys = [];
  }
  // Proxy：勾选移除时清除节点，否则整体写回（含 enabled 开关）。
  var removeProxyEl = configEl("cfg-provider-remove-proxy");
  if (removeProxyEl && removeProxyEl.checked) {
    payload.clear_proxy = true;
  } else {
    payload.proxy = {
      enabled: configEl("cfg-provider-proxy-enabled").checked,
      http: (configEl("cfg-provider-proxy-http").value || "").trim(),
      https: (configEl("cfg-provider-proxy-https").value || "").trim(),
      no_proxy: (configEl("cfg-provider-proxy-no-proxy").value || "").trim()
    };
  }
  setCfgStatus(statusEl, "保存中…", "busy");
  fetch("/web/api/config/providers", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload)
  })
    .then(function (res) { return res.json().catch(function () { return { status: "error", reason: "bad response" }; }); })
    .then(function (json) {
      if (json.status !== "ok") {
        setCfgStatus(statusEl, "保存失败: " + (json.reason || json.status), "err");
        return;
      }
      setCfgStatus(statusEl, "", "");
      showConfigEditor(false);
      showToast("已保存 provider: " + name);
      loadConfigAdmin();
    })
    .catch(function (err) { setCfgStatus(statusEl, "保存失败: " + err, "err"); });
}

// 调用后端 /web/api/config/providers/fetch-models 拉取该 provider 的
// GET /models 清单：优先用表单里新填的 api key，否则用已保存的 key；
// 结果合并（去重）进「支持模型」文本域，不覆盖用户手填的行。
function fetchModelsFromProvider() {
  var btn = configEl("cfg-provider-fetch-models-btn");
  var statusEl = configEl("cfg-provider-fetch-models-status");
  if (!btn || btn.disabled) { return; }
  var apiKeyVal = (configEl("cfg-provider-api-key").value || "").trim();
  var payload = {
    name: (configEl("cfg-provider-original-name").value || "").trim(),
    protocol: (configEl("cfg-provider-protocol").value || "").trim(),
    base_url: (configEl("cfg-provider-base-url").value || "").trim()
  };
  if (apiKeyVal) { payload.api_key = apiKeyVal; }
  btn.disabled = true;
  var oldText = btn.textContent;
  btn.textContent = "获取中…";
  if (statusEl) { statusEl.textContent = ""; statusEl.className = "cfg-hint-inline"; }
  fetch("/web/api/config/providers/fetch-models", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload)
  })
    .then(function (res) {
      return res.json().catch(function () { return { status: "error", reason: "bad response" }; });
    })
    .then(function (json) {
      if (json.status !== "ok" || !json.models) {
        var msg = "获取失败: " + (json.reason || json.status || "unknown");
        if (statusEl) { statusEl.textContent = msg; statusEl.className = "cfg-hint-inline err"; }
        showToast(msg, "err");
        return;
      }
      mergeFetchedModels(json.models);
      var okMsg = "已获取 " + json.models.length + " 个模型" + (json.endpoint ? "（" + json.endpoint + "）" : "");
      if (statusEl) { statusEl.textContent = okMsg; statusEl.className = "cfg-hint-inline ok"; }
      showToast("已获取 " + json.models.length + " 个模型");
      // 端点是公开的（匿名可访问）：获取列表成功不代表 key 有效，明确提示。
      if (json.auth_notice) {
        if (statusEl) {
          statusEl.textContent = okMsg + "。⚠ " + json.auth_notice;
          statusEl.className = "cfg-hint-inline warn";
        }
        showToast(json.auth_notice, "warn");
      }
    })
    .catch(function (err) {
      var msg = "获取失败: " + err;
      if (statusEl) { statusEl.textContent = msg; statusEl.className = "cfg-hint-inline err"; }
      showToast(msg, "err");
    })
    .then(function () {
      btn.disabled = false;
      btn.textContent = oldText;
    });
}

// 把拉取到的模型 id 合并进「支持模型」文本域（去重，保留手填行），
// 并重建 reasoning 编辑行（先收草稿再重建，不丢已输入内容）。
function mergeFetchedModels(fetched) {
  var ta = configEl("cfg-provider-models");
  if (!ta || !fetched) { return; }
  var existing = [];
  ta.value.split(/\r?\n/).forEach(function (m) {
    m = String(m).trim();
    if (m && existing.indexOf(m) < 0) { existing.push(m); }
  });
  var added = 0;
  fetched.forEach(function (m) {
    m = String(m).trim();
    if (!m) { return; }
    if (existing.indexOf(m) < 0) { existing.push(m); added++; }
  });
  ta.value = existing.join("\n");
  if (added > 0) {
    collectReasoningDrafts();
    var orig = (configEl("cfg-provider-original-name").value || "").trim();
    rebuildModelReasoningEditors(existing, orig ? providerByName(orig) : null);
  }
}

// Escape 关闭协议 popup(与 runtime 的 model popup 各自独立监听)。
export function initGlobalProtocolPopupDismiss() {
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") { closeProtocolPopup(); }
  });
}

export function initProviderEditor() {
  // 协议 popup 同样点击外部关闭（wrap 内含 ▼ 与 popup，均为输入框的兄弟节点）。
  document.addEventListener("click", function (e) {
    var popup = configEl("cfg-provider-protocol-popup");
    if (!popup || popup.style.display === "none") { return; }
    var input = configEl("cfg-provider-protocol");
    var wrap = input && input.parentNode ? input.parentNode : null;
    if (wrap && wrap.contains(e.target)) { return; }
    closeProtocolPopup();
  });
    var addBtn = configEl("config-add-provider-btn");
    if (addBtn) { addBtn.addEventListener("click", function () { openProviderEditor(null); }); }
    var cancelBtn = configEl("cfg-provider-cancel-btn");
    if (cancelBtn) { cancelBtn.addEventListener("click", function () { showConfigEditor(false); }); }
    var closeBtn = configEl("cfg-provider-close-btn");
    if (closeBtn) { closeBtn.addEventListener("click", function () { showConfigEditor(false); }); }
    // 协议字段 ▼ + 自定义 popup（见 renderProtocolPopup 注释）。
    var protoToggle = configEl("cfg-provider-protocol-toggle");
    var protoInput = configEl("cfg-provider-protocol");
    if (protoToggle) {
      protoToggle.addEventListener("click", function (e) {
        e.preventDefault();
        e.stopPropagation();
        toggleProtocolPopup();
        if (protoInput) { protoInput.focus(); }
      });
    }
    if (protoInput) {
      protoInput.addEventListener("input", function () {
        // 输入时同步 popup 高亮与自定义值附加（popup 打开时）。
        var popup = configEl("cfg-provider-protocol-popup");
        if (popup && popup.style.display !== "none") { renderProtocolPopup(); }
      });
      protoInput.addEventListener("keydown", function (e) {
        if (e.key === "Escape") { closeProtocolPopup(); }
      });
    }
    var protoPopup = configEl("cfg-provider-protocol-popup");
    if (protoPopup) {
      // 事件委托：popup 内容每次重渲染，无需重复绑定。
      protoPopup.addEventListener("click", function (e) {
        var btn = e.target && e.target.closest ? e.target.closest("[data-protocol]") : null;
        if (!btn) { return; }
        e.preventDefault();
        e.stopPropagation();
        selectProtocolFromPopup(btn.getAttribute("data-protocol") || "");
      });
    }
    var editorOverlay = configEl("config-editor-overlay");
    if (editorOverlay) {
      editorOverlay.addEventListener("click", function (e) {
        if (e.target === editorOverlay) { showConfigEditor(false); }
      });
      // 挂到 <body> 下：避免受 .tab-panel 的定位/层叠上下文影响，
      // 保证固定定位与 z-index 层级全局一致（见 style.css 层级约定）。
      if (editorOverlay.parentNode && editorOverlay.parentNode !== document.body) {
        document.body.appendChild(editorOverlay);
      }
    }
    var editorModal = configEl("config-editor-modal");
    var editorHeader = configEl("config-editor-modal-header");
    var editorResize = configEl("config-editor-resize");
    function editorDragStart(e) {
      if (e.button !== 0) { return; }
      if (e.target && e.target.closest && e.target.closest(".modal-close")) { return; }
      if (!editorOverlay || !editorOverlay.classList.contains("active") || !editorModal) { return; }
      e.preventDefault();
      var startX = e.clientX, startY = e.clientY;
      var startLeft = parseInt(editorModal.style.left, 10) || 0;
      var startTop = parseInt(editorModal.style.top, 10) || 0;
      var vw = window.innerWidth || document.documentElement.clientWidth;
      var vh = window.innerHeight || document.documentElement.clientHeight;
      function onMove(ev) {
        var w = editorModal.offsetWidth, h = editorModal.offsetHeight;
        var left = startLeft + (ev.clientX - startX);
        var top = startTop + (ev.clientY - startY);
        if (left < 8 - (w - 80)) { left = 8 - (w - 80); } // 保留标题栏可抓取
        if (left > vw - 40) { left = vw - 40; }
        if (top < 0) { top = 0; }
        if (top > vh - 40) { top = vh - 40; }
        editorModal.style.left = left + "px";
        editorModal.style.top = top + "px";
      }
      function onUp() {
        document.removeEventListener("mousemove", onMove);
        document.removeEventListener("mouseup", onUp);
        document.body.style.userSelect = "";
      }
      document.body.style.userSelect = "none";
      document.addEventListener("mousemove", onMove);
      document.addEventListener("mouseup", onUp);
    }
    function editorResizeStart(e) {
      if (e.button !== 0) { return; }
      e.preventDefault();
      e.stopPropagation();
      var startX = e.clientX, startY = e.clientY;
      var startW = editorModal.offsetWidth, startH = editorModal.offsetHeight;
      var vw = window.innerWidth || document.documentElement.clientWidth;
      var vh = window.innerHeight || document.documentElement.clientHeight;
      function onMove(ev) {
        var w = startW + (ev.clientX - startX);
        var h = startH + (ev.clientY - startY);
        var minW = Math.min(420, vw - 24);
        var minH = Math.min(260, vh - 24);
        if (w < minW) { w = minW; }
        if (h < minH) { h = minH; }
        if (w > vw - 24) { w = vw - 24; }
        if (h > vh - 24) { h = vh - 24; }
        editorModal.style.width = w + "px";
        editorModal.style.height = h + "px";
        cfgEditorSize = { w: w, h: h };
        // 弹窗变大时如超出可视区，拉回窗口内（保留标题栏）。
        var left = parseInt(editorModal.style.left, 10) || 0;
        var top = parseInt(editorModal.style.top, 10) || 0;
        if (left + w > vw - 8) {
          left = Math.max(8 - (w - 80), vw - 8 - w);
          editorModal.style.left = left + "px";
        }
        if (top + h > vh - 8) {
          top = Math.max(0, vh - 8 - h);
          editorModal.style.top = top + "px";
        }
      }
      function onUp() {
        document.removeEventListener("mousemove", onMove);
        document.removeEventListener("mouseup", onUp);
        document.body.style.userSelect = "";
      }
      document.body.style.userSelect = "none";
      document.addEventListener("mousemove", onMove);
      document.addEventListener("mouseup", onUp);
    }
    if (editorHeader) { editorHeader.addEventListener("mousedown", editorDragStart); }
    if (editorResize) { editorResize.addEventListener("mousedown", editorResizeStart); }
    // 窗口尺寸变化时把弹窗拉回可视范围。
    window.addEventListener("resize", function () {
      if (!editorOverlay || !editorModal || !editorOverlay.classList.contains("active")) { return; }
      var vw = window.innerWidth || document.documentElement.clientWidth;
      var vh = window.innerHeight || document.documentElement.clientHeight;
      var w = editorModal.offsetWidth, h = editorModal.offsetHeight;
      if (w > vw - 24) { w = vw - 24; editorModal.style.width = w + "px"; }
      if (h > vh - 24) { h = vh - 24; editorModal.style.height = h + "px"; }
      var left = parseInt(editorModal.style.left, 10) || 0;
      var top = parseInt(editorModal.style.top, 10) || 0;
      editorModal.style.left = Math.max(8 - (w - 80), Math.min(left, vw - 40)) + "px";
      editorModal.style.top = Math.max(0, Math.min(top, vh - 40)) + "px";
    });
    var form = configEl("config-provider-form");
    if (form) { form.addEventListener("submit", saveProvider); }
    var fetchModelsBtn = configEl("cfg-provider-fetch-models-btn");
    if (fetchModelsBtn) { fetchModelsBtn.addEventListener("click", fetchModelsFromProvider); }
    var apiKeySaveBtn = configEl("cfg-provider-api-key-save");
    if (apiKeySaveBtn) { apiKeySaveBtn.addEventListener("click", saveAPIKeyOnly); }
    var apiKeyInput = configEl("cfg-provider-api-key");
    if (apiKeyInput) {
      // 输入非空即可快速「更新」；「将清除」待定态下保持禁用（renderAPIKeyStatus 管理）。
      apiKeyInput.addEventListener("input", function () {
        var btn = configEl("cfg-provider-api-key-save");
        if (btn && !cfgApiKeyClearPending) {
          btn.disabled = !String(apiKeyInput.value || "").trim();
        }
      });
    }
    // API key 状态行里的「清除/取消」按钮（innerHTML 动态重建，走事件委托）。
    if (form) {
      form.addEventListener("click", function (e) {
        var t = e.target;
        while (t && t !== form) {
          if (t.getAttribute && t.getAttribute("data-action") === "clear-api-key") {
            cfgApiKeyClearPending = !cfgApiKeyClearPending;
            renderAPIKeyStatus();
            return;
          }
          t = t.parentNode;
        }
      });
    }
    var modelsInput = configEl("cfg-provider-models");
    if (modelsInput) {
      modelsInput.addEventListener("input", function () {
        collectReasoningDrafts(); // 先把已填内容收进草稿，重建不丢失
        var models = modelsInput.value.split(/\r?\n/).map(function (s) { return s.trim(); }).filter(Boolean);
        rebuildModelReasoningEditors(models, null);
      });
    }
  initGlobalProtocolPopupDismiss();
}
