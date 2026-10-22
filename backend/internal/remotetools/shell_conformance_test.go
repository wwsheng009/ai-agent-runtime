package remotetools_test

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// TestRemoteShellSchemaIsFinalResultOnly pins the v1 local-only boundary from
// design §11.1 / Phase 4: the remote shell accepts one final-result command and
// must never advertise interactive stdin, PTY, detach or job control.
func TestRemoteShellSchemaIsFinalResultOnly(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"echo"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	session, ctx := startTestSession(t, server)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var shell *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "remote_dev__shell" {
			shell = tool
			break
		}
	}
	if shell == nil {
		t.Fatalf("remote shell tool missing: %v", tools.Tools)
	}
	schema, ok := shell.InputSchema.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected schema type %T", shell.InputSchema)
	}
	properties, _ := schema["properties"].(map[string]interface{})
	for _, forbidden := range []string{"stdin", "detach", "pty", "job_id", "interactive"} {
		if _, exists := properties[forbidden]; exists {
			t.Errorf("remote shell schema must not expose %q: %#v", forbidden, properties)
		}
	}
	for _, required := range []string{"command", "workdir", "timeout_ms"} {
		if _, exists := properties[required]; !exists {
			t.Errorf("remote shell schema missing %q: %#v", required, properties)
		}
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("remote shell schema must reject unknown args: %#v", schema["additionalProperties"])
	}
}

// TestRemoteShellRejectsUnknownArguments proves the schema boundary is also
// enforced at call time: a caller that sends stdin/detach/pty must be refused
// instead of having the field silently ignored.
func TestRemoteShellRejectsUnknownArguments(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"echo"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	for _, forbidden := range []string{"stdin", "detach", "pty", "job_id"} {
		args := map[string]interface{}{"command": "echo hi", forbidden: "x"}
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "remote_dev__shell",
			Arguments: args,
		})
		if callErr != nil {
			// Some protocol layers reject an unknown argument as a protocol
			// error instead of an IsError result; both are fail-closed.
			continue
		}
		if !result.IsError {
			t.Errorf("expected %q to be refused, got %s", forbidden, textContent(result))
		}
	}
}

// TestRemoteShellFoldKeepsTailAndArtifact proves the remote surface uses the
// same capture -> fold -> artifact ordering as the local shell: the tail
// verdict stays visible and the omitted middle is pageable.
func TestRemoteShellFoldKeepsTailAndArtifact(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newJobsConfig(t, root, []string{"echo"})
	cfg.Limits.MaxOutputBytes = 4096
	cfg.Limits.ModelVisibleCap = 1024
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	// Build one line larger than the model-visible window, with a distinctive
	// tail. A single echo keeps this inside the remote metachar policy.
	command := "echo " + strings.Repeat("A", 2000) + " TAIL-VERDICT-OK"
	if runtime.GOOS == "windows" {
		command = "echo " + strings.Repeat("A", 2000) + " TAIL-VERDICT-OK"
	}
	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": command,
	})
	if !strings.Contains(body, "TAIL-VERDICT-OK") {
		t.Fatalf("folded output lost the tail verdict: %s", body)
	}
	if !strings.Contains(body, "artifact_read") {
		t.Fatalf("folded output lost the artifact pointer: %s", body)
	}
}

// TestRemoteShellMetadataIsStructured pins the final-result fields the remote
// surface promises, without relying on model-facing prose.
func TestRemoteShellMetadataIsStructured(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"echo"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_dev__shell",
		Arguments: map[string]interface{}{"command": "echo metadata-ok"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", textContent(result))
	}
	// The MCP adapter renders metadata into the text payload and Meta; the
	// in-memory SDK does not populate StructuredContent for this tool shape.
	// Assert the stable keys the final-result contract promises.
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatalf("marshal result: %v", marshalErr)
	}
	for _, key := range []string{"exit_code", "duration_ms", "timed_out", "remote"} {
		if !strings.Contains(string(encoded), key) {
			t.Errorf("result missing %q: %s", key, encoded)
		}
	}
}
