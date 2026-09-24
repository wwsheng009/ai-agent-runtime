package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func gateTestResolved(profileRoot string) *ResolvedAgent {
	return &ResolvedAgent{
		ProfileName: "dev",
		ProfileRoot: profileRoot,
		AgentID:     "coder",
		Prompts: ResolvedPromptFiles{
			System: filepath.Join(profileRoot, "agents", "coder", "prompts", "system.md"),
			Tools:  filepath.Join(profileRoot, "agents", "coder", "prompts", "tools.md"),
		},
		PromptMode: PromptModeReplace,
		Paths:      ResolvedPaths{ProfileRoot: profileRoot},
	}
}

func TestEvaluateProjectPromptGate(t *testing.T) {
	workspace := t.TempDir()
	projectProfileRoot := filepath.Join(workspace, ".aicli", "profiles", "dev")
	foreignProfileRoot := t.TempDir()

	cases := []struct {
		name        string
		root        string
		workspace   string
		trusted     bool
		wantBlocked bool
	}{
		{"trusted workspace keeps prompts", projectProfileRoot, workspace, true, false},
		{"untrusted project profile blocked", projectProfileRoot, workspace, false, true},
		{"untrusted foreign profile untouched", foreignProfileRoot, workspace, false, false},
		{"unknown workspace root fails closed", projectProfileRoot, "", false, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			gate := EvaluateProjectPromptGate(gateTestResolved(tc.root), tc.workspace, tc.trusted)
			if gate.Suppressed != tc.wantBlocked {
				t.Fatalf("Suppressed = %v, want %v", gate.Suppressed, tc.wantBlocked)
			}
			if tc.wantBlocked && gate.Reason == "" {
				t.Fatal("blocked gate must carry a user-facing reason")
			}
			if !tc.wantBlocked && gate.Reason != "" {
				t.Fatalf("unblocked gate must not carry a reason, got %q", gate.Reason)
			}
		})
	}
}

func TestEvaluateProjectPromptGateSkipsUserLayerHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no user home available")
	}
	workspace := t.TempDir()
	userProfileRoot := filepath.Join(home, ".aicli", "profiles", "dev")

	if gate := EvaluateProjectPromptGate(gateTestResolved(userProfileRoot), workspace, false); gate.Suppressed {
		t.Fatal("user-layer profile must never be suppressed by workspace trust")
	}
}

func TestEvaluateProjectPromptGateWithoutPromptContent(t *testing.T) {
	workspace := t.TempDir()
	resolved := gateTestResolved(filepath.Join(workspace, ".aicli", "profiles", "dev"))
	resolved.Prompts = ResolvedPromptFiles{}

	if gate := EvaluateProjectPromptGate(resolved, workspace, false); gate.Suppressed {
		t.Fatal("profile without prompt files must not raise a suppression warning")
	}
}

func TestApplyProjectPromptGate(t *testing.T) {
	workspace := t.TempDir()
	resolved := gateTestResolved(filepath.Join(workspace, ".aicli", "profiles", "dev"))

	if !ApplyProjectPromptGate(resolved, workspace, false) {
		t.Fatal("expected prompts to be withheld")
	}
	if resolved.Prompts.System != "" || resolved.Prompts.Role != "" || resolved.Prompts.Tools != "" {
		t.Fatalf("prompt carriers must be cleared, got %#v", resolved.Prompts)
	}
	if !resolved.PromptSuppressed || resolved.PromptSuppressionReason == "" {
		t.Fatalf("suppression must be recorded: %+v", resolved)
	}
	// 声明保留：mode 只是 replace/append 语义，内容为空时无副作用。
	if resolved.PromptMode != PromptModeReplace {
		t.Fatalf("prompt mode declaration must survive the gate, got %q", resolved.PromptMode)
	}
	// 分级门控：其余生效面不受影响。
	if resolved.AgentID != "coder" || resolved.ProfileName != "dev" {
		t.Fatalf("non-prompt fields must be untouched, got %+v", resolved)
	}

	trusted := gateTestResolved(filepath.Join(workspace, ".aicli", "profiles", "dev"))
	if ApplyProjectPromptGate(trusted, workspace, true) {
		t.Fatal("trusted workspace must keep prompts")
	}
	if trusted.Prompts.System == "" || trusted.PromptSuppressed {
		t.Fatalf("trusted resolution must stay intact, got %+v", trusted)
	}
}

func TestApplyProjectPromptGateNilAndFeatureOff(t *testing.T) {
	if ApplyProjectPromptGate(nil, "x", false) {
		t.Fatal("nil resolved must be a no-op")
	}
	workspace := t.TempDir()
	resolved := gateTestResolved(filepath.Join(workspace, ".aicli", "profiles", "dev"))
	// 特性关闭时 foldertrust 既有语义给出 Trusted=true；调用方直接传该结论。
	if ApplyProjectPromptGate(resolved, workspace, true) {
		t.Fatal("feature-off resolution (trusted) must not suppress")
	}
}
