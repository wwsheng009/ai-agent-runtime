// Provider 编辑弹窗:表单回显/保存、协议下拉 popup、API Key 状态、模型编辑器（点击模型打开详细面板）、拖拽缩放。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { configData, loadConfigAdmin, providerByName, setCfgStatus } from "./config-admin.js";
import { configEl, esc, showToast } from "./util.js";

// cfgModelDraft: model -> 完整 model_capabilities 草稿（面板内所有可编辑字段）。
// 草稿是「未保存」的唯一真相：模型列表文本域改动、fetch-models 覆盖、面板编辑
// 都只改这里，保存时按当前模型列表整体提交。
var cfgModelDraft = {};
var cfgModelSelected = "";    // 模型编辑器当前打开的模型
var cfgModelFilter = "";      // 模型列表过滤词（小写）
var cfgEditorSize = null;   // 弹窗用户调整过的尺寸 {w, h}，会话内记忆
var cfgApiKeySaved = false;        // 当前编辑的 provider 是否已配置凭据
var cfgApiKeySource = "";         // 凭据来源：inline / pool / key_store / oauth
var cfgApiKeyClearPending = false; // 用户点了「清除」等待保存生效
var cfgApiKeyMasked = "";          // 已保存 key 的掩码回显（快照 api_key_masked 或本地计算）
var assumedFetchedModels = [];     // 最近一次 fetch-models 的 assumed 模型清单（探测按钮用）
var fetchedModelMetadata = {};     // 最近一次 fetch-models 的 model -> 元数据匹配结果（模型编辑器用）

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

// headers 对象 -> 文本域（每行 "Name: value"，按名字排序保证稳定顺序）。
function headersToText(headers) {
  if (!headers) { return ""; }
  var names = Object.keys(headers).sort();
  var lines = [];
  names.forEach(function (name) {
    var value = String(headers[name] || "");
    lines.push(name + ": " + value);
  });
  return lines.join("\n");
}

// 文本域 -> headers 对象；空输入返回 null（不提交，保持原值），
// 全空行返回 {}（清空 headers 节点）。
function headersFromText(text) {
  if (text == null) { return null; }
  var headers = {};
  var hasLine = false;
  text.split(/\r?\n/).forEach(function (line) {
    var trimmed = String(line || "").trim();
    if (!trimmed) { return; }
    var idx = trimmed.indexOf(":");
    if (idx <= 0) { return; }
    hasLine = true;
    var name = trimmed.slice(0, idx).trim();
    var value = trimmed.slice(idx + 1).trim();
    if (name) { headers[name] = value; }
  });
  return hasLine ? headers : {};
}

export function openProviderEditor(name) {
  var p = name ? providerByName(name) : null;
  cfgModelDraft = {};
  cfgModelSelected = "";
  cfgModelFilter = "";
  fetchedModelMetadata = {};
  closeProtocolPopup();
  renderAssumedFetchedModels(null);
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
  // Headers：回显合并后的值（preset + 用户 config.yaml），保存时整体写回
  // 用户配置（presets.yaml 只读）。值保留模板占位符原文。
  var headersEl = configEl("cfg-provider-headers");
  if (headersEl) {
    headersEl.value = headersToText(p ? (p.headers || null) : null);
  }
  // 模型列表：supported ∪ default_model（去重）
  var models = [];
  (p ? (p.supported_models || []) : []).forEach(function (m) {
    if (m && models.indexOf(m) < 0) { models.push(m); }
  });
  if (p && p.default_model && models.indexOf(p.default_model) < 0) { models.push(p.default_model); }
  configEl("cfg-provider-models").value = models.join("\n");
  var filterEl = configEl("cfg-model-filter");
  if (filterEl) { filterEl.value = ""; }
  rebuildModelEditors(models, p);
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
    // 收起模型编辑器：关闭时把面板里的输入收进草稿并清空选中项，避免下次
    // 打开（openProviderEditor 会重置草稿）时选中项与面板内容不一致。
    collectModelDrafts();
    cfgModelSelected = "";
    overlay.classList.remove("active");
  }
}

// ---------------------------------------------------------------------------
// 模型编辑器：左侧可点击模型列表 + 右侧详细编辑面板
//
// 面板字段 = providers.items.<name>.model_capabilities.<model> 的全部可编辑项
// （后端 providerops.ModelCapabilityView 的投影），保存时以
// POST /web/api/config/providers 的 model_capabilities 字段整体写回。
//
// 取值优先级（与旧 reasoning 编辑器一致）：
//   本地草稿（用户正在编辑、尚未保存） > 模型元数据 > 已保存 provider 配置 > 空。
// ---------------------------------------------------------------------------

// 空草稿：所有字段用「空」表示（字符串 "" / 数字 0 / false / null），保存时
// 转换为“显式清空”语义（0 / false / 空数组），即从 config.yaml 移除该字段。
function emptyModelDraft() {
  return {
    reasoning_model: false,
    reasoning_efforts: [],
    reasoning_effort_budgets: {},
    default_reasoning_effort: "",
    compact_reasoning_effort: "",
    max_context_tokens: 0,
    max_tokens: 0,
    auto_compact_ratio: 0,
    auto_compact_token_limit: 0,
    auto_compact_mode: "",
    supports_remote_compact: false,
    // replay_reasoning_content 是三态：null=未声明（回退名称启发式）。
    replay_reasoning_content: null,
    input_modalities: [],
    image_generation: false,
    images_generations_api: false
  };
}

// 输入模态词表（与 config.yaml 的 model_capabilities.<model>.input_modalities 对应）。
// honored 标注该值是否真被运行时消费：
//   text / image —— agent/loop.go 与 agentconfig/images.go 只认这两个；
//   audio —— ACP 提示路径显式拒绝（"audio prompts are not supported"）；
//   video / file —— 仅随元数据落盘，运行时无行为。
// 前端据此把「勾上就生效」和「只是记一笔」在视觉上区分开。
var MODALITY_VOCAB = [
  { code: "text", label: "文本", honored: true, hint: "text：接收文本提示。几乎所有模型都需要。" },
  { code: "image", label: "图像", honored: true, hint: "image：接收图片输入。与 文本 同时具备时，原生 image_generation 才生效。" },
  { code: "audio", label: "音频", honored: false, hint: "audio：仅写入配置。当前运行时不支持音频提示（ACP 路径会直接拒绝）。" },
  { code: "video", label: "视频", honored: false, hint: "video：仅写入配置。当前运行时无行为（部分网关元数据会带这个值）。" },
  { code: "file", label: "文件", honored: false, hint: "file：仅写入配置。当前运行时无行为。" }
];

// 说明文案随两个跨字段约束变化：
//   1. 只有 text / image 被运行时消费（loop.go / images.go 只认这两个）；
//   2. 原生 image_generation 要求同时具备 text 与 image（images.go 要求
//      两者同时存在），只勾一个等于没开。
function modalityNote(picked, imageGen) {
  var hasText = picked.indexOf("text") >= 0;
  var hasImage = picked.indexOf("image") >= 0;
  if (imageGen && !(hasText && hasImage)) {
    return '<span class="cfg-modality-warn">原生 image_generation 需要同时勾选 文本 + 图像，否则不生效。</span>';
  }
  if (!picked.length) { return "未声明：运行时按模型名启发式推断。"; }
  var inert = picked.filter(function (v) {
    return !MODALITY_VOCAB.some(function (m) { return m.code === v && m.honored; });
  });
  if (inert.length) {
    return "仅 文本 / 图像 会被运行时消费；" + inert.join("、") + " 只写入配置，当前不生效。";
  }
  return "文本 / 图像 会被运行时消费。";
}

// 勾选态 → 隐藏输入的逗号连接值。chip 的选中态由 CSS :has() 负责，这里只
// 同步草稿值与说明文案。顺序按词表顺序归一化，保证同一组选值总是序列化成
// 同一个字符串（便于 diff 与回归断言）。
function syncModalitiesField(panel) {
  var hidden = panel.querySelector('[data-field="input_modalities"]');
  if (!hidden) { return; }
  var picked = [];
  var boxes = panel.querySelectorAll("[data-modality]");
  for (var i = 0; i < boxes.length; i++) {
    if (boxes[i].checked) { picked.push(boxes[i].getAttribute("data-modality")); }
  }
  hidden.value = picked.join(", ");
  refreshModalityNote(panel, picked);
}

// 说明文案还要看原生 image_generation 开关，所以它在面板上重新读一次开关态。
// picked 省略时从当前勾选态重算（原生工具开关变化时走这条路径）。
function refreshModalityNote(panel, picked) {
  var noteEl = panel.querySelector("[data-modality-note]");
  if (!noteEl) { return; }
  var list = picked;
  if (!list) {
    list = [];
    var boxes = panel.querySelectorAll("[data-modality]");
    for (var i = 0; i < boxes.length; i++) {
      if (boxes[i].checked) { list.push(boxes[i].getAttribute("data-modality")); }
    }
  }
  var gen = panel.querySelector('[data-field="image_generation"]');
  noteEl.innerHTML = modalityNote(list, !!(gen && gen.checked));
}

// 已保存 provider 快照中该模型的完整 capability → 草稿（未保存过返回 null）。
function providerModelDraft(provider, model) {
  if (!provider) { return null; }
  var found = null;
  (provider.models || []).forEach(function (m) {
    if (m && m.name === model) {
      var draft = emptyModelDraft();
      draft.reasoning_model = !!m.reasoning_model;
      draft.reasoning_efforts = (m.reasoning_efforts || []).slice();
      draft.reasoning_effort_budgets = Object.assign({}, m.reasoning_effort_budgets || {});
      draft.default_reasoning_effort = m.default_reasoning_effort || "";
      draft.compact_reasoning_effort = m.compact_reasoning_effort || "";
      draft.max_context_tokens = Number(m.max_context_tokens) || 0;
      draft.max_tokens = Number(m.max_tokens) || 0;
      draft.auto_compact_ratio = Number(m.auto_compact_ratio) || 0;
      draft.auto_compact_token_limit = Number(m.auto_compact_token_limit) || 0;
      draft.auto_compact_mode = m.auto_compact_mode || "";
      draft.supports_remote_compact = !!m.supports_remote_compact;
      draft.replay_reasoning_content = (m.replay_reasoning_content === true || m.replay_reasoning_content === false)
        ? m.replay_reasoning_content
        : null;
      draft.input_modalities = (m.input_modalities || []).slice();
      var tools = m.native_tools || {};
      draft.image_generation = !!tools.image_generation;
      draft.images_generations_api = !!tools.images_generations_api;
      found = draft;
    }
  });
  return found;
}

// fetch-models 返回的模型元数据（/models 端点元数据 → model card → 协议兼容
// 默认值的匹配结果）→ 草稿。
function modelDraftFromMetadata(meta) {
  var draft = emptyModelDraft();
  if (!meta) { return draft; }
  draft.reasoning_model = !!meta.reasoning_model;
  draft.reasoning_efforts = (meta.reasoning_efforts || []).slice();
  draft.reasoning_effort_budgets = Object.assign({}, meta.reasoning_effort_budgets || {});
  draft.default_reasoning_effort = meta.default_reasoning_effort || "";
  draft.compact_reasoning_effort = meta.compact_reasoning_effort || "";
  draft.max_context_tokens = Number(meta.max_context_tokens) || 0;
  draft.max_tokens = Number(meta.max_tokens) || 0;
  draft.auto_compact_ratio = Number(meta.auto_compact_ratio) || 0;
  draft.auto_compact_token_limit = Number(meta.auto_compact_token_limit) || 0;
  draft.auto_compact_mode = meta.auto_compact_mode || "";
  draft.supports_remote_compact = !!meta.supports_remote_compact;
  draft.replay_reasoning_content = (meta.replay_reasoning_content === true || meta.replay_reasoning_content === false)
    ? meta.replay_reasoning_content
    : null;
  draft.input_modalities = (meta.input_modalities || []).slice();
  var tools = meta.native_tools || {};
  draft.image_generation = !!tools.image_generation;
  draft.images_generations_api = !!tools.images_generations_api;
  return draft;
}

// 档位预算 map -> "effort: budget" 多行文本（面板文本域 ↔ 配置互转）。
function budgetsToText(budgets) {
  if (!budgets) { return ""; }
  var keys = Object.keys(budgets).sort();
  var lines = [];
  for (var i = 0; i < keys.length; i++) {
    var v = Number(budgets[keys[i]]);
    if (Number.isFinite(v)) { lines.push(keys[i] + ": " + v); }
  }
  return lines.join("\n");
}

// 文本域 -> 档位预算 map；空输入返回 null（不提交，保持原值），全空行返回 {}（清空）。
function budgetsFromText(text) {
  if (text == null) { return null; }
  var out = {};
  var hasLine = false;
  String(text).split(/\r?\n/).forEach(function (line) {
    var trimmed = String(line || "").trim();
    if (!trimmed) { return; }
    var idx = trimmed.lastIndexOf(":");
    if (idx <= 0) { return; }
    var effort = trimmed.slice(0, idx).trim();
    var value = parseInt(trimmed.slice(idx + 1).trim(), 10);
    if (!effort || !Number.isFinite(value)) { return; }
    hasLine = true;
    out[effort] = value;
  });
  return hasLine ? out : {};
}

// 逗号 / 空白分隔的列表 -> 去重保序数组（"low, medium" 与 "low medium" 等价）。
function listFromText(text) {
  var out = [];
  String(text == null ? "" : text).split(/[,，\s]+/).forEach(function (part) {
    part = String(part || "").trim();
    if (part && out.indexOf(part) < 0) { out.push(part); }
  });
  return out;
}

function listToText(list) {
  return (list || []).join(", ");
}

// 唯一化模型列表（去空 + 去重保序），模型列表文本域与选择器共用。
function uniqueModels(models) {
  var out = [];
  (models || []).forEach(function (m) {
    m = String(m == null ? "" : m).trim();
    if (m && out.indexOf(m) < 0) { out.push(m); }
  });
  return out;
}

// 草稿是否为空（所有字段都是空值）：空草稿不写入配置，避免留下
// `model_capabilities: {model: {}}` 噪音节点。
function modelDraftIsEmpty(d) {
  if (!d) { return true; }
  if (d.reasoning_model || d.supports_remote_compact || d.image_generation || d.images_generations_api) { return false; }
  if (d.replay_reasoning_content !== null && d.replay_reasoning_content !== undefined) { return false; }
  if ((d.reasoning_efforts || []).length) { return false; }
  if (Object.keys(d.reasoning_effort_budgets || {}).length) { return false; }
  if ((d.input_modalities || []).length) { return false; }
  var scalars = [
    d.default_reasoning_effort, d.compact_reasoning_effort, d.max_context_tokens,
    d.max_tokens, d.auto_compact_ratio, d.auto_compact_token_limit, d.auto_compact_mode
  ];
  for (var i = 0; i < scalars.length; i++) {
    if (scalars[i] !== "" && scalars[i] !== 0 && scalars[i] !== null && scalars[i] !== undefined) { return false; }
  }
  return true;
}

// 列表行摘要：让用户不打开面板也能看出该模型配了什么。
function modelSummaryChips(model, d) {
  var chips = [];
  function add(text, cls) {
    chips.push('<span class="cfg-model-chip' + (cls ? " " + cls : "") + '">' + esc(text) + "</span>");
  }
  if (d.max_context_tokens > 0) { add("ctx " + formatTokenCount(d.max_context_tokens), "ctx"); }
  if (d.max_tokens > 0) { add("out " + formatTokenCount(d.max_tokens), "out"); }
  if (d.reasoning_model) { add("reasoning", "reasoning"); }
  if ((d.reasoning_efforts || []).length) { add((d.reasoning_efforts || []).length + " 档", "efforts"); }
  if (d.default_reasoning_effort) { add("默认 " + d.default_reasoning_effort); }
  if (d.auto_compact_token_limit > 0) { add("压缩 " + formatTokenCount(d.auto_compact_token_limit), "compact"); }
  if (d.auto_compact_mode) { add(d.auto_compact_mode); }
  if (d.supports_remote_compact) { add("远端压缩", "compact"); }
  if (d.replay_reasoning_content === true) { add("回传 reasoning", "contract"); }
  if (d.replay_reasoning_content === false) { add("禁注入 reasoning", "contract"); }
  if ((d.input_modalities || []).length) { add(d.input_modalities.join("/"), "modalities"); }
  if (d.image_generation) { add("图像生成", "tool"); }
  if (d.images_generations_api) { add("images API", "tool"); }
  if (!chips.length) {
    return '<span class="cfg-model-chip empty">未配置</span>';
  }
  return chips.join("");
}

// 128000 → "128K"，1048576 → "1M"，其余原样。纯展示，不影响提交值。
function formatTokenCount(n) {
  n = Number(n) || 0;
  if (n >= 1000000) {
    var m = n / 1000000;
    return (Math.round(m * 10) / 10) + "M";
  }
  if (n >= 1000) {
    var k = n / 1000;
    return (Math.round(k * 10) / 10) + "K";
  }
  return String(n);
}

// 根据模型列表重建模型编辑器（左侧列表 + 右侧面板）。取值优先级见文件头注释。
// metadataAuthoritative=true（「获取模型列表」覆盖路径）时，未命中元数据的
// 模型不再回退已保存配置——旧配置必须让位于本次元数据重匹配结果，否则
// 页面上仍会残留过期值，与“覆盖”语义不符。
//
// 先把面板里未保存的输入收进草稿，保证任何调用方重建列表都不会丢编辑。
// 「丢弃草稿」的调用方（fetch-models 覆盖）必须先清空 cfgModelSelected，
// 否则这里会把即将被丢弃的草稿写回来。
function rebuildModelEditors(models, provider, metadata, metadataAuthoritative) {
  collectModelDrafts();
  var unique = uniqueModels(models);
  // 面板正在编辑的模型已从列表移除时关闭面板，避免编辑一个不会提交的模型。
  if (cfgModelSelected && unique.indexOf(cfgModelSelected) < 0) {
    cfgModelSelected = "";
  }
  unique.forEach(function (model) {
    if (cfgModelDraft[model]) { return; } // 草稿优先：重建不丢用户已填内容
    if (metadata && Object.prototype.hasOwnProperty.call(metadata, model)) {
      cfgModelDraft[model] = modelDraftFromMetadata(metadata[model]);
      return;
    }
    var saved = metadataAuthoritative ? null : providerModelDraft(provider, model);
    cfgModelDraft[model] = saved || emptyModelDraft();
  });
  // 清理已从模型列表移除的草稿，避免切换 provider 后残留上一个 provider 的内容。
  Object.keys(cfgModelDraft).forEach(function (model) {
    if (unique.indexOf(model) < 0) { delete cfgModelDraft[model]; }
  });
  renderModelList(unique);
  renderModelEditorPanel();
}

// 左侧模型列表：可点击行 + 摘要 chip + 过滤。
function renderModelList(models) {
  var listEl = configEl("cfg-model-list");
  var countEl = configEl("cfg-model-count");
  if (!listEl) { return; }
  var visible = models.filter(function (m) {
    return !cfgModelFilter || m.toLowerCase().indexOf(cfgModelFilter) >= 0;
  });
  if (countEl) {
    countEl.textContent = models.length
      ? (visible.length === models.length
        ? models.length + " 个模型"
        : visible.length + " / " + models.length + " 个模型")
      : "";
  }
  if (models.length === 0) {
    listEl.innerHTML = '<div class="config-empty">在上方填写支持模型后在此逐模型编辑</div>';
    return;
  }
  if (visible.length === 0) {
    listEl.innerHTML = '<div class="config-empty">没有匹配「' + esc(cfgModelFilter) + '」的模型</div>';
    return;
  }
  var html = "";
  visible.forEach(function (model) {
    var d = cfgModelDraft[model] || emptyModelDraft();
    var cls = "cfg-model-item" + (model === cfgModelSelected ? " active" : "") +
      (modelDraftIsEmpty(d) ? "" : " configured");
    html += '<button type="button" class="' + cls + '" data-model="' + esc(model) + '" title="' + esc(model) + '">' +
      '<span class="cfg-model-name">' + esc(model) + "</span>" +
      '<span class="cfg-model-chips">' + modelSummaryChips(model, d) + "</span>" +
      "</button>";
  });
  listEl.innerHTML = html;
}

// 打开某个模型的编辑面板（再次点击同一模型收起）。
function toggleModelEditor(model) {
  collectModelDrafts();
  cfgModelSelected = cfgModelSelected === model ? "" : model;
  renderModelList(currentModels());
  renderModelEditorPanel();
}

// 当前「支持模型」文本域里的模型列表（面板与列表的共同数据源）。
function currentModels() {
  var ta = configEl("cfg-provider-models");
  if (!ta) { return []; }
  return uniqueModels(ta.value.split(/\r?\n/));
}

// 右侧详细面板：按分组渲染该模型的全部可编辑字段。
//
// 布局取向（密度优先，2026-10 改版）：
//   · 4 个分组取代原来的 6 个 fieldset 盒子——盒子边框 + legend + 整段说明
//     文案合计占掉约 1/4 的面板高度，内容却被挤到滚动区外（旧版 915px 内容
//     塞进 320px 视口，要滚 3 屏）。现在用「小标题 + 细分隔线」分区。
//   · `auto_compact_ratio` 归入「自动压缩」与 auto_compact_* 同组（旧版把它
//     和上下文窗口放一起，割裂了同一组三个字段）。
//   · 布尔字段不进数值栅格，单独渲染成一行开关 chip（否则一个孤零零的
//     checkbox 占据 190px 栅格格，看起来像坏掉的空输入框）。
//   · 每个字段统一「中文名 + 灰色 config key」，对照 config.yaml 自解释。
//   · 逐字段含义放 title 提示，不再每个分组挂一段说明文字。
function renderModelEditorPanel() {
  var panel = configEl("cfg-model-editor");
  if (!panel) { return; }
  if (!cfgModelSelected) {
    panel.innerHTML = '<div class="config-empty">点击左侧任一模型打开编辑面板</div>';
    return;
  }
  var model = cfgModelSelected;
  var d = cfgModelDraft[model] || emptyModelDraft();
  var isDefault = (configEl("cfg-provider-default-model").value || "").trim() === model;
  var configured = modelConfiguredCount(d);

  // 分区：小标题 + 细分隔线（不再用 fieldset 盒子，省掉边框与 legend 占位）。
  function section(title, rows) {
    return '<section class="cfg-model-section">' +
      '<div class="cfg-model-section-title">' + esc(title) + "</div>" +
      rows + "</section>";
  }
  // 字段：label 竖排两行——中文名一行、config key 另起一行（横排时 3 列
  // 栅格装不下 auto_compact_token_limit 这类长 key）。key 用灰色等宽小字，
  // 让面板与 config.yaml 的 model_capabilities.<model>.* 逐字段对得上。
  function labelOf(text, key) {
    return '<span class="cfg-label"><span class="cfg-label-text">' + esc(text) + "</span>" +
      (key ? '<span class="cfg-key">' + esc(key) + "</span>" : "") + "</span>";
  }
  function textField(field, text, value, placeholder, title2) {
    return '<label class="cfg-item">' + labelOf(text, field) +
      '<input type="text" data-field="' + esc(field) + '" value="' + esc(value) + '" placeholder="' + esc(placeholder) +
      '" autocomplete="off" spellcheck="false"' + (title2 ? ' title="' + esc(title2) + '"' : "") + "></label>";
  }
  function numField(field, text, value, placeholder, title2) {
    // 0 是「未声明」，渲染成空串让 placeholder 透出（直接显示 0 会被误读成
    // 真实值）。collectModelDrafts 把空输入重新归一为 0，往返不丢语义。
    var shown = Number(value) ? String(value) : "";
    return '<label class="cfg-item">' + labelOf(text, field) +
      '<input type="number" min="0" step="any" class="cfg-num" data-field="' + esc(field) + '" value="' + esc(shown) +
      '" placeholder="' + esc(placeholder) + '" autocomplete="off"' +
      (title2 ? ' title="' + esc(title2) + '"' : "") + "></label>";
  }
  // 布尔开关：整行 chip，不占数值栅格（避免孤零零的 checkbox 像坏掉的空输入框）。
  function toggle(field, text, checked, title2) {
    return '<label class="cfg-switch"' + (title2 ? ' title="' + esc(title2) + '"' : "") + ">" +
      '<input type="checkbox" data-field="' + esc(field) + '"' + (checked ? " checked" : "") + ">" +
      "<span>" + esc(text) + "</span></label>";
  }
  function toggles(rows) { return '<div class="cfg-model-toggles">' + rows + "</div>"; }
  function grid(rows) { return '<div class="cfg-model-grid">' + rows + "</div>"; }
  // 字段下方的单行微注释：只给「格式不看代码就懂」的字段用。
  function note(text) { return '<p class="cfg-model-note">' + text + "</p>"; }

  // 输入模态词表。honored=false 的值会照常写进 config.yaml，但当前运行时
  // 不消费它们（audio 更是被 ACP 提示路径显式拒绝），所以在 chip 上标出来，
  // 免得用户以为勾上就等于能力已生效。
  function modalitiesField(list, imageGen) {
    var picked = [];
    for (var i = 0; i < (list || []).length; i++) {
      var v = String(list[i] || "").trim();
      if (v && picked.indexOf(v) < 0) { picked.push(v); }
    }
    var known = MODALITY_VOCAB.filter(function (m) { return picked.indexOf(m.code) >= 0; });
    // 词表外的值（例如手写配置或将来新增的模态）原样渲染成可删除的 chip，
    // 否则「打开面板→保存」会静默把它丢掉。
    var unknown = picked.filter(function (v) {
      return !MODALITY_VOCAB.some(function (m) { return m.code === v; });
    });
    var chips = known.map(function (m) { return modalityChip(m.code, m.label, true, m.hint, !m.honored); });
    chips = chips.concat(unknown.map(function (v) {
      return modalityChip(v, v, true, "词表外的值：会原样保存，但运行时按名称不识别", true);
    }));
    MODALITY_VOCAB.forEach(function (m) {
      if (picked.indexOf(m.code) < 0) { chips.push(modalityChip(m.code, m.label, false, m.hint, !m.honored)); }
    });
    // 隐藏输入承载草稿值：草稿收集器与既有回归都按 [data-field] 读它，
    // 逗号连接形式与旧的自由文本框完全一致（listFromText 直接可解析）。
    return '<label class="cfg-item cfg-item-wide">' + labelOf("输入模态", "input_modalities") +
      '<input type="hidden" data-field="input_modalities" value="' + esc(listToText(picked)) + '">' +
      '<div class="cfg-modalities" role="group" aria-label="输入模态">' + chips.join("") + "</div>" +
      '<p class="cfg-model-note" data-modality-note>' + modalityNote(picked, !!imageGen) + "</p>" +
      "</label>";
  }
  function modalityChip(code, label, on, hint, inert) {
    return '<label class="cfg-mod' + (inert ? " inert" : "") + '" title="' + esc(hint || "") + '">' +
      '<input type="checkbox" data-modality="' + esc(code) + '"' + (on ? " checked" : "") + ">" +
      '<span class="cfg-mod-name">' + esc(label) + "</span>" +
      '<span class="cfg-mod-code">' + esc(code) + "</span></label>";
  }

  var head = '<div class="cfg-model-editor-head">' +
    '<div class="cfg-model-editor-title">' +
    '<span class="cfg-model-editor-name">' + esc(model) + "</span>" +
    '<span class="cfg-model-editor-sub">' + (configured ? "已配置 " + configured + " 项" : "未配置") + "</span>" +
    "</div>" +
    '<div class="cfg-model-editor-actions">' +
    (isDefault
      ? '<span class="cfg-model-chip default">默认模型</span>'
      : '<button type="button" class="cfg-btn" data-action="set-default-model">设为默认</button>') +
    '<button type="button" class="cfg-btn" data-action="remove-model">移除</button>' +
    "</div></div>";

  // 每个分区自带它的开关与宽字段：开关紧跟分区标题（先说「这一组是什么」，
  // 再说「是/否」），而不是掉到下一段里去——否则 reasoning 的开关会看起来
  // 属于它下面的预算文本域。
  var body =
    section("令牌预算", grid(
      numField("max_context_tokens", "上下文窗口", d.max_context_tokens, "如 200000",
        "model_capabilities.<model>.max_context_tokens：上下文窗口大小（tokens）") +
      numField("max_tokens", "最大输出", d.max_tokens, "如 32000",
        "model_capabilities.<model>.max_tokens：单次响应最大输出（tokens）")
    )) +
    section("Reasoning",
      toggles(toggle("reasoning_model", "reasoning 模型", d.reasoning_model,
        "显式声明该模型是否为推理模型（true / false），避免运行时按名称猜测")) +
      grid(
        textField("reasoning_efforts", "支持档位", listToText(d.reasoning_efforts), "low, medium, high",
          "reasoning_efforts：逗号或空格分隔；显式声明后运行时不再隐式推断") +
        textField("default_reasoning_effort", "默认 effort", d.default_reasoning_effort, "medium",
          "default_reasoning_effort") +
        textField("compact_reasoning_effort", "压缩 effort", d.compact_reasoning_effort, "low",
          "compact_reasoning_effort：上下文压缩时降到的档位")) +
      '<label class="cfg-item cfg-item-wide">' + labelOf("每档 token 预算", "reasoning_effort_budgets") +
      '<textarea rows="2" data-field="reasoning_effort_budgets" placeholder="high: 16000&#10;low: 2000" ' +
      'autocomplete="off" spellcheck="false">' + esc(budgetsToText(d.reasoning_effort_budgets)) + "</textarea></label>" +
      note("每行 <code>effort: tokens</code>；清空表示显式清空该字段。")
    ) +
    section("自动压缩", grid(
      numField("auto_compact_ratio", "压缩比例", d.auto_compact_ratio, "如 0.85",
        "auto_compact_ratio：达到上下文窗口该比例时触发压缩（0 = 不按比例）") +
      numField("auto_compact_token_limit", "压缩阈值", d.auto_compact_token_limit, "如 150000",
        "auto_compact_token_limit：超过该 token 数触发压缩（0 = 不限制）") +
      textField("auto_compact_mode", "压缩模式", d.auto_compact_mode, "如 aggressive", "auto_compact_mode")
    ) +
      toggles(toggle("supports_remote_compact", "支持远端压缩", d.supports_remote_compact,
        "supports_remote_compact：允许调用网关的远端压缩接口"))
    ) +
    section("能力与契约", grid(
      modalitiesField(d.input_modalities, d.image_generation) +
      '<label class="cfg-item cfg-item-wide">' + labelOf("端点契约", "replay_reasoning_content") +
      '<select data-field="replay_reasoning_content">' +
      '<option value=""' + (d.replay_reasoning_content === null ? " selected" : "") + ">未声明（按名称启发式）</option>" +
      '<option value="true"' + (d.replay_reasoning_content === true ? " selected" : "") + ">true 强制回传</option>" +
      '<option value="false"' + (d.replay_reasoning_content === false ? " selected" : "") + ">false 禁止注入</option>" +
      "</select></label>"
    ) +
      toggles(
        toggle("image_generation", "原生 image_generation", d.image_generation,
          "native_tools.image_generation：走 provider 原生图像生成工具") +
        toggle("images_generations_api", "原生 images_generations", d.images_generations_api,
          "native_tools.images_generations_api：走 images/generations 端点")
      )
    );

  panel.innerHTML = head + '<div class="cfg-model-editor-body">' + body + "</div>";
}

// 面板头显示的「已配置 N 项」：统计非空字段，让用户一眼看出该模型配了多少。
function modelConfiguredCount(d) {
  if (!d) { return 0; }
  var n = 0;
  if (d.reasoning_model) { n++; }
  if (d.reasoning_efforts && d.reasoning_efforts.length) { n++; }
  if (d.reasoning_effort_budgets && Object.keys(d.reasoning_effort_budgets).length) { n++; }
  if (d.default_reasoning_effort) { n++; }
  if (d.compact_reasoning_effort) { n++; }
  if (d.max_context_tokens) { n++; }
  if (d.max_tokens) { n++; }
  if (d.auto_compact_ratio) { n++; }
  if (d.auto_compact_token_limit) { n++; }
  if (d.auto_compact_mode) { n++; }
  if (d.supports_remote_compact) { n++; }
  if (d.replay_reasoning_content !== null && d.replay_reasoning_content !== undefined) { n++; }
  if (d.input_modalities && d.input_modalities.length) { n++; }
  if (d.image_generation) { n++; }
  if (d.images_generations_api) { n++; }
  return n;
}

// 把面板内正在编辑的字段收进草稿（提交前、切换模型前、删除模型前调用）。
// 面板每次打开都整体重渲染，所以这里只需读取当前打开的那一个面板。
function collectModelDrafts() {
  var panel = configEl("cfg-model-editor");
  if (!panel || !cfgModelSelected) { return; }
  var d = cfgModelDraft[cfgModelSelected] || emptyModelDraft();
  var controls = panel.querySelectorAll("[data-field]");
  for (var i = 0; i < controls.length; i++) {
    var el = controls[i];
    var field = el.getAttribute("data-field");
    if (el.type === "checkbox") { d[field] = el.checked; }
    else if (field === "replay_reasoning_content") {
      d[field] = el.value === "" ? null : el.value === "true";
    } else if (field === "reasoning_efforts" || field === "input_modalities") {
      d[field] = listFromText(el.value);
    } else if (field === "reasoning_effort_budgets") {
      var budgets = budgetsFromText(el.value);
      if (budgets !== null) { d[field] = budgets; }
    } else if (el.type === "number") {
      var n = parseFloat(el.value);
      d[field] = Number.isFinite(n) && n >= 0 ? n : 0;
    } else {
      d[field] = String(el.value || "").trim();
    }
  }
  cfgModelDraft[cfgModelSelected] = d;
}

// 面板动作：设为默认模型 / 从支持模型列表移除。
function handleModelEditorAction(action) {
  collectModelDrafts();
  var model = cfgModelSelected;
  if (!model) { return; }
  if (action === "set-default-model") {
    var defEl = configEl("cfg-provider-default-model");
    if (defEl) { defEl.value = model; }
    renderModelList(currentModels());
    renderModelEditorPanel();
    return;
  }
  if (action === "remove-model") {
    var ta = configEl("cfg-provider-models");
    if (ta) {
      var kept = currentModels().filter(function (m) { return m !== model; });
      ta.value = kept.join("\n");
      delete cfgModelDraft[model];
      cfgModelSelected = "";
      rebuildModelEditors(kept, null);
    }
  }
}

function saveProvider(ev) {
  ev.preventDefault();
  var statusEl = configEl("cfg-provider-status");
  var nameEl = configEl("cfg-provider-name");
  var name = (nameEl && nameEl.value ? nameEl.value : "").trim();
  if (!name) { setCfgStatus(statusEl, "名称不能为空", "err"); return; }
  collectModelDrafts();
  var models = uniqueModels((configEl("cfg-provider-models").value || "").split(/\r?\n/));
  // 模型编辑器写回：面板内所有字段都是**显式值**（0 / false / 空数组 = 清空
  // 该字段），所以这里提交完整条目而不是部分字段。空草稿也必须提交——用户把
  // 某个已配置模型清空时，草稿变空正是“删除该 model_capabilities 条目”的
  // 意图；跳过它会让旧配置留在 config.yaml 里。没有草稿的模型才跳过。
  var capabilities = {};
  models.forEach(function (model) {
    var d = cfgModelDraft[model];
    if (!d) { return; }
    capabilities[model] = {
      reasoning_model: !!d.reasoning_model,
      reasoning_efforts: (d.reasoning_efforts || []).slice(),
      reasoning_effort_budgets: Object.assign({}, d.reasoning_effort_budgets || {}),
      default_reasoning_effort: d.default_reasoning_effort || "",
      compact_reasoning_effort: d.compact_reasoning_effort || "",
      max_context_tokens: d.max_context_tokens || 0,
      max_tokens: d.max_tokens || 0,
      auto_compact_ratio: d.auto_compact_ratio || 0,
      auto_compact_token_limit: d.auto_compact_token_limit || 0,
      auto_compact_mode: d.auto_compact_mode || "",
      supports_remote_compact: !!d.supports_remote_compact,
      replay_reasoning_content: (d.replay_reasoning_content === true || d.replay_reasoning_content === false)
        ? d.replay_reasoning_content
        : null,
      input_modalities: (d.input_modalities || []).slice(),
      native_tools: {
        image_generation: !!d.image_generation,
        images_generations_api: !!d.images_generations_api
      }
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
    model_capabilities: capabilities
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
  // Headers：非空提交整体写回用户配置；用户主动清空文本域（无任何行）时
  // 提交 {} 清除 headers 节点。
  var headersVal = configEl("cfg-provider-headers");
  if (headersVal) {
    var headersPayload = headersFromText(headersVal.value);
    if (headersPayload !== null) { payload.headers = headersPayload; }
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
// GET /models 清单：优先用表单里新填的 api key，否则用已保存的 key。
// 后端按协议分类（与 aicli login 同源）后，models 只含与当前 provider
// 协议一致的模型。执行结果是“覆盖”：支持模型列表整体替换为本次结果，
// 模型编辑器按返回的 model_metadata（模型元数据重匹配结果）重建草稿，
// 旧模型 ID 与旧草稿不再保留；其他协议模型仅在状态栏提示，
// 不自动合并（assumed 模型可确认后手动合并）。
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
      // 后端已按协议分类（与 aicli login 同源）：models 只含与当前 provider
      // 协议一致且有 model card 数据支持的模型（openai 协议例外：/v1/models
      // 是 OpenAI 风格端点，未匹配卡片的模型按当前协议假定可用，照旧合并）；
      // 无卡数据的其他协议模型在 assumed_models / groups 里单列，不合并。
      //
      // 覆盖语义：模型列表与 reasoning 配置整体替换为本次结果，reasoning 按
      // model_metadata（模型元数据重匹配结果）重建，旧模型 ID / 旧草稿不再保留。
      // 模型列表为空（网关未返回可合并模型）时保留原列表，避免误清空配置。
      var metadata = (json.model_metadata && typeof json.model_metadata === "object") ? json.model_metadata : null;
      var fetched = json.models || [];
      var replacedCount = (fetched.length > 0) ? replaceFetchedModels(fetched, metadata, metadata !== null) : 0;
      var total = json.models_total || fetched.length;
      var okMsg = "已获取 " + total + " 个模型" + (json.endpoint ? "（" + json.endpoint + "）" : "");
      if (json.protocol) { okMsg += "，协议 " + json.protocol; }
      if (replacedCount > 0) {
        okMsg += "。已覆盖支持模型列表为 " + replacedCount + " 个" + (json.protocol ? " " + json.protocol : "") + " 协议模型";
        if (metadata) {
          okMsg += "，并按模型元数据重匹配模型配置（命中 " + countMetadataMatches(fetched, metadata) + "/" + replacedCount + "）";
        } else {
          okMsg += "（后端未返回模型元数据，模型配置沿用已保存配置）";
        }
      } else {
        okMsg += "。本次未返回可覆盖的模型，已保留原支持模型列表";
      }
      var assumed = json.assumed_models || [];
      if (assumed.length) {
        okMsg += "。另有 " + assumed.length + " 个模型无 " + (json.protocol || "当前") +
          " 协议匹配数据，未自动覆盖（网关未提供协议支持证据，确认后可在下方列表手动合并）";
      }
      var otherCount = json.other_models_count || 0;
      var filtered = false;
      if (otherCount > 0) {
        // 各组只统计“不属于主组”的模型（跨组去重）：一个模型可能同时命中
        // 多张多协议卡而出现在多个组里，但它对当前 provider 仍然可用，
        // 不应重复计入“被过滤”。各组 other_models 之和 == other_models_count。
        var parts = [];
        (json.groups || []).forEach(function (g) {
          if (!g.primary && g.other_models && g.other_models.length) {
            parts.push((g.protocol || "unknown") + "×" + g.other_models.length);
          }
        });
        okMsg += "。已按协议过滤 " + otherCount + " 个其他协议模型" +
          (parts.length ? "（" + parts.join("、") + "）" : "") +
          "；如需使用请用「自动导入」生成对应协议的 provider";
        filtered = true;
      }
      renderAssumedFetchedModels(assumed, json.protocol);
      if (statusEl) {
        statusEl.textContent = okMsg;
        statusEl.className = filtered ? "cfg-hint-inline warn" : "cfg-hint-inline ok";
      }
      showToast("已获取 " + total + " 个模型");
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

// 「获取模型列表」覆盖路径：用本次拉取结果整体替换「支持模型」文本域
// （去重保序），清空旧草稿与旧元数据映射，并按新元数据重建模型编辑器
// 草稿。metadataAuthoritative=true 时未命中元数据的模型清空旧配置；
// 后端未返回元数据（旧后端）时回退已保存配置，避免误清空。
// 返回实际写入的模型数量；列表为空时不改动表单并返回 0。
function replaceFetchedModels(fetched, metadata, metadataAuthoritative) {
  var ta = configEl("cfg-provider-models");
  if (!ta || !fetched) { return 0; }
  var models = uniqueModels(fetched);
  if (models.length === 0) { return 0; }
  ta.value = models.join("\n");
  // 覆盖语义：旧模型 / 旧草稿不再保留。先清选中项，否则 rebuildModelEditors
  // 开头的 collectModelDrafts() 会把即将丢弃的草稿写回草稿表。
  cfgModelSelected = "";
  cfgModelDraft = {};
  fetchedModelMetadata = metadata || {};
  var orig = (configEl("cfg-provider-original-name").value || "").trim();
  rebuildModelEditors(models, orig ? providerByName(orig) : null, fetchedModelMetadata, !!metadataAuthoritative);
  return models.length;
}

// 把拉取到的模型 id 合并进「支持模型」文本域（去重，保留手填行）。
// 仅用于 assumed 模型的手动合并按钮：在现有列表上追加，并让新增行走
// fetch-models 返回的元数据回显；已有行保留草稿/已保存配置。
function mergeFetchedModels(fetched) {
  var ta = configEl("cfg-provider-models");
  if (!ta || !fetched) { return; }
  var existing = uniqueModels(ta.value.split(/\r?\n/));
  var added = 0;
  fetched.forEach(function (m) {
    m = String(m).trim();
    if (!m) { return; }
    if (existing.indexOf(m) < 0) { existing.push(m); added++; }
  });
  ta.value = existing.join("\n");
  if (added > 0) {
    collectModelDrafts();
    var orig = (configEl("cfg-provider-original-name").value || "").trim();
    rebuildModelEditors(existing, orig ? providerByName(orig) : null, fetchedModelMetadata, false);
  }
}

// 统计本次覆盖的模型里命中模型元数据的数量（状态栏提示用）。
function countMetadataMatches(models, metadata) {
  if (!metadata) { return 0; }
  var count = 0;
  (models || []).forEach(function (m) {
    if (Object.prototype.hasOwnProperty.call(metadata, String(m))) { count++; }
  });
  return count;
}

// 渲染「未自动合并模型」折叠块：列出无当前协议匹配数据（仅靠协议 fallback
// 假定可用）的模型 ID。用户确认网关确实支持这些模型后可一键全部合并
// （去重追加，同 mergeFetchedModels）；否则保持不合并，避免污染
// supported_models（实测部分网关会拒绝这些模型走非 openai 端点）。
// 「探测协议支持」按钮则对这批模型做跨协议最小补全实测（probe-models），
// supported 结论由后端写回用户级 model_cards.yaml，之后重新获取列表即可
// 自动分类合并——把“人工确认”升级成“有证据的自动判定”。
function renderAssumedFetchedModels(assumed, protocol) {
  var box = configEl("cfg-provider-fetch-models-assumed");
  if (!box) { return; }
  var listEl = configEl("cfg-provider-fetch-models-assumed-list");
  var countEl = configEl("cfg-provider-fetch-models-assumed-count");
  var mergeBtn = configEl("cfg-provider-fetch-models-assumed-merge");
  var probeBtn = configEl("cfg-provider-fetch-models-assumed-probe");
  var probeStatus = configEl("cfg-provider-fetch-models-assumed-probe-status");
  var probeResult = configEl("cfg-provider-fetch-models-assumed-probe-result");
  assumedFetchedModels = (assumed || []).slice();
  if (!assumed || !assumed.length) {
    box.hidden = true;
    if (listEl) { listEl.textContent = ""; }
    if (mergeBtn) { mergeBtn.onclick = null; }
    if (probeBtn) { probeBtn.onclick = null; }
    if (probeStatus) { probeStatus.textContent = ""; probeStatus.className = "cfg-hint-inline"; }
    if (probeResult) { probeResult.textContent = ""; }
    return;
  }
  box.hidden = false;
  if (countEl) { countEl.textContent = assumed.length + " 个无 " + (protocol || "当前协议") + " 协议匹配数据"; }
  if (listEl) { listEl.textContent = assumed.join("\n"); }
  if (mergeBtn) {
    mergeBtn.onclick = function () {
      mergeFetchedModels(assumed);
      box.hidden = true;
    };
  }
  if (probeBtn) { probeBtn.onclick = function () { probeAssumedModels(assumedFetchedModels); }; }
  // 新一轮获取列表后清掉上一轮探测结果，避免与最新 assumed 集合错位。
  if (probeStatus) { probeStatus.textContent = ""; probeStatus.className = "cfg-hint-inline"; }
  if (probeResult) { probeResult.textContent = ""; }
}

// 对 assumed 模型清单做跨协议探测：POST /web/api/config/providers/probe-models。
// 后端对每个模型 × 协议发送一次最小补全请求（复用运行时 adapter 的
// 请求构造与鉴权头），按错误分类学给出 supported / unsupported / unknown；
// supported 结论写回用户级 model_cards.yaml（探测卡片），重新获取列表后
// 这些模型会带卡片数据自动合并。探测期间按钮禁用，结果渲染为结论矩阵。
function probeAssumedModels(models) {
  var btn = configEl("cfg-provider-fetch-models-assumed-probe");
  var statusEl = configEl("cfg-provider-fetch-models-assumed-probe-status");
  var resultEl = configEl("cfg-provider-fetch-models-assumed-probe-result");
  if (!btn || btn.disabled) { return; }
  if (!models || !models.length) { return; }
  var apiKeyVal = (configEl("cfg-provider-api-key").value || "").trim();
  var payload = {
    name: (configEl("cfg-provider-original-name").value || "").trim(),
    protocol: (configEl("cfg-provider-protocol").value || "").trim(),
    base_url: (configEl("cfg-provider-base-url").value || "").trim(),
    models: models
  };
  if (apiKeyVal) { payload.api_key = apiKeyVal; }
  btn.disabled = true;
  var oldText = btn.textContent;
  btn.textContent = "探测中…";
  if (statusEl) { statusEl.textContent = "正在对 " + models.length + " 个模型做跨协议探测（每模型×协议一次最小补全请求），请稍候…"; statusEl.className = "cfg-hint-inline"; }
  if (resultEl) { resultEl.textContent = ""; }
  fetch("/web/api/config/providers/probe-models", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload)
  })
    .then(function (res) {
      return res.json().catch(function () { return { status: "error", reason: "bad response" }; });
    })
    .then(function (json) {
      if (json.status !== "ok" || !json.results) {
        var msg = "探测失败: " + (json.reason || json.status || "unknown");
        if (statusEl) { statusEl.textContent = msg; statusEl.className = "cfg-hint-inline err"; }
        showToast(msg, "err");
        return;
      }
      renderProbeResults(json, resultEl);
      var supportedCount = (json.supported_models || []).length;
      var summary = "探测完成：" + supportedCount + " 个模型有协议实测支持";
      if ((json.written_cards || []).length) {
        summary += "；" + json.written_cards.length + " 条结论已写入 " + (json.user_cards_path || "用户卡片");
      }
      if (json.write_back_error) { summary += "。⚠ 写回失败: " + json.write_back_error; }
      summary += "。重新点「获取模型列表」即可自动合并 supported 模型";
      if (statusEl) {
        statusEl.textContent = summary;
        statusEl.className = json.write_back_error ? "cfg-hint-inline warn" : "cfg-hint-inline ok";
      }
      showToast("探测完成");
    })
    .catch(function (err) {
      var msg = "探测失败: " + err;
      if (statusEl) { statusEl.textContent = msg; statusEl.className = "cfg-hint-inline err"; }
      showToast(msg, "err");
    })
    .then(function () {
      btn.disabled = false;
      btn.textContent = oldText;
    });
}

// 渲染探测结论矩阵：每行一个模型 + 各协议结论 chip（悬停显示判定依据），
// 末行附写回说明。verdict → 样式：supported 绿 / unsupported 红 / unknown 黄。
function renderProbeResults(json, resultEl) {
  if (!resultEl) { return; }
  resultEl.textContent = "";
  (json.results || []).forEach(function (result) {
    var row = document.createElement("div");
    row.className = "cfg-probe-row";
    var name = document.createElement("span");
    name.className = "cfg-probe-model";
    name.textContent = result.model || "";
    row.appendChild(name);
    (result.probes || []).forEach(function (probe) {
      var chip = document.createElement("span");
      var verdict = probe.verdict || "unknown";
      chip.className = "cfg-probe-chip " + verdict;
      chip.textContent = (probe.protocol || "?") + " " + verdict;
      var tip = (probe.detail || "").trim();
      if (probe.http_status) { tip = "HTTP " + probe.http_status + (tip ? " · " + tip : ""); }
      chip.title = tip || verdict;
      row.appendChild(chip);
    });
    resultEl.appendChild(row);
  });
  if ((json.written_cards || []).length && json.user_cards_path) {
    var note = document.createElement("div");
    note.className = "cfg-probe-note";
    note.textContent = "已写入探测卡片 → " + json.user_cards_path + "（手工卡片优先，探测卡可随时删除）";
    resultEl.appendChild(note);
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
        rebuildModelEditors(currentModels(), null);
      });
    }
    // 模型编辑器：左侧列表点击选中、过滤词重绘。
    var modelList = configEl("cfg-model-list");
    if (modelList) {
      // 列表内容每次重渲染，事件委托绑定一次。
      modelList.addEventListener("click", function (e) {
        var btn = e.target && e.target.closest ? e.target.closest("[data-model]") : null;
        if (!btn) { return; }
        e.preventDefault();
        toggleModelEditor(btn.getAttribute("data-model") || "");
      });
    }
    var modelFilter = configEl("cfg-model-filter");
    if (modelFilter) {
      modelFilter.addEventListener("input", function () {
        cfgModelFilter = String(modelFilter.value || "").trim().toLowerCase();
        renderModelList(currentModels());
      });
    }
    var modelPanel = configEl("cfg-model-editor");
    if (modelPanel) {
      modelPanel.addEventListener("click", function (e) {
        var btn = e.target && e.target.closest ? e.target.closest("[data-action]") : null;
        if (!btn) { return; }
        e.preventDefault();
        handleModelEditorAction(btn.getAttribute("data-action") || "");
      });
      // 输入模态 chip 与原生工具开关都会改变「说明文案」（原生图像生成要求
      // 文本+图像同时具备）。change 先于切换模型/保存触发，所以隐藏输入
      // 在 collectModelDrafts 读到的一定是最新值。
      modelPanel.addEventListener("change", function (e) {
        var mod = e.target && e.target.closest ? e.target.closest("[data-modality]") : null;
        if (mod) { syncModalitiesField(modelPanel); }
        refreshModalityNote(modelPanel);
      });
    }
  initGlobalProtocolPopupDismiss();
}
