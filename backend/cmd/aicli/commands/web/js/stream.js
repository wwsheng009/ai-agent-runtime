// 打字机流式渲染:turn 增量累积、逐字揭示定时器、流式消息容器管理。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { appendLocalConversationRow, copyTextToClipboard, getUserScrolledAway, refreshScreen, screenEl } from "./chat.js";
import { renderMarkdown } from "./markdown.js";
import { filterAllowsRole, isFilterActive } from "./msg-filter.js";
import { esc, showToast } from "./util.js";

// ---- 打字机状态（实时逐字揭示） ----
var streamActive = false;          // turn_start → turn_end
var streamEnded = false;           // turn_end 已到达，等待打字机揭示完成
var streamReasoning = "";          // 推理文本（完整累积）
var streamText = "";               // 助手文本（完整累积）
var streamTool = "";               // 当前工具指示
var streamImages = [];             // 已渲染图像（assistant_image_progress，按 src 去重）
var streamRevealed = 0;            // streamText 已显示的字符数
var streamReasoningRevealed = 0;   // streamReasoning 已显示行数
var typeTimer = null;              // 打字机定时器句柄
var streamMsgEl = null;            // 流式消息容器（screen 下方追加，信息流模式）
var TYPE_SPEED = 20;               // 每字符间隔（毫秒），越小越快
var TYPE_CHARS_PER_TICK = 1;       // 每 tick 揭示字符数

// 流式气泡是否该显示：过滤把「助手 / 推理 / 工具」全排除时（例如只看用户消息），
// 实时气泡不属于任何可见类别，直接不显示——它是混合文本，无法按角色拆分过滤。
function streamVisibleUnderFilter() {
  if (!isFilterActive()) { return true; }
  return filterAllowsRole("assistant") || filterAllowsRole("reasoning") || filterAllowsRole("tool");
}

export function startTypeTimer() {
  if (typeTimer) return;
  typeTimer = setInterval(function () {
    var changed = false;
    // 推理文本：逐段揭示
    if (streamReasoningRevealed < streamReasoning.length) {
      var rRemaining = streamReasoning.length - streamReasoningRevealed;
      // 大段推理提速：保证最多约 5 秒内揭示完
      streamReasoningRevealed = Math.min(streamReasoningRevealed + Math.max(10, Math.ceil(rRemaining / 250)), streamReasoning.length);
      changed = true;
    }
    // 助手文本：逐字揭示
    if (streamRevealed < streamText.length) {
      var tRemaining = streamText.length - streamRevealed;
      // 大文本提速：保证最多约 5 秒内揭示完
      var perTick = Math.max(TYPE_CHARS_PER_TICK, Math.ceil(tRemaining / 250));
      streamRevealed = Math.min(streamRevealed + perTick, streamText.length);
      changed = true;
    }
    if (changed) renderStream();
    // 队列排空后停止定时器（节省资源）；若 turn 已结束则收尾
    if (streamRevealed >= streamText.length && streamReasoningRevealed >= streamReasoning.length) {
      stopTypeTimer();
      if (streamEnded) { finishStream(); }
    }
  }, TYPE_SPEED);
}

function stopTypeTimer() {
  if (typeTimer) {
    clearInterval(typeTimer);
    typeTimer = null;
  }
}

// 流式气泡的复制图标（右上角）。类名与对话区单条复制的 .msg-copy-btn 分开：
// 两处委托都挂在 #conversation 上，同名前缀会让两个处理器重复响应同一次点击。
function streamCopyBtnHtml() {
  return '<button class="stream-copy-btn" type="button" data-copy-stream="1"' +
    ' title="复制本条消息" aria-label="复制本条消息">⧉</button>';
}

// 复制流式气泡内容：已累积的完整助手文本（streamText），尚未产生助手文本时
// 退回推理文本。复制的是完整累积内容，不是打字机当前已揭示的部分——气泡每
// tick 重建，来不及做「✓ 已复制」的行内反馈，故只用 toast。
function onCopyStreamClick() {
  copyTextToClipboard(streamText || streamReasoning, "本条消息已复制");
}

export function renderStream() {
  var parts = [];
  // 图像预览（assistant_image_progress）
  streamImages.forEach(function (src) {
    parts.push('<div class="image-preview"><img src="' + esc(src) + '" alt="生成图像" loading="lazy"></div>');
  });
  if (streamReasoningRevealed > 0) {
    var reasoningHtml = renderMarkdown(streamReasoning.substring(0, streamReasoningRevealed));
    parts.push('<details class="reasoning-block" open>');
    parts.push('<summary>推理过程</summary>');
    parts.push('<div class="reasoning-content">' + reasoningHtml + '</div>');
    parts.push('</details>');
  }
  if (streamTool) {
    parts.push('<div class="stream-tool">' + esc(streamTool) + '</div>');
  }
  var displayed = streamText.substring(0, streamRevealed);
  if (displayed) {
    var mdHtml = renderMarkdown(displayed);
    parts.push('<div class="stream-text">' + mdHtml + '</div>');
    // 光标闪烁（仅在未揭示完时）
    if (streamRevealed < streamText.length) {
      parts.push('<span class="tw-cursor"></span>');
    }
  }
  if (!streamMsgEl) return;
  if (parts.length) {
    parts.unshift('<div class="stream-head">' + streamCopyBtnHtml() + '</div>');
  }
  streamMsgEl.innerHTML = parts.length ? parts.join("\n") : '思考中…<span class="tw-cursor"></span>';
  // 过滤下气泡可能被隐藏（streamVisibleUnderFilter）：隐藏时不再跟随滚动。
  if (streamMsgEl.style.display !== "none" && !getUserScrolledAway()) {
    streamMsgEl.scrollIntoView(false);
  }
}

export function beginStream() {
  streamActive = true;
  streamEnded = false;
  streamReasoning = "";
  streamReasoningRevealed = 0;
  streamText = "";
  streamRevealed = 0;
  streamTool = "";
  streamImages = [];
  // 确保 streamMsgEl 引用有效，并显示在 screen 下方（信息流模式：不覆盖历史）
  if (!streamMsgEl) {
    streamMsgEl = document.getElementById("stream-msg");
  }
  // 过滤把流式内容所属角色全排除时不显示实时气泡（回合结束后的权威快照仍按
  // 过滤条件渲染）。
  var visible = streamVisibleUnderFilter();
  streamMsgEl.style.display = visible ? "block" : "none";
  streamMsgEl.innerHTML = '思考中…<span class="tw-cursor"></span>';
  if (visible && !getUserScrolledAway()) {
    streamMsgEl.scrollIntoView(false);
  }
  // 并行加载对话历史（异步，不影响流式消息渲染）
  refreshScreen();
  startTypeTimer();
}

// 打字机揭示完成后收尾：隐藏流式消息，刷新权威屏幕内容（含新回合）。
function finishStream() {
  streamActive = false;
  streamEnded = false;
  // 把流式累积文本持久化到屏幕区，作为无 surface 时的会话历史。
  // 有 surface 时 refreshScreen() 会覆盖为权威屏幕快照（含全部历史）。
  // 无 surface 时追加累积，避免多轮对话互相覆盖。
  var persisted = "";
  if (streamReasoning) {
    persisted += "[reasoning]\n" + streamReasoning + "\n";
  }
  if (streamText) {
    persisted += streamText;
  }
  // 过滤激活时不把流式累积文本落到对话区：这些行不带索引、也不经过服务端过滤，
  // 会与过滤后的权威快照重复或矛盾（下面的 refreshScreen 按条件渲染）。
  if (persisted && !isFilterActive()) {
    // 结构化模式（服务端 messages 气泡）：以角色行追加，保留现场；
    // 纯文本回退模式（无 surface 快照）：沿用旧拼接逻辑。
    if (screenEl.querySelector(".msg-row")) {
      // 本地兜底行（data-msg-local=stream）：随后 refreshScreen 的权威窗口
      // 一旦覆盖同一内容就会把它移除（见 chat.js 的对账），因此不会与服务端
      // 行重复；窗口不可用（无 surface / 请求失败）时它保证内容不丢。
      if (streamReasoning) {
        appendLocalConversationRow("reasoning", streamReasoning, "stream");
      }
      if (streamText) {
        appendLocalConversationRow("assistant", streamText, "stream");
      }
    } else {
      var existing = screenEl.textContent;
      var prefix = (existing && existing !== "(empty)") ? existing + "\n\n" : "";
      screenEl.textContent = prefix + persisted;
    }
  }
  if (streamMsgEl) { streamMsgEl.style.display = "none"; }
  refreshScreen();
}

export function endStream() {
  // turn_end：不再有增量。不强制揭示——保留已缓冲文本，
  // 由打字机定时器继续逐字揭示完剩余部分后调用 finishStream()。
  streamEnded = true;
  if (!typeTimer) {
    if (streamRevealed >= streamText.length && streamReasoningRevealed >= streamReasoning.length) {
      // 无待揭示内容，直接收尾
      finishStream();
    } else {
      // 异常兜底：有未揭示内容但定时器未运行，重新启动它
      startTypeTimer();
    }
  }
  // 定时器运行中：由定时器 tick 检测揭示完成后调用 finishStream()
}

// ---- 跨模块状态访问接口(拆分引入:可变流式状态不跨模块直读直写) ----
export function isStreamActive() { return streamActive; }
export function isStreamEnded() { return streamEnded; }
// 实时气泡当前承载的内容：供 chat.js 的窗口对账判定"权威行是否与气泡同源"
// （同源行让位给气泡，避免同一段内容被渲染两次）。
export function getLiveStreamState() {
  return { active: streamActive, ended: streamEnded, text: streamText, reasoning: streamReasoning };
}
export function appendStreamReasoning(text) { streamReasoning += text; }
export function appendStreamText(text) { streamText += text; }
export function setStreamText(text) { streamText = text || streamText; }
export function setStreamTool(text) { streamTool = text; }
export function addStreamImage(src) {
  if (streamImages.indexOf(src) !== -1) { return false; }
  streamImages.push(src);
  return true;
}
export function hideStreamMessage() {
  if (streamMsgEl) { streamMsgEl.style.display = "none"; }
}
export function clearStreamMessage() {
  if (streamMsgEl) { streamMsgEl.innerHTML = ""; }
}

// 会话切换 / 会话结束：丢弃本回合的流式累积与气泡。
// 不这么做的话，旧会话的累积文本会在新会话的第一帧刷新里成为"实时气泡同源"
// 的判定输入（chat.js 的 suppressRowsCoveredByLiveStream），并被 finishStream
// 落成新会话的本地兜底行——两种都是跨会话的串味/重复渲染。
export function resetStreamState() {
  streamActive = false;
  streamEnded = false;
  streamReasoning = "";
  streamReasoningRevealed = 0;
  streamText = "";
  streamRevealed = 0;
  streamTool = "";
  streamImages = [];
  stopTypeTimer();
  hideStreamMessage();
}

// 代码块复制按钮（事件委托，复制 <code> 文本）。
// 流式气泡与对话区 assistant 气泡（切到 md 后的 .msg-md）共用同一处理。
function onCopyCodeClick(e) {
  var t = e.target;
  var streamBtn = (t && t.closest) ? t.closest(".stream-copy-btn") : null;
  if (streamBtn) { onCopyStreamClick(); return; }
  var btn = (t && t.closest) ? t.closest(".copy-code-btn") : null;
  if (!btn) { return; }
  var codeEl = btn.parentNode ? btn.parentNode.querySelector("code") : null;
  if (!codeEl) { return; }
  var codeText = codeEl.textContent || "";
  if (!navigator.clipboard) {
    showToast("复制失败（浏览器不支持剪贴板）", "error");
    return;
  }
  navigator.clipboard.writeText(codeText).then(function () {
    var old = btn.textContent;
    btn.textContent = "✓ 已复制";
    setTimeout(function () { btn.textContent = old; }, 1500);
  }).catch(function () {
    showToast("复制失败", "error");
  });
}

export function initStream() {
  // 委托挂在 #conversation（#stream-msg 与 #screen 都是它的子节点）：一处覆盖
  // 流式气泡与 assistant 切到 md 后的代码块。该元素在 init 时已存在，不再依赖
  // beginStream 才惰性取得的 streamMsgEl（否则委托注册不到任何元素上）。
  var root = document.getElementById("conversation") || streamMsgEl || screenEl;
  if (root) { root.addEventListener("click", onCopyCodeClick); }
}
