package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

func newAddDirTestSession(workspace string) *ChatSession {
	context := map[string]interface{}{}
	if workspace != "" {
		context[chatPlanWorkspacePathKey] = workspace
	}
	return &ChatSession{
		RuntimeSession: &runtimechat.Session{
			ID: "add-dir-test",
			Metadata: runtimechat.SessionMetadata{
				Context: context,
			},
		},
	}
}

func TestAddDirCommandAddsListsAndRemoves(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	session := newAddDirTestSession(workspace)

	out := addDirCommandText(session, "/add-dir "+outside)
	if !strings.Contains(out, "已添加外部目录") {
		t.Fatalf("expected add confirmation, got %q", out)
	}
	roots := chatAllowedRoots(session)
	if len(roots) != 1 || !strings.EqualFold(roots[0], filepath.Clean(outside)) {
		t.Fatalf("AllowedRoots = %#v, want %q", roots, outside)
	}
	if raw, ok := session.RuntimeSession.Metadata.Context[chatAllowedRootsContextKey]; !ok {
		t.Fatal("expected the admitted set to be persisted into session metadata")
	} else if persisted, ok := raw.([]string); !ok || len(persisted) != 1 {
		t.Fatalf("persisted roots = %#v", raw)
	}

	list := addDirCommandText(session, "/add-dir list")
	if !strings.Contains(list, filepath.Clean(outside)) {
		t.Fatalf("list output missing the admitted dir: %q", list)
	}

	dup := addDirCommandText(session, "/add-dir "+outside)
	if !strings.Contains(dup, "已准入") {
		t.Fatalf("expected duplicate note, got %q", dup)
	}
	if got := chatAllowedRoots(session); len(got) != 1 {
		t.Fatalf("duplicate add changed the set: %#v", got)
	}

	removed := addDirCommandText(session, "/add-dir remove "+outside)
	if !strings.Contains(removed, "已移除外部目录") {
		t.Fatalf("expected removal confirmation, got %q", removed)
	}
	if got := chatAllowedRoots(session); len(got) != 0 {
		t.Fatalf("expected empty set after removal, got %#v", got)
	}
}

func TestAddDirCommandRejectsInvalidPaths(t *testing.T) {
	workspace := t.TempDir()
	session := newAddDirTestSession(workspace)

	missing := filepath.Join(t.TempDir(), "nope")
	if out := addDirCommandText(session, "/add-dir "+missing); !strings.Contains(out, "目录不存在") {
		t.Fatalf("expected missing-dir error, got %q", out)
	}
	if got := chatAllowedRoots(session); len(got) != 0 {
		t.Fatalf("invalid path must not be admitted: %#v", got)
	}

	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if out := addDirCommandText(session, "/add-dir "+file); !strings.Contains(out, "不是目录") {
		t.Fatalf("expected not-a-directory error, got %q", out)
	}

	if out := addDirCommandText(session, "/add-dir "+workspace); !strings.Contains(out, "已在工作区内") {
		t.Fatalf("expected workspace note, got %q", out)
	}
	if got := chatAllowedRoots(session); len(got) != 0 {
		t.Fatalf("workspace path must not be admitted: %#v", got)
	}
}

func TestAddDirCommandSupportsQuotedPathsAndMultipleEntries(t *testing.T) {
	workspace := t.TempDir()
	first := t.TempDir()
	second := t.TempDir()
	session := newAddDirTestSession(workspace)

	out := addDirCommandText(session, `/add-dir "`+first+`" `+second)
	if !strings.Contains(out, "已添加外部目录") {
		t.Fatalf("expected add confirmation, got %q", out)
	}
	if got := chatAllowedRoots(session); len(got) != 2 {
		t.Fatalf("expected two admitted dirs, got %#v", got)
	}
}

func TestAddDirFlagValidation(t *testing.T) {
	valid := t.TempDir()
	roots, err := applyChatAddDirFlagArgs([]string{valid, valid})
	if err != nil {
		t.Fatalf("valid --add-dir rejected: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("expected deduped roots, got %#v", roots)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := applyChatAddDirFlagArgs([]string{missing}); err == nil {
		t.Fatal("expected --add-dir validation error for a missing directory")
	}
}

func TestChatAllowedRootsFallsBackToSessionMetadata(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: &runtimechat.Session{
			ID: "resume-test",
			Metadata: runtimechat.SessionMetadata{
				Context: map[string]interface{}{
					chatAllowedRootsContextKey: []interface{}{"/ext/a", "", "/ext/a", "/ext/b"},
				},
			},
		},
	}
	got := chatAllowedRoots(session)
	want := []string{"/ext/a", "/ext/b"}
	if len(got) != len(want) {
		t.Fatalf("AllowedRoots = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllowedRoots = %#v, want %#v", got, want)
		}
	}
}

func TestApplyACPAdditionalDirectoriesSkipsInvalidEntries(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	session := newAddDirTestSession(workspace)

	added := applyACPAdditionalDirectories(session, []string{outside, filepath.Join(t.TempDir(), "missing")})
	if len(added) != 1 || !strings.EqualFold(added[0], filepath.Clean(outside)) {
		t.Fatalf("added = %#v, want %q", added, outside)
	}
	if got := chatAllowedRoots(session); len(got) != 1 {
		t.Fatalf("AllowedRoots = %#v", got)
	}

	// A second call with the same directory is idempotent.
	if again := applyACPAdditionalDirectories(session, []string{outside}); len(again) != 0 {
		t.Fatalf("expected no new admission, got %#v", again)
	}
}

func TestSplitChatPathArguments(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a b", []string{"a", "b"}},
		{`"a b" c`, []string{"a b", "c"}},
		{`remove "C:\my dir"`, []string{"remove", `C:\my dir`}},
	}
	for _, tc := range cases {
		got := splitChatPathArguments(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("splitChatPathArguments(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("splitChatPathArguments(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		}
	}
}

func TestBuildLocalChatAgentCarriesAllowedRoots(t *testing.T) {
	outside := t.TempDir()
	session := &ChatSession{AllowedRoots: []string{outside}}
	host := &localChatRuntimeHost{Bootstrap: &runtimebootstrap.Manager{}}

	apiAgent := buildLocalChatAgent(session, host, nil, "", "", "")
	if apiAgent == nil {
		t.Fatal("expected agent")
	}
	cfg := apiAgent.GetConfig()
	if cfg == nil || cfg.Options == nil {
		t.Fatal("expected agent options")
	}
	roots, ok := cfg.Options["allowed_roots"].([]string)
	if !ok || len(roots) != 1 || roots[0] != outside {
		t.Fatalf("allowed_roots option = %#v, want %q", cfg.Options["allowed_roots"], outside)
	}
}
