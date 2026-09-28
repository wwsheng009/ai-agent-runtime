package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestCollectorIngestsRenderFenceDrops 锁定 P1-1b：渲染围栏拒绝按类别落进
// usage_render_fence_drops，ErrorPatterns 以 source="fence" 呈现且重复投递幂等累加。
func TestCollectorIngestsRenderFenceDrops(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	bus := runtimeevents.NewBus()
	collector := newCollector(store, nil, nil)
	collector.subscribe(bus)
	defer collector.close()

	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	publish := func(payload map[string]interface{}, when time.Time) {
		bus.Publish(runtimeevents.Event{
			Type:      runtimeevents.EventRenderFenceDropped,
			SessionID: "session-fence",
			Payload:   payload,
			Timestamp: when,
		})
	}

	publish(map[string]interface{}{
		"idle":            3,
		"closed":          2,
		"active_mismatch": 1,
		"last_reason":     "runtime event action targets closed run epoch",
	}, at)
	// 第二轮只上报 closed 增量（新 turn 键）：同一 (session, turn, class) 累加。
	publish(map[string]interface{}{"closed": 4, "turn_id": "turn-2"}, at.Add(time.Minute))

	result, err := store.ErrorPatterns(ErrorPatternsQuery{Top: 10})
	if err != nil {
		t.Fatalf("ErrorPatterns: %v", err)
	}
	fenceCounts := map[string]int{}
	for _, pattern := range result.Patterns {
		if pattern.Source != "fence" {
			continue
		}
		fenceCounts[pattern.FailureCategory] = pattern.Count
	}
	if fenceCounts["render_fence_drop_idle"] != 3 ||
		fenceCounts["render_fence_drop_closed"] != 6 ||
		fenceCounts["render_fence_drop_active_mismatch"] != 1 {
		t.Fatalf("fence patterns = %#v, want idle=3 closed=6 active_mismatch=1", fenceCounts)
	}

	fenceOnly, err := store.ErrorPatterns(ErrorPatternsQuery{Source: "fence"})
	if err != nil {
		t.Fatalf("ErrorPatterns(source=fence): %v", err)
	}
	if len(fenceOnly.Patterns) != 3 {
		t.Fatalf("fence-only patterns = %d, want 3", len(fenceOnly.Patterns))
	}
	for _, pattern := range fenceOnly.Patterns {
		if pattern.Source != "fence" {
			t.Fatalf("source filter leaked %q", pattern.Source)
		}
		if pattern.ErrorCode == "" {
			t.Fatalf("fence pattern %#v must carry a stable error_code", pattern)
		}
	}

	// 会话级过滤：不存在的会话必须过滤掉全部 fence 行（与 tools/requests 同口径）。
	other, err := store.ErrorPatterns(ErrorPatternsQuery{Source: "fence", SessionID: "session-other"})
	if err != nil {
		t.Fatalf("ErrorPatterns(session filter): %v", err)
	}
	if len(other.Patterns) != 0 {
		t.Fatalf("session-scoped fence patterns = %#v, want none", other.Patterns)
	}
}
