package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelcard"
)

func writeModelCardsLayerFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestLoadProviderRefreshModelCardCatalogAppliesWorkspaceLayer(t *testing.T) {
	dir := t.TempDir()
	userPath := writeModelCardsLayerFile(t, filepath.Join(dir, "user-model_cards.yaml"), `
version: 1
cards:
  - id: layered.card
    match:
      model_ids:
        - layered-model
    capability:
      max_context_tokens: 100
      input_modalities:
        - text
`)
	workspacePath := writeModelCardsLayerFile(t, filepath.Join(dir, ".aicli", "model_cards.yaml"), `
version: 1
cards:
  - id: layered.card
    capability:
      max_context_tokens: 300
`)
	cfg := &config.Config{
		AICLI: &config.AICLIConfig{
			ModelCards: &config.AICLIModelCardsConfig{
				UserPath:      userPath,
				WorkspacePath: workspacePath,
			},
		},
	}

	catalog, warnings, err := loadProviderRefreshModelCardCatalog(providerRefreshModelCardsRequest{}, cfg)
	if err != nil {
		t.Fatalf("loadProviderRefreshModelCardCatalog: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	spec, _ := catalog.Resolve(modelcard.Context{RuntimeProtocol: "openai"}, "layered-model")
	if spec.MaxContextTokens != 300 {
		t.Fatalf("expected workspace override 300, got %d", spec.MaxContextTokens)
	}
	if strings.Join(spec.InputModalities, ",") != "text" {
		t.Fatalf("expected user fields preserved, got %+v", spec.InputModalities)
	}

	catalog, _, err = loadProviderRefreshModelCardCatalog(providerRefreshModelCardsRequest{NoWorkspaceCards: true}, cfg)
	if err != nil {
		t.Fatalf("loadProviderRefreshModelCardCatalog with skip: %v", err)
	}
	spec, _ = catalog.Resolve(modelcard.Context{RuntimeProtocol: "openai"}, "layered-model")
	if spec.MaxContextTokens != 100 {
		t.Fatalf("NoWorkspaceCards should fall back to user layer, got %d", spec.MaxContextTokens)
	}
}

func TestLoadProviderLoginModelCardCatalogRespectsDisable(t *testing.T) {
	catalog, warnings, err := loadProviderLoginModelCardCatalog(providerLoginRequest{DisableModelCards: true}, nil)
	if err != nil {
		t.Fatalf("loadProviderLoginModelCardCatalog: %v", err)
	}
	if catalog != nil || len(warnings) != 0 {
		t.Fatalf("disabled catalog should return nil,nil, got catalog=%+v warnings=%+v", catalog, warnings)
	}
}
