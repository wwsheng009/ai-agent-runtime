// 「GIT」页签：当前会话工作目录的 git 管理器。
// 数据源 /web/api/git/*（与 runtime-server 的 /api/runtime/git/* 同源：internal/gitbrowse），
// 因此状态分组、diff 解析、提交分页与 TUI/后端完全一致。
//
// 作用域纪律与「文件」页签相同：scope 一律用 /web/api/fs/roots 返回的根**原样回传**，
// 前端不拼字面量；后端说不是 git 仓库就如实显示，不回退、不猜测。
// 写操作只有 stage/unstage（与后端白名单一致，commit 不在本期范围）。
// aicli micro web client 前端模块(无构建步骤,由 app.js 入口聚合)。

import { apiFetch, esc, showToast } from "./util.js";
import { createSplitPane } from "./splitpane.js";

var gitSeq = 0;
var diffSeq = 0;
var loaded = false;
var currentSessionID = "";
var loadedSessionID = "";

// ---- 状态 ----
var roots = [];
var gitRoot = null;      // 选中的作用域根（原样保留 scope）
var statusData = null;   // 最近一次 /git/status 结果
var commits = [];
var commitsCursor = "";
var commitsHasMore = false;
var diffFile = null;     // 当前 diff 的文件路径
var diffTarget = "working";
var diffWhitespace = "show";
var diffTrigger = null;

function gitEl(id) { return document.getElementById(id); }

function gitAPI(path, init) {
  return apiFetch(path, init || { cache: "no-store" }).then(function (res) {
    return res.json().then(function (body) {
      return { status: res.status, body: body };
    }, function () {
      return { status: res.status, body: null };
    });
  });
}

function gitError(body) {
  if (body && body.error) { return body.error; }
  return null;
}

// ---- 大屏左右分栏（编辑器式布局）----
// 与「文件」页签同款（js/splitpane.js）：≥900px 时左栏 = 状态分组 / 变更列表 / 提交记录，
// 右栏 = 选中变更的 diff。折叠、宽度拖拽、记忆值、无障碍那套全在 splitpane 里，
// 这里只保留 git 特有的对账（右栏空态、diff 面板 dialog↔region 的语义切换），
// 通过 onApply 回调执行；几何常量与文件页签保持一致，两个页签手感一致。
var GIT_SPLIT_QUERY = "(min-width: 900px)";
var GIT_SIDE_DEFAULT = 320; // 默认宽度 = 双击复位值（与 style.css 的 320px 对齐）
var GIT_SIDE_MIN = 200;     // 最小宽度（与 style.css 的 min-width 对齐）
var GIT_SIDE_MAX = 640;     // 最大宽度（容器更窄时按容器算）
var GIT_MAIN_MIN = 320;     // 拖拽/键盘调整时给右侧 diff 至少留出的宽度
var GIT_SIDE_STEP = 24;     // 键盘 ←/→ 每步调整的像素

var gitPane = createSplitPane({
  layoutId: "git-layout",
  sideId: "git-side",
  toggleId: "git-side-toggle",
  splitterId: "git-side-splitter",
  splitQuery: GIT_SPLIT_QUERY,
  widthVar: "--git-side-width",
  widthKey: "webGitSideWidth",
  collapsedKey: "webGitSideCollapsed",
  defaultWidth: GIT_SIDE_DEFAULT,
  minWidth: GIT_SIDE_MIN,
  maxWidth: GIT_SIDE_MAX,
  mainMin: GIT_MAIN_MIN,
  step: GIT_SIDE_STEP,
  collapseTitle: "折叠 git 侧栏（左栏）",
  expandTitle: "展开 git 侧栏（左栏）",
  onApply: syncGitLayout
});

// syncGitLayout 对账分栏相关的可见性与语义（几何在 CSS，折叠/宽度在 splitpane）：
//   1) 右栏空态：大屏且没打开 diff 时显示引导（窄屏由弹窗本身承载，始终 hidden）；
//   2) diff 面板的语义：窄屏是 aria-modal 的 dialog，大屏是右栏里的常驻 region——
//      同一个 DOM 两种呈现，语义得跟着走，否则读屏会以为页面被一个「模态」挡住；
//   3) 列表里的选中行：右栏显示谁的 diff，左栏那条就得标出来（大屏尤其需要）。
function syncGitLayout(nowSplit) {
  var modal = gitEl("git-diff-modal");
  if (modal) {
    if (nowSplit) {
      modal.setAttribute("role", "region");
      modal.removeAttribute("aria-modal");
    } else {
      modal.setAttribute("role", "dialog");
      modal.setAttribute("aria-modal", "true");
    }
  }
  var closeBtn = gitEl("git-diff-close");
  if (closeBtn) { closeBtn.title = nowSplit ? "关闭 diff（右栏回到空态）" : "关闭"; }
  var overlay = gitEl("git-diff-overlay");
  var open = !!(overlay && overlay.classList && overlay.classList.contains("active"));
  var empty = gitEl("git-detail-empty");
  if (empty) { empty.hidden = !(nowSplit && !open); }
  syncGitActiveRow();
}

// syncGitActiveRow 把「右栏正在显示的那条变更」在左栏列表里标出来（class + aria-current）。
// 同一个文件可能同时在「已暂存」与「未暂存」两组里，所以还要按 target 对齐分组；
// 列表是整块重建的（renderGitStatus），每次重建后都要重新标一次。
function syncGitActiveRow() {
  var listEl = gitEl("git-changes");
  if (!listEl || !listEl.querySelectorAll) { return; }
  var rows = listEl.querySelectorAll(".git-row");
  var wantStaged = diffTarget === "staged";
  for (var i = 0; i < rows.length; i++) {
    var row = rows[i];
    var on = !!diffFile
      && (row.getAttribute("data-path") || "") === diffFile
      && ((row.getAttribute("data-group") || "") === "staged") === wantStaged;
    if (row.classList) { row.classList.toggle("active", on); }
    if (on) { row.setAttribute("aria-current", "true"); } else { row.removeAttribute("aria-current"); }
  }
}

// ---- 会话同步 ----

export function syncGitSession(sessionID) {
  var next = sessionID || "";
  if (next === currentSessionID) { return; }
  currentSessionID = next;
  if (!loaded) { return; }
  if (loadedSessionID === next) { return; }
  gitRoot = null;
  statusData = null;
  commits = [];
  commitsCursor = "";
  commitsHasMore = false;
  refreshGitIfActive();
}

// ---- 加载入口 ----

// loadGit：GIT 页签按「进入即重拉」处理（与调试页签同约定）——工作区的 git 状态
// 随时被编辑器/命令改变，缓存的旧状态会误导用户；一次 status 调用足够轻量。
export function loadGit() {
  loaded = true;
  refreshGit();
}

export function refreshGitIfActive() {
  var panel = gitEl("tab-git");
  if (!panel || !panel.classList.contains("active")) { return; }
  refreshGit();
}

export function refreshGit() {
  var listEl = gitEl("git-changes");
  if (!listEl) { return; }
  var seq = ++gitSeq;
  loadedSessionID = currentSessionID;
  setGitStatus("");
  setGitSubTabCount("git-changes-count", ""); // 加载中不留上一轮的旧条数
  listEl.innerHTML = '<div class="files-empty">加载中…</div>';
  ensureGitRoot().then(function (root) {
    if (seq !== gitSeq) { return; }
    if (!root) {
      renderGitError({ code: "fs_no_root", message: "当前会话没有可浏览的工作目录" }, 0);
      return;
    }
    gitRoot = root;
    return gitAPI("/web/api/git/status?scope=" + encodeURIComponent(root.scope) + "&path=").then(function (result) {
      if (seq !== gitSeq) { return; }
      if (result.status !== 200) {
        renderGitError(gitError(result.body), result.status);
        return;
      }
      statusData = result.body || {};
      renderGitStatus();
      loadCommits(false);
    });
  }).catch(function () {
    if (seq !== gitSeq) { return; }
    renderGitError(null, 0);
  });
}

// ensureGitRoot：复用「文件」页签的作用域根端点，保证两个页签对同一会话工作目录
// 的认知一致；优先会话根。
function ensureGitRoot() {
  if (gitRoot) { return Promise.resolve(gitRoot); }
  return gitAPI("/web/api/fs/roots").then(function (result) {
    if (result.status !== 200) { return null; }
    roots = (result.body && result.body.roots) || [];
    for (var i = 0; i < roots.length; i++) {
      if (roots[i] && roots[i].kind === "session" && roots[i].exists !== false) { return roots[i]; }
    }
    for (var j = 0; j < roots.length; j++) {
      if (roots[j] && roots[j].exists !== false) { return roots[j]; }
    }
    return null;
  });
}

// ---- 侧栏二级页签（变更 / 提交记录） ----

// 两条列表不再上下堆叠：一次只看一条。切换是纯客户端状态（不重建、不重新请求），
// 键盘走 roving tabindex——只有当前页签能被 Tab 到，←/→ 在两条之间环绕，与文件页签同款。
var gitSubTab = "changes";

function gitSubTabButtons() {
  var tabsEl = gitEl("git-subtabs");
  if (!tabsEl || !tabsEl.querySelectorAll) { return []; }
  return tabsEl.querySelectorAll("button[data-git-subtab]");
}

function gitSubTabOf(button) {
  if (!button || !button.getAttribute) { return "changes"; }
  return button.getAttribute("data-git-subtab") === "commits" ? "commits" : "changes";
}

// selectGitSubTab 就地更新选中态与面板可见性；focus=true 时把焦点一并带过去（键盘切换）。
function selectGitSubTab(key, focus) {
  var want = key === "commits" ? "commits" : "changes";
  gitSubTab = want;
  var buttons = gitSubTabButtons();
  for (var i = 0; i < buttons.length; i++) {
    var btn = buttons[i];
    var on = gitSubTabOf(btn) === want;
    if (btn.classList) { btn.classList.toggle("active", on); }
    btn.setAttribute("aria-selected", on ? "true" : "false");
    btn.setAttribute("tabindex", on ? "0" : "-1");
    if (on && focus && btn.focus) { btn.focus(); }
  }
  var panels = [["git-panel-changes", "changes"], ["git-panel-commits", "commits"]];
  for (var j = 0; j < panels.length; j++) {
    var panel = gitEl(panels[j][0]);
    if (panel) { panel.hidden = panels[j][1] !== want; }
  }
}

// moveGitSubTabFocus 与文件页签同一套按键：←/→ 环绕、Home/End 到两端。
function moveGitSubTabFocus(key) {
  var buttons = gitSubTabButtons();
  if (!buttons.length) { return; }
  var index = -1;
  for (var i = 0; i < buttons.length; i++) {
    if (gitSubTabOf(buttons[i]) === gitSubTab) { index = i; break; }
  }
  if (index < 0) { index = 0; }
  var last = buttons.length - 1;
  if (key === "ArrowRight") { index = index === last ? 0 : index + 1; }
  else if (key === "ArrowLeft") { index = index === 0 ? last : index - 1; }
  else if (key === "Home") { index = 0; }
  else if (key === "End") { index = last; }
  else { return; }
  selectGitSubTab(gitSubTabOf(buttons[index]), true);
}

// 页签上的条数：另一条页签的内容看不见，得在页签上说明白有多少（加载中/失败置空，
// 不留上一轮的旧数字）。
function setGitSubTabCount(id, text) {
  var el = gitEl(id);
  if (el) { el.textContent = text || ""; }
}

// ---- 状态渲染 ----

function renderGitStatus() {
  var listEl = gitEl("git-changes");
  if (!listEl) { return; }
  var repo = (statusData && statusData.repo) || {};
  var repoEl = gitEl("git-repo-label");
  if (repoEl) {
    var parts = [];
    parts.push(repo.detached ? "detached@" + (repo.head || "?") : (repo.branch || "(无分支)"));
    if (repo.upstream) {
      var track = repo.upstream;
      if (repo.ahead) { track += " ↑" + repo.ahead; }
      if (repo.behind) { track += " ↓" + repo.behind; }
      parts.push(track);
    }
    if (repo.root) { parts.push(repo.root); }
    repoEl.textContent = parts.join(" · ");
  }
  var warnings = (statusData && statusData.warnings) || [];
  setGitStatus(warnings.length ? "warnings: " + warnings.join(" / ") : "");

  var conflicts = (statusData && statusData.conflicts) || [];
  var staged = (statusData && statusData.staged) || [];
  var unstaged = (statusData && statusData.unstaged) || [];
  var untracked = (statusData && statusData.untracked) || [];
  var renames = (statusData && statusData.renames) || [];
  // 页签条数：另一条页签的内容看不见，得在页签上说明白有多少（干净仓库显示 0，不是空白）。
  setGitSubTabCount("git-changes-count",
    String(conflicts.length + staged.length + unstaged.length + untracked.length + renames.length));

  var html = [];
  html.push(renderGroup("冲突", conflicts, "conflict"));
  html.push(renderGroup("已暂存", staged, "staged"));
  html.push(renderGroup("未暂存", unstaged, "unstaged"));
  html.push(renderGroup("未跟踪", untracked, "untracked"));
  if (renames.length) {
    var rows = ['<div class="git-group"><div class="git-group-head">重命名/复制 <span class="git-group-count">'
      + renames.length + "</span></div>"];
    for (var r = 0; r < renames.length; r++) {
      var item = renames[r] || {};
      rows.push('<div class="git-row git-row-static"><span class="git-badge">' + esc(item.status || "")
        + '</span><span class="git-path">' + esc(item.from || "") + " → " + esc(item.to || "") + "</span></div>");
    }
    rows.push("</div>");
    html.push(rows.join(""));
  }
  if (statusData && statusData.clean) {
    html.push('<div class="files-empty">工作区干净（没有未提交的改动）</div>');
  }
  listEl.innerHTML = html.join("");
  bindGitRows(listEl);
  syncGitActiveRow(); // 列表是整块重建的：右栏正在看的那条要重新标出来
}

function renderGroup(title, entries, group) {
  var items = entries || [];
  if (!items.length) { return ""; }
  var rows = ['<div class="git-group"><div class="git-group-head">' + esc(title)
    + ' <span class="git-group-count">' + items.length + "</span></div>"];
  for (var i = 0; i < items.length; i++) {
    rows.push(renderChangeRow(items[i], group));
  }
  rows.push("</div>");
  return rows.join("");
}

// renderChangeRow 只渲染后端实际返回的字段：没有 xy / insertions 就不出现对应节点。
function renderChangeRow(entry, group) {
  var item = entry || {};
  var path = String(item.path || "");
  var meta = [];
  if (item.xy) { meta.push(String(item.xy)); }
  if (typeof item.insertions === "number" && item.insertions > 0) { meta.push("+" + item.insertions); }
  if (typeof item.deletions === "number" && item.deletions > 0) { meta.push("-" + item.deletions); }
  if (item.binary) { meta.push("binary"); }
  if (item.from) { meta.push("← " + item.from); }
  var actions = [];
  if (group === "staged") {
    actions.push('<button class="git-action" type="button" data-action="unstage" data-path="' + esc(path)
      + '" title="取消暂存（git restore --staged）">－</button>');
  } else if (group === "unstaged" || group === "untracked" || group === "conflict") {
    actions.push('<button class="git-action" type="button" data-action="stage" data-path="' + esc(path)
      + '" title="暂存该文件（git add）">＋</button>');
  }
  return '<div class="git-row" data-path="' + esc(path) + '" data-group="' + esc(group) + '" role="button" tabindex="0">'
    + '<span class="git-badge git-badge-' + esc(group) + '">' + esc(groupLabel(group)) + "</span>"
    + '<span class="git-path">' + esc(path) + "</span>"
    + '<span class="git-row-meta">' + esc(meta.join(" · ")) + "</span>"
    + '<span class="git-row-actions">' + actions.join("") + "</span>"
    + "</div>";
}

function groupLabel(group) {
  switch (group) {
    case "staged": return "S";
    case "unstaged": return "U";
    case "untracked": return "?";
    case "conflict": return "C";
    default: return group;
  }
}

function bindGitRows(container) {
  var rows = container.querySelectorAll(".git-row");
  for (var i = 0; i < rows.length; i++) {
    rows[i].addEventListener("click", function (event) {
      var actionBtn = event.target && event.target.closest ? event.target.closest(".git-action") : null;
      if (actionBtn) {
        event.stopPropagation();
        applyStageAction(actionBtn.getAttribute("data-action"), actionBtn.getAttribute("data-path"));
        return;
      }
      var path = this.getAttribute("data-path") || "";
      var group = this.getAttribute("data-group") || "unstaged";
      if (path) { openDiff(path, group === "staged" ? "staged" : "working", this); }
    });
    rows[i].addEventListener("keydown", function (event) {
      if (event.key !== "Enter" && event.key !== " ") { return; }
      event.preventDefault();
      var path = this.getAttribute("data-path") || "";
      var group = this.getAttribute("data-group") || "unstaged";
      if (path) { openDiff(path, group === "staged" ? "staged" : "working", this); }
    });
  }
}

function renderGitError(err, status) {
  var code = err && err.code ? err.code : "network_error";
  var msg = err && err.message ? err.message : "";
  var listEl = gitEl("git-changes");
  var repoEl = gitEl("git-repo-label");
  if (repoEl) { repoEl.textContent = "—"; }
  if (listEl) {
    // not_a_git_repo 是可预期状态（工作目录本来就不是仓库），文案与其它失败区分。
    var prefix = code === "not_a_git_repo" ? "当前工作目录不是 git 仓库" : "加载失败";
    listEl.innerHTML = '<div class="files-empty' + (code === "not_a_git_repo" ? "" : " files-error") + '">'
      + esc(prefix) + ": " + esc(code) + (msg ? " — " + esc(msg) : "")
      + (status ? "（HTTP " + status + "）" : "") + "</div>";
  }
}

function setGitStatus(text) {
  var el = gitEl("git-status-line");
  if (el) { el.textContent = text || ""; }
}

// ---- stage / unstage ----

function applyStageAction(action, path) {
  if (!gitRoot || !path || !action) { return; }
  var seq = ++gitSeq;
  gitAPI("/web/api/git/stage", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ scope: gitRoot.scope, path: "", action: action, files: [path] })
  }).then(function (result) {
    if (seq !== gitSeq) { return; }
    if (result.status !== 200) {
      var err = gitError(result.body);
      showToast("操作失败: " + ((err && err.code) || "network_error"), "error");
      return;
    }
    // 后端返回更新后的完整状态摘要：直接替换本地缓存，避免再次拉取（也避免竞态）。
    statusData = (result.body && result.body.status) || statusData;
    renderGitStatus();
    showToast(action === "stage" ? "已暂存" : "已取消暂存", "ok");
  }).catch(function () {
    if (seq !== gitSeq) { return; }
    showToast("操作失败: network_error", "error");
  });
}

// ---- 提交列表 ----

function loadCommits(append) {
  if (!gitRoot) { return; }
  var listEl = gitEl("git-commits");
  if (!listEl) { return; }
  if (!append) {
    commits = [];
    commitsCursor = "";
    setGitSubTabCount("git-commits-count", ""); // 加载中不留上一轮的旧条数
    listEl.innerHTML = '<div class="files-empty">加载中…</div>';
  }
  var url = "/web/api/git/commits?scope=" + encodeURIComponent(gitRoot.scope) + "&path=&limit=50";
  if (append && commitsCursor) { url += "&cursor=" + encodeURIComponent(commitsCursor); }
  var seq = gitSeq;
  gitAPI(url).then(function (result) {
    if (seq !== gitSeq) { return; }
    if (result.status !== 200) {
      var err = gitError(result.body);
      listEl.innerHTML = '<div class="files-empty files-error">提交列表加载失败: '
        + esc((err && err.code) || "network_error") + "</div>";
      return;
    }
    var body = result.body || {};
    commits = append ? commits.concat(body.commits || []) : (body.commits || []);
    commitsCursor = body.next_cursor || "";
    commitsHasMore = !!body.has_more;
    renderCommits();
  }).catch(function () {
    if (seq !== gitSeq) { return; }
    listEl.innerHTML = '<div class="files-empty files-error">提交列表加载失败: network_error</div>';
  });
}

function renderCommits() {
  var listEl = gitEl("git-commits");
  if (!listEl) { return; }
  if (!commits.length) {
    setGitSubTabCount("git-commits-count", "0");
    listEl.innerHTML = '<div class="files-empty">没有提交记录</div>';
    return;
  }
  // has_more 时带 "+"：条数是「已加载多少」，不是「总共有多少」（分页见「加载更多」）。
  setGitSubTabCount("git-commits-count", commits.length + (commitsHasMore ? "+" : ""));
  var rows = [];
  for (var i = 0; i < commits.length; i++) {
    var commit = commits[i] || {};
    var refs = (commit.refs || []).join(", ");
    rows.push('<div class="git-commit">'
      + '<span class="git-commit-sha" title="' + esc(commit.sha || "") + '">' + esc(commit.short || commit.sha || "") + "</span>"
      + '<span class="git-commit-subject">' + esc(commit.subject || "") + "</span>"
      + '<span class="git-commit-meta">' + esc((commit.author || "") + " · " + (commit.date || "")) + "</span>"
      + (refs ? '<span class="git-commit-refs">' + esc(refs) + "</span>" : "")
      + "</div>");
  }
  if (commitsHasMore) {
    rows.push('<button id="git-commits-more" class="files-more" type="button">加载更多…</button>');
  }
  listEl.innerHTML = rows.join("");
  var moreBtn = gitEl("git-commits-more");
  if (moreBtn) { moreBtn.addEventListener("click", function () { loadCommits(true); }); }
}

// ---- diff ----

function openDiff(path, target, trigger) {
  var overlay = gitEl("git-diff-overlay");
  if (!overlay || !gitRoot) { return; }
  diffFile = path;
  diffTarget = target === "staged" ? "staged" : "working";
  diffTrigger = trigger || null;
  overlay.classList.add("active");
  var titleEl = gitEl("git-diff-title");
  if (titleEl) { titleEl.textContent = path; }
  syncDiffTargetButtons();
  // 大屏右栏是常驻的：打开 diff 的同时收起空态、并在左栏标出这一条（见 syncGitLayout）。
  gitPane.apply();
  loadDiff();
}

function loadDiff() {
  if (!gitRoot || !diffFile) { return; }
  var seq = ++diffSeq;
  var bodyEl = gitEl("git-diff-body");
  var metaEl = gitEl("git-diff-meta");
  if (metaEl) { metaEl.textContent = "加载中…"; }
  if (bodyEl) {
    bodyEl.textContent = "加载中…";
    bodyEl.scrollTop = 0; // 右栏是同一个容器：换文件要从头看，不留在上一个文件的位置
  }
  var url = "/web/api/git/diff?scope=" + encodeURIComponent(gitRoot.scope)
    + "&path=&file=" + encodeURIComponent(diffFile)
    + "&target=" + encodeURIComponent(diffTarget)
    + "&whitespace=" + encodeURIComponent(diffWhitespace)
    + "&context=3";
  gitAPI(url).then(function (result) {
    if (seq !== diffSeq) { return; }
    if (result.status !== 200) {
      var err = gitError(result.body);
      if (metaEl) { metaEl.textContent = ""; }
      if (bodyEl) {
        bodyEl.className = "git-diff-body";
        bodyEl.textContent = "diff 失败: " + ((err && err.code) || "network_error")
          + ((err && err.message) ? " — " + err.message : "");
      }
      return;
    }
    renderDiff(result.body || {});
  }).catch(function () {
    if (seq !== diffSeq) { return; }
    if (bodyEl) { bodyEl.textContent = "diff 失败: network_error"; }
  });
}

function renderDiff(diff) {
  var metaEl = gitEl("git-diff-meta");
  var bodyEl = gitEl("git-diff-body");
  var file = diff.file || {};
  var meta = [];
  meta.push("target: " + (diff.effective_target || diff.target || diffTarget));
  if (diff.target_fallback) { meta.push("（回退: " + diff.target_fallback + "）"); }
  if (file.status) { meta.push("status: " + file.status); }
  if (typeof diff.insertions === "number") { meta.push("+" + diff.insertions); }
  if (typeof diff.deletions === "number") { meta.push("-" + diff.deletions); }
  if (diff.whitespace && diff.whitespace !== "show") { meta.push("whitespace: " + diff.whitespace); }
  if (metaEl) { metaEl.textContent = meta.join(" · "); }
  if (!bodyEl) { return; }
  bodyEl.className = "git-diff-body";
  if (file.is_binary) { bodyEl.textContent = "二进制文件，无文本 diff"; return; }
  if (file.is_submodule) { bodyEl.textContent = "子模块，无文本 diff"; return; }
  var hunks = diff.hunks || [];
  if (!hunks.length) {
    // 解析失败不伪造内容：后端给出 parse_error 时退回纯文本 raw（与 gitbrowse 的降级一致）。
    if (diff.raw) {
      bodyEl.textContent = diff.parse_error
        ? "（结构化解析失败: " + diff.parse_error + "，以下为原始 diff）\n" + diff.raw
        : "（无差异）\n" + diff.raw;
      return;
    }
    bodyEl.textContent = diff.parse_error ? "结构化解析失败: " + diff.parse_error : "没有差异";
    return;
  }
  var html = [];
  for (var i = 0; i < hunks.length; i++) {
    var hunk = hunks[i] || {};
    html.push('<div class="git-hunk-head">' + esc(hunk.header || "") + "</div>");
    var lines = hunk.lines || [];
    for (var j = 0; j < lines.length; j++) {
      var line = lines[j] || {};
      var type = line.type || "context";
      var oldNo = line.old_no ? String(line.old_no) : "";
      var newNo = line.new_no ? String(line.new_no) : "";
      var prefix = type === "add" ? "+" : (type === "del" ? "-" : (type === "nonewline" ? "\\" : " "));
      html.push('<div class="git-diff-line git-diff-' + esc(type) + '">'
        + '<span class="git-diff-no">' + esc(oldNo + " " + newNo) + "</span>"
        + '<span class="git-diff-text">' + esc(prefix + " " + (line.text || "")) + "</span>"
        + "</div>");
    }
  }
  bodyEl.innerHTML = html.join("");
}

function syncDiffTargetButtons() {
  var workingBtn = gitEl("git-diff-target-working");
  var stagedBtn = gitEl("git-diff-target-staged");
  if (workingBtn) { workingBtn.classList.toggle("active", diffTarget === "working"); }
  if (stagedBtn) { stagedBtn.classList.toggle("active", diffTarget === "staged"); }
  var wsBtn = gitEl("git-diff-whitespace");
  if (wsBtn) {
    wsBtn.textContent = diffWhitespace === "show" ? "忽略空白: 关" : "忽略空白: 开";
    wsBtn.classList.toggle("active", diffWhitespace !== "show");
  }
}

// switchDiffTarget 切换「工作区 / 已暂存」并重拉 diff。同一个文件可能同时出现在两组里
// （xy=MM 之类），所以切完必须重新标一次左栏选中行，否则右栏已经换成已暂存的 diff，
// 左栏却还停在未暂存那一条上（见 syncGitActiveRow 的分组对齐）。
function switchDiffTarget(target) {
  diffTarget = target === "staged" ? "staged" : "working";
  syncDiffTargetButtons();
  syncGitActiveRow();
  loadDiff();
}

function closeDiff() {
  var overlay = gitEl("git-diff-overlay");
  if (!overlay || !overlay.classList.contains("active")) { return false; }
  overlay.classList.remove("active");
  diffFile = null; // 右栏空了，左栏的选中标记要一起撤掉（见 syncGitActiveRow）
  diffSeq++;
  gitPane.apply(); // 收起空态对账（大屏回到「从左侧选择一条变更」）
  if (diffTrigger && diffTrigger.focus) { diffTrigger.focus(); }
  diffTrigger = null;
  return true;
}

// ---- 初始化 ----

export function initGit() {
  var overlay = gitEl("git-diff-overlay");
  if (overlay) {
    // 不挪到 body：大屏（≥900px）它要留在 #git-main 里当右栏（媒体查询里改定位），
    // 窄屏才靠 position:fixed 当弹窗遮罩——两种呈现共用一个 DOM。
    overlay.addEventListener("click", function (event) {
      if (event.target === overlay) { closeDiff(); }
    });
  }
  var closeBtn = gitEl("git-diff-close");
  if (closeBtn) { closeBtn.addEventListener("click", function () { closeDiff(); }); }
  document.addEventListener("keydown", function (event) {
    if (event.key === "Escape") { closeDiff(); }
  });
  var refreshBtn = gitEl("git-refresh-btn");
  if (refreshBtn) { refreshBtn.addEventListener("click", function () { refreshGit(); }); }
  var workingBtn = gitEl("git-diff-target-working");
  if (workingBtn) {
    workingBtn.addEventListener("click", function () { switchDiffTarget("working"); });
  }
  var stagedBtn = gitEl("git-diff-target-staged");
  if (stagedBtn) {
    stagedBtn.addEventListener("click", function () { switchDiffTarget("staged"); });
  }
  var wsBtn = gitEl("git-diff-whitespace");
  if (wsBtn) {
    wsBtn.addEventListener("click", function () {
      diffWhitespace = diffWhitespace === "show" ? "ignore_all" : "show";
      syncDiffTargetButtons();
      loadDiff();
    });
  }

  // 大屏左右分栏（折叠按钮 / 拖拽把手 / 跨断点对账 / 记忆值恢复）与「文件」页签共用
  // js/splitpane.js；这里只把 git 特有的对账接上去，init 会顺带做第一次对账。
  gitPane.init();

  // 侧栏二级页签（变更 / 提交记录）：点击 + 键盘（←/→/Home/End）。与文件页签同一套
  // roving tabindex；init 时做一次对账，让 DOM 初始态与 gitSubTab 对齐。
  var subtabsEl = gitEl("git-subtabs");
  if (subtabsEl) {
    subtabsEl.addEventListener("click", function (event) {
      var btn = event.target && event.target.closest ? event.target.closest("button[data-git-subtab]") : null;
      if (!btn) { return; }
      selectGitSubTab(gitSubTabOf(btn), false);
    });
    subtabsEl.addEventListener("keydown", function (event) {
      if (event.key !== "ArrowRight" && event.key !== "ArrowLeft" && event.key !== "Home" && event.key !== "End") { return; }
      event.preventDefault();
      moveGitSubTabFocus(event.key);
    });
    selectGitSubTab(gitSubTab, false);
  }
}
