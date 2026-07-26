package policy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePermissionsFileAndToRules(t *testing.T) {
	raw := []byte(`
version: 1
deny_tools: [shell, aicli_exec]
allow_tools: [view, grep]
rules:
  - name: ask-writes
    tools: [write, edit]
    decision: ask
    reason: review_writes
  - name: allow-ls
    tools: [ls]
    decision: allow
`)
	file, err := ParsePermissionsFile(raw)
	if err != nil {
		t.Fatalf("ParsePermissionsFile: %v", err)
	}
	if file.Version != 1 {
		t.Fatalf("version: got %d", file.Version)
	}
	rules := file.ToRules("project")
	if len(rules) < 4 {
		t.Fatalf("expected deny_tools + rules, got %d: %+v", len(rules), rules)
	}
	if rules[0].Decision != DecisionDeny || rules[0].Tools[0] != "shell" {
		t.Fatalf("first rule should be deny shell, got %+v", rules[0])
	}
	foundAsk := false
	for _, rule := range rules {
		if rule.Name == "project:ask-writes" && rule.Decision == DecisionAsk {
			foundAsk = true
		}
	}
	if !foundAsk {
		t.Fatalf("missing ask-writes rule: %+v", rules)
	}
}

func TestParsePermissionsFileRejectsBadDecision(t *testing.T) {
	_, err := ParsePermissionsFile([]byte(`
rules:
  - tools: [write]
    decision: maybe
`))
	if err == nil {
		t.Fatal("expected invalid decision error")
	}
}

func TestLoadProjectPermissions(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".aicli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "permissions.yaml")
	if err := os.WriteFile(path, []byte("deny_tools: [download]\nrules:\n  - tools: [fetch]\n    decision: deny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := LoadProjectPermissions(root)
	if err != nil {
		t.Fatalf("LoadProjectPermissions: %v", err)
	}
	if file == nil || file.SourcePath != path {
		t.Fatalf("unexpected file: %+v", file)
	}
	if len(file.DenyTools) != 1 || file.DenyTools[0] != "download" {
		t.Fatalf("deny_tools: %+v", file.DenyTools)
	}
}

func TestBuildPermissionsOverlayCLIDenyFirst(t *testing.T) {
	project := &PermissionsFile{
		SourcePath: "/proj/.aicli/permissions.yaml",
		Rules: []PermissionsFileRule{{
			Name:     "allow-shell",
			Tools:    []string{"shell"},
			Decision: "allow",
		}},
	}
	overlay := BuildPermissionsOverlay(project, []string{"view"}, []string{"shell", "write"})
	if len(overlay.Rules) < 3 {
		t.Fatalf("rules: %+v", overlay.Rules)
	}
	if overlay.Rules[0].Decision != DecisionDeny || overlay.Rules[0].Tools[0] != "shell" {
		t.Fatalf("CLI deny should be first, got %+v", overlay.Rules[0])
	}
	// Engine evaluate: CLI deny shell wins over project allow shell.
	engine := &Engine{Mode: ModeDefault, Rules: overlay.Rules}
	decision, err := engine.Evaluate(nil, EvalRequest{ToolName: "shell", Mode: ModeDefault})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Type != DecisionDeny || decision.Stage != StageRules {
		t.Fatalf("want deny@rules, got %+v", decision)
	}
	// CLI allow view
	decision, err = engine.Evaluate(nil, EvalRequest{ToolName: "view", Mode: ModeDefault})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Type != DecisionAllow || decision.Stage != StageRules {
		t.Fatalf("want allow@rules for view, got %+v", decision)
	}
}

func TestApplyPermissionsOverlayToPolicy(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, false)
	overlay := BuildPermissionsOverlay(&PermissionsFile{DenyTools: []string{"download"}}, []string{"view"}, []string{"shell"})
	policy = ApplyPermissionsOverlayToPolicy(policy, overlay)
	if err := policy.AllowTool("shell"); err == nil {
		t.Fatal("expected shell denied")
	}
	if err := policy.AllowTool("download"); err == nil {
		t.Fatal("expected download denied")
	}
	if err := policy.AllowTool("view"); err != nil {
		t.Fatalf("view should be allowlisted: %v", err)
	}
	if err := policy.AllowTool("write"); err == nil {
		t.Fatal("write should not be on allowlist")
	}
}

func TestApplyPermissionsOverlayToEnginePrepends(t *testing.T) {
	engine := &Engine{
		Mode: ModeDefault,
		Rules: []Rule{{
			Name:     "legacy",
			Tools:    []string{"view"},
			Decision: DecisionAsk,
		}},
	}
	overlay := BuildPermissionsOverlay(nil, nil, []string{"view"})
	ApplyPermissionsOverlayToEngine(engine, overlay)
	decision, err := engine.Evaluate(nil, EvalRequest{ToolName: "view"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Type != DecisionDeny {
		t.Fatalf("CLI deny should win over legacy ask, got %+v", decision)
	}
}
