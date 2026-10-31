  /* ─────────────────────── rendering: connectors ─────────────── */

  var connectorsLoaded = false;

  // 连接器列表不是单一数据源：网关自身 store 是主数据，在线节点上的
  // aicli/runtime-server 连接器经 /n/{node}/ 反代读取。页面把两边合并展示；
  // 同一 id 出现在多个共享 config.yaml 的节点时只保留正在运行该连接器的一份。
  var connectorEntries = [];
  var connectorGatewayEntries = [];
  var connectorGatewayMessage = "";
  var connectorGatewaySettled = false;
  var connectorMessage = "";
  var connectorLoadSeq = 0;
  var connectorNodeFetchSeq = 0;
  var connectorLoadedAt = 0;
  var connectorNodeSignature = "";
  var connectorNodeReadTimeoutMs = 4000;
  var connectorCacheTTLMs = 10000;

  // connectorSchemaProviders 是三端共用的 GET /api/connectors/schema 缓存
  // （providers 数组）；连接器卡片据此渲染通用动作按钮（kind=button，
  // 如 QQ「授权校验」）。拉取失败/未接线时静默降级（卡片仍可用）。
  var connectorSchemaProviders = null;

  function loadConnectorSchema() {
    if (connectorSchemaProviders) { return Promise.resolve(connectorSchemaProviders); }
    return apiJSON("/api/connectors/schema").then(function (r) {
      if (r.res.ok && r.body && Array.isArray(r.body.providers)) {
        connectorSchemaProviders = r.body.providers;
      }
      return connectorSchemaProviders;
    }).catch(function () { return null; });
  }

  function connectorSchemaFor(provider) {
    if (!connectorSchemaProviders) { return null; }
    for (var i = 0; i < connectorSchemaProviders.length; i++) {
      if (connectorSchemaProviders[i].provider === provider) { return connectorSchemaProviders[i]; }
    }
    return null;
  }

  // connectorNodeBasePath 返回该在线节点连接器视图的网关只读入口。
  // 网关用固定路径 /api/connectors 代读到节点本地，因此不要求节点把
  // /api/connectors 加进通用 /n/ 反代白名单；节点的本地 token 由隧道注入。
  function connectorNodeBasePath(node) {
    if (!node || node.stale || !node.node_id) { return ""; }
    var kind = String(node.kind || "").toLowerCase();
    if (kind !== "aicli" && kind !== "runtime-server") { return ""; }
    var routes = Array.isArray(node.routes) ? node.routes : [];
    if (!routes.length) { return ""; }
    return "/api/nodes/" + encodeURIComponent(node.node_id) + "/connectors";
  }

  function connectorBasePath(entry) {
    if (entry.source === "gateway") { return "/api/connectors"; }
    return connectorNodeBasePath(entry.node);
  }

  function connectorSourceLabel(entry) {
    if (entry.source === "gateway") { return "网关"; }
    return nodeDisplayName(entry.node) || entry.node.node_id;
  }

  function connectorEntryRank(entry) {
    if (entry && entry.connector && entry.connector.owned_by_current_host) { return 2; }
    if (entry && entry.source === "gateway") { return 1; }
    return 0;
  }

  // mergeConnectorEntries 按 id 去重：同一份共享 config.yaml 会被多个节点
  // 同时返回；优先保留“本宿主正在运行”的那份，并把实际来源合并展示。
  function mergeConnectorEntries(groups) {
    var byID = {};
    var order = [];
    function add(entry) {
      if (!entry || !entry.connector) { return; }
      var id = String(entry.connector.id || "").trim();
      if (!id) { return; }
      var existing = byID[id];
      if (!existing) {
        byID[id] = entry;
        order.push(id);
        return;
      }
      if (!existing.sources) { existing.sources = [connectorSourceLabel(existing)]; }
      var label = connectorSourceLabel(entry);
      if (existing.sources.indexOf(label) === -1) { existing.sources.push(label); }
      if (connectorEntryRank(entry) > connectorEntryRank(existing)) {
        var primary = connectorSourceLabel(entry);
        entry.sources = [primary].concat(existing.sources.filter(function (label) { return label !== primary; }));
        byID[id] = entry;
      }
    }
    (groups || []).forEach(function (group) {
      (group || []).forEach(add);
    });
    return order.map(function (id) { return byID[id]; });
  }

  function connectorNodesSignature(nodes) {
    return (nodes || []).filter(function (node) { return !node.stale && node.node_id; }).map(function (node) {
      return node.node_id + ":" + (Array.isArray(node.routes) ? node.routes.join(",") : "");
    }).sort().join("|");
  }

  function connectorNodeFetch(node) {
    var base = connectorNodeBasePath(node);
    if (!base) { return Promise.resolve({ node: node, items: [] }); }
    var controller = null;
    var timer = null;
    var opts = {};
    if (typeof window.AbortController === "function") {
      controller = new window.AbortController();
      opts.signal = controller.signal;
      timer = setTimeout(function () { controller.abort(); }, connectorNodeReadTimeoutMs);
    }
    return apiJSON(base, opts).then(function (r) {
      if (timer) { clearTimeout(timer); }
      if (!r.res.ok || !r.body || !Array.isArray(r.body.items)) {
        var reason = r.body && (r.body.reason || r.body.error);
        return {
          node: node,
          items: [],
          error: "HTTP " + r.res.status + (reason ? " " + reason : ""),
        };
      }
      return { node: node, items: r.body.items };
    }).catch(function (err) {
      if (timer) { clearTimeout(timer); }
      return {
        node: node,
        items: [],
        error: err && err.name === "AbortError" ? "读取超时" : (err && err.message ? err.message : String(err)),
      };
    });
  }

  function connectorNodeHint(results) {
    var live = (lastNodes || []).filter(function (node) { return !node.stale; });
    if (!live.length) {
      return "网关配置为空，当前也没有在线节点可读取本地连接器。";
    }
    if (!results || !results.length) {
      return "网关配置为空；未找到可读取的在线节点连接器（节点可能未开启 Web）。";
    }
    var returned = 0;
    var failed = 0;
    results.forEach(function (res) {
      if (res && res.error) { failed++; } else { returned++; }
    });
    if (returned === 0) {
      return "网关配置为空；已尝试读取 " + results.length + " 个在线节点，但没有节点返回连接器视图（可能未开启 Web 或连接器未装配）。";
    }
    return "网关与在线节点都没有已配置的连接器。" + (failed ? "（另有 " + failed + " 个节点读取失败）" : "");
  }

  function connectorInitialMessage() {
    if (connectorGatewayMessage) { return connectorGatewayMessage; }
    if (!connectorGatewayEntries.length && (lastNodes || []).some(function (node) { return !node.stale; })) {
      return "正在读取在线节点的连接器…";
    }
    return "";
  }

  function renderConnectorLoading() {
    var list = el("connectors-list");
    if (!list) { return; }
    var countEl = el("connectors-count");
    if (countEl) { countEl.hidden = true; }
    list.innerHTML = "";
    var loadingEl = document.createElement("p");
    loadingEl.className = "empty";
    loadingEl.textContent = "正在读取连接器…";
    list.appendChild(loadingEl);
  }

  // refreshConnectorNodes 只读在线节点声明的 /api/connectors；节点结果按
  // 签名去重（节点集合/路由不变不重复请求），与网关结果合并后重绘。
  function refreshConnectorNodes(force) {
    var signature = connectorNodesSignature(lastNodes);
    if (!force && signature === connectorNodeSignature) { return Promise.resolve(); }
    connectorNodeSignature = signature;
    var seq = connectorLoadSeq;
    var nodeSeq = ++connectorNodeFetchSeq;
    var targets = (lastNodes || []).filter(function (node) { return connectorNodeBasePath(node); });
    if (!targets.length) {
      connectorEntries = mergeConnectorEntries([connectorGatewayEntries, []]);
      connectorMessage = connectorGatewayMessage || (connectorEntries.length ? "" : connectorNodeHint(null));
      renderConnectors(connectorEntries, connectorMessage);
      loadConnectorSchema().then(function () {
        if (seq === connectorLoadSeq && currentView() === "connectors") {
          renderConnectors(connectorEntries, connectorMessage);
        }
      });
      return Promise.resolve();
    }
    if (!connectorGatewayEntries.length) {
      connectorMessage = connectorGatewayMessage || "正在读取在线节点的连接器…";
      renderConnectors(connectorEntries, connectorMessage);
    }
    return Promise.all(targets.map(connectorNodeFetch)).then(function (results) {
      if (seq !== connectorLoadSeq || nodeSeq !== connectorNodeFetchSeq) { return; }
      var nodeEntries = [];
      results.forEach(function (res) {
        if (!res || !Array.isArray(res.items)) { return; }
        res.items.forEach(function (item) {
          nodeEntries.push({ connector: item, source: "node", node: res.node });
        });
      });
      connectorEntries = mergeConnectorEntries([connectorGatewayEntries, nodeEntries]);
      connectorMessage = connectorGatewayMessage || (connectorEntries.length ? "" : connectorNodeHint(results));
      renderConnectors(connectorEntries, connectorMessage);
      loadConnectorSchema().then(function () {
        if (seq === connectorLoadSeq && currentView() === "connectors") {
          renderConnectors(connectorEntries, connectorMessage);
        }
      });
    });
  }

  // loadConnectors 读取网关自身 GET /api/connectors（owner 会话鉴权），再聚合
  // 在线节点的同一契约。force=true 时跳过缓存；网关未接线时仍尝试节点视图，
  // 但把网关错误保留在页面提示中。
  function loadConnectors(force) {
    if (connectorsLoaded && !force) {
      if (Date.now() - connectorLoadedAt < connectorCacheTTLMs) { return Promise.resolve(); }
      force = true;
    }
    var seq = ++connectorLoadSeq;
    connectorNodeFetchSeq++;
    connectorsLoaded = false;
    connectorGatewaySettled = false;
    connectorNodeSignature = "";
    if (!connectorEntries.length) { renderConnectorLoading(); }
    return apiJSON("/api/connectors").then(function (r) {
      if (seq !== connectorLoadSeq) { return; }
      if (r.res.status === 401) { setAuthed(false); return; }
      connectorGatewayEntries = [];
      connectorGatewayMessage = "";
      if (r.res.status === 403 || r.res.status === 503) {
        connectorGatewayMessage = (r.body && r.body.error) || ("连接器管理面不可用（HTTP " + r.res.status + "）");
      } else if (!r.res.ok || !r.body || !Array.isArray(r.body.items)) {
        connectorGatewayMessage = "连接器管理面未接线（gateway 启动日志中查找 connectors disabled）";
      } else {
        connectorGatewayEntries = r.body.items.map(function (item) {
          return { connector: item, source: "gateway" };
        });
        connectorsLoaded = true;
        connectorLoadedAt = Date.now();
      }
      connectorGatewaySettled = true;
      connectorEntries = connectorGatewayEntries;
      connectorMessage = connectorInitialMessage();
      renderConnectors(connectorEntries, connectorMessage);
      loadConnectorBindings();
      return refreshConnectorNodes(true);
    }).catch(function (err) {
      if (seq !== connectorLoadSeq) { return; }
      connectorsLoaded = false;
      connectorGatewaySettled = false;
      connectorEntries = [];
      connectorGatewayEntries = [];
      connectorGatewayMessage = "";
      connectorMessage = "读取连接器失败：" + (err && err.message ? err.message : err);
      renderConnectors(null, connectorMessage);
    });
  }

  function renderConnectors(entries, messageText) {
    var list = el("connectors-list");
    if (!list) { return; }
    var countEl = el("connectors-count");
    list.innerHTML = "";
    entries = entries || [];
    if (!entries.length) {
      if (countEl) { countEl.hidden = true; }
      var emptyEl = document.createElement("p");
      emptyEl.className = "empty";
      emptyEl.textContent = messageText || "还没有连接器 —— 网关与在线节点都没有已配置的通道。";
      list.appendChild(emptyEl);
      return;
    }
    if (countEl) {
      countEl.textContent = entries.length + " 个";
      countEl.hidden = false;
    }
    syncConnectorIDOptions();
    if (messageText) {
      var noticeEl = document.createElement("p");
      noticeEl.className = "connectors-hint";
      noticeEl.textContent = messageText;
      list.appendChild(noticeEl);
    }
    entries.forEach(function (entry) {
      var item = entry.connector;
      var isGateway = entry.source === "gateway";
      var migratedEntry = isGateway ? null : connectorMigratedEntry(entry);
      var busy = connectorIsBusy(item.id);
      var card = document.createElement("div");
      card.className = "card" + (item.enabled ? "" : " offline");
      card.innerHTML =
        '<div class="card-head"><span class="card-title"></span><span class="status-chip ' + (item.enabled ? "chip-active" : "chip-revoked") + '"></span></div>' +
        '<div class="card-meta"></div>' +
        '<div class="card-note"></div>' +
        '<div class="card-actions"></div>';
      card.querySelector(".card-title").textContent = (item.name || item.id) + "（" + item.provider + "）";
      card.querySelector(".status-chip").textContent = item.enabled ? "已启用" : "已停用";
      var owner = item.runtime_mode === "outbound"
        ? "只出站副本（不轮询）"
        : (item.active_owner
          ? "运行宿主 " + item.active_owner + (item.owned_by_current_host ? (isGateway ? "（本网关）" : "（此节点）") : "")
          : "未认领运行（网关重启或宿主离线）");
      var sources = entry.sources && entry.sources.length ? entry.sources : [connectorSourceLabel(entry)];
      var meta = card.querySelector(".card-meta");
      meta.textContent = "来源 " + sources.join(" / ") + " · id " + item.id +
        (item.mode ? " · " + item.mode : "") + " · " + owner +
        (item.runtime_mode ? " · " + item.runtime_mode : "") +
        (item.origin ? " · 复制自 " + item.origin.node_id + "/" + item.origin.connector_id : "") +
        (item.secret_keys && item.secret_keys.length ? " · 密钥字段 " + item.secret_keys.join("/") : "");
      var note = card.querySelector(".card-note");
      if (isGateway) {
        var bindings = connectorBindingRows(item.id);
        if (bindings.length) {
          var bindLine = document.createElement("p");
          bindLine.className = "connectors-hint";
          bindLine.textContent = "绑定：" + bindings.map(connectorBindingLabel).join("；");
          note.appendChild(bindLine);
        }
        if (item.origin) {
          var copied = document.createElement("p");
          copied.className = "connectors-hint";
          var targets = notifyIMTargets().filter(function (t) {
            return t && t.connector_id === item.id && t.enabled !== false;
          });
          copied.textContent = "已从节点 " + item.origin.node_id + "/" + item.origin.connector_id +
            " 复制；节点本地通知已关闭；通知目标 " + targets.length + " 个。" +
            (targets.length ? "" : " 尚未配置 Bot 目标，通知不会发送。");
          note.appendChild(copied);
        }
      } else {
        var ro = document.createElement("p");
        ro.className = "connectors-hint";
        ro.textContent = migratedEntry
          ? "已迁移到网关副本 " + migratedEntry.connector.id + "；节点继续负责入站/回复，本地通知已关闭。"
          : "来自在线节点 " + (entry.node && (nodeDisplayName(entry.node) || entry.node.node_id) || "") + "，只读；可迁移通知到网关。";
        note.appendChild(ro);
      }
      var actionsHost = card.querySelector(".card-actions");
      if (isGateway) {
        var schema = connectorSchemaFor(item.provider);
        var providerActions = (schema && schema.actions) || [];
        providerActions.forEach(function (action) {
          if (!action || !action.key) { return; }
          var actionBtn = connectorActionBtn(action.label || action.key, busy || !item.enabled, function (b) {
            runConnectorActionButton(entry, action, b);
          });
          actionsHost.appendChild(actionBtn);
        });
        if (item.enabled && item.active_owner && !item.owned_by_current_host) {
          actionsHost.appendChild(connectorActionBtn("接管运行", busy, function (b) {
            connectorTakeover(item, b);
          }));
        }
        if (item.origin) {
          actionsHost.appendChild(connectorActionBtn("管理通知目标", busy, function () {
            manageNotifyTargets(item.id);
          }));
          var originNodeEntry = connectorNodeEntryForGateway(item);
          if (originNodeEntry) {
            actionsHost.appendChild(connectorActionBtn("重新同步", busy, function (b) {
              connectorMigrate(originNodeEntry, b, true);
            }));
          }
        }
        actionsHost.appendChild(connectorActionBtn("日志", busy, function () { openConnectorLogs(item); }));
        actionsHost.appendChild(connectorActionBtn(item.enabled ? "停用" : "启用", busy, function () {
          connectorToggle(item);
        }));
        if (!item.origin) {
          actionsHost.appendChild(connectorActionBtn("编辑", busy, function () { openConnectorEditor(item); }));
        }
        actionsHost.appendChild(connectorActionBtn("测试", busy || !item.enabled, function () { openConnectorTest(item); }));
        if (!item.origin) {
          actionsHost.appendChild(connectorActionBtn("重载", busy, function () { connectorReload(item); }));
        }
        actionsHost.appendChild(connectorActionBtn("删除", busy, function (b) { connectorDelete(item, b); }, "danger-outline"));
      } else {
        if (migratedEntry) {
          actionsHost.appendChild(connectorActionBtn("管理通知目标", busy, function () {
            manageNotifyTargets(migratedEntry.connector.id);
          }));
          actionsHost.appendChild(connectorActionBtn("重新同步", busy, function (b) {
            connectorMigrate(entry, b, true);
          }));
        } else if (item.enabled && (item.provider === "qqbot" || item.provider === "wechat") &&
                   entry.node && (entry.node.capabilities || []).indexOf("connector-handoff") >= 0) {
          actionsHost.appendChild(connectorActionBtn("迁移通知到网关", busy, function (b) {
            connectorMigrate(entry, b, false);
          }));
        } else if (item.enabled && (item.provider === "qqbot" || item.provider === "wechat")) {
          var oldBtn = connectorActionBtn("节点版本过旧", true, function () {});
          oldBtn.title = "该节点未提供 connector-handoff；升级并重启节点后可迁移";
          actionsHost.appendChild(oldBtn);
        }
      }
      var detail = document.createElement("a");
      detail.className = "btn ghost sm";
      detail.href = connectorBasePath(entry) + "/" + encodeURIComponent(item.id);
      detail.target = "_blank";
      detail.rel = "noopener";
      detail.textContent = "配置 JSON";
      actionsHost.appendChild(detail);
      list.appendChild(card);
    });
  }

  // runConnectorActionButton 执行一次通道动作（POST /api/connectors/{id}/actions/{action}，
  // 空体=无参数）；节点来源走 /n/{node}/api/connectors 同一契约。执行期间禁用按钮防连点，
  // 失败给出可读错误。
  function runConnectorActionButton(entry, action, btn) {
    var item = entry.connector;
    if (action && action.kind === "qr") {
      openConnectorQRDialog(item, action, { draft: false });
      return;
    }
    var label = action.label || action.key;
    var path = connectorBasePath(entry) + "/" + encodeURIComponent(item.id) +
      "/actions/" + encodeURIComponent(action.key);
    btn.disabled = true;
    apiJSON(path, { method: "POST" }).then(function (r) {
      btn.disabled = false;
      if (!r.res.ok) {
        notify(label + "失败：" + ((r.body && r.body.error) || ("HTTP " + r.res.status)), "error");
        return;
      }
      var summary = connectorActionSummary(r.body);
      notify("动作完成：" + (summary || label), "ok");
      openConnectorActionResult(item, action, r.body);
    }).catch(function (err) {
      btn.disabled = false;
      notify(label + "失败：" + (err && err.message ? err.message : err), "error");
    });
  }

  // connectorActionSummary 压一行摘要：message 优先（QQ 授权校验），
  // 回退 status（微信 login_status），再回退整个载荷 JSON。
  function connectorActionSummary(body) {
    if (!body) { return ""; }
    if (typeof body.message === "string" && body.message.trim()) { return body.message.trim(); }
    if (typeof body.status === "string" && body.status.trim()) { return body.status.trim(); }
    var keys = Object.keys(body);
    if (!keys.length) { return ""; }
    try { return JSON.stringify(body); } catch (err) { return ""; }
  }

