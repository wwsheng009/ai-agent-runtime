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
    var labelEl = el("theme-label");
    if (labelEl) { labelEl.textContent = label; }
    var btn = el("theme-toggle");
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

  el("theme-toggle").addEventListener("click", cycleTheme);
  applyTheme(document.documentElement.getAttribute("data-theme"));

  // Narrow screens keep one topbar action: this menu absorbs the theme and
  // refresh buttons. Logout deliberately stays only in the sidebar.
  function syncTopbarMenuLabel() {
    var item = el("topbar-theme");
    if (!item) { return; }
    var label = el("theme-label");
    item.textContent = "主题：" + (label && label.textContent ? label.textContent : "跟随系统");
  }

  function topbarMenuItems() {
    var menu = el("topbar-menu");
    if (!menu) { return []; }
    return Array.prototype.slice.call(menu.querySelectorAll('[role="menuitem"]'));
  }

  function closeTopbarMenu(restoreFocus) {
    var menu = el("topbar-menu");
    var trigger = el("topbar-more");
    if (!menu || menu.hidden) { return; }
    var hadFocus = !!(document.activeElement && menu.contains(document.activeElement));
    menu.hidden = true;
    if (trigger) {
      trigger.setAttribute("aria-expanded", "false");
      if (restoreFocus && hadFocus) { trigger.focus(); }
    }
  }

  var topbarMore = el("topbar-more");
  var topbarMenu = el("topbar-menu");
  if (topbarMore && topbarMenu) {
    topbarMore.addEventListener("click", function (e) {
      e.stopPropagation();
      var willOpen = topbarMenu.hidden;
      closeSessionMenus(false);
      if (!willOpen) {
        closeTopbarMenu(false);
        return;
      }
      syncTopbarMenuLabel();
      topbarMenu.hidden = false;
      topbarMore.setAttribute("aria-expanded", "true");
      var items = topbarMenuItems();
      if (items.length) { items[0].focus(); }
    });
    topbarMenu.addEventListener("keydown", function (e) {
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
    var topbarTheme = el("topbar-theme");
    if (topbarTheme) {
      topbarTheme.addEventListener("click", function () {
        cycleTheme();
        syncTopbarMenuLabel();
        closeTopbarMenu(true);
      });
    }
    var topbarRefresh = el("topbar-refresh");
    if (topbarRefresh) {
      topbarRefresh.addEventListener("click", function () {
        var refresh = el("refresh");
        if (refresh) { refresh.click(); }
        closeTopbarMenu(true);
      });
    }
  }

