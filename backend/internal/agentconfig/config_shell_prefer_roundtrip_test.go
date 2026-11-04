package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"gopkg.in/yaml.v3"
)

// TestApplyDocumentPathChangeWritesShellSliceValue covers the plan's appendix B
// item 1: `shell.prefer` is the first slice-valued key written through
// ApplyDocumentPathChange, so the (write -> reload -> deep-equal) round trip has
// to be proven rather than assumed.
//
// 注意：这条路径会把层文件解码成 map 再整体 Marshal，因此**不保留注释与键顺序**。
// 需要保留注释的 runtime.yaml 写入走 runtimeserver.persistShellSettings 的
// yaml.Node 单键 upsert（见 TestPersistShellSettingsPreservesCommentsAndOrder）。
func TestApplyDocumentPathChangeWritesShellSliceValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")
	original := "shell:\n  autoDetect: true\n  prefer: [powershell]\nbackground:\n  maxConcurrentJobs: 4\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	document := &MergedConfigDocument{
		Merged: map[string]interface{}{
			"shell": map[string]interface{}{
				"autoDetect": true,
				"prefer":     []interface{}{"powershell"},
			},
		},
		DefaultWriteLayer: &ConfigLayer{Kind: LayerKindUser, Path: path},
	}
	want := []string{"git-bash", "busybox", "powershell"}
	if _, err := document.ApplyDocumentPathChange("shell.prefer", want, path); err != nil {
		t.Fatalf("apply shell.prefer: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var decoded struct {
		Shell      runtimeexecutor.ShellConfig `yaml:"shell"`
		Background struct {
			MaxConcurrentJobs int `yaml:"maxConcurrentJobs"`
		} `yaml:"background"`
	}
	if err := yaml.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("reload: %v\n%s", err, string(raw))
	}
	if len(decoded.Shell.Prefer) != len(want) {
		t.Fatalf("prefer = %#v, want %#v\n%s", decoded.Shell.Prefer, want, string(raw))
	}
	for index := range want {
		if decoded.Shell.Prefer[index] != want[index] {
			t.Fatalf("prefer[%d] = %q, want %q\n%s", index, decoded.Shell.Prefer[index], want[index], string(raw))
		}
	}
	// Single-key write: the sibling key and section survive.
	if text := string(raw); !containsAll(text, "maxConcurrentJobs: 4", "autoDetect: true") {
		t.Fatalf("unrelated content was dropped:\n%s", text)
	}
	// The slice must be stored as a YAML block sequence, not a string.
	if text := string(raw); !strings.Contains(text, "- git-bash") {
		t.Fatalf("prefer must serialise as a sequence:\n%s", text)
	}
}

func TestApplyDocumentPathChangeWritesShellDuration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")
	document := &MergedConfigDocument{
		Merged:            map[string]interface{}{},
		DefaultWriteLayer: &ConfigLayer{Kind: LayerKindUser, Path: path},
	}
	if _, err := document.ApplyDocumentPathChange("shell.probeTimeout", (3 * time.Second).String(), path); err != nil {
		t.Fatalf("apply shell.probeTimeout: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var decoded struct {
		Shell runtimeexecutor.ShellConfig `yaml:"shell"`
	}
	if err := yaml.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("reload: %v\n%s", err, string(raw))
	}
	if decoded.Shell.ProbeTimeout != 3*time.Second {
		t.Fatalf("probeTimeout = %s, want 3s\n%s", decoded.Shell.ProbeTimeout, string(raw))
	}
}

func containsAll(text string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(text, needle) {
			return false
		}
	}
	return true
}
