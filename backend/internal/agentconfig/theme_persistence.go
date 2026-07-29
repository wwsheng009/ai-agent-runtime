package agentconfig

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// AICLIThemePreferenceUpdate describes a partial update to the persisted theme defaults.
type AICLIThemePreferenceUpdate struct {
	// Name 使用指针以便在“不修改”与“显式写入”之间区分。
	// nil 表示不修改；非 nil 时写入 strings.TrimSpace 后的值（可为空以清空）。
	Name *string
	// Mode 使用同样的指针语义（auto|dark|light）。
	Mode *string
	// Syntax 使用同样的指针语义（Chroma theme name）。
	Syntax *string
}

// UpdateAICLIThemePreferences updates the aicli.theme section inside a config file
// without rewriting unrelated top-level sections.
func UpdateAICLIThemePreferences(configPath string, update AICLIThemePreferenceUpdate) (*AICLIThemeConfig, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, fmt.Errorf("config path is required")
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			if _, _, starterErr := EnsureStarterConfigAtPath(configPath); starterErr != nil {
				return nil, starterErr
			}
			raw, err = os.ReadFile(configPath)
			if err != nil {
				return nil, fmt.Errorf("read starter config file %s: %w", configPath, err)
			}
		} else {
			return nil, fmt.Errorf("read config file %s: %w", configPath, err)
		}
	}

	document, err := parseYAMLDocument(raw)
	if err != nil {
		return nil, err
	}

	root, err := ensureYAMLRootMapping(document)
	if err != nil {
		return nil, err
	}

	current, err := currentAICLIThemeConfig(root)
	if err != nil {
		return nil, err
	}
	applyAICLIThemePreferenceUpdate(current, update)

	sectionNode, err := marshalYAMLNode(current)
	if err != nil {
		return nil, err
	}

	aicliNode := mappingValue(root, "aicli")
	if aicliNode == nil || aicliNode.Kind != yaml.MappingNode {
		aicliNode = &yaml.Node{Kind: yaml.MappingNode}
		upsertYAMLMappingValue(root, "aicli", aicliNode)
	}
	upsertYAMLMappingValue(aicliNode, "theme", sectionNode)

	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		_ = encoder.Close()
		return nil, fmt.Errorf("encode config yaml: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("finalize config yaml: %w", err)
	}

	if err := writeFileAtomic(configPath, output.Bytes()); err != nil {
		return nil, err
	}

	return current, nil
}

func applyAICLIThemePreferenceUpdate(current *AICLIThemeConfig, update AICLIThemePreferenceUpdate) {
	if current == nil {
		return
	}
	if update.Name != nil {
		current.Name = strings.TrimSpace(*update.Name)
	}
	if update.Mode != nil {
		current.Mode = strings.TrimSpace(*update.Mode)
	}
	if update.Syntax != nil {
		current.Syntax = strings.TrimSpace(*update.Syntax)
	}
}

func currentAICLIThemeConfig(root *yaml.Node) (*AICLIThemeConfig, error) {
	current := &AICLIThemeConfig{}
	if root == nil {
		return current, nil
	}
	aicliNode := mappingValue(root, "aicli")
	if aicliNode == nil {
		return current, nil
	}
	themeNode := mappingValue(aicliNode, "theme")
	if themeNode == nil {
		return current, nil
	}
	if err := decodeYAMLNode(themeNode, current); err != nil {
		return nil, fmt.Errorf("decode aicli.theme section: %w", err)
	}
	return current, nil
}
