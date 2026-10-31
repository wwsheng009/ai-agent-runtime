// 60-markdown.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=98";

Object.assign(G, {
  escapeHTML: escapeHTML,
  safeLinkHref: safeLinkHref,
  mdInline: mdInline,
  mdSplitCells: mdSplitCells,
  mdIsSeparatorRow: mdIsSeparatorRow,
  mdAlignOf: mdAlignOf,
  mdTable: mdTable,
  renderMarkdown: renderMarkdown,
  setMessageHTML: setMessageHTML,
  chatSetStreamingText: chatSetStreamingText,
  chatResetStreamingText: chatResetStreamingText,
  chatNewStreamChunk: chatNewStreamChunk,
  chatSplitStreamingChunks: chatSplitStreamingChunks,
  chatSetStreamingMarkdown: chatSetStreamingMarkdown,
  chatFlushMarkdownChunks: chatFlushMarkdownChunks,
  chatMarkdownStableBoundary: chatMarkdownStableBoundary,
  setText: setText,
  reconcileChildren: reconcileChildren,
  pruneSessionRowEls: pruneSessionRowEls,
  ensureSessionRow: ensureSessionRow,
  ensureSessionGroup: ensureSessionGroup,
  renderSessionTable: renderSessionTable,
  createSessionGroup: createSessionGroup,
  updateSessionGroup: updateSessionGroup,
  closeSessionMenus: closeSessionMenus,
  positionSessionMenu: positionSessionMenu,
  visibleMenuItems: visibleMenuItems,
  bindCardMenu: bindCardMenu,
  repositionOpenSessionMenus: repositionOpenSessionMenus,
  syncExpandButton: syncExpandButton,
  createSessionCard: createSessionCard,
  requestWindowAction: requestWindowAction,
  updateSessionCard: updateSessionCard,
});

  /* ─────────────────────── markdown rendering ────────────────── */

  function escapeHTML(text) {
    return String(text).replace(/[&<>"']/g, function (ch) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[ch];
    });
  }

  // safeLinkHref 只放行 http(s)/mailto 链接（输入已转义）：其余协议整体丢弃，
  // 防止 agent 输出里的 javascript: 等伪协议被点开。
  function safeLinkHref(href) {
    return /^(https?:\/\/|mailto:)/i.test(href) ? href : "";
  }

  // mdInline 渲染一行内的标记（输入为未转义的原始文本，内部先转义）。代码
  // span 与链接先抽取成占位符，避免强调/删除线规则改写它们内部的文本。
  function mdInline(raw) {
    var tokens = [];
    var stash = function (html) {
      tokens.push(html);
      return "\u0000" + (tokens.length - 1) + "\u0000";
    };
    var out = escapeHTML(raw)
      .replace(/`([^`\n]+)`/g, function (m, code) { return stash("<code>" + code + "</code>"); })
      .replace(/\[([^\]\n]+)\]\(([^)\s]+)\)/g, function (m, label, href) {
        var safe = safeLinkHref(href);
        if (!safe) { return m; }
        return stash('<a href="' + safe + '" target="_blank" rel="noopener noreferrer">' + label + "</a>");
      });
    out = out
      .replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>")
      .replace(/__([^_\n]+)__/g, "<strong>$1</strong>")
      .replace(/~~([^~\n]+)~~/g, "<del>$1</del>")
      .replace(/\*([^*\n]+)\*/g, "<em>$1</em>")
      .replace(/(^|[^A-Za-z0-9_])_([^_\n]+)_(?![A-Za-z0-9_])/g, "$1<em>$2</em>");
    return out.replace(/\u0000(\d+)\u0000/g, function (m, i) { return tokens[+i]; });
  }

  // renderMarkdown 是有意收敛的子集渲染器（消息正文用）：标题、粗体/斜体/
  // 删除线、行内代码、围栏代码块、有序/无序列表、引用、分割线、链接与 GFM
  // 管道表格；段落内
  // 的单个换行保留为 <br>（聊天语义）。先逐行判块、再行内渲染；输出不含原始
  // HTML（输入全量转义），可安全写入 innerHTML。

  // GFM 管道表格：行必须以 | 起止（与节点 micro web client 的 markdown.js 同一
  // 识别规则，保证同一条消息在面板与原版 Web 里渲染一致）；第二行是分隔行
  // （:--- / :---: / ---:）时首行为表头，否则整块当无表头表格。
  S.MD_TABLE_ROW = /^ {0,3}\|.*\|[ \t]*$/;

  function mdSplitCells(row) {
    var s = String(row).trim();
    if (s.charAt(0) === "|") { s = s.slice(1); }
    if (s.charAt(s.length - 1) === "|") { s = s.slice(0, -1); }
    return s.split("|").map(function (c) { return c.trim(); });
  }

  function mdIsSeparatorRow(cells) {
    return cells.length > 0 && cells.every(function (c) { return /^:?-{3,}:?$/.test(c); });
  }

  // mdAlignOf 只可能返回 left/center/right/""，因此下面的 style 属性拼接没有
  // 注入面（值来自固定集合，不来自消息内容）。
  function mdAlignOf(cell) {
    if (/^:.*:$/.test(cell)) { return "center"; }
    if (/^:/.test(cell)) { return "left"; }
    if (/:$/.test(cell)) { return "right"; }
    return "";
  }

  function mdTable(rows) {
    var hasHeader = rows.length > 1 && mdIsSeparatorRow(mdSplitCells(rows[1]));
    var header = hasHeader ? mdSplitCells(rows[0]) : null;
    var aligns = hasHeader ? mdSplitCells(rows[1]).map(mdAlignOf) : [];
    var body = (hasHeader ? rows.slice(2) : rows).map(mdSplitCells);
    var styleAt = function (i) {
      return aligns[i] ? ' style="text-align:' + aligns[i] + '"' : "";
    };
    var h = "";
    if (header) {
      h += "<thead><tr>";
      header.forEach(function (c, i) { h += "<th" + styleAt(i) + ">" + mdInline(c) + "</th>"; });
      h += "</tr></thead>";
    }
    if (body.length) {
      h += "<tbody>";
      body.forEach(function (r) {
        h += "<tr>" + r.map(function (c, i) {
          return "<td" + styleAt(i) + ">" + mdInline(c) + "</td>";
        }).join("") + "</tr>";
      });
      h += "</tbody>";
    }
    return "<table>" + h + "</table>";
  }

  function renderMarkdown(text) {
    var lines = String(text || "").replace(/\r\n?/g, "\n").split("\n");
    var html = [];
    var para = [];
    var flushPara = function () {
      if (!para.length) { return; }
      html.push("<p>" + para.map(mdInline).join("<br>") + "</p>");
      para = [];
    };
    var i = 0;
    while (i < lines.length) {
      var line = lines[i];
      var fence = line.match(/^\s*(```|~~~)\s*([\w.+#-]*)\s*$/);
      if (fence) {
        flushPara();
        var buf = [];
        i++;
        for (; i < lines.length; i++) {
          if (/^\s*(```|~~~)\s*$/.test(lines[i])) { i++; break; }
          buf.push(lines[i]);
        }
        html.push('<pre class="md-code"><code>' + escapeHTML(buf.join("\n")) + "</code></pre>");
        continue;
      }
      if (/^\s*(-{3,}|\*{3,}|_{3,})\s*$/.test(line)) { flushPara(); html.push("<hr>"); i++; continue; }
      var head = line.match(/^\s*(#{1,6})\s+(.*)$/);
      if (head) {
        flushPara();
        var lvl = Math.min(6, head[1].length + 2); // # → h3：#/## 对卡片字号太粗放
        html.push("<h" + lvl + ">" + mdInline(head[2]) + "</h" + lvl + ">");
        i++;
        continue;
      }
      if (/^\s*>\s?/.test(line)) {
        flushPara();
        var quote = [];
        while (i < lines.length && /^\s*>\s?/.test(lines[i])) {
          quote.push(lines[i].replace(/^\s*>\s?/, ""));
          i++;
        }
        html.push("<blockquote>" + quote.map(mdInline).join("<br>") + "</blockquote>");
        continue;
      }
      // 表格：连续 | 行（至少两行）——与 micro web client 的块级扫描同一位置
      // （列表之前），否则单元格里的 "-" 会被列表规则先吃掉。
      if (S.MD_TABLE_ROW.test(line)) {
        var trows = [];
        var tj = i;
        while (tj < lines.length && S.MD_TABLE_ROW.test(lines[tj])) { trows.push(lines[tj]); tj++; }
        if (trows.length >= 2) {
          flushPara();
          html.push(mdTable(trows));
          i = tj;
          continue;
        }
      }
      var ul = line.match(/^\s*[-*+]\s+(.*)$/);
      var ol = line.match(/^\s*\d+[.)]\s+(.*)$/);
      if (ul || ol) {
        flushPara();
        var ordered = !!ol;
        var items = [];
        while (i < lines.length) {
          var item = lines[i].match(ordered ? /^\s*\d+[.)]\s+(.*)$/ : /^\s*[-*+]\s+(.*)$/);
          if (!item) { break; }
          items.push("<li>" + mdInline(item[1]) + "</li>");
          i++;
        }
        html.push(ordered ? "<ol>" + items.join("") + "</ol>" : "<ul>" + items.join("") + "</ul>");
        continue;
      }
      if (!line.trim()) { flushPara(); i++; continue; }
      para.push(line);
      i++;
    }
    flushPara();
    return html.join("");
  }

  // setMessageHTML 渲染消息正文（Markdown 开关决定富文本或纯文本），并且只在
  // 源文本/渲染模式变化时写入——流式刷新因此不会触碰未变化的 DOM。
  function setMessageHTML(node, text) {
    if (!node) { return; }
    var src = text || "";
    if (node.__mdSrc === src && node.__mdOn === S.mdEnabled) { return; }
    node.__mdSrc = src;
    node.__mdOn = S.mdEnabled;
    if (S.mdEnabled) {
      node.classList.add("md");
      node.innerHTML = renderMarkdown(src);
    } else {
      node.classList.remove("md");
      setText(node, src);
    }
  }

  // chatSetStreamingText appends only the new suffix to the live text.
  // Long text is split at line boundaries into content-visibility chunks so a
  // 30KB answer does not force the browser to re-layout all previous lines on
  // every token. The concatenated textContent remains byte-for-byte the source.
  function chatSetStreamingText(node, text) {
    if (!node) { return; }
    var src = text || "";
    var chunks = node.__streamChunks;
    if (!chunks || !chunks.length || chunks[0].el.parentNode !== node) {
      chunks = chatResetStreamingText(node, src);
    } else {
      var current = "";
      chunks.forEach(function (chunk) { current += chunk.node.data; });
      if (src.indexOf(current) !== 0) {
        chunks = chatResetStreamingText(node, src);
      } else if (src.length > current.length) {
        chunks[chunks.length - 1].node.appendData(src.slice(current.length));
      }
    }
    chatSplitStreamingChunks(node, chunks);
  }

  function chatResetStreamingText(node, text) {
    node.textContent = "";
    var chunk = chatNewStreamChunk(text);
    node.__streamChunks = [chunk];
    node.appendChild(chunk.el);
    return node.__streamChunks;
  }

  function chatNewStreamChunk(text) {
    // 局部名避开顶层 el()（模块化后顶层函数走 G 表，局部同名会遮蔽）。
    var chunkEl = document.createElement("div");
    chunkEl.className = "chat-stream-chunk";
    var textNode = document.createTextNode(text || "");
    chunkEl.appendChild(textNode);
    return { el: chunkEl, node: textNode };
  }

  function chatSplitStreamingChunks(node, chunks) {
    if (!chunks || !chunks.length) { return; }
    var last = chunks[chunks.length - 1];
    while (last.node.data.length > S.CHAT_LIVE_CHUNK_CHARS) {
      var data = last.node.data;
      var cut = data.lastIndexOf("\n", S.CHAT_LIVE_CHUNK_CHARS - 1);
      if (cut >= 0) { cut += 1; }
      else { cut = S.CHAT_LIVE_CHUNK_CHARS; }
      var rest = data.slice(cut);
      last.node.deleteData(cut, rest.length);
      var next = chatNewStreamChunk(rest);
      node.insertBefore(next.el, last.el.nextSibling);
      chunks.push(next);
      last = next;
    }
  }

  // chatSetStreamingMarkdown keeps the live bubble formatted at every frame
  // without re-rendering the whole answer. Completed Markdown blocks are
  // appended once before a stable marker; only the unfinished tail is replaced.
  function chatSetStreamingMarkdown(node, text) {
    if (!node) { return; }
    var src = String(text || "").replace(/\r\n?/g, "\n");
    var mark = node.__streamMdMark;
    if (!mark || mark.parentNode !== node) {
      node.textContent = "";
      mark = document.createComment("chat-stream-md-stable-end");
      node.appendChild(mark);
      node.__streamMdMark = mark;
      node.__streamMdStable = "";
      node.__streamMdPending = "";
      node.__streamMdTail = "";
    }
    var boundary = chatMarkdownStableBoundary(src);
    var stable = src.slice(0, boundary);
    var tail = src.slice(boundary);
    var previousStable = node.__streamMdStable || "";
    if (stable.indexOf(previousStable) !== 0) {
      while (node.firstChild && node.firstChild !== mark) {
        node.removeChild(node.firstChild);
      }
      while (mark.nextSibling) { node.removeChild(mark.nextSibling); }
      previousStable = "";
      node.__streamMdStable = "";
      node.__streamMdPending = "";
      node.__streamMdTail = "";
    }
    if (stable.length > previousStable.length) {
      node.__streamMdPending = (node.__streamMdPending || "") +
        stable.slice(previousStable.length);
      node.__streamMdStable = stable;
      chatFlushMarkdownChunks(node);
    }
    // Pending stable blocks stay visible in the tail until they cross the chunk
    // threshold; that keeps the per-frame re-render bounded to one small region.
    var tailText = (node.__streamMdPending || "") + tail;
    if (tailText !== node.__streamMdTail) {
      while (mark.nextSibling) { node.removeChild(mark.nextSibling); }
      if (tailText) {
        var tailFrag = document.createElement("template");
        tailFrag.innerHTML = renderMarkdown(tailText);
        node.appendChild(tailFrag.content);
      }
      node.__streamMdTail = tailText;
    }
  }

  function chatFlushMarkdownChunks(node) {
    var pending = node.__streamMdPending || "";
    var mark = node.__streamMdMark;
    while (pending.length >= S.CHAT_LIVE_MD_CHUNK_CHARS) {
      // Stable text always contains blank-line boundaries outside fences. Cut at
      // the nearest one after the threshold so a code fence is never split.
      var cut = pending.indexOf("\n\n", S.CHAT_LIVE_MD_CHUNK_CHARS - 80);
      if (cut === -1) {
        if (pending.length < S.CHAT_LIVE_MD_CHUNK_CHARS * 2) { break; }
        cut = pending.length;
      } else {
        cut += 2;
      }
      var chunkHTML = renderMarkdown(pending.slice(0, cut));
      pending = pending.slice(cut);
      if (chunkHTML) {
        var chunk = document.createElement("div");
        chunk.className = "chat-stream-md-chunk";
        chunk.innerHTML = chunkHTML;
        node.insertBefore(chunk, mark);
      }
    }
    node.__streamMdPending = pending;
  }

  // chatMarkdownStableBoundary returns the end of the last blank line outside a
  // fenced code block. Everything before it is final Markdown and can be
  // appended once; only the suffix needs another parse while tokens arrive.
  function chatMarkdownStableBoundary(src) {
    var pos = 0;
    var boundary = 0;
    var inFence = false;
    while (pos < src.length) {
      var nl = src.indexOf("\n", pos);
      var lineEnd = nl === -1 ? src.length : nl;
      var line = src.slice(pos, lineEnd);
      if (inFence) {
        if (/^\s*(```|~~~)\s*$/.test(line)) { inFence = false; }
      } else if (/^\s*(```|~~~)/.test(line)) {
        inFence = true;
      } else if (/^\s*$/.test(line)) {
        boundary = nl === -1 ? src.length : nl + 1;
      }
      if (nl === -1) { break; }
      pos = nl + 1;
    }
    return boundary;
  }

  // ── Incremental board: keyed reconcile instead of a full rebuild ──
  // setText writes only when the value actually changed: untouched nodes keep
  // text selection and avoid needless layout invalidation on every data tick.
  function setText(node, text) {
    if (node && node.textContent !== text) { node.textContent = text; }
  }

  // reconcileChildren reorders host's direct children to exactly match
  // `desired` (an ordered element array): unwanted nodes are removed, desired
  // nodes are moved (never re-created) into place. Element identity — and with
  // it scroll position, text selection, focus and listeners — is preserved.
  function reconcileChildren(host, desired) {
    Array.prototype.slice.call(host.children).forEach(function (child) {
      if (desired.indexOf(child) === -1) { child.remove(); }
    });
    var refNode = host.firstChild;
    desired.forEach(function (child) {
      if (child === refNode) {
        refNode = refNode.nextSibling;
        return;
      }
      host.insertBefore(child, refNode);
    });
  }

  // pruneSessionRowEls forgets registry entries whose elements left the board
  // (filtered out, removed snapshot, …) so the next render starts clean.
  function pruneSessionRowEls() {
    Object.keys(S.sessionRowEls).forEach(function (key) {
      if (!S.sessionRowEls[key].isConnected) { delete S.sessionRowEls[key]; }
    });
  }

  // ensureSessionRow returns the keyed element for one item, patching it in
  // place; only new rows (or rows that switched kind) allocate DOM.
  function ensureSessionRow(item) {
    var key = "c:" + item.node.node_id + "|" + item.session.session_id;
    var kind = G.isSubagentSession(item.session) ? "child" : "card";
    var rowEl = S.sessionRowEls[key];
    if (rowEl && rowEl.__kind === kind) {
      if (kind === "card") { updateSessionCard(rowEl, item); }
      else { G.updateSubagentRow(rowEl, item); }
      return rowEl;
    }
    var created = kind === "child" ? G.createSubagentRow() : createSessionCard();
    created.setAttribute("data-rkey", key);
    created.__kind = kind;
    S.sessionRowEls[key] = created;
    if (kind === "card") { updateSessionCard(created, item); }
    else { G.updateSubagentRow(created, item); }
    return created;
  }

  function ensureSessionGroup(node) {
    var key = "g:" + node.node_id;
    var group = S.sessionRowEls[key];
    if (group && group.__kind === "group") { return group; }
    group = createSessionGroup(node);
    group.setAttribute("data-rkey", key);
    group.__kind = "group";
    S.sessionRowEls[key] = group;
    return group;
  }

  function renderSessionTable(nodes) {
    var host = S.el("session-table");
    if (!host) { return; }
    var items = G.allSessions(nodes).filter(G.sessionMatches).sort(G.sessionSorter);
    var empty = S.el("session-empty");
    if (empty) { empty.hidden = items.length > 0; }
    var listCount = S.el("im-list-count");
    if (listCount) {
      setText(listCount, String(items.length));
      listCount.hidden = items.length === 0;
    }
    // Skip redundant rebuilds (identical SSE frames): typing, focus and
    // selection stay untouched unless something visible actually changed.
    var signature = G.sessionTableSignature(items);
    if (signature === S.lastSessionTableSignature) { return; }
    S.lastSessionTableSignature = signature;
    G.captureSessionDrafts();
    // Nodes with several visible sessions collapse into a group (session list
    // grouped by device); single-session nodes stay flat so the two-column
    // board keeps its density.
    // 子代理行跟随父会话：先按可见父行重排（孤儿子行直接丢弃），节点分组只按
    // 顶层会话计数，避免"1 个父 + N 个子"把节点误判成多会话分组。
    items = G.orderSessionsWithChildren(items);
    var nodeCounts = {};
    items.forEach(function (item) {
      if (G.isSubagentSession(item.session)) { return; }
      var k = item.node.node_id;
      nodeCounts[k] = (nodeCounts[k] || 0) + 1;
    });
    var renderedNodes = {};
    var desired = [];
    items.forEach(function (item) {
      var nodeID = item.node.node_id;
      if (renderedNodes[nodeID]) { return; }
      renderedNodes[nodeID] = true;
      var nodeItems = items.filter(function (it) { return it.node.node_id === nodeID; });
      if (nodeCounts[nodeID] > 1) {
        // Grouped nodes: reconcile the rows inside the group body first, then
        // the group itself at board level — a row that flips between flat and
        // grouped moves parent without losing DOM identity.
        var group = ensureSessionGroup(item.node);
        reconcileChildren(group.querySelector(".group-body"), nodeItems.map(ensureSessionRow));
        updateSessionGroup(group, item.node, nodeItems);
        desired.push(group);
        return;
      }
      nodeItems.forEach(function (it) { desired.push(ensureSessionRow(it)); });
    });
    reconcileChildren(host, desired);
    pruneSessionRowEls();
    G.restoreSessionDrafts(host);
    G.syncImActiveCard();
    if (G.currentView() === "sessions") { setTimeout(G.autoSelectFirstSession, 0); }
    if (!S.sessionsScrollRestored) {
      S.sessionsScrollRestored = true;
      setTimeout(G.restoreSessionsScroll, 0);
    }
  }

  // createSessionGroup wraps several sessions of one node in a collapsible
  // header (device grouping): live/offline dot + name + kind/count/last seen.
  function createSessionGroup(node) {
    var group = document.createElement("section");
    group.className = "session-group";
    group.innerHTML =
      '<button type="button" class="group-head" aria-expanded="true">' +
      '<span class="dot live"></span>' +
      '<span class="group-name"></span><span class="group-meta"></span>' +
      '<span class="group-caret" aria-hidden="true">▾</span></button>' +
      '<div class="group-body"></div>';
    group.querySelector(".group-head").addEventListener("click", function () {
      var next = !S.collapsedGroups[node.node_id];
      if (next) { S.collapsedGroups[node.node_id] = true; } else { delete S.collapsedGroups[node.node_id]; }
      group.querySelector(".group-body").hidden = next;
      group.querySelector(".group-caret").textContent = next ? "▸" : "▾";
      this.setAttribute("aria-expanded", String(!next));
    });
    return group;
  }

  function updateSessionGroup(group, node, groupItems) {
    group.classList.toggle("offline", !!node.stale);
    group.querySelector(".group-head .dot").className = "dot " + (node.stale ? "offline" : "live");
    setText(group.querySelector(".group-name"), G.nodeDisplayName(node));
    var latest = 0;
    groupItems.forEach(function (item) {
      var t = new Date(item.session.updated_at || 0).getTime();
      if (t > latest) { latest = t; }
    });
    setText(group.querySelector(".group-meta"),
      (node.kind || "agent") + " · 会话 " + groupItems.length +
      (latest ? " · 最近 " + new Date(latest).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : ""));
    var collapsed = !!S.collapsedGroups[node.node_id];
    group.querySelector(".group-body").hidden = collapsed;
    group.querySelector(".group-caret").textContent = collapsed ? "▸" : "▾";
    group.querySelector(".group-head").setAttribute("aria-expanded", String(!collapsed));
  }

  // Session action menus live in the DOM. One pair of document listeners closes
  // any open menu (outside click / Escape) so cards never accumulate handlers.
  function closeSessionMenus(restoreFocus) {
    document.querySelectorAll(".s-menu").forEach(function (menu) {
      if (menu.hidden) { return; }
      var hadFocus = !!(document.activeElement && menu.contains(document.activeElement));
      menu.hidden = true;
      var btn = menu.parentElement && menu.parentElement.querySelector("[data-more]");
      if (btn) {
        btn.setAttribute("aria-expanded", "false");
        if (restoreFocus && hadFocus) { btn.focus(); }
      }
    });
  }

  // Menus are rendered fixed to the viewport, not inside the card's scroll
  // container: the list clips overflow, and a popup anchored above the action
  // row would otherwise be invisible. Flip above/below when the viewport runs
  // out of room and keep the right edge aligned with the trigger.
  function positionSessionMenu(trigger, menu) {
    var r = trigger.getBoundingClientRect();
    var m = menu.getBoundingClientRect();
    var pad = 8;
    var gap = 6;
    var left = Math.min(window.innerWidth - m.width - pad, Math.max(pad, r.right - m.width));
    var below = r.bottom + gap;
    var above = r.top - m.height - gap;
    var top = below + m.height <= window.innerHeight - pad ? below : Math.max(pad, above);
    menu.style.left = Math.round(left) + "px";
    menu.style.top = Math.round(top) + "px";
  }

  function visibleMenuItems(menu) {
    return Array.prototype.slice.call(menu.querySelectorAll('[role="menuitem"]')).filter(function (item) {
      return !item.hidden;
    });
  }

  // 任意卡片（设备 / MCP / 离线快照）共用同一套溢出菜单行为：
  // 固定定位、方向键/Home/End、Escape 关闭、焦点归还。
  function bindCardMenu(card, menu) {
    if (!card || !menu) { return; }
    var trigger = card.querySelector("[data-more]");
    if (!trigger) { return; }
    var menuID = menu.id || ("card-menu-" + (++S.sessionMenuSeq));
    menu.id = menuID;
    trigger.setAttribute("aria-controls", menuID);
    trigger.setAttribute("aria-expanded", "false");
    trigger.addEventListener("click", function (e) {
      e.stopPropagation();
      var willOpen = menu.hidden;
      closeSessionMenus(false);
      if (!willOpen) { return; }
      menu.hidden = false;
      trigger.setAttribute("aria-expanded", "true");
      positionSessionMenu(trigger, menu);
      var items = visibleMenuItems(menu);
      if (items.length) { items[0].focus(); }
    });
    menu.addEventListener("keydown", function (e) {
      var items = visibleMenuItems(menu);
      if (!items.length) { return; }
      var idx = items.indexOf(document.activeElement);
      if (e.key === "ArrowDown") {
        e.preventDefault();
        items[(idx + 1) % items.length].focus();
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        items[(idx - 1 + items.length) % items.length].focus();
      } else if (e.key === "Home") {
        e.preventDefault();
        items[0].focus();
      } else if (e.key === "End") {
        e.preventDefault();
        items[items.length - 1].focus();
      } else if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        closeSessionMenus(true);
      } else if (e.key === "Tab") {
        closeSessionMenus(true);
      }
    });
  }

  document.addEventListener("click", function (e) {
    if (e.target && e.target.closest && e.target.closest(".s-menu, [data-more]")) { return; }
    closeSessionMenus(false);
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") { closeSessionMenus(true); }
  });
  function repositionOpenSessionMenus() {
    document.querySelectorAll(".s-menu").forEach(function (menu) {
      if (menu.hidden) { return; }
      var btn = menu.parentElement && menu.parentElement.querySelector("[data-more]");
      if (btn) { positionSessionMenu(btn, menu); }
    });
  }
  window.addEventListener("resize", repositionOpenSessionMenus);
  window.addEventListener("scroll", function (e) {
    if (e.target && e.target.closest && e.target.closest(".s-menu")) { return; }
    repositionOpenSessionMenus();
  }, true);
  S.sessionListBody = document.querySelector(".im-list-body");
  if (S.sessionListBody) {
    S.sessionListBody.addEventListener("scroll", repositionOpenSessionMenus);
  }

  function syncExpandButton(card, expanded) {
    var btn = card.querySelector("[data-expand]");
    if (!btn) { return; }
    btn.setAttribute("aria-expanded", String(!!expanded));
    btn.setAttribute("aria-label", expanded ? "收起详情" : "展开详情");
    btn.title = expanded ? "收起详情" : "展开详情";
  }

  // createSessionCard builds the full card skeleton once; every conditional
  // block stays in the DOM (toggled via `hidden`) so updateSessionCard only
  // patches text/visibility and never re-creates nodes — scroll positions,
  // text selection, focus and the live tail therefore survive data updates.
  function createSessionCard() {
    var card = document.createElement("div");
    card.className = "session-card";
    card.setAttribute("role", "group");
    card.setAttribute("aria-keyshortcuts", "Enter Space c o a");
    // Put the card itself in the tab order (arrow-key nav already focuses it,
    // but Tab could not reach it); updateSessionCard sets its spoken name.
    card.setAttribute("tabindex", "0");
    // Information is layered by business attribute, top → bottom: identity
    // (title/badge/time) → live activity band (state chip + host activity
    // text + elapsed clock) → content (preview / last message) → meta line
    // (node · ID · sync) → actions → reply → live tail (pinned footer).
    card.innerHTML =
      '<div class="s-title"><span class="dot live"></span><span class="s-name"></span></div>' +
      '<span class="att-badge s-badge" hidden></span><span class="s-time"></span>' +
      '<div class="s-activity" hidden><span class="s-state" hidden></span>' +
      '<span class="s-status-text"></span><span class="s-status-elapsed" hidden></span></div>' +
      '<div class="s-preview" hidden></div>' +
      '<div class="s-last" hidden><span class="s-last-role"></span><span class="s-last-text"></span></div>' +
      '<div class="s-meta"><span class="s-node-text"></span>' +
      '<span class="s-access" hidden></span>' +
      '<span class="s-sync" hidden></span></div>' +
      '<div class="s-actions">' +
      '<button class="btn primary sm att-action" type="button" data-attend hidden></button>' +
      '<button class="btn primary sm" type="button" data-open hidden>打开会话</button>' +
      '<button class="btn ghost sm s-expand" type="button" data-expand aria-expanded="false" aria-label="展开详情" title="展开详情" hidden>' +
      '<svg aria-hidden="true" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 12 15 18 9"/></svg></button>' +
      '<button class="btn ghost sm s-more" type="button" data-more aria-haspopup="menu" aria-expanded="false" title="更多操作" aria-label="更多操作">⋯</button>' +
      '<div class="s-menu" role="menu" aria-label="会话操作" hidden>' +
      '<button class="menu-item" type="button" role="menuitem" data-detail>详情</button>' +
      '<button class="menu-item" type="button" role="menuitem" data-copy-link>复制链接</button>' +
      '<button class="menu-item" type="button" role="menuitem" data-web hidden>原版 Web</button>' +
      '<button class="menu-item" type="button" role="menuitem" data-window-open hidden title="在该节点本机的新终端窗口里打开此会话（带它当初的启动参数）">新终端</button>' +
      '<button class="menu-item" type="button" role="menuitem" data-window-resume hidden title="让该会话在节点本机重新跑起来（无窗口则新开一个）">恢复</button>' +
      '<button class="menu-item danger" type="button" role="menuitem" data-window-close hidden title="关闭节点本机上的这个会话窗口（等同在窗口里敲 /exit）">关窗口</button>' +
      '<button class="menu-item danger" type="button" role="menuitem" data-remove hidden>移除</button>' +
      '</div>' +
      '</div>' +
      '<div class="s-live" hidden title="实时输出（最后一行），点击展开完整尾流"></div>' +
      '<div class="s-tail" hidden>' +
      '<div class="s-tail-head"><span class="s-tail-title">实时输出</span><span class="s-tail-meta"></span>' +
      '<button class="btn ghost sm" type="button" data-tail-follow>跟随中</button></div>' +
      '<pre class="s-tail-body"></pre></div>';
    // One-time listeners; they read card.__item for the newest snapshot so a
    // data patch never needs to re-bind handlers.
    // 审批 / 回答：卡片上的主操作，直接打开站内面板并聚焦交互区（不发外部跳转）。
    card.querySelector("[data-attend]").addEventListener("click", function (e) {
      e.stopPropagation();
      var it = card.__item;
      if (it) { G.openSessionChat(it.node, it.session, { focusInteractive: true }); }
    });
    // 微信式：整行就是「打开这条会话」。行内按钮 / 展开区 / 可选中文本
    // 仍然各自生效，只有真正的空白处落点才打开会话流。
    card.addEventListener("click", function (e) {
      var it = card.__item;
      if (!it) { return; }
      var t = e.target;
      if (t && t.closest && t.closest("button, a, input, textarea, select, label, .s-tail-body, .s-last, .s-menu")) { return; }
      var sel = window.getSelection && window.getSelection();
      if (sel && String(sel).length) { return; }
      G.openSessionFromCard(it.node, it.session);
    });
    // 动作收纳：小屏只保留「打开会话 / ⋯」，其余动作收进弹出菜单。
    var moreBtn = card.querySelector("[data-more]");
    var menu = card.querySelector(".s-menu");
    var menuID = "s-menu-" + (++S.sessionMenuSeq);
    menu.id = menuID;
    moreBtn.setAttribute("aria-controls", menuID);
    moreBtn.addEventListener("click", function (e) {
      e.stopPropagation();
      var willOpen = menu.hidden;
      closeSessionMenus(false);
      if (willOpen) {
        menu.hidden = false;
        moreBtn.setAttribute("aria-expanded", "true");
        positionSessionMenu(moreBtn, menu);
        var items = visibleMenuItems(menu);
        if (items.length) { items[0].focus(); }
      }
    });
    menu.addEventListener("keydown", function (e) {
      var items = visibleMenuItems(menu);
      if (!items.length) { return; }
      var idx = items.indexOf(document.activeElement);
      if (e.key === "ArrowDown") {
        e.preventDefault();
        e.stopPropagation();
        items[(idx + 1 + items.length) % items.length].focus();
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        e.stopPropagation();
        items[(idx - 1 + items.length) % items.length].focus();
      } else if (e.key === "Home") {
        e.preventDefault();
        e.stopPropagation();
        items[0].focus();
      } else if (e.key === "End") {
        e.preventDefault();
        e.stopPropagation();
        items[items.length - 1].focus();
      } else if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        closeSessionMenus(true);
      } else if (e.key === "Tab") {
        closeSessionMenus(true);
      }
    });
    menu.addEventListener("focusout", function () {
      setTimeout(function () {
        if (menu.hidden) { return; }
        if (menu.contains(document.activeElement) || document.activeElement === moreBtn) { return; }
        closeSessionMenus(false);
      }, 0);
    });
    menu.addEventListener("click", function (e) {
      // 菜单项自己的处理器先执行；随后关闭菜单，避免遮住下一次操作。
      e.stopPropagation();
      closeSessionMenus(true);
    });
    // The card body was informational only before; 详情 stays explicit.
    card.querySelector("[data-detail]").addEventListener("click", function () {
      var it = card.__item;
      if (it) { G.openSessionDetail(it.node, it.session); }
    });
    // 网关会话 URL 标识：把这张卡片的规范化深链复制到剪贴板，便于贴到 IM /
    // 工单里，点击后回到控制台并定位同一会话。
    card.querySelector("[data-copy-link]").addEventListener("click", function () {
      var it = card.__item;
      if (it) { G.copyGatewaySessionLink(it.node, it.session); }
    });
    // 原版 Web 是明确的外部/旧版入口，和站内「打开会话」分开命名，避免
    // 两个都叫「打开」却走向不同页面。
    card.querySelector("[data-web]").addEventListener("click", function () {
      var it = card.__item;
      if (it) { G.openSession(it.node, it.session); }
    });
    // Primary action: open the same inline session as a card click. Legacy
    // agents without screen/events fall back to their original Web UI and use
    // the explicit 原版 Web label instead.
    var openBtn = card.querySelector("[data-open]");
    openBtn.addEventListener("click", function () {
      var it = card.__item;
      if (!it || openBtn.disabled) { return; }
      if (openBtn.getAttribute("data-open-mode") === "web") {
        G.openSession(it.node, it.session);
      } else {
        G.openSessionFromCard(it.node, it.session);
      }
    });
    // 展开/收起: the full last message and the reply form live in the
    // expanded area; the choice is remembered per session across updates.
    var expandBtn = card.querySelector("[data-expand]");
    expandBtn.addEventListener("click", function () {
      var it = card.__item;
      if (!it || expandBtn.hidden) { return; }
      var key = card.getAttribute("data-draft-key");
      var next = !S.expandedSessions[key];
      if (next) { S.expandedSessions[key] = true; } else { delete S.expandedSessions[key]; }
      card.classList.toggle("expanded", next);
      syncExpandButton(card, next);
      // Expansion is the "I'm watching this session" signal: it gates the
      // live tail on the agent (P1-b) and suppresses pushes while open.
      G.rememberPresence(it.node, it.session, { tail: next });
    });
    // Collapsed cards still surface the newest live line, so progress is
    // visible without expanding; clicking it expands the full tail.
    var liveLine = card.querySelector(".s-live");
    liveLine.addEventListener("click", function () {
      var it = card.__item;
      if (!it) { return; }
      var key = card.getAttribute("data-draft-key");
      if (!S.expandedSessions[key]) {
        S.expandedSessions[key] = true;
        card.classList.add("expanded");
        syncExpandButton(card, true);
        G.rememberPresence(it.node, it.session, { tail: true });
      }
      var body = card.querySelector(".s-tail-body");
      if (body && body.scrollIntoView) { body.scrollIntoView({ block: "nearest" }); }
    });
    // Live-output tail: the 跟随 button pins the viewport to the newest line
    // and pauses automatically when the operator scrolls up（原位补丁只更新
    // 文本、不回零 scrollTop——这正是滚动阅读不被刷新打断的原因）。
    var tailBody = card.querySelector(".s-tail-body");
    var followBtn = card.querySelector("[data-tail-follow]");
    card.__syncFollowBtn = function () {
      var key = card.getAttribute("data-draft-key");
      followBtn.textContent = S.tailFollow[key] === false ? "跳到底部" : "跟随中";
    };
    followBtn.addEventListener("click", function () {
      S.tailFollow[card.getAttribute("data-draft-key")] = true;
      card.__syncFollowBtn();
      tailBody.scrollTop = tailBody.scrollHeight;
    });
    tailBody.addEventListener("scroll", function () {
      var atBottom = tailBody.scrollHeight - tailBody.scrollTop - tailBody.clientHeight < 24;
      S.tailFollow[card.getAttribute("data-draft-key")] = atBottom;
      card.__syncFollowBtn();
    });
    // 窗口动作（新终端 / 恢复 / 关窗口）：控制台 → 网关排队 → 节点下一次心跳
    // 取走 → 在节点那台机器上执行。202 只代表「已排进队列」，提示必须如实说明
    // 投递时机——谎称窗口已打开，操作者就会对着空桌面找原因。
    var windowActions = [
      { selector: "[data-window-open]", kind: "open-terminal", done: "已排队：该节点下一个心跳会在本机新终端里打开此会话", confirm: null },
      { selector: "[data-window-resume]", kind: "resume", done: "已排队：该节点下一个心跳会恢复此会话", confirm: null },
      { selector: "[data-window-close]", kind: "close", done: "已排队：该节点下一个心跳会关闭此窗口", confirm: "关闭该会话在本机的窗口进程？\n（等同在窗口里敲 /exit：会话会正常保存。）" }
    ];
    windowActions.forEach(function (action) {
      var btn = card.querySelector(action.selector);
      btn.addEventListener("click", function (e) {
        e.stopPropagation();
        var it = card.__item;
        if (!it || btn.disabled) { return; }
        function run() {
          return requestWindowAction(it.node, it.session, action.kind)
            .then(function () { G.notify(action.done, "ok"); });
        }
        if (action.confirm) {
          closeSessionMenus(false);
          G.confirmDestructive({
            title: "关闭窗口进程？",
            message: action.confirm,
            trigger: moreBtn,
            confirmText: "关闭窗口",
            onConfirm: run
          });
          return;
        }
        run().catch(function (err) { G.notify("窗口动作失败：" + err, "error"); });
      });
    });
    var rmBtn = card.querySelector("[data-remove]");
    rmBtn.addEventListener("click", function (e) {
      e.stopPropagation();
      var it = card.__item;
      if (!it) { return; }
      closeSessionMenus(false);
      G.confirmDestructive({
        title: "移除会话条目？",
        message: "从注册表移除该会话条目？\n（仅清除网关侧快照，不影响 agent 上的会话数据。）",
        trigger: moreBtn,
        confirmText: "移除",
        onConfirm: function () {
          return G.api("/api/nodes/" + encodeURIComponent(it.node.node_id) +
              "/sessions/" + encodeURIComponent(it.session.session_id), { method: "DELETE" })
            .then(function () { G.notify("会话条目已移除", "ok"); G.loadNodes(); });
        }
      });
    });
    return card;
  }

  // requestWindowAction 把一条窗口动作排给节点，并把「排队成功」与「执行成功」
  // 分开表达（网关只保证投递，执行结果要等节点自己的下一次上报才可见）。
  function requestWindowAction(node, session, kind) {
    var path = "/api/nodes/" + encodeURIComponent(node.node_id) +
      "/sessions/" + encodeURIComponent(session.session_id) +
      "/window?action=" + encodeURIComponent(kind);
    return G.api(path, { method: "POST" });
  }
  // updateSessionCard patches one card to match the latest snapshot: text is
  // written only when it changed, visibility flips via `hidden`, and the live
  // tail keeps its scrollTop unless 跟随中 pins it to the bottom — so an
  // operator reading a scrolled-up detail is never thrown back by an update.
  function updateSessionCard(card, item) {
    var s = item.session, node = item.node;
    var cardKey = node.node_id + "|" + s.session_id;
    var canOpen = G.nodeHasWebUI(node);
    card.__item = item;
    card.setAttribute("data-draft-key", cardKey);
    // URL 导航标识：卡片 DOM 上一直带着这张卡片的规范深链，事件条/外部脚本
    // 都能据此生成可分享的网关会话 URL。
    card.setAttribute("data-session-url", G.gatewaySessionLink(node.node_id, s.session_id));
    var expanded = !!S.expandedSessions[cardKey];
    card.classList.toggle("offline", !!node.stale);
    card.classList.toggle("expanded", expanded);
    var attClass = s.attention ? "att-" + s.attention : "";
    if (card.__attClass !== attClass) {
      if (card.__attClass) { card.classList.remove(card.__attClass); }
      if (attClass) { card.classList.add(attClass); }
      card.__attClass = attClass;
    }
    // Pulse when this session visibly moved since the previous render.
    var fingerprint = G.sessionFingerprint(s);
    if (S.sessionLastSeen[cardKey] !== undefined && S.sessionLastSeen[cardKey] !== fingerprint) {
      card.classList.remove("card-updated");
      void card.offsetWidth; // restart the pulse animation
      card.classList.add("card-updated");
    }
    S.sessionLastSeen[cardKey] = fingerprint;
    card.querySelector(".s-title .dot").className = "dot " + (node.stale ? "offline" : "live");
    card.classList.toggle("is-active",
      !!S.chatState && S.chatState.node && S.chatState.session &&
      S.chatState.node.node_id === node.node_id &&
      S.chatState.session.session_id === s.session_id);
    var label = G.sessionLabelOf(s);
    setText(card.querySelector(".s-name"), label);
    // 头像位：微信列表行的首字母/首字方块（CSS ::before 读取 data-avatar）。
    var avatar = Array.from((label || "").trim())[0] || "·";
    if (card.getAttribute("data-avatar") !== avatar) { card.setAttribute("data-avatar", avatar); }
    card.setAttribute("aria-label",
      G.sessionLabelOf(s) + "，" + G.nodeDisplayName(node) + "，" + G.stateLabelOf(s) +
      (s.attention ? "，" + G.attentionLabel(s.attention) : "") +
      (canOpen ? "，按 Enter 打开会话" : "，按 Enter 查看详情"));
    setText(card.querySelector(".s-node-text"),
      G.nodeDisplayName(node) + " · " + (node.kind || "agent"));
    var accessEl = card.querySelector(".s-access");
    var accessText = node.stale || canOpen ? "" :
      (node.kind === "runtime-server" ? "服务未就绪" : "网页未启用");
    accessEl.hidden = !accessText;
    accessEl.textContent = accessText;
    if (accessText) {
      accessEl.title = G.webUnavailableText(node);
    } else {
      accessEl.removeAttribute("title");
    }
    // 列表不再显示 node-id / session-id（id 收进「详情」）；注意力为审批/提问
    // 且节点支持站内面板时，把主操作提升为「审批 / 回答」。
    var attendBtn = card.querySelector("[data-attend]");
    var attendKind = (s.attention === "approval" || s.attention === "question") &&
      G.chatPanelSupported(node, s) && G.chatCanInput(node) ? s.attention : "";
    if (attendKind) {
      attendBtn.hidden = false;
      attendBtn.textContent = attendKind === "approval" ? "审批" : "回答";
      attendBtn.title = attendKind === "approval"
        ? "在本面板内批准或拒绝该工具调用"
        : "在本面板内回答该提问";
      attendBtn.setAttribute("data-attention", attendKind);
    } else {
      attendBtn.hidden = true;
      attendBtn.removeAttribute("data-attention");
    }
    var syncEl = card.querySelector(".s-sync");
    var syncedMs = s.synced_at ? new Date(s.synced_at).getTime() : NaN;
    if (!isNaN(syncedMs)) {
      G.setRelTime(syncEl, syncedMs, "同步 ", 60000,
        "网关最后收到该会话更新：" + G.absoluteTime(syncedMs) + "（服务器时间）");
      syncEl.hidden = false;
    } else {
      syncEl.hidden = true;
    }
    // Live activity band (动态状态): state chip (运行中/思考中/回答中/等待中/
    // 审查中/已中断) + the host's one-line activity text + a locally-advanced
    // clock, kept apart from the metadata line so "what is happening now"
    // reads as the card's most time-sensitive zone.
    var stateChip = card.querySelector(".s-state");
    var stateLabel = S.STATE_LABELS[s.state];
    var chipShown = false;
    if (stateLabel && (G.isActiveSessionState(s.state) || s.state === "interrupted")) {
      stateChip.textContent = stateLabel;
      stateChip.className = "s-state st-" + s.state;
      stateChip.hidden = false;
      chipShown = true;
    } else {
      stateChip.hidden = true;
    }
    var statusText = (s.status_text || "").trim();
    var startedMs = Date.parse(s.status_started_at || "");
    var showElapsed = G.isActiveSessionState(s.state) && !isNaN(startedMs);
    var activityLine = card.querySelector(".s-activity");
    var statusTextEl = card.querySelector(".s-status-text");
    setText(statusTextEl, statusText);
    if (statusText) { statusTextEl.title = statusText; }
    var elapsedEl = card.querySelector(".s-status-elapsed");
    if (chipShown || statusText || showElapsed) {
      if (showElapsed) {
        elapsedEl.setAttribute("data-started", s.status_started_at);
        setText(elapsedEl, "· " + G.formatStatusElapsed(Date.now() - startedMs));
        elapsedEl.hidden = false;
      } else {
        elapsedEl.hidden = true;
        elapsedEl.removeAttribute("data-started");
      }
      activityLine.hidden = false;
    } else {
      activityLine.hidden = true;
      elapsedEl.hidden = true;
      elapsedEl.removeAttribute("data-started");
    }
    if (s.attention) {
      var b = card.querySelector(".s-badge");
      b.textContent = G.attentionLabel(s.attention);
      b.className = "att-badge s-badge att-" + s.attention;
      b.hidden = false;
    } else {
      card.querySelector(".s-badge").hidden = true;
    }
    var t = card.querySelector(".s-time");
    var updatedMs = s.updated_at ? new Date(s.updated_at).getTime() : NaN;
    if (!isNaN(updatedMs)) {
      G.setRelTime(t, updatedMs, "", 0, "内容时间：" + G.absoluteTime(updatedMs));
      t.hidden = false;
    } else { t.hidden = true; }
    var previewEl = card.querySelector(".s-preview");
    if (s.preview) { setText(previewEl, s.preview); }
    // Full last message (multi-line, unlike the one-line preview). Falls back
    // to the preview when the agent build predates the field.
    var lastBlock = card.querySelector(".s-last");
    var lastText = s.last_message || "";
    if (lastText) {
      var role = s.last_message_role || "";
      var roleEl = card.querySelector(".s-last-role");
      roleEl.className = "s-last-role" + (role ? " role-" + role : "");
      setText(roleEl, G.roleLabelOf(role));
      setMessageHTML(card.querySelector(".s-last-text"), lastText);
      lastBlock.hidden = false;
    } else {
      lastBlock.hidden = true;
    }
    // Live-output tail (bounded by the shared core): rendered only while
    // the global "实时输出" switch is on; the 跟随 button pins the viewport
    // to the newest line and pauses automatically when the user scrolls up.
    var tailText = S.tailEnabled ? (s.status_tail || "") : "";
    var tailBlock = card.querySelector(".s-tail");
    var liveLine = card.querySelector(".s-live");
    if (tailText) {
      var lastLine = (tailText.replace(/\s+$/, "").split("\n").pop() || "").replace(/\s+/g, " ").trim();
      if (lastLine.length > 140) { lastLine = "…" + lastLine.slice(lastLine.length - 140); }
      setText(liveLine, lastLine || "…");
      liveLine.hidden = false;
      tailBlock.hidden = false;
      var tailBody = card.querySelector(".s-tail-body");
      setMessageHTML(tailBody, tailText);
      setText(card.querySelector(".s-tail-meta"), tailText.length + " 字符");
      card.__syncFollowBtn();
      // Follow mode pins the viewport to the newest line. When the operator
      // scrolled up (tailFollow false) scrollTop is left untouched — that is
      // what keeps a scrolled-up detail readable while the session streams.
      if (S.tailFollow[cardKey] !== false) { tailBody.scrollTop = tailBody.scrollHeight; }
    } else {
      liveLine.hidden = true;
      tailBlock.hidden = true;
    }
    // preview 与实时行互斥：有流式输出时让尾部文本优先（与旧卡片一致）。
    previewEl.hidden = !s.preview || !!tailText;
    // 展开 button: only meaningful when there is more content than the
    // collapsed card shows; the full last message and the reply form live in
    // the expanded area.
    var wantReply = !node.stale && canOpen &&
      (node.capabilities || []).indexOf("input") !== -1;
    var hasExtras = !!(s.last_message || "") || wantReply || !!tailText;
    var expandBtn = card.querySelector("[data-expand]");
    expandBtn.hidden = !hasExtras;
    card.classList.toggle("expanded", expanded && hasExtras);
    syncExpandButton(card, expanded && hasExtras);
    // Primary action: inline chat when the node advertises the panel contract;
    // legacy Web-only nodes keep a clearly labelled 原版 Web fallback. Offline
    // or Web-less nodes get no dead disabled button: their card click opens
    // the detail dialog instead.
    var openBtn = card.querySelector("[data-open]");
    var webItem = card.querySelector("[data-web]");
    var canInline = G.chatPanelSupported(node, s);
    var openMode = "";
    if (attendKind) {
      openBtn.hidden = true;
      openBtn.disabled = false;
      openBtn.removeAttribute("data-open-mode");
    } else if (canOpen) {
      openMode = canInline ? "inline" : "web";
      openBtn.hidden = false;
      openBtn.disabled = false;
      openBtn.textContent = openMode === "inline" ? "打开会话" : "原版 Web";
      openBtn.title = openMode === "inline" ? "在站内打开该会话" : "在浏览器打开原版 Web UI";
      openBtn.setAttribute("data-open-mode", openMode);
    } else {
      openBtn.hidden = true;
      openBtn.disabled = true;
      openBtn.removeAttribute("data-open-mode");
      openBtn.title = "";
    }
    webItem.hidden = !canOpen || openMode === "web";
    card.querySelector("[data-remove]").hidden = !node.stale;
    // 窗口动作按钮：只在该节点自己通告了 window-actions（本机有可用终端）时出现。
    // 子代理行不独立拥有窗口——它是父进程里的一个任务，所以恒不显示。
    var canWindow = G.windowActionsSupported(node) && !G.isSubagentSession(s);
    card.querySelector("[data-window-open]").hidden = !canWindow;
    card.querySelector("[data-window-resume]").hidden = !canWindow;
    card.querySelector("[data-window-close]").hidden = !canWindow;
    // 「新终端」的落点与命令要能被看到：这两个字段是 agent 上报的本机事实
    // （工作目录错了或命令不对，操作者在按钮按下前就能发现）。
    var windowTitle = canWindow
      ? "窗口将在该节点本机执行" + (s.workspace ? "\n工作目录：" + s.workspace : "") +
          (s.launch_command ? "\n启动命令：" + s.launch_command : "") +
          (s.mesh_window ? "\n当前窗口：" + s.mesh_window : "")
      : "该节点未提供本机窗口能力";
    card.querySelector("[data-window-open]").title = windowTitle;
    // Inline reply: POST through the gateway reverse proxy to the agent's
    // own /web/api/input (the agent injects its local write token and the
    // route allowlist already covers /web). Gated on the agent advertising
    // the "input" capability so stale agents just hide the form.
    var replyForm = card.querySelector(".s-reply");
    if (wantReply && !replyForm) {
      card.insertBefore(G.buildReplyForm(node, s), card.querySelector(".s-live"));
    } else if (!wantReply && replyForm) {
      replyForm.remove();
    }
    var openMenu = card.querySelector(".s-menu");
    if (openMenu && !openMenu.hidden) {
      var menuItems = visibleMenuItems(openMenu);
      if (openMenu.contains(document.activeElement) && menuItems.indexOf(document.activeElement) === -1 && menuItems.length) {
        menuItems[0].focus();
      }
      var menuTrigger = card.querySelector("[data-more]");
      if (menuTrigger) { positionSessionMenu(menuTrigger, openMenu); }
    }
  }
