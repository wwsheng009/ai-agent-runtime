package runtimeserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"gopkg.in/yaml.v3"
)

func writeLayeredConfigFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readLayeredConfigFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func copyDocumentMap(t *testing.T, value interface{}) map[string]interface{} {
	t.Helper()
	raw, err := yaml.Marshal(value)
	if err != nil {
		t.Fatalf("marshal copy: %v", err)
	}
	out := map[string]interface{}{}
	if err := yaml.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal copy: %v", err)
	}
	return out
}

// With layered merging enabled the server must serve the merged document and
// distribute saves back to the owning layer, exactly like the CLI.
func TestLayeredConfigDocumentLoadAndDistributedSave(t *testing.T) {
	home := t.TempDir()
	previousHome := agentconfig.UserHomeDirForTest()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(previousHome) })

	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeLayeredConfigFile(t, userConfig, `
providers:
  items:
    openai:
      base_url: https://user.example
`)
	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)
	writeLayeredConfigFile(t, projectConfig, `
aicli:
  chat:
    default_model: project-model
`)
	t.Chdir(projectDir)
	t.Setenv(agentconfig.MergeConfigEnvVar, "on")

	service := NewLocalConfigDocumentService(projectConfig)
	if service == nil {
		t.Fatal("expected a config document service")
	}

	doc, err := service.LoadDocument()
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	// The served document is the merged view, so both layers contribute.
	if !strings.Contains(doc.Raw, "user.example") || !strings.Contains(doc.Raw, "project-model") {
		t.Fatalf("expected the merged document, got:\n%s", doc.Raw)
	}
	if len(doc.Layers) == 0 {
		t.Fatal("expected layer metadata on the served document")
	}
	present := 0
	for _, layer := range doc.Layers {
		if layer.Present {
			present++
		}
	}
	if present != 2 {
		t.Fatalf("expected 2 present layers, got %d (%#v)", present, doc.Layers)
	}
	if got := doc.Origins["providers.items.openai.base_url"]; got != "user" {
		t.Fatalf("origin of the user-owned key = %q, want user", got)
	}
	if got := doc.Origins["aicli.chat.default_model"]; got != "project" {
		t.Fatalf("origin of the project-owned key = %q, want project", got)
	}

	// Edit a user-owned key through the API shape and save it.
	parsed := copyDocumentMap(t, doc.Parsed)
	items := parsed["providers"].(map[string]interface{})["items"].(map[string]interface{})
	openai := items["openai"].(map[string]interface{})
	openai["base_url"] = "https://from-server.example"

	if _, err := service.SaveDocument(skillsapi.ConfigDocumentSaveRequest{
		Parsed: parsed,
		Mode:   "structured",
	}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	userRaw := readLayeredConfigFile(t, userConfig)
	if !strings.Contains(userRaw, "from-server.example") {
		t.Fatalf("user-owned edit must be written to the user layer:\n%s", userRaw)
	}
	projectRaw := readLayeredConfigFile(t, projectConfig)
	if strings.Contains(projectRaw, "openai") {
		t.Fatalf("user-owned edit must not be pinned into the project layer:\n%s", projectRaw)
	}
	if !strings.Contains(projectRaw, "project-model") {
		t.Fatalf("unrelated project keys must survive:\n%s", projectRaw)
	}
}

// Without layered merging the server keeps its previous single-file behaviour.
func TestConfigDocumentSingleFileBehaviourWithoutLayering(t *testing.T) {
	home := t.TempDir()
	previousHome := agentconfig.UserHomeDirForTest()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(previousHome) })

	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeLayeredConfigFile(t, userConfig, "providers:\n  items:\n    openai:\n      base_url: https://user.example\n")
	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)
	writeLayeredConfigFile(t, projectConfig, "aicli:\n  chat:\n    default_model: project-model\n")
	t.Chdir(projectDir)

	service := NewLocalConfigDocumentService(projectConfig)
	doc, err := service.LoadDocument()
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if strings.Contains(doc.Raw, "user.example") {
		t.Fatalf("merge mode is off, the user layer must stay invisible:\n%s", doc.Raw)
	}
	if len(doc.Layers) != 0 || len(doc.Origins) != 0 {
		t.Fatalf("layer metadata must stay empty when merging is off: %#v %#v", doc.Layers, doc.Origins)
	}
}

func TestLayeredConfigDocumentPreviewAttributesChangedPaths(t *testing.T) {
	home := t.TempDir()
	previousHome := agentconfig.UserHomeDirForTest()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(previousHome) })

	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeLayeredConfigFile(t, userConfig, "providers:\n  items:\n    openai:\n      base_url: https://user.example\n")
	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)
	writeLayeredConfigFile(t, projectConfig, "aicli:\n  chat:\n    default_model: project-model\n")
	t.Chdir(projectDir)
	t.Setenv(agentconfig.MergeConfigEnvVar, "on")

	service := NewLocalConfigDocumentService(projectConfig)
	doc, err := service.LoadDocument()
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	parsed := copyDocumentMap(t, doc.Parsed)
	items := parsed["providers"].(map[string]interface{})["items"].(map[string]interface{})
	items["openai"].(map[string]interface{})["base_url"] = "https://preview.example"
	parsed["aicli"].(map[string]interface{})["chat"].(map[string]interface{})["default_model"] = "next-model"

	preview, err := service.PreviewDocument(skillsapi.ConfigDocumentSaveRequest{
		Parsed: parsed,
		Mode:   "structured",
	})
	if err != nil {
		t.Fatalf("PreviewDocument: %v", err)
	}
	if preview.RuntimeImpact == nil {
		t.Fatal("expected a runtime impact for the preview")
	}
	layers := preview.RuntimeImpact.PathLayers
	if got := layers["providers.items.openai.base_url"]; got != "user" {
		t.Fatalf("user-owned change attributed to %q, want user (all=%v)", got, layers)
	}
	if got := layers["aicli.chat.default_model"]; got != "project" {
		t.Fatalf("project-owned change attributed to %q, want project (all=%v)", got, layers)
	}
}
