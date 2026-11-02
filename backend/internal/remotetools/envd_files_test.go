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
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/viewcore"
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
			"input": patch,
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

// A file larger than the read budget is now served as a window instead of being
// refused, and the window must be the text the shared core produces from the whole
// file. That is the §8.9 item closing: the remote surface no longer gives up
// where the local surface would page.
func TestEnvDViewsBigFilesByWindowingLikeTheCore(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&body, "row %03d: some payload text\n", i)
	}
	full := body.String()
	stub := newEnvdStub(t, map[string]string{"/srv/app/page.log": full}, nil)

	cfg := newEnvDConfig(stub.server.URL)
	// One documented knob: Normalize derives the pre-fold ceiling from it.
	cfg.Limits.MaxBytesPerCall = 128
	server, err := remotetools.NewServer(cfg, "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	session, ctx := connectEnvDServer(t, server)

	cases := []struct {
		name          string
		offset, limit float64
	}{
		{"first window", 0, 3},
		{"deep window", 120, 4},           // beyond the initial prefix: the fetch must grow
		{"window to the end", 398, 99},    // must not invent lines past the end
		{"offset past the end", 99999, 5}, // core's own beyond-EOF note, once the file is whole
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			request := viewcore.Request{FilePath: "page.log", Offset: int(test.offset), Limit: int(test.limit)}
			request.Offset, request.Limit = viewcore.Normalize(request.Offset, request.Limit)
			want, _, wantErr := viewcore.ReadTextFromBytes([]byte(full), request, viewcore.Options{ByteBudget: 128})
			if wantErr != nil {
				t.Fatalf("shared core render: %v", wantErr)
			}
			got := callTool(t, ctx, session, "remote_envd__view", map[string]interface{}{
				"file_path": "page.log", "offset": test.offset, "limit": test.limit,
			})
			if got != want {
				t.Fatalf("remote window differs from the local render for offset=%d limit=%d:\n got %q\nwant %q",
					request.Offset, request.Limit, clipWindow(got), clipWindow(want))
			}
		})
	}
}

// clipWindow keeps a failure message readable without dumping every fetched byte.
func clipWindow(text string) string {
	if len(text) > 160 {
		return text[:160] + "..."
	}
	return text
}

// An encoded file is only served whole: cutting the prefix mid-codepoint would
// change the text, so "same as local" stops being true and a refusal is honest.
func TestEnvDViewRefusesTruncatedEncodedFiles(t *testing.T) {
	body := "\xff\xfe" + strings.Repeat("h\x00i\x00\n\x00", 200) // UTF-16LE BOM, past the budget
	stub := newEnvdStub(t, map[string]string{"/srv/app/utf16-big.txt": body}, nil)
	cfg := newEnvDConfig(stub.server.URL)
	cfg.Limits.MaxBytesPerCall = 128
	server, err := remotetools.NewServer(cfg, "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	session, ctx := connectEnvDServer(t, server)

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__view", map[string]interface{}{
		"file_path": "utf16-big.txt",
	})
	if !strings.Contains(refused, "byte-order mark") {
		t.Fatalf("refusal = %q, want it to name the encoding constraint", refused)
	}
}

// The blanket size refusal is gone: an oversized single-line file now returns a
// window instead of giving up.
func TestEnvDViewNoLongerRefusesOversizedFiles(t *testing.T) {
	body := strings.Repeat("x", 4000)
	stub := newEnvdStub(t, map[string]string{"/srv/app/wide.txt": body}, nil)
	cfg := newEnvDConfig(stub.server.URL)
	cfg.Limits.MaxBytesPerCall = 64
	server, err := remotetools.NewServer(cfg, "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	session, ctx := connectEnvDServer(t, server)

	got := callTool(t, ctx, session, "remote_envd__view", map[string]interface{}{"file_path": "wide.txt"})
	if strings.Contains(got, "above the") {
		t.Fatalf("the old blanket size refusal came back: %q", got)
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("expected a rendered window, got nothing")
	}
}

// Differential parity, not self-reference: the expected text is produced by the
// same shared core the local surface runs over the same bytes. If envd later grows
// its own windowing, this fails even though envd alone would still look sane.
func TestEnvDViewWindowsMatchTheSharedCore(t *testing.T) {
	const body = "line one\nline two\nline three\nline four\nline five\n"
	stub := newEnvdStub(t, map[string]string{"/srv/app/pages.txt": body}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileReadonly)

	cases := []struct {
		name          string
		offset, limit float64
	}{
		{"head", 0, 2},
		{"page two", 2, 2},
		{"window to end", 3, 99},
		{"last lines", -2, 0},
		{"beyond eof", 99, 3},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			request := viewcore.Request{FilePath: "pages.txt", Offset: int(test.offset), Limit: int(test.limit)}
			request.Offset, request.Limit = viewcore.Normalize(request.Offset, request.Limit)
			want, _, err := viewcore.ReadTextFromBytes([]byte(body), request, viewcore.Options{ByteBudget: 4096})
			if err != nil {
				t.Fatalf("shared core render: %v", err)
			}
			got := callTool(t, ctx, session, "remote_envd__view", map[string]interface{}{
				"file_path": "pages.txt", "offset": test.offset, "limit": test.limit,
			})
			if got != want {
				t.Fatalf("envd window differs from the shared core for offset=%d limit=%d:\n got %q\nwant %q",
					request.Offset, request.Limit, got, want)
			}
		})
	}
}

// Unsupported parameters must never be quietly served as a plain single read.
// Two layers can say no: today the published JSON schema rejects them first
// ("unexpected additional properties"), and envdRefuseUnsupportedViewParams is the
// backstop for entry points that do not validate the schema. Both are acceptable;
// silently answering with one file's window is not.
func TestEnvDViewRefusesParametersItDoesNotImplement(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/a.txt": "a\n"}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileReadonly)

	for _, key := range []string{"files", "symbol", "compact"} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "remote_envd__view",
			Arguments: map[string]interface{}{
				"file_path": "a.txt", key: "anything",
			},
		})
		// The schema layer answers with a protocol error today; the handler guard
		// would answer with a tool error. Either is a refusal.
		refused := err != nil || result.IsError
		if !refused {
			t.Fatalf("%q was accepted by the surface; text=%q", key, textContent(result))
		}
		if err == nil && strings.Contains(textContent(result), "1: a") {
			t.Fatalf("%q was served as a normal single read: %q", key, textContent(result))
		}
	}
}
