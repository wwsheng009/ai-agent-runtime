// 行为验证：aicli micro web client「分析」页签失败模式显示（Node 沙盒，stub document/fetch）。
// 运行：node scripts/verify-micro-web-analysis-errors.mjs（仓库根目录）
// 覆盖：
//   1. fence/idle 渲染为“围栏丢弃（无 run）”并带“非故障诊断”徽标；
//   2. fence/closed 仍显示为“围栏丢弃（run 已结束）”，但不得标为非故障；
//   3. 普通 timeout 分类本地化，不影响原失败语义。
import assert from "node:assert";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_DIR = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web", "js");

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
    addEventListener() {},
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
globalThis.document = {
  documentElement: proxyElement,
  body: proxyElement,
  getElementById(id) { return elements[id] || proxyElement; },
  createElement() { return proxyElement; },
  querySelector() { return proxyElement; },
  querySelectorAll() { return []; },
  addEventListener() {},
  removeEventListener() {},
};
globalThis.window = globalThis;
globalThis.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };
globalThis.sessionStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };
globalThis.addEventListener = () => {};
globalThis.removeEventListener = () => {};
globalThis.EventSource = function () { return proxyElement; };
globalThis.fetch = async (url) => {
  const target = String(url);
  let body = {};
  if (target.indexOf("/web/api/analysis/status") >= 0) {
    body = { attached: true, degraded: false, ingested_total: 1, last_ingest_at: "2026-11-05T00:00:00Z" };
  } else if (target.indexOf("/web/api/analysis/tools") >= 0) {
    body = { tools: [], available_tools: [] };
  } else if (target.indexOf("/web/api/analysis/subagents") >= 0) {
    body = { summary: { total: 0, failed: 0, timeouts: 0 }, subagents: [] };
  } else if (target.indexOf("/web/api/analysis/errors") >= 0) {
    body = {
      patterns: [
        {
          source: "fence",
          error_code: "RENDER_FENCE_DROPPED_IDLE",
          failure_category: "render_fence_drop_idle",
          count: 2,
        },
        {
          source: "fence",
          error_code: "RENDER_FENCE_DROPPED_CLOSED",
          failure_category: "render_fence_drop_closed",
          count: 1,
        },
        {
          source: "tools",
          error_code: "tool_timeout",
          failure_category: "timeout",
          count: 3,
        },
      ],
    };
  } else if (target.indexOf("/web/api/analysis/tool_efficiency") >= 0) {
    body = { captured_at: null, artifact_flow: null };
  } else if (target.indexOf("/web/api/analysis/routing/events") >= 0) {
    body = { events: [], count: 0 };
  } else if (target.indexOf("/web/api/analysis/routing") >= 0) {
    body = { totals: {}, by_scope: [], by_kind: [], by_reason: [], by_source: [], by_difficulty: [], by_difficulty_source: [], by_task_type: [], by_role: [], by_provider: [], by_model: [], warnings: [] };
  }
  return { status: 200, json: async () => body };
};

const analysis = await import(pathToFileURL(path.join(WEB_DIR, "analysis.js")).href);
analysis.initAnalysis();
analysis.refreshAnalysis();
await new Promise((resolve) => setTimeout(resolve, 20));

const html = elements["analysis-errors"].innerHTML;
assert.ok(html.indexOf("围栏丢弃（无 run）") >= 0, "idle fence 分类未本地化: " + html);
assert.ok(html.indexOf("围栏丢弃（run 已结束）") >= 0, "closed fence 分类未本地化: " + html);
assert.ok(html.indexOf(">非故障诊断<") >= 0, "idle fence 缺少非故障诊断徽标: " + html);
const markerCount = html.split("非故障诊断").length - 1;
assert.strictEqual(markerCount, 1, "非故障诊断徽标应只出现一次: " + html);
assert.ok(html.indexOf(">超时<") >= 0, "普通失败分类未本地化: " + html);
assert.ok(html.indexOf(">render_fence_drop_idle<") < 0, "idle 仍显示裸失败分类: " + html);

console.log("verify-micro-web-analysis-errors: 全部断言通过");
