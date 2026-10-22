package remotetools_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

func readAuditRecords(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	var records []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]interface{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode audit line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

// TestAuditRecordsCommandIdentity proves a successful shell record names the
// program, hashes the command, names the matched rule, and includes the opt-in
// preview.
func TestAuditRecordsCommandIdentity(t *testing.T) {
	root := newTestWorkspace(t)
	auditPath := filepath.Join(t.TempDir(), "audit.ndjson")
	cfg := newOpsConfig(t, root, []string{"echo"})
	cfg.Audit.Path = auditPath
	cfg.Audit.CommandPreviewBytes = 6

	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	session, ctx := startTestSession(t, server)
	callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": "echo hello",
	})
	if err := server.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readAuditRecords(t, auditPath)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	record := records[0]
	if record["program"] != "echo" {
		t.Fatalf("program = %v", record["program"])
	}
	hash, _ := record["command_hash"].(string)
	if len(hash) != 64 {
		t.Fatalf("command_hash = %q", hash)
	}
	if record["policy_rule"] != "allowedCommands:echo" {
		t.Fatalf("policy_rule = %v", record["policy_rule"])
	}
	if record["command_preview"] != "echo h" {
		t.Fatalf("command_preview = %v", record["command_preview"])
	}
	if record["tool_call_id"] == "" || record["tool_call_id"] == nil {
		t.Fatal("tool_call_id is missing")
	}
	if record["principal"] == "" || record["principal"] == nil {
		t.Fatal("principal is missing")
	}
}

// TestAuditRecordsDeniedCommand proves a policy refusal is traceable even though
// the tool never executed: program, hash and the failing rule are all recorded.
func TestAuditRecordsDeniedCommand(t *testing.T) {
	root := newTestWorkspace(t)
	auditPath := filepath.Join(t.TempDir(), "audit.ndjson")
	cfg := newOpsConfig(t, root, []string{"echo"})
	cfg.Audit.Path = auditPath

	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	session, ctx := startTestSession(t, server)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_dev__shell",
		Arguments: map[string]interface{}{"command": "whoami"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected whoami to be denied")
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readAuditRecords(t, auditPath)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	record := records[0]
	if record["decision"] != "deny" {
		t.Fatalf("decision = %v", record["decision"])
	}
	if record["reason"] != "COMMAND_DENIED" {
		t.Fatalf("reason = %v", record["reason"])
	}
	if record["program"] != "whoami" {
		t.Fatalf("program = %v", record["program"])
	}
	if record["policy_rule"] != "policy:not-allowed" {
		t.Fatalf("policy_rule = %v", record["policy_rule"])
	}
	if hash, _ := record["command_hash"].(string); len(hash) != 64 {
		t.Fatalf("command_hash = %q", hash)
	}
}

// TestAuditRecordsExitCode proves a real command failure is classified as an
// execution error (not a policy denial) and carries the process exit code.
func TestAuditRecordsExitCode(t *testing.T) {
	root := newTestWorkspace(t)
	auditPath := filepath.Join(t.TempDir(), "audit.ndjson")
	cfg := newOpsConfig(t, root, []string{"git status"})
	cfg.Audit.Path = auditPath

	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	session, ctx := startTestSession(t, server)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_dev__shell",
		Arguments: map[string]interface{}{"command": "git status"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected git status to fail outside a repository")
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	records := readAuditRecords(t, auditPath)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	record := records[0]
	if record["decision"] != "error" {
		t.Fatalf("decision = %v", record["decision"])
	}
	if record["reason"] != "COMMAND_FAILED" {
		t.Fatalf("reason = %v", record["reason"])
	}
	if record["program"] != "git" {
		t.Fatalf("program = %v", record["program"])
	}
	if code, ok := record["exit_code"].(float64); !ok || code == 0 {
		t.Fatalf("exit_code = %v", record["exit_code"])
	}
}

// TestAuditRecordsRequestInfo proves the decorator adds a call id and a
// principal even for the in-memory stdio test session.
func TestAuditRecordsRequestInfo(t *testing.T) {
	root := newTestWorkspace(t)
	auditPath := filepath.Join(t.TempDir(), "audit.ndjson")
	cfg := newOpsConfig(t, root, []string{"echo"})
	cfg.Audit.Path = auditPath

	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	session, ctx := startTestSession(t, server)
	callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": "echo hello",
	})
	if err := server.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	record := readAuditRecords(t, auditPath)[0]
	if record["tool_call_id"] == nil || record["tool_call_id"] == "" {
		t.Fatal("tool_call_id is missing")
	}
	if record["principal"] == nil || record["principal"] == "" {
		t.Fatal("principal is missing")
	}
}
