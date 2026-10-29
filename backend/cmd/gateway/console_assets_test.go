package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The console is embedded, so a missing asset/reference fails silently at
// runtime (blank view / dead button). These assertions keep the new MCP view and
// the session overflow menu wired end to end.
func TestConsoleMCPViewAndSessionOverflowMenu(t *testing.T) {
	html, err := os.ReadFile("console/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	for _, want := range []string{
		`data-view="mcp"`,
		`id="mcp-list"`,
		`id="mcp-summary"`,
		`id="logs-follow"`,
		`id="a11y-status"`,
		`class="connector-advanced"`,
		`<h1 class="view-title" id="view-title">`,
		`<div class="event-strip" id="event-strip" hidden>`,
		`id="stat-attention-card"`,
		`data-view="logs"`,
		`id="log-mount-list"`,
		`id="log-audit-list"`,
		`id="mount-token-modal"`,
		`id="mount-token-form"`,
		`id="mount-token-list"`,
		`id="sd-access"`,
		`id="sd-diagnostics"`,
		`id="sd-copy-diagnostics"`,
		`id="notify-quiet-rows"`,
		`id="notify-quiet-add"`,
		`id="notify-session-search"`,
		`id="notify-session-only-custom"`,
		`id="notify-savebar"`,
		`id="notify-dirty"`,
		`id="topbar-more"`,
		`id="topbar-menu"`,
		`id="device-search"`,
		`id="device-status-filter"`,
		`id="mount-token-days"`,
		`id="confirm-modal"`,
		`id="confirm-cancel"`,
		`id="confirm-confirm"`,
		`aria-describedby="confirm-message"`,
		`>不过期<`,
		`>指定天数…<`,
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("index.html missing %s", want)
		}
	}

	js, err := os.ReadFile("console/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	for _, want := range []string{
		"function loadMounts",
		"function renderMounts",
		"function mountHostText",
		"function mountProfileText",
		"function appendMountFacts",
		"labels.hostname",
		"labels.tool_count",
		"labels.tools",
		"labels.roots",
		"function openMountTokens",
		"function loadMountTokens",
		"function syncMountTokenTTL",
		`never_expires`,
		"function initLogsView",
		"function loadMountLogs",
		"function loadAuditLogs",
		"function closeSessionMenus",
		"function positionSessionMenu",
		"isActionableAttention",
		"function sessionDiagnostics",
		"function autoSelectFirstSession",
		"function notifyCollectQuietRows",
		"function renderNotifyQuietRows",
		"function notifyPolicyIsDirty",
		"function notifyFilteredSessionRows",
		"function notifyRefreshDirty",
		"function appendDeviceMeta",
		"function closeTopbarMenu",
		"function syncTopbarMenuLabel",
		"function confirmDestructive",
		"function bindCardMenu",
		"function refreshCurrentView",
		"function announceAttentionChange",
		"var stripExpanded",
		`data-more`,
		`data-web`,
		`class="s-menu"`,
		"打开会话",
		`/api/mounts/`,
		`/api/mounts/logs`,
		`mount-token-modal`,
	} {
		if !strings.Contains(string(js), want) {
			t.Fatalf("app.js missing %s", want)
		}
	}

	css, err := os.ReadFile("console/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	for _, want := range []string{
		".s-menu", ".menu-item", ".s-expand", ".mcp-endpoint", ".mcp-head-actions",
		".mcp-facts", ".mcp-fact", ".mcp-details", ".mcp-chip-list", ".mcp-chip",
		".mcp-token-prefix", ".log-row", ".log-status", ".stat.attention.has-attention",
		".s-access", ".detail-diagnostics", ".chat-notice-text",
		".field-hint", ".field-error", ".notify-savebar", ".notify-quiet-row",
		".notify-session-group", ".topbar-menu", ".device-toolbar", ".meta-time",
		".btn.danger-outline", ".modal.modal-sm", "#confirm-message",
		".sr-only", ".event-strip.expanded", "max-width: 1440px",
	} {
		if !strings.Contains(string(css), want) {
			t.Fatalf("style.css missing %s", want)
		}
	}
	if strings.Contains(string(html), `id="attn-completed"`) {
		t.Fatal("index.html still renders completed in the pending breakdown")
	}
	// UX-07: topbar refresh is the single manual refresh entry.
	for _, removed := range []string{"mcp-refresh", "logs-refresh", "connectors-refresh", "refresh-nodes"} {
		if strings.Contains(string(html), `id="`+removed+`"`) ||
			strings.Contains(string(js), `"`+removed+`"`) {
			t.Fatalf("duplicate refresh control still wired: %s", removed)
		}
	}
	// UX-06: destructive list-row actions live in the overflow menu and the
	// in-page confirm dialog, not as always-visible solid-red buttons.
	if strings.Contains(string(js), "btn danger sm") {
		t.Fatal("app.js still renders an always-visible solid-red button")
	}
	if strings.Contains(string(html), `id="event-strip" hidden aria-live`) {
		t.Fatal("event strip must not announce every transient chip")
	}
}

// The console ships a manual cache-busting version (?v=N) in index.html, the
// service-worker registration and sw.js's CACHE/SHELL. If they drift, a release
// can keep serving the previous app.js out of the browser/CDN cache — observed
// as "I deployed the console change but the page still shows the old UI".
func TestConsoleAssetVersionIsConsistent(t *testing.T) {
	files := []string{"console/index.html", "console/app.js", "console/sw.js"}
	versions := map[string][]string{}
	query := regexp.MustCompile(`\?v=(\d+)`)
	cache := regexp.MustCompile(`aicli-gateway-shell-v(\d+)`)
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(body)
		for _, match := range query.FindAllStringSubmatch(text, -1) {
			versions[file] = append(versions[file], match[1])
		}
		for _, match := range cache.FindAllStringSubmatch(text, -1) {
			versions[file] = append(versions[file], match[1])
		}
		if len(versions[file]) == 0 {
			t.Fatalf("%s carries no ?v= / CACHE version", file)
		}
	}

	seen := map[string]struct{}{}
	for _, list := range versions {
		for _, version := range list {
			seen[version] = struct{}{}
		}
	}
	if len(seen) != 1 {
		t.Fatalf("asset versions drifted (%v); bump index.html, app.js register() and sw.js together", versions)
	}
}
