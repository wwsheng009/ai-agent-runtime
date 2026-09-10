// 缓存分析页签（cache.analytics.v1）:会话总览 + 请求明细 + 消息追溯。
// 数据源 /web/api/cache/*（与 TUI /usage 共用同一后端 Source）。
// aicli micro web client 前端模块(无构建步骤,由 app.js 入口聚合)。

import { esc } from "./util.js";

var cacheLoaded = false;
var cacheAvailable = null; // null=未探测, true/false
var cacheRequestsCache = [];
// 会话感知（修复：左侧切换会话后缓存页签仍显示旧会话数据）：
//   currentSessionID      最近一次从 /web/api/sessions 同步到的当前会话 id；
//   cacheLoadedSessionID  已渲染数据对应的会话 id（后端 /web/api/cache/* 按
//                         请求时刻的当前会话解析，二者不一致即数据已过期）。
var currentSessionID = "";
var cacheLoadedSessionID = "";
// 各区块请求序号：丢弃会话切换 / SSE 防抖竞态下迟到的过期响应。
var overviewSeq = 0;
var requestsSeq = 0;

function cacheEl(id) { return document.getElementById(id); }

function cacheAPI(path) {
  return fetch(path, { cache: "no-store" })
    .then(function (res) {
      return res.json().then(function (body) {
        return { status: res.status, body: body };
      });
    });
}

function fmtInt(n) {
  if (n === undefined || n === null) { return "-"; }
  return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
}

function fmtPct(ratio) {
  if (ratio === undefined || ratio === null) { return "-"; }
  return (Math.round(ratio * 1000) / 10).toFixed(1) + "%";
}

function fmtTime(iso) {
  if (!iso) { return "-"; }
  try {
    var d = new Date(iso);
    if (isNaN(d.getTime())) { return "-"; }
    return d.toLocaleTimeString("zh-CN", { hour12: false }) + "." + String(d.getMilliseconds()).padStart(3, "0");
  } catch (e) { return "-"; }
}

// cache_status 徽标样式映射（hit=绿 write=蓝 reported_zero=黄 not_reported=灰 error=红）。
// 文案语义：reported_zero=provider 明确上报 cached_tokens=0（真未命中）；
// not_reported=provider 未回传缓存字段，命中状态未知（不能断言未命中）。
function statusBadgeClass(status) {
  switch (status) {
    case "hit": return "badge-hit";
    case "write": return "badge-write";
    case "reported_zero": return "badge-zero";
    case "error": return "badge-error";
    default: return "badge-none";
  }
}

function statusLabel(status) {
  switch (status) {
    case "hit": return "命中";
    case "write": return "写入";
    case "reported_zero": return "未命中";
    case "not_reported": return "未上报（未知）";
    case "error": return "错误";
    default: return status || "-";
  }
}

export function loadCacheAnalytics(force) {
  // 首次进入、强制刷新，或已渲染数据属于其他会话（切换会话发生在页签后台时
  // 由 syncCacheSession 记录）才拉取；同一会话内重复切页签不重复发请求。
  if (cacheLoaded && !force && cacheLoadedSessionID === currentSessionID) { return; }
  cacheLoaded = true;
  refreshCacheAnalytics();
}

// 强制刷新全部区块（页签切换后数据可能已更新；刷新按钮调用）。
export function refreshCacheAnalytics() {
  // 后端按请求时刻的当前会话解析 session_id，本次拉取即当前会话数据。
  cacheLoadedSessionID = currentSessionID;
  refreshCacheCapabilities();
  refreshCacheOverview();
  refreshCacheRequests();
}

// 仅当缓存页签当前可见时刷新（同一会话内的显式刷新；避免后台页签浪费请求）。
export function refreshCacheAnalyticsIfActive() {
  var panel = cacheEl("tab-cache");
  if (!panel || !panel.classList.contains("active")) { return; }
  refreshCacheAnalytics();
}

// 会话同步（sessions.js 拿到 /web/api/sessions 的 current_session_id 时调用）。
// 会话变化时：页签可见则立即重拉（旧会话快照不能留在屏幕上）；不可见则仅
// 记录，待下次进入页签时由 loadCacheAnalytics 按会话不一致强制刷新。
export function syncCacheSession(sessionID) {
  var next = sessionID || "";
  if (next === currentSessionID) { return; }
  currentSessionID = next;
  if (!cacheLoaded) { return; }                  // 尚未加载过：进入页签时自然拉取
  if (cacheLoadedSessionID === next) { return; } // 数据已是该会话（罕见竞态）
  refreshCacheAnalyticsIfActive();
}

// SSE cache_request_finished 到达（§6.3 增量刷新）：防抖重拉 overview +
// requests（§6.3 允许"直接重拉 overview"分支）。仅当页签 DOM 已挂载且
// 能力探测未失败时刷新；防抖窗口内多个请求终态合并为一次拉取。
var sseRefreshTimer = null;
export function handleCacheSSEEvent() {
  if (cacheAvailable === false) { return; }
  if (!cacheEl("cache-overview") && !cacheEl("cache-requests")) { return; }
  if (sseRefreshTimer) { return; }
  sseRefreshTimer = setTimeout(function () {
    sseRefreshTimer = null;
    refreshCacheOverview();
    refreshCacheRequests();
  }, 400);
}

function refreshCacheCapabilities() {
  var el = cacheEl("cache-capabilities");
  if (!el) { return; }
  cacheAPI("/web/api/cache/capabilities").then(function (result) {
    if (result.status !== 200 || !result.body || result.body.schema_version !== "cache.analytics.v1") {
      cacheAvailable = false;
      el.textContent = "缓存分析不可用（旧后端或未启用）";
      el.className = "cache-capabilities cache-disabled";
      return;
    }
    cacheAvailable = true;
    var detail = result.body.persisted
      ? "已持久化（重启/恢复会话后可回放，无上限）"
      : "每会话上限 " + (result.body.max_requests_per_session || 0) + " 条";
    el.textContent = "契约 " + result.body.schema_version + " · 数据源 " + (result.body.data_source || "live")
      + " · " + detail;
    el.className = "cache-capabilities";
  }).catch(function () {
    cacheAvailable = false;
    el.textContent = "缓存分析不可用（网络错误）";
    el.className = "cache-capabilities cache-disabled";
  });
}

function renderCacheError(el, err) {
  var code = err && err.code ? err.code : "unknown";
  var msg = err && err.message ? err.message : "";
  el.innerHTML = '<div class="cache-empty">加载失败: ' + esc(code) + (msg ? " — " + esc(msg) : "") + "</div>";
}

function refreshCacheOverview() {
  var el = cacheEl("cache-overview");
  if (!el) { return; }
  var seq = ++overviewSeq;
  el.innerHTML = '<div class="cache-empty">加载中…</div>';
  cacheAPI("/web/api/cache/overview").then(function (result) {
    if (seq !== overviewSeq) { return; } // 过期响应：期间已发出更新的拉取（如会话切换）
    if (result.status !== 200) { renderCacheError(el, result.body && result.body.error); return; }
    el.innerHTML = renderOverviewCards(result.body);
  }).catch(function () {
    if (seq !== overviewSeq) { return; }
    renderCacheError(el, null);
  });
}

function renderOverviewCards(overview) {
  var dist = overview.cache_status_distribution || {};
  var tokens = overview.tokens || {};
  var coverage = overview.coverage || {};
  var cards = [];
  cards.push(card("请求总数", fmtInt(overview.requests_total)));
  cards.push(card("缓存命中率", fmtPct(overview.cache_hit_ratio)));
  cards.push(card("缓存写入率", fmtPct(overview.cache_write_ratio)));
  cards.push(card("缓存读取 tokens", fmtInt(tokens.cache_read_tokens)));
  cards.push(card("缓存写入 tokens", fmtInt(tokens.cache_creation_tokens)));
  cards.push(card("prompt tokens", fmtInt(tokens.prompt_tokens)));
  var distParts = [];
  distParts.push("命中 " + fmtInt(dist.hit));
  distParts.push("写入 " + fmtInt(dist.write));
  distParts.push("未命中 " + fmtInt(dist.reported_zero));
  distParts.push("未上报（未知） " + fmtInt(dist.not_reported));
  distParts.push("错误 " + fmtInt(dist.error));
  var html = '<div class="cache-cards">' + cards.join("") + "</div>";
  html += '<div class="cache-dist">状态分布: ' + esc(distParts.join(" · ")) + "</div>";
  if (coverage.partial) {
    var reasons = (coverage.partial_reasons || []).join(", ");
    html += '<div class="cache-coverage cache-partial">⚠ 数据不完整' + (reasons ? ": " + esc(reasons) : "") + "</div>";
  }
  return html;
}

function card(label, value) {
  return '<div class="cache-card"><div class="cache-card-value">' + esc(value) + '</div><div class="cache-card-label">' + esc(label) + "</div></div>";
}

function refreshCacheRequests() {
  var el = cacheEl("cache-requests");
  if (!el) { return; }
  var seq = ++requestsSeq;
  el.innerHTML = '<div class="cache-empty">加载中…</div>';
  cacheAPI("/web/api/cache/requests?limit=50").then(function (result) {
    if (seq !== requestsSeq) { return; } // 过期响应：期间已发出更新的拉取（如会话切换）
    if (result.status !== 200) { renderCacheError(el, result.body && result.body.error); return; }
    cacheRequestsCache = result.body.requests || [];
    el.innerHTML = renderRequestsTable(cacheRequestsCache);
    bindRequestRows(el);
  }).catch(function () {
    if (seq !== requestsSeq) { return; }
    renderCacheError(el, null);
  });
}

function renderRequestsTable(requests) {
  if (!requests.length) {
    return '<div class="cache-empty">暂无 LLM 请求记录（发送一条消息后刷新）</div>';
  }
  var rows = "";
  for (var i = 0; i < requests.length; i++) {
    var r = requests[i];
    var usage = r.usage || {};
    rows += '<tr class="cache-row" data-request-id="' + esc(r.llm_request_id) + '">'
      + "<td>" + esc(fmtTime(r.started_at)) + "</td>"
      + "<td>" + esc((r.provider || "-") + "/" + (r.model || "-")) + "</td>"
      + "<td>" + esc(String(r.step || "-")) + "</td>"
      + "<td>" + esc(r.status || "-") + "</td>"
      + '<td><span class="cache-badge ' + statusBadgeClass(r.cache_status) + '">' + esc(statusLabel(r.cache_status)) + "</span></td>"
      + "<td>" + esc(fmtPct(r.cache_hit_ratio)) + "</td>"
      + "<td>" + esc(fmtInt(usage.prompt_tokens)) + "</td>"
      + "<td>" + esc(fmtInt(usage.cache_read_tokens)) + "</td>"
      + "<td>" + esc(fmtInt(usage.cache_creation_tokens)) + "</td>"
      + "</tr>";
  }
  return '<table class="cache-table"><thead><tr>'
    + "<th>时间</th><th>provider/model</th><th>step</th><th>状态</th><th>缓存</th><th>命中率</th><th>prompt</th><th>读缓存</th><th>写缓存</th>"
    + "</tr></thead><tbody>" + rows + "</tbody></table>"
    + '<div class="cache-hint">点击行查看请求详情与消息追溯（trace_id / turn_id / 关联消息）</div>'
    + '<div id="cache-request-detail"></div>';
}

function bindRequestRows(el) {
  var rows = el.querySelectorAll(".cache-row");
  for (var i = 0; i < rows.length; i++) {
    rows[i].addEventListener("click", function (event) {
      var requestId = event.currentTarget.getAttribute("data-request-id");
      showRequestDetail(requestId);
    });
  }
}

function showRequestDetail(requestId) {
  var el = cacheEl("cache-request-detail");
  if (!el || !requestId) { return; }
  el.innerHTML = '<div class="cache-empty">加载详情…</div>';
  cacheAPI("/web/api/cache/requests/" + encodeURIComponent(requestId)).then(function (result) {
    if (result.status !== 200) { renderCacheError(el, result.body && result.body.error); return; }
    el.innerHTML = renderRequestDetail(result.body);
    bindTraceLinks(el);
  }).catch(function () { renderCacheError(el, null); });
}

function renderRequestDetail(record) {
  var usage = record.usage || {};
  var lines = [];
  lines.push(kv("llm_request_id", record.llm_request_id));
  lines.push(kv("trace_id", record.trace_id));
  lines.push(kv("turn_id", record.turn_id));
  lines.push(kv("step", record.step));
  lines.push(kv("状态", record.status + (record.error_category ? " (" + record.error_category + ")" : "")));
  lines.push(kv("缓存状态", statusLabel(record.cache_status)));
  lines.push(kv("cache_epoch", record.cache_epoch));
  lines.push(kv("prompt_cache_key", record.prompt_cache_key));
  lines.push(kv("prompt_fingerprint", record.prompt_fingerprint));
  lines.push(kv("命中率", fmtPct(record.cache_hit_ratio)));
  lines.push(kv("写入率", fmtPct(record.cache_write_ratio)));
  lines.push(kv("prompt tokens", fmtInt(usage.prompt_tokens)));
  lines.push(kv("completion tokens", fmtInt(usage.completion_tokens)));
  lines.push(kv("缓存读取", fmtInt(usage.cache_read_tokens) + (usage.cache_read_reported ? " (已上报)" : " (未上报，未知)")));
  lines.push(kv("缓存写入", fmtInt(usage.cache_creation_tokens)));
  lines.push(kv("reasoning tokens", fmtInt(usage.reasoning_tokens)));
  if (record.user_message_id) {
    lines.push(kv("触发消息", traceLink(record.user_message_id, "user")));
  }
  if (record.assistant_message_id) {
    lines.push(kv("产出消息", traceLink(record.assistant_message_id, "assistant")));
  }
  if (record.correlation_source) {
    lines.push(kv("关联来源", record.correlation_source === "history_inferred" ? "历史推断" : record.correlation_source));
  }
  return '<div class="cache-detail"><div class="cache-detail-title">请求详情</div>' + lines.join("") + "</div>";
}

function kv(label, value) {
  return '<div class="cache-kv"><span class="cache-kv-label">' + esc(String(label)) + '</span><span class="cache-kv-value">' + (value === undefined || value === null || value === "" ? "-" : esc(String(value))) + "</span></div>";
}

function traceLink(messageId, role) {
  return '<a href="#" class="cache-trace-link" data-message-id="' + esc(messageId) + '" data-role="' + esc(role || "") + '">' + esc(messageId) + "</a>";
}

function bindTraceLinks(el) {
  var links = el.querySelectorAll(".cache-trace-link");
  for (var i = 0; i < links.length; i++) {
    links[i].addEventListener("click", function (event) {
      event.preventDefault();
      var messageId = event.currentTarget.getAttribute("data-message-id");
      showMessageTrace(messageId);
    });
  }
}

function showMessageTrace(messageId) {
  var el = cacheEl("cache-request-detail");
  if (!el || !messageId) { return; }
  el.innerHTML = '<div class="cache-empty">加载消息追溯…</div>';
  cacheAPI("/web/api/cache/messages/" + encodeURIComponent(messageId) + "/trace").then(function (result) {
    if (result.status !== 200) { renderCacheError(el, result.body && result.body.error); return; }
    el.innerHTML = renderMessageTrace(result.body);
  }).catch(function () { renderCacheError(el, null); });
}

function renderMessageTrace(trace) {
  var lines = [];
  lines.push(kv("message_id", trace.message_id));
  lines.push(kv("角色", trace.message_role));
  lines.push(kv("turn_id", trace.turn_id));
  if (trace.neighbors && (trace.neighbors.prev_message_id || trace.neighbors.next_message_id)) {
    lines.push(kv("相邻消息", (trace.neighbors.prev_message_id || "-") + " ← → " + (trace.neighbors.next_message_id || "-")));
  }
  if (trace.correlation_source === "history_inferred") {
    lines.push(kv("关联来源", "历史推断（事件流未携带 message_id）"));
  }
  if (trace.produced_by) {
    var produced = trace.produced_by;
    lines.push(kv("产出请求", produced.llm_request_id + " · " + statusLabel(produced.cache_status) + " · 命中率 " + fmtPct(produced.cache_hit_ratio)));
  }
  var consumers = trace.consumed_by || [];
  if (consumers.length) {
    var items = [];
    for (var i = 0; i < consumers.length; i++) {
      items.push(consumers[i].llm_request_id + " (step " + (consumers[i].step || "-") + ", " + statusLabel(consumers[i].cache_status) + ")");
    }
    lines.push(kv("消费请求", items.join("；")));
  }
  return '<div class="cache-detail"><div class="cache-detail-title">消息追溯</div>' + lines.join("") + "</div>";
}
