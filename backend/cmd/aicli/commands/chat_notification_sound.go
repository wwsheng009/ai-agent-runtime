package commands

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

type chatSoundEvent string

const (
	chatSoundActionRequired chatSoundEvent = "action-required"
	chatSoundTurnComplete    chatSoundEvent = "turn-complete"

	defaultChatSoundCooldown = 800 * time.Millisecond
	maxChatSoundCooldown     = 60 * time.Second
)

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
type chatSoundNotifier struct {
	output   chatSoundOutput
	events   map[chatSoundEvent]struct{}
	cooldown time.Duration
	now      func() time.Time

	mu       sync.Mutex
	lastBell time.Time
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
	if n.cooldown > 0 && !n.lastBell.IsZero() && now.Before(n.lastBell.Add(n.cooldown)) {
		return false
	}
	if err := n.output.Notify(); err != nil {
		logpkg.Debugf("failed to emit chat notification sound: %v", err)
		return false
	}
	n.lastBell = now
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
	if session != nil && session.SoundNotifier != nil {
		session.SoundNotifier.Notify(event)
	}
}
