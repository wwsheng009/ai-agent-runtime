// 91-chat.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=95";

Object.assign(G, {
  chatPanelSupported: chatPanelSupported,
  chatCanInput: chatCanInput,
  syncChatHeaderActions: syncChatHeaderActions,
  chatQueueRank: chatQueueRank,
  pendingQueue: pendingQueue,
  chatUpdateNextPending: chatUpdateNextPending,
  chatSwitchTo: chatSwitchTo,
  chatFocusOptsFor: chatFocusOptsFor,
  chatOfferNextPending: chatOfferNextPending,
  chatEmptyLive: chatEmptyLive,
  chatSetStatus: chatSetStatus,
  chatNotice: chatNotice,
});

  /* ──────────────── session chat panel (history + live) ───────── */
  //
  // 数据源是节点自身的 micro web client，经网关反代访问：
  //   · 历史  GET  /n/{node}/web/api/screen?format=json&msg_limit=N[&msg_before=..]
  //   · 实时  SSE  /n/{node}/web/api/events
  //   · 发送  POST /n/{node}/web/api/input      {"prompt":"…"}
  // 本面板只做「消息渲染 + 输入」：provider / model / reasoning effort 切换
  // 一律不搬过来（这也是它与节点原版 web 客户端的范围差异）。
  //
  // 三条必须记住的服务端语义：
  //   1) EventSource 只把**显式注册过**的命名事件派发给 listener，漏注册 = 整帧
  //      被浏览器静默丢弃（节点侧用 web_event_sequence_asset_test.go 守这条）。
  //      本面板只注册自己消费的子集，见 CHAT_SSE_EVENTS。
  //   2) 网关 forwarder 有 5 分钟请求超时（agent/forward.go），SSE 会被周期性断开
  //      并由浏览器自动重连；重连后必须重拉一次 screen 快照补齐断线期间丢掉的帧，
  //      否则面板会永久缺失那一段（服务端队列满时同样是静默丢帧）。
  //   3) /web/api/screen 返回的是**节点当前活跃会话**的 transcript。aicli 一个进程
  //      只有一个活跃会话，因此只对父级会话开放入口：子代理行共享父 transcript，
  //      给了按钮只会让人以为在看子会话。

  S.CHAT_PAGE_LIMIT = 40;    // 首屏与实时刷新窗口（条）
  S.CHAT_OLDER_LIMIT = 60;   // 「加载更早消息」每页条数
  S.CHAT_REFRESH_MS = 1000;  // 快照刷新尾随节流：事件风暴下每秒最多一次
  S.CHAT_LIVE_CHUNK_CHARS = 1200; // 纯文本流式按行边界切块；旧块不被重写，
                                    // 屏外块由 content-visibility 跳过布局。
  S.CHAT_LIVE_MD_CHUNK_CHARS = 800; // 实时 Markdown 的稳定前缀按块落盘，限制
                                       // 每帧只需要重绘未完成的尾部。
  S.CHAT_MAX_LIVE = 60000;   // 实时气泡本地累积上限（字符），超限只等快照收敛
  S.CHAT_MAX_WINDOW = 400;   // 本地保留的消息行上限：刷新是「保前缀」合并（见
                               // chatApplyWindow），长跑会话会一直往后长，到顶就从
                               // 头部丢弃并重新开放「加载更早消息」。
  S.CHAT_BUSY_POLL_MS = 2000; // 「会话忙」期间的兜底快照轮询间隔（见 chatSetBusy）
  S.CHAT_SSE_EVENTS = [
    "connected", "heartbeat", "screen_refresh", "session_switched", "error",
    "session_start", "session_end", "session_interrupted",
    "turn_start", "turn_delta", "turn_end",
    "assistant_delta", "assistant_message", "assistant_image_progress", "reasoning_delta",
    "tool_start", "tool_end",
    "approval_requested", "approval_resolved", "question_asked", "question_answered",
    // 状态栏：dynamic_status 驱动动态状态行，model_changed 触发静态段重取。
    "dynamic_status", "model_changed",
  ];

  // chatState 是单例面板状态；null = 面板关闭。字段只由本节读写。
  S.chatState = null;

  // chatPanelSupported：只有能给出 transcript + 实时流的节点才给「会话」入口。
  // runtime-server 是 SPA（自带 /api/runtime/*），未带 --web 的 aicli 不注册
  // screen/events 路由；离线快照没有隧道，一律不给。
  function chatPanelSupported(node, session) {
    if (!node || node.stale) { return false; }
    if (!G.nodeHasWebUI(node)) { return false; }
    if (G.isSubagentSession(session)) { return false; }
    var caps = node.capabilities || [];
    return caps.indexOf("screen") !== -1 && caps.indexOf("events") !== -1;
  }

  function chatCanInput(node) {
    return !!(node && !node.stale && (node.capabilities || []).indexOf("input") !== -1);
  }

  // UX: 桌面端把会话级管理动作集中到右栏会话头，列表卡片只保留摘要。
  function syncChatHeaderActions() {
    var st = S.chatState;
    var menu = S.el("chat-more-menu");
    if (!st || !menu) { return; }
    var node = st.node, session = st.session;
    var canWeb = G.nodeHasWebUI(node);
    var canWindow = G.windowActionsSupported(node) && !G.isSubagentSession(session);
    menu.querySelector("[data-chat-detail]").hidden = false;
    menu.querySelector("[data-chat-web]").hidden = !canWeb;
    menu.querySelector("[data-chat-window-open]").hidden = !canWindow;
    menu.querySelector("[data-chat-window-resume]").hidden = !canWindow;
    menu.querySelector("[data-chat-window-close]").hidden = !canWindow;
    menu.querySelector("[data-chat-remove]").hidden = !node.stale;
  }

  /* ── 「下一条待处理」流转（孤立的审批清理循环）────────────────────
     面板一次只显示一条会话；逐个清掉待审批/待回答时，用户此前必须关闭面板、
     回列表、再找下一条。这两个函数把这条循环收进面板：头部常驻按钮 + 处理完
     一条后的流转 toast，目标都取自同一份 inboxItems（未读、actionable 的
     attention，按注意力
     优先级排序），并显式跳过当前面板会话。 */

  // pendingQueue 返回除当前面板会话外的待处理队列（未读、未离线）。
  // 排序在 inboxItems 的注意力优先级之上做一次稳定分组：能面板内直接完成的
  // 条目（有 screen+events；审批/提问还须有 input）排前，「下一条」因此总是
  // 优先落回面板；只能外部打开的条目仍保留在队列里（计数不撒谎）。
  function chatQueueRank(item) {
    if (!chatPanelSupported(item.node, item.session)) { return 1; }
    var att = item.session.attention;
    if ((att === "approval" || att === "question") && !chatCanInput(item.node)) { return 1; }
    return 0;
  }

  function pendingQueue() {
    var st = S.chatState;
    var items = G.inboxItems(S.lastNodes);
    if (st) {
      items = items.filter(function (item) {
        return !(item.node.node_id === st.node.node_id &&
          item.session.session_id === st.session.session_id);
      });
    }
    // Array.prototype.sort 在现代引擎是稳定的：同组内保持注意力优先级顺序。
    return items.sort(function (a, b) { return chatQueueRank(a) - chatQueueRank(b); });
  }

  // chatUpdateNextPending 刷新头部「下一条待处理」按钮：队列为空时隐藏，
  // 否则显示剩余条数。任何数据刷新（renderInbox）与面板开合都会走到这里。
  function chatUpdateNextPending() {
    var btn = S.el("chat-next-pending");
    if (!btn) { return; }
    var st = S.chatState;
    if (!st) { btn.hidden = true; return; }
    var queue = pendingQueue();
    if (!queue.length) { btn.hidden = true; return; }
    btn.hidden = false;
    var label = S.el("chat-next-pending-label");
    if (label) {
      label.textContent = queue.length > 1
        ? "下一条待处理（" + queue.length + "）"
        : "下一条待处理";
    }
    var tip = "下一条待处理：" + G.sessionLabelOf(queue[0].session) +
      " · " + G.nodeDisplayName(queue[0].node) +
      "（" + G.attentionLabel(queue[0].session.attention) + "）";
    btn.title = tip;
    btn.setAttribute("aria-label", tip);
  }

  // chatSwitchTo 在面板内切换到另一条会话：复用既有一对生命周期函数
  // （close 断流/释放 presence/归还焦点，open 全新连接），不产生第二套状态。
  // 目标节点无法站内打开时落回外部流程且不关闭当前面板（用户可自行决定）。
  function chatSwitchTo(node, session, opts) {
    if (!node || !session) { return; }
    if (!chatPanelSupported(node, session)) {
      G.openSession(node, session);
      return;
    }
    G.closeSessionChat();
    G.openSessionChat(node, session, opts);
  }

  // chatFocusOptsFor 目标带待审批/待回答时，切换后聚焦交互区（与卡片
  // 「审批 / 回答」按钮同一语义）；其余注意力只打开面板不抢焦点。
  function chatFocusOptsFor(session) {
    if (session && (session.attention === "approval" || session.attention === "question")) {
      return { focusInteractive: true };
    }
    return null;
  }

  // chatOfferNextPending 在审批/提问被处理掉的瞬间，把下一条待处理作为
  // 可点击流转呈现——「处理完这条，接着处理下一条」不必回列表找。
  function chatOfferNextPending() {
    var queue = pendingQueue();
    if (!queue.length) { chatUpdateNextPending(); return; }
    var next = queue[0];
    chatUpdateNextPending();
    G.pushToast("已处理 · 下一条待处理：" +
      G.nodeDisplayName(next.node) + " · " + G.sessionLabelOf(next.session) +
      "（" + G.attentionLabel(next.session.attention) + "）", null, {
      label: "打开",
      run: function () {
        chatSwitchTo(next.node, next.session, chatFocusOptsFor(next.session));
      },
    });
  }

  function chatEmptyLive() {
    return { streaming: false, done: false, assistant: "", reasoning: "", tool: "", images: [] };
  }

  function chatSetStatus(text, kind) {
    var dot = S.el("chat-live-status");
    var label = S.el("chat-live-text");
    if (!dot || !label) { return; }
    var st = S.chatState;
    // 帧计数直接写进状态点：SSE 是否真的在投递，是「面板不更新」类问题的第一个
    // 分叉点（连上了但零帧 ≠ 连接失败），运维/排障时一眼可读。
    var suffix = st && st.frames ? " · " + st.frames + " 帧" : "";
    // 只替换文本节点：状态点本身带 ::before 圆点，textContent 会把它一起清掉。
    label.textContent = (text || "") + suffix;
    dot.className = "chat-live-dot" + (kind ? " is-" + kind : "");
    dot.title = st && st.lastEvent
      ? "最近事件：" + st.lastEvent + "（累计 " + st.frames + " 帧）"
      : "尚未收到任何实时事件";
  }

  function chatNotice(text, actions) {
    var box = S.el("chat-notice");
    if (!box) { return; }
    box.innerHTML = "";
    if (!text) { box.hidden = true; return; }
    box.hidden = false;
    var copy = document.createElement("span");
    copy.className = "chat-notice-text";
    copy.textContent = text;
    box.appendChild(copy);
    (actions || []).forEach(function (action) {
      if (!action || !action.label || typeof action.run !== "function") { return; }
      var btn = document.createElement("button");
      btn.type = "button";
      btn.className = "btn ghost sm";
      btn.textContent = action.label;
      btn.addEventListener("click", action.run);
      box.appendChild(btn);
    });
  }
