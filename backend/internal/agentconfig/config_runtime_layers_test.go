package agentconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

// P2 (runtime.yaml layering): the portable/repository file supplies defaults but
// never receives writes, the user layer wins over it, and a fresh installation
// targets the user-level path.
func TestRuntimeConfigLayerStackPrefersWritableLayers(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	projectDir := t.TempDir()
	t.Chdir(projectDir)

	// Development checkout: the portable layer is the only file on disk.
	portable := filepath.Join(projectDir, "configs", aiclipaths.DefaultRuntimeConfigFileName)
	writeConfigLayerFile(t, portable, "agent:\n  maxSteps: 1\n")
	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultRuntimeConfigFileName)

	merged, err := LoadMergedRuntimeConfigDocument()
	if err != nil {
		t.Fatalf("LoadMergedRuntimeConfigDocument: %v", err)
	}
	if got := merged.OriginKindFor("agent.maxSteps"); got != "portable" {
		t.Fatalf("origin = %q, want portable", got)
	}
	// A read-only origin is only a default: the write lands in the user layer.
	if got := merged.WriteLayerKindFor("agent.maxSteps"); got != "user" {
		t.Fatalf("write layer = %q, want user (portable is read-only)", got)
	}
	target, kind := RuntimeConfigWriteTarget()
	if filepath.Clean(target) != filepath.Clean(userConfig) || kind != LayerKindUser {
		t.Fatalf("write target = %q (%s), want the user layer %q", target, kind, userConfig)
	}

	// The user layer wins over the portable default.
	writeConfigLayerFile(t, userConfig, "agent:\n  maxSteps: 7\n")
	merged, err = LoadMergedRuntimeConfigDocument()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := merged.OriginKindFor("agent.maxSteps"); got != "user" {
		t.Fatalf("origin = %q, want user", got)
	}
	if value, ok := lookupDocumentPath(merged.Merged, []string{"agent", "maxSteps"}); !ok || fmt.Sprintf("%v", value) != "7" {
		t.Fatalf("merged agent.maxSteps = %v (ok=%v), want 7", value, ok)
	}

	// Editing a key that the read-only layer owns must not touch that file.
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
	portableRaw, err := os.ReadFile(portable)
	if err != nil {
		t.Fatalf("read portable layer: %v", err)
	}
	if !strings.Contains(string(portableRaw), "maxSteps: 1") {
		t.Fatalf("portable layer must stay untouched:\n%s", portableRaw)
	}
}

// A fresh installation has no runtime.yaml anywhere: the write target is still
// the user-level path so the first edit creates it there.
func TestRuntimeConfigLayerStackFreshInstallTargetsUserPath(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	t.Chdir(t.TempDir())

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
