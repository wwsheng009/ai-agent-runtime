// 行为验证：aicli micro web client 启动加载次序（2026-10 性能约定）。
// 运行：node scripts/verify-micro-web-load-order.mjs（仓库根目录）
// 覆盖：
//   1. 静态契约：app.js 启动序列「屏幕快照先发、会话列表挂起后放行」；
//      app.js 不再直接调用 loadSessions（入口只经由 hold/release 门闩）；
//   2. 静态契约：其它页签数据源仍由 ui.js::activateTab 在用户点击时才懒加载；
//   3. 行为契约：各页签 initXxx() 只绑定事件、不发请求（代理 DOM + fetch 记账）；
//   4. 行为契约：sessions.js 门闩期内 loadSessions 不发请求；release 后只发一次；
//      在途期间并发触发合并为「至多一个在途 + 恰好一次补拉」。
import assert from "node:assert";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_DIR = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web");

let failures = 0;
function check(name, fn) {
  try {
    fn();
    console.log("  ok  - " + name);
  } catch (err) {
    failures++;
    console.log("  FAIL- " + name + "\n        " + (err && err.message ? err.message : err));
  }
}

// ===========================================================================
// [1] 静态契约：app.js 启动加载次序
// ===========================================================================
console.log("[1] app.js 启动加载次序");
const APP_JS = fs.readFileSync(path.join(WEB_DIR, "app.js"), "utf8");

check("启动序列先挂起会话列表、再发屏幕快照、落地后放行", () => {
  const holdAt = APP_JS.indexOf("holdSessionsLoad()");
  const screenAt = APP_JS.indexOf("releaseSessionsAfterScreen(refreshScreen())");
  assert.ok(holdAt >= 0, "缺少 holdSessionsLoad() 调用");
  assert.ok(screenAt >= 0, "缺少 releaseSessionsAfterScreen(refreshScreen())");
  assert.ok(holdAt < screenAt, "hold 必须在屏幕快照发出之前");
  assert.ok(
    APP_JS.indexOf('from "./js/sessions.js"') >= 0 &&
      APP_JS.indexOf("releaseSessionsLoad") >= 0,
    "未从 sessions.js 引入 hold/release 门闩",
  );
});

check("app.js 不再直接调用 loadSessions（列表只经由门闩放行）", () => {
  assert.ok(!/\bloadSessions\s*\(/.test(APP_JS), "app.js 仍直接调用 loadSessions()");
  assert.ok(!/import[^\n]*\bloadSessions\b/.test(APP_JS), "app.js 仍 import loadSessions");
});

check("状态栏首次拉取只发生一次（initStatusBar 内部完成）", () => {
  const bootCalls = APP_JS.match(/^\s*loadStatusBar\(\);/gm) || [];
  assert.equal(bootCalls.length, 0, "app.js 启动序列不应再重复调用 loadStatusBar()");
  assert.ok(APP_JS.indexOf("initStatusBar();") >= 0, "缺少 initStatusBar() 初始化");
});

check("门闩带兜底放行（屏幕快照异常缓慢时不无限拖住侧栏）", () => {
  assert.ok(/setTimeout\(release,\s*SESSION_LIST_FALLBACK_DELAY_MS\)/.test(APP_JS),
    "缺少兜底超时放行");
  assert.ok(/SESSION_LIST_FALLBACK_DELAY_MS\s*=\s*\d+/.test(APP_JS), "缺少兜底延时常量");
});

// ===========================================================================
// [2] 静态契约：其它页签数据源仍按用户点击懒加载
// ===========================================================================
console.log("[2] 页签数据源懒加载（ui.js::activateTab）");
const UI_JS = fs.readFileSync(path.join(WEB_DIR, "js", "ui.js"), "utf8");
const activateStart = UI_JS.indexOf("function activateTab(");
const activateEnd = UI_JS.indexOf("// ---- 关于页签", activateStart);
const activateBody = UI_JS.slice(activateStart, activateEnd);

check("activateTab 覆盖全部页签数据源", () => {
  const lazyLoads = [
    ["loadSkills", "if (isSkills) { loadSkills(); }"],
    ["loadFiles", "if (isFiles) { loadFiles(); }"],
    ["loadGit", "if (isGit) { loadGit(); }"],
    ["loadMCPs", "if (isMCP) { loadMCPs(); }"],
    ["loadSchedule", "if (isSchedule) { loadSchedule(); }"],
    ["loadConfigAdmin", "if (isConfig) { loadConfigAdmin(); }"],
    ["loadCacheAnalytics", "if (isCache) { loadCacheAnalytics(); }"],
    ["loadAnalysis", "if (isAnalysis) { loadAnalysis(); }"],
    ["loadLSP", "if (isLSP) { loadLSP(); }"],
    ["loadDebugInfo", "if (isDebug) { loadDebugInfo(); }"],
  ];
  for (const [label, needle] of lazyLoads) {
    assert.ok(activateBody.indexOf(needle) >= 0, "activateTab 缺少 " + label + " 分支");
  }
  assert.ok(activateBody.indexOf("loadAboutEndpoints();") >= 0, "activateTab 缺少关于页签分支");
});

// ===========================================================================
// 共享桩：代理 DOM + fetch 记账（真实 ES 模块在 Node 下可跑）
// ===========================================================================
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
const proxyDocument = new Proxy(function () {}, {
  get(target, prop) {
    if (prop === "then") { return undefined; }
    if (prop === "getElementById" || prop === "createElement") { return () => proxyElement; }
    if (prop === "querySelector" || prop === "querySelectorAll") { return () => proxyElement; }
    if (prop === "addEventListener" || prop === "removeEventListener") { return () => {}; }
    if (prop === "title") { return ""; }
    if (prop in target) { return target[prop]; }
    return proxyElement;
  },
  set() { return true; },
  apply() { return proxyElement; },
});
globalThis.document = proxyDocument;
globalThis.window = globalThis;
globalThis.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };
globalThis.sessionStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };
globalThis.addEventListener = () => {};
globalThis.removeEventListener = () => {};
globalThis.EventSource = function () { return proxyElement; };

const fetchCalls = [];
const fetchResolvers = [];
globalThis.fetch = (url) => {
  fetchCalls.push(String(url));
  return new Promise((resolve) => {
    fetchResolvers.push(() =>
      resolve({ ok: true, status: 200, json: async () => ({ sessions: [], current_session_id: "" }) }));
  });
};
const flush = () => new Promise((resolve) => setTimeout(resolve, 10));

// ===========================================================================
// [3] 行为契约：各页签 initXxx() 只绑定事件、不发请求
// ===========================================================================
console.log("[3] 页签 initXxx 不预热数据（行为：只绑定事件不发请求）");
const TAB_INITS = [
  ["analysis.js", "initAnalysis"],
  ["lsp.js", "initLSP"],
  ["skills.js", "initSkills"],
  ["files.js", "initFiles"],
  ["git.js", "initGit"],
  ["mcp.js", "initMCP"],
  ["schedule.js", "initSchedule"],
  ["config-admin.js", "initConfigAdmin"],
];
for (const [file, initName] of TAB_INITS) {
  const mod = await import(pathToFileURL(path.join(WEB_DIR, "js", file)).href);
  const before = fetchCalls.length;
  if (typeof mod[initName] === "function") { mod[initName](); }
  await flush();
  check("init " + initName + "() 只绑定事件、不发请求", () => {
    assert.equal(fetchCalls.length, before, initName + " 在初始化时发起了请求");
  });
}

// ===========================================================================
// [4] 行为契约：sessions.js 门闩 + 在途合并
// ===========================================================================
console.log("[4] sessions.js 门闩与在途合并（真实模块 + 代理 DOM）");
const sessions = await import(pathToFileURL(path.join(WEB_DIR, "js", "sessions.js")).href);
await flush();
const callsAtImport = fetchCalls.length;

sessions.holdSessionsLoad();
sessions.loadSessions();
sessions.loadSessions();
await flush();
check("门闩期内 loadSessions 不发请求", () => {
  assert.equal(fetchCalls.length, callsAtImport, "门闩期内不应发会话列表请求");
});

sessions.releaseSessionsLoad();
await flush();
check("release 后恰好发一次列表请求", () => {
  assert.equal(fetchCalls.length, callsAtImport + 1, "应恰好一次 /web/api/sessions");
  assert.ok(fetchCalls[callsAtImport].indexOf("/web/api/sessions") >= 0, "请求路径应为 /web/api/sessions");
});

// 在途合并：在途期间再触发两次 → 不并发发请求；落地后恰好补拉一次。
fetchResolvers[callsAtImport]();
await flush();
sessions.loadSessions();
await flush();
sessions.loadSessions();
sessions.loadSessions();
await flush();
check("在途期间并发触发不产生并发请求", () => {
  assert.equal(fetchCalls.length, callsAtImport + 2, "在途期间不应再发请求");
});
fetchResolvers[callsAtImport + 1]();
await flush();
check("在途合并只补拉一次（无重复轮询）", () => {
  assert.equal(fetchCalls.length, callsAtImport + 3, "并发触发应合并为恰好一次补拉");
});
fetchResolvers[callsAtImport + 2]();
await flush();
check("补拉落地后无多余请求", () => {
  assert.equal(fetchCalls.length, callsAtImport + 3, "补拉落地后不应再发请求");
});

console.log(failures === 0 ? "\n全部通过" : "\n失败 " + failures + " 项");
process.exit(failures === 0 ? 0 : 1);
