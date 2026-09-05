// 底部 cfg-bar:provider/model/reasoning 切换器、model 自定义 popup、权威配置同步与轮询。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { esc } from "./util.js";

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
    model: document.getElementById("cfg-model"),
    modelOpts: document.getElementById("cfg-model-options"),
    modelToggle: document.getElementById("cfg-model-toggle"),
    modelPopup: document.getElementById("cfg-model-popup"),
    modelCount: document.getElementById("cfg-model-count"),
    reasoning: document.getElementById("cfg-reasoning"),
    current: document.getElementById("cfg-current"),
    status: document.getElementById("cfg-status")
  };
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
    els.model.title = "切换模型（/model --model）：可直接输入自定义模型名，或点 ▼ 弹出列表选择" +
      (models.length > 0 ? "（共 " + models.length + " 个： " + models.slice(0, 8).join(", ") + (models.length > 8 ? "…" : "") + "）" : "");
  }
  renderModelPopup(els, provider, models);
  return models;
}

// 自定义弹出列表：解决 <input list=datalist> 无下拉箭头、点击不弹出、
// 各浏览器表现不一致的问题。popup 与 datalist 同数据源，点选即切换。
function renderModelPopup(els, provider, models) {
  var popup = els.modelPopup;
  if (!popup) { return; }
  models = models || (provider ? (provider.models || []) : []);
  var curVal = (els.model && els.model.value) || "";
  if (!models || models.length === 0) {
    popup.innerHTML = '<div class="cfg-model-empty">暂无预置模型，可直接输入自定义模型名</div>';
    return;
  }
  var def = provider ? (provider.default_model || "") : "";
  var html = "";
  models.forEach(function (m) {
    var cls = "cfg-model-item" + (m === curVal ? " current" : "");
    var tag = m === def ? '<span class="tag">默认</span>' : (m === curVal ? '<span class="tag">当前</span>' : "");
    html += '<button type="button" class="' + cls + '" data-model="' + esc(m) + '" title="' + esc(m) + '">' +
      esc(m) + tag + "</button>";
  });
  popup.innerHTML = html;
}

function closeModelPopup() {
  var els = cfgEls();
  if (els.modelPopup) { els.modelPopup.style.display = "none"; }
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
  }
  els.modelPopup.style.display = "block";
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
}

function renderProviderSelect(els, providers, selected) {
  var html = "";
  var hasCurrent = false;
  providers.forEach(function (p) {
    if (p.name === selected) { hasCurrent = true; }
    html += '<option value="' + esc(p.name) + '"' + (p.name === selected ? " selected" : "") + ">" + esc(p.name) + "</option>";
  });
  if (selected && !hasCurrent) {
    html = '<option value="' + esc(selected) + '" selected>' + esc(selected) + "</option>" + html;
  }
  els.provider.innerHTML = html;
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
  fetch("/web/api/runtime", { cache: "no-store" })
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
        renderProviderSelect(els, providers, cfg.provider);
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
        renderProviderSelect(els, providers, selProvider);
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
      els.current.textContent = (cfg.provider || "?") + " · " + (cfg.model || "?") + (cfg.reasoning ? " · " + cfg.reasoning : "");
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
  var cmd = parts.join(" ");
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
  fetch("/web/api/input", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ prompt: cmd })
  })
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
  fetch("/web/api/runtime", { cache: "no-store" })
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

// Escape 关闭 model popup(与 provider-editor 的协议 popup 各自独立监听)。
export function initGlobalModelPopupDismiss() {
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") { closeModelPopup(); }
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
    cfgProviderEl.addEventListener("change", function () {
      previewProviderChange(cfgProviderEl.value || "");
      applyRuntimeConfig();
    });
  }
  if (cfgReasoningEl) {
    cfgReasoningEl.addEventListener("change", applyRuntimeConfig);
  }
  if (cfgModelEl) {
    cfgModelEl.addEventListener("input", function () {
      previewModelChange(cfgModelEl.value || "");
      // 输入时同步 popup 高亮（popup 打开时）。
      var els = cfgEls();
      if (els.modelPopup && els.modelPopup.style.display !== "none" && runtimeMetaCache) {
        var selProvider = cfgProviderEl ? (cfgProviderEl.value || cfg.provider) : cfg.provider;
        renderModelPopup(els, findRuntimeProvider(runtimeMetaCache, selProvider));
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
    // 聚焦时尝试原生下拉（部分浏览器双击才出 datalist，单击无反馈）。
    cfgModelEl.addEventListener("focus", function () {
      var els = cfgEls();
      if (els.modelPopup && els.modelPopup.style.display !== "none" && runtimeMetaCache) {
        var selProvider = cfgProviderEl ? (cfgProviderEl.value || cfg.provider) : cfg.provider;
        renderModelPopup(els, findRuntimeProvider(runtimeMetaCache, selProvider));
      }
    });
  }
  var cfgModelToggleEl = document.getElementById("cfg-model-toggle");
  if (cfgModelToggleEl) {
    cfgModelToggleEl.addEventListener("click", function (e) {
      e.preventDefault();
      e.stopPropagation();
      toggleModelPopup();
      if (cfgModelEl) { cfgModelEl.focus(); }
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
  }
  // 点击外部关闭 popup（与审批弹窗/快捷键帮助一致的轻量模式）。
  document.addEventListener("click", function (e) {
    var els = cfgEls();
    if (!els.modelPopup || els.modelPopup.style.display === "none") { return; }
    var wrap = els.model && els.model.parentNode ? els.model.parentNode : null;
    if (wrap && wrap.contains(e.target)) { return; }
    if (els.modelToggle && (e.target === els.modelToggle || (els.modelToggle.contains && els.modelToggle.contains(e.target)))) { return; }
    closeModelPopup();
  });
  initGlobalModelPopupDismiss();
}
