package toolbroker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedulehost"
)

func newScheduleBroker(t *testing.T) *Broker {
	t.Helper()
	store, err := schedule.NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	broker := &Broker{Schedule: schedule.NewService(store, func() time.Time { return now })}
	// S3：默认注入"在岗且兼容"，让既有用例不依赖本机真实 mesh 状态。
	broker.SchedulePresence = func() schedulehost.Presence {
		return schedulehost.Presence{
			State:       schedulehost.PresenceOnline,
			StoreSchema: schedule.SupportedSchemaVersion(),
		}
	}
	return broker
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

// TestScheduleCreateCarriesDriverNotice 钉住 S3（§4.1）：无调度者在岗/不兼容
// 时创建照常成功，但回执携带可操作警告（在线时无警告由 RoundTrip 用例覆盖）。
func TestScheduleCreateCarriesDriverNotice(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		presence schedulehost.Presence
		wantSub  string
	}{
		{
			name:     "driver absent",
			presence: schedulehost.Presence{State: schedulehost.PresenceAbsent, StoreSchema: schedule.SupportedSchemaVersion()},
			wantSub:  "无调度者在岗",
		},
		{
			name: "driver incompatible",
			presence: schedulehost.Presence{
				State:             schedulehost.PresenceIncompatible,
				StoreSchema:       schedule.SupportedSchemaVersion(),
				DriverStoreSchema: 3,
				HolderNodeID:      "node-old",
			},
			wantSub: "过旧",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broker := newScheduleBroker(t)
			presence := tc.presence
			broker.SchedulePresence = func() schedulehost.Presence { return presence }
			created, _, err := broker.Execute(ctx, "session-notice", ToolScheduleCreate, map[string]interface{}{
				"prompt": "p", "title": "t", "after_seconds": float64(600),
			})
			if err != nil {
				t.Fatalf("schedule_create: %v", err)
			}
			view, ok := created.(schedule.View)
			if !ok {
				t.Fatalf("returned %T, want schedule.View", created)
			}
			if !strings.Contains(view.Notice, tc.wantSub) {
				t.Fatalf("notice = %q, want substring %q", view.Notice, tc.wantSub)
			}
		})
	}
}
