// LSP 观测页签（lsp.observe.v1）：池状态 + 使用读数 + 最近事件 + 优化建议。
// 数据源 /web/api/lsp/*，与 TUI /lsp status 同源（当前会话的 LSP 池）。
// 诚实降级：事件埋点未接入时 available=false，页面显示「待埋点」；绝不把
// 未采集渲染成 0（方案 docs/plan/lsp-observability-and-analysis-plan-20260929.md §5.4）。
// aicli micro web client 前端模块(无构建步骤,由 app.js 入口聚合)。

import { esc } from "./util.js";
import { getCurrentSessionID } from "./sessions.js";

var lspBase = "/web/api/lsp";

// 事件埋点未接入时后端返回的稳定原因码（lsp_instrumentation_pending）：
// 前端显式识别并按「待埋点」渲染，不做数值判断、不伪造 0。
var LSP_PENDING_REASON = "lsp_instrumentation_pending";

// 会话感知（与 cache.js/analysis.js 同约定）：数据属于「当前会话」，切换会话后
// 旧快照不再代表当前会话，需按会话 id 判定过期；同会话重复切页签不重复拉取。
var lspLoaded = false;
var lspLoadedSessionID = "";
// 请求序号：丢弃会话切换 / 手动刷新竞态下迟到的过期响应。
var lspSeq = 0;
// 自动刷新（页签可见时每 15 秒重拉一次，离开页签即停）。
var lspAutoEnabled = false;
var lspAutoTimer = null;
var lspAutoIntervalMS = 15000;

var LSP_STATE_LABELS = {
  starting: "启动中",
  ready: "就绪",
  unavailable: "不可用",
  crashed: "已崩溃",
  stopped: "已停止"
};

// 结果枚举与 lsp.classifyOutcome 一一对应（未知值原样展示，不猜测）。
var LSP_OUTCOME_LABELS = {
  injected: "已注入",
  clean: "无问题",
  no_server: "无 server 覆盖",
  degraded_no_fresh: "无新诊断/等待超时",
  degraded_read_error: "读文件失败",
  degraded: "降级"
};

function lspEl(id) { return document.getElementById(id); }

function lspAPI(path) {
  return fetch(path, { cache: "no-store" }).then(function (res) {
    return res.json().then(function (body) {
      return { status: res.status, body: body };
    }, function () {
      return { status: res.status, body: null };
    });
  });
}

// 当前会话 id：由 sessions.js 随会话列表响应同步并导出（唯一来源）。
function lspCurrentSessionID() {
  return getCurrentSessionID();
}

// ---- 格式化（与 TUI/analysis 同口径：未上报 → --，不伪造 0） ----

function lspNum(value) {
  if (value === undefined || value === null) { return "--"; }
  return String(value).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
}

function lspPercent(value) {
  if (value === undefined || value === null || isNaN(value)) { return "--"; }
  return (Math.round(Number(value) * 1000) / 10).toFixed(1) + "%";
}

function lspMS(value) {
  if (value === undefined || value === null || Number(value) <= 0) { return "--"; }
  return lspNum(Math.round(Number(value))) + " ms";
}

function lspText(value) { return value === undefined || value === null ? "" : String(value).trim(); }

function lspClip(text, max) {
  var value = lspText(text);
  return value.length > max ? value.substring(0, max) + "…" : value;
}

function lspStateLabel(state) { return LSP_STATE_LABELS[state] || (state || "未知"); }

function lspOutcomeLabel(outcome) { return LSP_OUTCOME_LABELS[outcome] || (outcome || "--"); }

function lspStatusMessage(text, tone) {
  var el = lspEl("lsp-status-line");
  if (!el) { return; }
  el.textContent = text;
  el.style.color = tone === "error" ? "var(--dangerFg, #c62828)" : (tone === "warn" ? "var(--warnFg, #b8860b)" : "");
}

// ---- 渲染 ----

function lspCard(value, label) {
  return '<div class="cache-card"><div class="cache-card-value">' + esc(value) +
    '</div><div class="cache-card-label">' + esc(label) + "</div></div>";
}

function lspCards(cards) { return '<div class="cache-cards">' + cards.join("") + "</div>"; }

// lspSuggestions 从已采集事实推导优化建议（不依赖未标定阈值）：
// 池状态 + 真实读数（available=true 时）都可作为判定证据。
function lspSuggestions(status, overview) {
  status = status || {};
  var servers = status.servers || [];
  var tips = [];
  var missing = [];
  var crashed = [];
  var notReady = 0;
  for (var i = 0; i < servers.length; i++) {
    var s = servers[i] || {};
    var reason = String(s.reason || "");
    if (s.state === "unavailable" && /not found|no such file|找不到|cannot find/i.test(reason)) {
      missing.push(lspText(s.name) || "(未命名)");
    }
    if (s.state === "crashed") { crashed.push(lspText(s.name) || "(未命名)"); }
    if (s.state === "starting") { notReady++; }
  }
  if (!status.enabled) {
    tips.push("LSP 未启用：编辑工具不会追加诊断（编辑本身不受影响）。可在工作区 .aicli/runtime.yaml 的 lsp 段开启。");
  }
  if (missing.length > 0) {
    tips.push("检测到 server 二进制缺失：" + missing.join("、") + "。安装后可消除 degraded_no_server 降级。");
  }
  if (crashed.length > 0) {
    tips.push("server 已崩溃：" + crashed.join("、") + "。可在 TUI 执行 /lsp restart 重启；反复崩溃请对照原因列排查。");
  }
  if (notReady > 0) {
    tips.push("有 " + notReady + " 个 server 仍在启动中：首次编辑等待时间可能偏高，可稍后刷新或开启 lsp.prewarm。");
  }
  var m = (overview && overview.metrics) || null;
  if (m) {
    if ((m.no_server || 0) > 0) {
      tips.push("累计 " + lspNum(m.no_server) + " 次请求没有 server 覆盖（no_server）：检查工作区根与 server 类型映射。");
    }
    if ((m.degraded || 0) > 0) {
      tips.push("累计 " + lspNum(m.degraded) + " 次降级（fallback_ratio " + lspPercent(m.fallback_ratio) +
        "）：对照「最近事件」结果列定位缺二进制 / 等待超时 / 读文件失败。");
    }
    if ((m.latency_samples || 0) > 0 && (m.wait_latency_p95_ms || 0) > 0) {
      tips.push("等待 P95 = " + lspMS(m.wait_latency_p95_ms) + "（样本 " + lspNum(m.latency_samples) +
        "）：显著偏高时优先调整 diagnostics.wait_ms / scope，再考虑 prewarm。");
    }
  }
  return tips;
}

function lspServersSection(status) {
  var html = '<div class="cache-detail-title" style="margin-top:10px">池状态</div>';
  if (!status.enabled) {
    return html + '<div class="cache-empty">LSP 未启用（当前会话未挂载语言服务器池）。</div>';
  }
  var servers = status.servers || [];
  if (servers.length === 0) {
    return html + '<div class="cache-empty">没有配置任何已启用的 server。</div>';
  }
  var rows = [];
  for (var i = 0; i < servers.length; i++) {
    var s = servers[i] || {};
    rows.push("<tr><td>" + esc(lspText(s.name) || "(未命名)") +
      "</td><td>" + esc(lspStateLabel(s.state)) +
      "</td><td>" + (s.pid ? esc(String(s.pid)) : "--") +
      "</td><td>" + esc(lspText(s.position_encoding) || "--") +
      "</td><td>" + esc(lspClip(s.root, 60) || "--") +
      "</td><td>" + esc(lspClip(s.reason, 80) || "--") + "</td></tr>");
  }
  return html + '<div style="overflow-x:auto"><table class="cache-table"><thead><tr>' +
    "<th>Server</th><th>状态</th><th>PID</th><th>编码</th><th>工作区</th><th>原因</th>" +
    "</tr></thead><tbody>" + rows.join("") + "</tbody></table></div>";
}

function lspOverviewSection(overview) {
  overview = overview || {};
  var byState = overview.servers_by_state || {};
  var html = '<div class="cache-detail-title" style="margin-top:10px">使用读数</div>';
  html += lspCards([
    lspCard(lspNum(overview.servers_total), "Server 总数"),
    lspCard(lspNum(byState.ready), "就绪"),
    lspCard(lspNum(byState.unavailable), "不可用"),
    lspCard(lspNum(byState.crashed), "崩溃")
  ]);
  if (!overview.available) {
    return html + '<div class="cache-empty">事件埋点未接入（' + esc(lspText(overview.reason) || LSP_PENDING_REASON) +
      "）：触发次数 / 命中率 / 降级率 / 等待 P95 在 M1/M2 落地后自动点亮；当前仅池计数为真实值。</div>";
  }
  var m = overview.metrics || {};
  return html + lspCards([
    lspCard(lspNum(m.inline_attempts), "内联触发"),
    lspCard(lspPercent(m.diag_hit_ratio), "诊断命中率"),
    lspCard(lspPercent(m.fallback_ratio), "降级率"),
    lspCard(lspMS(m.wait_latency_p95_ms), "等待 P95")
  ]);
}

function lspEventsSection(eventsBody) {
  eventsBody = eventsBody || {};
  var html = '<div class="cache-detail-title" style="margin-top:10px">最近事件</div>';
  if (!eventsBody.available) {
    return html + '<div class="cache-empty">事件流待接入（' + esc(lspText(eventsBody.reason) || LSP_PENDING_REASON) + "）。</div>";
  }
  var events = eventsBody.events || [];
  if (events.length === 0) {
    return html + '<div class="cache-empty">暂无数据</div>';
  }
  var rows = [];
  for (var i = 0; i < events.length; i++) {
    var e = events[i] || {};
    rows.push("<tr><td>" + esc(lspClip(e.timestamp, 24) || "--") +
      "</td><td>" + esc(lspText(e.trigger) || "--") +
      "</td><td>" + esc(lspText(e.server) || "--") +
      "</td><td>" + esc(lspOutcomeLabel(e.outcome)) +
      "</td><td>" + lspMS(e.duration_ms) +
      "</td><td>" + lspNum(e.diag_count) +
      "</td><td>" + lspNum(e.appended_bytes) + "</td></tr>");
  }
  return html + '<div style="overflow-x:auto"><table class="cache-table"><thead><tr>' +
    "<th>时间</th><th>触发</th><th>Server</th><th>结果</th><th>等待</th><th>诊断数</th><th>追加字节</th>" +
    "</tr></thead><tbody>" + rows.join("") + "</tbody></table></div>";
}

// lspBaselineSection 渲染 §4.3 基线登记表（跨会话聚合，非当前会话读数）：
// 数据源 /web/api/lsp/baseline（与 TUI /lsp baseline 同源）。未采集项由后端
// 输出 n/a；本区块只展示事实与"待标定"，不做阈值告警。
function lspBaselineSection(baseline) {
  baseline = baseline || {};
  var html = '<div class="cache-detail-title" style="margin-top:10px">基线（§4.3 登记表，跨会话）</div>';
  if (baseline.loading) {
    return html + '<div class="cache-empty">基线扫描中（首次约数秒，之后命中 10 分钟缓存）…</div>';
  }
  if (baseline.available === false) {
    return html + '<div class="cache-empty">基线不可用（' + esc(lspText(baseline.reason) || "unknown") +
      "）：" + esc(lspText(baseline.note) || "日志目录尚未产生数据") + "</div>";
  }
  var digest = baseline.digest || {};
  var scan = baseline.scan || {};
  html += lspCards([
    lspCard(lspNum(digest.requests), "窗口请求"),
    lspCard(lspNum(digest.sessions), "LSP 活跃会话"),
    lspCard(lspNum(digest.injected), "已注入"),
    lspCard(lspNum(digest.degraded), "降级"),
    lspCard(lspMS(digest.p95_ms), "等待 P95")
  ]);
  var rows = baseline.rows || [];
  if (rows.length === 0) {
    return html + '<div class="cache-empty">暂无数据</div>';
  }
  var body = [];
  for (var i = 0; i < rows.length; i++) {
    var row = rows[i] || {};
    body.push("<tr><td><code>" + esc(lspText(row.metric)) + "</code></td><td>" +
      esc(lspText(row.value) || "n/a") + "</td><td>" + esc(lspText(row.samples)) +
      "</td><td>" + esc(lspText(row.conclusion)) + "</td></tr>");
  }
  html += '<div style="overflow-x:auto"><table class="cache-table"><thead><tr>' +
    "<th>指标</th><th>基线值</th><th>样本量</th><th>结论/阈值</th>" +
    "</tr></thead><tbody>" + body.join("") + "</tbody></table></div>";
  var scopeParts = ["窗口 " + (baseline.days ? ("最近 " + lspNum(baseline.days) + " 天") : "全窗口")];
  if (baseline.first_at || baseline.last_at) {
    scopeParts.push((lspText(baseline.first_at) || "n/a") + " → " + (lspText(baseline.last_at) || "n/a"));
  }
  scopeParts.push("扫描 " + lspNum(scan.files) + " 文件 / " + lspNum(scan.lines) + " 行");
  if (scan.skipped_files) { scopeParts.push("整文件跳过 " + lspNum(scan.skipped_files)); }
  if (scan.malformed) { scopeParts.push("损坏行 " + lspNum(scan.malformed)); }
  if (baseline.cached_at) { scopeParts.push("缓存于 " + lspClip(baseline.cached_at, 19)); }
  html += '<div class="cache-hint">' + esc(scopeParts.join("；")) +
    "。未采集项显示 n/a；阈值待标定（不做告警）。</div>";
  return html;
}

function renderLSP(status, overview, eventsBody, baseline) {
  var content = lspEl("lsp-content");
  if (!content) { return; }
  status = status || {};
  var html = "";
  var tips = lspSuggestions(status, overview);
  if (tips.length > 0) {
    html += '<div class="cache-hint">' + tips.map(esc).join("<br>") + "</div>";
  }
  html += lspServersSection(status);
  html += lspOverviewSection(overview);
  html += lspEventsSection(eventsBody);
  html += lspBaselineSection(baseline);
  content.innerHTML = html;
}

// ---- 数据加载 ----

export function refreshLSP() {
  var seq = ++lspSeq;
  var sessionID = lspCurrentSessionID();
  Promise.all([
    lspAPI(lspBase + "/status"),
    lspAPI(lspBase + "/overview"),
    lspAPI(lspBase + "/events?limit=50")
  ]).then(function (results) {
    if (seq !== lspSeq) { return; }
    var statusRes = results[0] || {};
    if (statusRes.status !== 200 || !statusRes.body) {
      lspStatusMessage("读取失败（HTTP " + lspText(statusRes.status) + "）", "error");
      return;
    }
    lspStatusMessage("更新于 " + new Date().toLocaleTimeString(), "");
    var overviewBody = results[1] && results[1].body;
    var eventsBody = results[2] && results[2].body;
    // 基线是跨会话全库聚合（服务端 10 分钟 TTL 缓存）：首帧先渲染其余区块，
    // 基线单独异步补位，冷扫耗时不会拖住池状态与读数的展示。
    renderLSP(statusRes.body, overviewBody, eventsBody, { loading: true });
    lspLoadBaseline(seq, statusRes.body, overviewBody, eventsBody);
    lspLoaded = true;
    lspLoadedSessionID = sessionID;
  }, function () {
    if (seq !== lspSeq) { return; }
    lspStatusMessage("读取失败", "error");
  });
}

function lspLoadBaseline(seq, statusBody, overviewBody, eventsBody) {
  lspAPI(lspBase + "/baseline?days=14").then(function (res) {
    if (seq !== lspSeq) { return; }
    if (res && res.status === 200 && res.body) {
      renderLSP(statusBody, overviewBody, eventsBody, res.body);
      return;
    }
    renderLSP(statusBody, overviewBody, eventsBody,
      { available: false, reason: "lsp_baseline_request_failed" });
  }, function () {
    if (seq !== lspSeq) { return; }
    renderLSP(statusBody, overviewBody, eventsBody,
      { available: false, reason: "lsp_baseline_request_failed" });
  });
}

// loadLSP 在页签激活时调用：首次进入或会话变化才拉取（手动刷新走 refreshLSP）。
export function loadLSP() {
  var panel = lspEl("tab-lsp");
  if (!panel || !panel.classList.contains("active")) { return; }
  if (lspLoaded && lspLoadedSessionID === lspCurrentSessionID()) { return; }
  refreshLSP();
}

// refreshLSPIfActive 仅在页签可见时刷新（供外部定时/事件调用）。
export function refreshLSPIfActive() {
  var panel = lspEl("tab-lsp");
  if (!panel || !panel.classList.contains("active")) { return; }
  refreshLSP();
}

function lspAutoButton() { return lspEl("lsp-auto-btn"); }

function lspUpdateAutoButton() {
  var btn = lspAutoButton();
  if (!btn) { return; }
  btn.textContent = lspAutoEnabled ? "自动 ⏸" : "自动 ⟳";
  btn.title = lspAutoEnabled ? "停止自动刷新（页签可见时每 15 秒重拉）" : "自动刷新：页签可见时每 15 秒重拉";
}

function lspAutoTick() {
  var panel = lspEl("tab-lsp");
  if (!lspAutoEnabled || !panel || !panel.classList.contains("active")) {
    stopLSPAuto();
    return;
  }
  refreshLSP();
}

export function stopLSPAuto() {
  lspAutoEnabled = false;
  if (lspAutoTimer) {
    clearInterval(lspAutoTimer);
    lspAutoTimer = null;
  }
  lspUpdateAutoButton();
}

function toggleLSPAuto() {
  if (lspAutoEnabled) { stopLSPAuto(); return; }
  lspAutoEnabled = true;
  lspAutoTimer = setInterval(lspAutoTick, lspAutoIntervalMS);
  lspUpdateAutoButton();
}

export function initLSP() {
  var refreshBtn = lspEl("lsp-refresh-btn");
  if (refreshBtn) {
    refreshBtn.addEventListener("click", function () { refreshLSP(); });
  }
  var autoBtn = lspAutoButton();
  if (autoBtn) {
    autoBtn.addEventListener("click", function () { toggleLSPAuto(); });
  }
  lspUpdateAutoButton();
}
