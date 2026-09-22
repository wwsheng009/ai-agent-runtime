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

// 路由观测快照（stats + events 分页状态）。
var analysisRoutingData = null;
var analysisRoutingEvents = [];
var analysisRoutingEventsTotal = 0;
var analysisRoutingEventsOffset = 0;
var analysisRoutingEventsLoading = false;

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

// analysisText 归一展示文本：null/undefined/非字符串安全 → 去首尾空白字符串。
function analysisText(value) {
  if (value === undefined || value === null) { return ""; }
  return String(value).trim();
}

// analysisClip 截断展示文本（超出长度补省略号；完整值由 title 承载）。
function analysisClip(text, max) {
  var value = analysisText(text);
  return value.length > max ? value.substring(0, max) + "…" : value;
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
  if (!analysisEl("analysis-tools") && !analysisEl("analysis-subagents") && !analysisEl("analysis-errors") && !analysisEl("analysis-routing")) { return; }
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
  // 路由观测：stats + events 首页（offset=0，每次刷新重置分页状态）。
  analysisRoutingEvents = [];
  analysisRoutingEventsOffset = 0;
  analysisRoutingEventsTotal = 0;
  analysisRoutingData = null;
  analysisAPI(analysisBase + "/routing" + analysisQuery()).then(function (result) {
    if (seq !== analysisSeq) { return; }
    renderAnalysisRouting(result);
  }).catch(function () {
    if (seq !== analysisSeq) { return; }
    renderAnalysisRouting(null);
  });
  analysisRoutingEventsLoading = true;
  analysisAPI(analysisBase + "/routing/events" + analysisQuery() + "&limit=50").then(function (result) {
    if (seq !== analysisSeq) { return; }
    analysisRoutingEventsLoading = false;
    var body = result && result.status === 200 && result.body ? result.body : null;
    analysisRoutingEvents = (body && body.events) ? body.events : [];
    analysisRoutingEventsTotal = (body && body.count) ? body.count : analysisRoutingEvents.length;
    analysisRoutingEventsOffset = analysisRoutingEvents.length;
    renderAnalysisRoutingEvents(false);
  }).catch(function () {
    if (seq !== analysisSeq) { return; }
    analysisRoutingEventsLoading = false;
    renderAnalysisRoutingEvents(false);
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
  var sections = ["analysis-tools", "analysis-subagents", "analysis-errors", "analysis-efficiency", "analysis-routing", "analysis-routing-events"];
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
  // task_type 优先、role 兜底（W-5）：路由层 role 已由 task_type 取代，旧库/旧事件
  // 仍只有 role；两者都缺才退回 subagent_id。
  var taskType = analysisText(stat.task_type);
  if (taskType) { return taskType; }
  var role = analysisText(stat.role);
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

// ---- 路由观测区块 ----

// 路由维度标签（对齐 frontend i18n，微 web 客户端无 i18n 框架，直接中文映射）。
var routingScopeLabels = { main_agent: "主 Agent", subagent: "子代理" };
var routingKindLabels = { applied: "改道", cleared: "还原", warning: "告警" };
var routingSourceLabels = {
  parent_inherit: "父级继承", fallback: "降级回退", role_override: "角色覆盖",
  difficulty_level: "难度路由", explicit: "显式声明", explicit_promoted: "显式提升",
  heuristic: "启发式", default: "默认", inferred: "推断",
  // task_type 轴新增来源（W-2）；role_override 保留为历史值。
  task_type_override: "任务类型覆盖", task_type_floor: "任务类型抬档",
  task_type_downgrade: "任务类型降档",
};
var routingFlagLabels = { routeChanged: "路由已变", routeUnchanged: "路由未变", fallbackUsed: "降级已用", fallbackUnused: "降级未用" };

// 告警词前缀归一（W-6）：只改写 task_type 三个新 token 前缀，其余 token
// （如历史 difficulty_promoted_by_keyword:*）原样透出，渲染口径不变。
var routingWarningPrefixLabels = [
  { prefix: "difficulty_floor_by_task_type:", label: "任务类型抬档：" },
  { prefix: "difficulty_downgraded_by_task_type:", label: "任务类型降档：" },
  { prefix: "task_type_unknown:", label: "未知任务类型：" },
];

function routingWarningLabel(raw) {
  var token = analysisText(raw);
  for (var i = 0; i < routingWarningPrefixLabels.length; i++) {
    var entry = routingWarningPrefixLabels[i];
    if (token.indexOf(entry.prefix) === 0) {
      var suffix = token.substring(entry.prefix.length).trim();
      // 前缀后无取值（退化 token）时不做半截改写，原样透出。
      return suffix ? entry.label + suffix : token;
    }
  }
  return routingLabel({}, token);
}

function routingLabel(map, raw) {
  var key = (raw || "").trim().toLowerCase();
  return map[key] || (raw || "").trim() || "未记录";
}

function triStateLabel(value, trueLabel, falseLabel) {
  if (value === true) { return trueLabel; }
  if (value === false) { return falseLabel; }
  return "未记录";
}

// renderAnalysisRouting 渲染路由总览：指标卡 + 10 维分布桶。
function renderAnalysisRouting(result) {
  var el = analysisEl("analysis-routing");
  if (!el) { return; }
  if (!result || result.status !== 200 || !result.body) {
    analysisRoutingData = null;
    el.innerHTML = analysisUnavailableHTML(result);
    return;
  }
  analysisRoutingData = result.body;
  var totals = result.body.totals || {};
  var html = '<div class="cache-detail-title" style="margin-top:10px">路由切换观测</div>';
  // 指标卡
  html += '<div class="cache-cards">' +
    analysisCard(analysisInt(totals.total), "总事件（改道 " + analysisInt(totals.applied) + " · 还原 " + analysisInt(totals.cleared) + "）") +
    analysisCard(analysisInt(totals.main_agent), "主 Agent（候选 " + analysisInt(totals.candidate_total) + "）") +
    analysisCard(analysisInt(totals.subagent), "子代理（会话 " + analysisInt(totals.distinct_sessions) + "）") +
    analysisCard(analysisInt(totals.route_changed), "路由变更（模型 " + analysisInt(totals.distinct_models) + "）") +
    analysisCard(analysisInt(totals.fallback_used), "降级使用") +
    analysisCard(analysisInt(totals.warnings), "告警") +
    "</div>";
  // 分布桶
  var bucketGroups = [
    { title: "范围", buckets: result.body.by_scope, label: function (k) { return routingLabel(routingScopeLabels, k); } },
    { title: "类型", buckets: result.body.by_kind, label: function (k) { return routingLabel(routingKindLabels, k); } },
    { title: "原因", buckets: result.body.by_reason, label: function (k) { return routingLabel(routingSourceLabels, k); } },
    { title: "来源", buckets: result.body.by_source, label: function (k) { return routingLabel(routingSourceLabels, k); } },
    { title: "难度", buckets: result.body.by_difficulty, label: function (k) { return routingLabel({}, k); } },
    { title: "难度来源", buckets: result.body.by_difficulty_source, label: function (k) { return routingLabel(routingSourceLabels, k); } },
    // 任务类型轴（W-1）：字段缺失（旧服务端）时与既有卡片一致渲染「暂无数据」；
    // by_role 保留一个 release，暂不删除。
    { title: "任务类型", buckets: result.body.by_task_type, label: function (k) { return routingLabel({}, k); } },
    { title: "角色", buckets: result.body.by_role, label: function (k) { return routingLabel({}, k); } },
    { title: "Provider", buckets: result.body.by_provider, label: function (k) { return routingLabel({}, k); } },
    { title: "Model", buckets: result.body.by_model, label: function (k) { return routingLabel({}, k); } },
    { title: "告警词", buckets: result.body.warnings, label: function (k) { return routingWarningLabel(k); } },
  ];
  html += '<div style="display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:8px;margin-top:8px">';
  for (var g = 0; g < bucketGroups.length; g++) {
    html += renderRoutingBucketGroup(bucketGroups[g]);
  }
  html += "</div>";
  el.innerHTML = html;
}

function renderRoutingBucketGroup(group) {
  var buckets = group.buckets || [];
  if (buckets.length === 0) {
    return '<div class="cache-card" style="min-height:60px"><div class="cache-card-label">' +
      esc(group.title) + '</div><div class="cache-empty" style="font-size:11px">暂无数据</div></div>';
  }
  var max = 0;
  for (var i = 0; i < buckets.length; i++) {
    if (buckets[i].count > max) { max = buckets[i].count; }
  }
  var html = '<div class="cache-card" style="min-height:60px"><div class="cache-card-label" style="margin-bottom:4px">' +
    esc(group.title) + "</div>";
  for (var j = 0; j < buckets.length; j++) {
    var b = buckets[j];
    var label = group.label(b.key);
    var pct = max > 0 ? Math.max(4, Math.round(b.count / max * 100)) : 0;
    html += '<div style="display:flex;align-items:center;gap:4px;margin:2px 0;font-size:11px">' +
      '<span style="min-width:0;flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="' +
      esc(label) + '">' + esc(label) + "</span>" +
      '<span style="flex-shrink:0;color:var(--fg3)">' + analysisInt(b.count) + "</span></div>" +
      '<div style="height:3px;border-radius:2px;background:var(--bg3);margin-bottom:2px">' +
      '<div style="height:100%;border-radius:2px;background:var(--accent2);width:' + pct + '%"></div></div>';
  }
  return html + "</div>";
}

// renderAnalysisRoutingEvents 渲染路由明细表（首次 + 加载更多追加）。
function renderAnalysisRoutingEvents(append) {
  var el = analysisEl("analysis-routing-events");
  if (!el) { return; }
  if (analysisRoutingEventsLoading && !append) {
    el.innerHTML = '<div class="cache-empty">加载中…</div>';
    return;
  }
  if (analysisRoutingEvents.length === 0) {
    el.innerHTML = '<div class="cache-detail-title" style="margin-top:10px">路由事件明细</div>' +
      '<div class="cache-empty">暂无数据</div>';
    return;
  }
  var html = '<div class="cache-detail-title" style="margin-top:10px">路由事件明细' +
    '（' + analysisInt(analysisRoutingEvents.length) + " / " + analysisInt(analysisRoutingEventsTotal) + "）</div>";
  html += '<div style="overflow-x:auto"><table class="cache-table"><thead><tr>' +
    "<th>时间</th><th>范围</th><th>类型</th><th>Agent</th><th>目标</th>" +
    "<th>难度 / 任务类型</th><th>路由</th><th>标记</th><th>告警</th><th>尝试</th>" +
    "</tr></thead><tbody>";
  for (var i = 0; i < analysisRoutingEvents.length; i++) {
    html += renderRoutingEventRow(analysisRoutingEvents[i]);
  }
  html += "</tbody></table></div>";
  if (analysisRoutingEvents.length < analysisRoutingEventsTotal) {
    html += '<div style="text-align:center;margin-top:8px">' +
      '<button type="button" id="analysis-routing-load-more" style="padding:4px 14px;font-size:12px"' +
      ' title="加载更多路由事件">加载更多</button></div>';
  }
  el.innerHTML = html;
  // 绑定加载更多按钮。
  var loadMoreBtn = analysisEl("analysis-routing-load-more");
  if (loadMoreBtn) {
    loadMoreBtn.addEventListener("click", function () { loadMoreRoutingEvents(); });
  }
}

function renderRoutingEventRow(ev) {
  var scope = routingLabel(routingScopeLabels, ev.scope);
  var kind = routingLabel(routingKindLabels, ev.kind);
  var kindStyle = ev.kind === "applied"
    ? 'style="color:var(--green)"'
    : (ev.kind === "warning" ? 'style="color:var(--yellow)"' : "");
  var agent = ev.agent_id ? ev.agent_id.substring(0, 8) : "-";
  var goal = (ev.goal || "-").substring(0, 32);
  var diffSource = ev.difficulty_source ? "（" + routingLabel(routingSourceLabels, ev.difficulty_source) + "）" : "";
  // 任务类型与难度同格并列（W-3）：缺字段（旧库/主 Agent 行）不占位；task_subject
  // 有值时以次行小字 + 单元格 title 展示完整值。
  var taskType = analysisText(ev.task_type);
  var taskSubject = analysisText(ev.task_subject);
  var difficultyTitle = [];
  if (taskType) { difficultyTitle.push("任务类型：" + taskType); }
  if (taskSubject) { difficultyTitle.push("任务主题：" + taskSubject); }
  var difficultyCell = "<td" +
    (difficultyTitle.length > 0 ? ' title="' + esc(difficultyTitle.join(" · ")) + '"' : "") + ">" +
    esc(ev.difficulty || "未记录") + esc(diffSource) + (taskType ? " · " + esc(taskType) : "") +
    (taskSubject ? '<div style="font-size:10px;color:var(--fg3)">' + esc(analysisClip(taskSubject, 24)) + "</div>" : "") +
    "</td>";
  var route = [ev.provider, ev.model].filter(Boolean).join(" / ") || "未记录";
  var routeChanged = triStateLabel(ev.route_changed, routingFlagLabels.routeChanged, routingFlagLabels.routeUnchanged);
  var fallback = triStateLabel(ev.fallback_used, routingFlagLabels.fallbackUsed, routingFlagLabels.fallbackUnused);
  var warnings = (ev.warnings || []);
  var warnText = warnings.length > 0 ? warnings.length + " 条" : "-";
  var attempt = ev.attempt || 0;
  var maxAtt = ev.max_attempts || 0;
  var attemptLabel = (maxAtt > 1) ? attempt + "/" + maxAtt : String(attempt);
  var time = analysisTime(ev.recorded_at);
  return '<tr class="cache-row">' +
    "<td>" + esc(time) + "</td>" +
    "<td>" + esc(scope) + "</td>" +
    "<td><span " + kindStyle + ">" + esc(kind) + "</span></td>" +
    '<td style="font-family:monospace;font-size:11px">' + esc(agent) + "</td>" +
    '<td style="max-width:160px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="' +
    esc(ev.goal || "") + '">' + esc(goal) + "</td>" +
    difficultyCell +
    '<td style="font-size:11px">' + esc(route) + "</td>" +
    "<td>" + esc(routeChanged) + " · " + esc(fallback) + "</td>" +
    '<td style="color:var(--yellow)">' + esc(warnText) + "</td>" +
    "<td>" + esc(attemptLabel) + "</td></tr>";
}

function loadMoreRoutingEvents() {
  if (analysisRoutingEventsLoading) { return; }
  analysisRoutingEventsLoading = true;
  var btn = analysisEl("analysis-routing-load-more");
  if (btn) { btn.textContent = "加载中…"; btn.disabled = true; }
  analysisAPI(analysisBase + "/routing/events" + analysisQuery() +
    "&limit=50&offset=" + analysisRoutingEventsOffset).then(function (result) {
    analysisRoutingEventsLoading = false;
    var body = result && result.status === 200 && result.body ? result.body : null;
    var rows = (body && body.events) ? body.events : [];
    for (var i = 0; i < rows.length; i++) {
      analysisRoutingEvents.push(rows[i]);
    }
    analysisRoutingEventsOffset += rows.length;
    renderAnalysisRoutingEvents(true);
  }).catch(function () {
    analysisRoutingEventsLoading = false;
    var loadMoreBtn = analysisEl("analysis-routing-load-more");
    if (loadMoreBtn) { loadMoreBtn.textContent = "加载失败，点击重试"; loadMoreBtn.disabled = false; }
  });
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
  var taskType = analysisText(stat.task_type);
  var taskSubject = analysisText(stat.task_subject);
  var html = '<div class="cache-detail-title">子代理明细</div>' +
    analysisKV("子代理", analysisOrDash(stat.subagent_id)) +
    analysisKV("角色 / 来源", analysisOrDash(stat.role) + " / " + analysisOrDash(stat.source)) +
    analysisKV("任务类型", analysisOrDash(taskType) + (taskSubject ? "（" + taskSubject + "）" : "")) +
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
