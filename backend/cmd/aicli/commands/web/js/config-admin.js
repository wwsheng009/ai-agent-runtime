// 配置页:provider 表格(搜索/排序/分页)、启用/删除、默认偏好保存。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { openProviderEditor } from "./provider-editor.js";
import { loadRuntimeMeta } from "./runtime.js";
import { configEl, esc, showToast } from "./util.js";

// ================= 配置页签：provider / model / reasoning effort CRUD =================
// 数据来自 GET /web/api/config（文件快照），写操作走 /web/api/config/* 端点，
// 成功后刷新列表并同步顶部选择器（/web/api/runtime 权威值）。
export var configData = null;
// provider 列表视图状态：搜索词 / 排序键 / 分页（page 1-based，pageSize 0=全部）
var cfgProviderQuery = "";
var cfgProviderSort = "name";
var cfgProviderPage = 1;
var cfgProviderPageSize = 25;

export function loadConfigAdmin() {
  fetch("/web/api/config", { cache: "no-store" })
    .then(function (res) { return res.json(); })
    .then(function (snap) {
      configData = snap || { providers: [], chat: {}, config_path: "" };
      renderConfigAdmin();
      loadRuntimeMeta(); // 权威切换值可能有调整（如默认 provider 变化）
    })
    .catch(function (err) {
      var list = configEl("config-provider-list");
      if (list) { list.innerHTML = '<div class="config-empty">加载失败: ' + esc(String(err)) + "</div>"; }
    });
}

export function providerByName(name) {
  var providers = (configData && configData.providers) || [];
  for (var i = 0; i < providers.length; i++) {
    if (providers[i].name === name) { return providers[i]; }
  }
  return null;
}

export function setCfgStatus(el, text, kind) {
  if (!el) { return; }
  el.textContent = text;
  el.className = "cfg-status" + (kind ? " " + kind : "");
}

function renderConfigAdmin() {
  var pathEl = configEl("config-path");
  if (pathEl) { pathEl.textContent = configData.config_path || "（未识别到配置文件）"; }

  var providers = configData.providers || [];

  // 默认偏好区（aicli.chat）
  var chatProv = configEl("config-chat-provider");
  if (chatProv) {
    var html = '<option value="">(未设置)</option>';
    providers.forEach(function (p) {
      html += '<option value="' + esc(p.name) + '">' + esc(p.name) + "</option>";
    });
    chatProv.innerHTML = html;
    chatProv.value = (configData.chat && configData.chat.default_provider) || "";
  }
  var chatModel = configEl("config-chat-model");
  if (chatModel) {
    chatModel.value = (configData.chat && configData.chat.default_model) || "";
    var modelOpts = configEl("config-chat-model-options");
    if (modelOpts) {
      var mhtml = "";
      providers.forEach(function (p) {
        (p.models || []).forEach(function (m) {
          mhtml += '<option value="' + esc(m.name) + '"></option>';
        });
      });
      modelOpts.innerHTML = mhtml;
    }
  }
  var chatReasoning = configEl("config-chat-reasoning");
  if (chatReasoning) { chatReasoning.value = (configData.chat && configData.chat.reasoning_effort) || ""; }

  // provider 列表（工具栏 + 分页/排序/搜索渲染）
  var list = configEl("config-provider-list");
  if (!list) { return; }
  var toolbar = configEl("config-provider-toolbar");
  if (providers.length === 0) {
    if (toolbar) { toolbar.hidden = true; }
    list.innerHTML = '<div class="config-empty">暂无 provider，点击右上角「＋ 新增 Provider」创建。</div>';
    return;
  }
  if (toolbar) { toolbar.hidden = false; }
  renderProviderTable();
}

// filterAndSortProviders 按搜索词过滤、按排序键排序（纯函数，不改动
// configData.providers 原数组）。
function filterAndSortProviders() {
  var all = configData.providers || [];
  var q = cfgProviderQuery.toLowerCase().trim();
  var filtered;
  if (q) {
    filtered = [];
    all.forEach(function (p) {
      var haystack = [
        p.name, p.protocol, p.base_url, p.api_key_ref,
        p.default_model, p.api_key_source
      ].join(" ").toLowerCase();
      (p.models || []).forEach(function (m) { haystack += " " + String(m.name).toLowerCase(); });
      (p.supported_models || []).forEach(function (m) { haystack += " " + String(m).toLowerCase(); });
      if (haystack.indexOf(q) !== -1) { filtered.push(p); }
    });
  } else {
    filtered = all.slice();
  }
  filtered.sort(function (a, b) {
    switch (cfgProviderSort) {
      case "name-desc":
        return b.name.localeCompare(a.name, undefined, { numeric: true, sensitivity: "base" });
      case "enabled":
        if (a.enabled !== b.enabled) { return a.enabled ? -1 : 1; }
        return a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: "base" });
      case "models": {
        var ma = (a.models || []).length, mb = (b.models || []).length;
        if (ma !== mb) { return mb - ma; }
        return a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: "base" });
      }
      case "default-model":
        return String(a.default_model || "").localeCompare(String(b.default_model || ""), undefined, { numeric: true, sensitivity: "base" });
      default:
        return a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: "base" });
    }
  });
  return filtered;
}

// renderProviderTable 渲染 provider 表格：应用搜索/排序/分页后输出到
// config-provider-list，并同步更新工具栏计数与分页控件。
function renderProviderTable() {
  var list = configEl("config-provider-list");
  if (!list) { return; }
  var providers = filterAndSortProviders();
  var maxPage = 1;
  if (cfgProviderPageSize > 0) {
    maxPage = Math.max(1, Math.ceil(providers.length / cfgProviderPageSize));
  }
  if (cfgProviderPage > maxPage) { cfgProviderPage = maxPage; }
  if (cfgProviderPage < 1) { cfgProviderPage = 1; }

  var pageRows = providers;
  var from = 0, to = providers.length;
  if (cfgProviderPageSize > 0) {
    from = (cfgProviderPage - 1) * cfgProviderPageSize;
    to = Math.min(from + cfgProviderPageSize, providers.length);
    pageRows = providers.slice(from, to);
    from = from + 1; // 显示用 1-based 区间
  }

  if (pageRows.length === 0) {
    list.innerHTML = '<div class="config-empty">没有匹配的 provider' +
      (cfgProviderQuery ? "（搜索词: “" + esc(cfgProviderQuery) + "”）" : "") +
      "，可清空搜索或调整筛选。</div>";
    var emptyCount = configEl("config-provider-count");
    if (emptyCount) { emptyCount.textContent = "共 0 个"; }
    var emptyPage = configEl("config-provider-page");
    if (emptyPage) { emptyPage.textContent = cfgProviderPageSize > 0 ? "0 / 0" : "全部"; }
    var emptyPrev = configEl("config-provider-prev");
    if (emptyPrev) { emptyPrev.disabled = true; }
    var emptyNext = configEl("config-provider-next");
    if (emptyNext) { emptyNext.disabled = true; }
    return;
  }

  var rows = '<table class="config-table"><thead><tr>' +
    "<th>名称</th><th>协议</th><th>状态</th><th>默认模型</th><th>模型数</th><th>Reasoning 模型</th><th>操作</th>" +
    "</tr></thead><tbody>";
  pageRows.forEach(function (p) {
    var models = p.models || [];
    var reasoningCount = 0;
    models.forEach(function (m) { if (m.reasoning_model) { reasoningCount++; } });
    var isDefault = p.name === configData.default_provider;
    rows += "<tr>" +
      "<td>" + esc(p.name) + (isDefault ? ' <span class="config-badge default">默认</span>' : "") + "</td>" +
      "<td>" + esc(p.protocol || "-") + "</td>" +
      "<td>" + (p.enabled ? '<span class="config-badge ok">启用</span>' : '<span class="config-badge off">禁用</span>') + "</td>" +
      "<td>" + esc(p.default_model || "-") + "</td>" +
      "<td>" + models.length + "</td>" +
      "<td>" + (reasoningCount ? reasoningCount + " 个" : "-") + "</td>" +
      '<td class="config-actions">' +
      '<button type="button" data-action="edit" data-name="' + esc(p.name) + '">编辑</button>' +
      '<button type="button" data-action="toggle" data-name="' + esc(p.name) + '">' + (p.enabled ? "禁用" : "启用") + "</button>" +
      '<button type="button" data-action="delete" data-name="' + esc(p.name) + '" class="danger-btn">删除</button>' +
      "</td></tr>";
  });
  rows += "</tbody></table>";
  list.innerHTML = rows;

  var countEl = configEl("config-provider-count");
  if (countEl) {
    var total = providers.length;
    if (cfgProviderPageSize > 0) {
      countEl.textContent = "显示 " + from + "–" + to + " / 共 " + total + " 个" +
        (cfgProviderQuery ? "（搜索命中）" : "");
    } else {
      countEl.textContent = "共 " + total + " 个" + (cfgProviderQuery ? "（搜索命中）" : "");
    }
  }
  var pageEl = configEl("config-provider-page");
  if (pageEl) {
    pageEl.textContent = cfgProviderPageSize > 0 ? cfgProviderPage + " / " + maxPage : "全部";
  }
  var prevBtn = configEl("config-provider-prev");
  if (prevBtn) { prevBtn.disabled = cfgProviderPageSize <= 0 || cfgProviderPage <= 1; }
  var nextBtn = configEl("config-provider-next");
  if (nextBtn) { nextBtn.disabled = cfgProviderPageSize <= 0 || cfgProviderPage >= maxPage; }
}

// bindProviderToolbar 绑定 provider 列表工具栏事件：搜索（防抖）、排序、
// 每页条数、上一页/下一页。搜索与排序变化时回到第 1 页。
function bindProviderToolbar() {
  var searchEl = configEl("config-provider-search");
  if (searchEl) {
    var debounceTimer = null;
    searchEl.addEventListener("input", function () {
      var v = searchEl.value;
      if (debounceTimer) { clearTimeout(debounceTimer); }
      debounceTimer = setTimeout(function () {
        cfgProviderQuery = v;
        cfgProviderPage = 1;
        renderProviderTable();
      }, 150);
    });
  }
  var sortEl = configEl("config-provider-sort");
  if (sortEl) {
    sortEl.addEventListener("change", function () {
      cfgProviderSort = sortEl.value;
      cfgProviderPage = 1;
      renderProviderTable();
    });
  }
  var sizeEl = configEl("config-provider-page-size");
  if (sizeEl) {
    sizeEl.addEventListener("change", function () {
      cfgProviderPageSize = parseInt(sizeEl.value, 10) || 0;
      cfgProviderPage = 1;
      renderProviderTable();
    });
  }
  var prevBtn = configEl("config-provider-prev");
  if (prevBtn) {
    prevBtn.addEventListener("click", function () {
      if (cfgProviderPage > 1) { cfgProviderPage--; renderProviderTable(); }
    });
  }
  var nextBtn = configEl("config-provider-next");
  if (nextBtn) {
    nextBtn.addEventListener("click", function () {
      cfgProviderPage++;
      renderProviderTable(); // 越界时 renderProviderTable 内钳制
    });
  }
}

function bindConfigTableActions() {
  var list = configEl("config-provider-list");
  if (!list) { return; }
  list.onclick = function (ev) {
    var btn = ev.target;
    if (!btn || btn.tagName !== "BUTTON") { return; }
    var action = btn.getAttribute("data-action");
    var name = btn.getAttribute("data-name");
    if (!name) { return; }
    if (action === "edit") {
      openProviderEditor(name);
    } else if (action === "toggle") {
      var p = providerByName(name);
      setProvidersEnabled([name], !(p && p.enabled));
    } else if (action === "delete") {
      deleteProviders([name]);
    }
  };
}

function setProvidersEnabled(names, enabled) {
  fetch("/web/api/config/providers/enabled", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ names: names, enabled: enabled })
  })
    .then(function (res) { return res.json().catch(function () { return { status: "error", reason: "bad response" }; }); })
    .then(function (json) {
      if (json.status !== "ok") { showToast("操作失败: " + (json.reason || json.status), "err"); return; }
      showToast(enabled ? "已启用" : "已禁用");
      loadConfigAdmin();
    })
    .catch(function (err) { showToast("操作失败: " + err, "err"); });
}

function deleteProviders(names) {
  if (!window.confirm("确定删除 provider: " + names.join(", ") + "？\n将同时删除其全部配置字段。")) { return; }
  fetch("/web/api/config/providers/delete", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ names: names })
  })
    .then(function (res) { return res.json().catch(function () { return { status: "error", reason: "bad response" }; }); })
    .then(function (json) {
      if (json.status !== "ok") { showToast("删除失败: " + (json.reason || json.status), "err"); return; }
      showToast("已删除: " + names.join(", "));
      loadConfigAdmin();
    })
    .catch(function (err) { showToast("删除失败: " + err, "err"); });
}

function saveChatDefaults() {
  var statusEl = configEl("config-chat-status");
  var payload = {
    default_provider: configEl("config-chat-provider").value || "",
    default_model: (configEl("config-chat-model").value || "").trim(),
    reasoning_effort: configEl("config-chat-reasoning").value || ""
  };
  setCfgStatus(statusEl, "保存中…", "busy");
  fetch("/web/api/config/chat", {
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
      setCfgStatus(statusEl, "✓ 已保存", "ok");
      showToast("默认偏好已保存");
      loadConfigAdmin();
    })
    .catch(function (err) { setCfgStatus(statusEl, "保存失败: " + err, "err"); });
}

export function initConfigAdmin() {
    var refreshBtn = configEl("config-refresh-btn");
    if (refreshBtn) { refreshBtn.addEventListener("click", function () { loadConfigAdmin(); }); }
    var chatSave = configEl("config-chat-save-btn");
    if (chatSave) { chatSave.addEventListener("click", saveChatDefaults); }
    bindProviderToolbar();
    bindConfigTableActions();
}
