// MCP 表单 env / headers 结构化键值行编辑器：纯函数 + 轻量 DOM 渲染，无外部依赖。
//
// 数据模型：一行 = { key: string, value: string }（数组顺序即界面顺序）。
//
// 与后端的约定（backend/internal/mcp/admin/configfile.go）：
//   - URL 传输的 headers 以 "HEADER_<Name>" 形式存进 env（applyHeaders 只做前缀
//     拼接，不做大小写归一）；URL 传输把 env 中 HEADER_* 当 header 行、其余当
//     环境变量行；stdio 的 env 全部是环境变量行。
//   - 因此 splitMcpEnv 按前缀拆两组回填，mergeMcpEnv 合成单个 env map；表单不再
//     发送 headers 字段，避免与 env 双写。
//
// 校验约定（annotateKvRows，渲染与提交两端共用）：
//   - 空键行在 mergeMcpEnv 中被忽略；
//   - 「有值但无键名」的行标为 invalid（空键高亮），提交前由调用方拦截；
//   - 重复键「后者胜出」（数组靠后的行覆盖靠前的行，见 mergeMcpEnv），所有同键行
//     标为 invalid 以提示冲突，但不阻止提交；
//   - 完全空白的行不算错误（新增行的占位状态，不标红）。
//
// 本模块刻意不 import util.js：纯函数要能在 Node（无 DOM）下直接 import 做单测。
// DOM 渲染全部走 createElement / textContent / value，不经过 innerHTML，因此没有
// HTML 注入面（无需 esc()）。

var HEADER_PREFIX = "HEADER_";

var DEFAULT_OPTIONS = {
  separator: "=",
  keyPlaceholder: "KEY",
  valuePlaceholder: "VALUE",
  keyAriaLabel: "键名",
  valueAriaLabel: "值",
  removeLabel: "删除此行",
  duplicateHint: "键名重复：保存时最后一行生效",
  emptyKeyHint: "键名不能为空：该行不会被保存"
};

function stringValue(value) {
  return value === null || value === undefined ? "" : String(value);
}

// normalizeRows 统一为 { key, value } 并 trim（无效行忽略）。键/值 trim 与原文本
// 解析行为保持一致（避免行尾空格悄悄写进配置）。
function normalizeRows(rows) {
  var out = [];
  if (!rows || !rows.length) { return out; }
  for (var i = 0; i < rows.length; i++) {
    var row = rows[i] || {};
    out.push({
      key: stringValue(row.key).trim(),
      value: stringValue(row.value).trim()
    });
  }
  return out;
}

// ---- 纯函数 ----

// splitMcpEnv 把服务端 env 拆成 { envRows, headerRows }：
// HEADER_ 前缀的键归入 headerRows 并去掉前缀，其余归入 envRows；两组各自按键名
// 排序（Object.keys().sort()，UTF-16 码点序，跨引擎稳定）。
// keepHeaderPrefix=true 表示调用方按 stdio 语义处理：HEADER_* 只是普通环境变量，
// 不做前缀拆分（与 console 面板 createMcpDraft 的行为一致，避免保存时丢键）。
export function splitMcpEnv(env, keepHeaderPrefix) {
  var src = env && typeof env === "object" ? env : {};
  var envRows = [];
  var headerRows = [];
  var keys = Object.keys(src).sort();
  for (var i = 0; i < keys.length; i++) {
    var key = keys[i];
    var value = stringValue(src[key]);
    if (!keepHeaderPrefix && key.indexOf(HEADER_PREFIX) === 0) {
      headerRows.push({ key: key.slice(HEADER_PREFIX.length), value: value });
    } else {
      envRows.push({ key: key, value: value });
    }
  }
  return { envRows: envRows, headerRows: headerRows };
}

// mergeMcpEnv 把两组行合并为请求体 payload.env：
//   - 空键行忽略（含完全空白的占位行）；
//   - headerRows 的键统一加 "HEADER_" 前缀（与 applyHeaders 对齐，不加大小写归一）；
//   - 重复键后者胜出：数组靠后的行覆盖靠前的行（校验层会把重复行标红提示）。
export function mergeMcpEnv(envRows, headerRows) {
  var out = {};
  assignRows(out, normalizeRows(envRows), "");
  assignRows(out, normalizeRows(headerRows), HEADER_PREFIX);
  return out;
}

function assignRows(target, rows, prefix) {
  for (var i = 0; i < rows.length; i++) {
    var key = rows[i].key;
    if (!key) { continue; }
    target[prefix + key] = rows[i].value;
  }
}

// annotateKvRows 逐行返回 { key, value, invalid, reasons }（不改动入参）：
//   - reasons 取值 "empty-key"（有值无键）/"duplicate-key"（同容器内重复）；
//   - 完全空白的行不算错误（新增行的占位状态）。
export function annotateKvRows(rows) {
  var normalized = normalizeRows(rows);
  var counts = {};
  for (var i = 0; i < normalized.length; i++) {
    var key = normalized[i].key;
    if (key) { counts[key] = (counts[key] || 0) + 1; }
  }
  var out = [];
  for (var j = 0; j < normalized.length; j++) {
    var row = normalized[j];
    var reasons = [];
    if (!row.key && row.value) { reasons.push("empty-key"); }
    if (row.key && counts[row.key] > 1) { reasons.push("duplicate-key"); }
    out.push({
      key: row.key,
      value: row.value,
      invalid: reasons.length > 0,
      reasons: reasons
    });
  }
  return out;
}

// kvRowsHaveEmptyKey 判断（通常来自 annotateKvRows 的）行里是否存在「有值无键」，
// 供提交前拦截：这类值会被 mergeMcpEnv 忽略，静默丢弃对用户不友好。
export function kvRowsHaveEmptyKey(rows) {
  if (!rows || !rows.length) { return false; }
  for (var i = 0; i < rows.length; i++) {
    var reasons = rows[i] && rows[i].reasons ? rows[i].reasons : [];
    for (var j = 0; j < reasons.length; j++) {
      if (reasons[j] === "empty-key") { return true; }
    }
  }
  return false;
}

// parseKvText 解析粘贴/文本块为行数组，支持 "=" 与 ":" 两种分隔（可粘贴
// "A=1\nB=2"）。preferredSeparator 优先；不存在时取两种分隔符中更靠前者；
// 整行没有分隔符时视为只有键。空白行忽略；值保留分隔符之后的全部内容。
export function parseKvText(text, preferredSeparator) {
  var out = [];
  var lines = stringValue(text).split(/\r?\n/);
  for (var i = 0; i < lines.length; i++) {
    var line = lines[i];
    if (!line.trim()) { continue; }
    var idx = separatorIndex(line, preferredSeparator);
    if (idx < 0) {
      out.push({ key: line.trim(), value: "" });
      continue;
    }
    out.push({
      key: line.slice(0, idx).trim(),
      value: line.slice(idx + 1).trim()
    });
  }
  return out;
}

function separatorIndex(line, preferred) {
  if (preferred) {
    var preferredIdx = line.indexOf(preferred);
    if (preferredIdx >= 0) { return preferredIdx; }
  }
  var eq = line.indexOf("=");
  var colon = line.indexOf(":");
  if (eq < 0) { return colon; }
  if (colon < 0) { return eq; }
  return Math.min(eq, colon);
}

// ---- DOM 渲染（浏览器环境；纯函数单测无需触发） ----

var containerOptions = new WeakMap();
var boundContainers = new WeakSet();

function resolveOptions(options, fallback) {
  var source = options || {};
  var base = fallback || {};
  var out = {};
  var keys = Object.keys(DEFAULT_OPTIONS);
  for (var i = 0; i < keys.length; i++) {
    var key = keys[i];
    if (Object.prototype.hasOwnProperty.call(source, key) && source[key] !== null && source[key] !== undefined) {
      out[key] = source[key];
    } else if (base[key] !== null && base[key] !== undefined) {
      out[key] = base[key];
    } else {
      out[key] = DEFAULT_OPTIONS[key];
    }
  }
  return out;
}

function createRowElement(opts, row) {
  var rowEl = document.createElement("div");
  rowEl.className = "kv-row";

  var keyInput = document.createElement("input");
  keyInput.type = "text";
  keyInput.className = "kv-key";
  keyInput.placeholder = opts.keyPlaceholder;
  keyInput.setAttribute("aria-label", opts.keyAriaLabel);
  keyInput.autocomplete = "off";
  keyInput.spellcheck = false;
  keyInput.value = row && row.key !== null && row.key !== undefined ? String(row.key) : "";

  var sep = document.createElement("span");
  sep.className = "kv-sep";
  sep.setAttribute("aria-hidden", "true");
  sep.textContent = opts.separator;

  var valueInput = document.createElement("input");
  valueInput.type = "text";
  valueInput.className = "kv-value";
  valueInput.placeholder = opts.valuePlaceholder;
  valueInput.setAttribute("aria-label", opts.valueAriaLabel);
  valueInput.autocomplete = "off";
  valueInput.spellcheck = false;
  valueInput.value = row && row.value !== null && row.value !== undefined ? String(row.value) : "";

  var removeBtn = document.createElement("button");
  removeBtn.type = "button";
  removeBtn.className = "kv-remove";
  removeBtn.textContent = "✕";
  removeBtn.title = opts.removeLabel;
  removeBtn.setAttribute("aria-label", opts.removeLabel);

  rowEl.appendChild(keyInput);
  rowEl.appendChild(sep);
  rowEl.appendChild(valueInput);
  rowEl.appendChild(removeBtn);
  return rowEl;
}

function ensureRow(container) {
  if (container.querySelector(".kv-row")) { return null; }
  var opts = containerOptions.get(container) || DEFAULT_OPTIONS;
  var rowEl = createRowElement(opts, { key: "", value: "" });
  container.appendChild(rowEl);
  return rowEl;
}

// renderKvRows 全量渲染容器：rows 为空时给一行空白行占位（保证可输入）。
// options: { separator, keyPlaceholder, valuePlaceholder, keyAriaLabel,
//            valueAriaLabel, removeLabel }
// 重复调用会重置容器内容；首次调用绑定容器级事件（删除 / 输入校验 / 粘贴拆分）。
export function renderKvRows(container, rows, options) {
  if (!container) { return; }
  var opts = resolveOptions(options, containerOptions.get(container));
  containerOptions.set(container, opts);
  bindContainer(container);
  while (container.firstChild) { container.removeChild(container.firstChild); }
  var list = Array.isArray(rows) && rows.length ? rows : [{ key: "", value: "" }];
  for (var i = 0; i < list.length; i++) {
    container.appendChild(createRowElement(opts, list[i]));
  }
  refreshKvValidation(container);
}

// appendKvRow 在末尾追加一行空白行并聚焦键输入框（供「＋ 添加一行」按钮调用）。
export function appendKvRow(container) {
  if (!container) { return; }
  var opts = containerOptions.get(container) || DEFAULT_OPTIONS;
  var rowEl = createRowElement(opts, { key: "", value: "" });
  container.appendChild(rowEl);
  refreshKvValidation(container);
  var keyInput = rowEl.querySelector(".kv-key");
  if (keyInput && keyInput.focus) { keyInput.focus(); }
}

// readKvRows 按 DOM 顺序读回行数组（键/值均 trim）。
export function readKvRows(container) {
  var out = [];
  if (!container) { return out; }
  var rowEls = container.querySelectorAll(".kv-row");
  for (var i = 0; i < rowEls.length; i++) {
    var keyInput = rowEls[i].querySelector(".kv-key");
    var valueInput = rowEls[i].querySelector(".kv-value");
    out.push({
      key: keyInput ? keyInput.value.trim() : "",
      value: valueInput ? valueInput.value.trim() : ""
    });
  }
  return out;
}

function bindContainer(container) {
  if (boundContainers.has(container)) { return; }
  boundContainers.add(container);
  container.addEventListener("click", function (event) {
    var target = event.target;
    if (!target || typeof target.closest !== "function") { return; }
    var removeBtn = target.closest(".kv-remove");
    if (!removeBtn || !container.contains(removeBtn)) { return; }
    var rowEl = removeBtn.closest(".kv-row");
    if (rowEl) { rowEl.remove(); }
    ensureRow(container); // 删空时留一行占位，避免容器塌缩成不可输入状态
    refreshKvValidation(container);
  });
  container.addEventListener("input", function () { refreshKvValidation(container); });
  container.addEventListener("paste", function (event) { handlePaste(container, event); });
}

// 粘贴拆分：剪贴板文本解析出多行（或键输入框里粘贴了带值的单行 "KEY=VALUE"）时，
// 用解析结果替换当前行；键输入框里无分隔符的单行文本、值输入框里的单行文本仍走
// 浏览器默认粘贴（否则会误把值内容当成新行）。
function handlePaste(container, event) {
  var target = event.target;
  if (!target || typeof target.closest !== "function") { return; }
  var inputEl = target.closest(".kv-key, .kv-value");
  if (!inputEl) { return; }
  var clipboard = event.clipboardData;
  var text = clipboard ? clipboard.getData("text/plain") : "";
  if (!text) { return; }
  var multiline = text.indexOf("\n") >= 0 || text.indexOf("\r") >= 0;
  if (!multiline && inputEl.classList.contains("kv-value")) { return; }
  var opts = containerOptions.get(container) || DEFAULT_OPTIONS;
  var parsed = parseKvText(text, opts.separator);
  if (!parsed.length) { return; }
  // 键输入框粘贴无分隔符的单行文本：交给默认粘贴（用户可能只是输入普通键名片段）。
  if (parsed.length === 1 && !parsed[0].value && !multiline) { return; }
  event.preventDefault();
  var rowEl = inputEl.closest(".kv-row");
  var newRows = [];
  for (var i = 0; i < parsed.length; i++) {
    newRows.push(createRowElement(opts, parsed[i]));
  }
  for (var j = 0; j < newRows.length; j++) {
    container.insertBefore(newRows[j], rowEl || null);
  }
  if (rowEl) { rowEl.remove(); }
  refreshKvValidation(container);
  var firstKey = newRows[0].querySelector(".kv-key");
  if (firstKey && firstKey.focus) { firstKey.focus(); }
}

// refreshKvValidation 就地更新 .invalid / aria-invalid / title，不重建 DOM
// （重建会丢焦点），输入与增删行后调用。
function refreshKvValidation(container) {
  var annotated = annotateKvRows(readKvRows(container));
  var rowEls = container.querySelectorAll(".kv-row");
  for (var i = 0; i < rowEls.length; i++) {
    var rowEl = rowEls[i];
    var row = annotated[i] || { invalid: false, reasons: [] };
    if (row.invalid) { rowEl.classList.add("invalid"); }
    else { rowEl.classList.remove("invalid"); }
    var hint = row.invalid ? reasonText(row.reasons) : "";
    if (hint) { rowEl.title = hint; }
    else { rowEl.removeAttribute("title"); }
    var inputs = rowEl.querySelectorAll("input");
    for (var j = 0; j < inputs.length; j++) {
      if (row.invalid) { inputs[j].setAttribute("aria-invalid", "true"); }
      else { inputs[j].removeAttribute("aria-invalid"); }
    }
  }
}

function reasonText(reasons) {
  var parts = [];
  for (var i = 0; i < reasons.length; i++) {
    if (reasons[i] === "empty-key") { parts.push(DEFAULT_OPTIONS.emptyKeyHint); }
    else if (reasons[i] === "duplicate-key") { parts.push(DEFAULT_OPTIONS.duplicateHint); }
  }
  return parts.join("；");
}
