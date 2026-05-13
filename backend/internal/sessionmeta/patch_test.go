package sessionmeta

import (
	"context"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestApplyContextPatch_PreservesUnrelatedContext(t *testing.T) {
	storage := runtimechat.NewInMemoryStorage()
	session := runtimechat.NewSession("tester")
	session.SetContext("keep", "value")
	session.AddMessage(*runtimetypes.NewUserMessage("hello"))
	if err := storage.Save(context.Background(), session); err != nil {
		t.Fatalf("save session: %v", err)
	}

	updated, err := ApplyContextPatch(context.Background(), storage, ContextPatch{
		SessionID: session.ID,
		SetContext: map[string]interface{}{
			"changed": "new",
		},
	})
	if err != nil {
		t.Fatalf("ApplyContextPatch failed: %v", err)
	}
	if got, ok := updated.GetContext("keep"); !ok || got != "value" {
		t.Fatalf("expected unrelated context to be preserved, got ok=%v value=%#v", ok, got)
	}
	if got, ok := updated.GetContext("changed"); !ok || got != "new" {
		t.Fatalf("expected changed context, got ok=%v value=%#v", ok, got)
	}
	if len(updated.History) != 1 || updated.History[0].Content != "hello" {
		t.Fatalf("expected history to be preserved, got %+v", updated.History)
	}
}

func TestApplyContextPatch_UsesLatestStoredSession(t *testing.T) {
	storage := runtimechat.NewInMemoryStorage()
	stale := runtimechat.NewSession("tester")
	stale.SetContext("stale", "old")
	if err := storage.Save(context.Background(), stale); err != nil {
		t.Fatalf("save stale session: %v", err)
	}
	latest, err := storage.Load(context.Background(), stale.ID)
	if err != nil {
		t.Fatalf("load latest session: %v", err)
	}
	latest.SetContext("latest", "kept")
	delete(latest.Metadata.Context, "stale")
	if err := storage.Save(context.Background(), latest); err != nil {
		t.Fatalf("save latest session: %v", err)
	}

	updated, err := ApplyContextPatch(context.Background(), storage, ContextPatch{
		SessionID: stale.ID,
		SetContext: map[string]interface{}{
			"patched": "value",
		},
	})
	if err != nil {
		t.Fatalf("ApplyContextPatch failed: %v", err)
	}
	if _, ok := updated.GetContext("stale"); ok {
		t.Fatalf("did not expect stale context to be restored: %#v", updated.Metadata.Context)
	}
	if got, ok := updated.GetContext("latest"); !ok || got != "kept" {
		t.Fatalf("expected latest context to remain, got ok=%v value=%#v", ok, got)
	}
	if got, ok := updated.GetContext("patched"); !ok || got != "value" {
		t.Fatalf("expected patched context, got ok=%v value=%#v", ok, got)
	}
}

func TestApplyContextPatch_UsesMergePolicy(t *testing.T) {
	storage := runtimechat.NewInMemoryStorage()
	session := runtimechat.NewSession("tester")
	session.SetContext("state", "complete")
	if err := storage.Save(context.Background(), session); err != nil {
		t.Fatalf("save session: %v", err)
	}

	updated, err := ApplyContextPatch(context.Background(), storage, ContextPatch{
		SessionID: session.ID,
		SetContext: map[string]interface{}{
			"state": "active",
		},
		MergePolicies: map[string]MergePolicy{
			"state": keepCompletePolicy{},
		},
	})
	if err != nil {
		t.Fatalf("ApplyContextPatch failed: %v", err)
	}
	if got, ok := updated.GetContext("state"); !ok || got != "complete" {
		t.Fatalf("expected merge policy to preserve complete, got ok=%v value=%#v", ok, got)
	}
}

type keepCompletePolicy struct{}

func (keepCompletePolicy) MergeContextValue(key string, oldValue, newValue interface{}) (interface{}, error) {
	if oldValue == "complete" && newValue == "active" {
		return oldValue, nil
	}
	return newValue, nil
}
