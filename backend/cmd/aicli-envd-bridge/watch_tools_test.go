package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
)

type watchFixture struct {
	Name string
	Type string
}

// writeWatchFrame ignores write errors: a test that stops after max_events may
// close the stream while the fake is still writing later events.
func writeWatchFrame(w io.Writer, payload any) {
	frame, err := envdclient.EncodeJSONFrame(payload)
	if err != nil {
		return
	}
	_, _ = w.Write(frame)
}

// fakeWatchClient serves the resolution call and a WatchDir stream.
func fakeWatchClient(t *testing.T, canonical string, events []watchFixture, block bool) *envdclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/process.Process/Start":
			w.Header().Set("Content-Type", "application/connect+json")
			writeWatchFrame(w, map[string]any{"event": map[string]any{"start": map[string]any{"pid": 7}}})
			writeWatchFrame(w, map[string]any{"event": map[string]any{"data": map[string]any{
				"stdout": base64.StdEncoding.EncodeToString([]byte(canonical + "\n")),
			}}})
			writeWatchFrame(w, map[string]any{"event": map[string]any{"end": map[string]any{
				"exited": true, "status": "exit status 0",
			}}})
		case "/filesystem.Filesystem/WatchDir":
			w.Header().Set("Content-Type", "application/connect+json")
			writeWatchFrame(w, map[string]any{"start": map[string]any{}})
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			for _, event := range events {
				writeWatchFrame(w, map[string]any{"filesystem": map[string]any{
					"name": event.Name, "type": event.Type,
				}})
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			if block {
				select {
				case <-r.Context().Done():
				case <-time.After(2 * time.Second):
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := envdclient.Dial(envdclient.Config{Addr: srv.URL, User: "user"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return client
}

// TestWatchDirReturnsEvents covers the normal bounded window.
func TestWatchDirReturnsEvents(t *testing.T) {
	b := &bridge{
		client:    fakeWatchClient(t, "/srv/app", []watchFixture{{"a.txt", "EVENT_TYPE_CREATE"}, {"b.txt", "EVENT_TYPE_WRITE"}}, false),
		workspace: "/srv/app",
		profile:   profileReadonly,
		timeout:   5 * time.Second,
	}
	res := b.callTool(context.Background(), "watch_dir", json.RawMessage(`{"path":".","wait_ms":1000}`))
	if res.IsError {
		t.Fatalf("watch_dir failed: %+v", res)
	}
	text := res.Content[0].Text
	for _, want := range []string{"2 event(s)", `EVENT_TYPE_CREATE "a.txt"`, `EVENT_TYPE_WRITE "b.txt"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("output = %q, want %q", text, want)
		}
	}
}

// TestWatchDirHonorsMaxEvents proves the tool closes the stream early instead of
// returning unbounded events.
func TestWatchDirHonorsMaxEvents(t *testing.T) {
	b := &bridge{
		client: fakeWatchClient(t, "/srv/app", []watchFixture{
			{"a.txt", "EVENT_TYPE_CREATE"},
			{"b.txt", "EVENT_TYPE_WRITE"},
			{"c.txt", "EVENT_TYPE_REMOVE"},
		}, false),
		workspace: "/srv/app",
		profile:   profileReadonly,
		timeout:   5 * time.Second,
	}
	res := b.callTool(context.Background(), "watch_dir", json.RawMessage(`{"path":".","wait_ms":1000,"max_events":1}`))
	if res.IsError {
		t.Fatalf("watch_dir failed: %+v", res)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "1 event(s)") || !strings.Contains(text, `"a.txt"`) {
		t.Fatalf("output = %q, want exactly the first event", text)
	}
	if strings.Contains(text, `"b.txt"`) || strings.Contains(text, `"c.txt"`) {
		t.Fatalf("output = %q, want max_events to stop the stream", text)
	}
}

// TestWatchDirTimeout returns a normal no-events result rather than a tool error.
func TestWatchDirTimeout(t *testing.T) {
	b := &bridge{
		client:    fakeWatchClient(t, "/srv/app", nil, true),
		workspace: "/srv/app",
		profile:   profileReadonly,
		timeout:   5 * time.Second,
	}
	started := time.Now()
	res := b.callTool(context.Background(), "watch_dir", json.RawMessage(`{"path":".","wait_ms":100}`))
	if res.IsError {
		t.Fatalf("watch_dir timeout became an error: %+v", res)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("watch_dir took %v, want it bounded by wait_ms", time.Since(started))
	}
	if !strings.Contains(res.Content[0].Text, "no change events within") {
		t.Fatalf("output = %q, want the bounded-window message", res.Content[0].Text)
	}
}

// TestWatchDirStreamClosed covers a clean stream end before any event.
func TestWatchDirStreamClosed(t *testing.T) {
	b := &bridge{
		client:    fakeWatchClient(t, "/srv/app", nil, false),
		workspace: "/srv/app",
		profile:   profileReadonly,
		timeout:   5 * time.Second,
	}
	res := b.callTool(context.Background(), "watch_dir", json.RawMessage(`{"path":".","wait_ms":1000}`))
	if res.IsError || !strings.Contains(res.Content[0].Text, "stream closed") {
		t.Fatalf("watch_dir = %+v, want a clean stream-closed result", res)
	}
}

// TestWatchDirRejectsJailEscape ensures the watch path goes through the same
// target-side symlink resolution as the other file tools.
func TestWatchDirRejectsJailEscape(t *testing.T) {
	b := &bridge{
		client:    fakeWatchClient(t, "/etc", nil, false),
		workspace: "/srv/app",
		profile:   profileReadonly,
		timeout:   5 * time.Second,
	}
	res := b.callTool(context.Background(), "watch_dir", json.RawMessage(`{"path":"link"}`))
	if !res.IsError || !strings.Contains(res.Content[0].Text, "outside the workspace") {
		t.Fatalf("watch_dir = %+v, want a path-jail error", res)
	}
}
