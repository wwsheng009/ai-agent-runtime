package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// P1.5-A：store 级批量写入（docs/plan/runtime-store-event-persistence-batching-and-async-plan-20260918.md §3.1）。
//
// 语义契约（§9 D1/D2）：
//   - 一个批量一个事务，all-or-nothing；失败时整批不可见；
//   - seq 与单条写入同源：批内按 session 分组，每组一次 MAX(seq) 取基，
//     组内按输入顺序 base+1.. 递增，跨进程仍由 SQLite 写锁串行化；
//   - 通知保持逐事件、非阻塞、可丢（消费者按 ListEvents(afterSeq) 追平）；
//   - 批上限 maxAppendEventsBatch，超限由调用方拆批。
const maxAppendEventsBatch = 128

// runtimeAppendBatchHook 是测试专用探针（生产恒为 nil），在每条 INSERT 之前调用；
// 返回错误时整批回滚，用于验证 all-or-nothing。
var runtimeAppendBatchHook func(index int) error

type preparedAppendEvent struct {
	event       runtimeevents.Event
	payloadJSON []byte
}

// AppendEvents stores a batch of events in one transaction.
// Returns the assigned seq for each event, in input order.
// All-or-nothing: on error no event of the batch is visible.
func (s *SQLiteRuntimeStore) AppendEvents(ctx context.Context, events []runtimeevents.Event) ([]int64, error) {
	if s == nil {
		return nil, fmt.Errorf("runtime store is not initialized")
	}
	if len(events) == 0 {
		return nil, nil
	}
	if len(events) > maxAppendEventsBatch {
		return nil, fmt.Errorf("append events batch size %d exceeds limit %d", len(events), maxAppendEventsBatch)
	}
	if len(events) == 1 {
		seq, err := s.AppendEvent(ctx, events[0])
		if err != nil {
			return nil, err
		}
		return []int64{seq}, nil
	}
	if err := s.ensureCtx(ctx); err != nil {
		return nil, err
	}
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	if err := s.checkPoolReentry("AppendEvents"); err != nil {
		return nil, err
	}

	// 校验与序列化放在事务外：失败不占用写锁。
	batchNow := time.Now().UTC()
	prepared := make([]preparedAppendEvent, len(events))
	for index := range events {
		event := events[index]
		if strings.TrimSpace(event.SessionID) == "" {
			return nil, fmt.Errorf("event requires session id")
		}
		if event.Timestamp.IsZero() {
			event.Timestamp = batchNow
		}
		payloadJSON, err := json.Marshal(event.Payload)
		if err != nil {
			return nil, fmt.Errorf("marshal event payload: %w", err)
		}
		// P1-2/H2：批量路径与单条路径共用同一字节上限，行为一致。
		payloadJSON = boundSessionEventPayload(payloadJSON, s.sessionEventPayloadByteCap())
		prepared[index] = preparedAppendEvent{event: event, payloadJSON: payloadJSON}
	}

	s.maintenance.touchWrite()
	start := time.Now()
	var seqs []int64
	err := sqliteutil.RetryWriteTx(ctx, s.trackWriteRetry, func(attemptCtx context.Context) error {
		var txErr error
		seqs, txErr = s.appendEventsTx(attemptCtx, prepared)
		return txErr
	})
	elapsed := time.Since(start).Nanoseconds()
	counters := &s.appendCounters
	counters.batches.Add(1)
	counters.batchedEvents.Add(int64(len(events)))
	counters.batchTotalNs.Add(elapsed)
	if err != nil {
		s.trackWriteError(err)
		return nil, err
	}
	return seqs, nil
}

func (s *SQLiteRuntimeStore) appendEventsTx(ctx context.Context, prepared []preparedAppendEvent) ([]int64, error) {
	s.mu.Lock()
	txStart := time.Now()
	tx, err := s.db.BeginTx(ctx, sqliteutil.WriteTxOptions)
	if err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("begin event batch tx: %w", err)
	}
	rollback := func() {
		_ = tx.Rollback()
		s.mu.Unlock()
	}

	type sessionGroup struct {
		indices []int
		base    int64
		maxSeq  int64
	}
	groups := make(map[string]*sessionGroup)
	order := make([]string, 0, len(prepared))
	for index := range prepared {
		sessionID := prepared[index].event.SessionID
		group, ok := groups[sessionID]
		if !ok {
			group = &sessionGroup{}
			groups[sessionID] = group
			order = append(order, sessionID)
		}
		group.indices = append(group.indices, index)
	}

	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO session_events (
			session_id, seq, type, trace_id, agent_name, tool_name, payload_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		rollback()
		return nil, fmt.Errorf("prepare session event batch: %w", err)
	}
	defer statement.Close()

	seqs := make([]int64, len(prepared))
	for _, sessionID := range order {
		group := groups[sessionID]
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(seq), 0) FROM session_events WHERE session_id = ?
		`, sessionID).Scan(&group.base); err != nil {
			rollback()
			return nil, fmt.Errorf("next event seq: %w", err)
		}
		for offset, index := range group.indices {
			seq := group.base + int64(offset) + 1
			if hook := runtimeAppendBatchHook; hook != nil {
				if err := hook(index); err != nil {
					rollback()
					return nil, err
				}
			}
			event := prepared[index].event
			if _, err := statement.ExecContext(ctx, event.SessionID, seq, event.Type,
				nullIfEmpty(event.TraceID), nullIfEmpty(event.AgentName), nullIfEmpty(event.ToolName),
				string(prepared[index].payloadJSON), event.Timestamp.Format(time.RFC3339Nano)); err != nil {
				rollback()
				return nil, fmt.Errorf("insert session event: %w", err)
			}
			seqs[index] = seq
			group.maxSeq = seq
		}
	}

	// 每个 session 仅用批内最大 seq 触发一次 prune（DELETE 仍在事务内）。
	for _, sessionID := range order {
		group := groups[sessionID]
		if err := s.pruneRuntimeRowsTx(ctx, tx, sessionID, group.maxSeq, 0); err != nil {
			rollback()
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("commit session event batch: %w", err)
	}
	s.appendCounters.batchLockHoldNs.Add(time.Since(txStart).Nanoseconds())
	s.mu.Unlock()

	// 通知在提交后逐事件发出；非阻塞、可丢，durable 顺序由 seq 决定。
	for index := range prepared {
		s.notifyEventWatchers(seqs[index], prepared[index].event)
	}
	return seqs, nil
}
