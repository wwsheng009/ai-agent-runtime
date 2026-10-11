package toolbroker

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

// depthContextStore 是一个可注入委派深度的 SessionContextStore 测试替身。
type depthContextStore struct {
	values map[string]map[string]interface{}
}

func (s *depthContextStore) LoadContextValue(_ context.Context, sessionID, key string) (interface{}, error) {
	if values, ok := s.values[sessionID]; ok {
		if value, ok := values[key]; ok {
			return value, nil
		}
	}
	return nil, nil
}

func (s *depthContextStore) SaveContextValue(_ context.Context, sessionID, key string, value interface{}) error {
	if s.values == nil {
		s.values = map[string]map[string]interface{}{}
	}
	if s.values[sessionID] == nil {
		s.values[sessionID] = map[string]interface{}{}
	}
	s.values[sessionID][key] = value
	return nil
}

func newDepthScheduleBroker(t *testing.T, depth int) *Broker {
	t.Helper()
	store, err := schedule.NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	broker := &Broker{
		Schedule: schedule.NewService(store, func() time.Time { return now }),
	}
	ctxStore := &depthContextStore{}
	if depth > 0 {
		_ = ctxStore.SaveContextValue(context.Background(), "child-session", AgentSessionContextDepth, depth)
	}
	broker.SessionContextStore = ctxStore
	return broker
}

func TestScheduleToolsRejectDelegatedCaller(t *testing.T) {
	broker := newDepthScheduleBroker(t, 1)
	ctx := context.Background()

	result, _, err := broker.Execute(ctx, "child-session", ToolScheduleCreate, map[string]interface{}{
		"prompt": "x", "title": "t", "after_seconds": float64(60),
	})
	if err != nil {
		t.Fatalf("create must be a structured refusal, got err %v", err)
	}
	if payload, ok := result.(map[string]interface{}); !ok || payload["code"] != string(schedule.CodeSubagentSession) {
		t.Fatalf("create = %#v, want subagent_session", result)
	}

	// list 仍可服务（返回空数组而非拒绝）。
	listed, _, err := broker.Execute(ctx, "child-session", ToolScheduleList, map[string]interface{}{})
	if err != nil {
		t.Fatalf("list must still be served: %v", err)
	}
	if _, ok := listed.([]schedule.View); !ok {
		t.Fatalf("list = %#v, want views", listed)
	}

	// delete 仍可服务。
	deleted, _, err := broker.Execute(ctx, "child-session", ToolScheduleDelete, map[string]interface{}{"id": "schedule-x"})
	if err != nil {
		t.Fatalf("delete must still be served: %v", err)
	}
	if payload, ok := deleted.(map[string]interface{}); !ok || payload["deleted"] != false {
		t.Fatalf("delete = %#v, want deleted:false", deleted)
	}
}

func TestScheduleToolsAllowTopLevelCaller(t *testing.T) {
	broker := newDepthScheduleBroker(t, 0)
	ctx := context.Background()
	created, _, err := broker.Execute(ctx, "top-session", ToolScheduleCreate, map[string]interface{}{
		"prompt": "x", "title": "t", "after_seconds": float64(60),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, ok := created.(schedule.View); !ok {
		t.Fatalf("create = %#v, want view", created)
	}
}
