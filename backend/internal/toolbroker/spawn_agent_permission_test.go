package toolbroker

import (
	"context"
	"strings"
	"testing"

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
	tests := []struct {
		name        string
		parent      string
		args        SpawnAgentArgs
		wantMode    string
		wantRequest string
		wantWarning string
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
			name:     "yolo parent keeps a prompt-free plan child",
			parent:   "bypass_permissions",
			args:     SpawnAgentArgs{PermissionMode: "plan", ReadOnly: true},
			wantMode: "plan",
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
			name:        "escalation stays auditable",
			parent:      "default",
			args:        SpawnAgentArgs{PermissionMode: "bypass_permissions"},
			wantMode:    "bypass_permissions",
			wantRequest: "bypass_permissions",
			wantWarning: SpawnAgentRouteWarningPermissionEscalated,
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
			got := ResolveSpawnAgentPermissionPolicy(tt.args, tt.parent)
			if got.PermissionMode != tt.wantMode {
				t.Fatalf("permission mode=%q want %q", got.PermissionMode, tt.wantMode)
			}
			if got.RequestedPermissionMode != tt.wantRequest {
				t.Fatalf("requested permission mode=%q want %q", got.RequestedPermissionMode, tt.wantRequest)
			}
			if tt.wantWarning == "" {
				for _, warning := range []string{
					SpawnAgentRouteWarningPermissionInherited,
					SpawnAgentRouteWarningPermissionPinned,
					SpawnAgentRouteWarningPermissionEscalated,
				} {
					if hasSpawnAgentRouteWarning(got.RouteWarnings, warning) {
						t.Fatalf("unexpected %s warning: %#v", warning, got.RouteWarnings)
					}
				}
				return
			}
			if !hasSpawnAgentRouteWarning(got.RouteWarnings, tt.wantWarning) {
				t.Fatalf("missing %s warning: %#v", tt.wantWarning, got.RouteWarnings)
			}
			pinnedCount := 0
			for _, warning := range got.RouteWarnings {
				if strings.TrimSpace(warning) == tt.wantWarning {
					pinnedCount++
				}
			}
			if pinnedCount != 1 {
				t.Fatalf("warning must be recorded exactly once: %#v", got.RouteWarnings)
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

	_, meta, err := broker.Execute(ctx, "parent-session", ToolSpawnAgent, map[string]interface{}{
		"message":         "implement the fix",
		"permission_mode": "accept_edits",
	})
	if err != nil {
		t.Fatalf("spawn_agent failed: %v", err)
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

	_, meta, err := broker.Execute(ctx, "parent-session", ToolSpawnAgent, map[string]interface{}{
		"message":         "write the code",
		"permission_mode": "default",
	})
	if err != nil {
		t.Fatalf("spawn_agent failed: %v", err)
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

	_, meta, err := broker.Execute(ctx, "parent-session", ToolSpawnAgent, map[string]interface{}{"message": "inspect"})
	if err != nil {
		t.Fatalf("spawn_agent failed: %v", err)
	}
	if controller.lastSpawn.PermissionMode != "bypass_permissions" {
		t.Fatalf("expected inherited permission mode, got %#v", controller.lastSpawn)
	}
	warnings, ok := meta["route_warnings"].([]string)
	if !ok || !hasSpawnAgentRouteWarning(warnings, SpawnAgentRouteWarningPermissionInherited) {
		t.Fatalf("expected inherited route warning, got %#v", meta["route_warnings"])
	}
}
