package toolbroker

import (
	"context"
	"errors"
	"strings"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// AgentEventsFlow is the host-neutral subagent_events window loop.
//
// Hosts supply list/subscribe/build callbacks; mailbox selection, wait windows,
// soft timeout, pagination, unread probing and the 500ms wake tick stay here so
// runtime-server and aicli cannot drift.
type AgentEventsFlow struct {
	Resolve         func(context.Context, string) (string, error)
	Subscribe       func(context.Context, string, bool) (<-chan struct{}, func())
	List            func(context.Context, string, int64, int) ([]runtimeevents.Event, error)
	ListMailbox     func(context.Context, string, int64, int) ([]runtimeevents.Event, bool, bool, error)
	FilterMailbox   func([]runtimeevents.Event) []runtimeevents.Event
	Build           func(string, []runtimeevents.Event) *AgentEventsResult
	Unread          func(context.Context, string, int64) int
	ShouldSoftRetry func(context.Context, error) bool
}

func (f AgentEventsFlow) Run(ctx context.Context, sessionID string, args ReadAgentEventsArgs) (*AgentEventsResult, error) {
	if f.List == nil || f.Subscribe == nil || f.Build == nil {
		return nil, errors.New("agent events flow: list/subscribe/build callbacks not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if !args.MailboxOnly && f.Resolve != nil {
		resolved, err := f.Resolve(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		sessionID = resolved
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 20
	}
	waitMs := args.WaitMs
	if waitMs < 0 {
		waitMs = 0
	}
	readCtx := ctx
	cancel := func() {}
	if waitMs > 0 {
		readCtx, cancel = context.WithTimeout(ctx, time.Duration(waitMs)*time.Millisecond)
	}
	defer cancel()
	wakeCh, unsubscribe := f.Subscribe(readCtx, sessionID, args.MailboxOnly)
	if unsubscribe != nil {
		defer unsubscribe()
	}
	softTimeout := func() (*AgentEventsResult, error) {
		return f.softTimeout(ctx, sessionID, args.AfterSeq, limit, args.MailboxOnly)
	}
	for {
		if args.MailboxOnly && f.ListMailbox != nil {
			events, ok, hasMailboxRows, err := f.ListMailbox(readCtx, sessionID, args.AfterSeq, limit+1)
			if err != nil {
				if waitMs > 0 && f.shouldSoftRetry(readCtx, err) {
					return softTimeout()
				}
				return nil, err
			}
			if ok {
				hasMore := len(events) > limit
				if hasMore {
					events = events[:limit]
				}
				if len(events) > 0 || (hasMailboxRows && waitMs == 0) {
					result := f.build(sessionID, events)
					if hasMore {
						return ApplyAgentEventsPagination(result, true, 0), nil
					}
					return result, nil
				}
				if hasMailboxRows {
					if timeout := waitForAgentEventsTick(readCtx, wakeCh); timeout {
						return softTimeout()
					}
					continue
				}
			}
		}
		events, err := f.List(readCtx, sessionID, args.AfterSeq, limit+1)
		if err != nil {
			if waitMs > 0 && f.shouldSoftRetry(readCtx, err) {
				return softTimeout()
			}
			return nil, err
		}
		if args.MailboxOnly {
			events = f.filterMailbox(events)
		}
		hasMore := len(events) > limit
		if hasMore {
			events = events[:limit]
		}
		if len(events) > 0 || waitMs == 0 {
			result := f.build(sessionID, events)
			if hasMore {
				return ApplyAgentEventsPagination(result, true, f.unread(ctx, sessionID, result.LatestSeq)), nil
			}
			return result, nil
		}
		if timeout := waitForAgentEventsTick(readCtx, wakeCh); timeout {
			return softTimeout()
		}
	}
}

func (f AgentEventsFlow) softTimeout(ctx context.Context, sessionID string, afterSeq int64, limit int, mailboxOnly bool) (*AgentEventsResult, error) {
	parentCtx := ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	if mailboxOnly && f.ListMailbox != nil {
		if events, ok, _, err := f.ListMailbox(parentCtx, sessionID, afterSeq, limit); err == nil && ok && len(events) > 0 {
			return f.build(sessionID, events), nil
		}
	}
	if events, err := f.List(parentCtx, sessionID, afterSeq, limit); err == nil {
		if mailboxOnly {
			events = f.filterMailbox(events)
		}
		if len(events) > 0 {
			return f.build(sessionID, events), nil
		}
	}
	return BuildAgentEventsTimeoutResult(sessionID, nil), nil
}

func (f AgentEventsFlow) build(sessionID string, events []runtimeevents.Event) *AgentEventsResult {
	if f.Build != nil {
		return f.Build(sessionID, events)
	}
	return BuildAgentEventsResult(sessionID, events)
}

func (f AgentEventsFlow) filterMailbox(events []runtimeevents.Event) []runtimeevents.Event {
	if f.FilterMailbox == nil {
		return events
	}
	return f.FilterMailbox(events)
}

func (f AgentEventsFlow) unread(ctx context.Context, sessionID string, afterSeq int64) int {
	if f.Unread == nil {
		return 0
	}
	return f.Unread(ctx, sessionID, afterSeq)
}

func (f AgentEventsFlow) shouldSoftRetry(ctx context.Context, err error) bool {
	if f.ShouldSoftRetry != nil {
		return f.ShouldSoftRetry(ctx, err)
	}
	return IsAgentEventsWaitInterrupted(ctx, err)
}

func waitForAgentEventsTick(ctx context.Context, wakeCh <-chan struct{}) bool {
	select {
	case <-ctx.Done():
		return true
	case <-wakeCh:
		return false
	case <-time.After(500 * time.Millisecond):
		return false
	}
}

// IsAgentEventsWaitInterrupted identifies the expected cancellation/lock
// interruption shape of a bounded read window. It is fail-open: an interrupted
// wait returns a soft-timeout snapshot instead of surfacing a storage error.
func IsAgentEventsWaitInterrupted(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && (errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(message, "sqlite3: interrupted") ||
		strings.Contains(message, "database operation interrupted") {
		return true
	}
	return team.IsSQLiteLockError(err)
}
