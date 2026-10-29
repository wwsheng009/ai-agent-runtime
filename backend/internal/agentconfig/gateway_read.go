package agentconfig

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadGatewaySection resolves the persisted gateway.* section from the same
// layered config stack aicli / runtime-server use, without touching the global
// config singleton. An explicit configFile (for example the file the TUI writes)
// is read directly; an empty path walks ConfigLayerSearchPaths, highest
// precedence first.
//
// It exists so host tools such as aicli-remote-tools can reuse what the user
// already configured in the TUI (`/gateway config url|token|proxy`) instead of
// duplicating connection settings (docs/gateway/16-mcp-mount-tunnel.md §8.5).
//
// The returned boolean reports whether a gateway section was present. A missing
// explicit file is an error; a missing default stack is simply (zero, false, nil).
func LoadGatewaySection(configFile string) (GatewayConfig, bool, error) {
	configFile = strings.TrimSpace(configFile)
	if configFile != "" {
		data, err := os.ReadFile(configFile)
		if err != nil {
			return GatewayConfig{}, false, fmt.Errorf("read gateway config %s: %w", configFile, err)
		}
		return decodeGatewaySection(data, configFile)
	}

	document, err := LoadMergedConfigDocument()
	if err != nil {
		return GatewayConfig{}, false, fmt.Errorf("load layered config: %w", err)
	}
	if len(strings.TrimSpace(string(document.MergedYAML))) == 0 {
		return GatewayConfig{}, false, nil
	}
	return decodeGatewaySection(document.MergedYAML, document.SourcePath)
}

func decodeGatewaySection(data []byte, source string) (GatewayConfig, bool, error) {
	// Decode only the section we own: a partial/foreign config file must not fail
	// because some unrelated key has a shape this package does not expect.
	var doc struct {
		Gateway *GatewayConfig `yaml:"gateway"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return GatewayConfig{}, false, fmt.Errorf("decode gateway config %s: %w", source, err)
	}
	if doc.Gateway == nil {
		return GatewayConfig{}, false, nil
	}
	section := *doc.Gateway
	section.URL = strings.TrimSpace(section.URL)
	section.Token = strings.TrimSpace(section.Token)
	section.Name = strings.TrimSpace(section.Name)
	section.ProxyURL = strings.TrimSpace(section.ProxyURL)
	return section, true, nil
}
