// 缓存分析页签（cache.analytics.v1）:会话总览 + 请求明细 + 消息追溯。
// 数据源 /web/api/cache/*（与 TUI /usage 共用同一后端 Source）。
// aicli micro web client 前端模块(无构建步骤,由 app.js 入口聚合)。

import { esc } from "./util.js";

var cacheLoaded = false;
var cacheAvailable = null; // null=未探测, true/false
var cacheRequestsCache = [];
var CACHE_REQUEST_PAGE_SIZES = [20, 50, 100, 200];
var cacheRequestPage = 1;
var cacheRequestPageSize = 50;
var cacheRequestTotal = 0;
var cacheRequestCacheStatus = "";
var cacheRequestHitMinRatio = null;
var cacheRequestHitMaxRatio = null;
var cacheRequestProvider = "";
var cacheRequestModel = "";
var cacheRequestStatus = "";
var cacheRequestFrom = "";
var cacheRequestTo = "";
var cacheRequestDurationMinMS = null;
var cacheRequestDurationMaxMS = null;
var cacheFiltersBound = false;
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

// shortCacheID 短 id 展示（超长截断）；完整 id 由调用方放 title 属性。
function shortCacheID(id) {
  if (!id) { return "-"; }
  return id.length > 12 ? id.slice(0, 8) + "…" : id;
}

// isChildSessionRequest 判断请求是否来自子代理子会话（schema v9 父链字段）。
function isChildSessionRequest(record) {
  return !!(record && record.session_id && record.root_session_id && record.root_session_id !== record.session_id);
}

function fmtPct(ratio) {
  if (ratio === undefined || ratio === null) { return "-"; }
  return (Math.round(ratio * 1000) / 10).toFixed(1) + "%";
}

// fmtMillis 毫秒展示（>=1s 折算为秒，保留 1 位小数）。
function fmtMillis(ms) {
  if (ms >= 1000) { return (Math.round(ms / 100) / 10).toFixed(1) + " s"; }
  return ms + " ms";
}

// fmtLatency 单请求延迟展示：0/缺省 = 未采集（非流式请求、历史记录或首个
// 增量前就失败）。总耗时不可冒充首字时间，必须显示"未采集"而不是 0ms。
function fmtLatency(ms) {
  if (ms === undefined || ms === null || ms <= 0) { return "未采集"; }
  return fmtMillis(ms);
}

// fmtAverageLatency 会话级均值展示：样本数为 0 时均值不可用（缺省 0 不摊平）。
function fmtAverageLatency(avg, samples) {
  if (!samples || avg === undefined || avg === null || avg <= 0) { return "未采集"; }
  return fmtMillis(avg) + " · " + fmtInt(samples) + " 次";
}

// uncachedInputTokens 未缓存输入（输入总量中未命中缓存、真机处理的部分）：
// 后端新记录直接给 uncached_input_tokens；旧记录/旧库该字段缺失或为 0 时按
// 可用字段降级推导——
//   - 未上报缓存（cache_read_reported=false）时输入全部视为未缓存；
//   - cache_read > prompt 说明是不含式口径（Anthropic 的 input 不含 cache read），
//     此时输入本身即未缓存；
//   - 其余按包含式口径（OpenAI/DeepSeek/Gemini）做 prompt - cache_read。
// 旧记录里不含式口径且 cache_read < prompt 的极少数场景会低估（仅历史数据）。
function uncachedInputTokens(usage, cacheReportedOverride) {
  if (!usage) { return null; }
  if (usage.uncached_input_tokens !== undefined && usage.uncached_input_tokens !== null && usage.uncached_input_tokens > 0) {
    return usage.uncached_input_tokens;
  }
  var prompt = usage.prompt_tokens || 0;
  if (prompt <= 0) { return null; }
  var reported = cacheReportedOverride !== undefined ? cacheReportedOverride : !!usage.cache_read_reported;
  if (!reported) { return prompt; }
  var cached = usage.cache_read_tokens || 0;
  if (cached > prompt) { return prompt; }
  return Math.max(0, prompt - cached);
}

function fmtTime(iso) {
  if (!iso) { return "-"; }
  try {
    var d = new Date(iso);
    if (isNaN(d.getTime())) { return "-"; }
    // 使用 Date 的本地 getter，确保表格与 datetime-local 筛选使用同一本地时区。
    return String(d.getFullYear())
      + "-" + padNumber(d.getMonth() + 1, 2)
      + "-" + padNumber(d.getDate(), 2)
      + " " + padNumber(d.getHours(), 2)
      + ":" + padNumber(d.getMinutes(), 2)
      + ":" + padNumber(d.getSeconds(), 2)
      + "." + padNumber(d.getMilliseconds(), 3);
  } catch (e) { return "-"; }
}

function padNumber(value, width) {
  return String(value).padStart(width, "0");
}

// cache_status 徽标样式映射（hit=绿 write=蓝 reported_zero=黄 not_reported=灰 error=红）。
// 文案语义：reported_zero=provider 明确上报 cached_tokens=0（真未命中）；
// not_reported=provider 未回传缓存字段，命中状态未知（不能断言未命中）。
function statusBadgeClass(status) {
  switch (status) {
    case "hit": return "badge-hit";
    case "write": return "badge-write";
    case "confirmed_miss":
    case "reported_zero": return "badge-zero";
    case "error": return "badge-error";
    default: return "badge-none";
  }
}

function statusLabel(status) {
  switch (status) {
    case "hit": return "命中";
    case "write": return "写入";
    case "confirmed_miss": return "确认未命中";
    case "reported_zero": return "未命中";
    case "not_reported": return "未上报（未知）";
    case "error": return "错误";
    default: return status || "-";
  }
}

export function loadCacheAnalytics(force) {
  // 首次进入、强制刷新，或已渲染数据属于其他会话（切换会话发生在页签后台时
  // 由 syncCacheSession 记录）才拉取；同一会话内重复切页签不重复发请求。
  var sessionChanged = cacheLoadedSessionID !== currentSessionID;
  if (cacheLoaded && !force && !sessionChanged) { return; }
  if (!cacheLoaded || sessionChanged) {
    resetCacheRequestPagination();
    resetCacheDimensionFilters();
  }
  ensureCacheFiltersBound();
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
  if (!cachePanelActive()) { return; }
  refreshCacheAnalytics();
}

function cachePanelActive() {
  var panel = cacheEl("tab-cache");
  return !!(panel && panel.classList.contains("active"));
}

function resetCacheRequestPagination() {
  cacheRequestPage = 1;
  cacheRequestTotal = 0;
  cacheRequestsCache = [];
}

// provider/model 选项来自会话级 overview，切换会话时不能沿用上一会话的选择。
function resetCacheDimensionFilters() {
  cacheRequestProvider = "";
  cacheRequestModel = "";
  var providerEl = cacheEl("cache-filter-provider");
  var modelEl = cacheEl("cache-filter-model");
  if (providerEl) {
    providerEl.innerHTML = '<option value="">全部 provider</option>';
    providerEl.value = "";
  }
  if (modelEl) {
    modelEl.innerHTML = '<option value="">全部 model</option>';
    modelEl.value = "";
  }
}

function ensureCacheFiltersBound() {
  if (cacheFiltersBound) { return; }
  var apply = cacheEl("cache-filter-apply");
  var reset = cacheEl("cache-filter-reset");
  if (!apply || !reset) { return; }
  bindCacheFilterTimeZone();
  apply.addEventListener("click", applyCacheFilters);
  reset.addEventListener("click", resetCacheFilters);
  var inputs = [
    cacheEl("cache-filter-provider"),
    cacheEl("cache-filter-model"),
    cacheEl("cache-filter-request-status"),
    cacheEl("cache-filter-status"),
    cacheEl("cache-filter-hit-min"),
    cacheEl("cache-filter-hit-max"),
    cacheEl("cache-filter-from"),
    cacheEl("cache-filter-to"),
    cacheEl("cache-filter-duration-min"),
    cacheEl("cache-filter-duration-max"),
  ];
  for (var i = 0; i < inputs.length; i++) {
    if (!inputs[i]) { continue; }
    inputs[i].addEventListener("keydown", function (event) {
      if (event.key !== "Enter") { return; }
      event.preventDefault();
      applyCacheFilters();
    });
  }
  cacheFiltersBound = true;
}

function bindCacheFilterTimeZone() {
  var el = cacheEl("cache-filter-timezone");
  if (!el) { return; }
  var zone = "";
  try {
    zone = Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  } catch (e) {
    zone = "";
  }
  el.textContent = zone ? "(" + zone + ")" : "（浏览器本地时间）";
  el.title = "表格时间和筛选时间均按浏览器本地时区显示";
}

function applyCacheFilters() {
  var providerEl = cacheEl("cache-filter-provider");
  var modelEl = cacheEl("cache-filter-model");
  var requestStatusEl = cacheEl("cache-filter-request-status");
  var statusEl = cacheEl("cache-filter-status");
  var minEl = cacheEl("cache-filter-hit-min");
  var maxEl = cacheEl("cache-filter-hit-max");
  var fromEl = cacheEl("cache-filter-from");
  var toEl = cacheEl("cache-filter-to");
  var durationMinEl = cacheEl("cache-filter-duration-min");
  var durationMaxEl = cacheEl("cache-filter-duration-max");
  var min = parseCacheRatioPercent(minEl && minEl.value);
  var max = parseCacheRatioPercent(maxEl && maxEl.value);
  var from = parseCacheDateTime(fromEl && fromEl.value);
  var to = parseCacheDateTime(toEl && toEl.value);
  var durationMin = parseCacheDurationMS(durationMinEl && durationMinEl.value);
  var durationMax = parseCacheDurationMS(durationMaxEl && durationMaxEl.value);
  if (!min.ok || !max.ok) {
    setCacheFilterError("缓存率需填写 0–100 之间的百分比");
    return;
  }
  if (min.value !== null && max.value !== null && min.value > max.value) {
    setCacheFilterError("最低缓存率不能高于最高缓存率");
    return;
  }
  if (!from.ok || !to.ok) {
    setCacheFilterError("时间范围格式无效");
    return;
  }
  if (from.time !== null && to.time !== null && from.time > to.time) {
    setCacheFilterError("开始时间不能晚于结束时间");
    return;
  }
  if (!durationMin.ok || !durationMax.ok) {
    setCacheFilterError("耗时需填写非负整数毫秒");
    return;
  }
  if (durationMin.value !== null && durationMax.value !== null && durationMin.value > durationMax.value) {
    setCacheFilterError("最低耗时不能高于最高耗时");
    return;
  }
  cacheRequestProvider = providerEl ? String(providerEl.value || "").trim() : "";
  cacheRequestModel = modelEl ? String(modelEl.value || "").trim() : "";
  cacheRequestStatus = requestStatusEl ? String(requestStatusEl.value || "") : "";
  cacheRequestCacheStatus = statusEl ? String(statusEl.value || "") : "";
  cacheRequestHitMinRatio = min.value;
  cacheRequestHitMaxRatio = max.value;
  cacheRequestFrom = from.value;
  cacheRequestTo = to.value;
  cacheRequestDurationMinMS = durationMin.value;
  cacheRequestDurationMaxMS = durationMax.value;
  cacheRequestPage = 1;
  setCacheFilterError("");
  refreshCacheRequests();
}

function resetCacheFilters() {
  var ids = [
    "cache-filter-provider", "cache-filter-model", "cache-filter-request-status",
    "cache-filter-status", "cache-filter-hit-min", "cache-filter-hit-max",
    "cache-filter-from", "cache-filter-to", "cache-filter-duration-min", "cache-filter-duration-max",
  ];
  for (var i = 0; i < ids.length; i++) {
    var el = cacheEl(ids[i]);
    if (el) { el.value = ""; }
  }
  cacheRequestCacheStatus = "";
  cacheRequestHitMinRatio = null;
  cacheRequestHitMaxRatio = null;
  cacheRequestProvider = "";
  cacheRequestModel = "";
  cacheRequestStatus = "";
  cacheRequestFrom = "";
  cacheRequestTo = "";
  cacheRequestDurationMinMS = null;
  cacheRequestDurationMaxMS = null;
  cacheRequestPage = 1;
  setCacheFilterError("");
  refreshCacheRequests();
}

function parseCacheRatioPercent(raw) {
  var text = String(raw === undefined || raw === null ? "" : raw).trim();
  if (text === "") { return { ok: true, value: null }; }
  var percent = Number(text);
  if (!isFinite(percent) || percent < 0 || percent > 100) {
    return { ok: false, value: null };
  }
  return { ok: true, value: percent / 100 };
}

function parseCacheDateTime(raw) {
  var text = String(raw === undefined || raw === null ? "" : raw).trim();
  if (text === "") { return { ok: true, value: "", time: null }; }
  // datetime-local 没有时区后缀；new Date(text) 按浏览器本地时区解析。
  var parsed = new Date(text);
  if (isNaN(parsed.getTime())) { return { ok: false, value: "", time: null }; }
  return { ok: true, value: parsed.toISOString(), time: parsed.getTime() };
}

function parseCacheDurationMS(raw) {
  var text = String(raw === undefined || raw === null ? "" : raw).trim();
  if (text === "") { return { ok: true, value: null }; }
  if (!/^\d+$/.test(text)) { return { ok: false, value: null }; }
  var value = Number(text);
  if (!Number.isSafeInteger(value) || value < 0) { return { ok: false, value: null }; }
  return { ok: true, value: value };
}

function setCacheFilterError(message) {
  var el = cacheEl("cache-filter-error");
  if (el) { el.textContent = message || ""; }
}

function hasActiveCacheFilters() {
  return cacheRequestCacheStatus !== ""
    || cacheRequestHitMinRatio !== null
    || cacheRequestHitMaxRatio !== null
    || cacheRequestProvider !== ""
    || cacheRequestModel !== ""
    || cacheRequestStatus !== ""
    || cacheRequestFrom !== ""
    || cacheRequestTo !== ""
    || cacheRequestDurationMinMS !== null
    || cacheRequestDurationMaxMS !== null;
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
  if (cachePanelActive()) {
    resetCacheRequestPagination();
    resetCacheDimensionFilters();
    refreshCacheAnalytics();
  }
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
    syncCacheDimensionOptions(result.body || {});
    el.innerHTML = renderOverviewCards(result.body);
  }).catch(function () {
    if (seq !== overviewSeq) { return; }
    renderCacheError(el, null);
  });
}

// syncCacheDimensionOptions 使用 overview 返回的会话级去重维度填充下拉框。
// 当前选择若在新会话中不存在则清空，避免界面保留一个无法匹配的旧值。
function syncCacheDimensionOptions(overview) {
  cacheRequestProvider = syncCacheDimensionSelect(
    cacheEl("cache-filter-provider"),
    overview.providers,
    cacheRequestProvider,
    "全部 provider"
  );
  cacheRequestModel = syncCacheDimensionSelect(
    cacheEl("cache-filter-model"),
    overview.models,
    cacheRequestModel,
    "全部 model"
  );
}

function syncCacheDimensionSelect(el, values, current, allLabel) {
  if (!el || !Array.isArray(values)) { return current; }
  var selected = String(current || "");
  var selectedExists = selected === "";
  var html = '<option value="">' + esc(allLabel) + "</option>";
  for (var i = 0; i < values.length; i++) {
    var value = String(values[i] || "").trim();
    if (!value) { continue; }
    if (value === selected) { selectedExists = true; }
    html += '<option value="' + esc(value) + '">' + esc(value) + "</option>";
  }
  el.innerHTML = html;
  if (!selectedExists) {
    el.value = "";
    return "";
  }
  el.value = selected;
  return selected;
}

function renderOverviewCards(overview) {
  var dist = overview.cache_status_distribution || {};
  var tokens = overview.tokens || {};
  var coverage = overview.coverage || {};
  var cards = [];
  cards.push(card("请求总数", fmtInt(overview.requests_total)));
  cards.push(card("子会话请求", fmtInt(overview.child_requests_total)));
  cards.push(card("缓存命中率", fmtPct(overview.cache_hit_ratio)));
  cards.push(card("缓存写入率", fmtPct(overview.cache_write_ratio)));
  cards.push(card("缓存读取 tokens", fmtInt(tokens.cache_read_tokens)));
  cards.push(card("缓存写入 tokens", fmtInt(tokens.cache_creation_tokens)));
  cards.push(card("prompt tokens", fmtInt(tokens.prompt_tokens)));
  cards.push(card("未缓存输入 tokens", fmtInt(uncachedInputTokens(tokens, (overview.requests_cache_reported || 0) > 0))));
  cards.push(card("输出 tokens", fmtInt(tokens.completion_tokens)));
  cards.push(card("合计 tokens", fmtInt(tokens.total_tokens)));
  cards.push(card("推理 tokens", fmtInt(tokens.reasoning_tokens)));
  cards.push(card("平均耗时", fmtAverageLatency(overview.average_duration_ms, overview.duration_samples)));
  cards.push(card("平均首字（TTFT）", fmtAverageLatency(overview.average_first_token_ms, overview.first_token_samples)));
  var distParts = [];
  distParts.push("命中 " + fmtInt(dist.hit));
  distParts.push("写入 " + fmtInt(dist.write));
  distParts.push("确认未命中 " + fmtInt(dist.confirmed_miss));
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
  var page = cacheRequestPage;
  var pageSize = cacheRequestPageSize;
  var offset = (page - 1) * pageSize;
  el.innerHTML = '<div class="cache-empty">加载中…</div>';
  cacheAPI(cacheRequestsURL(pageSize, offset)).then(function (result) {
    if (seq !== requestsSeq) { return; } // 过期响应：期间已发出更新的拉取（如会话切换）
    if (result.status !== 200) { renderCacheError(el, result.body && result.body.error); return; }
    var body = result.body || {};
    var total = Number(body.total) || 0;
    var totalPages = Math.max(1, Math.ceil(total / pageSize));
    if (page > totalPages) {
      // 数据可能在翻页期间减少：夹到最后一个有效页后重查，避免空白页。
      cacheRequestPage = totalPages;
      if (seq === requestsSeq) { refreshCacheRequests(); }
      return;
    }
    cacheRequestTotal = total;
    cacheRequestsCache = body.requests || [];
    el.innerHTML = renderRequestsTable(cacheRequestsCache, body);
    bindRequestRows(el);
    bindRequestPagination(el);
  }).catch(function () {
    if (seq !== requestsSeq) { return; }
    renderCacheError(el, null);
  });
}

function cacheRequestsURL(pageSize, offset) {
  var url = "/web/api/cache/requests?limit=" + encodeURIComponent(pageSize)
    + "&offset=" + encodeURIComponent(offset);
  if (cacheRequestProvider) {
    url += "&provider=" + encodeURIComponent(cacheRequestProvider);
  }
  if (cacheRequestModel) {
    url += "&model=" + encodeURIComponent(cacheRequestModel);
  }
  if (cacheRequestStatus) {
    url += "&status=" + encodeURIComponent(cacheRequestStatus);
  }
  if (cacheRequestCacheStatus) {
    url += "&cache_status=" + encodeURIComponent(cacheRequestCacheStatus);
  }
  if (cacheRequestHitMinRatio !== null) {
    url += "&cache_hit_ratio_min=" + encodeURIComponent(cacheRequestHitMinRatio);
  }
  if (cacheRequestHitMaxRatio !== null) {
    url += "&cache_hit_ratio_max=" + encodeURIComponent(cacheRequestHitMaxRatio);
  }
  if (cacheRequestFrom) {
    url += "&from=" + encodeURIComponent(cacheRequestFrom);
  }
  if (cacheRequestTo) {
    url += "&to=" + encodeURIComponent(cacheRequestTo);
  }
  if (cacheRequestDurationMinMS !== null) {
    url += "&duration_ms_min=" + encodeURIComponent(cacheRequestDurationMinMS);
  }
  if (cacheRequestDurationMaxMS !== null) {
    url += "&duration_ms_max=" + encodeURIComponent(cacheRequestDurationMaxMS);
  }
  return url;
}

function renderRequestsTable(requests, result) {
  if (!requests.length) {
    var empty = hasActiveCacheFilters()
      ? "没有符合筛选条件的请求（可调整筛选条件或重置）"
      : "暂无 LLM 请求记录（发送一条消息后刷新）";
    return '<div class="cache-empty">' + esc(empty) + "</div>";
  }
  var rows = "";
  for (var i = 0; i < requests.length; i++) {
    var r = requests[i];
    var usage = r.usage || {};
    rows += '<tr class="cache-row" data-request-id="' + esc(r.llm_request_id) + '">'
      + "<td>" + esc(fmtTime(r.started_at)) + "</td>"
      + "<td>" + esc((r.provider || "-") + "/" + (r.model || "-")) + "</td>"
      + "<td>" + esc(String(r.step || "-")) + "</td>"
      + "<td>" + renderChildSessionCell(r) + "</td>"
      + "<td>" + esc(r.status || "-") + "</td>"
      + '<td><span class="cache-badge ' + statusBadgeClass(r.cache_status) + '">' + esc(statusLabel(r.cache_status)) + "</span></td>"
      + "<td>" + esc(fmtPct(r.cache_hit_ratio)) + "</td>"
      + "<td>" + esc(fmtLatency(r.duration_ms)) + "</td>"
      + "<td>" + esc(fmtLatency(r.first_token_ms)) + "</td>"
      + "<td>" + esc(fmtInt(usage.prompt_tokens)) + "</td>"
      + "<td>" + esc(fmtInt(uncachedInputTokens(usage))) + "</td>"
      + "<td>" + esc(fmtInt(usage.completion_tokens)) + "</td>"
      + "<td>" + esc(fmtInt(usage.cache_read_tokens)) + "</td>"
      + "<td>" + esc(fmtInt(usage.cache_creation_tokens)) + "</td>"
      + "</tr>";
  }
  return '<table class="cache-table"><thead><tr>'
    + "<th>时间（本地）</th><th>provider/model</th><th>step</th><th>子会话</th><th>状态</th><th>缓存</th><th>命中率</th><th>耗时</th><th>首字</th><th>prompt</th><th>未缓存输入</th><th>输出</th><th>读缓存</th><th>写缓存</th>"
    + "</tr></thead><tbody>" + rows + "</tbody></table>"
    + renderRequestPagination(result)
    + '<div class="cache-hint">点击行查看请求详情与消息追溯（trace_id / turn_id / 关联消息）</div>'
    + '<div class="cache-hint">「子会话」列标注该请求由子代理（subagent_spawn / subagent_spawn_batch）产生，鼠标悬停可看完整子会话与子代理 ID。</div>'
    + '<div class="cache-hint">首字/耗时显示"未采集"表示该请求没有观测到（非流式请求、历史记录或首个增量前失败），不代表 0ms。</div>'
    + '<div id="cache-request-detail"></div>';
}

// renderRequestPagination 请求明细分页条：total 来自后端 COUNT(*)，
// offset 始终由当前页与每页条数计算，避免前端另存一份可能漂移的游标。
function renderRequestPagination(result) {
  var total = Math.max(0, Number(result && result.total) || 0);
  if (total <= 0) { return ""; }
  var totalPages = Math.max(1, Math.ceil(total / cacheRequestPageSize));
  var page = Math.min(Math.max(cacheRequestPage, 1), totalPages);
  var from = (page - 1) * cacheRequestPageSize + 1;
  var to = Math.min(page * cacheRequestPageSize, total);
  var options = "";
  for (var i = 0; i < CACHE_REQUEST_PAGE_SIZES.length; i++) {
    var size = CACHE_REQUEST_PAGE_SIZES[i];
    options += '<option value="' + size + '"' + (size === cacheRequestPageSize ? " selected" : "") + ">" + size + "</option>";
  }
  return '<div class="cache-pagination" role="navigation" aria-label="请求明细分页">'
    + '<button type="button" data-cache-page="prev"' + (page <= 1 ? " disabled" : "") + ">‹ 上一页</button>"
    + '<span class="cache-page-status" aria-live="polite">第 ' + page + " / " + totalPages
    + " 页 · 共 " + fmtInt(total) + " 条（" + fmtInt(from) + "–" + fmtInt(to) + "）</span>"
    + '<button type="button" data-cache-page="next"' + (page >= totalPages ? " disabled" : "") + ">下一页 ›</button>"
    + '<label class="cache-page-size">每页 <select data-cache-page-size>' + options + "</select></label>"
    + "</div>";
}

function bindRequestPagination(el) {
  var prev = el.querySelector('[data-cache-page="prev"]');
  if (prev) {
    prev.addEventListener("click", function () { goCacheRequestPage(cacheRequestPage - 1); });
  }
  var next = el.querySelector('[data-cache-page="next"]');
  if (next) {
    next.addEventListener("click", function () { goCacheRequestPage(cacheRequestPage + 1); });
  }
  var sizeSelect = el.querySelector("[data-cache-page-size]");
  if (sizeSelect) {
    sizeSelect.addEventListener("change", function () {
      var nextSize = parseInt(sizeSelect.value, 10);
      if (CACHE_REQUEST_PAGE_SIZES.indexOf(nextSize) < 0) { return; }
      cacheRequestPageSize = nextSize;
      cacheRequestPage = 1;
      refreshCacheRequests();
    });
  }
}

function goCacheRequestPage(page) {
  var totalPages = Math.max(1, Math.ceil(cacheRequestTotal / cacheRequestPageSize));
  var next = Math.min(Math.max(page, 1), totalPages);
  if (next === cacheRequestPage) { return; }
  cacheRequestPage = next;
  refreshCacheRequests();
}

// renderChildSessionCell 子会话列：子代理请求显示徽标 + 短 id（title 给全 id）。
function renderChildSessionCell(record) {
  if (!isChildSessionRequest(record)) {
    return '<span class="cache-child-none">-</span>';
  }
  var title = record.session_id + (record.subagent_id ? " · " + record.subagent_id : "");
  return '<span class="cache-badge badge-write">子代理</span> '
    + '<span class="cache-child-id" title="' + esc(title) + '">' + esc(shortCacheID(record.session_id)) + "</span>";
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
  if (isChildSessionRequest(record)) {
    lines.push(kv("子会话", record.session_id));
    lines.push(kv("父会话", record.parent_session_id));
    lines.push(kv("根会话", record.root_session_id));
    if (record.subagent_id) {
      lines.push(kv("子代理", record.subagent_id));
    }
  }
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
  lines.push(kv("总耗时", fmtLatency(record.duration_ms)));
  lines.push(kv("首字时间（TTFT）", fmtLatency(record.first_token_ms)));
  lines.push(kv("prompt tokens", fmtInt(usage.prompt_tokens)));
  lines.push(kv("未缓存输入 tokens", fmtInt(uncachedInputTokens(usage))));
  lines.push(kv("completion tokens", fmtInt(usage.completion_tokens)));
  lines.push(kv("total tokens", fmtInt(usage.total_tokens)));
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
    lines.push(kv("产出用量", usageSummary(produced.usage)));
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

// usageSummary 单条请求用量的紧凑摘要（追溯的产出请求用；字段与请求详情一致）。
function usageSummary(usage) {
  if (!usage) { return "-"; }
  return "prompt " + fmtInt(usage.prompt_tokens)
    + " · 未缓存输入 " + fmtInt(uncachedInputTokens(usage))
    + " · 输出 " + fmtInt(usage.completion_tokens)
    + " · 读缓存 " + fmtInt(usage.cache_read_tokens)
    + " · 写缓存 " + fmtInt(usage.cache_creation_tokens);
}
