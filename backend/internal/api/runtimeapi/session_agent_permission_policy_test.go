package runtimeapi

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func hasAPISpawnRouteWarning(warnings []string, want string) bool {
	for _, warning := range warnings {
		if strings.TrimSpace(warning) == want {
			return true
		}
	}
	return false
}

// The API host must resolve the child permission mode from the durable parent
// session, not only from the current run meta: follow-up/resumed runs may not
// carry run meta, and a --yolo session must stay prompt-free for its children.
func TestSessionAgentController_ResolveSpawnAgentRoutePinsPermissionModeToParentSession(t *testing.T) {
	parent := chat.NewSession("user-1")
	parent.SetContext(toolbroker.AgentSessionContextPermissionMode, "bypass_permissions")

	controller := &sessionAgentController{}
	args, err := controller.resolveSpawnAgentRoute(parent, "child-1", toolbroker.SpawnAgentArgs{
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
	if !hasAPISpawnRouteWarning(args.RouteWarnings, toolbroker.SpawnAgentRouteWarningPermissionPinned) {
		t.Fatalf("expected pinned route warning, got %#v", args.RouteWarnings)
	}
}

func TestSessionAgentController_ResolveSpawnAgentRouteInheritsParentSessionPermissionMode(t *testing.T) {
	parent := chat.NewSession("user-1")
	parent.SetContext(toolbroker.AgentSessionContextPermissionMode, "bypass_permissions")

	controller := &sessionAgentController{}
	args, err := controller.resolveSpawnAgentRoute(parent, "child-1", toolbroker.SpawnAgentArgs{
		Message:                "inspect",
		RequestedRouteCaptured: true,
	})
	if err != nil {
		t.Fatalf("resolveSpawnAgentRoute failed: %v", err)
	}
	if args.PermissionMode != "bypass_permissions" || args.EffectivePermissionMode != "bypass_permissions" {
		t.Fatalf("expected inherited parent session mode, got %#v", args)
	}
	if !hasAPISpawnRouteWarning(args.RouteWarnings, toolbroker.SpawnAgentRouteWarningPermissionInherited) {
		t.Fatalf("expected inherited route warning, got %#v", args.RouteWarnings)
	}
}
