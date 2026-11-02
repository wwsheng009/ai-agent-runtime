package remotetools_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// newJobsConfig builds an ops config with a target-local durable job root.
func newJobsConfig(t *testing.T, root string, allowed []string) *remotetools.Config {
	t.Helper()
	cfg := newOpsConfig(t, root, allowed)
	stateDir := t.TempDir()
	cfg.Jobs.Root = filepath.Join(stateDir, "jobs")
	cfg.Artifacts.Root = filepath.Join(stateDir, "artifacts")
	cfg.Limits.MaxOutputBytes = 4096
	return cfg
}

func TestJobLifecycleUsesCoreManager(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newJobsConfig(t, root, []string{"echo"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	body := callTool(t, ctx, session, "remote_dev__job_start", map[string]interface{}{
		"command": "echo job-output-marker",
	})
	jobID := extractJobID(t, body)
	if jobID == "" {
		t.Fatalf("no job id in response: %s", body)
	}

	deadline := time.Now().Add(10 * time.Second)
	var output string
	for time.Now().Before(deadline) {
		output = callTool(t, ctx, session, "remote_dev__job_output", map[string]interface{}{
			"job_id": jobID,
		})
		if strings.Contains(output, "completed") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(output, "job-output-marker") {
		t.Fatalf("job output missing marker: %s", output)
	}
	if !strings.Contains(output, "completed") {
		t.Fatalf("job did not reach the core terminal state: %s", output)
	}
}

func TestJobCancelUsesCoreManager(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newJobsConfig(t, root, []string{"sleep"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	body := callTool(t, ctx, session, "remote_dev__job_start", map[string]interface{}{
		"command": "sleep 60",
	})
	jobID := extractJobID(t, body)
	if jobID == "" {
		t.Fatalf("no job id in response: %s", body)
	}
	cancelBody := callTool(t, ctx, session, "remote_dev__job_cancel", map[string]interface{}{
		"job_id": jobID,
	})
	if !strings.Contains(cancelBody, "cancelled") && !strings.Contains(cancelBody, "completed") {
		t.Fatalf("unexpected cancel result: %s", cancelBody)
	}
}

func TestJobListReportsJobsOwnedBySession(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newJobsConfig(t, root, []string{"echo"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	callTool(t, ctx, session, "remote_dev__job_start", map[string]interface{}{"command": "echo one"})
	body := callTool(t, ctx, session, "remote_dev__job_list", map[string]interface{}{})
	if !strings.Contains(body, "job_") {
		t.Fatalf("job list missing entries: %s", body)
	}
}

// TestJobOutputSurfacesQueueSaturation locks the remote mapping of the core
// queue diagnostics. Before this, job_output said only "(no output yet; job is
// queued)" while all four execution slots were wedged, so the model had no way
// to distinguish normal queuing from a saturated scheduler.
func TestJobOutputSurfacesQueueSaturation(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newJobsConfig(t, root, []string{"echo", "sleep"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	running := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		body := callTool(t, ctx, session, "remote_dev__job_start", map[string]interface{}{
			"command": "sleep 30",
		})
		jobID := extractJobID(t, body)
		if jobID == "" {
			t.Fatalf("job_start %d returned no job id: %s", i, body)
		}
		running = append(running, jobID)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		list := callTool(t, ctx, session, "remote_dev__job_list", map[string]interface{}{})
		if strings.Count(list, "running") >= 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("four sleep jobs did not occupy the scheduler slots:\n%s", list)
		}
		time.Sleep(50 * time.Millisecond)
	}

	queuedBody := callTool(t, ctx, session, "remote_dev__job_start", map[string]interface{}{
		"command": "echo queued-probe",
	})
	queuedID := extractJobID(t, queuedBody)
	if queuedID == "" {
		t.Fatalf("queued job_start returned no job id: %s", queuedBody)
	}
	output := callTool(t, ctx, session, "remote_dev__job_output", map[string]interface{}{
		"job_id": queuedID,
	})
	if !strings.Contains(output, "Scheduler: saturated") {
		t.Fatalf("job_output hid the saturated scheduler state: %s", output)
	}
	if !strings.Contains(output, "4/4 slots active") {
		t.Fatalf("job_output hid the slot capacity: %s", output)
	}

	for _, jobID := range append(running, queuedID) {
		_, _ = session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "remote_dev__job_cancel",
			Arguments: map[string]interface{}{"job_id": jobID},
		})
	}
}

func TestRemoteShellFastCommandForgetsJobAndTimeoutPromotes(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newJobsConfig(t, root, []string{"echo", "sleep"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	fastResult := callToolResult(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": "echo fast-marker",
	})
	if fastResult.IsError {
		t.Fatalf("fast shell failed: %s", textContent(fastResult))
	}
	fast := textContent(fastResult)
	if !strings.Contains(fast, "fast-marker") {
		t.Fatalf("fast shell output missing marker: %s", fast)
	}
	if fastResult.Meta != nil {
		if _, ok := fastResult.Meta["job_id"]; ok {
			t.Fatalf("fast shell must not advertise a job id it is about to forget: %v", fastResult.Meta)
		}
	}
	list := callTool(t, ctx, session, "remote_dev__job_list", map[string]interface{}{})
	if !strings.Contains(list, "(no jobs)") {
		t.Fatalf("fast shell left a model-visible job: %s", list)
	}

	promotedResult := callToolResult(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command":    "sleep 30",
		"timeout_ms": 200,
	})
	if promotedResult.IsError {
		t.Fatalf("promoted shell failed: %s", textContent(promotedResult))
	}
	promoted := textContent(promotedResult)
	if !strings.Contains(promoted, "background job") {
		t.Fatalf("timeout did not promote to a job: %s", promoted)
	}
	if promotedResult.Meta == nil {
		t.Fatalf("promoted result is missing _meta: %v", promotedResult)
	}
	if _, ok := promotedResult.Meta["job_id"]; !ok {
		t.Fatalf("promoted result must retain its job id: %v", promotedResult.Meta)
	}
	jobID := extractJobID(t, promoted)
	if jobID == "" {
		t.Fatalf("promoted result missing job id: %s", promoted)
	}
	var output string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		output = callTool(t, ctx, session, "remote_dev__job_output", map[string]interface{}{
			"job_id": jobID,
		})
		if strings.Contains(output, "running") || strings.Contains(output, "completed") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(output, "running") && !strings.Contains(output, "completed") {
		t.Fatalf("promoted job is not readable through job_output: %s", output)
	}
	_ = callTool(t, ctx, session, "remote_dev__job_cancel", map[string]interface{}{"job_id": jobID})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		output = callTool(t, ctx, session, "remote_dev__job_output", map[string]interface{}{"job_id": jobID})
		if strings.Contains(output, "cancelled") || strings.Contains(output, "completed") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestReadOnlyProfileDoesNotExposeJobTools(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newOpsConfig(t, root, []string{"echo"})
	cfg.Profile = remotetools.ProfileReadonly
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	for _, name := range server.ToolNames() {
		if strings.Contains(name, "job_") || strings.Contains(name, "shell") {
			t.Fatalf("read-only surface exposed %q: %v", name, server.ToolNames())
		}
	}
}

func TestLegacyRemoteJobsPurgedOnStartup(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newJobsConfig(t, root, []string{"echo"})
	legacyDir := filepath.Join(cfg.Jobs.Root, "jobs", "job_legacy")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	record, _ := json.Marshal(map[string]interface{}{
		"id":         "job_legacy",
		"pid":        0,
		"started_at": time.Now().Add(-time.Hour).Format(time.RFC3339Nano),
		"state":      "exited",
	})
	if err := os.WriteFile(filepath.Join(legacyDir, "job.json"), record, 0o600); err != nil {
		t.Fatalf("write legacy record: %v", err)
	}
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	if _, statErr := os.Stat(legacyDir); !os.IsNotExist(statErr) {
		t.Fatalf("legacy job directory was not removed: %v", statErr)
	}
}

func TestNormalizeCreatesDefaultJobsRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfg := remotetools.DefaultConfig()
	cfg.Workspace = t.TempDir()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	want := filepath.Join(home, ".aicli", "remote-tools", "jobs")
	if filepath.Clean(cfg.Jobs.Root) != filepath.Clean(want) {
		t.Fatalf("jobs.root = %q, want %q", cfg.Jobs.Root, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("default jobs.root was not created: %v", err)
	}
	if cfg.Jobs.Retention.Duration != 7*24*time.Hour {
		t.Fatalf("jobs.retention = %s, want 168h", cfg.Jobs.Retention.Duration)
	}
}

func TestArtifactArchiveAndRead(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newJobsConfig(t, root, []string{"echo"})
	cfg.Limits.MaxOutputBytes = 256
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	if !containsTool(server.ToolNames(), "remote_dev__artifact_read") {
		t.Fatalf("artifact_read missing from surface: %v", server.ToolNames())
	}
	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": "echo " + strings.Repeat("A", 2000),
	})
	if !strings.Contains(body, "artifact_read") {
		t.Fatalf("expected an artifact pointer in folded output: %s", body)
	}
	artifactID := extractArtifactID(t, body)
	page := callTool(t, ctx, session, "remote_dev__artifact_read", map[string]interface{}{
		"artifact_id": artifactID,
		"offset":      0,
		"limit":       512,
	})
	if !strings.Contains(page, "AAAA") {
		t.Fatalf("artifact page missing content: %s", page)
	}
}

func TestArtifactReadRejectsTraversal(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newJobsConfig(t, root, []string{"echo"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_dev__artifact_read",
		Arguments: map[string]interface{}{"artifact_id": "../../etc/passwd"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected traversal-shaped artifact id to be refused")
	}
	if !strings.Contains(textContent(result), "INVALID_ARGUMENT") {
		t.Fatalf("unexpected error: %s", textContent(result))
	}
}

func TestArtifactStoreExpiry(t *testing.T) {
	store, err := remotetools.NewArtifactStore(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	id, err := store.Put([]byte("payload"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	content, size, eof, err := store.Read(id, 0, 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if content != "payload" || size != 7 || !eof {
		t.Fatalf("unexpected read: %q size=%d eof=%v", content, size, eof)
	}
	if _, _, _, err := store.Read("art_zzzz", 0, 10); err == nil {
		t.Fatal("expected malformed id to be refused")
	}
	if _, err := store.Put(make([]byte, 2048)); err == nil {
		t.Fatal("expected oversized payload to be refused")
	}
}

func TestJobStartPreservesQuotedPathOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows shell quoting")
	}
	root := newTestWorkspace(t)
	cfg := newJobsConfig(t, root, []string{"*"})
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	script := writeQuotedCmdProbe(t)
	body := callTool(t, ctx, session, "remote_dev__job_start", map[string]interface{}{
		"command": quotedProbeCommand(cfg.ResolvedShell.Type, script, "job hello"),
	})
	jobID := extractJobID(t, body)
	deadline := time.Now().Add(10 * time.Second)
	var output string
	for time.Now().Before(deadline) {
		output = callTool(t, ctx, session, "remote_dev__job_output", map[string]interface{}{
			"job_id": jobID,
		})
		if strings.Contains(output, "completed") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(output, "job hello") {
		t.Fatalf("job did not receive the quoted path/arg: %s", output)
	}
}

func TestJobStartPreservesQuotedPathWithCmdOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows cmd.exe quoting")
	}
	root := newTestWorkspace(t)
	cfg := newJobsConfig(t, root, []string{"*"})
	cfg.Shell.Program = "cmd"
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	script := writeQuotedCmdProbe(t)
	body := callTool(t, ctx, session, "remote_dev__job_start", map[string]interface{}{
		"command": quotedProbeCommand(runtimeexecutor.ShellTypeCmd, script, "cmd job hello"),
	})
	jobID := extractJobID(t, body)
	deadline := time.Now().Add(10 * time.Second)
	var output string
	for time.Now().Before(deadline) {
		output = callTool(t, ctx, session, "remote_dev__job_output", map[string]interface{}{
			"job_id": jobID,
		})
		if strings.Contains(output, "completed") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(output, "cmd job hello") {
		t.Fatalf("job did not receive the quoted cmd path/arg: %s", output)
	}
}

func extractJobID(t *testing.T, body string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		index := strings.Index(line, "job_")
		if index < 0 {
			continue
		}
		rest := line[index:]
		end := strings.IndexAny(rest, " \t\n);,")
		if end < 0 {
			end = len(rest)
		}
		return rest[:end]
	}
	return ""
}

func extractArtifactID(t *testing.T, body string) string {
	t.Helper()
	index := strings.Index(body, "art_")
	if index < 0 {
		t.Fatalf("no artifact id in output: %s", body)
	}
	rest := body[index:]
	end := strings.IndexAny(rest, " \t\n;,)]")
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

func containsTool(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
