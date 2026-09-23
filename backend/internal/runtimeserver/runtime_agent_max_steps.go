package runtimeserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"gopkg.in/yaml.v3"
)

// NewLayeredRuntimeAgentMaxStepsPersister 是分层版的最大步骤数保存动作（P2）：
// 内存快照部分与单文件版一致；落盘改走 runtime.yaml 的层栈——键所属的**可写**层优先，
// 只读的 portable 层（仓库/发行包自带）永不写入，新键落到可写层（用户级，必要时新建）。
// 因此开发态下编辑 agent.maxSteps 不会再改脏仓库里的 backend/configs/runtime.yaml。
func NewLayeredRuntimeAgentMaxStepsPersister(
	manager *runtimecfg.RuntimeManager,
) skillsapi.AgentMaxStepsPersister {
	return func(maxSteps int) (string, error) {
		if manager == nil {
			return "", fmt.Errorf("runtime config manager is not configured")
		}
		previous := manager.Get()
		if previous == nil {
			return "", fmt.Errorf("runtime config is not loaded")
		}

		next := *previous
		next.Agent.MaxMaxSteps = maxSteps
		if err := manager.Update(&next); err != nil {
			return "", err
		}

		target, _ := config.RuntimeConfigWriteTarget()
		merged, err := config.LoadMergedRuntimeConfigDocument()
		if err != nil || merged == nil {
			// 层栈不可用时退回单文件写入，保持旧行为可用。
			configFile := manager.GetFilePath()
			if err := PersistRuntimeAgentMaxSteps(configFile, maxSteps); err != nil {
				_ = manager.Update(previous)
				return configFile, err
			}
			return configFile, nil
		}
		if _, err := merged.ApplyDocumentPathChange("agent.maxSteps", maxSteps, target); err != nil {
			_ = manager.Update(previous)
			return target, err
		}
		return target, nil
	}
}

// NewLayeredRuntimeAgentMaxStepsReader 与单文件版一致，但把返回的「来源配置文件路径」
// 换成**写入目标**（可写层；全新安装时是用户级路径），使设置页显示的文件与实际落盘一致。
func NewLayeredRuntimeAgentMaxStepsReader(
	manager *runtimecfg.RuntimeManager,
) skillsapi.AgentMaxStepsProvider {
	return func() (int, string, error) {
		if manager == nil {
			return 0, "", fmt.Errorf("runtime config manager is not configured")
		}
		current := manager.Get()
		if current == nil {
			return 0, "", fmt.Errorf("runtime config is not loaded")
		}
		configFile := manager.GetFilePath()
		if target, _ := config.RuntimeConfigWriteTarget(); strings.TrimSpace(target) != "" {
			configFile = target
		}
		return current.Agent.MaxMaxSteps, configFile, nil
	}
}

// NewRuntimeAgentMaxStepsPersister 组装「最大步骤数」保存动作：
// 先改 RuntimeManager 的内存快照（后续每轮请求的缺省值来源），再把 agent.maxSteps
// 落盘到 runtime 配置文件；落盘失败则回滚内存，避免内存与文件不一致。
//
// 配置文件不存在不算失败：PersistRuntimeAgentMaxSteps 会新建（与 policy_persistence
// 的既有 persister 一致，也与 RuntimeManager.Load 把「文件不存在」当成功相呼应）。
func NewRuntimeAgentMaxStepsPersister(
	manager *runtimecfg.RuntimeManager,
) skillsapi.AgentMaxStepsPersister {
	return func(maxSteps int) (string, error) {
		if manager == nil {
			return "", fmt.Errorf("runtime config manager is not configured")
		}
		// previous == nil 按构造不可达：NewRuntimeManager 先装 DefaultRuntimeConfig，
		// Load 遇到文件不存在也算成功，Update 又先过 Validate 永不写入 nil。保留判断只为
		// 与读取端防御对称；真的为 nil 时 panic 会发生在 Get() 内部（configCopy := *rm.config）。
		previous := manager.Get()
		if previous == nil {
			return "", fmt.Errorf("runtime config is not loaded")
		}

		next := *previous
		next.Agent.MaxMaxSteps = maxSteps
		if err := manager.Update(&next); err != nil {
			return "", err
		}

		configFile := manager.GetFilePath()
		if err := PersistRuntimeAgentMaxSteps(configFile, maxSteps); err != nil {
			_ = manager.Update(previous)
			return configFile, err
		}
		return configFile, nil
	}
}

// NewRuntimeAgentMaxStepsReader 组装「最大步骤数」读取动作：只读 RuntimeManager 的
// 内存快照与来源配置文件路径，不修改快照、不写文件（路径原样返回，trim 交给 handler）。
func NewRuntimeAgentMaxStepsReader(
	manager *runtimecfg.RuntimeManager,
) skillsapi.AgentMaxStepsProvider {
	return func() (int, string, error) {
		if manager == nil {
			return 0, "", fmt.Errorf("runtime config manager is not configured")
		}
		current := manager.Get()
		// 同 persister，current == nil 按构造不可达（Get() 在 config 为 nil 时会先 panic）。
		// 保留判断只为防御对称，不代表存在「配置未加载」这种失败模式。
		if current == nil {
			return 0, "", fmt.Errorf("runtime config is not loaded")
		}
		return current.Agent.MaxMaxSteps, manager.GetFilePath(), nil
	}
}

// PersistRuntimeAgentMaxSteps 把 agent.maxSteps 写回 runtime 配置文件。
//
// 作用域刻意收窄到单个键：文件里其余键、注释与顺序都保留（先解析成 yaml.Node
// 再 upsert，而不是用结构体整体序列化），写入前先生成时间戳备份。
// 文件不存在时按空文档处理并新建（目录一并创建），因此「GET 回缺省值 + 路径 →
// 用户填值保存」在 runtime 配置文件尚未落盘的全新安装下也能闭环。
// 内存侧的同步由调用方负责（runtime-server 里是 runtimeManager.Update）。
func PersistRuntimeAgentMaxSteps(configPath string, maxSteps int) error {
	path := strings.TrimSpace(configPath)
	if path == "" {
		return fmt.Errorf("runtime config path is required")
	}
	if maxSteps < 0 {
		return fmt.Errorf("runtime agent maxSteps must be >= 0, got %d", maxSteps)
	}

	// 与 aicli TUI / API 的其它配置写者共用同一把写锁（agentconfig.LockConfigFileWrite）：
	// 本函数是「读整份文件 → 只改 agent.maxSteps → 写回」，只锁写入端会让并发写者在读取
	// 窗口里丢更新，因此锁必须覆盖整个读-改-写。
	unlock := config.LockConfigFileWrite(path)
	defer unlock()

	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read runtime config file: %w", err)
		}
		// 文件还不存在：RuntimeManager.Load 同样把这种情况当成功并用默认值，这里若报错就会
		// 出现「读取端 200 回缺省值 0 + 路径，保存端 500」的断链。按空文档继续，写盘时建文件。
		raw = nil
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return persistRuntimeAgentMaxStepsJSON(path, raw, maxSteps)
	case ".yaml", ".yml", "":
		return persistRuntimeAgentMaxStepsYAML(path, raw, maxSteps)
	default:
		return fmt.Errorf("unsupported runtime config format: %s", filepath.Ext(path))
	}
}

func persistRuntimeAgentMaxStepsYAML(path string, raw []byte, maxSteps int) error {
	var document yaml.Node
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := yaml.Unmarshal(raw, &document); err != nil {
			return fmt.Errorf("parse runtime config yaml: %w", err)
		}
	}

	root, err := ensureYAMLRootMapping(&document)
	if err != nil {
		return err
	}
	agent, err := ensureYAMLMappingChild(root, "agent")
	if err != nil {
		return err
	}

	// 已有 maxSteps 时只改这一行的标量，其它字节（空行、注释、缩进）原样保留：
	// yaml.v3 重新编码会吃掉空行，配置文件 diff 会显得很大。
	if updated, ok := rewriteYAMLMaxStepsInline(string(raw), agent, maxSteps); ok {
		return writeFilePreserveMode(path, []byte(updated))
	}

	value, err := marshalYAMLValueNode(maxSteps)
	if err != nil {
		return err
	}
	// yaml.v3 不会把旧值的行内注释搬到新节点上，这里手动继承，保持改动最小。
	if existing := yamlMappingValue(agent, "maxSteps"); existing != nil {
		value.HeadComment = existing.HeadComment
		value.LineComment = existing.LineComment
		value.FootComment = existing.FootComment
	}
	upsertYAMLMappingValue(agent, "maxSteps", value)

	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		_ = encoder.Close()
		return fmt.Errorf("encode runtime config yaml: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("finalize runtime config yaml: %w", err)
	}

	return writeFilePreserveMode(path, output.Bytes())
}

func persistRuntimeAgentMaxStepsJSON(path string, raw []byte, maxSteps int) error {
	root := make(map[string]interface{})
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &root); err != nil {
			return fmt.Errorf("parse runtime config json: %w", err)
		}
	}

	agent := make(map[string]interface{})
	if existing, ok := root["agent"]; ok {
		current, ok := existing.(map[string]interface{})
		if !ok {
			return fmt.Errorf("runtime config key %q must be an object", "agent")
		}
		agent = current
	}
	agent["maxSteps"] = maxSteps
	root["agent"] = agent

	output, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runtime config json: %w", err)
	}
	output = append(output, '\n')
	return writeFilePreserveMode(path, output)
}

// yamlMappingValue 返回 mapping 里 key 对应的值节点，不存在时返回 nil。
func yamlMappingValue(root *yaml.Node, key string) *yaml.Node {
	if root == nil {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return root.Content[i+1]
		}
	}
	return nil
}

// rewriteYAMLMaxStepsInline 在原文里就地替换 agent.maxSteps 的标量值。
// 只有「改动后重新解析仍得到期望值」时才返回 ok，否则交给调用方走节点重写兜底。
func rewriteYAMLMaxStepsInline(text string, agent *yaml.Node, maxSteps int) (string, bool) {
	value := yamlMappingValue(agent, "maxSteps")
	if value == nil || value.Kind != yaml.ScalarNode || value.Line <= 0 || value.Column <= 0 {
		return "", false
	}

	lines := strings.SplitAfter(text, "\n")
	index := value.Line - 1
	if index >= len(lines) {
		return "", false
	}
	line := lines[index]
	start, ok := yamlColumnByteOffset(line, value.Column)
	if !ok {
		return "", false
	}

	end := start
	for end < len(line) && line[end] != '#' && line[end] != '\n' && line[end] != '\r' {
		end++
	}
	trimmed := end
	for trimmed > start && (line[trimmed-1] == ' ' || line[trimmed-1] == '\t') {
		trimmed--
	}
	if trimmed == start {
		return "", false
	}

	lines[index] = line[:start] + strconv.Itoa(maxSteps) + line[trimmed:]
	updated := strings.Join(lines, "")

	var verify struct {
		Agent struct {
			MaxSteps int `yaml:"maxSteps"`
		} `yaml:"agent"`
	}
	if err := yaml.Unmarshal([]byte(updated), &verify); err != nil {
		return "", false
	}
	if verify.Agent.MaxSteps != maxSteps {
		return "", false
	}
	return updated, true
}

// yamlColumnByteOffset 把 yaml.v3 的 1-based 列号（按字符计）换算成行内字节偏移。
func yamlColumnByteOffset(line string, column int) (int, bool) {
	if column <= 1 {
		return 0, true
	}
	target := column - 1
	seen := 0
	for offset := range line {
		if seen == target {
			return offset, true
		}
		seen++
	}
	if seen == target {
		return len(line), true
	}
	return 0, false
}

// ensureYAMLMappingChild 返回 root 下 key 对应的子 mapping 节点，不存在则新建。
func ensureYAMLMappingChild(root *yaml.Node, key string) (*yaml.Node, error) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != key {
			continue
		}
		child := root.Content[i+1]
		if child.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("runtime config key %q must be a mapping", key)
		}
		return child, nil
	}

	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		child,
	)
	return child, nil
}
