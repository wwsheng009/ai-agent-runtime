package hooks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadDeclarativeFileParsesCodexShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "description": "demo",
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash|Write",
        "hooks": [
          {"type": "command", "command": "check.sh", "timeout": 12, "statusMessage": "checking"},
          {"type": "command", "command": "async.sh", "async": true}
        ]
      }
    ],
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "prompt.sh"}]}
    ]
  }
}`), 0o600))

	hooks, warnings, err := LoadDeclarativeFile(path, ScopeProject, "")
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, hooks, 3)

	require.Equal(t, EventPreToolUse, hooks[0].Event)
	require.Equal(t, "Bash|Write", hooks[0].Matcher)
	require.Equal(t, "check.sh", hooks[0].Exec.Command)
	require.Equal(t, "shell", hooks[0].Exec.Type)
	require.Equal(t, 12.0, hooks[0].Timeout.Seconds())
	require.Equal(t, "checking", hooks[0].StatusMessage)
	require.False(t, hooks[0].Async)
	require.True(t, hooks[1].Async)
	require.Equal(t, EventUserPromptSubmit, hooks[2].Event)
	require.NotEmpty(t, hooks[0].ID)
}

func TestLoadDeclarativeFileKeepsLegacyFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"hooks":[{"id":"legacy","event":"stop","exec":{"type":"http","url":"http://example.invalid"}}]}`), 0o600))

	hooks, _, err := LoadDeclarativeFile(path, ScopeRuntime, "")
	require.NoError(t, err)
	require.Len(t, hooks, 1)
	require.Equal(t, "legacy", hooks[0].ID)
	require.Equal(t, EventStop, hooks[0].Event)
}

func TestLoadDeclarativeFileParsesMCPTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "hooks": {
    "PostToolUse": [
      {"hooks": [{"type": "mcp_tool", "server": "lint", "tool": "check", "input": {"level": "strict"}}]}
    ]
  }
}`), 0o600))

	hooks, warnings, err := LoadDeclarativeFile(path, ScopeProject, "")
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, hooks, 1)
	require.Equal(t, "mcp", hooks[0].Exec.Type)
	require.Equal(t, "lint", hooks[0].Exec.Server)
	require.Equal(t, "check", hooks[0].Exec.Tool)
	require.Equal(t, "strict", hooks[0].Exec.Input["level"])
}

func TestMatchCodexMatcher(t *testing.T) {
	require.True(t, matchCodexMatcher("", "anything"))
	require.True(t, matchCodexMatcher("*", "anything"))
	require.True(t, matchCodexMatcher("Bash|Write", "Write"))
	require.False(t, matchCodexMatcher("Bash|Write", "Read"))
	require.True(t, matchCodexMatcher("^Bash.*$", "Bash(1)"))
	require.False(t, matchCodexMatcher("^Bash$", "Bash(1)"))
}

func TestParseCodexOutput(t *testing.T) {
	out := ParseCodexOutput(EventPreToolUse, `{
  "hookSpecificOutput": {
    "hookEventName": "PreToolUse",
    "permissionDecision": "allow",
    "updatedInput": {"command": "safe"}
  }
}`, "", 0)
	require.Empty(t, out.InvalidReason)
	require.Equal(t, "allow", out.PermissionDecisionValue)
	decision := out.ToDecision()
	require.Equal(t, DecisionModify, decision.Action)
	require.JSONEq(t, `{"command":"safe"}`, string(decision.PatchedPayload))

	blocked := ParseCodexOutput(EventUserPromptSubmit, `{"decision":"block","reason":"policy"}`, "", 0)
	require.Empty(t, blocked.InvalidReason)
	require.Equal(t, DecisionBlock, blocked.ToDecision().Action)
	require.Equal(t, "policy", blocked.ToDecision().Message)

	stop := ParseCodexOutput(EventStop, `{"continue":false,"stopReason":"done"}`, "", 0)
	require.Empty(t, stop.InvalidReason)
	require.Equal(t, DecisionStop, stop.ToDecision().Action)

	exitTwo := ParseCodexOutput(EventUserPromptSubmit, "", "blocked by policy\n", 2)
	require.Empty(t, exitTwo.InvalidReason)
	require.Equal(t, DecisionBlock, exitTwo.ToDecision().Action)

	plain := ParseCodexOutput(EventSessionStart, "hello context\n", "", 0)
	require.Equal(t, "hello context", plain.AdditionalContext)

	invalid := ParseCodexOutput(EventPreToolUse, `{"decision":"block"}`, "", 0)
	require.NotEmpty(t, invalid.InvalidReason)
	require.Equal(t, DecisionContinue, invalid.ToDecision().Action)
}

func TestParseCodexPermissionRequestDecision(t *testing.T) {
	allow := ParseCodexOutput(EventPermissionRequest, `{
  "hookSpecificOutput": {
    "hookEventName": "PermissionRequest",
    "decision": {"behavior": "allow"}
  }
}`, "", 0)
	require.Empty(t, allow.InvalidReason)
	require.Equal(t, DecisionContinue, allow.ToPermissionDecision().Action)

	deny := ParseCodexOutput(EventPermissionRequest, `{
  "hookSpecificOutput": {
    "hookEventName": "PermissionRequest",
    "decision": {"behavior": "deny", "message": "no writes"}
  }
}`, "", 0)
	require.Empty(t, deny.InvalidReason)
	decision := deny.ToPermissionDecision()
	require.Equal(t, DecisionBlock, decision.Action)
	require.Equal(t, "no writes", decision.Message)

	reserved := ParseCodexOutput(EventPermissionRequest, `{
  "hookSpecificOutput": {
    "hookEventName": "PermissionRequest",
    "decision": {"behavior": "allow", "updatedInput": {"x": 1}}
  }
}`, "", 0)
	require.NotEmpty(t, reserved.InvalidReason)
}

func TestParseCodexPreToolUseAllowRequiresUpdatedInput(t *testing.T) {
	allowWithoutInput := ParseCodexOutput(EventPreToolUse, `{
  "hookSpecificOutput": {
    "hookEventName": "PreToolUse",
    "permissionDecision": "allow"
  }
}`, "", 0)
	require.NotEmpty(t, allowWithoutInput.InvalidReason)
	require.Equal(t, DecisionContinue, allowWithoutInput.ToDecision().Action)
}

func TestDispatchAsyncIgnoresControlEffects(t *testing.T) {
	manager := NewManager([]HookConfig{{
		ID:    "async-block",
		Event: EventPreToolUse,
		Async: true,
		Exec:  ExecConfig{Type: "static"},
	}})
	manager.executors["static"] = &staticExecutor{decision: Decision{Action: DecisionBlock, Message: "must not block"}}

	decision, err := manager.Dispatch(context.Background(), EventPreToolUse, map[string]interface{}{"tool_name": "Write"})
	require.NoError(t, err)
	require.Equal(t, DecisionContinue, decision.Action)
}

func TestResolveHookStatesDisablesHandler(t *testing.T) {
	disabled := false
	hooks := []HookConfig{{ID: "a", Event: EventStop}, {ID: "b", Event: EventStop}}
	out := ResolveHookStates(hooks, map[string]HookState{"a": {Enabled: &disabled}})
	require.Len(t, out, 1)
	require.Equal(t, "b", out[0].ID)
}

func TestResolveHookStatesRejectsTrustedHashMismatch(t *testing.T) {
	hook := HookConfig{ID: "a", Event: EventStop, Exec: ExecConfig{Type: "shell", Command: "echo ok"}}
	out, warnings := resolveHookStates([]HookConfig{hook}, map[string]HookState{
		"a": {TrustedHash: "deadbeef"},
	})
	require.Empty(t, out)
	require.NotEmpty(t, warnings)
}

func TestResolveHookStatesAcceptsMatchingTrustedHash(t *testing.T) {
	hook := HookConfig{ID: "a", Event: EventStop, Exec: ExecConfig{Type: "shell", Command: "echo ok"}}
	out, warnings := resolveHookStates([]HookConfig{hook}, map[string]HookState{
		"a": {TrustedHash: handlerTrustHash(hook)},
	})
	require.Len(t, out, 1)
	require.Empty(t, warnings)
}

func TestDispatchFailClosedOnHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	manager := NewManager([]HookConfig{{
		ID:      "fail-closed",
		Event:   EventPreToolUse,
		OnError: "fail_closed",
		Exec:    ExecConfig{Type: "http", URL: server.URL},
	}})
	decision, err := manager.Dispatch(context.Background(), EventPreToolUse, map[string]interface{}{"tool_name": "Write"})
	require.NoError(t, err)
	require.Equal(t, DecisionBlock, decision.Action)
}

func TestDispatchFailOpenOnHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	manager := NewManager([]HookConfig{{
		ID:    "fail-open",
		Event: EventPreToolUse,
		Exec:  ExecConfig{Type: "http", URL: server.URL},
	}})
	decision, err := manager.Dispatch(context.Background(), EventPreToolUse, map[string]interface{}{"tool_name": "Write"})
	require.NoError(t, err)
	require.Equal(t, DecisionContinue, decision.Action)
}

func TestManagerSpillsOversizedAdditionalContext(t *testing.T) {
	manager := NewManager([]HookConfig{{
		ID:                     "ctx",
		Event:                  EventSessionStart,
		AdditionalContextLimit: 1,
		Exec:                   ExecConfig{Type: "static"},
	}})
	manager.executors["static"] = &staticExecutor{decision: Decision{
		Action:       DecisionEnrich,
		ExtraContext: map[string]string{"additional_context": strings.Repeat("x", 100)},
	}}
	var gotLimit int
	manager.SetContextSpiller(func(ctx context.Context, text string, limit int) (string, error) {
		gotLimit = limit
		return "spilled", nil
	})

	decision, err := manager.Dispatch(context.Background(), EventSessionStart, map[string]interface{}{})
	require.NoError(t, err)
	require.Equal(t, 1, gotLimit)
	require.Equal(t, "spilled", decision.ExtraContext["additional_context"])
}

func TestDiscoverDeclarativeHooksManagedOnly(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "managed.json")
	require.NoError(t, os.WriteFile(managed, []byte(`{
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "managed.sh"}]}]}
}`), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "project", ".aicli"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "project", ".aicli", "hooks.json"), []byte(`{
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "project.sh"}]}]}
}`), 0o600))

	discovered := DiscoverDeclarativeHooks(DiscoverOptions{
		ProjectRoot:      filepath.Join(root, "project"),
		ManagedFiles:     []string{managed},
		AllowManagedOnly: true,
	})
	require.Len(t, discovered.Hooks, 1)
	require.Equal(t, "managed.sh", discovered.Hooks[0].Exec.Command)
}

func TestMergeForHostOrdersBaseThenDeclarativeThenLegacy(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".aicli"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".aicli", "hooks.json"), []byte(`{
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "project.sh"}]}]}
}`), 0o600))

	merged := MergeForHost(HostSources{
		Base:              []HookConfig{{ID: "base", Event: EventStop}},
		ProjectRoot:       root,
		LegacyPluginHooks: []HookConfig{{ID: "legacy", Event: EventStop}},
	})
	require.Len(t, merged, 3)
	require.Equal(t, "base", merged[0].ID)
	require.Equal(t, "project.sh", merged[1].Exec.Command)
	require.Equal(t, "legacy", merged[2].ID)
}

func TestMergeForHostManagedOnlyDropsEveryOtherSource(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "managed.json")
	require.NoError(t, os.WriteFile(managed, []byte(`{
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "managed.sh"}]}]}
}`), 0o600))

	merged := MergeForHost(HostSources{
		Base:              []HookConfig{{ID: "base", Event: EventStop}},
		ProjectRoot:       root,
		LegacyPluginHooks: []HookConfig{{ID: "legacy", Event: EventStop}},
		ManagedFiles:      []string{managed},
		AllowManagedOnly:  true,
	})
	require.Len(t, merged, 1)
	require.Equal(t, "managed.sh", merged[0].Exec.Command)
}
