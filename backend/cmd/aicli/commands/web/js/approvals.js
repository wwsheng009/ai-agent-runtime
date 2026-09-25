// 审批/问题模态框:展示、决议、建议回答、自由回答写入。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { renderMarkdown } from "./markdown.js";
import { sendInput } from "./sessions.js";
import { showToast } from "./util.js";

var approvalOverlay = document.getElementById("approval-overlay");
var approvalModalTitle = document.getElementById("approval-modal-title");
var approvalPrompt = document.getElementById("approval-prompt");
var approvalDetail = document.getElementById("approval-detail");
var detailToggleBtn = document.getElementById("detail-toggle");
var approvalModalCloseBtn = document.getElementById("approval-modal-close");
var approveBtn = document.getElementById("approve-btn");
var denyBtn = document.getElementById("deny-btn");
var questionSuggestionsEl = document.getElementById("question-suggestions");
var questionAnswerRow = document.getElementById("question-answer-row");
var questionAnswerInput = document.getElementById("question-answer-input");
var questionAnswerSubmit = document.getElementById("question-answer-submit");
var questionAnswerHint = document.getElementById("question-answer-hint");
var pendingApprovalRequestID = null;
var pendingQuestionID = null;

// 自由回答输入区的显隐与清空：仅 question 显示（approval 的决议入口是
// 允许/拒绝按钮，不需要文本输入）。
function setQuestionAnswerRowVisible(visible) {
  if (questionAnswerRow) { questionAnswerRow.style.display = visible ? "" : "none"; }
  if (!visible) {
    if (questionAnswerInput) { questionAnswerInput.value = ""; }
    if (questionAnswerHint) { questionAnswerHint.textContent = ""; }
  }
}

export function hideApproval() {
  if (approvalOverlay) { approvalOverlay.classList.remove("active"); }
  pendingApprovalRequestID = null;
  pendingQuestionID = null;
  questionSuggestionsEl.innerHTML = "";
  setQuestionAnswerRowVisible(false);
  approveBtn.style.display = "";
  denyBtn.style.display = "";
  if (approvalDetail) { approvalDetail.classList.remove("open"); approvalDetail.textContent = ""; }
  if (detailToggleBtn) { detailToggleBtn.textContent = "显示详情"; detailToggleBtn.style.display = ""; }
}

// 用户主动收起对话框（✕ / 点击遮罩空白 / 输入区 Esc）。
//   - 审批：模态框内的允许/拒绝是唯一决议入口，收起即放弃本次决议（仍可在
//     终端侧完成决议），保持原有语义；
//   - 提问：回答还有底部输入区（composer）这一入口（chat.js 的
//     hasPendingQuestion 分支会把输入内容当答案提交），因此收起只隐藏对话框、
//     保留 pendingQuestionID —— 否则一关框就永久丢失答案路由，答案再也写不进去。
function dismissApprovalDialog() {
  if (pendingQuestionID) {
    if (approvalOverlay) { approvalOverlay.classList.remove("active"); }
    showToast("已收起提问对话框：可在底部输入区输入回答后回车提交", "ok");
    return;
  }
  hideApproval();
}

export function showApproval(data) {
  pendingApprovalRequestID = (data && data.request_id) || null;
  pendingQuestionID = null;
  approvalModalTitle.textContent = "待审批工具: " + (data && data.tool_name || "?");
  approvalPrompt.innerHTML = renderMarkdown((data && data.prompt) || "");
  questionSuggestionsEl.innerHTML = "";
  setQuestionAnswerRowVisible(false);
  approveBtn.style.display = "";
  denyBtn.style.display = "";
  if (detailToggleBtn) { detailToggleBtn.style.display = ""; }
  if (approvalDetail) {
    var args = (data && data.arguments) || "";
    if (typeof args === "object") { args = JSON.stringify(args, null, 2); }
    approvalDetail.textContent = args || "";
    approvalDetail.classList.remove("open");
  }
  if (detailToggleBtn) { detailToggleBtn.textContent = "显示详情"; }
  if (approvalOverlay) { approvalOverlay.classList.add("active"); }
}

export function showQuestion(data) {
  pendingQuestionID = (data && data.question_id) || null;
  pendingApprovalRequestID = null;
  approvalModalTitle.textContent = "问题: " + (pendingQuestionID || "?");
  approvalPrompt.innerHTML = renderMarkdown((data && data.prompt) || "");
  approveBtn.style.display = "none";
  denyBtn.style.display = "none";
  if (detailToggleBtn) { detailToggleBtn.style.display = "none"; }
  if (approvalDetail) { approvalDetail.classList.remove("open"); approvalDetail.textContent = ""; }
  // 建议项渲染为可点击按钮，点击即作为回答提交（快捷路径）。
  questionSuggestionsEl.innerHTML = "";
  var suggestions = (data && data.suggestions) || [];
  if (typeof suggestions === "string") { suggestions = [suggestions]; }
  suggestions.forEach(function (s) {
    var btn = document.createElement("button");
    btn.className = "suggestion-btn";
    btn.textContent = s;
    btn.addEventListener("click", function () {
      sendQuestionAnswer(s);
    });
    questionSuggestionsEl.appendChild(btn);
  });
  // 自由回答：开放问题（无建议项 / 建议项不匹配）必须能在模态框内直接写入。
  // 遮罩层盖住了浮动 composer 面板，这里不给输入框就等于「答案无法写入」。
  setQuestionAnswerRowVisible(true);
  if (approvalOverlay) { approvalOverlay.classList.add("active"); }
  if (questionAnswerInput) {
    questionAnswerInput.value = "";
    // 焦点必须在遮罩显示之后再给：display:none 子树里的元素不可聚焦，
    // 先 focus 会静默失败（用户还得再点一次输入框）。
    try { questionAnswerInput.focus(); } catch (e) { /* 无焦点能力的环境（测试沙盒）忽略 */ }
  }
}

export function sendQuestionAnswer(answer) {
  if (!pendingQuestionID) { return; }
  var qid = pendingQuestionID;
  sendInput({ type: "question_answer", question_id: qid, answer: answer });
  hideApproval();
}

// 模态框内自由回答提交：空输入只提示不发送（避免把空答案写进会话）。
function submitQuestionAnswerFromInput() {
  if (!pendingQuestionID) { return; }
  var text = questionAnswerInput ? String(questionAnswerInput.value || "").trim() : "";
  if (!text) {
    if (questionAnswerHint) { questionAnswerHint.textContent = "请输入回答"; }
    return;
  }
  if (questionAnswerHint) { questionAnswerHint.textContent = ""; }
  sendQuestionAnswer(text);
}

// 供发送区/快捷键路径判断是否有待处理审批/问题(封装完整可见性条件)。
export function hasPendingQuestion() { return !!pendingQuestionID; }
export function hasPendingApproval() {
  return !!(approvalOverlay && approvalOverlay.classList.contains("active") && pendingApprovalRequestID);
}

export function initApprovals() {
  approveBtn.addEventListener("click", function () {
    if (pendingApprovalRequestID) {
      sendInput({ type: "approval", request_id: pendingApprovalRequestID, allow: true });
      hideApproval();
    }
  });

  denyBtn.addEventListener("click", function () {
    if (pendingApprovalRequestID) {
      sendInput({ type: "approval", request_id: pendingApprovalRequestID, allow: false });
      hideApproval();
    }
  });

  // ---- 提问模态框：自由回答输入区（提交按钮 + Enter）----
  if (questionAnswerSubmit) {
    questionAnswerSubmit.addEventListener("click", function () { submitQuestionAnswerFromInput(); });
  }
  if (questionAnswerInput) {
    questionAnswerInput.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { dismissApprovalDialog(); return; }
      if (e.key !== "Enter" || e.shiftKey) { return; } // Shift+Enter: 换行（textarea 默认）
      if (e.isComposing || e.keyCode === 229) { return; } // IME 组合输入确认：不触发提交
      e.preventDefault(); // 阻止在 textarea 里插入换行
      submitQuestionAnswerFromInput();
    });
  }

  // ---- 审批模态框：关闭 / 详情展开 ----
  if (approvalModalCloseBtn) {
    approvalModalCloseBtn.addEventListener("click", function () { dismissApprovalDialog(); });
  }
  // 点击遮罩空白处关闭（审批=放弃本次决议；提问=仅收起，回答入口仍在 composer）
  if (approvalOverlay) {
    approvalOverlay.addEventListener("click", function (e) {
      if (e.target === approvalOverlay) { dismissApprovalDialog(); }
    });
  }
  if (detailToggleBtn) {
    detailToggleBtn.addEventListener("click", function () {
      if (!approvalDetail) { return; }
      var open = approvalDetail.classList.toggle("open");
      detailToggleBtn.textContent = open ? "隐藏详情" : "显示详情";
    });
  }

}
