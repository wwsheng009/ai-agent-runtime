// 92-chat-approvals.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=100";

Object.assign(G, {
  chatInteractiveHint: chatInteractiveHint,
  chatRenderApprovalArgs: chatRenderApprovalArgs,
  chatRenderInteractive: chatRenderInteractive,
  chatPostInput: chatPostInput,
  chatSendApprovalDecision: chatSendApprovalDecision,
  chatSendQuestionAnswer: chatSendQuestionAnswer,
  chatToggleInteractiveDetail: chatToggleInteractiveDetail,
  chatPlaceholder: chatPlaceholder,
  chatShowHint: chatShowHint,
  chatSyncComposer: chatSyncComposer,
  chatSetSending: chatSetSending,
  chatSyncSendState: chatSyncSendState,
});

  /* ── 审批 / 提问交互区（与节点原版 Web 客户端同契约）─────────────
     数据来自节点 SSE：connected.pending_approval / pending_question、
     approval_requested、question_asked；决议走 POST /web/api/input：
       approval        {type:"approval", request_id, allow}
       question_answer {type:"question_answer", question_id, answer}
     这样卡片上的「审批 / 回答」不必跳转到原版 Web。 */

  function chatInteractiveHint(text, kind) {
    var hint = S.el("ci-answer-hint");
    if (!hint) { return; }
    hint.textContent = text || "";
    hint.hidden = !text;
    hint.className = "chat-hint" + (kind ? " is-" + kind : "");
  }

  // chatRenderApprovalArgs 把审批参数渲染成「键: 值」行，而不是一整坨缩进 JSON：
  // 手机上的审批决策要一眼看清每个参数是什么；嵌套值保持 pretty-print，超长值
  // 截到 400 字符（悬停看全文）。非对象/解析失败的参数保持原文，绝不吞信息。
  function chatRenderApprovalArgs(target, rawArgs) {
    if (!target) { return; }
    target.textContent = "";
    var value = rawArgs;
    if (typeof value === "string") {
      try { value = JSON.parse(value); } catch (err) { value = rawArgs; }
    }
    if (!value || typeof value !== "object" || Array.isArray(value)) {
      G.setText(target, typeof value === "string" ? value : (JSON.stringify(value, null, 2) || ""));
      return;
    }
    Object.keys(value).forEach(function (key) {
      var row = document.createElement("div");
      row.className = "ci-arg-row";
      var k = document.createElement("span");
      k.className = "ci-arg-key";
      k.textContent = key;
      var v = document.createElement("span");
      v.className = "ci-arg-val";
      var item = value[key];
      var text = typeof item === "string" ? item : JSON.stringify(item, null, 2);
      if (text == null) { text = String(item); }
      if (text.length > 400) {
        v.textContent = text.slice(0, 400) + "…";
        v.title = text;
      } else {
        v.textContent = text;
      }
      row.appendChild(k);
      row.appendChild(v);
      target.appendChild(row);
    });
  }

  function chatRenderInteractive() {
    var box = S.el("chat-interactive");
    if (!box) { return; }
    var st = S.chatState;
    if (!st || (!st.pendingApproval && !st.pendingQuestion)) {
      box.hidden = true;
      return;
    }
    var approval = st.pendingApproval;
    var question = st.pendingQuestion;
    var kindEl = S.el("ci-kind");
    var titleEl = S.el("ci-title");
    var promptEl = S.el("ci-prompt");
    var detail = S.el("ci-detail");
    var toggle = S.el("ci-detail-toggle");
    var actions = S.el("ci-actions");
    var suggestions = S.el("ci-suggestions");
    var form = S.el("ci-answer-form");
    var answerInput = S.el("ci-answer-input");
    if (approval) {
      G.setText(kindEl, "待审批工具");
      G.setText(titleEl, approval.tool_name || "?");
      G.setText(promptEl, approval.prompt || "");
      chatRenderApprovalArgs(detail, approval.arguments);
      var args = approval.arguments;
      if (typeof args === "string") {
        try { args = JSON.parse(args); } catch (err) { /* keep raw string */ }
      }
      var hasArgs = !!(args && (typeof args !== "object" || Object.keys(args).length));
      if (detail) { detail.hidden = !hasArgs; }
      // 参数默认可见（审批决策的直接依据），标签跟随当前可见性——旧实现固定
      // 写「显示详情」而内容已展开，第一次点击标签与动作相反。
      if (toggle) { toggle.hidden = !hasArgs; toggle.textContent = (detail && detail.hidden) ? "显示详情" : "隐藏详情"; }
      if (actions) { actions.hidden = false; }
      if (S.el("ci-approve")) { S.el("ci-approve").disabled = false; }
      if (S.el("ci-deny")) { S.el("ci-deny").disabled = false; }
      if (suggestions) { suggestions.hidden = true; suggestions.innerHTML = ""; }
      if (form) { form.hidden = true; }
      chatInteractiveHint("", "");
    } else {
      G.setText(kindEl, "提问");
      // 交互条不暴露 question_id：id 只走数据面（question_answer 用它回执），
      // 界面保留「提问」+ 题面 + 建议/自由输入即可。
      G.setText(titleEl, "");
      G.setText(promptEl, question.prompt || "");
      if (detail) { detail.hidden = true; detail.textContent = ""; }
      if (toggle) { toggle.hidden = true; }
      if (actions) { actions.hidden = true; }
      var list = (question.suggestions || []);
      if (typeof list === "string") { list = [list]; }
      if (suggestions) {
        suggestions.innerHTML = "";
        suggestions.hidden = list.length === 0;
        list.forEach(function (text) {
          var btn = document.createElement("button");
          btn.type = "button";
          btn.className = "btn ghost sm ci-suggestion";
          btn.textContent = text;
          btn.addEventListener("click", function () { chatSendQuestionAnswer(text); });
          suggestions.appendChild(btn);
        });
      }
      if (form) { form.hidden = false; }
      if (answerInput && st.focusInteractive) {
        try { answerInput.focus(); } catch (err) { /* focus unsupported */ }
      }
      chatInteractiveHint("", "");
    }
    st.focusInteractive = false;
    box.hidden = false;
  }

  function chatPostInput(payload) {
    var st = S.chatState;
    if (!st) { return Promise.reject(new Error("面板已关闭")); }
    return G.api("/n/" + encodeURIComponent(st.node.node_id) + "/web/api/input", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (body) {
        return { ok: res.ok, status: res.status, body: body || {} };
      });
    });
  }

  function chatSendApprovalDecision(allow) {
    var st = S.chatState;
    if (!st || !st.pendingApproval) { return; }
    var requestID = st.pendingApproval.request_id;
    if (!requestID) { G.notify("审批请求缺少 request_id，无法提交", "error"); return; }
    var approveBtn = S.el("ci-approve");
    var denyBtn = S.el("ci-deny");
    if (approveBtn) { approveBtn.disabled = true; }
    if (denyBtn) { denyBtn.disabled = true; }
    chatInteractiveHint("已提交，等待节点确认…", "");
    chatPostInput({ type: "approval", request_id: requestID, allow: !!allow }).then(function (r) {
      var body = r.body || {};
      if (body.status === "resolved") {
        G.chatNotice("");
        chatInteractiveHint("", "");
      } else {
        chatInteractiveHint(body.reason || ("提交失败（" + (body.status || r.status) + "）"), "error");
        if (approveBtn) { approveBtn.disabled = false; }
        if (denyBtn) { denyBtn.disabled = false; }
      }
    }).catch(function (err) {
      chatInteractiveHint("提交失败：" + err, "error");
      if (approveBtn) { approveBtn.disabled = false; }
      if (denyBtn) { denyBtn.disabled = false; }
    });
  }

  function chatSendQuestionAnswer(answer) {
    var st = S.chatState;
    if (!st || !st.pendingQuestion) { return; }
    var text = String(answer == null ? "" : answer).trim();
    if (!text) { chatInteractiveHint("请输入回答", "error"); return; }
    var questionID = st.pendingQuestion.question_id;
    if (!questionID) { G.notify("提问缺少 question_id，无法提交", "error"); return; }
    var submit = S.el("ci-answer-submit");
    if (submit) { submit.disabled = true; }
    chatInteractiveHint("已提交，等待节点确认…", "");
    chatPostInput({ type: "question_answer", question_id: questionID, answer: text }).then(function (r) {
      var body = r.body || {};
      if (body.status === "resolved") {
        chatInteractiveHint("", "");
        var input = S.el("ci-answer-input");
        if (input) { input.value = ""; }
      } else {
        chatInteractiveHint(body.reason || ("提交失败（" + (body.status || r.status) + "）"), "error");
      }
    }).catch(function (err) {
      chatInteractiveHint("提交失败：" + err, "error");
    }).finally(function () {
      if (submit) { submit.disabled = false; }
    });
  }

  function chatToggleInteractiveDetail() {
    var detail = S.el("ci-detail");
    var toggle = S.el("ci-detail-toggle");
    if (!detail || !toggle) { return; }
    detail.hidden = !detail.hidden;
    toggle.textContent = detail.hidden ? "显示详情" : "隐藏详情";
  }

  (function bindChatInteractive() {
    var approve = S.el("ci-approve");
    if (approve) { approve.addEventListener("click", function () { chatSendApprovalDecision(true); }); }
    var deny = S.el("ci-deny");
    if (deny) { deny.addEventListener("click", function () { chatSendApprovalDecision(false); }); }
    var toggle = S.el("ci-detail-toggle");
    if (toggle) { toggle.addEventListener("click", chatToggleInteractiveDetail); }
    var form = S.el("ci-answer-form");
    if (form) {
      form.addEventListener("submit", function (e) {
        e.preventDefault();
        var input = S.el("ci-answer-input");
        chatSendQuestionAnswer(input ? input.value : "");
      });
    }
    var answer = S.el("ci-answer-input");
    if (answer) {
      answer.addEventListener("keydown", function (e) {
        if (e.key !== "Enter" || e.shiftKey) { return; }
        if (e.isComposing || e.keyCode === 229) { return; }
        e.preventDefault();
        chatSendQuestionAnswer(answer.value);
      });
    }
  })();

  // chatPlaceholder 在列表为空时给一句空状态文案（复用同一个节点，避免堆叠）。
  function chatPlaceholder(text) {
    var host = S.el("chat-list");
    var node = S.el("chat-placeholder");
    if (!host) { return; }
    if (!node) {
      node = document.createElement("p");
      node.className = "chat-placeholder";
      node.id = "chat-placeholder";
      host.appendChild(node);
    }
    G.setText(node, text);
  }

  function chatShowHint(text) {
    var hint = S.el("chat-hint");
    if (!hint) { return; }
    hint.textContent = text || "";
    hint.hidden = !text;
  }

  // chatSyncComposer 是发送/停止按钮的唯一状态机：
  //   idle（纸飞机）→ posting（转圈）→ busy（停止方块）→ interrupting（转圈，禁用）。
  // busy 来自 registry/SSE 的会话忙闲，不靠“输入框里有没有字”猜测。
  function chatSyncComposer() {
    var st = S.chatState;
    var btn = S.el("chat-send");
    var input = S.el("chat-input");
    if (!btn) { return; }
    var busy = !!(st && st.busy);
    var interrupting = !!(st && st.interrupting);
    var sending = !!(st && st.sending);
    var stale = !!(st && st.node && st.node.stale);
    var canInput = !!(st && G.chatCanInput(st.node));
    var hasText = !!(input && String(input.value || "").trim());
    var label = "发送";
    var disabled = false;
    btn.classList.toggle("is-stop", busy || interrupting);
    btn.classList.toggle("is-sending", sending || interrupting);
    btn.removeAttribute("aria-busy");
    if (interrupting) {
      label = "正在停止…";
      disabled = true;
      btn.setAttribute("aria-busy", "true");
    } else if (sending) {
      label = "发送中…";
      disabled = true;
      btn.setAttribute("aria-busy", "true");
    } else if (busy) {
      label = "停止";
      disabled = stale || !canInput;
    } else {
      disabled = !hasText || stale || !canInput;
    }
    btn.disabled = disabled;
    btn.setAttribute("aria-label", label);
    btn.title = label === "发送" ? "发送（Enter）" : label;
    G.setText(btn.querySelector(".chat-send-label"), label);
    if (input) {
      input.placeholder = interrupting
        ? "正在停止当前任务…"
        : (busy
          ? "当前任务运行中，Enter 排队下一条…（Shift+Enter 换行）"
          : "回复该会话…（Enter 发送，Shift+Enter 换行）");
    }
  }

  // chatSetSending 保留原调用点语义，实际由统一状态机决定按钮外观。
  function chatSetSending(sending) {
    var st = S.chatState;
    if (st) { st.sending = !!sending; }
    chatSyncComposer();
  }

  // chatSyncSendState 在每次输入变化时刷新按钮可用性。
  function chatSyncSendState() {
    var st = S.chatState;
    if (!st || st.sending) { return; }
    chatSyncComposer();
  }
