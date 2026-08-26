package commands

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

type chatTitleState int

const (
	chatTitleIdle chatTitleState = iota
	chatTitleWaiting
	chatTitleRunning
	chatTitleStopping
	chatTitleActionRequired
)

const (
	chatTitleWaitingInterval = 100 * time.Millisecond
	chatTitleRunningInterval = 160 * time.Millisecond
	chatTitleActionInterval  = time.Second
)

var chatTitleWaitingFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
var chatTitleRunningFrames = []string{"◐", "◓", "◑", "◒"}

type chatTitleOutput interface {
	Supported() bool
	Set(string) (bool, error)
	Clear() error
}

type chatTitleOptions struct {
	enabled    bool
	animations bool
	items      []string
}

type chatTitleSnapshot struct {
	baseState chatTitleState
	tools     map[string]struct{}
	actions   map[uint64]string
	latestID  uint64
	project   string
	model     string
	thread    string
	// branch is the current git branch for the title "git-branch" item.
	// Empty when unavailable/detached, matching Codex omission semantics.
	branch     string
	animations bool
	items      []string
}

// chatTitleNotifier aggregates overlapping chat activity into one title state.
// A single event loop owns animation timing, so tool calls and approvals never
// create competing title goroutines.
type chatTitleNotifier struct {
	output  chatTitleOutput
	session *ChatSession

	mu                    sync.Mutex
	snapshot              chatTitleSnapshot
	nextID                uint64
	lastGoalStatusSegment string
	wake                  chan struct{}
	done                  chan struct{}
	stopped               chan struct{}
	close                 sync.Once
}

func newChatTitleNotifier(output chatTitleOutput, options chatTitleOptions, session *ChatSession) *chatTitleNotifier {
	if output == nil || !options.enabled || !output.Supported() || len(options.items) == 0 {
		return nil
	}
	n := &chatTitleNotifier{
		output:  output,
		session: session,
		snapshot: chatTitleSnapshot{
			baseState:  chatTitleIdle,
			tools:      make(map[string]struct{}),
			actions:    make(map[uint64]string),
			animations: options.animations,
			items:      append([]string(nil), options.items...),
		},
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	n.refreshMetadataLocked(session)
	go n.run()
	return n
}

func (n *chatTitleNotifier) Close() {
	if n == nil {
		return
	}
	n.close.Do(func() { close(n.done) })
	<-n.stopped
}

func (n *chatTitleNotifier) signal() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

func (n *chatTitleNotifier) SetBaseState(state chatTitleState) {
	if n == nil {
		return
	}
	if state == chatTitleActionRequired {
		state = chatTitleRunning
	}
	n.mu.Lock()
	n.snapshot.baseState = state
	n.mu.Unlock()
	n.signal()
}

func (n *chatTitleNotifier) RefreshMetadata(session *ChatSession) {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.refreshMetadataLocked(session)
	n.mu.Unlock()
	n.signal()
}

func (n *chatTitleNotifier) refreshMetadataLocked(session *ChatSession) {
	if n == nil {
		return
	}
	if cwd, err := os.Getwd(); err == nil {
		project := filepath.Base(filepath.Clean(cwd))
		if project == "." || project == string(filepath.Separator) {
			project = filepath.Clean(cwd)
		}
		n.snapshot.project = truncateChatTitlePart(project, 48)
	}
	// Only pay for git lookup when the configured title actually includes the
	// branch segment (Codex also gates branch probes on configured items).
	if chatTitleItemsInclude(n.snapshot.items, "git-branch") {
		n.snapshot.branch = truncateChatTitlePart(resolveChatStatusGitBranch(session), 32)
	} else {
		n.snapshot.branch = ""
	}
	if session == nil {
		n.snapshot.model = ""
		n.snapshot.thread = ""
		return
	}
	n.snapshot.model = truncateChatTitlePart(strings.TrimSpace(session.Model), 40)
	n.snapshot.thread = ""
	if session.RuntimeSession != nil {
		if preview := session.RuntimeSession.BuildPreview(); preview != nil {
			n.snapshot.thread = truncateChatTitlePart(strings.TrimSpace(preview.Title), 56)
		}
	}
}

func chatTitleItemsInclude(items []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return false
	}
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item), target) {
			return true
		}
	}
	return false
}

func (n *chatTitleNotifier) SetToolRunning(key string, running bool) {
	if n == nil || strings.TrimSpace(key) == "" {
		return
	}
	n.mu.Lock()
	if running {
		n.snapshot.tools[key] = struct{}{}
	} else {
		delete(n.snapshot.tools, key)
	}
	n.mu.Unlock()
	n.signal()
}

func (n *chatTitleNotifier) ClearTools() {
	if n == nil {
		return
	}
	n.mu.Lock()
	for key := range n.snapshot.tools {
		delete(n.snapshot.tools, key)
	}
	n.mu.Unlock()
	n.signal()
}

func (n *chatTitleNotifier) ClearToolsForSession(sessionID string) {
	if n == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	prefix := strings.TrimSpace(sessionID) + "\x00"
	n.mu.Lock()
	for key := range n.snapshot.tools {
		if strings.HasPrefix(key, prefix) {
			delete(n.snapshot.tools, key)
		}
	}
	n.mu.Unlock()
	n.signal()
}

func (n *chatTitleNotifier) BeginActionRequired(label string) func() {
	if n == nil {
		return func() {}
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = "Action Required"
	}
	n.mu.Lock()
	n.nextID++
	id := n.nextID
	n.snapshot.actions[id] = label
	n.snapshot.latestID = id
	n.mu.Unlock()
	n.signal()

	var once sync.Once
	return func() {
		once.Do(func() {
			n.mu.Lock()
			delete(n.snapshot.actions, id)
			n.snapshot.latestID = latestChatTitleActionID(n.snapshot.actions)
			n.mu.Unlock()
			n.signal()
		})
	}
}

func latestChatTitleActionID(actions map[uint64]string) uint64 {
	var latest uint64
	for id := range actions {
		if id > latest {
			latest = id
		}
	}
	return latest
}

func (n *chatTitleNotifier) run() {
	defer close(n.stopped)
	defer func() {
		if err := n.output.Clear(); err != nil {
			logpkg.Debugf("failed to clear chat terminal title: %v", err)
		}
	}()

	frame := 0
	for {
		snapshot := n.currentSnapshot()
		if _, err := n.output.Set(renderChatTerminalTitle(snapshot, frame)); err != nil {
			logpkg.Debugf("failed to update chat terminal title: %v", err)
			return
		}
		// Codex refreshes the goal indicator on a time tick while a turn is
		// running so "Pursuing goal (Ns)" advances without waiting for events.
		n.refreshGoalStatusIndicatorForTimeTick(snapshot)
		interval := chatTitleAnimationInterval(snapshot)
		var timer *time.Timer
		var timerC <-chan time.Time
		if interval > 0 {
			timer = time.NewTimer(interval)
			timerC = timer.C
		}

		select {
		case <-n.done:
			if timer != nil {
				timer.Stop()
			}
			return
		case <-n.wake:
			if timer != nil {
				timer.Stop()
			}
			frame = 0
		case <-timerC:
			frame++
		}
	}
}

// refreshGoalStatusIndicatorForTimeTick updates the composer status line while
// an active turn is running so live goal elapsed time can advance. Codex only
// rewrites the indicator when the rendered usage string changes.
func (n *chatTitleNotifier) refreshGoalStatusIndicatorForTimeTick(snapshot chatTitleSnapshot) {
	if n == nil || n.session == nil || n.session.Interaction == nil {
		return
	}
	switch snapshot.effectiveState() {
	case chatTitleRunning, chatTitleWaiting, chatTitleActionRequired, chatTitleStopping:
	default:
		return
	}
	if chatGoalStatusActiveTurnStartedAt(n.session).IsZero() {
		return
	}
	segment := chatSurfaceGoalStatusSegment(n.session).full
	n.mu.Lock()
	changed := segment != n.lastGoalStatusSegment
	if changed {
		n.lastGoalStatusSegment = segment
	}
	n.mu.Unlock()
	if !changed {
		return
	}
	// Keep the status line in sync with live goal accrual. RefreshStatus
	// recomputes segments under the interaction lock.
	n.session.Interaction.RefreshStatus("")
}

func (n *chatTitleNotifier) currentSnapshot() chatTitleSnapshot {
	n.mu.Lock()
	defer n.mu.Unlock()
	snapshot := n.snapshot
	snapshot.items = append([]string(nil), n.snapshot.items...)
	snapshot.tools = make(map[string]struct{}, len(n.snapshot.tools))
	for key := range n.snapshot.tools {
		snapshot.tools[key] = struct{}{}
	}
	snapshot.actions = make(map[uint64]string, len(n.snapshot.actions))
	for id, label := range n.snapshot.actions {
		snapshot.actions[id] = label
	}
	return snapshot
}

func (s chatTitleSnapshot) effectiveState() chatTitleState {
	if s.baseState == chatTitleStopping {
		return chatTitleStopping
	}
	if len(s.actions) > 0 {
		return chatTitleActionRequired
	}
	if len(s.tools) > 0 || s.baseState == chatTitleRunning {
		return chatTitleRunning
	}
	if s.baseState == chatTitleWaiting {
		return chatTitleWaiting
	}
	return chatTitleIdle
}

func chatTitleAnimationInterval(snapshot chatTitleSnapshot) time.Duration {
	if !snapshot.animations {
		return 0
	}
	switch snapshot.effectiveState() {
	case chatTitleWaiting:
		return chatTitleWaitingInterval
	case chatTitleRunning:
		return chatTitleRunningInterval
	case chatTitleActionRequired:
		return chatTitleActionInterval
	case chatTitleIdle, chatTitleStopping:
		return 0
	default:
		return 0
	}
}

func renderChatTerminalTitle(snapshot chatTitleSnapshot, frame int) string {
	state := snapshot.effectiveState()
	// Codex replaces the activity segment with a combined "[ ! ] Action Required"
	// prefix when the title includes activity and the session is blocked. The
	// separate run-state label is intentionally omitted in that path.
	if state == chatTitleActionRequired && chatTitleItemsContain(snapshot.items, "activity") {
		return renderChatActionRequiredTerminalTitle(snapshot, frame)
	}

	// Prefer the activity glyph when both activity and state are configured so
	// the title never shows icon + Ready/Working prose together.
	preferActivityGlyph := chatTitleItemsContain(snapshot.items, "activity")

	var title strings.Builder
	previousItem := ""
	for _, item := range snapshot.items {
		value := ""
		switch item {
		case "activity":
			value = chatTitleActivity(state, snapshot.animations, frame)
		case "state":
			// Optional text-only run-state. Skipped when activity is present so
			// icons and prose never stack.
			if preferActivityGlyph {
				continue
			}
			value = chatTitleStateLabel(snapshot, state)
		case "project":
			value = snapshot.project
		case "model":
			value = snapshot.model
		case "thread":
			value = snapshot.thread
		case "git-branch":
			// Omitted when empty (detached HEAD / non-git / lookup miss).
			value = snapshot.branch
		case "app-name":
			value = "aicli"
		}
		if value != "" {
			if title.Len() > 0 {
				if item == "activity" || previousItem == "activity" {
					title.WriteByte(' ')
				} else {
					title.WriteString(" | ")
				}
			}
			title.WriteString(value)
			previousItem = item
		}
	}
	return title.String()
}

func renderChatActionRequiredTerminalTitle(snapshot chatTitleSnapshot, frame int) string {
	// Match Codex: one combined action-required prefix, then remaining items
	// except activity/state so the title never reads as icon + prose twice.
	parts := []string{chatTitleActionRequiredPrefix(snapshot.animations, frame)}
	for _, item := range snapshot.items {
		if item == "activity" || item == "state" {
			continue
		}
		value := ""
		switch item {
		case "project":
			value = snapshot.project
		case "model":
			value = snapshot.model
		case "thread":
			value = snapshot.thread
		case "git-branch":
			value = snapshot.branch
		case "app-name":
			value = "aicli"
		}
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " | ")
}

func chatTitleActionRequiredPrefix(animations bool, frame int) string {
	if animations && frame%2 == 1 {
		return "[ . ] Action Required"
	}
	return "[ ! ] Action Required"
}

func chatTitleItemsContain(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func chatTitleActivity(state chatTitleState, animations bool, frame int) string {
	switch state {
	case chatTitleIdle:
		// Codex only shows a spinner while work is in progress; idle keeps the
		// title free of a redundant Ready glyph when activity is configured.
		return ""
	case chatTitleWaiting:
		if animations {
			return chatTitleWaitingFrames[frame%len(chatTitleWaitingFrames)]
		}
		return "…"
	case chatTitleRunning:
		if animations {
			return chatTitleRunningFrames[frame%len(chatTitleRunningFrames)]
		}
		return "▶"
	case chatTitleStopping:
		return "■"
	case chatTitleActionRequired:
		// Reached only when activity is configured without the combined
		// action-required path (state-only titles still use chatTitleStateLabel).
		if animations && frame%2 == 1 {
			return "[ . ]"
		}
		return "[ ! ]"
	default:
		return ""
	}
}

func chatTitleStateLabel(snapshot chatTitleSnapshot, state chatTitleState) string {
	switch state {
	case chatTitleIdle:
		return "Ready"
	case chatTitleWaiting:
		return "Waiting"
	case chatTitleRunning:
		return "Working"
	case chatTitleStopping:
		return "Stopping"
	case chatTitleActionRequired:
		if label := snapshot.actions[snapshot.latestID]; label != "" {
			return label
		}
		return "Action Required"
	default:
		return ""
	}
}

func truncateChatTitlePart(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return value
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

func resolveChatTitleOptions(cfg *config.Config) chatTitleOptions {
	options := chatTitleOptions{
		enabled:    true,
		animations: true,
		// Codex default is activity + project only: icon/spinner without a
		// parallel Ready/Working text label. Users can still opt into "state".
		items: []string{"activity", "project"},
	}
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.Chat == nil || cfg.AICLI.Chat.TerminalTitle == nil {
		return options
	}
	configured := cfg.AICLI.Chat.TerminalTitle
	if configured.Enabled != nil {
		options.enabled = *configured.Enabled
	}
	if configured.Animations != nil {
		options.animations = *configured.Animations
	}
	if configured.Items != nil {
		options.items = normalizeChatTitleItems(configured.Items)
	}
	return options
}

func normalizeChatTitleItems(items []string) []string {
	normalized := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		item = strings.ToLower(strings.TrimSpace(item))
		switch item {
		case "spinner":
			item = "activity"
		case "run-state", "status":
			item = "state"
		case "project-name":
			item = "project"
		case "thread-title":
			item = "thread"
		case "branch":
			// Codex config id is git-branch; accept short alias for convenience.
			item = "git-branch"
		}
		switch item {
		case "activity", "state", "project", "model", "thread", "git-branch", "app-name":
		default:
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		normalized = append(normalized, item)
	}
	return normalized
}

func initializeChatTitleNotifier(session *ChatSession) {
	if session == nil || session.NoInteractive || session.JSONOutput || session.Layout == nil {
		return
	}
	terminal := session.Layout.Terminal()
	if terminal == nil {
		return
	}
	options := resolveChatTitleOptions(session.Config)
	output := ui.NewTerminalTitleWriter(terminal, os.Stdout)
	session.TitleNotifier = newChatTitleNotifier(output, options, session)
}

func chatTitleStateForSurface(state string) chatTitleState {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "streaming", "thinking", "reasoning", "running", "working":
		return chatTitleRunning
	case "stopping":
		return chatTitleStopping
	case "waiting", "retrying", "pending", "busy":
		return chatTitleWaiting
	default:
		return chatTitleIdle
	}
}

func beginChatTitleAction(session *ChatSession, label string) func() {
	notifyChatSound(session, chatSoundActionRequired)
	if session == nil || session.TitleNotifier == nil {
		return func() {}
	}
	return session.TitleNotifier.BeginActionRequired(label)
}

func beginChatTitleTool(session *ChatSession, key string) func() {
	if session == nil || session.TitleNotifier == nil {
		return func() {}
	}
	session.TitleNotifier.SetToolRunning(key, true)
	var once sync.Once
	return func() {
		once.Do(func() {
			session.TitleNotifier.SetToolRunning(key, false)
		})
	}
}

func refreshChatTitleMetadata(session *ChatSession) {
	if session != nil && session.TitleNotifier != nil {
		session.TitleNotifier.RefreshMetadata(session)
	}
}
