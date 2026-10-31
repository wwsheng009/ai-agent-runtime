// 05-ui-basics.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=100";

Object.assign(G, {
  confirmDestructive: confirmDestructive,
  toastHost: toastHost,
  pushToast: pushToast,
  notify: notify,
  setAuthed: setAuthed,
});

  /* ─────────────────────────── toasts ────────────────────────── */

  /* ───────────────────────── UX-06: 通用二次确认 ──────────────── */
  // 用站内模态框替代 window.confirm：文案可包含对象、数量与后果，
  // 取消/确认都会把焦点还给触发按钮，且不会累积 document 监听器。
  function confirmDestructive(options) {
    var modal = S.el("confirm-modal");
    if (!modal) { return Promise.resolve({ confirmed: false }); }

    var titleEl = S.el("confirm-title");
    var msgEl = S.el("confirm-message");
    var cancelBtn = S.el("confirm-cancel");
    var confirmBtn = S.el("confirm-confirm");
    var closeBtn = modal.querySelector(".modal-close");
    var previousFocus = options.trigger || document.activeElement;
    var closed = false;
    var resolvePromise;
    var promise = new Promise(function (resolve) { resolvePromise = resolve; });

    titleEl.textContent = options.title || "确认操作";
    msgEl.textContent = options.message || "";
    cancelBtn.textContent = options.cancelText || "取消";
    confirmBtn.textContent = options.confirmText || "是的，执行";
    cancelBtn.hidden = options.showCancel === false;

    function focusables() {
      return [closeBtn, cancelBtn, confirmBtn].filter(function (node) {
        return node && !node.hidden && !node.disabled;
      });
    }

    function onKeydown(e) {
      if (closed) { return; }
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        close(false);
        return;
      }
      if (e.key !== "Tab") { return; }
      var nodes = focusables();
      if (!nodes.length) { return; }
      var idx = nodes.indexOf(document.activeElement);
      if (idx === -1) { idx = 0; }
      var next = e.shiftKey ? (idx - 1 + nodes.length) % nodes.length : (idx + 1) % nodes.length;
      e.preventDefault();
      nodes[next].focus();
    }

    function onOverlayClick(e) {
      if (e.target === modal) { close(false); }
    }

    function close(confirmed, result) {
      if (closed) { return; }
      closed = true;
      modal.hidden = true;
      document.removeEventListener("keydown", onKeydown, true);
      modal.removeEventListener("click", onOverlayClick);
      cancelBtn.onclick = null;
      confirmBtn.onclick = null;
      if (closeBtn) { closeBtn.onclick = null; }
      // 操作可能触发列表重绘；等一帧再还焦点：触发器还在就回去，
      // 已被移除则落到当前视图标题，避免焦点掉到 body。
      setTimeout(function () {
        if (previousFocus && previousFocus.isConnected && typeof previousFocus.focus === "function") {
          previousFocus.focus();
          return;
        }
        var view = document.querySelector(".view:not([hidden])");
        var fallback = view && view.querySelector("h2, .page-title");
        if (!fallback) { fallback = S.el("main"); }
        if (fallback) {
          if (!fallback.hasAttribute("tabindex")) { fallback.setAttribute("tabindex", "-1"); }
          fallback.focus();
        }
      }, 0);
      resolvePromise({ confirmed: confirmed, result: result });
    }

    cancelBtn.onclick = function () { close(false); };
    if (closeBtn) { closeBtn.onclick = function () { close(false); }; }
    confirmBtn.onclick = function () {
      confirmBtn.disabled = true;
      Promise.resolve(options.onConfirm ? options.onConfirm() : null)
        .then(function (result) { close(true, result); })
        .catch(function (err) {
          confirmBtn.disabled = false;
          pushToast("操作失败：" + (err && err.message ? err.message : err), "error");
        });
    };

    document.addEventListener("keydown", onKeydown, true);
    modal.addEventListener("click", onOverlayClick);
    modal.hidden = false;
    confirmBtn.disabled = false;
    // 破坏性操作默认聚焦「取消」，避免 Enter 直接执行。
    var initial = options.focusConfirm ? confirmBtn : (cancelBtn.hidden ? confirmBtn : cancelBtn);
    initial.focus();

    return promise;
  }
  /* ───────────────────────── UX-06 结束 ──────────────────────── */

  function toastHost() {
    var host = S.el("toast-host");
    if (!host) {
      host = document.createElement("div");
      host.id = "toast-host";
      host.className = "toast-host";
      document.body.appendChild(host);
    }
    return host;
  }

  // Toasts are announced (role=status / role=alert), stack inside one host
  // instead of overlapping on the same fixed coordinates, and hold their
  // dismissal timer while hovered or focused so they can actually be read.
  function pushToast(text, kind, action) {
    var host = toastHost();
    var toast = document.createElement("div");
    toast.className = "toast" + (kind ? " toast-" + kind : "");
    toast.setAttribute("role", kind === "error" ? "alert" : "status");

    var msg = document.createElement("span");
    msg.className = "toast-text";
    msg.textContent = text;
    toast.appendChild(msg);

    var actions = document.createElement("div");
    actions.className = "toast-actions";
    if (action) {
      var actionBtn = document.createElement("button");
      actionBtn.type = "button";
      actionBtn.className = "btn ghost sm";
      actionBtn.textContent = action.label;
      actionBtn.addEventListener("click", function () { dismiss(); action.run(); });
      actions.appendChild(actionBtn);
    }
    var close = document.createElement("button");
    close.type = "button";
    close.className = "toast-close";
    close.setAttribute("aria-label", "关闭通知");
    close.textContent = "\u00d7";
    close.addEventListener("click", dismiss);
    actions.appendChild(close);
    toast.appendChild(actions);

    var timer = null;
    var done = false;
    function dismiss() {
      if (done) { return; }
      done = true;
      if (timer) { clearTimeout(timer); }
      toast.remove();
    }
    function schedule(ms) {
      if (timer) { clearTimeout(timer); }
      timer = setTimeout(dismiss, ms);
    }
    function hold() { if (timer) { clearTimeout(timer); timer = null; } }
    toast.addEventListener("mouseenter", hold);
    toast.addEventListener("focusin", hold);
    toast.addEventListener("mouseleave", function () { if (!done) { schedule(2500); } });
    toast.addEventListener("focusout", function () {
      if (!done && !toast.contains(document.activeElement)) { schedule(2500); }
    });

    host.appendChild(toast);
    // Cap the stack so a burst of events cannot fill the viewport.
    while (host.children.length > 4) { host.removeChild(host.firstChild); }
    schedule(kind === "error" ? 6000 : 4000);
    return toast;
  }

  function notify(text, kind) {
    pushToast(text, kind);
  }

  function setAuthed(authed) {
    S.authedState = !!authed;
    S.el("login").hidden = authed;
    S.el("app").hidden = !authed;
    S.el("logout").hidden = !authed;
    S.el("logout-top").hidden = !authed;
    // Two-screen layout: pre-auth = standalone login page (chrome hidden),
    // authed = full workspace. The class also gates the chrome via CSS.
    document.body.classList.toggle("login-mode", !authed);
    if (!authed) {
      G.closeSidebar();
      S.el("password").setAttribute("aria-invalid", "false");
      S.el("password").focus();
      S.pushConfig = null; // re-read gateway push config on the next login
      S.notifyPolicy = null;
      S.notifyPolicyMeta = null;
      S.notifyPolicyDraft = null;
      S.notifyPolicyLoaded = false;
      S.notifyPolicyBaseline = "";
      S.notifyPolicySaving = false;
      S.notifyQuietError = "";
      S.notifyQuietTouched = false;
      S.notifyQuietInvalidEl = null;
    }
    G.refreshPushState();
    G.renderInstallHint();
  }
