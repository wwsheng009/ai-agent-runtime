package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
)

type ptyCall struct {
	Path string
	Body map[string]any
}

// newPtyFakeEnvD serves Start plus the unary control RPCs and records their
// request bodies, so the interactive session tools can be tested end to end.
func newPtyFakeEnvD(t *testing.T, writeStart func(w http.ResponseWriter)) (*envdclient.Client, *[]ptyCall) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls []ptyCall
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/process.Process/Start":
			w.Header().Set("Content-Type", "application/connect+json")
			if writeStart != nil {
				writeStart(w)
			}
		case "/process.Process/SendInput", "/process.Process/SendSignal", "/process.Process/Update":
			payload, _ := io.ReadAll(r.Body)
			var body map[string]any
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Errorf("decode %s: %v", r.URL.Path, err)
			}
			mu.Lock()
			calls = append(calls, ptyCall{Path: r.URL.Path, Body: body})
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := envdclient.Dial(envdclient.Config{Addr: srv.URL, User: "n9e"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return client, &calls
}

func writeStartFrame(t *testing.T, w http.ResponseWriter, pid uint32) {
	t.Helper()
	writeConnectFrame(t, w, map[string]any{"event": map[string]any{"start": map[string]any{"pid": pid}}})
}

// TestPtyStartReadClose covers the session lifecycle: start, buffered pty read,
// and close.
func TestPtyStartReadClose(t *testing.T) {
	client, _ := newPtyFakeEnvD(t, func(w http.ResponseWriter) {
		writeStartFrame(t, w, 4242)
		writeConnectFrame(t, w, map[string]any{"event": map[string]any{"data": map[string]any{
			"pty": base64.StdEncoding.EncodeToString([]byte("hello\n")),
		}}})
		writeConnectFrame(t, w, map[string]any{"event": map[string]any{"end": map[string]any{
			"exited": true, "status": "exit status 0",
		}}})
	})
	b := &bridge{
		client:    client,
		workspace: "/srv/app",
		profile:   profileOps,
		timeout:   5 * time.Second,
		sessions:  make(map[uint32]*ptySession),
	}
	ctx := context.Background()

	start := b.callTool(ctx, "pty_start", json.RawMessage(`{"command":"bash"}`))
	if start.IsError {
		t.Fatalf("pty_start: %+v", start)
	}
	if !strings.Contains(start.Content[0].Text, "pid=4242") {
		t.Fatalf("start = %q, want the pid", start.Content[0].Text)
	}

	// pty_read returns as soon as it has data (interactive-first), so poll until
	// the end frame has been observed too.
	var text string
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(text, "[exit 0") {
		if time.Now().After(deadline) {
			t.Fatalf("read = %q, want output and exit status", text)
		}
		read := b.callTool(ctx, "pty_read", json.RawMessage(`{"pid":4242,"wait_ms":200}`))
		if read.IsError {
			t.Fatalf("pty_read: %+v", read)
		}
		text += read.Content[0].Text
	}
	if !strings.Contains(text, "hello") {
		t.Fatalf("read = %q, want the buffered output", text)
	}

	closeRes := b.callTool(ctx, "pty_close", json.RawMessage(`{"pid":4242}`))
	if closeRes.IsError {
		t.Fatalf("pty_close: %+v", closeRes)
	}
	b.sessionMu.Lock()
	if len(b.sessions) != 0 {
		t.Fatalf("sessions = %d, want none after close", len(b.sessions))
	}
	b.sessionMu.Unlock()
}

// TestPtyWriteResize checks the wire shape used for pty input and resize.
func TestPtyWriteResize(t *testing.T) {
	client, calls := newPtyFakeEnvD(t, func(w http.ResponseWriter) {
		writeStartFrame(t, w, 77)
		writeConnectFrame(t, w, map[string]any{"event": map[string]any{"end": map[string]any{
			"exited": true, "status": "exit status 0",
		}}})
	})
	b := &bridge{
		client:    client,
		workspace: "/srv/app",
		profile:   profileOps,
		timeout:   5 * time.Second,
		sessions:  make(map[uint32]*ptySession),
	}
	ctx := context.Background()

	if res := b.callTool(ctx, "pty_start", json.RawMessage(`{"command":"bash"}`)); res.IsError {
		t.Fatalf("pty_start: %+v", res)
	}
	if res := b.callTool(ctx, "pty_write", json.RawMessage(`{"pid":77,"data":"ls\n"}`)); res.IsError {
		t.Fatalf("pty_write: %+v", res)
	}
	if res := b.callTool(ctx, "pty_resize", json.RawMessage(`{"pid":77,"cols":120,"rows":40}`)); res.IsError {
		t.Fatalf("pty_resize: %+v", res)
	}

	if len(*calls) != 2 {
		t.Fatalf("recorded %d calls, want 2", len(*calls))
	}
	input := (*calls)[0]
	if input.Path != "/process.Process/SendInput" {
		t.Fatalf("first call = %s, want SendInput", input.Path)
	}
	encoded := input.Body["input"].(map[string]any)["pty"].(string)
	if decoded, err := base64.StdEncoding.DecodeString(encoded); err != nil || string(decoded) != "ls\n" {
		t.Errorf("pty input = %q (decoded %q, err %v), want ls\\n", encoded, decoded, err)
	}
	resize := (*calls)[1]
	if resize.Path != "/process.Process/Update" {
		t.Fatalf("second call = %s, want Update", resize.Path)
	}
	size := resize.Body["pty"].(map[string]any)["size"].(map[string]any)
	if size["cols"] != float64(120) || size["rows"] != float64(40) {
		t.Errorf("resize = %v, want 120x40", size)
	}
}

// TestPtyToolsAreOpsOnly locks the tier gate.
func TestPtyToolsAreOpsOnly(t *testing.T) {
	b := &bridge{workspace: "/srv/app", profile: profileStandard, timeout: time.Second}
	for _, tool := range []string{"pty_start", "pty_write", "pty_read", "pty_resize", "pty_close"} {
		res := b.callTool(context.Background(), tool, json.RawMessage(`{}`))
		if !res.IsError || !strings.Contains(res.Content[0].Text, "not available under the standard profile") {
			t.Errorf("%s = %+v, want a standard-profile refusal", tool, res)
		}
	}
}

// TestPtyUnknownSession covers the pid guard.
func TestPtyUnknownSession(t *testing.T) {
	b := &bridge{workspace: "/srv/app", profile: profileOps, timeout: time.Second, sessions: make(map[uint32]*ptySession)}
	for _, tool := range []string{"pty_write", "pty_read", "pty_resize", "pty_close"} {
		res := b.callTool(context.Background(), tool, json.RawMessage(`{"pid":99,"data":"x","cols":80,"rows":24}`))
		if !res.IsError || !strings.Contains(res.Content[0].Text, "no pty session") {
			t.Errorf("%s = %+v, want an unknown-session error", tool, res)
		}
	}
}

// TestPtyStartRejectsJailEscape proves the cwd goes through the same jail as
// shell, before any envd call (the bridge has no client here).
func TestPtyStartRejectsJailEscape(t *testing.T) {
	b := &bridge{workspace: "/srv/app", profile: profileOps, timeout: time.Second, sessions: make(map[uint32]*ptySession)}
	res := b.callTool(context.Background(), "pty_start", json.RawMessage(`{"command":"bash","cwd":"../../etc"}`))
	if !res.IsError || !strings.Contains(res.Content[0].Text, "outside the workspace") {
		t.Fatalf("pty_start = %+v, want a jail error", res)
	}
}
