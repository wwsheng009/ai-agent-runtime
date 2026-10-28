package envdclient

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestLiveWatchDir captures a real WatchDir event so its schema can be confirmed
// against a live envd instead of guessed. Gated like the other live tests.
func TestLiveWatchDir(t *testing.T) {
	client := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	dir := fmt.Sprintf("/tmp/envd-watch-%d", time.Now().UnixNano())
	if err := client.MakeDir(ctx, dir); err != nil {
		t.Fatalf("MakeDir: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelCleanup()
		_ = client.Remove(cleanup, dir)
	})

	stream, err := client.WatchDir(ctx, dir)
	if err != nil {
		t.Fatalf("WatchDir: %v", err)
	}
	defer func() { _ = stream.Close() }()

	events := make(chan WatchEvent, 8)
	errs := make(chan error, 1)
	go func() {
		for {
			var event WatchEvent
			if err := stream.Recv(&event); err != nil {
				errs <- err
				return
			}
			events <- event
		}
	}()

	// Give the watcher a moment to attach before changing the directory.
	time.Sleep(300 * time.Millisecond)
	_, stderr, code, err := client.Run(ctx, StartRequest{Process: ProcessConfig{
		Cmd:  "/bin/sh",
		Args: []string{"-c", fmt.Sprintf("printf hi > %s/probe.txt", dir)},
	}})
	if err != nil || code != 0 {
		t.Fatalf("trigger write: err=%v code=%d stderr=%s", err, code, strings.TrimSpace(string(stderr)))
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case event := <-events:
			t.Logf("raw event: %s", string(event.Raw))
			if !event.Start {
				if event.Name != "probe.txt" {
					t.Fatalf("event name = %q, want probe.txt", event.Name)
				}
				if event.Type == "" {
					t.Fatal("event type is empty")
				}
				return
			}
		case err := <-errs:
			t.Fatalf("watch stream ended before a change event: %v", err)
		case <-deadline:
			t.Fatal("no change event within 10s (only the start frame was observed)")
		}
	}
}
