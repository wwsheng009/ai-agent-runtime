package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func listPlanReviewRequestedEvents(t *testing.T, store *InMemoryRuntimeStore, sessionID string) []runtimeevents.Event {
	t.Helper()
	events, err := store.ListEvents(context.Background(), sessionID, 0, 0)
	require.NoError(t, err)
	out := make([]runtimeevents.Event, 0, 1)
	for _, event := range events {
		if event.Type == EventPlanReviewRequested {
			out = append(out, event)
		}
	}
	return out
}

// plan_review on the current session plan returns the body plus the verdict
// entry points, and announces the opened surface to hosts.
func TestSessionActorReviewPlanSessionSurface(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	actor, _, store := newPlanReviewBackstopActor(t, runtimepolicy.ModeDefault, workspace)

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "docs/plan.md"})
	require.NoError(t, err)
	writeBackstopPlanFile(t, workspace, "docs/plan.md", "# 计划\n\n1. 收紧路径语义\n")

	result, err := actor.ReviewPlan(ctx, "", toolbroker.PlanReviewArgs{})
	require.NoError(t, err)
	require.True(t, result.Active)
	require.Equal(t, "session", result.Source)
	require.Equal(t, "docs/plan.md", result.PlanPath)
	require.Equal(t, planReviewVerdictOptions(), result.VerdictOptions)
	require.Contains(t, result.Content, "收紧路径语义")
	require.False(t, result.Truncated)
	require.Contains(t, result.Hint, "/plan approve")

	events := listPlanReviewRequestedEvents(t, store, actor.id)
	require.Len(t, events, 1)
	require.Equal(t, "docs/plan.md", events[0].Payload["plan_path"])
	require.Equal(t, "session", events[0].Payload["source"])
	require.Equal(t, true, events[0].Payload["active"])
}

// plan_path can point at an arbitrary plan file even when plan mode is not
// active; the review surface is read-only and never flips plan state.
func TestSessionActorReviewPlanExplicitPathOutsidePlanMode(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	actor, _, _ := newPlanReviewBackstopActor(t, runtimepolicy.ModeDefault, workspace)
	writeBackstopPlanFile(t, workspace, "notes/legacy-plan.md", "# 旧计划\n")

	result, err := actor.ReviewPlan(ctx, "", toolbroker.PlanReviewArgs{PlanPath: "notes/legacy-plan.md"})
	require.NoError(t, err)
	require.False(t, result.Active)
	require.Equal(t, "notes/legacy-plan.md", result.PlanPath)
	require.Contains(t, result.Content, "旧计划")
	require.Contains(t, result.Hint, "/plan enter", "an inactive plan must point at re-entry, not at a verdict")

	reloaded, err := actor.loadSession(ctx)
	require.NoError(t, err)
	require.False(t, planmode.IsActive(planmode.Load(reloaded)), "review must not change plan state")
	require.Equal(t, string(runtimepolicy.ModeDefault), actor.currentPermissionMode(reloaded, ctx))
}

// An archived record is addressable by id (with "/" in it) and by version.
func TestSessionActorReviewPlanArchiveSurface(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	actor, _, _ := newPlanReviewBackstopActor(t, runtimepolicy.ModeDefault, workspace)

	rec, err := planmode.ArchivePlan(ctx, planmode.ArchiveOptions{
		Store:     actor.planArtifactStore(),
		SessionID: actor.id,
		Workspace: workspace,
		PlanPath:  "docs/plan.md",
		Decision:  "enter",
		Source:    "user",
		Content:   []byte("# 归档计划 v1\n"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, rec.ID)

	result, err := actor.ReviewPlan(ctx, "", toolbroker.PlanReviewArgs{PlanID: rec.ID})
	require.NoError(t, err)
	require.Equal(t, "archive", result.Source)
	require.Equal(t, rec.ID, result.PlanID)
	require.Equal(t, "docs/plan.md", result.PlanPath)
	require.GreaterOrEqual(t, result.Version, 1)
	require.Contains(t, result.Content, "归档计划 v1")
	require.Contains(t, result.Hint, "只读")

	_, err = actor.ReviewPlan(ctx, "", toolbroker.PlanReviewArgs{PlanID: "missing/plan"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

// The session plan body is bounded so a huge plan cannot blow the context; the
// truncation flag keeps the caller honest about what it received.
func TestSessionActorReviewPlanTruncatesLargeBody(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	actor, _, _ := newPlanReviewBackstopActor(t, runtimepolicy.ModeDefault, workspace)

	large := strings.Repeat("x", maxPlanReviewBytes+1024)
	writeBackstopPlanFile(t, workspace, "plan.md", large)

	result, err := actor.ReviewPlan(ctx, "", toolbroker.PlanReviewArgs{})
	require.NoError(t, err)
	require.True(t, result.Truncated)
	require.Equal(t, maxPlanReviewBytes, len(result.Content))
	require.Equal(t, len(large), result.ContentSize)
}

// An empty (or not yet written) plan is reported as such instead of as an error.
func TestSessionActorReviewPlanEmptyBody(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	actor, _, _ := newPlanReviewBackstopActor(t, runtimepolicy.ModeDefault, workspace)

	result, err := actor.ReviewPlan(ctx, "", toolbroker.PlanReviewArgs{})
	require.NoError(t, err)
	require.Empty(t, result.Content)
	require.Contains(t, result.Hint, "为空或不可读")
}

// A session plan that is also archived surfaces the archive id/version so a
// follow-up call can address the record.
func TestSessionActorReviewPlanLinksArchiveRecord(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	actor, _, _ := newPlanReviewBackstopActor(t, runtimepolicy.ModeDefault, workspace)

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "docs/plan.md"})
	require.NoError(t, err)
	writeBackstopPlanFile(t, workspace, "docs/plan.md", "# 计划\n")

	result, err := actor.ReviewPlan(ctx, "", toolbroker.PlanReviewArgs{})
	require.NoError(t, err)
	require.NotEmpty(t, result.PlanID, "enter archives metadata, so the record id must be reported")
	// enter only registers metadata, so the record still has no snapshot round.
	require.Equal(t, 0, result.Version)
}

// The actor must back the plan_review tool so the controller is reachable from
// the model surface (the tool is advertised only when the host wires it).
func TestSessionActorWiresPlanReviewController(t *testing.T) {
	actor, _, _ := newPlanReviewBackstopActor(t, runtimepolicy.ModeDefault, t.TempDir())

	broker := actor.agent.GetToolBroker()
	require.NotNil(t, broker, "actor must configure a tool broker")
	require.NotNil(t, broker.PlanReview, "actor must implement the plan review controller")

	names := make([]string, 0, len(broker.Definitions()))
	for _, def := range broker.Definitions() {
		names = append(names, def.Name)
	}
	require.Contains(t, names, toolbroker.ToolPlanReview)
}

// plan_review with compare_version attaches the round-to-round diff so a model
// can see what moved between two archived rounds without re-reading both bodies.
func TestSessionActorReviewPlanCompareVersionDiff(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	actor, _, _ := newPlanReviewBackstopActor(t, runtimepolicy.ModeDefault, workspace)

	rec, err := planmode.ArchivePlan(ctx, planmode.ArchiveOptions{
		Store:     actor.planArtifactStore(),
		SessionID: actor.id,
		Workspace: workspace,
		PlanPath:  "docs/plan.md",
		Decision:  "enter",
		Source:    "user",
		Content:   []byte("# 计划\n\n1. step one\n2. step two\n"),
	})
	require.NoError(t, err)
	rec, err = planmode.ArchivePlan(ctx, planmode.ArchiveOptions{
		Store:     actor.planArtifactStore(),
		SessionID: actor.id,
		Workspace: workspace,
		PlanPath:  "docs/plan.md",
		Decision:  "request_changes",
		Source:    "user",
		Content:   []byte("# 计划\n\n1. step one\n2. step two revised\n3. step three\n"),
	})
	require.NoError(t, err)
	require.Equal(t, 2, rec.Version)

	result, err := actor.ReviewPlan(ctx, "", toolbroker.PlanReviewArgs{PlanID: rec.ID, CompareVersion: 1})
	require.NoError(t, err)
	require.NotNil(t, result.Diff)
	require.Equal(t, 1, result.Diff.FromVersion)
	require.Equal(t, 2, result.Diff.ToVersion)
	require.Equal(t, 2, result.Diff.Added)
	require.Equal(t, 1, result.Diff.Removed)
	require.False(t, result.Diff.Identical)
	require.Contains(t, result.Diff.Text, "-2. step two")
	require.Contains(t, result.Diff.Text, "+2. step two revised")
	require.Contains(t, result.Diff.Text, "--- v1 enter (user, ")
	require.Contains(t, result.Hint, "轮次对比已附")

	// Comparing a round with itself is honest data, not an error.
	same, err := actor.ReviewPlan(ctx, "", toolbroker.PlanReviewArgs{PlanID: rec.ID, Version: 2, CompareVersion: 2})
	require.NoError(t, err)
	require.NotNil(t, same.Diff)
	require.True(t, same.Diff.Identical)

	// A version the retention policy pruned (or that never existed) is an error.
	_, err = actor.ReviewPlan(ctx, "", toolbroker.PlanReviewArgs{PlanID: rec.ID, CompareVersion: 9})
	require.Error(t, err)
	require.Contains(t, err.Error(), "compare_version v9")
}

// Asking for a round diff on a plan that has no archive record explains why it is
// missing instead of failing the whole review payload.
func TestSessionActorReviewPlanCompareVersionWithoutArchive(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	actor, _, _ := newPlanReviewBackstopActor(t, runtimepolicy.ModeDefault, workspace)

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "docs/plan.md"})
	require.NoError(t, err)
	writeBackstopPlanFile(t, workspace, "docs/plan.md", "# 计划\n\n1. 唯一一轮\n")

	result, err := actor.ReviewPlan(ctx, "", toolbroker.PlanReviewArgs{CompareVersion: 1})
	require.NoError(t, err)
	require.Nil(t, result.Diff)
	require.Contains(t, result.Hint, "未生成轮次 diff", "enter 只登记元数据，此时只有提示而不是错误")
	require.Contains(t, result.Content, "唯一一轮", "the review payload must stay usable")

	// A plan that was never registered at all is also explained, not failed.
	writeBackstopPlanFile(t, workspace, "notes/unregistered.md", "# 未登记计划\n")
	unregistered, err := actor.ReviewPlan(ctx, "", toolbroker.PlanReviewArgs{PlanPath: "notes/unregistered.md", CompareVersion: 1})
	require.NoError(t, err)
	require.Nil(t, unregistered.Diff)
	require.Contains(t, unregistered.Hint, "还没有归档记录")

	// Once the plan has archived rounds, the same call produces the comparison.
	_, err = planmode.ArchivePlan(ctx, planmode.ArchiveOptions{
		Store:     actor.planArtifactStore(),
		SessionID: actor.id,
		Workspace: workspace,
		PlanPath:  "docs/plan.md",
		Decision:  "request_changes",
		Source:    "user",
		Content:   []byte("# 计划\n\n1. 唯一一轮\n"),
	})
	require.NoError(t, err)
	_, err = planmode.ArchivePlan(ctx, planmode.ArchiveOptions{
		Store:     actor.planArtifactStore(),
		SessionID: actor.id,
		Workspace: workspace,
		PlanPath:  "docs/plan.md",
		Decision:  "request_changes",
		Source:    "user",
		Content:   []byte("# 计划\n\n1. 唯一一轮\n2. 追加一轮\n"),
	})
	require.NoError(t, err)
	withArchive, err := actor.ReviewPlan(ctx, "", toolbroker.PlanReviewArgs{CompareVersion: 1})
	require.NoError(t, err)
	require.NotNil(t, withArchive.Diff)
	require.Equal(t, 1, withArchive.Diff.FromVersion)
	require.Equal(t, 2, withArchive.Diff.ToVersion)
	require.Equal(t, 1, withArchive.Diff.Added)
}
