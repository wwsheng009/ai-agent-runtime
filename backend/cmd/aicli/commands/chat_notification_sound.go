package commands

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

type chatSoundEvent string

const (
	chatSoundActionRequired chatSoundEvent = "action-required"
	chatSoundTurnComplete   chatSoundEvent = "turn-complete"

	defaultChatSoundCooldown = 800 * time.Millisecond
	maxChatSoundCooldown     = 60 * time.Second
)

// chatSoundEventPriority mirrors Codex Notification::priority:
// action-required / approval / plan prompts outrank agent-turn-complete.
func chatSoundEventPriority(event chatSoundEvent) int {
	switch event {
	case chatSoundActionRequired:
		return 1
	default:
		return 0
	}
}

type chatSoundOutput interface {
	Supported() bool
	Notify() error
}

type chatSoundOptions struct {
	enabled  bool
	events   []chatSoundEvent
	cooldown time.Duration
}

// chatSoundNotifier coalesces nearby attention signals so nested approvals and
// fast turn completion cannot produce a burst of terminal bells.
// Higher-priority action-required signals can break through a recent
// lower-priority turn-complete bell (Codex pending_notification priority).
type chatSoundNotifier struct {
	output   chatSoundOutput
	events   map[chatSoundEvent]struct{}
	cooldown time.Duration
	now      func() time.Time

	mu       sync.Mutex
	lastBell time.Time
	lastPrio int
}

func newChatSoundNotifier(output chatSoundOutput, options chatSoundOptions) *chatSoundNotifier {
	if output == nil || !options.enabled || !output.Supported() || len(options.events) == 0 {
		return nil
	}
	events := make(map[chatSoundEvent]struct{}, len(options.events))
	for _, event := range options.events {
		events[event] = struct{}{}
	}
	return &chatSoundNotifier{
		output:   output,
		events:   events,
		cooldown: options.cooldown,
		now:      time.Now,
	}
}

func (n *chatSoundNotifier) Notify(event chatSoundEvent) bool {
	if n == nil {
		return false
	}
	if _, enabled := n.events[event]; !enabled {
		return false
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	priority := chatSoundEventPriority(event)
	if n.cooldown > 0 && !n.lastBell.IsZero() && now.Before(n.lastBell.Add(n.cooldown)) {
		// Codex: higher-priority notifications replace lower-priority ones.
		// Equal/lower priority stays suppressed for the cooldown window so
		// nested approvals and rapid turn-complete rings do not stack.
		if priority <= n.lastPrio {
			return false
		}
	}
	if err := n.output.Notify(); err != nil {
		logpkg.Debugf("failed to emit chat notification sound: %v", err)
		return false
	}
	n.lastBell = now
	n.lastPrio = priority
	return true
}

func resolveChatSoundOptions(cfg *config.Config) chatSoundOptions {
	options := chatSoundOptions{
		enabled:  true,
		events:   []chatSoundEvent{chatSoundActionRequired, chatSoundTurnComplete},
		cooldown: defaultChatSoundCooldown,
	}
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.Chat == nil || cfg.AICLI.Chat.Notifications == nil || cfg.AICLI.Chat.Notifications.Sound == nil {
		return options
	}
	configured := cfg.AICLI.Chat.Notifications.Sound
	if configured.Enabled != nil {
		options.enabled = *configured.Enabled
	}
	if configured.Events != nil {
		options.events = normalizeChatSoundEvents(configured.Events)
	}
	if configured.CooldownMS != nil {
		cooldownMS := *configured.CooldownMS
		if cooldownMS < 0 {
			cooldownMS = 0
		}
		maxCooldownMS := int(maxChatSoundCooldown / time.Millisecond)
		if cooldownMS > maxCooldownMS {
			cooldownMS = maxCooldownMS
		}
		options.cooldown = time.Duration(cooldownMS) * time.Millisecond
	}
	return options
}

func normalizeChatSoundEvents(events []string) []chatSoundEvent {
	normalized := make([]chatSoundEvent, 0, len(events))
	seen := make(map[chatSoundEvent]struct{}, len(events))
	for _, event := range events {
		value := strings.ToLower(strings.TrimSpace(event))
		var canonical chatSoundEvent
		switch value {
		case "action-required", "approval-required", "approval", "input-required":
			canonical = chatSoundActionRequired
		case "turn-complete", "completed", "complete", "done":
			canonical = chatSoundTurnComplete
		default:
			continue
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, canonical)
	}
	return normalized
}

func initializeChatSoundNotifier(session *ChatSession) {
	if session == nil || session.NoInteractive || session.JSONOutput || session.Layout == nil {
		return
	}
	terminal := session.Layout.Terminal()
	if terminal == nil {
		return
	}
	output := ui.NewTerminalBellWriter(terminal, os.Stdout)
	session.SoundNotifier = newChatSoundNotifier(output, resolveChatSoundOptions(session.Config))
}

func notifyChatSound(session *ChatSession, event chatSoundEvent) {
	if session == nil || session.SoundNotifier == nil {
		return
	}
	if !shouldEmitChatNotification(session) {
		return
	}
	session.SoundNotifier.Notify(event)
}

// chatNotificationCondition mirrors Codex NotificationCondition.
// Default is unfocused-only so focused interactive work stays quiet.
type chatNotificationCondition string

const (
	chatNotificationConditionUnfocused chatNotificationCondition = "unfocused"
	chatNotificationConditionAlways    chatNotificationCondition = "always"
)

func resolveChatNotificationCondition(cfg *config.Config) chatNotificationCondition {
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.Chat == nil || cfg.AICLI.Chat.Notifications == nil {
		return chatNotificationConditionUnfocused
	}
	switch strings.ToLower(strings.TrimSpace(cfg.AICLI.Chat.Notifications.Condition)) {
	case "always", "on", "true", "1":
		return chatNotificationConditionAlways
	case "", "unfocused", "background", "blurred":
		return chatNotificationConditionUnfocused
	default:
		return chatNotificationConditionUnfocused
	}
}

// shouldEmitChatNotification gates attention signals by terminal focus.
// Codex default: only emit while the terminal is unfocused.
func shouldEmitChatNotification(session *ChatSession) bool {
	condition := chatNotificationConditionUnfocused
	if session != nil {
		condition = resolveChatNotificationCondition(session.Config)
	}
	return shouldEmitChatNotificationWithFocus(condition, ui.TerminalFocused())
}

func shouldEmitChatNotificationWithFocus(condition chatNotificationCondition, terminalFocused bool) bool {
	switch condition {
	case chatNotificationConditionAlways:
		return true
	default:
		return !terminalFocused
	}
}

// shouldNotifyChatTurnComplete mirrors Codex AgentTurnComplete gating: only
// emit attention when the agent is truly waiting for the user.
//
// Suppress when:
//   - the session was interrupted
//   - queued follow-up input will start the next turn
//   - a residual active goal remains (Codex active_goal_continuing)
//
// Goal auto-continuation itself runs inside sendMessage before this check, so
// mid-loop auto-continues never reach notify. The active-goal gate still
// matters after the continuation limit: the goal is still active and the
// agent is not cleanly waiting for a fresh user turn.
func shouldNotifyChatTurnComplete(session *ChatSession) bool {
	if session == nil {
		return false
	}
	if session.IsInterrupted() {
		return false
	}
	if queuedCount, draining := queuedInteractiveInputState(session); queuedCount > 0 || draining {
		return false
	}
	if activeSessionGoalContinuing(session) {
		return false
	}
	return true
}

// activeSessionGoalContinuing mirrors Codex:
//
//	current_goal_status.as_ref().is_some_and(GoalStatusState::is_active)
//
// Only StatusActive suppresses attention. Completed / paused / budget-limited
// goals allow turn-complete notify. Lookup failures fail open so a broken
// goal record cannot permanently silence notifications.
func activeSessionGoalContinuing(session *ChatSession) bool {
	goal, ok, err := currentSessionGoal(session)
	if err != nil || !ok || goal == nil {
		return false
	}
	return goal.Status == runtimegoal.StatusActive
}
