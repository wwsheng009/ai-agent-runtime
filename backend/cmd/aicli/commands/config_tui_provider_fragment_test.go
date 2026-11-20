package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestConfigTUIHeaderShowsProviderFragmentLayer(t *testing.T) {
	cfg := &agentconfig.Config{
		ConfigFilePath: "C:/home/.aicli/config.yaml",
		ConfigLayers: []agentconfig.ConfigLayer{
			{
				Kind:    agentconfig.LayerKindUser,
				Path:    "C:/home/.aicli/config.yaml",
				Present: true,
			},
			{
				Kind:     agentconfig.LayerKindUserProviders,
				Path:     "C:/home/.aicli/providers.yaml",
				Present:  true,
				Fragment: true,
				Section:  "providers",
			},
		},
	}
	input := strings.NewReader("q\n")
	var output bytes.Buffer
	if err := runConfigTUI(input, &output, cfg); err != nil {
		t.Fatalf("runConfigTUI: %v\noutput:\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "Providers:") ||
		!strings.Contains(output.String(), "providers.yaml") ||
		!strings.Contains(output.String(), "user-providers") {
		t.Fatalf("TUI header must show the provider fragment layer, got:\n%s", output.String())
	}
}
