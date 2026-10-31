// 80-mounts.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=100";

Object.assign(G, {
  loadMounts: loadMounts,
  mountLabels: mountLabels,
  mountLabelList: mountLabelList,
  mountToolCount: mountToolCount,
  mountHostText: mountHostText,
  mountProfileText: mountProfileText,
  mountVersionText: mountVersionText,
  appendMountFact: appendMountFact,
  appendMountChips: appendMountChips,
  appendMountDetails: appendMountDetails,
  appendMountFacts: appendMountFacts,
  renderMounts: renderMounts,
  openMountTokens: openMountTokens,
  hideMountTokenReveal: hideMountTokenReveal,
  syncMountTokenTTL: syncMountTokenTTL,
  closeMountTokenModal: closeMountTokenModal,
  showMountToken: showMountToken,
  loadMountTokens: loadMountTokens,
  validTokenTime: validTokenTime,
  mountTokenState: mountTokenState,
  renderMountTokens: renderMountTokens,
});

  /* ─────────────────────── MCP mounts ────────────────────────── */

  function loadMounts() {
    return G.apiJSON("/api/mounts").then(function (r) {
      if (r.res.status === 401) { G.setAuthed(false); return; }
      S.lastMounts = (r.body && r.body.mounts) || [];
      renderMounts(S.lastMounts);
    });
  }

  function mountLabels(m) {
    return (m && m.labels) || {};
  }

  function mountLabelList(value) {
    if (!Array.isArray(value)) { return []; }
    return value.filter(function (item) {
      return typeof item === "string" && item.trim() !== "";
    });
  }

  function mountToolCount(m) {
    var labels = mountLabels(m);
    var count = Number(labels.tool_count);
    if (!isFinite(count) || count < 0) { count = 0; }
    return Math.max(count, mountLabelList(labels.tools).length);
  }

  function mountHostText(labels) {
    var host = labels.hostname || "—";
    var platform = [labels.os, labels.arch].filter(Boolean).join("/");
    return platform ? host + " · " + platform : host;
  }

  function mountProfileText(labels) {
    var profile = labels.profile || "—";
    if (labels.read_only && profile !== "readonly") {
      profile += " · 只读";
    }
    if (labels.tool_prefix) {
      profile += " · " + labels.tool_prefix;
    }
    return profile;
  }

  function mountVersionText(labels) {
    var bits = [];
    if (labels.version) { bits.push(labels.version); }
    if (labels.instance) { bits.push("实例 " + labels.instance); }
    if (labels.backend) { bits.push(labels.backend); }
    return bits.length ? bits.join(" · ") : "—";
  }

  function appendMountFact(facts, label, value, asCode) {
    var row = document.createElement("div");
    row.className = "mcp-fact";
    var name = document.createElement("span");
    name.className = "mcp-fact-label";
    name.textContent = label;
    var text = document.createElement(asCode ? "code" : "span");
    text.className = "mcp-fact-value";
    text.textContent = value;
    row.appendChild(name);
    row.appendChild(text);
    facts.appendChild(row);
  }

  function appendMountChips(container, values) {
    var list = document.createElement("div");
    list.className = "mcp-chip-list";
    values.forEach(function (value) {
      var chip = document.createElement("code");
      chip.className = "mcp-chip";
      chip.textContent = value;
      list.appendChild(chip);
    });
    container.appendChild(list);
  }

  function appendMountDetails(host, title, values, emptyText) {
    var details = document.createElement("details");
    details.className = "mcp-details";
    var summary = document.createElement("summary");
    summary.textContent = title;
    summary.title = "展开查看";
    details.appendChild(summary);
    if (values.length) {
      appendMountChips(details, values);
    } else if (emptyText) {
      var empty = document.createElement("p");
      empty.className = "mcp-detail-empty";
      empty.textContent = emptyText;
      details.appendChild(empty);
    }
    host.appendChild(details);
  }

  function appendMountFacts(factsHost, detailsHost, m) {
    var labels = mountLabels(m);
    var facts = factsHost;
    if (!facts) { return; }
    appendMountFact(facts, "主机", mountHostText(labels));
    appendMountFact(facts, "Profile", mountProfileText(labels));
    appendMountFact(facts, "工作区", labels.workspace || "—", true);
    appendMountFact(facts, "版本", mountVersionText(labels));

    var tools = mountLabelList(labels.tools);
    var toolCount = mountToolCount(m);
    if (toolCount > 0 || tools.length > 0) {
      appendMountDetails(
        detailsHost,
        "工具（" + (toolCount || tools.length) + "）",
        tools,
        "节点只上报了数量，没有工具名（旧版节点或列表被裁剪）。"
      );
    }
    var roots = mountLabelList(labels.roots);
    if (roots.length > 0) {
      appendMountDetails(detailsHost, "目录（" + roots.length + "）", roots, "");
    }
  }

  function renderMounts(mounts) {
    var list = S.el("mcp-list");
    if (!list) { return; }
    var summary = S.el("mcp-summary");
    var live = 0, disabled = 0, offline = 0;
    mounts.forEach(function (m) {
      if (m.disabled) { disabled++; } else if (m.stale) { offline++; } else { live++; }
    });
    if (summary) {
      summary.textContent = mounts.length
        ? (mounts.length + " 个 · 在线 " + live +
           (disabled ? " · 禁用 " + disabled : "") +
           (offline ? " · 离线 " + offline : ""))
        : "";
    }
    list.innerHTML = "";
    if (!mounts.length) {
      list.innerHTML = '<p class="empty">还没有 MCP 挂载 —— 在节点上运行 aicli-remote-tools serve 并连到本网关后会自动出现。</p>';
      return;
    }
    mounts.forEach(function (m) {
      var statusText = m.disabled ? "已禁用" : (m.stale ? "离线" : "在线");
      var chip = m.disabled ? "chip-revoked" : (m.stale ? "chip-offline" : "chip-active");
      var card = document.createElement("div");
      card.className = "card mcp-card";
      card.innerHTML =
        '<div class="card-head"><span class="card-title"></span>' +
        '<span class="status-chip ' + chip + '"></span></div>' +
        '<div class="mcp-slug"><code></code></div>' +
        '<div class="mcp-endpoint"><code class="mcp-url"></code></div>' +
        '<div class="mcp-facts"></div>' +
        '<div class="mcp-details-host"></div>' +
        '<div class="card-meta"></div>' +
        '<div class="card-actions"></div>';
      card.querySelector(".card-title").textContent = m.name || m.slug;
      card.querySelector(".status-chip").textContent = statusText;
      card.querySelector(".mcp-slug code").textContent = "/m/" + m.slug + "/mcp";
      var urlEl = card.querySelector(".mcp-url");
      urlEl.textContent = m.public_url || "（网关未配置 public-base-url）";
      var bits = ["节点 " + (G.shortNodeID(m.node_id) || "—")];
      if (m.local_path) { bits.push("本地 " + m.local_path); }
      if (m.tokens != null) { bits.push(m.tokens + " 个令牌"); }
      if (m.limits && m.limits.max_body_bytes) {
        bits.push(Math.round(m.limits.max_body_bytes / 1024 / 1024) + " MiB");
      }
      if (m.last_seen) { bits.push("最近 " + new Date(m.last_seen).toLocaleString()); }
      card.querySelector(".card-meta").textContent = bits.join(" · ");
      appendMountFacts(
        card.querySelector(".mcp-facts"),
        card.querySelector(".mcp-details-host"),
        m
      );

      var actions = card.querySelector(".card-actions");
      if (m.public_url) {
        var copy = document.createElement("button");
        copy.className = "btn ghost sm";
        copy.type = "button";
        copy.textContent = "复制地址";
        copy.addEventListener("click", function () { G.copyText(m.public_url, copy); });
        actions.appendChild(copy);
      }
      var tokensBtn = document.createElement("button");
      tokensBtn.className = "btn ghost sm";
      tokensBtn.type = "button";
      tokensBtn.textContent = m.tokens ? "令牌 · " + m.tokens : "令牌";
      tokensBtn.addEventListener("click", function () { openMountTokens(m); });
      actions.appendChild(tokensBtn);
      var logsBtn = document.createElement("button");
      logsBtn.className = "btn ghost sm";
      logsBtn.type = "button";
      logsBtn.textContent = "日志";
      logsBtn.addEventListener("click", function () {
        G.navigate("logs", "slug=" + encodeURIComponent(m.slug));
      });
      actions.appendChild(logsBtn);
      var toggle = document.createElement("button");
      toggle.className = "btn sm " + (m.disabled ? "primary" : "ghost");
      toggle.type = "button";
      toggle.textContent = m.disabled ? "启用" : "禁用";
      toggle.addEventListener("click", function () {
        var verb = m.disabled ? "启用" : "禁用";
        var path = "/api/mounts/" + encodeURIComponent(m.slug) + (m.disabled ? "/enable" : "/disable");
        var opts = { method: "POST", headers: { "Content-Type": "application/json" } };
        if (!m.disabled) { opts.body = JSON.stringify({ reason: "console" }); }
        function apply() {
          return G.api(path, opts).then(function (res) {
            if (!res.ok) { throw new Error("HTTP " + res.status); }
            G.notify("已" + verb + "：" + (m.name || m.slug), "ok");
            loadMounts();
          });
        }
        if (m.disabled) {
          apply().catch(function (err) { G.notify(verb + "失败：" + err, "error"); });
          return;
        }
        G.confirmDestructive({
          title: "禁用挂载？",
          message: "禁用挂载「" + (m.name || m.slug) + "」？\n公网入口会立即返回 403，节点与地址保留。",
          trigger: toggle,
          onConfirm: apply
        });
      });
      actions.appendChild(toggle);

      // UX-06: 仅离线挂载存在删除动作，因此只有它需要 ⋯；
      // 菜单项使用 danger 文本色，而不是实心红按钮。
      if (m.stale) {
        actions.insertAdjacentHTML("beforeend",
          '<button class="btn ghost sm s-more" type="button" data-more aria-haspopup="menu" aria-expanded="false" title="更多操作" aria-label="更多操作">⋯</button>' +
          '<div class="s-menu" role="menu" aria-label="MCP 挂载操作" hidden>' +
          '<button class="menu-item danger" type="button" role="menuitem" data-delete>删除离线挂载</button>' +
          '</div>');
        var mountMenu = actions.querySelector(".s-menu");
        var mountMore = actions.querySelector("[data-more]");
        G.bindCardMenu(card, mountMenu);
        mountMenu.querySelector("[data-delete]").addEventListener("click", function () {
          G.closeSessionMenus(false);
          G.confirmDestructive({
            title: "删除离线挂载？",
            message: "删除离线挂载「" + (m.name || m.slug) + "」？\n地址将不再保留，持有该 URL 的客户端会得到 404。",
            trigger: mountMore,
            onConfirm: function () {
              return G.api("/api/mounts/" + encodeURIComponent(m.slug), { method: "DELETE" })
                .then(function (res) {
                  if (!res.ok) { throw new Error("HTTP " + res.status); }
                  G.notify("已删除挂载：" + (m.name || m.slug), "ok");
                  loadMounts();
                });
            }
          });
        });
      }

      list.appendChild(card);
    });
  }

  /* ─────────────────── MCP mount tokens ──────────────────────── */

  function openMountTokens(m) {
    if (!m) { return; }
    S.mountTokenTarget = { slug: m.slug, name: m.name || m.slug };
    S.el("mount-token-modal-title").textContent = "MCP 令牌 · " + S.mountTokenTarget.name;
    S.el("mount-token-modal-sub").textContent =
      "外部客户端调用 " + (m.public_url || ("/m/" + m.slug + "/mcp")) +
      " 的凭据。明文只在签发时显示一次；网关只保存 SHA-256 与指纹。";
    S.el("mount-token-name").value = "";
    S.el("mount-token-ttl").value = "7776000";
    S.el("mount-token-days").value = "";
    syncMountTokenTTL();
    hideMountTokenReveal();
    G.openModal("mount-token-modal");
    S.el("mount-token-name").focus();
    loadMountTokens();
  }

  function hideMountTokenReveal() {
    var box = S.el("mount-token-reveal");
    if (!box) { return; }
    box.hidden = true;
    box.innerHTML = "";
  }

  function syncMountTokenTTL() {
    var select = S.el("mount-token-ttl");
    var days = S.el("mount-token-days");
    var hint = S.el("mount-token-ttl-hint");
    if (!select) { return; }
    var custom = select.value === "custom";
    var never = select.value === "never";
    if (days) {
      days.hidden = !custom;
      if (custom) { days.focus(); }
    }
    if (hint) { hint.hidden = !never; }
  }

  function closeMountTokenModal() {
    // Never leave the one-time plaintext in a hidden DOM node after close.
    hideMountTokenReveal();
    S.mountTokenTarget = null;
    G.closeModal("mount-token-modal");
  }

  function showMountToken(token) {
    var box = S.el("mount-token-reveal");
    if (!box) { return; }
    box.innerHTML = "";
    var hint = document.createElement("div");
    hint.className = "token-hint";
    hint.textContent = "完整令牌仅显示这一次，关闭后无法再次查看（只能撤销重建）——请立即复制保存：";
    var row = document.createElement("div");
    row.className = "token-row";
    var strong = document.createElement("strong");
    strong.textContent = token;
    var copy = document.createElement("button");
    copy.className = "btn ghost sm";
    copy.type = "button";
    copy.textContent = "复制";
    copy.addEventListener("click", function () { G.copyText(token, copy); });
    row.appendChild(strong);
    row.appendChild(copy);
    var note = document.createElement("div");
    note.className = "token-note";
    note.textContent = "客户端请求头：Authorization: Bearer <上方令牌>";
    box.appendChild(hint);
    box.appendChild(row);
    box.appendChild(note);
    box.hidden = false;
  }

  function loadMountTokens() {
    if (!S.mountTokenTarget) { return Promise.resolve(); }
    var slug = S.mountTokenTarget.slug;
    return G.apiJSON("/api/mounts/" + encodeURIComponent(slug) + "/tokens").then(function (r) {
      if (r.res.status === 401) { G.setAuthed(false); return; }
      if (!r.res.ok) {
        G.notify("读取 MCP 令牌失败：HTTP " + r.res.status, "error");
        return;
      }
      renderMountTokens((r.body && r.body.tokens) || []);
    }).catch(function (err) {
      G.notify("读取 MCP 令牌失败：" + err, "error");
    });
  }

  function validTokenTime(raw) {
    if (!raw) { return null; }
    var d = new Date(raw);
    if (isNaN(d.getTime()) || d.getUTCFullYear() <= 1) { return null; }
    return d;
  }

  function mountTokenState(t) {
    if (t.revoked) { return { text: "已撤销", chip: "chip-revoked" }; }
    var exp = validTokenTime(t.expires_at);
    if (exp && exp.getTime() <= Date.now()) {
      return { text: "已过期", chip: "chip-revoked" };
    }
    return { text: "有效", chip: "chip-active" };
  }

  function renderMountTokens(tokens) {
    var list = S.el("mount-token-list");
    if (!list) { return; }
    list.innerHTML = "";
    if (!tokens.length) {
      list.innerHTML = '<p class="empty">还没有令牌 —— 在上方签发一个给外部 MCP 客户端。</p>';
      return;
    }
    var targetSlug = S.mountTokenTarget ? S.mountTokenTarget.slug : "";
    tokens.forEach(function (t) {
      var state = mountTokenState(t);
      var card = document.createElement("div");
      card.className = "card mcp-token-card";
      card.innerHTML =
        '<div class="card-head"><span class="card-title"></span>' +
        '<span class="status-chip ' + state.chip + '"></span></div>' +
        '<div class="mcp-token-prefix"><code></code></div>' +
        '<div class="card-meta"></div>' +
        '<div class="card-actions"></div>';
      card.querySelector(".card-title").textContent = t.name || "未命名调用方";
      card.querySelector(".status-chip").textContent = state.text;
      var prefix = card.querySelector(".mcp-token-prefix code");
      prefix.textContent = t.prefix;
      prefix.title = "令牌指纹（完整令牌只在签发时显示一次）";
      if (t.revoked) { prefix.style.textDecoration = "line-through"; }
      var bits = ["id " + t.id];
      var created = validTokenTime(t.created_at);
      if (created) { bits.push("签发 " + created.toLocaleString()); }
      var used = validTokenTime(t.last_used);
      if (used) { bits.push("最近使用 " + used.toLocaleString()); }
      var expires = validTokenTime(t.expires_at);
      if (expires) {
        bits.push("到期 " + expires.toLocaleString());
      } else {
        bits.push("不过期");
      }
      if (t.mounts && t.mounts.length > 1) {
        bits.push("作用域 " + t.mounts.join(", "));
      }
      card.querySelector(".card-meta").textContent = bits.join(" · ");

      var actions = card.querySelector(".card-actions");
      if (!t.revoked) {
        var revoke = document.createElement("button");
        revoke.className = "btn ghost sm";
        revoke.type = "button";
        revoke.textContent = "撤销";
        revoke.addEventListener("click", function () {
          G.confirmDestructive({
            title: "撤销 MCP 令牌？",
            message: "撤销令牌「" + (t.name || t.id) + "」？\n使用它的客户端下一次请求即 401。",
            trigger: revoke,
            confirmText: "撤销",
            onConfirm: function () {
              return G.api("/api/mounts/" + encodeURIComponent(targetSlug) + "/tokens/" + encodeURIComponent(t.id), {
                method: "DELETE",
              }).then(function (res) {
                if (!res.ok) { throw new Error("HTTP " + res.status); }
                G.notify("令牌已撤销：" + (t.name || t.id), "ok");
                loadMountTokens();
                loadMounts();
              });
            }
          });
        });
        actions.appendChild(revoke);
      } else {
        var del = document.createElement("button");
        del.className = "btn ghost sm";
        del.type = "button";
        del.textContent = "删除记录";
        del.addEventListener("click", function () {
          G.confirmDestructive({
            title: "删除令牌记录？",
            message: "彻底删除已撤销令牌「" + (t.name || t.id) + "」的记录？\n此操作不可恢复。",
            trigger: del,
            confirmText: "删除记录",
            onConfirm: function () {
              return G.api("/api/mounts/" + encodeURIComponent(targetSlug) + "/tokens/" +
                  encodeURIComponent(t.id) + "?purge=1", { method: "DELETE" })
                .then(function (res) {
                  if (!res.ok) { throw new Error("HTTP " + res.status); }
                  G.notify("令牌记录已删除", "ok");
                  loadMountTokens();
                });
            }
          });
        });
        actions.appendChild(del);
      }
      list.appendChild(card);
    });
  }
