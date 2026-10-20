package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Manager dispatches hook events to configured executors.
type Manager struct {
	hooks     []HookConfig
	executors map[string]Executor
	mu        sync.RWMutex
	observer  AsyncObserver
	spiller   ContextSpiller
	status    StatusObserver
	recorder  *RunRecorder
}

// Executor executes a hook action.
type Executor interface {
	Execute(ctx context.Context, hook HookConfig, payload map[string]interface{}) (Decision, error)
}

// RawExecutor is implemented by executors that can expose the original
// stdout/stderr/exit code so the manager can apply Codex's event-specific
// output protocol. Executors that only implement Executor keep the native
// decision parsing path.
type RawExecutor interface {
	ExecuteRaw(ctx context.Context, hook HookConfig, payload map[string]interface{}) (RawResult, error)
}

// RawResult is one hook process/HTTP response before event-specific parsing.
type RawResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// AsyncResult is delivered to observers after an async hook finishes.
type AsyncResult struct {
	Hook     HookConfig
	Decision Decision
	Output   CodexOutput
	Err      error
}

// AsyncObserver receives async hook results. It must be fast and non-blocking;
// the manager invokes it from the hook goroutine.
type AsyncObserver func(AsyncResult)

// ContextSpiller persists oversized additionalContext and returns a short
// pointer/preview for the model. limit <= 0 means "use the host default";
// negative disables spilling.
type ContextSpiller func(ctx context.Context, text string, limit int) (string, error)

// StatusObserver receives synchronous hook lifecycle transitions for host UI
// (statusMessage). It must be fast and non-blocking.
type StatusObserver func(hook HookConfig, phase string)

// NewManager creates a new hook manager.
func NewManager(hooks []HookConfig) *Manager {
	manager := &Manager{
		hooks:     append([]HookConfig(nil), hooks...),
		executors: make(map[string]Executor),
		recorder:  NewRunRecorder(100),
	}
	manager.executors["shell"] = &ShellExecutor{}
	manager.executors["http"] = &HTTPExecutor{}
	manager.executors["mcp"] = &MCPExecutor{}
	return manager
}

// SetMCPToolCaller wires the MCP bridge used by mcp_tool handlers.
func (m *Manager) SetMCPToolCaller(caller MCPToolCaller) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if executor, ok := m.executors["mcp"].(*MCPExecutor); ok {
		executor.SetCall(caller)
	}
}

// SetAsyncObserver installs a receiver for async hook results. Nil clears it.
func (m *Manager) SetAsyncObserver(observer AsyncObserver) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observer = observer
}

// SetContextSpiller installs the oversized-context handler.
func (m *Manager) SetContextSpiller(spiller ContextSpiller) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spiller = spiller
}

// SetStatusObserver installs the synchronous hook lifecycle observer.
func (m *Manager) SetStatusObserver(observer StatusObserver) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = observer
}

// Hooks returns a copy of the configured handlers (diagnostics/tests).
func (m *Manager) Hooks() []HookConfig {
	if m == nil {
		return nil
	}
	return append([]HookConfig(nil), m.hooks...)
}

// MatchingHooks returns the handlers that would actually run for this event and
// payload: same enabled/matcher filtering Dispatch applies, but no execution.
//
// Diagnostics previously counted handlers by event name alone, so a hook with a
// non-matching matcher (or a disabled one) was reported as "matched" and the
// operator concluded the payload was wrong when the matcher was.
func (m *Manager) MatchingHooks(event Event, payload map[string]interface{}) []HookConfig {
	if m == nil {
		return nil
	}
	payload = PrepareEventPayload(event, payload)
	var matched []HookConfig
	for _, hook := range m.hooks {
		if hook.Event != event || !hookEnabled(hook) || !matchesHook(hook, payload) {
			continue
		}
		matched = append(matched, hook)
	}
	return matched
}

// Dispatch executes matching hooks and returns a combined decision.
func (m *Manager) Dispatch(ctx context.Context, event Event, payload map[string]interface{}) (Decision, error) {
	if m == nil || len(m.hooks) == 0 {
		return Decision{Action: DecisionContinue}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Normalize once for every caller. Hosts build payloads for telemetry, so
	// hook_event_name (validated by the output parser) and event-specific
	// matcher inputs (for example compaction's trigger) may be missing.
	payload = PrepareEventPayload(event, payload)
	decision := Decision{Action: DecisionContinue}
	for _, hook := range m.hooks {
		if hook.Event != event {
			continue
		}
		if !hookEnabled(hook) {
			continue
		}
		if !matchesHook(hook, payload) {
			continue
		}
		// Async handlers are observational only. Control effects are ignored;
		// the runtime keeps the turn moving and the observer receives the
		// parsed result later.
		if hook.Async {
			m.dispatchAsyncHook(ctx, hook, payload)
			continue
		}
		executor := m.executors[strings.ToLower(strings.TrimSpace(hook.Exec.Type))]
		if executor == nil {
			errorDecision, handled := m.handleHookError(hook, "unknown hook executor")
			if handled {
				return errorDecision, nil
			}
			continue
		}
		m.notifyStatus(hook, "started")
		hookDecision, err := m.executeHook(ctx, executor, hook, payload)
		if err != nil {
			m.notifyStatus(hook, "failed")
			errorDecision, handled := m.handleHookError(hook, err.Error())
			if !handled {
				continue
			}
			return errorDecision, nil
		}
		m.notifyStatus(hook, "completed")
		hookDecision = m.applyContextSpill(ctx, hook, hookDecision)
		mergeDecision(&decision, hookDecision)
		if decision.Action == DecisionBlock {
			return decision, nil
		}
	}
	return decision, nil
}

// DispatchAsync triggers hooks asynchronously.
func (m *Manager) DispatchAsync(ctx context.Context, event Event, payload map[string]interface{}) {
	go func() {
		_, _ = m.Dispatch(ctx, event, payload)
	}()
}

// DispatchSync executes only synchronous handlers and returns their combined
// decision. It is used by events whose result must be known before the turn
// continues (user prompt, session start, stop, tool use).
func (m *Manager) DispatchSync(ctx context.Context, event Event, payload map[string]interface{}) (Decision, error) {
	if m == nil || len(m.hooks) == 0 {
		return Decision{Action: DecisionContinue}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload = PrepareEventPayload(event, payload)
	decision := Decision{Action: DecisionContinue}
	for _, hook := range m.hooks {
		if hook.Event != event || hook.Async || !hookEnabled(hook) || !matchesHook(hook, payload) {
			continue
		}
		executor := m.executors[strings.ToLower(strings.TrimSpace(hook.Exec.Type))]
		if executor == nil {
			if errorDecision, handled := m.handleHookError(hook, "unknown hook executor"); handled {
				return errorDecision, nil
			}
			continue
		}
		m.notifyStatus(hook, "started")
		hookDecision, err := m.executeHook(ctx, executor, hook, payload)
		if err != nil {
			m.notifyStatus(hook, "failed")
			if errorDecision, handled := m.handleHookError(hook, err.Error()); handled {
				return errorDecision, nil
			}
			continue
		}
		m.notifyStatus(hook, "completed")
		hookDecision = m.applyContextSpill(ctx, hook, hookDecision)
		mergeDecision(&decision, hookDecision)
		if decision.Action == DecisionBlock || decision.Action == DecisionStop {
			return decision, nil
		}
	}
	return decision, nil
}

func (m *Manager) dispatchAsyncHook(ctx context.Context, hook HookConfig, payload map[string]interface{}) {
	executor := m.executors[strings.ToLower(strings.TrimSpace(hook.Exec.Type))]
	if executor == nil {
		return
	}
	m.mu.RLock()
	observer := m.observer
	m.mu.RUnlock()
	go func() {
		hookDecision, err := m.executeHook(ctx, executor, hook, payload)
		if observer != nil {
			observer(AsyncResult{Hook: hook, Decision: hookDecision, Err: err})
		}
	}()
}

func (m *Manager) executeHook(ctx context.Context, executor Executor, hook HookConfig, payload map[string]interface{}) (Decision, error) {
	rawExecutor, ok := executor.(RawExecutor)
	if !ok {
		return executor.Execute(ctx, hook, payload)
	}
	raw, err := rawExecutor.ExecuteRaw(ctx, hook, payload)
	if err != nil {
		return Decision{}, err
	}
	// Exit code 2 is a protocol-level block; any other non-zero status is a
	// handler failure and must flow through on_error (fail_open/fail_closed).
	if raw.ExitCode != 0 && raw.ExitCode != 2 {
		return Decision{}, fmt.Errorf("hook exited with code %d", raw.ExitCode)
	}
	output := ParseCodexOutput(hook.Event, raw.Stdout, raw.Stderr, raw.ExitCode)
	if output.InvalidReason != "" {
		// Invalid control output is non-blocking by default. fail_closed
		// handlers still block through handleHookError in the caller.
		return Decision{Action: DecisionContinue, Message: output.InvalidReason}, nil
	}
	if hook.Event == EventPermissionRequest {
		return output.ToPermissionDecision(), nil
	}
	return output.ToDecision(), nil
}

func (m *Manager) applyContextSpill(ctx context.Context, hook HookConfig, decision Decision) Decision {
	if m == nil || len(decision.ExtraContext) == 0 {
		return decision
	}
	text := strings.TrimSpace(decision.ExtraContext["additional_context"])
	if text == "" {
		return decision
	}
	m.mu.RLock()
	spiller := m.spiller
	m.mu.RUnlock()
	if spiller == nil {
		return decision
	}
	limit := hook.AdditionalContextLimit
	if limit < 0 {
		return decision
	}
	if replaced, err := spiller(ctx, text, limit); err == nil && strings.TrimSpace(replaced) != "" {
		decision.ExtraContext["additional_context"] = replaced
	}
	return decision
}

func (m *Manager) notifyStatus(hook HookConfig, phase string) {
	if m == nil {
		return
	}
	m.mu.RLock()
	observer := m.status
	m.mu.RUnlock()
	if observer != nil {
		observer(hook, phase)
	}
	if m.recorder != nil {
		m.recorder.Record(RunRecord{
			HookID:        hook.ID,
			Event:         string(hook.Event),
			Phase:         phase,
			StatusMessage: hook.StatusMessage,
			Source:        hook.Source,
		})
	}
}

// RecentRuns returns the bounded process-local hook run history.
func (m *Manager) RecentRuns() []RunRecord {
	if m == nil || m.recorder == nil {
		return nil
	}
	return m.recorder.Recent()
}

func mergeDecision(target *Decision, next Decision) {
	if target == nil {
		return
	}
	// Terminal decisions are first-wins: a later handler must not downgrade a
	// stop/block into a softer action.
	if target.Action == DecisionStop || target.Action == DecisionBlock {
		return
	}
	if next.Action == DecisionBlock {
		target.Action = DecisionBlock
		target.Message = next.Message
		return
	}
	if next.Action == DecisionStop {
		target.Action = DecisionStop
		target.Message = next.Message
		return
	}
	if next.Action == DecisionModify && len(next.PatchedPayload) > 0 {
		target.Action = DecisionModify
		target.PatchedPayload = next.PatchedPayload
	}
	if strings.TrimSpace(next.Message) != "" {
		if strings.TrimSpace(target.Message) == "" {
			target.Message = strings.TrimSpace(next.Message)
		} else {
			target.Message += "\n" + strings.TrimSpace(next.Message)
		}
	}
	if len(next.ExtraContext) > 0 {
		if target.ExtraContext == nil {
			target.ExtraContext = make(map[string]string, len(next.ExtraContext))
		}
		for key, value := range next.ExtraContext {
			target.ExtraContext[key] = value
		}
	}
	switch next.Action {
	case DecisionEnrich:
		// Enrichment adds context but must not mask a modify: updatedInput is
		// a stronger control effect and dropping it would silently execute the
		// original args.
		if target.Action == DecisionContinue || target.Action == DecisionNotify {
			target.Action = DecisionEnrich
		}
	case DecisionNotify:
		if target.Action == DecisionContinue {
			target.Action = DecisionNotify
		}
	}
}

func hookEnabled(hook HookConfig) bool {
	return hook.Enabled == nil || *hook.Enabled
}

func (m *Manager) handleHookError(hook HookConfig, message string) (Decision, bool) {
	onError := strings.ToLower(strings.TrimSpace(hook.OnError))
	if onError == "fail_closed" {
		return Decision{Action: DecisionBlock, Message: message}, true
	}
	return Decision{Action: DecisionContinue}, false
}

func parseDecision(data []byte) (Decision, error) {
	if len(data) == 0 {
		return Decision{Action: DecisionContinue}, nil
	}
	var decoded struct {
		Action         string            `json:"action"`
		Message        string            `json:"message"`
		PatchedPayload json.RawMessage   `json:"patched_payload"`
		ExtraContext   map[string]string `json:"extra_context"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return Decision{Action: DecisionContinue}, nil
	}
	action := DecisionAction(strings.ToLower(strings.TrimSpace(decoded.Action)))
	if action == "" {
		action = DecisionContinue
	}
	return Decision{
		Action:         action,
		Message:        strings.TrimSpace(decoded.Message),
		PatchedPayload: decoded.PatchedPayload,
		ExtraContext:   decoded.ExtraContext,
	}, nil
}

var (
	hookMatcherRegexMu    sync.RWMutex
	hookMatcherRegexCache = map[string]*regexp.Regexp{}
)

// matchCodexMatcher implements Codex's matcher contract:
//   - empty or "*" matches everything;
//   - only [A-Za-z0-9_|] is treated as exact/alias matching;
//   - otherwise the pattern is a regular expression.
func matchCodexMatcher(pattern, input string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "*" {
		return true
	}
	if isSimpleMatcher(pattern) {
		for _, candidate := range strings.Split(pattern, "|") {
			if candidate == input {
				return true
			}
		}
		return false
	}
	hookMatcherRegexMu.RLock()
	re, ok := hookMatcherRegexCache[pattern]
	hookMatcherRegexMu.RUnlock()
	if !ok {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return false
		}
		hookMatcherRegexMu.Lock()
		hookMatcherRegexCache[pattern] = compiled
		hookMatcherRegexMu.Unlock()
		re = compiled
	}
	return re.MatchString(input)
}

func isSimpleMatcher(pattern string) bool {
	for _, ch := range pattern {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '_' || ch == '|' {
			continue
		}
		return false
	}
	return true
}
