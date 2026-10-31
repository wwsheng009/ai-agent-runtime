// 85-logs.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=100";

Object.assign(G, {
  stopLogTimer: stopLogTimer,
  startLogTimer: startLogTimer,
  initLogsView: initLogsView,
  logHashSlug: logHashSlug,
  populateLogSlugOptions: populateLogSlugOptions,
  setLogSummary: setLogSummary,
  refreshLogs: refreshLogs,
  loadMountLogs: loadMountLogs,
  mountStatusFilter: mountStatusFilter,
  renderMountLogs: renderMountLogs,
  mountLogRow: mountLogRow,
  loadAuditLogs: loadAuditLogs,
  renderAuditLogs: renderAuditLogs,
  auditLogRow: auditLogRow,
  formatLogTime: formatLogTime,
  formatBytes: formatBytes,
  truncate: truncate,
  applyLogTabUI: applyLogTabUI,
  setLogTab: setLogTab,
});

  /* ───────────────────────── logs view ───────────────────────── */

  function stopLogTimer() {
    if (S.logTimer) {
      clearInterval(S.logTimer);
      S.logTimer = null;
    }
  }

  function startLogTimer() {
    stopLogTimer();
    if (!S.logFollow) { return; }
    S.logTimer = setInterval(function () {
      if (G.currentView() === "logs") { refreshLogs(); }
    }, 5000);
  }

  function initLogsView() {
    // A per-mount "日志" link always means "this mount's calls": force the
    // mounts tab even if the operator last looked at audit events.
    if (logHashSlug()) {
      S.logTab = "mounts";
      applyLogTabUI();
    }
    var ready = S.lastMounts.length ? Promise.resolve() : G.loadMounts();
    Promise.resolve(ready).then(function () {
      populateLogSlugOptions();
      refreshLogs();
    });
    startLogTimer();
  }

  function logHashSlug() {
    var h = window.location.hash || "";
    var q = h.indexOf("?");
    if (q < 0) { return ""; }
    return new URLSearchParams(h.slice(q + 1)).get("slug") || "";
  }

  function populateLogSlugOptions() {
    var sel = S.el("log-slug");
    if (!sel) { return; }
    var wanted = logHashSlug() || sel.value;
    sel.innerHTML = '<option value="">全部挂载</option>';
    S.lastMounts.forEach(function (m) {
      var opt = document.createElement("option");
      opt.value = m.slug;
      opt.textContent = (m.name || m.slug) + " · " + m.slug;
      sel.appendChild(opt);
    });
    if (wanted) { sel.value = wanted; }
  }

  function setLogSummary(text) {
    var summary = S.el("logs-summary");
    if (summary) { summary.textContent = text || ""; }
  }

  function refreshLogs() {
    if (G.currentView() !== "logs" || S.logLoading) { return; }
    S.logLoading = true;
    var job = S.logTab === "audit" ? loadAuditLogs() : loadMountLogs();
    Promise.resolve(job).catch(function () {
      setLogSummary("读取失败，稍后重试");
    }).then(function () {
      S.logLoading = false;
    });
  }

  function loadMountLogs() {
    var sel = S.el("log-slug");
    var slug = sel ? sel.value : "";
    var url = "/api/mounts/logs?limit=500";
    if (slug) { url += "&slug=" + encodeURIComponent(slug); }
    return G.apiJSON(url).then(function (r) {
      if (r.res.status === 401) { G.setAuthed(false); return; }
      if (!r.res.ok) {
        setLogSummary("MCP 日志读取失败：HTTP " + r.res.status);
        return;
      }
      S.logMountEntries = (r.body && r.body.entries) || [];
      renderMountLogs(S.logMountEntries);
      setLogSummary("MCP 调用 " + S.logMountEntries.length + " 条 · 更新于 " +
        new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }));
    });
  }

  function mountStatusFilter() {
    var sel = S.el("log-status");
    return sel ? sel.value : "";
  }

  function renderMountLogs(entries) {
    var host = S.el("log-mount-list");
    if (!host) { return; }
    var filter = mountStatusFilter();
    var visible = entries.filter(function (e) {
      if (!filter) { return true; }
      if (filter === "errors") { return e.status >= 400 && e.reason !== "sse_unavailable"; }
      return String(e.status) === filter;
    });
    host.innerHTML = "";
    if (!visible.length) {
      host.innerHTML = '<p class="empty">没有匹配的调用记录。</p>';
      return;
    }
    visible.forEach(function (e) { host.appendChild(mountLogRow(e)); });
  }

  function mountLogRow(e) {
    var row = document.createElement("div");
    row.className = "log-row";
    var status = document.createElement("span");
    var expectedSse = e.reason === "sse_unavailable";
    status.className = "log-status " +
      (expectedSse ? "st-event" : (e.status >= 500 ? "st-bad" : (e.status >= 400 ? "st-warn" : "st-ok")));
    status.textContent = e.status ? String(e.status) : "—";
    var main = document.createElement("span");
    main.className = "log-main";
    var title = document.createElement("span");
    title.className = "log-title";
    var slug = document.createElement("code");
    slug.textContent = e.slug || "—";
    title.appendChild(slug);
    var titleText = " " + (e.method || "") + " " + (e.path || "");
    if (e.command) { titleText += " · " + e.command; }
    title.appendChild(document.createTextNode(titleText));
    var meta = document.createElement("span");
    meta.className = "log-meta";
    var bits = [];
    if (e.token_id) { bits.push("token " + e.token_id); }
    if (e.remote_addr) { bits.push(e.remote_addr); }
    if (e.request_bytes != null || e.response_bytes != null) {
      bits.push(formatBytes(e.request_bytes) + " → " + formatBytes(e.response_bytes));
    }
    if (e.duration_ms != null) { bits.push(e.duration_ms + "ms"); }
    if (e.user_agent) { bits.push("UA " + truncate(e.user_agent, 48)); }
    if (e.reason) { bits.push(e.reason === "sse_unavailable" ? "SSE 推送未启用（正常）" : e.reason); }
    meta.textContent = bits.join(" · ");
    main.appendChild(title);
    main.appendChild(meta);
    var time = document.createElement("span");
    time.className = "log-time";
    time.textContent = formatLogTime(e.time);
    var when = G.validTokenTime(e.time);
    if (when) { time.title = when.toLocaleString(); }
    row.appendChild(status);
    row.appendChild(main);
    row.appendChild(time);
    return row;
  }

  function loadAuditLogs() {
    var type = S.el("log-type") ? S.el("log-type").value : "";
    var query = S.el("log-query") ? S.el("log-query").value.trim() : "";
    var url = "/api/audit?limit=500";
    if (type) { url += "&type=" + encodeURIComponent(type); }
    if (query) { url += "&q=" + encodeURIComponent(query); }
    return G.apiJSON(url).then(function (r) {
      if (r.res.status === 401) { G.setAuthed(false); return; }
      if (!r.res.ok) {
        setLogSummary("审计日志读取失败：HTTP " + r.res.status);
        return;
      }
      S.logAuditEntries = (r.body && r.body.events) || [];
      renderAuditLogs(S.logAuditEntries);
      setLogSummary("审计事件 " + S.logAuditEntries.length + " 条 · 更新于 " +
        new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }));
    });
  }

  function renderAuditLogs(events) {
    var host = S.el("log-audit-list");
    if (!host) { return; }
    host.innerHTML = "";
    if (!events.length) {
      host.innerHTML = '<p class="empty">没有匹配的审计事件。</p>';
      return;
    }
    events.forEach(function (event) { host.appendChild(auditLogRow(event)); });
  }

  function auditLogRow(event) {
    var row = document.createElement("div");
    row.className = "log-row";
    var type = document.createElement("span");
    type.className = "log-status st-event" + (event.result ? " st-warn" : "");
    type.textContent = (event.type || "event").replace(/^mount_/, "mount/");
    var main = document.createElement("span");
    main.className = "log-main";
    var title = document.createElement("span");
    title.className = "log-title";
    title.textContent = (event.type || "event") +
      (event.result ? " · " + event.result : "") +
      (event.actor ? " · " + event.actor : "");
    var meta = document.createElement("span");
    meta.className = "log-meta";
    var bits = [];
    if (event.detail) { bits.push(event.detail); }
    if (event.ip) { bits.push("ip " + event.ip); }
    meta.textContent = bits.join(" · ");
    main.appendChild(title);
    main.appendChild(meta);
    var time = document.createElement("span");
    time.className = "log-time";
    time.textContent = formatLogTime(event.time);
    var when = G.validTokenTime(event.time);
    if (when) { time.title = when.toLocaleString(); }
    row.appendChild(type);
    row.appendChild(main);
    row.appendChild(time);
    return row;
  }

  function formatLogTime(raw) {
    var d = G.validTokenTime(raw);
    if (!d) { return "—"; }
    return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  }

  // formatBytes renders a byte count compactly; null/undefined yields "—".
  function formatBytes(n) {
    if (n == null || isNaN(n)) { return "—"; }
    if (n < 1024) { return n + "B"; }
    if (n < 1024 * 1024) { return (n / 1024).toFixed(1) + "KB"; }
    return (n / (1024 * 1024)).toFixed(1) + "MB";
  }

  // truncate caps a string to max characters, appending an ellipsis when cut.
  function truncate(s, max) {
    s = String(s == null ? "" : s);
    if (s.length <= max) { return s; }
    return s.slice(0, max) + "…";
  }

  function applyLogTabUI() {
    document.querySelectorAll("[data-logtab]").forEach(function (btn) {
      var active = btn.getAttribute("data-logtab") === S.logTab;
      btn.classList.toggle("active", active);
      btn.setAttribute("aria-selected", active ? "true" : "false");
      btn.setAttribute("aria-pressed", active ? "true" : "false");
    });
    var mountsPanel = S.el("log-mounts-panel");
    var auditPanel = S.el("log-audit-panel");
    if (mountsPanel) { mountsPanel.hidden = S.logTab !== "mounts"; }
    if (auditPanel) { auditPanel.hidden = S.logTab !== "audit"; }
  }

  function setLogTab(tab) {
    S.logTab = tab === "audit" ? "audit" : "mounts";
    applyLogTabUI();
    refreshLogs();
  }
