package commands

import (
	"strings"
	"sync"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

type recordingChatTitleOutput struct {
	mu        sync.Mutex
	supported bool
	titles    []string
	clears    int
}

func (o *recordingChatTitleOutput) Supported() bool { return o != nil && o.supported }

func (o *recordingChatTitleOutput) Set(title string) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.titles = append(o.titles, title)
	return true, nil
}

func (o *recordingChatTitleOutput) Clear() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.clears++
	return nil
}

func TestResolveChatTitleOptions(t *testing.T) {
	defaults := resolveChatTitleOptions(nil)
	if !defaults.enabled || !defaults.animations {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
	if got := strings.Join(defaults.items, ","); got != "activity,state,project" {
		t.Fatalf("unexpected default items: %q", got)
	}

	disabled := false
	cfg := &config.Config{AICLI: &config.AICLIConfig{Chat: &config.AICLIChatConfig{
		TerminalTitle: &config.AICLITerminalTitleConfig{
			Enabled:    &disabled,
			Animations: &disabled,
			Items:      []string{" spinner ", "STATUS", "project-name", "thread-title", "status", "unknown"},
		},
	}}}
	options := resolveChatTitleOptions(cfg)
	if options.enabled || options.animations {
		t.Fatalf("expected disabled flags, got %+v", options)
	}
	if got := strings.Join(options.items, ","); got != "activity,state,project,thread" {
		t.Fatalf("unexpected normalized items: %q", got)
	}
}

func TestRenderChatTerminalTitleStates(t *testing.T) {
	snapshot := chatTitleSnapshot{
		baseState:  chatTitleIdle,
		project:    "runtime",
		animations: true,
		items:      []string{"activity", "state", "project"},
		tools:      make(map[string]struct{}),
		actions:    make(map[uint64]string),
	}
	if got := renderChatTerminalTitle(snapshot, 0); got != "○ Ready | runtime" {
		t.Fatalf("idle title = %q", got)
	}
	snapshot.baseState = chatTitleWaiting
	if got := renderChatTerminalTitle(snapshot, 0); got != "⠋ Waiting | runtime" {
		t.Fatalf("waiting title = %q", got)
	}
	snapshot.tools["tool-1"] = struct{}{}
	if got := renderChatTerminalTitle(snapshot, 0); got != "◐ Running | runtime" {
		t.Fatalf("running title = %q", got)
	}
	snapshot.actions[1] = "Approval Required"
	snapshot.latestID = 1
	if got := renderChatTerminalTitle(snapshot, 0); got != "[ ! ] Approval Required | runtime" {
		t.Fatalf("action title = %q", got)
	}
	if got := renderChatTerminalTitle(snapshot, 1); got != "[ . ] Approval Required | runtime" {
		t.Fatalf("animated action title = %q", got)
	}
	snapshot.animations = false
	if got := renderChatTerminalTitle(snapshot, 1); got != "[ ! ] Approval Required | runtime" {
		t.Fatalf("static action title = %q", got)
	}
}

func TestChatTitleNotifierPreservesOverlappingActivity(t *testing.T) {
	output := &recordingChatTitleOutput{supported: true}
	notifier := newChatTitleNotifier(output, chatTitleOptions{
		enabled: true, animations: false, items: []string{"activity", "state"},
	}, nil)
	if notifier == nil {
		t.Fatal("expected notifier")
	}

	notifier.SetBaseState(chatTitleWaiting)
	notifier.SetToolRunning("tool-1", true)
	notifier.SetToolRunning("tool-2", true)
	notifier.SetToolRunning("tool-1", false)
	if got := notifier.currentSnapshot().effectiveState(); got != chatTitleRunning {
		t.Fatalf("one remaining tool should keep running, got %v", got)
	}

	endAction := notifier.BeginActionRequired("Input Required")
	if got := notifier.currentSnapshot().effectiveState(); got != chatTitleActionRequired {
		t.Fatalf("action should override tools, got %v", got)
	}
	endAction()
	if got := notifier.currentSnapshot().effectiveState(); got != chatTitleRunning {
		t.Fatalf("ending action should restore running, got %v", got)
	}
	notifier.SetToolRunning("tool-2", false)
	if got := notifier.currentSnapshot().effectiveState(); got != chatTitleWaiting {
		t.Fatalf("ending all tools should restore waiting, got %v", got)
	}
	notifier.SetToolRunning("session-1\x00trace\x00call-1", true)
	notifier.SetToolRunning("session-2\x00trace\x00call-2", true)
	notifier.ClearToolsForSession("session-1")
	if got := notifier.currentSnapshot().effectiveState(); got != chatTitleRunning {
		t.Fatalf("clearing one session should preserve other tools, got %v", got)
	}
	notifier.ClearToolsForSession("session-2")
	if got := notifier.currentSnapshot().effectiveState(); got != chatTitleWaiting {
		t.Fatalf("clearing all session tools should restore waiting, got %v", got)
	}

	notifier.Close()
	notifier.Close()
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.clears != 1 {
		t.Fatalf("Close() clears = %d, want 1", output.clears)
	}
}

func TestChatRuntimeEventBridgeUpdatesToolTitle(t *testing.T) {
	output := &recordingChatTitleOutput{supported: true}
	notifier := newChatTitleNotifier(output, chatTitleOptions{enabled: true, items: []string{"state"}}, nil)
	defer notifier.Close()
	bridge := &chatRuntimeEventBridge{session: &ChatSession{TitleNotifier: notifier}}
	event := runtimeevents.Event{
		Type: runtimechat.EventToolStarted, SessionID: "session-1", TraceID: "trace-1",
		Payload: map[string]interface{}{"tool_call_id": "call-1"},
	}
	bridge.updateChatTitleForRuntimeEvent(event)
	if got := notifier.currentSnapshot().effectiveState(); got != chatTitleRunning {
		t.Fatalf("tool start state = %v", got)
	}
	event.Type = runtimechat.EventToolFinished
	bridge.updateChatTitleForRuntimeEvent(event)
	if got := notifier.currentSnapshot().effectiveState(); got != chatTitleIdle {
		t.Fatalf("tool finish state = %v", got)
	}
}

func TestNewChatTitleNotifierRejectsDisabledOutputs(t *testing.T) {
	output := &recordingChatTitleOutput{supported: true}
	if got := newChatTitleNotifier(output, chatTitleOptions{enabled: false, items: []string{"state"}}, nil); got != nil {
		t.Fatal("disabled notifier should be nil")
	}
	if got := newChatTitleNotifier(output, chatTitleOptions{enabled: true}, nil); got != nil {
		t.Fatal("notifier with no items should be nil")
	}
}
