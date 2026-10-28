package envdclient

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestLiveStdinRoundTrip proves SendInput works against a real envd. SendInput is
// a unary RPC, so it works over HTTP/1.1 and HTTP/2.
func TestLiveStdinRoundTrip(t *testing.T) {
	client := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	stream, err := client.Start(ctx, StartRequest{
		Process: ProcessConfig{
			Cmd:  "/bin/sh",
			Args: []string{"-c", `read -r line; printf 'got:%s\n' "$line"`},
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = stream.Close() }()
	var event ProcessEvent
	var pid uint32
	for pid == 0 {
		if err := stream.Recv(&event); err != nil {
			t.Fatalf("Recv(start): %v", err)
		}
		switch event.Kind {
		case EventStart:
			if event.Start != nil {
				pid = event.Start.Pid
			}
		case EventEnd:
			t.Fatal("process ended before the start event")
		}
	}

	if err := client.SendInput(ctx, pid, []byte("hello\n")); err != nil {
		t.Fatalf("SendInput: %v", err)
	}

	var out strings.Builder
	deadline := time.After(10 * time.Second)
	for !strings.Contains(out.String(), "got:hello") {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for output; got %q", out.String())
		default:
		}
		if err := stream.Recv(&event); err != nil {
			t.Fatalf("Recv: %v (output %q)", err, out.String())
		}
		switch event.Kind {
		case EventData:
			out.Write(event.Stdout)
		case EventEnd:
			t.Fatalf("process ended before the expected output; got %q", out.String())
		}
	}
}

// TestLiveSignalTermination proves SendSignal reaches a real process.
func TestLiveSignalTermination(t *testing.T) {
	client := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	stream, err := client.Start(ctx, StartRequest{
		Process: ProcessConfig{Cmd: "/bin/sleep", Args: []string{"30"}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = stream.Close() }()
	var event ProcessEvent
	var pid uint32
	for pid == 0 {
		if err := stream.Recv(&event); err != nil {
			t.Fatalf("Recv(start): %v", err)
		}
		if event.Kind == EventStart && event.Start != nil {
			pid = event.Start.Pid
		}
		if event.Kind == EventEnd {
			t.Fatal("process ended before the start event")
		}
	}

	if err := client.SendSignal(ctx, pid, "SIGTERM"); err != nil {
		t.Fatalf("SendSignal: %v", err)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for the process to end")
		default:
		}
		if err := stream.Recv(&event); err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if event.Kind != EventEnd {
			continue
		}
		if event.End == nil {
			t.Fatal("end event is nil")
		}
		if code := event.End.ExitStatus(); code < 128 && !strings.Contains(event.End.Status, "signal") {
			t.Fatalf("exit status = %d (%q), want a signal termination", code, event.End.Status)
		}
		return
	}
}

// TestLivePTYRoundTrip starts a shell under a pty, proves pty output decodes, and
// resizes the terminal. This is the interactive path the first implementation
// wrongly believed required HTTP/2 client-streaming.
func TestLivePTYRoundTrip(t *testing.T) {
	client := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	stream, err := client.Start(ctx, StartRequest{
		Process: ProcessConfig{Cmd: "/bin/sh"},
		Pty:     &PTY{Size: &PTYSize{Cols: 80, Rows: 24}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var event ProcessEvent
	var pid uint32
	for pid == 0 {
		if err := stream.Recv(&event); err != nil {
			t.Fatalf("Recv(start): %v", err)
		}
		if event.Kind == EventStart && event.Start != nil {
			pid = event.Start.Pid
		}
		if event.Kind == EventEnd {
			t.Fatal("process ended before the start event")
		}
	}

	if err := client.SendPtyInput(ctx, pid, []byte("printf 'pty-%s\\n' ok\n")); err != nil {
		t.Fatalf("SendPtyInput: %v", err)
	}

	var output strings.Builder
	deadline := time.After(10 * time.Second)
	for !strings.Contains(output.String(), "pty-ok") {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for pty output; got %q", output.String())
		default:
		}
		if err := stream.Recv(&event); err != nil {
			t.Fatalf("Recv: %v (output %q)", err, output.String())
		}
		if event.Kind == EventData {
			output.Write(event.Pty)
		}
		if event.Kind == EventEnd {
			t.Fatalf("process ended before pty output; got %q", output.String())
		}
	}

	if err := client.UpdatePty(ctx, pid, 120, 40); err != nil {
		t.Fatalf("UpdatePty: %v", err)
	}
	if err := client.SendPtyInput(ctx, pid, []byte("exit\n")); err != nil {
		t.Fatalf("SendPtyInput(exit): %v", err)
	}
}
