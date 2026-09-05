// 审批/问题模态框:展示、决议、建议回答。
// aicli micro web client 前端模块(拆分自 app.js,无构建步骤,由 app.js 入口聚合)。

import { renderMarkdown } from "./markdown.js";
import { sendInput } from "./sessions.js";

var approvalOverlay = document.getElementById("approval-overlay");
var approvalModalTitle = document.getElementById("approval-modal-title");
var approvalPrompt = document.getElementById("approval-prompt");
var approvalDetail = document.getElementById("approval-detail");
var detailToggleBtn = document.getElementById("detail-toggle");
var approvalModalCloseBtn = document.getElementById("approval-modal-close");
var approveBtn = document.getElementById("approve-btn");
var denyBtn = document.getElementById("deny-btn");
var questionSuggestionsEl = document.getElementById("question-suggestions");
var pendingApprovalRequestID = null;
var pendingQuestionID = null;
export function hideApproval() {
  if (approvalOverlay) { approvalOverlay.classList.remove("active"); }
  pendingApprovalRequestID = null;
  pendingQuestionID = null;
  questionSuggestionsEl.innerHTML = "";
  approveBtn.style.display = "";
  denyBtn.style.display = "";
  if (approvalDetail) { approvalDetail.classList.remove("open"); approvalDetail.textContent = ""; }
  if (detailToggleBtn) { detailToggleBtn.textContent = "显示详情"; detailToggleBtn.style.display = ""; }
}

export function showApproval(data) {
  pendingApprovalRequestID = (data && data.request_id) || null;
  pendingQuestionID = null;
  approvalModalTitle.textContent = "待审批工具: " + (data && data.tool_name || "?");
  approvalPrompt.innerHTML = renderMarkdown((data && data.prompt) || "");
  questionSuggestionsEl.innerHTML = "";
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
  // 建议项渲染为可点击按钮，点击即作为回答提交。
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
  if (approvalOverlay) { approvalOverlay.classList.add("active"); }
}

export function sendQuestionAnswer(answer) {
  if (!pendingQuestionID) { return; }
  var qid = pendingQuestionID;
  sendInput({ type: "question_answer", question_id: qid, answer: answer });
  hideApproval();
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

  // ---- 审批模态框：关闭 / 详情展开 ----
  if (approvalModalCloseBtn) {
    approvalModalCloseBtn.addEventListener("click", function () { hideApproval(); });
  }
  // 点击遮罩空白处关闭（但需有活动审批时；question/approval 均适用）
  if (approvalOverlay) {
    approvalOverlay.addEventListener("click", function (e) {
      if (e.target === approvalOverlay) { hideApproval(); }
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
