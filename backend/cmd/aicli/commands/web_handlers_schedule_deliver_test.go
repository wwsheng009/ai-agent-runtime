package commands

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// TestScheduleDeliverPlanMapping 钉住 S2b 的 mesh 分发映射：op 走 POST
// /web/api/schedule-deliver，参数按白名单搬运（未知键丢弃，occurrences 数组透传）。
func TestScheduleDeliverPlanMapping(t *testing.T) {
	args := []byte(`{"session_id":"s1","text":"hello","occurrences":["k1"],"extra":"dropped"}`)
	plan, err := chatWebMeshCallPlanFor("schedule.deliver", args, "")
	if err != nil {
		t.Fatalf("planFor: %v", err)
	}
	if plan.method != http.MethodPost || plan.path != ChatWebAPIScheduleDeliverPath {
		t.Fatalf("plan = %s %s, want POST %s", plan.method, plan.path, ChatWebAPIScheduleDeliverPath)
	}
	var body map[string]any
	if err := json.Unmarshal(plan.body, &body); err != nil {
		t.Fatalf("decode plan body: %v", err)
	}
	if body["session_id"] != "s1" || body["text"] != "hello" {
		t.Fatalf("body = %+v", body)
	}
	if _, ok := body["extra"]; ok {
		t.Fatal("unknown keys must be dropped by the whitelist subset")
	}
	occurrences, ok := body["occurrences"].([]any)
	if !ok || len(occurrences) != 1 || occurrences[0] != "k1" {
		t.Fatalf("occurrences = %#v, want [k1]", body["occurrences"])
	}
}

// TestScheduleDeliverOpRegistered 白名单契约：op 已知、写操作、端点与常量同源。
func TestScheduleDeliverOpRegistered(t *testing.T) {
	spec, ok := mesh.CallOpSpecFor("schedule.deliver")
	if !ok {
		t.Fatal("schedule.deliver must be in the mesh op whitelist")
	}
	if !spec.Write {
		t.Fatal("schedule.deliver must be a write op (no implicit allow)")
	}
	if spec.Endpoint != ChatWebAPIScheduleDeliverPath {
		t.Fatalf("endpoint = %q, want %q", spec.Endpoint, ChatWebAPIScheduleDeliverPath)
	}
}
