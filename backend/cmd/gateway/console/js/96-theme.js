// 96-theme.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=101";

Object.assign(G, {
  applyTheme: applyTheme,
  cycleTheme: cycleTheme,
  syncTopbarMenuLabel: syncTopbarMenuLabel,
  topbarMenuItems: topbarMenuItems,
  closeTopbarMenu: closeTopbarMenu,
});

  /* ─────────────────────── theme ─────────────────────────────── */

  // Three-state cycle: follow-system → light → dark → follow-system.
  // Persisted in localStorage; rendered pre-paint by the inline head script.
  function applyTheme(theme) {
    if (theme === "light" || theme === "dark") {
      document.documentElement.setAttribute("data-theme", theme);
    } else {
      document.documentElement.removeAttribute("data-theme");
    }
    var label = theme === "light" ? "亮色" : theme === "dark" ? "暗色" : "跟随系统";
    var labelEl = S.el("theme-label");
    if (labelEl) { labelEl.textContent = label; }
    var btn = S.el("theme-toggle");
    if (btn) { btn.title = "当前：" + label + "（点击切换）"; }
    syncTopbarMenuLabel();
  }

  function cycleTheme() {
    var cur = document.documentElement.getAttribute("data-theme");
    var next;
    if (!cur) { next = "light"; }        // system → light
    else if (cur === "light") { next = "dark"; }
    else { next = null; }                // dark → system
    try {
      if (next) { localStorage.setItem("aicli-gw-theme", next); }
      else { localStorage.removeItem("aicli-gw-theme"); }
    } catch (e) { /* storage unavailable */ }
    applyTheme(next);
  }

  S.el("theme-toggle").addEventListener("click", cycleTheme);
  applyTheme(document.documentElement.getAttribute("data-theme"));

  // Narrow screens keep one topbar action: this menu absorbs the theme and
  // refresh buttons. Logout deliberately stays only in the sidebar.
  function syncTopbarMenuLabel() {
    var item = S.el("topbar-theme");
    if (!item) { return; }
    var label = S.el("theme-label");
    item.textContent = "主题：" + (label && label.textContent ? label.textContent : "跟随系统");
  }

  function topbarMenuItems() {
    var menu = S.el("topbar-menu");
    if (!menu) { return []; }
    return Array.prototype.slice.call(menu.querySelectorAll('[role="menuitem"]'));
  }

  function closeTopbarMenu(restoreFocus) {
    var menu = S.el("topbar-menu");
    var trigger = S.el("topbar-more");
    if (!menu || menu.hidden) { return; }
    var hadFocus = !!(document.activeElement && menu.contains(document.activeElement));
    menu.hidden = true;
    if (trigger) {
      trigger.setAttribute("aria-expanded", "false");
      if (restoreFocus && hadFocus) { trigger.focus(); }
    }
  }

  S.topbarMore = S.el("topbar-more");
  S.topbarMenu = S.el("topbar-menu");
  if (S.topbarMore && S.topbarMenu) {
    S.topbarMore.addEventListener("click", function (e) {
      e.stopPropagation();
      var willOpen = S.topbarMenu.hidden;
      G.closeSessionMenus(false);
      if (!willOpen) {
        closeTopbarMenu(false);
        return;
      }
      syncTopbarMenuLabel();
      S.topbarMenu.hidden = false;
      S.topbarMore.setAttribute("aria-expanded", "true");
      var items = topbarMenuItems();
      if (items.length) { items[0].focus(); }
    });
    S.topbarMenu.addEventListener("keydown", function (e) {
      var items = topbarMenuItems();
      if (!items.length) { return; }
      var idx = items.indexOf(document.activeElement);
      if (e.key === "ArrowDown") {
        e.preventDefault();
        e.stopPropagation();
        items[(idx + 1 + items.length) % items.length].focus();
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        e.stopPropagation();
        items[(idx - 1 + items.length) % items.length].focus();
      } else if (e.key === "Home") {
        e.preventDefault();
        e.stopPropagation();
        items[0].focus();
      } else if (e.key === "End") {
        e.preventDefault();
        e.stopPropagation();
        items[items.length - 1].focus();
      } else if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        closeTopbarMenu(true);
      }
    });
    document.addEventListener("click", function (e) {
      if (e.target && e.target.closest && e.target.closest("#topbar-more, #topbar-menu")) { return; }
      closeTopbarMenu(false);
    });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { closeTopbarMenu(true); }
    });
    window.addEventListener("resize", function () {
      if (window.innerWidth > 860) { closeTopbarMenu(false); }
    });
    var topbarTheme = S.el("topbar-theme");
    if (topbarTheme) {
      topbarTheme.addEventListener("click", function () {
        cycleTheme();
        syncTopbarMenuLabel();
        closeTopbarMenu(true);
      });
    }
    var topbarRefresh = S.el("topbar-refresh");
    if (topbarRefresh) {
      topbarRefresh.addEventListener("click", function () {
        var refresh = S.el("refresh");
        if (refresh) { refresh.click(); }
        closeTopbarMenu(true);
      });
    }
  }
