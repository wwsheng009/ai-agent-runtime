// 打字机流式渲染:turn 增量累积、逐字揭示定时器、流式消息容器管理。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { chatMsgRowHtml, getUserScrolledAway, refreshScreen, screenEl } from "./chat.js";
import { renderMarkdown } from "./markdown.js";
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
  streamMsgEl.innerHTML = parts.length ? parts.join("\n") : '思考中…<span class="tw-cursor"></span>';
  if (!getUserScrolledAway()) {
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
  streamMsgEl.style.display = "block";
  streamMsgEl.innerHTML = '思考中…<span class="tw-cursor"></span>';
  if (!getUserScrolledAway()) {
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
  if (persisted) {
    // 结构化模式（服务端 messages 气泡）：以角色行追加，保留现场；
    // 纯文本回退模式（无 surface 快照）：沿用旧拼接逻辑。
    if (screenEl.querySelector(".msg-row")) {
      if (streamReasoning) {
        screenEl.insertAdjacentHTML("beforeend", chatMsgRowHtml("reasoning", streamReasoning, false));
      }
      if (streamText) {
        screenEl.insertAdjacentHTML("beforeend", chatMsgRowHtml("assistant", streamText, false));
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

export function initStream() {
  // ---- 代码块复制按钮（事件委托，复制 <code> 文本） ----
  if (streamMsgEl) {
    streamMsgEl.addEventListener("click", function (e) {
      var t = e.target;
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
    });
  }

}
