package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
)

// testBridge returns a configured bridge without a client.
//
// Tests that never reach envd build on this; the jail, the profile gate and the
// write cap all run before the first network call.
func testBridge(p profile, prefix string) *bridge {
	return &bridge{
		workspace: "/srv/app",
		prefix:    prefix,
		profile:   p,
		timeout:   5 * time.Second,
	}
}

// TestPrefixSeparatorMatchesRemoteTools pins the tool-name spelling.
//
// aicli-remote-tools advertises "<prefix>__<tool>"; the bridge must use the same
// separator or one target exposes two naming conventions depending on transport.
func TestPrefixSeparatorMatchesRemoteTools(t *testing.T) {
	b := testBridge(profileReadonly, "remote_10-0-0-9")
	if got := b.prefixed("view"); got != "remote_10-0-0-9__view" {
		t.Fatalf("prefixed(view) = %q, want remote_10-0-0-9__view", got)
	}
	empty := testBridge(profileReadonly, "")
	if got := empty.prefixed("view"); got != "view" {
		t.Fatalf("prefixed(view) without a prefix = %q, want view", got)
	}
}

// TestUnprefixedToleratesBareAndPrefixedNames covers the call side of the naming
// convention.
func TestUnprefixedToleratesBareAndPrefixedNames(t *testing.T) {
	b := testBridge(profileReadonly, "remote_10-0-0-9")
	if got := b.unprefixed("remote_10-0-0-9__view"); got != "view" {
		t.Fatalf("unprefixed(prefixed name) = %q, want view", got)
	}
	if got := b.unprefixed("view"); got != "view" {
		t.Fatalf("unprefixed(bare name) = %q, want view", got)
	}
	// The old concatenation without a separator no longer resolves; that is the
	// point of the change, so lock it in rather than leave it ambiguous.
	if got := b.unprefixed("remote_10-0-0-9view"); got != "remote_10-0-0-9view" {
		t.Fatalf("unprefixed(legacy concatenation) = %q, want it left untouched", got)
	}
}

// TestProfileAllows locks the tier ladder.
func TestProfileAllows(t *testing.T) {
	cases := []struct {
		profile profile
		tool    string
		want    bool
	}{
		{profileReadonly, "view", true},
		{profileReadonly, "host_metrics", true},
		{profileReadonly, "write", false},
		{profileReadonly, "shell", false},
		{profileStandard, "write", true},
		{profileStandard, "move", true},
		{profileStandard, "shell", false},
		{profileOps, "shell", true},
		{profileOps, "view", true},
		{profileOps, "no_such_tool", false},
		{profile("nonsense"), "view", false},
	}
	for _, tc := range cases {
		if got := tc.profile.allows(tc.tool); got != tc.want {
			t.Errorf("%s.allows(%q) = %v, want %v", tc.profile, tc.tool, got, tc.want)
		}
	}
}

// TestToolListRespectsProfileAndPrefix proves the catalog the model sees is both
// gated and prefixed.
func TestToolListRespectsProfileAndPrefix(t *testing.T) {
	cases := []struct {
		profile profile
		count   int
	}{
		{profileReadonly, 5},
		{profileStandard, 9},
		{profileOps, 10},
	}
	for _, tc := range cases {
		b := testBridge(tc.profile, "remote_10-0-0-9")
		list := b.toolList()
		if len(list) != tc.count {
			t.Errorf("%s toolList length = %d, want %d", tc.profile, len(list), tc.count)
		}
		for _, def := range list {
			if !strings.HasPrefix(def.Name, "remote_10-0-0-9__") {
				t.Errorf("%s tool %q is not prefixed", tc.profile, def.Name)
			}
		}
	}
}

// TestResolveJail pins the path fence: traversal, absolute escapes and
// prefix-boundary near-misses must all be refused.
func TestResolveJail(t *testing.T) {
	b := &bridge{workspace: "/srv/app", roots: []string{"/srv/data"}}
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"relative inside", "sub/file.txt", "/srv/app/sub/file.txt", false},
		{"absolute inside", "/srv/app/sub", "/srv/app/sub", false},
		{"dot resolves to workspace", ".", "/srv/app", false},
		{"parent traversal", "../etc/passwd", "", true},
		{"absolute outside", "/etc/passwd", "", true},
		{"sibling prefix", "/srv/appx", "", true},
		{"allowed root", "/srv/data/f", "/srv/data/f", false},
		{"allowed root prefix", "/srv/database", "", true},
		{"empty path", "   ", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := b.resolve(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolve(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("resolve(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestViewRejectsPathOutsideWorkspace proves the jail runs before any envd call:
// the bridge here has no client, so reaching envd would panic instead of erroring.
func TestViewRejectsPathOutsideWorkspace(t *testing.T) {
	b := testBridge(profileReadonly, "")
	res := b.callTool(context.Background(), "view", json.RawMessage(`{"path":"../../etc/passwd"}`))
	if !res.IsError {
		t.Fatalf("view outside the workspace was allowed: %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, "outside the workspace") {
		t.Errorf("error text = %q, want it to name the workspace fence", res.Content[0].Text)
	}
}

// TestShellIsGatedToOpsProfile locks the one rule that matters most: envd applies
// no command filtering, so shell must stay behind the ops tier.
func TestShellIsGatedToOpsProfile(t *testing.T) {
	b := testBridge(profileReadonly, "")
	res := b.callTool(context.Background(), "shell", json.RawMessage(`{"command":"id"}`))
	if !res.IsError {
		t.Fatalf("shell was available under the readonly profile: %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, "not available under the readonly profile") {
		t.Errorf("error text = %q, want a profile refusal", res.Content[0].Text)
	}
}

// TestWriteLimitIsEnforced checks the argv cap before any client call, again with
// no client configured.
func TestWriteLimitIsEnforced(t *testing.T) {
	b := testBridge(profileStandard, "")
	payload, err := json.Marshal(map[string]string{
		"path":    "sub/big.txt",
		"content": strings.Repeat("a", maxWriteBytes+1),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res := b.callTool(context.Background(), "write", payload)
	if !res.IsError {
		t.Fatalf("oversized write was accepted: %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, "write limit") {
		t.Errorf("error text = %q, want the write limit message", res.Content[0].Text)
	}
}

// TestShellRunThroughFakeEnvD drives one full tool call through the real client
// code against a stub envd, so envelope framing, base64 decoding, the end event
// and the shell result formatting are exercised together.
func TestShellRunThroughFakeEnvD(t *testing.T) {
	client := fakeEnvDClient(t, func(w http.ResponseWriter) {
		writeConnectFrame(t, w, map[string]any{
			"event": map[string]any{"start": map[string]any{"pid": 42}},
		})
		writeConnectFrame(t, w, map[string]any{
			"event": map[string]any{"data": map[string]any{
				"stdout": base64.StdEncoding.EncodeToString([]byte("echoed\n")),
			}},
		})
		writeConnectFrame(t, w, map[string]any{
			"event": map[string]any{"end": map[string]any{"exited": true, "status": "exit status 0"}},
		})
	})

	b := &bridge{client: client, workspace: "/srv/app", profile: profileOps, timeout: 5 * time.Second}
	res := b.callTool(context.Background(), "shell", json.RawMessage(`{"command":"echo echoed"}`))
	if res.IsError {
		t.Fatalf("shell reported an error: %+v", res)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "echoed") || !strings.Contains(text, "[exit 0]") {
		t.Fatalf("shell output = %q, want the stdout and the exit status", text)
	}
}

// TestMetricsThroughFakeEnvD covers the plain-HTTP metrics path.
func TestMetricsThroughFakeEnvD(t *testing.T) {
	client := fakeEnvDClient(t, nil)

	b := &bridge{client: client, workspace: "/srv/app", profile: profileReadonly, timeout: 5 * time.Second}
	res := b.callTool(context.Background(), "host_metrics", json.RawMessage(`{}`))
	if res.IsError {
		t.Fatalf("host_metrics reported an error: %+v", res)
	}
	if text := res.Content[0].Text; !strings.Contains(text, "cpus: 2") {
		t.Fatalf("host_metrics output = %q, want the CPU count", text)
	}
	if text := res.Content[0].Text; !strings.Contains(text, "memory available: 512 MiB") {
		t.Fatalf("host_metrics output = %q, want the reported available memory", text)
	}
}

// fakeEnvDClient serves the two endpoints the bridge uses, so tool calls can be
// tested without a real envd. startHandler, when non-nil, writes the framed
// response body for process.Process/Start.
func fakeEnvDClient(t *testing.T, startHandler func(http.ResponseWriter)) *envdclient.Client {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/process.Process/Start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Start method = %s, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/connect+json")
		if startHandler != nil {
			startHandler(w)
		}
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("metrics method = %s, want GET", r.Method)
		}
		fmt.Fprint(w, `{"ts":1,"cpu_count":2,"mem_total":1073741824,"mem_used":536870912,"available":536870912,"disk_used":1048576,"disk_total":10737418240}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := envdclient.Dial(envdclient.Config{Addr: srv.URL, User: "user"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return client
}

// writeConnectFrame emits one Connect envelope frame, the same encoding the real
// client decodes.
func writeConnectFrame(t *testing.T, w io.Writer, payload any) {
	t.Helper()
	frame, err := envdclient.EncodeJSONFrame(payload)
	if err != nil {
		t.Fatalf("EncodeJSONFrame: %v", err)
	}
	if _, err := w.Write(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}
