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
// `?token=`。取值顺序（与页面骨架注入的 fetch 包装一致）：
//   1. sessionStorage 缓存（本页内复用，避免每连接都查 DOM）；
//   2. 页面 <meta name="aicli-web-token">（非回环模式由服务端注入，先于 ES 模块执行）。
// 只返回**本进程**令牌；peer 令牌从不进入前端（Web 子方案 §5.5 红线 1）。
export function webAuthToken() {
  try {
    var cached = sessionStorage.getItem("aicli-web-token");
    if (cached) { return cached; }
  } catch (e) { /* 隐私模式下 sessionStorage 可能抛错：退回 meta */ }
  var meta = document.querySelector('meta[name="aicli-web-token"]');
  var token = meta && meta.content ? String(meta.content).trim() : "";
  if (token) {
    try { sessionStorage.setItem("aicli-web-token", token); } catch (e) { /* ignore */ }
  }
  return token;
}

