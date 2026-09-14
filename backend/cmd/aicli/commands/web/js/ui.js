// 全局 UI 杂项:页签切换、深浅主题、快捷键帮助面板、页脚链接。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { refreshScreen } from "./chat.js";
import { loadConfigAdmin } from "./config-admin.js";
import { loadCacheAnalytics, refreshCacheAnalytics } from "./cache.js";
import { loadDebugInfo, refreshDebugInfo } from "./debug.js";
import { loadSkills } from "./skills.js";

var tabMainBtn = document.getElementById("tab-main-btn");
var tabLogBtn = document.getElementById("tab-log-btn");
var tabMainEl = document.getElementById("tab-main");
var tabLogEl = document.getElementById("tab-log");
var tabSkillsBtn = document.getElementById("tab-skills-btn");
var tabSkillsEl = document.getElementById("tab-skills");
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
var tabCacheBtn = document.getElementById("tab-cache-btn");
var tabCacheEl = document.getElementById("tab-cache");
var tabDebugBtn = document.getElementById("tab-debug-btn");
var tabDebugEl = document.getElementById("tab-debug");
var tabAboutBtn = document.getElementById("tab-about-btn");
var tabAboutEl = document.getElementById("tab-about");

function activateTab(tabName) {
  var isMain = tabName === "main";
  var isSkills = tabName === "skills";
  var isLog = tabName === "log";
  var isConfig = tabName === "config";
  var isCache = tabName === "cache";
  var isDebug = tabName === "debug";
  var isAbout = tabName === "about";
  tabMainBtn.classList.toggle("active", isMain);
  if (tabSkillsBtn) { tabSkillsBtn.classList.toggle("active", isSkills); }
  tabLogBtn.classList.toggle("active", isLog);
  if (tabConfigBtn) { tabConfigBtn.classList.toggle("active", isConfig); }
  if (tabCacheBtn) { tabCacheBtn.classList.toggle("active", isCache); }
  if (tabDebugBtn) { tabDebugBtn.classList.toggle("active", isDebug); }
  if (tabAboutBtn) { tabAboutBtn.classList.toggle("active", isAbout); }
  tabMainEl.classList.toggle("active", isMain);
  if (tabSkillsEl) { tabSkillsEl.classList.toggle("active", isSkills); }
  tabLogEl.classList.toggle("active", isLog);
  if (tabConfigEl) { tabConfigEl.classList.toggle("active", isConfig); }
  if (tabCacheEl) { tabCacheEl.classList.toggle("active", isCache); }
  if (tabDebugEl) { tabDebugEl.classList.toggle("active", isDebug); }
  if (tabAboutEl) { tabAboutEl.classList.toggle("active", isAbout); }
  if (isMain) { refreshScreen(); }
  // 技能页签：目录来自当前会话的 Function Catalog（与 TUI /skills 同源），
  // 首次进入或显式刷新才拉取，同会话重复切页签不重复发请求。
  if (isSkills) { loadSkills(); }
  if (isConfig) { loadConfigAdmin(); }
  // 会话感知按需拉取：cache.js 内部对比已渲染数据与当前会话 id，仅在首次进入、
  // 会话变化时重拉；同会话重复切页签不重复发请求（页内更新由 SSE 增量刷新兜底）。
  if (isCache) { loadCacheAnalytics(); }
  // 调试页签的快照是拉取时刻的后端状态（无 SSE 增量），每次进入都重拉一次。
  if (isDebug) { loadDebugInfo(); }
  // 关于页签为静态内容，无需拉取。
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
  if (tabSkillsBtn) { tabSkillsBtn.addEventListener("click", function () { activateTab("skills"); }); }
  tabLogBtn.addEventListener("click", function () { activateTab("log"); });
  if (tabConfigBtn) { tabConfigBtn.addEventListener("click", function () { activateTab("config"); }); }
  if (tabCacheBtn) { tabCacheBtn.addEventListener("click", function () { activateTab("cache"); }); }
  if (tabDebugBtn) { tabDebugBtn.addEventListener("click", function () { activateTab("debug"); }); }
  if (tabAboutBtn) { tabAboutBtn.addEventListener("click", function () { activateTab("about"); }); }
  var cacheRefreshBtn = document.getElementById("cache-refresh-btn");
  if (cacheRefreshBtn) { cacheRefreshBtn.addEventListener("click", function () { refreshCacheAnalytics(); }); }
  var debugRefreshBtn = document.getElementById("debug-refresh-btn");
  if (debugRefreshBtn) { debugRefreshBtn.addEventListener("click", function () { refreshDebugInfo(); }); }
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
