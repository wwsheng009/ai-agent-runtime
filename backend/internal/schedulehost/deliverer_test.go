package schedulehost_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedulehost"
)

type stageDeliverer struct {
	calls atomic.Int64
	err   error
}

func (d *stageDeliverer) Deliver(_ context.Context, _, _ string) error {
	d.calls.Add(1)
	return d.err
}

// envelopeStageDeliverer 实现 schedule.EnvelopeDeliverer：验证 occurrence
// 键会原样透传到就近路由的每一层。
type envelopeStageDeliverer struct {
	stageDeliverer
	lastEnv schedule.DeliveryEnvelope
}

func (d *envelopeStageDeliverer) DeliverEnvelope(_ context.Context, env schedule.DeliveryEnvelope) error {
	d.calls.Add(1)
	d.lastEnv = env
	return d.err
}

func TestNewLocalFirstDelivererRequiresStage(t *testing.T) {
	if _, err := schedulehost.NewLocalFirstDeliverer(schedulehost.LocalFirstOptions{}); err == nil {
		t.Fatal("empty stages must fail (fail-closed)")
	}
}

func TestLocalFirstDelivererFallsBackInOrder(t *testing.T) {
	local := &stageDeliverer{err: errors.New("no local hub")}
	peer := &envelopeStageDeliverer{}
	http := &stageDeliverer{}
	d, err := schedulehost.NewLocalFirstDeliverer(schedulehost.LocalFirstOptions{
		Local: local, Peer: peer, HTTP: http,
	})
	if err != nil {
		t.Fatalf("NewLocalFirstDeliverer: %v", err)
	}
	env := schedule.DeliveryEnvelope{
		SessionID:   "session_x",
		Text:        "hello",
		Occurrences: []string{"schedule-1@2026-10-14T13:43:42Z"},
	}
	if err := d.DeliverEnvelope(context.Background(), env); err != nil {
		t.Fatalf("DeliverEnvelope: %v", err)
	}
	if local.calls.Load() != 1 || peer.calls.Load() != 1 {
		t.Fatalf("calls local=%d peer=%d, want 1/1", local.calls.Load(), peer.calls.Load())
	}
	if http.calls.Load() != 0 {
		t.Fatal("http stage must not run after peer success")
	}
	if len(peer.lastEnv.Occurrences) != 1 || peer.lastEnv.Occurrences[0] != env.Occurrences[0] {
		t.Fatalf("occurrences not passed through: %+v", peer.lastEnv)
	}
}

func TestLocalFirstDelivererAggregatesFailures(t *testing.T) {
	local := &stageDeliverer{err: errors.New("l1 boom")}
	http := &stageDeliverer{err: errors.New("l3 boom")}
	d, err := schedulehost.NewLocalFirstDeliverer(schedulehost.LocalFirstOptions{Local: local, HTTP: http})
	if err != nil {
		t.Fatalf("NewLocalFirstDeliverer: %v", err)
	}
	err = d.DeliverEnvelope(context.Background(), schedule.DeliveryEnvelope{SessionID: "s", Text: "t"})
	if err == nil {
		t.Fatal("all stages failing must return an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "local(L1)") || !strings.Contains(msg, "http(L3)") {
		t.Fatalf("aggregated error must name every failed stage: %s", msg)
	}
}
