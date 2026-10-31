package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
		`id="chat-more"`,
		`id="chat-more-menu"`,
		`data-chat-window-close`,
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
		"function syncChatHeaderActions",
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
		"grid-template-columns: 304px minmax(0, 1fr)",
		"align-items: stretch",
		"grid-template-columns: minmax(0, 1fr) auto",
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
	for _, removed := range []string{`id="chat-modal-open"`, `id="chat-detail"`} {
		if strings.Contains(string(html), removed) {
			t.Fatalf("obsolete chat header control still present: %s", removed)
		}
	}
	if strings.Contains(string(html), `id="chat-older"`) {
		t.Fatal("top load-older button should be removed; scrolling to top auto-loads")
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

// The connector management UI is the only place operators can create/edit a
// gateway bot; keep its HTML hooks, API verbs and schema-driven helpers wired.
func TestConsoleConnectorManagementUI(t *testing.T) {
	html, err := os.ReadFile("console/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	for _, want := range []string{
		`id="connectors-create"`,
		`id="connector-editor-modal"`,
		`id="connector-editor-form"`,
		`id="connector-editor-save"`,
		`id="connector-test-modal"`,
		`id="connector-logs-modal"`,
		`id="connector-action-modal"`,
		`id="connector-qr-modal"`,
		`id="connector-qr-image"`,
		`id="notify-im-connector"`,
		`id="notify-im-source"`,
		`id="notify-im-node"`,
		`id="notify-im-kinds"`,
		// 通知设置页分三层（全局策略 / Bot 目标 / 会话级覆盖），每个 Bot 目标有
		// 自己的配置面。
		`class="notify-layer"`,
		`id="notify-im-editor"`,
		`id="notify-im-apply"`,
		`id="notify-im-cancel"`,
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
		"function connectorBuildPayload",
		"function openConnectorEditor",
		"function saveConnectorEditor",
		"function loadConnectorLogs",
		"function connectorLoginStatus",
		"function openConnectorQRDialog",
		"function startConnectorQRFromEditor",
		"function notifyIMSourceField",
		"function populateNotifyIMNodes",
		// IM 目标事件类型是多选（值域 = kinds 元数据），不是手写逗号分隔。
		"function renderNotifyIMKinds(",
		"function notifyIMCollectKinds(",
		"function notifyIMSyncKindsHint(",
		"function openNotifyIMEditor(",
		"function applyNotifyIMEditor(",
		"function buildNotifyIMTargetFromEditor(",
		"function setNotifyIMKindsSelection(",
		`data-notify-kind`,
		`"/api/qr?text="`,
		`apiJSON("/api/connectors/"`,
		`"/bindings/"`,
		`"__clear__"`,
		`apiJSON("/api/connectors/bindings")`,
	} {
		if !strings.Contains(string(js), want) {
			t.Fatalf("app.js missing %s", want)
		}
	}
	// kinds 输入框必须已经换成复选框容器（回归：又退回自由文本）。
	if strings.Contains(string(html), `id="notify-im-kinds" class="filter-input"`) ||
		strings.Contains(string(html), `<input id="notify-im-kinds"`) {
		t.Fatal("notify-im-kinds is still a free-text input; it must be a multi-select group")
	}
	css, err := os.ReadFile("console/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	for _, want := range []string{".connector-form", ".connector-binding-form", ".connector-log-row", ".connector-action-qr", ".connector-qr-img", ".notify-kinds-compact", ".notify-layer", ".notify-layer-head", ".notify-im-editor"} {
		if !strings.Contains(string(css), want) {
			t.Fatalf("style.css missing %s", want)
		}
	}
}

// console/app.js 是 console-src/ 片段按 manifest.json 拼装的单文件产物
// （go:embed 的入口仍必须是单文件，见 console-src/README.md）。直接改 app.js，
// 或者改了片段却忘记重建，都会让两者漂移——这里把它钉成显式失败，避免
// 「源码改了、线上还是旧逻辑」这种最难查的回归。
func TestConsoleAppMatchesSplitSources(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("console-src", "manifest.json"))
	if err != nil {
		t.Fatalf("read console-src/manifest.json: %v", err)
	}
	var manifest struct {
		Generated string   `json:"generated"`
		Files     []string `json:"files"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse console-src/manifest.json: %v", err)
	}
	if manifest.Generated != "console/app.js" {
		t.Fatalf("manifest.generated = %q, want console/app.js", manifest.Generated)
	}
	if len(manifest.Files) < 10 {
		t.Fatalf("manifest lists %d fragments; expected the feature split", len(manifest.Files))
	}
	want := "(function () {\n"
	for _, name := range manifest.Files {
		body, err := os.ReadFile(filepath.Join("console-src", name))
		if err != nil {
			t.Fatalf("read console-src/%s: %v", name, err)
		}
		if len(bytes.TrimSpace(body)) == 0 {
			t.Fatalf("console-src/%s is empty", name)
		}
		want += string(body)
	}
	want += "})();\n"
	got, err := os.ReadFile(filepath.Join("console", "app.js"))
	if err != nil {
		t.Fatalf("read console/app.js: %v", err)
	}
	if string(got) != want {
		t.Fatal("console/app.js drifted from console-src/; run: node scripts/build-gateway-console.mjs")
	}
}
