package prompt

import (
	"strings"
	"testing"
)

func TestToolGuidanceFactsNilKeepsLegacyOutput(t *testing.T) {
	var nilFacts *ToolGuidanceFacts
	if got := RenderShellExecutionGuidanceFacts(nilFacts, ""); got != RenderShellExecutionGuidanceWithCapability("") {
		t.Fatal("nil facts must keep legacy shell guidance")
	}
	if got := RenderShellExecutionGuidanceFacts(nilFacts, "cap"); got != RenderShellExecutionGuidanceWithCapability("cap") {
		t.Fatal("nil facts must keep legacy shell guidance with capability")
	}
	if got := RenderFileEditingGuidanceFacts(nilFacts); got != RenderFileEditingGuidance() {
		t.Fatal("nil facts must keep legacy file editing guidance")
	}
	if got := RenderParallelToolGuidanceFacts(nilFacts); got != RenderParallelToolGuidance() {
		t.Fatal("nil facts must keep legacy parallel guidance")
	}
	if got := RenderTaskDifficultyGuidanceFacts(nilFacts); got != RenderTaskDifficultyGuidance() {
		t.Fatal("nil facts must keep legacy task difficulty guidance")
	}
	if got := RenderMultiAgentCollaborationGuidanceFacts(nilFacts); got != RenderMultiAgentCollaborationGuidance() {
		t.Fatal("nil facts must keep legacy multi-agent guidance")
	}
}

func TestRenderShellExecutionGuidanceFactsGatesOnShellFamily(t *testing.T) {
	readOnly := &ToolGuidanceFacts{
		Denied: map[string]bool{"write": true, "edit": true, "multiedit": true, "append_write": true, "apply_patch": true},
	}
	if RenderShellExecutionGuidanceFacts(readOnly, "") == "" {
		t.Fatal("read-only surface still has shell/read tools: guidance must stay")
	}

	noShell := &ToolGuidanceFacts{
		Denied: map[string]bool{
			"shell": true, "bash": true, "execute_shell_command": true, "aicli_exec": true,
			"grep": true, "rg": true, "view": true, "read": true, "ls": true, "glob": true,
		},
	}
	if got := RenderShellExecutionGuidanceFacts(noShell, ""); got != "" {
		t.Fatal("shell-family absence must withhold shell guidance, got non-empty")
	}
}

func TestRenderFileEditingGuidanceFactsGatesOnWriteTools(t *testing.T) {
	readOnly := &ToolGuidanceFacts{
		Denied: map[string]bool{"write": true, "edit": true, "multiedit": true, "append_write": true, "apply_patch": true},
	}
	if got := RenderFileEditingGuidanceFacts(readOnly); got != "" {
		t.Fatal("read-only surface must withhold file editing guidance, got non-empty")
	}

	writer := &ToolGuidanceFacts{}
	if RenderFileEditingGuidanceFacts(writer) == "" {
		t.Fatal("unrestricted surface must keep file editing guidance")
	}
}

func TestRenderParallelToolGuidanceFactsGatesOnBatchableTools(t *testing.T) {
	empty := &ToolGuidanceFacts{
		Denied: map[string]bool{
			"view": true, "read": true, "grep": true, "rg": true, "glob": true, "ls": true,
			"shell": true, "bash": true, "execute_shell_command": true, "aicli_exec": true,
		},
	}
	if got := RenderParallelToolGuidanceFacts(empty); got != "" {
		t.Fatal("no batchable tools: parallel guidance must be withheld, got non-empty")
	}
}

func TestDelegationAndMultiAgentGuidanceGateOnSpawnTools(t *testing.T) {
	noSpawn := &ToolGuidanceFacts{
		Denied: map[string]bool{
			"spawn_subagents": true, "spawn_agent": true,
			"spawn_team": true, "team_spawn": true,
			"wait_agent": true, "wait_team": true,
		},
	}
	if got := RenderTaskDifficultyGuidanceFacts(noSpawn); got != "" {
		t.Fatal("no spawn tools: delegation guidance must be withheld, got non-empty")
	}
	if got := RenderMultiAgentCollaborationGuidanceFacts(noSpawn); got != "" {
		t.Fatal("no orchestration tools: collaboration guidance must be withheld, got non-empty")
	}

	spawnOnly := &ToolGuidanceFacts{Only: map[string]bool{"spawn_subagents": true, "view": true}}
	if RenderTaskDifficultyGuidanceFacts(spawnOnly) == "" {
		t.Fatal("allowlist containing spawn_subagents must keep delegation guidance")
	}
	if RenderMultiAgentCollaborationGuidanceFacts(spawnOnly) == "" {
		t.Fatal("allowlist containing spawn_subagents must keep collaboration guidance")
	}

	bare := &ToolGuidanceFacts{Only: map[string]bool{"view": true}}
	if got := RenderTaskDifficultyGuidanceFacts(bare); got != "" {
		t.Fatal("allowlist without spawn tools must withhold delegation guidance")
	}
}

func TestToolGuidanceFactsDenyWinsOverOnly(t *testing.T) {
	facts := &ToolGuidanceFacts{
		Only:   map[string]bool{"shell": true, "view": true},
		Denied: map[string]bool{"shell": true},
	}
	if facts.any("shell") {
		t.Fatal("deny must win over only")
	}
	if !facts.any("view") {
		t.Fatal("only member not denied must stay visible")
	}
	if !strings.Contains(RenderFileEditingGuidance(), "apply_patch") {
		t.Fatal("sanity: legacy file guidance mentions apply_patch")
	}
}
