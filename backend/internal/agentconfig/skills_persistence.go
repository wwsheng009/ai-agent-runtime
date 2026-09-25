package agentconfig

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// UpdateSkillsDisabledSkills 写入 skills_runtime.disabled_skills（SK-6 per-skill 启停）。
//
// 语义：
//   - names 为空 → 删除该键（回到"全部启用"），兼容 key disabledSkills 一并清理；
//   - names 非空 → 统一写成 snake_case 的 disabled_skills（stringSliceYAMLNode 已
//     完成去空白/去重/保序），避免 snake 与 camel 两个 key 并存产生歧义；
//   - 整份「读-改-写」复用 config_file_write.go 的写事务，与 provider / chat /
//     theme 写入共用同一把配置文件写锁；分层配置下路由到拥有该 key 的层。
func UpdateSkillsDisabledSkills(configPath string, names []string) error {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return fmt.Errorf("config path is required")
	}
	valueNode := stringSliceYAMLNode(names)
	configPath = routeConfigWritePath(configPath, "skills_runtime.disabled_skills")
	return updateConfigFileDocument(configPath, configDocumentWriteOptions{createStarterWhenMissing: true}, func(_ *yaml.Node, root *yaml.Node) error {
		existing := mappingValue(root, "skills_runtime")
		if len(valueNode.Content) == 0 {
			// 空名单：只做清理，不为了删键而新造 skills_runtime 节点。
			if existing == nil || existing.Kind != yaml.MappingNode {
				return nil
			}
			removeYAMLMappingValue(existing, "disabledSkills")
			removeYAMLMappingValue(existing, "disabled_skills")
			return nil
		}
		skillsRuntimeNode := ensureChildMapping(root, "skills_runtime")
		skillsRuntimeNode.Style = 0
		removeYAMLMappingValue(skillsRuntimeNode, "disabledSkills")
		upsertYAMLMappingValue(skillsRuntimeNode, "disabled_skills", valueNode)
		return nil
	})
}
