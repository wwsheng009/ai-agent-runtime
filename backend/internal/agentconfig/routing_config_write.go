package agentconfig

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// aicli 配置层的 routing 写入（方案 §5.4「config 层按层路由」）。
//
// 目标文件判定与既有配置写路由同源：`./.aicli/config.yaml` 存在→项目层，
// 否则→用户层 `$HOME/.aicli/config.yaml`（按文件存在性，不看 cwd 之外的信息）。

// aicliConfigFileNameForRouting 取 aicli 主配置文件名（config.yaml）。
func aicliConfigFileNameForRouting() string {
	names := defaultConfigSearchNames()
	for _, name := range names {
		if strings.EqualFold(strings.TrimSpace(name), "config.yaml") {
			return name
		}
	}
	if len(names) > 0 && strings.TrimSpace(names[0]) != "" {
		return names[0]
	}
	return "config.yaml"
}

// aicliConfigLayerStackForRouting 返回 config.yaml 的两层候选（低→高）。
func aicliConfigLayerStackForRouting() []ConfigLayer {
	name := aicliConfigFileNameForRouting()
	layers := make([]ConfigLayer, 0, 2)
	if home, err := userHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		layers = append(layers, ConfigLayer{Kind: LayerKindUser, Path: filepath.Join(home, ".aicli", name)})
	} else {
		layers = append(layers, ConfigLayer{Kind: LayerKindUser, Path: filepath.Join(".aicli", name)})
	}
	layers = append(layers, ConfigLayer{Kind: LayerKindProject, Path: filepath.Join(".aicli", name)})
	for index := range layers {
		layers[index].Present = fileExists(layers[index].Path)
	}
	return layers
}

// AICLIConfigWriteTargetForRouting 返回 routing 写入的目标文件与层名（§5.4）。
// 返回值 path 为绝对路径（可解析时），layer 为 user|project。
func AICLIConfigWriteTargetForRouting() (string, string) {
	layer, _ := WritableLayer(aicliConfigLayerStackForRouting())
	if strings.TrimSpace(layer.Path) == "" {
		return "", ""
	}
	kind := string(layer.Kind)
	if absolute, err := filepath.Abs(layer.Path); err == nil && absolute != "" {
		return absolute, kind
	}
	return filepath.Clean(layer.Path), kind
}

// UpdateAICLIRoutingSection 写入/清除 `aicli.main_agent.routing` 与
// `aicli.subagents.routing`。
//
// 参数语义：
//   - set=false：调用方**未触碰**该节，文件原值保持不变；
//   - set=true 且 value!=nil：写入/覆盖该节；
//   - set=true 且 value==nil：删除该节（整节清除）。
//
// 2026-09-22 修正：此前 set=false 会删除对应节，于是「只改 sub_agent」或
// 「只清一个字段」的补丁会连带抹掉 `aicli.main_agent.routing`（数据破坏）。
// 写前校验由调用方负责（§5.4：先 ValidateMainAgentRoutingConfig 再落盘）。
func UpdateAICLIRoutingSection(configPath string, main *AICLIMainAgentRoutingConfig, mainSet bool, sub *AICLISubagentRoutingConfig, subSet bool) error {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return fmt.Errorf("config path is required")
	}
	// §3.4/M11：整份「读-改-写」在同一把文件锁内完成——TUI 与 API handler 调的是
	// 本函数，两个写入端并发时会各自基于旧文件落盘并互相覆盖（丢更新）。
	unlock := LockRoutingFileWrite(configPath)
	defer unlock()
	raw, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			if _, _, starterErr := EnsureStarterConfigAtPath(configPath); starterErr != nil {
				return starterErr
			}
			raw, err = os.ReadFile(configPath)
			if err != nil {
				return fmt.Errorf("read starter config file %s: %w", configPath, err)
			}
		} else {
			return fmt.Errorf("read config file %s: %w", configPath, err)
		}
	}

	document, err := parseYAMLDocument(raw)
	if err != nil {
		return err
	}
	root, err := ensureYAMLRootMapping(document)
	if err != nil {
		return err
	}
	aicliNode := mappingValue(root, "aicli")
	if aicliNode == nil || aicliNode.Kind != yaml.MappingNode {
		aicliNode = &yaml.Node{Kind: yaml.MappingNode}
		upsertYAMLMappingValue(root, "aicli", aicliNode)
	}

	if err := upsertRoutingSection(aicliNode, "main_agent", main, mainSet); err != nil {
		return err
	}
	if err := upsertRoutingSection(aicliNode, "subagents", sub, subSet); err != nil {
		return err
	}

	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		_ = encoder.Close()
		return fmt.Errorf("encode config yaml: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("finalize config yaml: %w", err)
	}
	return writeFileAtomic(configPath, output.Bytes())
}

func upsertRoutingSection(parent *yaml.Node, section string, value interface{}, set bool) error {
	if parent == nil || !set {
		// 未触碰的节保持原值：不写入、不删除。
		return nil
	}
	node := mappingValue(parent, section)
	if node == nil || node.Kind != yaml.MappingNode {
		if value == nil {
			// 没有该节可删：不凭空造一个空节（避免写入 `main_agent: {}`）。
			return nil
		}
		node = &yaml.Node{Kind: yaml.MappingNode}
		upsertYAMLMappingValue(parent, section, node)
	}
	if value == nil {
		deleteYAMLMappingValue(node, "routing")
		return nil
	}
	marshaled, err := marshalYAMLNode(value)
	if err != nil {
		return err
	}
	upsertYAMLMappingValue(node, "routing", marshaled)
	return nil
}

// deleteYAMLMappingValue 删除映射中的一个键（不存在时静默返回）。
func deleteYAMLMappingValue(root *yaml.Node, key string) {
	if root == nil || root.Kind != yaml.MappingNode || strings.TrimSpace(key) == "" {
		return
	}
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Value == key {
			root.Content = append(root.Content[:index], root.Content[index+2:]...)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// workspace 层写入（§5.4/N9：以会话绑定 workspace 路径定位 chat-prefs.yaml）

// UpdateWorkspaceRoutingSection 把补丁写入工作区偏好文件的 routing 节。
//
// 语义：
//   - patch.MainAgent / patch.SubAgent 非 nil 时，物化到既有 workspace 配置之上
//     （字段级合并，INV-A7），nil 字段保留文件原值；
//   - patch.ClearFields 按 §3.5.1 键路径删除；
//   - 合并后整节为空时写入空节（等价于清除覆盖，解析回落到 config/default）；
//   - 只改 routing 键，文件内其他 chat 偏好保持不变。
func UpdateWorkspaceRoutingSection(workspacePath string, patch *SessionRoutingPatch) error {
	path := WorkspacePrefsPathForPath(workspacePath)
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("workspace routing preferences unavailable: cannot resolve path %q", strings.TrimSpace(workspacePath))
	}
	// §3.4/M11：同一份 chat-prefs.yaml 的「读-改-写」必须串行化——TUI 与 API
	// handler 共享本函数，两个不同会话绑定同一工作区时也会落到同一份文件。
	unlock := LockRoutingFileWrite(path)
	defer unlock()
	current, err := loadWorkspaceChatPreferencesAt(path)
	if err != nil {
		return err
	}
	if current == nil {
		current = &AICLIChatConfig{}
	}
	prefs := current.Routing
	if prefs == nil {
		prefs = &AICLIWorkspaceRoutingPreferences{}
	}
	if patch != nil {
		if patch.MainAgent != nil {
			prefs.MainAgent = MaterializeMainAgentRoutingConfig(prefs.MainAgent, patch.MainAgent)
		}
		if patch.SubAgent != nil {
			prefs.SubAgent = MaterializeSubagentRoutingConfig(prefs.SubAgent, patch.SubAgent)
		}
		ClearWorkspaceRoutingFields(prefs, patch.ClearFields)
	}
	if prefs.MainAgent == nil && prefs.SubAgent == nil {
		prefs = &AICLIWorkspaceRoutingPreferences{}
	}
	return saveWorkspaceChatPreferencesAt(path, AICLIChatPreferenceUpdate{Routing: prefs})
}

// WorkspaceRoutingTargetPath 返回 workspace 层的目标文件路径（响应回显用，N9）。
func WorkspaceRoutingTargetPath(workspacePath string) string {
	return WorkspacePrefsPathForPath(workspacePath)
}
