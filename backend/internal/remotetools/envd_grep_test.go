package remotetools_test

// A capped grep must cost the window, not the whole scan.
//
// These assertions stay on deterministic ground: the returned window is bounded,
// the engine line survives the cut (it is echoed before the search runs, which is
// the whole reason for that ordering), and the result says it is not complete.
// Whether the target stopped mid-write is recorded on the stub for diagnosis, but
// no assertion depends on socket timing.

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

func TestEnvDGrepStopsAtTheByteCeiling(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/README.md": "hello envd\n"}, nil)
	// 40 frames of ~90 bytes: the full stream would be roughly 3.6 KB.
	stub.grepFrames = 40

	cfg := newEnvDConfig(stub.server.URL)
	// One documented knob is enough: Normalize derives the pre-fold ceiling from it.
	cfg.Limits.MaxBytesPerCall = 512
	server, err := remotetools.NewServer(cfg, "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	session, ctx := connectEnvDServer(t, server)

	got := callTool(t, ctx, session, "remote_envd__grep", map[string]interface{}{
		"path": ".", "pattern": "hello",
	})

	// The engine line is echoed before the search starts, so a cut stream still
	// reports which dialect produced these matches.
	if !strings.Contains(got, "searched with rg") {
		t.Fatalf("engine lost after truncation: %q", got)
	}
	if !strings.Contains(got, "stopped at 512 bytes") || !strings.Contains(got, "not the full match set") {
		t.Fatalf("truncation was not disclosed: %q", got)
	}
	if frames := strings.Count(got, "/srv/app/f"); frames == 0 {
		t.Fatalf("no matches came back at all: %q", got)
	} else if frames >= stub.grepFrames {
		t.Fatalf("the capped search drained all %d frames; the reader never stopped early", frames)
	}

	// The ceiling must be honoured, not merely approached. Output reaches the
	// reader in frames, so stopping at ">= cap" without trimming lets the last
	// frame overshoot: a live run measured 4210 bytes against a 512-byte cap.
	matches := got
	if line := strings.Index(matches, "\n"); line >= 0 {
		matches = matches[line+1:]
	}
	if notice := strings.Index(matches, "\n(stopped at "); notice >= 0 {
		matches = matches[:notice]
	}
	if len(matches) > 512 {
		t.Fatalf("returned %d bytes of matches under a 512-byte ceiling; the capture limit is not a suggestion", len(matches))
	}
	if stub.streamCut.Load() {
		t.Logf("target stopped writing mid-stream (early termination observed on the socket)")
	}
}

// Without a ceiling in the way, a normal search reports no truncation and keeps
// the no-match wording that callers already branch on.
func TestEnvDGrepUncappedKeepsPlainResults(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{
		"/srv/app/README.md": "line one\nhello envd\nline three\n",
		"/srv/app/other.md":  "nothing here\n",
	}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileReadonly)

	got := callTool(t, ctx, session, "remote_envd__grep", map[string]interface{}{
		"path": ".", "pattern": "hello",
	})
	if strings.Contains(got, "stopped at") {
		t.Fatalf("a small result must not claim truncation: %q", got)
	}
	if !strings.Contains(got, "hello envd") {
		t.Fatalf("match missing: %q", got)
	}

	empty := callTool(t, ctx, session, "remote_envd__grep", map[string]interface{}{
		"path": ".", "pattern": "absent-token",
	})
	if !strings.Contains(empty, "no matches for") {
		t.Fatalf("no-match wording changed: %q", empty)
	}
}
