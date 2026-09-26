package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustSpecifier(t *testing.T, raw string, decision DecisionType) ToolSpecifier {
	t.Helper()
	spec, err := ParseToolSpecifier(raw, decision)
	require.NoError(t, err)
	require.NotNil(t, spec, "entry %q should parse as a specifier", raw)
	return *spec
}

func TestParseToolSpecifierForms(t *testing.T) {
	// Plain tool names keep legacy exact semantics and produce no specifier.
	for _, raw := range []string{"shell", "view", "write", "mcp__github__get_issue"} {
		spec, err := ParseToolSpecifier(raw, DecisionAllow)
		require.NoError(t, err)
		require.Nil(t, spec, "plain name %q must stay an exact tool entry", raw)
	}

	command := mustSpecifier(t, "Shell(git status)", DecisionAllow)
	assert.Equal(t, SpecifierKindCommand, command.Kind)
	assert.True(t, command.Group)
	assert.Equal(t, "git status", command.Pattern)

	path := mustSpecifier(t, "Read(**/*.pem)", DecisionAsk)
	assert.Equal(t, SpecifierKindPath, path.Kind)
	assert.Equal(t, "**/*.pem", path.Pattern)

	edit := mustSpecifier(t, "Edit(src/**)", DecisionAllow)
	assert.Equal(t, SpecifierKindPath, edit.Kind)

	domain := mustSpecifier(t, "WebFetch(domain:*.example.com)", DecisionAllow)
	assert.Equal(t, SpecifierKindDomain, domain.Kind)
	assert.Equal(t, "*.example.com", domain.Pattern)

	param := mustSpecifier(t, "Shell(run_in_background:true)", DecisionDeny)
	assert.Equal(t, SpecifierKindParam, param.Kind)
	assert.Equal(t, "run_in_background", param.Param)
	assert.Equal(t, "true", param.Pattern)

	glob := mustSpecifier(t, "mcp__github__get_*", DecisionAllow)
	assert.Equal(t, SpecifierKindToolGlob, glob.Kind)

	// Allow-side restrictions of §4.10.
	_, err := ParseToolSpecifier("Shell(run_in_background:true)", DecisionAllow)
	assert.Error(t, err)
	_, err = ParseToolSpecifier("*", DecisionAllow)
	assert.Error(t, err)
	_, err = ParseToolSpecifier("mcp__*", DecisionAllow)
	assert.Error(t, err)
	_, err = ParseToolSpecifier("mcp__github__get_*", DecisionAllow)
	assert.NoError(t, err)
	_, err = ParseToolSpecifier("shell(command:ls)", DecisionDeny)
	assert.Error(t, err)
}

func shellEvalRequest(args map[string]interface{}) EvalRequest {
	return EvalRequest{ToolName: "shell", Args: args}
}

func TestCommandSpecifierMatching(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name     string
		pattern  string
		decision DecisionType
		args     map[string]interface{}
		want     bool
	}{
		{"exact match", "git status", DecisionAllow, map[string]interface{}{"command": "git status"}, true},
		{"exact is space sensitive", "git status", DecisionAllow, map[string]interface{}{"command": "git status --short"}, false},
		{"prefix form", "git:*", DecisionAllow, map[string]interface{}{"command": "git log -5"}, true},
		{"prefix form does not match other commands", "git:*", DecisionAllow, map[string]interface{}{"command": "gitk --all"}, false},
		{"deny matches any segment", "rm -rf .", DecisionDeny, map[string]interface{}{"command": "git status && rm -rf ."}, true},
		{"allow requires every segment", "git status", DecisionAllow, map[string]interface{}{"command": "git status && rm -rf ."}, false},
		{"wrapper is pierced", "rm -rf .", DecisionDeny, map[string]interface{}{"command": "timeout 5 rm -rf ."}, true},
		{"env prefix is stripped", "git status", DecisionAllow, map[string]interface{}{"command": "FOO=bar git status"}, true},
		{"interpreter payload never auto-allows", "git status", DecisionAllow, map[string]interface{}{"command": `bash -c "git status"`}, false},
		{"batch allow requires all entries", "git:*", DecisionAllow, map[string]interface{}{"commands": []interface{}{
			map[string]interface{}{"command": "git status"},
			map[string]interface{}{"command": "git diff"},
		}}, true},
		{"batch deny hits one entry", "git diff", DecisionDeny, map[string]interface{}{"commands": []interface{}{
			map[string]interface{}{"command": "git status"},
			map[string]interface{}{"command": "git diff"},
		}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := mustSpecifier(t, "Shell("+tc.pattern+")", tc.decision)
			matched, detail := spec.MatchesTool(root, shellEvalRequest(tc.args), tc.decision)
			assert.Equal(t, tc.want, matched, detail)
		})
	}
}

func TestPathSpecifierMatching(t *testing.T) {
	root := t.TempDir()
	home, homeErr := os.UserHomeDir()
	cases := []struct {
		name     string
		pattern  string
		decision DecisionType
		target   string
		want     bool
	}{
		{"relative glob", "src/**", DecisionAllow, "src/main.go", true},
		{"single star does not cross separators", "src/*.go", DecisionAllow, "src/deep/main.go", false},
		{"double star crosses separators", "**/*.pem", DecisionAsk, "certs/a.pem", true},
		{"basename matches at any depth", ".env", DecisionAsk, "config/.env", true},
		{"workspace anchor", "/src/**", DecisionAllow, "src/x.go", true},
		{"workspace anchor on absolute target", "src/**", DecisionAllow, filepath.Join(root, "src", "x.go"), true},
		{"filesystem anchor", "//etc/hosts", DecisionDeny, "/etc/hosts", true},
		{"ask folds case", ".ENV", DecisionAsk, "config/.env", true},
		{"allow stays exact", "src/a.go", DecisionAllow, "src/b.go", false},
	}
	if homeErr == nil && home != "" {
		cases = append(cases, struct {
			name     string
			pattern  string
			decision DecisionType
			target   string
			want     bool
		}{"home anchor", "~/.ssh/id_rsa", DecisionDeny, filepath.Join(home, ".ssh", "id_rsa"), true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := mustSpecifier(t, "Read("+tc.pattern+")", tc.decision)
			req := EvalRequest{ToolName: "view", Args: map[string]interface{}{"file_path": tc.target}}
			matched, detail := spec.MatchesTool(root, req, tc.decision)
			assert.Equal(t, tc.want, matched, detail)
		})
	}
}

func TestPathSpecifierAllowRequiresEveryTarget(t *testing.T) {
	root := t.TempDir()
	patch := "*** Begin Patch\n*** Update File: src/a.go\n*** Update File: other/b.go\n*** End Patch\n"
	req := EvalRequest{ToolName: "apply_patch", Args: map[string]interface{}{"patch": patch}}

	narrow := mustSpecifier(t, "Edit(src/**)", DecisionAllow)
	matched, _ := narrow.MatchesTool(root, req, DecisionAllow)
	assert.False(t, matched, "one uncovered target must stop an allow rule")

	wide := mustSpecifier(t, "Edit(**/*.go)", DecisionAllow)
	matched, _ = wide.MatchesTool(root, req, DecisionAllow)
	assert.True(t, matched)
}

func TestDomainSpecifierMatching(t *testing.T) {
	spec := mustSpecifier(t, "WebFetch(domain:*.example.com)", DecisionAllow)
	req := EvalRequest{ToolName: "fetch", Args: map[string]interface{}{"url": "https://api.example.com/v1"}}
	matched, _ := spec.MatchesTool("", req, DecisionAllow)
	assert.True(t, matched)

	apex := EvalRequest{ToolName: "fetch", Args: map[string]interface{}{"url": "https://example.com/v1"}}
	matched, _ = spec.MatchesTool("", apex, DecisionAllow)
	assert.False(t, matched, "a subdomain glob does not cover the apex")

	deny := mustSpecifier(t, "WebFetch(example.com)", DecisionDeny)
	matched, _ = deny.MatchesTool("", apex, DecisionDeny)
	assert.True(t, matched)
}

func TestParamSpecifierMatching(t *testing.T) {
	spec := mustSpecifier(t, "Shell(run_in_background:true)", DecisionDeny)

	hit := shellEvalRequest(map[string]interface{}{"command": "sleep 1", "run_in_background": true})
	matched, _ := spec.MatchesTool("", hit, DecisionDeny)
	assert.True(t, matched)

	absent := shellEvalRequest(map[string]interface{}{"command": "sleep 1"})
	matched, _ = spec.MatchesTool("", absent, DecisionDeny)
	assert.False(t, matched, "an unsent parameter must not match")

	otherValue := shellEvalRequest(map[string]interface{}{"command": "sleep 1", "run_in_background": false})
	matched, _ = spec.MatchesTool("", otherValue, DecisionDeny)
	assert.False(t, matched)
}

func TestToolGlobSpecifierMatching(t *testing.T) {
	denyAll := mustSpecifier(t, "mcp__*", DecisionDeny)
	req := EvalRequest{ToolName: "mcp__github__get_issue"}
	matched, _ := denyAll.MatchesTool("", req, DecisionDeny)
	assert.True(t, matched)

	denyNamed := mustSpecifier(t, "mcp__github__get_*", DecisionDeny)
	assert.True(t, func() bool { m, _ := denyNamed.MatchesTool("", req, DecisionDeny); return m }())
	other := EvalRequest{ToolName: "mcp__gitlab__get_issue"}
	matched, _ = denyNamed.MatchesTool("", other, DecisionDeny)
	assert.False(t, matched)

	editGlob := mustSpecifier(t, "edit_*", DecisionAllow)
	editReq := EvalRequest{ToolName: "edit_file"}
	matched, _ = editGlob.MatchesTool("", editReq, DecisionAllow)
	assert.True(t, matched)
}

func TestPermissionsFileParsesSpecifierRules(t *testing.T) {
	file, err := ParsePermissionsFile([]byte(`
version: 1
rules:
  - name: allow-git-read
    tools: ["Shell(git:*)", "view"]
    decision: allow
  - name: protect-secrets
    tools: ["Read(.env)", "Edit(**/*.pem)"]
    decision: ask
`))
	require.NoError(t, err)
	rules := file.ToRules("project")
	require.Len(t, rules, 2)
	assert.Equal(t, []string{"view"}, rules[0].Tools, "plain names stay in Tools")
	require.Len(t, rules[0].Specifiers, 1)
	assert.Equal(t, SpecifierKindCommand, rules[0].Specifiers[0].Kind)
	require.Len(t, rules[1].Specifiers, 2)
	assert.Empty(t, rules[1].Tools)
}

func TestPermissionsFileRejectsIllegalAllowSpecifiers(t *testing.T) {
	for name, body := range map[string]string{
		"bare wildcard":        "version: 1\nrules:\n  - tools: [\"*\"]\n    decision: allow\n",
		"mcp wildcard":         "version: 1\nrules:\n  - tools: [\"mcp__*\"]\n    decision: allow\n",
		"allow param":          "version: 1\nrules:\n  - tools: [\"Shell(run_in_background:true)\"]\n    decision: allow\n",
		"deny_tools specifier": "version: 1\ndeny_tools: [\"Shell(git:*)\"]\n",
		"allow_tools glob":     "version: 1\nallow_tools: [\"mcp__*\"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePermissionsFile([]byte(body))
			assert.Error(t, err)
		})
	}
}

func TestEngineSpecifierRules(t *testing.T) {
	engine := &Engine{
		Mode: ModeDefault,
		Rules: []Rule{{
			Name:     "allow-git-status",
			Tools:    nil,
			Decision: DecisionAllow,
			Reason:   "project_allow_git_status",
			Specifiers: []ToolSpecifier{
				mustSpecifier(t, "Shell(git status)", DecisionAllow),
			},
		}},
		Policy: NewToolExecutionPolicy(nil, false),
	}
	decision, err := engine.Evaluate(context.Background(), shellEvalRequest(map[string]interface{}{"command": "git status"}))
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageRules, decision.Stage)

	// The allow rule must not authorize a compound command with a mutating tail.
	handler := &recordingApprovalHandler{response: ApprovalResponse{Allowed: false, Reason: "approval_denied"}}
	engine.AskHandler = handler
	decision, err = engine.Evaluate(context.Background(), shellEvalRequest(map[string]interface{}{"command": "git status && rm -rf x"}))
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, 1, handler.calls, "the tail segment falls through to the mode ask")
}
