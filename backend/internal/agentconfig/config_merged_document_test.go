package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"gopkg.in/yaml.v3"
)

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func mustDocumentCopy(t *testing.T, value interface{}) map[string]interface{} {
	t.Helper()
	raw, err := yaml.Marshal(value)
	if err != nil {
		t.Fatalf("marshal document copy: %v", err)
	}
	out := map[string]interface{}{}
	if err := yaml.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal document copy: %v", err)
	}
	return out
}

// The runtime-server serves the merged document, so its write-back must send
// each changed key to the layer that owns it instead of flattening the stack.
func TestApplyMergedDocumentChangesWritesEachKeyToItsLayer(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, userConfig, `
providers:
  items:
    openai:
      api_key: ${OPENAI_API_KEY}
      base_url: https://user.example
      api_keys:
        - old-key
`)
	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, projectConfig, `
aicli:
  chat:
    default_model: project-model
`)
	chdirTest(t, projectDir)
	t.Setenv(MergeConfigEnvVar, "on")

	merged, err := LoadMergedConfigDocument()
	if err != nil {
		t.Fatalf("LoadMergedConfigDocument: %v", err)
	}
	if !merged.MultiLayer() {
		t.Fatalf("expected two present layers, got %#v", merged.PresentLayers)
	}

	updated := mustDocumentCopy(t, merged.Merged)
	items := updated["providers"].(map[string]interface{})["items"].(map[string]interface{})
	openai := items["openai"].(map[string]interface{})
	openai["base_url"] = "https://routed.example" // user-owned edit
	delete(openai, "api_keys")                    // user-owned deletion -> explicit null
	updated["aicli"].(map[string]interface{})["chat"].(map[string]interface{})["default_model"] = "next-model"
	items["brand-new"] = map[string]interface{}{"api_key": "new-key"} // new key -> highest layer

	applied, err := ApplyMergedDocumentChanges(merged, updated, projectConfig)
	if err != nil {
		t.Fatalf("ApplyMergedDocumentChanges: %v", err)
	}
	if applied == 0 {
		t.Fatal("expected edits to be applied")
	}

	userRaw := mustReadFile(t, userConfig)
	if !strings.Contains(userRaw, "routed.example") {
		t.Fatalf("user-owned edit must land in the user layer:\n%s", userRaw)
	}
	if !strings.Contains(userRaw, "api_keys: null") {
		t.Fatalf("deletions must be written as an explicit null in the owning layer:\n%s", userRaw)
	}
	if !strings.Contains(userRaw, "${OPENAI_API_KEY}") {
		t.Fatalf("untouched placeholders must survive (target files are parsed without env expansion):\n%s", userRaw)
	}
	if strings.Contains(userRaw, "default_model") || strings.Contains(userRaw, "brand-new") {
		t.Fatalf("project-owned and new keys must not be written to the user layer:\n%s", userRaw)
	}

	projectRaw := mustReadFile(t, projectConfig)
	if !strings.Contains(projectRaw, "next-model") {
		t.Fatalf("project-owned edit must land in the project layer:\n%s", projectRaw)
	}
	if !strings.Contains(projectRaw, "brand-new") {
		t.Fatalf("new keys must go to the highest writable layer:\n%s", projectRaw)
	}
	if strings.Contains(projectRaw, "openai") {
		t.Fatalf("user-owned keys must not leak into the project layer:\n%s", projectRaw)
	}
}

func TestApplyMergedDocumentChangesIsNoopWithoutChanges(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	writeConfigLayerFile(t, filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName), "aicli:\n  chat:\n    default_model: user-model\n")
	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, projectConfig, "skills_runtime:\n  config_file: project.yaml\n")
	chdirTest(t, projectDir)
	t.Setenv(MergeConfigEnvVar, "on")

	merged, err := LoadMergedConfigDocument()
	if err != nil {
		t.Fatalf("LoadMergedConfigDocument: %v", err)
	}
	before := mustReadFile(t, projectConfig)
	applied, err := ApplyMergedDocumentChanges(merged, mustDocumentCopy(t, merged.Merged), projectConfig)
	if err != nil {
		t.Fatalf("ApplyMergedDocumentChanges: %v", err)
	}
	if applied != 0 {
		t.Fatalf("unchanged document must not write anything, applied=%d", applied)
	}
	if after := mustReadFile(t, projectConfig); after != before {
		t.Fatalf("unchanged document must not touch the file:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// Pending changes are attributed to the layer their write will land in, so the
// UI can show which file an edit touches (design §9 H4 / P1-6).
func TestMergedDocumentWriteLayerKind(t *testing.T) {
	preserveGlobalConfig(t)
	home := isolateConfigLayerHome(t)
	writeConfigLayerFile(t, filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName), "aicli:\n  chat:\n    default_model: user-model\n")
	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)
	writeConfigLayerFile(t, projectConfig, "skills_runtime:\n  config_file: project.yaml\n")
	chdirTest(t, projectDir)
	t.Setenv(MergeConfigEnvVar, "on")

	merged, err := LoadMergedConfigDocument()
	if err != nil {
		t.Fatalf("LoadMergedConfigDocument: %v", err)
	}
	if got := merged.OriginKindFor("aicli.chat.default_model"); got != "user" {
		t.Fatalf("origin kind = %q, want user", got)
	}
	if got := merged.OriginKindFor("skills_runtime.config_file"); got != "project" {
		t.Fatalf("origin kind = %q, want project", got)
	}
	// New keys have no origin but still have a known destination.
	if got := merged.OriginKindFor("providers.items.brand-new"); got != "" {
		t.Fatalf("unknown key must have no origin, got %q", got)
	}
	if got := merged.WriteLayerKindFor("providers.items.brand-new"); got != "project" {
		t.Fatalf("new key write layer = %q, want project (highest present layer)", got)
	}
	if got := merged.WriteLayerKindFor("aicli.chat.default_model"); got != "user" {
		t.Fatalf("write layer = %q, want user", got)
	}
}
