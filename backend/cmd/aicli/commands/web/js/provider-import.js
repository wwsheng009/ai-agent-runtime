// Provider 自动导入弹窗(同 aicli login 的自动生成逻辑)。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { loadConfigAdmin, setCfgStatus } from "./config-admin.js";
import { configEl, showToast } from "./util.js";

// ---- Provider 自动导入（同 aicli login 的自动生成逻辑）----
// 由名称 + Base URL + API Key 一键生成完整配置：后端负责协议探测、
// 模型列表拉取/校验、api_path / forward_url / default_model /
// supported_models / model_capabilities 生成，并把 API Key 写入
// Key Store（config.yaml 只保存 api_key_ref）。
function showProviderImport(show) {
  var overlay = configEl("config-import-overlay");
  if (!overlay) { return; }
  if (show) { overlay.classList.add("active"); }
  else { overlay.classList.remove("active"); }
}

function openProviderImport() {
  var fields = ["cfg-import-name", "cfg-import-base-url", "cfg-import-api-key",
    "cfg-import-default-model", "cfg-import-models-path"];
  fields.forEach(function (id) {
    var el = configEl(id);
    if (el) { el.value = ""; }
  });
  var proto = configEl("cfg-import-protocol");
  if (proto) { proto.value = "auto"; }
  var sdEl = configEl("cfg-import-set-default");
  if (sdEl) { sdEl.checked = false; }
  var result = configEl("cfg-import-result");
  if (result) { result.style.display = "none"; result.textContent = ""; }
  setCfgStatus(configEl("cfg-import-status"), "", "");
  var submitBtn = configEl("cfg-import-submit-btn");
  if (submitBtn) { submitBtn.disabled = false; submitBtn.textContent = "开始导入"; }
  showProviderImport(true);
  var nameEl = configEl("cfg-import-name");
  if (nameEl) { nameEl.focus(); }
}

function closeProviderImport() {
  showProviderImport(false);
  setCfgStatus(configEl("cfg-import-status"), "", "");
}

// 成功响应即后端 providerLoginResult（同 aicli login 的 JSON 输出）。
// 全部用 createElement/textContent 渲染，避免 innerHTML 注入。
function renderProviderImportResult(result) {
  var box = configEl("cfg-import-result");
  if (!box) { return; }
  box.textContent = "";
  box.style.display = "";
  var heading = document.createElement("h4");
  heading.textContent = "导入成功：" + (result.provider || "");
  box.appendChild(heading);
  var rows = [];
  if (result.login_protocol) { rows.push(["登录协议", result.login_protocol]); }
  if (result.protocol) { rows.push(["协议", result.protocol]); }
  if (result.base_url) { rows.push(["Base URL", result.base_url]); }
  if (result.models_endpoint) { rows.push(["模型列表端点", result.models_endpoint]); }
  if (result.default_model) { rows.push(["默认模型", result.default_model]); }
  if (result.supported_models && result.supported_models.length) {
    rows.push(["支持模型", result.supported_models.length + " 个"]);
  }
  if (result.created || result.updated) {
    rows.push(["状态", result.created ? "新建" : "更新已有配置"]);
  }
  if (result.set_default) { rows.push(["默认 Provider", "已设为全局默认"]); }
  if (result.api_key_masked) { rows.push(["API Key", result.api_key_masked]); }
  if (result.provider_configs && result.provider_configs.length) {
    result.provider_configs.forEach(function (info) {
      rows.push(["生成配置" + (info.provider_template ? "（" + info.provider_template + "）" : ""), info.provider]);
    });
  }
  if (rows.length) {
    var table = document.createElement("table");
    rows.forEach(function (pair) {
      var tr = document.createElement("tr");
      var th = document.createElement("th");
      th.textContent = pair[0];
      var td = document.createElement("td");
      td.textContent = pair[1];
      tr.appendChild(th);
      tr.appendChild(td);
      table.appendChild(tr);
    });
    box.appendChild(table);
  }
  var warns = (result.model_card_warnings || []).concat(result.site_account_warnings || []);
  if (warns.length) {
    var w = document.createElement("div");
    w.className = "cfg-import-warn";
    w.textContent = warns.join("；");
    box.appendChild(w);
  }
}

function submitProviderImport(ev) {
  ev.preventDefault();
  var statusEl = configEl("cfg-import-status");
  var submitBtn = configEl("cfg-import-submit-btn");
  var name = (configEl("cfg-import-name").value || "").trim();
  var baseURL = (configEl("cfg-import-base-url").value || "").trim();
  var apiKey = (configEl("cfg-import-api-key").value || "").trim();
  if (!name) { setCfgStatus(statusEl, "名称不能为空", "err"); return; }
  if (!baseURL) { setCfgStatus(statusEl, "Base URL 不能为空", "err"); return; }
  if (!apiKey) { setCfgStatus(statusEl, "API Key 不能为空", "err"); return; }
  var payload = {
    name: name,
    base_url: baseURL,
    api_key: apiKey,
    protocol: (configEl("cfg-import-protocol").value || "").trim(),
    default_model: (configEl("cfg-import-default-model").value || "").trim(),
    models_path: (configEl("cfg-import-models-path").value || "").trim()
  };
  var sdEl = configEl("cfg-import-set-default");
  if (sdEl && sdEl.checked) { payload.set_default = true; }
  setCfgStatus(statusEl, "导入中（探测协议、拉取模型列表）…", "busy");
  if (submitBtn) { submitBtn.disabled = true; submitBtn.textContent = "导入中…"; }
  fetch("/web/api/config/providers/auto-import", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload)
  })
    .then(function (res) { return res.json().catch(function () { return { status: "error", reason: "bad response" }; }); })
    .then(function (json) {
      if (!json || json.status === "error") {
        setCfgStatus(statusEl, "导入失败: " + (json && json.reason ? json.reason : "未知错误"), "err");
        return;
      }
      if (!json.provider && !(json.provider_configs && json.provider_configs.length)) {
        setCfgStatus(statusEl, "导入失败: 响应异常", "err");
        return;
      }
      setCfgStatus(statusEl, "", "");
      renderProviderImportResult(json);
      showToast("已自动导入 provider: " + name);
      loadConfigAdmin();
    })
    .catch(function (err) { setCfgStatus(statusEl, "导入失败: " + err, "err"); })
    .then(function () {
      if (submitBtn) { submitBtn.disabled = false; submitBtn.textContent = "开始导入"; }
    });
}

export function initProviderImport() {
    // ---- Provider 自动导入弹窗 ----
    var importBtn = configEl("config-auto-import-btn");
    if (importBtn) { importBtn.addEventListener("click", openProviderImport); }
    var importCancel = configEl("cfg-import-cancel-btn");
    if (importCancel) { importCancel.addEventListener("click", closeProviderImport); }
    var importClose = configEl("config-import-close-btn");
    if (importClose) { importClose.addEventListener("click", closeProviderImport); }
    var importOverlay = configEl("config-import-overlay");
    if (importOverlay) {
      importOverlay.addEventListener("click", function (e) {
        if (e.target === importOverlay) { closeProviderImport(); }
      });
      // 与编辑弹窗一致：挂到 <body> 下，保证固定定位与 z-index 全局一致。
      if (importOverlay.parentNode && importOverlay.parentNode !== document.body) {
        document.body.appendChild(importOverlay);
      }
    }
    var importForm = configEl("config-import-form");
    if (importForm) { importForm.addEventListener("submit", submitProviderImport); }
}
