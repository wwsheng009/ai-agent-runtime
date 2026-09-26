package agentdef

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAgentFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestDefinitionNewFieldsParseAndNormalize(t *testing.T) {
	def, err := Parse([]byte(`---
name: reviewer
description: Reviews diffs for bugs
tools: ["view", "grep"]
disallowedTools: ["write"]
model: test-model
provider: test-provider
reasoningEffort: HIGH
maxTurns: 7
background: true
showOutput: true
---
Review carefully.
`), "reviewer.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if def.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want high", def.ReasoningEffort)
	}
	if def.MaxTurns != 7 {
		t.Fatalf("MaxTurns = %d, want 7", def.MaxTurns)
	}
	if !def.Background || !def.ShowOutput {
		t.Fatalf("Background/ShowOutput = %v/%v, want true/true", def.Background, def.ShowOutput)
	}
	binding, err := BuildBinding(def)
	if err != nil {
		t.Fatalf("BuildBinding: %v", err)
	}
	if binding.MaxTurns != 7 || binding.ReasoningEffort != "high" || !binding.Background || !binding.ShowOutput {
		t.Fatalf("binding lost new fields: %+v", binding)
	}
}

func TestDefinitionUnknownReasoningEffortDropsWithWarning(t *testing.T) {
	def, err := Parse([]byte(`---
name: typos
description: typo effort
tools: ["view"]
reasoningEffort: meduim
maxTurns: -3
---
Body
`), "typos.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if def.ReasoningEffort != "" {
		t.Fatalf("unknown reasoningEffort must be dropped, got %q", def.ReasoningEffort)
	}
	if def.MaxTurns != 0 {
		t.Fatalf("negative MaxTurns must normalize to 0, got %d", def.MaxTurns)
	}
	joined := strings.Join(def.Warnings, ",")
	if !strings.Contains(joined, "reasoning_effort_unknown:meduim") || !strings.Contains(joined, "max_turns_negative") {
		t.Fatalf("warnings = %v", def.Warnings)
	}
	issues := LintDefinition(def)
	warnings := 0
	for _, issue := range issues {
		if issue.Severity == "warning" {
			warnings++
		}
	}
	if warnings < 2 {
		t.Fatalf("expected at least 2 lint warnings, got %v", issues)
	}
}

func TestDefinitionToolsWildcardMapsToAllowAll(t *testing.T) {
	def, err := Parse([]byte(`---
name: wild
description: wildcard tools
tools: ["*"]
---
Body
`), "wild.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !def.ToolsWildcard() {
		t.Fatal("ToolsWildcard() = false, want true")
	}
	binding, err := BuildBinding(def)
	if err != nil {
		t.Fatalf("BuildBinding: %v", err)
	}
	if !binding.ToolsWildcard || binding.ToolAllowlist != nil {
		t.Fatalf("wildcard binding = %+v, want ToolsWildcard=true and nil allowlist", binding)
	}
}

func TestCatalogRecordsOverridesAndWarnings(t *testing.T) {
	projectRoot := t.TempDir()
	userHome := t.TempDir()
	writeAgentFile(t, filepath.Join(projectRoot, ".agents", "agents"), "explore.md", `---
name: explore
description: project explorer override
tools: ["view", "grep"]
reasoningEffort: nope
---
Project explorer.
`)
	catalog, err := Discover(DiscoverOptions{ProjectRoot: projectRoot, UserHome: userHome})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := catalog.Overridden["explore"]; got != SourceBuiltin {
		t.Fatalf("Overridden[explore] = %q, want builtin", got)
	}
	def, ok := catalog.Get("explore")
	if !ok || def.Source != SourceProject {
		t.Fatalf("explore source = %v (ok=%v), want project", def, ok)
	}
	issues := LintCatalog(catalog)
	foundOverride := false
	for _, issue := range issues {
		if strings.Contains(issue.Message, "覆盖") {
			foundOverride = true
		}
	}
	if !foundOverride {
		t.Fatalf("expected an override lint warning, got %v", issues)
	}
}

func TestModelVisibleSummaryFormatsBuiltinRoles(t *testing.T) {
	catalog := &Catalog{ByName: map[string]*Definition{}}
	for _, def := range BuiltinDefinitions() {
		clone := *def
		clone.Normalize()
		if err := catalog.put(&clone); err != nil {
			t.Fatalf("put %s: %v", def.Name, err)
		}
	}
	catalog.rebuildOrder()
	summary := FormatModelVisibleAgents(ModelVisibleAgents(catalog), 8, 2000)
	for _, want := range []string{"- explore: Read-only codebase explorer", "- plan:", "- general:"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
	if !strings.Contains(summary, "read-only") {
		t.Fatalf("summary should mark the read-only role:\n%s", summary)
	}
}
