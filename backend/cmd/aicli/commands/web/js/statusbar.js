// 底部状态栏（micro web client 版 ChatBar）。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。
//
// 数据源: GET /web/api/statusbar （复用 TUI 同名段值构建函数）。
// 渲染: 将服务端返回的 segments 数组渲染为可点击的 span 段，段与段之间以
//       " · " 分隔（与 TUI 一致）。鼠标悬浮显示完整 full_line。
//       动态状态（动态状态栏）由 SSE 的 dynamic_status 事件独立驱动，
//       这里仅负责静态的 provider/model/balance/context/dir/git/window。

import { apiFetch, esc } from "./util.js";

var statusBarEl = document.getElementById("status-bar");
var refreshBtnEl = document.getElementById("status-bar-refresh");

// 段图标映射：与 TUI 的 StatusSegmentKind 保持一致（参考 chat_interaction.go）。
// 在 web 端用 Unicode 图标作为视觉锚，提升可扫描性。
var segIcons = {
  "balance": "💰",
  "context_used": "▣",
  "directory": "📁",
  "project": "📂",
  "git_branch": "⎇",
  "window": "□",
  "input_tokens": "→",
  "output_tokens": "←"
};

// 根据段类型返回图标，可用于悬浮标题提示。
function segIcon(kind) {
  return segIcons[kind] || "";
}

// 渲染状态栏：segments 数组 → span 段 + " · " 分隔。
// 当 segments 为空或 available=false，显示友好占位。
function renderStatusBar(snap) {
  if (!statusBarEl) { return; }

  // 清空
  statusBarEl.innerHTML = "";
  statusBarEl.className = "status-bar";
  statusBarEl.title = "";

  if (!snap || !snap.available) {
    statusBarEl.classList.add("empty");
    var hint = (snap && snap.reason) || "暂无状态信息";
    statusBarEl.textContent = hint;
    return;
  }

  var segments = snap.segments || [];
  if (segments.length === 0) {
    statusBarEl.classList.add("empty");
    statusBarEl.textContent = "（无可用状态段）";
    return;
  }

  // 构建 fullLine 作为整体悬浮标题
  if (snap.full_line) {
    statusBarEl.title = snap.full_line;
  }

  // 渲染每个段
  for (var i = 0; i < segments.length; i++) {
    var seg = segments[i];
    var kind = seg.kind || "";
    var text = seg.text || "";

    if (i > 0) {
      var sep = document.createElement("span");
      sep.className = "sep";
      sep.textContent = "·";
      statusBarEl.appendChild(sep);
    }

    var span = document.createElement("span");
    span.className = "seg kind-" + kind;
    span.setAttribute("data-seg-kind", kind);
    span.title = segIcon(kind) + " " + text;
    var icon = segIcon(kind);
    span.textContent = icon ? icon + " " + text : text;
    statusBarEl.appendChild(span);
  }

  // 隐藏刷新按钮的「未就绪」态
  if (refreshBtnEl) { refreshBtnEl.style.opacity = ""; }
}

// 拉取 /web/api/statusbar 并渲染。
export function loadStatusBar() {
  if (!statusBarEl) { return; }

  // 在请求期间显示轻度 busy 状态
  if (refreshBtnEl) { refreshBtnEl.style.opacity = "0.4"; }

  // 带超时（util.js::apiFetch，5s）：状态栏属于页面主壳，连接池被常驻事件流
  // 占满时它会永久排队（既不返回也不报错）。超时让它主动出队，并计入降级状态机
  // （§4.7），由 sse.js 显示横幅、转入轮询重试，连接空出后自动恢复。
  apiFetch("/web/api/statusbar", { cache: "no-store" }, 5000)
    .then(function (res) {
      if (!res.ok) { throw new Error("HTTP " + res.status); }
      return res.json();
    })
    .then(function (snap) {
      renderStatusBar(snap);
    })
    .catch(function (err) {
      // abort (超时)不视为错误，仅静默（降级提示统一由 sse.js 的横幅负责）
      if (err && err.name === "AbortError") { return; }
      statusBarEl.className = "status-bar empty";
      statusBarEl.title = "";
      statusBarEl.textContent = "状态栏加载失败：" + err;
    })
    .finally(function () {
      if (refreshBtnEl) { refreshBtnEl.style.opacity = ""; }
    });
}

// 初始化状态栏：绑定刷新按钮 + 首次拉取。
export function initStatusBar() {
  if (refreshBtnEl) {
    refreshBtnEl.addEventListener("click", function () {
      loadStatusBar();
    });
  }

  // Ctrl+R 在页面上通常是刷新整页，这里不捕获；
  // 刷新按钮的 title 提示用户点击刷新即可。

  loadStatusBar();
}

// 导出渲染函数，供 SSE 事件处理驱动刷新。
export { renderStatusBar };
