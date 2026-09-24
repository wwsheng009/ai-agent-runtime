// 顶部菜单栏:文件/视图/帮助 下拉菜单 + 会话导出下载。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。
//
// 设计要点:
//   1. 菜单项不重复实现业务逻辑,统一转发到既有控件或既有函数(点击
//      #sessions-new-btn / #sessions-refresh-btn / #sidebar-toggle / #theme-toggle /
//      页签按钮,调用 ui.js 的 toggleShortcutHelp),保证菜单与页签栏行为一致,
//      不产生第二条实现路径。
//   2. 导出走 GET /web/api/export(与 TUI /export、顶层 `aicli export` 同源:
//      同一份会话解析、格式归一化与写出实现),响应是 attachment,前端用
//      Blob + <a download> 落地文件;文件名优先取 Content-Disposition(服务端
//      与 CLI 默认命名同规则),取不到才用本地兜底名。
//   3. 菜单开合只做"单选展开":点击菜单按钮切换自身、点击其它菜单按钮切换过去、
//      点击外部或 Esc 关闭(把焦点还给菜单按钮)。

import { toggleShortcutHelp } from "./ui.js";
import { showToast } from "./util.js";

// data-menu-action → 导出格式。format 取值与 CLI 的格式词一致
// (full=完整 JSON / body=正文 / tools=正文+工具调用 / trace=正文+工具轨迹)。
var exportActions = {
  "export-full": { format: "full", extension: "json", label: "完整 JSON" },
  "export-body": { format: "body", extension: "md", label: "正文 Markdown" },
  "export-tools": { format: "tools", extension: "md", label: "正文 + 工具调用" },
  "export-trace": { format: "trace", extension: "md", label: "正文 + 工具轨迹" }
};

var menuRoots = [];
var menuItems = [];
var exportInFlight = false;

function queryAll(selector) {
  if (!document || typeof document.querySelectorAll !== "function") { return []; }
  try {
    return Array.prototype.slice.call(document.querySelectorAll(selector));
  } catch (e) {
    return [];
  }
}

function setMenuOpen(root, open) {
  if (!root || !root.classList) { return; }
  root.classList.toggle("open", !!open);
  var btn = root.querySelector ? root.querySelector(".menu-btn") : null;
  if (btn && btn.setAttribute) { btn.setAttribute("aria-expanded", open ? "true" : "false"); }
}

function closeAllMenus(focusButton) {
  var focused = false;
  menuRoots.forEach(function (root) {
    var wasOpen = !!(root.classList && root.classList.contains("open"));
    setMenuOpen(root, false);
    if (focusButton && wasOpen && !focused) {
      var btn = root.querySelector ? root.querySelector(".menu-btn") : null;
      if (btn && typeof btn.focus === "function") { btn.focus(); }
      focused = true;
    }
  });
}

// clickExisting 转发点击既有控件:菜单项与页签栏/侧栏按钮共用同一处理函数,
// 避免"菜单里能做、页签栏里行为不同"的双份逻辑。
function clickExisting(id) {
  var el = document.getElementById(id);
  if (el && typeof el.click === "function") { el.click(); }
}

function runExport(action) {
  var spec = exportActions[action];
  if (!spec) { return; }
  if (exportInFlight) { showToast("导出进行中，请稍候…", "busy", 1500); return; }
  exportInFlight = true;
  showToast("正在导出" + spec.label + "…", "busy", 1500);
  fetch("/web/api/export?format=" + encodeURIComponent(spec.format), { cache: "no-store" })
    .then(function (res) {
      if (!res.ok) { throw new Error("HTTP " + res.status); }
      var messages = res.headers.get("X-AICLI-Export-Messages") || "";
      return res.blob().then(function (blob) {
        return { blob: blob, name: exportFileName(res, spec.extension), messages: messages };
      });
    })
    .then(function (out) {
      downloadBlob(out.blob, out.name);
      showToast("已导出 " + out.name + (out.messages ? "（" + out.messages + " 条消息）" : ""), "ok", 4000);
    })
    .catch(function (err) {
      showToast("导出失败: " + (err && err.message ? err.message : err), "err", 4000);
    })
    .then(function () { exportInFlight = false; });
}

// exportFileName 解析服务端 Content-Disposition:优先 RFC 5987 的 filename*
//(会话 ID 可能含非 ASCII,服务端两种写法都会给出),回退 ASCII 名,最后才用兜底名。
function exportFileName(res, extension) {
  var disposition = "";
  try { disposition = res.headers.get("Content-Disposition") || ""; } catch (e) { disposition = ""; }
  var star = /filename\*=UTF-8''([^;]+)/i.exec(disposition);
  if (star && star[1]) {
    try { return decodeURIComponent(star[1].trim()); } catch (e) { /* 回退到 ASCII 名 */ }
  }
  var plain = /filename="([^"]+)"/i.exec(disposition);
  if (plain && plain[1]) { return plain[1].trim(); }
  return "aicli-session-export." + extension;
}

function downloadBlob(blob, name) {
  var url = URL.createObjectURL(blob);
  var link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.style.display = "none";
  document.body.appendChild(link);
  link.click();
  if (link.parentNode) { link.parentNode.removeChild(link); }
  // 立即 revoke 会让部分浏览器取消尚未开始的下载,延迟释放。
  setTimeout(function () { URL.revokeObjectURL(url); }, 10000);
}

function handleAction(action) {
  if (!action) { return; }
  if (exportActions[action]) { runExport(action); return; }
  switch (action) {
    case "session-new": clickExisting("sessions-new-btn"); break;
    case "session-refresh": clickExisting("sessions-refresh-btn"); break;
    case "sidebar-toggle": clickExisting("sidebar-toggle"); break;
    case "theme-toggle": clickExisting("theme-toggle"); break;
    case "shortcut-help": toggleShortcutHelp(); break;
    case "tab-main":
    case "tab-log":
    case "tab-debug":
    case "tab-about":
    case "tab-cache":
    case "tab-analysis": clickExisting(action + "-btn"); break;
    default: break;
  }
}

export function initMenu() {
  menuRoots = queryAll("#menu-bar .menu-root");
  menuItems = queryAll("#menu-bar .menu-item");
  menuRoots.forEach(function (root) {
    var btn = root.querySelector ? root.querySelector(".menu-btn") : null;
    if (!btn) { return; }
    btn.addEventListener("click", function (ev) {
      if (ev && ev.stopPropagation) { ev.stopPropagation(); }
      var open = !(root.classList && root.classList.contains("open"));
      closeAllMenus(false);
      setMenuOpen(root, open);
    });
  });
  // 先关菜单再执行动作:动作可能切换页签或打开面板,留着展开的菜单会挡住内容。
  menuItems.forEach(function (item) {
    item.addEventListener("click", function (ev) {
      if (ev && ev.stopPropagation) { ev.stopPropagation(); }
      closeAllMenus(false);
      handleAction(item.getAttribute("data-menu-action") || "");
    });
  });
  if (document && typeof document.addEventListener === "function") {
    document.addEventListener("click", function () { closeAllMenus(false); });
    document.addEventListener("keydown", function (ev) {
      if (ev && ev.key === "Escape") { closeAllMenus(true); }
    });
  }
}
