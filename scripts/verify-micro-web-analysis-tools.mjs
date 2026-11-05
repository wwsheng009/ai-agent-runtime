// 行为验证：aicli micro web client「分析」页签工具表（Node 沙盒，stub document/fetch）。
// 运行：node scripts/verify-micro-web-analysis-tools.mjs（仓库根目录）
// 覆盖：
//   1. /web/api/analysis/tools 返回 available_tools 时，工具表取「usage 明细 ∪ 可用工具面」；
//   2. 未使用工具以 0 调用 + 「未使用」徽标列出，不是「暂无数据」；
//   3. 汇总条 / 计分卡显示可用 / 已用 / 未用计数；
//   4. 全部 / 已使用 / 未使用筛选按钮按 aria-pressed 工作；
//   5. scope=all 的响应没有 available_tools 时，忽略筛选并显示全局视图说明。
import assert from "node:assert";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_DIR = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web", "js");

// ---- 通用代理桩：供 sessions.js / chat.js 等依赖模块在 Node 下加载 ----
const proxyElement = new Proxy(function () {}, {
  get(target, prop) {
    if (prop === "then") { return undefined; }
    if (prop === "nodeType") { return 1; }
    if (prop === "length") { return 0; }
    if (prop === Symbol.iterator) { return function* () {}; }
    if (prop === Symbol.toPrimitive) { return () => ""; }
    if (prop === "toString") { return () => "[stub]"; }
    if (prop in target) { return target[prop]; }
    return proxyElement;
  },
  set() { return true; },
  apply() { return proxyElement; },
});

function makeElement(id) {
  const listeners = {};
  return {
    id,
    innerHTML: "",
    textContent: "",
    className: "",
    style: {},
    classList: {
      _set: new Set(),
      contains(name) { return this._set.has(name); },
      add(name) { this._set.add(name); },
      remove(name) { this._set.delete(name); },
      toggle(name, on) { on ? this._set.add(name) : this._set.delete(name); },
    },
    addEventListener(type, fn) { (listeners[type] = listeners[type] || []).push(fn); },
    dispatch(type, event) { (listeners[type] || []).forEach((fn) => fn(event || {})); },
    getAttribute() { return null; },
    setAttribute() {},
    querySelector() { return null; },
    querySelectorAll() { return []; },
  };
}

const analysisIDs = [
  "analysis-status", "analysis-health", "analysis-cards", "analysis-tools",
  "analysis-subagents", "analysis-errors", "analysis-efficiency", "analysis-routing",
  "analysis-routing-events", "analysis-detail-overlay", "analysis-detail-title",
  "analysis-detail-body", "analysis-detail-close", "analysis-scope-btn",
  "analysis-refresh-btn", "analysis-auto-btn", "tab-analysis",
];
const elements = {};
analysisIDs.forEach((id) => { elements[id] = makeElement(id); });
const proxyDocument = {
  documentElement: proxyElement,
  body: proxyElement,
  getElementById(id) { return elements[id] || proxyElement; },
  createElement() { return proxyElement; },
  querySelector() { return proxyElement; },
  querySelectorAll() { return []; },
  addEventListener() {},
  removeEventListener() {},
};
globalThis.document = proxyDocument;
globalThis.window = globalThis;
globalThis.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };
globalThis.sessionStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };
globalThis.addEventListener = () => {};
globalThis.removeEventListener = () => {};
globalThis.EventSource = function () { return proxyElement; };

const usedTool = {
  tool_name: "view",
  calls: 3,
  failures: 1,
  failure_rate: 0.333,
  empty_results: 0,
  retried_calls: 1,
  average_duration_ms: 10,
  min_duration_ms: 5,
  max_duration_ms: 15,
  p50_duration_ms: 10,
  p95_duration_ms: 15,
  error_top: [{ error_code: "E_TIMEOUT", failure_category: "timeout", count: 1 }],
};
const availableTools = [
  { name: "shell", source: "core" },
  { name: "view", source: "core" },
  { name: "never_used", source: "mcp:demo" },
];

globalThis.fetch = async (url) => {
  const target = String(url);
  let body;
  if (target.indexOf("/web/api/analysis/status") >= 0) {
    body = { attached: true, degraded: false, ingested_total: 1, last_ingest_at: "2026-03-01T00:00:00Z" };
  } else if (target.indexOf("/web/api/analysis/tools") >= 0) {
    body = {
      schema_version: "usage.analytics.v2",
      generated_at: "2026-03-01T00:00:00Z",
      tools: [usedTool],
      totals: {
        tool_name: "all", calls: 3, failures: 1, average_duration_ms: 10,
        min_duration_ms: 5, max_duration_ms: 15,
      },
      available_tools: target.indexOf("scope=all") >= 0 ? [] : availableTools,
      available_tools_source: target.indexOf("scope=all") >= 0 ? "scope_all" : "stable_surface",
    };
  } else if (target.indexOf("/web/api/analysis/subagents") >= 0) {
    body = { summary: { total: 0, failed: 0, timeouts: 0 }, subagents: [] };
  } else if (target.indexOf("/web/api/analysis/errors") >= 0) {
    body = { patterns: [] };
  } else if (target.indexOf("/web/api/analysis/tool_efficiency") >= 0) {
    body = { captured_at: null, artifact_flow: null };
  } else if (target.indexOf("/web/api/analysis/routing/events") >= 0) {
    body = { events: [], count: 0 };
  } else if (target.indexOf("/web/api/analysis/routing") >= 0) {
    body = {
      totals: {},
      by_scope: [], by_kind: [], by_reason: [], by_source: [], by_difficulty: [],
      by_difficulty_source: [], by_task_type: [], by_role: [], by_provider: [],
      by_model: [], warnings: [],
    };
  } else {
    body = {};
  }
  return { status: 200, json: async () => body };
};

const flush = () => new Promise((resolve) => setTimeout(resolve, 20));

function fakeTarget(attrs) {
  return {
    parentNode: null,
    getAttribute(name) { return Object.prototype.hasOwnProperty.call(attrs, name) ? attrs[name] : null; },
  };
}

function clickFilter(key) {
  elements["analysis-tools"].dispatch("click", {
    target: fakeTarget({ "data-analysis-tool-filter": key }),
  });
}

let failures = 0;
function check(name, fn) {
  try {
    fn();
    console.log("  ok  - " + name);
  } catch (err) {
    failures++;
    console.error("  FAIL - " + name + "\n        " + (err && err.message ? err.message : err));
  }
}

const analysis = await import(pathToFileURL(path.join(WEB_DIR, "analysis.js")).href);
analysis.initAnalysis();
analysis.refreshAnalysis();
await flush();

const toolsHTML = () => elements["analysis-tools"].innerHTML;
const cardsHTML = () => elements["analysis-cards"].innerHTML;

console.log("[1] 工具表合并 usage 明细与 available_tools");
check("未使用工具以 0 调用列出", () => {
  assert.ok(toolsHTML().indexOf("never_used") >= 0, "缺少 available_tools 中的 never_used");
  assert.ok(toolsHTML().indexOf(">未使用<") >= 0, "缺少未使用状态徽标");
  assert.ok(toolsHTML().indexOf(">0<") >= 0, "未使用工具应显示 0 次调用");
});
check("已使用工具保留统计与来源 title", () => {
  assert.ok(toolsHTML().indexOf(">view<") >= 0, "缺少已使用工具 view");
  assert.ok(toolsHTML().indexOf(">已使用<") >= 0, "缺少已使用状态徽标");
  assert.ok(toolsHTML().indexOf('title="来源：mcp:demo"') >= 0, "未使用工具缺少来源 title");
});
check("汇总条与筛选按钮", () => {
  assert.ok(toolsHTML().indexOf("可用 3") >= 0, "缺少可用数");
  assert.ok(toolsHTML().indexOf("已使用 1") >= 0, "缺少已使用数");
  assert.ok(toolsHTML().indexOf("未使用 2") >= 0, "缺少未使用数");
  assert.ok(toolsHTML().indexOf('data-analysis-tool-filter="unused"') >= 0, "缺少未使用筛选按钮");
  assert.ok(toolsHTML().indexOf('data-analysis-tool-filter="used"') >= 0, "缺少已使用筛选按钮");
});
check("计分卡显示可用 / 已用 / 未用", () => {
  assert.ok(cardsHTML().indexOf("可用 3") >= 0, "计分卡缺少可用数");
  assert.ok(cardsHTML().indexOf("已用 1") >= 0, "计分卡缺少已用数");
  assert.ok(cardsHTML().indexOf("未用 2") >= 0, "计分卡缺少未用数");
});

console.log("[2] 全部 / 已使用 / 未使用筛选");
clickFilter("unused");
check("未使用筛选只保留 available && 无调用行", () => {
  assert.ok(toolsHTML().indexOf("never_used") >= 0, "未使用筛选应保留 never_used");
  assert.ok(toolsHTML().indexOf("shell") >= 0, "未使用筛选应保留 shell");
  assert.ok(toolsHTML().indexOf(">view<") < 0, "未使用筛选不应保留已使用工具 view");
  assert.ok(toolsHTML().indexOf('aria-pressed="true"') >= 0, "筛选按钮缺少 aria-pressed=true");
});
clickFilter("used");
check("已使用筛选只保留 usage 明细行", () => {
  assert.ok(toolsHTML().indexOf(">view<") >= 0, "已使用筛选应保留 view");
  assert.ok(toolsHTML().indexOf("never_used") < 0, "已使用筛选不应保留 never_used");
});

console.log("[3] scope=all 忽略可用工具面筛选");
clickFilter("all");
elements["analysis-scope-btn"].dispatch("click", {});
await flush();
check("全局视图显示说明且不再展示未使用筛选", () => {
  assert.ok(toolsHTML().indexOf("全局视图只统计已使用工具") >= 0, "缺少全局视图说明");
  assert.ok(toolsHTML().indexOf('data-analysis-tool-filter="unused"') < 0, "全局视图不应展示未使用筛选");
  assert.ok(toolsHTML().indexOf(">view<") >= 0, "全局视图应保留已使用工具行: " + toolsHTML());
});

console.log(failures === 0 ? "\n全部通过" : "\n失败 " + failures + " 项");
process.exit(failures === 0 ? 0 : 1);
