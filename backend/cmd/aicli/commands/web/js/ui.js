// 全局 UI 杂项:页签切换、深浅主题、快捷键帮助面板、页脚链接。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { refreshScreen } from "./chat.js";
import { loadConfigAdmin } from "./config-admin.js";
import { loadAnalysis, stopAnalysisAuto } from "./analysis.js";
import { loadCacheAnalytics, refreshCacheAnalytics } from "./cache.js";
import { loadDebugInfo, refreshDebugInfo } from "./debug.js";
import { loadFiles } from "./files.js";
import { loadGit } from "./git.js";
import { loadMCPs } from "./mcp.js";
import { loadSkills } from "./skills.js";
import { apiFetch, esc, showToast } from "./util.js";

var tabMainBtn = document.getElementById("tab-main-btn");
var tabLogBtn = document.getElementById("tab-log-btn");
var tabMainEl = document.getElementById("tab-main");
var tabLogEl = document.getElementById("tab-log");
var tabSkillsBtn = document.getElementById("tab-skills-btn");
var tabSkillsEl = document.getElementById("tab-skills");
var tabFilesBtn = document.getElementById("tab-files-btn");
var tabFilesEl = document.getElementById("tab-files");
var tabGitBtn = document.getElementById("tab-git-btn");
var tabGitEl = document.getElementById("tab-git");
var tabMCPBtn = document.getElementById("tab-mcp-btn");
var tabMCPEl = document.getElementById("tab-mcp");
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
var tabAnalysisBtn = document.getElementById("tab-analysis-btn");
var tabAnalysisEl = document.getElementById("tab-analysis");
var tabDebugBtn = document.getElementById("tab-debug-btn");
var tabDebugEl = document.getElementById("tab-debug");
var tabAboutBtn = document.getElementById("tab-about-btn");
var tabAboutEl = document.getElementById("tab-about");
var aboutEndpointsEl = document.getElementById("about-endpoints");
var aboutMeshEl = document.getElementById("about-mesh");
var aboutTokenValueEl = document.getElementById("about-token-value");
// 当前会话 ID（值由 sessions.js 写入，见 updateSessionIdentity）
var aboutSessionIDEl = document.getElementById("about-session-id");

function activateTab(tabName) {
  var isMain = tabName === "main";
  var isSkills = tabName === "skills";
  var isFiles = tabName === "files";
  var isGit = tabName === "git";
  var isMCP = tabName === "mcp";
  var isLog = tabName === "log";
  var isConfig = tabName === "config";
  var isCache = tabName === "cache";
  var isAnalysis = tabName === "analysis";
  var isDebug = tabName === "debug";
  var isAbout = tabName === "about";
  tabMainBtn.classList.toggle("active", isMain);
  if (tabSkillsBtn) { tabSkillsBtn.classList.toggle("active", isSkills); }
  if (tabFilesBtn) { tabFilesBtn.classList.toggle("active", isFiles); }
  if (tabGitBtn) { tabGitBtn.classList.toggle("active", isGit); }
  if (tabMCPBtn) { tabMCPBtn.classList.toggle("active", isMCP); }
  tabLogBtn.classList.toggle("active", isLog);
  if (tabConfigBtn) { tabConfigBtn.classList.toggle("active", isConfig); }
  if (tabCacheBtn) { tabCacheBtn.classList.toggle("active", isCache); }
  if (tabAnalysisBtn) { tabAnalysisBtn.classList.toggle("active", isAnalysis); }
  if (tabDebugBtn) { tabDebugBtn.classList.toggle("active", isDebug); }
  if (tabAboutBtn) { tabAboutBtn.classList.toggle("active", isAbout); }
  tabMainEl.classList.toggle("active", isMain);
  if (tabSkillsEl) { tabSkillsEl.classList.toggle("active", isSkills); }
  if (tabFilesEl) { tabFilesEl.classList.toggle("active", isFiles); }
  if (tabGitEl) { tabGitEl.classList.toggle("active", isGit); }
  if (tabMCPEl) { tabMCPEl.classList.toggle("active", isMCP); }
  tabLogEl.classList.toggle("active", isLog);
  if (tabConfigEl) { tabConfigEl.classList.toggle("active", isConfig); }
  if (tabCacheEl) { tabCacheEl.classList.toggle("active", isCache); }
  if (tabAnalysisEl) { tabAnalysisEl.classList.toggle("active", isAnalysis); }
  if (tabDebugEl) { tabDebugEl.classList.toggle("active", isDebug); }
  if (tabAboutEl) { tabAboutEl.classList.toggle("active", isAbout); }
  if (isMain) { refreshScreen(); }
  // 技能页签：目录来自当前会话的 Function Catalog（与 TUI /skills 同源），
  // 首次进入或显式刷新才拉取，同会话重复切页签不重复发请求。
  if (isSkills) { loadSkills(); }
  // 文件页签：作用域根与目录列表属于当前会话的工作目录，首次进入或会话变化
  // 才拉取（同一会话内切回页签沿用已有列表，手动刷新见工具栏 ⟳）。
  if (isFiles) { loadFiles(); }
  // GIT 页签：状态按「进入即重拉」处理（工作区随时被外部命令改变），
  // 一次 status + 一页 commits 足够轻量。
  if (isGit) { loadGit(); }
  // MCP 页签：管理本地 mcp.yaml（进程级，非会话数据），首次激活懒加载，之后每次
  // 进入都重拉一次，保证列表反映外部改动与服务端热重载后的最新状态。
  if (isMCP) { loadMCPs(); }
  if (isConfig) { loadConfigAdmin(); }
  // 会话感知按需拉取：cache.js 内部对比已渲染数据与当前会话 id，仅在首次进入、
  // 会话变化时重拉；同会话重复切页签不重复发请求（页内更新由 SSE 增量刷新兜底）。
  if (isCache) { loadCacheAnalytics(); }
  // 分析页签同约定：数据属于当前会话，仅首次进入/会话变化时拉取；离开页签
  // 关闭自动刷新，避免后台页签空转（页内刷新按钮见 js/analysis.js initAnalysis）。
  if (isAnalysis) { loadAnalysis(); } else { stopAnalysisAuto(); }
  // 调试页签的快照是拉取时刻的后端状态（无 SSE 增量），每次进入都重拉一次。
  if (isDebug) { loadDebugInfo(); }
  // 关于页签的端点清单同样按「进入即重拉」处理：清单由服务端渲染，
  // 与 /debug display 区块同源，新增端点无需改前端。
  if (isAbout) { loadAboutEndpoints(); loadAboutMesh(); }
}

// ---- 关于页签：远程调用端点清单（数据源 GET /debug/endpoints?format=json） ----
var aboutEndpointsLoading = false;

// ---- 关于页签：写令牌显示（来源：页面注入 meta，回退 GET /web/api/token） ----
//
// 优先读 meta：与自动注入 fetch 包装用的是同一个值，零额外请求、无失败面；
// meta 缺失（例如手工用其它服务器托管静态页）才回退到服务端端点。
export function initAboutToken() {
  if (!aboutTokenValueEl) { return; }
  // 优先 meta（**当前**进程注入的权威值），回退 sessionStorage 缓存：
  // 反序会在进程重启换随机令牌后把上一个进程的旧令牌显示在「关于」页。
  var meta = document.querySelector('meta[name="aicli-web-token"]');
  var token = meta && meta.content ? String(meta.content).trim() : "";
  if (!token) {
    try { token = sessionStorage.getItem('aicli-web-token') || ""; } catch (e) { /* ignore */ }
  }
  if (token) {
    aboutTokenValueEl.textContent = token;
  } else {
    aboutTokenValueEl.textContent = "（不可用）";
    // 带超时（util.js::apiFetch）：这是页面主壳里的初始化请求，连接池被常驻
    // 事件流占满时不能永久排队（失败时保留「不可用」占位，不打扰用户）。
    apiFetch("/web/api/token", { cache: "no-store" })
      .then(function (res) { return res.json(); })
      .then(function (data) {
        aboutTokenValueEl.textContent = (data && data.token) ? String(data.token) : "（不可用）";
      })
      .catch(function () { /* 保留「不可用」占位 */ });
  }
  bindAboutCopyButton(document.getElementById("about-token-copy"), aboutTokenValueEl, "（不可用）", "写令牌已复制");
}

// ---- 关于页签：当前会话 ID 复制（值来自 sessions.js 的会话身份同步） ----
// 未选择会话时元素为占位文案，不复制（避免把「（未选择会话）」贴到终端）。
export function initAboutSessionCopy() {
  bindAboutCopyButton(document.getElementById("about-session-copy"), aboutSessionIDEl, "（未选择会话）", "会话 ID 已复制");
}

// bindAboutCopyButton：关于页签「值 + 复制按钮」共用交互（写令牌 / 当前会话 ID）。
// 空值、占位文案、无剪贴板权限统一按失败提示，不做静默降级。
function bindAboutCopyButton(btn, valueEl, placeholder, okText) {
  if (!btn || !valueEl) { return; }
  btn.addEventListener("click", function () {
    var text = (valueEl.textContent || "").trim();
    if (!text || text === placeholder || !navigator.clipboard) {
      showToast("复制失败", "error");
      return;
    }
    navigator.clipboard.writeText(text).then(function () {
      showToast(okText, "ok");
    }).catch(function () {
      showToast("复制失败", "error");
    });
  });
}

// 端点分组顺序：web（本次会话的远程调用 API）优先，其后为 loopback 调试与观察平面。
var aboutEndpointGroups = [
  { scheme: "web", label: "web  (微型 Web 客户端 / 远程调用 API)", baseKey: "web_base_url" },
  { scheme: "loopback", label: "loopback  (aicli --pprof 本机调试服务器)", baseKey: "loopback_base_url" },
  { scheme: "runtime-observe", label: "runtime-observe  (Runtime Observation Plane)", baseKey: "observe_base_url" }
];

// 写操作（Method 含 POST）需要 X-AICLI-Token；GET 只受 Host/Origin 校验保护。
function aboutEndpointNeedsAuth(info) {
  return String(info && info.method || "").toUpperCase().indexOf("POST") >= 0;
}

// renderAboutEndpoints 把 /debug/endpoints JSON 渲染为分组清单 HTML。
// 纯字符串拼接（不做 DOM 操作），所有服务端文本一律 esc() 转义。
export function renderAboutEndpoints(snap) {
  if (!snap || snap.available === false) {
    var reason = snap && snap.reason ? snap.reason : "no active chat session";
    return '<span class="about-endpoints-dim">端点清单不可用：' + esc(reason) + "</span>";
  }
  var list = Array.isArray(snap.endpoints) ? snap.endpoints : [];
  if (!list.length) {
    return '<span class="about-endpoints-dim">端点清单为空。</span>';
  }
  var authHeader = String(snap.write_auth_header || "X-AICLI-Token").trim();
  var html = "";
  aboutEndpointGroups.forEach(function (group) {
    var items = list.filter(function (info) { return info && info.scheme === group.scheme; });
    if (!items.length) { return; }
    html += '<div class="about-endpoints-group">';
    html += '<div class="about-endpoints-group-title">' + esc(group.label) + "</div>";
    var base = String(snap[group.baseKey] || (group.scheme === "web" ? snap.base_url : "") || "").trim();
    if (base) {
      html += '<div class="about-endpoints-base">Base: ' + esc(base) + "</div>";
    } else if (group.scheme === "runtime-observe") {
      html += '<div class="about-endpoints-base">Base: &lt;route-only&gt;</div>';
    }
    items.forEach(function (info) {
      var auth = aboutEndpointNeedsAuth(info);
      html += '<div class="about-endpoints-row">';
      html += '<span class="about-endpoints-method">' + esc(info.method || "GET") + "</span>";
      html += '<span class="about-endpoints-path">' + esc(info.path || "") + "</span>";
      if (auth) {
        html += '<span class="about-endpoints-auth" title="写操作需要 ' + esc(authHeader) + '">需令牌</span>';
      }
      if (info.enabled === false) {
        html += '<span class="about-endpoints-dim">[disabled]</span>';
      }
      if (info.note) {
        html += '<span class="about-endpoints-note">' + esc(info.note) + "</span>";
      }
      html += "</div>";
    });
    html += "</div>";
  });
  if (String(snap.write_auth_hint || "").trim()) {
    html += '<div class="about-endpoints-base">Auth: ' + esc(String(snap.write_auth_hint).trim()) + "</div>";
  }
  return html;
}

export function loadAboutEndpoints() {
  if (!aboutEndpointsEl || aboutEndpointsLoading) { return; }
  aboutEndpointsLoading = true;
  fetchAboutEndpoints(0);
}

// 进程刚启动时可出现「HTTP 服务已就绪、chat 会话尚未绑定」的窗口，
// 此时清单返回 available=false；做有限次重试，避免关于页停在不可用状态。
var aboutEndpointsRetries = 4;
var aboutEndpointsRetryDelayMs = 800;

function fetchAboutEndpoints(attempt) {
  fetch("/debug/endpoints?format=json", { cache: "no-store" })
    .then(function (res) { return res.json(); })
    .then(function (snap) {
      aboutEndpointsEl.innerHTML = renderAboutEndpoints(snap);
      if (snap && snap.available === false && attempt < aboutEndpointsRetries) {
        setTimeout(function () { fetchAboutEndpoints(attempt + 1); }, aboutEndpointsRetryDelayMs);
        return;
      }
      aboutEndpointsLoading = false;
    })
    .catch(function (err) {
      var msg = err && err.message ? err.message : String(err);
      aboutEndpointsEl.innerHTML = '<span class="about-endpoints-dim">端点清单加载失败：' + esc(msg) + "</span>";
      aboutEndpointsLoading = false;
    });
}

// ---- 关于页签：网格小节（§5.8，只读）----
//
// 数据源：GET /web/api/mesh/self（档案 + derived + mesh 自描述）与
// GET /web/api/mesh/peers（counts 恒全量口径）。两者都是只读端点、默认脱敏；
// 治理动作（gc / stop / spawn）留在 CLI，本页不提供按钮（§8 R13）。
var aboutMeshLoading = false;

export function loadAboutMesh() {
  if (!aboutMeshEl || aboutMeshLoading) { return; }
  aboutMeshLoading = true;
  fetch("/web/api/mesh/self", { cache: "no-store" })
    .then(function (res) { return res.json(); })
    .then(function (self) {
      if (!self || self.available === false) {
        aboutMeshEl.innerHTML = '<span class="about-endpoints-dim">网格未启用：' +
          esc(self && self.reason ? self.reason : "mesh disabled") + "</span>";
        aboutMeshLoading = false;
        return null;
      }
      // counts 只有 peers 视图给（self 段不含）：peers 失败不影响本节点摘要渲染。
      return fetch("/web/api/mesh/peers", { cache: "no-store" })
        .then(function (res) { return res.json(); })
        .catch(function () { return null; })
        .then(function (peers) {
          aboutMeshEl.innerHTML = renderAboutMesh(self, peers);
          aboutMeshLoading = false;
        });
    })
    .catch(function (err) {
      var msg = err && err.message ? err.message : String(err);
      aboutMeshEl.innerHTML = '<span class="about-endpoints-dim">网格信息加载失败：' + esc(msg) + "</span>";
      aboutMeshLoading = false;
    });
}

// renderAboutMesh 把 self（+peers.counts）渲染为只读清单 HTML。
// 纯字符串拼接，所有服务端文本一律 esc() 转义；不含令牌原文（§5.5 红线 3）。
export function renderAboutMesh(self, peers) {
  if (!self || self.available === false) {
    return '<span class="about-endpoints-dim">网格未启用：' +
      esc(self && self.reason ? self.reason : "mesh disabled") + "</span>";
  }
  var mesh = self.mesh || {};
  var derived = self.derived || {};
  var workspace = self.workspace || {};
  var rows = [];
  function pushRow(label, value) {
    var text = String(value || "").trim();
    if (!text) { return; }
    rows.push('<div class="about-endpoints-row">' +
      '<span class="about-endpoints-method">' + esc(label) + "</span>" +
      '<span class="about-endpoints-path">' + esc(text) + "</span></div>");
  }
  var node = String(self.node_id || "").trim();
  if (self.pid) { node = (node ? node + " · " : "") + "pid " + self.pid; }
  pushRow("节点", node);
  var wsText = String(workspace.name || "").trim();
  var wsPath = String(workspace.path || "").trim();
  if (wsPath) { wsText = (wsText ? wsText + " " : "") + "(" + wsPath + ")"; }
  pushRow("工作区", wsText);
  pushRow("网格根", mesh.enabled === false ? "未启用" : mesh.root);
  pushRow("journal", mesh.journal);
  if (peers && peers.counts) {
    var counts = peers.counts;
    pushRow("节点计数", "live " + (counts.live || 0) + " · stale " + (counts.stale || 0) +
      " · conflict " + (counts.conflict || 0));
  }
  if (derived.lease || derived.busy || derived.pending_inputs) {
    var d = "归属 " + (derived.lease || "none");
    if (derived.busy) { d += " · 忙碌"; }
    if (derived.pending_inputs) { d += " · 排队 " + derived.pending_inputs; }
    if (derived.peer_count) { d += " · 对端 live " + derived.peer_count; }
    pushRow("会话", d);
  }
  pushRow("建议命令", "aicli-mesh ls --probe · aicli-mesh gc · aicli-mesh show <session>");
  if (!rows.length) {
    return '<span class="about-endpoints-dim">网格档案暂不可读。</span>';
  }
  return rows.join("");
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
  if (tabFilesBtn) { tabFilesBtn.addEventListener("click", function () { activateTab("files"); }); }
  if (tabGitBtn) { tabGitBtn.addEventListener("click", function () { activateTab("git"); }); }
  if (tabMCPBtn) { tabMCPBtn.addEventListener("click", function () { activateTab("mcp"); }); }
  tabLogBtn.addEventListener("click", function () { activateTab("log"); });
  if (tabConfigBtn) { tabConfigBtn.addEventListener("click", function () { activateTab("config"); }); }
  if (tabCacheBtn) { tabCacheBtn.addEventListener("click", function () { activateTab("cache"); }); }
  if (tabAnalysisBtn) { tabAnalysisBtn.addEventListener("click", function () { activateTab("analysis"); }); }
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

// 底部栏不再渲染 /debug/endpoints 与 /web/ 链接（原 initFooter 已移除）：
// 底部只保留 js/statusbar.js 的状态段；调试端点入口见「关于」页签与
// GET /debug/endpoints。
