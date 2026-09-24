// 对话区:消息气泡渲染、屏幕刷新、发送/停止按钮状态机、智能滚动与复制。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { hasPendingApproval, hasPendingQuestion, sendQuestionAnswer } from "./approvals.js";
import { normalizeRenderMode, renderMessageBody } from "./markdown.js";
import { filterAllowsRole, filterQueryString, isFilterActive, setFilterChangeHandler, updateFilterMatchInfo } from "./msg-filter.js";
import { loadRuntimeMeta } from "./runtime.js";
import { getInputHistory, getInputHistoryIdx, meshNodeSuffix, sendInput, setInputHistoryIdx } from "./sessions.js";
import { statusEl } from "./sse.js";
import { clearStreamMessage, hideStreamMessage, isStreamActive, isStreamEnded } from "./stream.js";
import { closeShortcutHelpIfOpen, toggleShortcutHelp, toggleTheme } from "./ui.js";
import { apiFetch, esc, showToast } from "./util.js";

export var screenEl = document.getElementById("screen");
export var promptEl = document.getElementById("prompt");
var sendBtn = document.getElementById("send-btn");
export var sendStatusEl = document.getElementById("send-status");
var welcomeEl = document.getElementById("welcome");
var scrollBottomBtn = document.getElementById("scroll-bottom-btn");
var screenCopyBtn = document.getElementById("screen-copy-btn");
var conversationEl = document.getElementById("conversation");

var userScrolledAway = false;    // 用户上滚阅读历史：暂停自动跟随
// 本地乐观回显：已发送但尚未被服务端 screen 快照确认的 user prompt。
// 发送时立即以 pending 气泡追加到对话区；refreshScreen 收到服务端
// messages 后按内容去重确认（服务端已包含则移除，未包含则保留 pending）。
var localPendingPrompts = [];

// ---- 长会话窗口化渲染（懒加载更早消息）----
// 服务端 /web/api/screen?format=json 支持消息窗口（msg_limit/msg_before，绝对
// 索引，左闭右开）。首屏与实时刷新只取最新一页；用户上滚到顶部附近时按
// msg_before 游标向前逐页加载。这样 DOM 节点数、HTML 解析量与 JSON 体积都
// 不再随会话总 turn 数线性增长（长会话下 CPU/内存占用显著下降）。
var MSG_WINDOW_LIMIT = 40;        // 首屏 / 实时刷新：最新 N 条
var MSG_OLDER_PAGE_LIMIT = 40;    // 上滚一次加载的更早 N 条
var SCROLL_LOAD_THRESHOLD = 160;  // 距顶部多少像素触发加载更早消息
var loadedStart = 0;              // 已渲染窗口在服务端消息数组中的起始索引（左闭）
var loadedEnd = 0;                // 已渲染窗口的结束索引（右开）
var serverMessageTotal = 0;       // 服务端最近一次返回的消息总数
var loadingOlder = false;         // 更早消息请求进行中（防重入）
var olderExhausted = true;        // 已到达最早一条（loadedStart == 0）
var screenReqSeq = 0;             // refreshScreen 请求序号：丢弃过期响应
var filterGen = 0;                // 过滤条件代次：变化后作废在途的更早消息分页请求
var convLoadOlderEl = null;       // 顶部「加载更早消息…」指示行（懒创建）

// ---- 发送/停止按钮状态机 ----
// idle        就绪：按钮为「发送」，输入为空时禁用
// posting     POST 已发出，等待队列确认：按钮为「发送中…」禁用（脉冲）
// busy        turn 正在执行：按钮变为「停止」（可点击中断）
// interrupting 停止信号已发出，等待会话复位：按钮为「正在停止…」禁用
var uiState = "idle";
var uiResetTimer = null; // interrupting 超时保护

// setUI 切换状态机并刷新按钮外观与禁用态。
export function setUI(state, statusText) {
  if (state !== "interrupting") { clearTimeout(uiResetTimer); }
  uiState = state;
  if (statusText !== undefined) { sendStatusEl.textContent = statusText; }
  renderButton();
  updateTitle();
  if (state === "interrupting") {
    // 兜底：若 10s 内未收到 session_interrupted / turn_end 复位事件
    //（SSE 断线 / 事件丢失 / 与 turn_end 竞态），强制回到 idle，
    // 避免按钮永久卡在「正在停止…」。
    uiResetTimer = setTimeout(function () {
      if (uiState === "interrupting") { setUI("idle", "已停止"); }
    }, 10000);
  }
}

export function renderButton() {
  switch (uiState) {
    case "posting":
      sendBtn.textContent = "发送中…";
      sendBtn.className = "sending-btn";
      sendBtn.disabled = true;
      break;
    case "busy":
      sendBtn.textContent = "停止";
      sendBtn.className = "stop-btn";
      sendBtn.disabled = false;
      break;
    case "interrupting":
      sendBtn.textContent = "正在停止…";
      sendBtn.className = "stop-btn sending-btn";
      sendBtn.disabled = true;
      break;
    default: // idle
      sendBtn.textContent = "发送";
      sendBtn.className = "";
      sendBtn.disabled = !promptEl.value.trim();
      break;
  }
}
// ---- 结构化对话渲染（role-based 气泡） ----
var MSG_LABELS = {
  user: "你",
  assistant: "aicli",
  reasoning: "推理",
  tool: "工具",
  system: "系统",
  command: "命令",
  diagnostic: "诊断",
  runtime: "事件"
};

// 单条消息 HTML：label（角色）+ body（内容），支持 pending 态。
// index 为消息在服务端消息数组中的绝对索引（窗口化渲染用，写入 data-msg-index）：
// 带索引的行属于「服务端窗口」，上滚加载/增量替换据此定位；pending 行无索引。
// 所有角色都带单条复制图标（抬头行最右，见 messageCopyBtnHtml）：复制内容由
// domMessageText 从该行自己的正文容器提取，控件文字不会混入。
// assistant 行额外带 md|txt 渲染切换（抬头行）与两种正文容器：
//   .msg-text 始终保存转义后的原文（复制/切回 txt 的权威来源）；
//   .msg-md   默认渲染为 Markdown（DEFAULT_RENDER_MODE=md）并于行生成时同步渲染
//            （默认即显状态不可留空），切回 txt/再切 md 时由 applyMessageRenderMode
//             重新渲染（innerHTML 覆盖，反复切换不叠加）。

// assistant 消息的默认渲染方式：Markdown。
//   - 实时流式渲染（stream.js renderStream/renderMarkdown）已默认走 Markdown；
//   - 回放路径（finishStream 追加行 / 服务端 screen 快照 → serverMessagesHtml）
//     也通过 chatMsgRowHtml 走此默认，保证「打字机期间 = 停止后 = 快照回放」三者一致。
//   用户仍可逐条切回纯文本 txt；切换只影响该条消息（data-render-mode 驱动 CSS）。
export var DEFAULT_RENDER_MODE = "md";

function messageRenderToggleHtml() {
  var mdActive = DEFAULT_RENDER_MODE === "md";
  return '<div class="msg-render-toggle" role="group" aria-label="渲染方式">' +
    '<button class="render-mode-btn' + (mdActive ? " active" : "") + '" type="button" data-render-mode-set="md"' +
    ' aria-pressed="' + (mdActive ? "true" : "false") + '" title="Markdown 渲染">md</button>' +
    '<button class="render-mode-btn' + (mdActive ? "" : " active") + '" type="button" data-render-mode-set="txt"' +
    ' aria-pressed="' + (mdActive ? "false" : "true") + '" title="纯文本渲染">txt</button>' +
    '</div>';
}

// 单条消息复制图标（所有角色共用）：抬头行最右的 ⧉ 按钮。
// 只复制本条消息正文（domMessageText），不混入角色标签 / 控件 / 相邻消息。
function messageCopyBtnHtml() {
  return '<button class="msg-copy-btn" type="button" data-copy-msg="1"' +
    ' title="复制本条消息" aria-label="复制本条消息">⧉</button>';
}

// 统一抬头行：角色标签 + 右上角复制图标（assistant / tool 各自在标签后追加专属控件）。
function msgHeadHtml(labelHtml) {
  return '<div class="msg-head">' +
    '<div class="msg-label">' + labelHtml + '</div>' +
    messageCopyBtnHtml() +
    '</div>';
}

export function chatMsgRowHtml(role, content, pending, index) {
  var label = MSG_LABELS[role] || "消息";
  var cls = "msg-row msg-" + role + (pending ? " msg-pending" : "");
  var idxAttr = (typeof index === "number" && index >= 0) ? ' data-msg-index="' + index + '"' : "";
  if (role === "assistant") {
    // 默认 Markdown（DEFAULT_RENDER_MODE=md）：data-render-mode 是渲染方式的唯一
    // 事实来源，CSS 按它在 .msg-text / .msg-md 之间切换显示，不靠内联样式或
    // hidden 属性。.msg-md 于行生成时同步渲染（默认即显，不可留空）；
    // .msg-text 始终保存转义原文，作为切回 txt / 复制的权威来源。
    var asstMode = DEFAULT_RENDER_MODE;
    var mdBody = asstMode === "md" ? renderMessageBody(content, "md") : "";
    return '<div class="' + cls + '"' + idxAttr + ' data-render-mode="' + asstMode + '">' +
      '<div class="msg-head">' +
      '<div class="msg-label">' + esc(label) + '</div>' +
      messageRenderToggleHtml() +
      messageCopyBtnHtml() +
      '</div>' +
      '<div class="msg-body">' +
      '<div class="msg-text">' + esc(content) + '</div>' +
      '<div class="msg-md">' + mdBody + '</div>' +
      '</div>' +
      '</div>';
  }
  if (role === "reasoning") {
    // 推理过程：折叠面板（与流式渲染 #stream-msg .reasoning-block 视觉一致）。
    // 抬头行即 <summary>，复制图标绝对定位在行右上角（不放进 summary，
    // 避免点击复制时连带展开/收起面板）。
    return '<div class="' + cls + '"' + idxAttr + '>' +
      messageCopyBtnHtml() +
      '<details class="reasoning-block">' +
      '<summary>' + esc(label) + '</summary>' +
      '<div class="reasoning-content">' + esc(content) + '</div>' +
      '</details>' +
      '</div>';
  }
  if (role === "tool") {
    // 工具输出：默认折叠（最多显示约 5 行）。
    // 展开/收起控件并入「工具」抬头行（文字 + ▼/▲ 图标），仅内容溢出时可用；
    // 完整文本始终渲染在 DOM 中（CSS 截断），会话复制可获取全文。
    return '<div class="' + cls + '"' + idxAttr + '>' +
      '<div class="msg-head">' +
      '<div class="msg-label tool-toggle" data-tool-toggle="1" role="button" tabindex="0" aria-expanded="false">' +
      '<span class="tool-toggle-text">' + esc(label) + '</span>' +
      '<span class="tool-toggle-action">展开</span>' +
      '<span class="tool-toggle-icon" aria-hidden="true">▼</span>' +
      '</div>' +
      messageCopyBtnHtml() +
      '</div>' +
      '<div class="msg-body">' +
      '<div class="tool-output tool-collapsed" data-tool-output="1">' + esc(content) + '</div>' +
      '</div>' +
      '</div>';
  }
  return '<div class="' + cls + '"' + idxAttr + '>' +
    msgHeadHtml(esc(label)) +
    '<div class="msg-body">' + esc(content) + '</div>' +
    '</div>';
}

// ---- assistant 消息渲染方式切换（md | txt）----

// 读取行的当前渲染方式：读 data-render-mode 属性（assistant 行默认为
// DEFAULT_RENDER_MODE=md）；非 assistant 行 / 缺属性一律归一为 text。
export function getMessageRenderMode(rowEl) {
  if (!rowEl || !rowEl.getAttribute) { return "text"; }
  return normalizeRenderMode(rowEl.getAttribute("data-render-mode"));
}

// 把渲染方式应用到单条 assistant 行：
//   - data-render-mode 驱动 CSS 在 .msg-text / .msg-md 间切换；
//   - 两个按钮同步 active / aria-pressed；
//   - 切到 md 时（re）渲染 Markdown 正文（innerHTML 覆盖，反复切换不叠加）；
//     默认 md 已于 chatMsgRowHtml 同步渲染，此处多次切回切入均以 .msg-text 原文
//     为输入、保证可复切换、不丢内容。
// 正文原文只从 .msg-text 取（渲染后的 .msg-md 不再作为输入，保证可反复切换）。
export function applyMessageRenderMode(rowEl, mode) {
  var next = normalizeRenderMode(mode);
  if (!rowEl || !rowEl.setAttribute) { return next; }
  rowEl.setAttribute("data-render-mode", next);
  var buttons = rowEl.querySelectorAll ? rowEl.querySelectorAll(".render-mode-btn") : [];
  for (var i = 0; i < buttons.length; i++) {
    var btn = buttons[i];
    var btnMode = (btn.getAttribute ? normalizeRenderMode(btn.getAttribute("data-render-mode-set")) : "text");
    var on = btnMode === next;
    if (btn.classList) { if (on) { btn.classList.add("active"); } else { btn.classList.remove("active"); } }
    if (btn.setAttribute) { btn.setAttribute("aria-pressed", on ? "true" : "false"); }
  }
  var mdEl = rowEl.querySelector ? rowEl.querySelector(".msg-md") : null;
  if (mdEl && next === "md") {
    var textEl = rowEl.querySelector ? rowEl.querySelector(".msg-text") : null;
    mdEl.innerHTML = renderMessageBody(textEl ? textEl.textContent : "", "md");
  }
  return next;
}

// md|txt 按钮点击入口（事件委托）：按钮 → 所属消息行 → 应用该按钮代表的渲染方式。
export function toggleMessageRenderMode(btnEl) {
  if (!btnEl || !btnEl.closest) { return "text"; }
  var rowEl = btnEl.closest(".msg-row");
  if (!rowEl) { return "text"; }
  var mode = btnEl.getAttribute ? btnEl.getAttribute("data-render-mode-set") : "text";
  return applyMessageRenderMode(rowEl, mode);
}

// 检测工具输出是否溢出折叠高度：溢出时保留折叠并启用抬头控件；
// 内容未溢出时直接展示全文，并隐藏抬头控件（tool-toggle-off，不可点击）。
// root 省略时扫描整个对话区；已测量过的行（tool-toggle-ready）跳过——窗口化
// 渲染下每次刷新只新增尾部若干行，避免每轮都全量重排/重测量。
export function refreshToolOutputToggles(root) {
  var scope = root || screenEl;
  if (!scope || !scope.querySelectorAll) { return; }
  var outputs = scope.querySelectorAll(".tool-output");
  if (!outputs.length) { return; }
  outputs.forEach(function (el) {
    var rowEl = el.closest ? el.closest(".msg-row") : null;
    if (!rowEl) { return; }
    if (rowEl.classList.contains("tool-toggle-ready")) { return; }
    var labelEl = rowEl.querySelector(".tool-toggle");
    if (!labelEl) { return; }
    if (el.scrollHeight > el.clientHeight) {
      labelEl.classList.remove("tool-toggle-off");
      labelEl.setAttribute("aria-expanded", "false");
    } else {
      labelEl.classList.add("tool-toggle-off");
      el.classList.remove("tool-collapsed");
      el.classList.add("tool-expanded");
      labelEl.setAttribute("aria-expanded", "true");
    }
    // 布局不可用时（面板隐藏 / 尚未排版：clientHeight 为 0）不打标记，
    // 下次刷新会重新测量，避免控件永久停在「未溢出」的错误状态。
    if (el.clientHeight > 0) { rowEl.classList.add("tool-toggle-ready"); }
  });
}

// 切换单条工具行的展开/收起（抬头控件点击/键盘触发）。
// 抬头控件现在位于 .msg-head 内（不再是行的直接子节点），按 .msg-row 向上定位。
function toggleToolOutput(labelEl) {
  if (!labelEl || labelEl.classList.contains("tool-toggle-off")) { return; }
  var rowEl = labelEl.closest ? labelEl.closest(".msg-row") : labelEl.parentNode;
  if (!rowEl) { return; }
  var output = rowEl.querySelector(".tool-output");
  if (!output) { return; }
  var actionEl = labelEl.querySelector(".tool-toggle-action");
  var iconEl = labelEl.querySelector(".tool-toggle-icon");
  var collapsed = output.classList.contains("tool-collapsed");
  if (collapsed) {
    output.classList.remove("tool-collapsed");
    output.classList.add("tool-expanded");
  } else {
    output.classList.remove("tool-expanded");
    output.classList.add("tool-collapsed");
  }
  if (actionEl) { actionEl.textContent = collapsed ? "收起" : "展开"; }
  if (iconEl) { iconEl.textContent = collapsed ? "▲" : "▼"; }
  labelEl.setAttribute("aria-expanded", collapsed ? "true" : "false");
}

// ---- 服务端消息窗口渲染 ----

// 归一化服务端 message_window（缺字段/未分页时按「全量 = 一个窗口」处理）。
export function parseMessageWindow(win, fallbackLen) {
  var len = fallbackLen || 0;
  var out = { start: 0, end: len, total: len, hasMore: false };
  if (!win) { return out; }
  if (typeof win.total === "number" && win.total >= 0) { out.total = win.total; }
  if (typeof win.start === "number" && win.start >= 0) { out.start = win.start; }
  // end 缺失/非法时按「窗口长度 = 本次返回的消息条数」推算，避免 end < start 的空窗口。
  out.end = (typeof win.end === "number" && win.end >= out.start) ? win.end : (out.start + len);
  out.hasMore = (typeof win.has_more === "boolean") ? win.has_more : (out.start > 0);
  return out;
}

// 是否需要整体重建：首次渲染 / 窗口左移 / 窗口收缩（会话切换、上下文压缩、
// 分支回退）/ 新窗口与已渲染区间断开（中间消息缺失，增量会留下空洞）。
// 其余情况只替换尾部（增量 append），避免整块 DOM 重建。
export function shouldRebuildForWindow(prevStart, prevEnd, nextStart, nextEnd) {
  if (prevEnd <= prevStart) { return true; }
  if (nextStart < prevStart) { return true; }
  if (nextEnd < prevEnd) { return true; }
  if (nextStart > prevEnd) { return true; }
  return false;
}

// 服务端消息 → 行 HTML；startIdx 为窗口起始绝对索引（写入 data-msg-index）。
export function serverMessagesHtml(messages, startIdx) {
  var html = "";
  (messages || []).forEach(function (m, i) {
    var role = m.role || "assistant";
    var content = (m.content || "").replace(/\s+$/, "");
    html += chatMsgRowHtml(role, content, false, (startIdx || 0) + i);
  });
  return html;
}

// HTML 字符串 → DocumentFragment（insertBefore 需要节点，不能直接插字符串）。
function htmlToFragment(html) {
  var holder = document.createElement("div");
  holder.innerHTML = html;
  var frag = document.createDocumentFragment();
  while (holder.firstChild) { frag.appendChild(holder.firstChild); }
  return frag;
}

// 第一条 pending 气泡：服务端行必须插在它之前（保持「历史 → 最新 → 待确认」）。
function firstPendingRow() {
  if (!screenEl || !screenEl.querySelector) { return null; }
  return screenEl.querySelector(".msg-row.msg-pending");
}

// 移除绝对索引 >= startIdx 的服务端行（增量尾部替换；pending 行无索引不受影响）。
function removeServerRowsFrom(startIdx) {
  if (!screenEl || !screenEl.querySelectorAll) { return; }
  var rows = screenEl.querySelectorAll("[data-msg-index]");
  rows.forEach(function (el) {
    var idx = parseInt(el.getAttribute("data-msg-index"), 10);
    if (!isNaN(idx) && idx >= startIdx) { el.remove(); }
  });
}

// 未确认的本地 prompt → pending 气泡（服务端窗口内已确认的丢弃）。
function renderPendingPrompts(seenUser) {
  if (!screenEl) { return; }
  localPendingPrompts = localPendingPrompts.filter(function (text) {
    return !seenUser[text];
  });
  var stale = screenEl.querySelectorAll(".msg-row.msg-pending");
  stale.forEach(function (el) { el.remove(); });
  if (!localPendingPrompts.length) { return; }
  // 过滤把「用户消息」排除在外时不渲染本地乐观回显：它下一轮就会被服务端
  // 过滤掉，先显示再消失比不显示更突兀（pendingPrompts 仍保留，清过滤后重现）。
  if (!filterAllowsRole("user")) { return; }
  // 首条消息时先清除占位符 "(empty)"，避免气泡混在占位文本后。
  if (screenEl.textContent === "(empty)" && !screenEl.querySelector(".msg-row")) {
    screenEl.innerHTML = "";
  }
  localPendingPrompts.forEach(function (text) {
    screenEl.insertAdjacentHTML("beforeend", chatMsgRowHtml("user", text, true));
  });
}

// 重置窗口状态（会话切换 / 回退到纯文本快照时）。
function resetConversationWindow() {
  loadedStart = 0;
  loadedEnd = 0;
  serverMessageTotal = 0;
  olderExhausted = true;
  loadingOlder = false;
  syncOlderHint();
}

// 空对话区占位文案：过滤激活时说清是「没有匹配」而不是「没有消息」。
function emptyScreenText() {
  return isFilterActive() ? "没有匹配的消息（已过滤）" : "(empty)";
}

// 过滤条件变化（msg-filter.js 回调）：必须整块重建对话区 —— 过滤改变了索引
// 语义（同一 index 指向不同消息），走增量尾部替换会把未过滤的旧行留在列表里；
// 同时作废在途的更早消息分页请求（游标属于旧条件），并按新条件重取最新一页。
function onFilterChange() {
  filterGen++;
  screenReqSeq++;
  resetConversationWindow();
  userScrolledAway = false; // 新结果集从最新处开始
  // keepStream：过滤切换不该掐掉正在显示的流式气泡（它由流式路径自行维护）。
  refreshScreen(false, { keepStream: isStreamActive() });
}

// 应用服务端窗口：需要时整体重建，否则只替换尾部新增/变更部分。
function applyServerWindow(messages, win) {
  if (!screenEl) { return; }
  var seenUser = {};
  (messages || []).forEach(function (m) {
    if ((m.role || "assistant") === "user") {
      seenUser[(m.content || "").replace(/\s+$/, "")] = true;
    }
  });
  var w = parseMessageWindow(win, (messages || []).length);
  // 过滤激活时 message_window.total 是「匹配条数」，unfiltered_total 是过滤前
  // 总数（见后端 buildChatWebScreenSnapshotForFilter）；未过滤时两者一致。
  var unfilteredTotal = (win && typeof win.unfiltered_total === "number") ? win.unfiltered_total : w.total;
  updateFilterMatchInfo(w.total, unfilteredTotal);
  if (shouldRebuildForWindow(loadedStart, loadedEnd, w.start, w.end)) {
    screenEl.innerHTML = serverMessagesHtml(messages, w.start) || emptyScreenText();
    loadedStart = w.start;
    loadedEnd = w.end;
  } else {
    // 增量尾部替换：窗口右移时保留已渲染的更早部分（用户上滚加载过的内容
    // 不能因实时刷新丢失），因此渲染范围取并集 —— 否则游标前移后再上滚会
    // 把已存在的旧消息重复插入。
    removeServerRowsFrom(w.start);
    screenEl.insertBefore(htmlToFragment(serverMessagesHtml(messages, w.start)), firstPendingRow());
    loadedStart = Math.min(loadedStart, w.start);
    loadedEnd = Math.max(loadedEnd, w.end);
  }
  serverMessageTotal = w.total;
  olderExhausted = loadedStart <= 0;
  syncOlderHint();
  renderPendingPrompts(seenUser);
  refreshToolOutputToggles();
}

// 过滤下 0 命中：服务端返回空 messages（text 随之为空），显式给出「没有匹配」
// 并同步计数，而不是把对话区清成空白。
function applyFilterEmptyResult(win) {
  var w = parseMessageWindow(win, 0);
  var unfilteredTotal = (win && typeof win.unfiltered_total === "number") ? win.unfiltered_total : w.total;
  updateFilterMatchInfo(w.total, unfilteredTotal);
  if (screenEl) { screenEl.textContent = emptyScreenText(); }
  resetConversationWindow();
}

// 顶部提示行：请求进行中显示「加载更早消息…」；空闲但仍存在更早消息时提示
// 「上滚加载更早消息」（否则用户不知道上方还有历史）；已到最早一条时移除。
function updateOlderHint(mode) { // mode: "loading" | "idle" | "none"
  if (!screenEl || !screenEl.insertBefore) { return; }
  if (mode === "none") {
    if (convLoadOlderEl && convLoadOlderEl.parentNode) {
      convLoadOlderEl.parentNode.removeChild(convLoadOlderEl);
    }
    return;
  }
  if (!convLoadOlderEl) {
    convLoadOlderEl = document.createElement("div");
    convLoadOlderEl.className = "conv-load-older";
  }
  convLoadOlderEl.textContent = (mode === "loading") ? "加载更早消息…" : "↑ 上滚加载更早消息";
  if (!convLoadOlderEl.parentNode) {
    screenEl.insertBefore(convLoadOlderEl, screenEl.firstChild);
  }
}

// 依据窗口状态刷新顶部提示行（每次窗口变化后调用）。
function syncOlderHint() {
  if (loadingOlder) { updateOlderHint("loading"); }
  else if (!olderExhausted && loadedStart > 0) { updateOlderHint("idle"); }
  else { updateOlderHint("none"); }
}

// 更早一页插到窗口前部（指示行之后、已有服务端/pending 行之前）。
function insertOlderRows(messages, startIdx) {
  if (!screenEl) { return; }
  var anchor = null;
  if (screenEl.querySelector) {
    anchor = screenEl.querySelector("[data-msg-index]") || firstPendingRow();
  }
  if (!anchor && screenEl.textContent === "(empty)") { screenEl.innerHTML = ""; }
  screenEl.insertBefore(htmlToFragment(serverMessagesHtml(messages, startIdx)), anchor);
}

// 上滚加载更早一页：以 loadedStart 为游标（msg_before），返回后前插并补偿
// scrollTop（插入前后 scrollHeight 差值），保持用户当前阅读位置不跳动。
export function loadOlderMessages() {
  if (!screenEl || loadingOlder || olderExhausted || loadedStart <= 0) { return; }
  loadingOlder = true;
  syncOlderHint();
  var cursor = loadedStart;
  var gen = filterGen; // 过滤条件代次：返回时若已变化，本页作废（游标属于旧条件）
  var url = "/web/api/screen?format=json&msg_limit=" + MSG_OLDER_PAGE_LIMIT +
    "&msg_before=" + cursor + filterQueryString();
  // 带超时（util.js::apiFetch）：连接池被常驻流占满时，上滚分页请求会永久排队，
  // 表现为「一直转圈、没有新内容也没有错误」。
  apiFetch(url, { cache: "no-store" })
    .then(function (res) { return res.ok ? res.json() : null; })
    .then(function (data) {
      if (gen !== filterGen) { loadingOlder = false; return; }
      loadingOlder = false;
      if (!data || !data.available || !Array.isArray(data.messages) || !data.messages.length) {
        olderExhausted = true; // 无更早内容（会话被压缩/清空）：停止继续请求
        syncOlderHint();
        return;
      }
      var w = parseMessageWindow(data.message_window, data.messages.length);
      // 只接受「严格更早、且与已渲染区间相邻」的一页：
      //   w.start >= cursor → 没有更早内容（游标未前进）
      //   w.end !== cursor  → 与已渲染区间重叠（会重复）或断开（会话被压缩/重写）
      // 异常情况一律停止继续上滚，交给下一次 refreshScreen 整体重建。
      if (w.start >= cursor || w.end !== cursor) {
        olderExhausted = true;
        syncOlderHint();
        return;
      }
      var prevTop = conversationEl ? conversationEl.scrollTop : 0;
      var prevHeight = conversationEl ? conversationEl.scrollHeight : 0;
      insertOlderRows(data.messages, w.start);
      loadedStart = w.start;
      if (w.total > 0) { serverMessageTotal = w.total; }
      olderExhausted = w.start <= 0;
      syncOlderHint();
      if (conversationEl) {
        // 前插内容把下方内容整体下移：补偿等量 scrollTop，视野停留在原处。
        conversationEl.scrollTop = prevTop + (conversationEl.scrollHeight - prevHeight);
      }
      refreshToolOutputToggles();
      updateScrollBtn();
      maybeFillViewport(); // 一页仍不足一屏时继续向上取
    })
    .catch(function (err) {
      loadingOlder = false;
      syncOlderHint();
      console.error("older messages fetch failed:", err);
    });
}

// 上滚接近顶部（阈值内）时自动加载更早消息。
function maybeLoadOlderOnScroll() {
  if (!conversationEl) { return; }
  if (conversationEl.scrollTop > SCROLL_LOAD_THRESHOLD) { return; }
  loadOlderMessages();
}

// 窗口内容不足一屏时（无滚动条，用户无法上滚触发加载）继续向上取，
// 直到填满视口或取完最早一条。
function maybeFillViewport() {
  if (!conversationEl || loadingOlder || olderExhausted || loadedStart <= 0) { return; }
  if (conversationEl.scrollHeight <= conversationEl.clientHeight + 8) {
    loadOlderMessages();
  }
}

// ---- 消息复制（整会话 / 单条）----

// 单条消息的正文容器（按角色优先级）：推理 → .reasoning-content；工具 →
// .tool-output（排除抬头 toggle 控件文字）；assistant → .msg-text（原文，
// 切到 md 后 .msg-md 里的代码块「复制」按钮文字不得混进复制结果）；
// 其余 → .msg-body。抬头行（.msg-head）在正文容器之外，角色标签与复制图标
// 天然不入正文，因此单条复制与会话复制共用同一套提取规则。
function messageBodyEl(rowEl) {
  if (!rowEl || !rowEl.querySelector) { return null; }
  return rowEl.querySelector(".reasoning-content")
    || rowEl.querySelector(".tool-output")
    || rowEl.querySelector(".msg-text")
    || rowEl.querySelector(".msg-body");
}

// 单条消息正文文本（单条复制按钮用）：只取本条消息自己的正文容器，
// 不含角色标签、控件文字与相邻消息。推理不加 "[推理] " 前缀——那是
// 整会话复制的语义标注，单条复制保持原文。
export function domMessageText(rowEl) {
  var el = messageBodyEl(rowEl);
  return el ? (el.textContent || "") : "";
}

// 已渲染行 → 文本（按 DOM 顺序 = 对话时序）：整会话复制用。
// 推理内容加 [推理] 前缀保留语义，其余按各自正文容器收集。
function domConversationText() {
  var rows = screenEl.querySelectorAll(".msg-row");
  if (!rows.length) { return screenEl.textContent || ""; }
  var parts = [];
  rows.forEach(function (row) {
    var el = messageBodyEl(row);
    if (!el) { return; }
    var text = el.textContent || "";
    parts.push(row.querySelector(".reasoning-content") ? "[推理] " + text : text);
  });
  return parts.join("\n\n");
}

// 本地窗口是否只覆盖会话的一部分（长会话首屏 / 未一直上滚到最早一条）。
function isWindowPartial() {
  if (loadedStart > 0) { return true; }
  return serverMessageTotal > 0 && loadedEnd < serverMessageTotal;
}

// 复制用文本：窗口完整时按 DOM 收集（保留 [推理] 前缀等既有格式）；
// 窗口只覆盖一部分时改取服务端完整 transcript（不传窗口参数 = 全量），
// 否则「复制」在长会话下只会复制到已加载的一页。取回前仍以 DOM 文本兜底。
function copyConversationText(done) {
  if (!isWindowPartial()) { done(domConversationText()); return; }
  // 过滤激活时同样带上条件：复制的是「当前看到的消息集合」，不是未过滤全量。
  // msg_limit=all 显式请求完整 transcript（服务端缺省已按
  // chatWebMessageWindowDefaultLimit 截断，见 HandleChatWebAPIScreen）；
  // 全量响应可能很大，超时放宽到 60s。
  apiFetch("/web/api/screen?format=json&msg_limit=all" + filterQueryString(), { cache: "no-store" }, 60000)
    .then(function (res) { return res.ok ? res.json() : null; })
    .then(function (data) {
      var full = (data && typeof data.text === "string") ? data.text : "";
      done(full || domConversationText());
    })
    .catch(function () { done(domConversationText()); });
}

// 写剪贴板 + toast 反馈；okMessage 省略时用会话复制的提示语。
// 整会话复制、单条消息复制与流式气泡复制（stream.js）共用。
export function copyTextToClipboard(text, okMessage) {
  if (!text) { showToast("没有可复制的内容", "error"); return; }
  if (!navigator.clipboard) { showToast("复制失败", "error"); return; }
  navigator.clipboard.writeText(text).then(function () {
    showToast(okMessage || "会话内容已复制", "ok");
  }).catch(function () {
    showToast("复制失败", "error");
  });
}

// 整会话复制：窗口占位符 / 空文本静默忽略，不弹错误提示。
function writeClipboardText(text) {
  if (!text || text === "(empty)") { return; }
  copyTextToClipboard(text);
}

// 单条消息复制（事件委托入口）：按钮 → 所属消息行 → 该行正文。
// 复制成功后图标短暂变 ✓（与代码块复制按钮同款反馈）；空内容只弹 toast。
function copyRowMessage(btnEl) {
  var rowEl = (btnEl && btnEl.closest) ? btnEl.closest(".msg-row") : null;
  var text = domMessageText(rowEl);
  if (!text) { showToast("没有可复制的内容", "error"); return; }
  copyTextToClipboard(text, "本条消息已复制");
  if (btnEl.classList) {
    var old = btnEl.textContent;
    btnEl.textContent = "✓";
    setTimeout(function () { btnEl.textContent = old; }, 1200);
  }
}

// 立即追加一条本地 user pending 气泡（乐观回显，不等服务端回合）。
function appendPendingUserPrompt(text) {
  if (!text) { return; }
  localPendingPrompts.push(text);
  // 过滤排除用户消息时不追加乐观回显（与 renderPendingPrompts 同口径）。
  if (screenEl && filterAllowsRole("user")) {
    // 首个消息时先清除占位符 "(empty)"，避免气泡混在占位文本后。
    if (screenEl.textContent === "(empty)" && !screenEl.querySelector(".msg-row")) {
      screenEl.innerHTML = "";
    }
    screenEl.insertAdjacentHTML("beforeend", chatMsgRowHtml("user", text, true));
  }
  scrollToBottom(true);
}

// 发送失败时移除本地 pending 气泡（释放乐观回显）。
export function dropPendingUserPrompt(text) {
  if (!text) { return; }
  localPendingPrompts = localPendingPrompts.filter(function (p) { return p !== text; });
  if (screenEl) {
    var pendingRows = screenEl.querySelectorAll(".msg-row.msg-pending");
    pendingRows.forEach(function (el) {
      var bodyEl = el.querySelector(".msg-body");
      if (bodyEl && bodyEl.textContent.trim() === text) {
        el.remove();
      }
    });
  }
}

// refreshScreen 以服务端权威快照重建对话区。options.keepStream=true 用于
// 流式进行中的节流刷新（见 sse.js 的 tool_end）：只更新对话区，不隐藏
// 正在显示的流式气泡（hideStreamMessage 会终止实时渲染视图）。
export function refreshScreen(forceClear, options) {
  var keepStream = !!(options && options.keepStream);
  var seq = ++screenReqSeq;
  // 只取最新一页（msg_limit）；更早的消息由上滚懒加载（loadOlderMessages）。
  // 过滤条件（roles/q）交给服务端：前端只持有最新一页，客户端过滤会漏掉未加载
  // 的更早消息，也拿不到「匹配 N / 共 M 条」的准确计数（= 搜索结果分页语义）。
  // 带超时（util.js::apiFetch）：这是页面最高频的请求（秒级刷新 + 每个事件触发的
  // 节流刷新）。连接池被占满时它会永久排队，页面就停在旧快照上且没有任何提示；
  // 超时后计入降级状态，sse.js 显示横幅并转入轮询重试（§4.7）。
  apiFetch("/web/api/screen?format=json&msg_limit=" + MSG_WINDOW_LIMIT + filterQueryString(), { cache: "no-store" })
    .then(function (res) { return res.ok ? res.json() : null; })
    .then(function (data) {
      if (seq !== screenReqSeq) { return; } // 过期响应（会话已切换/已有更新请求）丢弃
      loadRuntimeMeta(); // 会话切换/命令执行后同步 provider/model/reasoning 权威值
      if (!data || !data.available) {
        // 无可用屏幕快照（无 surface / 空帧）：保留 screenEl 已有内容
        // （finishStream 已写入流式累积文本），不覆盖为 "(empty)"。
        // 会话切换（新建/恢复）后旧会话内容不应残留：forceClear 时清空。
        if (forceClear) {
          screenEl.textContent = "";
          resetConversationWindow();
          userScrolledAway = false; // 新会话：从最新处开始
          hideStreamMessage();
        }
        scrollToBottom(forceClear);
        updateWelcome();
        return;
      }
      // 结构化消息可用（推荐路径）：角色气泡渲染；否则回退纯文本快照。
      if (Array.isArray(data.messages) && data.messages.length > 0) {
        applyServerWindow(data.messages, data.message_window);
      } else if (isFilterActive()) {
        applyFilterEmptyResult(data.message_window);
      } else {
        screenEl.textContent = data.text || "";
        resetConversationWindow();
      }
      if (!keepStream) {
        hideStreamMessage();
      }
      // 尊重用户滚动位置：若用户上滚阅读历史，不强制拉底（G3）；
      // 会话切换（forceClear）后强制回到最新。
      scrollToBottom(forceClear);
      updateWelcome();
      maybeFillViewport(); // 窗口不足一屏时继续向上补，直到可滚动或取完
    })
    .catch(function (err) {
      // 网络错误：保留现有内容，不覆盖。
      console.error("screen fetch failed:", err);
    });
}

function scrollToBottom(force) {
  // force=true 强制滚底；否则尊重用户滚动位置（上滚阅读历史时不打扰）。
  if (!force && userScrolledAway) { return; }
  var conv = document.getElementById("conversation");
  if (conv) { conv.scrollTop = conv.scrollHeight; }
}

// ---- textarea 自动增高 ----
export function autoGrow() {
  if (!promptEl) { return; }
  // 空输入不留内联高度，交回 CSS 的 min-height：占位提示在窄屏会折行，
  // 照 scrollHeight 记账会把空输入框撑成两行（发送后清空输入同样走这里）。
  if (!promptEl.value) {
    promptEl.style.height = "";
    updateScrollBtn();
    return;
  }
  promptEl.style.height = "auto";
  promptEl.style.height = Math.min(promptEl.scrollHeight, 160) + "px";
  updateScrollBtn();
}

// ---- 页面标题反映运行状态 ----
export function updateTitle() {
  var prefix = "";
  if (uiState === "busy") { prefix = "● "; }
  else if (uiState === "posting") { prefix = "… "; }
  else if (uiState === "interrupting") { prefix = "… "; }
  else if (statusEl && statusEl.classList.contains("disconnected")) { prefix = "✗ "; }
  // 节点后缀（P2 ⑤ / §7.3）：网格可用时拼「· <工作区> · <节点短 id>」，
  // 多窗口并排时能直接分辨窗口归属；网格关闭时 meshNodeSuffix() 为空串。
  document.title = prefix + "aicli micro web client" + meshNodeSuffix();
}

// ---- 欢迎页显示/隐藏 ----
function updateWelcome() {
  if (!welcomeEl) { return; }
  var screenText = (screenEl.textContent || "").trim();
  var show = !isStreamActive() && !isStreamEnded() &&
    (screenText === "" || screenText === "(empty)");
  welcomeEl.style.display = show ? "block" : "none";
  if (screenCopyBtn) {
    screenCopyBtn.style.display = (screenText && screenText !== "(empty)") ? "inline-block" : "none";
  }
}

// ---- 浮动回底按钮显示/隐藏 ----
function updateScrollBtn() {
  if (!scrollBottomBtn || !conversationEl) { return; }
  scrollBottomBtn.style.display = userScrolledAway ? "inline-block" : "none";
  positionScrollBtn();
}

// 锚定到信息流可视区右下角：以信息流下沿为基准反推它与面板下沿的距离，
// 底部区域（动态状态条 / 输入区 / 配置栏）任意高度变化都自动跟随。
// 旧实现只减输入区高度、漏了其下的配置栏，按钮落进输入行里压住发送/结束按钮。
function positionScrollBtn() {
  if (!scrollBottomBtn || !conversationEl) { return; }
  var panel = document.getElementById("tab-main");
  if (!panel) { return; }
  // 面板隐藏（非「对话」页签）时几何全为 0，跳过以免写入错误位置；切回后
  // 由滚动事件或下一次 updateScrollBtn 重新锚定。
  if (!panel.clientHeight) { return; }
  var streamBottom = (conversationEl.offsetTop || 0) + (conversationEl.offsetHeight || 0);
  var below = (panel.clientHeight || 0) - streamBottom;
  scrollBottomBtn.style.bottom = (Math.max(0, below) + 12) + "px";
}

// ---- 跨模块状态访问接口(拆分引入) ----
export function getUiState() { return uiState; }
export function clearPendingPrompts() { localPendingPrompts = []; }
export function getUserScrolledAway() { return userScrolledAway; }

export function initChat() {
  // 过滤条件变化 → 整块重建对话区（msg-filter.js 只维护面板状态与条件）
  setFilterChangeHandler(onFilterChange);

  // 窄屏换短占位提示：桌面文案（含 Shift+Enter）在手机上既无意义，又会在 16px 字号下
  // 折成两行把输入框挤高。跟随视口宽度（旋屏）切换，桌面文案取自 HTML，避免两处漂移。
  if (promptEl && window.matchMedia) {
    var narrowScreen = window.matchMedia("(max-width: 767px)");
    var desktopHint = promptEl.placeholder;
    var syncPromptHint = function () {
      promptEl.placeholder = narrowScreen.matches ? "输入消息，回车发送…" : desktopHint;
    };
    syncPromptHint();
    if (narrowScreen.addEventListener) { narrowScreen.addEventListener("change", syncPromptHint); }
  }

  sendBtn.addEventListener("click", function () {
    // 状态机 dispatch
    if (uiState === "busy") {
      // 执行中：按钮变为「停止」→ 发中断信号
      sendInput({ type: "interrupt" });
      return;
    }
    if (uiState === "posting" || uiState === "interrupting") {
      return; // 等待中，不重复触发
    }
    // idle 状态
    var text = promptEl.value.trim();
    if (!text) { return; }
    if (hasPendingQuestion()) {
      if (!text) { sendStatusEl.textContent = "请输入回答"; return; }
      sendQuestionAnswer(text);
      promptEl.value = "";
      autoGrow();
      return;
    }
    if (hasPendingApproval()) {
      sendStatusEl.textContent = "请使用允许/拒绝按钮";
      return;
    }
    setUI("posting", "发送中…");
    appendPendingUserPrompt(text); // 乐观回显：立即显示用户气泡（发送中态）
    sendInput({ prompt: text });
  });

  promptEl.addEventListener("input", function () {
    autoGrow();
    if (uiState === "idle") { renderButton(); }
  });

  promptEl.addEventListener("keydown", function (e) {
    if (e.key === "ArrowUp" && !e.altKey) {
      // 输入历史：仅当光标位于首行（向上）时才浏览历史，
      // 否则让默认行为移动光标到上一行（多行输入场景）。
      if (uiState === "posting" || uiState === "interrupting") { return; }
      if (promptEl.selectionStart > 0) { return; }
      if (!getInputHistory().length) { return; }
      e.preventDefault();
      if (getInputHistoryIdx() === -1) { setInputHistoryIdx(getInputHistory().length - 1); }
      else if (getInputHistoryIdx() > 0) { setInputHistoryIdx(getInputHistoryIdx() - 1); }
      promptEl.value = getInputHistory()[getInputHistoryIdx()];
      autoGrow();
      return;
    }
    if (e.key === "ArrowDown" && !e.altKey) {
      // 仅当光标位于末行（向下）时才浏览历史
      if (uiState === "posting" || uiState === "interrupting") { return; }
      if (promptEl.selectionStart < promptEl.value.length) { return; }
      if (getInputHistoryIdx() === -1) { return; }
      e.preventDefault();
      if (getInputHistoryIdx() < getInputHistory().length - 1) { setInputHistoryIdx(getInputHistoryIdx() + 1); promptEl.value = getInputHistory()[getInputHistoryIdx()]; }
      else { setInputHistoryIdx(-1); promptEl.value = ""; }
      autoGrow();
      return;
    }
    if (e.key === "Escape") {
      // Esc：若快捷键帮助面板打开则先关闭它
      if (closeShortcutHelpIfOpen()) {
        return;
      }
      // Esc：执行中触发中断（与「停止」按钮同路径）
      if (uiState === "busy") {
        e.preventDefault();
        sendInput({ type: "interrupt" });
      }
      return;
    }
    if ((e.ctrlKey || e.metaKey) && e.key === "/") {
      // Ctrl+/：显示/隐藏快捷键帮助面板
      e.preventDefault();
      toggleShortcutHelp();
      return;
    }
    if ((e.ctrlKey || e.metaKey) && (e.key === "l" || e.key === "L")) {
      // Ctrl+L：切换深色/浅色主题
      e.preventDefault();
      toggleTheme();
      return;
    }
    if ((e.ctrlKey || e.metaKey) && (e.key === "k" || e.key === "K")) {
      // Ctrl+K：清空当前对话视图（仅清除本地屏幕显示，不影响会话历史）
      e.preventDefault();
      screenEl.textContent = "";
      localPendingPrompts = [];
      resetConversationWindow();
      userScrolledAway = false;
      clearStreamMessage();
      updateWelcome();
      scrollToBottom(true);
      return;
    }
    if (e.key !== "Enter" || e.shiftKey) { return; } // Shift+Enter: 换行（textarea 默认）
    if (e.isComposing || e.keyCode === 229) { return; } // IME 组合输入确认：不触发发送
    if (uiState === "posting" || uiState === "interrupting") { return; }
    if (uiState === "busy") {
      // 执行中：Enter 排队下一条消息（不中断当前任务）；按钮点击才触发停止。
      var text = promptEl.value.trim();
      if (!text) { return; }
      if (hasPendingQuestion()) {
        sendQuestionAnswer(text);
        promptEl.value = "";
        autoGrow();
        return;
      }
      if (hasPendingApproval()) {
        sendStatusEl.textContent = "请使用允许/拒绝按钮";
        return;
      }
      appendPendingUserPrompt(text); // 排队期间同样先回显用户气泡
      sendInput({ prompt: text });
      return;
    }
    sendBtn.click();
  });

  // ---- 智能滚动：用户手动上滚时暂停自动跟随 ----
  if (conversationEl) {
    conversationEl.addEventListener("scroll", function () {
      var atBottom = conversationEl.scrollHeight - conversationEl.scrollTop - conversationEl.clientHeight < 40;
      userScrolledAway = !atBottom;
      updateScrollBtn();
      maybeLoadOlderOnScroll(); // 上滚接近顶部：懒加载更早消息
    });
    // ---- 单条消息复制（所有角色抬头行的 ⧉ 图标，真实 <button> 自带键盘激活）----
    // ---- 工具输出展开/收起（「工具」抬头行控件，事件委托 + 键盘可达）----
    // ---- assistant 消息 md|txt 渲染切换（右上角控件，真实 <button> 自带键盘激活）----
    conversationEl.addEventListener("click", function (e) {
      var copyBtn = (e.target && e.target.closest) ? e.target.closest(".msg-copy-btn") : null;
      if (copyBtn) { copyRowMessage(copyBtn); return; }
      var modeBtn = (e.target && e.target.closest) ? e.target.closest("[data-render-mode-set]") : null;
      if (modeBtn) { toggleMessageRenderMode(modeBtn); return; }
      var labelEl = (e.target && e.target.closest) ? e.target.closest(".tool-toggle") : null;
      if (labelEl) { toggleToolOutput(labelEl); }
    });
    conversationEl.addEventListener("keydown", function (e) {
      if (e.key !== "Enter" && e.key !== " " && e.key !== "Spacebar") { return; }
      var labelEl = (e.target && e.target.closest) ? e.target.closest(".tool-toggle") : null;
      if (!labelEl) { return; }
      if (e.preventDefault) { e.preventDefault(); }
      toggleToolOutput(labelEl);
    });
  }
  // 浮动回底按钮
  if (scrollBottomBtn) {
    scrollBottomBtn.addEventListener("click", function () {
      userScrolledAway = false;
      scrollToBottom(true);
      updateScrollBtn();
    });
  }
  // 信息流右下角锚定：多行输入增高 / 窄屏配置栏换行 / 动态状态条显隐都会
  // 改变信息流下沿，必须重算，否则「最新」按钮可能再次压回发送/结束按钮。
  window.addEventListener("resize", positionScrollBtn);
  if (typeof ResizeObserver === "function") {
    var bottomObserver = new ResizeObserver(function () { positionScrollBtn(); });
    ["dynamic-status", "input-row", "cfg-bar"].forEach(function (id) {
      var el = document.getElementById(id);
      if (el) { bottomObserver.observe(el); }
    });
  }
  updateScrollBtn();
  // 会话复制按钮
  if (screenCopyBtn) {
    screenCopyBtn.addEventListener("click", function () {
      // 窗口只覆盖一部分时先取回完整 transcript（见 copyConversationText）。
      copyConversationText(writeClipboardText);
    });
  }
  // 欢迎页示例按钮
  if (welcomeEl) {
    welcomeEl.addEventListener("click", function (e) {
      var chip = e.target.closest(".welcome-chip");
      if (!chip) { return; }
      var text = chip.getAttribute("data-prompt") || "";
      if (!text) { return; }
      promptEl.value = text;
      autoGrow();
      promptEl.focus();
      // 程序赋值不触发 input 事件，需手动刷新按钮可用态
      if (uiState === "idle") { renderButton(); }
      // 自动发送（若当前 idle）
      if (uiState === "idle") {
        sendBtn.click();
      }
    });
  }
}
