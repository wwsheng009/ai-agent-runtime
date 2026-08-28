package agent

import (
	"context"
	stderrors "errors"
	"testing"
	"time"
)

type compatContextKey struct{}

type delayedDeadlineContext struct {
	context.Context
	deadline time.Time
	delay    time.Duration
}

func (c delayedDeadlineContext) Deadline() (time.Time, bool) {
	time.Sleep(c.delay)
	return c.deadline, true
}

func TestAgentWithoutCancelDetachesCancellationAndPreservesValues(t *testing.T) {
	parent, cancelParent := context.WithCancel(
		context.WithValue(context.Background(), compatContextKey{}, "trace-value"),
	)
	detached := agentWithoutCancel(parent)
	cancelParent()

	if detached.Done() != nil || detached.Err() != nil {
		t.Fatalf("detached context was canceled: done=%v err=%v", detached.Done(), detached.Err())
	}
	if got := detached.Value(compatContextKey{}); got != "trace-value" {
		t.Fatalf("detached value = %v; want trace-value", got)
	}
	if cause := agentContextCause(detached); cause != nil {
		t.Fatalf("detached cause = %v; want nil", cause)
	}
	if cause := context.Cause(detached); cause != nil {
		t.Fatalf("standard context.Cause(detached) = %v; want nil", cause)
	}
	wrapped := context.WithValue(detached, struct{ name string }{"wrapped"}, true)
	if cause := context.Cause(wrapped); cause != nil {
		t.Fatalf("standard context.Cause(wrapped detached) = %v; want nil", cause)
	}
}

func TestAgentWithTimeoutCauseUsesStandardErrAndCustomCause(t *testing.T) {
	timeoutCause := stderrors.New("child run timeout")
	child, cancelChild := agentWithTimeoutCause(context.Background(), 10*time.Millisecond, timeoutCause)
	defer cancelChild()

	waitForCompatContextDone(t, child)
	if child.Err() != context.DeadlineExceeded {
		t.Fatalf("child Err() = %#v; want context.DeadlineExceeded", child.Err())
	}
	if !stderrors.Is(agentContextCause(child), timeoutCause) {
		t.Fatalf("child cause = %v; want %v", agentContextCause(child), timeoutCause)
	}
	if got := context.Cause(child); got != timeoutCause {
		t.Fatalf("standard context.Cause(child) = %#v; want supplied cause %#v", got, timeoutCause)
	}
}

func TestAgentWithTimeoutCauseInteroperatesWithStandardWrappers(t *testing.T) {
	timeoutCause := stderrors.New("wrapped child timeout")
	child, cancelChild := agentWithTimeoutCause(context.Background(), 10*time.Millisecond, timeoutCause)
	defer cancelChild()
	withValue := context.WithValue(child, struct{ name string }{"wrapper"}, "value")
	descendant, cancelDescendant := context.WithCancel(child)
	defer cancelDescendant()

	waitForCompatContextDone(t, child)
	waitForCompatContextDone(t, descendant)
	if got := context.Cause(withValue); got != timeoutCause {
		t.Fatalf("context.Cause(WithValue(child)) = %#v; want %#v", got, timeoutCause)
	}
	if descendant.Err() != context.DeadlineExceeded {
		t.Fatalf("descendant Err() = %#v; want context.DeadlineExceeded", descendant.Err())
	}
	if got := context.Cause(descendant); got != timeoutCause {
		t.Fatalf("descendant cause = %#v; want %#v", got, timeoutCause)
	}
}

func TestAgentWithTimeoutCauseUsesAdvertisedDeadlineAfterSlowParentLookup(t *testing.T) {
	parent := delayedDeadlineContext{
		Context:  context.Background(),
		deadline: time.Now().Add(time.Hour),
		delay:    30 * time.Millisecond,
	}
	child, cancelChild := agentWithTimeoutCause(parent, 5*time.Millisecond, stderrors.New("short timeout"))
	defer cancelChild()

	select {
	case <-child.Done():
	default:
		t.Fatal("child remained live after its advertised deadline elapsed during parent lookup")
	}
	if child.Err() != context.DeadlineExceeded {
		t.Fatalf("child Err() = %#v; want context.DeadlineExceeded", child.Err())
	}
}

func TestAgentWithTimeoutCauseNilCauseUsesDeadlineExceeded(t *testing.T) {
	child, cancelChild := agentWithTimeoutCause(context.Background(), 0, nil)
	defer cancelChild()
	waitForCompatContextDone(t, child)

	if child.Err() != context.DeadlineExceeded {
		t.Fatalf("child Err() = %#v; want context.DeadlineExceeded", child.Err())
	}
	if cause := agentContextCause(child); cause != context.DeadlineExceeded {
		t.Fatalf("child cause = %#v; want context.DeadlineExceeded", cause)
	}
}

func TestAgentWithTimeoutCausePropagatesEarlierParentDeadline(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancelParent()
	parentDeadline, ok := parent.Deadline()
	if !ok {
		t.Fatal("parent context did not expose its deadline")
	}

	ownCause := stderrors.New("child run timeout")
	child, cancelChild := agentWithTimeoutCause(parent, time.Minute, ownCause)
	defer cancelChild()
	childDeadline, ok := child.Deadline()
	if !ok || !childDeadline.Equal(parentDeadline) {
		t.Fatalf("child deadline = %v, %v; want inherited %v", childDeadline, ok, parentDeadline)
	}

	waitForCompatContextDone(t, child)
	if !stderrors.Is(child.Err(), context.DeadlineExceeded) {
		t.Fatalf("child Err() = %v; want parent deadline exceeded", child.Err())
	}
	if stderrors.Is(agentContextCause(child), ownCause) {
		t.Fatalf("parent deadline was misclassified as child timeout cause: %v", agentContextCause(child))
	}
	if !stderrors.Is(agentContextCause(child), context.DeadlineExceeded) {
		t.Fatalf("child cause = %v; want parent deadline exceeded", agentContextCause(child))
	}
}

func TestAgentWithTimeoutCausePropagatesParentCause(t *testing.T) {
	parentCause := stderrors.New("parent timeout cause")
	parent, cancelParent := agentWithTimeoutCause(context.Background(), 20*time.Millisecond, parentCause)
	defer cancelParent()

	childCause := stderrors.New("child timeout cause")
	child, cancelChild := agentWithTimeoutCause(parent, time.Minute, childCause)
	defer cancelChild()

	waitForCompatContextDone(t, child)
	if !stderrors.Is(child.Err(), context.DeadlineExceeded) {
		t.Fatalf("child Err() = %v; want deadline exceeded", child.Err())
	}
	if !stderrors.Is(agentContextCause(child), parentCause) {
		t.Fatalf("child cause = %v; want inherited parent cause", agentContextCause(child))
	}
	if stderrors.Is(agentContextCause(child), childCause) {
		t.Fatalf("parent timeout was misclassified as child timeout cause: %v", agentContextCause(child))
	}
}

func TestAgentWithTimeoutCausePropagatesParentCancellation(t *testing.T) {
	parentCause := stderrors.New("parent canceled")
	parent, cancelParent := context.WithCancelCause(context.Background())
	child, cancelChild := agentWithTimeoutCause(parent, time.Minute, stderrors.New("child timeout cause"))
	defer cancelChild()

	cancelParent(parentCause)
	waitForCompatContextDone(t, child)
	if !stderrors.Is(child.Err(), context.Canceled) {
		t.Fatalf("child Err() = %v; want canceled", child.Err())
	}
	if !stderrors.Is(agentContextCause(child), parentCause) {
		t.Fatalf("child cause = %v; want %v", agentContextCause(child), parentCause)
	}
}

func TestAgentWithTimeoutCauseManualCancelDoesNotUseTimeoutCause(t *testing.T) {
	timeoutCause := stderrors.New("child timeout cause")
	child, cancelChild := agentWithTimeoutCause(context.Background(), time.Minute, timeoutCause)
	cancelChild()

	waitForCompatContextDone(t, child)
	if child.Err() != context.Canceled {
		t.Fatalf("child Err() = %#v; want context.Canceled", child.Err())
	}
	if cause := agentContextCause(child); cause != context.Canceled {
		t.Fatalf("child cause = %#v; want context.Canceled", cause)
	}
	if stderrors.Is(agentContextCause(child), timeoutCause) {
		t.Fatalf("manual cancellation incorrectly used timeout cause: %v", agentContextCause(child))
	}
}

func waitForCompatContextDone(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("context did not finish after its parent")
	}
}
