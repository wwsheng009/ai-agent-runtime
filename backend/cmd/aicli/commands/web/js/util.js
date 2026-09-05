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

