package agentconfig

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProfilesConfigUpdate describes one profiles-section write, used by
// `aicli profile create --use / --set-default` (实施方案 Batch 2 / D5).
// 零值字段表示不改动对应键。
type ProfilesConfigUpdate struct {
	// Root, when non-nil, sets profiles.root.
	Root *string
	// ItemName/ItemRoot register profiles.items[<name>].root.
	ItemName string
	ItemRoot string
	// DefaultProfile, when non-empty, sets profiles.default_profile.
	DefaultProfile string
	// ClearDefaultProfile, when true, removes profiles.default_profile（删除被
	// default 引用的 profile 时 `--force` 的语义：D25「同时清空 default」）。
	ClearDefaultProfile bool
}

// UpdateProfilesConfig updates the profiles section inside a config file
// without rewriting unrelated top-level sections.
//
// 与 routing / theme / provider / chat 偏好等写者共用同一把配置文件写锁，并走
// 分层写路由（profiles 段归属的层），见 config_file_write.go / config_write_route.go。
func UpdateProfilesConfig(configPath string, update ProfilesConfigUpdate) error {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return fmt.Errorf("config path is required")
	}
	itemName := strings.TrimSpace(update.ItemName)
	defaultProfile := strings.TrimSpace(update.DefaultProfile)
	if update.Root == nil && itemName == "" && defaultProfile == "" && !update.ClearDefaultProfile {
		return fmt.Errorf("profiles config update is empty")
	}
	if itemName != "" && strings.TrimSpace(update.ItemRoot) == "" {
		return fmt.Errorf("profile item root is required")
	}

	writeKeys := make([]string, 0, 3)
	if update.Root != nil {
		writeKeys = append(writeKeys, "profiles.root")
	}
	if itemName != "" {
		writeKeys = append(writeKeys, "profiles.items."+itemName+".root")
	}
	if defaultProfile != "" {
		writeKeys = append(writeKeys, "profiles.default_profile")
	}
	if update.ClearDefaultProfile {
		writeKeys = append(writeKeys, "profiles.default_profile")
	}
	configPath = routeConfigWritePath(configPath, writeKeys...)

	return updateConfigFileDocument(configPath, configDocumentWriteOptions{createStarterWhenMissing: true}, func(_ *yaml.Node, root *yaml.Node) error {
		profilesNode := mappingValue(root, "profiles")
		if profilesNode == nil || profilesNode.Kind != yaml.MappingNode {
			profilesNode = &yaml.Node{Kind: yaml.MappingNode}
			upsertYAMLMappingValue(root, "profiles", profilesNode)
		}

		if update.Root != nil {
			upsertYAMLMappingValue(profilesNode, "root", yamlStringNode(strings.TrimSpace(*update.Root)))
		}
		if itemName != "" {
			itemsNode := mappingValue(profilesNode, "items")
			if itemsNode == nil || itemsNode.Kind != yaml.MappingNode {
				itemsNode = &yaml.Node{Kind: yaml.MappingNode}
				upsertYAMLMappingValue(profilesNode, "items", itemsNode)
			}
			itemNode := &yaml.Node{Kind: yaml.MappingNode}
			upsertYAMLMappingValue(itemNode, "root", yamlStringNode(strings.TrimSpace(update.ItemRoot)))
			upsertYAMLMappingValue(itemsNode, itemName, itemNode)
		}
		if defaultProfile != "" {
			upsertYAMLMappingValue(profilesNode, "default_profile", yamlStringNode(defaultProfile))
		}
		if update.ClearDefaultProfile {
			removeYAMLMappingValue(profilesNode, "default_profile")
		}
		return nil
	})
}

func yamlStringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

// RemoveProfilesConfigItem 删除 `profiles.items[<name>]` 注册项（Batch 8 生命周期
// 端点：rename/move/delete 需要清掉旧键，否则会在配置里留下指向已不存在目录的
// dormant 条目——R9）。与 UpdateProfilesConfig 共用同一把写锁与分层写路由。
func RemoveProfilesConfigItem(configPath, name string) error {
	configPath = strings.TrimSpace(configPath)
	name = strings.TrimSpace(name)
	if configPath == "" {
		return fmt.Errorf("config path is required")
	}
	if name == "" {
		return fmt.Errorf("profile item name is required")
	}
	configPath = routeConfigWritePath(configPath, "profiles.items."+name)

	return updateConfigFileDocument(configPath, configDocumentWriteOptions{createStarterWhenMissing: false}, func(_ *yaml.Node, root *yaml.Node) error {
		profilesNode := mappingValue(root, "profiles")
		if profilesNode == nil || profilesNode.Kind != yaml.MappingNode {
			return nil
		}
		itemsNode := mappingValue(profilesNode, "items")
		if itemsNode == nil || itemsNode.Kind != yaml.MappingNode {
			return nil
		}
		removeYAMLMappingValue(itemsNode, name)
		return nil
	})
}
