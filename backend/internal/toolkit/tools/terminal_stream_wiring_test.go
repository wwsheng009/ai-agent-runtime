package tools

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
)

func TestResolveToolTerminalOutputMirror_TeesChatAndProgress(t *testing.T) {
	var mu sync.Mutex
	var reports []toolprotocol.Progress
	ctx := toolprotocol.WithReporter(context.Background(), toolprotocol.ReporterFunc(func(p toolprotocol.Progress) {
		mu.Lock()
		defer mu.Unlock()
		reports = append(reports, p)
	}))

	var chat strings.Builder
	ctx = runtimeexecutor.WithOutputMirror(ctx, &chat)
	mirror := resolveToolTerminalOutputMirror(ctx)
	if mirror == nil {
		t.Fatal("expected non-nil mirror when reporter is bound")
	}
	_, _ = io.WriteString(mirror, "live-line-1\n")
	if flusher, ok := mirror.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
	}

	if chat.String() != "live-line-1\n" {
		t.Fatalf("chat mirror = %q", chat.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 1 {
		t.Fatalf("expected 1 stream progress event, got %d: %+v", len(reports), reports)
	}
	if reports[0].Metadata[toolprotocol.MetadataStream] != true {
		t.Fatalf("expected stream metadata, got %+v", reports[0].Metadata)
	}
	if reports[0].Metadata[toolprotocol.MetadataOutputMirrored] != true {
		t.Fatalf("expected output_mirrored metadata, got %+v", reports[0].Metadata)
	}
	if !strings.Contains(reports[0].Partial, "live-line-1") {
		t.Fatalf("partial=%q", reports[0].Partial)
	}
}

func TestDefaultCommandExecuter_EmitsTerminalStreamProgress(t *testing.T) {
	var mu sync.Mutex
	var reports []toolprotocol.Progress
	ctx := toolprotocol.WithReporter(context.Background(), toolprotocol.ReporterFunc(func(p toolprotocol.Progress) {
		mu.Lock()
		defer mu.Unlock()
		reports = append(reports, p)
	}))

	executer := &DefaultCommandExecuter{}
	result, err := executer.Execute(ctx, "echo terminal-stream-ok", 10*time.Second)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result.Output, "terminal-stream-ok") {
		t.Fatalf("output=%q", result.Output)
	}

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, p := range reports {
		if p.Metadata[toolprotocol.MetadataStream] == true && strings.Contains(p.Partial, "terminal-stream-ok") {
			if p.Metadata[toolprotocol.MetadataOutputMirrored] == true {
				t.Fatalf("unexpected output_mirrored metadata without an existing mirror: %+v", p.Metadata)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected stream progress with command output, got %+v", reports)
	}
}

func TestResolveToolTerminalOutputMirror_NoReporterLeavesExisting(t *testing.T) {
	var chat strings.Builder
	ctx := runtimeexecutor.WithOutputMirror(context.Background(), &chat)
	mirror := resolveToolTerminalOutputMirror(ctx)
	if mirror != &chat {
		// MultiFlushWriter may wrap only one writer and return it directly.
		_, _ = io.WriteString(mirror, "x")
		if chat.String() != "x" {
			t.Fatalf("expected existing chat mirror to receive writes, chat=%q", chat.String())
		}
	}
}
