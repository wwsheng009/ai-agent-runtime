package runtimeserver

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"gopkg.in/yaml.v3"
)

// NewRuntimeShellSettingsProvider 读取 shell 段：值来自 RuntimeManager 的内存快照，
// 路径用**写入目标**（可写层），与设置页显示的实际落盘位置一致。
func NewRuntimeShellSettingsProvider(manager *runtimecfg.RuntimeManager) runtimeapi.ShellSettingsProvider {
	return func() (runtimeexecutor.ShellConfig, string, error) {
		if manager == nil {
			return runtimeexecutor.ShellConfig{}, "", fmt.Errorf("runtime config manager is not configured")
		}
		current := manager.Get()
		if current == nil {
			return runtimeexecutor.ShellConfig{}, "", fmt.Errorf("runtime config is not loaded")
		}
		configFile := strings.TrimSpace(manager.GetFilePath())
		if target, _ := config.RuntimeConfigWriteTarget(); strings.TrimSpace(target) != "" {
			configFile = target
		}
		return current.Shell, configFile, nil
	}
}

// NewLayeredRuntimeShellSettingsPersister 保存 shell 段：
//
//  1. 先改内存快照（下一轮请求立即生效），Validate 由 manager.Update 负责；
//  2. 再按**单个键**逐条落盘到可写层，保留其余键、注释与顺序；
//  3. 任一步失败回滚内存，避免内存与文件不一致。
//
// portable 层（backend/configs/runtime.yaml、发行包 configs/）永不写入：
// 目标路径由 RuntimeConfigWriteTarget() 决定（用户级或项目级）。
func NewLayeredRuntimeShellSettingsPersister(manager *runtimecfg.RuntimeManager) runtimeapi.ShellSettingsPersister {
	return func(next runtimeexecutor.ShellConfig) (string, error) {
		if manager == nil {
			return "", fmt.Errorf("runtime config manager is not configured")
		}
		previous := manager.Get()
		if previous == nil {
			return "", fmt.Errorf("runtime config is not loaded")
		}

		updated := *previous
		updated.Shell = next
		if err := manager.Update(&updated); err != nil {
			return "", err
		}

		target, _ := config.RuntimeConfigWriteTarget()
		if strings.TrimSpace(target) == "" {
			_ = manager.Update(previous)
			return target, fmt.Errorf("no writable runtime config layer for shell settings")
		}
		if err := persistShellSettings(target, next); err != nil {
			_ = manager.Update(previous)
			return target, err
		}
		return target, nil
	}
}

// PersistShellSettings writes the `shell:` block to one layer file. It is the
// exported entry point for surfaces that do not hold a RuntimeManager (the
// `aicli config --tui` shell section).
func PersistShellSettings(path string, cfg runtimeexecutor.ShellConfig) error {
	return persistShellSettings(path, cfg)
}

// persistShellSettings writes the `shell:` block to one layer file.
//
// 为什么不用 MergedConfigDocument.ApplyDocumentPathChange：它把层文件解码成
// map[string]interface{} 再整体 Marshal，**会丢掉全部注释并把键按字母序重排**
// （实测见 internal/agentconfig/config_shell_prefer_roundtrip_test.go）。对
// runtime.yaml 这种人工维护、带注释的文件不可接受，因此这里走 yaml.Node 单键 upsert：
// 只改动 shell.* 的值节点，文件其余字节、注释与键顺序全部保留。
func persistShellSettings(path string, cfg runtimeexecutor.ShellConfig) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("runtime config path is required")
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return persistShellSettingsJSON(path, cfg)
	case ".yaml", ".yml", "":
	default:
		return fmt.Errorf("unsupported runtime config format: %s", filepath.Ext(path))
	}

	// 与其它配置写者共用同一把写锁：锁必须覆盖整个读-改-写。
	unlock := config.LockConfigFileWrite(path)
	defer unlock()

	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read runtime config file: %w", err)
	}
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
	shell, err := ensureYAMLMappingChild(root, "shell")
	if err != nil {
		return err
	}

	entries := []struct {
		key   string
		value interface{}
	}{
		{"autoDetect", optionalBool(cfg.AutoDetect)},
		{"prefer", optionalStrings(cfg.Prefer)},
		{"path", optionalString(cfg.Path)},
		{"probeTimeout", optionalDuration(cfg.ProbeTimeout)},
		{"strictProbe", optionalBool(cfg.StrictProbe)},
		{"scriptDelivery", optionalString(string(cfg.ScriptDelivery))},
		{"env", optionalEnv(cfg.Env)},
	}
	for _, entry := range entries {
		if err := setYAMLShellKey(shell, entry.key, entry.value); err != nil {
			return err
		}
	}

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

// setYAMLShellKey upserts one key, carrying the old node's comments over so a
// hand-written `# 候选顺序` note survives a settings save. A nil value removes
// the key (fall back to the built-in default).
func setYAMLShellKey(shell *yaml.Node, key string, value interface{}) error {
	existing := yamlMappingValue(shell, key)
	if value == nil {
		if existing == nil {
			return nil
		}
		removeYAMLMappingKey(shell, key)
		return nil
	}
	node, err := marshalYAMLValueNode(value)
	if err != nil {
		return fmt.Errorf("encode shell.%s: %w", key, err)
	}
	if existing != nil {
		node.HeadComment = existing.HeadComment
		node.LineComment = existing.LineComment
		node.FootComment = existing.FootComment
	}
	upsertYAMLMappingValue(shell, key, node)
	return nil
}

func removeYAMLMappingKey(mapping *yaml.Node, key string) {
	if mapping == nil {
		return
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value != key {
			continue
		}
		mapping.Content = append(mapping.Content[:index], mapping.Content[index+2:]...)
		return
	}
}

// persistShellSettingsJSON keeps JSON layer files working. JSON has no comments,
// so the generic document writer is fine here.
func persistShellSettingsJSON(path string, cfg runtimeexecutor.ShellConfig) error {
	merged, err := config.LoadMergedRuntimeConfigDocument()
	if err != nil || merged == nil {
		return fmt.Errorf("load layered runtime config: %w", err)
	}
	for _, change := range shellDocumentChanges(cfg) {
		if _, err := merged.ApplyDocumentPathChange(change.path, change.value, path); err != nil {
			return err
		}
	}
	return nil
}

// shellDocumentChange 是一条单键写入；value == nil 表示删除该键（回落到更低层/缺省）。
type shellDocumentChange struct {
	path  string
	value interface{}
}

// shellDocumentChanges 把 ShellConfig 展开成逐键改动。
//
// 逐键而不是整段替换：整段写会把用户手写的 `shell:` 注释和键顺序一并吃掉
// （plan §9 实现要点 4）。
func shellDocumentChanges(cfg runtimeexecutor.ShellConfig) []shellDocumentChange {
	changes := []shellDocumentChange{
		{path: "shell.autoDetect", value: optionalBool(cfg.AutoDetect)},
		{path: "shell.prefer", value: optionalStrings(cfg.Prefer)},
		{path: "shell.path", value: optionalString(cfg.Path)},
		{path: "shell.probeTimeout", value: optionalDuration(cfg.ProbeTimeout)},
		{path: "shell.strictProbe", value: optionalBool(cfg.StrictProbe)},
		{path: "shell.scriptDelivery", value: optionalString(string(cfg.ScriptDelivery))},
		{path: "shell.env", value: optionalEnv(cfg.Env)},
	}
	return changes
}

func optionalBool(value *bool) interface{} {
	if value == nil {
		return nil
	}
	return *value
}

func optionalStrings(values []string) interface{} {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

func optionalString(value string) interface{} {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

// optionalDuration writes the human-readable form ("3s"), which is what yaml.v3
// reads back into time.Duration. Zero means "use the built-in default", so it is
// removed from the layer instead of pinned.
func optionalDuration(value time.Duration) interface{} {
	if value <= 0 {
		return nil
	}
	return value.String()
}

func optionalEnv(env map[string]string) interface{} {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(env))
	for key, value := range env {
		out[key] = value
	}
	return out
}
