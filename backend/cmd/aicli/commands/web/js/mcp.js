// MCP 页签：MCP Server 管理（列表 / 新增 / 编辑 / 删除 / 启停 / 热重载）。
// 数据源 /web/api/mcps（config + status，与 /web/api/mcps/{name} 同源，见
// backend/cmd/aicli/commands/web_mcp_handlers.go）；写操作（POST/PUT/DELETE）由
// 页面内置 fetch 包装自动携带 X-AICLI-Token（web_auth.go 注入），此处直接 fetch。
// 服务端文本一律经 esc() 转义后再进 innerHTML。
// aicli micro web client 前端模块(无构建步骤,由 app.js 入口聚合)。

import { esc, showToast } from "./util.js";
import {
  annotateKvRows,
  appendKvRow,
  kvRowsHaveEmptyKey,
  mergeMcpEnv,
  readKvRows,
  renderKvRows,
  splitMcpEnv
} from "./mcp-kv.js";

// 请求序号：丢弃连续刷新 / 变更后迟到的过期响应。
var mcpsSeq = 0;
// 最近一次列表快照（按名称索引）：编辑时优先 GET 单个配置，失败回退该快照。
var mcpConfigs = {};
// 当前编辑对象的原名（空串 = 新增）。
var mcpEditingName = "";

function mcpEl(id) { return document.getElementById(id); }

// ---- HTTP ----

function mcpAPI(path, options) {
  return fetch(path, options).then(function (res) {
    return res.json().catch(function () { return null; }).then(function (body) {
      return { status: res.status, body: body };
    });
  });
}

function mcpErrorText(result, fallback) {
  var err = result && result.body && result.body.error;
  if (err && err.message) { return String(err.message); }
  if (err && err.code) { return String(err.code); }
  if (result && result.status) { return fallback + "（HTTP " + result.status + "）"; }
  return fallback;
}

// ---- 列表 ----

// 页签首次激活时懒加载（ui.js activateTab 调用）；进入页签即重拉，保证
// 列表反映最新配置（热重载 / 外部改 mcp.yaml 后无需刷新整页）。
export function loadMCPs() {
  refreshMCPs();
}

export function refreshMCPs() {
  var listEl = mcpEl("mcp-list");
  if (!listEl) { return; }
  var seq = ++mcpsSeq;
  listEl.innerHTML = '<div class="skills-empty">加载中…</div>';
  mcpAPI("/web/api/mcps").then(function (result) {
    if (seq !== mcpsSeq) { return; }
    if (result.status !== 200 || !result.body) {
      renderMCPListError(result);
      return;
    }
    renderMCPList(result.body);
  }).catch(function () {
    if (seq !== mcpsSeq) { return; }
    renderMCPListError(null);
  });
}

function renderMCPListError(result) {
  var msg = mcpErrorText(result, "加载失败");
  var countEl = mcpEl("mcp-count");
  var listEl = mcpEl("mcp-list");
  if (countEl) { countEl.textContent = "加载失败"; }
  if (listEl) {
    listEl.innerHTML = '<div class="skills-empty skills-error">加载失败: ' + esc(msg) + "</div>";
  }
}

function renderMCPList(body) {
  var listEl = mcpEl("mcp-list");
  var countEl = mcpEl("mcp-count");
  var items = (body && body.mcps) || [];
  var count = body && typeof body.count === "number" ? body.count : items.length;
  if (countEl) { countEl.textContent = "共 " + count + " 个"; }
  mcpConfigs = {};
  if (!listEl) { return; }
  if (!items.length) {
    listEl.innerHTML = '<div class="skills-empty">暂无 MCP 配置，点击「＋ 新增」添加</div>';
    return;
  }
  var rows = [];
  for (var i = 0; i < items.length; i++) {
    rows.push(renderMCPRow(items[i]));
  }
  listEl.innerHTML = rows.join("");
}

function renderMCPRow(item) {
  var cfg = (item && item.config) || {};
  var status = (item && item.status) || null;
  var name = cfg.name ? String(cfg.name) : "";
  if (name) { mcpConfigs[name] = cfg; }

  var type = cfg.type ? String(cfg.type) : "";
  var enabled = mcpConfigEnabled(cfg);
  var connected = !!(status && status.connected);
  var toolCount = status && typeof status.toolCount === "number" ? status.toolCount : 0;
  var trust = cfg.trustLevel || (status && status.trustLevel) || "";
  var endpoint = mcpEndpoint(cfg);

  var statusText = connected ? "已连接" : (enabled ? "未连接" : "已停用");
  var badges = [
    '<span class="skill-badge">' + esc(type || "-") + "</span>",
    '<span class="skill-badge ' + (connected ? "mcp-badge-on" : "mcp-badge-off") + '">' + esc(statusText) + "</span>",
    '<span class="skill-badge">工具 ' + toolCount + "</span>"
  ];
  if (trust) { badges.push('<span class="skill-badge">' + esc(trust) + "</span>"); }

  var sub = [];
  if (endpoint) { sub.push('<span class="skill-fn">' + esc(endpoint) + "</span>"); }
  if (cfg.description) { sub.push('<span class="skill-desc">' + esc(cfg.description) + "</span>"); }
  if (status && status.lastError) {
    sub.push('<span class="skill-desc mcp-last-error">错误: ' + esc(status.lastError) + "</span>");
  }

  var actions = '<div class="mcp-row-actions">'
    + '<button type="button" data-mcp-action="toggle" data-mcp-name="' + esc(name) + '"'
    + ' title="' + (enabled ? "停用并热重载" : "启用并热重载") + '">' + (enabled ? "停用" : "启用") + "</button>"
    + '<button type="button" data-mcp-action="tools" data-mcp-name="' + esc(name) + '"'
    + ' title="查看该 MCP 暴露的工具清单（GET /web/api/mcps/{name}/tools）">工具</button>'
    + '<button type="button" data-mcp-action="edit" data-mcp-name="' + esc(name) + '">编辑</button>'
    + '<button type="button" class="danger-btn" data-mcp-action="delete" data-mcp-name="' + esc(name) + '">删除</button>'
    + "</div>";

  return '<div class="skill-row mcp-row">'
    + '<div class="skill-row-main"><span class="skill-name">' + esc(name) + "</span>" + badges.join("") + "</div>"
    + (sub.length ? '<div class="skill-row-sub">' + sub.join("") + "</div>" : "")
    + actions
    + "</div>";
}

// stdio 展示 command + args，其余传输展示 url（后端字段缺失时不补默认值）。
function mcpEndpoint(cfg) {
  if (cfg && cfg.command) {
    var args = cfg.args && cfg.args.length ? " " + cfg.args.join(" ") : "";
    return String(cfg.command) + args;
  }
  if (cfg && cfg.url) { return String(cfg.url); }
  return "";
}

// enabled 与 disabled 同时存在（兼容 MCP 官方格式），disabled 优先。
function mcpConfigEnabled(cfg) {
  if (!cfg) { return false; }
  if (cfg.disabled) { return false; }
  return !!cfg.enabled;
}

// ---- 工具清单弹窗 ----

// 请求序号：连续打开不同 MCP 时丢弃迟到响应。
var mcpToolsSeq = 0;
// 当前工具弹窗对应的 MCP 名称（工具级启停后按名称重新加载）。
var mcpToolsName = "";

// 打开工具弹窗并加载该 MCP 的工具清单（未连接 / 未启用时后端返回空列表）。
function showMCPTools(name) {
  var overlay = mcpEl("mcp-tools-overlay");
  var titleEl = mcpEl("mcp-tools-title");
  var metaEl = mcpEl("mcp-tools-meta");
  var bodyEl = mcpEl("mcp-tools-body");
  if (!overlay || !bodyEl) { return; }

  mcpToolsName = name;
  if (titleEl) { titleEl.textContent = "工具列表 · " + name; }
  if (metaEl) { metaEl.textContent = ""; }
  bodyEl.innerHTML = '<div class="skills-empty">加载中…</div>';
  overlay.style.display = "flex";

  var seq = ++mcpToolsSeq;
  mcpAPI("/web/api/mcps/" + encodeURIComponent(name) + "/tools").then(function (result) {
    if (seq !== mcpToolsSeq) { return; }
    if (result.status !== 200 || !result.body) {
      bodyEl.innerHTML = '<div class="skills-empty skills-error">加载失败: '
        + esc(mcpErrorText(result, "请求失败")) + "</div>";
      return;
    }
    renderMCPTools(result.body);
  }).catch(function () {
    if (seq !== mcpToolsSeq) { return; }
    bodyEl.innerHTML = '<div class="skills-empty skills-error">加载失败（网络错误）</div>';
  });
}

function renderMCPTools(body) {
  var metaEl = mcpEl("mcp-tools-meta");
  var bodyEl = mcpEl("mcp-tools-body");
  if (!bodyEl) { return; }

  var tools = (body && body.tools) || [];
  var count = body && typeof body.count === "number" ? body.count : tools.length;
  if (metaEl) { metaEl.textContent = "共 " + count + " 个"; }
  if (!tools.length) {
    bodyEl.innerHTML = '<div class="skills-empty">未发现工具：该 MCP 可能未启用、未连接，或尚未完成 tools/list 握手</div>';
    return;
  }

  var rows = [];
  rows.push('<div style="display:flex;gap:8px;padding:0 0 8px">'
    + '<button type="button" data-mcp-tools-batch="enable">全部启用</button>'
    + '<button type="button" data-mcp-tools-batch="disable">全部禁用</button>'
    + "</div>");
  for (var i = 0; i < tools.length; i++) {
    var tool = tools[i] || {};
    var toolName = tool.name ? String(tool.name) : "(未命名)";
    var desc = tool.description ? String(tool.description) : "";
    var badges = "";
    if (tool.enabled === false) { badges += '<span class="skill-badge mcp-badge-off">已禁用</span>'; }
    if (tool.healthy === false) { badges += '<span class="skill-badge mcp-badge-off">健康异常</span>'; }
    var schema = tool.inputSchema ? JSON.stringify(tool.inputSchema, null, 2) : "";
    var enableNow = tool.enabled === false;
    var toggle = '<button type="button" data-mcp-tool-toggle="1" data-mcp-tool-name="' + esc(toolName) + '"'
      + ' data-mcp-tool-enable="' + (enableNow ? "true" : "false") + '">' + (enableNow ? "启用" : "禁用") + "</button>";
    rows.push('<div class="skill-row mcp-tool-row">'
      + '<div class="skill-row-main"><span class="skill-name">' + esc(toolName) + "</span>" + badges
      + '<span style="margin-left:auto">' + toggle + "</span></div>"
      + (desc ? '<div class="skill-row-sub"><span class="skill-desc">' + esc(desc) + "</span></div>" : "")
      + (schema ? '<details class="mcp-tool-schema"><summary>inputSchema</summary><pre class="mcp-tool-schema-pre">'
        + esc(schema) + "</pre></details>" : "")
      + "</div>");
  }
  bodyEl.innerHTML = rows.join("");
}

function closeMCPTools() {
  var overlay = mcpEl("mcp-tools-overlay");
  if (overlay) { overlay.style.display = "none"; }
}

// ---- 工具级启停 ----

function setMCPToolEnabled(toolName, enable) {
  var name = mcpToolsName;
  if (!name || !toolName) { return; }
  mcpAPI("/web/api/mcps/" + encodeURIComponent(name) + "/tools/" + encodeURIComponent(toolName) + "/" + (enable ? "enable" : "disable"), { method: "POST" })
    .then(function (result) {
      if (result.status < 200 || result.status >= 300) {
        showToast("操作失败: " + mcpErrorText(result, "请求失败"), "err");
        return;
      }
      showToast((enable ? "已启用工具: " : "已禁用工具: ") + toolName);
      showMCPTools(name);
    })
    .catch(function () { showToast("操作失败（网络错误）", "err"); });
}

function setAllMCPToolsEnabled(enable) {
  var name = mcpToolsName;
  if (!name) { return; }
  mcpAPI("/web/api/mcps/" + encodeURIComponent(name) + "/tools/" + (enable ? "enable" : "disable"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ tools: [] })
  }).then(function (result) {
    if (result.status < 200 || result.status >= 300) {
      showToast("操作失败: " + mcpErrorText(result, "请求失败"), "err");
      return;
    }
    showToast(enable ? "已启用全部工具" : "已禁用全部工具");
    showMCPTools(name);
  }).catch(function () { showToast("操作失败（网络错误）", "err"); });
}

// ---- 变更操作（启停 / 删除 / 热重载） ----

function toggleMCP(name) {
  var cfg = mcpConfigs[name];
  var enable = !mcpConfigEnabled(cfg);
  var action = enable ? "enable" : "disable";
  mcpAPI("/web/api/mcps/" + encodeURIComponent(name) + "/" + action, { method: "POST" })
    .then(function (result) {
      if (result.status < 200 || result.status >= 300) {
        showToast("操作失败: " + mcpErrorText(result, "请求失败"), "err");
        return;
      }
      showToast(enable ? "已启用: " + name : "已停用: " + name);
      refreshMCPs();
    })
    .catch(function () { showToast("操作失败（网络错误）", "err"); });
}

function deleteMCP(name) {
  if (!window.confirm("确定删除 MCP「" + name + "」？\n将从 mcp 配置文件删除并热重载。")) { return; }
  mcpAPI("/web/api/mcps/" + encodeURIComponent(name), { method: "DELETE" })
    .then(function (result) {
      if (result.status < 200 || result.status >= 300) {
        showToast("删除失败: " + mcpErrorText(result, "请求失败"), "err");
        return;
      }
      if (mcpEditingName === name) { closeMCPEditor(); }
      showToast("已删除: " + name);
      refreshMCPs();
    })
    .catch(function () { showToast("删除失败（网络错误）", "err"); });
}

function reloadMCPs() {
  mcpAPI("/web/api/mcps/reload", { method: "POST" })
    .then(function (result) {
      if (result.status < 200 || result.status >= 300) {
        showToast("热重载失败: " + mcpErrorText(result, "请求失败"), "err");
        return;
      }
      var count = result.body && typeof result.body.count === "number" ? result.body.count : null;
      showToast(count === null ? "MCP 配置已热重载" : "MCP 配置已热重载（" + count + " 个）");
      refreshMCPs();
    })
    .catch(function () { showToast("热重载失败（网络错误）", "err"); });
}

// ---- 新增 / 编辑表单 ----

function editMCP(name) {
  var cached = mcpConfigs[name] || null;
  mcpAPI("/web/api/mcps/" + encodeURIComponent(name))
    .then(function (result) {
      if (result.status === 200 && result.body && result.body.config) {
        openMCPEditor(result.body.config);
        return;
      }
      if (cached) { openMCPEditor(cached); return; }
      showToast("读取配置失败: " + mcpErrorText(result, "请求失败"), "err");
    })
    .catch(function () {
      if (cached) { openMCPEditor(cached); return; }
      showToast("读取配置失败（网络错误）", "err");
    });
}

function openMCPEditor(cfg) {
  var editor = mcpEl("mcp-editor");
  if (!editor) { return; }
  cfg = cfg || {};
  mcpEditingName = cfg.name ? String(cfg.name) : "";

  var titleEl = mcpEl("mcp-editor-title");
  if (titleEl) { titleEl.textContent = mcpEditingName ? "编辑 MCP: " + mcpEditingName : "新增 MCP"; }
  setMCPValue("mcp-form-original-name", mcpEditingName);
  setMCPValue("mcp-form-name", mcpEditingName);
  setMCPValue("mcp-form-type", cfg.type || "stdio");
  setMCPValue("mcp-form-trust", cfg.trustLevel || "");
  setMCPValue("mcp-form-timeout", durationToSeconds(cfg.timeout));
  setMCPValue("mcp-form-max-parallel", typeof cfg.maxParallelCalls === "number" && cfg.maxParallelCalls > 0 ? String(cfg.maxParallelCalls) : "");
  var enabledEl = mcpEl("mcp-form-enabled");
  // 新增默认勾选启用（服务端 BuildConfig 对新建项的默认值也是 enabled=true）。
  if (enabledEl) { enabledEl.checked = mcpEditingName ? mcpConfigEnabled(cfg) : true; }
  // 重命名不受支持：服务端 Update 强制使用路径中的名称（req.Name = name），
  // 编辑时锁定名称输入框，避免保存后“名称没变”的误解。
  var nameEl = mcpEl("mcp-form-name");
  if (nameEl) { nameEl.disabled = !!mcpEditingName; }
  setMCPValue("mcp-form-description", cfg.description || "");
  setMCPValue("mcp-form-command", cfg.command || "");
  setMCPValue("mcp-form-args", cfg.args && cfg.args.length ? cfg.args.join("\n") : "");
  // env 拆两组回填行编辑器：URL 传输把 HEADER_* 拆进 headers（去前缀），其余进环境变量；
  // stdio（或未知类型）不做拆分——HEADER_* 只是普通环境变量，与 console 面板一致，
  // 否则保存时会被 merge 丢掉。
  // 两组都渲染，可见性由 syncMCPEditorFields 按传输类型控制（服务端把 URL 传输的
  // headers 映射为 HEADER_* 环境变量，见 admin/configfile.go applyHeaders）。
  var transportLower = String(cfg.type || "").trim().toLowerCase();
  var stdioLike = transportLower === "" || transportLower === "stdio";
  var envSplit = splitMcpEnv(cfg.env, stdioLike);
  renderKvRows(mcpEl("mcp-env-rows"), envSplit.envRows, {
    separator: "=",
    keyPlaceholder: "KEY",
    valuePlaceholder: "VALUE"
  });
  renderKvRows(mcpEl("mcp-headers-rows"), envSplit.headerRows, {
    separator: ":",
    keyPlaceholder: "HEADER",
    valuePlaceholder: "VALUE"
  });
  setMCPValue("mcp-form-url", cfg.url || "");
  setMCPFormStatus("", "");
  syncMCPEditorFields();

  editor.hidden = false;
  if (editor.scrollIntoView) { editor.scrollIntoView({ block: "nearest" }); }
  if (nameEl && !nameEl.disabled && nameEl.focus) { nameEl.focus(); }
}

function closeMCPEditor() {
  var editor = mcpEl("mcp-editor");
  if (editor) { editor.hidden = true; }
  mcpEditingName = "";
  setMCPFormStatus("", "");
}

// stdio 显示 command/args/env，其余传输显示 url/headers（需求约定）。
function syncMCPEditorFields() {
  var typeEl = mcpEl("mcp-form-type");
  var isStdio = !typeEl || typeEl.value === "stdio";
  var stdioBlock = mcpEl("mcp-form-stdio-block");
  var urlBlock = mcpEl("mcp-form-url-block");
  if (stdioBlock) { stdioBlock.hidden = !isStdio; }
  if (urlBlock) { urlBlock.hidden = isStdio; }
}

function submitMCPForm(event) {
  if (event) { event.preventDefault(); }
  var nameEl = mcpEl("mcp-form-name");
  var typeEl = mcpEl("mcp-form-type");
  var name = (nameEl && nameEl.value ? nameEl.value : "").trim();
  var type = typeEl ? typeEl.value : "stdio";
  if (!name) {
    setMCPFormStatus("名称不能为空", "err");
    showToast("请先填写 MCP 名称", "err");
    return;
  }
  if (type === "stdio") {
    var commandEl = mcpEl("mcp-form-command");
    if (!commandEl || !commandEl.value.trim()) {
      setMCPFormStatus("stdio 类型必须填写命令", "err");
      showToast("stdio 类型必须填写命令", "err");
      return;
    }
  } else {
    var urlEl = mcpEl("mcp-form-url");
    if (!urlEl || !urlEl.value.trim()) {
      setMCPFormStatus(type + " 类型必须填写 URL", "err");
      showToast(type + " 类型必须填写 URL", "err");
      return;
    }
  }

  // 行编辑器校验只看当前传输类型可见的编辑器：有值但无键名的行会被 mergeMcpEnv
  // 忽略，提交前显式拦截；重复键不拦截（后者胜出，见 mcp-kv.js 注释），由行内
  // invalid 样式提示。URL 传输下 env 编辑器虽隐藏，但仍随表单一并提交（见下）。
  var envKvRows = annotateKvRows(readKvRows(mcpEl("mcp-env-rows")));
  var headerKvRows = annotateKvRows(readKvRows(mcpEl("mcp-headers-rows")));
  var visibleKvRows = type === "stdio" ? envKvRows : headerKvRows;
  if (kvRowsHaveEmptyKey(visibleKvRows)) {
    var kvMsg = "存在未填写键名的行，请补全键名或删除该行";
    setMCPFormStatus(kvMsg, "err");
    showToast(kvMsg, "err");
    return;
  }

  var originalEl = mcpEl("mcp-form-original-name");
  var originalName = originalEl && originalEl.value ? originalEl.value : "";
  var isUpdate = !!originalName;
  var path = isUpdate ? "/web/api/mcps/" + encodeURIComponent(originalName) : "/web/api/mcps";
  var payload = buildMCPUpsertRequest(name, type, envKvRows, headerKvRows);
  var saveBtn = mcpEl("mcp-form-save");
  if (saveBtn) { saveBtn.disabled = true; }
  setMCPFormStatus(isUpdate ? "保存中…" : "创建中…", "busy");

  mcpAPI(path, {
    method: isUpdate ? "PUT" : "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload)
  })
    .then(function (result) {
      if (saveBtn) { saveBtn.disabled = false; }
      if (result.status < 200 || result.status >= 300) {
        var msg = "保存失败: " + mcpErrorText(result, "请求失败");
        setMCPFormStatus(msg, "err");
        showToast(msg, "err");
        return;
      }
      closeMCPEditor();
      showToast(isUpdate ? "已保存 MCP: " + name : "已新增 MCP: " + name);
      refreshMCPs();
    })
    .catch(function () {
      if (saveBtn) { saveBtn.disabled = false; }
      setMCPFormStatus("保存失败（网络错误）", "err");
      showToast("保存失败（网络错误）", "err");
    });
}

// buildMCPUpsertRequest 只发送全量表单值（envRows/headerRows 为 annotateKvRows 后的行）：
//   - args 始终发送（空数组清除旧值）；
//   - env 单轴承载两种行：stdio 只发环境变量行；URL 传输发「环境变量行 ∪ HEADER_
//     前缀行」（服务端 applyHeaders/BuildConfig 语义，见 admin/configfile.go）。
//     URL 下 env 编辑器虽隐藏，仍随表单一并提交，避免编辑既有配置时丢失非 HEADER_
//     变量；不发送 headers 字段，避免与 env 双写；
//   - description 始终发送（空串可清除）；trustLevel/timeout/maxParallel 留空即省略
//     （服务端语义：nil 保持原值，见 admin/configfile.go BuildConfig）。
function buildMCPUpsertRequest(name, type, envRows, headerRows) {
  var payload = {
    name: name,
    type: type,
    enabled: !!(mcpEl("mcp-form-enabled") && mcpEl("mcp-form-enabled").checked)
  };
  var descriptionEl = mcpEl("mcp-form-description");
  payload.description = descriptionEl ? descriptionEl.value.trim() : "";
  var trustEl = mcpEl("mcp-form-trust");
  if (trustEl && trustEl.value) { payload.trustLevel = trustEl.value; }
  var timeoutEl = mcpEl("mcp-form-timeout");
  var timeout = timeoutEl ? parseInt(timeoutEl.value, 10) : NaN;
  if (!isNaN(timeout) && timeout > 0) { payload.timeoutSeconds = timeout; }
  var maxParallelEl = mcpEl("mcp-form-max-parallel");
  var maxParallel = maxParallelEl ? parseInt(maxParallelEl.value, 10) : NaN;
  if (!isNaN(maxParallel) && maxParallel >= 0) { payload.maxParallelCalls = maxParallel; }
  if (type === "stdio") {
    var commandEl = mcpEl("mcp-form-command");
    payload.command = commandEl ? commandEl.value.trim() : "";
    var argsEl = mcpEl("mcp-form-args");
    payload.args = splitLines(argsEl ? argsEl.value : "");
    payload.env = mergeMcpEnv(envRows, []);
  } else {
    var urlEl = mcpEl("mcp-form-url");
    payload.url = urlEl ? urlEl.value.trim() : "";
    payload.env = mergeMcpEnv(envRows, headerRows);
  }
  return payload;
}

// ---- 表单辅助 ----

function setMCPValue(id, value) {
  var el = mcpEl(id);
  if (el) { el.value = value; }
}

function setMCPFormStatus(text, kind) {
  var el = mcpEl("mcp-form-status");
  if (!el) { return; }
  el.textContent = text || "";
  el.className = "cfg-status" + (kind ? " " + kind : "");
}

function splitLines(value) {
  var lines = String(value || "").split(/\r?\n/);
  var out = [];
  for (var i = 0; i < lines.length; i++) {
    var line = lines[i].trim();
    if (line) { out.push(line); }
  }
  return out;
}

// Go time.Duration.String()（如 "30s" / "1m0s"）转秒数；无法解析返回空串。
var mcpDurationUnits = { ns: 1e-9, us: 1e-6, "µs": 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600 };

function durationToSeconds(text) {
  var s = String(text || "").trim();
  var match = /^([0-9]+(?:\.[0-9]+)?)(ns|us|µs|ms|s|m|h)$/.exec(s);
  if (!match || !Object.prototype.hasOwnProperty.call(mcpDurationUnits, match[2])) { return ""; }
  var seconds = parseFloat(match[1]) * mcpDurationUnits[match[2]];
  if (!isFinite(seconds) || seconds <= 0) { return ""; }
  return String(Math.round(seconds * 1000) / 1000);
}

// ---- 初始化 ----

export function initMCP() {
  var listEl = mcpEl("mcp-list");
  if (listEl) {
    // 行内按钮每次渲染重建，用容器级委托绑定一次。
    listEl.addEventListener("click", function (event) {
      var button = mcpActionButtonFrom(event.target);
      if (!button) { return; }
      var name = button.getAttribute("data-mcp-name") || "";
      var action = button.getAttribute("data-mcp-action") || "";
      if (!name) { return; }
      if (action === "toggle") { toggleMCP(name); }
      else if (action === "tools") { showMCPTools(name); }
      else if (action === "edit") { editMCP(name); }
      else if (action === "delete") { deleteMCP(name); }
    });
  }
  var toolsBody = mcpEl("mcp-tools-body");
  if (toolsBody) {
    toolsBody.addEventListener("click", function (event) {
      var node = event.target;
      var batch = null;
      var toggle = null;
      while (node && node !== toolsBody) {
        if (node.getAttribute) {
          if (!batch && node.getAttribute("data-mcp-tools-batch")) {
            batch = node.getAttribute("data-mcp-tools-batch");
          }
          if (!toggle && node.getAttribute("data-mcp-tool-toggle")) { toggle = node; }
        }
        node = node.parentNode;
      }
      if (batch) { setAllMCPToolsEnabled(batch === "enable"); return; }
      if (toggle) {
        var toolName = toggle.getAttribute("data-mcp-tool-name") || "";
        if (toolName) { setMCPToolEnabled(toolName, toggle.getAttribute("data-mcp-tool-enable") === "true"); }
      }
    });
  }
  var refreshBtn = mcpEl("mcp-refresh-btn");
  if (refreshBtn) { refreshBtn.addEventListener("click", function () { refreshMCPs(); }); }
  var reloadBtn = mcpEl("mcp-reload-btn");
  if (reloadBtn) { reloadBtn.addEventListener("click", function () { reloadMCPs(); }); }
  var addBtn = mcpEl("mcp-add-btn");
  if (addBtn) { addBtn.addEventListener("click", function () { openMCPEditor(null); }); }

  var form = mcpEl("mcp-form");
  if (form) {
    form.addEventListener("submit", submitMCPForm);
    // 焦点在表单内时 Esc 只收起表单：阻止冒泡避免触发 chat.js 的会话中断。
    form.addEventListener("keydown", function (event) {
      if (event.key === "Escape") {
        event.stopPropagation();
        closeMCPEditor();
      }
    });
  }
  var typeEl = mcpEl("mcp-form-type");
  if (typeEl) { typeEl.addEventListener("change", syncMCPEditorFields); }
  // 行编辑器「＋ 添加一行」：appendKvRow 会复用容器首次渲染时缓存的 placeholder 等选项。
  var envAddBtn = mcpEl("mcp-env-add");
  if (envAddBtn) {
    envAddBtn.addEventListener("click", function () { appendKvRow(mcpEl("mcp-env-rows")); });
  }
  var headersAddBtn = mcpEl("mcp-headers-add");
  if (headersAddBtn) {
    headersAddBtn.addEventListener("click", function () { appendKvRow(mcpEl("mcp-headers-rows")); });
  }
  var cancelBtn = mcpEl("mcp-form-cancel");
  if (cancelBtn) { cancelBtn.addEventListener("click", function () { closeMCPEditor(); }); }

  // 工具弹窗：✕ / 遮罩点击 / Esc 关闭（Esc 只在弹窗打开时拦截）。
  var toolsCloseBtn = mcpEl("mcp-tools-close");
  if (toolsCloseBtn) { toolsCloseBtn.addEventListener("click", function () { closeMCPTools(); }); }
  var toolsOverlay = mcpEl("mcp-tools-overlay");
  if (toolsOverlay) {
    toolsOverlay.addEventListener("click", function (event) {
      if (event.target === toolsOverlay) { closeMCPTools(); }
    });
  }
  document.addEventListener("keydown", function (event) {
    var overlay = mcpEl("mcp-tools-overlay");
    if (event.key === "Escape" && overlay && overlay.style.display !== "none") {
      closeMCPTools();
    }
  });
}

function mcpActionButtonFrom(target) {
  if (!target || typeof target.closest !== "function") { return null; }
  return target.closest("button[data-mcp-action]");
}
