package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolexec"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type circuitMCPManager struct {
	calls int
}

func (m *circuitMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{
		Name:    toolName,
		Enabled: true,
		MCPName: "mock-mcp",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]interface{}{"type": "string"},
			},
			"required": []interface{}{"file_path"},
		},
		Metadata: map[string]interface{}{
			"retry_class": "safe",
		},
	}, nil
}

func (m *circuitMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	m.calls++
	return nil, fmt.Errorf("path not found: %v", args["file_path"])
}

func (m *circuitMCPManager) ListTools() []skill.ToolInfo {
	info, _ := m.FindTool("view")
	return []skill.ToolInfo{info}
}

func TestAct_CircuitOpensAfterIdenticalTerminalFailures(t *testing.T) {
	mcp := &circuitMCPManager{}
	agent := &Agent{
		config:     &Config{Name: "circuit-test"},
		mcpManager: mcp,
	}
	loop := NewReActLoop(agent, nil, &LoopReActConfig{MaxSteps: 5})

	call := types.ToolCall{
		ID:   "tc-1",
		Name: "view",
		Args: map[string]interface{}{"file_path": "does-not-exist-for-circuit.txt"},
	}

	// Two identical terminal path preflight failures open the run-scoped circuit.
	// Safe read tools never reach CallTool for missing paths.
	for i := 0; i < 2; i++ {
		results, err := loop.act(context.Background(), "trace-circuit", "session-circuit", i+1, 0, nil, []types.ToolCall{call}, nil)
		if err != nil {
			t.Fatalf("act %d: %v", i+1, err)
		}
		if len(results) != 1 || strings.TrimSpace(results[0].Error) == "" {
			t.Fatalf("act %d expected tool error, got %+v", i+1, results)
		}
		if !strings.Contains(strings.ToLower(results[0].Error), "path not found") {
			t.Fatalf("act %d unexpected error: %s", i+1, results[0].Error)
		}
		if results[0].Envelope == nil || results[0].Envelope.Metadata == nil {
			t.Fatalf("act %d missing envelope metadata", i+1)
		}
		meta := results[0].Envelope.Metadata
		if code, _ := meta[toolresult.MetadataErrorCodeKey].(string); code != "TOOL_PATH_NOT_FOUND" {
			t.Fatalf("act %d expected TOOL_PATH_NOT_FOUND, got %q meta=%v", i+1, code, meta)
		}
		if preflight, _ := meta[toolexec.MetadataPreflightKey].(string); preflight != "path_existence" {
			t.Fatalf("act %d expected path_existence preflight, got %q", i+1, preflight)
		}
	}
	if mcp.calls != 0 {
		t.Fatalf("path preflight should skip tool execution, calls=%d", mcp.calls)
	}

	// Third identical call should be blocked by circuit without executing the tool.
	results, err := loop.act(context.Background(), "trace-circuit", "session-circuit", 3, 0, nil, []types.ToolCall{call}, nil)
	if err != nil {
		t.Fatalf("act 3: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if mcp.calls != 0 {
		t.Fatalf("circuit should prevent tool execution, calls=%d", mcp.calls)
	}
	if results[0].Envelope == nil || results[0].Envelope.Metadata == nil {
		t.Fatal("expected envelope metadata on circuit block")
	}
	meta := results[0].Envelope.Metadata
	if open, _ := meta[toolexec.MetadataCircuitOpenKey].(bool); !open {
		if preflight, _ := meta[toolexec.MetadataPreflightKey].(string); preflight != "circuit_open" {
			t.Fatalf("expected circuit_open metadata, got %+v", meta)
		}
	}
	if preflight, _ := meta[toolexec.MetadataPreflightKey].(string); preflight != "circuit_open" {
		t.Fatalf("expected preflight=circuit_open, got %q", preflight)
	}
	next, _ := meta[toolresult.MetadataNextActionKey].(string)
	if !strings.Contains(strings.ToLower(next), "do not retry") {
		t.Fatalf("expected strengthened next_action, got %q", next)
	}
}

func TestPrepareToolExecution_MissingRequiredArgs(t *testing.T) {
	loop := NewReActLoop(&Agent{}, nil, nil)
	metadata := map[string]interface{}{}
	info := &skill.ToolInfo{
		Name: "glob",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"pattern"},
			"properties": map[string]interface{}{
				"pattern": map[string]interface{}{"type": "string"},
			},
		},
	}
	decision := loop.prepareToolExecution(metadata, "glob", "tc-missing", map[string]interface{}{}, info)
	if decision.Allow {
		t.Fatal("expected missing required args to deny")
	}
	if decision.ErrorCode != "TOOL_INVALID_ARGS" {
		t.Fatalf("error_code=%s", decision.ErrorCode)
	}
	if metadata[toolexec.MetadataArgumentsDigestKey] == nil {
		t.Fatal("expected arguments_digest metadata")
	}
}

func TestToolWorkspaceRootPrefersToolBasePath(t *testing.T) {
	loop := NewReActLoop(&Agent{
		config: &Config{
			Options: map[string]interface{}{
				"tool_base_path": "/tools/root",
				"workspace_path": "/context/root",
			},
		},
	}, nil, nil)
	if got := loop.toolWorkspaceRoot(); got != "/tools/root" {
		t.Fatalf("tool_base_path should win, got %q", got)
	}

	loop = NewReActLoop(&Agent{
		config: &Config{
			Options: map[string]interface{}{
				"workspace_path": "/context/root",
			},
		},
	}, nil, nil)
	if got := loop.toolWorkspaceRoot(); got != "/context/root" {
		t.Fatalf("workspace_path fallback missing, got %q", got)
	}

	loop = NewReActLoop(&Agent{config: &Config{}}, nil, nil)
	if got := loop.toolWorkspaceRoot(); got != "" {
		t.Fatalf("expected empty root without options, got %q", got)
	}
}

// emptySoftMCPManager returns successful empty results with explicit empty_result
// metadata so RecordOutcome can open the soft negative cache.
type emptySoftMCPManager struct {
	calls int
}

func (m *emptySoftMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{
		Name:    toolName,
		Enabled: true,
		MCPName: "mock-mcp",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]interface{}{"type": "string"},
			},
			"required": []interface{}{"pattern"},
		},
	}, nil
}

func (m *emptySoftMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	m.calls++
	return "no matches", nil
}

func (m *emptySoftMCPManager) CallToolWithMeta(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	m.calls++
	return "no matches", map[string]interface{}{
		toolresult.MetadataEmptyResultKey: true,
		toolresult.MetadataOutcomeKey:     toolresult.OutcomeEmpty,
		"match_count":                     0,
	}, nil
}

func (m *emptySoftMCPManager) ListTools() []skill.ToolInfo {
	info, _ := m.FindTool("grep")
	return []skill.ToolInfo{info}
}

func TestAct_EmptySoftCacheShortCircuitsWithoutToolExec(t *testing.T) {
	mcp := &emptySoftMCPManager{}
	agent := &Agent{
		config:     &Config{Name: "empty-soft-test"},
		mcpManager: mcp,
	}
	loop := NewReActLoop(agent, nil, &LoopReActConfig{MaxSteps: 5})

	call := types.ToolCall{
		ID:   "tc-empty",
		Name: "grep",
		Args: map[string]interface{}{"pattern": "definitely-not-present-xyz"},
	}

	// Two identical empty successes open the soft negative cache.
	for i := 0; i < 2; i++ {
		results, err := loop.act(context.Background(), "trace-empty", "session-empty", i+1, 0, nil, []types.ToolCall{call}, nil)
		if err != nil {
			t.Fatalf("act %d: %v", i+1, err)
		}
		if len(results) != 1 {
			t.Fatalf("act %d expected 1 result, got %d", i+1, len(results))
		}
		if strings.TrimSpace(results[0].Error) != "" {
			t.Fatalf("act %d expected success (empty), got error %q", i+1, results[0].Error)
		}
		if results[0].Envelope == nil || results[0].Envelope.Metadata == nil {
			t.Fatalf("act %d missing envelope metadata", i+1)
		}
		meta := results[0].Envelope.Metadata
		if empty, _ := meta[toolresult.MetadataEmptyResultKey].(bool); !empty {
			// Gateway may promote from tool_metadata/match_count; require outcome empty or empty_result.
			if outcome, _ := meta[toolresult.MetadataOutcomeKey].(string); outcome != toolresult.OutcomeEmpty {
				t.Fatalf("act %d expected empty disposition, meta=%v", i+1, meta)
			}
		}
	}
	if mcp.calls != 2 {
		t.Fatalf("expected 2 tool executions before soft cache open, calls=%d", mcp.calls)
	}

	// Third identical call: soft empty short-circuit, no tool exec, no hard deny.
	results, err := loop.act(context.Background(), "trace-empty", "session-empty", 3, 0, nil, []types.ToolCall{call}, nil)
	if err != nil {
		t.Fatalf("act 3: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if mcp.calls != 2 {
		t.Fatalf("soft empty must skip tool execution, calls=%d", mcp.calls)
	}
	if strings.TrimSpace(results[0].Error) != "" {
		t.Fatalf("soft empty must not set Error, got %q", results[0].Error)
	}
	if results[0].Envelope == nil || results[0].Envelope.Metadata == nil {
		t.Fatal("expected envelope metadata on empty replay")
	}
	meta := results[0].Envelope.Metadata
	if preflight, _ := meta[toolexec.MetadataPreflightKey].(string); preflight != "empty_replay" {
		t.Fatalf("expected preflight=empty_replay, got %q meta=%v", preflight, meta)
	}
	if replay, _ := meta[toolexec.MetadataEmptyReplayKey].(bool); !replay {
		t.Fatalf("expected empty_replay=true, meta=%v", meta)
	}
	if outcome, _ := meta[toolresult.MetadataOutcomeKey].(string); outcome != toolresult.OutcomeEmpty {
		t.Fatalf("expected outcome=empty, got %q", outcome)
	}
	if empty, _ := meta[toolresult.MetadataEmptyResultKey].(bool); !empty {
		t.Fatalf("expected empty_result=true, meta=%v", meta)
	}
	// Hard circuit / deny signals must stay off.
	if open, _ := meta[toolexec.MetadataCircuitOpenKey].(bool); open {
		t.Fatalf("soft empty must not set circuit_open: %v", meta)
	}
	if code, _ := meta[toolresult.MetadataErrorCodeKey].(string); strings.TrimSpace(code) != "" {
		t.Fatalf("soft empty must not set error_code, got %q", code)
	}
}

func TestApplySoftEmptyPreflightResult_StampsSuccessDisposition(t *testing.T) {
	result := toolExecutionResult{}
	metadata := map[string]interface{}{}
	decision := toolexec.PreflightDecision{
		Allow:      false,
		SoftEmpty:  true,
		Preflight:  "empty_replay",
		NextAction: "Broaden inputs or proceed.",
		Args:       map[string]interface{}{"pattern": "x"},
	}
	applySoftEmptyPreflightResult(&result, metadata, decision)
	if strings.TrimSpace(result.Error) != "" {
		t.Fatalf("Error must stay empty, got %q", result.Error)
	}
	if !strings.Contains(result.Output.(string), "Broaden") {
		t.Fatalf("expected next_action as output body, got %#v", result.Output)
	}
	if metadata[toolexec.MetadataEmptyReplayKey] != true {
		t.Fatalf("expected empty_replay, got %#v", metadata)
	}
	if metadata[toolresult.MetadataOutcomeKey] != toolresult.OutcomeEmpty {
		t.Fatalf("expected outcome=empty, got %#v", metadata)
	}
	if metadata[toolresult.MetadataEmptyResultKey] != true {
		t.Fatalf("expected empty_result, got %#v", metadata)
	}
}
