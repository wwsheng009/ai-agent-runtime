  /* ─────────────────────── rendering: stats ──────────────────── */

  var lastAnnouncedAttention = null;
  var attentionAnnounceTimer = null;

  // UX-08: 计数变化只通过一个 role=status 汇总播报，并做延迟合并；
  // 避免 SSE 连发时反复打断读屏用户。
  function announceAttentionChange(count) {
    if (lastAnnouncedAttention === null) {
      lastAnnouncedAttention = count;
      return;
    }
    if (count === lastAnnouncedAttention) { return; }
    lastAnnouncedAttention = count;
    if (attentionAnnounceTimer) { clearTimeout(attentionAnnounceTimer); }
    attentionAnnounceTimer = setTimeout(function () {
      attentionAnnounceTimer = null;
      var status = el("a11y-status");
      if (!status) { return; }
      status.textContent = count ? "待处理 " + count + " 项" : "当前没有待处理";
    }, 1500);
  }

  function updateStats(nodes) {
    var live = 0, offline = 0, active = 0;
    (nodes || []).forEach(function (node) {
      if (node.stale) { offline++; } else { live++; active += (node.sessions || []).length; }
    });
    var attention = inboxItems(nodes).length;
    announceAttentionChange(attention);
    el("stat-online").textContent = String(live);
    el("stat-offline").textContent = String(offline);
    el("stat-sessions").textContent = String(active);
    el("stat-attention").textContent = String(attention);
    var attentionCard = el("stat-attention-card");
    if (attentionCard) {
      attentionCard.classList.toggle("has-attention", attention > 0);
      attentionCard.classList.toggle("is-empty", attention === 0);
      attentionCard.setAttribute("aria-label", attention ? attention + " 项待处理" : "暂无待处理");
      if (attention) {
        attentionCard.setAttribute("href", "#sessions?attention=1");
      } else {
        attentionCard.removeAttribute("href");
      }
    }
    var badge = el("nav-badge-sessions");
    if (badge) {
      badge.textContent = attention ? String(attention) : "";
      badge.hidden = !attention;
      badge.title = attention ? attention + " 项待处理" : "";
    }
    var navSessions = document.querySelector('.nav-item[data-view="sessions"]');
    if (navSessions) {
      navSessions.setAttribute("aria-label", attention ? "会话，" + attention + " 项待处理" : "会话");
    }
    var chip = el("conn-chip");
    if (chip) {
      var chipText = live ? live + " 个节点在线" : "无节点在线";
      chip.innerHTML = '<span class="dot ' + (live ? "live" : "offline") + '"></span>' +
        '<span class="conn-text"></span>';
      chip.querySelector(".conn-text").textContent = chipText;
    }
  }

  /* ─────────────────────── rendering: sessions ───────────────── */

  // 子代理行（wire.SessionSummary.parent）是 display-only 的从属会话：没有自己的
  // 注意力轴，只跟随父会话展示与筛选（docs/gateway/07-console.md）。
  function isSubagentSession(s) {
    return !!(s && s.parent && s.parent.parent_session_id);
  }

  // orderSessionsWithChildren 把列表重排成「父会话紧跟其子代理行」，并丢弃父会话
  // 不可见的孤儿子行（父被过滤或子代理行被隐藏时，子行不应单独出现）。
  function orderSessionsWithChildren(items) {
    var visibleParents = {};
    var childrenByParent = {};
    items.forEach(function (item) {
      if (!isSubagentSession(item.session)) { visibleParents[item.session.session_id] = true; }
    });
    items.forEach(function (item) {
      if (!isSubagentSession(item.session)) { return; }
      var pid = item.session.parent.parent_session_id;
      if (!visibleParents[pid]) { return; }
      (childrenByParent[pid] = childrenByParent[pid] || []).push(item);
    });
    var out = [];
    items.forEach(function (item) {
      if (isSubagentSession(item.session)) { return; }
      out.push(item);
      var kids = childrenByParent[item.session.session_id];
      if (kids && kids.length) { out = out.concat(kids); }
    });
    return out;
  }

  // 建/更分离约定（本文件所有行组件共用）：create 只组一次骨架与事件，事件
  // 处理器读 row.__item 取最新数据；update 做原位补丁。数据刷新不重建 DOM，
  // 滚动位置、选中文本与焦点得以保全——这是繁忙看板保持可读的关键。
  function createSubagentRow() {
    var row = document.createElement("div");
    row.className = "session-card s-child";
    row.setAttribute("tabindex", "0");
    row.innerHTML =
      '<span class="s-child-arrow" aria-hidden="true">↳</span>' +
      '<span class="s-child-title"></span>' +
      '<span class="s-child-agent chip"></span>' +
      '<span class="s-child-state"></span>';
    row.addEventListener("click", function () {
      var it = row.__item;
      if (it) { openSession(it.node, it.session); }
    });
    return row;
  }

  // updateSubagentRow renders one live child task: indent + agent identity + the
  // coarse state. Clicking opens the same session detail dialog as any row.
  function updateSubagentRow(row, item) {
    var s = item.session, node = item.node;
    row.__item = item;
    row.classList.toggle("offline", !!node.stale);
    setText(row.querySelector(".s-child-title"), sessionLabelOf(s));
    var agent = s.parent.agent_id || "";
    if (s.parent.agent_type) { agent += (agent ? " · " : "") + s.parent.agent_type; }
    setText(row.querySelector(".s-child-agent"), agent);
    setText(row.querySelector(".s-child-state"), stateLabelOf(s));
  }

  function sessionMatches(item) {
    var s = item.session;
    if (!showSubagents && isSubagentSession(s)) { return false; }
    if (sessionFilterKind === "online" && item.node.stale) { return false; }
    if (sessionFilterKind === "attention" &&
        (item.node.stale || !isActionableAttention(s.attention) ||
         readAttention[attentionKey(item.node, s)])) { return false; }
    if (sessionFilterKind === "running" && (item.node.stale || !(s.busy || isActiveSessionState(s.state)))) { return false; }
    if (sessionFilterKind === "offline" && !item.node.stale) { return false; }
    if (sessionFilterText) {
      var q = sessionFilterText.toLowerCase();
      // 节点显示名一起参与匹配："找 laptop 上那个会话"是高频搜索意图；
      // 节点 id（裸 uuid）不参与，避免常见子串误命中。
      var hay = (nodeDisplayName(item.node) + " " + (s.title || "") + " " + (s.preview || "") + " " +
        (s.last_message || "") + " " + (s.status_text || "") + " " + s.session_id).toLowerCase();
      if (hay.indexOf(q) === -1) { return false; }
    }
    return true;
  }

  // 角色标签：前四个是网关快照里的 last_message_role；其余来自节点
  // /web/api/screen 的 transcript（会话面板按原样渲染这些行）。
  var ROLE_LABELS = {
    user: "用户",
    assistant: "助手",
    reasoning: "推理",
    tool: "工具",
    system: "系统",
    command: "命令",
    diagnostic: "诊断",
    runtime: "运行时",
  };

  function roleLabelOf(role) {
    return ROLE_LABELS[role] || (role || "消息");
  }

  // Reply box rendered inside each session card. It posts through the gateway
  // reverse proxy (/n/{node}/web/api/input) to the agent's own input endpoint:
  // the agent's forwarder injects the local write token and rewrites Origin,
  // so no extra auth wiring is needed here. Rendered only when the node
  // advertises the "input" capability (older agents keep the old card).
  function buildReplyForm(node, session) {
    var form = document.createElement("form");
    form.className = "s-reply";
    form.innerHTML =
      '<div class="s-reply-main">' +
      '<textarea class="s-reply-input" rows="1" placeholder="回复该会话…（Enter 发送，Shift+Enter 换行）"></textarea>' +
      '<button class="btn primary sm s-reply-send" type="submit">发送</button>' +
      '</div>' +
      '<span class="s-reply-hint" hidden></span>';
    var input = form.querySelector(".s-reply-input");
    var sendBtn = form.querySelector(".s-reply-send");
    var hint = form.querySelector(".s-reply-hint");
    var busy = false;
    // The input and 发送 share one row — the button never takes a line of its
    // own — and the textarea grows with the draft up to the CSS max-height.
    // (jsdom has no layout engine: scrollHeight === 0, so the sync is skipped.)
    var autoGrow = function () {
      if (!input.scrollHeight) { return; }
      input.style.height = "auto";
      input.style.height = Math.min(input.scrollHeight, 160) + "px";
    };
    input.addEventListener("input", autoGrow);
    // Touch keyboards: Enter should submit, not insert a newline. A soft
    // keyboard sends keydown with key === "Enter" and no shiftKey, so this is
    // the same branch desktop already uses; multi-line drafts still use the
    // on-screen hint (Shift+Enter) where a hardware keyboard exists.
    input.setAttribute("enterkeyhint", "send");
    // Keep clicks inside the reply box from bubbling out to the card.
    form.addEventListener("click", function (e) { e.stopPropagation(); });
    form.addEventListener("submit", function (e) {
      e.preventDefault();
      var text = (input.value || "").trim();
      if (!text || busy) { return; }
      if (node.stale) {
        notify("节点「" + nodeDisplayName(node) + "」离线，无法回复。", "error");
        return;
      }
      busy = true;
      sendBtn.disabled = true;
      hint.textContent = "发送中…";
      hint.hidden = false;
      api("/n/" + encodeURIComponent(node.node_id) + "/web/api/input", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ prompt: text })
      }).then(function (res) {
        return res.json().catch(function () { return null; }).then(function (body) {
          if (!res.ok) {
            throw new Error((body && body.reason) || ("HTTP " + res.status));
          }
          if (body && body.status === "queued") {
            notify("已发送到会话：" + sessionLabelOf(session), "ok");
            input.value = "";
            autoGrow();
            hint.hidden = true;
            hint.textContent = "";
          } else {
            throw new Error((body && body.reason) || (body && body.status) || "发送被拒绝");
          }
        });
      }).catch(function (err) {
        notify("回复失败：" + err.message, "error");
        hint.textContent = "发送失败，可重试";
      }).finally(function () {
        busy = false;
        sendBtn.disabled = false;
      });
    });
    // Enter submits; Shift+Enter inserts a newline (textarea default).
    input.addEventListener("keydown", function (e) {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        form.dispatchEvent(new Event("submit", { cancelable: true }));
      }
    });
    return form;
  }

  function sessionSorter(a, b) {
    if (sessionSort === "title") {
      return sessionLabelOf(a.session) < sessionLabelOf(b.session) ? -1 : 1;
    }
    if (sessionSort === "node") {
      var na = nodeDisplayName(a.node), nb = nodeDisplayName(b.node);
      return na < nb ? -1 : na > nb ? 1 : 0;
    }
    // Default sort: sessions that need the operator first (approval/question/
    // error), then running ones, then everything else by recency.
    var ra = attentionSortRank(a.session), rb = attentionSortRank(b.session);
    if (ra !== rb) { return ra - rb; }
    var ba = a.session.busy ? 0 : 1, bb = b.session.busy ? 0 : 1;
    if (ba !== bb) { return ba - bb; }
    var ta = new Date(a.session.updated_at || 0).getTime();
    var tb = new Date(b.session.updated_at || 0).getTime();
    return tb - ta;
  }

  function attentionSortRank(session) {
    switch (session.attention) {
      case "approval": return 0;
      case "question": return 1;
      case "error": return 2;
    }
    return isActiveSessionState(session.state) || session.busy ? 3 : 4;
  }

  /* ── Session view preferences (filter kind + sort, persisted) ── */

  var SESSION_PREFS_KEY = "aicli-gw-session-prefs";
  var SESSIONS_SCROLL_KEY = "aicli-gw-sessions-scroll";

  function loadSessionPrefs() {
    try {
      var p = JSON.parse(localStorage.getItem(SESSION_PREFS_KEY) || "null");
      if (!p) { return; }
      if (typeof p.kind === "string" && p.kind) { sessionFilterKind = p.kind; }
      if (typeof p.sort === "string" && p.sort) { sessionSort = p.sort; }
      if (p.tail === false) { tailEnabled = false; }
      if (p.subagents === false) { showSubagents = false; }
      if (p.md === false) { mdEnabled = false; }
    } catch (err) { /* storage unavailable */ }
  }

  function saveSessionPrefs() {
    try {
      localStorage.setItem(SESSION_PREFS_KEY, JSON.stringify({ kind: sessionFilterKind, sort: sessionSort, tail: tailEnabled, subagents: showSubagents, md: mdEnabled }));
    } catch (err) { /* storage unavailable */ }
  }

  // Reflect the current filter/sort on the static controls (boot restore,
  // deep links and programmatic changes share one path).
  function syncFilterControls() {
    document.querySelectorAll(".seg-btn").forEach(function (b) {
      var on = b.getAttribute("data-sfilter") === sessionFilterKind;
      b.classList.toggle("active", on);
      // These are toggle buttons in a group, not tabs: aria-pressed (not
      // aria-selected) is what conveys the current filter.
      b.setAttribute("aria-pressed", on ? "true" : "false");
    });
    var sortSel = el("session-sort");
    if (sortSel) { sortSel.value = sessionSort; }
  }

  function findSessionByKey(key) {
    if (!key) { return null; }
    var items = allSessions(lastNodes);
    for (var i = 0; i < items.length; i++) {
      if (items[i].node.node_id + "|" + items[i].session.session_id === key) { return items[i]; }
    }
    return null;
  }

  // ── Session reply drafts: keep user input across list updates ──
  // renderSessionTable patches keyed card DOM in place (see below); a card is
  // only re-created when it is new, re-parented or switched row kind. Drafts
  // are keyed by node+session so even that rebuild keeps the text (and
  // identical SSE frames must not steal focus or caret).
  var sessionDrafts = {};
  var lastSessionTableSignature = "";
  // Keyed DOM registry for the incremental board: key → element. Rows and
  // groups are patched in place so scroll positions, text selection, focus and
  // the live tail survive a data update instead of being wiped by a rebuild.
  var sessionRowEls = {};
  // Per-session UI state: which cards are expanded (kept across re-renders),
  // and the last rendered fingerprint (drives the "changed" pulse).
  var expandedSessions = {};
  var sessionLastSeen = {};
  // Device groups the operator folded; scroll restore runs once per page load.
  var collapsedGroups = {};
  var sessionsScrollRestored = false;
  var sessionsAutoSelected = false;
  // Live-output tail: global on/off (persisted) + per-card follow state.
  var tailEnabled = true;
  // 子代理行开关（持久化）：宿主按 AICLI_GATEWAY_REPORT_SUBAGENTS 上报子会话行，
  // 控制台默认显示，可折叠成"只看顶层会话"（docs/gateway/07-console.md）。
  var showSubagents = true;
  var tailFollow = {};
  // 消息正文 Markdown 渲染（持久化，默认开）：会话卡片最近消息、实时输出与
  // 详情弹窗共用内置渲染器 renderMarkdown——无外部依赖，先转义再渲染，不支持
  // 原始 HTML，链接仅放行 http(s)/mailto。
  var mdEnabled = true;

  // Fingerprint of the visible session fields; a change pulses the card so a
  // busy list still tells the operator where something moved.
  function sessionFingerprint(s) {
    return [
      s.state || "", s.attention || "", s.busy ? 1 : 0, s.status_text || "",
      s.updated_at || "", (s.last_message || "").length, s.last_message_role || "",
    ].join("|");
  }

  function sessionTableSignature(items) {
    var parts = items.map(function (item) {
      var s = item.session;
      return [
        item.node.node_id, item.node.stale ? 1 : 0, (item.node.capabilities || []).join(","),
        s.session_id, s.title || "", s.state || "", s.attention || "", s.busy ? 1 : 0,
        s.status_text || "", s.status_started_at || "", s.status_tail || "", s.updated_at || "",
        s.preview || "", s.last_message_role || "", s.last_message || "",
        s.parent ? (s.parent.agent_id || "") : "",
      ].join("\u0001");
    });
    // tailEnabled participates: toggling the live-output switch must rebuild
    // the cards even when the session data itself is unchanged.
    return sessionFilterKind + "\u0002" + sessionFilterText + "\u0002" + sessionSort +
      "\u0002" + (tailEnabled ? "1" : "0") + "\u0002" + (showSubagents ? "1" : "0") +
      "\u0002" + (mdEnabled ? "1" : "0") +
      "\u0002" + parts.join("\u0003");
  }

  function captureSessionDrafts() {
    document.querySelectorAll(".session-card[data-draft-key]").forEach(function (card) {
      var input = card.querySelector(".s-reply-input");
      if (!input) { return; }
      var key = card.getAttribute("data-draft-key");
      var focused = document.activeElement === input;
      var text = input.value || "";
      if (!text && !focused) { delete sessionDrafts[key]; return; }
      sessionDrafts[key] = {
        text: text,
        focused: focused,
        start: focused ? input.selectionStart : null,
        end: focused ? input.selectionEnd : null,
      };
    });
  }

  function restoreSessionDrafts(host) {
    host.querySelectorAll(".session-card[data-draft-key]").forEach(function (card) {
      var d = sessionDrafts[card.getAttribute("data-draft-key")];
      var input = card.querySelector(".s-reply-input");
      if (!d || !input) { return; }
      if (d.text) { input.value = d.text; }
      if (d.focused) {
        d.focused = false; // focus is restored once; later re-renders keep it
        input.focus();
        if (d.start !== null && d.start !== undefined) {
          try { input.setSelectionRange(d.start, d.end); } catch (err) { /* detached */ }
        }
      }
    });
  }

  // ── Active session states (shared by cards / filters / detail dialog) ──
  var ACTIVE_SESSION_STATES = { running: true, thinking: true, answering: true, waiting: true, review: true };

  function isActiveSessionState(state) {
    return !!ACTIVE_SESSION_STATES[state];
  }

  function formatStatusElapsed(ms) {
    var seconds = Math.max(0, Math.floor(ms / 1000));
    if (seconds < 60) { return seconds + "s"; }
    var minutes = Math.floor(seconds / 60);
    var rem = seconds % 60;
    if (minutes < 60) { return minutes + "m " + (rem < 10 ? "0" : "") + rem + "s"; }
    return Math.floor(minutes / 60) + "h " + (minutes % 60) + "m";
  }

  // The agent sends the activity anchor once; the console advances the clock
  // locally so a running turn never streams near-identical updates.
  function refreshStatusElapsed() {
    var now = Date.now();
    document.querySelectorAll(".s-status-elapsed[data-started]").forEach(function (node) {
      var started = Date.parse(node.getAttribute("data-started"));
      if (isNaN(started)) { return; }
      node.textContent = "· " + formatStatusElapsed(now - started);
    });
  }
  // Local 5s tick: elapsed clocks + relative labels (内容/同步/心跳) keep
  // advancing even when no SSE frame arrives.
  function refreshRelativeTimes() {
    document.querySelectorAll("[data-rel-ts]").forEach(function (node) {
      var ms = parseInt(node.getAttribute("data-rel-ts"), 10);
      if (!ms) { return; }
      var prefix = node.getAttribute("data-rel-prefix") || "";
      node.textContent = prefix + timeAgo(ms);
      var staleMs = parseInt(node.getAttribute("data-rel-stale-ms") || "0", 10);
      node.classList.toggle("age-stale", !!(staleMs && Date.now() - ms > staleMs));
    });
  }
  setInterval(function () {
    refreshStatusElapsed();
    refreshRelativeTimes();
    pruneStripEvents();
  }, 5000);

