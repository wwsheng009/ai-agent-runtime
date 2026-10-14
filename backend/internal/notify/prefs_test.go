package notify

import (
	"context"
	"testing"
)

func prefsBool(v bool) *bool { return &v }

func prefsPriority(p Priority) *Priority { return &p }

// TestMemoryPreferenceStoreResolvePrecedence 锁定三级合并与字段级覆盖
// （06 §1 D6：global → session → channel）。
func TestMemoryPreferenceStoreResolvePrecedence(t *testing.T) {
	s := NewMemoryPreferenceStore()
	// 默认：不静音、门槛 low。
	eff := s.Resolve("s1", "ntfy")
	if eff.Muted || eff.MinPriority != PriorityLow || !eff.Allows(PriorityLow) {
		t.Fatalf("defaults = %+v", eff)
	}

	s.Set(GlobalScope(), Preference{MinPriority: prefsPriority(PriorityNormal)})
	if eff := s.Resolve("s1", "webpush"); eff.MinPriority != PriorityNormal || eff.Allows(PriorityLow) {
		t.Fatalf("global 门槛 = %+v", eff)
	}

	s.Set(SessionScope("s1"), Preference{MinPriority: prefsPriority(PriorityHigh)})
	if eff := s.Resolve("s1", "webpush"); eff.MinPriority != PriorityHigh || eff.Allows(PriorityNormal) {
		t.Fatalf("session 覆盖 = %+v", eff)
	}
	// 其它会话不受 session 覆盖影响。
	if eff := s.Resolve("s2", "webpush"); eff.MinPriority != PriorityNormal {
		t.Fatalf("其它会话 = %+v", eff)
	}

	s.Set(ChannelScope("webpush"), Preference{MinPriority: prefsPriority(PriorityNormal)})
	if eff := s.Resolve("s1", "webpush"); eff.MinPriority != PriorityNormal {
		t.Fatalf("channel 覆盖 = %+v", eff)
	}

	// 静音与解除：global 静音 + channel 显式 false 解除（字段级覆盖）。
	s2 := NewMemoryPreferenceStore()
	s2.Set(GlobalScope(), Preference{Muted: prefsBool(true)})
	if eff := s2.Resolve("s1", "ntfy"); !eff.Muted || eff.Allows(PriorityHigh) {
		t.Fatalf("global 静音 = %+v", eff)
	}
	s2.Set(ChannelScope("ntfy"), Preference{Muted: prefsBool(false)})
	if eff := s2.Resolve("s1", "ntfy"); eff.Muted {
		t.Fatalf("channel 解除静音 = %+v", eff)
	}
	if eff := s2.Resolve("s1", "webpush"); !eff.Muted {
		t.Fatalf("其它通道保持静音 = %+v", eff)
	}
}

// TestDispatcherPreferenceGating 锁定 Dispatcher 集成：静音通道被跳过并计入
// Skipped 回执；会话门槛拦截低优先、放行高优先；nil 偏好 = 行为不变。
func TestDispatcherPreferenceGating(t *testing.T) {
	web := &notifyTestChannel{name: "webpush"}
	ntfyCh := &notifyTestChannel{name: "ntfy"}
	d := NewDispatcher(nil, web, ntfyCh)
	prefs := NewMemoryPreferenceStore()
	d.SetPreferences(prefs)

	// channel:ntfy 静音：high 扇出时 ntfy 跳过（Skipped 记账），webpush 照投。
	prefs.Set(ChannelScope("ntfy"), Preference{Muted: prefsBool(true)})
	r, err := d.Dispatch(context.Background(), EventFor("approval", "k1", notifyTestNote("a")))
	if err != nil {
		t.Fatal(err)
	}
	if len(web.calls) != 1 || len(ntfyCh.calls) != 0 {
		t.Fatalf("calls = %d/%d", len(web.calls), len(ntfyCh.calls))
	}
	sawSkip := false
	for _, o := range r.Outcomes {
		if o.Channel == "ntfy" && o.Skipped {
			sawSkip = true
		}
	}
	if !sawSkip {
		t.Fatalf("outcomes = %+v", r.Outcomes)
	}

	// session 门槛 high：normal 被拦截；high 正常。
	prefs.Set(SessionScope("s-quiet"), Preference{MinPriority: prefsPriority(PriorityHigh)})
	ev := EventFor("completion", "k2", notifyTestNote("c"))
	ev.Session = "s-quiet"
	if _, err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if len(web.calls) != 1 {
		t.Fatal("normal 应被会话门槛拦截")
	}
	ev2 := EventFor("error", "k3", notifyTestNote("e"))
	ev2.Session = "s-quiet"
	if _, err := d.Dispatch(context.Background(), ev2); err != nil {
		t.Fatal(err)
	}
	if len(web.calls) != 2 {
		t.Fatal("high 应正常投递")
	}

	// nil 偏好（新 Dispatcher）= 无门槛，行为不变。
	d2 := NewDispatcher(nil, &notifyTestChannel{name: "webpush"})
	if _, err := d2.Dispatch(context.Background(), EventFor("completion", "k4", notifyTestNote("c"))); err != nil {
		t.Fatal(err)
	}
}
