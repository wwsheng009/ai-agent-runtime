package remotetools_test

// M3: the content-editing pair. The point of these tests is that envd shares the
// EDITING semantics (ambiguity guard, stale-context recovery, replay detection),
// while only the I/O differs — so the refusals must read exactly like the local
// surface's, and a refused edit must leave the target byte-identical.

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

func TestEnvDEditReplacesUniqueMatch(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/main.go": "package main\n\nfunc main() {}\n"}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	got := callTool(t, ctx, session, "remote_envd__edit", map[string]interface{}{
		"file_path":  "main.go",
		"old_string": "func main() {}",
		"new_string": "func main() {\n\tprintln(\"hi\")\n}",
	})
	// The reply reads exactly like the local surface's edit result.
	if !strings.Contains(got, "Replaced 1 occurrence(s) in main.go") {
		t.Fatalf("edit result = %q", got)
	}
	// The diff is produced by the shared unified-diff layer, not a second one.
	if !strings.Contains(got, "@@") || !strings.Contains(got, "-func main() {}") {
		t.Fatalf("edit result missing the shared diff: %q", got)
	}
	content, _ := stub.content("/srv/app/main.go")
	if !strings.Contains(content, `println("hi")`) || strings.Contains(content, "func main() {}") {
		t.Fatalf("target content after edit = %q", content)
	}
}

func TestEnvDEditRefusesAmbiguousMatchAndWritesNothing(t *testing.T) {
	original := "x\nx\n"
	stub := newEnvdStub(t, map[string]string{"/srv/app/a.txt": original}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	before := stub.processCalls.Load()
	refused := callEnvDExpectError(t, ctx, session, "remote_envd__edit", map[string]interface{}{
		"file_path": "a.txt", "old_string": "x", "new_string": "y",
	})
	if !strings.Contains(refused, "ambiguous edit") || !strings.Contains(refused, "命中 2 处") {
		t.Fatalf("ambiguity refusal = %q", refused)
	}
	// An ambiguous refusal must be a refusal BEFORE any read-modify-write lands.
	if _, moved, _ := stub.history(); len(moved) != 0 {
		t.Fatalf("unexpected move RPCs: %v", moved)
	}
	if content, _ := stub.content("/srv/app/a.txt"); content != original {
		t.Fatalf("ambiguous edit mutated the target: %q", content)
	}
	_ = before

	// The explicit escape hatches work, and the guard does not fire for them.
	all := callTool(t, ctx, session, "remote_envd__edit", map[string]interface{}{
		"file_path": "a.txt", "old_string": "x", "new_string": "y", "replace_all": true,
	})
	if !strings.Contains(all, "Replaced 2 occurrence(s)") {
		t.Fatalf("replace_all result = %q", all)
	}
	if content, _ := stub.content("/srv/app/a.txt"); content != "y\ny\n" {
		t.Fatalf("replace_all content = %q", content)
	}
}

func TestEnvDEditStaleContextPointsAtTheCurrentLines(t *testing.T) {
	original := "alpha\nbeta\ngamma\n"
	stub := newEnvdStub(t, map[string]string{"/srv/app/b.txt": original}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__edit", map[string]interface{}{
		"file_path": "b.txt", "old_string": "no-such-line\nbeta", "new_string": "whatever",
	})
	if !strings.Contains(refused, "next_action") {
		t.Fatalf("stale-context refusal lost its guidance: %q", refused)
	}
	if content, _ := stub.content("/srv/app/b.txt"); content != original {
		t.Fatalf("stale edit mutated the target: %q", content)
	}
}

func TestEnvDEditRequiresAnExistingFile(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__edit", map[string]interface{}{
		"file_path": "absent.txt", "old_string": "a", "new_string": "b",
	})
	if !strings.Contains(refused, "file not found") {
		t.Fatalf("missing-file refusal = %q", refused)
	}
}

func TestEnvDEditStaysInsideTheJail(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/etc/passwd": "root:x:0:0\n"}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__edit", map[string]interface{}{
		"file_path": "../../etc/passwd", "old_string": "root", "new_string": "hacked",
	})
	if !strings.Contains(refused, "outside the workspace") {
		t.Fatalf("edit escape refusal = %q", refused)
	}
	if content, _ := stub.content("/etc/passwd"); content != "root:x:0:0\n" {
		t.Fatalf("edit escaped the jail: %q", content)
	}
}

func TestEnvDAppendCreatesByDefaultLikeTheSharedContract(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	// The shared schema defaults create_if_missing to true; envd must not be
	// stricter than the ssh surface.
	got := callTool(t, ctx, session, "remote_envd__append_write", map[string]interface{}{
		"file_path": "new.log", "content": "first\n",
	})
	if !strings.Contains(got, "Appended 6 bytes to") && !strings.Contains(got, "Appended") {
		t.Fatalf("append result = %q", got)
	}
	if content, _ := stub.content("/srv/app/new.log"); content != "first\n" {
		t.Fatalf("created content = %q", content)
	}
}

func TestEnvDAppendHonoursCreateIfMissingFalse(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__append_write", map[string]interface{}{
		"file_path": "absent.log", "content": "x", "create_if_missing": false,
	})
	if !strings.Contains(refused, "file does not exist") || !strings.Contains(refused, "create_if_missing=true") {
		t.Fatalf("append refusal = %q", refused)
	}
}

func TestEnvDAppendIsReplaySafe(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/log.txt": "ab"}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	first := callTool(t, ctx, session, "remote_envd__append_write", map[string]interface{}{
		"file_path": "log.txt", "content": "c", "expected_offset": 2,
	})
	if !strings.Contains(first, "Appended 1 bytes to") {
		t.Fatalf("first append = %q", first)
	}
	if content, _ := stub.content("/srv/app/log.txt"); content != "abc" {
		t.Fatalf("content after first append = %q", content)
	}

	// A retry with the same offset must be recognized as a replay, not doubled.
	second := callTool(t, ctx, session, "remote_envd__append_write", map[string]interface{}{
		"file_path": "log.txt", "content": "c", "expected_offset": 2,
	})
	if !strings.Contains(second, "append replayed") {
		t.Fatalf("replay not detected: %q", second)
	}
	if content, _ := stub.content("/srv/app/log.txt"); content != "abc" {
		t.Fatalf("replay doubled the content: %q", content)
	}

	// A stale offset is a precondition failure, and it must not write.
	before, _, _ := stub.history()
	refused := callEnvDExpectError(t, ctx, session, "remote_envd__append_write", map[string]interface{}{
		"file_path": "log.txt", "content": "d", "expected_offset": 0,
	})
	if !strings.Contains(refused, "precondition failed") {
		t.Fatalf("stale offset refusal = %q", refused)
	}
	if content, _ := stub.content("/srv/app/log.txt"); content != "abc" {
		t.Fatalf("stale offset still wrote: %q", content)
	}
	_ = before
}

func TestEnvDEditRefusesBinaryTargets(t *testing.T) {
	// NUL bytes in the body make it binary by the shared classifier.
	stub := newEnvdStub(t, map[string]string{"/srv/app/blob.bin": "\x00\x01\x02"}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__edit", map[string]interface{}{
		"file_path": "blob.bin", "old_string": "\x00", "new_string": "x",
	})
	if !strings.Contains(refused, "looks binary") {
		t.Fatalf("binary refusal = %q", refused)
	}
}
