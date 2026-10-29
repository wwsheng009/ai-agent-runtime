package remotetools_test

// These tests pin the two rules the /files spike made necessary:
//
//  1. The native file endpoint is preferred, and a deployment without it degrades
//     to the process channel once, with the reason remembered rather than
//     rediscovered per file.
//  2. Only a file the TARGET USER may open is served. /files answers as the daemon
//     user (measured: it returned /etc/shadow for a user the kernel refused), so
//     without this gate the same tool would see more through one channel than
//     through the other - and more than the local backend would allow.
//
// Assertions use observed side effects (which endpoint was hit, whether a process
// was spawned) rather than result prose, because the channel choice must be
// verifiable independently of the message text.

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// envDServe starts the envd backend against one stub and returns the session plus
// the stub, so a test can inspect what the target actually received.
func envDServe(t *testing.T, stub *envdStub) (*mcp.ClientSession, context.Context) {
	t.Helper()
	server, err := remotetools.NewServer(newEnvDConfig(stub.server.URL), "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return connectEnvDServer(t, server)
}

func viewCall(t *testing.T, session *mcp.ClientSession, ctx context.Context, target string) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_envd__view",
		Arguments: map[string]interface{}{"file_path": target},
	})
	if err != nil {
		t.Fatalf("call view %s: %v", target, err)
	}
	return result
}

// probeCount counts the endpoint's own availability probe, whose path shape is
// fixed by ProbeFiles. A second read must not pay for a second probe.
func probeCount(requests []string) int {
	var seen int
	for _, entry := range requests {
		if strings.HasPrefix(entry, "/.aicli-envd-files-probe-") {
			seen++
		}
	}
	return seen
}

func TestEnvDViewUsesNativeFileChannel(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/README.md": "# hello envd\n"}, nil)
	session, ctx := envDServe(t, stub)

	result := viewCall(t, session, ctx, "README.md")
	if result.IsError {
		t.Fatalf("view failed: %s", textContent(result))
	}
	if got := textContent(result); !strings.Contains(got, "# hello envd") {
		t.Fatalf("content = %q", got)
	}

	requests := stub.filesRequests()
	if len(requests) != 2 { // one probe, one read
		t.Fatalf("files requests = %v, want probe + read", requests)
	}
	if probeCount(requests) != 1 {
		t.Fatalf("probe requests = %d, want exactly one cached across reads", probeCount(requests))
	}
	// The read must be bounded by the view limit rather than open-ended, so a
	// huge file can never turn into a huge transfer even if Stat raced.
	if !strings.Contains(requests[1], "range=bytes=0-") {
		t.Fatalf("read request = %q, want a bounded prefix range", requests[1])
	}
	// No process-channel read at all: neither cat, nor a permission probe, because
	// the stub reports 0644 and world-readable needs no asking. (The jail's own
	// target-side canonicalization is counted separately and is not a read.)
	if stub.catCalls.Load() != 0 {
		t.Fatalf("cat calls = %d, want 0: /files must serve this read", stub.catCalls.Load())
	}
	if stub.readableChecks.Load() != 0 {
		t.Fatalf("permission probes = %d, want 0 for a world-readable file", stub.readableChecks.Load())
	}
}

// A file that is NOT world-readable is asked of the target kernel before any
// bytes move - and the endpoint is still what serves them.
func TestEnvDViewProbesPermissionForNonWorldReadableFile(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/private.txt": "mine\n"}, nil)
	stub.pinMode("/srv/app/private.txt", 0o600)
	session, ctx := envDServe(t, stub)

	result := viewCall(t, session, ctx, "private.txt")
	if result.IsError {
		t.Fatalf("view failed: %s", textContent(result))
	}
	if stub.readableChecks.Load() != 1 {
		t.Fatalf("permission probes = %d, want exactly one `test -r`", stub.readableChecks.Load())
	}
	if stub.catCalls.Load() != 0 {
		t.Fatalf("cat calls = %d, want 0: the file channel still serves the read", stub.catCalls.Load())
	}
	var served bool
	for _, entry := range stub.filesRequests() {
		if strings.HasPrefix(entry, "/srv/app/private.txt") {
			served = true
		}
	}
	if !served {
		t.Fatalf("expected the file channel to serve the read, got %v", stub.filesRequests())
	}
}

// The gate is the whole point: /files would happily serve this file, because the
// daemon can read it. Refusing means the endpoint is never asked for it.
func TestEnvDViewGateRefusesWhatTheTargetUserCannotOpen(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/shadowish.txt": "root only\n"}, nil)
	stub.pinMode("/srv/app/shadowish.txt", 0o640)
	stub.markUnreadable("/srv/app/shadowish.txt")
	session, ctx := envDServe(t, stub)

	result := viewCall(t, session, ctx, "shadowish.txt")
	if !result.IsError {
		t.Fatalf("gate must refuse a file the target user cannot open, got %q", textContent(result))
	}
	text := textContent(result)
	if !strings.Contains(text, "permission denied") || !strings.Contains(text, "shadowish.txt") {
		t.Fatalf("refusal = %q, want a permission-denied message naming the path", text)
	}
	// The refusal must be actionable, not just negative: the mode and owner this
	// call already learned from Stat belong in the text, because "permission
	// denied" alone leaves the reader guessing whether it is ownership or a group.
	if !strings.Contains(text, "-rw-r-----") || !strings.Contains(text, "dev:dev") {
		t.Fatalf("refusal = %q, want the pinned mode and owner to be reported", text)
	}
	// The endpoint must never have been asked for that path: only the probe of an
	// unrelated missing file is allowed there.
	for _, entry := range stub.filesRequests() {
		if strings.HasPrefix(entry, "/srv/app/shadowish.txt") {
			t.Fatalf("the file channel served a file the gate should have refused: %v", stub.filesRequests())
		}
	}
	if stub.readableChecks.Load() == 0 {
		t.Fatal("expected the permission probe to have run on the target")
	}
	if stub.catCalls.Load() != 0 {
		t.Fatalf("cat calls = %d, want 0: a refused file must not be read by any channel", stub.catCalls.Load())
	}
}

// Without the endpoint, the surface must still work: the process channel answers,
// and the absence is discovered once rather than retried per file.
func TestEnvDViewFallsBackToProcessChannel(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/README.md": "# fallback\n"}, nil)
	stub.disableFiles()
	session, ctx := envDServe(t, stub)

	for i := 0; i < 2; i++ {
		result := viewCall(t, session, ctx, "README.md")
		if result.IsError {
			t.Fatalf("read %d failed: %s", i, textContent(result))
		}
		if got := textContent(result); !strings.Contains(got, "# fallback") {
			t.Fatalf("read %d content = %q", i, got)
		}
	}

	if probeCount(stub.filesRequests()) != 1 {
		t.Fatalf("files requests = %v, want a single cached probe", stub.filesRequests())
	}
	if stub.catCalls.Load() != 2 {
		t.Fatalf("cat calls = %d, want one per read after the channel was found unusable", stub.catCalls.Load())
	}
}

// The kill switch must not merely prefer the other channel: it must never touch
// this one, so an operator who refused the daemon-user read sees no request for it
// at all, and a live run can use it to prove both channels agree byte for byte.
func TestEnvDViewFilesChannelDisabledByConfig(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/README.md": "# switched off\n"}, nil)
	cfg := newEnvDConfig(stub.server.URL)
	cfg.EnvD.FilesChannelDisabled = true
	server, err := remotetools.NewServer(cfg, "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	session, ctx := connectEnvDServer(t, server)

	result := viewCall(t, session, ctx, "README.md")
	if result.IsError {
		t.Fatalf("view failed: %s", textContent(result))
	}
	if len(stub.filesRequests()) != 0 {
		t.Fatalf("files requests = %v, want none when the channel is disabled", stub.filesRequests())
	}
	if stub.catCalls.Load() != 1 {
		t.Fatalf("cat calls = %d, want the process channel to answer", stub.catCalls.Load())
	}
}

// The gate must cover every tool that reads, not just the one that was in mind
// when it was written: `edit` and `apply_patch` load the same file through the
// same backend, and a read that skips the gate shows the model content its own
// shell could not open.
func TestEnvDEditAndPatchRespectTheReadGate(t *testing.T) {
	const secret = "hash: abc123\n"
	setup := func(t *testing.T) (*envdStub, *mcp.ClientSession, context.Context) {
		t.Helper()
		stub := newEnvdStub(t, map[string]string{"/srv/app/secrets.env": secret}, nil)
		stub.pinOwner("/srv/app/secrets.env", "root")
		stub.pinMode("/srv/app/secrets.env", 0o600)
		stub.markUnreadable("/srv/app/secrets.env")
		session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)
		return stub, session, ctx
	}

	t.Run("edit", func(t *testing.T) {
		stub, session, ctx := setup(t)
		before := stub.readableChecks.Load()
		refused := callEnvDExpectError(t, ctx, session, "remote_envd__edit", map[string]interface{}{
			"file_path": "secrets.env", "old_string": "abc123", "new_string": "leaked",
		})
		if !strings.Contains(refused, "permission denied reading") {
			t.Fatalf("edit refusal = %q", refused)
		}
		if stub.readableChecks.Load() == before {
			t.Fatal("edit did not ask the target kernel before reading")
		}
		if landed, ok := stub.content("/srv/app/secrets.env"); !ok || landed != secret {
			t.Fatalf("the gated edit changed the file: %q", landed)
		}
	})

	t.Run("apply_patch", func(t *testing.T) {
		stub, session, ctx := setup(t)
		patch := "*** Begin Patch\n*** Update File: secrets.env\n@@\n-hash: abc123\n+hash: leaked\n*** End Patch\n"
		refused := callEnvDExpectError(t, ctx, session, "remote_envd__apply_patch", map[string]interface{}{
			"patch": patch,
		})
		if !strings.Contains(refused, "permission denied reading") {
			t.Fatalf("apply_patch refusal = %q, want the read gate to stop the hunk", refused)
		}
		if landed, ok := stub.content("/srv/app/secrets.env"); !ok || landed != secret {
			t.Fatalf("the gated patch changed the file: %q", landed)
		}
	})
}

// A refusal about the file must not be re-asked through the other channel: two
// channels answering the same question differently is how a real error becomes a
// surprising empty result.
func TestEnvDViewDoesNotReaskAPathLevelFailure(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/README.md": "# hi\n"}, nil)
	session, ctx := envDServe(t, stub)

	result := viewCall(t, session, ctx, "gone.md")
	if !result.IsError {
		t.Fatalf("missing file must be refused, got %q", textContent(result))
	}
	// Stat answers this before the file channel is ever consulted, and the
	// refusal must not be re-asked through the process channel.
	if stub.catCalls.Load() != 0 {
		t.Fatalf("cat calls = %d, want no retry through the process channel", stub.catCalls.Load())
	}
}

// The size gate is unchanged by the new channel: an oversized file is refused
// before any transfer, and nothing is read.
func TestEnvDViewStillRefusesOversizedFiles(t *testing.T) {
	big := strings.Repeat("x", 4096)
	stub := newEnvdStub(t, map[string]string{"/srv/app/big.txt": big}, nil)
	cfg := newEnvDConfig(stub.server.URL)
	// One knob must be enough: this is the operator-facing setting aicli expands
	// from a target's Limits block (--max-bytes-per-call). Before Normalize derived
	// CaptureLimit from it, the ceiling stayed at the compiled default and read
	// tools ignored whatever the operator asked for.
	cfg.Limits.MaxBytesPerCall = 64
	server, err := remotetools.NewServer(cfg, "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	session, ctx := connectEnvDServer(t, server)

	result := viewCall(t, session, ctx, "big.txt")
	if !result.IsError {
		t.Fatalf("oversized file must be refused, got %q", textContent(result))
	}
	if !strings.Contains(textContent(result), "above the") {
		t.Fatalf("refusal = %q, want the size-limit message", textContent(result))
	}
	for _, entry := range stub.filesRequests() {
		if strings.HasPrefix(entry, "/srv/app/big.txt") {
			t.Fatalf("an oversized file must not be transferred at all: %v", stub.filesRequests())
		}
	}
}
