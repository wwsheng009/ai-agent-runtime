package profileinput

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

)

func TestBuildResolvedAgentInputs_LoadsPromptsAndToolPolicy(t *testing.T) {
	dir := t.TempDir()
	systemPath := filepath.Join(dir, "system.md")
	toolsPath := filepath.Join(dir, "tools.md")
	if err := os.WriteFile(systemPath, []byte("System prompt."), 0o644); err != nil {
		t.Fatalf("write system prompt: %v", err)
	}
	if err := os.WriteFile(toolsPath, []byte("Use tools carefully."), 0o644); err != nil {
		t.Fatalf("write tools prompt: %v", err)
	}

	readOnly := true
	inputs, err := BuildResolvedAgentInputs(&ResolvedAgent{
		Prompts: ResolvedPromptFiles{
			System: systemPath,
			Tools:  toolsPath,
		},
		ToolPolicy: ResolvedToolPolicy{
			Allowlist: []string{"read_file"},
			Denylist:  []string{"write_file"},
			ReadOnly:  &readOnly,
			Sandbox: map[string]interface{}{
				"allowedPaths": []string{"."},
			},
		},
	})
	if err != nil {
		t.Fatalf("BuildResolvedAgentInputs: %v", err)
	}
	if inputs.PromptText != "# System\nSystem prompt.\n\n# Tools\nUse tools carefully." {
		t.Fatalf("unexpected prompt text:\n%s", inputs.PromptText)
	}
	if inputs.ToolPolicy == nil {
		t.Fatal("expected tool policy")
	}
	if !inputs.ToolPolicy.ReadOnly {
		t.Fatal("expected read-only tool policy")
	}
	if err := inputs.ToolPolicy.AllowTool("read_file"); err != nil {
		t.Fatalf("expected read_file to be allowed: %v", err)
	}
	if err := inputs.ToolPolicy.AllowTool("write_file"); err == nil {
		t.Fatal("expected write_file to be blocked")
	}
	if inputs.ToolPolicy.Sandbox == nil {
		t.Fatal("expected sandbox to be created")
	}
}

func TestBuildResolvedAgentInputs_LoadsProfileResourcesIntoPromptAndContext(t *testing.T) {
	dir := t.TempDir()
	memoryPath := filepath.Join(dir, "memory", "memory.json")
	notesPath := filepath.Join(dir, "context", "notes.md")
	if err := os.MkdirAll(filepath.Dir(memoryPath), 0o755); err != nil {
		t.Fatalf("mkdir memory: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(notesPath), 0o755); err != nil {
		t.Fatalf("mkdir context: %v", err)
	}
	if err := os.WriteFile(memoryPath, []byte(`{"summary":"cached profile memory"}`), 0o644); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	if err := os.WriteFile(notesPath, []byte("Profile investigation notes."), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}

	inputs, err := BuildResolvedAgentInputs(&ResolvedAgent{
		Paths: ResolvedPaths{
			MemoryFile:       memoryPath,
			ContextNotesFile: notesPath,
		},
	})
	if err != nil {
		t.Fatalf("BuildResolvedAgentInputs: %v", err)
	}
	if inputs.ContextText == "" {
		t.Fatal("expected context text")
	}
	if !strings.Contains(inputs.PromptText, "cached profile memory") {
		t.Fatalf("expected memory preview in prompt:\n%s", inputs.PromptText)
	}
	if !strings.Contains(inputs.PromptText, "Profile investigation notes.") {
		t.Fatalf("expected notes preview in prompt:\n%s", inputs.PromptText)
	}
	if inputs.ContextValues == nil {
		t.Fatal("expected context values")
	}
	if inputs.ContextValues["profile_memory_path"] != memoryPath {
		t.Fatalf("unexpected memory path: %#v", inputs.ContextValues["profile_memory_path"])
	}
	if inputs.ContextValues["profile_notes_path"] != notesPath {
		t.Fatalf("unexpected notes path: %#v", inputs.ContextValues["profile_notes_path"])
	}
	resources, ok := inputs.ContextValues["profile_resources"].(map[string]interface{})
	if !ok || len(resources) != 2 {
		t.Fatalf("expected profile resources map, got %#v", inputs.ContextValues["profile_resources"])
	}
}

func TestBuildResolvedAgentInputs_AllowsNilResolvedAgent(t *testing.T) {
	inputs, err := BuildResolvedAgentInputs(nil)
	if err != nil {
		t.Fatalf("BuildResolvedAgentInputs(nil): %v", err)
	}
	if inputs == nil {
		t.Fatal("expected non-nil inputs")
	}
	if inputs.PromptText != "" {
		t.Fatalf("expected empty prompt text, got %q", inputs.PromptText)
	}
	if inputs.ToolPolicy != nil {
		t.Fatalf("expected nil tool policy, got %#v", inputs.ToolPolicy)
	}
}
