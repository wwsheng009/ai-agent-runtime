package agent

import (
	"context"
	"testing"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// A control-plane mode switch during an in-flight turn must be visible to the
// very next permission evaluation: RunMeta is frozen at submit time, so the
// live source attached at submit time is the only channel that can carry it.
func TestPermissionModeFromContextPrefersLiveSource(t *testing.T) {
	live := string(runtimepolicy.ModeDefault)
	ctx := team.WithRunMeta(context.Background(), &team.RunMeta{
		PermissionMode: string(runtimepolicy.ModeDefault),
	})
	ctx = team.WithPermissionModeSource(ctx, func() string { return live })

	if got := permissionModeFromContext(ctx); got != runtimepolicy.ModeDefault {
		t.Fatalf("initial mode = %q, want %q", got, runtimepolicy.ModeDefault)
	}

	live = string(runtimepolicy.ModeBypassPermissions)
	if got := permissionModeFromContext(ctx); got != runtimepolicy.ModeBypassPermissions {
		t.Fatalf("mode after live switch = %q, want %q", got, runtimepolicy.ModeBypassPermissions)
	}
}

// An empty live value (session mode unset) must not mask the RunMeta snapshot,
// and a context without either source still resolves to the empty mode so the
// caller falls back to the engine default.
func TestPermissionModeFromContextFallsBackToRunMetaSnapshot(t *testing.T) {
	ctx := team.WithRunMeta(context.Background(), &team.RunMeta{
		PermissionMode: string(runtimepolicy.ModeAcceptEdits),
	})
	ctx = team.WithPermissionModeSource(ctx, func() string { return "  " })
	if got := permissionModeFromContext(ctx); got != runtimepolicy.ModeAcceptEdits {
		t.Fatalf("mode = %q, want %q", got, runtimepolicy.ModeAcceptEdits)
	}

	if got := permissionModeFromContext(context.Background()); got != "" {
		t.Fatalf("mode without meta = %q, want empty", got)
	}
}

// A silent live source (the control plane did not switch the mode) must let an
// in-turn RunMeta mutation win: the session actor republishes plan transitions
// by mutating the RunMeta pointer attached to the run context, and the loop
// reads that pointer for every tool evaluation. Regression for the same-turn
// freeze where exit_plan_mode returned status=exited while write/apply_patch
// still evaluated as plan.
func TestPermissionModeFromContextPrefersUpdatedRunMetaOverSilentSource(t *testing.T) {
	ctx := team.WithRunMeta(context.Background(), &team.RunMeta{
		PermissionMode: string(runtimepolicy.ModePlan),
	})
	ctx = team.WithPermissionModeSource(ctx, func() string { return "" })

	if got := permissionModeFromContext(ctx); got != runtimepolicy.ModePlan {
		t.Fatalf("initial mode = %q, want %q", got, runtimepolicy.ModePlan)
	}

	meta, ok := team.GetRunMeta(ctx)
	if !ok || meta == nil {
		t.Fatal("run meta missing from context")
	}
	// Same pointer the actor mutates in syncLivePermissionMode on exit_plan_mode.
	meta.PermissionMode = string(runtimepolicy.ModeAcceptEdits)

	if got := permissionModeFromContext(ctx); got != runtimepolicy.ModeAcceptEdits {
		t.Fatalf("mode after mid-turn plan exit = %q, want %q", got, runtimepolicy.ModeAcceptEdits)
	}
}
