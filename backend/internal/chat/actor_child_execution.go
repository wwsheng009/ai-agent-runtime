package chat

import (
	"context"
	"errors"

	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// errSessionRunFinished distinguishes disposal of a completed/parked execution
// context from cancellation of the work it delegated. It is not a run failure.
var errSessionRunFinished = errors.New("session_run_finished")

// SubmitChildPromptAsync queues a delegated execution that survives clean
// completion (including parking) of its parent run. Unlike a blanket
// WithoutCancel, explicit parent cancellation, failure and deadlines still
// propagate while that parent is running. The child's own Interrupt/Stop and
// execution limits remain authoritative after its parent has finished.
func (a *SessionActor) SubmitChildPromptAsync(ctx context.Context, prompt string, runMeta *team.RunMeta, opts ...SubmitPromptOption) error {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, release := childExecutionContext(ctx)
	return a.submitPromptAsync(ctx, runCtx, release, prompt, runMeta, opts...)
}

// The bridge drops the parent's deadline timer, not its cancellation policy:
// a deadline that actually terminates the active parent still cancels the
// child. Once the parent finishes cleanly, only the child's own limits apply.
// Context values (routing, permission boundaries, tracing) are preserved.
func childExecutionContext(parent context.Context) (context.Context, func()) {
	runCtx, cancel := context.WithCancelCause(context.WithoutCancel(parent))
	stop := context.AfterFunc(parent, func() {
		cause := context.Cause(parent)
		if !errors.Is(cause, errSessionRunFinished) {
			cancel(cause)
		}
	})
	return runCtx, func() {
		stop()
		cancel(context.Canceled)
	}
}
