package shelljob

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeRuntime struct {
	jobID     string
	wait      Snapshot
	waitErr   error
	output    Output
	outputErr error
	forgotten bool
	cancelled bool
}

func (f *fakeRuntime) SubmitShell(context.Context, string, string, string) (string, string, error) {
	return f.jobID, "running", nil
}

func (f *fakeRuntime) WaitShell(context.Context, string, time.Duration) (Snapshot, error) {
	return f.wait, f.waitErr
}

func (f *fakeRuntime) ReadShell(context.Context, string, int64, int) (Output, error) {
	return f.output, f.outputErr
}

func (f *fakeRuntime) CancelShell(context.Context, string) error {
	f.cancelled = true
	return nil
}

func (f *fakeRuntime) ForgetShell(context.Context, string, string) error {
	f.forgotten = true
	return nil
}

func TestExecuteSettledForgetsQuickJob(t *testing.T) {
	runtime := &fakeRuntime{
		jobID: "job_fast",
		wait:  Snapshot{ID: "job_fast", Status: "completed"},
		output: Output{
			Output:     "done\n",
			NextOffset: 5,
			Status:     "completed",
		},
	}
	execution, err := Execute(context.Background(), runtime, Request{
		SessionID:   "session-a",
		Command:     "echo done",
		WaitTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if execution.Promoted || execution.Output != "done\n" || execution.NextOffset != 5 {
		t.Fatalf("unexpected execution: %+v", execution)
	}
	if !runtime.forgotten {
		t.Fatal("settled quick job was not forgotten")
	}
}

func TestExecuteTimeoutKeepsPromotedJob(t *testing.T) {
	runtime := &fakeRuntime{
		jobID: "job_slow",
		wait:  Snapshot{ID: "job_slow", Status: "running", TimedOut: true},
		output: Output{
			Output:     "early\n",
			NextOffset: 6,
			Status:     "running",
		},
	}
	execution, err := Execute(context.Background(), runtime, Request{
		SessionID:   "session-a",
		Command:     "long command",
		WaitTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !execution.Promoted || !execution.TimedOut || execution.JobID != "job_slow" {
		t.Fatalf("unexpected promoted execution: %+v", execution)
	}
	if runtime.forgotten {
		t.Fatal("promoted job must remain visible")
	}
}

func TestExecuteWaitErrorCancelsJob(t *testing.T) {
	runtime := &fakeRuntime{
		jobID:   "job_wait",
		waitErr: errors.New("client disconnected"),
	}
	_, err := Execute(context.Background(), runtime, Request{
		SessionID: "session-a",
		Command:   "long command",
	})
	if err == nil || StageOf(err) != "wait" {
		t.Fatalf("expected wait stage error, got %v", err)
	}
	if !runtime.cancelled {
		t.Fatal("failed wait left the job running")
	}
}

func TestExecuteRequiresOwner(t *testing.T) {
	_, err := Execute(context.Background(), &fakeRuntime{}, Request{Command: "echo hi"})
	if err == nil || StageOf(err) != "owner" {
		t.Fatalf("expected owner stage error, got %v", err)
	}
}
