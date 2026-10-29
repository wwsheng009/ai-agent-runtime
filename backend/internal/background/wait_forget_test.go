package background

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWaitJobReturnsTerminalSnapshot(t *testing.T) {
	manager := NewManager(Config{})
	t.Cleanup(func() { _ = manager.Close() })

	job, err := manager.SubmitShell(context.Background(), "session-wait", BackgroundTaskArgs{
		Command: "echo wait-terminal-marker",
	})
	if err != nil {
		t.Fatalf("SubmitShell: %v", err)
	}
	view, timedOut, err := manager.WaitJob(context.Background(), job.ID, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitJob: %v", err)
	}
	if timedOut {
		t.Fatalf("WaitJob timed out for short command: %+v", view)
	}
	if view == nil || view.Status != StatusCompleted {
		t.Fatalf("terminal view = %+v, want completed", view)
	}
	output, err := manager.ReadOutput(context.Background(), TaskOutputArgs{
		JobID: job.ID,
		Limit: 4096,
	})
	if err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	if !strings.Contains(output.Output, "wait-terminal-marker") {
		t.Fatalf("job output missing marker: %q", output.Output)
	}
}

func TestWaitJobTimeoutLeavesJobRunning(t *testing.T) {
	manager := &Manager{jobs: map[string]*managedJob{}}
	managed := &managedJob{
		info: Job{
			ID:        "job_wait_timeout",
			SessionID: "session-wait",
			Status:    StatusRunning,
		},
		output: newOutputBuffer(1024),
	}
	manager.jobs[managed.info.ID] = managed

	view, timedOut, err := manager.WaitJob(context.Background(), managed.info.ID, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitJob: %v", err)
	}
	if !timedOut || view == nil || view.Status != StatusRunning {
		t.Fatalf("timeout view = %+v timedOut=%v, want running", view, timedOut)
	}
}

func TestForgetJobRemovesTerminalJobAndChecksOwner(t *testing.T) {
	manager := &Manager{jobs: map[string]*managedJob{}}
	finished := time.Now().UTC()
	managed := &managedJob{
		info: Job{
			ID:         "job_forget",
			SessionID:  "owner-a",
			Status:     StatusCompleted,
			FinishedAt: &finished,
		},
		output: newOutputBuffer(1024),
	}
	manager.jobs[managed.info.ID] = managed

	if err := manager.ForgetJob(context.Background(), managed.info.ID, "owner-b"); err == nil {
		t.Fatal("ForgetJob accepted a foreign owner")
	}
	if _, ok := manager.jobs[managed.info.ID]; !ok {
		t.Fatal("foreign-owner refusal removed the job")
	}
	if err := manager.ForgetJob(context.Background(), managed.info.ID, "owner-a"); err != nil {
		t.Fatalf("ForgetJob: %v", err)
	}
	if _, err := manager.GetJob(context.Background(), managed.info.ID); err == nil {
		t.Fatal("job still exists after ForgetJob")
	}
}

func TestForgetJobDeletesPersistedTerminalJob(t *testing.T) {
	manager := NewManager(Config{
		StorePath: filepath.Join(t.TempDir(), "jobs.db"),
	})
	t.Cleanup(func() { _ = manager.Close() })

	job, err := manager.SubmitShell(context.Background(), "session-store-forget", BackgroundTaskArgs{
		Command: "echo forget-store-marker",
	})
	if err != nil {
		t.Fatalf("SubmitShell: %v", err)
	}
	if _, _, err := manager.WaitJob(context.Background(), job.ID, 5*time.Second); err != nil {
		t.Fatalf("WaitJob: %v", err)
	}
	if err := manager.ForgetJob(context.Background(), job.ID, "session-store-forget"); err != nil {
		t.Fatalf("ForgetJob: %v", err)
	}
	if _, err := manager.GetJob(context.Background(), job.ID); err == nil {
		t.Fatal("persisted job still exists after ForgetJob")
	}
}
