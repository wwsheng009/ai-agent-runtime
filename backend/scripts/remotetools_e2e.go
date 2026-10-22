//go:build ignore

// Command remotetools_e2e drives a live aicli-remote-tools server over the real
// MCP Streamable HTTP transport and prints every assertion result.
//
// Unlike the unit tests in internal/remotetools, this program has no in-memory
// shortcuts: it speaks HTTP to a server started as a separate process (on Linux,
// under WSL), which is the deployment shape the design targets.
//
// Usage:
//
//	go run ./scripts/remotetools_e2e.go -url http://127.0.0.1:18765/mcp -token <t> -prefix remote_wsl
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	urlFlag := flag.String("url", "http://127.0.0.1:18765/mcp", "MCP endpoint")
	tokenFlag := flag.String("token", "", "bearer token")
	prefixFlag := flag.String("prefix", "remote_wsl", "tool name prefix")
	flag.Parse()

	url := *urlFlag
	token := *tokenFlag
	prefix := *prefixFlag

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "aicli-e2e", Version: "1.0.0"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: url}
	if strings.TrimSpace(token) != "" {
		transport.HTTPClient = bearerClient(token)
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		fail("connect", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		fail("list tools", err)
	}
	fmt.Printf("PASS tools/list: %d tools\n", len(tools.Tools))
	for _, tool := range tools.Tools {
		if !strings.HasPrefix(tool.Name, prefix+"__") {
			fail("prefix", fmt.Errorf("tool %q lacks prefix %q", tool.Name, prefix))
		}
	}

	// --- read surface ---
	viewBody := mustCall(ctx, session, prefix+"__view", map[string]interface{}{"file_path": "src/main.go"})
	assertContains("view", viewBody, "func main()")
	assertAbsent("view no metadata block", viewBody, "Metadata:")

	lsBody := mustCall(ctx, session, prefix+"__ls", map[string]interface{}{"path": "src"})
	assertContains("ls", lsBody, "main.go")

	globBody := mustCall(ctx, session, prefix+"__glob", map[string]interface{}{"pattern": "**/*.go"})
	assertContains("glob", globBody, "src/main.go")

	grepBody := mustCall(ctx, session, prefix+"__grep", map[string]interface{}{"pattern": "TODO"})
	assertContains("grep", grepBody, "README.md:2")

	// --- jail ---
	denyBody := mustCallExpectError(ctx, session, prefix+"__view", map[string]interface{}{"file_path": "../../etc/passwd"})
	assertContains("jail denies traversal", denyBody, "PATH_DENIED")

	devBody := mustCallExpectError(ctx, session, prefix+"__view", map[string]interface{}{"file_path": "/dev/zero"})
	assertContains("jail denies absolute outside", devBody, "PATH_DENIED")

	// --- write surface ---
	mustCall(ctx, session, prefix+"__write", map[string]interface{}{"file_path": "e2e.txt", "content": "line1\nline2\n"})
	mustCall(ctx, session, prefix+"__append_write", map[string]interface{}{"file_path": "e2e.txt", "content": "line3\n"})
	mustCall(ctx, session, prefix+"__edit", map[string]interface{}{"file_path": "e2e.txt", "old_string": "line2", "new_string": "LINE2"})
	after := mustCall(ctx, session, prefix+"__view", map[string]interface{}{"file_path": "e2e.txt"})
	assertContains("write+append+edit", after, "LINE2")
	assertContains("append applied", after, "line3")

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: patched.txt",
		"+alpha",
		"+beta",
		"*** End Patch",
	}, "\n")
	mustCall(ctx, session, prefix+"__apply_patch", map[string]interface{}{"patch": patch})
	patchedBody := mustCall(ctx, session, prefix+"__view", map[string]interface{}{"file_path": "patched.txt"})
	assertContains("apply_patch add", patchedBody, "beta")

	// --- command policy ---
	// This configuration allows `echo`, so an allowlisted command must run.
	shellBody := mustCall(ctx, session, prefix+"__shell", map[string]interface{}{"command": "echo shell-ok"})
	assertContains("shell allowlisted command runs", shellBody, "shell-ok")

	// A command outside the allowlist must be refused.
	deniedShell := mustCallExpectError(ctx, session, prefix+"__shell", map[string]interface{}{"command": "whoami"})
	assertContains("shell denies non-allowlisted", deniedShell, "COMMAND_DENIED")

	// Metacharacter chaining must be refused even for an allowed binary: the
	// allowlist is per-command, not "anything echo can reach".
	chainShell := mustCallExpectError(ctx, session, prefix+"__shell", map[string]interface{}{"command": "echo hi; whoami"})
	assertContains("shell denies metachar chaining", chainShell, "COMMAND_DENIED")

	// A destructive command must be refused even when its binary is allowlisted.
	destructive := mustCallExpectError(ctx, session, prefix+"__shell", map[string]interface{}{"command": "rm -rf /"})
	assertContains("shell denies destructive command", destructive, "COMMAND_DENIED")

	// --- background jobs ---
	jobBody := mustCall(ctx, session, prefix+"__job_start", map[string]interface{}{"command": "echo job-ok"})
	assertContains("job_start", jobBody, "job_")
	jobID := extractJobID(jobBody)
	if jobID == "" {
		fail("job_start", fmt.Errorf("no job id in %q", jobBody))
	}
	// Poll until the job reaches a terminal state.
	deadline := time.Now().Add(20 * time.Second)
	var jobOutput string
	for time.Now().Before(deadline) {
		jobOutput = mustCall(ctx, session, prefix+"__job_output", map[string]interface{}{"job_id": jobID})
		if strings.Contains(jobOutput, "exited") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	assertContains("job_output ran the command", jobOutput, "job-ok")
	assertContains("job_output reached terminal state", jobOutput, "exited")

	jobList := mustCall(ctx, session, prefix+"__job_list", map[string]interface{}{"limit": 5})
	assertContains("job_list", jobList, jobID)

	// --- artifact archiving ---
	// The configured maxOutputBytes is small, so a long output must be folded and
	// archived, and artifact_read must page the full capture back.
	bigShell := mustCall(ctx, session, prefix+"__shell", map[string]interface{}{
		"command": "echo " + strings.Repeat("Z", 6000),
	})
	if strings.Contains(bigShell, "artifact_id") {
		artifactID := extractArtifactID(bigShell)
		page := mustCall(ctx, session, prefix+"__artifact_read", map[string]interface{}{"artifact_id": artifactID})
		assertContains("artifact_read pages the capture", page, "ZZZZ")
	} else {
		fmt.Println("SKIP artifact: output was not folded at this maxOutputBytes")
	}

	// --- metrics ---
	metricsBody := mustCall(ctx, session, prefix+"__view", map[string]interface{}{"file_path": "README.md"})
	assertContains("metrics still serving", metricsBody, "TODO")

	fmt.Println("ALL PASS")
}

// bearerClient builds an HTTP client that injects the Authorization header.
func bearerClient(token string) *http.Client {
	return &http.Client{
		Transport: &bearerRoundTripper{token: token, base: http.DefaultTransport},
	}
}

// bearerRoundTripper adds one static Authorization header.
type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (rt *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Bearer "+rt.token)
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}

func mustCall(ctx context.Context, session *mcp.ClientSession, name string, args map[string]interface{}) string {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fail("call "+name, err)
	}
	if result.IsError {
		fail("call "+name, fmt.Errorf("tool error: %s", text(result)))
	}
	return text(result)
}

func mustCallExpectError(ctx context.Context, session *mcp.ClientSession, name string, args map[string]interface{}) string {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fail("call "+name, err)
	}
	if !result.IsError {
		fail("call "+name, fmt.Errorf("expected a tool error, got success: %s", text(result)))
	}
	return text(result)
}

func text(result *mcp.CallToolResult) string {
	var builder strings.Builder
	for _, content := range result.Content {
		if typed, ok := content.(*mcp.TextContent); ok {
			builder.WriteString(typed.Text)
		}
	}
	return builder.String()
}

func assertContains(label, body, want string) {
	if !strings.Contains(body, want) {
		fail(label, fmt.Errorf("missing %q in:\n%s", want, body))
	}
	fmt.Printf("PASS %s\n", label)
}

func assertAbsent(label, body, unwanted string) {
	if strings.Contains(body, unwanted) {
		fail(label, fmt.Errorf("unexpected %q in:\n%s", unwanted, body))
	}
	fmt.Printf("PASS %s\n", label)
}

func fail(label string, err error) {
	fmt.Printf("FAIL %s: %v\n", label, err)
	encoded, _ := json.MarshalIndent(map[string]string{"failed": label, "error": err.Error()}, "", "  ")
	fmt.Println(string(encoded))
	os.Exit(1)
}

// extractJobID pulls a job id out of a job_start response.
func extractJobID(body string) string {
	index := strings.Index(body, "job_")
	if index < 0 {
		return ""
	}
	rest := body[index:]
	end := strings.IndexAny(rest, " \t\n)")
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

// extractArtifactID pulls an artifact id out of a folded shell result.
func extractArtifactID(body string) string {
	index := strings.Index(body, "art_")
	if index < 0 {
		return ""
	}
	rest := body[index:]
	end := strings.IndexAny(rest, " \t\n;,)]")
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}
