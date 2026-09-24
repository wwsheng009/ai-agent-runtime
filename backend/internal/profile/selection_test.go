package profile

import (
	"errors"
	"path/filepath"
	"testing"
)

func assertStringSlice(t *testing.T, name string, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v (len %d), want %v (len %d)", name, got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
	}
}

func TestNormalizePromptMode(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"", PromptModeReplace, true},
		{"  ", PromptModeReplace, true},
		{"replace", PromptModeReplace, true},
		{"REPLACE", PromptModeReplace, true},
		{" append ", PromptModeAppend, true},
		{"Append", PromptModeAppend, true},
		{"prepend", "", false},
		{"merge", "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizePromptMode(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Fatalf("NormalizePromptMode(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestMergeSkillSelections_UnionDedupeAndTrim(t *testing.T) {
	merged := MergeSkillSelections(
		SkillsSpec{Allowlist: []string{" docs ", "review"}, Denylist: []string{"danger"}},
		SkillsSpec{Allowlist: []string{"docs", "release"}, Denylist: []string{" Danger ", "network"}},
	)
	assertStringSlice(t, "allowlist", merged.Allowlist, []string{"docs", "review", "release"})
	assertStringSlice(t, "denylist", merged.Denylist, []string{"danger", "network"})
}

func TestMergeSkillSelections_EmptyMeansNoDeclaration(t *testing.T) {
	merged := MergeSkillSelections()
	if !merged.Empty() {
		t.Fatalf("expected empty selection, got %+v", merged)
	}
	// 无声明时必须保持"全允许"，否则会误伤既有行为（NFR-1）。
	if !merged.AllowsSkill("anything") {
		t.Fatalf("empty selection must allow every skill")
	}
}

func TestResolvedSkillSelection_AllowsSkill(t *testing.T) {
	cases := []struct {
		name      string
		selection ResolvedSkillSelection
		skill     string
		want      bool
	}{
		{"empty allowlist allows all", ResolvedSkillSelection{}, "docs", true},
		{"allowlist restricts", ResolvedSkillSelection{Allowlist: []string{"docs"}}, "release", false},
		{"allowlist admits listed", ResolvedSkillSelection{Allowlist: []string{"docs"}}, "docs", true},
		{"deny wins over allow", ResolvedSkillSelection{Allowlist: []string{"docs"}, Denylist: []string{"docs"}}, "docs", false},
		{"deny wins with empty allowlist", ResolvedSkillSelection{Denylist: []string{"docs"}}, "docs", false},
		{"deny other keeps default allow", ResolvedSkillSelection{Denylist: []string{"docs"}}, "release", true},
		{"case insensitive", ResolvedSkillSelection{Allowlist: []string{"Docs"}}, "docs", true},
		{"wildcard allow", ResolvedSkillSelection{Allowlist: []string{WildcardAll}}, "anything", true},
		{"wildcard deny", ResolvedSkillSelection{Denylist: []string{WildcardAll}}, "anything", false},
		{"blank name never allowed", ResolvedSkillSelection{}, "  ", false},
		{"blank declaration ignored", ResolvedSkillSelection{Denylist: []string{" "}}, "docs", true},
	}
	for _, tc := range cases {
		if got := tc.selection.AllowsSkill(tc.skill); got != tc.want {
			t.Fatalf("%s: AllowsSkill(%q) = %v, want %v", tc.name, tc.skill, got, tc.want)
		}
	}
}

func TestMergeMCPSelections_UnionAndExcludeWins(t *testing.T) {
	// 回归：只含空白的声明不得变成"拒绝一切"的隐式 allowlist。
	blankOnly := ResolvedSkillSelection{Allowlist: []string{"  ", ""}}
	if !blankOnly.Empty() {
		t.Fatalf("blank-only selection must count as empty")
	}
	if !blankOnly.AllowsSkill("docs") {
		t.Fatalf("blank-only allowlist must not deny every skill")
	}
	blankMCP := ResolvedMCPSelection{ExcludeServers: []string{" "}}
	if !blankMCP.Empty() {
		t.Fatalf("blank-only MCP selection must count as empty")
	}
	if !blankMCP.AllowsServer("filesystem") {
		t.Fatalf("blank-only exclude list must not drop every server")
	}

	merged := MergeMCPSelections(
		MCPSpec{UseServers: []string{" filesystem ", "git"}, ExcludeServers: []string{"browser"}},
		MCPSpec{UseServers: []string{"Filesystem", "search"}, ExcludeServers: []string{" Browser "}},
	)
	assertStringSlice(t, "use_servers", merged.UseServers, []string{"filesystem", "git", "search"})
	assertStringSlice(t, "exclude_servers", merged.ExcludeServers, []string{"browser"})

	if merged.AllowsServer("git") != true {
		t.Fatalf("git should be allowed")
	}
	if merged.AllowsServer("browser") {
		t.Fatalf("excluded server must not be allowed even if used elsewhere")
	}
	if merged.AllowsServer("search") != true {
		t.Fatalf("search should be allowed")
	}
}

func TestResolvedMCPSelection_AllowsServer(t *testing.T) {
	cases := []struct {
		name      string
		selection ResolvedMCPSelection
		server    string
		want      bool
	}{
		{"empty allows all", ResolvedMCPSelection{}, "filesystem", true},
		{"use restricts", ResolvedMCPSelection{UseServers: []string{"filesystem"}}, "git", false},
		{"exclude wins", ResolvedMCPSelection{UseServers: []string{"filesystem"}, ExcludeServers: []string{"filesystem"}}, "filesystem", false},
		{"wildcard exclude", ResolvedMCPSelection{ExcludeServers: []string{WildcardAll}}, "git", false},
		{"wildcard use", ResolvedMCPSelection{UseServers: []string{WildcardAll}}, "git", true},
	}
	for _, tc := range cases {
		if got := tc.selection.AllowsServer(tc.server); got != tc.want {
			t.Fatalf("%s: AllowsServer(%q) = %v, want %v", tc.name, tc.server, got, tc.want)
		}
	}
}

func TestValidateProfileSpec_ValidSpecHasNoIssues(t *testing.T) {
	spec := &ProfileSpec{
		Prompts: PromptsSpec{Mode: PromptModeAppend},
		Skills:  SkillsSpec{Allowlist: []string{"docs"}, Denylist: []string{"danger"}},
		MCP:     MCPSpec{UseServers: []string{"filesystem"}, ExcludeServers: []string{"browser"}},
	}
	if issues := ValidateProfileSpec(spec); len(issues) != 0 {
		t.Fatalf("expected no issues, got %+v", issues)
	}
}

func TestValidateProfileSpec_ReportsUnknownModeAndConflicts(t *testing.T) {
	spec := &ProfileSpec{
		Prompts: PromptsSpec{Mode: "prepend"},
		Skills:  SkillsSpec{Allowlist: []string{"docs", "docs"}, Denylist: []string{"docs", " "}},
		MCP:     MCPSpec{UseServers: []string{"filesystem"}, ExcludeServers: []string{"filesystem"}},
	}
	issues := ValidateProfileSpec(spec)
	counts := map[ProfileSpecIssueSeverity]int{}
	paths := map[string]int{}
	for _, issue := range issues {
		counts[issue.Severity]++
		paths[issue.Path]++
	}
	if counts[ProfileSpecIssueError] != 2 {
		t.Fatalf("expected 2 errors (unknown mode + blank name), got %d: %+v", counts[ProfileSpecIssueError], issues)
	}
	if counts[ProfileSpecIssueWarning] != 3 {
		t.Fatalf("expected 3 warnings (duplicate skill + deny conflict + mcp conflict), got %d: %+v", counts[ProfileSpecIssueWarning], issues)
	}
	if paths["prompts.mode"] != 1 || paths["skills.allowlist/denylist"] != 3 || paths["mcp.use_servers/exclude_servers"] != 1 {
		t.Fatalf("unexpected issue paths: %+v", issues)
	}
}

func TestValidateProfileSpec_NilSpecIsValid(t *testing.T) {
	if issues := ValidateProfileSpec(nil); len(issues) != 0 {
		t.Fatalf("expected no issues for nil spec, got %+v", issues)
	}
}

func TestResolve_RejectsUnknownPromptMode(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "profile.yaml"), `profile:
  name: dev
  default_agent: coder
prompts:
  mode: prepend
agents:
  coder: {}
`)
	_, err := Resolve(ResolveOptions{Root: root})
	if !errors.Is(err, ErrInvalidProfileSpec) {
		t.Fatalf("expected ErrInvalidProfileSpec, got %v", err)
	}
}

func TestResolve_PopulatesSelectionDeclarations(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "profile.yaml"), `profile:
  name: dev
  default_agent: coder
prompts:
  mode: append
skills:
  allowlist: [docs, review]
  denylist: [danger]
mcp:
  use_servers: [filesystem, git]
  exclude_servers: [browser]
agents:
  coder: {}
`)
	resolved, err := Resolve(ResolveOptions{Root: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.PromptMode != PromptModeAppend {
		t.Fatalf("expected prompt mode append, got %q", resolved.PromptMode)
	}
	if resolved.Skills.Empty() {
		t.Fatalf("expected non-empty skill selection")
	}
	if resolved.Skills.AllowsSkill("danger") {
		t.Fatalf("danger must be denied")
	}
	if !resolved.Skills.AllowsSkill("docs") {
		t.Fatalf("docs must be allowed")
	}
	if resolved.MCPSelection.Empty() {
		t.Fatalf("expected non-empty MCP selection")
	}
	if resolved.MCPSelection.AllowsServer("browser") {
		t.Fatalf("browser must be excluded")
	}
	if !resolved.MCPSelection.AllowsServer("git") {
		t.Fatalf("git must be allowed")
	}
}

func TestResolve_DefaultsToReplaceModeAndEmptySelections(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "profile.yaml"), `profile:
  name: dev
  default_agent: coder
agents:
  coder: {}
`)
	resolved, err := Resolve(ResolveOptions{Root: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// 无声明路径必须保持零变化（NFR-1）。
	if resolved.PromptMode != PromptModeReplace {
		t.Fatalf("expected default prompt mode replace, got %q", resolved.PromptMode)
	}
	if !resolved.Skills.Empty() || !resolved.MCPSelection.Empty() {
		t.Fatalf("expected empty selections, got skills=%+v mcp=%+v", resolved.Skills, resolved.MCPSelection)
	}
}
