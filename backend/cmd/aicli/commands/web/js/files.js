// 「文件」页签：当前会话工作目录的文件管理器。
// 数据源 /web/api/fs/*（与 runtime-server 的 /api/runtime/fs/* 同源：internal/filebrowse
// + internal/fsscope），因此页面看到的作用域、路径与越界校验与 TUI/后端完全一致。
//
// 作用域纪律（与 frontend 项目 fs-roots.ts 的 pickDefaultRoot/pickPreviewRoot 同约定）：
// scope 一律用 /web/api/fs/roots 返回的根对象里的 scope **原样回传**，前端不拼
// "session:"/"workspace:" 字面量；后端没给根就如实报错，不猜测、不回退。
// aicli micro web client 前端模块(无构建步骤,由 app.js 入口聚合)。

import { apiFetch, esc, showToast } from "./util.js";
import { renderMarkdown } from "./markdown.js";
import { createSplitPane } from "./splitpane.js";

// 请求序号：丢弃会话切换、连续点击或刷新竞态下迟到的过期响应。
var filesSeq = 0;
var previewSeq = 0;
var loaded = false;
// 会话感知（与 skills.js / cache.js 同约定）：目录属于当前会话的工作目录，
// 切换会话后旧列表不再代表当前会话，需按会话 id 判定过期。
var currentSessionID = "";
var loadedSessionID = "";

// ---- 浏览状态 ----
var roots = [];             // /web/api/fs/roots 返回的根列表（原样保留）
var browseRoot = null;      // 当前选中的根
var browsePath = "";        // 作用域相对路径（POSIX；"" = 根目录）
var browseSort = "type_then_name";
var browseShowHidden = false;
var browseEntries = [];     // 已渲染条目（分页累加）
var browseCursor = "";      // 下一页游标
var browseHasMore = false;
var searchMode = false;     // 搜索结果视图（与目录浏览互斥）

function filesEl(id) { return document.getElementById(id); }

// filesAPI：带超时的同源请求（util.js::apiFetch），返回 {status, body}，
// 网络层失败由调用方按 error 渲染，不吞异常、不伪造数据。
function filesAPI(path, init) {
  return apiFetch(path, init || { cache: "no-store" }).then(function (res) {
    return res.json().then(function (body) {
      return { status: res.status, body: body };
    }, function () {
      return { status: res.status, body: null };
    });
  });
}

// filesError 取后端统一错误体 {"error":{"code","message"}}。
function filesError(body) {
  if (body && body.error) { return body.error; }
  return null;
}

// ---- 会话同步 ----

export function syncFilesSession(sessionID) {
  var next = sessionID || "";
  if (next === currentSessionID) { return; }
  currentSessionID = next;
  if (!loaded) { return; }                  // 尚未加载过：进入页签时自然拉取
  if (loadedSessionID === next) { return; } // 已渲染的目录属于该会话
  resetBrowseState();
  resetFileTabs();
  refreshFilesIfActive();
}

function resetBrowseState() {
  browseRoot = null;
  browsePath = "";
  browseEntries = [];
  browseCursor = "";
  browseHasMore = false;
  searchMode = false;
}

// ---- 加载入口 ----

export function loadFiles() {
  // 首次进入页签，或已渲染目录属于其它会话才拉取；同一会话内重复切页签不重复发请求。
  if (loaded && loadedSessionID === currentSessionID && browseRoot) { return; }
  loaded = true;
  refreshFiles();
}

export function refreshFilesIfActive() {
  var panel = filesEl("tab-files");
  if (!panel || !panel.classList.contains("active")) { return; }
  refreshFiles();
}

// refreshFiles：重拉作用域根并列出当前目录（根变化时回到根目录）。
export function refreshFiles() {
  var listEl = filesEl("files-list");
  if (!listEl) { return; }
  var seq = ++filesSeq;
  loadedSessionID = currentSessionID;
  setStatus("");
  listEl.innerHTML = '<div class="files-empty">加载中…</div>';
  filesAPI("/web/api/fs/roots").then(function (result) {
    if (seq !== filesSeq) { return; }
    if (result.status !== 200) {
      renderFilesError(filesError(result.body), result.status);
      return;
    }
    roots = (result.body && result.body.roots) || [];
    renderRootOptions();
    var picked = pickRoot(browseRoot);
    if (!picked) {
      renderFilesError({ code: "fs_no_root", message: "当前会话没有可浏览的工作目录" }, 0);
      return;
    }
    if (!browseRoot || picked.scope !== browseRoot.scope) { browsePath = ""; }
    browseRoot = picked;
    searchMode = false;
    loadDirectory(browsePath, false);
  }).catch(function () {
    if (seq !== filesSeq) { return; }
    renderFilesError(null, 0);
  });
}

// pickRoot 选根纪律：优先沿用当前根（会话内切换目录不跳根），否则取会话根
// （kind==="session"，即当前 aicli 会话的工作目录），再退到第一个存在的根。
function pickRoot(preferred) {
  if (preferred) {
    for (var i = 0; i < roots.length; i++) {
      if (roots[i] && roots[i].scope === preferred.scope) { return roots[i]; }
    }
  }
  for (var j = 0; j < roots.length; j++) {
    if (roots[j] && roots[j].kind === "session" && roots[j].exists !== false) { return roots[j]; }
  }
  for (var k = 0; k < roots.length; k++) {
    if (roots[k] && roots[k].exists !== false) { return roots[k]; }
  }
  return roots.length ? roots[0] : null;
}

// loadDirectory 列出**一层**目录（cursor 非空时为追加下一页）。
function loadDirectory(path, append) {
  var listEl = filesEl("files-list");
  if (!listEl || !browseRoot) { return; }
  var seq = ++filesSeq;
  var query = "/web/api/fs/list?scope=" + encodeURIComponent(browseRoot.scope)
    + "&path=" + encodeURIComponent(path || "")
    + "&sort=" + encodeURIComponent(browseSort)
    + "&show_hidden=" + (browseShowHidden ? "1" : "0")
    + "&dirs_first=1";
  if (append && browseCursor) { query += "&cursor=" + encodeURIComponent(browseCursor); }
  if (!append) { listEl.innerHTML = '<div class="files-empty">加载中…</div>'; }

  filesAPI(query).then(function (result) {
    if (seq !== filesSeq) { return; }
    if (result.status !== 200) {
      renderFilesError(filesError(result.body), result.status);
      return;
    }
    var body = result.body || {};
    browsePath = (body.dir && typeof body.dir.path === "string") ? body.dir.path : (path || "");
    browseEntries = append ? browseEntries.concat(body.entries || []) : (body.entries || []);
    browseCursor = body.next_cursor || "";
    browseHasMore = !!body.has_more;
    searchMode = false;
    renderList();
    renderPath();
    setStatus(body.truncated ? "结果被截断（目录条目过多）" : "");
  }).catch(function () {
    if (seq !== filesSeq) { return; }
    renderFilesError(null, 0);
  });
}

// ---- 渲染 ----

function renderRootOptions() {
  var select = filesEl("files-root-select");
  if (!select) { return; }
  var options = [];
  for (var i = 0; i < roots.length; i++) {
    var item = roots[i] || {};
    var label = (item.kind || "root") + ": " + (item.path || item.scope || "");
    if (item.exists === false) { label += "（不可用）"; }
    options.push('<option value="' + esc(item.scope || "") + '">' + esc(label) + "</option>");
  }
  select.innerHTML = options.join("");
  if (browseRoot) { select.value = browseRoot.scope; }
  select.disabled = roots.length < 2;
}

function renderPath() {
  var pathEl = filesEl("files-path");
  if (!pathEl) { return; }
  var segments = (browsePath || "").split("/").filter(function (part) { return part !== ""; });
  var html = ['<button class="files-crumb" type="button" data-crumb="">/</button>'];
  var acc = "";
  for (var i = 0; i < segments.length; i++) {
    acc = acc ? acc + "/" + segments[i] : segments[i];
    html.push('<span class="files-crumb-sep">/</span>');
    html.push('<button class="files-crumb" type="button" data-crumb="' + esc(acc) + '">' + esc(segments[i]) + "</button>");
  }
  pathEl.innerHTML = html.join("");
  var crumbs = pathEl.querySelectorAll(".files-crumb");
  for (var j = 0; j < crumbs.length; j++) {
    crumbs[j].addEventListener("click", function () {
      var target = this.getAttribute("data-crumb") || "";
      loadDirectory(target, false);
    });
  }
  var upBtn = filesEl("files-up-btn");
  if (upBtn) { upBtn.disabled = !browsePath; }
}

function renderList() {
  var listEl = filesEl("files-list");
  var countEl = filesEl("files-count");
  if (!listEl) { return; }
  if (countEl) {
    countEl.textContent = browseEntries.length + " 项" + (browseHasMore ? "（还有更多）" : "");
  }
  if (!browseEntries.length) {
    listEl.innerHTML = '<div class="files-empty">空目录</div>';
    return;
  }
  var rows = [];
  for (var i = 0; i < browseEntries.length; i++) {
    rows.push(renderEntryRow(browseEntries[i]));
  }
  if (browseHasMore) {
    rows.push('<button id="files-more-btn" class="files-more" type="button">加载更多…</button>');
  }
  listEl.innerHTML = rows.join("");
  var rowEls = listEl.querySelectorAll(".files-row");
  for (var j = 0; j < rowEls.length; j++) {
    rowEls[j].addEventListener("click", function () {
      var path = this.getAttribute("data-path") || "";
      var type = this.getAttribute("data-type") || "";
      if (type === "dir") { loadDirectory(path, false); } else { openFile(path); }
    });
  }
  var moreBtn = filesEl("files-more-btn");
  if (moreBtn) {
    moreBtn.addEventListener("click", function () { loadDirectory(browsePath, true); });
  }
}

function renderEntryRow(entry) {
  var type = entry && entry.type ? String(entry.type) : "file";
  var path = entry && entry.path ? String(entry.path) : "";
  var name = entry && entry.name ? String(entry.name) : path;
  var isDir = type === "dir";
  var icon = isDir ? "📁" : filesIcon(entry && entry.ext);
  var meta = [];
  if (!isDir) { meta.push(formatSize(entry && entry.size)); }
  meta.push(formatMtime(entry && entry.mtime));
  if (entry && entry.is_symlink) { meta.push("symlink"); }
  return '<button class="files-row" type="button" data-path="' + esc(path) + '" data-type="' + esc(type) + '">'
    + '<span class="files-icon">' + icon + "</span>"
    + '<span class="files-name">' + esc(name) + "</span>"
    + '<span class="files-meta">' + esc(meta.join(" · ")) + "</span>"
    + "</button>";
}

function filesIcon(ext) {
  var value = (ext || "").toLowerCase();
  if (value === ".go" || value === ".js" || value === ".ts" || value === ".py" || value === ".rs") { return "📜"; }
  if (value === ".json" || value === ".yaml" || value === ".yml" || value === ".toml") { return "⚙"; }
  if (value === ".md" || value === ".txt") { return "📄"; }
  if (value === ".png" || value === ".jpg" || value === ".jpeg" || value === ".gif" || value === ".webp") { return "🖼"; }
  return "📄";
}

function renderFilesError(err, status) {
  var code = err && err.code ? err.code : "network_error";
  var msg = err && err.message ? err.message : "";
  var listEl = filesEl("files-list");
  var countEl = filesEl("files-count");
  if (countEl) { countEl.textContent = "加载失败"; }
  if (listEl) {
    listEl.innerHTML = '<div class="files-empty files-error">加载失败: ' + esc(code)
      + (msg ? " — " + esc(msg) : "") + (status ? "（HTTP " + status + "）" : "") + "</div>";
  }
}

function setStatus(text) {
  var el = filesEl("files-status");
  if (el) { el.textContent = text || ""; }
}

// ---- 二级页签（目录浏览 + 每个打开的文件一个页签）----
// 与技能详情页签同约定：role=tablist + roving tabindex（选中 0、其余 -1）+ aria-selected，
// ←/→ 环绕、Home/End 首尾。差别在于文件页签是**动态增删**的：打开文件即「查找同路径页签，
// 命中则复用、否则新建」（openFile），Delete 或页签上的 ✕ 关闭。选中态只就地更新 class/aria，
// 不重建按钮——重建会丢焦点，也会丢掉面板里已经渲染好的内容。页签顺序 = 打开顺序（新的追加
// 在末尾，与编辑器一致）。

var fileTabs = [];            // [{key,scope,path,name,ext,mode,state,preview,error,loadSeq,tabEl,paneEl}]
var fileTabSeq = 0;           // 页签 id 序号（files-tab-N / files-pane-N）；只增不减，避免 id 复用
var FILE_TAB_MAX = 12;        // 同时打开的页签上限（超出关闭最早打开的，见 enforceFileTabLimit）
var activeFilesTab = "browse";

// ---- 大屏左右分栏（编辑器式布局）----
// ≥900px 时「文件」页签内部改成左栏文件浏览器 + 右栏打开的文件页签（几何见 style.css 的
// 同名媒体查询）。折叠/宽度拖拽这套交互与「GIT」页签完全一致，统一由 js/splitpane.js
// 提供；这里只保留文件页签特有的对账（浏览面板的 hidden、右栏空态、页签栏收起），
// 通过 onApply 回调在每次分栏对账后执行。
var FILES_SPLIT_QUERY = "(min-width: 900px)";
var FILES_SIDE_KEY = "webFilesSideCollapsed"; // localStorage 键，与 webSidebarCollapsed 同套路
var FILES_SIDE_WIDTH_KEY = "webFilesSideWidth"; // localStorage：左栏宽度（px），客户端偏好
var FILES_SIDE_DEFAULT = 300; // 默认宽度 = 双击复位值（与 style.css 的 300px 对齐）
var FILES_SIDE_MIN = 200;     // 最小宽度（与 style.css 的 min-width 对齐）
var FILES_SIDE_MAX = 640;     // 最大宽度（容器更窄时按容器算）
var FILES_MAIN_MIN = 320;     // 拖拽/键盘调整时给右侧编辑器至少留出的宽度
var FILES_SIDE_STEP = 24;     // 键盘 ←/→ 每步调整的像素

var filesPane = createSplitPane({
  layoutId: "files-layout",
  sideId: "files-side",
  toggleId: "files-side-toggle",
  splitterId: "files-side-splitter",
  splitQuery: FILES_SPLIT_QUERY,
  widthVar: "--files-side-width",
  widthKey: FILES_SIDE_WIDTH_KEY,
  collapsedKey: FILES_SIDE_KEY,
  defaultWidth: FILES_SIDE_DEFAULT,
  minWidth: FILES_SIDE_MIN,
  maxWidth: FILES_SIDE_MAX,
  mainMin: FILES_MAIN_MIN,
  step: FILES_SIDE_STEP,
  collapseTitle: "折叠文件浏览器（左栏）",
  expandTitle: "展开文件浏览器（左栏）",
  onApply: syncFilesLayout,
  onBreakpoint: applyFilesLayout
});

// extOf 取小写扩展名（含点）；无扩展名返回 ""。
function extOf(path) {
  var name = String(path || "").split("/").pop() || "";
  var dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(dot).toLowerCase() : "";
}

// isMarkdownPreview：后端按扩展名给出 text/markdown（internal/filebrowse mimeByExt），
// 拿不到 mime 时退回扩展名判断，保证 .md 不因 mime 缺失而退化成纯文本。
function isMarkdownPreview(preview, path) {
  if (!preview || preview.kind !== "text") { return false; }
  var mime = String(preview.mime || "").toLowerCase();
  if (mime.indexOf("text/markdown") === 0) { return true; }
  var ext = extOf(path);
  return ext === ".md" || ext === ".markdown";
}

function findFileTab(scope, path) {
  for (var i = 0; i < fileTabs.length; i++) {
    if (fileTabs[i].scope === scope && fileTabs[i].path === path) { return fileTabs[i]; }
  }
  return null;
}

function findFileTabByKey(key) {
  for (var i = 0; i < fileTabs.length; i++) {
    if (fileTabs[i].key === key) { return fileTabs[i]; }
  }
  return null;
}

// filesTabButtons 页签按钮（含「目录浏览」）：键盘导航按 DOM 顺序走，所以从容器里查，
// 不用 fileTabs 数组——DOM 才是可聚焦顺序的唯一来源。
function filesTabButtons() {
  var tabsEl = filesEl("files-tabs");
  if (!tabsEl || !tabsEl.querySelectorAll) { return []; }
  return tabsEl.querySelectorAll("button[data-files-tab]");
}

// tabKeyOf 页签按钮 → 选中键："browse" 或文件页签的 key。
function tabKeyOf(button) {
  if (!button) { return ""; }
  if (button.getAttribute("data-files-tab") !== "file") { return "browse"; }
  return button.getAttribute("data-files-key") || "";
}

// applyFilesLayout 是文件页签的统一对账入口：先做「进入大屏时自动选中最后打开的文件」
// 这个一次性动作，再把折叠态 / 宽度 / 折叠按钮交给 js/splitpane.js——它随后回调
// syncFilesLayout 把文件页签特有的部分做完。
// 进入大屏时若还停在「目录浏览」（该页签在大屏被隐藏），直接选中最后打开的文件——
// 窗口变宽后右栏应该是编辑器内容，而不是一句「请打开文件」。
function applyFilesLayout() {
  if (filesPane.wantsSplit() && !filesPane.isSplit() && activeFilesTab === "browse" && fileTabs.length) {
    // 走 selectFilesTab 而不是直接改 activeFilesTab：选中态（class/aria-selected/tabindex）
    // 得一起跟上，否则页签栏里会出现「可见的文件页签 aria-selected=false 且 tabindex=-1」
    // 这种键盘进不去、读屏也不认的悬空状态。它会回到这里把剩下的对账做完。
    selectFilesTab(fileTabs[fileTabs.length - 1].key, false);
    return;
  }
  filesPane.apply();
}

// syncFilesLayout 对账文件页签特有的可见性与语义（几何在 CSS，折叠/宽度在 splitpane）：
//   1) 浏览面板：大屏常显且语义改成 region（不再挂在被隐藏的「目录浏览」页签上），
//      窄屏回到 tabpanel 语义与「仅选中时可见」；
//   2) 文件面板 hidden 始终跟着 activeFilesTab 走（大屏也一样，右栏只显示选中的那个）；
//   3) 右栏空态：大屏且没选中文件时显示引导，并把空页签栏一并收起（不留一条空条）；
//   4) 页签栏收起时（没打开任何文件）浏览面板也不能挂 tabpanel 语义：唯一的页签被
//      display:none 后 aria-labelledby 指向读不出名字的元素，降级成 region + aria-label；
//   5) 左栏控件（.files-side-tools）跟着浏览面板走——它只作用于那份目录列表，面板不可见时
//      一起收起（CSS 侧是 .files-layout.browse-hidden）。
function syncFilesLayout(nowSplit) {
  var layout = filesEl("files-layout");
  // 没打开任何文件 → 页签栏整条收起（只剩「目录浏览」一条、没得可切，白占一行）。
  var tabsHidden = !fileTabs.length;
  var browsePane = filesEl("files-pane-browse");
  if (browsePane) {
    browsePane.hidden = !nowSplit && activeFilesTab !== "browse";
    if (nowSplit || tabsHidden) {
      browsePane.setAttribute("role", "region");
      browsePane.setAttribute("aria-label", "文件浏览器");
      browsePane.removeAttribute("aria-labelledby");
    } else {
      browsePane.setAttribute("role", "tabpanel");
      browsePane.setAttribute("aria-labelledby", "files-tab-browse");
      browsePane.removeAttribute("aria-label");
    }
  }
  if (layout && layout.classList) {
    layout.classList.toggle("browse-hidden", !!(browsePane && browsePane.hidden));
  }
  for (var i = 0; i < fileTabs.length; i++) {
    if (fileTabs[i].paneEl) { fileTabs[i].paneEl.hidden = fileTabs[i].key !== activeFilesTab; }
  }
  var main = filesEl("files-main");
  if (main && main.classList) {
    main.classList.toggle("no-file-tabs", tabsHidden);
  }
  var empty = filesEl("files-main-empty");
  if (empty) { empty.hidden = !(nowSplit && activeFilesTab === "browse"); }
}

// selectFilesTab 切换二级页签：就地更新选中态 + 面板可见性，不重建、不重新请求。
// 面板可见性统一交给 applyFilesLayout（大屏下浏览面板是常显左栏，不吃这里的 hidden）。
function selectFilesTab(key, focus) {
  if (key !== "browse" && !findFileTabByKey(key)) { return; }
  activeFilesTab = key;
  var buttons = filesTabButtons();
  for (var i = 0; i < buttons.length; i++) {
    var on = tabKeyOf(buttons[i]) === key;
    if (buttons[i].classList) { buttons[i].classList.toggle("active", on); }
    buttons[i].setAttribute("aria-selected", on ? "true" : "false");
    buttons[i].setAttribute("tabindex", on ? "0" : "-1");
    if (on && focus && buttons[i].focus) { buttons[i].focus(); }
  }
  applyFilesLayout();
}

// moveFilesTab 键盘在页签间移动：只走**可见**页签——大屏下「目录浏览」被左栏取代
// （display:none），若还算进环绕，←/→ 会选中一个看不见的页签、右栏莫名其妙变空。
function moveFilesTab(mode) {
  var all = filesTabButtons();
  var buttons = [];
  for (var k = 0; k < all.length; k++) {
    if (all[k].offsetParent === null) { continue; }
    buttons.push(all[k]);
  }
  if (!buttons.length) { return; }
  var index = -1;
  for (var i = 0; i < buttons.length; i++) {
    if (tabKeyOf(buttons[i]) === activeFilesTab) { index = i; break; }
  }
  if (index < 0) { index = 0; }
  var last = buttons.length - 1;
  if (mode === "ArrowRight") { index = index === last ? 0 : index + 1; }
  else if (mode === "ArrowLeft") { index = index === 0 ? last : index - 1; }
  else if (mode === "Home") { index = 0; }
  else if (mode === "End") { index = last; }
  else { return; }
  selectFilesTab(tabKeyOf(buttons[index]), true);
  ensureTabVisible(buttons[index]);
}

// ensureTabVisible 把新建/聚焦的页签滚进可视区：只动页签栏的 scrollLeft，
// 不用 scrollIntoView——那会连带滚动整个页面。
function ensureTabVisible(button) {
  var bar = button && button.parentNode;
  if (!bar || !bar.getBoundingClientRect) { return; }
  var b = button.getBoundingClientRect(), r = bar.getBoundingClientRect();
  if (b.left < r.left) { bar.scrollLeft -= (r.left - b.left) + 8; }
  else if (b.right > r.right) { bar.scrollLeft += (b.right - r.right) + 8; }
}

// ---- 打开文件（查找或新建页签）----

// openFile 把文件打开到二级页签：同 scope+path 已有页签就复用（重新读取，md|txt 保留），
// 否则新建页签 + 面板。页签**立即**出现（加载中），读取失败也留在页签里如实显示——
// 「打开」这个动作已经发生，把页签悄悄撤掉比留一个写着失败原因的页签更让人迷惑。
function openFile(path) {
  if (!browseRoot || !path) { return; }
  var scope = browseRoot.scope;
  var tab = findFileTab(scope, path);
  if (!tab) { tab = createFileTab(scope, path); }
  if (!tab) { return; }
  selectFilesTab(tab.key, true);
  ensureTabVisible(tab.tabEl);
  loadFileTab(tab);
}

function createFileTab(scope, path) {
  var tabsEl = filesEl("files-tabs");
  var panesEl = filesEl("files-panes");
  if (!tabsEl || !panesEl) { return null; }
  var seq = ++fileTabSeq;
  var tab = {
    key: String(seq),
    scope: scope,
    path: path,
    name: path.split("/").pop() || path,
    ext: extOf(path),
    mode: "md",        // Markdown 文件默认渲染；非 Markdown 文件不使用该状态
    state: "loading",  // loading | ready | error
    preview: null,
    error: null,
    loadSeq: 0,
    tabEl: null,
    paneEl: null
  };
  var button = document.createElement("button");
  button.id = "files-tab-" + seq;
  button.className = "files-tab";
  button.type = "button";
  button.setAttribute("role", "tab");
  button.setAttribute("data-files-tab", "file");
  button.setAttribute("data-files-key", tab.key);
  button.setAttribute("aria-selected", "false");
  button.setAttribute("aria-controls", "files-pane-" + seq);
  button.setAttribute("tabindex", "-1");
  button.title = path + "（Delete 关闭页签）";
  // 关闭按钮只能是 span：button 里再嵌 button 是非法 HTML（浏览器会把内层拆掉），
  // 所以鼠标走 span 上的点击委托，键盘走 Delete（见 initFiles 的页签栏键盘处理）。
  button.innerHTML = '<span class="files-tab-icon">' + filesIcon(tab.ext) + "</span>"
    + '<span class="files-tab-name">' + esc(tab.name) + "</span>"
    + '<span class="files-tab-close" aria-hidden="true" title="关闭">✕</span>';
  tabsEl.appendChild(button);

  var pane = document.createElement("div");
  pane.id = "files-pane-" + seq;
  pane.className = "files-pane files-view";
  pane.setAttribute("role", "tabpanel");
  pane.setAttribute("aria-labelledby", "files-tab-" + seq);
  pane.hidden = true;
  pane.innerHTML = fileTabShell(tab);
  pane.addEventListener("click", function (event) { onFileTabClick(tab, event); });
  panesEl.appendChild(pane);

  tab.tabEl = button;
  tab.paneEl = pane;
  fileTabs.push(tab);
  enforceFileTabLimit(tab);
  return tab;
}

// fileTabShell 面板骨架：头部（文件名/路径/操作按钮）+ meta 行 + 正文容器。
// 骨架只建一次，meta 与正文由 updateFileTab 按状态填。
function fileTabShell(tab) {
  return '<div class="files-view-head">'
    + '<span class="files-view-name">' + esc(tab.name) + "</span>"
    + '<span class="files-view-path" title="' + esc(tab.path) + '">' + esc(tab.path) + "</span>"
    + '<span class="config-toolbar-spacer"></span>'
    + '<button class="files-view-render" type="button" data-file-action="render" hidden>md</button>'
    + '<button class="files-view-reload" type="button" data-file-action="reload" title="重新读取该文件（GET /web/api/fs/preview）">⟳ 重读</button>'
    + '<button class="files-view-download" type="button" data-file-action="download" title="下载该文件（GET /web/api/fs/download）">⤓ 下载</button>'
    + "</div>"
    + '<div class="files-view-meta"></div>'
    + '<div class="files-view-body"></div>';
}

// onFileTabClick 面板内的按钮与代码块复制：一个委托覆盖两者（面板是动态创建的，
// 委托挂在面板自身，不依赖 #conversation 上那份——那份只覆盖对话区）。
function onFileTabClick(tab, event) {
  var target = event.target;
  if (!target || !target.closest) { return; }
  var copyBtn = target.closest(".copy-code-btn");
  if (copyBtn) { copyCodeBlock(copyBtn); return; }
  var btn = target.closest("button[data-file-action]");
  if (!btn || btn.disabled) { return; }
  var action = btn.getAttribute("data-file-action");
  if (action === "render") {
    tab.mode = tab.mode === "text" ? "md" : "text";
    updateFileTab(tab); // 只重绘，不重新请求
    return;
  }
  if (action === "reload") { loadFileTab(tab); return; }
  if (action === "download") { downloadCurrent(tab.scope, tab.path); }
}

// loadFileTab 拉取 /fs/preview 并渲染进面板。请求序号挂在页签上：连续「重读」时迟到的
// 响应不写入面板（会话切换时 resetFileTabs 统一作废，见 previewSeq）。
function loadFileTab(tab) {
  if (!tab || !tab.paneEl) { return; }
  var seq = ++previewSeq;
  tab.loadSeq = seq;
  tab.state = "loading";
  updateFileTab(tab);
  var url = "/web/api/fs/preview?scope=" + encodeURIComponent(tab.scope)
    + "&path=" + encodeURIComponent(tab.path);
  filesAPI(url).then(function (result) {
    if (tab.loadSeq !== seq) { return; }
    if (result.status !== 200) {
      var err = filesError(result.body);
      tab.state = "error";
      tab.preview = null;
      tab.error = {
        code: (err && err.code) || "network_error",
        message: (err && err.message) || "",
        status: result.status
      };
      updateFileTab(tab);
      return;
    }
    tab.state = "ready";
    tab.error = null;
    tab.preview = result.body || {};
    updateFileTab(tab);
  }).catch(function () {
    if (tab.loadSeq !== seq) { return; }
    tab.state = "error";
    tab.preview = null;
    tab.error = { code: "network_error", message: "", status: 0 };
    updateFileTab(tab);
  });
}

// updateFileTab 按页签状态重绘面板（加载中 / 成功 / 失败）。首次打开、md|txt 切换与
// 「⟳ 重读」共用这一条渲染路径。
function updateFileTab(tab) {
  var pane = tab && tab.paneEl;
  if (!pane) { return; }
  var metaEl = pane.querySelector(".files-view-meta");
  var bodyEl = pane.querySelector(".files-view-body");
  var renderBtn = pane.querySelector(".files-view-render");
  var reloadBtn = pane.querySelector(".files-view-reload");
  var downloadBtn = pane.querySelector(".files-view-download");
  var preview = tab.preview;
  var markdown = tab.state === "ready" && isMarkdownPreview(preview, tab.path);
  if (reloadBtn) { reloadBtn.disabled = tab.state === "loading"; }
  if (downloadBtn) { downloadBtn.disabled = tab.state !== "ready"; }
  setFileTabRenderButton(renderBtn, markdown, tab.mode);

  if (tab.state === "loading") {
    if (metaEl) { metaEl.textContent = "加载中…"; }
    if (bodyEl) {
      bodyEl.className = "files-view-body";
      bodyEl.innerHTML = '<div class="files-empty">加载中…</div>';
    }
    return;
  }
  if (tab.state === "error") {
    if (metaEl) { metaEl.textContent = "读取失败"; }
    if (bodyEl) {
      bodyEl.className = "files-view-body";
      bodyEl.innerHTML = '<div class="files-empty files-error">打开失败: ' + esc(tab.error.code)
        + (tab.error.message ? " — " + esc(tab.error.message) : "")
        + (tab.error.status ? "（HTTP " + esc(String(tab.error.status)) + "）" : "")
        + "；可点「⟳ 重读」重试。</div>";
    }
    return;
  }
  if (metaEl) { metaEl.textContent = fileMetaText(preview); }
  if (!bodyEl) { return; }
  switch (preview.kind) {
    case "text":
      if (markdown && tab.mode !== "text") {
        // 复用对话区同款精简 Markdown 解析器（输出已转义的安全 HTML，见 js/markdown.js）。
        bodyEl.className = "files-view-body files-view-md";
        bodyEl.innerHTML = renderMarkdown(typeof preview.text === "string" ? preview.text : "")
          || '<div class="files-empty">（Markdown 文档为空，或只含空白字符）</div>';
      } else {
        bodyEl.className = "files-view-body files-view-text";
        bodyEl.textContent = typeof preview.text === "string" ? preview.text : "";
      }
      break;
    case "image":
      bodyEl.className = "files-view-body files-view-image";
      bodyEl.innerHTML = preview.data_base64
        ? '<img alt="' + esc(tab.path) + '" src="data:' + esc(preview.mime || "image/png") + ";base64," + preview.data_base64 + '">'
        : "（图片内容为空）";
      break;
    case "too_large":
      bodyEl.className = "files-view-body";
      bodyEl.textContent = "文件过大，未做预览"
        + (preview.reason ? "（" + preview.reason + "）" : "") + "；可点「⤓ 下载」取回完整内容。";
      break;
    default:
      bodyEl.className = "files-view-body";
      bodyEl.textContent = "二进制文件，不支持预览"
        + (preview.reason ? "（" + preview.reason + "）" : "") + "；可点「⤓ 下载」取回。";
      break;
  }
}

function fileMetaText(preview) {
  var meta = [];
  if (preview.mime) { meta.push(preview.mime); }
  meta.push(formatSize(preview.size));
  meta.push(formatMtime(preview.mtime));
  meta.push("kind: " + (preview.kind || "unknown"));
  if (preview.truncated) { meta.push("已截断" + (preview.reason ? "（" + preview.reason + "）" : "")); }
  return meta.join(" · ");
}

// setFileTabRenderButton：只有 Markdown 文件才显示 md|txt 切换，其它 kind 一律隐藏
// （不给用户一个点了没反应的开关）。按钮文字即当前方式，与对话区 md|txt 同一约定。
function setFileTabRenderButton(btn, markdown, mode) {
  if (!btn) { return; }
  btn.hidden = !markdown;
  var value = mode === "text" ? "text" : "md";
  btn.textContent = value;
  if (btn.classList) { btn.classList.toggle("active", value === "md"); }
  btn.title = value === "md" ? "当前：Markdown 渲染（点击切到原文）" : "当前：原文（点击切到 Markdown 渲染）";
}

// closeFileTab 关闭页签：移除按钮与面板、丢弃该文件的状态（磁盘文件不动）。
// 关掉当前选中的页签时，选中态交给左邻（没有左邻就回「目录浏览」），焦点跟着走，
// 避免焦点留在已被移除的按钮上。
function closeFileTab(tab, focusNeighbor) {
  if (!tab) { return; }
  var index = fileTabs.indexOf(tab);
  var wasActive = activeFilesTab === tab.key;
  if (tab.tabEl && tab.tabEl.parentNode) { tab.tabEl.parentNode.removeChild(tab.tabEl); }
  if (tab.paneEl && tab.paneEl.parentNode) { tab.paneEl.parentNode.removeChild(tab.paneEl); }
  tab.tabEl = null;
  tab.paneEl = null;
  if (index >= 0) { fileTabs.splice(index, 1); }
  if (!wasActive) {
    // 关掉的不是当前页签：选中态不动，但页签栏/右栏空态要跟着数量重新对账。
    applyFilesLayout();
    return;
  }
  var next = fileTabs[index - 1] || null;
  selectFilesTab(next ? next.key : "browse", !!focusNeighbor);
  if (next && focusNeighbor) { ensureTabVisible(next.tabEl); }
}

// closeFileTabsAround 批量关闭文件页签（右键菜单的「关闭其它 / 左侧 / 右侧 / 全部」共用）。
// mode 决定关哪一批：others = 除目标外的全部、left/right = 目标左/右侧的全部、all = 全部。
// targetKey 是右键点中的页签：「目录浏览」按位置 -1 算（恒在所有文件页签左侧），
// 而且它**永远不参与关闭**——「关闭左侧」在它身上天然无对象。
// 选中态：被关掉的那批里有当前页签时，选中目标页签（「关闭全部」回「目录浏览」），
// 焦点跟着走——否则焦点会留在已被移除的按钮上。
function closeFileTabsAround(mode, targetKey, focus) {
  var index = -1;
  for (var i = 0; i < fileTabs.length; i++) {
    if (fileTabs[i].key === targetKey) { index = i; break; }
  }
  var victims = [];
  for (var j = 0; j < fileTabs.length; j++) {
    if (mode === "all") { victims.push(fileTabs[j]); continue; }
    if (mode === "others" && fileTabs[j].key !== targetKey) { victims.push(fileTabs[j]); continue; }
    if (mode === "left" && j < index) { victims.push(fileTabs[j]); continue; }
    if (mode === "right" && j > index) { victims.push(fileTabs[j]); }
  }
  if (!victims.length) { return; }
  var wasActive = false;
  for (var k = 0; k < victims.length; k++) {
    if (victims[k].key === activeFilesTab) { wasActive = true; }
  }
  // 逐个关（每个都自己收尾：移除按钮/面板、从 fileTabs 摘掉、必要时重新对账）。
  for (var m = 0; m < victims.length; m++) { closeFileTab(victims[m], false); }
  if (!wasActive) { return; } // 当前页签没被关：选中态不动，不抢用户的上下文
  var keep = mode === "all" ? null : findFileTabByKey(targetKey);
  selectFilesTab(keep ? keep.key : "browse", !!focus);
  if (keep && focus) { ensureTabVisible(keep.tabEl); }
}

// enforceFileTabLimit 同时打开的页签上限：超限时关掉最早打开的页签（新建的这个除外），
// 并如实提示——静默丢弃用户刚打开的文件比多留一个页签更糟。
// 两轮挑牺牲者：先跳过**当前正在看**的页签（正对着的这份内容被抽走最粗暴，而腾位置这件事
// 挑哪个都行），只剩它可选时才退回去关它——上限是硬约束，宁可关掉看得见的，也不能超。
function enforceFileTabLimit(keepTab) {
  if (fileTabs.length <= FILE_TAB_MAX) { return; }
  var victim = null;
  for (var pass = 0; pass < 2 && !victim; pass++) {
    for (var i = 0; i < fileTabs.length; i++) {
      var candidate = fileTabs[i];
      if (candidate === keepTab) { continue; }
      if (pass === 0 && candidate.key === activeFilesTab) { continue; }
      victim = candidate;
      break;
    }
  }
  if (!victim) { return; }
  showToast("同时打开的页签已达 " + FILE_TAB_MAX + " 个，已关闭最早打开的「" + victim.name + "」", "error");
  closeFileTab(victim, false);
}

// resetFileTabs 会话切换即关闭全部文件页签：页签里的路径属于上一个作用域根
// （session 根在新会话里可能指向别处，甚至不存在），留着只会让人点到读不出来的文件。
function resetFileTabs() {
  previewSeq++; // 在途的预览响应作废，不再写入已被移除的面板
  for (var i = 0; i < fileTabs.length; i++) {
    if (fileTabs[i].tabEl && fileTabs[i].tabEl.parentNode) { fileTabs[i].tabEl.parentNode.removeChild(fileTabs[i].tabEl); }
    if (fileTabs[i].paneEl && fileTabs[i].paneEl.parentNode) { fileTabs[i].paneEl.parentNode.removeChild(fileTabs[i].paneEl); }
    fileTabs[i].tabEl = null;
    fileTabs[i].paneEl = null;
  }
  fileTabs = [];
  selectFilesTab("browse", false);
}

// ---- 页签右键菜单 ----
// 页签多起来之后逐个点 ✕ 太慢：右键（或键盘 Shift+F10 / 菜单键）在页签上弹一个小菜单，
// 一次关掉其它 / 左侧 / 右侧 / 全部。菜单项就是普通 button（Enter/Space 即激活），
// ↑↓ 在**可用**项之间环绕、Home/End 到首尾；Esc、点击菜单外、滚动、窗口尺寸变化一律收起。
// 「目录浏览」是固定页签：关闭类动作对它一律禁用（它在最左侧，所以「关闭左侧」也禁用）。
var filesMenuKey = "";      // 菜单打开时对应的页签键（"browse" 或文件页签 key）
var filesMenuOpened = false;

function filesTabMenuEl() { return filesEl("files-tab-menu"); }

// filesTabMenuItems 菜单项（DOM 顺序 = 视觉顺序 = ↑↓ 顺序）。
function filesTabMenuItems() {
  var menu = filesTabMenuEl();
  if (!menu || !menu.querySelectorAll) { return []; }
  return menu.querySelectorAll(".tab-menu-item");
}

// filesTabMenuFlags 各项菜单动作在当前页签上是否可用。计数只数**文件页签**：
// 「目录浏览」既不能被关，也不能被当成某个页签的「左侧页签」关掉。
function filesTabMenuFlags(key) {
  var index = -1; // 「目录浏览」= -1：它在所有文件页签左侧
  for (var i = 0; i < fileTabs.length; i++) {
    if (fileTabs[i].key === key) { index = i; break; }
  }
  var left = 0, right = 0;
  for (var j = 0; j < fileTabs.length; j++) {
    if (j < index) { left++; } else if (j > index) { right++; }
  }
  return {
    close: key !== "browse",
    others: key !== "browse" && fileTabs.length > 1,
    left: left > 0,
    right: right > 0,
    all: fileTabs.length > 0
  };
}

// openFilesTabMenu 在 (x, y) 处弹出菜单并锚定到 button。x/y 缺省（键盘触发，没有指针坐标）
// 时贴按钮左下角。几何：先撤 hidden 再量（display:none 量出来全是 0），贴右/下边时翻到
// 锚点内侧，最后夹进视口——菜单不会被切掉一半，也不会跑到窗口外。
function openFilesTabMenu(button, x, y) {
  var menu = filesTabMenuEl();
  if (!menu || !button) { return; }
  var key = tabKeyOf(button);
  filesMenuKey = key;
  var flags = filesTabMenuFlags(key);
  var items = filesTabMenuItems();
  var first = null;
  for (var i = 0; i < items.length; i++) {
    var on = !!flags[items[i].getAttribute("data-menu-action")];
    items[i].disabled = !on;
    if (on) { items[i].removeAttribute("aria-disabled"); } else { items[i].setAttribute("aria-disabled", "true"); }
    if (on && !first) { first = items[i]; }
  }
  menu.hidden = false;
  filesMenuOpened = true;
  if (typeof x !== "number" || typeof y !== "number") {
    var box = button.getBoundingClientRect ? button.getBoundingClientRect() : null;
    if (box) { x = box.left; y = box.bottom + 2; }
  }
  if (typeof x !== "number") { x = 0; }
  if (typeof y !== "number") { y = 0; }
  var rect = menu.getBoundingClientRect ? menu.getBoundingClientRect() : { width: 0, height: 0 };
  var margin = 8;
  if (x + rect.width > (window.innerWidth || 0) - margin) { x = x - rect.width; }
  if (y + rect.height > (window.innerHeight || 0) - margin) { y = y - rect.height; }
  menu.style.left = Math.round(Math.max(margin, x)) + "px";
  menu.style.top = Math.round(Math.max(margin, y)) + "px";
  if (first && first.focus) { first.focus(); } // 打开即入菜单：键盘用户不用先按 Tab
}

// closeFilesTabMenu 收起菜单。restoreFocus=true 时把焦点还给触发它的页签按钮
// （Esc / Tab / 收起但没执行动作）；执行了关闭动作就交给选中的新页签，不还。
function closeFilesTabMenu(restoreFocus) {
  if (!filesMenuOpened) { return false; }
  var menu = filesTabMenuEl();
  if (menu) { menu.hidden = true; }
  filesMenuOpened = false;
  var key = filesMenuKey;
  filesMenuKey = "";
  if (restoreFocus && key) { focusFilesTabButton(key); }
  return true;
}

// focusFilesTabButton 把焦点还给某个页签按钮。按钮可能已经不在了（刚被关掉），或整条页签栏
// 已收起（没有打开任何文件）——两种情况都不硬点：聚焦一个 display:none 的按钮只会静默失败，
// 不如什么都不做。
function focusFilesTabButton(key) {
  var buttons = filesTabButtons();
  for (var i = 0; i < buttons.length; i++) {
    if (tabKeyOf(buttons[i]) !== key) { continue; }
    if (buttons[i].offsetParent === null) { return; }
    if (buttons[i].focus) { buttons[i].focus(); }
    return;
  }
}

// runFilesTabMenuAction 执行菜单项。先收起菜单再动手：关闭动作会把目标页签（连带触发它的
// 那个按钮）从 DOM 里删掉，那时再谈「把焦点还给它」已经没有意义。
function runFilesTabMenuAction(action) {
  var key = filesMenuKey;
  closeFilesTabMenu(false);
  if (!key || !action) { return; }
  if (action === "close") {
    var tab = findFileTabByKey(key);
    if (tab) { closeFileTab(tab, true); }
    return;
  }
  closeFileTabsAround(action, key, true);
}

// moveFilesTabMenuFocus 在**可用**项之间移动焦点（↑↓ 环绕、Home/End 到首尾）。
// 禁用项直接跳过：它们没有可执行的动作，停在那里只会让人以为菜单坏了。
function moveFilesTabMenuFocus(mode) {
  var items = filesTabMenuItems();
  var usable = [];
  for (var i = 0; i < items.length; i++) {
    if (!items[i].disabled) { usable.push(items[i]); }
  }
  if (!usable.length) { return; }
  var current = -1;
  for (var j = 0; j < usable.length; j++) {
    if (usable[j] === document.activeElement) { current = j; }
  }
  var next = 0;
  if (mode === "last") { next = usable.length - 1; }
  else if (mode === "prev") { next = current < 0 ? usable.length - 1 : (current - 1 + usable.length) % usable.length; }
  else if (mode === "next") { next = current < 0 ? 0 : (current + 1) % usable.length; }
  if (usable[next] && usable[next].focus) { usable[next].focus(); }
}

// ---- 下载 ----

// downloadCurrent 下载文件（浏览器侧 blob 下载）。scope 由调用方给出：文件页签可能是在
// 另一个根下打开的（用户之后又切了根），不能拿当前的 browseRoot 顶替。
function downloadCurrent(scope, path) {
  if (!scope || !path) { return; }
  var url = "/web/api/fs/download?scope=" + encodeURIComponent(scope)
    + "&path=" + encodeURIComponent(path);
  // 大文件不设超时（apiFetch 语义：0 = 不超时），失败如实提示。
  apiFetch(url, { cache: "no-store" }, 0).then(function (res) {
    if (!res.ok) {
      showToast("下载失败（HTTP " + res.status + "）", "error");
      return null;
    }
    return res.blob();
  }).then(function (blob) {
    if (!blob) { return; }
    var objectURL = URL.createObjectURL(blob);
    var link = document.createElement("a");
    link.href = objectURL;
    link.download = path.split("/").pop() || "download";
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);
    URL.revokeObjectURL(objectURL);
  }).catch(function () {
    showToast("下载失败", "error");
  });
}

// ---- 搜索 ----

function runSearch(query) {
  var listEl = filesEl("files-list");
  if (!listEl || !browseRoot) { return; }
  query = (query || "").trim();
  if (!query) { loadDirectory(browsePath, false); return; }
  var seq = ++filesSeq;
  searchMode = true;
  listEl.innerHTML = '<div class="files-empty">搜索中…</div>';
  var url = "/web/api/fs/search?scope=" + encodeURIComponent(browseRoot.scope)
    + "&q=" + encodeURIComponent(query)
    + "&path=" + encodeURIComponent(browsePath)
    + "&limit=200&show_hidden=" + (browseShowHidden ? "1" : "0");
  filesAPI(url).then(function (result) {
    if (seq !== filesSeq) { return; }
    if (result.status !== 200) {
      renderFilesError(filesError(result.body), result.status);
      return;
    }
    renderSearchResults(query, result.body || {});
  }).catch(function () {
    if (seq !== filesSeq) { return; }
    renderFilesError(null, 0);
  });
}

function renderSearchResults(query, body) {
  var listEl = filesEl("files-list");
  var countEl = filesEl("files-count");
  var items = body.items || [];
  if (countEl) { countEl.textContent = "匹配 " + items.length + " 项（扫描 " + (body.scanned || 0) + "）"; }
  setStatus(body.truncated ? "搜索被截断: " + ((body.truncated_reason || []).join(", ")) : "");
  if (!items.length) {
    listEl.innerHTML = '<div class="files-empty">没有匹配 “' + esc(query) + '” 的条目</div>';
    return;
  }
  var rows = ['<div class="files-search-head">搜索: ' + esc(query) + "</div>"];
  for (var i = 0; i < items.length; i++) {
    var item = items[i] || {};
    var type = item.type === "dir" ? "dir" : "file";
    rows.push('<button class="files-row" type="button" data-path="' + esc(item.path || "") + '" data-type="' + type + '">'
      + '<span class="files-icon">' + (type === "dir" ? "📁" : filesIcon(item.ext)) + "</span>"
      + '<span class="files-name">' + esc(item.path || item.name || "") + "</span>"
      + '<span class="files-meta">' + esc(formatSize(item.size) + " · " + formatMtime(item.mtime)) + "</span>"
      + "</button>");
  }
  listEl.innerHTML = rows.join("");
  var rowEls = listEl.querySelectorAll(".files-row");
  for (var j = 0; j < rowEls.length; j++) {
    rowEls[j].addEventListener("click", function () {
      var path = this.getAttribute("data-path") || "";
      var type = this.getAttribute("data-type") || "";
      if (type === "dir") { loadDirectory(path, false); } else { openFile(path); }
    });
  }
}

// ---- 格式化 ----

export function formatSize(bytes) {
  var size = typeof bytes === "number" && isFinite(bytes) ? bytes : 0;
  if (size < 1024) { return size + " B"; }
  var units = ["KB", "MB", "GB", "TB"];
  var value = size / 1024;
  var unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value = value / 1024; unit++; }
  return (value >= 10 ? value.toFixed(0) : value.toFixed(1)) + " " + units[unit];
}

export function formatMtime(seconds) {
  var value = typeof seconds === "number" && seconds > 0 ? seconds : 0;
  if (!value) { return "-"; }
  var date = new Date(value * 1000);
  if (isNaN(date.getTime())) { return "-"; }
  var pad = function (n) { return (n < 10 ? "0" : "") + n; };
  return date.getFullYear() + "-" + pad(date.getMonth() + 1) + "-" + pad(date.getDate())
    + " " + pad(date.getHours()) + ":" + pad(date.getMinutes());
}

// ---- 初始化 ----

export function initFiles() {
  var refreshBtn = filesEl("files-refresh-btn");
  if (refreshBtn) { refreshBtn.addEventListener("click", function () { refreshFiles(); }); }
  var upBtn = filesEl("files-up-btn");
  if (upBtn) {
    upBtn.addEventListener("click", function () {
      var parent = browsePath.indexOf("/") >= 0 ? browsePath.slice(0, browsePath.lastIndexOf("/")) : "";
      loadDirectory(parent, false);
    });
  }
  var rootSelect = filesEl("files-root-select");
  if (rootSelect) {
    rootSelect.addEventListener("change", function () {
      for (var i = 0; i < roots.length; i++) {
        if (roots[i] && roots[i].scope === rootSelect.value) {
          browseRoot = roots[i];
          browsePath = "";
          loadDirectory("", false);
          renderPath();
          return;
        }
      }
    });
  }
  var hiddenToggle = filesEl("files-hidden-toggle");
  if (hiddenToggle) {
    hiddenToggle.addEventListener("change", function () {
      browseShowHidden = !!hiddenToggle.checked;
      loadDirectory(browsePath, false);
    });
  }
  var sortSelect = filesEl("files-sort");
  if (sortSelect) {
    sortSelect.addEventListener("change", function () {
      browseSort = sortSelect.value;
      loadDirectory(browsePath, false);
    });
  }
  var searchInput = filesEl("files-search");
  if (searchInput) {
    searchInput.addEventListener("keydown", function (event) {
      if (event.key === "Enter") { runSearch(searchInput.value); }
    });
  }
  var searchBtn = filesEl("files-search-btn");
  if (searchBtn) { searchBtn.addEventListener("click", function () { runSearch(searchInput ? searchInput.value : ""); }); }
  var searchClear = filesEl("files-search-clear");
  if (searchClear) {
    searchClear.addEventListener("click", function () {
      if (searchInput) { searchInput.value = ""; }
      loadDirectory(browsePath, false);
    });
  }
  // 二级页签：文件页签动态增删，用容器级委托处理选中/关闭 + 键盘导航。
  // 键盘约定与技能详情页签一致（←/→ 环绕、Home/End 首尾），另加 Delete 关闭文件页签。
  var tabsEl = filesEl("files-tabs");
  if (tabsEl) {
    tabsEl.addEventListener("click", function (event) {
      var target = event.target;
      if (!target || !target.closest) { return; }
      var closeEl = target.closest(".files-tab-close");
      if (closeEl) {
        // 关闭按钮是页签内部的 span（button 里不能再嵌 button）：点它只关页签，不切页签。
        var closeTab = findFileTabByKey(tabKeyOf(closeEl.closest("button[data-files-tab]")));
        if (closeTab) { closeFileTab(closeTab, true); }
        return;
      }
      var button = filesTabButtonFrom(target);
      if (button) { selectFilesTab(tabKeyOf(button), false); }
    });
    tabsEl.addEventListener("keydown", function (event) {
      var button = filesTabButtonFrom(event.target);
      if (!button) { return; }
      // 菜单键 / Shift+F10：与右键同效（键盘用户没有指针坐标，菜单贴按钮左下角）。
      if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) {
        event.preventDefault();
        openFilesTabMenu(button);
        return;
      }
      if (event.key === "Delete") {
        var tab = findFileTabByKey(tabKeyOf(button)); // 「目录浏览」不可关闭
        if (!tab) { return; }
        event.preventDefault();
        closeFileTab(tab, true);
        return;
      }
      if (event.key !== "ArrowRight" && event.key !== "ArrowLeft" && event.key !== "Home" && event.key !== "End") { return; }
      event.preventDefault();
      moveFilesTab(event.key);
    });
    // 右键菜单：只在页签按钮上接管（空白处保留浏览器原生菜单，免得整条页签栏都吃不到右键）。
    tabsEl.addEventListener("contextmenu", function (event) {
      var button = filesTabButtonFrom(event.target);
      if (!button) { return; }
      event.preventDefault();
      openFilesTabMenu(button, event.clientX, event.clientY);
    });
  }

  // 页签菜单的事件与收起时机。菜单是浮层：位置一失效就该消失，不能挂在半空——
  // 点到菜单外（捕获阶段，不拦默认行为，点击照常落到原处）、滚动、窗口尺寸变化都收起。
  var menuEl = filesTabMenuEl();
  if (menuEl) {
    menuEl.addEventListener("click", function (event) {
      var item = event.target && event.target.closest ? event.target.closest(".tab-menu-item") : null;
      if (!item || item.disabled) { return; }
      runFilesTabMenuAction(item.getAttribute("data-menu-action"));
    });
    menuEl.addEventListener("keydown", function (event) {
      if (event.key === "Escape") { event.preventDefault(); closeFilesTabMenu(true); return; }
      if (event.key === "Tab") { closeFilesTabMenu(true); return; } // 不拦默认：焦点从触发页签继续走
      if (event.key === "ArrowDown") { event.preventDefault(); moveFilesTabMenuFocus("next"); return; }
      if (event.key === "ArrowUp") { event.preventDefault(); moveFilesTabMenuFocus("prev"); return; }
      if (event.key === "Home") { event.preventDefault(); moveFilesTabMenuFocus("first"); return; }
      if (event.key === "End") { event.preventDefault(); moveFilesTabMenuFocus("last"); }
    });
  }
  document.addEventListener("pointerdown", function (event) {
    if (!filesMenuOpened) { return; }
    var menu = filesTabMenuEl();
    if (menu && event.target && menu.contains && menu.contains(event.target)) { return; }
    closeFilesTabMenu(false);
  }, true);
  window.addEventListener("resize", function () { closeFilesTabMenu(false); });
  window.addEventListener("scroll", function () { closeFilesTabMenu(false); }, true);

  // 大屏左右分栏（折叠按钮 / 拖拽把手 / 跨断点对账 / 记忆值恢复）与「GIT」页签共用
  // js/splitpane.js；这里只把文件页签特有的对账接上去，init 会顺带做第一次对账。
  filesPane.init();
}

// copyCodeBlock 复制代码块文本（结构见 js/markdown.js：pre > button.copy-code-btn + code）。
function copyCodeBlock(btn) {
  var codeEl = btn.parentNode ? btn.parentNode.querySelector("code") : null;
  if (!codeEl) { return; }
  if (!navigator.clipboard) {
    showToast("复制失败（浏览器不支持剪贴板）", "error");
    return;
  }
  navigator.clipboard.writeText(codeEl.textContent || "").then(function () {
    var old = btn.textContent;
    btn.textContent = "✓ 已复制";
    setTimeout(function () { btn.textContent = old; }, 1500);
  }).catch(function () {
    showToast("复制失败", "error");
  });
}

// filesTabButtonFrom 从事件目标回溯到页签按钮（点击按钮内部的文本节点也能命中）。
function filesTabButtonFrom(target) {
  if (!target) { return null; }
  if (typeof target.closest === "function") { return target.closest("button[data-files-tab]"); }
  return null;
}
