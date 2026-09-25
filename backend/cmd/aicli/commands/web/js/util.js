// 通用小工具:HTML 转义、Toast 轻提示、getElementById 快捷方式。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

var toastContainer = document.getElementById("toast-container");
// 转义 HTML 实体，防止流式文本中的 < > & 破坏 innerHTML 渲染。
export function esc(s) {
  return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

// ---- Toast 轻提示 ----
var toastTimer = null;
export function showToast(msg, kind, duration) {
  kind = kind || "ok";
  duration = duration || 2500;
  if (!toastContainer) { return; }
  clearTimeout(toastTimer);
  toastContainer.innerHTML = "";
  var t = document.createElement("div");
  t.className = "toast toast-" + kind;
  t.textContent = msg;
  toastContainer.appendChild(t);
  toastTimer = setTimeout(function () {
    if (t.parentNode) { t.parentNode.removeChild(t); }
  }, duration);
}

export function configEl(id) { return document.getElementById(id); }

// ---- 本进程写令牌（X-AICLI-Token / ?token=）----
//
// 回环模式下 GET/SSE 无需令牌（Host/Origin 校验已足够，web_auth.go）；非回环模式
// 下所有请求（含 SSE）都要带令牌，而 EventSource 无法设置请求头，只能走
// `?token=`。取值顺序（与页面骨架注入的 fetch 包装一致，2026-09 修订）：
//   1. 页面 <meta name="aicli-web-token">：**当前**进程注入的权威值（先于 ES
//      模块执行，head 内联脚本已把同一个值写进 sessionStorage）；
//   2. sessionStorage 缓存：仅当 meta 缺失时兜底——同源上一个进程（随机令牌
//      重启后）可能留下过期值，反序会让首个请求带旧令牌 → 403。
// 只返回**本进程**令牌；peer 令牌从不进入前端（Web 子方案 §5.5 红线 1）。
export function webAuthToken() {
  var meta = document.querySelector('meta[name="aicli-web-token"]');
  var token = meta && meta.content ? String(meta.content).trim() : "";
  if (token) {
    try { sessionStorage.setItem("aicli-web-token", token); } catch (e) { /* ignore */ }
    return token;
  }
  try { return sessionStorage.getItem("aicli-web-token") || ""; } catch (e) { return ""; }
}


// ---- 请求超时 + 降级上报（§4.7 降级契约 / 连接预算）----
//
// 动机：浏览器对单 host 的并发连接有硬上限（HTTP/1.1 常见 6 条）。常驻 SSE 一旦
// 占满连接池，后续 fetch 会在浏览器侧**永久排队**：既不失败也不返回，页面表现为
// 白屏/卡死（无错误、无降级、无法自愈）。给请求加超时让排队中的请求主动出队
// （AbortController 会把请求从浏览器队列里摘掉）并如实上报，页面据此显示降级提示、
// 转入轮询兜底；连接一旦空出，下一次轮询就会命中并自动恢复。
export var API_TIMEOUT_MS = 12000;

// NET_FAILURE_THRESHOLD：连续失败多少次才判定「降级」。取 2 是为了避开单次抖动
// （一次超时可能只是某一帧很大），同时又不至于让用户长时间盯着卡死的界面。
var NET_FAILURE_THRESHOLD = 2;
var netFailures = 0;
var netDegraded = false;
var netReason = "";
var netListeners = [];

// onNetStateChange：订阅降级状态变化（sse.js 用它显示横幅并启动降级轮询）。
export function onNetStateChange(fn) {
  if (typeof fn === "function") { netListeners.push(fn); }
}

export function isNetDegraded() { return netDegraded; }
export function netDegradedReason() { return netReason; }

function notifyNetState() {
  for (var i = 0; i < netListeners.length; i++) {
    try { netListeners[i](netDegraded, netReason); } catch (e) { /* 监听器异常不得影响请求路径 */ }
  }
}

// reportAPISuccess：任何一次成功响应都视为「连接可用」，立即退出降级。
export function reportAPISuccess() {
  netFailures = 0;
  if (!netDegraded) { return; }
  netDegraded = false;
  netReason = "";
  notifyNetState();
}

// reportAPIFailure：网络层失败（超时/中断/断网）计数；HTTP 4xx/5xx 不算失败
// ——那说明连接是通的，属于业务错误，不应触发降级横幅。
export function reportAPIFailure(err) {
  var aborted = !!err && (err.name === "AbortError" || err.code === 20);
  netReason = aborted
    ? "请求超时：浏览器连接可能已被常驻事件流占满"
    : "请求失败：" + ((err && err.message) ? err.message : String(err));
  netFailures++;
  if (netDegraded || netFailures < NET_FAILURE_THRESHOLD) { return; }
  netDegraded = true;
  notifyNetState();
}

// trackAPIRequest：把一次请求的结果接到降级状态机上（超时定时器同时清理）。
function trackAPIRequest(path, promise, clearTimer) {
  return promise.then(function (res) {
    if (clearTimer) { clearTimer(); }
    reportAPISuccess();
    return res;
  }, function (err) {
    if (clearTimer) { clearTimer(); }
    reportAPIFailure(err);
    throw err;
  });
}

// apiFetch：带超时的 fetch 封装。
//   - timeoutMs 省略 → API_TIMEOUT_MS；显式传 0/负数 → 不超时（长任务/大下载）；
//   - 调用方自带 signal 时与之联动（任一 abort 都会中断请求）；
//   - 只有网络层失败才计入降级（见 reportAPIFailure）。
export function apiFetch(path, init, timeoutMs) {
  init = init || {};
  var timeout = (timeoutMs === undefined || timeoutMs === null) ? API_TIMEOUT_MS : timeoutMs;
  var opts = {};
  for (var key in init) {
    if (Object.prototype.hasOwnProperty.call(init, key)) { opts[key] = init[key]; }
  }
  if (!timeout || timeout <= 0 || typeof AbortController === "undefined") {
    return trackAPIRequest(path, fetch(path, opts), null);
  }
  var controller = new AbortController();
  var timer = setTimeout(function () { controller.abort(); }, timeout);
  if (init.signal) {
    if (init.signal.aborted) {
      controller.abort();
    } else if (init.signal.addEventListener) {
      init.signal.addEventListener("abort", function () { controller.abort(); });
    }
  }
  opts.signal = controller.signal;
  return trackAPIRequest(path, fetch(path, opts), function () { clearTimeout(timer); });
}
