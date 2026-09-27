package providerops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelcard"
)

func writeModelCardsFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func modelCardsConfig(builtinPath, userPath, workspacePath string) *config.Config {
	return &config.Config{
		AICLI: &config.AICLIConfig{
			ModelCards: &config.AICLIModelCardsConfig{
				BuiltinPath:   builtinPath,
				UserPath:      userPath,
				WorkspacePath: workspacePath,
			},
		},
	}
}

func TestLoadModelCardCatalogMergesBuiltinUserWorkspaceLayers(t *testing.T) {
	dir := t.TempDir()
	builtinPath := writeModelCardsFile(t, filepath.Join(dir, "extra-builtin.yaml"), `
version: 1
cards:
  - id: layered.model
    priority: 100
    match:
      model_ids:
        - layered-model
      protocols:
        - openai
    capability:
      input_modalities:
        - text
        - image
      max_context_tokens: 100
`)
	userPath := writeModelCardsFile(t, filepath.Join(dir, "user", "model_cards.yaml"), `
version: 1
cards:
  - id: layered.model
    capability:
      max_context_tokens: 200
      reasoning_efforts:
        - low
        - medium
`)
	workspacePath := writeModelCardsFile(t, filepath.Join(dir, "workspace", ".aicli", "model_cards.yaml"), `
version: 1
cards:
  - id: layered.model
    capability:
      max_context_tokens: 300
      native_tools:
        images_generations_api: true
`)

	catalog, warnings, err := LoadModelCardCatalogRaw(modelCardsConfig(builtinPath, userPath, workspacePath), CatalogOptions{})
	if err != nil {
		t.Fatalf("LoadModelCardCatalogRaw: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	if catalog == nil || countModelCards(catalog, "layered.model") != 1 {
		t.Fatalf("expected single merged layered.model card, got %+v", catalog)
	}

	spec, applied := catalog.Resolve(modelcard.Context{RuntimeProtocol: "openai"}, "layered-model")
	if len(applied) != 1 || applied[0].CardID != "layered.model" {
		t.Fatalf("unexpected applied cards: %+v", applied)
	}
	if spec.MaxContextTokens != 300 {
		t.Fatalf("expected workspace max_context_tokens 300, got %d", spec.MaxContextTokens)
	}
	if strings.Join(spec.InputModalities, ",") != "text,image" {
		t.Fatalf("expected builtin input_modalities preserved, got %+v", spec.InputModalities)
	}
	if strings.Join(spec.ReasoningEfforts, ",") != "low,medium" {
		t.Fatalf("expected user reasoning_efforts preserved, got %+v", spec.ReasoningEfforts)
	}
	if !spec.NativeTools.ImagesGenerationsAPI {
		t.Fatalf("expected workspace native_tools merge, got %+v", spec.NativeTools)
	}
}

func TestLoadModelCardCatalogWorkspaceWinsEqualPriorityTie(t *testing.T) {
	dir := t.TempDir()
	userPath := writeModelCardsFile(t, filepath.Join(dir, "user.yaml"), `
version: 1
cards:
  - id: user.tie
    match:
      model_ids:
        - tie-model
    capability:
      max_context_tokens: 100
      input_modalities:
        - text
`)
	workspacePath := writeModelCardsFile(t, filepath.Join(dir, "workspace.yaml"), `
version: 1
cards:
  - id: workspace.tie
    match:
      model_ids:
        - tie-model
    capability:
      max_context_tokens: 200
`)

	catalog, warnings, err := LoadModelCardCatalogRaw(modelCardsConfig("", userPath, workspacePath), CatalogOptions{NoUserCards: false})
	if err != nil {
		t.Fatalf("LoadModelCardCatalogRaw: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	spec, applied := catalog.Resolve(modelcard.Context{RuntimeProtocol: "openai"}, "tie-model")
	if len(applied) != 2 || applied[0].CardID != "workspace.tie" {
		t.Fatalf("expected workspace card first, got %+v", applied)
	}
	if spec.MaxContextTokens != 200 {
		t.Fatalf("expected workspace max_context_tokens, got %d", spec.MaxContextTokens)
	}
	if strings.Join(spec.InputModalities, ",") != "text" {
		t.Fatalf("expected user card to fill missing fields, got %+v", spec.InputModalities)
	}
}

func TestLoadModelCardCatalogRequestPathOutranksWorkspace(t *testing.T) {
	dir := t.TempDir()
	workspacePath := writeModelCardsFile(t, filepath.Join(dir, "workspace.yaml"), `
version: 1
cards:
  - id: request.model
    match:
      model_ids:
        - request-model
    capability:
      max_context_tokens: 100
`)
	requestPath := writeModelCardsFile(t, filepath.Join(dir, "request.yaml"), `
version: 1
cards:
  - id: request.model
    capability:
      max_context_tokens: 400
`)

	catalog, warnings, err := LoadModelCardCatalogRaw(modelCardsConfig("", "", workspacePath), CatalogOptions{CatalogPath: requestPath})
	if err != nil {
		t.Fatalf("LoadModelCardCatalogRaw: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	spec, _ := catalog.Resolve(modelcard.Context{RuntimeProtocol: "openai"}, "request-model")
	if spec.MaxContextTokens != 400 {
		t.Fatalf("expected request path to outrank workspace, got %d", spec.MaxContextTokens)
	}
}

func TestLoadModelCardCatalogLayerSkipOptions(t *testing.T) {
	dir := t.TempDir()
	userPath := writeModelCardsFile(t, filepath.Join(dir, "user.yaml"), `
version: 1
cards:
  - id: user.only
    match:
      model_ids:
        - user-model
    capability:
      input_modalities:
        - text
`)
	workspacePath := writeModelCardsFile(t, filepath.Join(dir, "workspace.yaml"), `
version: 1
cards:
  - id: workspace.only
    match:
      model_ids:
        - workspace-model
    capability:
      input_modalities:
        - text
`)

	catalog, _, err := LoadModelCardCatalogRaw(modelCardsConfig("", userPath, workspacePath), CatalogOptions{NoUserCards: true})
	if err != nil {
		t.Fatalf("LoadModelCardCatalogRaw: %v", err)
	}
	if hasModelCard(catalog, "user.only") {
		t.Fatalf("NoUserCards should skip user layer: %+v", catalog.Cards)
	}
	if !hasModelCard(catalog, "workspace.only") {
		t.Fatalf("workspace layer should stay: %+v", catalog.Cards)
	}

	catalog, _, err = LoadModelCardCatalogRaw(modelCardsConfig("", userPath, workspacePath), CatalogOptions{NoWorkspaceCards: true})
	if err != nil {
		t.Fatalf("LoadModelCardCatalogRaw: %v", err)
	}
	if !hasModelCard(catalog, "user.only") {
		t.Fatalf("user layer should stay: %+v", catalog.Cards)
	}
	if hasModelCard(catalog, "workspace.only") {
		t.Fatalf("NoWorkspaceCards should skip workspace layer: %+v", catalog.Cards)
	}
}

func TestLoadModelCardCatalogDisabled(t *testing.T) {
	dir := t.TempDir()
	workspacePath := writeModelCardsFile(t, filepath.Join(dir, "workspace.yaml"), `
version: 1
cards:
  - id: disabled.model
    match:
      model_ids:
        - disabled-model
    capability:
      input_modalities:
        - text
`)
	cfg := modelCardsConfig("", "", workspacePath)
	enabled := false
	cfg.AICLI.ModelCards.Enabled = &enabled

	catalog, warnings, err := LoadModelCardCatalogRaw(cfg, CatalogOptions{})
	if err != nil {
		t.Fatalf("LoadModelCardCatalogRaw: %v", err)
	}
	if catalog != nil || len(warnings) != 0 {
		t.Fatalf("disabled catalog should return nil,nil, got catalog=%+v warnings=%+v", catalog, warnings)
	}

	// 显式请求级目录仍可强制启用（沿用既有语义）。
	requestPath := writeModelCardsFile(t, filepath.Join(dir, "request.yaml"), `
version: 1
cards:
  - id: request.enabled
    match:
      model_ids:
        - request-model
    capability:
      input_modalities:
        - text
`)
	catalog, _, err = LoadModelCardCatalogRaw(cfg, CatalogOptions{CatalogPath: requestPath})
	if err != nil {
		t.Fatalf("LoadModelCardCatalogRaw with request path: %v", err)
	}
	if !hasModelCard(catalog, "request.enabled") {
		t.Fatalf("request path should force-load catalog: %+v", catalog)
	}
}

func TestLoadModelCardCatalogDefaultWorkspacePath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeModelCardsFile(t, filepath.Join(dir, ".aicli", "model_cards.yaml"), `
version: 1
cards:
  - id: default.workspace
    match:
      model_ids:
        - default-workspace-model
    capability:
      input_modalities:
        - text
`)
	// 显式指向不存在的用户文件，避免读取真实 ~/.aicli/model_cards.yaml。
	missingUser := filepath.Join(dir, "missing-user-model_cards.yaml")

	catalog, warnings, err := LoadModelCardCatalogRaw(modelCardsConfig("", missingUser, ""), CatalogOptions{})
	if err != nil {
		t.Fatalf("LoadModelCardCatalogRaw: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	if !hasModelCard(catalog, "default.workspace") {
		t.Fatalf("expected default workspace path .aicli/model_cards.yaml to load, got %+v", catalog.Cards)
	}
}

func hasModelCard(catalog *modelcard.Catalog, id string) bool {
	return countModelCards(catalog, id) > 0
}

func countModelCards(catalog *modelcard.Catalog, id string) int {
	if catalog == nil {
		return 0
	}
	count := 0
	for _, card := range catalog.Cards {
		if strings.EqualFold(strings.TrimSpace(card.ID), strings.TrimSpace(id)) {
			count++
		}
	}
	return count
}
