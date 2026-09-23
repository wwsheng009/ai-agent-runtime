package agentconfig

import (
	"fmt"
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
//
// 整份「读-改-写」在 config_file_write.go 的写事务内完成：与 routing / chat /
// provider 等写者共用同一把配置文件写锁（§3.4 M11 / §12 R4）。
func UpdateAICLIThemePreferences(configPath string, update AICLIThemePreferenceUpdate) (*AICLIThemeConfig, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, fmt.Errorf("config path is required")
	}
	// Layered configs: theme edits go back to the layer that owns aicli.theme.
	configPath = routeConfigWritePath(configPath, "aicli.theme")

	var current *AICLIThemeConfig
	err := updateConfigFileDocument(configPath, configDocumentWriteOptions{createStarterWhenMissing: true}, func(_ *yaml.Node, root *yaml.Node) error {
		loaded, err := currentAICLIThemeConfig(root)
		if err != nil {
			return err
		}
		applyAICLIThemePreferenceUpdate(loaded, update)

		sectionNode, err := marshalYAMLNode(loaded)
		if err != nil {
			return err
		}
		aicliNode := mappingValue(root, "aicli")
		if aicliNode == nil || aicliNode.Kind != yaml.MappingNode {
			aicliNode = &yaml.Node{Kind: yaml.MappingNode}
			upsertYAMLMappingValue(root, "aicli", aicliNode)
		}
		upsertYAMLMappingValue(aicliNode, "theme", sectionNode)
		current = loaded
		return nil
	})
	if err != nil {
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
