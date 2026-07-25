package commands

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
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

type recordingChatSoundOutput struct {
	mu        sync.Mutex
	supported bool
	calls     int
}

func (o *recordingChatSoundOutput) Supported() bool { return o != nil && o.supported }

func (o *recordingChatSoundOutput) Notify() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	return nil
}

func TestResolveChatTitleOptions(t *testing.T) {
	defaults := resolveChatTitleOptions(nil)
	if !defaults.enabled || !defaults.animations {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
	if got := strings.Join(defaults.items, ","); got != "activity,project" {
		t.Fatalf("unexpected default items: %q", got)
	}

	disabled := false
	cfg := &config.Config{AICLI: &config.AICLIConfig{Chat: &config.AICLIChatConfig{
		TerminalTitle: &config.AICLITerminalTitleConfig{
			Enabled:    &disabled,
			Animations: &disabled,
			Items:      []string{" spinner ", "STATUS", "project-name", "thread-title", "status", "branch", "git-branch", "unknown"},
		},
	}}}
	options := resolveChatTitleOptions(cfg)
	if options.enabled || options.animations {
		t.Fatalf("expected disabled flags, got %+v", options)
	}
	if got := strings.Join(options.items, ","); got != "activity,state,project,thread,git-branch" {
		t.Fatalf("unexpected normalized items: %q", got)
	}
}

func TestRenderChatTerminalTitleStates(t *testing.T) {
	// Default Codex-aligned layout: activity icon only (no Ready/Working prose).
	snapshot := chatTitleSnapshot{
		baseState:  chatTitleIdle,
		project:    "runtime",
		animations: true,
		items:      []string{"activity", "project"},
		tools:      make(map[string]struct{}),
		actions:    make(map[uint64]string),
	}
	if got := renderChatTerminalTitle(snapshot, 0); got != "runtime" {
		t.Fatalf("idle title = %q", got)
	}
	snapshot.baseState = chatTitleWaiting
	if got := renderChatTerminalTitle(snapshot, 0); got != "⠋ runtime" {
		t.Fatalf("waiting title = %q", got)
	}
	snapshot.tools["tool-1"] = struct{}{}
	if got := renderChatTerminalTitle(snapshot, 0); got != "◐ runtime" {
		t.Fatalf("working title = %q", got)
	}
	snapshot.baseState = chatTitleStopping
	if got := renderChatTerminalTitle(snapshot, 0); got != "■ runtime" {
		t.Fatalf("stopping title = %q", got)
	}
	snapshot.baseState = chatTitleWaiting
	snapshot.actions[1] = "Approval Required"
	snapshot.latestID = 1
	if got := renderChatTerminalTitle(snapshot, 0); got != "[ ! ] Action Required | runtime" {
		t.Fatalf("action title = %q", got)
	}
	if got := renderChatTerminalTitle(snapshot, 1); got != "[ . ] Action Required | runtime" {
		t.Fatalf("animated action title = %q", got)
	}
	snapshot.animations = false
	if got := renderChatTerminalTitle(snapshot, 1); got != "[ ! ] Action Required | runtime" {
		t.Fatalf("static action title = %q", got)
	}

	// Opt-in state labels remain available when activity is not configured.
	textOnly := chatTitleSnapshot{
		baseState:  chatTitleRunning,
		project:    "runtime",
		animations: false,
		items:      []string{"state", "project"},
		tools:      map[string]struct{}{"tool-1": {}},
		actions:    make(map[uint64]string),
	}
	if got := renderChatTerminalTitle(textOnly, 0); got != "Working | runtime" {
		t.Fatalf("state-only title = %q", got)
	}

	// activity + state together still renders only the glyph (no prose stack).
	both := chatTitleSnapshot{
		baseState:  chatTitleWaiting,
		project:    "runtime",
		animations: true,
		items:      []string{"activity", "state", "project"},
		tools:      make(map[string]struct{}),
		actions:    make(map[uint64]string),
	}
	if got := renderChatTerminalTitle(both, 0); got != "⠋ runtime" {
		t.Fatalf("activity+state title = %q, want glyph only", got)
	}
}

func TestRenderChatTerminalTitleIncludesGitBranch(t *testing.T) {
	snapshot := chatTitleSnapshot{
		baseState:  chatTitleIdle,
		project:    "runtime",
		branch:     "feat/title-branch",
		animations: false,
		items:      []string{"activity", "project", "git-branch"},
		tools:      make(map[string]struct{}),
		actions:    make(map[uint64]string),
	}
	if got := renderChatTerminalTitle(snapshot, 0); got != "runtime | feat/title-branch" {
		t.Fatalf("title with branch = %q", got)
	}
	// Empty branch is omitted, matching Codex unavailable-item semantics.
	snapshot.branch = ""
	if got := renderChatTerminalTitle(snapshot, 0); got != "runtime" {
		t.Fatalf("title without branch = %q", got)
	}
}

func TestChatTitleNotifierLoadsGitBranchWhenConfigured(t *testing.T) {
	previousLookup := chatStatusGitBranchLookup
	chatStatusGitBranchLookup = func(string) string { return "feat/title-notify" }
	defer func() { chatStatusGitBranchLookup = previousLookup }()
	resetChatStatusGitBranchCacheForTest()

	output := &recordingChatTitleOutput{supported: true}
	notifier := newChatTitleNotifier(output, chatTitleOptions{
		enabled: true, animations: false, items: []string{"activity", "project", "git-branch"},
	}, &ChatSession{})
	if notifier == nil {
		t.Fatal("expected notifier")
	}
	defer notifier.Close()

	snapshot := notifier.currentSnapshot()
	if snapshot.branch != "feat/title-notify" {
		t.Fatalf("branch = %q, want feat/title-notify", snapshot.branch)
	}
	// Project is resolved from process cwd; only assert branch inclusion shape.
	if got := renderChatTerminalTitle(snapshot, 0); !strings.HasSuffix(got, " | feat/title-notify") {
		t.Fatalf("rendered title = %q", got)
	}

	// When git-branch is not configured, skip the lookup cost and clear cache field.
	output2 := &recordingChatTitleOutput{supported: true}
	notifier2 := newChatTitleNotifier(output2, chatTitleOptions{
		enabled: true, animations: false, items: []string{"activity", "project"},
	}, &ChatSession{})
	if notifier2 == nil {
		t.Fatal("expected notifier without branch item")
	}
	defer notifier2.Close()
	if got := notifier2.currentSnapshot().branch; got != "" {
		t.Fatalf("branch without item should be empty, got %q", got)
	}
}

func TestChatTitleStoppingOverridesStaleToolsAndActions(t *testing.T) {
	snapshot := chatTitleSnapshot{
		baseState: chatTitleStopping,
		tools:     map[string]struct{}{"tool-1": {}},
		actions:   map[uint64]string{1: "Approval Required"},
		latestID:  1,
	}
	if got := snapshot.effectiveState(); got != chatTitleStopping {
		t.Fatalf("stopping should override stale activity, got %v", got)
	}
	if got := chatTitleStateForSurface(chatSurfaceTitleState("Stopping")); got != chatTitleStopping {
		t.Fatalf("surface stopping should map to title stopping, got %v", got)
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

func TestShouldNotifyChatTurnComplete(t *testing.T) {
	if shouldNotifyChatTurnComplete(nil) {
		t.Fatal("nil session should not notify")
	}
	session := &ChatSession{}
	if !shouldNotifyChatTurnComplete(session) {
		t.Fatal("idle session without queued input should notify")
	}

	session.queuedInputDrain = true
	if shouldNotifyChatTurnComplete(session) {
		t.Fatal("queued-input drain should suppress turn-complete notify")
	}
	session.queuedInputDrain = false

	queue := newChatInputQueue(nil)
	queue.routeInputText("queued follow-up\n")
	session.InputQueue = queue
	if shouldNotifyChatTurnComplete(session) {
		t.Fatal("pending queued follow-up should suppress turn-complete notify")
	}

	session.InputQueue = nil
	session.InterruptPreservePendingInput()
	if shouldNotifyChatTurnComplete(session) {
		t.Fatal("interrupted session should suppress turn-complete notify")
	}

	// Residual active goal (e.g. after auto-continuation limit) should suppress
	// attention, matching Codex active_goal_continuing.
	activeSession, cleanupActive := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	t.Cleanup(cleanupActive)
	if shouldNotifyChatTurnComplete(activeSession) {
		t.Fatal("active residual goal should suppress turn-complete notify")
	}

	// Completed goals should still notify — the agent is waiting for the user.
	completeSession, cleanupComplete := newGoalAutoContinueTestSession(t, runtimegoal.StatusComplete)
	t.Cleanup(cleanupComplete)
	if !shouldNotifyChatTurnComplete(completeSession) {
		t.Fatal("completed goal should allow turn-complete notify")
	}

	// Paused / budget-limited goals are not Codex "active" continuation.
	pausedSession, cleanupPaused := newGoalAutoContinueTestSession(t, runtimegoal.StatusPaused)
	t.Cleanup(cleanupPaused)
	if !shouldNotifyChatTurnComplete(pausedSession) {
		t.Fatal("paused goal should allow turn-complete notify")
	}
}

func TestShouldEmitChatNotificationWithFocus(t *testing.T) {
	if shouldEmitChatNotificationWithFocus(chatNotificationConditionUnfocused, true) {
		t.Fatal("unfocused condition must suppress when terminal is focused")
	}
	if !shouldEmitChatNotificationWithFocus(chatNotificationConditionUnfocused, false) {
		t.Fatal("unfocused condition must emit when terminal is unfocused")
	}
	if !shouldEmitChatNotificationWithFocus(chatNotificationConditionAlways, true) {
		t.Fatal("always condition must emit when terminal is focused")
	}
	if !shouldEmitChatNotificationWithFocus(chatNotificationConditionAlways, false) {
		t.Fatal("always condition must emit when terminal is unfocused")
	}
}

func TestResolveChatNotificationCondition(t *testing.T) {
	if got := resolveChatNotificationCondition(nil); got != chatNotificationConditionUnfocused {
		t.Fatalf("nil config condition = %q, want unfocused", got)
	}

	cfg := &config.Config{
		AICLI: &config.AICLIConfig{
			Chat: &config.AICLIChatConfig{
				Notifications: &config.AICLIChatNotifications{Condition: "always"},
			},
		},
	}
	if got := resolveChatNotificationCondition(cfg); got != chatNotificationConditionAlways {
		t.Fatalf("always condition = %q", got)
	}

	cfg.AICLI.Chat.Notifications.Condition = "unfocused"
	if got := resolveChatNotificationCondition(cfg); got != chatNotificationConditionUnfocused {
		t.Fatalf("unfocused condition = %q", got)
	}

	cfg.AICLI.Chat.Notifications.Condition = "mystery"
	if got := resolveChatNotificationCondition(cfg); got != chatNotificationConditionUnfocused {
		t.Fatalf("unknown condition should default to unfocused, got %q", got)
	}
}

func TestNotifyChatSoundRespectsFocusCondition(t *testing.T) {
	t.Cleanup(ui.ResetTerminalFocusForTest)

	output := &recordingChatSoundOutput{supported: true}
	session := &ChatSession{
		SoundNotifier: newChatSoundNotifier(output, chatSoundOptions{
			enabled:  true,
			events:   []chatSoundEvent{chatSoundTurnComplete},
			cooldown: 0,
		}),
		Config: &config.Config{
			AICLI: &config.AICLIConfig{
				Chat: &config.AICLIChatConfig{
					Notifications: &config.AICLIChatNotifications{Condition: "unfocused"},
				},
			},
		},
	}

	ui.SetTerminalFocused(true)
	notifyChatSound(session, chatSoundTurnComplete)
	if output.calls != 0 {
		t.Fatalf("focused terminal should suppress unfocused notifications, calls=%d", output.calls)
	}

	ui.SetTerminalFocused(false)
	notifyChatSound(session, chatSoundTurnComplete)
	if output.calls != 1 {
		t.Fatalf("unfocused terminal should emit notification, calls=%d", output.calls)
	}

	session.Config.AICLI.Chat.Notifications.Condition = "always"
	ui.SetTerminalFocused(true)
	notifyChatSound(session, chatSoundTurnComplete)
	if output.calls != 2 {
		t.Fatalf("always condition should emit while focused, calls=%d", output.calls)
	}
}

func TestChatSoundEventPriority(t *testing.T) {
	if got := chatSoundEventPriority(chatSoundTurnComplete); got != 0 {
		t.Fatalf("turn-complete priority = %d, want 0", got)
	}
	if got := chatSoundEventPriority(chatSoundActionRequired); got != 1 {
		t.Fatalf("action-required priority = %d, want 1", got)
	}
}

func TestChatSoundNotifierPriorityBreaksCooldown(t *testing.T) {
	output := &recordingChatSoundOutput{supported: true}
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	notifier := newChatSoundNotifier(output, chatSoundOptions{
		enabled:  true,
		events:   []chatSoundEvent{chatSoundActionRequired, chatSoundTurnComplete},
		cooldown: time.Second,
	})
	if notifier == nil {
		t.Fatal("expected notifier")
	}
	notifier.now = func() time.Time { return now }

	if !notifier.Notify(chatSoundTurnComplete) {
		t.Fatal("first turn-complete should ring")
	}
	if output.calls != 1 {
		t.Fatalf("after first ring calls=%d", output.calls)
	}

	// Same/lower priority during cooldown is suppressed (Codex does not replace
	// a higher pending notification with AgentTurnComplete).
	if notifier.Notify(chatSoundTurnComplete) {
		t.Fatal("duplicate turn-complete during cooldown should be suppressed")
	}
	if output.calls != 1 {
		t.Fatalf("suppressed turn-complete should not ring, calls=%d", output.calls)
	}

	// Higher-priority action-required replaces a recent turn-complete bell.
	if !notifier.Notify(chatSoundActionRequired) {
		t.Fatal("action-required should break through lower-priority cooldown")
	}
	if output.calls != 2 {
		t.Fatalf("action-required break-through calls=%d, want 2", output.calls)
	}

	// Equal-priority action-required remains coalesced during cooldown.
	if notifier.Notify(chatSoundActionRequired) {
		t.Fatal("nested action-required during cooldown should stay suppressed")
	}
	if output.calls != 2 {
		t.Fatalf("nested action-required should not ring, calls=%d", output.calls)
	}

	// Lower-priority turn-complete cannot override a recent action-required.
	if notifier.Notify(chatSoundTurnComplete) {
		t.Fatal("turn-complete must not override recent action-required")
	}
	if output.calls != 2 {
		t.Fatalf("lower-priority override attempts should not ring, calls=%d", output.calls)
	}

	// After cooldown, any enabled event may ring again.
	now = now.Add(time.Second)
	if !notifier.Notify(chatSoundTurnComplete) {
		t.Fatal("turn-complete after cooldown should ring")
	}
	if output.calls != 3 {
		t.Fatalf("post-cooldown turn-complete calls=%d, want 3", output.calls)
	}
}
