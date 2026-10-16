package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func getConsole(path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	consoleHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestConsoleServesIndex(t *testing.T) {
	rec := getConsole("/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "aicli-gateway") {
		t.Fatalf("index body missing title: %s", rec.Body.String())
	}
}

func TestConsoleServesAssets(t *testing.T) {
	for _, path := range []string{"/style.css", "/app.js", "/sw.js", "/icon.svg", "/icon-192.png", "/icon-512.png", "/icon-180.png", "/badge-96.png", "/manifest.webmanifest"} {
		rec := getConsole(path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Fatalf("%s served an empty body", path)
		}
	}

	rec := getConsole("/manifest.webmanifest")
	if ct := rec.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Fatalf("manifest content type = %q, want application/manifest+json", ct)
	}
}

func TestConsoleFallsBackToIndexForUnknownRoutes(t *testing.T) {
	rec := getConsole("/some/spa/route")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "aicli-gateway") {
		t.Fatalf("fallback did not serve index: %s", rec.Body.String())
	}
}

func TestConsoleDoesNotShadowReservedPrefixes(t *testing.T) {
	for _, path := range []string{"/api/unknown", "/gw/agent", "/n/node-x/foo"} {
		rec := getConsole(path)
		if rec.Code != http.StatusNotFound {
			body, _ := io.ReadAll(rec.Body)
			t.Fatalf("%s status = %d, want 404 (body=%s)", path, rec.Code, body)
		}
	}
}

func TestConsoleRejectsTraversal(t *testing.T) {
	rec := getConsole("/../../etc/passwd")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// readConsoleAsset reads a shell asset from the embedded FS, i.e. exactly the
// bytes the console handler ships (and not whatever happens to be on disk).
func readConsoleAsset(t *testing.T, name string) string {
	t.Helper()
	raw, err := consoleFS.ReadFile("console/" + name)
	if err != nil {
		t.Fatalf("read console/%s: %v", name, err)
	}
	return string(raw)
}

func assetVersion(t *testing.T, label, pattern, body string) string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("%s: %q not found", label, pattern)
	}
	return m[1]
}

// TestConsoleAssetVersionsMatch guards the cache-busting contract: the version
// must be bumped in five places at once (index.html x2, sw.js CACHE + the two
// SHELL URLs, and the register() call in app.js). Miss one and phones keep
// running the old shell for hours/forever (Cloudflare edge-caches *.js/*.css on
// the versioned URL, and the SW serves its cached copy). That failure is
// invisible in development, which is exactly why it needs a test.
func TestConsoleAssetVersionsMatch(t *testing.T) {
	index := readConsoleAsset(t, "index.html")
	app := readConsoleAsset(t, "app.js")
	sw := readConsoleAsset(t, "sw.js")

	want := assetVersion(t, "index.html app.js", `app\.js\?v=(\d+)`, index)
	got := map[string]string{
		"index.html style.css":  assetVersion(t, "index.html style.css", `style\.css\?v=(\d+)`, index),
		"sw.js CACHE":           assetVersion(t, "sw.js CACHE", `aicli-gateway-shell-v(\d+)`, sw),
		"sw.js SHELL app.js":    assetVersion(t, "sw.js SHELL app.js", `app\.js\?v=(\d+)`, sw),
		"sw.js SHELL style.css": assetVersion(t, "sw.js SHELL style.css", `style\.css\?v=(\d+)`, sw),
		"app.js register sw.js": assetVersion(t, "app.js register sw.js", `sw\.js\?v=(\d+)`, app),
	}
	for label, version := range got {
		if version != want {
			t.Fatalf("%s version = %s, want %s (bump all five sites together)", label, version, want)
		}
	}
}

// chatSSERegistered extracts the quoted names of the CHAT_SSE_EVENTS array.
func chatSSERegistered(t *testing.T, app string) map[string]bool {
	t.Helper()
	start := strings.Index(app, "CHAT_SSE_EVENTS = [")
	if start < 0 {
		t.Fatal("app.js: CHAT_SSE_EVENTS array not found")
	}
	rest := app[start:]
	end := strings.Index(rest, "];")
	if end < 0 {
		t.Fatal("app.js: CHAT_SSE_EVENTS array not terminated")
	}
	names := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(rest[:end], -1) {
		names[m[1]] = true
	}
	if len(names) == 0 {
		t.Fatal("app.js: CHAT_SSE_EVENTS is empty")
	}
	return names
}

// TestConsoleChatPanelAsset pins the two contract points of the session chat
// panel that fail silently at runtime: the modal markup must exist, and the
// EventSource named-event registration must cover every consumed event (an
// unregistered event name is dropped by the browser without any error, the
// historical "assistant_message never arrived" bug — the node-side twin of this
// guard is web_event_sequence_asset_test.go).
func TestConsoleChatPanelAsset(t *testing.T) {
	index := readConsoleAsset(t, "index.html")
	app := readConsoleAsset(t, "app.js")

	if !strings.Contains(index, `id="chat-modal"`) {
		t.Fatal(`index.html is missing id="chat-modal"`)
	}
	for _, needle := range []string{
		"chat-modal", "/web/api/screen", "/web/api/events", "/web/api/input",
		"/web/api/statusbar", "message_window", "chat-dyn-status",
		// GFM tables: the block scanner must know the pipe-row rule, otherwise a
		// whole table degrades to a paragraph (and the "-" separator line gets
		// eaten by the list rule first).
		"MD_TABLE_ROW",
	} {
		if !strings.Contains(app, needle) {
			t.Fatalf("app.js is missing %q", needle)
		}
	}
	if !strings.Contains(index, `id="chat-dyn-status"`) || !strings.Contains(index, `id="chat-segs"`) {
		t.Fatal("index.html is missing the chat status bar (#chat-dyn-status / #chat-segs)")
	}

	// The pagination metadata key is message_window; there is no "window" alias
	// (chatDebugScreenSnapshot.MessageWindow). Reading data.window fails
	// silently: has_more stays false and the cursor stays 0, so "load older"
	// never works and nothing errors. Pin the negative too, because the wrong
	// key is the natural thing to write.
	if strings.Contains(app, "data.window") {
		t.Fatal(`app.js reads data.window: the screen response key is message_window`)
	}

	registered := chatSSERegistered(t, app)
	required := []string{
		"connected", "heartbeat", "screen_refresh", "session_switched",
		"session_start", "session_end", "session_interrupted",
		"turn_start", "turn_end",
		"assistant_delta", "assistant_message", "assistant_image_progress", "reasoning_delta",
		"tool_start", "tool_end",
		"approval_requested", "approval_resolved", "question_asked", "question_answered",
		"error",
		// Status bar: dynamic_status carries the Retrying/Analyzing/Running line,
		// model_changed invalidates the static segments. Missing either one leaves
		// the status bar silently dead (the browser drops unregistered frames).
		"dynamic_status", "model_changed",
	}
	for _, name := range required {
		if !registered[name] {
			t.Fatalf("CHAT_SSE_EVENTS never registers %q: the browser would drop that frame silently", name)
		}
	}

	// The reverse direction: a registered name without a handler is dead weight
	// (and a consumed case that is not registered can never fire).
	start := strings.Index(app, "function chatOnEvent")
	if start < 0 {
		t.Fatal("app.js: chatOnEvent not found")
	}
	body := app[start:]
	if cut := strings.Index(body, "function chatResetForSession"); cut >= 0 {
		body = body[:cut]
	}
	handled := map[string]bool{}
	for _, m := range regexp.MustCompile(`case "([a-z_]+)":`).FindAllStringSubmatch(body, -1) {
		handled[m[1]] = true
		// "message" is the unnamed default frame, delivered via onmessage
		// rather than a named listener, so it is legitimately unregistered.
		if m[1] != "message" && !registered[m[1]] {
			t.Fatalf("chatOnEvent handles %q but CHAT_SSE_EVENTS does not register it", m[1])
		}
	}
	for name := range registered {
		if !handled[name] {
			t.Fatalf("CHAT_SSE_EVENTS registers %q but chatOnEvent has no case for it", name)
		}
	}

	// session_start / session_end are per-run boundaries, not session switches: the
	// actor publishes them at the start of every run and right after
	// assistant_message, with a payload carrying only turn_id. Wiring them to a full
	// panel reset wipes the transcript, the scroll position and the reading progress
	// on every single turn (plus a "正在加载消息…" flash) — and nothing else in this
	// file would notice. Only session_switched (which carries a new session_id) may
	// reset the panel.
	for _, name := range []string{`case "session_start":`, `case "session_end":`} {
		at := strings.Index(body, name)
		if at < 0 {
			t.Fatalf("chatOnEvent has no %s", name)
		}
		arm := body[at:]
		if cut := strings.Index(arm, "break;"); cut >= 0 {
			arm = arm[:cut]
		}
		if strings.Contains(arm, "chatResetForSession") {
			t.Fatalf("%s is a per-run boundary, not a session switch: resetting the panel there wipes it every turn", name)
		}
	}
}
