package supervision

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The injected preflight block is what the parent model spends context on, so
// its content contract is asserted directly: business state and payload only.
// The control-plane spelling (sequence cursors, counters, action vocabularies,
// session/notification handles) belongs to DiagnosticsText, which hosts expose
// through /debug and the supervision tools.
func TestBuildDigest_InjectedTextCarriesNoControlPlaneNoise(t *testing.T) {
	store := testDigestStore(t, "supervision-digest-injection-contract")
	ctx := context.Background()

	_, err := store.UpsertNotification(ctx, testNotification("child-1", 11))
	require.NoError(t, err)

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
	})
	require.NoError(t, err)

	for _, noise := range []string{
		"snapshot_seq", "critical_unresolved", "action_required",
		"auto_actions_in_progress", "resolved_since_last_turn",
		"recommended=", "next_action=", "wake_budget", "resume_queue",
		"turn_id=", "can_finalize", "agent_run ", "artifact_refs",
		"error_class=", "result_ref=",
	} {
		require.NotContains(t, digest.Text, noise,
			"control-plane token %q must stay out of the injected prompt", noise)
	}
	// The one handle a decision needs is still there.
	require.Contains(t, digest.Text, "notification_id=")
	// And the same facts remain available to every diagnostics surface.
	require.Contains(t, digest.DiagnosticsTextOf(), "snapshot_seq")
	require.Contains(t, digest.DiagnosticsTextOf(), "recommended=cancel")
}

// A host that wires ItemDetail gets the child's own words in the injected
// block, in place of the truncated ledger reason.
func TestBuildDigest_ItemDetailReachesInjectedText(t *testing.T) {
	store := testDigestStore(t, "supervision-digest-item-detail")
	ctx := context.Background()

	_, err := store.UpsertNotification(ctx, testNotification("child-detail", 12))
	require.NoError(t, err)

	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		ItemDetail: func(_ context.Context, n Notification) string {
			if n.SubjectID != "child-detail" {
				return ""
			}
			return "全部 42 个测试通过；新增 3 个文件。"
		},
	})
	require.NoError(t, err)
	require.Len(t, digest.Items, 1)
	require.Equal(t, "全部 42 个测试通过；新增 3 个文件。", digest.Items[0].Detail)
	require.Contains(t, digest.Text, "全部 42 个测试通过；新增 3 个文件。")
	// Detail is indented under its row so the block stays scannable.
	require.Contains(t, digest.Text, "\n  全部 42 个测试通过")
}

// A chatty child must not be able to eat the parent's context budget: the
// detail is bounded and the tail is kept (the last lines explain the outcome).
func TestBuildDigest_ItemDetailIsBounded(t *testing.T) {
	store := testDigestStore(t, "supervision-digest-item-detail-budget")
	ctx := context.Background()

	_, err := store.UpsertNotification(ctx, testNotification("child-chatty", 13))
	require.NoError(t, err)

	long := strings.Repeat("verbose line\n", 400) + "FINAL LINE"
	digest, err := BuildDigest(ctx, store, DigestRequest{
		RootScopeID:           "root-session-1",
		TargetParentSessionID: "root-session-1",
		ItemDetail:            func(context.Context, Notification) string { return long },
	})
	require.NoError(t, err)
	require.LessOrEqual(t, len([]rune(digest.Items[0].Detail)), MaxDigestItemDetailRunes+1)
	require.Contains(t, digest.Items[0].Detail, "FINAL LINE")
}

// Progress rows keep the one handle convergence tools need (the child session
// id) but drop the rest of the control-plane spelling.
func TestFormatProgressBriefText_KeepsOnlyActionableHandles(t *testing.T) {
	digest := &Digest{Progress: []ProgressSummary{{
		GroupID:       "batch-1",
		Total:         3,
		Completed:     2,
		Running:       1,
		ProgressAgeMs: 12000,
		RunningTasks: []ProgressTask{{
			TaskID:         "worker-3",
			ChildSessionID: "session-worker-3",
			State:          "running",
			LastProgressAt: time.Now().Add(-12 * time.Second),
			LastMessage:    "bash: go test ./internal/supervision/",
		}},
	}}}
	text := formatProgressBriefText(digest, time.Now())
	require.Contains(t, text, "batch-1：共 3 项，完成 2，进行中 1")
	require.Contains(t, text, "worker-3（session session-worker-3）")
	require.Contains(t, text, "bash: go test ./internal/supervision/")
	require.NotContains(t, text, "2/3 completed")
	require.NotContains(t, text, "last progress")
}
