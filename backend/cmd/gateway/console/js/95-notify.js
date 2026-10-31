// 95-notify.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=103";

Object.assign(G, {
  trackAttention: trackAttention,
  showAttentionToast: showAttentionToast,
  rememberPresence: rememberPresence,
  sendPresence: sendPresence,
  releasePresence: releasePresence,
  authHeartbeat: authHeartbeat,
  urlBase64ToUint8Array: urlBase64ToUint8Array,
  setPushLabel: setPushLabel,
  isIOSDevice: isIOSDevice,
  isStandalone: isStandalone,
  nativeNotificationApi: nativeNotificationApi,
  readNativeNotifyPref: readNativeNotifyPref,
  writeNativeNotifyPref: writeNativeNotifyPref,
  nativeNotificationsEnabled: nativeNotificationsEnabled,
  enableNativeNotifications: enableNativeNotifications,
  disableNativeNotifications: disableNativeNotifications,
  sendNativeNotification: sendNativeNotification,
  nativeAttentionNotification: nativeAttentionNotification,
  notifyNormalizeKind: notifyNormalizeKind,
  notifyKindPriority: notifyKindPriority,
  notifyKindDescription: notifyKindDescription,
  notifyPriorityOption: notifyPriorityOption,
  notifyPriorityLabel: notifyPriorityLabel,
  notifyPriorityRank: notifyPriorityRank,
  notifyParseQuietHours: notifyParseQuietHours,
  notifyInQuietHours: notifyInQuietHours,
  notifyPolicyAllowsAttention: notifyPolicyAllowsAttention,
  cloneJSON: cloneJSON,
  notifyPolicyKindsMeta: notifyPolicyKindsMeta,
  notifyPolicyChannelRows: notifyPolicyChannelRows,
  notifyCanonicalJSON: notifyCanonicalJSON,
  notifyPolicyIsDirty: notifyPolicyIsDirty,
  notifySetQuietError: notifySetQuietError,
  notifySetCompletedError: notifySetCompletedError,
  notifyCollectCompletedContent: notifyCollectCompletedContent,
  notifyRefreshDirty: notifyRefreshDirty,
  notifyDiscardPolicyDraft: notifyDiscardPolicyDraft,
  notifyParseClock: notifyParseClock,
  notifyQuietRowsFromPolicy: notifyQuietRowsFromPolicy,
  notifyQuietRangeKey: notifyQuietRangeKey,
  notifyCollectQuietRows: notifyCollectQuietRows,
  loadNotifyPolicy: loadNotifyPolicy,
  setNotifyStatus: setNotifyStatus,
  renderNotifyPolicy: renderNotifyPolicy,
  renderNotifyPriorityHint: renderNotifyPriorityHint,
  renderNotifyQuietRows: renderNotifyQuietRows,
  notifyAppendQuietRow: notifyAppendQuietRow,
  renderNotifyPolicyKinds: renderNotifyPolicyKinds,
  collectCheckedNotifyKinds: collectCheckedNotifyKinds,
  renderNotifyPolicyChannels: renderNotifyPolicyChannels,
  notifyPolicySessionRows: notifyPolicySessionRows,
  notifySessionOverrideActive: notifySessionOverrideActive,
  notifySessionSearchText: notifySessionSearchText,
  notifyFilteredSessionRows: notifyFilteredSessionRows,
  renderNotifyPolicySessions: renderNotifyPolicySessions,
  notifyMarkSessionRow: notifyMarkSessionRow,
  updateNotifySessionOverride: updateNotifySessionOverride,
  saveNotifyPolicy: saveNotifyPolicy,
  resetNotifyPolicyDraft: resetNotifyPolicyDraft,
  pushEnvironment: pushEnvironment,
  loadPushConfig: loadPushConfig,
  pushLabel: pushLabel,
  syncPushSubscription: syncPushSubscription,
  refreshPushState: refreshPushState,
  mapPushError: mapPushError,
  requestPushPermission: requestPushPermission,
  enablePush: enablePush,
  disablePush: disablePush,
});

  /* ─────────────────────── attention tracking ────────────────── */

  function trackAttention(nodes) {
    var current = {};
    var fresh = [];
    (nodes || []).forEach(function (node) {
      if (node.stale) { return; }
      (node.sessions || []).forEach(function (session) {
        if (!session.attention) { return; }
        var key = G.attentionKey(node, session);
        current[key] = true;
        if (G.isWatching(node.node_id, session.session_id)) {
          S.readAttention[key] = Date.now();
          return;
        }
        if (!S.seenAttention[key]) { fresh.push({ node: node, session: session }); }
      });
    });
    var seeded = S.attentionSeeded;
    Object.keys(S.readAttention).forEach(function (k) {
      if (!current[k]) { delete S.readAttention[k]; }
    });
    // attentionKey 清理后落盘：服务端已消解的条目不留在存储里无限增长。
    G.saveReadAttention();
    S.seenAttention = current;
    S.attentionSeeded = true;
    if (!seeded) { return; }
    fresh.forEach(function (item) { showAttentionToast(item.node, item.session); });
  }

  function showAttentionToast(node, session) {
    // The Tauri shell cannot use Web Push (WebView2 rejects subscribe()).
    // Deliver the OS notification from the same fresh-attention edge that
    // drives the in-page toast; gateway presence handles server-side dedup.
    nativeAttentionNotification(node, session);
    // The "open" affordance is a real button, so the toast is usable without a
    // mouse; clicking anywhere on the toast is no longer the only entry point.
    G.pushToast(
      G.nodeDisplayName(node) + " · " + G.sessionLabelOf(session) + "：" +
        G.attentionLabel(session.attention),
      null,
      {
        label: "打开",
        run: function () {
          if (!G.nodeHasWebUI(node)) {
            G.notify(G.webUnavailableText(node), "error");
            return;
          }
          window.open(G.sessionOpenTarget(node, session.session_id), "_blank");
        },
      }
    );
  }

  /* ─────────────────────── presence ──────────────────────────── */

  function rememberPresence(node, session, opts) {
    if (!node || !node.node_id) { return; }
    var target = {
      node_id: node.node_id,
      session_id: session ? session.session_id : "",
      // tail=true asks the gateway to keep the live-output stream on for this
      // session (P1-b watch gating); false releases it.
      tail: !!(opts && opts.tail),
    };
    S.presenceTargets[target.node_id + "|" + target.session_id] = target;
    sendPresence();
  }

  function sendPresence() {
    if (document.hidden) { return; }
    Object.keys(S.presenceTargets).forEach(function (key) {
      G.api("/api/presence", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(S.presenceTargets[key]),
      }).catch(function () {});
    });
  }

  // releasePresence clears the gateway-side suppression as soon as the page is
  // hidden or unloaded. Without it a phone that backgrounded the console keeps
  // suppressing pushes for the session it last viewed until the presence TTL
  // (default 90s) expires — the classic "切后台后没有及时提醒" report.
  function releasePresence() {
    if (!Object.keys(S.presenceTargets).length) { return; }
    var body = JSON.stringify({ all: true });
    try {
      fetch("/api/presence/release", {
        method: "POST",
        credentials: "same-origin",
        keepalive: true,
        headers: { "Content-Type": "application/json" },
        body: body,
      }).catch(function () {});
    } catch (e) { /* keepalive unsupported: the TTL still bounds the window */ }
  }

  setInterval(sendPresence, 30000);
  // 会话心跳：静置在某个视图（SSE 静默失败、页面不再发其它请求）时，owner cookie
  // 过期也能在一分钟内把用户送回登录页。命中 401 由 api() 集中处理；无 presence
  // 目标时 sendPresence 不发请求，所以这里用 /api/devices 做独立的轻量复检。
  S.authHeartbeatAt = 0;
  function authHeartbeat() {
    if (!S.authedState || document.hidden) { return; }
    var now = Date.now();
    if (now - S.authHeartbeatAt < 45000) { return; }
    S.authHeartbeatAt = now;
    G.api("/api/devices").catch(function () {});
  }
  setInterval(authHeartbeat, 60000);
  document.addEventListener("visibilitychange", function () {
    if (document.hidden) {
      releasePresence();
      return;
    }
    sendPresence();
    authHeartbeat();
    // Returning to the tab: re-sync the notification toggle with the browser's
    // actual permission/subscription (a prompt may have been answered while
    // the page was hidden).
    if (S.authedState) { refreshPushState(); }
    // Coming back from a suspended tab: if the SSE feed has been quiet for
    // a long stretch, pull a fresh snapshot instead of trusting the cache.
    if (S.eventsSubscribed && S.lastEventAt && Date.now() - S.lastEventAt > 60000) { G.loadNodes(); }
  });
  window.addEventListener("pagehide", releasePresence);

  /* ─────────────────────── Web Push ──────────────────────────── */

  S.pushRegistration = null;
  S.pushConfig = null;       // cached GET /api/push/config
  S.pushConfigPromise = null;
  S.pushSynced = false;      // per-page guard for the idempotent re-register

  function urlBase64ToUint8Array(base64) {
    var padding = "=".repeat((4 - (base64.length % 4)) % 4);
    var normalized = (base64 + padding).replace(/-/g, "+").replace(/_/g, "/");
    var raw = atob(normalized);
    var out = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) { out[i] = raw.charCodeAt(i); }
    return out;
  }

  function setPushLabel(text) {
    var label = S.el("push-toggle-label");
    if (label) { label.textContent = text; }
  }

  function isIOSDevice() {
    return /iPad|iPhone|iPod/.test(navigator.userAgent) ||
      (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
  }

  function isStandalone() {
    return (window.matchMedia && window.matchMedia("(display-mode: standalone)").matches) ||
      navigator.standalone === true;
  }

  /* ── Tauri native notifications ─────────────────────────────── */

  // The desktop/mobile shell loads this page in a WebView. WebView2 exposes
  // PushManager but rejects subscribe(), so system notifications must go
  // through Tauri's native notification bridge.
  function nativeNotificationApi() {
    return (window.__TAURI__ && window.__TAURI__.notification) || null;
  }

  function readNativeNotifyPref() {
    try { return localStorage.getItem("aicli-gw-native-notify"); } catch (e) { return null; }
  }

  function writeNativeNotifyPref(enabled) {
    try { localStorage.setItem("aicli-gw-native-notify", enabled ? "1" : "0"); } catch (e) {}
  }

  // Default: follow the OS permission. An explicit local "0" disables app-side
  // sends without trying to revoke a system permission from JS.
  function nativeNotificationsEnabled(granted) {
    var pref = readNativeNotifyPref();
    if (pref === "0") { return false; }
    if (pref === "1") { return true; }
    return !!granted;
  }

  function enableNativeNotifications() {
    // 局部名避开顶层 api()（模块化后顶层函数走 G 表，局部同名会遮蔽）。
    var tnApi = nativeNotificationApi();
    if (!tnApi) { return Promise.resolve(); }
    return Promise.resolve(tnApi.isPermissionGranted()).then(function (granted) {
      if (granted) { return "granted"; }
      return tnApi.requestPermission();
    }).then(function (state) {
      if (state !== "granted") {
        throw new Error("应用通知权限未授予（当前：" + state + "）。请在系统设置中允许通知后重试。");
      }
      writeNativeNotifyPref(true);
    });
  }

  function disableNativeNotifications() {
    // The OS permission cannot be revoked from JS; this only disables sends.
    writeNativeNotifyPref(false);
    return Promise.resolve();
  }

  function sendNativeNotification(title, body, extra) {
    var tnApi = nativeNotificationApi();
    if (!tnApi) { return Promise.resolve(false); }
    return Promise.resolve(tnApi.isPermissionGranted()).then(function (granted) {
      if (!granted || !nativeNotificationsEnabled(granted)) { return false; }
      return Promise.resolve(tnApi.sendNotification({
        title: title,
        body: body,
        extra: extra || {},
      })).then(function () { return true; });
    }).catch(function () { return false; });
  }

  function nativeAttentionNotification(node, session) {
    if (!nativeNotificationApi()) { return; }
    // 策略门（GET /api/notify/policy）：服务端 notify-policy.json 里被关闭的
    // kind / 免打扰 / 会话级静音在这里拦截，避免只改网关配置却仍被 App 弹窗
    // 打扰。策略未加载时保持原行为（fail-open），防止网络失败吞掉全部通知。
    if (!notifyPolicyAllowsAttention(session.attention, session.session_id)) { return; }
    var extra = {
      node_id: node.node_id,
      session_id: session.session_id,
      attention: session.attention,
    };
    // 点击深链：跳回网关控制台并定位该会话（#sessions?node=…&session=…），与
    // Web Push / ntfy 的 click 落点一致；节点原版 Web 仍在卡片菜单里提供，
    // 不再作为通知的默认落点（缺 url 时桌面壳会自行拼节点 URL 并可能 403）。
    try { extra.url = G.gatewaySessionLink(node.node_id, session.session_id); }
    catch (err) { /* URL 构造失败时退回桌面壳的 node/session 兜底拼装 */ }
    sendNativeNotification(
      G.nodeDisplayName(node) + " · " + G.sessionLabelOf(session),
      G.attentionLabel(session.attention),
      extra
    );
  }

  /* ────────────── 通知设置页 / 通知策略（GET-PUT API） ────────── */

  // 词表兜底：正常取 /api/notify/policy 的 meta.kinds（与网关登记表同源）。
  S.NOTIFY_KIND_FALLBACK = [
    { kind: "approval", label: "待审批", priority: "high" },
    { kind: "question", label: "待回答", priority: "high" },
    { kind: "error", label: "出错", priority: "high" },
    { kind: "completed", label: "已完成", priority: "normal" },
    { kind: "waiting", label: "等待中", priority: "normal" },
  ];
  S.NOTIFY_PRIORITY_OPTIONS = [
    { value: "low", label: "全部通知", hint: "所有类型都通知，包括未来新增的低优先级消息。" },
    { value: "normal", label: "重要通知（默认）", hint: "待处理、已完成和等待中都会通知。" },
    { value: "high", label: "仅待处理", hint: "仅待审批、待回答和出错；完成/等待状态不打扰。" },
  ];
  S.NOTIFY_KIND_DESCRIPTIONS = {
    approval: "工具请求审批，需要你允许或拒绝。",
    question: "会话向你提问，等待你的答复。",
    error: "执行失败，需要排查或重试。",
    completed: "一轮执行完成，可以回来查看结果。",
    waiting: "等待子任务或外部条件继续。",
  };

  function notifyNormalizeKind(kind) {
    return kind === "completion" ? "completed" : String(kind || "");
  }

  function notifyKindPriority(kind) {
    kind = notifyNormalizeKind(kind);
    if (kind === "approval" || kind === "question" || kind === "error") { return "high"; }
    return "normal";
  }

  function notifyKindDescription(kind) {
    kind = notifyNormalizeKind(kind);
    return S.NOTIFY_KIND_DESCRIPTIONS[kind] || "收到该类事件时通知。";
  }

  function notifyPriorityOption(value) {
    for (var i = 0; i < S.NOTIFY_PRIORITY_OPTIONS.length; i++) {
      if (S.NOTIFY_PRIORITY_OPTIONS[i].value === value) { return S.NOTIFY_PRIORITY_OPTIONS[i]; }
    }
    return S.NOTIFY_PRIORITY_OPTIONS[1];
  }

  function notifyPriorityLabel(value) {
    return notifyPriorityOption(value).label;
  }

  function notifyPriorityRank(priority) {
    if (priority === "high") { return 2; }
    if (priority === "normal") { return 1; }
    return 0;
  }

  function notifyParseQuietHours(list) {
    var out = [];
    (Array.isArray(list) ? list : []).forEach(function (item) {
      var m = /^\s*(\d{1,2}):(\d{2})\s*-\s*(\d{1,2}):(\d{2})\s*$/.exec(String(item || ""));
      if (!m) { return; }
      var start = parseInt(m[1], 10) * 60 + parseInt(m[2], 10);
      var end = parseInt(m[3], 10) * 60 + parseInt(m[4], 10);
      if (start === end) { return; }
      out.push([start, end]);
    });
    return out;
  }

  function notifyInQuietHours(ranges, now) {
    var minutes = now.getHours() * 60 + now.getMinutes();
    for (var i = 0; i < ranges.length; i++) {
      var start = ranges[i][0], end = ranges[i][1];
      if (start < end) {
        if (minutes >= start && minutes < end) { return true; }
      } else if (minutes >= start || minutes < end) {
        return true;
      }
    }
    return false;
  }

  // notifyPolicyAllowsAttention 是控制台侧的策略判定（与服务端
  // notify.Preference 的字段级覆盖同序：global → session → channel）。
  // 策略未加载时返回 true，保持既有行为，避免一次网络失败把原生通知
  // 全部静默。
  function notifyPolicyAllowsAttention(kind, sessionID) {
    if (!kind) { return false; }
    if (!S.notifyPolicy) { return true; }
    var global = S.notifyPolicy.global || {};
    if (global.muted === true) { return false; }
    var session = (S.notifyPolicy.sessions || {})[sessionID] || {};
    if (session.muted === true) { return false; }
    var priority = notifyKindPriority(kind);
    var minPriority = session.min_priority || global.min_priority || "low";
    if (notifyPriorityRank(priority) < notifyPriorityRank(minPriority)) { return false; }
    var kinds = session.allow_kinds != null ? session.allow_kinds : global.allow_kinds;
    if (kinds != null) {
      var allowed = false;
      for (var i = 0; i < kinds.length; i++) {
        if (notifyNormalizeKind(kinds[i]) === notifyNormalizeKind(kind)) { allowed = true; break; }
      }
      if (!allowed) { return false; }
    }
    var quiet = session.quiet_hours != null ? session.quiet_hours : global.quiet_hours;
    if (priority !== "high" && notifyInQuietHours(notifyParseQuietHours(quiet), new Date())) {
      return false;
    }
    return true;
  }

  function cloneJSON(value) {
    try { return JSON.parse(JSON.stringify(value || {})); } catch (e) { return {}; }
  }

  function notifyPolicyKindsMeta() {
    if (S.notifyPolicyMeta && Array.isArray(S.notifyPolicyMeta.kinds) && S.notifyPolicyMeta.kinds.length) {
      return S.notifyPolicyMeta.kinds;
    }
    return S.NOTIFY_KIND_FALLBACK;
  }

  function notifyPolicyChannelRows() {
    return (S.notifyPolicyMeta && Array.isArray(S.notifyPolicyMeta.channels)) ? S.notifyPolicyMeta.channels : [];
  }

  function notifyCanonicalJSON(value) {
    if (value === null || typeof value !== "object") { return JSON.stringify(value); }
    if (Array.isArray(value)) {
      return "[" + value.map(notifyCanonicalJSON).join(",") + "]";
    }
    var keys = Object.keys(value).sort();
    return "{" + keys.map(function (key) {
      return JSON.stringify(key) + ":" + notifyCanonicalJSON(value[key]);
    }).join(",") + "}";
  }

  function notifyPolicyIsDirty() {
    if (S.notifyQuietError) { return true; }
    if (S.notifyCompletedError) { return true; }
    if (!S.notifyPolicyDraft || !S.notifyPolicy) { return false; }
    return notifyCanonicalJSON(S.notifyPolicyDraft) !== S.notifyPolicyBaseline;
  }

  function notifySetQuietError(message) {
    S.notifyQuietError = String(message || "");
    var node = S.el("notify-quiet-error");
    if (node) {
      node.textContent = S.notifyQuietError;
      node.hidden = !S.notifyQuietError;
    }
  }

  function notifySetCompletedError(message) {
    S.notifyCompletedError = String(message || "");
    var node = S.el("notify-completed-error");
    if (node) {
      node.textContent = S.notifyCompletedError;
      node.hidden = !S.notifyCompletedError;
    }
  }

  // notifyCollectCompletedContent 校验并写回「已完成通知内容」两个旋钮
  // （docs/gateway/08-notifications.md §5.1）。返回 false = 输入非法（错误已就地
  // 显示），调用方应中止保存。
  function notifyCollectCompletedContent(showError) {
    var include = S.el("notify-completed-include-last-message");
    var maxInput = S.el("notify-completed-max-chars");
    if (!include || !maxInput || !S.notifyPolicyDraft) { return true; }
    S.notifyPolicyDraft.global = S.notifyPolicyDraft.global || {};
    var global = S.notifyPolicyDraft.global;
    var raw = String(maxInput.value || "").trim();
    var limit = parseInt(raw, 10);
    if (include.checked) {
      global.completed_include_last_message = true;
      if (!raw || !(limit >= 1 && limit <= 2000)) {
        if (showError) { notifySetCompletedError("附带正文上限需为 1..2000 的整数（字符数）。"); }
        // 与免打扰收集器一致：非法值也要刷新保存条（否则保存按钮停在 disabled，
        // 用户改了却点不动，看起来像"功能不生效"）。
        notifyRefreshDirty();
        return false;
      }
      global.completed_last_message_max_chars = limit;
    } else {
      delete global.completed_include_last_message;
      // 关闭时上限只是"下次打开用"的草稿值：合法才写回，非法忽略（打开时会重校验）。
      if (raw && limit >= 1 && limit <= 2000) {
        global.completed_last_message_max_chars = limit;
      }
    }
    if (showError) { notifySetCompletedError(""); }
    notifyRefreshDirty();
    return true;
  }

  function notifyRefreshDirty() {
    var bar = S.el("notify-savebar");
    var text = S.el("notify-dirty");
    var dot = S.el("notify-dirty-dot");
    var save = S.el("notify-save");
    var dirty = notifyPolicyIsDirty();
    var blocked = !!S.notifyQuietError;
    if (bar) {
      bar.classList.toggle("is-dirty", dirty || blocked);
      bar.classList.toggle("is-invalid", blocked);
    }
    if (dot) { dot.hidden = !(dirty || blocked); }
    if (text) {
      if (!S.notifyPolicyLoaded && !S.notifyPolicyDraft) {
        text.textContent = "策略尚未加载";
      } else if (blocked) {
        text.textContent = S.notifyQuietError;
      } else if (S.notifyPolicySaving) {
        text.textContent = "正在保存…";
      } else if (dirty) {
        text.textContent = "有未保存的修改";
      } else {
        text.textContent = "所有修改已保存";
      }
    }
    if (save) { save.disabled = !dirty || blocked || S.notifyPolicySaving; }
  }

  function notifyDiscardPolicyDraft() {
    if (!S.notifyPolicy) { return; }
    S.notifyPolicyDraft = cloneJSON(S.notifyPolicy);
    notifySetQuietError("");
    notifyRefreshDirty();
  }

  function notifyParseClock(value) {
    var match = /^(\d{1,2}):(\d{2})$/.exec(String(value || "").trim());
    if (!match) { return ""; }
    var hour = parseInt(match[1], 10);
    var minute = parseInt(match[2], 10);
    if (hour > 23 || minute > 59) { return ""; }
    return (hour < 10 ? "0" : "") + hour + ":" + (minute < 10 ? "0" : "") + minute;
  }

  function notifyQuietRowsFromPolicy() {
    var raw = S.notifyPolicyDraft && S.notifyPolicyDraft.global && S.notifyPolicyDraft.global.quiet_hours;
    if (!Array.isArray(raw)) { return []; }
    var rows = [];
    raw.forEach(function (item) {
      var match = /^\s*(\d{1,2}:\d{2})\s*-\s*(\d{1,2}:\d{2})\s*$/.exec(String(item || ""));
      if (!match) { return; }
      var start = notifyParseClock(match[1]);
      var end = notifyParseClock(match[2]);
      if (start && end) { rows.push({ start: start, end: end }); }
    });
    return rows;
  }

  function notifyQuietRangeKey(start, end) {
    return start + "-" + end;
  }

  function notifyCollectQuietRows(showError) {
    var host = S.el("notify-quiet-rows");
    var invalid = null;
    if (!host) { return true; }
    var rows = Array.prototype.slice.call(host.querySelectorAll(".notify-quiet-row"));
    var ranges = [];
    var error = "";
    rows.forEach(function (row, index) {
      var startInput = row.querySelector("[data-quiet-start]");
      var endInput = row.querySelector("[data-quiet-end]");
      var start = notifyParseClock(startInput && startInput.value);
      var end = notifyParseClock(endInput && endInput.value);
      if (!start || !end) {
        if (!error) {
          error = "请同时填写第 " + (index + 1) + " 个免打扰时段的开始和结束时间。";
          invalid = !start ? startInput : endInput;
        }
        return;
      }
      if (start === end) {
        if (!error) {
          error = "免打扰时段的开始和结束时间不能相同。";
          invalid = startInput;
        }
        return;
      }
      var key = notifyQuietRangeKey(start, end);
      if (ranges.indexOf(key) !== -1) {
        if (!error) {
          error = "存在重复的免打扰时段，请合并相同区间。";
          invalid = startInput;
        }
        return;
      }
      ranges.push(key);
    });
    if (showError) { notifySetQuietError(error); }
    if (!error) {
      S.notifyPolicyDraft.global = S.notifyPolicyDraft.global || {};
      if (ranges.length || S.notifyQuietTouched) {
        S.notifyPolicyDraft.global.quiet_hours = ranges;
      }
    }
    S.notifyQuietInvalidEl = invalid;
    notifyRefreshDirty();
    return !error;
  }

  function loadNotifyPolicy(force) {
    if (S.notifyPolicyLoading) { return S.notifyPolicyLoading; }
    if (S.notifyPolicyLoaded && !force) { return Promise.resolve(S.notifyPolicy); }
    S.notifyPolicyLoading = G.apiJSON("/api/notify/policy").then(function (r) {
      if (r.res.status === 401) { G.setAuthed(false); return S.notifyPolicy; }
      if (!r.res.ok || !r.body || !r.body.policy) {
        throw new Error("HTTP " + r.res.status + (r.body && r.body.error ? " " + r.body.error : ""));
      }
      S.notifyPolicy = r.body.policy;
      S.notifyPolicyMeta = r.body.meta || {};
      S.notifyPolicyDraft = cloneJSON(S.notifyPolicy);
      S.notifyPolicyBaseline = notifyCanonicalJSON(S.notifyPolicy);
      S.notifyPolicyLoaded = true;
      S.notifyPolicySaving = false;
      S.notifyQuietTouched = false;
      S.notifyQuietInvalidEl = null;
      notifySetQuietError("");
      renderNotifyPolicy();
      return S.notifyPolicy;
    }).catch(function (err) {
      if (G.currentView() === "notifications") {
        G.notify("通知策略读取失败：" + (err && err.message ? err.message : err), "error");
      }
      notifyRefreshDirty();
      return S.notifyPolicy;
    });
    var pending = S.notifyPolicyLoading;
    pending.finally(function () { if (S.notifyPolicyLoading === pending) { S.notifyPolicyLoading = null; } });
    return pending;
  }

  function setNotifyStatus(text) {
    var status = S.el("notify-status");
    if (!status) { return; }
    status.textContent = text || "";
    status.hidden = !text;
  }

  function renderNotifyPolicy() {
    if (!S.notifyPolicyDraft) { notifyRefreshDirty(); return; }
    var global = S.notifyPolicyDraft.global || {};
    var enabled = S.el("notify-global-enabled");
    if (enabled) { enabled.checked = global.muted !== true; }
    var min = S.el("notify-min-priority");
    if (min) { min.value = global.min_priority || "normal"; }
    renderNotifyPriorityHint();
    // docs/gateway/08 §5.1：「已完成」是否附带最后一条助手回复 + 长度上限。
    var includeLast = S.el("notify-completed-include-last-message");
    if (includeLast) { includeLast.checked = global.completed_include_last_message === true; }
    var maxChars = S.el("notify-completed-max-chars");
    if (maxChars) {
      var limit = parseInt(global.completed_last_message_max_chars, 10);
      maxChars.value = String(limit >= 1 && limit <= 2000 ? limit : 200);
    }
    notifySetCompletedError("");
    renderNotifyQuietRows();
    var source = S.el("notify-policy-source");
    if (source) {
      var meta = S.notifyPolicyMeta || {};
      var labels = { file: "策略文件", legacy: "旧偏好回退", "default": "默认值" };
      source.textContent = labels[meta.source] || "策略";
      source.title = meta.path || "";
      source.hidden = false;
    }
    renderNotifyPolicyKinds();
    renderNotifyPolicyChannels();
    renderNotifyPolicySessions();
    notifyRefreshDirty();
  }

  function renderNotifyPriorityHint() {
    var hint = S.el("notify-priority-hint");
    var select = S.el("notify-min-priority");
    if (!hint || !select) { return; }
    hint.textContent = notifyPriorityOption(select.value).hint;
  }

  function renderNotifyQuietRows() {
    var host = S.el("notify-quiet-rows");
    if (!host) { return; }
    host.textContent = "";
    notifyQuietRowsFromPolicy().forEach(function (range) {
      notifyAppendQuietRow(range);
    });
  }

  function notifyAppendQuietRow(range) {
    var host = S.el("notify-quiet-rows");
    if (!host) { return; }
    range = range || { start: "", end: "" };
    var row = document.createElement("div");
    row.className = "notify-quiet-row";
    var start = document.createElement("input");
    start.type = "time";
    start.className = "filter-input notify-quiet-time";
    start.value = range.start || "";
    start.setAttribute("data-quiet-start", "");
    start.setAttribute("aria-label", "免打扰开始时间");
    var separator = document.createElement("span");
    separator.className = "notify-quiet-sep";
    separator.textContent = "至";
    separator.setAttribute("aria-hidden", "true");
    var end = document.createElement("input");
    end.type = "time";
    end.className = "filter-input notify-quiet-time";
    end.value = range.end || "";
    end.setAttribute("data-quiet-end", "");
    end.setAttribute("aria-label", "免打扰结束时间");
    var remove = document.createElement("button");
    remove.type = "button";
    remove.className = "btn ghost sm notify-quiet-remove";
    remove.textContent = "移除";
    remove.setAttribute("aria-label", "移除此免打扰时段");
    var changed = function () {
      S.notifyQuietTouched = true;
      notifyCollectQuietRows(true);
    };
    start.addEventListener("input", changed);
    start.addEventListener("change", changed);
    end.addEventListener("input", changed);
    end.addEventListener("change", changed);
    remove.addEventListener("click", function () {
      S.notifyQuietTouched = true;
      row.remove();
      notifyCollectQuietRows(true);
    });
    row.appendChild(start);
    row.appendChild(separator);
    row.appendChild(end);
    row.appendChild(remove);
    host.appendChild(row);
    return start;
  }

  function renderNotifyPolicyKinds() {
    var host = S.el("notify-kinds");
    if (!host || !S.notifyPolicyDraft) { return; }
    host.textContent = "";
    var global = S.notifyPolicyDraft.global || {};
    var allow = global.allow_kinds;
    notifyPolicyKindsMeta().forEach(function (spec) {
      var label = document.createElement("label");
      label.className = "notify-kind";
      var input = document.createElement("input");
      input.type = "checkbox";
      input.setAttribute("data-notify-kind", spec.kind);
      if (allow == null) {
        input.checked = true;
      } else {
        input.checked = allow.some(function (kind) {
          return notifyNormalizeKind(kind) === notifyNormalizeKind(spec.kind);
        });
      }
      input.addEventListener("change", function () {
        S.notifyPolicyDraft.global = S.notifyPolicyDraft.global || {};
        S.notifyPolicyDraft.global.allow_kinds = collectCheckedNotifyKinds();
        notifyRefreshDirty();
      });
      var copy = document.createElement("span");
      copy.className = "notify-kind-copy";
      var title = document.createElement("strong");
      title.textContent = spec.label;
      var description = document.createElement("span");
      description.className = "notify-kind-desc";
      description.textContent = notifyKindDescription(spec.kind);
      copy.appendChild(title);
      copy.appendChild(description);
      label.appendChild(input);
      label.appendChild(copy);
      host.appendChild(label);
    });
  }

  function collectCheckedNotifyKinds() {
    var out = [];
    document.querySelectorAll("#notify-kinds input[data-notify-kind]").forEach(function (input) {
      if (input.checked) { out.push(input.getAttribute("data-notify-kind")); }
    });
    return out;
  }

  function renderNotifyPolicyChannels() {
    var host = S.el("notify-channels");
    if (!host || !S.notifyPolicyDraft) { return; }
    host.textContent = "";
    var rows = notifyPolicyChannelRows();
    if (!rows.length) {
      var empty = document.createElement("p");
      empty.className = "empty";
      empty.textContent = "后端未报告可用通道。";
      host.appendChild(empty);
      return;
    }
    rows.forEach(function (row) {
      var label = document.createElement("label");
      label.className = "notify-channel" + (row.available ? "" : " is-unavailable");
      var input = document.createElement("input");
      input.type = "checkbox";
      input.disabled = !row.available;
      var entry = (S.notifyPolicyDraft.channels || {})[row.name] || {};
      input.checked = row.available && entry.muted !== true;
      input.addEventListener("change", function () {
        S.notifyPolicyDraft.channels = S.notifyPolicyDraft.channels || {};
        if (input.checked) {
          delete S.notifyPolicyDraft.channels[row.name];
        } else {
          S.notifyPolicyDraft.channels[row.name] = { muted: true };
        }
        notifyRefreshDirty();
      });
      var copy = document.createElement("span");
      copy.className = "notify-channel-copy";
      var title = document.createElement("strong");
      title.textContent = row.label;
      var status = document.createElement("span");
      status.className = "notify-channel-desc";
      if (!row.available) {
        var badge = document.createElement("span");
        badge.className = "badge notify-channel-badge";
        badge.textContent = "未配置";
        title.appendChild(badge);
        status.textContent = "需在网关启动参数中配置后可用。";
        label.title = "该通道尚未在网关配置";
      } else {
        status.textContent = entry.muted === true ? "已关闭：不会向该通道发送通知。" : "接收网关外发通知。";
      }
      copy.appendChild(title);
      copy.appendChild(status);
      label.appendChild(input);
      label.appendChild(copy);
      host.appendChild(label);
    });
  }

  function notifyPolicySessionRows() {
    var rows = [];
    (S.lastNodes || []).forEach(function (node) {
      (node.sessions || []).forEach(function (session) {
        if (session && session.session_id) { rows.push({ node: node, session: session }); }
      });
    });
    rows.sort(function (a, b) {
      var na = G.nodeDisplayName(a.node).localeCompare(G.nodeDisplayName(b.node));
      if (na !== 0) { return na; }
      var ia = String(a.node.node_id || "").localeCompare(String(b.node.node_id || ""));
      if (ia !== 0) { return ia; }
      var pa = S.attentionPriority[a.session.attention] != null ? S.attentionPriority[a.session.attention] : 9;
      var pb = S.attentionPriority[b.session.attention] != null ? S.attentionPriority[b.session.attention] : 9;
      if (pa !== pb) { return pa - pb; }
      return String(a.session.title || "").localeCompare(String(b.session.title || ""));
    });
    return rows;
  }

  function notifySessionOverrideActive(entry) {
    return !!(entry && (entry.muted != null || entry.min_priority || entry.allow_kinds != null));
  }

  function notifySessionSearchText(item) {
    return [
      G.nodeDisplayName(item.node),
      item.node && item.node.node_id,
      item.session && item.session.session_id,
      item.session && item.session.title,
      item.session && item.session.preview,
    ].join(" ").toLowerCase();
  }

  function notifyFilteredSessionRows(rows) {
    var query = String(S.notifySessionQuery || "").trim().toLowerCase();
    var sessions = S.notifyPolicyDraft.sessions || {};
    return rows.filter(function (item) {
      if (S.notifySessionOnlyCustom && !notifySessionOverrideActive(sessions[item.session.session_id])) {
        return false;
      }
      return !query || notifySessionSearchText(item).indexOf(query) !== -1;
    });
  }

  function renderNotifyPolicySessions() {
    var host = S.el("notify-session-list");
    if (!host || !S.notifyPolicyDraft) { return; }
    // 正在编辑时不要被 SSE 刷新打断焦点/滚动。
    if (host.contains(document.activeElement)) { return; }
    host.textContent = "";
    var allRows = notifyPolicySessionRows();
    var filtered = notifyFilteredSessionRows(allRows);
    var rows = filtered.slice(0, 200);
    var search = S.el("notify-session-search");
    if (search && search.value !== S.notifySessionQuery) { search.value = S.notifySessionQuery; }
    var onlyCustom = S.el("notify-session-only-custom");
    if (onlyCustom) { onlyCustom.checked = S.notifySessionOnlyCustom; }
    var empty = S.el("notify-session-empty");
    if (empty) {
      empty.hidden = rows.length > 0;
      empty.textContent = allRows.length ? "没有匹配的会话。" : "暂无已知会话；会话上报后这里会出现可单独静音或提高门槛的条目。";
    }
    var count = S.el("notify-session-count");
    if (count) {
      count.textContent = filtered.length === allRows.length
        ? allRows.length + " 个会话"
        : rows.length + " / " + allRows.length + " 个会话";
      count.hidden = allRows.length === 0;
    }
    var sessions = S.notifyPolicyDraft.sessions || {};
    var group = null;
    var groupHost = null;
    rows.forEach(function (item) {
      var sessionID = item.session.session_id;
      var override = sessions[sessionID] || {};
      var nodeID = item.node.node_id || G.nodeDisplayName(item.node);
      if (!group || group.getAttribute("data-node-id") !== nodeID) {
        group = document.createElement("section");
        group.className = "notify-session-group";
        group.setAttribute("data-node-id", nodeID);
        var head = document.createElement("div");
        head.className = "notify-session-group-head";
        head.textContent = G.nodeDisplayName(item.node);
        groupHost = document.createElement("div");
        groupHost.className = "list notify-session-group-list";
        group.appendChild(head);
        group.appendChild(groupHost);
        host.appendChild(group);
      }
      var row = document.createElement("div");
      row.className = "notify-session-row";
      row.setAttribute("data-session-id", sessionID);
      row.classList.toggle("is-overridden", notifySessionOverrideActive(override));
      var name = document.createElement("span");
      name.className = "notify-session-name";
      name.textContent = G.sessionLabelOf(item.session);
      name.title = sessionID;
      row.appendChild(name);
      var custom = document.createElement("span");
      custom.className = "badge notify-session-custom";
      custom.textContent = "已调整";
      custom.hidden = !notifySessionOverrideActive(override);
      row.appendChild(custom);

      var mute = document.createElement("label");
      mute.className = "check-line";
      var muteInput = document.createElement("input");
      muteInput.type = "checkbox";
      muteInput.checked = override.muted === true;
      muteInput.addEventListener("change", function () {
        updateNotifySessionOverride(sessionID, "muted", muteInput.checked ? true : null);
        notifyMarkSessionRow(row, custom);
      });
      var muteText = document.createElement("span");
      muteText.textContent = "静音";
      mute.appendChild(muteInput);
      mute.appendChild(muteText);
      row.appendChild(mute);

      var select = document.createElement("select");
      select.className = "select sm";
      select.setAttribute("aria-label", "该会话的通知范围：" + G.sessionLabelOf(item.session));
      [["", "跟随全局"]].concat(S.NOTIFY_PRIORITY_OPTIONS.map(function (opt) {
        return [opt.value, opt.label];
      })).forEach(function (opt) {
        var option = document.createElement("option");
        option.value = opt[0];
        option.textContent = opt[1];
        select.appendChild(option);
      });
      select.value = override.min_priority || "";
      select.addEventListener("change", function () {
        updateNotifySessionOverride(sessionID, "min_priority", select.value || null);
        notifyMarkSessionRow(row, custom);
      });
      row.appendChild(select);
      groupHost.appendChild(row);
    });
  }

  function notifyMarkSessionRow(row, badge) {
    if (!row || !S.notifyPolicyDraft) { return; }
    var sessionID = row.getAttribute("data-session-id");
    var entry = (S.notifyPolicyDraft.sessions || {})[sessionID];
    var active = notifySessionOverrideActive(entry);
    row.classList.toggle("is-overridden", active);
    if (badge) { badge.hidden = !active; }
  }

  function updateNotifySessionOverride(sessionID, field, value) {
    S.notifyPolicyDraft.sessions = S.notifyPolicyDraft.sessions || {};
    var entry = S.notifyPolicyDraft.sessions[sessionID] || {};
    if (value == null) {
      delete entry[field];
    } else {
      entry[field] = value;
    }
    if (entry.muted == null && !entry.min_priority && entry.allow_kinds == null) {
      delete S.notifyPolicyDraft.sessions[sessionID];
    } else {
      S.notifyPolicyDraft.sessions[sessionID] = entry;
    }
    notifyRefreshDirty();
  }

  function saveNotifyPolicy() {
    if (!S.notifyPolicyDraft || !S.notifyPolicyLoaded || S.notifyPolicySaving) { return; }
    if (!notifyCollectQuietRows(true)) {
      if (S.notifyQuietInvalidEl) { S.notifyQuietInvalidEl.focus(); }
      return;
    }
    if (!notifyCollectCompletedContent(true)) {
      var completedLimitEl = S.el("notify-completed-max-chars");
      if (completedLimitEl) { completedLimitEl.focus(); }
      return;
    }
    if (!notifyPolicyIsDirty()) {
      notifyRefreshDirty();
      return;
    }
    var payload = cloneJSON(S.notifyPolicyDraft);
    payload.version = 1;
    S.notifyPolicySaving = true;
    notifyRefreshDirty();
    setNotifyStatus("保存中…");
    G.apiJSON("/api/notify/policy", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    }).then(function (r) {
      if (r.res.status === 401) {
        S.notifyPolicySaving = false;
        setNotifyStatus("");
        G.setAuthed(false);
        return;
      }
      if (!r.res.ok) {
        throw new Error((r.body && r.body.error) || ("HTTP " + r.res.status));
      }
      S.notifyPolicy = r.body.policy;
      S.notifyPolicyMeta = r.body.meta || S.notifyPolicyMeta;
      S.notifyPolicyDraft = cloneJSON(S.notifyPolicy);
      S.notifyPolicyBaseline = notifyCanonicalJSON(S.notifyPolicy);
      S.notifyPolicyLoaded = true;
      S.notifyPolicySaving = false;
      S.notifyQuietTouched = false;
      S.notifyQuietInvalidEl = null;
      notifySetQuietError("");
      setNotifyStatus("");
      renderNotifyPolicy();
      G.notify("通知策略已保存并立即生效", "ok");
    }).catch(function (err) {
      S.notifyPolicySaving = false;
      setNotifyStatus("");
      notifyRefreshDirty();
      G.notify("保存策略失败：" + (err && err.message ? err.message : err), "error");
    });
  }

  function resetNotifyPolicyDraft() {
    if (notifyPolicyIsDirty()) {
      var reset = true;
      try {
        reset = window.confirm("恢复默认值会覆盖当前未保存的修改，确定继续吗？");
      } catch (e) { /* headless/jsdom: proceed */ }
      if (!reset) { return; }
    }
    var defaults = (S.notifyPolicyMeta && S.notifyPolicyMeta.defaults) || {
      muted: false,
      min_priority: "normal",
      allow_kinds: ["approval", "question", "error", "completed", "waiting"],
    };
    S.notifyPolicyDraft = { version: 1, global: cloneJSON(defaults), sessions: {}, channels: {} };
    S.notifyPolicyDraft.global = S.notifyPolicyDraft.global || {};
    if (!Array.isArray(S.notifyPolicyDraft.global.quiet_hours)) {
      S.notifyPolicyDraft.global.quiet_hours = [];
    }
    S.notifyQuietTouched = true;
    S.notifyQuietInvalidEl = null;
    notifySetQuietError("");
    notifySetCompletedError("");
    renderNotifyPolicy();
    G.notify("已恢复默认值，点击「保存策略」后生效", "ok");
  }

  // pushEnvironment explains whether Web Push can work here and, when it
  // cannot, why. The reason is surfaced in the toggle tooltip and in error
  // toasts instead of silently hiding the button: on plain HTTP or in an iOS
  // Safari tab the user previously saw no notification affordance at all.
  function pushEnvironment() {
    if (nativeNotificationApi()) { return { ok: true, native: true }; }
    if (window.isSecureContext === false) {
      return { ok: false, reason: "当前地址不是 HTTPS（安全上下文），浏览器禁用了 Service Worker 与推送。请改用 https:// 访问网关，或用 --tls-cert/--tls-key 让网关直接提供 TLS。" };
    }
    if (isIOSDevice() && !isStandalone()) {
      return { ok: false, needsInstall: true, reason: "iOS 的 Web Push 只在“添加到主屏幕”后的 PWA 中生效：请用 Safari 打开本页 → 分享 → 添加到主屏幕，再从桌面图标打开并开启通知。" };
    }
    if (!("serviceWorker" in navigator) || !("PushManager" in window)) {
      return { ok: false, reason: "当前浏览器不支持 Web Push。请使用 Chrome / Edge / Safari 16.4+，或改用控制台实时更新。" };
    }
    return { ok: true };
  }

  function loadPushConfig() {
    if (S.pushConfig) { return Promise.resolve(S.pushConfig); }
    if (S.pushConfigPromise) { return S.pushConfigPromise; }
    S.pushConfigPromise = G.apiJSON("/api/push/config").then(function (r) {
      S.pushConfig = (r && r.res && r.res.ok && r.body) ? r.body : { enabled: false };
    }).catch(function () {
      S.pushConfig = { enabled: false };
    }).then(function () {
      S.pushConfigPromise = null;
      return S.pushConfig;
    });
    return S.pushConfigPromise;
  }

  function pushLabel() {
    var label = navigator.platform || "";
    if (navigator.userAgent.indexOf("Mobile") !== -1) { label += " mobile"; }
    if (isIOSDevice()) { label += " ios"; }
    return label.trim();
  }

  // Re-POST an existing browser subscription: the gateway upserts by endpoint,
  // so this self-heals a lost/restored push store without user action.
  function syncPushSubscription(sub) {
    if (!sub || S.pushSynced || !sub.toJSON) { return; }
    S.pushSynced = true;
    var json = sub.toJSON();
    G.apiJSON("/api/push/subscriptions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        endpoint: json.endpoint,
        keys: json.keys || {},
        label: pushLabel(),
      }),
    }).catch(function () {});
  }

  function refreshPushState() {
    var btn = S.el("push-toggle");
    var test = S.el("push-test");
    if (!btn) { return; }
    btn.hidden = !S.authedState;
    if (!S.authedState) { return; }
    var nativeApi = nativeNotificationApi();
    if (nativeApi) {
      btn.title = "使用 Tauri 系统原生通知";
      Promise.resolve(nativeApi.isPermissionGranted()).then(function (granted) {
        var enabled = nativeNotificationsEnabled(granted);
        setPushLabel(enabled ? "关闭应用通知" : "开启应用通知");
        if (test) { test.hidden = !enabled; }
        if (!granted) {
          btn.title = "尚未授予应用通知权限：点击开启并在系统设置中允许通知。";
        }
      }).catch(function () {
        setPushLabel("开启应用通知");
        if (test) { test.hidden = true; }
      });
      return;
    }
    var env = pushEnvironment();
    if (!env.ok) {
      setPushLabel(env.needsInstall ? "先安装到主屏幕" : "通知不可用");
      btn.title = env.reason;
      if (test) { test.hidden = true; }
      return;
    }
    if (!("Notification" in window) || !Notification.requestPermission) {
      setPushLabel("通知不可用");
      btn.title = "当前浏览器不支持系统通知。";
      if (test) { test.hidden = true; }
      return;
    }
    if (Notification.permission === "denied") {
      setPushLabel("通知被禁用");
      btn.title = "浏览器/系统已拒绝通知权限：请在站点设置或系统通知设置中允许后刷新页面。";
      if (test) { test.hidden = true; }
      return;
    }
    loadPushConfig().then(function (cfg) {
      if (!cfg || !cfg.enabled) {
        setPushLabel("通知未启用");
        btn.title = "网关未启用推送（--push-enabled=false 或 VAPID 初始化失败）。";
        if (test) { test.hidden = true; }
        return;
      }
      btn.title = "";
      return navigator.serviceWorker.ready
      .then(function (reg) {
        S.pushRegistration = reg;
        return reg.pushManager.getSubscription();
      })
      .then(function (sub) {
        if (sub && Notification.permission === "granted") {
          setPushLabel("关闭通知");
          if (test) { test.hidden = false; }
          syncPushSubscription(sub);
        } else {
          setPushLabel("开启通知");
          if (test) { test.hidden = true; }
        }
      });
    }).catch(function () {});
  }

  function mapPushError(err) {
    if (err && err.name === "AbortError") {
      return new Error("浏览器推送服务拒绝了订阅 —— 常见原因：当前网络无法访问浏览器的推送服务器（国内 Android 的 Chrome/Edge 依赖 Google FCM，可能不可用），或系统/浏览器策略禁用了推送。iOS 请确认已“添加到主屏幕”；也可改用桌面端控制台或配置替代通知通道。");
    }
    if (err && err.name === "NotAllowedError") {
      return new Error("通知权限被拒绝 —— 请在浏览器地址栏/系统设置中允许通知后重试。");
    }
    return err instanceof Error ? err : new Error(String(err));
  }

  // requestPushPermission must be called inside the click gesture. Chromium can
  // keep the returned promise pending (quiet UI, a prompt that was missed, or a
  // previously dismissed dialog), so we also poll Notification.permission and
  // give the user a full minute before failing with actionable guidance.
  function requestPushPermission() {
    if (Notification.permission === "granted" || Notification.permission === "denied") {
      return Promise.resolve(Notification.permission);
    }
    var req = Notification.requestPermission();
    return new Promise(function (resolve, reject) {
      var settled = false;
      var poll = setInterval(function () {
        var p = Notification.permission;
        if (p === "granted" || p === "denied") { finish(p); }
      }, 500);
      var timer = setTimeout(function () {
        fail(new Error("通知权限仍未确认（已等待 60 秒）。请点击地址栏的站点设置（🔒/⚙ 图标 → 通知 → 允许），或检查系统通知设置，然后刷新页面重试。"));
      }, 60000);
      function finish(value) {
        if (settled) { return; }
        settled = true;
        clearInterval(poll);
        clearTimeout(timer);
        resolve(value);
      }
      function fail(err) {
        if (settled) { return; }
        settled = true;
        clearInterval(poll);
        clearTimeout(timer);
        reject(err);
      }
      req.then(finish, function (err) {
        var p = Notification.permission;
        if (p === "granted" || p === "denied") { finish(p); } else { fail(mapPushError(err)); }
      });
    });
  }

  // enablePush must run inside the user gesture: iOS/Safari only shows the
  // permission prompt while the activation is live, so requestPermission is
  // called synchronously, before any async work (config fetch,
  // serviceWorker.ready, ...).
  function enablePush() {
    if (nativeNotificationApi()) { return enableNativeNotifications(); }
    var env = pushEnvironment();
    if (!env.ok) { return Promise.reject(new Error(env.reason)); }
    var permission;
    if ("Notification" in window && Notification.requestPermission) {
      permission = requestPushPermission();
    } else {
      permission = Promise.resolve("granted");
    }
    return permission.then(function (state) {
      if (state !== "granted") {
        throw new Error("通知权限未授予（当前：" + state + "）。请在浏览器/系统设置中允许通知后重试。");
      }
      return loadPushConfig();
    }).then(function (cfg) {
      if (!cfg || !cfg.enabled || !cfg.public_key) {
        throw new Error("网关未启用推送（--push-enabled 或 VAPID 初始化失败）");
      }
      return navigator.serviceWorker.ready.then(function (reg) {
        S.pushRegistration = reg;
        return reg.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: urlBase64ToUint8Array(cfg.public_key),
        });
      });
    }).then(function (sub) {
      if (!sub) { return null; }
      var json = sub.toJSON();
      return G.apiJSON("/api/push/subscriptions", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          endpoint: json.endpoint,
          keys: json.keys || {},
          label: pushLabel(),
        }),
      }).then(function (r) {
        if (!r.res.ok) { throw new Error("网关订阅失败（HTTP " + r.res.status + "）"); }
        return sub;
      });
    }).catch(function (err) {
      throw mapPushError(err);
    });
  }

  function disablePush() {
    if (nativeNotificationApi()) { return disableNativeNotifications(); }
    if (!S.pushRegistration) { return Promise.resolve(); }
    return S.pushRegistration.pushManager.getSubscription().then(function (sub) {
      if (!sub) { return; }
      var endpoint = sub.endpoint;
      return sub.unsubscribe().then(function () {
        return G.apiJSON("/api/push/unsubscribe", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ endpoint: endpoint }),
        });
      });
    });
  }
