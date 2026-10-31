  "use strict";

  var el = function (id) { return document.getElementById(id); };

  /* ─────────────────────────── state ─────────────────────────── */

  var lastNodes = [];
  var lastDevices = [];
  var lastMounts = [];
  var mountTokenTarget = null;
  var logTab = "mounts";
  var logFollow = true;
  var logTimer = null;
  var logLoading = false;
  var logMountEntries = [];
  var logAuditEntries = [];
  var sessionMenuSeq = 0;
  var seenAttention = {};
  var attentionSeeded = false;
  var readAttention = {};
  // 已读注意力（点击打开过的待处理）跨刷新保留，短 TTL 自动过期：一小时内
  // 刷新页面不再把处理过的待办重新弹回 inbox；TTL 保证误标的旧条目会自然
  // 回流（服务端 attention 还在时会重新出现在 seenAttention 检测里）。
  var READ_ATTENTION_KEY = "aicli-gw-read-attention";
  var READ_ATTENTION_TTL_MS = 60 * 60 * 1000;
  var presenceTargets = {};
  var sessionFilterText = "";
  // 默认只显示在线会话：离线快照收进「离线」筛选，避免主列表被旧数据占满。
  var sessionFilterKind = "online";
  var sessionSort = "activity";
  var authedState = false;
  // 通知策略（GET/PUT /api/notify/policy）：notifyPolicy 是生效值（原生通知
  // 桥据此过滤），notifyPolicyDraft 是「通知设置」页未保存的编辑副本。
  var notifyPolicy = null;
  var notifyPolicyMeta = null;
  var notifyPolicyDraft = null;
  var notifyPolicyLoaded = false;
  var notifyPolicyLoading = null;
  var notifyPolicyBaseline = "";
  var notifyPolicySaving = false;
  var notifyQuietError = "";
  var notifyQuietTouched = false;
  var notifyQuietInvalidEl = null;
  var notifySessionQuery = "";
  var notifySessionOnlyCustom = false;
  var deviceQuery = "";
  var deviceStatusFilter = "";

  var attentionPriority = { approval: 0, question: 1, error: 2, completed: 3, waiting: 4 };

  var attentionLabels = {
    completed: "已完成",
    approval: "待审批",
    question: "待回答",
    error: "出错",
    waiting: "等待中",
  };

  function attentionLabel(kind) {
    return attentionLabels[kind] || kind;
  }

  // "Pending" means the operator can act now. Completed/waiting remain valid
  // attention states for cards and notifications, but they are not work items.
  function isActionableAttention(kind) {
    return kind === "approval" || kind === "question" || kind === "error";
  }

  /* ─────────────────────────── api ───────────────────────────── */

  // Owner 会话过期/被吊销后，管理面一律返回 401。集中在这里把用户送回登录页：
  // 具体页面（连接器、MCP、通知设置、设备令牌……）各自漏判时不再停在"加载失败 /
  // 空列表 / 连接器面不可用"的状态。hash 不动，登录成功后 applyRoute() 会回到
  // 用户原本在看的视图。
  var authRedirecting = false;

  function handleUnauthorized() {
    if (!authedState || authRedirecting) { return; }
    authRedirecting = true;
    setAuthed(false);
    notify("登录已过期，请重新登录", "error");
    setTimeout(function () { authRedirecting = false; }, 1000);
  }

  function handleAuthResponse(path, res) {
    if (res && res.status === 401 && path !== "/api/auth/login") {
      handleUnauthorized();
    }
    return res;
  }

  function api(path, options) {
    var opts = Object.assign({ credentials: "same-origin" }, options || {});
    return fetch(path, opts).then(function (res) {
      return handleAuthResponse(path, res);
    });
  }

  function apiJSON(path, options) {
    return api(path, options).then(function (res) {
      return res.json().catch(function () { return null; }).then(function (body) {
        return { res: res, body: body };
      });
    });
  }

