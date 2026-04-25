package prompt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLayersCompileInstructionMessages_PreservesDeveloperForCodex(t *testing.T) {
	layers := NewLayers()
	layers.AddLayer(LayerBase, "Profile System", "System guardrails", "system.md")
	layers.AddLayer(LayerDeveloper, "Profile Tools", "Prefer rg when available", "tools.md")
	layers.AddLayer(LayerUser, "Workspace Instructions", "Stay inside the repo", "AGENTS.md")

	messages := layers.CompileInstructionMessages("codex")
	if len(messages) != 3 {
		t.Fatalf("expected 3 instruction messages, got %#v", messages)
	}
	if messages[0].Role != "system" {
		t.Fatalf("expected first message role system, got %q", messages[0].Role)
	}
	if messages[1].Role != "developer" {
		t.Fatalf("expected second message role developer, got %q", messages[1].Role)
	}
	if messages[2].Role != "developer" {
		t.Fatalf("expected third message role developer, got %q", messages[2].Role)
	}
	if got := messages[0].Metadata["prompt_layer"]; got != LayerBase {
		t.Fatalf("expected base layer metadata, got %#v", got)
	}
	if got := messages[1].Metadata["prompt_layer"]; got != LayerDeveloper {
		t.Fatalf("expected developer layer metadata, got %#v", got)
	}
	if got := messages[2].Metadata["prompt_layer"]; got != LayerUser {
		t.Fatalf("expected user layer metadata, got %#v", got)
	}
}

func TestLayersCompileInstructionMessages_CollapsesDeveloperForAnthropic(t *testing.T) {
	layers := NewLayers()
	layers.AddLayer(LayerBase, "Profile System", "System guardrails", "")
	layers.AddLayer(LayerUser, "Workspace Instructions", "Stay inside the repo", "AGENTS.md")

	messages := layers.CompileInstructionMessages("anthropic")
	if len(messages) != 2 {
		t.Fatalf("expected instruction layers to preserve boundaries, got %#v", messages)
	}
	if messages[0].Role != "system" {
		t.Fatalf("expected first collapsed system message, got %q", messages[0].Role)
	}
	if messages[1].Role != "system" {
		t.Fatalf("expected second collapsed system message, got %q", messages[1].Role)
	}
	if messages[0].Metadata["prompt_layer"] != LayerBase {
		t.Fatalf("expected base layer metadata, got %#v", messages[0].Metadata["prompt_layer"])
	}
	if messages[1].Metadata["prompt_layer"] != LayerUser {
		t.Fatalf("expected user layer metadata, got %#v", messages[1].Metadata["prompt_layer"])
	}
}

func TestLayersRenderModelVisibleLayout_IncludesStableLayerMarkers(t *testing.T) {
	layers := NewLayers()
	layers.AddLayer(LayerBase, "Base", "Base guardrail", "system.md")
	layers.AddLayer(LayerDeveloper, "Tools", "Prefer rg", "tools.md")
	layers.AddLayer(LayerUser, "Workspace", "Read AGENTS", "AGENTS.md")

	layout := layers.RenderModelVisibleLayout("codex")
	expected := `[base/system]
# Base
Source: system.md
Base guardrail

[developer/developer]
# Tools
Source: tools.md
Prefer rg

[user/developer]
# Workspace
Source: AGENTS.md
Read AGENTS`
	if layout != expected {
		t.Fatalf("unexpected model-visible layout:\n%s", layout)
	}
}

func TestLoadWorkspaceInstructions_PrefersOverrideAndWalksAncestors(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "repo")
	workDir := filepath.Join(projectDir, "pkg", "feature")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	writePromptFile(t, filepath.Join(projectDir, "AGENTS.md"), "Root instructions")
	writePromptFile(t, filepath.Join(workDir, "AGENTS.override.md"), "Feature override")

	layers, err := LoadWorkspaceInstructions(workDir, 4096)
	if err != nil {
		t.Fatalf("LoadWorkspaceInstructions: %v", err)
	}
	if layers == nil || len(layers.Fragments) != 2 {
		t.Fatalf("expected 2 workspace instruction fragments, got %#v", layers)
	}
	if layers.Fragments[0].Layer != LayerUser || layers.Fragments[1].Layer != LayerUser {
		t.Fatalf("expected workspace instructions to use user layer, got %#v", layers.Fragments)
	}
	if layers.Fragments[0].Source != filepath.Join(projectDir, "AGENTS.md") {
		t.Fatalf("expected root AGENTS first, got %#v", layers.Fragments[0])
	}
	if layers.Fragments[1].Source != filepath.Join(workDir, "AGENTS.override.md") {
		t.Fatalf("expected override file to win in leaf dir, got %#v", layers.Fragments[1])
	}
}
