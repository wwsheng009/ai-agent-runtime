package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
)

func TestNormalizeAgentAliases(t *testing.T) {
	cases := map[string]string{
		"":                 "auto",
		"auto":             "auto",
		"dsh":              string(sessionport.AgentDeepSeekHarness),
		"deepseek-harness": string(sessionport.AgentDeepSeekHarness),
		"workbuddy-ai":     string(sessionport.AgentWorkBuddy),
		"workbuddy":        string(sessionport.AgentWorkBuddy),
	}
	for input, want := range cases {
		got, err := NormalizeAgent(input)
		if err != nil || got != want {
			t.Fatalf("NormalizeAgent(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := NormalizeAgent("unknown"); err == nil {
		t.Fatal("expected unknown agent to fail")
	}
}

func TestDetectAgentForPathPrefersWorkBuddySignature(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "workbuddy.db"), []byte("not-a-db"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := detectAgentForPath(root, "", ""); got != string(sessionport.AgentWorkBuddy) {
		t.Fatalf("detectAgentForPath = %q, want workbuddy", got)
	}
}

func TestDetectAgentForPathDSH(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sessions", "--work--", "session-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := detectAgentForPath(root, "", ""); got != string(sessionport.AgentDeepSeekHarness) {
		t.Fatalf("detectAgentForPath = %q, want deepseek-harness", got)
	}
}

func TestResolveInstanceAliases(t *testing.T) {
	root, err := ResolveInstance("workbuddy:ai")
	if err != nil || root == "" {
		t.Fatalf("workbuddy:ai: root=%q err=%v", root, err)
	}
	root, err = ResolveInstance("workbuddy-ai:default")
	if err != nil || root == "" {
		t.Fatalf("workbuddy-ai:default alias: root=%q err=%v", root, err)
	}
	if _, err := ResolveInstance("workbuddy:deadbeef"); err == nil {
		t.Fatal("custom hash without --root should fail")
	}
}
