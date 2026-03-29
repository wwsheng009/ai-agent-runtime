package hooks

import (
	"context"
	"encoding/json"
	"strings"
)

// Manager dispatches hook events to configured executors.
type Manager struct {
	hooks     []HookConfig
	executors map[string]Executor
}

// Executor executes a hook action.
type Executor interface {
	Execute(ctx context.Context, hook HookConfig, payload map[string]interface{}) (Decision, error)
}

// NewManager creates a new hook manager.
func NewManager(hooks []HookConfig) *Manager {
	manager := &Manager{
		hooks:     append([]HookConfig(nil), hooks...),
		executors: make(map[string]Executor),
	}
	manager.executors["shell"] = &ShellExecutor{}
	manager.executors["http"] = &HTTPExecutor{}
	return manager
}

// Dispatch executes matching hooks and returns a combined decision.
func (m *Manager) Dispatch(ctx context.Context, event Event, payload map[string]interface{}) (Decision, error) {
	if m == nil || len(m.hooks) == 0 {
		return Decision{Action: DecisionContinue}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	decision := Decision{Action: DecisionContinue}
	for _, hook := range m.hooks {
		if hook.Event != event {
			continue
		}
		if !matchesHook(hook, payload) {
			continue
		}
		executor := m.executors[strings.ToLower(strings.TrimSpace(hook.Exec.Type))]
		if executor == nil {
			return m.handleHookError(hook, "unknown hook executor")
		}
		hookDecision, err := executor.Execute(ctx, hook, payload)
		if err != nil {
			return m.handleHookError(hook, err.Error())
		}
		if hookDecision.Action == DecisionBlock {
			return hookDecision, nil
		}
		if hookDecision.Action == DecisionModify && len(hookDecision.PatchedPayload) > 0 {
			decision = hookDecision
			continue
		}
		if strings.TrimSpace(hookDecision.Message) != "" {
			if decision.Message == "" {
				decision.Message = strings.TrimSpace(hookDecision.Message)
			} else {
				decision.Message += "\n" + strings.TrimSpace(hookDecision.Message)
			}
		}
		if len(hookDecision.ExtraContext) > 0 {
			if decision.ExtraContext == nil {
				decision.ExtraContext = make(map[string]string, len(hookDecision.ExtraContext))
			}
			for key, value := range hookDecision.ExtraContext {
				decision.ExtraContext[key] = value
			}
		}
		if decision.Action != DecisionModify {
			switch hookDecision.Action {
			case DecisionEnrich:
				decision.Action = DecisionEnrich
			case DecisionNotify:
				if decision.Action == DecisionContinue {
					decision.Action = DecisionNotify
				}
			}
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

func (m *Manager) handleHookError(hook HookConfig, message string) (Decision, error) {
	onError := strings.ToLower(strings.TrimSpace(hook.OnError))
	if onError == "fail_closed" {
		return Decision{Action: DecisionBlock, Message: message}, nil
	}
	return Decision{Action: DecisionContinue}, nil
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
