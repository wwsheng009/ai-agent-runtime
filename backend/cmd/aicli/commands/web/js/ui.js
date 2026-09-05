// 全局 UI 杂项:页签切换、深浅主题、快捷键帮助面板、页脚链接。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { refreshScreen } from "./chat.js";
import { loadConfigAdmin } from "./config-admin.js";

var tabMainBtn = document.getElementById("tab-main-btn");
var tabLogBtn = document.getElementById("tab-log-btn");
var tabMainEl = document.getElementById("tab-main");
var tabLogEl = document.getElementById("tab-log");
var themeToggleBtn = document.getElementById("theme-toggle");
var shortcutHelpEl = document.getElementById("shortcut-help");
// ---- 主题：深色/浅色（localStorage 记忆，默认跟随系统） ----
var themeMode = "auto"; // auto | light | dark
try {
  var savedTheme = localStorage.getItem("webTheme");
  if (savedTheme === "light" || savedTheme === "dark" || savedTheme === "auto") { themeMode = savedTheme; }
} catch (e) { /* ignore */ }

function applyTheme() {
  var root = document.documentElement;
  // 显式深/浅：设置 class；auto：跟随系统（由 CSS prefers-color-scheme 决定）
  root.classList.toggle("light", themeMode === "light");
  root.classList.toggle("dark", themeMode === "dark");
  if (themeToggleBtn) {
    themeToggleBtn.textContent = themeMode === "light" ? "☀" : (themeMode === "dark" ? "☾" : "◐");
    themeToggleBtn.title = "主题: " + themeMode + "（点击切换，Ctrl+L）";
  }
}

export function toggleTheme() {
  // auto → light → dark → auto 循环
  themeMode = themeMode === "auto" ? "light" : (themeMode === "light" ? "dark" : "auto");
  try { localStorage.setItem("webTheme", themeMode); } catch (e) { /* ignore */ }
  applyTheme();
}

applyTheme();

var tabConfigBtn = document.getElementById("tab-config-btn");
var tabConfigEl = document.getElementById("tab-config");

function activateTab(tabName) {
  var isMain = tabName === "main";
  var isLog = tabName === "log";
  var isConfig = tabName === "config";
  tabMainBtn.classList.toggle("active", isMain);
  tabLogBtn.classList.toggle("active", isLog);
  if (tabConfigBtn) { tabConfigBtn.classList.toggle("active", isConfig); }
  tabMainEl.classList.toggle("active", isMain);
  tabLogEl.classList.toggle("active", isLog);
  if (tabConfigEl) { tabConfigEl.classList.toggle("active", isConfig); }
  if (isMain) { refreshScreen(); }
  if (isConfig) { loadConfigAdmin(); }
}

// ---- 快捷键帮助面板切换 ----
export function toggleShortcutHelp() {
  if (!shortcutHelpEl) { return; }
  var show = shortcutHelpEl.style.display !== "block";
  shortcutHelpEl.style.display = show ? "block" : "none";
}

// Esc 优先关闭快捷键帮助面板;返回是否发生了关闭(调用方据此短路)。
export function closeShortcutHelpIfOpen() {
  if (shortcutHelpEl && shortcutHelpEl.style.display === "block") {
    toggleShortcutHelp();
    return true;
  }
  return false;
}

export function initTabs() {
  tabMainBtn.addEventListener("click", function () { activateTab("main"); });
  tabLogBtn.addEventListener("click", function () { activateTab("log"); });
  if (tabConfigBtn) { tabConfigBtn.addEventListener("click", function () { activateTab("config"); }); }

}

export function initTheme() {
  // ---- 主题切换按钮 ----
  if (themeToggleBtn) {
    themeToggleBtn.addEventListener("click", function () { toggleTheme(); });
  }

}

export function initShortcutHelp() {
  // 快捷键帮助面板
  if (shortcutHelpEl) {
    shortcutHelpEl.querySelector(".shortcut-close").addEventListener("click", function () { toggleShortcutHelp(); });
    shortcutHelpEl.querySelector(".shortcut-overlay").addEventListener("click", function () { toggleShortcutHelp(); });
  }
}

export function initFooter() {
  // 底部栏显示完整 URL
  var origin = window.location.origin;
  document.getElementById("footer-endpoints").innerHTML = '<a href="' + origin + '/debug/endpoints" target="_blank" rel="noopener">' + origin + '/debug/endpoints</a>';
  document.getElementById("footer-web").innerHTML = '<a href="' + origin + '/web/" target="_blank" rel="noopener">' + origin + '/web/</a>';

}
