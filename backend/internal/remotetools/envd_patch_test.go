package remotetools_test

// apply_patch on envd. The tests that matter here are the ordering ones: the
// patch language itself is mutatecore's, so these pin what this backend owns —
// pre-authorization, rollback, and the move/add refusal rules.

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// patch builds a Codex-style patch from complete lines.
func patch(lines ...string) string {
	return strings.Join(lines, "\n")
}

func TestEnvDPatchAppliesAddUpdateDelete(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{
		"/srv/app/main.go": "package main\n\nfunc main() {}\n",
		"/srv/app/old.txt": "gone\n",
	}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	got := callTool(t, ctx, session, "remote_envd__apply_patch", map[string]interface{}{
		"patch": patch(
			"*** Begin Patch",
			"*** Add File: notes.md",
			"+# notes",
			"*** Update File: main.go",
			"@@",
			" package main",
			"",
			"-func main() {}",
			"+func main() { run() }",
			"*** Delete File: old.txt",
			"*** End Patch",
		),
	})
	if !strings.Contains(got, "@@") {
		t.Fatalf("patch result missing the shared unified diff: %q", got)
	}
	if content, ok := stub.content("/srv/app/notes.md"); !ok || content != "# notes\n" {
		t.Fatalf("added file = %q ok=%v, want \"# notes\\n\"", content, ok)
	}
	if content, _ := stub.content("/srv/app/main.go"); !strings.Contains(content, "func main() { run() }") {
		t.Fatalf("updated file = %q", content)
	}
	if _, ok := stub.content("/srv/app/old.txt"); ok {
		t.Fatal("deleted file is still present")
	}
	removed, _, _ := stub.history()
	if len(removed) != 1 || removed[0] != "/srv/app/old.txt" {
		t.Fatalf("Remove RPCs = %v, want the one delete", removed)
	}
}

// A later failure must undo the earlier success, or a failed patch would leave a
// half-applied tree that the model cannot reason about.
func TestEnvDPatchRollsBackEarlierOperationsOnStaleHunk(t *testing.T) {
	original := "package main\n\nfunc main() {}\n"
	stub := newEnvdStub(t, map[string]string{"/srv/app/main.go": original}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__apply_patch", map[string]interface{}{
		"patch": patch(
			"*** Begin Patch",
			"*** Add File: scratch.txt",
			"+should not survive",
			"*** Update File: main.go",
			"@@",
			"-this text is not in the file",
			"+whatever",
			"*** End Patch",
		),
	})
	if !strings.Contains(refused, "next_action") {
		t.Fatalf("stale hunk refusal lost its guidance: %q", refused)
	}
	if _, ok := stub.content("/srv/app/scratch.txt"); ok {
		t.Fatal("the created file survived the rollback")
	}
	if content, _ := stub.content("/srv/app/main.go"); content != original {
		t.Fatalf("update target changed despite the failure: %q", content)
	}
}

// Every path is authorized before anything mutates: a patch that names an
// out-of-jail file must not touch the in-jail file it also names.
func TestEnvDPatchRefusesEscapeBeforeMutatingAnything(t *testing.T) {
	original := "keep\n"
	stub := newEnvdStub(t, map[string]string{
		"/srv/app/keep.txt": original,
		"/etc/outside.txt":  "outside\n",
	}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__apply_patch", map[string]interface{}{
		"patch": patch(
			"*** Begin Patch",
			"*** Update File: keep.txt",
			"@@",
			"-keep",
			"+changed",
			"*** Update File: ../etc/outside.txt",
			"@@",
			"-outside",
			"+hacked",
			"*** End Patch",
		),
	})
	if !strings.Contains(refused, "outside the workspace") {
		t.Fatalf("escape refusal = %q", refused)
	}
	if content, _ := stub.content("/srv/app/keep.txt"); content != original {
		t.Fatalf("the in-jail file was mutated despite the escape: %q", content)
	}
	if content, _ := stub.content("/etc/outside.txt"); content != "outside\n" {
		t.Fatalf("the out-of-jail file was mutated: %q", content)
	}
}

func TestEnvDPatchMoveRules(t *testing.T) {
	t.Run("rename preserves content", func(t *testing.T) {
		stub := newEnvdStub(t, map[string]string{"/srv/app/old.txt": "body\n"}, nil)
		session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

		callTool(t, ctx, session, "remote_envd__apply_patch", map[string]interface{}{
			"patch": patch(
				"*** Begin Patch",
				"*** Update File: old.txt",
				"*** Move to: renamed.txt",
				"*** End Patch",
			),
		})
		if content, ok := stub.content("/srv/app/renamed.txt"); !ok || content != "body\n" {
			t.Fatalf("destination = %q ok=%v, want body preserved", content, ok)
		}
		if _, ok := stub.content("/srv/app/old.txt"); ok {
			t.Fatal("the move left the source behind")
		}
	})

	t.Run("rename onto an existing file is refused", func(t *testing.T) {
		stub := newEnvdStub(t, map[string]string{
			"/srv/app/src.txt": "source\n",
			"/srv/app/dst.txt": "precious\n",
		}, nil)
		session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

		refused := callEnvDExpectError(t, ctx, session, "remote_envd__apply_patch", map[string]interface{}{
			"patch": patch(
				"*** Begin Patch",
				"*** Update File: src.txt",
				"*** Move to: dst.txt",
				"*** End Patch",
			),
		})
		if !strings.Contains(refused, "destination already exists") {
			t.Fatalf("move refusal = %q", refused)
		}
		if content, _ := stub.content("/srv/app/dst.txt"); content != "precious\n" {
			t.Fatalf("the existing destination was clobbered: %q", content)
		}
		if _, ok := stub.content("/srv/app/src.txt"); !ok {
			t.Fatal("the refused move still removed the source")
		}
	})
}

func TestEnvDPatchAddRefusesDifferentContent(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/srv/app/exists.txt": "real content\n"}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__apply_patch", map[string]interface{}{
		"patch": patch(
			"*** Begin Patch",
			"*** Add File: exists.txt",
			"+different content",
			"*** End Patch",
		),
	})
	if !strings.Contains(refused, "already exists with different content") {
		t.Fatalf("add refusal = %q", refused)
	}
	if content, _ := stub.content("/srv/app/exists.txt"); content != "real content\n" {
		t.Fatalf("add overwrote an existing file: %q", content)
	}

	// The same add twice is a replay, not a failure.
	replay := callTool(t, ctx, session, "remote_envd__apply_patch", map[string]interface{}{
		"patch": patch(
			"*** Begin Patch",
			"*** Add File: exists.txt",
			"+real content",
			"*** End Patch",
		),
	})
	if strings.Contains(replay, "already exists") {
		t.Fatalf("identical add reported a conflict: %q", replay)
	}
}

func TestEnvDPatchUnknownPathGetsNotFound(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__apply_patch", map[string]interface{}{
		"patch": patch(
			"*** Begin Patch",
			"*** Update File: nowhere.txt",
			"@@",
			"-a",
			"+b",
			"*** End Patch",
		),
	})
	if !strings.Contains(refused, "nowhere.txt") {
		t.Fatalf("missing-target refusal = %q", refused)
	}
}

func TestEnvDPatchSyntaxErrorIsReportedNotPanicking(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileStandard)

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__apply_patch", map[string]interface{}{
		"patch": "*** Begin Patch\n*** Frobnicate: bad.txt\n*** End Patch",
	})
	if strings.TrimSpace(refused) == "" {
		t.Fatal("syntax error produced an empty message")
	}
}
