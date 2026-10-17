package connectors

// replySink 契约回归：宿主回调的是 assistant_delta 增量片段，而
// StreamingSession.Update 接收累计全文（与 StreamMirror 同口径）。若 sink
// 原样透传增量，QQ（replace 前缀约束）/Telegram/飞书都会只显示首段。

import (
	"context"
	"testing"
	"time"
)

type recordingStreamingSession struct {
	updates []string
}

func (s *recordingStreamingSession) Update(_ context.Context, text string) error {
	s.updates = append(s.updates, text)
	return nil
}

func (s *recordingStreamingSession) Final(context.Context, []OutboundMessage) error { return nil }
func (s *recordingStreamingSession) Fail(context.Context, string) error             { return nil }

func TestReplySinkAccumulatesDeltas(t *testing.T) {
	session := &recordingStreamingSession{}
	throttle := newDeltaThrottle(time.Millisecond)
	sink := &replySink{session: session, throttle: throttle}

	for _, delta := range []string{"你", "好", "！"} {
		time.Sleep(2 * time.Millisecond) // 越过节流窗口，确保每帧都放行
		if err := sink.Delta(context.Background(), delta); err != nil {
			t.Fatalf("delta %q: %v", delta, err)
		}
	}

	want := []string{"你", "你好", "你好！"}
	if len(session.updates) != len(want) {
		t.Fatalf("updates = %v, want %v", session.updates, want)
	}
	for i, text := range want {
		if session.updates[i] != text {
			t.Fatalf("update[%d] = %q, want %q", i, session.updates[i], text)
		}
	}
}
