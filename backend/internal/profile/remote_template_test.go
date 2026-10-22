package profile

import (
	"strings"
	"testing"
)

// TestRemoteTemplateExistsAndIsBuiltin locks the template's presence.
//
// It is the only built-in template whose job is "operate one remote target", and
// the reason it must ship with the binary (rather than live in docs) is that
// getting the denylist wrong means a model writes to the wrong machine.
func TestRemoteTemplateExistsAndIsBuiltin(t *testing.T) {
	names := TemplateNames()
	found := false
	for _, name := range names {
		if name == "remote" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("built-in templates = %v, want one named %q", names, "remote")
	}
}

// TestRemoteTemplateDeniesEveryLocalWriteAndExecTool is the load-bearing
// assertion.
//
// A remote profile that forgets `write` or `shell` in its denylist silently
// allows the model to mutate the local filesystem while believing it operates the
// target. The list is explicit rather than wildcarded because `*` would also deny
// the remote tools (they share the same deny map).
func TestRemoteTemplateDeniesEveryLocalWriteAndExecTool(t *testing.T) {
	files, err := RenderTemplate("remote", "remote", TemplateDefaultAgent)
	if err != nil {
		t.Fatalf("RenderTemplate(remote): %v", err)
	}
	body, ok := files["profile.yaml"]
	if !ok {
		t.Fatalf("rendered files = %v, want profile.yaml", sortedKeys(files))
	}
	text := string(body)

	// Every local tool that can mutate files or run processes must be denied.
	required := []string{
		"write", "edit", "apply_patch", "append_write", "multiedit",
		"shell", "bash", "execute_shell_command",
		"job_start", "job_output", "job_cancel",
		// read family: denied so the tool list contains exactly one owner per name
		"view", "ls", "glob", "grep",
	}
	for _, tool := range required {
		if !strings.Contains(text, "- "+tool+"\n") {
			t.Errorf("remote template must deny local tool %q", tool)
		}
	}

	// The MCP surface must be pinned, otherwise a second server in mcp.yaml
	// contributes tools the profile never reasoned about.
	if !strings.Contains(text, "use_servers:") {
		t.Error("remote template must pin mcp.use_servers")
	}

	// A wildcard would also deny the remote tools; the template must not use it.
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == `- "*"` || trimmed == "- '*'" {
			t.Fatalf("remote template must not deny every tool: %q would also deny the remote surface", trimmed)
		}
	}
}

// TestRemoteTemplateOmitsEmptyPromptMode keeps the validate output clean.
//
// Declaring `prompts.mode: replace` with no prompt files emits
// "no prompt files found (no injection)" on every `profile validate`, which trains
// operators to ignore warnings.
func TestRemoteTemplateOmitsEmptyPromptMode(t *testing.T) {
	files, err := RenderTemplate("remote", "remote", TemplateDefaultAgent)
	if err != nil {
		t.Fatalf("RenderTemplate(remote): %v", err)
	}
	if strings.Contains(string(files["profile.yaml"]), "prompts:") {
		t.Error("remote template declares prompts with no prompt files; that only produces a warning")
	}
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
