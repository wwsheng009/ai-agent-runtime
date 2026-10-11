package tunnel

import (
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func TestAttentionForNotifyKind(t *testing.T) {
	cases := map[string]string{
		wire.NotifyKindTurnCompleted: "completed",
		wire.NotifyKindApproval:      "approval",
		wire.NotifyKindQuestion:      "question",
		wire.NotifyKindError:         "error",
		wire.NotifyKindWaiting:       "waiting",
		"":                           "",
		"unknown":                    "",
	}
	for kind, want := range cases {
		if got := attentionForNotifyKind(kind); got != want {
			t.Fatalf("attentionForNotifyKind(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestApplyNotifyToSessions(t *testing.T) {
	base := func() []wire.SessionSummary {
		return []wire.SessionSummary{{SessionID: "s1", Title: "one", State: "running", Busy: true}}
	}
	ts := time.Unix(100, 0).UTC()
	got := applyNotifyToSessions(base(), wire.Notify{
		SessionID: "s1",
		Kind:      wire.NotifyKindApproval,
		Title:     "renamed",
		TS:        ts,
	})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Attention != "approval" || got[0].Title != "renamed" || !got[0].UpdatedAt.Equal(ts) {
		t.Fatalf("updated session = %+v", got[0])
	}
	if got[0].State != "running" || !got[0].Busy {
		t.Fatalf("applyNotify clobbered unrelated fields: %+v", got[0])
	}

	// An empty title must not overwrite the existing one.
	kept := applyNotifyToSessions(base(), wire.Notify{SessionID: "s1", Kind: wire.NotifyKindQuestion})
	if kept[0].Title != "one" || kept[0].Attention != "question" {
		t.Fatalf("empty title clobbered: %+v", kept[0])
	}

	appended := applyNotifyToSessions(nil, wire.Notify{SessionID: "s2", Kind: wire.NotifyKindQuestion})
	if len(appended) != 1 || appended[0].SessionID != "s2" || appended[0].Attention != "question" {
		t.Fatalf("appended = %+v", appended)
	}
}

func TestMarkNotifySeenDedup(t *testing.T) {
	s := NewServer(Config{})
	if s.markNotifySeen("k1") {
		t.Fatal("first key must be unseen")
	}
	if !s.markNotifySeen("k1") {
		t.Fatal("second key must be seen")
	}
	if s.markNotifySeen("k2") {
		t.Fatal("distinct key must be unseen")
	}
}
