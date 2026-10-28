package main

import (
	"bytes"
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
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
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

// testCommandPolicy builds the shared policy with exactly the listed argv
// prefixes. Metacharacter rejection stays on, matching the bridge default.
func testCommandPolicy(t *testing.T, allowed ...string) *remotetools.CommandPolicy {
	t.Helper()
	policy, err := remotetools.NewCommandPolicy(remotetools.ShellConfig{
		AllowedCommands:    allowed,
		DenyShellMetachars: true,
	}, time.Minute)
	if err != nil {
		t.Fatalf("NewCommandPolicy: %v", err)
	}
	return policy
}

// fakeResolveClient answers the bridge's path-resolution process call with one
// fixed canonical path.
func fakeResolveClient(t *testing.T, resolved string) *envdclient.Client {
	t.Helper()
	return fakeEnvDClient(t, func(w http.ResponseWriter) {
		writeConnectFrame(t, w, map[string]any{"event": map[string]any{"start": map[string]any{"pid": 7}}})
		writeConnectFrame(t, w, map[string]any{"event": map[string]any{"data": map[string]any{
			"stdout": base64.StdEncoding.EncodeToString([]byte(resolved + "\n")),
		}}})
		writeConnectFrame(t, w, map[string]any{"event": map[string]any{"end": map[string]any{
			"exited": true, "status": "exit status 0",
		}}})
	})
}

// fakeResolveClientMap answers resolution calls by target path. Unlike
// fakeResolveClient it can distinguish "resolve the parent" from "resolve the
// final component", which is the contract mutation paths depend on.
func fakeResolveClientMap(t *testing.T, resolutions map[string]string) *envdclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/process.Process/Start" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		idx := bytes.IndexByte(body, '{')
		var req struct {
			Process struct {
				Args []string `json:"args"`
			} `json:"process"`
		}
		if idx < 0 || json.Unmarshal(body[idx:], &req) != nil || len(req.Process.Args) < 4 {
			http.Error(w, "bad resolve request", http.StatusBadRequest)
			return
		}
		target := req.Process.Args[len(req.Process.Args)-1]
		resolved, ok := resolutions[target]
		if !ok {
			http.Error(w, "unexpected resolve target "+target, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/connect+json")
		writeConnectFrame(t, w, map[string]any{"event": map[string]any{"start": map[string]any{"pid": 7}}})
		writeConnectFrame(t, w, map[string]any{"event": map[string]any{"data": map[string]any{
			"stdout": base64.StdEncoding.EncodeToString([]byte(resolved + "\n")),
		}}})
		writeConnectFrame(t, w, map[string]any{"event": map[string]any{"end": map[string]any{
			"exited": true, "status": "exit status 0",
		}}})
	}))
	t.Cleanup(srv.Close)

	client, err := envdclient.Dial(envdclient.Config{Addr: srv.URL, User: "user"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return client
}

// TestStringListFlagPreservesCommandText ensures command entries are not passed
// through path.Clean (which would rewrite double spaces and glob characters).
func TestStringListFlagPreservesCommandText(t *testing.T) {
	var flag stringListFlag
	if err := flag.Set("git  status"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := flag.Set("  host-*.sh  "); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := strings.Join(flag, "|"); got != "git  status|host-*.sh" {
		t.Fatalf("flag = %q, want the command text preserved", got)
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
		{profileReadonly, "watch_dir", true},
		{profileReadonly, "write", false},
		{profileReadonly, "shell", false},
		{profileStandard, "write", true},
		{profileStandard, "move", true},
		{profileStandard, "watch_dir", true},
		{profileStandard, "shell", false},
		{profileStandard, "pty_start", false},
		{profileOps, "shell", true},
		{profileOps, "pty_start", true},
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
		{profileReadonly, 6},
		{profileStandard, 10},
		{profileOps, 16},
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
		{"newline in path", "sub/\nfile", "", true},
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

// TestResolvePathRejectsSymlinkEscape covers the gap a lexical jail cannot see:
// the target resolves the in-workspace path to /etc, so the operation must stop
// before any read.
func TestResolvePathRejectsSymlinkEscape(t *testing.T) {
	b := &bridge{
		client:    fakeResolveClient(t, "/etc"),
		workspace: "/srv/app",
		timeout:   5 * time.Second,
	}
	got, err := b.resolvePath(context.Background(), "link/passwd")
	if err == nil {
		t.Fatalf("resolvePath = %q, want a symlink-escape error", got)
	}
	if !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("error = %v, want it to name the workspace fence", err)
	}
}

// TestResolvePathUsesCanonicalTarget proves the operation receives the resolved
// path, not the original lexical one.
func TestResolvePathUsesCanonicalTarget(t *testing.T) {
	b := &bridge{
		client:    fakeResolveClient(t, "/srv/app/real/file"),
		workspace: "/srv/app",
		timeout:   5 * time.Second,
	}
	got, err := b.resolvePath(context.Background(), "link")
	if err != nil {
		t.Fatalf("resolvePath: %v", err)
	}
	if got != "/srv/app/real/file" {
		t.Fatalf("resolvePath = %q, want the canonical target", got)
	}
}

// TestResolveMutationRejectsIntermediateSymlink covers remove/move: the final
// component may be a symlink, but a symlink in a parent component must not
// redirect the operation outside.
func TestResolveMutationRejectsIntermediateSymlink(t *testing.T) {
	b := &bridge{
		client:    fakeResolveClient(t, "/etc"),
		workspace: "/srv/app",
		timeout:   5 * time.Second,
	}
	got, err := b.resolveMutationPath(context.Background(), "link/file")
	if err == nil {
		t.Fatalf("resolveMutationPath = %q, want an escape error", got)
	}
	if !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("error = %v, want it to name the workspace fence", err)
	}
}

// TestResolveMutationAllowsFinalSymlink locks the unlink/rename semantics: the
// final component is a link to be removed or renamed, so only its parent is
// resolved. The fake fails if the implementation asks it to resolve /srv/app/link.
func TestResolveMutationAllowsFinalSymlink(t *testing.T) {
	b := &bridge{
		client:    fakeResolveClientMap(t, map[string]string{"/srv/app": "/srv/app"}),
		workspace: "/srv/app",
		timeout:   5 * time.Second,
	}
	got, err := b.resolveMutationPath(context.Background(), "link")
	if err != nil {
		t.Fatalf("resolveMutationPath: %v", err)
	}
	if got != "/srv/app/link" {
		t.Fatalf("resolveMutationPath = %q, want the link path unchanged", got)
	}
}

// TestRemoveProtectsWorkspaceRoot prevents a model from deleting the jail root
// through the filesystem tool.
func TestRemoveProtectsWorkspaceRoot(t *testing.T) {
	b := testBridge(profileStandard, "")
	res := b.callTool(context.Background(), "remove", json.RawMessage(`{"path":"."}`))
	if !res.IsError || !strings.Contains(res.Content[0].Text, "protected root") {
		t.Fatalf("remove(.) = %+v, want a protected-root refusal", res)
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

	b := &bridge{
		client:    client,
		workspace: "/srv/app",
		profile:   profileOps,
		timeout:   5 * time.Second,
		commands:  testCommandPolicy(t, "echo"),
	}
	res := b.callTool(context.Background(), "shell", json.RawMessage(`{"command":"echo echoed"}`))
	if res.IsError {
		t.Fatalf("shell reported an error: %+v", res)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "echoed") || !strings.Contains(text, "[exit 0]") {
		t.Fatalf("shell output = %q, want the stdout and the exit status", text)
	}
}

// TestShellCommandPolicyFailClosed pins the default: the ops profile alone is not
// enough to run a command. envd has no filter, so an empty allowlist must deny.
func TestShellCommandPolicyFailClosed(t *testing.T) {
	b := &bridge{
		workspace: "/srv/app",
		profile:   profileOps,
		timeout:   time.Second,
		commands:  testCommandPolicy(t),
	}
	res := b.callTool(context.Background(), "shell", json.RawMessage(`{"command":"id"}`))
	if !res.IsError || !strings.Contains(res.Content[0].Text, "fail-closed") {
		t.Fatalf("shell = %+v, want a fail-closed policy error", res)
	}
}

// TestShellCommandPolicyDenies proves an unlisted command never reaches envd.
func TestShellCommandPolicyDenies(t *testing.T) {
	b := &bridge{
		workspace: "/srv/app",
		profile:   profileOps,
		timeout:   time.Second,
		commands:  testCommandPolicy(t, "echo"),
	}
	res := b.callTool(context.Background(), "shell", json.RawMessage(`{"command":"id"}`))
	if !res.IsError {
		t.Fatalf("unlisted command was accepted: %+v", res)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "policy:not-allowed") || !strings.Contains(text, "COMMAND_DENIED") {
		t.Fatalf("error = %q, want the not-allowed rule and error code", text)
	}
}

// TestShellCommandPolicyDenyWins checks that a deny entry overrides a broader
// allow entry, matching aicli-remote-tools.
func TestShellCommandPolicyDenyWins(t *testing.T) {
	policy, err := remotetools.NewCommandPolicy(remotetools.ShellConfig{
		AllowedCommands:    []string{"echo"},
		DeniedCommands:     []string{"echo secret"},
		DenyShellMetachars: true,
	}, time.Minute)
	if err != nil {
		t.Fatalf("NewCommandPolicy: %v", err)
	}
	b := &bridge{
		workspace: "/srv/app",
		profile:   profileOps,
		timeout:   time.Second,
		commands:  policy,
	}
	res := b.callTool(context.Background(), "shell", json.RawMessage(`{"command":"echo secret"}`))
	if !res.IsError || !strings.Contains(res.Content[0].Text, "deniedCommands:echo secret") {
		t.Fatalf("shell = %+v, want the deny rule to win", res)
	}
}

// TestShellCommandPolicyRejectsMetachars covers the envd-specific risk: the
// command is handed to /bin/sh -c, so a chained command must be refused even when
// its first word is allowed.
func TestShellCommandPolicyRejectsMetachars(t *testing.T) {
	b := &bridge{
		workspace: "/srv/app",
		profile:   profileOps,
		timeout:   time.Second,
		commands:  testCommandPolicy(t, "echo"),
	}
	res := b.callTool(context.Background(), "shell", json.RawMessage(`{"command":"echo ok; id"}`))
	if !res.IsError || !strings.Contains(res.Content[0].Text, "policy:metachars") {
		t.Fatalf("shell = %+v, want a metacharacter refusal", res)
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

// TestAuditRecordsMetadataOnly locks the audit contract: one JSON object per
// call, and never the argument payload (which can contain file bodies or
// commands the operator would not want duplicated into a log).
func TestAuditRecordsMetadataOnly(t *testing.T) {
	var buf bytes.Buffer
	b := &bridge{
		workspace: "/srv/app",
		profile:   profileReadonly,
		timeout:   5 * time.Second,
		audit:     newAuditLogWriter(&buf),
	}
	res := b.callTool(context.Background(), "shell", json.RawMessage(`{"command":"echo do-not-log"}`))
	if !res.IsError {
		t.Fatalf("shell was allowed under readonly: %+v", res)
	}

	line := strings.TrimSpace(buf.String())
	if strings.Contains(line, "do-not-log") {
		t.Fatalf("audit log leaked argument content: %s", line)
	}
	var entry auditEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("decode audit line %q: %v", line, err)
	}
	if entry.Tool != "shell" || entry.Profile != "readonly" || entry.OK {
		t.Fatalf("entry = %+v, want a failed shell call under readonly", entry)
	}
	if entry.ArgumentBytes == 0 || entry.Timestamp == "" {
		t.Fatalf("entry = %+v, want argument size and timestamp", entry)
	}
}

// TestAuditMarksSuccessfulCalls proves the ok flag and target identity follow the
// result the model saw.
func TestAuditMarksSuccessfulCalls(t *testing.T) {
	client := fakeEnvDClient(t, nil)
	var buf bytes.Buffer
	b := &bridge{
		client:    client,
		workspace: "/srv/app",
		profile:   profileReadonly,
		timeout:   5 * time.Second,
		audit:     newAuditLogWriter(&buf),
	}
	if res := b.callTool(context.Background(), "host_metrics", json.RawMessage(`{}`)); res.IsError {
		t.Fatalf("host_metrics failed: %+v", res)
	}

	var entry auditEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry); err != nil {
		t.Fatalf("decode audit line: %v", err)
	}
	if !entry.OK || entry.Tool != "host_metrics" {
		t.Fatalf("entry = %+v, want a successful host_metrics call", entry)
	}
	if entry.Target == "" || entry.User != "user" {
		t.Fatalf("entry = %+v, want the target address and executing user", entry)
	}
}

// TestAuditRecordsShellPolicyAndExit locks the command-policy and exit metadata
// contract: enough to explain a decision, without command output or arguments.
func TestAuditRecordsShellPolicyAndExit(t *testing.T) {
	client := fakeEnvDClient(t, func(w http.ResponseWriter) {
		writeConnectFrame(t, w, map[string]any{"event": map[string]any{"start": map[string]any{"pid": 42}}})
		writeConnectFrame(t, w, map[string]any{"event": map[string]any{"data": map[string]any{
			"stdout": base64.StdEncoding.EncodeToString([]byte("ok\n")),
		}}})
		writeConnectFrame(t, w, map[string]any{"event": map[string]any{"end": map[string]any{
			"exited": true, "status": "exit status 0",
		}}})
	})
	var buf bytes.Buffer
	b := &bridge{
		client:    client,
		workspace: "/srv/app",
		profile:   profileOps,
		timeout:   5 * time.Second,
		commands:  testCommandPolicy(t, "echo"),
		audit:     newAuditLogWriter(&buf),
	}
	if res := b.callTool(context.Background(), "shell", json.RawMessage(`{"command":"echo ok"}`)); res.IsError {
		t.Fatalf("shell failed: %+v", res)
	}

	line := strings.TrimSpace(buf.String())
	var entry auditEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("decode audit line: %v", err)
	}
	if !entry.OK || entry.PolicyRule != "allowedCommands:echo" {
		t.Fatalf("entry = %+v, want an allowed echo rule", entry)
	}
	if entry.ExitCode == nil || *entry.ExitCode != 0 {
		t.Fatalf("exit_code = %v, want 0", entry.ExitCode)
	}
	if entry.OutputBytes != len("ok\n") {
		t.Fatalf("output_bytes = %d, want %d", entry.OutputBytes, len("ok\n"))
	}
	if strings.Contains(line, "echo ok") || strings.Contains(line, `"ok\n"`) {
		t.Fatalf("audit log leaked command or output content: %s", line)
	}
}

// TestAuditRecordsDeniedPolicyRule proves a refused command is auditable without
// reaching envd.
func TestAuditRecordsDeniedPolicyRule(t *testing.T) {
	var buf bytes.Buffer
	b := &bridge{
		workspace: "/srv/app",
		profile:   profileOps,
		timeout:   time.Second,
		commands:  testCommandPolicy(t, "echo"),
		audit:     newAuditLogWriter(&buf),
	}
	if res := b.callTool(context.Background(), "shell", json.RawMessage(`{"command":"id"}`)); !res.IsError {
		t.Fatalf("shell was allowed: %+v", res)
	}

	var entry auditEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry); err != nil {
		t.Fatalf("decode audit line: %v", err)
	}
	if entry.OK || entry.PolicyRule != "policy:not-allowed" {
		t.Fatalf("entry = %+v, want a denied policy rule", entry)
	}
	if entry.ExitCode != nil || entry.OutputBytes != 0 {
		t.Fatalf("entry = %+v, want no exit/output for a denied command", entry)
	}
}

// TestCallTimeoutAbortsSlowCall proves the per-call deadline also bounds a
// streaming tool call, not only unary requests.
func TestCallTimeoutAbortsSlowCall(t *testing.T) {
	client := fakeEnvDClient(t, func(w http.ResponseWriter) {
		// Flush headers so Run reaches Recv, then stall past the deadline.
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(400 * time.Millisecond)
	})
	b := &bridge{
		client:    client,
		workspace: "/srv/app",
		profile:   profileOps,
		timeout:   60 * time.Millisecond,
		commands:  testCommandPolicy(t, "sleep"),
	}

	started := time.Now()
	res := b.callTool(context.Background(), "shell", json.RawMessage(`{"command":"sleep 5"}`))
	elapsed := time.Since(started)
	if !res.IsError {
		t.Fatalf("slow call was not aborted: %+v", res)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("call took %v, want it bounded by the 60ms deadline", elapsed)
	}
	if text := strings.ToLower(res.Content[0].Text); !strings.Contains(text, "deadline") {
		t.Fatalf("error = %q, want a deadline error", res.Content[0].Text)
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
