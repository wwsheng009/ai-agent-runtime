package subagentbatch

import (
	"errors"
	"testing"
)

// The state graph is the single source of truth for monotonic transitions
// (plan §4.3). These tests pin the allowed batch/task transitions and the
// terminal-state invariant: once a batch or task is terminal it never moves.

func TestValidateBatchTransition(t *testing.T) {
	allowed := []struct {
		from, to BatchStatus
	}{
		{BatchQueued, BatchQueued},
		{BatchQueued, BatchRunning},
		{BatchQueued, BatchPartiallyCompleted},
		{BatchQueued, BatchCanceled},
		{BatchQueued, BatchTimedOut},
		{BatchQueued, BatchOrphaned},
		{BatchQueued, BatchFailed},

		{BatchRunning, BatchRunning},
		{BatchRunning, BatchPartiallyCompleted},
		{BatchRunning, BatchCompleted},
		{BatchRunning, BatchFailed},
		{BatchRunning, BatchCanceled},
		{BatchRunning, BatchTimedOut},
		{BatchRunning, BatchOrphaned},

		{BatchPartiallyCompleted, BatchPartiallyCompleted},
		{BatchPartiallyCompleted, BatchCompleted},
		{BatchPartiallyCompleted, BatchFailed},
		{BatchPartiallyCompleted, BatchCanceled},
		{BatchPartiallyCompleted, BatchTimedOut},
		{BatchPartiallyCompleted, BatchOrphaned},
	}
	for _, tt := range allowed {
		if err := ValidateBatchTransition(tt.from, tt.to); err != nil {
			t.Errorf("ValidateBatchTransition(%s -> %s) = %v, want nil", tt.from, tt.to, err)
		}
	}

	forbidden := []struct {
		from, to BatchStatus
	}{
		{BatchCompleted, BatchRunning},
		{BatchFailed, BatchCompleted},
		{BatchCanceled, BatchFailed},
		{BatchTimedOut, BatchPartiallyCompleted},
		{BatchOrphaned, BatchRunning},
		{BatchQueued, BatchCompleted}, // queued must pass through running
		{BatchStatus("bogus"), BatchRunning},
	}
	for _, tt := range forbidden {
		if err := ValidateBatchTransition(tt.from, tt.to); err == nil {
			t.Errorf("ValidateBatchTransition(%s -> %s) = nil, want error", tt.from, tt.to)
		}
	}
}

func TestValidateTaskTransition(t *testing.T) {
	allowed := []struct {
		from, to TaskStatus
	}{
		{TaskPending, TaskPending},
		{TaskPending, TaskReady},
		{TaskPending, TaskRunning},
		{TaskPending, TaskCanceled},
		{TaskPending, TaskTimedOut},
		{TaskPending, TaskSkipped},

		{TaskReady, TaskReady},
		{TaskReady, TaskRunning},
		{TaskReady, TaskCanceled},
		{TaskReady, TaskSkipped},
		{TaskReady, TaskTimedOut},

		{TaskRunning, TaskRunning},
		{TaskRunning, TaskSucceeded},
		{TaskRunning, TaskFailed},
		{TaskRunning, TaskCanceled},
		{TaskRunning, TaskTimedOut},
	}
	for _, tt := range allowed {
		if err := ValidateTaskTransition(tt.from, tt.to); err != nil {
			t.Errorf("ValidateTaskTransition(%s -> %s) = %v, want nil", tt.from, tt.to, err)
		}
	}

	forbidden := []struct {
		from, to TaskStatus
	}{
		{TaskSucceeded, TaskRunning},
		{TaskFailed, TaskSucceeded},
		{TaskCanceled, TaskRunning},
		{TaskTimedOut, TaskSucceeded},
		{TaskSkipped, TaskRunning},
		{TaskPending, TaskSucceeded}, // must be ready/running first
	}
	for _, tt := range forbidden {
		if err := ValidateTaskTransition(tt.from, tt.to); err == nil {
			t.Errorf("ValidateTaskTransition(%s -> %s) = nil, want error", tt.from, tt.to)
		}
	}
}

func TestTerminalStatesAreAbsorbing(t *testing.T) {
	for _, status := range []BatchStatus{BatchCompleted, BatchFailed, BatchCanceled, BatchTimedOut, BatchOrphaned} {
		if !status.Terminal() {
			t.Errorf("%s should be terminal", status)
		}
		for _, other := range []BatchStatus{BatchQueued, BatchRunning, BatchPartiallyCompleted, BatchCompleted, BatchFailed, BatchCanceled, BatchTimedOut, BatchOrphaned} {
			if other != status {
				if !isTerminalForbidden(status, other) {
					t.Errorf("terminal %s -> %s should be forbidden", status, other)
				}
			}
		}
	}
	for _, status := range []BatchStatus{BatchQueued, BatchRunning, BatchPartiallyCompleted} {
		if status.Terminal() {
			t.Errorf("%s should not be terminal", status)
		}
	}
}

// isTerminalForbidden mirrors the graph check for the terminal-absorbing test.
func isTerminalForbidden(from, to BatchStatus) bool {
	allowed, ok := batchTransitions[from]
	if !ok {
		return true
	}
	return !allowed[to]
}

func TestCanonicalErrorClass(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{errors.New("context deadline exceeded"), "timeout"},
		{errors.New("operation timed out after 30s"), "timeout"},
		{errors.New("canceled by parent turn"), "canceled"},
		{errors.New("context canceled"), "canceled"},
		{errors.New("nesting depth limit reached"), "depth_limit"},
		{errors.New("policy denied this tool call"), "policy"},
		{errors.New("api provider rate limited"), "provider"},
		{errors.New("mcp server connection refused"), "mcp"},
		{errors.New("dial tcp: no such host"), "network"},
		{errors.New("something else broke"), "error"},
	}
	for _, tt := range tests {
		if got := CanonicalErrorClass(tt.err); got != tt.want {
			t.Errorf("CanonicalErrorClass(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}
