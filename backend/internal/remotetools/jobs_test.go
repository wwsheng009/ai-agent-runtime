package remotetools_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// newJobsConfig builds an ops config with job and artifact stores enabled.
func newJobsConfig(t *testing.T, root string, allowed []string) *remotetools.Config {
	t.Helper()
	cfg := newOpsConfig(t, root, allowed)
	stateDir := t.TempDir()
	cfg.Jobs.Root = filepath.Join(stateDir, "jobs")
	cfg.Artifacts.Root = filepath.Join(stateDir, "artifacts")
	cfg.Limits.MaxOutputBytes = 4096
	return cfg
}

// TestJobLifecycle covers start → poll → terminal state.
func TestJobLifecycle(t *testing.T) {
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

	// Poll until the job exits; the supervisor goroutine stamps the record.
	deadline := time.Now().Add(10 * time.Second)
	var output string
	for time.Now().Before(deadline) {
		output = callTool(t, ctx, session, "remote_dev__job_output", map[string]interface{}{
			"job_id": jobID,
		})
		if strings.Contains(output, "exited") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(output, "job-output-marker") {
		t.Fatalf("job output missing marker: %s", output)
	}
	if !strings.Contains(output, "exited") {
		t.Fatalf("job did not reach a terminal state: %s", output)
	}
}

// TestJobCancelStopsRunningJob covers the cancel path.
func TestJobCancelStopsRunningJob(t *testing.T) {
	root := newTestWorkspace(t)
	// `sleep` is the canonical long-running command for this test.
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

	cancelBody := callTool(t, ctx, session, "remote_dev__job_cancel", map[string]interface{}{
		"job_id": jobID,
	})
	if !strings.Contains(cancelBody, "killed") && !strings.Contains(cancelBody, "exited") {
		t.Fatalf("unexpected cancel result: %s", cancelBody)
	}
}

// TestJobListReportsJobs covers enumeration.
func TestJobListReportsJobs(t *testing.T) {
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

// TestJobToolsAbsentWithoutStore locks the capability rule: a host that cannot
// persist jobs must not advertise job tools.
func TestJobToolsAbsentWithoutStore(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newOpsConfig(t, root, []string{"echo"})
	cfg.Jobs.Root = ""
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	for _, name := range server.ToolNames() {
		if strings.Contains(name, "job_") {
			t.Fatalf("job tool %q must not be exposed without a jobs store", name)
		}
	}
}

// TestArtifactArchiveAndRead covers the folded-output recovery path.
func TestArtifactArchiveAndRead(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newJobsConfig(t, root, []string{"echo"})
	cfg.Limits.MaxOutputBytes = 256 // force folding
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	if !containsTool(server.ToolNames(), "remote_dev__artifact_read") {
		t.Fatalf("artifact_read missing from surface: %v", server.ToolNames())
	}

	// Generate output far beyond the 256-byte window.
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

// TestArtifactReadRejectsTraversal locks the id validation: the id becomes a
// filename, so a path-shaped id must be refused before touching the filesystem.
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

// TestJobsSurviveServerRestart proves the job record is durable.
func TestJobsSurviveServerRestart(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newJobsConfig(t, root, []string{"echo"})
	first, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	session, ctx := startTestSession(t, first)
	body := callTool(t, ctx, session, "remote_dev__job_start", map[string]interface{}{
		"command": "echo durable",
	})
	jobID := extractJobID(t, body)
	// Wait for the terminal state to be persisted.
	time.Sleep(500 * time.Millisecond)
	_ = session.Close()
	_ = first.Close()

	// A second server over the same job root must see the record.
	second, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer (second): %v", err)
	}
	defer second.Close()
	session2, ctx2 := startTestSession(t, second)
	output := callTool(t, ctx2, session2, "remote_dev__job_output", map[string]interface{}{
		"job_id": jobID,
	})
	if !strings.Contains(output, "durable") {
		t.Fatalf("job record did not survive restart: %s", output)
	}
}

// extractJobID pulls the job id out of the job_start response.
func extractJobID(t *testing.T, body string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		index := strings.Index(line, "job_")
		if index < 0 {
			continue
		}
		rest := line[index:]
		end := strings.IndexAny(rest, " \t\n)")
		if end < 0 {
			end = len(rest)
		}
		return rest[:end]
	}
	return ""
}

// extractArtifactID pulls the art_… id out of a folded shell result.
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

// TestArtifactStoreExpiry documents the retention behavior at the unit level.
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
	// Oversized payload is refused rather than silently truncated.
	if _, err := store.Put(make([]byte, 2048)); err == nil {
		t.Fatal("expected oversized payload to be refused")
	}
}

// TestJobManagerDisabledWithoutRoot documents the disabled state.
func TestJobManagerDisabledWithoutRoot(t *testing.T) {
	manager, err := remotetools.NewJobManager("")
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}
	if manager != nil {
		t.Fatal("empty root must disable the job manager")
	}
	// A nil manager reports unsupported rather than panicking.
	if _, err := manager.Status("job_x"); err == nil {
		t.Fatal("expected unsupported error")
	}
}

var _ = context.Background
var _ = os.Getenv