package agentconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

// runtime.yaml layering: only the user ($HOME/.aicli) and project (./.aicli)
// layers participate — the development directory (configs/, backend/configs/)
// is never an implicit source — and the project layer overrides only the keys
// it explicitly writes.
func TestRuntimeConfigLayerStackMergesUserAndProjectLayers(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	projectDir := t.TempDir()
	chdirTest(t, projectDir)

	// A development-directory file must be ignored entirely, even when it is the
	// only file that would win a naive precedence search.
	writeConfigLayerFile(t, filepath.Join(projectDir, "configs", aiclipaths.DefaultRuntimeConfigFileName), "agent:\n  maxSteps: 99\n")

	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultRuntimeConfigFileName)
	writeConfigLayerFile(t, userConfig, "agent:\n  maxSteps: 1\n  defaultModel: user-model\n")
	projectConfig := filepath.Join(projectDir, ".aicli", aiclipaths.DefaultRuntimeConfigFileName)
	writeConfigLayerFile(t, projectConfig, "agent:\n  defaultModel: project-model\n")

	merged, err := LoadMergedRuntimeConfigDocument()
	if err != nil {
		t.Fatalf("LoadMergedRuntimeConfigDocument: %v", err)
	}
	if got := merged.OriginKindFor("agent.maxSteps"); got != "user" {
		t.Fatalf("agent.maxSteps origin = %q, want user (dev directory must be ignored)", got)
	}
	if got := merged.OriginKindFor("agent.defaultModel"); got != "project" {
		t.Fatalf("agent.defaultModel origin = %q, want project", got)
	}
	if value, ok := lookupDocumentPath(merged.Merged, []string{"agent", "maxSteps"}); !ok || fmt.Sprintf("%v", value) != "1" {
		t.Fatalf("merged agent.maxSteps = %v (ok=%v), want the user layer's 1", value, ok)
	}
	if value, ok := lookupDocumentPath(merged.Merged, []string{"agent", "defaultModel"}); !ok || fmt.Sprintf("%v", value) != "project-model" {
		t.Fatalf("merged agent.defaultModel = %v (ok=%v), want project-model", value, ok)
	}
	if len(merged.PresentLayers) != 2 {
		t.Fatalf("present layers = %#v, want user+project only", merged.PresentLayers)
	}
	if filepath.Clean(merged.SourcePath) != filepath.Clean(projectConfig) {
		t.Fatalf("source path = %q, want the highest present layer %q", merged.SourcePath, projectConfig)
	}

	// The highest present writable layer receives writes.
	target, kind := RuntimeConfigWriteTarget()
	if filepath.Clean(target) != filepath.Clean(projectConfig) || kind != LayerKindProject {
		t.Fatalf("write target = %q (%s), want the project layer %q", target, kind, projectConfig)
	}

	// A key the user layer owns is edited in place, not moved to the project file.
	if _, err := merged.ApplyDocumentPathChange("agent.maxSteps", 9, target); err != nil {
		t.Fatalf("ApplyDocumentPathChange: %v", err)
	}
	userRaw, err := os.ReadFile(userConfig)
	if err != nil {
		t.Fatalf("read user layer: %v", err)
	}
	if !strings.Contains(string(userRaw), "maxSteps: 9") {
		t.Fatalf("user layer must receive the edit:\n%s", userRaw)
	}
	projectRaw, err := os.ReadFile(projectConfig)
	if err != nil {
		t.Fatalf("read project layer: %v", err)
	}
	if strings.Contains(string(projectRaw), "maxSteps") {
		t.Fatalf("project layer must not receive a key it does not own:\n%s", projectRaw)
	}
}

// A fresh installation has no runtime.yaml anywhere: the write target is still
// the user-level path so the first edit creates it there.
func TestRuntimeConfigLayerStackFreshInstallTargetsUserPath(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	chdirTest(t, t.TempDir())

	merged, err := LoadMergedRuntimeConfigDocument()
	if err != nil {
		t.Fatalf("LoadMergedRuntimeConfigDocument: %v", err)
	}
	if len(merged.PresentLayers) != 0 {
		t.Fatalf("expected no present layer, got %#v", merged.PresentLayers)
	}
	if strings.TrimSpace(merged.SourcePath) != "" {
		t.Fatalf("source path = %q, want empty", merged.SourcePath)
	}
	if got := merged.WriteLayerKindFor("agent.maxSteps"); got != "user" {
		t.Fatalf("write layer = %q, want user", got)
	}
	want := filepath.Join(home, ".aicli", aiclipaths.DefaultRuntimeConfigFileName)
	if target, _ := RuntimeConfigWriteTarget(); filepath.Clean(target) != filepath.Clean(want) {
		t.Fatalf("write target = %q, want %q", target, want)
	}
}
