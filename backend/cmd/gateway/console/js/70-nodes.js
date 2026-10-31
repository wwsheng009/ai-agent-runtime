// 70-nodes.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=103";

Object.assign(G, {
  renderInbox: renderInbox,
  nodeCard: nodeCard,
  renderNodeLists: renderNodeLists,
  renderAttentionBreakdown: renderAttentionBreakdown,
  renderActivityFeed: renderActivityFeed,
  renderAll: renderAll,
  renderDevices: renderDevices,
  appendDeviceMeta: appendDeviceMeta,
});

  /* ─────────────────────── rendering: inbox ──────────────────── */

  function renderInbox(nodes) {
    var list = S.el("inbox-list");
    if (!list) { return; }
    var count = S.el("inbox-count");
    var items = G.inboxItems(nodes);
    if (count) {
      count.textContent = items.length ? String(items.length) : "";
      count.hidden = !items.length;
    }
    var panel = S.el("inbox-panel");
    if (panel) { panel.hidden = !items.length; }
    list.innerHTML = "";
    items.forEach(function (item) {
      // A real <button>: the whole card is the action, so it must be reachable
      // by keyboard (a clickable <div> is not).
      var row = document.createElement("button");
      row.type = "button";
      row.className = "card card-action";
      // <button> only allows phrasing content, so the card body uses spans
      // (styled as blocks) rather than divs.
      row.innerHTML =
        '<span class="card-head"><span class="card-title"></span><span class="att-badge" hidden></span></span>' +
        '<span class="card-meta"></span>';
      row.querySelector(".card-title").textContent =
        G.nodeDisplayName(item.node) + " · " + G.sessionLabelOf(item.session);
      var b = row.querySelector(".att-badge");
      b.textContent = G.attentionLabel(item.session.attention);
      b.className = "att-badge att-" + item.session.attention;
      b.hidden = false;
      // 副标题给出「节点 · 状态」，node-id 只收进 hover 提示，避免与标题重复。
      row.querySelector(".card-meta").textContent =
        G.nodeDisplayName(item.node) + " · " + G.stateLabelOf(item.session);
      if (item.node.node_id) { row.title = "节点 ID：" + item.node.node_id; }
      row.addEventListener("click", function () {
        // 待办行点击与卡片「审批 / 回答」同语义：能面板内打开就聚焦交互区。
        G.openSessionInline(item.node, item.session, G.chatFocusOptsFor(item.session));
      });
      list.appendChild(row);
    });
    // 面板头部的「下一条待处理」按钮随同一份 inbox 数据刷新（面板关闭时自隐藏）。
    G.chatUpdateNextPending();
  }

  /* ─────────────────────── rendering: nodes ──────────────────── */

  function nodeCard(node) {
    var sessions = node.sessions || [];
    var card = document.createElement("div");
    card.className = "card" + (node.stale ? " offline" : "");
    card.innerHTML =
      '<div class="card-head"><span class="card-title"></span><span class="badge"></span></div>' +
      '<div class="card-meta"></div>' +
      '<div class="node-sessions"></div>' +
      '<div class="card-actions"></div>';
    card.querySelector(".card-title").innerHTML =
      '<span class="dot ' + (node.stale ? "offline" : "live") + '"></span>' +
      G.nodeDisplayName(node);
    card.querySelector(".badge").textContent =
      (node.kind || "agent") + " · 会话 " + sessions.length;
    var meta = card.querySelector(".card-meta");
    // node-id 属技术标识：不再作为副标题与标题重复展示。
    // 完整 id 保留在 hover 提示与「详情」弹窗，排障时仍可查看/复制。
    if (node.node_id) {
      card.setAttribute("data-node-id", node.node_id);
      card.title = "节点 ID：" + node.node_id;
    }
    meta.textContent = "";
    var hbMs = node.last_heartbeat ? new Date(node.last_heartbeat).getTime() : NaN;
    if (!isNaN(hbMs)) {
      var hb = document.createElement("span");
      hb.className = "n-heartbeat";
      G.setRelTime(hb, hbMs, node.stale ? "最后心跳 " : "心跳 ",
        node.stale ? 0 : 60000,
        "网关最后收到该节点心跳：" + G.absoluteTime(hbMs) + "（服务器时间）");
      meta.appendChild(hb);
    }

    // Session rows live in their own divider list; .card-actions stays
    // button-only so the two never share a flex row.
    var sessionsHost = card.querySelector(".node-sessions");
    var actions = card.querySelector(".card-actions");
    if (sessions.length) {
      sessions.slice(0, 3).forEach(function (session) {
        var row = document.createElement("div");
        row.className = "session-row";
        row.innerHTML =
          '<div class="session-info"><span class="session-title"></span><div class="session-preview" hidden></div></div>' +
          '<span class="att-badge" hidden></span><button class="btn ghost sm" type="button">打开</button>';
        row.querySelector(".session-title").textContent = G.sessionLabelOf(session);
        var p = row.querySelector(".session-preview");
        if (session.preview) {
          p.textContent = session.preview;
          p.hidden = false;
        }
        var b = row.querySelector(".att-badge");
        if (session.attention) {
          b.textContent = G.attentionLabel(session.attention);
          b.className = "att-badge att-" + session.attention;
        } else {
          b.hidden = true;
        }
        row.querySelector("button").addEventListener("click", function (e) {
          e.stopPropagation();
          G.openSessionInline(node, session);
        });
        sessionsHost.appendChild(row);
      });
      if (sessions.length > 3) {
        var more = document.createElement("button");
        more.className = "btn ghost sm";
        more.type = "button";
        more.textContent = "查看全部 " + sessions.length + " 个会话";
        more.addEventListener("click", function () { G.navigate("sessions"); });
        actions.appendChild(more);
      }
    } else if (!node.stale) {
      var open = document.createElement("button");
      open.className = "btn ghost sm";
      open.type = "button";
      open.textContent = "打开节点";
      if (G.nodeHasWebUI(node)) {
        open.addEventListener("click", function () {
          G.rememberPresence(node, null);
          window.open(G.nodeWebBase(node), "_blank");
        });
      } else {
        open.disabled = true;
        open.title = G.webUnavailableText(node);
      }
      actions.appendChild(open);
    }
    if (node.stale) {
      // UX-06: 破坏性动作默认收进 ⋯，不再作为实心红按钮常驻。
      actions.insertAdjacentHTML("beforeend",
        '<button class="btn ghost sm s-more" type="button" data-more aria-haspopup="menu" aria-expanded="false" title="更多操作" aria-label="更多操作">⋯</button>' +
        '<div class="s-menu" role="menu" aria-label="离线快照节点操作" hidden>' +
        '<button class="menu-item danger" type="button" role="menuitem" data-remove>移除该节点</button>' +
        '</div>');
      var staleMenu = actions.querySelector(".s-menu");
      var staleMore = actions.querySelector("[data-more]");
      G.bindCardMenu(card, staleMenu);
      staleMenu.querySelector("[data-remove]").addEventListener("click", function () {
        G.closeSessionMenus(false);
        G.confirmDestructive({
          title: "移除离线快照节点？",
          message: "移除离线快照节点 " + G.nodeDisplayName(node) +
            "？\n该节点的所有会话条目会一并删除（agent 重新上线会自动重新注册）。",
          trigger: staleMore,
          onConfirm: function () {
            return G.api("/api/nodes/" + encodeURIComponent(node.node_id), { method: "DELETE" })
              .then(function () {
                G.notify("节点已移除：" + G.nodeDisplayName(node), "ok");
                G.loadNodes();
              });
          }
        });
      });
    }

    if (node.node_id) {
      var copyNode = document.createElement("button");
      copyNode.className = "btn ghost sm";
      copyNode.type = "button";
      copyNode.textContent = "复制节点 ID";
      copyNode.title = "复制完整节点 ID：" + node.node_id;
      copyNode.setAttribute("aria-label", "复制节点 ID " + G.shortNodeID(node.node_id));
      copyNode.addEventListener("click", function () { G.copyText(node.node_id, copyNode); });
      actions.appendChild(copyNode);
    }
    return card;
  }

  function renderNodeLists(nodes) {
    var liveHost = S.el("node-live-list");
    var offHost = S.el("node-offline-list");
    var dashHost = S.el("dash-node-list");
    var staleNodes = nodes.filter(function (n) { return n.stale; });
    var clearBtn = S.el("clear-stale");
    if (clearBtn) {
      // The one-click cleanup is offered only while offline snapshots exist.
      clearBtn.hidden = !staleNodes.length;
      clearBtn.textContent = staleNodes.length ? "清除全部（" + staleNodes.length + "）" : "清除全部";
      clearBtn.setAttribute("aria-label", "清除全部离线快照节点，共 " + staleNodes.length + " 个");
    }
    if (liveHost) {
      liveHost.innerHTML = "";
      nodes.filter(function (n) { return !n.stale; }).forEach(function (n) {
        liveHost.appendChild(nodeCard(n));
      });
      if (!liveHost.children.length) {
        liveHost.innerHTML = '<p class="empty">没有在线节点 —— 在 aicli / runtime-server 配置 gateway.url 后自动接入。</p>';
      }
    }
    if (offHost) {
      offHost.innerHTML = "";
      staleNodes.forEach(function (n) {
        offHost.appendChild(nodeCard(n));
      });
      if (!offHost.children.length) {
        offHost.innerHTML = '<p class="empty">没有离线快照。</p>';
      }
    }
    if (dashHost) {
      dashHost.innerHTML = "";
      nodes.slice(0, 4).forEach(function (n) {
        dashHost.appendChild(nodeCard(n));
      });
    }
  }

  function renderAttentionBreakdown(nodes) {
    var panel = S.el("attention-breakdown");
    if (!panel) { return; }
    var counts = { approval: 0, question: 0, error: 0 };
    var items = G.inboxItems(nodes);
    items.forEach(function (item) {
      var kind = item.session.attention;
      if (counts[kind] !== undefined) { counts[kind]++; }
    });
    panel.hidden = !items.length;
    ["approval", "question", "error"].forEach(function (k) {
      var n = S.el("attn-" + k);
      if (n) { n.textContent = String(counts[k]); }
    });
  }

  function renderActivityFeed(nodes) {
    var host = S.el("activity-feed");
    if (!host) { return; }
    // Aggregate: every session with an updated_at, newest first, cap 8.
    var items = G.allSessions(nodes).filter(function (item) {
      return item.session.updated_at;
    }).sort(function (a, b) {
      return new Date(b.session.updated_at) - new Date(a.session.updated_at);
    }).slice(0, 8);
    host.innerHTML = "";
    if (!items.length) {
      host.innerHTML = '<p class="empty">暂无会话活动。</p>';
      return;
    }
    items.forEach(function (item) {
      var s = item.session;
      // Real <button> (keyboard reachable); the offline state is spelled out
      // instead of being conveyed by the dot colour alone.
      var row = document.createElement("button");
      row.type = "button";
      row.className = "activity-row card-action";
      var when = new Date(s.updated_at);
      var fresh = (Date.now() - when.getTime()) < 5 * 60 * 1000;
      row.innerHTML =
        '<span class="dot ' + (item.node.stale ? "offline" : "live") + '"></span>' +
        '<span class="activity-main">' +
        '<span class="activity-title"></span>' +
        '<span class="activity-when"></span>' +
        '</span>';
      row.querySelector(".activity-title").textContent =
        G.nodeDisplayName(item.node) + " · " + G.sessionLabelOf(s) +
        (s.attention ? "（" + G.attentionLabel(s.attention) + "）" : "") +
        (item.node.stale ? "（离线快照）" : "");
      row.querySelector(".activity-when").textContent =
        when.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) +
        (fresh ? " · 刚刚" : "");
      if (fresh) { row.classList.add("fresh"); }
      row.addEventListener("click", function () { G.openSessionInline(item.node, s); });
      host.appendChild(row);
    });
    var win = S.el("activity-window");
    if (win) { win.textContent = "最近 " + items.length + " 条"; }
  }

  function renderAll(nodes) {
    // v40 重构丢了这里的调用，导致 trackAttention（fresh attention →
    // toast/原生通知）整条链路从不执行；恢复并加控制台资产测试防回归。
    G.trackAttention(nodes);
    G.updateStats(nodes);
    renderInbox(nodes);
    renderAttentionBreakdown(nodes);
    renderActivityFeed(nodes);
    renderNodeLists(nodes);
    G.renderSessionTable(nodes);
    if (G.currentView() === "notifications") { G.renderNotifyPolicySessions(); }
  }

  /* ─────────────────────── rendering: devices ────────────────── */

  function renderDevices(devices) {
    var list = S.el("device-list");
    if (!list) { return; }
    var search = S.el("device-search");
    if (search && search.value !== S.deviceQuery) { search.value = S.deviceQuery; }
    var status = S.el("device-status-filter");
    if (status && status.value !== S.deviceStatusFilter) { status.value = S.deviceStatusFilter; }
    var query = String(S.deviceQuery || "").trim().toLowerCase();
    var filtered = devices.filter(function (d) {
      if (S.deviceStatusFilter === "active" && d.revoked) { return false; }
      if (S.deviceStatusFilter === "revoked" && !d.revoked) { return false; }
      if (!query) { return true; }
      return [d.name, d.id, d.prefix].join(" ").toLowerCase().indexOf(query) !== -1;
    });
    var count = S.el("device-count");
    if (count) {
      count.textContent = filtered.length === devices.length
        ? devices.length + " 个令牌"
        : filtered.length + " / " + devices.length + " 个令牌";
      count.hidden = devices.length === 0;
    }
    list.innerHTML = "";
    if (!devices.length) {
      list.innerHTML = '<p class="empty">还没有设备令牌 —— 签发一个给 aicli / runtime-server 接入用。</p>';
      return;
    }
    if (!filtered.length) {
      list.innerHTML = '<p class="empty">没有匹配的令牌。清除搜索或切换状态过滤后再试。</p>';
      return;
    }
    filtered.forEach(function (d) {
      var card = document.createElement("div");
      card.className = "card device-card" + (d.revoked ? " offline" : "");
      card.innerHTML =
        '<div class="card-head"><span class="card-title"></span><span class="status-chip ' + (d.revoked ? "chip-revoked" : "chip-active") + '"></span></div>' +
        '<div class="device-token-row"><span class="device-token-label">令牌标识</span><code class="device-prefix"></code><button class="btn ghost sm device-prefix-copy" type="button">复制</button></div>' +
        '<div class="card-meta"></div>' +
        '<div class="card-actions"></div>';
      card.querySelector(".card-title").textContent = d.name || d.id;
      card.querySelector(".status-chip").textContent = d.revoked ? "已撤销" : "有效";
      var prefixEl = card.querySelector(".device-prefix");
      // The API only exposes the 8-char identifier (the full token is hashed at
      // rest and shown once at issue time), so render it as the complete id.
      prefixEl.textContent = d.prefix || "—";
      prefixEl.title = "令牌标识（完整令牌仅在签发时显示一次）";
      if (d.revoked) { prefixEl.style.textDecoration = "line-through"; }
      var prefixCopy = card.querySelector(".device-prefix-copy");
      prefixCopy.setAttribute("aria-label", "复制令牌标识 " + (d.prefix || ""));
      prefixCopy.title = "复制令牌标识（不是完整令牌）";
      prefixCopy.addEventListener("click", function () { G.copyText(d.prefix || "", prefixCopy); });
      var meta = card.querySelector(".card-meta");
      appendDeviceMeta(meta, "记录 ID " + (d.id || "—"), "meta-id");
      appendDeviceMeta(meta, "签发于 " + (d.created_at ? new Date(d.created_at).toLocaleString() : "—"), "meta-time");
      if (d.last_used && d.last_used.indexOf("0001") === -1) {
        appendDeviceMeta(meta, "最近使用 " + new Date(d.last_used).toLocaleString(), "meta-time");
      }
      // UX-06: 撤销/删除收进 ⋯，列表行不再常驻实心红按钮。
      var deviceActions = card.querySelector(".card-actions");
      deviceActions.insertAdjacentHTML("beforeend",
        '<button class="btn ghost sm s-more" type="button" data-more aria-haspopup="menu" aria-expanded="false" title="更多操作" aria-label="更多操作">⋯</button>' +
        '<div class="s-menu" role="menu" aria-label="设备令牌操作" hidden>' +
        '<button class="menu-item" type="button" role="menuitem" data-copy-id>复制记录 ID</button>' +
        '<button class="menu-item danger" type="button" role="menuitem" data-action>' +
        (d.revoked ? "删除记录" : "撤销令牌") +
        '</button>' +
        '</div>');
      var deviceMenu = deviceActions.querySelector(".s-menu");
      var deviceMore = deviceActions.querySelector("[data-more]");
      G.bindCardMenu(card, deviceMenu);
      deviceMenu.querySelector("[data-copy-id]").addEventListener("click", function () {
        G.copyText(d.id || "", this);
        G.closeSessionMenus(true);
      });
      deviceMenu.querySelector("[data-action]").addEventListener("click", function () {
        G.closeSessionMenus(false);
        var purge = !!d.revoked;
        G.confirmDestructive({
          title: purge ? "删除记录？" : "撤销令牌？",
          message: purge
            ? "彻底删除已撤销令牌「" + (d.name || d.id) + "」的记录？\n此操作不可恢复（令牌本身已失效）。"
            : "撤销令牌「" + (d.name || d.id) + "」？\n使用它的 agent 将无法再注册；撤销后可选择彻底删除记录。",
          trigger: deviceMore,
          onConfirm: function () {
            return G.api("/api/devices/" + encodeURIComponent(d.id) + (purge ? "?purge=1" : ""), { method: "DELETE" })
              .then(function () {
                G.notify((purge ? "令牌记录已删除：" : "令牌已撤销：") + (d.name || d.id), "ok");
                G.loadDevices();
              });
          }
        });
      });

      list.appendChild(card);
    });
  }

  function appendDeviceMeta(host, text, className) {
    if (!host) { return; }
    if (host.childNodes.length) {
      var sep = document.createElement("span");
      sep.className = "meta-sep";
      sep.textContent = " · ";
      host.appendChild(sep);
    }
    var part = document.createElement("span");
    part.className = className || "";
    part.textContent = text;
    host.appendChild(part);
  }
