package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// 2026-09-26 会话 postmortem：终态通知的 durable mailbox 投递失败后只把错误塞进
// payload 字段，父会话没有任何独立信号。以下用例固定：瞬时失败要重试、成功后
// 幂等、彻底失败要有独立告警事件。

func newTerminalDeliveryTestCoordinator(t *testing.T, cfg SubagentBatchCoordinatorConfig) *SubagentBatchCoordinator {
	t.Helper()
	if cfg.Store == nil {
		cfg.Store = testStore(t)
	}
	if cfg.TerminalDeliveryAttempts <= 0 {
		cfg.TerminalDeliveryAttempts = 3
	}
	if cfg.TerminalDeliveryRetryDelay <= 0 {
		cfg.TerminalDeliveryRetryDelay = time.Millisecond
	}
	return NewSubagentBatchCoordinator(cfg)
}

func TestDeliverTerminalOnceRetriesTransientFailures(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	sink := func(context.Context, BatchTerminalNotification) BatchTerminalDelivery {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls < 3 {
			return BatchTerminalDelivery{Err: errors.New("mailbox is locked")}
		}
		return BatchTerminalDelivery{Status: BatchTerminalDeliveryPersisted}
	}
	c := newTerminalDeliveryTestCoordinator(t, SubagentBatchCoordinatorConfig{TerminalSink: sink})
	batch := &subagentbatch.SubagentBatch{
		BatchID: subagentbatch.NewID("batch"),
		Status:  subagentbatch.BatchCompleted,
		Version: 1,
	}

	delivery := c.deliverTerminalOnce(context.Background(), batch, "subagent.batch.completed", "delivery-key", map[string]interface{}{"k": "v"})
	require.NoError(t, delivery.Err)
	require.Equal(t, BatchTerminalDeliveryPersisted, delivery.Status)
	require.Equal(t, 3, delivery.Attempts)
	require.Equal(t, 3, calls)

	again := c.deliverTerminalOnce(context.Background(), batch, "subagent.batch.completed", "delivery-key", map[string]interface{}{"k": "v"})
	require.True(t, again.AlreadyDelivered)
	require.Equal(t, 3, calls, "the in-memory delivered marker must not invoke the sink again")
}

func TestTerminalDeliveryFailureEmitsVisibleAlert(t *testing.T) {
	attempts := 0
	sink := func(context.Context, BatchTerminalNotification) BatchTerminalDelivery {
		attempts++
		return BatchTerminalDelivery{Err: errors.New("insert mailbox session event mirror: database disk image is malformed")}
	}
	var mu sync.Mutex
	type capturedEvent struct {
		EventType string
		Payload   map[string]interface{}
	}
	var captured []capturedEvent
	emitter := func(eventType string, payload map[string]interface{}) {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, capturedEvent{EventType: eventType, Payload: payload})
	}
	c := newTerminalDeliveryTestCoordinator(t, SubagentBatchCoordinatorConfig{
		TerminalSink: sink,
		Emitter:      emitter,
	})
	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         "batch-delivery-alert",
		RootScopeID:     "parent-1",
		ParentSessionID: "parent-1",
		Status:          subagentbatch.BatchCompleted,
		TaskCount:       1,
		CompletedCount:  1,
		Version:         1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	c.emitTerminalEvent(context.Background(), batch, batch.BatchID, subagentbatch.BatchCompleted,
		BatchStartOptions{ParentSessionID: "parent-1"},
		subagentbatch.BatchSummary{TaskCount: 1, CompletedCount: 1}, nil, "", nil)

	require.Equal(t, 3, attempts)
	var terminal, alert map[string]interface{}
	for _, event := range captured {
		switch event.EventType {
		case "subagent.batch.completed":
			terminal = event.Payload
		case runtimeevents.EventSubagentBatchDeliveryFailed:
			alert = event.Payload
		}
	}
	require.NotNil(t, terminal, "the terminal display-mirror event must still fire")
	require.Equal(t, string(BatchTerminalDeliveryFailed), terminal["mailbox_delivery_status"])
	require.Equal(t, 3, terminal["mailbox_delivery_attempts"])
	require.Contains(t, terminal["mailbox_delivery_error"], "malformed")

	require.NotNil(t, alert, "a lost durable notification must be visible as its own alert")
	require.Equal(t, "batch-delivery-alert", alert["batch_id"])
	require.Equal(t, "subagent.batch.completed", alert["terminal_event_type"])
	require.Equal(t, 3, alert["mailbox_delivery_attempts"])
	require.Contains(t, alert["mailbox_delivery_error"], "malformed")
}
