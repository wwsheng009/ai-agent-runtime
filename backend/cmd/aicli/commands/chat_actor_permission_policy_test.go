package commands

import (
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func hasLocalSpawnRouteWarning(warnings []string, want string) bool {
	for _, warning := range warnings {
		if strings.TrimSpace(warning) == want {
			return true
		}
	}
	return false
}

// A --yolo parent session must not spawn approval-asking children even when
// the tool call carries an explicit permission_mode: the incident this policy
// fixes was a session pinned to bypass_permissions whose child started in
// accept_edits and stopped for approvals.
func TestLocalActorRegistry_ResolveSpawnAgentRoutePinsPermissionModeToParentSession(t *testing.T) {
	parent := runtimechat.NewSession("user-1")
	parent.SetContext(toolbroker.AgentSessionContextPermissionMode, "bypass_permissions")

	registry := &localActorRegistry{}
	args, err := registry.resolveSpawnAgentRoute(parent, "child-1", toolbroker.SpawnAgentArgs{
		Message:                 "implement",
		PermissionMode:          "accept_edits",
		RequestedPermissionMode: "accept_edits",
		RequestedRouteCaptured:  true,
	})
	if err != nil {
		t.Fatalf("resolveSpawnAgentRoute failed: %v", err)
	}
	if args.PermissionMode != "bypass_permissions" || args.EffectivePermissionMode != "bypass_permissions" {
		t.Fatalf("expected child pinned to parent session mode, got %#v", args)
	}
	if args.RequestedPermissionMode != "accept_edits" {
		t.Fatalf("expected requested mode preserved for audit, got %q", args.RequestedPermissionMode)
	}
	if !hasLocalSpawnRouteWarning(args.RouteWarnings, toolbroker.SpawnAgentRouteWarningPermissionPinned) {
		t.Fatalf("expected pinned route warning, got %#v", args.RouteWarnings)
	}
}

func TestLocalActorRegistry_ResolveSpawnAgentRouteInheritsParentSessionPermissionMode(t *testing.T) {
	parent := runtimechat.NewSession("user-1")
	parent.SetContext(toolbroker.AgentSessionContextPermissionMode, "bypass_permissions")

	registry := &localActorRegistry{}
	args, err := registry.resolveSpawnAgentRoute(parent, "child-1", toolbroker.SpawnAgentArgs{
		Message:                "inspect",
		RequestedRouteCaptured: true,
	})
	if err != nil {
		t.Fatalf("resolveSpawnAgentRoute failed: %v", err)
	}
	if args.PermissionMode != "bypass_permissions" || args.EffectivePermissionMode != "bypass_permissions" {
		t.Fatalf("expected inherited parent session mode, got %#v", args)
	}
	if !hasLocalSpawnRouteWarning(args.RouteWarnings, toolbroker.SpawnAgentRouteWarningPermissionInherited) {
		t.Fatalf("expected inherited route warning, got %#v", args.RouteWarnings)
	}
}
