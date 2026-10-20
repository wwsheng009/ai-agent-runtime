package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// MCPToolCaller is the minimal bridge to an MCP manager. Hosts inject it so
// the hooks package does not depend on the skill/MCP implementation.
type MCPToolCaller func(ctx context.Context, server, tool string, input map[string]interface{}) (interface{}, error)

// MCPExecutor executes a Codex mcp_tool handler.
type MCPExecutor struct {
	mu   sync.RWMutex
	Call MCPToolCaller
}

// SetCall replaces the MCP bridge safely.
func (e *MCPExecutor) SetCall(caller MCPToolCaller) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Call = caller
}

func (e *MCPExecutor) caller() MCPToolCaller {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.Call
}

// Execute runs the MCP tool and parses its output as a hook decision.
func (e *MCPExecutor) Execute(ctx context.Context, hook HookConfig, payload map[string]interface{}) (Decision, error) {
	raw, err := e.ExecuteRaw(ctx, hook, payload)
	if err != nil {
		return Decision{}, err
	}
	return parseDecision([]byte(raw.Stdout))
}

// ExecuteRaw calls the MCP tool and serializes its result. The input is the
// handler's configured input merged with the hook payload under "hook_input",
// so tools can either use static args or inspect the event.
func (e *MCPExecutor) ExecuteRaw(ctx context.Context, hook HookConfig, payload map[string]interface{}) (RawResult, error) {
	call := e.caller()
	if call == nil {
		return RawResult{}, fmt.Errorf("mcp hook caller is not configured")
	}
	server := strings.TrimSpace(hook.Exec.Server)
	tool := strings.TrimSpace(hook.Exec.Tool)
	if server == "" || tool == "" {
		return RawResult{}, fmt.Errorf("mcp hook server/tool is required")
	}
	input := map[string]interface{}{}
	for key, value := range hook.Exec.Input {
		input[key] = value
	}
	input["hook_input"] = payload
	result, err := call(ctx, server, tool, input)
	if err != nil {
		return RawResult{}, err
	}
	stdout, err := mcpResultText(result)
	if err != nil {
		return RawResult{}, err
	}
	return RawResult{Stdout: stdout}, nil
}

// mcpResultText normalizes a host MCP call result into the hook decision text.
//
// Hosts hand back one of three shapes:
//   - a plain string: skill.MCPAdapter already extracted the text content;
//   - a protocol envelope: {"content":[{"type":"text","text":"..."}]};
//   - an arbitrary value (tests / custom hosts).
//
// Only the first two carry a usable decision verbatim. Marshaling them as-is
// would produce a quoted JSON string (or an envelope object) that the decision
// parser cannot read, silently turning every MCP hook into a no-op.
func mcpResultText(result interface{}) (string, error) {
	switch value := result.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	case []byte:
		return string(value), nil
	case json.RawMessage:
		return string(value), nil
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal mcp hook result: %w", err)
	}
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(encoded, &envelope) == nil && len(envelope.Content) > 0 {
		var text strings.Builder
		for _, item := range envelope.Content {
			if item.Type == "text" {
				text.WriteString(item.Text)
			}
		}
		if strings.TrimSpace(text.String()) != "" {
			return text.String(), nil
		}
	}
	return string(encoded), nil
}
