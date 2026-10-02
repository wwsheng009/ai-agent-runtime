// 浮动 composer 面板里的 cfg-bar:provider/model/reasoning 切换器、model 自定义 popup、
// 权威配置同步与轮询（面板本身的位置 / 折叠见 js/composer.js）。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { apiFetch, esc } from "./util.js";

// ---- provider / model / reasoning_effort 配置选择器 ----
// 权威值来自 GET /web/api/runtime；切换动作构造 /model 命令注入
// /web/api/input，由主循环统一执行（与 TTY 行为一致）。
var cfg = { provider: "", model: "", reasoning: "" };
var cfgUiDirty = false; // 切换提交后保持用户选择，等待权威确认前不重设 UI
var runtimeMetaCache = null; // 最近一次 /web/api/runtime 全量快照
var CFG_GENERIC_REASONING = ["minimal", "low", "medium", "high", "max", "xhigh"];

function cfgEls() {
  return {
    provider: document.getElementById("cfg-provider"),
    providerToggle: document.getElementById("cfg-provider-toggle"),
    providerPopup: document.getElementById("cfg-provider-popup"),
    model: document.getElementById("cfg-model"),
    modelOpts: document.getElementById("cfg-model-options"),
    modelToggle: document.getElementById("cfg-model-toggle"),
    modelPopup: document.getElementById("cfg-model-popup"),
    modelCount: document.getElementById("cfg-model-count"),
    reasoning: document.getElementById("cfg-reasoning"),
    toggleValue: document.getElementById("cfg-toggle-value"),
    status: document.getElementById("cfg-status")
  };
}

// ---- provider / model 弹出列表的搜索过滤（共用一套语义）----
// 过滤框固定在列表上方；命中项按 data-filter-value 显隐而不是重建列表，
// 这样输入框焦点不会在每次按键后丢失。计数显示「命中 / 总数」或「N 项」，
// 零命中时给出替代路径提示（自定义模型名 / 换个关键词）。
function popupFilterRowHTML(kind, placeholder) {
  return '<div class="cfg-popup-filter">' +
    '<input type="text" data-popup-filter="' + esc(kind) + '" placeholder="' + esc(placeholder) +
    '" aria-label="' + esc(placeholder) + '" title="按名称子串筛选（不区分大小写）" autocomplete="off">' +
    '<span class="cfg-popup-count" data-popup-count></span>' +
    "</div>";
}

function popupFilterInput(popup) {
  return popup ? popup.querySelector("[data-popup-filter]") : null;
}

function popupFilterKeyword(popup) {
  var input = popupFilterInput(popup);
  return input ? String(input.value || "").trim().toLowerCase() : "";
}

// 返回可见项数；无查询串时计数显示总数，有查询串时显示「命中 / 总数」。
function applyPopupFilter(popup) {
  if (!popup) { return 0; }
  var keyword = popupFilterKeyword(popup);
  var items = popup.querySelectorAll("[data-filter-value]");
  var visible = 0;
  for (var i = 0; i < items.length; i++) {
    var value = String(items[i].getAttribute("data-filter-value") || "").toLowerCase();
    var hit = !keyword || value.indexOf(keyword) >= 0;
    items[i].style.display = hit ? "" : "none";
    if (hit) { visible++; }
  }
  var empty = popup.querySelector(".cfg-popup-no-match");
  if (empty) { empty.style.display = keyword && visible === 0 ? "block" : "none"; }
  var count = popup.querySelector("[data-popup-count]");
  if (count) {
    count.textContent = items.length === 0 ? "" :
      (keyword ? visible + " / " + items.length : items.length + " 项");
  }
  return visible;
}

function clearPopupFilter(popup) {
  var input = popupFilterInput(popup);
  if (input) { input.value = ""; }
  applyPopupFilter(popup);
}

function focusPopupFilter(popup) {
  var input = popupFilterInput(popup);
  if (input) { input.focus(); }
}

function firstVisiblePopupItem(popup) {
  if (!popup) { return null; }
  var items = popup.querySelectorAll("[data-filter-value]");
  for (var i = 0; i < items.length; i++) {
    if (items[i].style.display !== "none") { return items[i]; }
  }
  return null;
}

// 方向键在可见项之间移动焦点（过滤后跳过的隐藏项不参与）。
function movePopupFocus(popup, step) {
  var items = popup.querySelectorAll("[data-filter-value]");
  var visible = [];
  for (var i = 0; i < items.length; i++) {
    if (items[i].style.display !== "none") { visible.push(items[i]); }
  }
  if (visible.length === 0) { return; }
  var current = visible.indexOf(document.activeElement);
  var next = current < 0
    ? (step > 0 ? 0 : visible.length - 1)
    : (current + step + visible.length) % visible.length;
  visible[next].focus();
}

// 弹出列表键盘契约（provider / model 共用）：
// - 检索框里 Enter = 选中首个可见项，↑/↓ = 在可见项间移动焦点；
// - Esc 关闭并把焦点还给触发控件（由调用方的 onDismiss 决定）。
function handlePopupKeydown(event, valueAttr, onSelect, onDismiss) {
  var popup = event.currentTarget;
  if (event.key === "Escape") {
    event.preventDefault();
    onDismiss();
    return;
  }
  if (event.key === "Enter") {
    var input = popupFilterInput(popup);
    if (input && event.target === input) {
      var first = firstVisiblePopupItem(popup);
      var value = first ? first.getAttribute(valueAttr) : "";
      if (value) {
        event.preventDefault();
        onSelect(value);
      }
    }
    return;
  }
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    movePopupFocus(popup, event.key === "ArrowDown" ? 1 : -1);
  }
}

function findRuntimeProvider(meta, name) {
  var providers = (meta && meta.providers) || [];
  for (var i = 0; i < providers.length; i++) {
    if (providers[i].name === name) { return providers[i]; }
  }
  return null;
}

function findModelDetail(provider, model) {
  if (!provider || !model) { return null; }
  var details = provider.model_details || [];
  for (var i = 0; i < details.length; i++) {
    if (details[i].name === model) { return details[i]; }
  }
  return null;
}

// 解析指定 provider/model 的 reasoning 可选项：优先 model_details，
// 其次 current.reasoning_options（当前生效模型），最后回退通用列表。
// 返回 { options, def, supported }，options 为空表示用通用列表。
function resolveReasoningOptions(meta, providerName, modelName) {
  var p = findRuntimeProvider(meta, providerName);
  var detail = p ? findModelDetail(p, modelName) : null;
  if (detail && detail.reasoning_efforts && detail.reasoning_efforts.length > 0) {
    return { options: detail.reasoning_efforts.slice(), def: detail.default_reasoning_effort || "", supported: true };
  }
  if (detail && (detail.default_reasoning_effort || detail.reasoning_model)) {
    return { options: [], def: detail.default_reasoning_effort || "", supported: true };
  }
  var cur = (meta && meta.current) || {};
  if (providerName && modelName && cur.provider === providerName && cur.model === modelName &&
      cur.reasoning_options && cur.reasoning_options.length > 0) {
    return { options: cur.reasoning_options.slice(), def: cur.reasoning_default || "", supported: !!cur.reasoning_supported };
  }
  return { options: [], def: "", supported: false };
}

// 动态渲染 reasoning 下拉：options 为空时用通用列表兜底，并保证当前值可见。
function renderReasoningSelect(els, options, currentValue, defValue) {
  if (!els.reasoning) { return; }
  var list = (options && options.length > 0) ? options.slice() : CFG_GENERIC_REASONING.slice();
  if (currentValue && list.indexOf(currentValue) < 0) { list.push(currentValue); }
  var html = '<option value="">(默认' + (defValue ? ": " + esc(defValue) : "") + ")</option>";
  list.forEach(function (v) {
    html += '<option value="' + esc(v) + '"' + (v === currentValue ? " selected" : "") + ">" + esc(v) + "</option>";
  });
  els.reasoning.innerHTML = html;
  els.reasoning.value = currentValue || "";
  var title = "切换 reasoning_effort（/model -r）";
  if (defValue) { title += "，模型默认: " + defValue; }
  if (!options || options.length === 0) { title += "（该模型未声明可用值，显示通用列表）"; }
  els.reasoning.title = title;
}

// 模型列表渲染：input 输入框只显示当前一个值（单值），全量列表同时写入
// datalist（输入联想）与自定义 popup（点击 ▼ 弹出，与 provider/reasoning
// 的 <select> 下拉对齐）。count 徽标显示可选数，避免“为什么只有一个模型”的困惑。
function renderModelDatalist(els, provider) {
  var models = provider ? (provider.models || []) : [];
  if (els.modelOpts) {
    els.modelOpts.innerHTML = models.map(function (m) {
      return '<option value="' + esc(m) + '"></option>';
    }).join("");
  }
  if (els.modelCount) {
    els.modelCount.textContent = models.length > 0 ? ("共 " + models.length + " 个") : "";
    els.modelCount.title = models.length > 0 ? ("可选模型: " + models.join(", ")) : "该 provider 暂无预置模型，可直接输入";
  }
  if (els.model) {
    els.model.title = "切换模型（/model --model）：可直接输入自定义模型名，或点 ▼ 弹出可搜索列表" +
      (models.length > 0 ? "（共 " + models.length + " 个： " + models.slice(0, 8).join(", ") + (models.length > 8 ? "…" : "") + "）" : "");
  }
  renderModelPopup(els, provider, models);
  return models;
}

// 自定义弹出列表：解决 <input list=datalist> 无下拉箭头、点击不弹出、
// 各浏览器表现不一致的问题。popup 与 datalist 同数据源，点选即切换。
// 列表顶部提供检索框：按子串过滤（不区分大小写），重渲染时保留在途查询串。
function renderModelPopup(els, provider, models) {
  var popup = els.modelPopup;
  if (!popup) { return; }
  models = models || (provider ? (provider.models || []) : []);
  var curVal = (els.model && els.model.value) || "";
  var keyword = popupFilterKeyword(popup);
  var filterHadFocus = document.activeElement === popupFilterInput(popup);
  if (!models || models.length === 0) {
    popup.innerHTML = '<div class="cfg-model-empty">暂无预置模型，可直接输入自定义模型名</div>';
    return;
  }
  var def = provider ? (provider.default_model || "") : "";
  var html = popupFilterRowHTML("model", "筛选模型…");
  html += '<div class="cfg-popup-list">';
  models.forEach(function (m) {
    var cls = "cfg-model-item" + (m === curVal ? " current" : "");
    var tag = m === def ? '<span class="tag">默认</span>' : (m === curVal ? '<span class="tag">当前</span>' : "");
    html += '<button type="button" class="' + cls + '" data-model="' + esc(m) + '" data-filter-value="' + esc(m) + '" title="' + esc(m) + '">' +
      esc(m) + tag + "</button>";
  });
  html += "</div>";
  html += '<div class="cfg-popup-no-match" style="display:none">没有匹配的模型，可直接输入自定义模型名</div>';
  popup.innerHTML = html;
  var filter = popupFilterInput(popup);
  if (filter && keyword) { filter.value = keyword; }
  applyPopupFilter(popup);
  // 元数据轮询可能在用户打字时重渲染列表：把焦点还给检索框，输入不中断。
  if (filter && filterHadFocus) { filter.focus(); }
}

function closeModelPopup() {
  var els = cfgEls();
  if (!els.modelPopup) { return; }
  clearPopupFilter(els.modelPopup);
  els.modelPopup.style.display = "none";
}

function toggleModelPopup() {
  var els = cfgEls();
  if (!els.modelPopup || !els.model) { return; }
  if (els.modelPopup.style.display !== "none" && els.modelPopup.innerHTML) {
    closeModelPopup();
    return;
  }
  // 以当前选中 provider 重新渲染，保证打开时即最新列表。
  if (runtimeMetaCache) {
    var selProvider = els.provider ? (els.provider.value || cfg.provider) : cfg.provider;
    renderModelDatalist(els, findRuntimeProvider(runtimeMetaCache, selProvider));
  } else if (!els.modelPopup.innerHTML) {
    els.modelPopup.innerHTML = '<div class="cfg-model-empty">加载中…</div>';
  }
  els.modelPopup.style.display = "block";
  // 打开即聚焦检索框：直接打字过滤，而不是先滚动长列表。
  focusPopupFilter(els.modelPopup);
}

function selectModelFromPopup(modelName) {
  var els = cfgEls();
  if (!els.model) { return; }
  modelName = (modelName || "").trim();
  if (!modelName) { return; }
  els.model.value = modelName;
  closeModelPopup();
  previewModelChange(modelName);
  applyRuntimeConfig();
  els.model.focus();
}

// ---- provider 选择器（可搜索 combo）----
// 原生 <select> 的选项由浏览器绘制，脚本无法在其上叠加检索框；改为只读输入框
// 显示当前值 + 自定义 popup（顶部检索框）。输入框只读 = provider 必须来自列表，
// 不存在把半截查询串当 provider 提交的状态；选择后走与旧 change 相同的提交路径。
function renderProviderPopup(els, providers, selected) {
  var popup = els.providerPopup;
  if (!popup) { return; }
  providers = providers || [];
  var keyword = popupFilterKeyword(popup);
  var filterHadFocus = document.activeElement === popupFilterInput(popup);
  if (providers.length === 0) {
    popup.innerHTML = '<div class="cfg-model-empty">暂无可用 provider</div>';
    return;
  }
  var html = popupFilterRowHTML("provider", "筛选 provider…");
  html += '<div class="cfg-popup-list">';
  providers.forEach(function (p) {
    var cls = "cfg-model-item" + (p.name === selected ? " current" : "");
    var tag = p.name === selected ? '<span class="tag">当前</span>' : "";
    html += '<button type="button" class="' + cls + '" data-provider="' + esc(p.name) + '" data-filter-value="' + esc(p.name) + '" title="' + esc(p.name) + '">' +
      esc(p.name) + tag + "</button>";
  });
  html += "</div>";
  html += '<div class="cfg-popup-no-match" style="display:none">没有匹配的 provider</div>';
  popup.innerHTML = html;
  var filter = popupFilterInput(popup);
  if (filter && keyword) { filter.value = keyword; }
  applyPopupFilter(popup);
  if (filter && filterHadFocus) { filter.focus(); }
}

function renderProviderCombo(els, providers, selected) {
  if (els.provider) {
    els.provider.value = selected || "";
    els.provider.title = "切换 provider（/model --provider）：点击或按 ▼ 打开可搜索列表" +
      (providers.length > 0 ? "（共 " + providers.length + " 个）" : "");
  }
  renderProviderPopup(els, providers, selected);
}

function closeProviderPopup() {
  var els = cfgEls();
  if (!els.providerPopup) { return; }
  clearPopupFilter(els.providerPopup);
  els.providerPopup.style.display = "none";
  if (els.provider) { els.provider.setAttribute("aria-expanded", "false"); }
}

function openProviderPopup() {
  var els = cfgEls();
  if (!els.providerPopup || !els.provider) { return; }
  // 以最新缓存重渲染：运行时候选可能在面板关闭期间变化。
  if (runtimeMetaCache) {
    renderProviderCombo(els, runtimeMetaCache.providers || [], els.provider.value || cfg.provider);
  } else if (!els.providerPopup.innerHTML) {
    els.providerPopup.innerHTML = '<div class="cfg-model-empty">加载中…</div>';
  }
  els.providerPopup.style.display = "block";
  els.provider.setAttribute("aria-expanded", "true");
  focusPopupFilter(els.providerPopup);
}

function toggleProviderPopup() {
  var els = cfgEls();
  if (!els.providerPopup || !els.provider) { return; }
  if (els.providerPopup.style.display !== "none" && els.providerPopup.innerHTML) {
    closeProviderPopup();
    return;
  }
  openProviderPopup();
}

function selectProviderFromPopup(providerName) {
  var els = cfgEls();
  if (!els.provider) { return; }
  providerName = (providerName || "").trim();
  if (!providerName) { return; }
  els.provider.value = providerName;
  closeProviderPopup();
  previewProviderChange(providerName);
  applyRuntimeConfig();
  els.provider.focus();
}

// provider 本地预览：切 provider 后立即刷新 model 列表与 reasoning 选项，
// 不等待后端 /model 生效。model 输入自动跟随为新 provider 默认模型（若旧
// 值不在新列表中），reasoning 按新模型能力重渲染。
function previewProviderChange(providerName) {
  if (!runtimeMetaCache) { return; }
  var els = cfgEls();
  var p = findRuntimeProvider(runtimeMetaCache, providerName);
  var models = renderModelDatalist(els, p);
  var curModel = (els.model.value || "").trim();
  var nextModel = curModel;
  if (!p || models.indexOf(curModel) < 0) {
    nextModel = (p && p.default_model) || (models[0] || "");
    els.model.value = nextModel;
    els.model.placeholder = nextModel || "输入模型名";
  } else {
    els.model.placeholder = curModel || (p && p.default_model) || "输入模型名";
  }
  var r = resolveReasoningOptions(runtimeMetaCache, providerName, nextModel);
  var curReasoning = els.reasoning.value || "";
  // 新模型不支持旧 effort 时回到默认（空），避免提交无效组合。
  if (r.options.length > 0 && curReasoning && r.options.indexOf(curReasoning) < 0) { curReasoning = ""; }
  renderReasoningSelect(els, r.options, curReasoning, r.def);
}

// model 本地预览：输入模型名变化时即时刷新 reasoning 选项。
function previewModelChange(modelName) {
  if (!runtimeMetaCache) { return; }
  var els = cfgEls();
  var providerName = els.provider.value || cfg.provider;
  var r = resolveReasoningOptions(runtimeMetaCache, providerName, (modelName || "").trim());
  var curReasoning = els.reasoning.value || "";
  if (r.options.length > 0 && curReasoning && r.options.indexOf(curReasoning) < 0) { curReasoning = ""; }
  renderReasoningSelect(els, r.options, curReasoning, r.def);
}

// 拉取并同步权威配置到选择器。非 dirty 时全量同步值+选项；
// dirty（切换提交等待生效）时仍刷新选项列表（model datalist /
// reasoning 下拉来自最新缓存），但不覆盖用户正在确认的值，避免
// “切 provider 后列表不变、reasoning 写死 low/medium/high/max”。
export function loadRuntimeMeta() {
  // 带超时（util.js::apiFetch）：连接池被常驻事件流占满时，这个请求会永久排队，
  // 运行时配置面板就一直是空的且没有任何提示；超时后计入降级状态（§4.7），
  // sse.js 显示横幅并转入轮询重试。
  apiFetch("/web/api/runtime", { cache: "no-store" })
    .then(function (res) { return res.ok ? res.json() : null; })
    .then(function (meta) {
      if (!meta) { return; }
      runtimeMetaCache = meta;
      var els = cfgEls();
      if (!els.provider || !els.model || !els.reasoning) { return; }
      var cur = meta.current || {};
      var providers = meta.providers || [];
      if (!cfgUiDirty) {
        cfg.provider = cur.provider || "";
        cfg.model = cur.model || "";
        cfg.reasoning = cur.reasoning_effort || "";
        renderProviderCombo(els, providers, cfg.provider);
        var curProvider = findRuntimeProvider(meta, cfg.provider);
        renderModelDatalist(els, curProvider);
        els.model.value = cfg.model;
        els.model.placeholder = cfg.model || (curProvider && curProvider.default_model) || "输入模型名";
        // 当前模型的 reasoning 动态选项：优先 current.reasoning_options，
        // 否则按 model_details 解析，最后回退通用列表。
        var rOpts = (cur.reasoning_options && cur.reasoning_options.length > 0)
          ? cur.reasoning_options.slice() : null;
        if (!rOpts) {
          var rr = resolveReasoningOptions(meta, cfg.provider, cfg.model);
          rOpts = rr.options;
        }
        var rDef = cur.reasoning_default || resolveReasoningOptions(meta, cfg.provider, cfg.model).def;
        renderReasoningSelect(els, rOpts || [], cfg.reasoning, rDef);
      } else {
        // dirty：刷新选项列表但不动用户值。provider 下拉重建后回填选中项，
        // model datalist 与 reasoning 下拉按当前选中即时刷新。
        var selProvider = els.provider.value || cfg.provider;
        var selModelVal = (els.model.value || "").trim();
        var keepReasoning = els.reasoning.value || "";
        renderProviderCombo(els, providers, selProvider);
        els.provider.value = selProvider;
        var selP = findRuntimeProvider(meta, selProvider);
        renderModelDatalist(els, selP);
        els.model.value = selModelVal;
        var selModel = selModelVal || cfg.model;
        var sr = resolveReasoningOptions(meta, selProvider, selModel);
        // 保留用户选择，等待后端确认后再纠正（不静默清空）。
        renderReasoningSelect(els, sr.options, keepReasoning, sr.def);
        els.provider.value = selProvider;
        els.model.value = selModelVal;
        els.reasoning.value = keepReasoning;
      }
      var currentText = (cfg.provider || "?") + " · " + (cfg.model || "?") + (cfg.reasoning ? " · " + cfg.reasoning : "");
      // 这份配置文案只出现在窄屏的 ⚙ 触发按钮上（选择框收进弹出面板）：桌面由三个
      // 选择框直接呈现；面板首行原有的 #composer-summary 摘要已随「两行合一」删除
      // （用户要求取消「输入 provider/model/reasoning」文案）。
      if (els.toggleValue) { els.toggleValue.textContent = currentText; }
    })
    .catch(function (err) { console.error("runtime meta fetch failed:", err); });
}

// 选择器变更 → 构造差异化的 /model 命令并注入。
// provider 单独切换时不强制携带旧 model（后端按新 provider 默认模型解析）；
// previewProviderChange 已在本地把 model 输入跟随为新默认值，此时 model
// 差异视为显式切换，轮询需同时校验 model。
function applyRuntimeConfig() {
  var els = cfgEls();
  if (!els.provider || !els.model || !els.reasoning) { return; }
  var provider = els.provider.value || "";
  var model = (els.model.value || "").trim();
  var reasoning = els.reasoning.value || "";
  if (provider === cfg.provider && (model === cfg.model || model === "") && reasoning === cfg.reasoning) {
    return; // 无实际变化
  }
  var providerChanged = !!(provider && provider !== cfg.provider);
  // provider 切换但 model 输入仍是旧值：视为“跟随默认”，不发送 --model，
  // 让后端按新 provider 的 default_model 生效（否则会把旧模型强制带到新 provider）。
  var modelIsStaleAfterProviderSwitch = providerChanged && model === cfg.model;
  var modelChanged = !!(model && model !== cfg.model && !modelIsStaleAfterProviderSwitch);
  var reasoningChanged = (reasoning !== cfg.reasoning);
  var parts = ["/model"];
  if (providerChanged) { parts.push("--provider=" + provider); }
  if (modelChanged) { parts.push("--model=" + model); }
  if (reasoning === "" && cfg.reasoning !== "") {
    parts.push("--clear-reasoning"); // 恢复默认 effort
  } else if (reasoning !== "" && reasoningChanged) {
    parts.push("-r=" + reasoning);
  }
  if (parts.length <= 1) {
    // 仅 provider 变化且 model/reasoning 均跟随：仍需发送 provider 切换。
    if (!providerChanged) { return; }
  }
  // --direct：声明本命令来自 web 注入，TUI 侧跳过全部交互式选择
  // （provider/model/reasoning picker）直接落盘。不带它时，切 model 且未显式
  // 给 reasoning 会在 TUI 弹全屏 reasoning 选择器——TUI 卡住等键盘、web 轮询超时。
  var cmd = parts.join(" ") + " --direct";
  // 轮询期望：只校验本次实际发送的维度。provider 单切时不校验 model
  //（后端会落到新 provider 默认模型，旧值必然不匹配）。
  var expect = {
    provider: providerChanged ? provider : cfg.provider,
    checkProvider: providerChanged || !!provider,
    model: modelChanged ? model : "",
    checkModel: modelChanged,
    reasoning: reasoningChanged ? reasoning : cfg.reasoning,
    checkReasoning: reasoningChanged
  };
  cfgUiDirty = true;
  if (els.status) {
    els.status.textContent = "切换中…";
    els.status.className = "cfg-status busy";
  }
  // 超时放宽到 30s：POST 一旦被服务端接收就可能已经排队，过早中断会让界面显示
  // 「提交失败」而实际已入队（与 sessions.js::sendInput 同一口径）。
  apiFetch("/web/api/input", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ prompt: cmd })
  }, 30000)
    .then(function (res) { return res.json().catch(function () { return { status: "error", reason: "bad response" }; }); })
    .then(function (json) {
      if (json.status !== "queued") {
        if (els.status) {
          els.status.textContent = "提交失败: " + (json.reason || json.status);
          els.status.className = "cfg-status err";
        }
        cfgUiDirty = false;
        loadRuntimeMeta();
        return;
      }
      // 轮询权威值直到生效（命令在输入队列中稍后执行）
      pollRuntimeMeta(expect, 8);
    })
    .catch(function (err) {
      if (els.status) {
        els.status.textContent = "提交失败: " + err;
        els.status.className = "cfg-status err";
      }
      cfgUiDirty = false;
      loadRuntimeMeta();
    });
}

// expect: { provider, checkProvider, model, checkModel, reasoning, checkReasoning }
// 仅校验本次实际变更的维度：provider 单切不校验 model（后端落默认模型），
// 避免旧模型值导致轮询永不命中而报“可能未同步”。
function pollRuntimeMeta(expect, attempts) {
  var els = cfgEls();
  if (attempts <= 0) {
    cfgUiDirty = false;
    loadRuntimeMeta();
    if (els.status) {
      els.status.textContent = "已提交（配置可能未同步）";
      els.status.className = "cfg-status";
    }
    return;
  }
  apiFetch("/web/api/runtime", { cache: "no-store" })
    .then(function (res) { return res.ok ? res.json() : null; })
    .then(function (meta) {
      if (meta) { runtimeMetaCache = meta; }
      var cur = (meta && meta.current) || {};
      var okProvider = !expect.checkProvider || cur.provider === expect.provider;
      var okModel = !expect.checkModel || cur.model === expect.model;
      var okReasoning = !expect.checkReasoning || (cur.reasoning_effort || "") === (expect.reasoning || "");
      var ok = okProvider && okModel && okReasoning;
      if (ok) {
        cfgUiDirty = false;
        loadRuntimeMeta();
        if (els.status) {
          els.status.textContent = "已生效";
          els.status.className = "cfg-status ok";
          setTimeout(function () { els.status.textContent = ""; els.status.className = "cfg-status"; }, 2500);
        }
      } else {
        setTimeout(function () { pollRuntimeMeta(expect, attempts - 1); }, 400);
      }
    })
    .catch(function () {
      setTimeout(function () { pollRuntimeMeta(expect, attempts - 1); }, 400);
    });
}

// Escape 关闭 model / provider popup(与 provider-editor 的协议 popup 各自独立监听)。
export function initGlobalModelPopupDismiss() {
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") { closeModelPopup(); closeProviderPopup(); }
  });
}

export function initRuntimeBar() {
  // provider / model / reasoning 切换选择器
  // provider/model 切换先做本地预览（即时刷新 model 列表与 reasoning 选项），
  // 再提交 /model 命令；model 输入时实时预览 reasoning，不等待后端生效。
  var cfgProviderEl = document.getElementById("cfg-provider");
  var cfgModelEl = document.getElementById("cfg-model");
  var cfgReasoningEl = document.getElementById("cfg-reasoning");
  var cfgBarEl = document.getElementById("cfg-bar");
  if (cfgProviderEl) {
    // 只读输入框：点击/Enter/Space/↓ 打开可搜索列表；provider 值只由列表项选择提交，
    // 不存在把半截检索词当 provider 送出去的路径。
    cfgProviderEl.addEventListener("click", function () {
      toggleProviderPopup();
    });
    cfgProviderEl.addEventListener("keydown", function (e) {
      if (e.key === "Enter" || e.key === " " || e.key === "ArrowDown") {
        e.preventDefault();
        openProviderPopup();
      } else if (e.key === "Escape") {
        closeProviderPopup();
      }
    });
  }
  if (cfgReasoningEl) {
    cfgReasoningEl.addEventListener("change", applyRuntimeConfig);
  }
  if (cfgModelEl) {
    cfgModelEl.addEventListener("input", function () {
      previewModelChange(cfgModelEl.value || "");
      // popup 打开时输入框同时充当检索词：同步过滤列表（显隐已有节点，不重渲染）。
      var els = cfgEls();
      if (els.modelPopup && els.modelPopup.style.display !== "none") {
        var filter = popupFilterInput(els.modelPopup);
        if (filter) {
          filter.value = cfgModelEl.value || "";
          applyPopupFilter(els.modelPopup);
        }
      }
    });
    cfgModelEl.addEventListener("change", function () {
      closeModelPopup();
      applyRuntimeConfig();
    }); // 失焦/选择 datalist 项时提交
    cfgModelEl.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        e.preventDefault();
        previewModelChange(cfgModelEl.value || "");
        closeModelPopup();
        applyRuntimeConfig();
        cfgModelEl.blur();
      } else if (e.key === "Escape") {
        closeModelPopup();
      } else if (e.key === "ArrowDown" && e.altKey) {
        // Alt+↓ 与原生 select 对齐：弹出模型列表。
        e.preventDefault();
        toggleModelPopup();
      }
    });
  }
  var cfgModelToggleEl = document.getElementById("cfg-model-toggle");
  if (cfgModelToggleEl) {
    cfgModelToggleEl.addEventListener("click", function (e) {
      e.preventDefault();
      e.stopPropagation();
      toggleModelPopup();
    });
  }
  var cfgModelPopupEl = document.getElementById("cfg-model-popup");
  if (cfgModelPopupEl) {
    // 事件委托：popup 内容每次重渲染，无需重复绑定。
    cfgModelPopupEl.addEventListener("click", function (e) {
      var btn = e.target && e.target.closest ? e.target.closest("[data-model]") : null;
      if (!btn) { return; }
      e.preventDefault();
      e.stopPropagation();
      selectModelFromPopup(btn.getAttribute("data-model") || "");
    });
    // 检索框输入：只切换列表项显隐（保持焦点 / 不重渲染）。
    cfgModelPopupEl.addEventListener("input", function () {
      applyPopupFilter(cfgModelPopupEl);
    });
    cfgModelPopupEl.addEventListener("keydown", function (e) {
      handlePopupKeydown(e, "data-model", selectModelFromPopup, function () {
        closeModelPopup();
        if (cfgModelEl) { cfgModelEl.focus(); }
      });
    });
  }
  // 点击外部关闭 popup（与审批弹窗/快捷键帮助一致的轻量模式）。
  document.addEventListener("click", function (e) {
    var els = cfgEls();
    [[els.providerPopup, els.provider, els.providerToggle, closeProviderPopup],
      [els.modelPopup, els.model, els.modelToggle, closeModelPopup]].forEach(function (row) {
      var popup = row[0];
      if (!popup || popup.style.display === "none") { return; }
      var wrap = row[1] && row[1].parentNode ? row[1].parentNode : null;
      if (wrap && wrap.contains(e.target)) { return; }
      var toggle = row[2];
      if (toggle && (e.target === toggle || (toggle.contains && toggle.contains(e.target)))) { return; }
      row[3]();
    });
  });
  var cfgProviderToggleEl = document.getElementById("cfg-provider-toggle");
  if (cfgProviderToggleEl) {
    cfgProviderToggleEl.addEventListener("click", function (e) {
      e.preventDefault();
      e.stopPropagation();
      toggleProviderPopup();
    });
  }
  var cfgProviderPopupEl = document.getElementById("cfg-provider-popup");
  if (cfgProviderPopupEl) {
    // 事件委托：popup 内容每次重渲染，无需重复绑定。
    cfgProviderPopupEl.addEventListener("click", function (e) {
      var btn = e.target && e.target.closest ? e.target.closest("[data-provider]") : null;
      if (!btn) { return; }
      e.preventDefault();
      e.stopPropagation();
      selectProviderFromPopup(btn.getAttribute("data-provider") || "");
    });
    cfgProviderPopupEl.addEventListener("input", function () {
      applyPopupFilter(cfgProviderPopupEl);
    });
    cfgProviderPopupEl.addEventListener("keydown", function (e) {
      handlePopupKeydown(e, "data-provider", selectProviderFromPopup, function () {
        closeProviderPopup();
        if (cfgProviderEl) { cfgProviderEl.focus(); }
      });
    });
  }
  // 窄屏折叠面板：触发按钮切换 .cfg-open（桌面该按钮 display:none，这段逻辑空转）。
  var cfgToggleEl = document.getElementById("cfg-toggle");
  function closeCfgPanel() {
    if (!cfgBarEl) { return; }
    cfgBarEl.classList.remove("cfg-open");
    if (cfgToggleEl) { cfgToggleEl.setAttribute("aria-expanded", "false"); }
  }
  if (cfgToggleEl && cfgBarEl) {
    cfgToggleEl.addEventListener("click", function (e) {
      e.preventDefault();
      var open = cfgBarEl.classList.toggle("cfg-open");
      cfgToggleEl.setAttribute("aria-expanded", open ? "true" : "false");
      if (!open) { closeModelPopup(); closeProviderPopup(); }
    });
    // 点面板外部或按 Esc 收起（与 model popup 一致的轻量模式）。
    document.addEventListener("click", function (e) {
      if (!cfgBarEl.classList.contains("cfg-open")) { return; }
      if (cfgBarEl.contains(e.target)) { return; }
      closeCfgPanel();
    });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && cfgBarEl.classList.contains("cfg-open")) { closeCfgPanel(); }
    });
  }
  initGlobalModelPopupDismiss();
}
