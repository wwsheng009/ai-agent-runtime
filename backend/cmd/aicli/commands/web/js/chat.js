// 对话区:消息气泡渲染、屏幕刷新、发送/停止按钮状态机、智能滚动与复制。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { hasPendingApproval, hasPendingQuestion, sendQuestionAnswer } from "./approvals.js";
import { loadRuntimeMeta } from "./runtime.js";
import { getInputHistory, getInputHistoryIdx, sendInput, setInputHistoryIdx } from "./sessions.js";
import { statusEl } from "./sse.js";
import { clearStreamMessage, hideStreamMessage, isStreamActive, isStreamEnded } from "./stream.js";
import { closeShortcutHelpIfOpen, toggleShortcutHelp, toggleTheme } from "./ui.js";
import { esc, showToast } from "./util.js";

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
export function chatMsgRowHtml(role, content, pending) {
  var label = MSG_LABELS[role] || "消息";
  var cls = "msg-row msg-" + role + (pending ? " msg-pending" : "");
  if (role === "reasoning") {
    // 推理过程：折叠面板（与流式渲染 #stream-msg .reasoning-block 视觉一致）
    return '<div class="' + cls + '">' +
      '<details class="reasoning-block">' +
      '<summary>' + esc(label) + '</summary>' +
      '<div class="reasoning-content">' + esc(content) + '</div>' +
      '</details>' +
      '</div>';
  }
  return '<div class="' + cls + '">' +
    '<div class="msg-label">' + esc(label) + '</div>' +
    '<div class="msg-body">' + esc(content) + '</div>' +
    '</div>';
}

// 用服务端 messages 重建对话区；未确认的本地 prompt 保留为 pending 气泡。
function renderConversationMessages(messages) {
  if (!screenEl) { return; }
  var html = "";
  var seenUser = {};
  (messages || []).forEach(function (m) {
    var role = m.role || "assistant";
    var content = (m.content || "").replace(/\s+$/, "");
    if (role === "user") { seenUser[content] = true; }
    html += chatMsgRowHtml(role, content, false);
  });
  // 服务端尚未包含的本地 prompt → pending 气泡（发送中态），保留待确认。
  localPendingPrompts = localPendingPrompts.filter(function (text) {
    if (seenUser[text]) { return false; } // 已被服务端确认
    html += chatMsgRowHtml("user", text, true);
    return true;
  });
  screenEl.innerHTML = html || "(empty)";
}

// 立即追加一条本地 user pending 气泡（乐观回显，不等服务端回合）。
function appendPendingUserPrompt(text) {
  if (!text) { return; }
  localPendingPrompts.push(text);
  if (screenEl) {
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

export function refreshScreen(forceClear) {
  fetch("/web/api/screen?format=json", { cache: "no-store" })
    .then(function (res) { return res.ok ? res.json() : null; })
    .then(function (data) {
      loadRuntimeMeta(); // 会话切换/命令执行后同步 provider/model/reasoning 权威值
      if (!data || !data.available) {
        // 无可用屏幕快照（无 surface / 空帧）：保留 screenEl 已有内容
        // （finishStream 已写入流式累积文本），不覆盖为 "(empty)"。
        // 会话切换（新建/恢复）后旧会话内容不应残留：forceClear 时清空。
        if (forceClear) {
          screenEl.textContent = "";
          hideStreamMessage();
        }
        scrollToBottom();
        updateWelcome();
        return;
      }
      // 结构化消息可用（推荐路径）：角色气泡渲染；否则回退纯文本快照。
      if (Array.isArray(data.messages) && data.messages.length > 0) {
        renderConversationMessages(data.messages);
      } else {
        screenEl.textContent = data.text || "";
      }
      hideStreamMessage();
      // 尊重用户滚动位置：若用户上滚阅读历史，不强制拉底（G3）。
      scrollToBottom();
      updateWelcome();
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
  document.title = prefix + "aicli micro web client";
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
  // 定位在输入区上方，输入区多行增高时自动跟随
  var ir = document.getElementById("input-row");
  if (ir) { scrollBottomBtn.style.bottom = (ir.offsetHeight + 12) + "px"; }
}

// ---- 跨模块状态访问接口(拆分引入) ----
export function getUiState() { return uiState; }
export function clearPendingPrompts() { localPendingPrompts = []; }
export function getUserScrolledAway() { return userScrolledAway; }

export function initChat() {
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
  // 会话复制按钮
  if (screenCopyBtn) {
    screenCopyBtn.addEventListener("click", function () {
      var text = "";
      var rows = screenEl.querySelectorAll(".msg-row");
      if (rows.length) {
        var parts = [];
        rows.forEach(function (row) {
          // 推理内容在折叠面板内（.reasoning-content），加前缀保留语义；
          // 其余角色取 .msg-body 正文。按 DOM 顺序（= 对话时序）收集。
          var reasoningEl = row.querySelector(".reasoning-content");
          if (reasoningEl) {
            parts.push("[推理] " + reasoningEl.textContent);
            return;
          }
          var bodyEl = row.querySelector(".msg-body");
          if (bodyEl) { parts.push(bodyEl.textContent); }
        });
        text = parts.join("\n\n");
      } else {
        text = screenEl.textContent || "";
      }
      if (!text || text === "(empty)") { return; }
      if (!navigator.clipboard) { showToast("复制失败", "error"); return; }
      navigator.clipboard.writeText(text).then(function () {
        showToast("会话内容已复制", "ok");
      }).catch(function () {
        showToast("复制失败", "error");
      });
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
