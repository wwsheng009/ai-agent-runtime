package toolbroker

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

func newScheduleBroker(t *testing.T) *Broker {
	t.Helper()
	store, err := schedule.NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	return &Broker{Schedule: schedule.NewService(store, func() time.Time { return now })}
}

func TestScheduleToolsAreAdvertisedOnlyWithService(t *testing.T) {
	without := toolDefinitionNames((&Broker{}).Definitions())
	for _, name := range without {
		if name == ToolScheduleCreate || name == ToolScheduleList {
			t.Fatalf("schedule tools must stay hidden without a service, got %s", name)
		}
	}
	with := toolDefinitionNames(newScheduleBroker(t).Definitions())
	want := map[string]bool{ToolScheduleCreate: false, ToolScheduleList: false, ToolScheduleUpdate: false, ToolScheduleDelete: false}
	for _, name := range with {
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("schedule tool %s is missing from Definitions()", name)
		}
	}
}

func TestScheduleToolsRoundTrip(t *testing.T) {
	broker := newScheduleBroker(t)
	ctx := context.Background()

	created, _, err := broker.Execute(ctx, "session-1", ToolScheduleCreate, map[string]interface{}{
		"prompt": "check the build", "title": "Build check", "after_seconds": float64(600),
	})
	if err != nil {
		t.Fatalf("schedule_create: %v", err)
	}
	view, ok := created.(schedule.View)
	if !ok {
		t.Fatalf("schedule_create returned %T, want schedule.View", created)
	}
	if view.Kind != schedule.KindAfter || view.State != schedule.StateScheduled {
		t.Fatalf("unexpected view: %+v", view)
	}

	listed, _, err := broker.Execute(ctx, "session-1", ToolScheduleList, map[string]interface{}{})
	if err != nil {
		t.Fatalf("schedule_list: %v", err)
	}
	views, ok := listed.([]schedule.View)
	if !ok || len(views) != 1 {
		t.Fatalf("schedule_list = %#v, want one view", listed)
	}

	deleted, _, err := broker.Execute(ctx, "session-1", ToolScheduleDelete, map[string]interface{}{"id": view.ID})
	if err != nil {
		t.Fatalf("schedule_delete: %v", err)
	}
	if payload, ok := deleted.(map[string]interface{}); !ok || payload["deleted"] != true {
		t.Fatalf("schedule_delete = %#v, want deleted:true", deleted)
	}
}

func TestScheduleToolsSelectorAndErrorCodes(t *testing.T) {
	broker := newScheduleBroker(t)
	ctx := context.Background()

	_, _, err := broker.Execute(ctx, "session-1", ToolScheduleCreate, map[string]interface{}{
		"prompt": "x", "title": "t", "after_seconds": float64(60), "every_seconds": float64(60),
	})
	if err != nil {
		t.Fatalf("selector error must be a structured value, got err %v", err)
	}

	result, _, err := broker.Execute(ctx, "session-1", ToolScheduleCreate, map[string]interface{}{
		"prompt": "x", "title": "  ", "after_seconds": float64(60),
	})
	if err != nil {
		t.Fatalf("blank title must be a structured value, got err %v", err)
	}
	payload, ok := result.(map[string]interface{})
	if !ok || payload["code"] != string(schedule.CodeInvalidPrompt) {
		t.Fatalf("blank title = %#v, want invalid_prompt", result)
	}
}
