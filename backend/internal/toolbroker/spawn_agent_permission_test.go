package toolbroker

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func hasSpawnAgentRouteWarning(warnings []string, want string) bool {
	for _, warning := range warnings {
		if strings.TrimSpace(warning) == want {
			return true
		}
	}
	return false
}

func TestResolveSpawnAgentPermissionPolicy(t *testing.T) {
	t.Setenv(SpawnAgentEnvAllowPermissionEscalation, "")
	tests := []struct {
		name            string
		parent          string
		args            SpawnAgentArgs
		wantMode        string
		wantRequest     string
		wantWarning     string
		wantWarnings    []string
		allowEscalation bool
	}{
		{
			name:        "yolo parent is inherited when the child omits the mode",
			parent:      "bypass_permissions",
			args:        SpawnAgentArgs{Message: "inspect"},
			wantMode:    "bypass_permissions",
			wantWarning: SpawnAgentRouteWarningPermissionInherited,
		},
		{
			name:     "unknown parent mode never rewrites the request",
			parent:   "unsafe",
			args:     SpawnAgentArgs{PermissionMode: "accept_edits"},
			wantMode: "accept_edits",
		},
		{
			name:        "yolo parent pins an approval-asking child",
			parent:      "bypass_permissions",
			args:        SpawnAgentArgs{PermissionMode: "accept_edits", RequestedPermissionMode: "accept_edits"},
			wantMode:    "bypass_permissions",
			wantRequest: "accept_edits",
			wantWarning: SpawnAgentRouteWarningPermissionPinned,
		},
		{
			name:        "yolo parent pins a default-mode child",
			parent:      "bypass_permissions",
			args:        SpawnAgentArgs{PermissionMode: "default"},
			wantMode:    "bypass_permissions",
			wantRequest: "default",
			wantWarning: SpawnAgentRouteWarningPermissionPinned,
		},
		{
			name:        "pinning an agentdef default does not invent an explicit request",
			parent:      "bypass_permissions",
			args:        SpawnAgentArgs{PermissionMode: "default", RequestedRouteCaptured: true},
			wantMode:    "bypass_permissions",
			wantWarning: SpawnAgentRouteWarningPermissionPinned,
		},
		{
			name:     "yolo parent preserves an uncaptured explicit plan child",
			parent:   "bypass_permissions",
			args:     SpawnAgentArgs{PermissionMode: "plan", ReadOnly: true},
			wantMode: "plan",
		},
		{
			name:        "yolo parent inherits raw read-only request with omitted mode",
			parent:      "bypass_permissions",
			args:        SpawnAgentArgs{ReadOnly: true},
			wantMode:    "bypass_permissions",
			wantWarning: SpawnAgentRouteWarningPermissionInherited,
		},
		{
			name:        "yolo parent replaces captured implicit read-only plan",
			parent:      "bypass_permissions",
			args:        SpawnAgentArgs{ReadOnly: true, PermissionMode: "plan", RequestedRouteCaptured: true},
			wantMode:    "bypass_permissions",
			wantWarning: SpawnAgentRouteWarningPermissionInherited,
		},
		{
			name:        "yolo parent replaces explore read-only default",
			parent:      "bypass_permissions",
			args:        SpawnAgentArgs{AgentType: "explore", ReadOnly: true, PermissionMode: "plan", RequestedRouteCaptured: true},
			wantMode:    "bypass_permissions",
			wantWarning: SpawnAgentRouteWarningPermissionInherited,
		},
		{
			name:        "captured explicit plan remains plan",
			parent:      "bypass_permissions",
			args:        SpawnAgentArgs{ReadOnly: true, PermissionMode: "plan", RequestedPermissionMode: " PLAN ", RequestedRouteCaptured: true},
			wantMode:    "plan",
			wantRequest: " PLAN ",
		},
		{
			name:     "planning agent is not an implicit read-only fallback",
			parent:   "bypass_permissions",
			args:     SpawnAgentArgs{AgentType: " PLAN ", ReadOnly: true, PermissionMode: "plan", RequestedRouteCaptured: true},
			wantMode: "plan",
		},
		{
			name:     "planning agent retains plan without hard read-only",
			parent:   "bypass_permissions",
			args:     SpawnAgentArgs{AgentType: "plan", PermissionMode: "plan", RequestedRouteCaptured: true},
			wantMode: "plan",
		},
		{
			name:     "a plan default without read-only is not replaced",
			parent:   "bypass_permissions",
			args:     SpawnAgentArgs{PermissionMode: "plan", RequestedRouteCaptured: true},
			wantMode: "plan",
		},
		{
			name:     "missing parent keeps conservative read-only fallback",
			args:     SpawnAgentArgs{ReadOnly: true, RequestedRouteCaptured: true},
			wantMode: "plan",
		},
		{
			name:     "default parent keeps conservative read-only fallback",
			parent:   "default",
			args:     SpawnAgentArgs{ReadOnly: true, RequestedRouteCaptured: true},
			wantMode: "plan",
		},
		{
			name:     "plan parent keeps read-only child in plan",
			parent:   "plan",
			args:     SpawnAgentArgs{ReadOnly: true, PermissionMode: "plan", RequestedRouteCaptured: true},
			wantMode: "plan",
		},
		{
			name:     "dont_ask parent is not widened by an implicit read-only plan",
			parent:   "dont_ask",
			args:     SpawnAgentArgs{ReadOnly: true, PermissionMode: "plan", RequestedRouteCaptured: true},
			wantMode: "plan",
		},
		{
			name:            "plan parent still pins a previous implicit bypass with escalation enabled",
			parent:          "plan",
			args:            SpawnAgentArgs{ReadOnly: true, PermissionMode: "bypass_permissions", RequestedRouteCaptured: true},
			wantMode:        "plan",
			wantWarning:     SpawnAgentRouteWarningPermissionPinned,
			allowEscalation: true,
		},
		{
			name:            "dont_ask parent still pins a previous implicit bypass with escalation enabled",
			parent:          "dont_ask",
			args:            SpawnAgentArgs{ReadOnly: true, PermissionMode: "bypass_permissions", RequestedRouteCaptured: true},
			wantMode:        "dont_ask",
			wantWarning:     SpawnAgentRouteWarningPermissionPinned,
			allowEscalation: true,
		},
		{
			name:            "dont_ask parent rejects explicit read-only bypass even with escalation enabled",
			parent:          "dont_ask",
			args:            SpawnAgentArgs{ReadOnly: true, PermissionMode: "bypass_permissions", RequestedPermissionMode: "bypass_permissions", RequestedRouteCaptured: true},
			wantMode:        "dont_ask",
			wantRequest:     "bypass_permissions",
			wantWarning:     SpawnAgentRouteWarningPermissionPinned,
			allowEscalation: true,
		},
		{
			name:        "plan parent pins a writing child",
			parent:      "plan",
			args:        SpawnAgentArgs{PermissionMode: "bypass_permissions"},
			wantMode:    "plan",
			wantRequest: "bypass_permissions",
			wantWarning: SpawnAgentRouteWarningPermissionPinned,
		},
		{
			name:        "plan parent pins an approval-asking child",
			parent:      "plan",
			args:        SpawnAgentArgs{PermissionMode: "accept_edits", RequestedPermissionMode: "accept_edits"},
			wantMode:    "plan",
			wantRequest: "accept_edits",
			wantWarning: SpawnAgentRouteWarningPermissionPinned,
		},
		{
			name:     "default parent keeps a plan narrowing",
			parent:   "default",
			args:     SpawnAgentArgs{PermissionMode: "plan"},
			wantMode: "plan",
		},
		{
			name:         "escalation is blocked and pinned to the parent by default",
			parent:       "accept_edits",
			args:         SpawnAgentArgs{PermissionMode: "bypass_permissions"},
			wantMode:     "accept_edits",
			wantRequest:  "bypass_permissions",
			wantWarning:  SpawnAgentRouteWarningPermissionEscalated,
			wantWarnings: []string{SpawnAgentRouteWarningPermissionPinned},
		},
		{
			name:            "explicit opt-in restores the honored escalation",
			parent:          "accept_edits",
			args:            SpawnAgentArgs{PermissionMode: "bypass_permissions"},
			wantMode:        "bypass_permissions",
			wantRequest:     "bypass_permissions",
			wantWarning:     SpawnAgentRouteWarningPermissionEscalated,
			allowEscalation: true,
		},
		{
			name:        "warnings are deduplicated",
			parent:      "bypass_permissions",
			args:        SpawnAgentArgs{PermissionMode: "default", RouteWarnings: []string{"", SpawnAgentRouteWarningPermissionPinned}},
			wantMode:    "bypass_permissions",
			wantRequest: "default",
			wantWarning: SpawnAgentRouteWarningPermissionPinned,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.allowEscalation {
				t.Setenv(SpawnAgentEnvAllowPermissionEscalation, "1")
			}
			got := ResolveSpawnAgentPermissionPolicy(tt.args, tt.parent)
			if got.PermissionMode != tt.wantMode {
				t.Fatalf("permission mode=%q want %q", got.PermissionMode, tt.wantMode)
			}
			if got.RequestedPermissionMode != tt.wantRequest {
				t.Fatalf("requested permission mode=%q want %q", got.RequestedPermissionMode, tt.wantRequest)
			}
			if got.ReadOnly != tt.args.ReadOnly {
				t.Fatalf("approval mode resolution changed the static read-only boundary: %#v", got)
			}
			if again := ResolveSpawnAgentPermissionPolicy(got, tt.parent); !reflect.DeepEqual(again, got) {
				t.Fatalf("repeated resolution must be stable:\nfirst: %#v\nagain: %#v", got, again)
			}
			// The three route warnings are a closed vocabulary: a case asserts
			// the exact set, and no warning may be recorded twice.
			for _, warning := range []string{
				SpawnAgentRouteWarningPermissionInherited,
				SpawnAgentRouteWarningPermissionPinned,
				SpawnAgentRouteWarningPermissionEscalated,
			} {
				seen := 0
				for _, recorded := range got.RouteWarnings {
					if strings.TrimSpace(recorded) == warning {
						seen++
					}
				}
				want := warning == tt.wantWarning
				for _, extra := range tt.wantWarnings {
					if extra == warning {
						want = true
					}
				}
				if want && seen == 0 {
					t.Fatalf("missing %s warning: %#v", warning, got.RouteWarnings)
				}
				if !want && seen > 0 {
					t.Fatalf("unexpected %s warning: %#v", warning, got.RouteWarnings)
				}
				if seen > 1 {
					t.Fatalf("%s must be recorded at most once: %#v", warning, got.RouteWarnings)
				}
			}
		})
	}
}

func TestParentSessionPermissionModePrefersEffectiveMode(t *testing.T) {
	session := testAgentContext{
		AgentSessionContextRequestedPermissionMode: "default",
		AgentSessionContextPermissionMode:          "bypass_permissions",
	}
	if got := ParentSessionPermissionMode(session); got != "bypass_permissions" {
		t.Fatalf("expected effective session mode, got %q", got)
	}

	requestedOnly := testAgentContext{AgentSessionContextRequestedPermissionMode: "accept_edits"}
	if got := ParentSessionPermissionMode(requestedOnly); got != "accept_edits" {
		t.Fatalf("expected requested session mode fallback, got %q", got)
	}

	unknown := testAgentContext{AgentSessionContextPermissionMode: "unsafe"}
	if got := ParentSessionPermissionMode(unknown); got != "" {
		t.Fatalf("expected unsupported mode to be ignored, got %q", got)
	}
}

// A session started with --yolo (bypass_permissions) must not silently regain
// approval prompts through a model-authored child permission_mode: the child
// stays prompt-free and the original request stays visible for audit.
func TestBroker_Execute_SpawnAgentPinsPermissionModeInYoloSession(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}
	ctx := team.WithRunMeta(context.Background(), &team.RunMeta{PermissionMode: "bypass_permissions"})

	_, meta, err := broker.Execute(ctx, "parent-session", ToolSubagentSpawn, map[string]interface{}{
		"message":         "implement the fix",
		"permission_mode": "accept_edits",
	})
	if err != nil {
		t.Fatalf("subagent_spawn failed: %v", err)
	}
	if controller.lastSpawn.PermissionMode != "bypass_permissions" ||
		controller.lastSpawn.EffectivePermissionMode != "bypass_permissions" ||
		controller.lastSpawn.RequestedPermissionMode != "accept_edits" {
		t.Fatalf("unexpected pinned spawn args: %#v", controller.lastSpawn)
	}
	if meta["permission_mode"] != "bypass_permissions" || meta["effective_permission_mode"] != "bypass_permissions" {
		t.Fatalf("unexpected pinned metadata: %#v", meta)
	}
	warnings, ok := meta["route_warnings"].([]string)
	if !ok || !hasSpawnAgentRouteWarning(warnings, SpawnAgentRouteWarningPermissionPinned) {
		t.Fatalf("expected pinned route warning, got %#v", meta["route_warnings"])
	}
}

func TestBroker_Execute_SpawnAgentPinsPlanSessionChild(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}
	ctx := team.WithRunMeta(context.Background(), &team.RunMeta{PermissionMode: "plan"})

	_, meta, err := broker.Execute(ctx, "parent-session", ToolSubagentSpawn, map[string]interface{}{
		"message":         "write the code",
		"permission_mode": "default",
	})
	if err != nil {
		t.Fatalf("subagent_spawn failed: %v", err)
	}
	if controller.lastSpawn.PermissionMode != "plan" || controller.lastSpawn.RequestedPermissionMode != "default" {
		t.Fatalf("expected plan-plan child, got %#v", controller.lastSpawn)
	}
	if meta["permission_mode"] != "plan" {
		t.Fatalf("unexpected plan metadata: %#v", meta)
	}
}

func TestBroker_Execute_SpawnAgentMarksInheritedPermissionMode(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}
	ctx := team.WithRunMeta(context.Background(), &team.RunMeta{PermissionMode: "bypass_permissions"})

	_, meta, err := broker.Execute(ctx, "parent-session", ToolSubagentSpawn, map[string]interface{}{"message": "inspect"})
	if err != nil {
		t.Fatalf("subagent_spawn failed: %v", err)
	}
	if controller.lastSpawn.PermissionMode != "bypass_permissions" {
		t.Fatalf("expected inherited permission mode, got %#v", controller.lastSpawn)
	}
	warnings, ok := meta["route_warnings"].([]string)
	if !ok || !hasSpawnAgentRouteWarning(warnings, SpawnAgentRouteWarningPermissionInherited) {
		t.Fatalf("expected inherited route warning, got %#v", meta["route_warnings"])
	}
}

type permissionResolvingAgentController struct {
	fakeAgentSessionController
	parentMode       string
	beforeResolution SpawnAgentArgs
}

func (c *permissionResolvingAgentController) Spawn(ctx context.Context, parentSessionID string, args SpawnAgentArgs) (*AgentStatusResult, error) {
	c.beforeResolution = args
	args = ResolveSpawnAgentPermissionPolicy(args, c.parentMode)
	args.EffectivePermissionMode = args.PermissionMode
	result, err := c.fakeAgentSessionController.Spawn(ctx, parentSessionID, args)
	if result != nil {
		result.RequestedPermissionMode = args.RequestedPermissionMode
		result.EffectivePermissionMode = args.EffectivePermissionMode
	}
	return result, err
}

// Exercise both broker run-meta inheritance and the host's second resolution
// using authoritative session state when run meta was absent (resume/follow-up).
func TestBroker_Execute_ReadOnlyPermissionInheritance(t *testing.T) {
	t.Setenv(SpawnAgentEnvAllowPermissionEscalation, "")
	tests := []struct {
		name          string
		args          map[string]interface{}
		wantMode      string
		wantReadOnly  bool
		wantRequested string
		wantInherited bool
	}{
		{
			name:          "read-only with omitted mode",
			args:          map[string]interface{}{"read_only": true},
			wantMode:      "bypass_permissions",
			wantReadOnly:  true,
			wantInherited: true,
		},
		{
			name:          "blank mode is omitted",
			args:          map[string]interface{}{"read_only": true, "permission_mode": "  "},
			wantMode:      "bypass_permissions",
			wantReadOnly:  true,
			wantInherited: true,
		},
		{
			name:          "explore definition supplies read-only",
			args:          map[string]interface{}{"agent_type": "explore"},
			wantMode:      "bypass_permissions",
			wantReadOnly:  true,
			wantInherited: true,
		},
		{
			name:          "explicit plan is not an implicit fallback",
			args:          map[string]interface{}{"read_only": true, "permission_mode": " PLAN "},
			wantMode:      "plan",
			wantReadOnly:  true,
			wantRequested: "PLAN",
		},
		{
			name:          "explore with explicit plan",
			args:          map[string]interface{}{"agent_type": "explore", "permission_mode": "plan"},
			wantMode:      "plan",
			wantReadOnly:  true,
			wantRequested: "plan",
		},
		{
			name:     "plan definition keeps planning mode",
			args:     map[string]interface{}{"agent_type": "plan"},
			wantMode: "plan",
		},
		{
			name:         "plan definition with hard read-only",
			args:         map[string]interface{}{"agent_type": "plan", "read_only": true},
			wantMode:     "plan",
			wantReadOnly: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, withRunMeta := range []bool{true, false} {
				name := "controller_session_mode_only"
				if withRunMeta {
					name = "broker_and_controller_modes"
				}
				t.Run(name, func(t *testing.T) {
					controller := &permissionResolvingAgentController{parentMode: "bypass_permissions"}
					broker := &Broker{AgentSessions: controller}
					ctx := context.Background()
					if withRunMeta {
						ctx = team.WithRunMeta(ctx, &team.RunMeta{PermissionMode: controller.parentMode})
					}
					args := map[string]interface{}{"message": "inspect permission inheritance"}
					for key, value := range tt.args {
						args[key] = value
					}
					_, meta, err := broker.Execute(ctx, "parent-session", ToolSubagentSpawn, args)
					require.NoError(t, err)
					require.True(t, controller.beforeResolution.RequestedRouteCaptured)
					require.Equal(t, tt.wantRequested, controller.beforeResolution.RequestedPermissionMode)
					require.Equal(t, tt.wantReadOnly, controller.beforeResolution.ReadOnly)
					wantBrokerMode := tt.wantMode
					if !withRunMeta {
						wantBrokerMode = "plan"
					}
					require.Equal(t, wantBrokerMode, controller.beforeResolution.PermissionMode)
					require.Equal(t, tt.wantMode, controller.lastSpawn.PermissionMode)
					require.Equal(t, tt.wantMode, controller.lastSpawn.EffectivePermissionMode)
					require.Equal(t, tt.wantReadOnly, controller.lastSpawn.ReadOnly)
					require.Equal(t, tt.wantRequested, controller.lastSpawn.RequestedPermissionMode)
					require.Equal(t, tt.wantMode, meta["permission_mode"])
					require.Equal(t, tt.wantMode, meta["effective_permission_mode"])
					require.Equal(t, tt.wantInherited, hasSpawnAgentRouteWarning(controller.lastSpawn.RouteWarnings, SpawnAgentRouteWarningPermissionInherited))
					if tt.wantReadOnly {
						require.Equal(t, true, meta["read_only"])
					} else {
						require.NotContains(t, meta, "read_only", "planning mode must not become a hard read-only boundary")
					}
					if tt.wantRequested == "" {
						require.NotContains(t, meta, "requested_permission_mode", "defaults must not be reported as user requests")
					} else {
						require.Equal(t, tt.wantRequested, meta["requested_permission_mode"])
					}
					require.Equal(t, controller.lastSpawn, ResolveSpawnAgentPermissionPolicy(controller.lastSpawn, controller.parentMode))
				})
			}
		})
	}
}

func TestBroker_Execute_InheritedBypassKeepsStaticReadOnlyBoundary(t *testing.T) {
	for _, args := range []map[string]interface{}{
		{"message": "inspect", "read_only": true},
		{"message": "inspect", "agent_type": "explore"},
	} {
		controller := &fakeAgentSessionController{}
		broker := &Broker{AgentSessions: controller}
		ctx := team.WithRunMeta(context.Background(), &team.RunMeta{PermissionMode: "bypass_permissions"})
		_, _, err := broker.Execute(ctx, "parent-session", ToolSubagentSpawn, args)
		require.NoError(t, err)
		require.Equal(t, "bypass_permissions", controller.lastSpawn.PermissionMode)
		require.True(t, controller.lastSpawn.ReadOnly)
		childContext := testAgentContext{}
		ApplySpawnAgentRouteContext(childContext, controller.lastSpawn)
		require.Equal(t, true, childContext[AgentSessionContextReadOnly])
		require.Equal(t, "bypass_permissions", SpawnAgentRunMetaFromContext(childContext).PermissionMode)

		// This is the static policy derived from the emitted read_only flag,
		// independent of the child's approval mode or any approval grant.
		policy := runtimepolicy.NewToolExecutionPolicy(nil, false).DeriveChild(nil, controller.lastSpawn.ReadOnly)
		for _, name := range []string{"write", "edit", "apply_patch", "append_write", "multiedit", "download", "job_start"} {
			require.Error(t, policy.AllowTool(name), "%s must remain denied", name)
		}
		require.NoError(t, policy.AllowTool("view"))
		shell := skill.ToolInfo{Name: "shell", MCPTrustLevel: "local", ExecutionMode: "local_mcp"}
		require.NoError(t, policy.AllowToolCall(shell, map[string]interface{}{"command": "git status --short"}))
		require.Error(t, policy.AllowToolCall(shell, map[string]interface{}{"command": "git reset --hard"}))
		require.Error(t, policy.AllowToolCall(shell, map[string]interface{}{"command": "echo changed > file.txt"}))
	}
}

func TestBroker_Execute_ReadOnlyCannotWidenPinnedParent(t *testing.T) {
	t.Setenv(SpawnAgentEnvAllowPermissionEscalation, "1")
	for _, parentMode := range []string{"plan", "dont_ask"} {
		t.Run(parentMode, func(t *testing.T) {
			controller := &fakeAgentSessionController{}
			broker := &Broker{AgentSessions: controller}
			ctx := team.WithRunMeta(context.Background(), &team.RunMeta{PermissionMode: parentMode})
			_, _, err := broker.Execute(ctx, "parent-session", ToolSubagentSpawn, map[string]interface{}{
				"message":         "inspect",
				"read_only":       true,
				"permission_mode": "bypass_permissions",
			})
			require.NoError(t, err)
			require.Equal(t, parentMode, controller.lastSpawn.PermissionMode)
			require.Equal(t, "bypass_permissions", controller.lastSpawn.RequestedPermissionMode)
			require.True(t, controller.lastSpawn.ReadOnly)
			require.Contains(t, controller.lastSpawn.RouteWarnings, SpawnAgentRouteWarningPermissionPinned)
		})
	}
}

// H13: a child may not widen the parent session mode. The default blocks the
// escalation, pins the child to the parent mode and hands the caller an
// actionable next_action instead of a silently narrowed child.
func TestBroker_Execute_SpawnAgentBlocksPermissionEscalationWithNextAction(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}
	ctx := team.WithRunMeta(context.Background(), &team.RunMeta{PermissionMode: "accept_edits"})

	_, meta, err := broker.Execute(ctx, "parent-session", ToolSubagentSpawn, map[string]interface{}{
		"message":         "run with more authority",
		"permission_mode": "bypass_permissions",
	})
	if err != nil {
		t.Fatalf("subagent_spawn failed: %v", err)
	}
	if controller.lastSpawn.PermissionMode != "accept_edits" ||
		controller.lastSpawn.RequestedPermissionMode != "bypass_permissions" {
		t.Fatalf("escalation must be pinned to the parent mode: %#v", controller.lastSpawn)
	}
	if meta["permission_mode"] != "accept_edits" {
		t.Fatalf("unexpected pinned metadata: %#v", meta)
	}
	nextAction, ok := meta["next_action"].(string)
	if !ok || !strings.Contains(nextAction, "permission_mode=accept_edits") ||
		!strings.Contains(nextAction, SpawnAgentEnvAllowPermissionEscalation) {
		t.Fatalf("blocked escalation must return actionable next_action, got %#v", meta["next_action"])
	}
}

func TestSpawnAgentPermissionEscalationNextAction(t *testing.T) {
	// An honored escalation (opt-in) has no pinned warning, so no guidance.
	if got := SpawnAgentPermissionEscalationNextAction(
		[]string{SpawnAgentRouteWarningPermissionEscalated}, "bypass_permissions"); got != "" {
		t.Fatalf("honored escalation must not report blocked guidance, got %q", got)
	}
	// A yolo parent pinning an approval-asking child is a narrowing, not an
	// escalation: it must not be reported as a blocked escalation either.
	if got := SpawnAgentPermissionEscalationNextAction(
		[]string{SpawnAgentRouteWarningPermissionPinned}, "bypass_permissions"); got != "" {
		t.Fatalf("narrowing pin must not report blocked guidance, got %q", got)
	}
	got := SpawnAgentPermissionEscalationNextAction(
		[]string{SpawnAgentRouteWarningPermissionEscalated, SpawnAgentRouteWarningPermissionPinned}, "accept_edits")
	for _, want := range []string{"blocked", "permission_mode=accept_edits", SpawnAgentEnvAllowPermissionEscalation} {
		if !strings.Contains(got, want) {
			t.Fatalf("guidance %q must contain %q", got, want)
		}
	}
}
