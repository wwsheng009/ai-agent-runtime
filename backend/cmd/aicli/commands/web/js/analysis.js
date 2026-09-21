// 分析页签（runtime.analytics.v1）：工具 / 子代理 / 失败模式三张表 + 采集健康条。
// 数据源 /web/api/analysis/*，与 runtime-server /api/runtime/analytics/* 同一
// 查询层、同一字段名（HTTP 契约只定义一次）；与 TUI /usage tools|subagents|errors
// 同库同源（usage_analytics.sqlite）。
// aicli micro web client 前端模块(无构建步骤,由 app.js 入口聚合)。

import { esc } from "./util.js";
import { getCurrentSessionID } from "./sessions.js";

var analysisBase = "/web/api/analysis";

var analysisLoaded = false;
// 会话感知（与 cache.js/skills.js 同约定）：数据属于「当前会话」，切换会话后旧
// 快照不再代表当前会话，需按会话 id 判定过期。
var analysisLoadedSessionID = "";
// 统计范围：session（缺省，后端注入当前会话）| all（?scope=all，全局）。
var analysisScope = "session";
// 详情弹层/计分卡使用的最近一次响应快照（null = 本次刷新尚未返回）。
var analysisToolsData = null;
var analysisSubagentsData = null;

// 各区块请求序号：丢弃会话切换 / 手动刷新竞态下迟到的过期响应。
var analysisSeq = 0;
// 自动刷新（页签可见时每 15 秒重拉一次，离开页签即停）。
var analysisAutoEnabled = false;
var analysisAutoTimer = null;
var analysisAutoIntervalMS = 15000;

function analysisEl(id) { return document.getElementById(id); }

function analysisAPI(path) {
  return fetch(path, { cache: "no-store" }).then(function (res) {
    return res.json().then(function (body) {
      return { status: res.status, body: body };
    }, function () {
      return { status: res.status, body: null };
    });
  });
}

// 当前会话 id：由 sessions.js 随会话列表响应同步并导出（唯一来源），
// 本模块不再从 DOM 反查，避免两处状态各说各话。
function analysisCurrentSessionID() {
  return getCurrentSessionID();
}

// analysisQuery 拼装查询串：范围开关 + 额外过滤（均由后端归一/校验）。
function analysisQuery() {
  return analysisScope === "all" ? "?scope=all" : "?scope=session";
}

// ---- 格式化（与 TUI /usage 的降级文案同口径：未上报 → --，不伪造 0） ----

function analysisOrDash(value) {
  if (value === undefined || value === null) { return "--"; }
  var text = String(value);
  return text === "" ? "--" : text;
}

function analysisInt(value) {
  if (value === undefined || value === null) { return "--"; }
  return String(value).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
}

function analysisFailureRate(failures, calls) {
  if (!calls || calls <= 0) { return "--"; }
  return (Math.round(failures / calls * 1000) / 10).toFixed(1) + "%";
}

function analysisDuration(ms) {
  if (ms === undefined || ms === null || ms <= 0) { return "--"; }
  if (ms < 1000) { return ms + "ms"; }
  return (ms / 1000).toFixed(1) + "s";
}

function analysisTime(iso) {
  if (!iso) { return "--"; }
  try {
    var d = new Date(iso);
    if (isNaN(d.getTime())) { return "--"; }
    return d.toLocaleString("zh-CN", { hour12: false });
  } catch (e) { return "--"; }
}

function analysisSubagentOutcome(success) {
  if (success === undefined || success === null) { return "未知"; }
  return success ? "完成" : "失败";
}

// 失败分类：●key count（次数倒序、键升序）；空对象 → 空串（不显示 0）。
function analysisCountMapChips(counts) {
  if (!counts) { return ""; }
  var keys = [];
  for (var key in counts) {
    if (Object.prototype.hasOwnProperty.call(counts, key) && String(key).trim() !== "") {
      keys.push(key);
    }
  }
  if (keys.length === 0) { return ""; }
  keys.sort(function (a, b) {
    if (counts[a] !== counts[b]) { return counts[b] - counts[a]; }
    return a < b ? -1 : (a > b ? 1 : 0);
  });
  var parts = [];
  for (var i = 0; i < keys.length; i++) {
    parts.push('<span class="cache-badge badge-none">●' + esc(keys[i]) + " " + analysisInt(counts[keys[i]]) + "</span>");
  }
  return parts.join(" ");
}

// ---- 页签生命周期 ----

// loadAnalysis 页签激活入口：首次进入、或已渲染数据属于其它会话才拉取。
// 同一会话内重复切页签不重复发请求（页内更新由刷新按钮 / 自动刷新兜底）。
export function loadAnalysis() {
  if (analysisLoaded && analysisLoadedSessionID === analysisCurrentSessionID()) { return; }
  analysisLoaded = true;
  refreshAnalysis();
}

// refreshAnalysis 重拉全部区块（状态 + 工具 + 子代理 + 失败模式）。
export function refreshAnalysis() {
  if (!analysisEl("analysis-tools") && !analysisEl("analysis-subagents") && !analysisEl("analysis-errors")) { return; }
  var seq = ++analysisSeq;
  analysisLoadedSessionID = analysisCurrentSessionID();
  analysisToolsData = null;
  analysisSubagentsData = null;
  renderAnalysisLoading();
  analysisAPI(analysisBase + "/status" + analysisQuery()).then(function (result) {
    if (seq !== analysisSeq) { return; }
    renderAnalysisHealth(result);
  }).catch(function () {
    if (seq !== analysisSeq) { return; }
    renderAnalysisHealth(null);
  });
  analysisAPI(analysisBase + "/tools" + analysisQuery()).then(function (result) {
    if (seq !== analysisSeq) { return; }
    renderAnalysisTools(result);
  }).catch(function () {
    if (seq !== analysisSeq) { return; }
    renderAnalysisTools(null);
  });
  analysisAPI(analysisBase + "/subagents" + analysisQuery()).then(function (result) {
    if (seq !== analysisSeq) { return; }
    renderAnalysisSubagents(result);
  }).catch(function () {
    if (seq !== analysisSeq) { return; }
    renderAnalysisSubagents(null);
  });
  analysisAPI(analysisBase + "/errors" + analysisQuery()).then(function (result) {
    if (seq !== analysisSeq) { return; }
    renderAnalysisErrors(result);
  }).catch(function () {
    if (seq !== analysisSeq) { return; }
    renderAnalysisErrors(null);
  });
  // 工具效率 / Artifact 链路快照（runtime.tool_efficiency 同源；计数器为进程级
  // 聚合，scope 切换不影响该端点，仍随本页竞态序号一起刷新）。
  analysisAPI(analysisBase + "/tool_efficiency" + analysisQuery()).then(function (result) {
    if (seq !== analysisSeq) { return; }
    renderAnalysisEfficiency(result);
  }).catch(function () {
    if (seq !== analysisSeq) { return; }
    renderAnalysisEfficiency(null);
  });
}

// 仅当分析页签当前可见时刷新（后台页签不浪费请求；下次进入按会话不一致重拉）。
export function refreshAnalysisIfActive() {
  var panel = analysisEl("tab-analysis");
  if (!panel || !panel.classList.contains("active")) { return; }
  refreshAnalysis();
}

// stopAnalysisAuto 关闭自动刷新（离开页签时调用；定时器不会在后台页签空转）。
export function stopAnalysisAuto() {
  analysisAutoEnabled = false;
  if (analysisAutoTimer) {
    clearInterval(analysisAutoTimer);
    analysisAutoTimer = null;
  }
  var btn = analysisEl("analysis-auto-btn");
  if (btn) { btn.classList.remove("primary-btn"); }
}

// ---- 状态区（采集健康 + 计分卡） ----

function renderAnalysisLoading() {
  var statusEl = analysisEl("analysis-status");
  if (statusEl) {
    statusEl.className = "cache-capabilities";
    statusEl.textContent = "加载中…";
  }
  var cardsEl = analysisEl("analysis-cards");
  if (cardsEl) { cardsEl.innerHTML = '<div class="cache-empty">加载中…</div>'; }
  var sections = ["analysis-tools", "analysis-subagents", "analysis-errors", "analysis-efficiency"];
  for (var i = 0; i < sections.length; i++) {
    var el = analysisEl(sections[i]);
    if (el) { el.innerHTML = '<div class="cache-empty">加载中…</div>'; }
  }
}

// ---- 工具效率 / Artifact 链路（runtime.tool_efficiency 同源快照，F-5） ----

// analysisCountMapValues 计数 map → 稳定排序条目（次数倒序、键升序，同 chips 口径）。
function analysisCountMapValues(counts) {
  var entries = [];
  if (counts) {
    for (var key in counts) {
      if (Object.prototype.hasOwnProperty.call(counts, key)) {
        entries.push([key, Number(counts[key]) || 0]);
      }
    }
  }
  entries.sort(function (a, b) {
    if (a[1] !== b[1]) { return b[1] - a[1]; }
    return a[0] < b[0] ? -1 : (a[0] > b[0] ? 1 : 0);
  });
  return entries;
}

// analysisFlowCard 单块卡片：total + 明细 chips；无任何数据 → 不伪造 0。
function analysisFlowCard(title, total, details) {
  var chips = analysisCountMapChips(details);
  var html = '<div class="cache-card"><div class="cache-card-value">' +
    (total > 0 ? esc(analysisInt(total)) : "--") + '</div><div class="cache-card-label">' +
    esc(title) + "</div>";
  if (chips) {
    html += '<div class="cache-dist" style="margin-top:4px">' + chips + "</div>";
  }
  return html + "</div>";
}

function analysisPercent(value, total) {
  if (!total || total <= 0 || value === undefined || value === null) { return "--"; }
  return (Math.round(value / total * 1000) / 10).toFixed(1) + "%";
}

// analysisRateLabel 比率字段（0~1 的 rate，如 success_rate）：total<=0 时为
// 「无样本」，绝不把缺省 0 伪造成 0.0%。
function analysisRateLabel(rate, sampleTotal) {
  if (rate === undefined || rate === null || !sampleTotal || sampleTotal <= 0) { return "--"; }
  return analysisPercent(rate, 1);
}

// renderAnalysisEfficiency 渲染 tool_efficiency 快照（降级矩阵与 /usage TUI、
// frontend ArtifactFlowPanel 同口径：缺 captured_at 或 artifact_flow → 快照无效，
// 整块「不可用」；全零 → 「暂无数据」；inefficiency_flags / gap 比例 → 警告条）。
function renderAnalysisEfficiency(result) {
  var el = analysisEl("analysis-efficiency");
  if (!el) { return; }
  var body = result && result.status === 200 && result.body ? result.body : null;
  if (!body || !body.captured_at || !body.artifact_flow) {
    el.innerHTML = analysisUnavailableHTML(result);
    return;
  }
  var flow = body.artifact_flow;
  var archives = flow.archives || {};
  var truncations = flow.truncations || {};
  var deref = flow.deref || {};
  var total = (archives.total || 0) + (truncations.total || 0) +
    (deref.total || 0) + flowCountMapTotal(flow.pointer_notice);
  if (total <= 0) {
    el.innerHTML = '<div class="cache-empty">暂无数据</div>';
    return;
  }
  var html = '<div class="cache-detail-title" style="margin-top:10px">工具效率 / Artifact 链路' +
    "（快照 " + esc(analysisTime(body.captured_at)) + "）</div>";
  html += '<div class="cache-cards">' +
    analysisFlowCard("Artifact 归档", archives.total, archives.by_layer) +
    analysisFlowCard("输出截断", truncations.total, truncations.by_truncated_by) +
    analysisFlowCard("指针提示", flowCountMapTotal(flow.pointer_notice), flow.pointer_notice) +
    analysisFlowCard("Deref 读取", deref.total, deref.miss_by_reason) +
    "</div>";
  // 低效信号：inefficiency_flags 非空 / L1-L4 gap 比例 ≥ 0.5（与 frontend 阈值一致）。
  var flags = body.inefficiency_flags || [];
  var gapRatio = Number(flow.l1_l4_gap_ratio);
  var gapWarning = !isNaN(gapRatio) && gapRatio >= 0.5;
  if (flags.length > 0 || gapWarning) {
    html += '<div class="cache-dist">低效信号：' +
      (flags.length > 0 ? esc(flags.join("、")) : "") +
      (gapWarning ? " L1/L4 截断比例 " + (Math.round(gapRatio * 1000) / 10).toFixed(1) + "%" : "") +
      "</div>";
  }
  // 派生率：preflight / outcomes / deref followup（未上报 → --，不伪造 0）。
  var outcomes = body.outcomes || {};
  var preflight = body.preflight || {};
  html += '<div class="cache-dist">成功率 ' + analysisRateLabel(outcomes.success_rate, outcomes.total) +
    " · 非失败率 " + analysisRateLabel(outcomes.non_fail_rate, outcomes.total) +
    " · Preflight 决策 " + analysisChipsOrDash(analysisCountMapChips(preflight.by_decision)) +
    " · Deref followup " + (deref.total > 0 ? analysisPercent(deref.followup_ratio, 1) : "--") + "</div>";
  el.innerHTML = html;
}

function flowCountMapTotal(counts) {
  var total = 0;
  var entries = analysisCountMapValues(counts);
  for (var i = 0; i < entries.length; i++) { total += entries[i][1]; }
  return total;
}

function analysisChipsOrDash(chips) {
  return chips ? chips : "--";
}

function showAnalysisHealth(el, text) {
  if (!el) { return; }
  el.textContent = text;
  el.style.display = "block";
}

function hideAnalysisHealth(el) {
  if (!el) { return; }
  el.textContent = "";
  el.style.display = "none";
}

function analysisStatusLabel(body) {
  var scope = analysisScope === "all" ? "全局" : "当前会话";
  var state = body.degraded ? "degraded" : (body.attached ? "attached" : "disabled");
  var total = body.ingested_total ? analysisInt(body.ingested_total) : "0";
  var last = body.last_ingest_at ? analysisTime(body.last_ingest_at) : "暂无数据";
  return "范围：" + scope + " · " + state + " · 已入库 " + total + " · 最近写入 " + last;
}

// renderAnalysisHealth 渲染采集健康（§9.1 降级：attached=false → 顶部健康条）。
// 服务缺失（analytics_disabled）不是页面错误：健康条 + 各区块「不可用」文案。
function renderAnalysisHealth(result) {
  var statusEl = analysisEl("analysis-status");
  var healthEl = analysisEl("analysis-health");
  var body = result && result.body ? result.body : null;
  if (!result || result.status !== 200 || !body) {
    var code = body && body.error && body.error.code ? body.error.code : (result ? "http_" + result.status : "network_error");
    if (statusEl) {
      statusEl.className = "cache-capabilities cache-disabled";
      statusEl.textContent = "分析数据不可用（" + code + "）";
    }
    showAnalysisHealth(healthEl, "采集健康：attached=false（分析服务未挂载，" + code + "）——本页数据不可用，请确认 aicli 已启用 usage_analytics.sqlite 采集。");
    return;
  }
  if (statusEl) {
    statusEl.className = "cache-capabilities" + (body.attached && !body.degraded ? "" : " cache-disabled");
    statusEl.textContent = analysisStatusLabel(body);
  }
  if (!body.attached) {
    showAnalysisHealth(healthEl, "采集健康：attached=false（分析服务未挂载）——本页数据不可用" +
      (body.db_path ? "；目标库 " + body.db_path : "") + "。");
    return;
  }
  if (body.degraded) {
    showAnalysisHealth(healthEl, "采集健康：degraded=true（分析库不可查询 / 只读降级），数据可能不完整" +
      (body.db_path ? "；目标库 " + body.db_path : "") + "。");
    return;
  }
  hideAnalysisHealth(healthEl);
}

function analysisCard(value, label) {
  return '<div class="cache-card"><div class="cache-card-value">' + esc(value) + '</div><div class="cache-card-label">' + esc(label) + "</div></div>";
}

function analysisSuccessRate(summary) {
  if (!summary || !summary.total || summary.total <= 0) { return "--"; }
  return (Math.round(summary.succeeded / summary.total * 1000) / 10).toFixed(1) + "%";
}

// renderAnalysisCards 汇总卡片行：工具（调用/失败/失败率）+ 子代理（完成率/失败/超时）
// + 失败分类 / 来源分布；任一侧未返回时只展示已到达的一侧。
function renderAnalysisCards() {
  var el = analysisEl("analysis-cards");
  if (!el) { return; }
  if (analysisToolsData === null && analysisSubagentsData === null) {
    el.innerHTML = '<div class="cache-empty">加载中…</div>';
    return;
  }
  var cards = [];
  if (analysisToolsData) {
    var totals = analysisToolsData.totals || {};
    var toolCount = (analysisToolsData.tools || []).length;
    cards.push(analysisCard(analysisInt(toolCount),
      "工具（调用 " + analysisInt(totals.calls || 0) + " · 失败 " + analysisInt(totals.failures || 0) +
      " · 失败率 " + analysisFailureRate(totals.failures || 0, totals.calls || 0) +
      " · 平均耗时 " + analysisDuration(totals.average_duration_ms) +
      "（最小 " + analysisDuration(totals.min_duration_ms) + " / 最大 " + analysisDuration(totals.max_duration_ms) + "））"));
  }
  if (analysisSubagentsData) {
    var summary = analysisSubagentsData.summary || {};
    cards.push(analysisCard(analysisInt(summary.total || 0),
      "子代理（完成率 " + analysisSuccessRate(summary) + " · 失败 " + analysisInt(summary.failed || 0) +
      " · 超时 " + analysisInt(summary.timeouts || 0) + "）"));
  }
  var html = '<div class="cache-cards">' + cards.join("") + "</div>";
  if (analysisSubagentsData) {
    var summaryRow = analysisSubagentsData.summary || {};
    var categories = analysisCountMapChips(summaryRow.failure_categories);
    if (categories) { html += '<div class="cache-dist">失败分类：' + categories + "</div>"; }
    var sources = analysisCountMapChips(summaryRow.sources);
    if (sources) { html += '<div class="cache-dist">来源：' + sources + "</div>"; }
  }
  el.innerHTML = html;
}

// ---- 数据区块 ----

function analysisResponseError(result) {
  if (!result) { return "network_error"; }
  if (result.body && result.body.error && result.body.error.code) { return result.body.error.code; }
  return "http_" + result.status;
}

function analysisUnavailableHTML(result) {
  return '<div class="cache-empty">分析数据不可用（' + esc(analysisResponseError(result)) + "）</div>";
}

function renderAnalysisTools(result) {
  var el = analysisEl("analysis-tools");
  if (!el) { return; }
  if (!result || result.status !== 200 || !result.body) {
    el.innerHTML = analysisUnavailableHTML(result);
    return;
  }
  analysisToolsData = result.body;
  var tools = result.body.tools || [];
  if (tools.length === 0) {
    el.innerHTML = '<div class="cache-empty">暂无数据</div>';
    renderAnalysisCards();
    return;
  }
  var rows = [];
  for (var i = 0; i < tools.length; i++) {
    var stat = tools[i];
    rows.push('<tr class="cache-row" data-analysis-tool="' + esc(stat.tool_name) + '">' +
      "<td>" + esc(analysisOrDash(stat.tool_name)) + "</td>" +
      "<td>" + analysisInt(stat.calls) + "</td>" +
      "<td>" + analysisInt(stat.failures) + "</td>" +
      "<td>" + analysisFailureRate(stat.failures, stat.calls) + "</td>" +
      "<td>" + analysisInt(stat.empty_results) + "</td>" +
      "<td>" + analysisInt(stat.retried_calls) + "</td>" +
      "<td>" + analysisDuration(stat.average_duration_ms) + "</td>" +
      "<td>" + analysisDuration(stat.min_duration_ms) + "</td>" +
      "<td>" + analysisDuration(stat.max_duration_ms) + "</td>" +
      "<td>" + analysisDuration(stat.p50_duration_ms) + "</td>" +
      "<td>" + analysisDuration(stat.p95_duration_ms) + "</td></tr>");
  }
  el.innerHTML = '<div style="overflow-x:auto">' +
    '<table class="cache-table"><thead><tr>' +
    "<th>工具</th><th>调用</th><th>失败</th><th>失败率</th><th>空结果</th><th>重试</th>" +
    "<th>平均</th><th>最小</th><th>最大</th><th>p50</th><th>p95</th>" +
    "</tr></thead><tbody>" + rows.join("") + "</tbody></table></div>";
  renderAnalysisCards();
}

function analysisSubagentLabel(stat) {
  var role = (stat.role || "").trim();
  if (role) { return role; }
  return analysisOrDash(stat.subagent_id);
}

function renderAnalysisSubagents(result) {
  var el = analysisEl("analysis-subagents");
  if (!el) { return; }
  if (!result || result.status !== 200 || !result.body) {
    el.innerHTML = analysisUnavailableHTML(result);
    return;
  }
  analysisSubagentsData = result.body;
  var subagents = result.body.subagents || [];
  if (subagents.length === 0) {
    el.innerHTML = '<div class="cache-empty">暂无数据</div>';
    renderAnalysisCards();
    return;
  }
  var rows = [];
  for (var i = 0; i < subagents.length; i++) {
    var stat = subagents[i];
    rows.push('<tr class="cache-row" data-analysis-subagent-index="' + i + '">' +
      "<td>" + esc(analysisSubagentLabel(stat)) + "</td>" +
      "<td>" + esc(analysisOrDash(stat.source)) + "</td>" +
      "<td>" + esc(analysisSubagentOutcome(stat.success)) + "</td>" +
      "<td>" + esc(analysisOrDash(stat.failure_category || stat.error_code)) + "</td>" +
      "<td>" + esc(analysisAttemptLabel(stat)) + "</td>" +
      "<td>" + analysisInt(stat.usage_total_tokens) + "</td>" +
      "<td>" + analysisDuration(stat.duration_ms) + "</td></tr>");
  }
  el.innerHTML = '<table class="cache-table"><thead><tr>' +
    "<th>子代理</th><th>来源</th><th>状态</th><th>失败分类</th><th>重试</th><th>token</th><th>耗时</th>" +
    "</tr></thead><tbody>" + rows.join("") + "</tbody></table>";
  renderAnalysisCards();
}

// analysisAttemptLabel 重试次数（attempt/max_attempts）；未上报 → --。
function analysisAttemptLabel(stat) {
  var attempt = stat.attempt || 0;
  var maxAttempts = stat.max_attempts || 0;
  if (attempt <= 0 && maxAttempts <= 0) { return "--"; }
  if (maxAttempts <= 0) { return String(attempt); }
  return attempt + "/" + maxAttempts;
}

function renderAnalysisErrors(result) {
  var el = analysisEl("analysis-errors");
  if (!el) { return; }
  if (!result || result.status !== 200 || !result.body) {
    el.innerHTML = analysisUnavailableHTML(result);
    return;
  }
  var patterns = result.body.patterns || [];
  if (patterns.length === 0) {
    el.innerHTML = '<div class="cache-empty">暂无数据</div>';
    return;
  }
  var rows = [];
  for (var i = 0; i < patterns.length; i++) {
    var pattern = patterns[i];
    rows.push("<tr>" +
      "<td>" + esc(analysisOrDash(pattern.source)) + "</td>" +
      "<td>" + esc(analysisOrDash(pattern.error_code)) + "</td>" +
      "<td>" + esc(analysisOrDash(pattern.failure_category)) + "</td>" +
      "<td>" + analysisInt(pattern.count) + "</td></tr>");
  }
  el.innerHTML = '<div class="cache-detail-title" style="margin-top:10px">失败模式 Top</div>' +
    '<table class="cache-table"><thead><tr>' +
    "<th>来源</th><th>错误码</th><th>失败分类</th><th>次数</th>" +
    "</tr></thead><tbody>" + rows.join("") + "</tbody></table>";
}

// ---- 行下钻弹层 ----

function analysisKV(label, value) {
  return '<div class="cache-kv"><span class="cache-kv-label">' + esc(label) +
    '</span><span class="cache-kv-value">' + esc(value) + "</span></div>";
}

function analysisPatternTable(patterns) {
  if (!patterns || patterns.length === 0) {
    return '<div class="cache-empty">暂无数据</div>';
  }
  var rows = [];
  for (var i = 0; i < patterns.length; i++) {
    var pattern = patterns[i];
    rows.push("<tr>" +
      "<td>" + esc(analysisOrDash(pattern.error_code)) + "</td>" +
      "<td>" + esc(analysisOrDash(pattern.failure_category)) + "</td>" +
      "<td>" + analysisInt(pattern.count) + "</td></tr>");
  }
  return '<table class="cache-table"><thead><tr><th>错误码</th><th>失败分类</th><th>次数</th></tr></thead><tbody>' +
    rows.join("") + "</tbody></table>";
}

export function openAnalysisDetail(title, html) {
  var overlay = analysisEl("analysis-detail-overlay");
  var titleEl = analysisEl("analysis-detail-title");
  var bodyEl = analysisEl("analysis-detail-body");
  if (!overlay || !bodyEl) { return; }
  if (titleEl) {
    titleEl.textContent = title;
    titleEl.title = title;
  }
  bodyEl.innerHTML = html;
  overlay.style.display = "flex";
}

export function closeAnalysisDetail() {
  var overlay = analysisEl("analysis-detail-overlay");
  if (overlay) { overlay.style.display = "none"; }
}

function openAnalysisToolDetail(stat) {
  if (!stat) { return; }
  var html = '<div class="cache-detail-title">工具明细</div>' +
    analysisKV("工具", analysisOrDash(stat.tool_name)) +
    analysisKV("调用 / 失败", analysisInt(stat.calls) + " / " + analysisInt(stat.failures) + "（失败率 " + analysisFailureRate(stat.failures, stat.calls) + "）") +
    analysisKV("空结果 / 重试", analysisInt(stat.empty_results) + " / " + analysisInt(stat.retried_calls)) +
    analysisKV("平均 / 最小 / 最大", analysisDuration(stat.average_duration_ms) + " / " + analysisDuration(stat.min_duration_ms) + " / " + analysisDuration(stat.max_duration_ms)) +
    analysisKV("p50 / p95", analysisDuration(stat.p50_duration_ms) + " / " + analysisDuration(stat.p95_duration_ms)) +
    '<div class="cache-detail-title" style="margin-top:10px">错误 Top</div>' +
    analysisPatternTable(stat.error_top);
  openAnalysisDetail("工具：" + analysisOrDash(stat.tool_name), html);
}

function openAnalysisSubagentDetail(stat) {
  if (!stat) { return; }
  var html = '<div class="cache-detail-title">子代理明细</div>' +
    analysisKV("子代理", analysisOrDash(stat.subagent_id)) +
    analysisKV("角色 / 来源", analysisOrDash(stat.role) + " / " + analysisOrDash(stat.source)) +
    analysisKV("状态", analysisSubagentOutcome(stat.success) + "（" + analysisOrDash(stat.completion_reason) + "）") +
    analysisKV("失败分类 / 错误码", analysisOrDash(stat.failure_category) + " / " + analysisOrDash(stat.error_code)) +
    analysisKV("重试", analysisAttemptLabel(stat) + (stat.retry_reason ? "（" + stat.retry_reason + "）" : "")) +
    analysisKV("耗时 / token", analysisDuration(stat.duration_ms) + " / " + analysisInt(stat.usage_total_tokens)) +
    analysisKV("父会话 / 子会话", analysisOrDash(stat.parent_session_id) + " / " + analysisOrDash(stat.child_session_id)) +
    analysisKV("完成时间", analysisTime(stat.completed_at)) +
    analysisKV("冲突计数", analysisInt(stat.conflict_count));
  openAnalysisDetail("子代理：" + analysisSubagentLabel(stat), html);
}

// 事件委托：工具表按工具名回查快照；子代理表按行下标回查快照。
function analysisClosestAttr(node, attr) {
  while (node && node !== document) {
    if (node.getAttribute) {
      var value = node.getAttribute(attr);
      if (value !== null && value !== undefined) { return value; }
    }
    node = node.parentNode;
  }
  return null;
}

function handleAnalysisRowClick(ev) {
  var toolName = analysisClosestAttr(ev.target, "data-analysis-tool");
  if (toolName !== null) {
    var tools = analysisToolsData && analysisToolsData.tools ? analysisToolsData.tools : [];
    for (var i = 0; i < tools.length; i++) {
      if (tools[i].tool_name === toolName) { openAnalysisToolDetail(tools[i]); return; }
    }
    return;
  }
  var index = analysisClosestAttr(ev.target, "data-analysis-subagent-index");
  if (index !== null) {
    var subagents = analysisSubagentsData && analysisSubagentsData.subagents ? analysisSubagentsData.subagents : [];
    var parsed = parseInt(index, 10);
    if (!isNaN(parsed) && parsed >= 0 && parsed < subagents.length) { openAnalysisSubagentDetail(subagents[parsed]); }
  }
}

// ---- 初始化（app.js 入口调用；页签切换分支在 ui.js activateTab） ----

function updateAnalysisScopeButton() {
  var btn = analysisEl("analysis-scope-btn");
  if (btn) { btn.textContent = analysisScope === "all" ? "范围：全局" : "范围：当前会话"; }
}

function toggleAnalysisScope() {
  analysisScope = analysisScope === "all" ? "session" : "all";
  updateAnalysisScopeButton();
  refreshAnalysis();
}

function analysisAutoTick() {
  var panel = analysisEl("tab-analysis");
  if (!analysisAutoEnabled || !panel || !panel.classList.contains("active")) {
    stopAnalysisAuto();
    return;
  }
  refreshAnalysis();
}

export function toggleAnalysisAuto() {
  if (analysisAutoEnabled) { stopAnalysisAuto(); return; }
  analysisAutoEnabled = true;
  var btn = analysisEl("analysis-auto-btn");
  if (btn) { btn.classList.add("primary-btn"); }
  analysisAutoTimer = setInterval(analysisAutoTick, analysisAutoIntervalMS);
}

export function initAnalysis() {
  var refreshBtn = analysisEl("analysis-refresh-btn");
  if (refreshBtn) { refreshBtn.addEventListener("click", function () { refreshAnalysis(); }); }
  var scopeBtn = analysisEl("analysis-scope-btn");
  if (scopeBtn) { scopeBtn.addEventListener("click", function () { toggleAnalysisScope(); }); }
  var autoBtn = analysisEl("analysis-auto-btn");
  if (autoBtn) { autoBtn.addEventListener("click", function () { toggleAnalysisAuto(); }); }
  var closeBtn = analysisEl("analysis-detail-close");
  if (closeBtn) { closeBtn.addEventListener("click", function () { closeAnalysisDetail(); }); }
  var overlay = analysisEl("analysis-detail-overlay");
  if (overlay) {
    overlay.addEventListener("click", function (ev) {
      if (ev.target === overlay) { closeAnalysisDetail(); }
    });
  }
  document.addEventListener("keydown", function (ev) {
    if (ev.key !== "Escape") { return; }
    closeAnalysisDetail();
  });
  // 行下钻：工具表 / 子代理表共用同一委托处理器（详情来自最近一次快照，不再发请求）。
  var toolsEl = analysisEl("analysis-tools");
  if (toolsEl) { toolsEl.addEventListener("click", handleAnalysisRowClick); }
  var subagentsEl = analysisEl("analysis-subagents");
  if (subagentsEl) { subagentsEl.addEventListener("click", handleAnalysisRowClick); }
  updateAnalysisScopeButton();
}
