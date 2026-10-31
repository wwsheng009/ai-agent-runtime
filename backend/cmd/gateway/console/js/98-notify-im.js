// 98-notify-im.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=95";

Object.assign(G, {
  installHintDismissed: installHintDismissed,
  renderInstallHint: renderInstallHint,
  ensureNotifyDraft: ensureNotifyDraft,
  notifyIMRequest: notifyIMRequest,
  notifyIMShowError: notifyIMShowError,
  notifyIMEmptyConfig: notifyIMEmptyConfig,
  notifyIMTargets: notifyIMTargets,
  notifyIMScopeValuesField: notifyIMScopeValuesField,
  populateNotifyIMNodes: populateNotifyIMNodes,
  notifyKindLabel: notifyKindLabel,
  notifyIMCollectKinds: notifyIMCollectKinds,
  notifyIMSyncKindsHint: notifyIMSyncKindsHint,
  notifyIMClearKinds: notifyIMClearKinds,
  renderNotifyIMKinds: renderNotifyIMKinds,
  notifyIMSourceField: notifyIMSourceField,
  renderNotifyIMTargets: renderNotifyIMTargets,
  loadNotifyIM: loadNotifyIM,
  setNotifyIMKindsSelection: setNotifyIMKindsSelection,
  notifyIMEditorReset: notifyIMEditorReset,
  closeNotifyIMEditor: closeNotifyIMEditor,
  openNotifyIMEditor: openNotifyIMEditor,
  buildNotifyIMTargetFromEditor: buildNotifyIMTargetFromEditor,
  applyNotifyIMEditor: applyNotifyIMEditor,
  saveNotifyIM: saveNotifyIM,
});

  /* ─────────────────────── PWA install hint ─────────────────── */

  S.deferredInstall = null;

  window.addEventListener("beforeinstallprompt", function (event) {
    // Android/Chrome: keep the prompt for an explicit "安装" click instead of
    // letting the mini-infobar disappear without teaching the user why.
    event.preventDefault();
    S.deferredInstall = event;
    renderInstallHint();
  });

  function installHintDismissed() {
    try { return localStorage.getItem("aicli-gw-install-hint-dismissed") === "1"; } catch (e) { return false; }
  }

  function renderInstallHint() {
    var hint = S.el("install-hint");
    if (!hint) { return; }
    if (!S.authedState || installHintDismissed()) { hint.hidden = true; return; }
    var action = S.el("install-hint-action");
    if (S.deferredInstall) {
      S.el("install-hint-text").textContent = "安装到主屏幕后可像 App 一样打开，并在后台收到通知提醒。";
      action.hidden = false;
      hint.hidden = false;
      return;
    }
    if (G.isIOSDevice() && !G.isStandalone()) {
      S.el("install-hint-text").textContent = "iPhone/iPad 需先安装：Safari 分享 → 添加到主屏幕，再从桌面图标打开，后台通知才会生效。";
      action.hidden = true;
      hint.hidden = false;
      return;
    }
    hint.hidden = true;
  }

  S.el("install-hint-dismiss").addEventListener("click", function () {
    S.el("install-hint").hidden = true;
    try { localStorage.setItem("aicli-gw-install-hint-dismissed", "1"); } catch (e) { /* storage unavailable */ }
  });
  S.el("install-hint-action").addEventListener("click", function () {
    var ev = S.deferredInstall;
    S.deferredInstall = null;
    S.el("install-hint").hidden = true;
    if (ev && ev.prompt) { ev.prompt(); }
  });

  /* ─────────────────── 通知设置页交互 ───────────────────────── */

  function ensureNotifyDraft() {
    S.notifyPolicyDraft = S.notifyPolicyDraft || { version: 1, global: {}, sessions: {}, channels: {} };
    S.notifyPolicyDraft.global = S.notifyPolicyDraft.global || {};
    return S.notifyPolicyDraft;
  }

  S.notifyEnabledInput = S.el("notify-global-enabled");
  if (S.notifyEnabledInput) {
    S.notifyEnabledInput.addEventListener("change", function () {
      ensureNotifyDraft().global.muted = !S.notifyEnabledInput.checked;
      G.notifyRefreshDirty();
    });
  }
  S.notifyMinInput = S.el("notify-min-priority");
  if (S.notifyMinInput) {
    S.notifyMinInput.addEventListener("change", function () {
      ensureNotifyDraft().global.min_priority = S.notifyMinInput.value;
      G.renderNotifyPriorityHint();
      G.notifyRefreshDirty();
    });
  }
  S.notifyQuietAdd = S.el("notify-quiet-add");
  if (S.notifyQuietAdd) {
    S.notifyQuietAdd.addEventListener("click", function () {
      S.notifyQuietTouched = true;
      var first = G.notifyAppendQuietRow({ start: "", end: "" });
      if (first) { first.focus(); }
      G.notifyCollectQuietRows(true);
    });
  }
  S.notifySessionSearch = S.el("notify-session-search");
  if (S.notifySessionSearch) {
    S.notifySessionSearch.addEventListener("input", function () {
      S.notifySessionQuery = S.notifySessionSearch.value || "";
      G.renderNotifyPolicySessions();
    });
  }
  S.notifySessionOnly = S.el("notify-session-only-custom");
  if (S.notifySessionOnly) {
    S.notifySessionOnly.addEventListener("change", function () {
      S.notifySessionOnlyCustom = S.notifySessionOnly.checked;
      G.renderNotifyPolicySessions();
    });
  }
  S.notifySaveBtn = S.el("notify-save");
  if (S.notifySaveBtn) { S.notifySaveBtn.addEventListener("click", G.saveNotifyPolicy); }
  S.notifyResetBtn = S.el("notify-reset");
  if (S.notifyResetBtn) { S.notifyResetBtn.addEventListener("click", G.resetNotifyPolicyDraft); }
  S.notifyTestBtn = S.el("notify-test");
  if (S.notifyTestBtn) {
    S.notifyTestBtn.addEventListener("click", function () {
      var sidebarTest = S.el("push-test");
      if (!sidebarTest) { return; }
      S.notifyTestBtn.disabled = true;
      sidebarTest.click();
      setTimeout(function () { S.notifyTestBtn.disabled = false; }, 1500);
    });
  }
  G.notifyRefreshDirty();

  /* ── Bot 通道（网关自持 IM 目标，docs/gateway/18 §6.1）─────────────
     与上方通知策略是两份独立配置：策略决定"发不发"，Bot 目标决定"发到
     哪个 IM 会话"。未配置目标时后端不注册通道，行为与以往一致。 */

  S.notifyIMState = { config: null, meta: null, busy: false };

  function notifyIMRequest(path, options) {
    var opts = options || {};
    // 走 api() 而不是裸 fetch：IM 目标读写同样受 owner 会话保护，过期时也要
    // 回到登录页，而不是把 401 当成"配置读取失败"。
    return G.api(path, opts).then(function (resp) {
      return resp.text().then(function (text) {
        var data = null;
        if (text) {
          try { data = JSON.parse(text); } catch (e) { data = null; }
        }
        if (!resp.ok) {
          var msg = (data && (data.error || data.message)) || ("HTTP " + resp.status);
          throw new Error(msg);
        }
        return data;
      });
    });
  }

  function notifyIMShowError(msg) {
    var box = S.el("notify-im-error");
    if (!box) { return; }
    if (!msg) {
      box.hidden = true;
      box.textContent = "";
      return;
    }
    box.hidden = false;
    box.textContent = msg;
  }

  function notifyIMEmptyConfig() {
    return { version: 1, targets: [] };
  }

  function notifyIMTargets() {
    if (!S.notifyIMState.config || !S.notifyIMState.config.targets) { return []; }
    return S.notifyIMState.config.targets;
  }

  function notifyIMScopeValuesField() {
    var scope = S.el("notify-im-scope");
    var field = S.el("notify-im-scope-values-field");
    if (!scope || !field) { return; }
    field.hidden = scope.value === "all";
  }

  // populateNotifyIMNodes 把在线节点填进「节点 Bot」下拉（离线快照不进入）。
  function populateNotifyIMNodes() {
    var sel = S.el("notify-im-node");
    if (!sel) { return; }
    var current = sel.value;
    sel.innerHTML = "";
    var nodes = (S.lastNodes || []).filter(function (n) { return n && !n.stale && n.node_id; });
    nodes.forEach(function (n) {
      var opt = document.createElement("option");
      opt.value = n.node_id;
      opt.textContent = (G.nodeDisplayName(n) || n.node_id) + " · " + (n.kind || "agent");
      sel.appendChild(opt);
    });
    if (!nodes.length) {
      var none = document.createElement("option");
      none.value = "";
      none.textContent = "（暂无在线节点）";
      sel.appendChild(none);
    } else if (current) {
      sel.value = current;
    }
  }

  // IM 目标的事件类型：多选复选框，值域与「哪些消息触发通知」同源
  // （/api/notify/policy 的 meta.kinds），不再要求用户手写逗号分隔字符串。
  S.notifyIMKindsSignature = "";

  function notifyKindLabel(kind) {
    var want = G.notifyNormalizeKind(kind);
    var specs = G.notifyPolicyKindsMeta();
    for (var i = 0; i < specs.length; i++) {
      if (G.notifyNormalizeKind(specs[i].kind) === want) { return specs[i].label; }
    }
    return kind;
  }

  function notifyIMCollectKinds() {
    var out = [];
    var host = S.el("notify-im-kinds");
    if (!host) { return out; }
    host.querySelectorAll("input[data-notify-kind]").forEach(function (input) {
      if (input.checked) { out.push(input.getAttribute("data-notify-kind")); }
    });
    return out;
  }

  function notifyIMSyncKindsHint() {
    var hint = S.el("notify-im-kinds-hint");
    if (!hint) { return; }
    var source = S.el("notify-im-source");
    var isNode = !!source && source.value === "node";
    var picked = notifyIMCollectKinds();
    if (picked.length) {
      hint.textContent = "已选：" + picked.map(notifyKindLabel).join("、") + "。";
      return;
    }
    // 空选的语义由后端决定：网关 Bot = 全部事件；节点 Bot = 只发节点本地
    // hook 不发的那两类（避免 approval/question/completed 重复）。
    hint.textContent = isNode
      ? "未勾选 = 节点 Bot 默认只发「出错 / 等待中」（避免与节点本地通知重复）。"
      : "未勾选 = 全部事件。";
  }

  function notifyIMClearKinds() {
    var host = S.el("notify-im-kinds");
    if (host) {
      host.querySelectorAll("input[data-notify-kind]").forEach(function (input) {
        input.checked = false;
      });
    }
    notifyIMSyncKindsHint();
  }

  // renderNotifyIMKinds 按当前 kinds 元数据补齐复选框；元数据不变时只刷新说明，
  // 不会清掉用户已勾选的项（切换来源、重新打开页面都安全）。
  function renderNotifyIMKinds() {
    var host = S.el("notify-im-kinds");
    if (!host) { return; }
    var specs = G.notifyPolicyKindsMeta();
    var signature = specs.map(function (spec) { return spec.kind; }).join("|");
    if (signature !== S.notifyIMKindsSignature) {
      host.textContent = "";
      specs.forEach(function (spec) {
        var label = document.createElement("label");
        label.className = "notify-kind";
        var input = document.createElement("input");
        input.type = "checkbox";
        input.setAttribute("data-notify-kind", spec.kind);
        input.addEventListener("change", notifyIMSyncKindsHint);
        var copy = document.createElement("span");
        copy.className = "notify-kind-copy";
        var title = document.createElement("strong");
        title.textContent = spec.label;
        copy.appendChild(title);
        label.appendChild(input);
        label.appendChild(copy);
        host.appendChild(label);
      });
      S.notifyIMKindsSignature = signature;
    }
    notifyIMSyncKindsHint();
  }

  // notifyIMSourceField 切换「网关 Bot / 节点 Bot」的可见字段与说明。
  function notifyIMSourceField() {
    var source = S.el("notify-im-source");
    if (!source) { return; }
    var isNode = source.value === "node";
    var conn = S.el("notify-im-gateway-connector-field");
    var chat = S.el("notify-im-gateway-chat-field");
    var node = S.el("notify-im-node-field");
    if (conn) { conn.hidden = isNode; }
    if (chat) { chat.hidden = isNode; }
    if (node) { node.hidden = !isNode; }
    renderNotifyIMKinds();
    var hint = S.el("notify-im-source-hint");
    if (hint) {
      hint.textContent = isNode
        ? "节点 Bot（兼容旧部署）：委托目标节点，用该节点自己的 bot 按本地绑定发送；通知依赖节点在线与节点策略。QQ/微信建议改用连接器页的「复制到网关」。测试需要 scope=sessions 且至少一个会话。"
        : "网关 Bot：由本网关连接器直接发到指定 chat；密钥只在本网关。";
    }
    if (isNode) { populateNotifyIMNodes(); }
  }

  function renderNotifyIMTargets() {
    var host = S.el("notify-im-targets");
    if (!host) { return; }
    // 保留输入焦点：整表重建会打断正在编辑的表单，因此只在渲染后恢复。
    host.textContent = "";
    var targets = notifyIMTargets();
    var empty = S.el("notify-im-empty");
    if (empty) { empty.hidden = targets.length > 0; }
    var count = S.el("notify-im-count");
    if (count) {
      count.hidden = targets.length === 0;
      count.textContent = targets.length + " 个目标";
    }
    targets.forEach(function (target, index) {
      var row = document.createElement("div");
      row.className = "notify-im-row";
      row.setAttribute("data-connector-id", target.connector_id || "");

      var main = document.createElement("div");
      main.className = "notify-im-main";

      var title = document.createElement("div");
      title.className = "notify-im-title";
      title.textContent = target.label || target.id;
      main.appendChild(title);

      var sub = document.createElement("div");
      sub.className = "notify-im-sub";
      var scopeText = target.scope === "nodes"
        ? "指定节点：" + (target.nodes || []).join(", ")
        : target.scope === "sessions"
          ? "指定会话：" + (target.sessions || []).join(", ")
          : "全部节点";
      var source = (target.source || "gateway").toLowerCase();
      var kindText = Array.isArray(target.kinds) && target.kinds.length
        ? " · " + target.kinds.map(notifyKindLabel).join("、")
        : "";
      if (source === "node") {
        sub.textContent = "节点 " + (target.node_id || "?") + " → 该节点本地 bot" + kindText + " · " + scopeText;
      } else {
        sub.textContent = "网关 " + (target.connector_id || "?") + " → " + (target.chat || "?") + kindText + " · " + scopeText;
      }
      main.appendChild(sub);
      row.appendChild(main);

      var tools = document.createElement("div");
      tools.className = "toolbar";

      var enabled = document.createElement("label");
      enabled.className = "check-line";
      var box = document.createElement("input");
      box.type = "checkbox";
      // 缺省视为启用（与后端 enabled==null → true 一致）。
      box.checked = target.enabled !== false;
      box.addEventListener("change", function () {
        target.enabled = box.checked;
      });
      enabled.appendChild(box);
      enabled.appendChild(document.createTextNode(" 启用"));
      tools.appendChild(enabled);

      // 「配置」打开该目标自己的配置面（来源/连接/范围/事件类型）。
      var configBtn = document.createElement("button");
      configBtn.type = "button";
      configBtn.className = "btn ghost sm";
      configBtn.textContent = "配置";
      configBtn.addEventListener("click", function () { openNotifyIMEditor(target); });
      tools.appendChild(configBtn);

      var testBtn = document.createElement("button");
      testBtn.type = "button";
      testBtn.className = "btn ghost sm";
      testBtn.textContent = "测试";
      testBtn.addEventListener("click", function () {
        if (S.notifyIMState.busy) { return; }
        S.notifyIMState.busy = true;
        testBtn.disabled = true;
        notifyIMShowError("");
        notifyIMRequest("/api/notify/im/test", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          // 带上完整 target：草稿（尚未保存）也能直接测试。
          body: JSON.stringify({ target_id: target.id, target: target })
        }).then(function () {
          G.notify("已发送测试消息：" + (target.label || target.id), "ok");
        }).catch(function (err) {
          notifyIMShowError("测试失败：" + err.message);
        }).then(function () {
          S.notifyIMState.busy = false;
          testBtn.disabled = false;
        });
      });
      tools.appendChild(testBtn);

      var delBtn = document.createElement("button");
      delBtn.type = "button";
      delBtn.className = "btn ghost sm";
      delBtn.textContent = "移除";
      delBtn.addEventListener("click", function () {
        S.notifyIMState.config.targets.splice(index, 1);
        renderNotifyIMTargets();
      });
      tools.appendChild(delBtn);

      row.appendChild(tools);
      host.appendChild(row);
    });
  }

  function loadNotifyIM() {
    return notifyIMRequest("/api/notify/im").then(function (data) {
      S.notifyIMState.config = (data && data.config) || notifyIMEmptyConfig();
      if (!S.notifyIMState.config.targets) { S.notifyIMState.config.targets = []; }
      S.notifyIMState.meta = (data && data.meta) || null;
      notifyIMShowError(data && data.warning ? ("配置未生效：" + data.warning) : "");
      notifyIMSourceField();
      populateNotifyIMNodes();
      // 配置从网关重载后，编辑器里可能还停在一个已被替换/删除的目标上：收起它。
      closeNotifyIMEditor();
      renderNotifyIMTargets();
      G.ensureGatewayConnectorOptions();
    }).catch(function (err) {
      notifyIMShowError("加载 Bot 通道配置失败：" + err.message);
    });
  }

  // ── Bot 目标编辑器 ────────────────────────────────────────────
  // 全局「哪些消息触发通知」与单个目标的收件范围是两个层级：这里只编辑「这一个
  // 目标」的来源/连接/范围/事件类型，不把 per-chat 的 kinds 混进全局设置面。
  S.notifyIMEditingId = null;

  function setNotifyIMKindsSelection(kinds) {
    var host = S.el("notify-im-kinds");
    if (!host) { return; }
    var want = {};
    (kinds || []).forEach(function (kind) { want[G.notifyNormalizeKind(kind)] = true; });
    host.querySelectorAll("input[data-notify-kind]").forEach(function (input) {
      input.checked = !!want[G.notifyNormalizeKind(input.getAttribute("data-notify-kind"))];
    });
    notifyIMSyncKindsHint();
  }

  function notifyIMEditorReset() {
    var source = S.el("notify-im-source");
    if (source) { source.value = "gateway"; }
    var connector = S.el("notify-im-connector");
    if (connector) { connector.value = ""; }
    var chat = S.el("notify-im-chat");
    if (chat) { chat.value = ""; }
    var node = S.el("notify-im-node");
    if (node) { node.value = ""; }
    var label = S.el("notify-im-label");
    if (label) { label.value = ""; }
    var scope = S.el("notify-im-scope");
    if (scope) { scope.value = "all"; }
    var values = S.el("notify-im-scope-values");
    if (values) { values.value = ""; }
    notifyIMClearKinds();
    notifyIMScopeValuesField();
    notifyIMSourceField();
  }

  function closeNotifyIMEditor() {
    var box = S.el("notify-im-editor");
    if (box) { box.hidden = true; }
    S.notifyIMEditingId = null;
    notifyIMEditorReset();
  }

  function openNotifyIMEditor(target) {
    var box = S.el("notify-im-editor");
    if (!box) { return; }
    S.notifyIMEditingId = target ? target.id : null;
    notifyIMEditorReset();
    var title = S.el("notify-im-editor-title");
    if (title) {
      title.textContent = target ? ("配置目标：" + (target.label || target.id)) : "添加 Bot 目标";
    }
    var mode = S.el("notify-im-editor-mode");
    if (mode) {
      mode.textContent = target ? "编辑" : "新增";
      mode.hidden = false;
    }
    if (target) {
      var isNode = (target.source || "gateway").toLowerCase() === "node";
      var source = S.el("notify-im-source");
      if (source) { source.value = isNode ? "node" : "gateway"; }
      notifyIMSourceField();
      if (isNode) {
        populateNotifyIMNodes();
        var node = S.el("notify-im-node");
        if (node) { node.value = target.node_id || ""; }
      } else {
        var chat = S.el("notify-im-chat");
        if (chat) { chat.value = target.chat || ""; }
        // 连接器下拉是懒加载的：选项到齐后再回填，避免编辑时被清空。
        G.ensureGatewayConnectorOptions().then(function () {
          var connector = S.el("notify-im-connector");
          if (connector && target.connector_id) { connector.value = target.connector_id; }
        });
      }
      var label = S.el("notify-im-label");
      if (label) { label.value = target.label || ""; }
      var scope = S.el("notify-im-scope");
      if (scope) { scope.value = target.scope || "all"; }
      notifyIMScopeValuesField();
      var values = S.el("notify-im-scope-values");
      if (values) {
        var list = target.scope === "nodes" ? (target.nodes || [])
          : target.scope === "sessions" ? (target.sessions || []) : [];
        values.value = list.join(", ");
      }
      setNotifyIMKindsSelection(target.kinds);
    }
    box.hidden = false;
    notifyIMShowError("");
    if (box.scrollIntoView) { box.scrollIntoView({ block: "nearest" }); }
  }

  // buildNotifyIMTargetFromEditor 读取编辑器字段并校验；失败返回 null（错误已
  // 显示）。空选事件类型 = 该来源的默认语义（网关=全部，节点=error/waiting）。
  function buildNotifyIMTargetFromEditor() {
    var sourceEl = S.el("notify-im-source");
    var connector = S.el("notify-im-connector");
    var chat = S.el("notify-im-chat");
    var node = S.el("notify-im-node");
    var label = S.el("notify-im-label");
    var scope = S.el("notify-im-scope");
    var values = S.el("notify-im-scope-values");
    if (!sourceEl || !scope) { return null; }
    var source = sourceEl.value === "node" ? "node" : "gateway";
    var scopeValue = scope.value || "all";
    var target = {
      source: source,
      label: (label && label.value ? label.value.trim() : ""),
      scope: scopeValue,
    };
    if (source === "node") {
      var nodeID = (node && node.value ? node.value.trim() : "");
      if (!nodeID) {
        notifyIMShowError("请选择目标节点（节点 Bot 需要在线节点 ID）。");
        return null;
      }
      target.node_id = nodeID;
    } else {
      var connectorID = (connector && connector.value ? connector.value.trim() : "");
      var chatID = (chat && chat.value ? chat.value.trim() : "");
      if (!connectorID || !chatID) {
        notifyIMShowError("连接器 ID 与会话 ID 都是必填项。");
        return null;
      }
      target.connector_id = connectorID;
      target.chat = chatID;
    }
    var kinds = notifyIMCollectKinds();
    if (kinds.length) { target.kinds = kinds; }
    if (scopeValue !== "all") {
      var list = (values && values.value ? values.value : "")
        .split(",").map(function (s) { return s.trim(); })
        .filter(function (s) { return s !== ""; });
      if (list.length === 0) {
        notifyIMShowError("选择「指定节点 / 会话」时必须填写范围明细。");
        return null;
      }
      if (scopeValue === "nodes") { target.nodes = list; } else { target.sessions = list; }
    }
    notifyIMShowError("");
    return target;
  }

  // applyNotifyIMEditor 把编辑器写回目标列表；网关配置仍需「保存到网关」提交。
  function applyNotifyIMEditor() {
    var built = buildNotifyIMTargetFromEditor();
    if (!built) { return; }
    var list = notifyIMTargets();
    if (S.notifyIMEditingId != null) {
      for (var i = 0; i < list.length; i++) {
        if (list[i].id === S.notifyIMEditingId) {
          built.id = S.notifyIMEditingId;
          // 启用开关是列表上的快捷操作，编辑保存不能把它复位。
          built.enabled = list[i].enabled !== false;
          list[i] = built;
          break;
        }
      }
    } else {
      built.id = "t-" + Date.now().toString(36);
      list.push(built);
    }
    closeNotifyIMEditor();
    renderNotifyIMTargets();
  }

  function saveNotifyIM() {
    if (S.notifyIMState.busy) { return; }
    S.notifyIMState.busy = true;
    notifyIMShowError("");
    var payload = { version: 1, targets: notifyIMTargets() };
    notifyIMRequest("/api/notify/im", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    }).then(function (data) {
      S.notifyIMState.config = (data && data.config) || payload;
      if (!S.notifyIMState.config.targets) { S.notifyIMState.config.targets = []; }
      renderNotifyIMTargets();
      G.notify("Bot 通道配置已保存", "ok");
    }).catch(function (err) {
      notifyIMShowError("保存失败：" + err.message);
    }).then(function () {
      S.notifyIMState.busy = false;
    });
  }

  S.notifyIMAddBtn = S.el("notify-im-add");
  if (S.notifyIMAddBtn) { S.notifyIMAddBtn.addEventListener("click", function () { openNotifyIMEditor(null); }); }
  S.notifyIMApplyBtn = S.el("notify-im-apply");
  if (S.notifyIMApplyBtn) { S.notifyIMApplyBtn.addEventListener("click", applyNotifyIMEditor); }
  S.notifyIMCancelBtn = S.el("notify-im-cancel");
  if (S.notifyIMCancelBtn) { S.notifyIMCancelBtn.addEventListener("click", closeNotifyIMEditor); }
  S.notifyIMSaveBtn = S.el("notify-im-save");
  if (S.notifyIMSaveBtn) { S.notifyIMSaveBtn.addEventListener("click", saveNotifyIM); }
  S.notifyIMReloadBtn = S.el("notify-im-reload");
  if (S.notifyIMReloadBtn) { S.notifyIMReloadBtn.addEventListener("click", loadNotifyIM); }
  S.notifyIMScopeSelect = S.el("notify-im-scope");
  if (S.notifyIMScopeSelect) { S.notifyIMScopeSelect.addEventListener("change", notifyIMScopeValuesField); }
  S.notifyIMSourceSelect = S.el("notify-im-source");
  if (S.notifyIMSourceSelect) { S.notifyIMSourceSelect.addEventListener("change", notifyIMSourceField); }
  if (S.el("notify-im-targets")) { loadNotifyIM(); }
