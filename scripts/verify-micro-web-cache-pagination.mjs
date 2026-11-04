// 行为验证：aicli micro web client 缓存分析请求明细分页与筛选。
// 运行：node scripts/verify-micro-web-cache-pagination.mjs
// 覆盖：首屏 limit/offset、上一页/下一页、每页条数切换、末页禁用、本地日期时间显示、
//       会话切换回到第 1 页、provider/model 下拉选项、缓存状态/缓存率筛选、
//       参数校验、空结果。
import assert from "node:assert";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_DIR = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web", "js");

function makeClassList() {
  return {
    _set: new Set(),
    contains(c) { return this._set.has(c); },
    add(c) { this._set.add(c); },
    remove(c) { this._set.delete(c); },
    toggle(c, on) { on ? this._set.add(c) : this._set.delete(c); },
  };
}

function makeControl(key, html) {
  const marker = key === "size" ? "data-cache-page-size" : 'data-cache-page="' + key + '"';
  const markerAt = html.indexOf(marker);
  const start = markerAt >= 0 ? html.lastIndexOf("<", markerAt) : -1;
  const end = markerAt >= 0 ? html.indexOf(">", markerAt) : -1;
  const tag = start >= 0 && end >= 0 ? html.slice(start, end + 1) : "";
  const selected = html.match(/<option value="(\d+)" selected>/);
  const control = {
    value: key === "size" && selected ? selected[1] : "",
    disabled: key !== "size" && tag.indexOf(" disabled") >= 0,
    listeners: {},
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    dispatch(type) { (this.listeners[type] || []).forEach((fn) => fn({ currentTarget: this })); },
  };
  return control;
}

function makeEl(id) {
  return {
    id,
    classList: makeClassList(),
    _html: "",
    _controls: {},
    _rows: {},
    listeners: {},
    set innerHTML(v) { this._html = String(v); this._controls = {}; this._rows = {}; },
    get innerHTML() { return this._html; },
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    dispatch(type) { (this.listeners[type] || []).forEach((fn) => fn({ currentTarget: this })); },
    querySelector(sel) {
      let key = "";
      if (sel === '[data-cache-page="prev"]') { key = "prev"; }
      if (sel === '[data-cache-page="next"]') { key = "next"; }
      if (sel === "[data-cache-page-size]") { key = "size"; }
      if (!key) { return null; }
      return this._controls[key] || (this._controls[key] = makeControl(key, this._html));
    },
    querySelectorAll(sel) {
      if (sel !== ".cache-row") { return []; }
      const ids = [...this._html.matchAll(/data-request-id="([^"]*)"/g)].map((m) => m[1]);
      return ids.map((requestId) => this._rows[requestId] || (this._rows[requestId] = {
        getAttribute(name) { return name === "data-request-id" ? requestId : null; },
        addEventListener(type, fn) { this.listeners[type] = fn; },
        listeners: {},
      }));
    },
  };
}

const elements = {};
globalThis.document = {
  getElementById(id) { return elements[id] || (elements[id] = makeEl(id)); },
  querySelector() { return null; },
  createElement(tag) { return makeEl("created:" + tag); },
  documentElement: makeEl("html"),
  body: { classList: makeClassList() },
};

let requestsTotal = 123;
const requestCalls = [];
function jsonResponse(status, body) {
  return Promise.resolve({ status, json: () => Promise.resolve(body) });
}
globalThis.fetch = function (input) {
  const url = String(input);
  if (url.indexOf("/web/api/cache/capabilities") >= 0) {
    return jsonResponse(200, {
      schema_version: "cache.analytics.v1",
      data_source: "live",
      max_requests_per_session: 4096,
      persisted: true,
    });
  }
  if (url.indexOf("/web/api/cache/overview") >= 0) {
    return jsonResponse(200, {
      requests_total: requestsTotal,
      providers: ["openai", "anthropic"],
      models: ["gpt-4o", "claude-sonnet"],
    });
  }
  if (url.indexOf("/web/api/cache/requests?") >= 0) {
    const params = new URL(url, "http://localhost").searchParams;
    const limit = Number(params.get("limit") || 0);
    const offset = Number(params.get("offset") || 0);
    const call = { limit, offset };
    if (params.has("cache_status")) { call.cacheStatus = params.get("cache_status"); }
    if (params.has("cache_hit_ratio_min")) { call.cacheHitRatioMin = Number(params.get("cache_hit_ratio_min")); }
    if (params.has("cache_hit_ratio_max")) { call.cacheHitRatioMax = Number(params.get("cache_hit_ratio_max")); }
    if (params.has("provider")) { call.provider = params.get("provider"); }
    if (params.has("model")) { call.model = params.get("model"); }
    if (params.has("status")) { call.status = params.get("status"); }
    if (params.has("from")) { call.from = params.get("from"); }
    if (params.has("to")) { call.to = params.get("to"); }
    if (params.has("duration_ms_min")) { call.durationMin = Number(params.get("duration_ms_min")); }
    if (params.has("duration_ms_max")) { call.durationMax = Number(params.get("duration_ms_max")); }
    requestCalls.push(call);
    const filtered = call.cacheStatus !== undefined
      || call.cacheHitRatioMin !== undefined
      || call.cacheHitRatioMax !== undefined
      || call.provider !== undefined
      || call.model !== undefined
      || call.status !== undefined
      || call.from !== undefined
      || call.to !== undefined
      || call.durationMin !== undefined
      || call.durationMax !== undefined;
    const responseTotal = filtered ? 7 : requestsTotal;
    const count = Math.max(0, Math.min(limit, responseTotal - offset));
    const requests = Array.from({ length: count }, (_, i) => ({
      llm_request_id: "req-" + (offset + i),
      started_at: "2026-01-01T00:00:00Z",
      provider: "openai",
      model: "gpt-4o",
      status: "success",
      cache_status: "hit",
      usage: {},
    }));
    return jsonResponse(200, {
      schema_version: "cache.analytics.v1",
      total: responseTotal,
      limit,
      offset,
      requests,
    });
  }
  return jsonResponse(404, { error: { code: "not_found", message: "unexpected URL: " + url } });
};

const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

(async () => {
  const cache = await import(pathToFileURL(path.join(WEB_DIR, "cache.js")).href);
  const panel = document.getElementById("tab-cache");
  const requestsEl = document.getElementById("cache-requests");
  panel.classList.add("active");

  cache.loadCacheAnalytics();
  await tick();
  await tick();
  assert.deepStrictEqual(requestCalls[0], { limit: 50, offset: 0 }, "首屏应按默认 50 条从第 1 页查询");
  assert.ok(requestsEl.innerHTML.indexOf("第 1 / 3 页") >= 0, "应显示总页数");
  assert.ok(requestsEl.innerHTML.indexOf("共 123 条") >= 0, "应显示后端 total");
  assert.strictEqual(requestsEl.querySelector('[data-cache-page="prev"]').disabled, true, "第 1 页上一页应禁用");
  assert.strictEqual(requestsEl.querySelector('[data-cache-page="next"]').disabled, false, "第 1 页下一页应可用");
  assert.ok(document.getElementById("cache-filter-provider").innerHTML.indexOf('value="openai"') >= 0, "provider 应为会话级下拉选项");
  assert.ok(document.getElementById("cache-filter-model").innerHTML.indexOf('value="claude-sonnet"') >= 0, "model 应为会话级下拉选项");
  assert.match(requestsEl.innerHTML, /\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}/, "时间列应显示完整本地日期时间");
  assert.ok(document.getElementById("cache-filter-timezone").textContent.length > 0, "时间筛选应显示本地时区标识");

  requestsEl.querySelector('[data-cache-page="next"]').dispatch("click");
  await tick();
  await tick();
  assert.deepStrictEqual(requestCalls[1], { limit: 50, offset: 50 }, "下一页应查询 offset=50");
  assert.ok(requestsEl.innerHTML.indexOf("第 2 / 3 页") >= 0, "应更新到第 2 页");
  assert.ok(requestsEl.innerHTML.indexOf("req-50") >= 0, "应渲染第 2 页记录");

  requestsEl.querySelector('[data-cache-page="next"]').dispatch("click");
  await tick();
  await tick();
  assert.deepStrictEqual(requestCalls[2], { limit: 50, offset: 100 }, "第 3 页应查询 offset=100");
  assert.strictEqual(requestsEl.querySelector('[data-cache-page="next"]').disabled, true, "末页下一页应禁用");

  const sizeSelect = requestsEl.querySelector("[data-cache-page-size]");
  sizeSelect.value = "100";
  sizeSelect.dispatch("change");
  await tick();
  await tick();
  assert.deepStrictEqual(requestCalls[3], { limit: 100, offset: 0 }, "切换每页条数应回到第 1 页");
  assert.ok(requestsEl.innerHTML.indexOf("第 1 / 2 页") >= 0, "每页 100 条时应为 2 页");

  requestsEl.querySelector('[data-cache-page="next"]').dispatch("click");
  await tick();
  await tick();
  assert.deepStrictEqual(requestCalls[4], { limit: 100, offset: 100 }, "每页 100 条的第 2 页应查询 offset=100");
  cache.syncCacheSession("session-b");
  await tick();
  await tick();
  assert.deepStrictEqual(requestCalls[5], { limit: 100, offset: 0 }, "切换会话应重置到第 1 页");

  requestsEl.querySelector('[data-cache-page="next"]').dispatch("click");
  await tick();
  await tick();
  assert.deepStrictEqual(requestCalls[6], { limit: 100, offset: 100 }, "切换会话后仍可继续翻页");
  panel.classList.remove("active");
  cache.syncCacheSession("session-c");
  cache.syncCacheSession("session-b");
  panel.classList.add("active");
  cache.loadCacheAnalytics();
  await tick();
  assert.strictEqual(requestCalls.length, 7, "切走再切回已缓存会话不应重复请求");
  assert.ok(requestsEl.innerHTML.indexOf("第 2 / 2 页") >= 0, "切回已缓存会话应保留原页码");

  document.getElementById("cache-filter-provider").value = "openai";
  document.getElementById("cache-filter-model").value = "gpt";
  document.getElementById("cache-filter-request-status").value = "success";
  document.getElementById("cache-filter-status").value = "hit";
  document.getElementById("cache-filter-hit-min").value = "40";
  document.getElementById("cache-filter-hit-max").value = "80";
  document.getElementById("cache-filter-from").value = "2026-01-01T10:00";
  document.getElementById("cache-filter-to").value = "2026-01-01T11:00";
  document.getElementById("cache-filter-duration-min").value = "100";
  document.getElementById("cache-filter-duration-max").value = "5000";
  document.getElementById("cache-filter-apply").dispatch("click");
  await tick();
  await tick();
  assert.deepStrictEqual(requestCalls[7], {
    limit: 100,
    offset: 0,
    provider: "openai",
    model: "gpt",
    status: "success",
    cacheStatus: "hit",
    cacheHitRatioMin: 0.4,
    cacheHitRatioMax: 0.8,
    from: new Date("2026-01-01T10:00").toISOString(),
    to: new Date("2026-01-01T11:00").toISOString(),
    durationMin: 100,
    durationMax: 5000,
  }, "筛选应携带缓存状态和缓存率区间，并从第 1 页查询");
  assert.ok(requestsEl.innerHTML.indexOf("共 7 条") >= 0, "筛选后的分页总数应使用后端返回的 total");

  document.getElementById("cache-filter-hit-min").value = "101";
  document.getElementById("cache-filter-apply").dispatch("click");
  assert.strictEqual(requestCalls.length, 8, "非法缓存率不应发请求");
  assert.ok(document.getElementById("cache-filter-error").textContent.indexOf("0–100") >= 0, "非法缓存率应提示范围");

  document.getElementById("cache-filter-hit-min").value = "90";
  document.getElementById("cache-filter-hit-max").value = "80";
  document.getElementById("cache-filter-apply").dispatch("click");
  assert.strictEqual(requestCalls.length, 8, "倒置缓存率区间不应发请求");
  assert.ok(document.getElementById("cache-filter-error").textContent.indexOf("不能高于") >= 0, "倒置区间应提示错误");

  document.getElementById("cache-filter-hit-min").value = "40";
  document.getElementById("cache-filter-duration-min").value = "abc";
  document.getElementById("cache-filter-apply").dispatch("click");
  assert.strictEqual(requestCalls.length, 8, "非法耗时不应发请求");
  assert.ok(document.getElementById("cache-filter-error").textContent.indexOf("非负整数") >= 0, "非法耗时应提示错误");

  document.getElementById("cache-filter-reset").dispatch("click");
  await tick();
  await tick();
  assert.deepStrictEqual(requestCalls[8], { limit: 100, offset: 0 }, "重置后应清空全部筛选参数");

  requestsTotal = 0;
  cache.refreshCacheAnalytics();
  await tick();
  await tick();
  assert.ok(requestsEl.innerHTML.indexOf("暂无 LLM 请求记录") >= 0, "空结果应显示空态");
  assert.ok(requestsEl.innerHTML.indexOf("cache-pagination") < 0, "空结果不应显示分页条");

  document.getElementById("cache-filter-provider").value = "openai";
  document.getElementById("cache-filter-model").value = "gpt-4o";
  cache.syncCacheSession("session-dimensions-reset");
  await tick();
  await tick();
  assert.strictEqual(document.getElementById("cache-filter-provider").value, "", "切换会话应清空 provider 选择");
  assert.strictEqual(document.getElementById("cache-filter-model").value, "", "切换会话应清空 model 选择");

  console.log("ALL CACHE PAGINATION TESTS PASSED");
})().catch((err) => {
  console.error("FAILED:", (err && err.stack) || err);
  process.exit(1);
});
