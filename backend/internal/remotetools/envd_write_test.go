package remotetools_test

// envd opens a file for writing as the DAEMON user (measured: root), while the
// process channel runs as the user the credentials name. Without a write gate,
// remote_x__write could replace files that remote_x__shell could not even open for
// writing - a privilege difference, and a difference from the local backend, where
// os.WriteFile is refused by the kernel. These tests pin that gate, including the
// two ways it can fail: refusing something the user may write (annoying), and
// accepting something the user may not (unsafe).

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

func TestEnvDWriteGateRefusesWhatTheTargetUserCannotWrite(t *testing.T) {
	const original = "root owns this, n9e does not\n"
	stub := newEnvdStub(t, map[string]string{"/srv/app/system.conf": original}, nil)
	// Owned by someone else and not writable by us. The stub does not model the
	// daemon's own permission check: envd writes as root, so the bytes WOULD land
	// unless the gate stops them first.
	stub.pinOwner("/srv/app/system.conf", "root")
	stub.pinMode("/srv/app/system.conf", 0o644)
	stub.markUnwritable("/srv/app/system.conf")

	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)
	refused := callEnvDExpectError(t, ctx, session, "remote_envd__write", map[string]interface{}{
		"path": "system.conf", "content": "pwned\n",
	})
	if !strings.Contains(refused, "permission denied writing") || !strings.Contains(refused, "system.conf") {
		t.Fatalf("refusal = %q, want a permission-denied message naming the path", refused)
	}

	// The real assertion: the file is untouched. Reporting a refusal while the
	// daemon wrote the bytes would be worse than no refusal at all.
	if landed, ok := stub.content("/srv/app/system.conf"); !ok || landed != original {
		t.Fatalf("the gated write landed anyway: %q", landed)
	}
	if stub.writableChecks.Load() == 0 {
		t.Fatal("expected the write gate to ask the target kernel")
	}
	// And the endpoint must never have been asked to write it.
	for _, entry := range stub.filesRequests() {
		if strings.HasPrefix(entry, "/srv/app/system.conf") {
			t.Fatalf("the file channel was asked to write a path the gate refused: %v", stub.filesRequests())
		}
	}
}

// The gate must not turn into a tax: writing into a directory the user owns, or
// replacing a file the user owns, is certain, so it is answered without a spawn.
func TestEnvDWriteGateFastPathForOwnedTargets(t *testing.T) {
	t.Run("existing file owned by the user", func(t *testing.T) {
		stub := newEnvdStub(t, map[string]string{"/srv/app/notes.md": "old\n"}, nil)
		stub.pinOwner("/srv/app/notes.md", "dev")
		stub.pinMode("/srv/app/notes.md", 0o600) // private, but ours: no probe needed
		session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

		got := callTool(t, ctx, session, "remote_envd__write", map[string]interface{}{
			"path": "notes.md", "content": "new\n",
		})
		if !strings.Contains(got, "Updated notes.md") {
			t.Fatalf("write = %q", got)
		}
		if stub.writableChecks.Load() != 0 {
			t.Fatalf("write gate probes = %d, want 0 when ownership already proves it", stub.writableChecks.Load())
		}
	})

	t.Run("new file in the user's workspace", func(t *testing.T) {
		stub := newEnvdStub(t, map[string]string{}, nil)
		session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

		got := callTool(t, ctx, session, "remote_envd__write", map[string]interface{}{
			"path": "fresh.txt", "content": "created\n",
		})
		if !strings.Contains(got, "Created fresh.txt") {
			t.Fatalf("write = %q", got)
		}
		if stub.writableChecks.Load() != 0 {
			t.Fatalf("write gate probes = %d, want 0 for creation inside an owned directory", stub.writableChecks.Load())
		}
	})
}

// envd answers a write it does not understand with 200 and an empty list, having
// written nothing (measured: the multipart field must be exactly "file"). A client
// that trusts the status code would report "Created x" for a file that is not there.
func TestEnvDWriteDetectsASilentNoOpWrite(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	stub.silentlyDropWrites()
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__write", map[string]interface{}{
		"path": "ghost.txt", "content": "hello\n",
	})
	if strings.Contains(refused, "Created") {
		t.Fatalf("a write that landed nothing was reported as success: %q", refused)
	}
	if _, ok := stub.content("/srv/app/ghost.txt"); ok {
		t.Fatal("stub claims the file exists although the write was dropped")
	}
}

// With the native endpoint gone, writes must still work through the process
// channel - the surface cannot regress to failing because a capability is missing.
func TestEnvDWriteFallsBackToProcessChannel(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	stub.disableFiles()
	cfg := newEnvDConfig(stub.server.URL)
	cfg.Profile = remotetools.ProfileStandard
	server, err := remotetools.NewServer(cfg, "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	session, ctx := connectEnvDServer(t, server)

	got := callTool(t, ctx, session, "remote_envd__write", map[string]interface{}{
		"path": "via-shell.txt", "content": "printf path\n",
	})
	if !strings.Contains(got, "Created via-shell.txt") {
		t.Fatalf("fallback write = %q", got)
	}
	landed, ok := stub.content("/srv/app/via-shell.txt")
	if !ok || landed != "printf path\n" {
		t.Fatalf("fallback content = %q (found=%v)", landed, ok)
	}
	// The gate is a native-channel concern only: `printf` already runs as the
	// target user, so the kernel answers for it and a probe would be pure cost.
	if stub.writableChecks.Load() != 0 {
		t.Fatalf("write gate probes = %d, want 0 on the process channel", stub.writableChecks.Load())
	}
}

// The NUL refusal was never a policy: it existed because argv cannot hold a NUL
// byte. With the native channel the payload is a stream, so refusing a UTF-16
// file would be a backend-specific rule the local surface does not have.
func TestEnvDWriteCarriesNULBytesThroughNativeChannel(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	// A UTF-16LE "hi" with BOM: exactly the bytes the shared encoder produces.
	payload := "ÿþ\x00h\x00i\x00"
	got := callTool(t, ctx, session, "remote_envd__write", map[string]interface{}{
		"path": "utf16.txt", "content": payload,
	})
	if !strings.Contains(got, "Created utf16.txt") {
		t.Fatalf("NUL payload write through the native channel = %q", got)
	}
	landed, ok := stub.content("/srv/app/utf16.txt")
	if !ok || landed != payload {
		t.Fatalf("bytes changed: got %q (found=%v)", landed, ok)
	}
	if stub.catCalls.Load() != 0 {
		t.Fatalf("cat calls = %d, want the native channel to carry the payload", stub.catCalls.Load())
	}
}

// Without the native channel the limit is real again, and the refusal must say
// so: naming the missing capability is what makes the rule look like an
// environment fact rather than a backend's own policy.
func TestEnvDWriteRefusesNULWithoutNativeChannel(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	stub.disableFiles()
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__write", map[string]interface{}{
		"path": "utf16.txt", "content": "a\x00b",
	})
	if !strings.Contains(refused, "NUL") {
		t.Fatalf("refusal = %q, want it to name the NUL bytes", refused)
	}
	if !strings.Contains(refused, "native file endpoint is unavailable") {
		t.Fatalf("refusal = %q, want it to blame the missing capability, not the tool", refused)
	}
	if _, ok := stub.content("/srv/app/utf16.txt"); ok {
		t.Fatal("the refused write landed anyway")
	}
}

// The case the NUL refusal actually blocked is not a model typing a NUL - it is
// `edit` preserving a file's own encoding: read UTF-16LE, write UTF-16LE back.
// The read is bytes, the write is bytes, and only argv refused in between.
func TestEnvDEditPreservesUTF16ThroughTheNativeChannel(t *testing.T) {
	original := "\xff\xfeh\x00i\x00!\x00" // UTF-16LE BOM + "hi!"
	stub := newEnvdStub(t, map[string]string{"/srv/app/utf16.txt": original}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	got := callTool(t, ctx, session, "remote_envd__edit", map[string]interface{}{
		"file_path": "utf16.txt", "old_string": "hi!", "new_string": "ok?",
	})
	if !strings.Contains(got, "Replaced 1 occurrence(s)") {
		t.Fatalf("edit = %q", got)
	}
	landed, ok := stub.content("/srv/app/utf16.txt")
	if !ok {
		t.Fatal("the edit did not land")
	}
	want := "\xff\xfe" + "o\x00k\x00?\x00" // UTF-16LE BOM kept, text replaced
	if landed != want {
		t.Fatalf("encoding not preserved:\n got % x\nwant % x", []byte(landed), []byte(want))
	}
}

// Content fidelity is the reason the native channel exists: bytes that contain
// printf-sensitive text must not be reinterpreted on the way to the target.
func TestEnvDWriteSendsBytesVerbatim(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	payload := "100%% sure\tand a backslash \\ and $(not-a-substitution)\n"
	got := callTool(t, ctx, session, "remote_envd__write", map[string]interface{}{
		"path": "tricky.txt", "content": payload,
	})
	if !strings.Contains(got, "Created tricky.txt") {
		t.Fatalf("write = %q", got)
	}
	landed, ok := stub.content("/srv/app/tricky.txt")
	if !ok {
		t.Fatal("the write did not land")
	}
	if landed != payload {
		t.Fatalf("content changed in transit:\n got %q\nwant %q", landed, payload)
	}
}
